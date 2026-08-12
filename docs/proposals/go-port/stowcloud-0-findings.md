# Findings from the Rust tree - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Fourteen defects and limits found by reading the current Rust tree, each with a
location, the failure it produces, and the rule in
[`stowcloud-1-defensive-standard.md`](stowcloud-1-defensive-standard.md) that
closes it. This is the ledger the port is measured against: a phase is not done
while a finding it owns is open.

## 2. Background & Motivation

The decision recorded in `../stowcloud-22-go-backend.md` is to fold fixes into
the port rather than fix them first in Rust. That decision is only safe if the
fixes are written down before the port starts, because otherwise "fixed during
the port" and "lost during the port" look identical from the outside.

These were found by reading, not by running. Three of them (F1, F2, F3) are in
the process sandbox and none of them would show up in a test, because the tests
that exist assert what the sandbox does when it works, not what the process does
when the sandbox is absent.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Each finding stated precisely enough to be argued with: a location, what
      goes wrong, and under what conditions.
- [ ] Each finding assigned to exactly one phase and one defensive rule.
- [ ] Each finding given the test that fails without the fix, so "closed" is a
      fact rather than a claim.

### 3.2 Non-Goals

- [ ] Fixing any of these in Rust. That was considered and rejected in the
      parent proposal.
- [ ] Severity ranking beyond the grouping below. Every one of them is in scope
      for the phase that owns it, so a ranking would only invite deferral.
- [ ] Findings in `web/`. The frontend is out of scope except for its API
      client.

## 4. Technical Design

### 4.1 Architecture Overview

The findings group into four kinds, and the grouping is what decides where each
is closed:

```
sandbox integrity      F1  F2  F3        closed in Phase 1 (jail and hardening)
unbounded or silent    F4  F7  F9  F10   closed by a type or a lint, everywhere
ambient or implicit    F5  F6            closed by a signature change
structural or gate     F8  F11 F12 F13 F14  closed by a gate or a deletion
```

### 4.2 Data Model Changes

None. This document changes no schema; the findings it records change several,
and each of those is specified where it belongs.

### 4.3 Core Logic

#### F1. The process seccomp filter does not check the ABI

`crates/sc-server/src/hardening.rs:155`

The filter's first instruction loads offset 0 of `struct seccomp_data`, the
syscall number, and compares it against a hardcoded `x86_64` list. Offset 4,
`arch`, is never read.

A syscall number is only meaningful together with the ABI that issued it. A task
running under a different ABI on the same kernel presents different numbers for
the same calls, so a filter that matches numbers without pinning the arch is
matching numbers that mean something else. On `x86_64` this includes the x32
ABI, which reports the same `AUDIT_ARCH_X86_64` with the x32 bit set on the
number.

The preview jail's filter, in the same repository, does check the arch, does
reject the x32 range, and does take its numbers from `libc::SYS_*`. Its module
documentation names all three as improvements over this one, so the gap is known
and recorded rather than newly discovered here.

**Closed by** D4. **Test**: assemble the filter and assert the first four
instructions are the arch load, the arch compare, the number load and the x32
rejection, in that order.

#### F2. Hardening is best-effort with no way to require it

`crates/sc-server/src/lib.rs:358`

```rust
let h = hardening::apply(&restrict);
tracing::info!(landlock = ?h.landlock, seccomp = ?h.seccomp, "process self-restriction applied (best-effort)");
```

The result is logged and dropped. Every failure path inside `apply` warns and
returns `Unavailable`, and the caller has no branch on it. There is no
configuration key that makes either mandatory.

The consequence is that a deployment whose kernel or container runtime silently
refuses Landlock, seccomp, or both starts identically to one where both are
enforced. `docs/proposals/stowcloud-13-deployment.md` describes a security
posture in layers; this is the layer that can be absent without anyone finding
out, and the operator has no way to say "if you cannot do this, do not start".

**Closed by** D3. **Test**: a startup test with the kernel probe faulted, under
`hardening = "required"`, asserting a refusal to start and the reason on stderr.

#### F3. No process seccomp filter at all on aarch64

`crates/sc-server/src/hardening.rs:125`

```rust
#[cfg(not(target_arch = "x86_64"))]
const DENIED_SYSCALLS: &[u32] = &[];
```

An empty list short-circuits to `Unavailable` with a warning. `Dockerfile`
builds and `docker.yml` publishes `linux/arm64`, so a published image runs with
no process-level syscall filter, and the only signal is one warning line among
the startup logs.

**Closed by** D4, which requires a verified mapping per architecture and refuses
to start under `required` where there is none, rather than continuing quietly.

#### F4. A directory is materialised before it is filtered

`crates/sc-vfs/src/backend/linux.rs:644`

