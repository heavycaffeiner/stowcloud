# Preview 01: the jail

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/jail` is referenced as a behavioral specification only.
> The new implementation is written completely from scratch; nothing is
> copied.

## What the jail is

A Landlock domain, two seccomp filters, POSIX rlimits, and the
restrict-then-exec sequence that makes the domain cover every thread the
Go runtime starts. Target `engine/infra/jail`; its only engine import is
`kit/num`. There is no portable stand-in and no build-tag fallback: a
second implementation of a security boundary is a second implementation
that is never the one that ships.

## Policy

```go
const (
    Required  Policy = iota // refuse to start when a step cannot apply; the shipped default
    Preferred               // report the degradation, keep running; the bare-metal default
    Off                     // attempt nothing, say so
)
func ParsePolicy(s string) (Policy, error) // the trust boundary; three spellings only
func PolicyNames() []string                // sent to the client, never compiled into it
```

A policy rather than an outcome: without a required mode, a sandbox is a
thing that usually happens. `Status`/`StepStatus`/`Refuse` report per
step what applied and what did not, so "preferred" degradations are
visible, not silent.

## The sequence

`Apply(policy, spec)`:

1. Already re-exec'd (the marker env var): the Landlock domain arrived
   by inheritance; record the step as applied.
2. Otherwise: check Landlock availability, lock the OS thread, restrict
   and re-exec. The thread lock is load-bearing: the goroutine must not
   migrate between the restrict and the exec, or the domain lands on a
   thread that is not the one that execs. A successful exec never
   returns.
3. Seccomp installs after re-exec (both filter kinds), covering every
   thread by fork inheritance.

Kernel-facing calls sit behind an unexported `steps` struct so the
sequencing is testable without a kernel that refuses (the audit calls
this seam out as worth keeping).

The Landlock implementation details that were verified and stay:
`handled == 0` checked before proceeding, `PR_SET_NO_NEW_PRIVS` before
`restrictSelf`, grant paths opened `O_RDONLY|O_PATH|O_CLOEXEC` with
matching closes, `runtime.KeepAlive` after every syscall referencing a
Go pointer.

## Seccomp

The BPF program is assembled at startup from an allow list.
**The methodology is the requirement, not the list**: every syscall on
the list carries a comment saying why it is there, derived from
measurement (strace over the worker's real workload), not guesswork.
The assembler's defensive checks stay: the refusal jump index is bounds
checked against BPF's 8-bit offset, and backward jumps refuse.
`ABIVersion` and `AllowedSyscalls` stay exported for the doctor
command.

## The two dead exports become real (the decision)

The audit found `ApplyLimits`/`DefaultLimits`/`Limits` and
`SealDescriptors` exported, documented as load-bearing, and called by
nothing. Comments in three files claim RLIMIT_AS backstops the decode
ceiling; nothing sets it. The rebuild **wires both**, in the worker's
startup order (02):

1. `SealDescriptors` closes everything above the control socket's fd,
   because RLIMIT_NOFILE does not touch descriptors the worker
   inherited at fork: listening sockets, share roots, database handles.
   `os/exec`'s CLOEXEC defaults cover most, and "most" is not a
   security answer; the seal makes it all of them, verifiably.
2. `ApplyLimits` sets RLIMIT_AS (and the rest of `DefaultLimits`) soft
   and hard, so the in-process pixel ceiling has the kernel backstop
   the comments always claimed. A decoder exploit is exactly the case
   where in-process bounds stop counting.

With both wired, the comments become true. The worker's jailproof test
gains two assertions: the address-space limit is reported by the child
and no descriptor beyond the expected set survives.

**Amended during implementation: `RLIMIT_AS` is 2 GiB, not 512 MiB.**
Wiring `ApplyLimits` is what exposed this, which is the point of wiring
it. RLIMIT_AS bounds the whole address space rather than resident
memory, and the Go runtime reserves far more than it touches: measured
on this tree, a Go process encoding a PNG dies under 1 GiB and succeeds
at 1.5 GiB, and the worst case the decode ceiling admits, 64 Mpx as
RGBA, also fits at 1.5 GiB. At 512 MiB every worker died at startup. Two
gigabytes is the measured floor with headroom, and the limit is still a
real backstop: the graceful pixel ceiling refuses a bomb long before it,
so what remains for the kernel is the case that ceiling cannot see, a
decoder exploit allocating on its own account.

The two reporting probes read their values through the socket rather
than calling `getrlimit` inside the finished jail: that syscall is not
on the measured allow list, and widening a filter so a proof can run is
the wrong direction for the thing being proved. The limits are captured
at startup, after `ApplyLimits` and before the filter.

## Deliberate changes

1. **The two exports are wired** (above); dead-and-documented was the
   defect.
2. Nothing else: the policy vocabulary, the sequence, the thread-lock
   rule, the assembler checks and the per-syscall-comment methodology
   are behavior-preserving.

## Tests

- The sequencing over the `steps` seam: required refuses on a failed
  step, preferred records and continues, off attempts nothing.
- Re-exec: the marker round-trips; the re-exec'd image reports the
  domain applied.
- Seccomp: the assembler refuses an over-limit jump and a backward
  jump; a mismatched-arch program is killed under both filter kinds
  (the old test, rebuilt).
- The jailed-worker proofs (in the worker's suite, 02): a jailed child
  cannot open a path, cannot connect a socket, reports the rlimits, and
  holds no unexpected descriptor.
- `ParsePolicy` refuses everything outside the three spellings.
