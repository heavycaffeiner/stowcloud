# VFS and paths - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Implemented**                  |
| Reviewers  |                                  |

---

## 1. Summary

The security core in Go: `openat2` resolution from a share root descriptor, the
three path types, descriptor ownership without `Drop`, streaming directory
reads, and the VFS-owned mutation and publication boundaries. This is principle
2 ("a path is a kernel handle, not a string") and it is the phase everything
else waits on.

## 2. Background & Motivation

`crates/sc-vfs` is 3,391 lines and the smallest crate that matters most. Its
Linux backend is a direct translation of `openat2` semantics, and the pattern it
uses throughout is the thing to preserve exactly: resolve the *parent* directory
in one multi-component `openat2` call, so the kernel enforces the resolve flags
across every intermediate component atomically, then act on the leaf with a
plain `*at()` call against that already-safe descriptor.

**The stance underneath it** is
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S1: the class of
bug this package exists to remove is normalise, prefix-check, then open. Every
published escape in that class comes from treating a path as a string that is
validated once and trusted afterwards. `openat2(RESOLVE_BENEATH)` moves the
check into the same syscall as the open, so there is no window because there is
no second step. Nothing in this package may reintroduce a step: no descriptor
cache, no normalisation, no revalidation of a path already resolved.

S6 is the other one that decides things here. The share is not ours, so the
package's job on every write is to leave the directory as usable to its other
readers as it found it, and a mode that could not be restored is an error
rather than a log line (F7).

Go changes three things about how that gets written and none about what it
means:

1. There is no `Drop`, so a descriptor's lifetime is the author's problem on
   every path including the error paths.
2. There is no `OwnedFd`/`BorrowedFd` distinction, so "who closes this" is a
   convention rather than a type.
3. There are no thread-locals worth the name, which forces F6's ambient flag to
   become a parameter whether or not anyone wanted it to.

Findings F4, F5, F6 and F7 all live in this package.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Every filesystem access resolves through a share root descriptor with the
      share's resolve flags. No API below `httpapi` accepts a string path.
- [ ] `SafePath`, `SharePath` and `Vpath` as three unconvertible types (D10).
- [ ] Directory reads stream (F4).
- [ ] Descriptor privilege decided by caller intent, not file mode (F5).
- [ ] The reserved-name policy as an argument (F6).
- [ ] Mode and ownership failures surfaced (F7).
- [ ] One durable content-replacement helper, named namespace and database
      publication operations, and a gate that no raw rename escapes
      `internal/vfs` (D11).
- [ ] A fuzz target on path parsing (D16).
- [ ] An **executable escape proof**, ported: S17 applies here as much as it
      does to the jail. §4.5 says what it has to attempt.

### 3.2 Non-Goals

- [ ] A portable backend. See
      [`stowcloud-2-gate-and-toolchain.md`](stowcloud-2-gate-and-toolchain.md)
      §4.3.3 for the two bugs the current one hid, and what dropping it costs.
- [ ] `fanotify`. The value is removed from the watch configuration until
      something implements it (F12), which is a deletion in this phase and a
      one-line addition whenever it stops being a lie.
- [ ] **Path normalisation.** `.` and `..` are rejected, never normalised, and
      this is a non-goal rather than an omission: normalising is what creates
      the bypass. A resolver that rewrites `a/../b` into `b` has to be right
      about every encoding, every separator and every Unicode form, and being
      wrong once is an escape. Rejecting is right by not deciding.
- [ ] **Rewriting on-disk filenames to a Unicode normal form.** The candidate
      loop in §4.3.6 exists so that a name another program wrote is found as it
      is written. Renaming it to suit us is the sidecar-litter failure wearing a
      different hat.
- [ ] Extended attributes, ACLs beyond mode bits, or `io_uring`. None is used
      today and none is needed for parity.
