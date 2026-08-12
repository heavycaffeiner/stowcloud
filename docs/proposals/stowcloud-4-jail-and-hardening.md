# Jail and hardening - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The process sandbox and the decoder jail in Go. Landlock cannot be applied from
a goroutine, so the sequence becomes restrict, re-exec, then filter. The forked
worker pool becomes an exec'd subcommand of the same binary. Findings F1, F2 and
F3 are closed here, and the security claim stays executable rather than
asserted.

## 2. Background & Motivation

### 2.1 Why a process, and why that survives the language change

[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S8: a thread
shares the address space, so memory corruption in a decoder is full process
compromise. That is the argument that put the decoder in a separate process
rather than a worker thread, and it is worth restating because the obvious
objection changes shape in Go and the answer does not.

The Rust tree's decoders are pure Rust and it jailed them anyway, on the
grounds that memory safety removes one class and leaves logic bugs, crashes and
infinite loops. Go's decoders are memory-safe on the same terms and leave the
same residue: `image/gif` decoding every frame of an animation is not a memory
error, and neither is a decode loop that does not terminate. A pure-Go decoder
is not a reason to drop the boundary. It was never the reason the boundary
existed.

Two other stances decide things here. **S3**, a downgrade is loud, is what F2
violates and what D3 turns into a startup refusal. **S17**, a claim that cannot
be executed is a comment, is why §4.3.6 ports the probe mechanism rather than
describing the jail and moving on.

### 2.2 The constraint Go adds

This is the one place where the Go runtime forces a different shape, and the
naive version is silently broken in a way no test would catch.

`landlock_restrict_self(2)` restricts **the calling thread**. A thread created
afterwards inherits the domain of the thread that created it. The Go runtime has
already started several threads before `main` runs and starts more on demand, so
calling it from a goroutine restricts whichever thread that goroutine happened
to be on, leaves every other thread unrestricted, and returns success. The
process then reports itself as sandboxed and is not.

`seccomp(2)` has an answer for exactly this problem,
`SECCOMP_FILTER_FLAG_TSYNC`, which applies a filter to every thread in the
process or fails atomically. Landlock has no equivalent flag, and this is not an
oversight in the Go ecosystem that a library papers over: the mainstream Go
Landlock binding reaches for `libpsx` through cgo precisely because there is no
in-kernel all-threads mechanism, and cgo is what
[`stowcloud-2-gate-and-toolchain.md`](stowcloud-2-gate-and-toolchain.md) §4.3.1
gives up the static binary to allow.

The property that makes a cgo-free answer possible is that **a Landlock domain
survives `execve`**, and after `execve` the process has exactly one thread
carrying it, from which every later thread inherits.

The second constraint is the preview jail. Its children are `fork(2)`s of the
running server with no `execve`, which is not reproducible in Go at all: between
fork and the child's first allocation only async-signal-safe work is legal, the
child decodes images and therefore allocates, and Go's runtime holds locks
across many more threads than a Rust process would. The current code documents
this as a known limitation and names the fix:

> A hardened variant would re-`exec` a dedicated `sc-preview-worker` binary and
> pass the socket as fd 3; that is a strictly better shape for production and is
> deliberately left as follow-up rather than pretended away.

The port takes that shape, with one change: not a dedicated binary, a subcommand
of the same one (folder README, contradiction C2).

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] A Landlock domain that provably covers every thread of the process.
- [ ] A seccomp filter that checks its ABI before it trusts a syscall number,
      rejects x32, and has a verified mapping for `amd64` and `arm64` (F1, F3).
- [ ] `hardening = "required"` as the shipped default, refusing to start when a
      step cannot be applied (F2).
- [ ] The decoder jail as an exec'd subcommand, with the job socket on fd 3 and
      no path ever passed to it.
- [ ] The probe mechanism ported, so the jail is demonstrated rather than
      described.
- [ ] Worker death remains an ordinary event that costs one thumbnail.

### 3.2 Non-Goals

