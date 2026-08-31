# Core rebuild: mutations

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (chiefly `ops.go` and `publish.go`) is referenced as a
> behavioral specification only. The new implementation is written
> completely new; nothing is copied.

## Purpose

One file, `core/write.go`, holds the single-entry mutations: `Mkdir`,
`CreateFile`, `Rename`, `Delete` with its resolved and recursive halves,
`Stat`, `PublishPart`, the precondition gate, and the journal recording
helper. Transfers (`Move`, the copy machinery, the conflict policy) are
07-transfers.md; trash is 08-trash.md; the aggregate invalidation and the
quota ledger these mutations call into are 09-quota-and-aggregates.md.

Every mutation follows one invariant, in this order:

1. **Resolve.** The operation takes a `Resolved`; the existence rule already
   ran (04-resolve.md).
2. **Check permission.** `r.Require(bits)` is the first statement.
3. **Check the precondition** where a validator applies.
4. **Act through the named VFS operation.** Nothing in this file parses a
   path or touches a raw syscall; `Mkdir`, `WriteDurable`, `Rename`,
   `Unlink`, `Rmdir` and `PublishPart` on the share root are the only ways
   the disk changes.
5. **Invalidate**: `markDirty` for every path the change touched.
6. **Record one journal row**, best-effort, after the commit.

Steps 5 and 6 are after the commit point; their failures are logged, never
returned, and can never undo the write.

Permission bits:

| Operation | Requires | Journal op |
| --- | --- | --- |
| `Mkdir` | `acl.Create` | `OpUpload` |
| `CreateFile` | `acl.Write \| acl.Create` | `OpUpload` |
| `Rename` | `acl.Rename` | `OpMove` |
| `Delete` | `acl.Delete` | none (see below) |
| `Stat` | `acl.Read` | none |
| `PublishPart` | `acl.Write \| acl.Create` | `OpUpload` |

`CreateFile` and `PublishPart` demand both bits rather than choosing by
whether the target exists: the two-stat answer would race, and a caller
allowed to replace but not create (or the reverse) getting different
refusals depending on a concurrent delete is a permission model nobody can
reason about.

## Spec: the precondition gate

```go
// Token is a caller-supplied validator: the current ETag the client last
// saw, sent to prove nothing changed in between.
type Token string

func precondition(ifMatch *Token, st vfs.Stat) error
```

This is the F11 gate (concurrent-edit protection). The chain of facts:

- Every file ETag this system mints is weak (02-domain-types.md): statx has
  no change-version field, so a metadata token cannot honestly claim strong.
- RFC 9110 requires strong comparison for `If-Match`.
- A weak token can never pass strong comparison.
- Therefore a supplied validator always fails, whatever its value.

So the function is: nil `ifMatch` passes; any non-nil `ifMatch` returns
`&PreconditionError{Current: cur}` where `cur` is `FileETag(st)`'s token.
The current token rides in the error so a conflict screen can show what the
file is now without a second round trip. The explicit unconditional retry
(the caller dropping its validator after seeing the conflict) is the only
way past, and that is the design: the server cannot prove nothing changed,
so it refuses to pretend, and the human decides.

When the target does not exist and a validator was supplied, the answer is
`&PreconditionError{Current: ""}`: a validator against a missing file failed
by definition, and the empty current token tells the client the file is
gone rather than changed.

The `Token` type itself moves to `ident.go` (02-domain-types.md); the gate
stays here with its callers.

## Spec: journal recording

```go
func (c *Core) record(ctx context.Context, r Resolved, op journal.Op)
```

One row per successful mutation: account, share, share-relative path, op.
Rules:

- Called only after the write has committed. A failure here is logged and
  dropped; the journal is a convenience surface (recent files,
  11-homes-and-recent.md) and nothing may treat a missing row as evidence
  that nothing happened.
- A nil journal (a deployment without one) is a silent no-op.
- The journal's account column is `uint32`; the user id is narrowed
  checked, and an id that does not fit skips the row with a warning rather
  than truncating into some other account's history.

Deletes are not recorded: the journal's op set (`upload`, `edit`, `copy`,
`move`, `restore`) feeds the recent-files surface, and a deleted file has
no row to surface. This is preserved as-is.

## Spec: the operations

### Mkdir

```go
func (c *Core) Mkdir(ctx context.Context, r Resolved) (Entry, error)
```

1. `Require(acl.Create)`.
2. `requireCreatableLeaf(r.path)` (below).
3. `root.Mkdir(path)`; an existing name surfaces as `ErrExists` through the
   VFS mapping.
4. `markDirty`, `record(OpUpload)`, return `buildEntry` for the new
   directory.

### CreateFile