- [ ] Caching resolved descriptors across requests. The current tree does not,
      and a descriptor cache is a TOCTOU surface reintroduced by hand.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/vfs
  path.go        SafePath, Vpath, SharePath, parsing, reserved names
  norm.go        NFC/NFD candidate spellings
  types.go       Stat, SharePolicy, FsType, DirEntry: the plain data
  errors.go      the typed errors, which path.go raises off Linux too
  root.go        ShareRoot: the anchor descriptor, policy, registration
  open.go        openat2 resolution: resolve_dir, the leaf hop
  read.go        streaming and buffered directory reads
  file.go        File: pread, pwrite, stat, truncate, sync, copy_range
  durable.go     the write-then-rename helper (D11 lives here)
  publish_linux.go
                 durable no-clobber publication for a complete non-share file
  publish_other.go
                 development-host stand-in for importer tests only
  replace_linux.go
                 durable replacement of a trusted private control file
  replace_other.go
                 development-host stand-in, never a shipping guarantee
  errno.go       errno to typed error
  caps.go        the kernel capability probe, which has to tell a syscall
                 blocked by a seccomp profile from one an old kernel lacks
                 (stowcloud-15 §4.2); `stowcloud caps` prints what it found
```

One package, not a package per concern. It is the security core and it is read
as a unit; splitting it would put the resolve flags in one file and the calls
that depend on them in another.

**Four of those files carry no build tag.** `path.go`, `norm.go`, `types.go`
and `errors.go` issue no syscall, so the parser, the candidate spellings and
the fuzz target run in the ordinary `go test` on the development host. The
descriptor and share-filesystem backend is `//go:build linux`.
The `_other.go` publication and private-file replacements are narrow
development-host stand-ins. They let ordinary control-file tests compile and
run, but they are not a portable share backend or a shipping durability
guarantee. `errors.go` exists because `path.go` raises
`ErrTooLarge` and `ErrReservedName` on both hosts, so the sentinels cannot live
beside the errno table.

### 4.2 Data Model Changes

None on disk. The in-memory types are §5-1.

### 4.3 Core Logic

#### 4.3.1 Resolve flags

Unchanged from `resolve_flags` in the Rust backend, and worth restating because
it is the whole security posture in five lines:

| Share symlink policy | Flags |
|---|---|
| `Deny` | `RESOLVE_BENEATH \| RESOLVE_NO_SYMLINKS` |
| `WithinShare` | `RESOLVE_IN_ROOT` |
| `Follow` | `RESOLVE_BENEATH` |

`RESOLVE_NO_MAGICLINKS` is added unconditionally, because it is what blocks
escape through `/proc/self/fd/*`. `RESOLVE_NO_XDEV` is added unless the share
opts into crossing mounts.

The names are an existing wire contract, and `Follow` does not mean escape the
share. It follows relative symlinks while `RESOLVE_BENEATH` keeps the result
beneath the root; an absolute target is refused. `WithinShare` additionally
rebases an absolute target into the share through `RESOLVE_IN_ROOT`. The Go
type comments must state this distinction and must not describe `Follow` as
unconditional.

#### 4.3.2 Descriptor ownership

Every descriptor is an `*os.File`, obtained with `os.NewFile(uintptr(fd), name)`
immediately after the syscall that produced it, and never held as a bare `int`.
Three reasons, and only the first is about convenience:

1. `defer f.Close()` becomes available, which is the only teardown mechanism Go
   offers and is worthless on an integer.
2. A raw `int` is invisible to the garbage collector, so nothing keeps the
   owning object alive and nothing closes it on an early return. Go's finalizer
   on `*os.File` is a backstop, not a policy, but a backstop is better than the
   nothing an `int` has.
3. Passing `f.Fd()` to a syscall wrapper takes the descriptor out of the
   runtime's view for the duration of the call, so every such site is wrapped in
   `runtime.KeepAlive(f)` after it. This is mechanical and it is the one Go
   detail in this package that a reviewer has to know to look for.

Note that `(*os.File).Fd()` sets the descriptor to blocking mode, which is
correct here (these are regular files and directories, never sockets) and would
not be for the job socket in
[`stowcloud-4-jail-and-hardening.md`](stowcloud-4-jail-and-hardening.md), which
uses `SyscallConn` instead.

#### 4.3.3 Directory reads

F4's fix. Two entry points, and the streaming one is the primitive:

```go
// ReadDirFunc streams the entries of p, calling fn once per entry with a name
// that is only valid for the duration of the call. It never materialises the
// directory. fn returning false stops the walk cleanly.
func (r *ShareRoot) ReadDirFunc(p SafePath, policy ReservedPolicy, fn func(DirEntry) bool) error

// ReadDir collects entries into a slice, refusing with ErrTooLarge past
// limits.DirEntriesBuffered. Callers that cannot tolerate a refusal use
// ReadDirFunc.
func (r *ShareRoot) ReadDir(p SafePath, policy ReservedPolicy) ([]DirEntry, error)
```

