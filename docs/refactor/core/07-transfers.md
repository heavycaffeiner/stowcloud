# 07: Transfers and long operations

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here `ops.go` and `operation.go`) is referenced as a
> behavioral specification only. The new implementation is written completely
> new; nothing is copied.

## Purpose

Two files own everything that moves or duplicates a subtree:

- `transfer.go`: the conflict policy type, `Move` with its cross-device and
  cross-share legs, the copy engine (`copyRecursive`, `copyFile`), the
  `WouldCopy` preflight, and `RefuseSelfDescendant`.
- `operation.go`: the long-operation surface. `Operation`, `StartCopy`,
  `CancelOperation`, `ListOperations`, the cancel gate, and the result-row
  rules for the runner's terminal paths.

In the old tree the transfer half lived in `ops.go` next to unrelated mutation
primitives, and the conflict policy type sat in `ops.go` while `operation.go`
was one of its two consumers. The rebuild closes both seams: everything that
decides what a transfer does with a taken destination is in `transfer.go`, and
everything that talks to the state store's operation rows is in `operation.go`.

## Spec: transfer.go

### OnConflict and ParseOnConflict

```go
type OnConflict uint8

const (
    ConflictFail OnConflict = iota // return ErrConflict; opens the dialogue
    ConflictRename                 // keep both: next free "name (2).ext"
    ConflictOverwrite              // replace the destination
    ConflictSkip                   // leave the destination, report done
)

func ParseOnConflict(s string) (OnConflict, bool)
```

`OnConflict` is a typed value, not a string the protocol layer compares. It
used to be a string, and a spelling the comparison did not recognise silently
became "fail", so choosing overwrite in the conflict dialogue asked the same
conflict again forever.

`ParseOnConflict` trims and lowercases before matching. Case-insensitive
because the two ends of the wire disagreed about case once already: the client
sent `"Overwrite"`, the exact comparison against `"overwrite"` fell through to
the default, same loop. Accepted spellings: empty string and `"fail"` map to
`ConflictFail`, then `"rename"`, `"overwrite"`, `"skip"`. Anything else
returns `(ConflictFail, false)`. The false return is load-bearing: a client
asking for a policy this build does not have must be refused by the caller,
never silently given a different policy.

### MoveOpts and MoveResult

```go
type MoveOpts struct {
    Overwrite  bool       // two-value legacy form of the policy
    OnConflict OnConflict // full policy; when Overwrite is set it wins
    IfMatch    *Token     // destination validator; weak is refused
}

type MoveResult struct {
    WillCopy bool         // the move had to become copy then delete
    Created  vfs.SafePath // where the entry landed (rename may differ)
    Moved    bool         // a plain rename happened
    Skipped  bool         // ConflictSkip left the taken destination alone
}
```

`MoveOpts` has an unexported `policy()` fold: `Overwrite == true` yields
`ConflictOverwrite`, otherwise `OnConflict` as given. The two-value field
exists because older callers speak it; the fold keeps exactly one switch over
the policy inside `Move`.

`Created` matters under `ConflictRename`: it is the suffixed name, not the
requested one, so the caller reports it back instead of echoing its request.
`Skipped` is distinct from both success and refusal: nothing was written.

### Move

```go
func (c *Core) Move(ctx context.Context, from, to Resolved, opt MoveOpts) (MoveResult, error)
```

Order of checks and actions:

1. `from` requires `acl.Move`; `to` requires `acl.Create`.
2. A share root on either end is refused with `ErrDenied` (wrapped through
   `errf`): a root is not movable and not a legal destination name.
3. Same share and equal path is a no-op success: `MoveResult{Created: to.path}`.
4. Stat the source through `from.root.Stat`; map errors with `mapVFSErr`.
5. Check destination existence with `pathExists` (lives in `resolve.go`).
   If taken, switch on `opt.policy()`:
   - `ConflictFail`: return `ErrConflict`.
   - `ConflictSkip`: return `MoveResult{Created: to.path, Skipped: true}`.
   - `ConflictRename`: replace `to.path` with `uniqueSiblingName` (also in
     `resolve.go`). The `Resolved` is re-pathed, not rebuilt, so the
     permission set travels with it.
   - `ConflictOverwrite`: stat the destination, run `precondition(opt.IfMatch,
     dstSt)`, and if the destination is a directory, delete it first through
     `deleteResolved` with quota crediting off. This is the RFC 4918 9.9.4
     rule: an overwrite replaces the destination, and rename cannot replace a
     directory with anything in it. The kernel answers ENOTEMPTY, which used
     to surface as a conflict on every collection move onto an existing
     collection. Deleting first is also what the specification says a copy
     does.
