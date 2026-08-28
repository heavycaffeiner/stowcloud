# 08: Trash

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here `trash.go`) is referenced as a behavioral
> specification only. The new implementation is written completely new;
> nothing is copied.

## Purpose

`trash.go` is the delete policy for shares that opt in: instead of removing
an entry, `Delete` relocates it into a per-share trash directory, and this
file owns the listing, the restore, and the purge over that directory. It
stays in the domain package because it constructs `Resolved` values
internally and its quota crediting is entangled with the delete path.

Trash is off by default. The toggle lives in the share definition and is
visible in the share listing, so the UI can warn before a destructive action
that a delete on this share really deletes.

## Spec

### On-disk layout

```
<share>/.sctrash/{id}-{base64url origin path}
```

One flat level under the control directory `.sctrash`, at the share root.
There is no nesting inside the trash; the origin path is carried in the entry
name, not in directory structure.

`.sctrash` is a control directory: created through `JoinControl`, which is
the only function that may produce a reserved prefix, so no user input can
name it. Every user-facing `ReadDir` passes `vfs.HideReserved`, so the trash
never appears in a listing and is never indexed.

The entry name carries the whole origin path, not just the basename, because
a flat store has nowhere else to keep the parent: restoring a file trashed
from `docs/2024/report.pdf` has to land there, not at the share root.

```go
const trashDir = ".sctrash"
```

### Entry names

- The id is 8 random bytes from `crypto/rand`, rendered as lowercase hex
  (16 characters). Random so two concurrent deletes of the same name cannot
  collide on the entry name. A `rand.Read` failure fails the delete; a trash
  entry is never given a guessable or reused name.
- The origin path is the share-relative path string, encoded with
  `base64.RawURLEncoding`. Base64url rather than the path's own slash-joined
  form because a slash inside a single component would be reinterpreted as a
  separator, exactly the character that must survive intact. Raw (unpadded)
  url encoding also keeps `/` and `=` out of the component.
- The two halves are joined with a single dash: `{id}-{encoded}`.

```go
func encodeOrigPath(p vfs.SafePath) string
func decodeOrigPath(encoded string) (vfs.SafePath, bool)
func splitTrashName(name string) (id, rest string, ok bool)
```

`decodeOrigPath` reports false on anything that does not base64url-decode to
a valid safe path. That false is how legacy entries are recognised: entries
written before the encoding existed carry a bare basename after the dash.
The caller treats such an entry as the legacy shape, restoring to the share
root, because the root is all the information the name carries.