`ReadDirFunc` is `unix.Getdents` into a reused 32 KiB buffer. `.` and `..` are
skipped, and `ReservedPolicy` decides whether the `.scpart-` prefix is filtered,
at the call site, visibly (F6).

`DT_UNKNOWN` stays conservative: reported as `KindOther` rather than paying a
`statx` per entry. Callers that need certainty stat the specific entry, exactly
as today.

**The records are walked directly rather than through `unix.ParseDirent`**, and
that is a correction to an earlier draft of this document rather than a
preference. `ParseDirent`'s signature returns names only
(`func ParseDirent(buf []byte, max int, names []string) (consumed, count int,
newnames []string)`), so it drops both fields this section depends on: `d_type`,
which is what the `DT_UNKNOWN` rule above is about, and `d_ino`, which is what
lets a caller order a later `stat` batch by `(dev, ino)`. `getdents64` lays a
record out identically on every architecture, so the four field offsets are the
kernel ABI and are read with `binary.NativeEndian` rather than through `unsafe`.

#### 4.3.4 Open intent

F5's fix. `OpenRead` takes what the caller means to do:

```go
type AccessIntent uint8
const (
    IntentRead      AccessIntent = iota // O_RDONLY. Everything except the below.
    IntentReadWrite                     // O_RDWR. Only the upload engine's
                                        // part-file handle, which takes chunk
                                        // writes and is read back at finalize
                                        // to verify the whole-file digest.
)
```

`IntentReadWrite` is used at exactly one call site, the upload engine's lazy
reopen of a part file ([`9`](stowcloud-9-upload.md) §4.3.2), and a gate greps
for a second. Note that the *create* path does not need it: the durable-write
helper opens its staging file `O_RDWR` by construction (§4.3.5 step 2), so a
freshly created part file is already writable without going through here.

The fallback chain disappears with the intent argument. An `O_RDONLY` open does
not fail with `EACCES` on a readable file, so there is nothing to fall back
from.

#### 4.3.5 The durable-write helper

D11 and F7. `WriteDurable` is the only staged share-content replacement
operation. Raw rename calls are callable nowhere outside `internal/vfs`:

```go
// WriteDurable stages content under the reserved prefix, makes it durable, and
// publishes it under p atomically. The staging name is unlistable while it
// exists, the mode and ownership of a file being replaced are restored before
// the rename, and the parent directory is synced after it.
//
// Durable reports what the write did to the entry it replaced: whether there
// was one, and the error that refused a uid restore if one did.
func (r *ShareRoot) WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) (Durable, error)
```

This does not subsume an explicit namespace move, publication of an already
complete database, or replacement of a trusted private control file.
`ShareRoot.Rename`, `PublishNew` and `ReplaceFileDurable` own those cases.
`PublishNew` is no-clobber. `ReplaceFileDurable` stages with the requested
private mode, syncs, replaces and syncs the parent. Neither is a substitute for
`WriteDurable` on share-content replacement or upload finalization.

**Why there is a return value at all**, since step 5 below is what forces it.
The mode is fatal: a replacement that could not take the original's mode fails.
The ownership is not, and cannot be, because `fchown` failing with `EPERM` is
the ordinary answer for an unprivileged process and failing the write there
would discard content that was otherwise fine. "Surfaced to the caller, which
decides whether transplanting the group alone is enough" is only true if the
caller is told after the file is published, so it is a field rather than an
error.

The sequence, and every step is load-bearing:

1. `SafePath.JoinControl` builds the `.scpart-{id}` staging name. That is the
   only call permitted to use the reserved prefix, and user input can never
   reach it because `SafePath.Parse` rejects the prefix outright. The Rust
   tree's own comment records what happens without this: an earlier revision
   disguised the name to get past validation, which defeated the reserved-name
   filter and put part files in every listing, in the web UI and over WebDAV,
   for the duration of every upload.
2. `openat2` with `O_CREAT|O_EXCL|O_RDWR|O_NOFOLLOW|O_CLOEXEC`, then `fchmod`
   to the exact configured mode regardless of umask. **The result is checked.**
