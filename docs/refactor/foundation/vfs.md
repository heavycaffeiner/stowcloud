# Foundation rebuild: vfs

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/vfs` is referenced as a behavioral specification only. The new
> implementation is written completely new; nothing is copied.

Target package: `engine/infra/vfs`. `//go:build linux` except the pure path-type
files (path vocabulary, name validation, error sentinels), which build on any
OS so a non-Linux development host can still compile a caller. This matches
the current split (`root.go`, `open.go`, `read.go`, `caps.go`, `require.go`,
`durable.go` are Linux-only; `path.go`, `norm.go`, `types.go`, `errors.go`
are not).

## Purpose

`vfs` is the only code in the engine allowed to call `open`, `openat2`,
`rename`, `unlink`, `mkdir`, `statx` or any other filesystem syscall against
a share's content. Every other package, `core` included, reaches disk
through a `ShareRoot` method or not at all. This is not a convenience
boundary; it is where every escape, symlink and race guarantee in the
product either holds or does not.

The package's four jobs, in the order a request touches them:

1. **Admission.** Decide, once per share and once per nested mount, whether
   a filesystem's guarantees are strong enough to hold this design's
   contracts (stable inode identity, a birth time, correct notification).
2. **Path validation.** Turn an untrusted string into one of three typed
   path vocabularies, refusing anything that could mean something other
   than what it says.
3. **Resolution.** Turn a validated path plus a share root into an open
   descriptor, entirely inside the kernel's own atomic path-walk primitive,
   so no window exists between checking a component and using it.
4. **Durable mutation.** Stage, sync, and atomically publish every write,
   so a crash mid-write leaves either the old state or the new one, never a
   torn file findable under its real name.

## Threat model

Three actors this package defends against, and the mechanism for each:

- **A client-supplied path.** A URL, a WebDAV header, an upload filename:
  anything that started outside this process. The path-type system
  (`Vpath` to `SharePath`/`SafePath`) is the trust boundary; nothing crosses
  it without passing the traversal and reserved-name table. A string that
  already passed validation is still re-validated at every crossing,
  because a `SafePath` value is the only thing this package's syscalls
  accept, and building one is the only way in.
