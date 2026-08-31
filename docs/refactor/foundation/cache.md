# Foundation: cache

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/store/cache` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## Purpose

`engine/store/cache` is the rebuildable half of the store: everything in it
can be walked back out of the filesystem, and deleting the file is a
supported operation, not an incident. It answers two questions a POSIX
filesystem will not answer on its own:

1. A stable id for a file across renames, which a sync client keys its
   whole local journal on.
2. Whether anything under a directory changed, without a full crawl.

Every method here is allowed to answer "I do not know" and have the caller
fall back to the filesystem. Nothing in this package, or in anything that
calls it, may treat a missing row as evidence that a file is missing.

This document consumes the `Ident` decision from `foundation/state.md`: the
identity tuple moves to a neutral package, and this document specifies the
cache's id-derivation and directory-etag contracts against that one type
rather than a locally defined one.

## Spec: identity and the neutral `Ident`

Per `audit/foundation-persistence.md` (`store/cache` finding 1, `store/state`
finding 2), the identity tuple (`Share`, `Dev`, `Ino`, `Btime *int64`) is
implemented three times across the current tree: `cache.Ident`,
`state.Ident` (a second, separately named type with the same four fields
but `Share` typed as `int64`), and `favorite.Favorite`'s inline fields.
Each carries its own `toSQL`/`fromSQL` bit-pattern reinterpretation, each
with its own repeated `//nolint:gosec` comment.

**Decision: `Ident` moves to `engine/store/ident`, a small, dependency-free
package below both `state` and `cache`.**

```go
package ident

type Ident struct {
    Share vfs.ShareID
    Dev   uint64
    Ino   uint64
    Btime *int64 // nil means the filesystem reported no birth time
}

func Of(share vfs.ShareID, st vfs.Stat) Ident
func (i Ident) Equal(o Ident) bool
func (i Ident) ToSQL() (dev, ino int64, present, btime int64)
func FromSQL(share, dev, ino, present, btime int64) (Ident, error)

// FileID and Assignment travel with Ident rather than living in `cache`,
// because the Overrides seam (below) crosses from `state` into `cache`
// carrying exactly these two shapes: `state.RecordFileIDs` and
// `state.LookupFileIDOwner` need to name a file id and an assignment
// without importing `cache` for them, the same reason `Ident` itself
// moved here. A type that only `cache` used would belong in `cache`; a
// type both databases need to spell belongs in the neutral package both
// already import.

type FileID int64

// RootID is the "no id" sentinel: never a real node's id, and also the
// parent id of a share root, so a parent-chain walk terminates on it
// without a sentinel row ever existing in any table.
const RootID FileID = 0

// Assignment is one identity and the id it holds, which is what a
// collision makes durable.
type Assignment struct {
    Ident Ident
    ID    FileID
}
```