3. The caller writes through the handle.
4. `fdatasync` the file.
5. If replacing an existing file, `fchmod` and `fchown` to the original's mode
   and ownership. **Both results are checked**, and a failure to restore the
   mode fails the operation. F7 is exactly this being discarded, and the
   product's premise is that Jellyfin and rsync must keep reading the file.
   `fchown` failing with `EPERM` for an unprivileged process is expected and is
   surfaced to the caller, which decides whether transplanting the group alone
   is enough, rather than being swallowed here.
6. `renameat2` with `RENAME_NOREPLACE` when no-clobber was asked for, falling
   back to `renameat` on `ENOSYS` only when the flag was empty anyway. **The
   destination is published under the spelling already on disk**, and NFC only
   when there was nothing there. Normalising unconditionally is a
   data-duplication bug rather than a tidy-up: a file another client wrote in
   NFD, renamed onto its NFC spelling, becomes a second entry with the same
   user-visible name and the one the caller meant to replace is still there.
7. `fsync` the **parent directory**. This is a separate act from step 4 and it
   is what makes the *name* durable. Without it, on ext4 or XFS, a power cut can
   leave the full contents under a `.scpart-` name nobody will look for. Note
   that the sync must be against a descriptor opened for reading, not an
   `O_PATH` one: `fsync` on `O_PATH` is `EBADF`, which is the bug quoted in
   document 2 §4.3.3 and which broke every write on Linux.
8. On any failure after step 1, the staging file is unlinked. In Go this is an
   explicit `defer` with a flag, because there is no `Drop` to do it.

#### 4.3.6 Unicode candidates

A name written by another program may be in NFC or NFD, and the same user-visible
name has to find it either way. `golang.org/x/text/unicode/norm` produces the
candidate spellings, tried in the same order as today: exact, NFC, NFD. New names
are normalised to NFC before creation, which is what `normalize_new_name` does.

This retry loop is why `ENOENT` and `ENOTDIR` both continue to the next
candidate while every other errno returns immediately, and that distinction is
preserved: collapsing it makes a permission error look like a missing file.

#### 4.3.7 Errno mapping

One table, unchanged in meaning:

| errno | Error |
|---|---|
| `ENOENT`, `ENOTDIR` | `ErrNotFound` |
| `EACCES`, `EPERM` | `ErrDenied` |
| `EEXIST` | `ErrExists` |
| `ENOTEMPTY` | `ErrNotEmpty` |
| `ENOSPC` | `ErrNoSpace` |
| `EXDEV` | `ErrCrossDevice` |
| `ELOOP` | `ErrSymlinkDenied` |
| anything else | wrapped, with the errno preserved for `errors.As` |

`ELOOP` mapping to a distinct error rather than to `ErrNotFound` matters: it is
how a share with `SymlinkPolicy.Deny` reports that a symlink was refused, which
is a different fact from the target not existing, and the layer above decides
which one a client is allowed to learn.

### 4.4 Watch

`internal/watch` is small enough to live in this phase. `unix.InotifyInit1`,
`unix.InotifyAddWatch`, and a read loop parsing `unix.InotifyEvent` directly. No
`fsnotify`: it is a portability layer over exactly the thing that is not being
made portable, and it adds a queueing model this code does not want.

The `fanotify` configuration value is deleted (F12). The enum accepts `inotify`
and nothing else, and an unknown value is a configuration refusal rather than a
warning and a fallback.

**The watch strategy is layered rather than absolute**, because
`fs.inotify.max_user_watches` cannot be raised from inside a container and a
watch is per directory, so a large tree cannot be fully watched. A hot set of
directories is watched, everything else is lazily revalidated, and a periodic
rescan backstops both. A queue overflow bumps the share generation to invalidate
everything at once rather than trying to replay what was missed.

**The hot set has two halves and only one of them is an LRU.** The *recent* half
is capped and evicts, which is the part that sounds like the whole feature. The
*sticky* half is refcounted and never auto-evicted, and its input comes from
outside this package: a WebSocket subscription pins the directory it is watching
and releases it on unsubscribe
([`8`](stowcloud-8-http-and-api.md) §4.3.5).

Building only the LRU half is the failure mode, and it is silent. The directory
a user is currently looking at is exactly the one an LRU will evict under load,
so invalidations stop arriving for the folder in front of them while everything
else keeps working. `Subscribe` and `Unsubscribe` are therefore part of this
package's public surface, not an internal detail of the HTTP layer.

