# 04: Resolution

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here: `resolve.go` and `descend.go`, plus the path
> helpers currently in `ops.go` and `links.go`) is referenced as a behavioral
> specification only. The new implementation is written completely new;
> nothing is copied.

Target file: `engine/service/core/resolve.go`. `//go:build linux`.

## Purpose

`Resolve` is the single gate between a client-supplied virtual path and
anything that touches disk. It applies two rules in exactly one place: the
existence rule (a path a caller may not know about answers "not found",
never "denied") and the permission check (the ACL evaluation for the
requested bits). Every operation in the package takes a `Resolved`, never a
path, so no operation can be reached without having passed the gate.

This file also holds the descent helpers a recursive walk needs
(`ResolveUnder`, `EntryAt`), the inverse crossing (`VpathFor`), and the
path predicates that four other files call (`pathExists`,
`requireCreatableLeaf`, `uniqueSiblingName`, `lastDot`). This is the
security-critical step of the build order; it gets its escape and
existence-rule tests before anything consumes it.

## Spec

### The Resolved type

```go
// Resolved is what every operation takes instead of a virtual path: the
// share root, the validated path under it (grant subpath already on the
// front), and the permissions the caller holds there.
type Resolved struct {
    user  UserID
    share ShareID
    root  *vfs.ShareRoot
    path  vfs.SafePath
    perms acl.Perms
}

func (r Resolved) User() UserID          // the caller
func (r Resolved) Share() ShareID        // the share this landed in
func (r Resolved) Root() *vfs.ShareRoot  // the live root
func (r Resolved) Path() vfs.SafePath    // share-relative, subpath prefixed
func (r Resolved) Perms() acl.Perms      // full effective set at Path
func (r Resolved) Has(want acl.Perms) bool
func (r Resolved) Require(want acl.Perms) error // ErrDenied when missing
```

Every field is unexported, and that is the whole of the guarantee: a
`Resolved` is a capability. No caller outside the package can construct one,
so the only way to hand an operation a target is to have gone through
`Resolve` (or a descent from one, which checks its own precondition). This
is also what forces the entire domain into one Go package: a sub-package
holding, say, the write operations could not read these fields, and either
an exported constructor or a getter-complete interface would let arbitrary
code mint a resolution and skip the gate. The package boundary is the
security boundary.

`Require` exists for an operation that needs more bits than the resolution's
`need` asked for, for example both READ and DOWNLOAD, without a second
resolution.

### Resolve

```go
func (c *Core) Resolve(user UserID, p vfs.Vpath, need acl.Perms) (Resolved, error)
```

The exact algorithm, in order:

1. **Root refusal.** `p.IsRoot()` returns `ErrNotFound`. The virtual root
   names no share; it is listed through `Roots`, never resolved.
2. **Eager best-effort home creation.** `ensureHome(ctx, user)` runs before
   the label lookup, because the home grant may be the very grant this
   resolution needs on a user's first access. A failure is logged through
   `warn` and resolution continues: a home hiccup must not break the user's
   other shares.
3. **Label lookup in the caller's projected root.** `p.Label()` is matched
   against `c.acl.Roots(user)`, first match wins (the projection already
   disambiguated duplicate labels with " (2)" suffixes). No match returns
   `ErrNotFound`. This covers both a label the caller has no grant over and
   a label naming no share at all: to this caller they are the same missing
   path, never a denial.
4. **Share id narrowing.** The matched grant's `int64` share id is narrowed
   to the `uint32` `ShareID`. A value that does not fit is a corrupt grant
   table and answers `ErrNotFound` wrapped with context (`errf`).
5. **Registry lookup.** `c.shareEntry(shareID)`; an unregistered id is
   `ErrNotFound` (a grant over a share that no longer exists).
6. **Broken share.** An entry with `brokenErr != nil` answers
   `&ShareBrokenError{Share: name, Reason: RejectionKind(brokenErr)}`,
   deliberately not `ErrNotFound`. The caller holds a grant and sees this
   share in their root listing; "not found" would be the server
   contradicting its own listing, and it would send a user whose drive did
   not come back looking for a deleted folder. Telling this caller the
   share exists leaks nothing: their own root list already says so.
7. **Path join.** `joinSubpath(def, match.Subpath, p.Rest())` builds the
   full share-relative path: the grant's subpath components first, then the
   client rest's components, each appended with `SafePath.JoinExisting`.
   Any join error is returned as-is (the vfs validation error).
8. **The permission check.** `c.acl.Evaluate(user, vpath, need)` on the
   full path. Not allowed answers `ErrDenied`. This is the only spot that
   can produce a denial from a client path, and it is reachable only after
   the label matched a grant in step 3: a 403 can only be earned by a
   caller who may already know the target exists.