`FileID` is a node's stable id, derived (by `cache.DeriveID`, below) as a
pure function of an `Ident`. `Assignment`'s fields are exactly what a
collision resolution needs to make durable: the identity that was
displaced or that displaced another, and the id it was assigned to hold
(`cache.AllocateID`'s collision path, below, is what constructs one).

Justification for `store/ident` over folding it into `dbfile`:

- `dbfile` is deliberately opinion-free about what any of the three
  databases store (`foundation/dbfile.md`); adding a domain-shaped type
  (`vfs.ShareID`, a filesystem birth time) to it would make it hold an
  opinion about identity that two of its three consumers (`journal`) do
  not share.
- `cache` and `state` are peers, and today's violation is that one imports
  the other for exactly this type (survey, "one smell inside the layer").
  A new, tiny, downward-only package that both import removes the
  direction question entirely rather than deciding which of the two
  "wins".
- The package is deliberately minimal: the identity tuple, equality, the
  two SQL-boundary conversions, and the two small shapes
  (`FileID`, `Assignment`) the `Overrides` seam carries across the same
  boundary. It imports only `vfs` (for `ShareID`) and holds no database
  handle and no SQL statement of its own, so it is a foundation leaf like
  `kit`, not a third database package.

`Btime` stays a pointer for the reason the current code documents: an
absent birth time and a zero one are different facts about a file, and
folding them together would let two distinct files share a derivation key.
Because of this, `Ident` is not `==`-comparable and must never be used as a
map key; `Equal` is the value comparison every caller uses instead.

This document's remaining sections use `ident.Ident`, `ident.FileID`,
`ident.RootID` and `ident.Assignment` throughout, with no re-export under
a `cache`-local name; the state document specifies where `dav.go` and
`favorite.go` adopt `ident.Ident`, retiring their own copies.

## Spec: id derivation

### DeriveID

```go
func DeriveID(id ident.Ident, attempt uint32) ident.FileID
```

The id is a pure function of the identity tuple: a rebuilt cache derives
the same ids for the same files, which is what lets a sync client's local
journal survive the cache being deleted.

The derivation folds `derivationPrefix || share || dev || ino ||
btime-flag || btime || attempt` through SHA-256, takes the first 8 bytes
big-endian, clears the top bit, and reduces modulo the 63-bit mask, adding
1 so the result is never zero. `derivationPrefix` domain-separates this
hash from every other hash in the system and versions the derivation
itself: changing the prefix changes every id in the deployment, which is
why it is a named constant with its version in the string
(`"stowcloud/fileid/v1"`), not a bare byte string.

`attempt` is zero in the ordinary case. It exists purely so that a second
file whose identity collides with a first at attempt zero can be re-derived
into a different id by incrementing it; nothing outside this package
chooses a value.

The 63-bit width (not 64) is a deliberate requirement, preserved as-is: a
sync client consumes the id as a signed 64-bit integer, and an id with the
top bit set would arrive at some clients as negative.

### AllocateID and collision handling

```go
func (d *DB) AllocateID(ctx context.Context, tx *sql.Tx, id ident.Ident) (ident.FileID, error)
```

The only function that may decide an id. In order:

1. Consult the `Overrides` interface (below) first, always: a past
   collision decision is authoritative and is never revisited, because
   which file "won" the base id was an insertion-order decision at the
   time and a rebuild does not reproduce insertion order.
2. If no override exists, derive at `attempt = 0`. If that candidate is
   free (no live node holds it, and no override reserves it for a
   different identity), it is the answer.
3. If the candidate is held by a different identity, walk `attempt` upward,
   bounded by `maxAttempts = 64`, deriving and checking each candidate.
   Reaching the bound is a hard error: it means the derivation itself is
   not distributing, a different and worse problem than one collision,
   and must not be hidden behind a retry loop that never terminates.
4. On the first free candidate past attempt zero, both the base holder's
   identity and the newcomer's identity are recorded as an `ident.Assignment`
   in one write to the durable `Overrides` table, and a `slog.Warn` is
   emitted: the event is worth an operator's attention as the first sign a
   corpus has reached the size where a 63-bit collision is no longer
   abstract, not because anything is broken.

Both sides of a collision are recorded, not just the newcomer, because
recording only the newcomer only reproduces a two-file collision. After a
cache rebuild, a third file colliding with the same base identity would
walk the tree, find the original base holder's id unclaimed (nothing
durable said otherwise), and take it, disagreeing with the earlier
rebuild's answer.

The override write happens inside the cache's own write transaction and
commits to the durable half before the node row using the id does. This
ordering is load-bearing: a crash between the override write and the node
insert leaves a reservation with no node, which is the state every rebuild
starts from anyway and is harmless. The reverse order (node first) would
let a node hold an id nothing durable recorded, and the next rebuild would
race the same collision again with no memory of the first outcome.

`maxResolveHops = 8192` (below, in resolve) and `maxAttempts = 64` here are
both explicit, named bounds against corrupt or cyclic stored data, not
incidental implementation details; both are preserved as hard requirements.

### The Overrides boundary

```go
type Overrides interface {
    LookupFileID(ctx context.Context, id ident.Ident) (ident.FileID, bool, error)
    LookupFileIDOwner(ctx context.Context, id ident.FileID) (ident.Ident, bool, error)
    RecordFileIDs(ctx context.Context, assignments ...ident.Assignment) error
}
```