What survives every one of those degradations is the property the design
actually promises: **a stale answer is always detectable and self-correcting.**
Not "every change is seen immediately", which no container can promise.

### 4.5 The escape proof

The jail has one ([`4`](stowcloud-4-jail-and-hardening.md) §4.3.6) and so does
this package, for the same reason: the claim that the *kernel* confines a share,
rather than this code's own checks, is a claim that can be executed.

A Linux-only test that, from a real share root on a real filesystem, attempts
each escape and asserts the kernel refused it. **Five of the eight have a Rust
ancestor to translate; three are new work**, written against §4.3.1's resolve
flags rather than ported, and they are marked below. The distinction matters
for estimating this task: "port the escape proof" is most of it and not all.

| Attempt | Expected | Source |
|---|---|---|
| a path containing `..` | rejected at parse, before any syscall | ported |
| an absolute path | rejected at parse | ported |
| a symlink inside the share pointing at a file outside it | `ErrSymlinkDenied` under `Deny`, resolved inside the root under `WithinShare` | ported |
| traversal through a symlinked directory | same | ported |
| a symlink pointing at `/proc/self/fd/N` | refused; this is what `RESOLVE_NO_MAGICLINKS` is for | **new** |
| a component replaced by a symlink between two calls | refused; there is no window to race, which is the whole point | **new** |
| a path crossing a bind mount inside the share, under `NO_XDEV` | `ErrCrossDevice` | **new** |
| a name spelled in the other Unicode form | found, because the candidate loop is not an escape | ported |

It skips on non-Linux and is **required** on the Linux job, which is the same
status the jail proof has. The distinction it exists to demonstrate is worth
restating: every one of these could be made to pass by string validation, and
string validation is what S1 refuses. A proof that passes for the wrong reason
is worse than none, so each case asserts the *errno* the kernel returned, not
merely that the operation failed.

## 5. API Design

### 5-1. New / Modified

```go
package vfs

// ShareRoot is one configured share, held open as an O_PATH directory
// descriptor for the process lifetime. Every resolution starts here; nothing
// in this package accepts a path relative to the process working directory.
type ShareRoot struct {
    ID     ShareID
    anchor *os.File
    policy SharePolicy
    dev    uint64
    fsType FsType
}

// OpenShareRoot opens host as an anchor and records the device and filesystem
// type for the cross-mount and reflink decisions. It does not validate that
// host is a sensible share; that is the config layer's boundary (D20).
func OpenShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, error)

// Stat resolves p under the share's policy and stats the leaf. btime comes
// from statx and is nil where the filesystem does not carry one, which is
// reported rather than faked: a zero btime and an absent one are different
// facts and the compat layer needs the difference.
func (r *ShareRoot) Stat(p SafePath) (Stat, error)

// OpenRead opens p for reading. intent decides the access mode; only the
// upload finalizer may pass IntentReadWrite. The returned File owns its
// descriptor and the caller must Close it.
func (r *ShareRoot) OpenRead(p SafePath, intent AccessIntent) (*File, error)

// Mkdir creates p with the share's configured directory mode, applied verbatim
// rather than through umask, and applies the configured ownership. A failure
// to apply either is returned, not logged.
func (r *ShareRoot) Mkdir(p SafePath) error

// Rename moves from to to within this share. noReplace maps to
// RENAME_NOREPLACE; without it the destination is replaced atomically.
// Crossing a mount boundary inside one share is an expected outcome, not a
// bug, and surfaces as ErrCrossDevice for the caller to fall back on.
func (r *ShareRoot) Rename(from, to SafePath, noReplace bool) error

// CopyRange copies len bytes from src to dst using copy_file_range: a reflink
// on btrfs and XFS when aligned, an in-kernel copy otherwise. It loops,
// because a short copy is documented even on success, and falls back to a
// bounded buffered loop for the remainder on EXDEV, EOPNOTSUPP or ENOSYS
// only. Any other errno is real and is returned.
func CopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error)

// Space reports the filesystem behind p. available is f_bavail, never f_bfree:
// the reserved blocks in the difference are not ours to write into, and
// reporting them as free promises an uploader room that ENOSPC then refuses.
func (r *ShareRoot) Space(p SafePath) (FsSpace, error)
```