6. `crossesDevice(from, to, srcSt.Dev)` decides the leg:
   - Same device: `to.root.Rename(from.path, to.path, !overwriting)`. The
     no-replace flag is on unless the policy chose overwrite, so a race that
     fills the name between the check and the rename is a refusal, not a
     clobber.
   - Across a device or a share: `copyRecursive` with a nil cancellation
     gate (a move answers inline; there is no job row for anybody to mark),
     then `deleteResolved` on the source with quota crediting off. If the
     delete fails after the copy committed, return
     `errf(ErrCrossShare, "the copy completed but removing the source failed")`.
     The partial completion is reported, never silently dropped: the caller
     is told a duplicate exists.
7. On success: `markDirty` on both ends, one `journal.OpMove` row recorded
   against the source, `Created` set to the final destination path.

Note the journal shape of the copy leg: `copyFile` records `journal.OpCopy`
per file it lands, and `Move` still records its single `OpMove`. That is the
existing observable behavior and it is kept.

### WouldCopy and crossesDevice

```go
func (c *Core) WouldCopy(from, to Resolved) bool
func crossesDevice(from, to Resolved, srcDev uint64) bool
```

`WouldCopy` is the preflight a destination picker asks before letting somebody
commit: a cross-device move is a copy plus a delete, time proportional to the
data, worth a warning first. A source that cannot be stat'd answers `false`
rather than an error: the move itself reports what is wrong with it, and a
preflight that refuses is a picker that cannot open.

`crossesDevice` is the one rule the move and its preflight share:

1. Different shares always cross. Two shares are two trees whatever the
   filesystem says.
2. Otherwise compare `srcDev` against `to.root.DirDev(to.path.Parent())`.
   The comparison is against the destination's own parent directory, not the
   destination share root. A volume mounted below the root (a RAID array
   under `media/`) is a different device with different numbers; answering
   from the root would call a real boundary same-device and attempt a rename
   the kernel refuses with EXDEV.
3. A destination whose device cannot be read answers `true`: a copy across a
   boundary that was not there is slow, a rename across one that was is a
   failed move.

### copyRecursive and copyFile

```go
func (c *Core) copyRecursive(ctx context.Context, from, to Resolved, srcSt vfs.Stat, cancelled func() bool) error
func (c *Core) copyFile(ctx context.Context, from, to Resolved) error
```

`copyRecursive` duplicates a subtree. `cancelled` is polled once at the top of
every call, which makes the poll an item-boundary check: once per directory
and once per file, never inside a file. A `true` answer returns
`errOpCancelled`. `cancelled` may be nil for a copy nobody can cancel, which
is the inline cross-device leg of `Move`.

For a directory source: `Mkdir` the destination tolerating `ErrExists`, read
the source with `ReadDir(path, vfs.HideReserved)` so control names never get
copied, then per child: join both child paths (a join failure skips the
child), stat the child (a stat failure skips it; the child vanished under the
walk), build child `Resolved` values carrying each side's user, share, root
and perms, and recurse. For a file source: `copyFile`.

`copyFile` opens the source with `OpenRead(path, vfs.IntentRead)`, stats the
handle, and writes the destination through `WriteDurable` with the share
policy's file mode. Inside the durable write it runs
`vfs.CopyRange(src, 0, dst, 0, size)`: a reflink on btrfs and XFS when
aligned, an in-kernel copy otherwise. Going through `WriteDurable` means a
pre-existing destination file is replaced atomically; there is no window with
neither version present. After the write: `markDirty` on the destination and
one `journal.OpCopy` row. A close failure on the source handle is logged at
warn and not returned; the copy already committed.

### RefuseSelfDescendant

```go
func RefuseSelfDescendant(from, to Resolved) error
```

Refuses a transfer whose destination is the source or sits inside it. Without
it a directory copied into its own subtree is a walk that does not terminate:
each pass copies what the previous one wrote, until the disk is full.
RFC 4918 9.8.4 and 9.9.4 make this a 403 for WebDAV, and the native surface
wants the same answer for the same reason. Exported because the DAV layer
calls it for its own preflight.

