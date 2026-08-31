# SMB 03: the agent's runtime

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/smbagent` (`scope.go`, `devices_linux.go`, `conf.go`,
> `smbd_linux.go`, and the loop halves of `sync_linux.go`) is referenced
> as a behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## The division of knowledge

The server renders the closed case: loopback and nothing else. It has
to, because it sits in a different network namespace and cannot see
the host's devices. The agent runs in the namespace that can, and its
job is to widen the closed rendering to exactly the networks this
machine is attached to, and no further. Every rule below serves that
one sentence.

## Scope detection

`Devices()` is the only OS ask; `Compute(devices, allowPublic)` is a
pure fold, so the decision that determines who can reach SMB is
testable on any machine. The rules, all normative:

- A network this machine is attached to is admitted with no
  configuration: the bind line gets the device, the admission line
  gets the **well-known private block enclosing its address**, not
  the address itself.
- A veth device means this process is inside a container's network
  namespace, where the address describes the bridge, not any network
  a client arrives from: private space is admitted wholesale.
- A globally routable address is left out unless the operator opted
  in (`AllowPublicBind`).
- Devices are sorted, so the same machine renders the same file and
  an unchanged republish is byte identical.
- `Detected == false` (nothing to bind) still promotes the closed
  configuration, which is safe, and reports it, because it is never
  what the operator wanted. Silence is the failure mode here.
- A mapped address is judged as the family it actually is (unmap
  before classifying), the same rule as `kit/netzone`.

`Policy` (the flags file the server writes): a missing or unreadable
file reads as **both flags off**. Neither flag is assumed permissive
because a file did not arrive. `PinnedInterfaces` means the operator
named the addresses; detection must not widen a pinned rendering, so
the scope is nil and the rendered lines are final.

## The candidate transformation

`Candidate(src, scope)` makes what gets validated and promoted:

- The two scope lines are **substituted, never inserted**: the server
  always renders both, and a file missing them is one the agent
  should not be widening.
- The log directives land after the first line, which the renderer
  guarantees is the global section header. `auth_audit:3` is the one
  level at which the daemon logs a failed authentication at all; one
  file, because the ban filter beside it names one file.

The read-back questions (`Sections`, `NetbiosWanted`,
`BoundInterfaces`) read the **promoted file**, not tracked state: what
gets reported is what the daemon is actually serving, and a settings
change takes effect on the next apply rather than the next restart.

## Daemon control

The one distinction everything hangs on: the daemon binds its sockets
once, at startup. Reload rereads shares and users in place and never
revisits the sockets, so **a changed bind line needs the process
replaced, not reloaded**. The pre-agent sidecar reloaded either way
and stayed bound to loopback for as long as it ran; that history is
why `settle` exists.

`settle` tells the daemon as little as will do: not running means
start, a changed bind line means restart, an identical promoted
candidate means nothing, anything else means reload.

Two ownership modes, detected at startup (`DetectMode`):

- **Supervise**: the agent spawns the daemon as its own child. The
  agent is its container's init, so it must reap (a crashed daemon
  lingers as a zombie otherwise) and must kill the whole process
  group (the daemon forks a child per connection, and those hold the
  listening port across a restart).
- **Service**: a service manager owns the unit; the agent asks it.
  Unit-name probing covers the distributions' disagreement.

`nmbd` runs only while the promoted configuration wants a name, and
its failure is a log line, never fatal: name service is broadcast, it
does not cross a bridge, and the address still mounts. `fail2ban`
starts only when the binary exists and the process holds the
firewall capability; without it, one line says what is off and why
the admission list plus authentication is what limits an attacker.

## The apply pass

One pass, driven by both the poll loop and the control socket
(`01-publish-and-agent-protocol.md`), with a hard line down its
middle: **everything before promotion can refuse; nothing after it
can.** Refusals (unreadable interfaces, a failed `testparm`, account
collisions, missing groups) keep the previous configuration serving.
After promotion, problems are warnings on the report: a passwd
rebuild failure, a failed import or prune, share paths that do not
exist in this container, a daemon that did not settle.

- **Absence is the off switch**: a missing rendered `smb.conf` means
  the server disabled SMB, and the agent tears down: stop the daemon,
  prune the managed accounts and their credentials. Leaving them
  behind would keep a revoked credential working.
- `Fingerprint` is the poll loop's "has anything changed": the
  rendered files' sizes and mtimes **plus the detected scope**, which
  moves when a tunnel or a tagged network comes up without anything
  on disk changing.
- `LogReport` writes one line per apply with its source; a
  socket-driven apply is the interesting one, and it used to log
  nothing on success.

## Deliberate changes

1. None to the behavior. The promotion durability fix lives in
   `02-agent-durable-writes.md`; everything in this document carries
   whole. The scope rules, the closed-by-default policy reading, the
   settle table and the teardown are behavior-preserving
   requirements.
2. **`settle` splits into a pure decision and the call that acts on it**
   (`Settle` and `Tell`). The four-row table was previously reachable
   only through a live daemon, so the one row that matters most, a moved
   bind line outranking an unchanged configuration, had no test. Both
   halves keep their behavior; only the seam is new.
3. **`Compute` sorts its devices itself** rather than relying on
   `Devices` having sorted them. The determinism requirement belongs to
   the fold that renders the line, not to the one caller that happens to
   supply it in order, and a test passing devices in a different order
   now proves it.

## What this phase built

The scope fold, the policy reading, the candidate transformation, the
read-backs, the settle decision and the fingerprint, each with the tests
this document lists. The daemon control itself (`DetectMode`, the
supervise and service modes, `nmbd`, `fail2ban`) and the apply pass that
drives them are wiring over a live `smbd` and a service manager, and land
with phase 3's assembly, where there is a process to supervise. `Daemon`
is the interface they satisfy, and `Tell` is the seam they attach to.

## Tests

- `Compute` fixtures: enclosing-block admission (the block, not the
  address); veth admits private space wholesale; a public address is
  excluded until opted in; sorted determinism (same devices in any
  order, same scope).
- A missing or unreadable policy file reads closed; a pinned policy
  leaves the rendered lines untouched.
- `Candidate`: the scope lines are substituted in place, never
  appended; the log directives land inside the global section; a nil
  scope changes nothing but the log directives.
- Read-back: `Sections` skips global and pairs names with paths;
  `NetbiosWanted` requires the directive present and not disabled; a
  commented directive is not a directive.
- `settle`: the four-row action table, including bind-change means
  restart while any other change means reload.
- Teardown: the managed accounts and credentials are gone after the
  rendered file disappears; the daemon is stopped.
- `Fingerprint` changes when the detected scope changes with no file
  on disk changing.
- Nothing after promotion returns a failed report (fault injection on
  the post-promotion steps yields warnings, and the report says which).
