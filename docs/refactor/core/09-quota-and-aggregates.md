# 09: Quota and aggregates

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here `quota.go` and `aggregate.go`) is referenced as a
> behavioral specification only. The new implementation is written completely
> from scratch; nothing is copied.

## Purpose

Two files of the rebuilt domain package:

- `engine/service/core/quota.go`: the two quota mechanisms. The free-space floor
  (`FreeSpace`) and the per-user byte ledger (`QuotaSink`, `AttachQuotaSink`,
  `chargeQuota`).
- `engine/service/core/aggregate.go`: the recursive directory rollup (`Aggregate`,
  `computeAggregate`, `ensureFileIDChain`, `upsertDir`), invalidation
  (`InvalidateShare`, `markDirty`).

The SQL implementation of the ledger leaves the core in this rebuild. See
"Deliberate changes".

## Spec: quota

Quota is two different mechanisms under one word, and both must survive the
rebuild. Losing one is how a port answers the same protocol question with
only half the feature.

1. The free-space floor is what the filesystem has left. It is reported to
   clients as RFC 4331 disk space and refuses a write that would fill the
   disk. It is not per account.
2. The per-user byte quota is a cap on an account plus a running ledger of
   what the account has used; both are columns on the user row. It is
   enforced through a reserve-then-commit seam so two concurrent uploads
   cannot both pass a check against the same headroom. The compat layer
   reports it to clients, so it is client-visible as well as enforced.

### FreeSpace

```go
type FreeSpace struct {
    Used      uint64 // what the filesystem holds, including root-reserved blocks
    Available uint64 // what an unprivileged writer can consume
    Total     uint64 // the filesystem's whole size
}

func (c *Core) FreeSpace(ctx context.Context, r Resolved) (FreeSpace, error)
```

Behaviors:

- Requires `acl.Read` on the resolved path; a refusal is the `Require` error.
- Asks the VFS for the space at `r`'s own path, not at the share root. The
  statfs must resolve to the filesystem holding the path asked about: a share
  with a RAID array mounted at one of its subdirectories has two filesystems
  in it, and a client asking about the RAID folder must be told the RAID's
  numbers, not the numbers of the filesystem the share root happens to sit
  on.
- `Available` is `f_bavail`, never `f_bfree`. The root-reserved blocks in the
  difference are not ours to write into, and reporting them invites a write
  that then fails.
- `Used` includes the root-reserved blocks; it is what the filesystem holds.
- A VFS error crosses through the standard VFS error mapping (01-errors.md).

### QuotaSink

The ledger side is an attachable interface. A `Core` with no sink attached
enforces nothing, which is a legitimate quota-less deployment state, not a
degraded one.

```go
// QuotaSink is the per-user byte ledger. The store layer implements it; the
// core only consumes it. User ids cross as int64 so the implementation does
// not have to import this package.
type QuotaSink interface {
    // Reserve atomically books additional bytes against user, if the cap
    // allows it. ok reports whether the booking landed; ok false with a nil
    // error means the user is at the cap. It must be a single guarded
    // update, never a read-then-write.
    Reserve(ctx context.Context, user int64, additional uint64) (ok bool, err error)

    // Commit settles a reservation whose write succeeded. The bytes were
    // already booked by Reserve, so Commit is idempotent; it exists to keep
    // the caller's intent explicit at the call site.
    Commit(ctx context.Context, user int64, additional uint64) error

    // Release returns reserved bytes whose write did not land, and credits
    // bytes freed by a permanent delete. The same operation serves both,
    // which is why it takes a non-negative magnitude with the direction
    // fixed: Release only ever credits.
    Release(ctx context.Context, user int64, delta int64) error
}
```

Contract, stated so the store side can be built independently:

- **Reserve** books `additional` bytes in one atomic step. Concurrency is the
  whole point: N concurrent uploads racing for the same headroom must not
  all pass. A read-then-write (SELECT the usage, compare, UPDATE) lets every
  racer observe the same headroom and all proceed; the implementation must
  instead be one guarded UPDATE whose WHERE clause carries the cap check, so
  the database's write serialization is the mutual exclusion. The reference
  statement shape:

  ```sql
  UPDATE user
  SET usage_bytes = usage_bytes + ?
  WHERE id = ?
    AND (quota_bytes IS NULL OR usage_bytes + ? <= quota_bytes)
  ```

  One affected row is success. Zero affected rows is a refusal (`ok` false,
  `err` nil): either the user is at the cap or the user row does not exist,
  and the two are deliberately not distinguished, because a missing user has
  no headroom either. A NULL `quota_bytes` is an unlimited account and always
  reserves. `additional` values that do not fit in the ledger's signed column
  are an error, not a refusal.