The comparison is component-wise on the resolved paths, never on request
strings. Different shares pass. Otherwise: if the destination has fewer
components than the source it cannot be inside it; if any source component
differs from the destination component at the same index it is not inside it;
otherwise refuse with `ErrDenied` ("the destination is inside the source").
Equal length means the destination is the source itself; longer means a
descendant. Text comparison is the trap this rule exists to avoid: a string
prefix check makes `/a/bc` look like a child of `/a/b`.

## Spec: operation.go

### OperationID and Operation

```go
type OperationID int64

type Operation struct {
    ID         OperationID
    Kind       state.OpKind
    State      state.OpState
    Progress   int64
    Total      int64
    Message    string
    Results    []state.OpResult
    Attempting []string
    Pending    []string
}
```

- `Progress` and `Total` are the item counter a progress bar is drawn from.
  `Total` is zero when the size is not known until the walk ends.
- `Message` is the failure line the runner recorded; empty while running or
  on success.
- `Results` is the bounded per-item outcome set, present once the operation
  is terminal. Nothing streams during the run.
- `Attempting` is what the runner had started and never recorded an outcome
  for. It is only non-empty for an operation the process died during. Whether
  the item landed is genuinely unknown, so it is reported apart from the ones
  nothing touched.
- `Pending` is what the operation was asked for and never reached. Untouched,
  so re-running exactly these is safe, which is what lets a client offer them
  as a to-do list rather than only a count.

### Get, cancel, list

```go
func (c *Core) Operation(ctx context.Context, owner UserID, id OperationID) (Operation, error)
func (c *Core) CancelOperation(ctx context.Context, owner UserID, id OperationID) error
func (c *Core) ListOperations(ctx context.Context, owner UserID, limit int) ([]Operation, error)
```

All three are owner-scoped, and the scoping is expressed as `ErrNotFound`:
a `state.ErrNoSuchOp` and a row owned by somebody else are the same answer,
so an id-probing client learns nothing. This is the same existence rule the
resolve gate applies to paths.

`Operation` reads the row and results through `state.GetOp`, then reads
`UnfinishedOpItems` only when the state is not `OpRunning`: a running
operation has outstanding items by definition, so the split is only
interesting once it has stopped.

`CancelOperation` marks the row through `state.RequestOpCancel`. It is the
only way to cancel: disconnecting the request that created the operation does
nothing, by design.

`ListOperations` reads `state.ListOps` (newest first, bounded; the store
returns running and interrupted rows). Per-item results are deliberately not
read in the listing: they are bounded per operation, a listing would multiply
that by the page, and the screen this feeds shows progress rather than
outcomes. `UnfinishedOpItems` is read for the `OpInterrupted` rows alone,
because the listing feeds the tray's re-attach after a restart, and a row
saying only "interrupted" gives nobody anything to redo; the ordinary rows
stay one query.

### StartCopy

```go
func (c *Core) StartCopy(ctx context.Context, owner UserID, from, to Resolved, policy OnConflict) (CopyStart, error)

type CopyStart struct {
    ID      OperationID
    Dest    Resolved // where the copy will land; differs from the request under rename
    Started bool     // a job exists to poll; false for a skip
    Skipped bool
}
```

`Skipped` is a field, not an error: the destination was taken and the caller
asked for it to be left alone, which is a completed request with no job
behind it.

Order of checks and actions, all before any row exists:

1. Stat the source. This must be the source's own stat, not a zero value:
   `copyRecursive` branches on it to decide whether it is walking a tree or
   copying one file. Handing it an empty stat once said "not a directory"
   about every directory; the copy took the single-file path, failed, and the
   caller had already been answered 202. A recursive COPY over WebDAV
   produced nothing at all, with a status saying it had started.
2. `RefuseSelfDescendant(from, to)`.
3. Check destination existence and apply the policy, same switch as `Move`:
   fail returns `ErrConflict`, skip returns `CopyStart{Dest: to, Skipped:
   true}`, rename re-paths `to` through `uniqueSiblingName`, overwrite stats
   the destination and deletes it first when it is a directory (crediting
   off). The directory delete has the same reason as in `Move` but a
   different failure mode: copying into an existing directory merges the two,
   so a member the destination had and the source does not would survive a
   copy that was supposed to replace it. A destination file needs no
   pre-delete; the durable write replaces it atomically.

This is the pre-created-row design: the destination is checked before the row
is created, so a taken name is a refusal the caller can act on immediately
rather than a job that reports the conflict minutes later. It used to check
nothing at all: `copyFile` replaced whatever was there, so "duplicate" wrote
a file over itself and the conflict dialogue the client draws could never
open.