- **The filesystem itself, racing with this process.** Another program (an
  SMB client, a sync daemon, an administrator's shell) can rename, replace,
  or symlink anything under a share at any time this process is not holding
  a lock on it, because share directories are shared with other software by
  design. The defense is not detecting the race; it is never having a
  window for one. Every multi-component resolution is one `openat2` call
  under `RESOLVE_BENEATH`/`RESOLVE_IN_ROOT`/`RESOLVE_NO_MAGICLINKS`, so the
  kernel enforces confinement atomically with the open. There is no
  normalize-then-check-then-open sequence anywhere in this package: that
  shape is exactly the TOCTOU race the design exists to close, and
  `require.go`'s refusal to start without `openat2` says so explicitly.
- **A filesystem type that cannot hold the design's promises.** tmpfs loses
  everything on reboot; NFS and CIFS mounts do not guarantee inode
  stability across a remount; overlayfs's writable layer does not survive a
  container restart; FUSE cannot prove either. `admit.go`'s allow-list
  refuses registration on anything not individually verified, rather than
  accepting an unknown magic number and finding out in production that its
  guarantees do not hold.

What this package deliberately does not defend against: a process running
as the same uid reading or writing the share directly, outside this server
(a local shell, cron, another program). Confinement is a property of paths
this server resolves, not a property of the directory's permission bits.

## Spec: share root lifecycle

### The ShareRoot type

```go
type ShareRoot struct {
    ID ShareID
    // unexported: anchor *os.File, policy SharePolicy, dev, ino uint64,
    // fsType FsType, hasBtime bool, admitted map[uint64]struct{}
}
```

`ShareRoot` holds one `O_PATH` directory descriptor, opened once and kept
for the process lifetime (or until the share is deregistered). Every
resolution in the package starts from this descriptor; no method accepts a
path relative to the process working directory, and no method opens
anything by an absolute host path after registration.

### SharePolicy and DefaultSharePolicy

```go
type Owner struct {
    UID uint32
    GID uint32
}

// SharePolicy is the per-share half of every resolution and every create.
type SharePolicy struct {
    Symlink SymlinkPolicy

    // CrossMount allows traversal into a filesystem mounted inside the
    // share, which is ordinary: a RAID array under media/ or a second
    // disk under archive/ is a different device and users browse
    // straight into it.
    CrossMount bool

    // ModeFile and ModeDir are applied verbatim to what this server
    // creates, not filtered through umask.
    ModeFile uint32
    ModeDir  uint32

    // Chown is nil to leave what this process creates at the process uid
    // and gid.
    Chown *Owner
}

// DefaultSharePolicy is the restrictive one: no symlink is followed, and
// the modes are the group-writable pair a shared folder needs so the
// neighbours keep their access.
func DefaultSharePolicy() SharePolicy {
    return SharePolicy{
        Symlink:    SymlinkDeny,
        CrossMount: true,
        ModeFile:   0o664,
        ModeDir:    0o775,
    }
}
```

`SharePolicy` is carried on `ShareRoot` (unexported, as shown above) and
consumed at two points: `resolveFlags` (below, "Spec: resolve mechanics")
turns `Symlink` and `CrossMount` into `openat2` resolve flags for every
resolution against the root, and the create path (`WriteDurable`'s
sibling create calls, below) applies `ModeFile`/`ModeDir`/`Chown` verbatim
to what it creates, never filtered through the process umask.
`DefaultSharePolicy` denies symlinks and allows crossing into a nested
mount, which is the policy a share gets when nothing overrides it;
`core/03-share-registry.md` and `core/07-transfers.md` construct or read
a `SharePolicy` value at the points where a share is registered or a
transfer needs to know its symlink and ownership rules.

### RegisterShareRoot

```go
func RegisterShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, Admission, error)
```

The entry point a share registration calls. It opens `host` as an `O_PATH`
directory, statx's it for device, inode and birth-time support, statfs's it
for the filesystem magic, and runs admission (below) before returning a
usable root. A refused admission closes the anchor and returns the zero
`ShareRoot`; nothing is left half-registered.

`OpenShareRoot(id, host, policy) (*ShareRoot, error)` is the lower-level
constructor that opens without admitting, used internally and by tests that
need to probe a root's raw facts (`Dev`, `FsType`) before deciding whether
to admit it.

Registering is refusal-at-setup, deliberately, over accept-and-degrade:

> Refusing at registration is the decision. Accepting anything and
> degrading quietly produces a deployment that looks healthy and loses
> file identity months later, by which time the sync clients have already
> written the wrong thing into their own journals.

### OpenScratchRoot (upload phase amendment)

```go
func OpenScratchRoot(dir string, policy SharePolicy) (*ShareRoot, error)
func (r *ShareRoot) IsScratch() bool
```

A rooted handle over a directory that is not a share: server-owned scratch
space, which today means the upload spool (`../upload/03-cache-spool.md`).

It exists so that space does not have to borrow a share id. The code it
replaces opened the spool as share zero with a comment admitting the id was
a lie, which put a non-share into the share domain: every accessor keyed by
id could reach it, and every reader of an id had to know that one value
meant "not really".

The handle type is `*ShareRoot` so every safe-path method works unchanged,
and the admission gate is `RegisterShareRoot`'s: scratch space on a
filesystem this build cannot hold its contracts on is the same problem there
as anywhere. What is absent is the id and the share semantics that come with
it; `IsScratch` is what a caller asks instead of comparing against a
reserved id value.

### Admission

```go
type Admission struct {
    OK      bool
    Warn    string // set for an admitted filesystem carrying a caveat
    Reflink bool   // true when a copy on this filesystem can be by reference
}

var ErrUnsupportedFilesystem = errors.New("vfs: the filesystem is not supported")

type AdmissionError struct {
    Path   string // where the refused filesystem was found (not always the share root)
    Type   FsType
    Reason string
}
func (e *AdmissionError) Error() string
func (e *AdmissionError) Is(target error) bool // true for ErrUnsupportedFilesystem

func AdmitFsType(t FsType) (Admission, string)
func AdmitMount(path string, t FsType, hasBtime bool) (Admission, error)
```

Admission is an allow-list, not a refusal list. A filesystem type this
build has never classified refuses by falling through to the default case,
not by matching a "known bad" entry. This is the fail-closed half of the
design: a magic value nobody has reasoned about cannot become supported by
omission.

The verdict table, and the reasoning behind each entry:

| Filesystem | OK | Reflink | Warn | Refusal reason |
| --- | --- | --- | --- | --- |
| ext4, zfs, f2fs | yes | no | | |
| btrfs, xfs | yes | yes | | can copy by reference |
| tmpfs | yes | no | everything is lost on restart | |
| overlay | no | | | a container's writable layer has inodes that do not survive a restart, and it misses changes made to the layer beneath |
| fuse | no | | | identity cannot be proven to survive a restart or a remount |
| nfs | no | | | identity cannot be proven to survive a remount, and changes made elsewhere are not seen |
| cifs, smb2 | no | | | identity cannot be proven to survive a remount, and the remote name rules differ from this server's |
| squashfs | no | | | read-only, and this server has no read-only share contract |
| ntfs | no | | | this driver's identity, name and notification behavior are not established here |
| anything else | no | | | this server has no name for this filesystem, so its identity and notification behavior are unknown |

`AdmitMount` adds a second, independent gate on top of the type table: an
admitted type is still refused if the specific mount instance reports no
birth time (`hasBtime == false`). The reason is the identity scheme itself:
file identity in this product is derived from `(share, dev, ino, btime)`,
and without a birth time an inode reused after a deletion is
indistinguishable from the file that had it before. A type being on the
allow-list does not waive this; some ext4 mount options and NFS omit btime
even where the type itself is otherwise supported.

**Warning-with-caveat path.** tmpfs is the one entry that is `OK: true` and
carries a non-empty `Warn`. This is deliberate: an operator who wants a
scratch share on tmpfs is entitled to one, but the loss-on-reboot fact must
reach them, not be silently accepted. The caller of `RegisterShareRoot`
(the share-admin layer, outside this package) is responsible for
surfacing `Admission.Warn` to the operator at registration time; `vfs`
itself only classifies and returns the string.

**Nested mounts.** Admission at registration only covers the share root
itself. A mount placed below an admitted root (a second disk under
`archive/`, a container volume mounted into a share) is classified
separately, the first time resolution crosses into it, by
`admitDevice`/`admitResolved` inside `open.go`. The verdict is cached per
device so a mount's classification costs one statfs and one statx, not one
per resolution. `AdmissionError.Path` names the mount's own path in this
case, not the share root's, because that is what the operator has to go
look at. A share that opts out of crossing mount boundaries at all
(`SharePolicy.CrossMount == false`) needs no runtime check here: the
kernel's `RESOLVE_NO_XDEV` already refuses the crossing before any
classification code runs, which is the stronger guarantee.

### Alive

```go
func (r *ShareRoot) Alive() error
```

Reports whether the configured host path still names the directory this
root has open. This exists because a descriptor outlives the directory it
names: an unmounted share leaves a handle that still stats successfully,
still reports the original device and inode, and is otherwise
indistinguishable from a healthy root, precisely because holding the
descriptor keeps the underlying filesystem alive. Nothing about the handle
itself can reveal that the mount is gone.

`Alive` therefore does a *fresh* resolution of the configured host path
(via `unix.Statx` against the path string, not the anchor descriptor) and
compares device and inode against what `RegisterShareRoot` recorded:

- The path resolves to something that is not a directory: `ErrNotADirectory`
  (the target is a non-directory where a directory was expected).
- The path resolves to a directory whose (dev, ino) differs from the
  recorded pair: `ErrNotFound` (something else now occupies the name; the
  original tree is unreachable by the name the operator gave it).
- The path does not resolve at all: the mapped errno (typically
  `ErrNotFound`).
- Match: `nil`.

This is explicitly *not* a security decision. It decides whether to mark a
share broken for health reporting; it never decides what a request may
reach. Every operation still resolves exclusively through the anchor under
`openat2`, so a path swapped between one `Alive` probe and the next request
changes nothing about what that request can touch. `Alive` is a diagnostic
probe layered on top of an already-safe resolver, not a second resolver.

### Close

```go
func (r *ShareRoot) Close() error
```

Releases the anchor descriptor. A `ShareRoot` normally lives for the
process; `Close` exists for share deregistration and for tests. There is no
reference counting: a caller holding a `*File` opened through this root
after `Close` has a descriptor that still works (the kernel keeps an open
file alive independent of the directory descriptor used to reach it), but
no new resolution through this root is possible. The share-admin layer is
responsible for not handing out a deregistered root for new resolutions.

## Spec: path types and validation

Three deliberate omissions from this package, stated in its own package
doc comment and preserved verbatim as a design constraint:

> Three things are deliberately absent and each one is the second step
> that removes the guarantee: no path normalization, no descriptor cache,
> and no revalidation of a path that was already resolved. A resolver that
> rewrites "a/../b" into "b" has to be right about every encoding, every
> separator and every Unicode form, and being wrong once is an escape.
> Rejecting is right by not deciding.

### The three path types

```go
type Vpath struct{ /* unexported */ }     // "{share label}/{rest}", the wire form
type SharePath struct{ /* unexported */ } // share-relative, grant subpath already applied
type SafePath struct{ /* unexported */ }  // component-wise, validated, the only vocabulary vfs accepts
```

Each is a struct with an unexported field, never a named string type. A
named string type still converts with a cast that reviews clean; a struct
forces every crossing between vocabularies through a function that states
which direction it goes and what validation it applies. `Vpath` is the only
one of the three that ever appears in a request or a response; `SafePath`
is the only one any `ShareRoot` method accepts.

Crossings:

```go
func ParseVpath(s string) (Vpath, error)               // the wire trust boundary
func NewVpath(label string, rest SharePath) (Vpath, error)
func (p Vpath) IsRoot() bool
func (p Vpath) Name() string
func (p Vpath) Label() string
func (p Vpath) Rest() SharePath
func (p Vpath) String() string                          // "{share label}/{rest}", the wire spelling

func ParseSharePath(s string) (SharePath, error)
func (p SharePath) Safe() (SafePath, error)             // SharePath -> SafePath
func (p SharePath) IsRoot() bool                        // zero components, the share root itself

func ParseSafePath(s string) (SafePath, error)
func RootPath() SafePath                                // the share root, zero components
func (p SafePath) Components() []string                 // a defensive copy
func (p SafePath) Parent() SafePath
func (p SafePath) Name() string                         // last component, empty at the root
func (p SafePath) String() string                       // "/"-joined components
func (p SafePath) Join(name string) (SafePath, error)        // creation table
func (p SafePath) JoinExisting(name string) (SafePath, error) // existing-name table
func (p SafePath) JoinControl(name string) (SafePath, error)  // reserved-prefix table
func (p SafePath) HasPrefix(other SafePath) bool         // component-wise
func (p SafePath) Under(other SafePath) bool              // component-wise, p at or beneath other
func (p SafePath) Equal(other SafePath) bool
func (p SafePath) Share() SharePath                       // SafePath -> SharePath
```

Four of these exist purely because a core document calls them and this
package otherwise had no reason to expose them: `SafePath.Name()`
(`core/06-mutations.md`'s control-name check,
`vfs.IsReservedName(part.Name())`), `SafePath.String()`
(`core/07-transfers.md`'s operation item naming, `to.path.String()`),
`SharePath.IsRoot()` (`core/04-resolution.md`'s `joinSubpath`, deciding
whether to append `rest.Safe()`'s components at all), and `Vpath.String()`
(`core/11-homes-and-recent.md`'s recent-listing scope check, comparing a
journal row's rebuilt vpath against a scope prefix as a string). Each
follows the same pattern as its sibling methods on the type: `Name` mirrors
`Vpath.Name`, `String` mirrors the type's own parse function in reverse,
and `IsRoot` mirrors `Vpath.IsRoot`.

`ParseVpath` strips exactly one leading `/` before validating (a client's
own URL model is rooted; this package's is not, and `"documents/a.txt"` and
`"/documents/a.txt"` name the same virtual path). This is the only repair
the parser performs. What the leading slash could have meant instead, a
host path, is still refused: `/etc/passwd` parses to the share label
`"etc"`, which no grant names, and resolves to not-found exactly like any
other unknown label. The traversal table makes this safe independent of
what the label happens to spell.

`HasPrefix` and `Under` are component-wise comparisons, never string
prefix tests: `"ab"` is not under `"a"`. A string-prefix implementation
would let a sibling directory whose name shares a byte prefix with a
granted one be reached by a caller who only has the shorter path.

### The two validation tables

Two functions apply two different rule sets, and the difference between
them is a security decision, not a style choice:

```go
func validateExisting(name string) error   // JoinExisting, path parsing
func validateCreatable(name string, rejectReserved bool) error // Join (true), JoinControl (false)
```

**`validateExisting`** (used by `ParseVpath`, `ParseSharePath`,
`ParseSafePath`, `JoinExisting`): the traversal rules that are not about
taste, applied to a name that already exists on disk or is being
traversed toward one.

| Refused | Reason |
| --- | --- |
| empty component | `"a//b"` would silently become two components |
| `"."`, `".."` | resolving either is what creates the bypass; never resolved, only rejected |
| contains `/` | a component may not itself embed a separator |
| contains NUL | truncates the C string the kernel eventually sees |
| longer than `limits.NameBytes` (255) | cannot exist on any filesystem this runs on |
| carries a reserved prefix (`.sctrash`, `.scpart-`, `.scmeta`, `.scindex`) | belongs to this server's own control files |

The Windows-portability table is deliberately **not** applied here. A
directory literally named `CON` or `report:final`, written by someone
else's tool before this server ever touched the share, must still be
listable and resolvable. Applying portability rules to an existing name
made every such pre-existing entry list fine and then fail to open.

**`validateCreatable`** (used by `Join` with `rejectReserved=true`,
`JoinControl` with `rejectReserved=false`): everything `validateExisting`
checks (minus the reserved-prefix rule, applied separately below), plus
the creation table, so nothing this server mints is a name no Windows or
SMB client could ever open:

| Refused | Reason |
| --- | --- |
| any byte `<= 0x1F` or `== 0x7F` | control characters, refused only in a name we create |
| contains `:` | the NTFS alternate-data-stream separator |
| trailing `.` or trailing space | Windows silently reinterprets both |
| a Windows device name, case-insensitive, extension ignored: `CON`, `PRN`, `AUX`, `NUL`, `COM1`-`COM9`, `LPT1`-`LPT9` | unopenable by a Windows or SMB client, `"con.txt"` included |
| carries a reserved prefix | only when `rejectReserved` is true |

`Join` calls this with `rejectReserved=true`: it is the general "this
server is minting a name" path, and user input must never produce a
reserved prefix. `JoinControl` calls it with `rejectReserved=false`: it is
the **only** function permitted to produce a name carrying the reserved
prefix, and it exists because an earlier revision had no such function, so
a caller needing a part file inside a share subdirectory disguised the
name to slip past validation, and the disguise defeated the reserved-name
filter, putting part files into every listing (web UI and WebDAV) for the
duration of every upload. `JoinControl` is unreachable from any parser that
accepts client input, so the reserved prefix stays exclusively this
server's.

`RefusedNames() []string` and `RefusedNameCharacters() []string` are the
advertisable subset of the creation table, exported so a protocol layer
that tells a client "these names are refused" derives the list from the
same table that enforces it rather than maintaining a second copy that can
drift. A name advertised as legal and then refused makes a sync client
retry forever without converging.

### Reserved and control names

```go
func IsReservedName(name string) bool
```

True for any name carrying one of `.sctrash`, `.scpart-`, `.scmeta`,
`.scindex` as a prefix. Exported because the directory listing, the path
parser, and the SMB veto-files configuration all must agree on the same
table; two independently maintained copies drift.

`stagingName() (string, error)` mints a `.scpart-` name with eight random
bytes from `crypto/rand`, hex-encoded. The randomness is what keeps two
concurrent writes to the same destination from picking the same staging
name; `O_EXCL` on the create turns any collision that does happen into a
refusal, never a clobber.

```go
func IsStagingName(name string) bool
```

Reports a name this package produced for a write in flight, so an external
sweep (the upload orphan collector) can distinguish "our staging file" from
an ordinary name that happens to start with a dot.

### Bounds

Every path operation is bounded before it does real work:

- `checkPathSize`: the whole path string, `limits.PathBytes` (4 KiB),
  checked before splitting, so a hostile length costs one comparison
  rather than one allocation per component.
- `checkComponentCount`: `limits.PathComponents` (256), checked after
  counting separators and again on every `push` (the shared implementation
  behind `Join`/`JoinExisting`/`JoinControl`), so a path built up one
  component at a time cannot exceed the bound by accretion.
- `validateExisting`'s per-component length check: `limits.NameBytes`
  (255).

### Normalization (norm.go)

Unicode handling is confined to two narrow, explicitly justified functions,
never a general path rewrite:

```go
func normalizeNewName(name string) string        // NFC, applied only to a brand-new name
func lookupCandidates(name string) []string       // {given, NFC, NFD}, deduplicated, order-preserving
func pathCandidates(comps []string) []string      // the same, applied uniformly across a whole path
```

`normalizeNewName` puts a name this server is about to create into NFC.
It is never applied to an existing name: a name another program wrote is
found as written, and silently renaming it to a different normal form
would break any external index (a sync client's own database, an SMB
client's cache) that recorded the original spelling.

`lookupCandidates` produces the spellings to try, in order, for a name
that might already exist: as given, then NFC, then NFD. This exists
because macOS SMB and AFP clients write filenames in NFD, and a later
lookup for the same user-visible name, spelled in NFC by whatever asked
for it, must still find the file. The list is deduplicated so the ordinary
all-ASCII case costs exactly one candidate.

`pathCandidates` does the same across every component of a whole path in
one pass, applying one normal form uniformly rather than per-component
(three candidates total, not three to the power of the depth). This is a
documented approximation: a path whose components were written by
different clients using different normal forms falls outside it, accepted
as a cost worth avoiding on every single resolution, since it is not the
case that actually happens (one non-conforming client tends to write a
whole tree consistently).

Every multi-candidate lookup (`resolveDir`, `openLeafNamed`, `Rename`'s
source lookup, `SetTimes`, `Unlink`/`Rmdir`) tries each candidate in this
order through the *same* `openat2` call shape, treating `ENOENT`/`ENOTDIR`
as "try the next spelling" and any other errno as an immediate, real
failure. A permission error on the first candidate must never be masked by
trying a second spelling: `isMissing(err)` is the one function deciding
which errnos continue the loop, and only two are in it.

## Spec: resolve mechanics

### The one call that opens anything

```go
func openat2(dir *os.File, path string, flags uint64, mode uint64, resolve uint64) (*os.File, error)
```

Every open in this package funnels through this one function, which is a
thin wrapper over `unix.Openat2`. There is no second code path that opens
a file by any other means.

### Resolve flags

```go
func resolveFlags(p SharePolicy) uint64
```

```
f := RESOLVE_NO_MAGICLINKS                       // always
switch p.Symlink {
case SymlinkDeny:         f |= RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS
case SymlinkWithinShare:  f |= RESOLVE_IN_ROOT
case SymlinkFollow:       f |= RESOLVE_BENEATH
}
if !p.CrossMount { f |= RESOLVE_NO_XDEV }
```

`RESOLVE_NO_MAGICLINKS` is unconditional and not policy-selectable: it is
what blocks escape through `/proc/self/fd/*`-style magic links, which are
not ordinary symlinks and are not covered by the symlink policy at all
(the escape test matrix's magic-link case exercises exactly this).

### Symlink policy

```go
type SymlinkPolicy uint8
const (
    SymlinkDeny SymlinkPolicy = iota // default: visible in a listing, cannot be opened or traversed
    SymlinkWithinShare               // followed if the resolved target stays inside the share root
    SymlinkFollow                    // a relative target under RESOLVE_BENEATH; an absolute target still fails
)
func ParseSymlinkPolicy(s string) (SymlinkPolicy, error) // "deny" | "within_share" | "follow"
func (p SymlinkPolicy) String() string
```

`ParseSymlinkPolicy` is a trust boundary for the configured value: an
unrecognized string is a refusal, never a silent fallback to `deny`. An
operator who configured `"within_share"` and got a silent `"deny"` believes
a share follows symlinks it does not, and the mismatch is invisible until
somebody's link fails to open with no diagnostic pointing at the cause.

`SymlinkWithinShare` uses `RESOLVE_IN_ROOT`, which rebases an absolute
symlink target against the share root rather than refusing it: a link to
`/secret.txt` under this policy reads the share's own `secret.txt`, not
whatever `/secret.txt` names on the host. `SymlinkFollow` uses
`RESOLVE_BENEATH` alone, which follows a relative target that stays beneath
the anchor and still refuses an absolute one outright (no rebasing).

### Why every operation re-resolves from the root handle

There is no descriptor cache and no "resolve once, reuse the handle for
several operations on paths under it" shortcut anywhere in this package,
with one narrow, explicit exception: `WriteDurable` and `PublishPart` open
the destination's *parent* directory once and perform both the staging
create and the publishing rename through that single descriptor, because
that is what makes the rename atomic (same directory, same descriptor,
kernel guarantee). Every other operation, including two operations on the
same nominal path issued moments apart, resolves fresh from the anchor.

This is deliberate, not an oversight: `resolveDir`'s own comment states the
principle plainly:

> There is no normalize, then check, then open: between a check and an
> open a component can become a symlink, and there is no window here
> because there is no second step.

A cache of "path X resolves to descriptor Y" is exactly the kind of second
step that reopens the TOCTOU window `openat2` closes: the filesystem is
shared with other software, so a component that was a directory when it
was cached can be a symlink by the time the cache is consulted. Resolving
fresh, one `openat2` call for the whole component chain, every time, is
what keeps the guarantee atomic rather than merely usually-true.

`resolveDir(comps []string) (*os.File, error)` resolves an entire component
chain to an `O_PATH` directory descriptor in one `openat2` call per Unicode
candidate, so the kernel enforces the resolve flags across every
intermediate component atomically. `openLeafNamed`/`openLeaf` resolve the
parent chain this way and then perform one more `openat2` hop for the leaf,
because the leaf's own symlink-ness must be gated by the same policy and
because a leaf written in a different normal form than its parent is the
ordinary case macOS clients produce.

## Spec: stat and types

### Stat

```go
type Stat struct {
    Dev, Ino uint64
    BtimeNs  *int64 // nil where the filesystem carries no birth time
    MtimeNs  int64
    CtimeNs  *int64 // nil where the mask did not report it; moves on rename, unlike mtime
    Size     uint64
    Mode     uint32
    UID, GID uint32
    Nlink    uint32
    Kind     Kind
}
```

`BtimeNs` and `CtimeNs` are pointers specifically so "the filesystem does
not report this" and "the filesystem reports zero" stay distinguishable
facts; both are read off the `statx` returned mask (`STATX_BTIME`,
`STATX_CTIME`) rather than assumed present. This matters downstream: the
cache layer's identity tuple keys off `Btime` as a pointer for the same
reason (audit: foundation-persistence, `store/cache` finding 1), and a
compat layer reporting "unknown" versus "1970" to a client needs the
difference. `CtimeNs` reports when an entry was last moved (a rename), not
when its content changed; a file moved into a trash directory keeps its
own `MtimeNs` untouched while `CtimeNs` moves, which is what lets a caller
tell "content changed" apart from "was relocated."

Timestamps are computed with `timestampNs(sec, nsec)`, which **saturates**
on overflow rather than wrapping, because the seconds field comes from a
filesystem another program wrote and a value wide enough to overflow a
`int64` nanosecond multiply is untrusted input, not an impossibility to
assume away.

### Kind

```go
type Kind uint8
const (
    KindOther Kind = iota // also what a directory read reports when getdents64 gave no type, rather than paying a statx per entry
    KindFile
    KindDir
    KindSymlink
)
func (k Kind) IsDir() bool // == KindDir, not "can be traversed"
```

`IsDir` is a strict equality check, not a traversability check: a symlink
to a directory answers `false`, because under the default `SymlinkDeny`
policy it cannot be entered at all, and a caller asking "is this a
directory" is asking whether it can walk into it, not what the target of
an unopenable link happens to be.

### DirEntry

```go
type DirEntry struct {
    Name string
    Kind Kind
    Ino  uint64 // free from getdents64; lets a caller order a later stat batch by (dev, ino)
}
```

### HideReserved

```go
type ReservedPolicy uint8
const (
    HideReserved ReservedPolicy = iota // every user-facing read
    IncludeReserved                    // trusted maintenance only: upload orphan sweep, trash collector
)
```

`ReservedPolicy` is an explicit argument on every directory read, never
ambient package state. A caller that does not say what it is asking for
would inherit whatever the previous caller wanted, and what a directory
read returns is itself a security-relevant answer (it decides whether a
part file in progress is visible to a listing). `IncludeReserved` is never
passed on a request path; only the two trusted maintenance sweeps use it.

## Spec: read side

### OpenRead and AccessIntent

```go
type AccessIntent uint8
const (
    IntentRead      AccessIntent = iota // O_RDONLY; every read except the one exception below
    IntentReadWrite                     // O_RDWR; the upload engine's part-file handle alone
)
func (r *ShareRoot) OpenRead(p SafePath, intent AccessIntent) (*File, error)
```

There is no fallback chain: `OpenRead` does not try `O_RDWR` and fall back
to `O_RDONLY` on failure, and it does not try `O_RDONLY` first and widen
later. An `O_RDONLY` open does not fail with `EACCES` on a file that is
merely readable, so nothing needs a widening fallback, and a fallback
chain is exactly what previously made every plain read of a mode-644 file
hold a handle capable of writing. `IntentReadWrite` exists for exactly one
caller (the upload finalizer, which writes chunks and then reads the whole
file back to verify a digest); a build-time grep for a second call site is
part of the package's own acceptance discipline.

### ReadDir and its bound

```go
func (r *ShareRoot) ReadDirFunc(p SafePath, policy ReservedPolicy, fn func(DirEntry) bool) error
func (r *ShareRoot) ReadDir(p SafePath, policy ReservedPolicy) ([]DirEntry, error)
```

`ReadDirFunc` streams via raw `getdents64`, parsed directly against the
kernel ABI (fixed offsets for `d_ino`, `d_reclen`, `d_type`, `d_name`)
rather than through a library that returns names only, because `d_type`
avoids a `statx` per entry and `d_ino` lets a caller order a later stat
batch to make the disk seek forward. It never materializes the whole
directory: the product's premise is that other programs write these
directories, so their size is never this server's to assume, and an
unbounded materialization is bounded only by available memory the moment
something drives a walk without a cap.

`ReadDir` is the bounded convenience wrapper: it refuses past
`limits.DirEntriesBuffered` (100,000) with `limits.Exceed`, rather than
truncating silently. A caller that cannot tolerate the refusal (a
recursive size walk, a search indexer) uses `ReadDirFunc` and streams.

Every dirent parse fails closed on a malformed record: a `d_reclen` that
claims more bytes than the buffer holds, or that is too short to contain a
header, is an immediate error, not a best-effort skip. This is the trust
boundary for a kernel-supplied buffer that this package still parses
defensively.

### Space

```go
type FsSpace struct {
    Total, Free, Available uint64 // Available is f_bavail
}
func (r *ShareRoot) Space(p SafePath) (FsSpace, error)
func (f *File) Space() (FsSpace, error) // agrees with Space, from an already-open handle
```

`Space` answers for the filesystem holding `p`, not the filesystem holding
the share root: a share is one directory tree, not one filesystem, and a
RAID array mounted under `media/` inside a share must report the RAID
array's own numbers, not the root disk's borrowed ones.

**The `f_bavail` rule**: `Available` is always `statfs`'s `f_bavail`
(blocks available to an unprivileged writer), never `f_bfree` (blocks free
including the filesystem's root reserve). The reserved blocks in the
difference between the two are not this server's to write into; reporting
`f_bfree` as available promises an uploader room that a subsequent write
then refuses with `ENOSPC`. `FsSpace.Used()` follows `df`'s convention and
counts the reserve as used.

Byte math saturates (`saturatingMul`) rather than overflowing: block count
times block size is computed with an explicit overflow check against
`math.MaxUint64`.

## Spec: write side

### Mkdir

```go
func (r *ShareRoot) Mkdir(p SafePath) error
```

Creates the directory with the share's configured mode applied verbatim
(never filtered through the process umask), by reopening the just-created
entry through a descriptor and calling `Fchmod`/`Fchown` on that
descriptor rather than by name. Reopening by descriptor, not by a second
name lookup, is deliberate: a second lookup by name is a window in which
the name could be something else by the time it resolves. A failure to
apply mode or ownership is returned, never logged-and-continued: the
product's premise is that the folder is not this server's own, so a
directory whose configured ownership silently failed to apply is exactly
the failure that makes a media server or a backup script stop seeing files
with no error anywhere in this process's own request path.

### Rename

```go
func (r *ShareRoot) Rename(from, to SafePath, noReplace bool) error
```

`noReplace` maps to `RENAME_NOREPLACE` on `renameat2`; without it the
destination is replaced atomically by the kernel. **The
`RENAME_NOREPLACE` rule**: when the flag set is non-empty and `renameat2`
is unavailable (`ENOSYS`), there is no fallback to plain `renameat`,
because plain `renameat` cannot honor `RENAME_NOREPLACE` and silently
dropping the flag would turn a caller's explicit "refuse to clobber" into
a clobber. The fallback to plain `renameat` fires only when the flag set
was already empty, i.e. the caller never asked for no-replace semantics in
the first place.

The destination spelling (`destinationSpelling`) is resolved before the
rename: if a name matching one of the Unicode candidates already exists,
the rename targets that exact on-disk spelling; only when nothing exists
under any candidate is the new name normalized to NFC. Normalizing
unconditionally would be a data-duplication bug, not a tidy-up: replacing
a destination another client wrote in NFD by renaming onto its NFC
spelling creates a second file with the requested name, leaving the one
the caller meant to replace untouched.

Crossing a mount boundary within one share (a rename between a subtree on
the share root's device and a subtree mounted below it) surfaces as
`ErrCrossDevice`, an expected outcome the caller (`core`) falls back to a
copy-then-delete for, never a bug in this package.

### Unlink and Rmdir

```go
func (r *ShareRoot) Unlink(p SafePath) error // non-directory leaf
func (r *ShareRoot) Rmdir(p SafePath) error  // empty directory only
```

Both resolve the parent once, then try each Unicode candidate spelling of
the leaf under one shared `unlinkAt` helper, exactly the same
candidate-loop discipline as every other named operation.

### WriteDurable

```go
type DurableOpts struct {
    Mode      uint32 // applied verbatim via fchmod, never through umask
    Owner     *Owner // applied to a newly created file only; nil leaves process uid/gid
    NoClobber bool   // RENAME_NOREPLACE
}
type Durable struct {
    Replaced     bool
    OwnerRestore error // non-nil: uid/gid could not be fully restored (EPERM is ordinary, unprivileged)
}
func (r *ShareRoot) WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) (Durable, error)
```

`WriteDurable` is the only function in the package that renames a
newly-written file into place, and every mutation of share content goes
through it. The exact sequence, every step load-bearing:

1. Mint a staging name via `stagingName()`, joined onto the destination's
   parent through `JoinControl` (the only path this reserved prefix can
   reach).
2. Open the destination's *parent* once, for reading (not `O_PATH`,
   because step 7 below fsyncs it and `fsync` on an `O_PATH` descriptor is
   `EBADF`). This descriptor is used for both the staging create and the
   publishing rename, which is what makes the rename atomic: both names
   are relative to the same open directory.
3. Stat the entry currently at the destination name, if any, through this
   same parent descriptor, *before* anything is staged. If `NoClobber` is
   set and something is already there, refuse immediately with `ErrExists`
   (a cheap early refusal; `RENAME_NOREPLACE` at the end is still the
   authority against a race, this only avoids staging a whole file to
   discover a collision that already existed).
4. Create the staging file `O_CREAT|O_EXCL|O_RDWR|O_NOFOLLOW`, mode from
   `opt.Mode` (`O_CREAT`'s own umask-filtered mode is discarded).
5. **Mode via fchmod, not the open's own mode bits.** `handle.SetMode`
   calls `Fchmod` on the open descriptor, applying `opt.Mode` exactly,
   because `O_CREAT`'s mode argument is filtered through the process
   umask and a key or credential file created at an unintended, wider mode
   is exactly the failure this exists to prevent.
6. Apply `opt.Owner` if set and the file is new (not replacing).
7. Invoke the caller's `write(handle)` callback.
8. `handle.SyncData()` (`fdatasync`): the content is durable before
   anything makes the name findable.
9. If replacing an existing entry, transplant its mode and ownership onto
   the staged file *before* the rename (mode failure fails the write;
   ownership failure is reported via `Durable.OwnerRestore` but does not
   fail the write, since `EPERM` here is the ordinary unprivileged
   outcome).
10. Rename the staging name onto the destination name, through the one
    held parent descriptor, with `RENAME_NOREPLACE` if `opt.NoClobber`.
11. `fsync` the parent directory. This is the step that makes the *name*
    durable, separate from step 8 making the *content* durable: without
    it, on ext4 or XFS, a power cut can leave the complete content sitting
    under the unlisted staging name with nothing pointed at it.

On any failure before the rename, the staging file is unlinked; a failure
to unlink it is logged, not returned, and the original write error is
still what the caller sees. This is a deliberate design, not a swallowed
error on the write path: an unlistable orphan under a `.scpart-` name is
picked up by the upload sweep later, and failing the caller's already-
failed write a second time over a cleanup detail would tell them nothing
new.

### CreatePart and SetTimes (upload phase amendment)

```go
func (r *ShareRoot) CreatePart(p SafePath) (*File, error)
func (r *ShareRoot) SetTimes(p SafePath, mtimeNs int64) error
```

The two operations the upload engine needs and no other caller does.

`CreatePart` is the part file bytes accumulate in. The handle is read-write
by construction rather than through `AccessIntent`, for the same reason
`WriteDurable`'s staging file is: a file this server just created is
writable because it made it, not because a caller asked for privilege on a
path that already existed. The create is `O_EXCL`, so a collision is the
kernel's refusal rather than a clobber, and the mode is applied by
descriptor afterwards because `O_CREAT` filters it through umask. The name
must come from `JoinControl`: a part file without the reserved prefix is
refused here rather than quietly creating something a listing would show,
which is the same defect the reserved-name rule above exists for.

`SetTimes` applies the modification time a client asked its finished file to
carry, and leaves the access time alone. The symlink is never followed: the
timestamp belongs to the entry named, not to whatever it points at. The
second and nanosecond split is written out rather than left to truncating
division, because a timestamp before the epoch has a negative remainder and
would otherwise land a second in the future.

### PublishPart

```go
func (r *ShareRoot) PublishPart(part, dest SafePath, replacing bool) (Durable, error)
```

`WriteDurable`'s second half without its first: for content that is
*already* complete and synced (the upload engine's part file, written and
`fsync`'d possibly over hours), staging it again would mean copying a
whole file beside itself for no reason. `PublishPart` does the rename half
alone.

**Same-directory rule, enforced, not trusted**: `part.Parent()` must equal
`dest.Parent()`, refused with `ErrDenied` otherwise. This is what makes
the rename atomic; the caller is checked, not assumed to have gotten it
right.

The sequence: open `dest`'s parent once; stat what currently occupies
`dest`'s name through that descriptor; if occupied and `!replacing`,
refuse with `ErrExists`; if occupied and `replacing`, reopen the part file
by descriptor (not a second name lookup) and transplant the prior entry's
mode (failure fails the call) and ownership (failure is reported via
`Durable.OwnerRestore`, not fatal) onto it *before* the rename;
`RENAME_NOREPLACE` when nothing was occupying the name, plain rename when
replacing; `fsync` the parent directory afterward, for the same reason
`WriteDurable`'s last step exists: the caller already synced the content,
and without this fsync a power cut can leave a complete upload sitting
under the part name with nothing pointed at it.

### CopyRange

```go
func CopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error)
```

**Reflink then in-kernel copy fallback**: implemented entirely on
`copy_file_range`, which on btrfs and XFS performs a reflink (a metadata
copy, not a byte copy) when the ranges are suitably aligned, and an
in-kernel byte copy otherwise, with no userspace round trip in either case.
`copy_file_range` is documented to perform a short copy even on success, so
the function loops until `n` bytes are moved or the source is exhausted.

On `EXDEV` (crossing a mount inside one share, an ordinary outcome since a
share is a tree and not one filesystem), `EOPNOTSUPP`, or `ENOSYS`, the
function falls back to `bufferedCopyRange`, the package's one userspace
copy loop, bounded to `copyBufBytes` (256 KiB) per iteration so no amount
of file content is ever fully materialized in memory regardless of what
the caller asked to copy. Any other errno from `copy_file_range` is a real
failure and is returned, never silently retried through the fallback.

Every offset passed to the underlying syscalls is explicit
(`&in`, `&out`), never `nil`: this package reads and writes positionally
throughout, and letting either syscall advance a descriptor's own cursor
would move that cursor under whatever the next caller of the same handle
expects to find.

## Spec: the errno mapping

```go
func mapErrno(op string, err error) error
```

The single canonical mapping from a raw `unix.Errno` to this package's
sentinel set, applied at the boundary of every syscall wrapper (no
sentinel is ever constructed by hand elsewhere in the package):

| errno | sentinel |
| --- | --- |
| `ENOENT`, `ENOTDIR` | `ErrNotFound` |
| `EACCES`, `EPERM` | `ErrDenied` |
| `EEXIST` | `ErrExists` |
| `ENOTEMPTY` | `ErrNotEmpty` |
| `ENOSPC` | `ErrNoSpace` |
| `EXDEV` | `ErrCrossDevice` |
| `ELOOP` | `ErrSymlinkDenied` |
| `EISDIR` | `ErrIsDirectory` |
| anything else | wrapped as-is (`errors.As` still finds the raw errno) |

`ErrIsDirectory` is its own sentinel, distinct from `ErrNotADirectory`
(produced directly by `Alive`, above, and by `ENOTDIR` where this table's
first row maps it into `ErrNotFound` instead, per the existence rule):
the two carry opposite meanings and a caller cannot branch correctly on
one sentinel standing for both. `ErrNotADirectory` is the target being a
non-directory where a directory was expected; `ErrIsDirectory` is the
target being a directory where a file was expected (`EISDIR`, from a
caller that opened a directory path expecting to stream or write file
content). Before this document, `EISDIR` mapped to `ErrNotADirectory`,
the wrong sentinel for what the errno means; the core maps the two
sentinels to opposite answers of its own, not the same one:
`ErrNotADirectory` to `ErrNotFound` (listing a non-directory reports the
path as not listable), `ErrIsDirectory` to `ErrDenied` (streaming or
reading a directory is a refusal), per `core/01-errors.md`'s `mapVFSErr`
table.

`ErrSymlinkDenied` is a distinct sentinel from `ErrNotFound`, not a
folding of the two, and the distinction is load-bearing: it is how a share
under `SymlinkDeny` reports "a symlink was here and refused" as a
different fact from "nothing was here," and only the layer above (which
knows the caller's grants) is positioned to decide which of the two facts
a given caller is allowed to learn. Folding them at this layer would
remove information the caller above cannot recover.

`isMissing(err) bool` is `errors.Is(err, ENOENT) || errors.Is(err, ENOTDIR)`,
the exact and only set of errnos a Unicode-candidate loop continues past.
Every other errno stops the loop immediately: collapsing that distinction
would make a permission error on the first candidate spelling look like a
missing file once the loop tried the second spelling.

Two service-layer packages (`core`, `upload`) independently re-map this
package's sentinels into their own vocabularies (audit: vfs finding 9).
This is expected layering, not a defect, but it means the sentinel set here
must stay small and stable: every addition is a contract two other
packages must be told to handle by hand, since nothing enforces the
mapping's completeness across packages at compile time.

## Rationale

The package's design is one continuous argument: confinement is a kernel
guarantee applied atomically at resolution time, never a userspace check
applied before it. Every section above traces back to this:

- Path types exist so nothing reaches a syscall without having been
  through exactly one validated vocabulary.
- Resolution is one `openat2` call per component chain because a second
  step (check, then open) is a window a concurrent filesystem write can
  land in.
- Admission exists because confinement alone does not make a filesystem's
  identity or notification guarantees hold; a share on the wrong
  filesystem type loses those guarantees regardless of how carefully every
  path is resolved.
- Durable write exists because confinement answers "can this touch that
  path," not "does a crash mid-write leave something coherent there";
  those are different guarantees and this package is where both live,
  because both gate what a caller may find on disk afterward.

## Deliberate changes

Two intruders leave the package, per the survey (01-package-survey.md) and
the audit (foundation-persistence.md, vfs findings 1-4):

1. **`ReplaceFileDurable` moves to `store/fsatomic`; `PublishNew` is
   dropped (no caller), per `foundation/fsatomic.md`.** `ReplaceFileDurable`
   takes a plain `string` path, never a `ShareRoot` or a `SafePath`, and
   opens its target with `unix.Open`/`os.Stat` rather than this package's
   `openat2` resolver (audit: vfs finding 1). It is file persistence for a
   control file this server itself wrote (a key ring, a passdb sidecar, an
   index snapshot), not filesystem security for share content, and after
   the move `vfs` no longer exports any function that accepts a bare
   string path. `PublishNew` has no verified call site anywhere in the
   current tree (audit: vfs finding 2); `foundation/fsatomic.md` makes the
   keep-or-drop decision explicit and drops it rather than moving it.

2. **`seqpacket.go` (`SocketPair`, `SendMessage`, `RecvMessage`) and
   `file.go`'s `SendJob`/`OSFile` move to the preview phase's worker
   transport package**, not into `engine/infra/vfs`. This is `SCM_RIGHTS`
   descriptor-passing IPC for the preview worker process; nothing in it
   touches a share root, a validated path, admission, or a durable write
   (audit: vfs finding 4). Its only callers today are `preview/pool.go`
   and `preview/worker/worker.go`, both preview-owned. The package's own
   stated justification for holding this code, "a raw descriptor must not
   leave this package" (because `(*os.File).Fd` takes a descriptor out of
   the Go runtime's view for the syscall's duration, and a finalizer could
   then close it underneath the call), is a real constraint on the
   `*os.File` type, but it is a constraint the preview worker transport
   package can hold for itself; it is not a reason this specific socket
   wiring belongs in the filesystem-security package. It moves out in the
   preview phase (`preview/00-overview.md` through `02-worker-protocol.md`
   per the document plan), documented there with the same keepalive
   discipline (`withFd`/`withFd2`, `runtime.KeepAlive`) this document
   requires `vfs` to keep for every descriptor it still owns.

After both moves, **every function `engine/infra/vfs` exports takes either a
`*ShareRoot` receiver or one of the three path types as an argument**, with
the sole documented exception of the path-type constructors themselves
(`ParseVpath`, `ParseSharePath`, `ParseSafePath`, which take the raw string
a caller is validating) and the pure classification helpers
(`AdmitFsType`, `ParseSymlinkPolicy`, `RequireResolver`) that take neither
a path nor a share and produce no filesystem effect. This is the
enforceable form of "vfs is the only door to disk, and every door has a
lock the type system holds": a `grep` for a function accepting a bare
`string` as a filesystem-bound argument, after the move, finds nothing.

No other behavioral change. `caps.go`'s runtime probe, `require.go`'s
startup refusal, `admit.go`'s allow-list, `path.go`'s two validation
tables, `norm.go`'s Unicode handling, `open.go`'s resolve mechanics,
`durable.go`'s durable-write and publish-part sequence, and `errno.go`'s
mapping table carry forward as-is; the audit found nothing in any of them
that weakens the guarantees (audit: vfs, "Rebuild notes").

The one naming duplication the audit records but does not require fixing
here (audit: vfs finding 8) is noted for cross-reference only: the
`.scpart-` staging-name *convention* is shared by reference across `vfs`,
`upload`, and `smb`, but each package still needs its own name-generator
tied to its own id scheme (a random suffix here, an upload-session id
there). This document records the convention once; it does not centralize
the generators, because the id spaces they draw from are not the same
concept.

3. **Three operations arrive with the phase that needs them**
   (`../upload/03-cache-spool.md`). `OpenScratchRoot` ends the fake share
   id, and `CreatePart` and `SetTimes` are the part-file create and the
   client modification time. Each is specified in its own section above
   rather than left for its caller to improvise, because all three touch
   this package's own rules: the admission gate, the reserved-name rule,
   and the do-not-follow-a-symlink rule.

## Tests

The package's own claim is that the kernel confines a share, not that this
code's checks do, so every escape-adjacent test asserts the *specific
errno-derived sentinel* the kernel produced, never merely "the operation
failed." A test that passes for the wrong reason (a string check happening
to also refuse the input) is worse than no test, because it stops
detecting a regression the moment the kernel-level enforcement breaks
while a coincidental string check keeps passing.

### Escape and traversal (escape_test.go)

1. **Rejected before any syscall.** `".."`, `"a/../.."`,
   `"../etc/passwd"`, `"/etc/passwd"`, `"//etc"` all refuse at
   `ParseSafePath` with `ErrInvalidName`, proving the traversal table
   catches these without ever reaching the resolver.
2. **Symlink out of the share, `SymlinkDeny`.** A symlink inside the share
   pointing at a file outside it: reading it answers `ErrSymlinkDenied`
   (from `ELOOP`), not `ErrNotFound`.
3. **Symlink out of the share, `SymlinkWithinShare`.** The same symlink
   under this policy rebases the absolute target against the share root
   and answers `ErrNotFound` (nothing exists at the rebased path inside
   the share); the outside content is never read.
4. **`SymlinkWithinShare` rebases rather than escaping, positive case.** A
   symlink to `/secret.txt` under `SymlinkWithinShare` reads the share's
   own `secret.txt`, proving the rebasing (not merely the refusal) is
   correct.
5. **Traversal through a symlinked intermediate directory.** The same two
   policy outcomes (3, 4) reproduced for a symlink standing in for a
   directory component, not just a leaf.
6. **Magic link, with both controls.** Against `/proc/self/fd/N` (a magic
   link, not an ordinary symlink), under `SymlinkFollow` (which would
   follow an ordinary relative symlink): the open answers
   `ErrSymlinkDenied` from `ELOOP`. Two controls make this a proof rather
   than an observation: with no resolve flags at all, the same open
   succeeds and reads the real content (proving the target is genuinely
   reachable and the refusal is not accidental); with `RESOLVE_BENEATH`
   alone (no `RESOLVE_NO_MAGICLINKS`), the open still refuses but with a
   *different* errno (`EXDEV`), proving the `ELOOP` in the real case is
   specifically `RESOLVE_NO_MAGICLINKS`'s doing and not `RESOLVE_BENEATH`'s.
7. **A component swapped between two calls (rename-swap race).** Resolve
   `a/b`, hold the descriptor, remove `a/b`'s contents and the directory
   itself out from under the held descriptor, replace `a/b`'s name with a
   symlink pointing outside the share. Assert two things: the *already
   held* descriptor still stats to its original (dev, ino), unaffected by
   the swap (a resolved descriptor names what it resolved to, not what the
   name currently spells); and a *fresh* resolution of the same path now
   answers `ErrSymlinkDenied`, refusing the new symlink rather than
   silently following it. This is the direct test of "there is no window
   to race": nothing in between the two calls could have been raced,
   because there is no in-between step.
8. **Mount boundary refused inside a share, real mount required.** In a
   re-executed child under a private user and mount namespace (so the test
   suite does not require host root), mount a real filesystem below an
   admitted share root with `CrossMount: false`. Both `Stat` and
   `ReadDir` on the mounted path answer `ErrCrossDevice`, before anything
   under the mount is exposed. With `CrossMount: true` on the same layout,
   the same path resolves and stats normally. The parent test skips
   (never silently passes) when the host disallows unprivileged user or
   mount namespaces, distinguished from an actual test failure by an
   explicit sentinel line the child prints only after successfully
   building and testing the boundary.
9. **Unicode: the other normal form is found, not refused.** A file
   written under one Unicode normal form is found by a lookup spelled in
   the other; this is the candidate loop functioning as intended, listed
   here because it sits directly beside the escape suite and a regression
   that turned it into a refusal would look like a hardening change.
10. **Only one on-disk spelling is found by both.** A file present under
    only its NFD spelling is found by both an NFD and an NFC lookup, the
    complementary case to 9.

### Nested mount, unsupported filesystem (nestedmount_linux_test.go)

11. **An unsupported filesystem mounted below an admitted root fails
    closed.** In a re-executed child under a private namespace, mount a
    filesystem type this build has no name for (e.g. `ramfs`) below an
    admitted, `CrossMount: true` share root. Both `Stat` and `ReadDir`
    into the nested mount answer `ErrUnsupportedFilesystem`, and the
    error's message names the *mount's own path*, not the share root's,
    so an operator investigating knows which directory to look at. The
    share root itself remains registered and usable outside the nested
    path.

### Replace and durable write (replace_linux_test.go, replace_test.go)

12. **Exact mode despite a hostile umask.** With the process umask set to
    refuse every bit (`0o777`), a durable write with `Mode: 0o600`
    produces a file at exactly `0o600`, proving mode is applied via
    `fchmod` on the open descriptor and never left to `O_CREAT`'s own
    umask-filtered mode.
13. **A failing writer leaves the prior content untouched.** A write
    callback that writes partial content and then returns an error leaves
    the original file's bytes exactly as they were; no staging file
    survives in the directory afterward (`IsStagingName` sweep over the
    directory listing finds nothing).
14. **A successful replace changes the content atomically.** No reader
    holding a descriptor opened before the replace observes a partial
    write; the replace is a single atomic rename.

### Space (space_linux_test.go)

15. **A file handle's `Space()` agrees with the path-based `Space()`** for
    the same file, proving the two accounting paths cannot silently
    disagree.
16. **`DirDev` answers for the directory asked about**, not the share
    root: the root's own device, a file's parent's device (same as root
    absent a nested mount), and an error (not a wrong answer) for a path
    whose parent does not exist.

### Fuzzing (fuzz_test.go)

17. **`FuzzParseVpath`**: the property under fuzz is not "does not crash."
    For every input the parser accepts, the output string equals the
    input with at most one leading slash stripped (the parser repairs
    nothing else); the byte length and component count stay within
    `limits.PathBytes`/`limits.PathComponents`; no component is empty,
    `"."`, `".."`, over `limits.NameBytes`, carrying a NUL, or carrying a
    reserved prefix; and the accepted value survives the crossing into
    `SafePath` unchanged. A fuzz corpus seeded with the traversal cases
    above, several Unicode strings, and pathological lengths runs as part
    of the standard test suite, not only on demand.

### Admission (admit_test.go)

18. **Every named filesystem type's verdict matches the table** in this
    document (OK/refused, reflink, warn), including the two-part
    `AdmitMount` check (type admitted, but no birth time reported, still
    refuses).
19. **An unclassified filesystem magic refuses** with a message that does
    not name a specific known type, proving the allow-list's default case
    is exercised and not merely assumed.

### Path validation (path_test.go)

20. **The escape table** (empty, `.`, `..`, embedded `/`, NUL, over-length,
    reserved prefix) is refused by every entry point that reaches
    `validateExisting` (`ParseVpath`, `ParseSharePath`, `ParseSafePath`,
    `JoinExisting`).
21. **The creation table** (control bytes, `:`, trailing dot or space,
    every Windows-reserved name case-insensitively with and without an
    extension) is refused by `Join` and accepted by `JoinExisting`
    (already-existing names bypass the portability table by design).
22. **`JoinControl` is the only function that can produce a reserved
    name**, and it still applies every other rule in the creation table
    (a `.scpart-` name with an embedded NUL or over-length still refuses).
23. **Bounds refuse rather than silently truncate or accept**: a path over
    `limits.PathBytes`, a component count over `limits.PathComponents`
    (built incrementally via repeated `Join` as well as in one string),
    and a single component over `limits.NameBytes`.
24. **`HasPrefix`/`Under` are component-wise**: `"ab"` is not under `"a"`.
25. **`Components()` returns a defensive copy**: mutating the returned
    slice does not affect the `SafePath`'s own state.