- **Commit** may be a no-op when Reserve already booked durably (the SQL
  implementation's case). It must be idempotent and must never fail a write
  that has already landed on disk.

- **Release** subtracts `delta` from the usage, clamped at zero:

  ```sql
  UPDATE user SET usage_bytes = max(0, usage_bytes - ?) WHERE id = ?
  ```

  The clamp exists because the ledger can drift low (a crash between a
  filesystem delete and its credit), and recovery must never push the usage
  negative. A zero delta is a no-op. The contract is a non-negative `int64`
  magnitude, credit only; a negative argument to `Release` itself is a
  caller bug, reported instead of silently booking.

- All three methods run on the durable state database's serialized write
  path.

### AttachQuotaSink

```go
func (c *Core) AttachQuotaSink(sink QuotaSink) error
```

One-shot. A second call returns an error: re-attaching a sink at runtime is a
wiring bug in the server's startup, not a runtime condition, and silently
replacing a ledger mid-flight would orphan reservations.

### chargeQuota

```go
func (c *Core) chargeQuota(ctx context.Context, user UserID, delta int64)
```

The internal settlement helper the write and delete paths call after the
filesystem change has committed. Behaviors:

- No sink attached, or a zero delta: no-op.
- A **negative** delta is bytes freed (a delete): it credits the ledger
  through `Release` with the magnitude, `-delta`. Callers pass a negative
  delta already saturated for the signed range (`int64Minus(freed)` in
  06-mutations.md and 08-trash.md).
- A **positive** delta is bytes grown (`PublishPart` replacing a smaller
  file with a larger one, `deltaOf` in 06-mutations.md): it books the grown
  bytes through `Reserve`, best-effort, since the write has already
  committed and the request cannot be failed over a booking refusal.
- Best-effort in both directions: an error, or on the booking side a
  refusal (`ok` false), is logged as a warning ("settling the quota
  ledger failed; the filesystem change has committed") and swallowed. The
  filesystem change is already durable; failing the request over ledger
  drift would report an operation as failed that in fact happened, which
  is worse than drift. On the credit side drift is bounded and
  self-correcting only downward (`Release` clamps at zero); on the
  booking side a refused best-effort charge for a grown file undercounts
  the account's usage rather than overcounting it, so the failure mode
  never blocks a legitimate later write on ledger drift it did not cause.

## Spec: aggregates

### Aggregate

```go
type Aggregate = cache.Aggregate // Etag string; RSize, RCount uint64

func (c *Core) Aggregate(ctx context.Context, share ShareID, p vfs.SafePath) (Aggregate, error)
```

Returns the recursive rollup for a directory: its ETag, and the size and
count of everything beneath it. The result is cached in the rebuildable
cache database and invalidated by generation bump or dirty-marking.

Behaviors, in order:

1. An unregistered share is `ErrNotFound`.
2. The share's current generation is read (`ShareGen`); every cached row this
   call writes is stamped with it, and a bumped generation makes every older
   row read as stale.
3. `ensureFileIDChain` walks from the share root to `p`, allocating (or
   reusing) a stable file id for every directory component on the way. The
   share root itself is the sentinel `ident.RootID`; no row is ever inserted
   for it, but its rollup is still cached under the sentinel. Directory ids
   are allocated lazily here because computing a rollup is the one thing
   that needs a directory's stable id to exist, so that is where it is
   minted. Each component is joined with `JoinExisting`, stat'ed, and
   upserted with its parent id and name.
4. `computeAggregate` runs the recursive rollup (below).

### computeAggregate

The recursion carries: the share root handle, the share id, the directory
path and file id, the generation, a `held` list of file ids the current call
chain has locked, and a per-call map of single-flight guards keyed by file
id.

Per directory:

1. **Cache read.** A fresh cached row (right generation, not dirty) is
   returned as-is.
2. **Single-flight guard.** A mutex per file id, from the per-call guard
   map, is acquired so only one branch of the walk computes a given
   directory's rollup. The guard map lives for one top-level `Aggregate`
   call; it serializes the recursive walk against itself, which matters
   because a native directory and a hard-linked alias can share one file id
   and appear twice in one walk. Re-entrant acquisition is skipped: if the
   id is already in `held`, the lock is not taken again, which both avoids
   self-deadlock and bounds a hard-link cycle (the second visit reads the
   cache or recomputes without descending forever behind its own lock).
3. **Recheck under the guard.** The cache is read again after acquiring:
   the branch that held the guard first may have stored the answer.
4. **Stat.** The path must be a directory; anything else is `ErrNotFound`.
5. **List.** `ReadDir` with reserved names hidden. Child names are sorted in
   ascending byte order; the hash is over an ordered sequence, so the order
   is part of the ETag's definition and must be deterministic.
6. **Fold.** A blake3 hasher (32-byte output) is fed, per child in sorted
   order, the child's name then the child's etag:
   - A child directory gets its own id via the same upsert, recurses, and
     contributes its aggregate's etag, recursive size and recursive count.
   - A child file contributes its `FileETag` (02-etag), its size, and a
     count of one.
   - A child that vanishes between the listing and its stat (or whose name
     no longer joins) is skipped, not an error: the listing races the world
     by design.
7. **Result.** `Etag` is the hex of the first 16 bytes of the blake3 sum;
   `RSize` and `RCount` are the folded totals.
8. **Store, best-effort.** `PutDirEtag` writes the row stamped with the
   generation. A store failure is logged as a warning and the computed value
   is returned anyway: a rollup that cannot be cached is a slower next
   listing, not a failure, and the walk committed nothing that needs
   rolling back.

Concurrent top-level `Aggregate` calls may duplicate work (each call has its
own guard map). That is accepted: the computation is idempotent, the cache
write is last-writer-wins on identical values, and a cross-call lock table
would be shared mutable state serving only a rare race.

### upsertDir

```go
func (c *Core) upsertDir(ctx context.Context, share ShareID, parent ident.FileID,
    name string, st vfs.Stat) (ident.FileID, error)
```

One cache write transaction around the cache package's `Upsert`, which
allocates a stable id for (share, parent, name, identity) or returns the
existing one. Errors propagate to the caller; the rollup cannot proceed
without ids for its directories.

### InvalidateShare

```go
func (c *Core) InvalidateShare(ctx context.Context, share ShareID) error
```

The O(1) whole-share invalidation: bump the share's generation counter so
every cached aggregate reads as stale on its next lookup, without walking or
naming a single path. The filesystem watcher calls this when it loses a
batch of events and can no longer say which paths changed.

### markDirty

```go
func (c *Core) markDirty(ctx context.Context, share ShareID, p vfs.SafePath)
```

Called from every mutation after the write commits. Marks the ancestor chain
of `p` (up to and including the share root) dirty so their cached aggregates
are recomputed on next read. Behaviors:

- An unregistered share is a silent no-op (the share vanished under a racing
  admin action; there is nothing left to invalidate).
- The share root's id (`ident.RootID`) is pushed unconditionally: it is a
  sentinel with no row, so it cannot be looked up, and the root's aggregate
  always covers the change.
- The remaining chain is `p.Parent()`'s components, walked from the root:
  each prefix is joined, stat'ed, and looked up in the cache with the
  non-allocating `Lookup`. Only ids that already have rows are marked; a
  directory with no cached row has no stale aggregate to mark. A component
  that fails to join stops the walk; a component that fails to stat is
  skipped.
- The whole chain is marked in one cache write (`MarkDirty`).
- Best-effort with a two-level fallback: the filesystem write already
  committed, so a failure here must not fail the request. If `MarkDirty`
  fails, the fallback is `InvalidateShare` (correct but coarse: every
  aggregate in the share recomputes). If that also fails, an error is logged
  stating that cached directory ETags may be stale; a later generation bump
  or recompute corrects it.

## Rationale

- **Two mechanisms, one word.** The free-space floor and the ledger answer
  different questions (what the disk has, what the account may use) through
  different protocols, and a rebuild that merges them or drops one breaks a
  client that asks the other question.
- **Guarded UPDATE over read-then-write.** The reserve is the only place the
  cap is enforced, and enforcement under concurrency is only real if the
  check and the booking are one atomic step. The WHERE clause is the check;
  the affected-row count is the answer. Everything else about the ledger
  (the no-op Commit, the clamped Release) follows from Reserve being the
  single booking point.
- **The sink is an interface; the floor is not.** There is exactly one way
  to ask a filesystem for space, so `FreeSpace` is concrete. There are two
  real implementations of the ledger (the store's SQL one and test fakes),
  and the deployment without quotas needs "no sink" to be representable.
- **Lazy id allocation.** Ids exist to key cached rollups. Minting them
  anywhere else (say, on every list) would grow the cache with rows nothing
  reads.
- **Best-effort everywhere after the commit point.** `chargeQuota`,
  `PutDirEtag` and `markDirty` all run after a filesystem change or a
  computation that is already correct. Failing the request over bookkeeping
  would report false failure; the actual cost of each failure is bounded
  (drift shown to one user, one slower listing, one stale ETag window) and
  each is stated in a log line.

## Deliberate changes

1. **The SQL ledger moves to the store layer.** The reference keeps
   `sqlQuota`, `reserveStmt` and `releaseStmt` inside the core, which means
   the domain package writes UPDATEs against the user table it does not
   own. In the rebuild, `engine/store/state` owns the implementation (a
   `Quota` value over the state DB's write path, constructed by the store
   package); `engine/service/core/quota.go` keeps only the `QuotaSink` interface,
   `AttachQuotaSink`, and `chargeQuota`. The server wires
   `core.AttachQuotaSink(state.NewQuota(db))` at startup. The statements and
   the schema knowledge live with the schema's owner.
2. **Reserve reports refusal as a value, not a sentinel.** The reference's
   `Reserve` returns `ErrQuotaExceeded` directly, which worked because the
   implementation lived in the same package as the sentinel. The store layer
   cannot import the core (the core imports the store), so the rebuilt
   interface returns `(ok bool, err error)`: `ok` false with a nil error is
   the cap refusal, and the core's write path maps it to `ErrQuotaExceeded`
   exactly once, at the call site. An error return is reserved for the
   ledger itself failing.
3. **User ids cross the interface as `int64`.** Same import-direction
   reason: `UserID` is a core type the store cannot name. The core converts
   at the seam.
4. **Sorting uses the standard library.** The reference carries a private
   insertion sort for child names; the rebuild uses `slices.Sort`. The
   observable contract (ascending byte order feeding the hash) is unchanged,
   so ETags are byte-identical.

No other observable behavior changes.

## Tests

Quota:

- `FreeSpace` refuses without Read.
- `FreeSpace` on a subdirectory with a different filesystem mounted reports
  that filesystem's numbers, not the share root's (bind-mount a tmpfs in the
  test fixture).
- `FreeSpace.Available` equals `f_bavail`, not `f_bfree`, on a filesystem
  with reserved blocks.
- `AttachQuotaSink` twice errors.
- A `Core` with no sink admits writes with no cap.
- `chargeQuota` with a failing sink logs and does not propagate.
- Store side (`engine/store/state`): Reserve refuses at the cap and admits
  below it; a NULL cap always admits; a missing user refuses; N goroutines
  racing Reserve against headroom for one admit exactly one; Commit is
  idempotent; Release clamps at zero; Release with a negative delta errors;
  Release of zero is a no-op.
- Core plus real store: an upload over quota fails with `ErrQuotaExceeded`;
  a permanent delete credits the ledger.

Aggregates:

- The rollup of a fixture tree returns the expected recursive size and
  count, and the ETag changes when any file under the tree changes, is
  renamed, appears, or disappears.
- The ETag is stable across two computations of an unchanged tree, and
  stable across cache eviction (recompute equals cached).
- Child order does not depend on readdir order (shuffle by creating in a
  different order; the ETag matches).
- A file link is refused: `Aggregate` on a file path is `ErrNotFound`;
  unregistered share is `ErrNotFound`.
- A child vanishing mid-walk (deleted between fixture setup steps) is
  skipped without error.
- A hard-link alias of a directory inside the same walk does not deadlock
  and terminates.
- The second call reads the cache (assert via a counting cache stub or by
  observing no new stats through a VFS probe).
- `PutDirEtag` failure returns the computed value and warns.
- `InvalidateShare` makes every cached aggregate recompute (bump, then
  assert a recompute happens and stale rows are not returned).
- `markDirty` after a write makes the parent chain's aggregates recompute
  while an unrelated sibling directory's cached row survives.
- `markDirty` with a failing cache falls back to `InvalidateShare`.
- The root aggregate is cached under the sentinel id and invalidated by a
  write anywhere in the share.