```go
func (c *Core) CreateFile(
    ctx context.Context, r Resolved, mode vfs.DurableOpts,
    ifMatch *Token, write func(*vfs.File) error,
) (Entry, error)
```

The upload finalization path and the content-replacement path, both through
`vfs.WriteDurable`: stage under a reserved name, sync, publish by atomic
rename, sync the parent. A truncate-and-write replace is neither atomic nor
mode-preserving, which is why no second write path exists.

Precondition rules, keyed on one stat of the target:

- **Target exists**: `precondition(ifMatch, st)` applies. Any supplied
  validator refuses with the current token (the F11 rule above); nil
  passes and the write replaces the content.
- **Target missing**: a supplied validator refuses with
  `&PreconditionError{Current: ""}`. With no validator, the name is about
  to be minted, so `requireCreatableLeaf` applies.
- **Stat failed otherwise**: the mapped error, nothing written.

Then `WriteDurable(path, mode, write)`; the callback receives the staging
handle and writes the content. On success: `markDirty`,
`record(OpUpload)`, `buildEntry`.

The stat and the write race by design; `WriteDurable` is what settles the
race (its own `NoClobber` and replace semantics are the atomic truth), and
the precondition is advisory ordering on top, same as every If-Match
implementation over a real filesystem.

Quota is not charged here. In the behavior being preserved, the ledger's
write-side integration point is `PublishPart`, where the finished size is
known; `CreateFile` writes without touching the ledger. The reservation
seam (`QuotaSink.Reserve`) and its enforcement story are
09-quota-and-aggregates.md.

### Rename

```go
func (c *Core) Rename(ctx context.Context, r Resolved, newName string, ifMatch *Token) (Entry, error)
```

Same-directory name change only; crossing a directory is `Move`
(07-transfers.md), and the ACL distinguishes the two on purpose
(`acl.Rename` versus `acl.Move`).

1. `Require(acl.Rename)`.
2. Stat the source; missing maps to `ErrNotFound`.
3. `precondition(ifMatch, st)`.
4. Destination is `path.Parent().Join(newName)`: `Join`, not
   `JoinExisting`, so the creation table applies to the new name and a
   name no Windows or SMB client could open is refused here.
5. `root.Rename(from, dest, noReplace = true)`: a taken destination is
   refused atomically (`RENAME_NOREPLACE`), surfacing as `ErrExists`.
6. `markDirty` on both the old and the new path, `record(OpMove)`, return
   `buildEntry` for the destination (a fresh `Resolved` carrying the same
   user, share, root and perms with the new path).

The journal row is `OpMove`: to the recent-files surface a rename is the
file moving, and one op keeps the vocabulary small.

### Delete

```go
func (c *Core) Delete(ctx context.Context, r Resolved, permanent bool) error
func (c *Core) deleteResolved(ctx context.Context, r Resolved, st vfs.Stat, charge bool) error
func (c *Core) deleteRecursive(ctx context.Context, r Resolved) error
```

`Delete`:

1. `Require(acl.Delete)`.
2. Stat the target.
3. **Trash dispatch**: when the share has trash enabled and the caller did
   not say `permanent`, the delete becomes `trashMove` (08-trash.md) and
   nothing below runs. `permanent` is the caller spelling out that the
   trash is to be bypassed.
4. Otherwise `deleteResolved(r, st, charge = true)`.

`deleteResolved` performs the permanent delete and credits the ledger:

- **A directory**: the freed size is read from the recursive aggregate
  (`Aggregate(share, path).RSize`) before anything is unlinked, so the
  ledger is credited what was actually on disk; then `deleteRecursive`. An
  aggregate failure fails the delete before any child is touched, because
  deleting first and guessing the credit after would corrupt the ledger.
- **A file**: the freed size is the stat's size; then `root.Unlink(path)`.
- When `charge` is true and bytes were freed, `chargeQuota(user,
  int64Minus(freed))`: a negative delta, saturating rather than wrapping
  for a size past the signed range. Best-effort, after the disk change.
- `markDirty` on the deleted path.

`charge == false` is for the callers that account the bytes themselves or
must not credit twice: the cross-device leg of a move deletes a source
whose bytes were already charged at the destination copy.

`deleteRecursive` walks top-down, deleting children before the parent:
read the directory (`HideReserved`), per child stat and recurse into
directories, `Unlink` files, then `Rmdir` the directory itself. The skip
rules match the read side: an unjoinable name or a vanished child
(stat failure) is skipped; the `Rmdir` at the end is the backstop, since a
directory that still holds something the walk could not remove fails there
with `ErrNotEmpty` rather than being silently left half-gone and reported
deleted. VFS errors map; nothing bypasses `Unlink`/`Rmdir` on the share
root.