`splitTrashName` cuts on the first dash. This is sound because the id half is
lowercase hex and can never contain a dash, while the encoded half may
contain dashes (base64url's alphabet includes `-`). Cutting on the first dash
therefore always separates the two correctly. A name with no dash at all is
not a trash entry and is skipped.

### TrashEntry

```go
type TrashEntry struct {
    ID          string // the hex id
    Name        string // display name: leaf of the origin path, or the raw suffix for a legacy entry
    OrigPath    string // share-relative origin, empty for a legacy entry
    IsDir       bool
    Size        uint64
    DeletedAtNs int64  // deletion time; see ctimeOrMtime
}
```

### trashMove

```go
func (c *Core) trashMove(ctx context.Context, r Resolved, st vfs.Stat) error
```

Called by `Delete` (in `write.go`) when the share has `TrashEnabled` and the
caller did not ask for permanent. Steps:

1. `ensureTrashDir`: build the control path with
   `vfs.RootPath().JoinControl(trashDir)` and `Mkdir` it, tolerating
   `vfs.ErrExists`.
2. Mint the id: 8 bytes of `crypto/rand`, `hexLower`.
3. Build the entry name `{id}-{encodeOrigPath(r.path)}` and join it under the
   trash directory.
4. `r.root.Rename(r.path, entryName, true)`: a rename, never a copy, so a
   trashed delete is atomic and costs nothing proportional to the data.
5. Two dirty marks: the origin path (its parent's listing and aggregates
   changed) and the trash directory (its own aggregate state changed). Both
   are required; marking only the origin left the trash side stale.

No quota movement here: the bytes still exist on disk, only relocated.
Crediting at trashMove and again at purge would credit twice.

### TrashList

```go
func (c *Core) TrashList(ctx context.Context, r Resolved) ([]TrashEntry, error)
```

Requires `acl.Read` on the share root: the grant that decides who may see the
share at all is the grant that decides who may see its trash.

Reads the trash directory with `HideReserved`. A missing trash directory is
an empty listing (`nil, nil`), not an error: a share nobody deleted from has
no trash directory and that is not a fault. Per entry:

- `splitTrashName`; a name with no dash is skipped.
- Join and stat; an entry that fails either is skipped (it vanished under
  the walk, or is not something this file wrote).
- `Name` is `trashDisplayName(rest)`: the leaf of the decoded origin path,
  or the raw suffix for a legacy entry.
- `OrigPath` is the decoded path string when decoding succeeds, empty for a
  legacy entry.
- `DeletedAtNs` is `ctimeOrMtime(st)`.

```go
func ctimeOrMtime(st vfs.Stat) int64
```

The deletion time is the inode change time: the move into the trash is an
inode change, which is exactly what records when the delete happened. This
field used to carry the file's mtime, which a rename does not touch, so a
file last edited a year ago and deleted a minute ago was listed as deleted a
year ago. A filesystem that reports no change time falls back to the file's
own mtime rather than showing nothing.

### TrashRestore

```go
func (c *Core) TrashRestore(ctx context.Context, r Resolved, id string) (vfs.SafePath, error)
```

Requires `acl.Create`: a restore creates an entry in the live tree, and that
is the capability it should demand. Steps:

1. Read the trash directory; find the entry whose name has the prefix
   `{id}-`. Not found is `ErrNotFound`.
2. Decode the destination from the suffix. Legacy entry (decode fails): parse
   the suffix as a bare safe path, which restores to the share root. That is
   the documented old behavior, applied only where the new information does
   not exist. A legacy suffix that does not even parse is `ErrNotFound`.
3. `ensureDirRecursive` on the destination's parent: the origin's ancestor
   directories may themselves have been deleted since, and the restore has to
   land where the entry was trashed from, so the chain is recreated,
   shallowest first. `ShareRoot.Mkdir` is one level at a time, hence the
   walk. A `vfs.ErrExists` on any level is tolerated: losing a race with
   something else creating the same ancestor is fine, the directory existing
   is all that mattered.
4. Conflict instead of overwrite: if anything exists at the destination,
   return `ErrConflict`. Something is at the origin path now, and the caller
   has to act on that; a restore never silently replaces live data.
5. Rename the trash entry to the destination, mark the destination and the
   trash directory dirty, return the destination path.

The origin path is resolved again at restore time, against the share root as
it is now. This re-resolution is the rule that makes restore safe: a path
that no longer resolves the same way produces a conflict or a parse failure
rather than a silent overwrite of whatever sits there today.

```go
func (c *Core) ensureDirRecursive(r Resolved, dir vfs.SafePath) error
```

No context parameter: every step is a path join, a stat and a mkdir through
the share root, none of which take one. The reference carries a `ctx` it
never reads, and passing one here would suggest the walk can be cancelled
partway, which would leave a half-built ancestor chain.

### TrashPurge

```go
func (c *Core) TrashPurge(ctx context.Context, r Resolved, id *string) error
```

Requires `acl.Delete`. `id == nil` purges everything; otherwise only the
entry whose name has the prefix `{id}-`. A missing trash directory is
success: there is nothing to purge.

Per entry, in one pass:

- Join and stat; a failure joins the error and continues to the next entry.
- Directory entry: read the recursive aggregate first for the freed size,
  then `deleteRecursive` through an internally built `Resolved` carrying the
  caller's user, share, root and perms.
- File entry: freed size is `st.Size`, then `Unlink`.
- On success with `freed > 0`: credit the quota ledger with
  `chargeQuota(ctx, r.user, int64Minus(freed))`.

Errors are joined, not skipped. Every failing branch appends to a single
`errors.Join` accumulator and moves on, and the accumulator is the return
value. Every branch used to `continue` silently, so a purge that deleted
nothing still answered success: the screen said the item was gone and the
next listing showed it again. The joined error keeps the batch behavior (one
bad entry does not stop the others) while making the failure visible.

The purge is where the quota is credited, not `trashMove`: purge is where
trashed bytes are actually freed; the move into the trash only relocated
them.

The directory aggregate read is best-effort by design. The rollup refuses a
path under the trash directory because that name is a control prefix, and
treating that refusal as fatal made every directory purge stop before the
delete: the entry stayed on disk and the caller was told it was deleted. So
an aggregate error leaves `freed` at zero and the delete proceeds. The
trade-off is stated in the code and holds here: losing the ledger credit is
worth strictly less than losing the delete, so a refusal costs the ledger
and not the operation.

After the loop, one dirty mark on the trash directory, then the joined error
(nil when everything went).

### The trash-disabled path

When `TrashEnabled` is off, or the caller passed `permanent`, `Delete` does
not touch this file: a delete is a delete, permanently, through
`deleteResolved`. The UI says so before the destructive action; the core does
not soften it.

`ErrTrashDisabled` is the sentinel reserved for restore and purge against a
share whose trash is off: those operations name a facility the share does
not have, which is a refusal, unlike delete, which simply takes the permanent
path. The protocol layer maps it to a conflict. The rebuilt
`TrashRestore` and `TrashPurge` check the share's `TrashEnabled` flag and
return it (see Deliberate changes).

### Helpers

```go
func hexLower(b []byte) string
func int64Minus(v uint64) int64
```

`hexLower` renders bytes as lowercase hex. It exists so the id alphabet is a
guarantee of this file (no dash, ever) rather than a property borrowed from
a formatting package.

`int64Minus` negates a `uint64` size for the ledger, saturating at the most
negative value that fits in `int64`: a freed size above the signed range is
clamped rather than wrapped, so a corrupt or absurd size can never turn a
credit into a huge debit. It lives beside its callers (`TrashPurge` here,
`deleteResolved` in `write.go`); the rebuild keeps it with the delete path's
file and lets trash call it.

## Rationale

- **Flat store, path in the name.** A trash that mirrors the origin tree
  needs its own directory management, its own empty-parent cleanup, and a
  merge story when the same path is deleted twice. A flat directory with the
  origin encoded in the name needs none of that, restores with one rename,
  and makes concurrent deletes of the same path trivially safe through the
  random id.
- **Rename, not copy.** Trashing must be cheap and atomic regardless of
  size; it is the default delete path on opted-in shares and runs inline in
  the request.
- **Read to list, Create to restore, Delete to purge.** Each operation
  demands the permission of what it actually does to the live tree, rather
  than a single trash permission that does not exist in the grant model.
- **Conflict on restore.** The origin may have been recreated since the
  delete. Overwriting it would destroy newer data with older data on a code
  path the user thinks of as "undo"; refusing with a conflict pushes the
  decision to the caller, which is the only party that can make it.
- **Credit at purge.** The ledger tracks bytes on disk. Trashed bytes are on
  disk.
- **Errors joined at purge.** Batch semantics without silent success. The
  bug history above is the reason this is a contract and not a style choice.

## Deliberate changes

- **`ErrTrashDisabled` is actually returned.** The current code defines the
  sentinel and the API layer maps it, but `TrashRestore` and `TrashPurge`
  never check the share's flag, so against a share whose trash was turned
  off they operate on whatever `.sctrash` content remains. The rebuild adds
  the check at the top of both (after the permission check): a disabled
  share answers `ErrTrashDisabled`. `TrashList` keeps answering (an empty or
  leftover listing is harmless and lets a UI show what would be lost by
  purging out of band); delete behavior is unchanged.
- Everything else is a straight re-specification: layout, names, encoding,
  permissions, timestamps, conflict rules, quota timing, and error joining
  are carried over as observable behavior.

## Tests

Written fresh against the new API. At minimum:

Naming and encoding:
- `encodeOrigPath`/`decodeOrigPath` round-trip nested paths, names with
  dashes, dots and spaces, and non-ASCII names.
- `decodeOrigPath` reports false for a bare basename, invalid base64, and
  bytes that decode to an invalid path.
- `splitTrashName` splits `{id}-{encoded-with-dashes}` on the first dash;
  a name with no dash reports not-ok.
- `hexLower` output is lowercase hex with no dash for random inputs.
- `int64Minus` on small values, on `1<<63 - 1`, and on values above it
  (saturation, never wrap to positive).

trashMove (through Delete on a trash-enabled share):
- The origin is gone, exactly one entry exists under `.sctrash`, and its
  name decodes back to the origin path.
- `.sctrash` never appears in a root listing.
- Two deletes producing the same origin path coexist as two entries.
- No quota movement at delete time on a trash-enabled share.
- Deleting on a trash-disabled share, and deleting with `permanent`, remove
  for good and credit the quota.

TrashList:
- Requires Read; a caller without it is refused.
- Empty share with no `.sctrash`: empty listing, no error.
- Entries carry id, leaf display name, origin path, kind, size.
- A legacy bare-basename entry lists with the raw suffix as `Name` and an
  empty `OrigPath`.
- `DeletedAtNs` reflects the delete time, not the file's mtime: create a
  file with an old mtime, delete it, assert the listed time is recent.
- A non-conforming name (no dash) in the directory is skipped.

TrashRestore:
- Requires Create.
- Restores to the exact origin path; content intact.
- Origin's parent directories deleted after the trashing are recreated.
- Occupied destination answers `ErrConflict` and the trash entry survives.
- Unknown id answers `ErrNotFound`.
- Legacy entry restores to the share root under its basename.
- Disabled share answers `ErrTrashDisabled` (deliberate change).

TrashPurge:
- Requires Delete.
- Single id removes only that entry; nil removes all.
- File purge credits the quota by the file size; directory purge credits by
  the recursive size.
- A directory entry purges successfully even when the aggregate read
  refuses (freed stays zero, the delete happens).
- One failing entry (arranged unremovable) yields a non-nil joined error
  while the other entries are still removed.
- Missing `.sctrash` answers success.
- Disabled share answers `ErrTrashDisabled` (deliberate change).