`cache` depends on this abstract interface rather than importing
`engine/store/state` directly. This is the correct half of the
`cache`/`state` relationship and is preserved unchanged
(`audit/foundation-persistence.md`, `store/cache` finding 2): the override
table is durable data (a past collision decision must survive a cache
rebuild), so it belongs in `state`, but `cache` must not import `state` to
reach it, both because that would make the rebuildable half depend on the
durable half's whole surface, and because it is exactly the wrong direction
for a database whose entire purpose is to be safely deletable. `state`
implements `Overrides` and is handed to `cache.New` as a value; `cache`
never imports `state`.

### Upsert and Lookup

```go
func (d *DB) Upsert(ctx context.Context, tx *sql.Tx,
    share vfs.ShareID, parent ident.FileID, name string, st vfs.Stat) (ident.FileID, error)
func (d *DB) Lookup(ctx context.Context, share vfs.ShareID, st vfs.Stat) (ident.FileID, bool, error)
```

`Upsert` is the only function that inserts into the node table, which
makes id allocation lazy: a deployment that never asks for a stable id
creates no rows at all. Behavior:

- An existing row for the identity is found by (`share`, `dev`, `ino`,
  `btime`), using one of two prepared statements depending on whether
  `Btime` is present: the planner cannot prove a bound parameter is
  non-NULL, so a single `btime_ns = ?` statement would miss both partial
  indexes and fall back to a full scan on every lookup. Splitting the
  statement, not the index, is what keeps a cold walk of a large tree at
  one index seek per file instead of one scan.
- If found and something moved (parent, name, or the directory flag), the
  row is updated (`moveNode`): this is the cache catching up with a rename
  or an out-of-band write, not a rename this server performed (which goes
  through `Rename` below, one row for the whole subtree since it moves
  under a stable id).
- If found and nothing moved, only size and mtime refresh (`touchNode`),
  sparing a stat per entry on every listing.
- If not found, `EnsureWritable` is checked first (a new row is what can
  grow the file; refreshing an existing one cannot), then `AllocateID`
  mints an id and the row is inserted.

`Lookup` is the non-allocating counterpart: it answers whether a file
already has a stable id, with no side effect of minting one if it does
not. Used by `markDirty` (core, `09-quota-and-aggregates.md`) to decide
which ancestors have a cached aggregate worth invalidating: a directory
with no cached row has nothing stale to mark.

## Spec: directory etags

### Aggregate

```go
type Aggregate struct {
    Etag   string
    RSize  uint64
    RCount uint64
}
```

This package stores and serves a directory's cached rollup; it does not
compute one. Computing an aggregate (walking children, hashing) is the
core's job (`09-quota-and-aggregates.md`); this package answers "do you
have a fresh one" and "here, store this one".

### DirEtag

```go
func (d *DB) DirEtag(ctx context.Context, share vfs.ShareID, id ident.FileID) (Aggregate, bool, error)
```

Returns `(_, false, nil)` when the caller must recompute: no row exists,
the row is marked dirty, or the row's stamped generation no longer matches
the share's current generation. A stale-generation row is never returned
as fresh; the caller cannot distinguish "never computed" from "computed
against an old generation" and does not need to, since both mean
recompute.

### PutDirEtag and MarkDirty

```go
func (d *DB) PutDirEtag(ctx context.Context, tx *sql.Tx,
    share vfs.ShareID, id ident.FileID, agg Aggregate, gen uint64) error
func (d *DB) MarkDirty(ctx context.Context, tx *sql.Tx,
    share vfs.ShareID, chain []ident.FileID) error
```

Both are **explicitly not gated by the size guard**, and this is a
deliberate asymmetry from every other write in this package, stated so a
future maintainer does not "fix" it into consistency. Refusing
`PutDirEtag` under the guard would leave a cached aggregate that is stale
and still flagged valid: the guard exists to stop the database from
growing, but the cost of refusing here is a client being told nothing
changed when it did, which is a wrong answer, not a saved page. The same
argument applies to `MarkDirty`: refusing it under the guard leaves a
directory's cached rollup wrongly marked fresh after a real write, which is
worse than the extra row `MarkDirty` writes.

`MarkDirty` invalidates every id in a chain (normally a node's ancestors up
to the share root). An id with no row yet gets a placeholder row that is
already invalid, so the next read of that id correctly answers "recompute"
rather than erroring on a missing row.

