# VFS and paths - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The security core in Go: `openat2` resolution from a share root descriptor, the
three path types, descriptor ownership without `Drop`, streaming directory
reads, and the one durable-write helper every mutation goes through. This is
principle 2 ("a path is a kernel handle, not a string") and it is the phase
everything else waits on.

## 2. Background & Motivation

`crates/sc-vfs` is 3,391 lines and the smallest crate that matters most. Its
Linux backend is a direct translation of `openat2` semantics, and the pattern it
uses throughout is the thing to preserve exactly: resolve the *parent* directory
in one multi-component `openat2` call, so the kernel enforces the resolve flags
across every intermediate component atomically, then act on the leaf with a
plain `*at()` call against that already-safe descriptor.

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
- [ ] One durable-write helper, and a gate that nothing else renames (D11).
- [ ] A fuzz target on path parsing (D16).

### 3.2 Non-Goals

- [ ] A portable backend. See
      [`stowcloud-2-gate-and-toolchain.md`](stowcloud-2-gate-and-toolchain.md)
      §4.3.3 for the two bugs the current one hid, and what dropping it costs.
- [ ] `fanotify`. The value is removed from the watch configuration until
      something implements it (F12), which is a deletion in this phase and a
      one-line addition whenever it stops being a lie.
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
  root.go        ShareRoot: the anchor descriptor, policy, registration
  open.go        openat2 resolution: resolve_dir, the leaf hop
  read.go        streaming and buffered directory reads
  file.go        File: pread, pwrite, stat, truncate, sync, copy_range
  durable.go     the write-then-rename helper (D11 lives here)
  errno.go       errno to typed error
```

One package, not a package per concern. It is the security core and it is read
as a unit; splitting it would put the resolve flags in one file and the calls
that depend on them in another.

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

`ReadDirFunc` is `unix.Getdents` into a reused 32 KiB buffer, parsed with
`unix.ParseDirent`. `.` and `..` are skipped, and `ReservedPolicy` decides
whether the `.scpart-` prefix is filtered, at the call site, visibly (F6).

`DT_UNKNOWN` stays conservative: reported as `KindOther` rather than paying a
`statx` per entry. Callers that need certainty stat the specific entry, exactly
as today.

#### 4.3.4 Open intent

F5's fix. `OpenRead` takes what the caller means to do:

```go
type AccessIntent uint8
const (
    IntentRead      AccessIntent = iota // O_RDONLY. Everything except the below.
    IntentReadWrite                     // O_RDWR. Only the upload finalizer,
                                        // which verifies a digest through the
                                        // handle it created.
)
```

`IntentReadWrite` is used at exactly one call site, and a gate greps for a
second. The fallback chain disappears with it: an `O_RDONLY` open does not fail
with `EACCES` on a readable file, so there is nothing to fall back from.

#### 4.3.5 The durable-write helper

D11 and F7. One function, and `os.Rename`, `unix.Renameat` and
`unix.Renameat2` are callable from nowhere else in the tree:

```go
// WriteDurable stages content under the reserved prefix, makes it durable, and
// publishes it under p atomically. The staging name is unlistable while it
// exists, the mode and ownership of a file being replaced are restored before
// the rename, and the parent directory is synced after it.
func (r *ShareRoot) WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) error
```

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
   back to `renameat` on `ENOSYS` only when the flag was empty anyway.
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
// path that arrives over the wire (D20).
func ParseVpath(s string) (Vpath, error)

// JoinControl appends a control-file name under the reserved prefix. It is the
// only function that may produce one, and user input cannot reach it because
// ParseVpath rejects the prefix.
func (p SafePath) JoinControl(id string) SafePath
```

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
| Phase 1e | `internal/watch`: inotify, the `fanotify` deletion | S | 1b | heavycaffeiner |

1e is independent of 1c and 1d. Phase 1a can start before Phase 0c's answers
land; 1b cannot, because A1 and A2 are about the calls it makes.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `golang.org/x/sys/unix` | every syscall in this document |
| `golang.org/x/text/unicode/norm` | NFC and NFD candidate spellings |

Both are assumption A1 and A2's subject matter. Nothing else.

## 7. References

- `crates/sc-vfs/src/backend/linux.rs`: the implementation this translates,
  including the two comments quoted in document 2 §4.3.3 and the reserved-prefix
  history quoted in §4.3.5.
- `crates/sc-upload/src/engine.rs:1-18`: the naming note that records what
  disguising a control file cost.
- `docs/proposals/stowcloud-2-core-vfs.md`: `SafePath`, the syscall contract,
  the virtual root.
- [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3: F4, F5, F6, F7, F12.
- `openat2(2)`, `statx(2)`, `renameat2(2)`, `copy_file_range(2)`,
  `getdents64(2)`, `inotify(7)`.