Delete records no journal row (see the journal section).

### Stat

```go
func (c *Core) Stat(ctx context.Context, r Resolved) (Entry, error)
```

`Require(acl.Read)`, then `buildEntry`. It sits with the mutations only
because it is the one single-path read and shares `buildEntry`; a single
named path is the bounded case where minting a stable id is worth doing,
which is what a share-link target needs. A vanished target comes back as
the skeleton entry (05-listing-and-read.md), which the caller detects by
the zero kind.

### requireCreatableLeaf

```go
func requireCreatableLeaf(p vfs.SafePath) error
```

Applies the creation table to a leaf about to be brought into existence:
re-join the leaf name through `Parent().Join(Name())` and return the
refusal, if any. The root passes vacuously. The table (control-prefix,
Windows portability, control characters) lives in the VFS; this helper
exists because a `Resolved` path was validated for traversal, and a name
that already exists on the share must stay fully usable while a name about
to be minted must not add one a Windows or SMB client could never open.
`Mkdir` applies it always; `CreateFile` and `PublishPart` apply it only
when the target does not already exist, because refusing to overwrite an
existing legacy name would make it unwritable rather than unmakeable.

### PublishPart

```go
func (c *Core) PublishPart(ctx context.Context, r Resolved, part vfs.SafePath, size uint64) (Entry, error)
```

`CreateFile`'s sibling for content already on disk. The upload engine has
been accumulating a part file, possibly for hours, already synced; staging
it again through `WriteDurable` would be a second full write of a file
already in the right directory. So this operation is the rename half alone,
through `vfs.ShareRoot.PublishPart`.

Order:

1. `Require(acl.Write | acl.Create)`.
2. **Control-name check**: `vfs.IsReservedName(part.Name())` must hold, or
   `ErrDenied` ("publish from a name that is not a control file"). Only the
   upload engine can have minted such a path (`JoinControl` is the only
   producer and user input cannot reach it), so this is the proof the
   source is ours.
3. **Same-directory rule**: `part.Parent().Equal(r.path.Parent())` must
   hold, or `ErrDenied`. Both paths relative to one parent is what makes
   the rename one atomic step, and the caller is refused rather than
   trusted on it.
4. **One prior stat** of the destination. It decides both the clobber flag
   and whose mode and ownership the published file ends up with, and it is
   read exactly once because the rename below settles the race either way:
   - exists: `replacing = true`. No creation-table check; something else
     already wrote the name, and refusing now would make it unwritable
     rather than unmakeable.
   - missing: `requireCreatableLeaf` applies.
   - other stat error: mapped, nothing renamed.
5. `root.PublishPart(part, dest, replacing)`: the VFS restores the replaced
   entry's mode and ownership before the rename and syncs the parent after.
   Failure maps; the part file stays where it was.
6. **OwnerRestore warning**: `Durable.OwnerRestore` non-nil means the
   replaced file's uid or gid could not be put back. EPERM here is the
   ordinary answer for an unprivileged process, so it is a logged warning,
   not an error; a mode that could not be restored already failed inside
   the VFS helper, because the mode is what the neighbours' access depends
   on.
7. **Post-commit, in this order**: `markDirty(share, dest)`,
   `record(OpUpload)`, then the quota charge. Everything after the rename
   is best-effort and logged on failure; none of it can undo the commit.
   Invalidation goes first so no reader caches the stale aggregate while
   the bookkeeping runs; the journal row precedes the ledger because the
   row describes the file and the ledger only counts bytes.
8. The quota delta is `deltaOf(size, prior.Size)` when replacing and
   `deltaOf(size, 0)` when creating: the ledger sees the signed change, not
   the gross size.

```go
func deltaOf(now, before uint64) int64
```

`deltaOf` narrows both sizes checked into `int64` and returns the
difference. Either size not fitting the signed width is a number no
filesystem produced; the function logs and charges nothing rather than
wrapping the ledger by petabytes. Saturating to zero, not to the extreme:
a garbage input earns no charge in either direction.

## Rationale

- **One invariant, spelled once.** Resolve, permission, precondition, named
  VFS operation, invalidate, one journal row. A reader can audit any
  mutation top to bottom against the list, and a new mutation that skips a
  step is visible in review by its shape.
- **No raw syscalls, no path parsing.** The VFS owns the rename, the
  durable write, and the name tables; the core owning any of it would be a
  second implementation to keep honest. This is invariant 3 of the overview
  enforced at file granularity.
- **The always-fail precondition is honest.** The alternative implemented
  by much WebDAV software, weak comparison where strong is required, tells
  a client its write is safe on evidence (mtime granularity, inode reuse)
  that cannot prove it. Refusing with the current token converts a silent
  lost update into a visible conflict the human resolves.