### BumpShareGen and ShareGen

```go
func (d *DB) BumpShareGen(ctx context.Context, tx *sql.Tx, share vfs.ShareID) (uint64, error)
func (d *DB) ShareGen(ctx context.Context, share vfs.ShareID) (uint64, error)
```

The generation scheme is the O(1) whole-share invalidation mechanism: every
cached aggregate row is stamped with the share generation at the time it
was written. Bumping the counter (an upsert that increments, or inserts at
1 for a share never bumped before) makes every row stamped with an older
generation read as stale on its next lookup, without walking the tree or
naming a single path. `ShareGen` for a share never bumped answers 0, which
every row's `gen` column can legitimately equal (a freshly created row is
also stamped 0), so a generation of 0 is not itself a "never invalidated"
sentinel; freshness is `valid == 1 AND gen == current`, not `gen != 0`.

The core's watcher calls `BumpShareGen` when it loses a batch of
filesystem events and can no longer say which paths changed
(`09-quota-and-aggregates.md`, `InvalidateShare`); this is the coarse,
always-correct fallback the fine-grained `MarkDirty` degrades to when it
cannot be precise.

## Spec: resolve

```go
func (d *DB) Resolve(ctx context.Context, id ident.FileID) (vfs.ShareID, vfs.SharePath, error)
```

Walks the parent chain from `id` to the share root, joining names in
reverse. There is no path column on the node table and there will not be
one: a directory rename is one `UPDATE` of the renamed row because of this,
instead of a write for every row underneath it.

The walk is bounded at `maxResolveHops = 8192`. A cyclic parent chain
should never happen, and "should not happen" is not a proof for data a
disk or a bug could have corrupted; the bound turns a corrupt chain into an
explicit error instead of an infinite loop. This bound is preserved as a
hard requirement, not an implementation detail to relax.

The walk is not a snapshot: a rename landing mid-walk can produce the path
the tree had at some point during the walk, which every caller must
tolerate, because the filesystem is the truth and this is a hint. A
resolved component that this server's own path grammar would refuse (a
reserved prefix, for example, from a name written by another program
sharing the directory) is an error rather than a silently repaired string;
the trust boundary here is stored data that this process did not
necessarily write.

```go
func (d *DB) Rename(ctx context.Context, tx *sql.Tx, id, newParent ident.FileID, newName string) error
```

Renaming under a stable id is one row update regardless of subtree size,
which is the entire reason the id exists. It is not gated by the size
guard: the row already exists, so a rename cannot grow the file, and
refusing it under the guard would leave the id pointing at a stale path,
which is a worse outcome than a database slightly larger than the floor
allows.

## Spec: rebuildability

`cache.Spec` sets `Rebuildable: true`, the only one of the three databases
that does. This is what lets a schema-changing migration discard the
table's contents outright (`Discard: true`, `foundation/dbfile.md`) instead
of migrating rows forward, and it is what makes deleting `cache.db` a
supported operator action rather than an incident:

- A rebuild after deletion re-derives the same `ident.FileID` values for the
  same files, because the derivation is pure over the identity tuple.
- The `Overrides` table (durable, in `state`) is what makes a rebuild also
  reproduce past collision resolutions rather than re-deciding them by
  insertion order, which a rebuild's walk order does not reproduce.
- Directory etags are lost and recomputed on next read; this costs one
  slower listing, not a wrong answer, because `DirEtag` degrades to "I do
  not know" and the core recomputes.

`cache.DB` deliberately has no `Close` method of its own. Once `New`
succeeds, its prepared statements are invalidated automatically when the
parent `dbfile.DB`'s pool closes; Go's `database/sql` handles this without
leaking a descriptor. This document states it explicitly because there is
no `Close` symbol to point a reader at otherwise (`audit/
foundation-persistence.md`, `store/cache` finding 6).

## Rationale

- **Rebuildability is the organizing property.** Every design choice in
  this document (pure derivation, override durability living in `state`,
  degrade-to-recompute on any doubt) exists to keep the promise that
  deleting this file is safe. A method that could not honestly answer "I
  do not know" would break that promise the first time it was wrong.