`dir_read_entries` iterates `fs::Dir::read_from` into a `Vec<DirEntry>` and
returns the whole thing. Every caller, including the ones that want the first
page of a listing and the ones that want a single name, gets the entire
directory allocated first.

`docs/proposals/stowcloud-11-footprint.md` sets the target at 32 GB of RAM and
12 TB of files. One directory with several million entries is bounded by
available memory and nothing else, and the product's premise is that other
programs write these directories, so its size is not ours to assume.

**Closed by** D5, and by the streaming signature in
[`stowcloud-3-vfs-and-paths.md`](stowcloud-3-vfs-and-paths.md) §4.3.
**Test**: a directory with a synthesised entry count that would exceed the cap,
asserting the streaming call completes and the buffered call refuses.

#### F5. Read paths hold a writable descriptor

`crates/sc-vfs/src/backend/linux.rs:258`

`open_read` attempts `O_RDWR` first and falls back to `O_RDONLY` only on
`EACCES` or `EPERM`. The recorded reason is real: `sc-upload` finalizes an
upload by reading through the handle it created, and on Linux a `pread` against
a write-only descriptor is `EBADF`.

The cost is that the privilege on a descriptor is decided by the file's mode
rather than by what the caller intends to do with it. Every plain read of a
mode-644 file owned by the server's uid holds a handle that can write.

**Closed by** an explicit intent argument, [`3`](stowcloud-3-vfs-and-paths.md)
§5-1. **Test**: assert the descriptor's access mode from `/proc/self/fdinfo`
matches the intent for both values.

#### F6. A thread-local decides what a directory read returns

`crates/sc-vfs/src/backend/linux.rs:637`

`INCLUDE_RESERVED` is a `thread_local!` `Cell<bool>` consulted inside
`dir_read_entries` to decide whether control files (the `.scpart-` prefix) are
filtered out. Two trusted callers set it: the upload orphan sweep and the trash
GC.

Ambient state deciding a security-relevant answer is a defect on its own terms:
the call site does not say what it is asking for, so a reader of either caller
cannot tell, and a new caller inherits whatever the thread was last used for.
It is also unportable in the specific sense that matters here, because goroutines
migrate between threads and Go has no equivalent.

**Closed by** D2, and by the explicit `ReservedPolicy` argument in
[`3`](stowcloud-3-vfs-and-paths.md) §5-1.

#### F7. Three metadata writes fail in silence

`crates/sc-vfs/src/backend/linux.rs:317`, `:330`, `:337`

```rust
let _ = fs::fchmod(&fd, m);
let _ = fs::chmodat(&parent_fd, leaf_nfc.as_str(), m, AtFlags::empty());
let _ = fs::chownat(&parent_fd, leaf_nfc.as_str(), Some(owner), Some(group), ...);
```

Three discarded results on the create and mkdir paths. The premise of the
product is principle 3: the folder is not ours and other services are reading
it. A file created with the wrong mode, or a directory whose configured
ownership was not applied, is exactly the failure that makes Jellyfin or a
backup script stop seeing files, and it produces no error, no log line and no
failed request.

`docs/proposals/stowcloud-16-correctness-sweep.md` records the same class under
"three metadata writes that fail in silence".

**Closed by** D1, and by the durable-write helper in
[`3`](stowcloud-3-vfs-and-paths.md) §4.3, which fails the operation when the
mode cannot be restored.

#### F8. Two files carry thirteen per cent of the tree

`crates/sc-http/src/routes.rs` (9,012 lines),
`crates/sc-server/src/bridge.rs` (3,953 lines)

12,965 of 99,614 lines in two files. Neither has a seam a reader can navigate
by, which is the operative problem rather than the count: finding the handler
for one route means searching, and a reviewer cannot tell what a change is
adjacent to.

**Closed by** D19, a line-count gate, and by the package split in
[`8`](stowcloud-8-http-and-api.md) §4.1.

#### F9. Panic density against `panic = "abort"`

Workspace-wide: 3,404 `.unwrap()`, 358 `.expect(`, 40 `panic!(`.

`Cargo.toml`'s release profile sets `panic = "abort"`, so any of these reachable
from a request is a process kill rather than a failed request. The count
includes test code, and nothing separates the two, so the number cannot be
driven down or even measured meaningfully as it stands.

**Closed by** D7, which makes the policy explicit in both directions: a
recovered panic on a request path is a 500, and a panic in the preview worker
is left to kill the worker because that is the correct outcome there.

#### F10. `now_ns` unwraps the clock

`crates/sc-upload/src/engine.rs:37`

```rust
std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap()
```

A clock behind the epoch aborts the process. Reachable on a machine whose RTC
has not been set, which is an ordinary state for a small server after a dead
battery, and the upload engine is on the request path.