- **Aggregate-before-delete.** The recursive size must be read while the
  tree still exists; it is the only source that matches what the disk held,
  and the delete destroys it.
- **PublishPart is not CreateFile.** Folding them would either double-write
  finished uploads or grow `CreateFile` a bypass flag that skips staging,
  and a flag that skips durability is a footgun with a name.
- **Best-effort bookkeeping is explicit.** Journal, ledger and dirty-marks
  ride after the commit because failing a committed write over its
  bookkeeping would tell the client its data is gone when it is on disk.
  The cost, drift in ledger and journal under crashes, is bounded and
  repairable; a lied-about write is not.

## Deliberate changes

- The `Token` type moves to `ident.go` (02-domain-types.md). The
  `precondition` function stays here with its callers.
- `pathExists` and `uniqueSiblingName` move to `resolve.go` per the
  overview; the transfer-side conflict machinery consuming them moves to
  `transfer.go` (07-transfers.md). `Move`, `WouldCopy`, `crossesDevice`,
  `copyRecursive`, `copyFile`, `OnConflict` and `MoveOpts` are out of this
  file entirely.
- `deltaOf` and `int64Minus` are one saturation concern spelled twice
  (`int64Minus(v)` is `deltaOf(0, v)` except for the clamp target). The
  rebuild keeps one signed-delta helper in `write.go` beside the ledger
  calls and the trash path uses it too.
- No behavioral changes: permission bits, precondition outcomes, the trash
  dispatch, the quota crediting rules, the journal op per operation, and
  the post-commit ordering are preserved exactly.

## Tests

Precondition:

- A nil validator passes against any stat.
- Any non-nil validator refuses; the error unwraps to `ErrPrecondition`
  and carries the current token of the stat it was checked against.
- A validator against a missing target refuses with an empty current
  token.
- After the refusal, the same call with a nil validator succeeds (the
  unconditional retry).

Mkdir:

- Creates, returns an entry with `Kind == KindDir`, and the directory
  lists afterwards.
- An existing name answers `ErrExists`; a Windows-hostile name (trailing
  dot, `CON`, a colon) answers the name error; a reserved prefix is
  refused. Without `acl.Create`: `ErrDenied`.

CreateFile:

- A new file: content readable back, entry size and etag match, mode from
  `DurableOpts` applied.
- A replace with nil validator: content replaced atomically; a reader
  holding the old descriptor sees old bytes (the durable-write property,
  asserted once here as the consumer).
- A replace preserves the prior file's mode.
- Any supplied validator refuses both against an existing and a missing
  target, with the tokens above; nothing is written either way.
- A write callback error fails the operation and leaves the prior content
  intact; no staging debris is listable.
- Journal receives one `OpUpload` row with the acting account; a failing
  journal stub does not fail the write.

Rename:

- Renames within the directory; the returned entry has the new name and
  the old name is gone.
- A taken destination answers `ErrExists` (no replace).
- A hostile new name is refused by the creation table; the source is
  untouched.
- Records `OpMove`; both paths are dirty-marked (asserted via aggregate
  invalidation).
- A supplied validator refuses with the source's current token.

Delete:

- A file delete unlinks and credits the ledger with minus its size.
- A directory delete removes the tree bottom-up and credits the ledger
  with the aggregate's `RSize` read before deletion.
- A share with trash enabled diverts to trash; `permanent == true` on the
  same share bypasses it; a share without trash deletes permanently either
  way.
- A child vanishing mid-walk does not fail the delete.
- An aggregate failure fails the delete with nothing removed.
- A user id past `uint32` skips the journal row (asserted on the
  operations that record; delete records nothing, asserted too).
- Without `acl.Delete`: `ErrDenied`, nothing removed.

PublishPart:

- A part name that is not reserved: `ErrDenied`, part untouched.
- A part in a different directory than the destination: `ErrDenied`.
- Publishing to a fresh name: the file appears with the part's content,
  the part name is gone, quota charged `+size`, journal `OpUpload`.
- Publishing over an existing file: prior mode preserved, quota charged
  `size - prior.Size` (negative when the replacement is smaller).
- A hostile destination name is refused when creating and accepted when
  replacing an existing entry of that name.
- An `OwnerRestore` error from the VFS surfaces as a log line, not an
  error; the returned entry is the published file.
- `deltaOf` with a size past `int64` charges zero.

Cross-cutting:

- Every mutation on a `Resolved` lacking its bits answers `ErrDenied`
  before any disk change (asserted with a VFS stub that counts calls).
- No mutation writes a journal row on failure.
- The file contains no call into `os` or `unix` and no string path
  manipulation (kept by review; the import list of `write.go` is part of
  the contract).