- **Split statements over one that covers both NULL and non-NULL.** SQLite
  cannot use a partial index against a bound parameter it cannot prove is
  non-NULL; two statements against two partial indexes is a seek, one
  general statement is a scan, and the difference is per-file on a cold
  walk of a large tree.
- **Both collision holders recorded.** Recording only the newcomer answers
  a two-file collision correctly and a three-file collision incorrectly on
  a later rebuild; recording both is what makes the override table an
  actual authority rather than half of one.
- **The generation scheme over per-row invalidation.** A whole-share
  invalidation that had to touch every cached row would be exactly the
  O(n) operation the watcher's "lost a batch of events" case needs to
  avoid; a counter bump is O(1) regardless of how many rows exist.

## Deliberate changes

1. **`Ident`, `FileID`, `RootID` and `Assignment` all move to
   `engine/store/ident`.** See "Spec: identity" above. `Ident` alone
   moving is not enough to end the `store/state` -> `store/cache` import:
   the `Overrides` interface still carries `FileID` and `Assignment` at
   its two call sites (`state.RecordFileIDs`, `state.LookupFileIDOwner`),
   so those two types move with `Ident` to the same neutral package. This
   retires the three separate identity representations the audit found
   (`audit/foundation-persistence.md`, `store/cache` finding 1 and
   `store/state` finding 2) and, together, are what removes the
   `store/state` -> `store/cache` import (`01-package-survey.md`
   cross-layer violation 5). `state.md` specifies the corresponding change
   on the `state` side (`dav.go`, `favorite.go`, the `Overrides`
   implementation).
2. **The two id-derivation and one bit-pattern helper pair
   (`toSQL`/`fromSQL`) become methods on `ident.Ident`
   (`ToSQL`/`FromSQL`)**, ending the three independent
   `//nolint:gosec`-commented reimplementations the audit found
   (`store/cache` finding 4).
3. **Documentation split into two sections** (id derivation, directory
   etags), matching the audit's organizational note (`store/cache` finding
   8), with no change to either contract.
4. Import path moves from `internal/store/cache` to `engine/store/cache`.

No other observable behavior changes: the derivation algorithm, the
collision-recording rule, the generation scheme, and the resolve bound are
unchanged from the reference.

## Tests

- `DeriveID` is pure and deterministic: same identity, same id, across
  process restarts (no I/O in the test).
- A forced collision at `attempt = 0` (two identities crafted or mocked to
  derive the same 63-bit value) resolves via `AllocateID`: the second
  identity gets a different id, both holders are recorded via
  `RecordFileIDs`, and a rebuild (fresh cache, same override rows) derives
  the same pair of ids again.
- A three-way collision after a rebuild does not let the third file steal
  the first file's id (the finding 4 regression).
- `maxAttempts` exhaustion is a hard error, not a hang (force via a stub
  derivation or a narrowed `bits` in a test-only constructor).
- `Upsert` on an unseen identity allocates; on a moved file it updates
  parent/name/flags; on an untouched file it only refreshes size/mtime
  (assert via row inspection, not id equality alone).
- `Upsert` of a new row refuses under `EnsureWritable() != nil`; `Upsert`
  of an existing row (refresh path) does not.
- `Lookup` never allocates: an unseen identity answers `(_, false, nil)`
  with no row created.
- `Resolve` on a cyclic parent chain (constructed directly against the
  table in a test) errors at `maxResolveHops`, not before and not by
  looping forever.
- `Resolve` on a resolved path this server's own grammar would refuse
  (a reserved-prefix component written into the table directly) errors.
- `DirEtag` answers false for: no row, a dirty row, and a row stamped with
  a superseded generation; true only when all three conditions clear.
- `PutDirEtag` and `MarkDirty` both succeed with `EnsureWritable()` in the
  blocked state (the deliberate non-gating).
- `BumpShareGen` invalidates every existing row for that share and no
  other share's rows (cross-share isolation).
- `Rename` on an id with no row is `ErrNoNode`, not a silent no-op.
- Deleting the cache file and reopening it re-derives every id from a
  fixture tree identically to the first pass (the rebuildability
  end-to-end test).
