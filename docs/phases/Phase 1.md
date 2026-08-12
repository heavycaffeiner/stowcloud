# Phase 1: vfs, paths, jail, hardening

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-3-vfs-and-paths.md` and
`docs/proposals/stowcloud-4-jail-and-hardening.md`.

## Scope

The security core. Principle 2 lives here and everything else waits on it.

Depends on Phase 0. Blocks Phase 4, and Phase 9 needs its jail.

## Milestones

- **1a**: `path.go`, `norm.go`, the reserved set, the path fuzz target, D10's
  three unconvertible types.
- **1b**: `root.go`, `open.go`, `errno.go`: resolution under the policy flags.
- **1c**: `read.go`, `file.go`: streaming reads, pread and pwrite, statx,
  `copy_file_range`.
- **1d**: `durable.go` and the D11 gate.
- **1e**: `internal/watch`: inotify, the hot set with both halves, the periodic
  rescan, the `fanotify` deletion.
- **1f**: `seccomp.go`: the assembler, both policies, the arch prologue, the
  jump-offset tests.
- **1g**: `landlock.go` and `reexec.go`: the ABI probe, ruleset construction,
  restrict-then-exec.
- **1h**: `policy.go`: required, preferred and off, the startup refusal, the
  health degradation.
- **1k**: `caps.go` and the escape proof, required on the Linux job.

**Proposal 4's last two rows carry Phase 9 numbers on purpose.** The jail is not
provably finished at the end of this phase. Phase 1 delivers a sandbox that
installs; Phase 9 delivers the evidence that it holds.

## Traps

- **Every descriptor is an `*os.File`, never a bare `int`**, and every `f.Fd()`
  site is followed by `runtime.KeepAlive(f)`. Proposal 3 §4.3.2 gives all three
  reasons.
- **`.` and `..` are rejected, never normalised.** Normalising is what creates
  the bypass. This is a non-goal, not an omission.
- **`ReadDirFunc` is the primitive; `ReadDir` is the capped wrapper.** Building
  it the other way round is F4 reintroduced.
- **`IntentReadWrite` has exactly one call site** and a gate greps for a second.
  The create path does not need it: the durable helper opens `O_RDWR` already.
- **The durable helper checks the `fchmod` and `fchown` results.** Discarding
  them is F7, and it is the failure that makes a neighbour lose access.
- **Landlock restricts the calling thread and has no TSYNC.** Lock the OS
  thread, restrict, re-exec, then install seccomp with TSYNC. Getting this
  wrong returns success and sandboxes nothing, which no test will notice.
- **The worker's Landlock ruleset leaves `LANDLOCK_ACCESS_FS_EXECUTE`
  unhandled**, or its own re-exec is denied. seccomp denies `execve` harder
  anyway by not carrying it.
- **The seccomp assembler checks `seccomp_data.arch` before any syscall number**
  and rejects the x32 range on `amd64`. That is F1. Numbers come from
  `unix.SYS_*`, never hardcoded, and the jump offsets are unit-tested.
- **`hardening = "required"` refuses to start**, exit 78, reason on stderr.
  Logging and continuing is F2.
- **Delete the `fanotify` config value.** That is F12. An unknown value is a
  refusal, not a warning and a fallback.
- **The hot set has two halves.** The recent half is an LRU; the sticky half is
  refcounted and fed from outside by WebSocket subscriptions. Export
  `Subscribe` and `Unsubscribe` now even though Phase 5 is what calls them.
  Building only the LRU half fails silently on exactly the directory a user is
  looking at.
- **Three of the eight escape-proof rows are new work**, not a port: the
  magic-link case, the symlink-swapped-between-calls case, and the `NO_XDEV`
  case. Each asserts the errno, not merely that the operation failed.

## Done when

- The gate is green, including `go test -race ./...`.
- The escape proof and the seccomp jump-offset tests pass in the Rocky guest.
- A `required`-mode startup with the kernel probe faulted refuses to start and
  names the step and the errno.
- F1, F2, F3, F4, F5, F6, F7 and F12 are closed, each with a test that fails
  without the fix.