9. **Effective permissions.** `c.acl.Effective(user, vpath)` fills
   `Resolved.perms` with the caller's full bit set at the path, so later
   `Require` calls and `Entry.Perms` do not re-evaluate.

`Resolve` does not stat the path. Existence on disk is the consuming
operation's problem; a resolved path that turns out to be missing surfaces
as `ErrNotFound` from the operation, which is byte-identical to the answer
step 3 gives. That identity is the point: whether the refusal happened at
the grant table or at the filesystem is not observable.

#### joinSubpath

```go
func (c *Core) joinSubpath(def ShareDef, subpath acl.Path, rest vfs.SharePath) (vfs.SafePath, error)
```

Starts from `vfs.RootPath()` and appends every component of the grant
subpath, then (unless `rest.IsRoot()`) every component of `rest.Safe()`,
all through `JoinExisting`.

`JoinExisting`, not `Join`: resolution addresses a path, it does not create
one. The creation table refuses Windows-reserved names (`CON`, `a:b`) so
this server never mints a name an SMB client cannot open, but it has no
business deciding whether a path already on disk can be named. A share
carrying a directory literally named `CON`, written by somebody else's
tool, must be listable and resolvable; only creating such a name through
this server is refused (see `requireCreatableLeaf` below).

A join failure on a grant subpath component is a corrupt grant, and the
resolution is refused rather than the subpath silently truncated: a grant
that cannot name its own scope must not resolve to a wider one.

#### aclPath

```go
func aclPath(p vfs.SafePath) acl.Path // acl.NewPath(p.Components()...)
```

The one crossing from the validated path vocabulary into the ACL engine's.

### ResolveUnder

```go
func (c *Core) ResolveUnder(parent Resolved, p vfs.SafePath, need acl.Perms) (Resolved, error)
```

Narrows a resolution onto a path beneath it, for the recursive walks
(archive streaming, WebDAV depth listings, copy) that hold a `Resolved` for
a directory and need one for a child they just listed. Re-resolving each
child from a virtual path would re-run grant matching per entry, and it
would also be wrong: once the grant subpath is on the front of `path`, the
child's virtual path is not reconstructible from it.

Behavior:

1. `!p.Under(parent.path)` answers `ErrDenied` wrapped with context.
   `Under` is component-wise, so `"ab"` is not under `"a"` and a sibling
   whose name shares a byte prefix cannot be reached.
2. `!parent.perms.Has(need)` answers `ErrDenied`.
3. Otherwise the child `Resolved` copies user, share, root and perms from
   the parent and carries `p` as its path.

The safety argument: a grant covers a subtree, so a path under a granted
path is under the same grant by construction, and the parent's effective
perms are the child's. Step 1 makes the "under" premise checked rather than
assumed, and step 2 makes descent unable to widen access: the child can
never carry a bit the parent did not hold. What descent deliberately does
not re-apply is a deny rule scoped below the parent; the current evaluator
has no deeper-deny semantics inside a granted subtree, and if that ever
changes this function is the single place the re-check goes.

### EntryAt

```go
func (c *Core) EntryAt(r Resolved, st vfs.Stat) Entry
```

The projection of the resolved path itself, for a protocol that reports on
a directory as well as its children (WebDAV `Depth: 0`). Builds an `Entry`
from the resolution and a stat the caller already holds: name and share
path from `r.path`, kind, size and times from `st`, `Ident` from
`ident.Of(r.share, st)`, the pair from `FileETag(st)`, and `Perms`
from `r.perms`. It exists in the core because `Entry` carries an identity
and a validator only the core mints; building one by hand outside the
package is impossible by design.

### VpathFor

```go
func (c *Core) VpathFor(user UserID, share ShareID, p vfs.SharePath) (vfs.Vpath, error)
```