Then the row and the runner:

4. `state.CreateOp(ctx, owner, state.OpCopy, 1, clk.Nanos(), []string{to.path.String()})`.
   One item, named, so a copy interrupted mid-run can say what it was on
   rather than only that it did not finish.
5. `ctx = context.WithoutCancel(ctx)`. The request's context ends when the
   response is written and this work outlives it by design; cancelling on
   client disconnect is exactly the bug this file exists to avoid. The caller
   polls the operation row for the result.
6. `task.Go(ctx, "core: long copy", ...)`. `task.Go` is the only legal
   goroutine spawn in the tree; it installs a recover so a panic fails this
   unit of work and the process survives.

The runner:

1. `state.StartOpItem(ctx, id, 0)`; a failure is logged at warn and does not
   stop the copy.
2. `copyRecursive(ctx, from, dest, st, c.cancelGate(ctx, id))`.
3. One terminal write. Every terminal path writes the item's result row,
   because an item with no result row is read as one the runner never got to;
   a finished copy that recorded nothing would report itself done and list
   its own file as never reached. The three paths:
   - `errOpCancelled`: `FinishOp(id, state.OpCancelled, 0, "", now, nil)`.
     The cancel is the one deliberate exception to the result-row rule: what
     was written stays and nothing undoes it, so the file is genuinely in an
     unknown state and recording no outcome is the honest answer.
   - Any other error: one result row with the destination path,
     `Reason: opReasonFor(err)`, `Text: err.Error()`, then
     `FinishOp(id, state.OpFailed, 0, err.Error(), now, results)`.
   - Success: one result row with `OK: true, Reason: state.ReasonItemOk`,
     then `FinishOp(id, state.OpDone, 1, "", now, results)`.

   `FinishOp` errors are ignored on every path: the copy's own outcome is
   already the answer, and the row is best-effort bookkeeping beside it.

### errOpCancelled and cancelGate

```go
var errOpCancelled = errors.New("core: the operation was cancelled")

func (c *Core) cancelGate(ctx context.Context, id int64) func() bool
```

`errOpCancelled` ends a walk because the row was marked. It never reaches a
client; the row's own `OpCancelled` state is what says the job was cancelled.

`cancelGate` returns a closure that reads the row through `state.GetOp` and
answers its `Cancellation` flag. Reading the row is what makes a cancel reach
a copy that is already running rather than only stopping one that has not
started; without it the request was recorded and the walk ran to the end. A
row that cannot be read answers `false`: the work is real and the bookkeeping
is what failed, so a store hiccup must not abort a copy. The gate is polled
at directory and file boundaries, not continuously: the row is a database
read, and one per item is cheap beside copying the item.

### opReasonFor

```go
func opReasonFor(err error) state.OpResultReason
```

Classifies a runner failure into the stored reason a client branches on:
`ErrConflict` and `ErrExists` become `ReasonItemConflict`, `ErrNotFound`
becomes `ReasonItemNotFound`, `ErrDenied` becomes `ReasonItemDenied`,
everything else `ReasonItemFailed`. The tray opens a conflict dialogue for a
conflict and shows a message for the rest, so a copy that failed on a taken
name has to arrive as a typed conflict rather than as prose.

## Rationale

The cohesion decision, spelled out:

- **The conflict policy type lives in `transfer.go`.** In the old tree it sat
  in `ops.go` while `operation.go` consumed it as much as `Move` did, so a
  reader of either consumer opened a third file. Both consumers, `Move` and
  `StartCopy`, run the same policy switch over the same destination check;
  the type, the parser and the switch semantics are one concept and belong
  beside the copy engine both call into.
- **The long-operation bookkeeping stays its own file.** `operation.go`
  changes when the state store's op rows change: a new state, a new result
  reason, a change to how unfinished items are split or listed. None of that
  touches the copy algorithm, and a change to the copy algorithm (a new
  CopyRange strategy, a different device rule) touches nothing about rows.
  Two files, one seam, and the seam is exactly the `cancelled func() bool`
  parameter: `transfer.go` knows a copy can be told to stop, and only
  `operation.go` knows the answer comes from a row.
- Helpers keep one home each, per the overview: `pathExists` and
  `uniqueSiblingName` in `resolve.go`, `precondition` and `deleteResolved` in
  `write.go`, `mapVFSErr` in `errors.go`. `transfer.go` and `operation.go`
  call them; they do not house them.