- [ ] cgo, `libpsx`, or a Landlock binding that needs either.
- [ ] A user namespace, a chroot, or a second container. The layers are the six
      named in [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3 F2,
      and adding a seventh is a separate proposal.
- [ ] Argument inspection in either filter. Both policies are flat syscall
      number lists, which is what makes them readable in one screen.
- [ ] Landlock ABI negotiation beyond what the running kernel reports. Handle
      what it knows, grant nothing extra.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/jail
  landlock.go     ruleset construction, restrict_self, the ABI probe
  seccomp.go      the BPF assembler, the arch prologue, both policies
  rlimit.go       setrlimit for the worker
  reexec.go       the restrict-then-exec sequence and its env marker
  proof.go        the probe types and the executable demonstration
  policy.go       Policy: required | preferred | off, and what each does
```

```mermaid
sequenceDiagram
  participant P as stowcloud (initial)
  participant P2 as stowcloud (re-exec'd)
  participant W as stowcloud preview-worker
  participant W2 as worker (re-exec'd)
  P->>P: LockOSThread, build ruleset, landlock_restrict_self
  P->>P2: execve(self, SC_REEXEC=1)
  Note over P2: single-threaded at runtime start;<br/>every later thread inherits the domain
  P2->>P2: PR_SET_NO_NEW_PRIVS, seccomp(TSYNC) deny-list
  P2->>W: execve(self, "preview-worker"), job socket on fd 3
  W->>W: LockOSThread, ruleset handling all rights except EXECUTE, granting none
  W->>W: landlock_restrict_self
  W->>W2: execve(self, "preview-worker", SC_JAILED=1)
  W2->>W2: close_range(4,~0), setrlimit x5
  W2->>W2: PR_SET_NO_NEW_PRIVS, seccomp(TSYNC) 22-call allow-list
  W2->>P2: ready, then reads jobs from fd 3
```

### 4.2 Data Model Changes

None.

### 4.3 Core Logic

#### 4.3.1 Server startup

In order, and the order is the design:

1. `runtime.LockOSThread()`. Without it the goroutine can migrate between the
   `landlock_restrict_self` call and the `execve`, and the domain would be left
   on a thread that is not the one that execs.
2. Build the ruleset: handle every filesystem access right the running kernel's
   ABI reports, then grant `PathBeneath` on each share root, the data directory,
   and the SMB configuration directory when SMB is enabled. Grant read and
   execute on the binary's own path, because step 4 and the worker exec in step
   6 both need it.
3. `landlock_restrict_self`.
4. `unix.Exec(selfPath, argv, env)` with `SC_REEXEC=1` added, so this happens
   exactly once and a missing marker after the exec is a bug rather than a loop.
   `unix.Exec` replaces the process image, so nothing after it runs.
5. In the re-exec'd process: `PR_SET_NO_NEW_PRIVS`, then the process seccomp
   filter with `SECCOMP_SET_MODE_FILTER` and `SECCOMP_FILTER_FLAG_TSYNC`.
6. Build the worker pool by exec'ing `stowcloud preview-worker`.

**Ordering note.** Hardening is applied after shares and the data directory are
opened, exactly as today, because the paths being granted have to exist as
descriptors first. The re-exec complicates that: the descriptors do not survive
into the new image unless deliberately passed. They do not need to. The ruleset
is built from paths, the domain survives the exec, and the re-exec'd process
opens its shares normally under a domain that already permits them.

**`selfPath`.** `/proc/self/exe` is the reliable answer and it requires `/proc`
mounted, which the runtime image has. `os.Executable()` reads the same link.
When `/proc` is absent, this is a refusal to start under `required` and a
warning under `preferred`, with no attempt to guess from `argv[0]`.

#### 4.3.2 Worker startup

1. `runtime.LockOSThread()`.
2. Build a ruleset that handles every filesystem right the ABI reports **except
   `LANDLOCK_ACCESS_FS_EXECUTE`**, and grants nothing at all.

   The exception is not a weakening and it is worth being explicit about why. A
   ruleset that handled `EXECUTE` and granted nothing would deny step 4's own
   `execve`, which is how the domain becomes process-wide in the first place.
   Denying `execve` is delegated to seccomp in step 6, which denies it harder:
   Landlock would refuse to execute a *file*, while seccomp removes the syscall,
   so there is nothing to execute and nothing to point it at.
3. `landlock_restrict_self`. The domain now grants no filesystem access of any
   other kind: no open, no stat, no readdir, anywhere, by any path.
4. `unix.Exec(selfPath, ["stowcloud", "preview-worker"], env+SC_JAILED=1)`,
   carrying the job socket, which is deliberately not `CLOEXEC`.
5. `close_range(4, ^uint(0))` to drop everything else inherited, then
   `setrlimit` for `RLIMIT_AS` (512 MiB), `RLIMIT_CPU` (10 s), `RLIMIT_NOFILE`
   (16), `RLIMIT_NPROC` (0) and `RLIMIT_CORE` (0). Same defaults as today.
6. `PR_SET_NO_NEW_PRIVS`, then the 22-call allow-list with TSYNC and
   `SECCOMP_RET_KILL_PROCESS`.
7. Only now, read from fd 3.

**The socket.** `unix.Socketpair(AF_UNIX, SOCK_SEQPACKET, 0)` in the parent, the
child end passed through `exec.Cmd.ExtraFiles` so it lands on fd 3. `SEQPACKET`
preserves message boundaries, so the 8 KiB cap in
[`stowcloud-1-defensive-standard.md`](stowcloud-1-defensive-standard.md) D5 is
an upper bound rather than a framing parameter. The parent's end stays a
`*net.UnixConn` and its control messages go through `SyscallConn`, not `Fd()`,
because `Fd()` would put the socket into blocking mode and take it out of the
runtime's poller.

**What the worker is given.** A job message plus two descriptors over
`SCM_RIGHTS`: input and output. Never a path. That is not a rule the worker
enforces, it is a fact about what it holds: `openat` is not on the allow-list,
and the empty Landlock domain would refuse it if it were. A path-traversal bug
in a decoder has nothing to traverse.

**Go runtime cost, stated.** An exec'd Go process is not a copy-on-write fork.
Each worker carries its own runtime, its own heap arenas and its own threads, so
a pool of N workers costs materially more resident memory than N forks do today.
The default pool size (half the available parallelism, minimum one) is kept, and
the real number is measured in Phase 13 against the resource budget in
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3 F4 rather than
asserted here. If it does not fit, the lever is the pool size, not the
isolation.

#### 4.3.3 The seccomp assembler

D4, and F1 and F3 are exactly the absence of its first four instructions.

```
  ld  [4]                    ; seccomp_data.arch
  jeq AUDIT_ARCH_<this>, +1, KILL
  ld  [0]                    ; seccomp_data.nr
  jge X32_SYSCALL_BIT, KILL  ; x86_64 only
  ... policy ...
```

- `AUDIT_ARCH` is compiled in per `GOARCH`: `0xC000003E` for `amd64`,
  `0xC00000B7` for `arm64`. An architecture with no entry in the table has no
  filter, and no filter is a refusal to start under `required` rather than a
  warning and a continue.
- The x32 rejection is `amd64` only, because that ABI reports the same
  `AUDIT_ARCH_X86_64` with the bit set on the number.
- Syscall numbers come from `unix.SYS_*`, never hardcoded. The current code's
  own preview filter names this as an improvement it made over the process
  filter, and here both get it.
- Jump offsets are computed by the assembler and unit-tested, because an
  off-by-one in a jump is a filter that allows what it meant to kill and there
  is no way to notice at runtime.

Two policies:

| Policy | Action | Contents |
|---|---|---|
| process | `SECCOMP_RET_ERRNO(EPERM)` | deny-list: `ptrace`, `process_vm_readv`, `process_vm_writev`, `mount`, `kexec_load`, `kexec_file_load`, `bpf`, `userfaultfd` |
| worker | `SECCOMP_RET_KILL_PROCESS` | allow-list of 22, everything else killed |

The worker allow-list's absences are the proof obligations: no `openat`, no
`openat2`, no `execve`, no `socket`, no `connect`, no `clone`, no `ptrace`.
`recvmsg` and `sendmsg` are on it because they are how a job arrives at all.

**Go-specific additions to the allow-list.** The Rust worker's 22 calls are the
starting point, not the answer. A Go runtime needs calls a Rust one does not:
`futex`, `nanosleep`, `sched_yield`, `mmap`, `munmap`, `madvise`, `sigaltstack`,
`rt_sigaction`, `rt_sigprocmask`, `rt_sigreturn`, `gettid`, `tgkill`,
`clock_gettime`, `exit_group`. The final list is produced by running the worker
under `SECCOMP_RET_LOG` with a corpus of real images and reading the audit log,
not by guessing, and the resulting list is committed with the corpus that
produced it. This is expected to be larger than 22 and that is a real reduction
in the jail's tightness, recorded here rather than discovered later.

#### 4.3.4 The policy

```go
type Policy uint8
const (
    Required  Policy = iota // any step that cannot be applied is a refusal to start
    Preferred                // a step that cannot be applied is a warning and a
                             // named degradation in GET /api/health
    Off                      // nothing is attempted, and health says so
)
```

`Required` is the default in the shipped image and `Preferred` for a bare-metal
install, where an older kernel is a legitimate state and the operator may not
control it. `Off` exists so "I know, and I accept it" is expressible without a
code change.

Under `Required`, a refusal prints the step, the errno and the kernel version to
stderr and exits 78. F2 is this existing at all: today the result is logged and
dropped, so a container that silently refuses both layers starts identically to
one that enforces both.

#### 4.3.5 Worker death

Unchanged in behaviour and worth restating because it is what makes the jail
affordable. A seccomp kill (`SIGSYS`), an `RLIMIT_AS` OOM, a segfault and the
CPU limit firing all look the same from the parent: the response read returns
empty or `ECONNRESET`. The parent reaps the child, fails **that one job** with
`ErrWorkerDied`, and execs a replacement on the next job that lands on the slot.
A crafted input that kills a decoder costs one thumbnail.

The worker does not install D7's recover. A panic there is a decoder failing on
input it could not handle, and dying is the designed outcome.

#### 4.3.6 The proof

`Probe` is ported whole, because a security claim that cannot be executed is a
comment. The worker accepts a probe message and attempts, from inside the
finished jail:

| Probe | Attempt | Good outcome |
|---|---|---|
| `OpenEtcPasswd` | `open("/etc/passwd", O_RDONLY)` | killed, or `EACCES` |
| `CreateSocket` | `socket(AF_INET, SOCK_STREAM, 0)` | killed |
| `Fork` | `fork()` | killed, or `EAGAIN` from `RLIMIT_NPROC` |
| `Spin` | burn CPU for N ms | the parent can kill it mid-job |
| `Ping` | nothing | the transport works |

Two of these deserve a note in Go terms. `OpenEtcPasswd` cannot be
`os.Open`, because the Go runtime may service that through a different path;
the probe issues the raw syscall. `Fork` has no Go form at all, so it is
`unix.Syscall(SYS_CLONE, ...)` directly, which is the thing the filter is
supposed to kill anyway.

The probes grant nothing. They run after every restriction is in place, and
their only possible outcomes are "the kernel said no" and "the kernel killed
me". A `Succeeded` result is a test failure.

This runs as a Go test that skips on non-Linux and is **required** on the Linux
job, which is the same status `examples/jail_proof.rs` has today.

## 5. API Design

### 5-1. New / Modified

```go
package jail

// Spec describes a Landlock domain: which access rights to handle, and which
// paths to grant beneath. A right handled with no grant is denied everywhere.
type Spec struct {
    HandleAll   bool     // handle every right the running ABI reports
    ExceptExec  bool     // leave LANDLOCK_ACCESS_FS_EXECUTE unhandled; see §4.3.2
    GrantBeneath []Grant
}

// RestrictAndReexec applies spec to the calling thread and then replaces the
// process image so that every thread of the new process inherits the domain.
// It does not return on success. marker is an environment key set on the new
// image so the sequence runs exactly once.
//
// The caller must hold the OS thread. Landlock restricts the calling thread
// only, and a goroutine that migrates between the restrict and the exec leaves
// the domain on a thread that is not the one that execs.
func RestrictAndReexec(spec Spec, marker string) error

// InstallSeccomp assembles and installs the policy for kind with
// SECCOMP_FILTER_FLAG_TSYNC, so it covers every thread the Go runtime has
// already started. It verifies seccomp_data.arch before comparing any syscall
// number, rejects the x32 range on amd64, and returns ErrArchUnsupported for
// an architecture with no verified mapping.
func InstallSeccomp(kind FilterKind) error

// Apply runs the whole sequence for the server process under policy. Under
// Required it returns only on success, because a failure has already exited.
// Under Preferred it returns a Status naming what was and was not applied,
// which GET /api/health reports.
func Apply(policy Policy, spec Spec) (Status, error)
```

```go
package preview

// Pool holds N exec'd worker processes. Generate hands a job and two
// descriptors to a free worker over SCM_RIGHTS; the worker is never told a
// path. A worker that dies mid-job fails this call with ErrWorkerDied and is
// replaced on the next job for that slot.
func (p *Pool) Generate(ctx context.Context, src, dst *os.File, spec Spec) (Result, error)
```

### 5-2. Error Handling

| Error | Meaning | Effect |
|---|---|---|
| `ErrHardeningRefused` | a step could not be applied under `Required` | exit 78, reason on stderr |
| `ErrArchUnsupported` | no verified syscall mapping for this `GOARCH` | as above, or the worker refuses to start |
| `ErrNoProc` | `/proc` is not mounted, so the binary's own path is unknown | as above |
| `ErrWorkerDied` | the worker was killed or the socket reset | 503 for that preview, pool stays up |
| `ErrWorkerBusy` | no free worker within the caller's deadline | 503 |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 1f | `seccomp.go`: the assembler, both policies, the arch prologue, the jump-offset tests | M | Phase 0c (A1) | heavycaffeiner |
| Phase 1g | `landlock.go` and `reexec.go`: the ABI probe, ruleset construction, the restrict-then-exec sequence | M | Phase 0c (A2) | heavycaffeiner |
| Phase 1h | `policy.go`: Required/Preferred/Off, the startup refusal, the health degradation | S | 1f, 1g | heavycaffeiner |
| Phase 1i | The `SECCOMP_RET_LOG` run against an image corpus that produces the worker allow-list | S | 1f, Phase 9's decoder | heavycaffeiner |
| Phase 1j | `proof.go` and the required Linux test | S | 1f, 1g, Phase 9's pool | heavycaffeiner |

1i and 1j need a decoder to exercise, so they land with Phase 9 even though they
belong to this document. That is stated rather than hidden: the jail is not
provably finished until something runs inside it.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `golang.org/x/sys/unix` | Landlock, seccomp, prctl, setrlimit, close_range, socketpair, SCM_RIGHTS, exec |

Nothing else, and that is deliberate: the jail around the most dangerous code
path in the product adds no module the rest of the tree does not already need.
This mirrors the third reason the current seccomp filter is hand-built.

**Kernel.** Landlock needs 5.13 or newer and degrades to unavailable below it,
which under `Preferred` is a named degradation and under `Required` a refusal.
`close_range` needs 5.9. `seccomp` with TSYNC needs 3.17. The floor for the
product stays 5.6 for `openat2`, so a 5.6 to 5.12 kernel runs with seccomp and
without Landlock, and that combination is a supported `Preferred` state.

## 7. References

- `crates/sc-preview/src/worker/jailed/mod.rs`: the current pool, and the
  "known limitation" section quoted in §2 that names this document's shape.
- `crates/sc-preview/src/worker/jailed/seccomp.rs`: the filter that already
  checks its arch, and the three reasons it is hand-built.
- `crates/sc-server/src/hardening.rs`: F1, F2 and F3 in one file.
- `crates/sc-preview/examples/jail_proof.rs`: the executable claim §4.3.6 ports.
- `crates/sc-server/src/lib.rs:358`: the call site F2 is about.
- `landlock(7)`, `seccomp(2)`, `prctl(2)`, `close_range(2)`, `unix(7)` for
  `SCM_RIGHTS`, `execve(2)` on what survives it.