The inverse crossing: a share-relative path back into the form this user's
client sees, under the label their grant projects the share as. Scans
`c.acl.Roots(user)` for the entry whose share id matches (narrowing each
projected id, skipping ones that do not fit); no match is an error ("vpath
for an unreadable share"), because a URL under a label the user cannot see
is a URL to nothing. On a match, `vfs.NewVpath(label, p)`.

Callers are the protocol layers answering "what is the URL of this":
search hits, recent-file listings, WebDAV hrefs.

Note the asymmetry with `Resolve`: `VpathFor` does not strip the grant
subpath from `p`. Its callers pass paths in the same projected coordinate
space the grant's label roots, which is the existing behavior and is kept.

### Path helpers

These are predicates over paths and roots, called from write, transfer,
operation, trash, link and home code. They move here because resolution
owns the path vocabulary; their old homes (`ops.go`, `links.go`) are files
that no longer exist.

```go
func pathExists(root *vfs.ShareRoot, p vfs.SafePath) (bool, error)
```

Stats the path and folds `vfs.ErrNotFound` to `(false, nil)`; any other
error crosses through `mapVFSErr` (which now lives in `errors.go`). It is
the one way a mutation asks "is the destination occupied" without
converting a refusal into an answer: a permission error stays an error,
never a "no".

```go
func requireCreatableLeaf(p vfs.SafePath) error
```

Applies the creation table to a leaf about to be brought into existence, by
re-joining the leaf name onto its parent with `Join` and discarding the
result. The root passes (nothing is being created there by name). This is
the asymmetry partner of `joinSubpath`: anything already on the share stays
fully usable, and nothing typed through this server adds a name a Windows
or SMB client could never open. Callers: create, mkdir, publish, rename
destinations.

```go
const uniqueNameBound = 10_000

func (c *Core) uniqueSiblingName(root *vfs.ShareRoot, taken vfs.SafePath) (vfs.SafePath, error)
```

Picks the next free `"stem (n).ext"` beside a taken path, n counting from
2. One rule for every place the server has to invent a name ("keep both"
conflict handling, drop-link upload collisions), so the suffix a person
sees is the same everywhere. Exact behavior:

- The name splits at `lastDot`, but only when the dot's index is greater
  than zero: a leading dot is a hidden file's name, not an extension, so
  `".bashrc"` becomes `".bashrc (2)"` and never `" (2).bashrc"`. A name
  with no dot gets the suffix appended whole.
- Candidates are minted with `Join`, so an invented name still passes the
  creation table; a candidate the table refuses is skipped, not fatal.
- Existence is probed with `pathExists`; its error aborts the search.
- The search gives up at 10,000 with `ErrConflict`. A directory holding
  that many collisions of one name is one where the caller wanted a
  different answer than a longer suffix, and an unbounded loop over a
  syscall per candidate is a request that never returns.

```go
func lastDot(s string) int
```

Index of the last `'.'` in a name, `-1` when absent. It serves
`uniqueSiblingName` and moves in beside it; its old home in `links.go` was
one of the helper-far-from-caller cases the overview lists.

## Rationale for the cohesion decisions

One file holds everything that turns a path into a capability or reasons
about paths on behalf of the operations: the gate (`Resolve`), the descent
(`ResolveUnder`, `EntryAt`), both crossings (`aclPath` in, `VpathFor` out),
and the predicates. The old tree split the gate (`resolve.go`) from the
descent (`descend.go`) and scattered the predicates into `ops.go` and
`links.go`; a reader auditing "can a client path escape" had to open four
files. The new file is the one place that audit reads, and it is the file
step 3 of the build order tests before anything consumes it.

`EntryAt` sits here rather than in `entry.go` or `list.go` because its
input is a `Resolved` and its reason to exist is the descent story: it is
the projection half of what `ResolveUnder` is the navigation half of.
`entry.go` stays pure (types only, no `Core`).

The one-package constraint is restated here because this file is its cause:
`Resolved`'s unexported fields are what make the gate unskippable, and any
package split of the operations would break exactly that.

## Threat model

What this file defends, and how:

- **Path traversal.** No string a client sent is ever handed to the
  filesystem. `vfs.ParseVpath` validated components at the protocol edge
  (refusing `..`, empty components, separators, the control-name prefix),
  `joinSubpath` re-validates through `JoinExisting`, and every disk touch
  goes through the share root's openat2 anchor, which confines resolution
  beneath the root even if a validated name aliased something (bind mounts,
  races). An absolute client path like `/etc/passwd` parses to the label
  `etc`, matches no grant, and answers `ErrNotFound`.
- **Existence probing.** A stranger must not be able to map the tree by
  distinguishing "missing" from "forbidden". The rule: `ErrNotFound` for
  anything outside the caller's grants (steps 1, 3, 4, 5), `ErrDenied` only
  after a label match proved the caller may know the share exists (step 8).
  Downstream, an operation's "unlistable" and "missing" answers are
  deliberately indistinguishable: an operation that cannot list a directory
  it resolved returns the same `ErrNotFound` as one whose target is not on
  disk, so the refusal's layer is not observable. The two carve-outs are
  earned: `ErrDenied` inside a granted share, and `ShareBrokenError` for a
  share the caller's own root listing already names.
- **Permission widening by descent.** `ResolveUnder` checks the child is
  component-wise under the parent and checks `need` against the parent's
  perms, and copies the perms rather than re-deriving them, so a walk can
  narrow but never widen. A forged "child" outside the subtree is refused
  before any permission logic runs.
- **Capability forgery.** Unexported fields on `Resolved`; the compiler is
  the enforcement. The package-internal discipline is that nothing but
  `Resolve`, `ResolveUnder`, and the internal constructors with their own
  gates (trash restore, link access, home seeding, documented in their own
  files) builds a `Resolved` literal.
- **Grant-table corruption.** A share id that does not narrow, or a grant
  subpath that fails validation, refuses the resolution instead of
  truncating or guessing. Fail closed.

## Deliberate changes

No behavioral changes to resolution, descent, or the helpers.

Cohesion moves (file placement only):

- `descend.go` merges into `resolve.go` (`ResolveUnder`, `EntryAt`).
- `pathExists`, `requireCreatableLeaf`, `uniqueSiblingName` and
  `uniqueNameBound` move in from the old `ops.go`.
- `lastDot` moves in from the old `links.go`.
- `mapVFSErr`, which the old `resolve.go`'s helpers call, lives in the new
  `errors.go` (01-errors.md), not here.

## Tests

New tests, written fresh, covering at least what the old
`resolve_test.go`, `symlinkpolicy_test.go` and `escape` suites assert. This
file's tests run before any operation consumes it (build order step 3).

1. **Indistinguishability table.** The core property, in one table so it
   cannot pass for the wrong reason: a label outside every grant, a label
   the user cannot read, and a path missing inside a granted share all
   answer `ErrNotFound`, and the answers are byte-identical (same
   `Error()` string), whether they arose in `Resolve` or in the consuming
   operation.
2. **Earned denial.** Resolving a readable path with a permission the
   caller lacks (for example `acl.Write` under a read-only grant) answers
   `ErrDenied`, and only after the label matched.
3. **Root refusal.** The virtual root and `"/"` both answer `ErrNotFound`.
4. **Traversal.** `".."`, empty components, backslashes and absolute paths
   are refused at parse or join; `/etc/passwd` answers `ErrNotFound`; a
   path that validates but names a symlink pointing outside the share is
   refused by the root's policy (the vfs suite proves the mechanism; this
   suite proves the core wires it).