Other rationale already embedded in the spec, kept as contract: the parent
device comparison over the share root comparison, the fail-open answer of
`WouldCopy` against the fail-closed answer of `crossesDevice`, the detached
context, the owner scoping through `ErrNotFound`, the cancel exception to the
result-row rule, and the component-wise self-descendant comparison.

## Deliberate changes

None behavioral. The changes are compositional:

- The transfer half of `ops.go` moves to `transfer.go`; the mutation
  primitives (`Mkdir`, `CreateFile`, `Rename`, `Delete`, `deleteResolved`,
  `deleteRecursive`, `precondition`, `record`) move to `write.go`; the path
  helpers (`pathExists`, `uniqueSiblingName`, `requireCreatableLeaf`) move to
  `resolve.go`. Documented in 04 and 06.
- `OnConflict`, `ParseOnConflict`, `MoveOpts`, `MoveResult` and
  `RefuseSelfDescendant` are defined in `transfer.go`.
- The stale doc comment on `StartCopy` mentioning a `vpath` parameter the
  signature does not have is not carried over. The signature stays as it is;
  a renamed destination is reported through `CopyStart.Dest`.

Everything a caller can observe (signatures, error identities, journal rows,
result-row shapes, the wire spellings `ParseOnConflict` accepts) is
unchanged.

## Tests

Written fresh against the new API. At minimum:

ParseOnConflict:
- Each accepted spelling, mixed case and surrounding whitespace, maps to its
  constant with `ok == true`; empty string maps to fail.
- An unrecognised spelling returns `ok == false` and never a different
  policy.

Move:
- Plain same-device rename: `Moved` true, `WillCopy` false, journal `OpMove`,
  both ends dirty.
- Share-root source and share-root destination each refuse with `ErrDenied`.
- Same path is a no-op success.
- Taken destination under each policy: fail returns `ErrConflict`; skip
  returns `Skipped` with nothing written; rename lands at "name (2).ext" and
  reports it in `Created`; overwrite replaces a file.
- Overwrite onto a non-empty directory succeeds (the 9.9.4 pre-delete) and
  the destination afterwards is the source's content, not a merge.
- Overwrite with `IfMatch` set is refused with `ErrPrecondition` carrying the
  current token.
- `MoveOpts{Overwrite: true}` behaves as `ConflictOverwrite`.
- Cross-share move: destination complete, source gone, `WillCopy` true.
- Cross-share move where the source delete fails (for example a read-only
  source tree arranged by the test): `ErrCrossShare`, destination intact.
- Nested-mount case: destination parent on a different device inside the same
  share is copied, not renamed (the existing
  `move_nestedmount_linux_test.go` scenario, rewritten).

WouldCopy and crossesDevice:
- Same share, same device: false. Different shares: true.
- Unstattable source: `WouldCopy` false. Unreadable destination parent
  device: `crossesDevice` true.

copyRecursive and copyFile:
- Tree copy preserves structure and content; reserved names in the source are
  not copied.
- A pre-existing destination file is replaced with no partial state visible.
- The cancellation gate returning true stops the walk at the next item and
  the error is `errOpCancelled`.
- A child that disappears between ReadDir and Stat is skipped, not fatal.

RefuseSelfDescendant:
- Onto itself: refused. Into a child: refused. Onto a sibling whose name
  extends the source's ("/a/b" to "/a/bc"): allowed. Different shares with
  identical paths: allowed.

Operation surface:
- Get, cancel and list with the wrong owner all answer `ErrNotFound`.
- A missing id answers `ErrNotFound`.
- `Attempting`/`Pending` are empty for a running operation and populated for
  an interrupted one; listing populates them only for interrupted rows.
- Listing carries no per-item results.

StartCopy:
- Directory source is walked (the zero-stat regression: a recursive copy of
  a directory produces the tree, not a failure).
- Taken destination under each policy, checked synchronously: fail is an
  immediate `ErrConflict` with no row created; skip answers `Skipped` with
  `Started` false and no row; rename reports the new leaf in `Dest`;
  overwrite of a directory pre-deletes it.
- Success: row reaches `OpDone`, progress 1, one OK result row.
- Failure: row reaches `OpFailed`, one result row whose reason matches
  `opReasonFor` for conflict, not-found, denied and generic errors.
- Cancel mid-copy: row reaches `OpCancelled` with no result rows, and the
  walk stopped at an item boundary.
- The copy survives the creating request's context being cancelled
  (detachment).