**Closed by** D8: one injectable clock, no panic on a clock behind the epoch.

#### F11. The file ETag cannot see a same-nanosecond rewrite

`crates/sc-upload/src/engine.rs:46`

```rust
format!("{:x}-{:x}", stat.mtime_ns as u64, stat.size)
```

`mtime` plus size. A rewrite landing in the same nanosecond with the same length
is invisible to `If-Match`, so the conflict check the product advertises passes
and the edit is lost. The nanosecond granularity makes it unlikely rather than
impossible, and the case it is likeliest in is the one that matters: a program
rewriting a file in place, which is what principle 3 says to expect.

**Closed by** [`7`](stowcloud-7-core-domain.md) §4.3, which adds the inode
generation available from `statx` where the filesystem provides it and states
the residual case where it does not.

#### F12. A configuration mode that is not implemented

`crates/sc-watch/src/lib.rs:119`

```rust
tracing::warn!("fanotify backend requested but not implemented; falling back to raw inotify (Linux) / notify (portable)");
```

The configuration accepts `fanotify`, warns, and does something else. An
operator who set it believes they have whole-mount watching and has per-directory
watching, with the difference invisible except in a log line at startup.

**Closed by** deletion. [`3`](stowcloud-3-vfs-and-paths.md) §3.2 removes the
value from the config enum until something implements it; adding it back is one
line when it does.

#### F13. Principle 4 is held by a text search

`scripts/verify.sh:140`

```sh
grep -rIn -iE '\boc[:_-]|\bocs\b|remote\.php' $CORE_DIRS | grep -vE '^[^:]+:[0-9]+:\s*(//|/\*|\*)'
```

The compat isolation rule is one of the five principles, and this grep plus the
`--no-default-features` build are what enforce it. The grep reads text, so a
constant defined elsewhere, a string built from parts, or a field named without
the vendor prefix all pass it while doing exactly what it exists to prevent.

**Closed by** the import-graph gate in
[`13`](stowcloud-13-compat-nc.md) §4.2, which reads the real dependency graph.
The grep is kept as well, narrowed to strings and comments, because it catches a
different mistake cheaply.

#### F14. The Korean-text gate passes unconditionally on the development host

`scripts/verify.sh:158`

```sh
KO_HITS=$(grep -rIn -P '[\x{AC00}-\x{D7A3}]' crates/*/src ...)
```

`grep -P` on the Windows development box does not interpret `\x{...}` as a
UTF-8 code point, so the pattern matches nothing and the gate reports PASS
whatever the tree contains. Only the Linux CI job actually checks it, which
means the gate is absent exactly where a developer would want it: before the
push.

**Closed by** D15, which makes the rule structural rather than textual: the
wire error type takes a `MessageKey`, not a `string`, so a sentence cannot be
put on the wire in any language. The scan is kept for comments and logs, in Go,
where the pattern is a `unicode.RangeTable` and behaves identically on every
host.

## 5. API Design

### 5-1. New / Modified

No API. This document is a ledger.

### 5-2. Error Handling

Not applicable. The errors these findings produce are specified in the documents
that close them.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Owner |
|---|---|---|---|
| Phase 1 | F1, F2, F3, F4, F5, F6, F7 | with the phase | heavycaffeiner |
| Phase 2 | F10 (the clock is a Phase 0 lint, its first users are here) | with the phase | heavycaffeiner |
| Phase 4 | F11 | with the phase | heavycaffeiner |
| Phase 5 | F8, F9 | with the phase | heavycaffeiner |
| Phase 10 | F13 | with the phase | heavycaffeiner |
| Phase 0 | F12, F14, and the lints behind F1 to F11 | with the phase | heavycaffeiner |

A phase closes its findings inside the phase. None may be carried to Phase 13,
which is measurement and cutover and has no room for a fix.

### 6-2. Dependencies

None beyond the phases above. This document depends on nothing and everything
depends on it.

## 7. References

- `crates/sc-server/src/hardening.rs`, `crates/sc-vfs/src/backend/linux.rs`,
  `crates/sc-upload/src/engine.rs`, `crates/sc-watch/src/lib.rs`,
  `scripts/verify.sh`: the code every finding above is read from.
- `crates/sc-preview/src/worker/jailed/seccomp.rs`: the filter that already
  does what F1 and F3 ask for, and whose module documentation names the gap.
- `docs/proposals/stowcloud-16-correctness-sweep.md`: records F7's class.
- `docs/proposals/stowcloud-17-audit-gaps.md`: seven further promises the
  current code does not keep, each owned by the phase for its subsystem.
- `docs/proposals/stowcloud-11-footprint.md`: the resource target F4 is
  measured against.