5. **Grant subpath prefixing.** A grant with subpath `a/b` resolves label
   plus `c` to path `a/b/c`, and the resolved path's perms come from the
   subpath grant.
6. **Existing-name resolution.** A directory named `CON` (or another
   creation-table refusal) already on disk resolves and lists; creating
   one through the server is refused by `requireCreatableLeaf`.
7. **Broken share answer.** Resolving into a broken share answers
   `ShareBrokenError` carrying the share name and the reason token, and
   `errors.Is(err, ErrShareBroken)` holds; it is not `ErrNotFound`.
8. **Corrupt grant.** A grant whose share id overflows uint32, and one
   whose subpath holds an invalid component, both refuse the resolution.
9. **Capability.** A `Resolved` from a read-only resolution reports
   `Has(Read)` and not `Has(Write)`; `Require(Write)` is `ErrDenied`.
   (The construction proof is the type system; the test documents it.)
10. **ResolveUnder.** A child under the parent resolves with the parent's
    perms; a sibling sharing a byte prefix (`"ab"` under `"a"`) is
    `ErrDenied`; a `need` the parent lacks is `ErrDenied`; the child's
    share, root and user equal the parent's.
11. **EntryAt.** The entry for a resolved directory carries its name,
    share path, ident, etag pair and the resolution's perms.
12. **VpathFor.** Round-trips with `Resolve` for a granted share
    (label plus rest in, same resolution back); an invisible share errors.
13. **Home eagerness.** With homes enabled, a first `Resolve` by a new
    user creates the home and can then resolve it; a failing home (root
    made unwritable) logs and still resolves the user's other shares.
14. **pathExists.** True for a present path, false with nil error for a
    missing one, error (not false) for an unreadable parent.
15. **uniqueSiblingName.** `"a.txt"` taken yields `"a (2).txt"`; with
    `"a (2).txt"` also taken, `"a (3).txt"`; `".bashrc"` yields
    `".bashrc (2)"`; a dotless name yields `"name (2)"`; the bound
    answers `ErrConflict` (exercised with a lowered bound or a stubbed
    exists check, not 10,000 files).
16. **lastDot.** Table over `"a.b"`, `".bashrc"`, `"noext"`, `"a.b.c"`,
    `""`.