```go
// ParseVpath validates a client-supplied path: "{share label}/{rest}". It
// rejects empty components, "." and "..", NUL, a component over 255 bytes, a
// path over limits.PathBytes, more than limits.PathComponents components, and
// any component carrying a reserved prefix. It is the trust boundary for every
// path that arrives over the wire (D20). The empty string is the virtual root,
// which is the one path naming no share.
func ParseVpath(s string) (Vpath, error)

// JoinControl appends a control-file name under the reserved prefix. It is the
// only function that may produce one, and user input cannot reach it because
// ParseVpath rejects the prefix. It returns an error for the same reason every
// other join does: the length and component bounds still apply, and a helper
// that can only panic on a bound is a helper that will.
func (p SafePath) JoinControl(name string) (SafePath, error)
```

**No depth parameter anywhere.** The Rust tree threads a per-share `max_depth`
through `parse`, `join` and `control`; D5 says nothing takes a limit as a
parameter a caller could widen, so every bound here is `limits.PathComponents`,
`limits.PathBytes` and `limits.NameBytes` read directly. That also removes
`max_depth` from `SharePolicy`.

### 5-2. Error Handling

The errors in §4.3.7, plus:

| Error | Meaning |
|---|---|
| `ErrTooLarge` | a `limits` bound refused: a path, a component, or a buffered directory read |
| `ErrReservedName` | a client path carried a control prefix |
| `ErrNotADirectory` | the leaf resolved but is not the kind the caller asked for |

None of these choose an HTTP status. That is
[`stowcloud-8-http-and-api.md`](stowcloud-8-http-and-api.md)'s single mapping
function, and the existence rule (an unlistable path is 404 everywhere, never
403) is applied there, where the caller's grants are known.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 1a | `path.go`, `norm.go`, the reserved set, the fuzz target, D10's three types | M | Phase 0 | heavycaffeiner |
| Phase 1b | `root.go`, `open.go`, `errno.go`: resolution with the policy flags | M | 1a | heavycaffeiner |
| Phase 1c | `read.go`, `file.go`: streaming reads, pread/pwrite, statx, copy_file_range | M | 1b | heavycaffeiner |
| Phase 1d | `durable.go` and the D11 gate | S | 1c | heavycaffeiner |
| Phase 1e | `internal/watch`: inotify, the hot set, the periodic rescan, the `fanotify` deletion | S | 1b | heavycaffeiner |
| Phase 1k | `caps.go` and the escape proof (§4.5), required on the Linux job | S | 1c | heavycaffeiner |

1e is independent of 1c and 1d. Phase 1a can start before Phase 0c's answers
land; 1b cannot, because A1 and A2 are about the calls it makes.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `golang.org/x/sys/unix` | every syscall in this document |
| `golang.org/x/text/unicode/norm` | NFC and NFD candidate spellings |

Nothing else.

**A1 is settled.** Every wrapper this document calls is present in
`x/sys/unix` v0.47.0 and shaped as assumed, compiling for both `linux/amd64`
and `linux/arm64`: `Openat2` with `OpenHow`, `Statx` with `Statx_t`,
`Renameat2`, `CopyFileRange`, `CloseRange`, `Fstatfs` with `Statfs_t`,
`Getdents`, `ParseDirent`, `Socketpair`, `UnixRights`,
`ParseSocketControlMessage`, `Prctl`, `Setrlimit` with `Rlimit`, and `Exec`.
None of them has to become a raw `unix.Syscall` and nothing in this document
moves. `go/internal/unixprobe/probe_linux.go` is the file that compiles the
claim; a rename or a changed signature in a future `x/sys` is a build failure
there rather than a surprise in the phase that needs it. A2, which is about
Landlock and does not have the same answer, is in
[`4`](stowcloud-4-jail-and-hardening.md) §6-2.

## 7. References

- `crates/sc-vfs/src/backend/linux.rs`: the implementation this translates,
  including the two comments quoted in document 2 §4.3.3 and the reserved-prefix
  history quoted in §4.3.5.
- `crates/sc-upload/src/engine.rs:1-18`: the naming note that records what
  disguising a control file cost.
- `crates/sc-vfs/src/safe_path.rs`, `reserved.rs`, `share_root.rs`: the path
  validation, the reserved set and the anchor handling this translates.
- [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3: F4, F5, F6, F7, F12.
- `openat2(2)`, `statx(2)`, `renameat2(2)`, `copy_file_range(2)`,
  `getdents64(2)`, `inotify(7)`.
