# Foundation: dbfile

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/store/dbfile` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## Purpose

`engine/store/dbfile` is one SQLite file: its pragmas, its migration runner
and version table, and the one serialized write path every write to it
takes. Three databases use it (`engine/store/state`, `engine/store/cache`,
`engine/store/journal`) and none of them holds an opinion about any of the
three concerns above. This document specifies the package alone; it says
nothing about what any of the three databases store.

Per `audit/foundation-persistence.md`, `store/dbfile`, this package audited
clean: no defects found in the pragma ordering, the write path, the
migration runner, or the statement discipline. This document therefore
carries the design forward as a set of hard requirements rather than
proposing changes.

## Spec: the pragma set and its ordering

Two distinct sets of pragmas exist, applied at two different times, because
SQLite pragmas divide into two kinds: per-connection settings that every
connection in a pool must repeat, and database-level settings that take
effect only once, while the database has no tables yet.

### Per-connection pragmas

Applied on every connection the pool opens, in this exact order:

```
PRAGMA busy_timeout = 5000
PRAGMA journal_mode = WAL
PRAGMA synchronous = NORMAL
PRAGMA wal_autocheckpoint = 1000
PRAGMA journal_size_limit = 67108864
PRAGMA cache_size = -16000
PRAGMA temp_store = MEMORY
PRAGMA foreign_keys = ON
```

`busy_timeout` leads, and the order is a hard requirement, not a style
choice. `journal_mode = WAL` is the pragma most likely to contend for an
exclusive lock while several connections race to open the same
not-yet-existing file. Setting the timeout after it leaves the one pragma
that actually needs to wait running without a wait configured, which is
what produces a bare "database is locked" error on a fresh database under
concurrent open. Every pragma after `busy_timeout` inherits its protection
for free; nothing after it needs its own ordering rule.

`foreign_keys = ON` is listed last for readability but has to be present on
every connection: SQLite's foreign key enforcement is a per-connection
setting with no durable, once-only equivalent, unlike `journal_mode`. This
matters directly for the state database's grant-to-share foreign key
(`foundation/state.md`, the grant aggregate): a schema-level `REFERENCES`
clause enforces nothing on a connection that never ran this pragma.

### Bootstrap (database-level) pragmas

```
PRAGMA page_size = 4096
PRAGMA auto_vacuum = INCREMENTAL
```

Both take effect only while the database holds no tables. They are applied
once, on a bare connection opened before the pool exists, and never again.
Re-running them against an existing database is silently ignored by
SQLite, which is exactly what makes re-opening an existing store harmless
and what makes verifying them meaningless on a second open.

## Spec: the two-phase open

`Open` runs two phases against the same file, in order:

1. **Bootstrap.** A single, unpooled connection opens the file. It reads
   `sqlite_schema`'s object count. If the count is zero (a brand-new file),
   the two bootstrap pragmas are applied and then read back
   (`page_size`, `auto_vacuum`) to prove they landed before any table
   exists; a mismatch is a hard error naming the file, because a bootstrap
   pragma that ran after the schema is a bug that otherwise surfaces only
   as a missing space saving, months later. If the count is nonzero, the
   bootstrap pragmas are skipped (SQLite would ignore them anyway) and no
   proof read runs, because the proof is only informative on a database
   this process just created. This connection is closed before phase two
   begins.
2. **Pool.** A connector opens the pool (`poolSize = 8` connections,
   `_txlock=immediate` in the DSN) and applies the full per-connection
   pragma list to every connection it opens, including ones opened later
   as the pool grows or reconnects. The migration runner then runs against
   this pool.

The two phases exist because the two pragma sets have different lifetimes:
a per-connection pragma has to run again every time a new connection is
made, while a database-level one must run exactly once, before any
statement that could create a table. Folding them into one pass would
either re-run page-size-setting pragmas that SQLite will ignore anyway (
harmless but misleading) or risk running them after a migration's `CREATE
TABLE` has already executed on some other connection (data-losing, since
the page size is then fixed for the file's life).

`_txlock=immediate` is set once, in the pool's DSN, not as a pragma: every
transaction this store opens is a write transaction (the whole point of a
single serialized write path), and a deferred transaction takes the write
lock only on its first write statement, which is exactly where a
transaction can fail after already having made stale reads.

## Spec: the write path and the size guard

```go
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error
```

One mutex per file (`wmu`) serializes every write. `Write` begins a
transaction, runs `fn`, and commits; a rollback that reports
`sql.ErrTxDone` after a successful commit is not an error and is not
propagated. `Write` never opens a second concurrent write against the same
file: every aggregate in `state.md`, and cache's own writes, funnel through
this one call.

```go
func (d *DB) EnsureWritable() error
func (d *DB) SetWritesBlocked(blocked bool)
func (d *DB) WritesBlocked() bool
```

`EnsureWritable` is what a statement that grows the file calls first. It is
purely a check against an in-process flag (`blocked atomic.Bool`); this
package never touches the filesystem to decide the flag's value. The
sampling that measures free space and calls `SetWritesBlocked` lives one
level up, in the aggregator that owns all three files together (out of
scope for this document); `dbfile` only defines the flag, the check, and
the setter.

The design principle the flag encodes, stated here because every caller in
`state.md` has to apply it correctly: **the guard gates growth, never
recovery.** A statement that inserts a new row calls `EnsureWritable`
first. A statement that updates a row in place, deletes a row, or
reclaims space (`Vacuum`) never does, because those are exactly the
operations that let a full volume recover. `Write` itself does not consult
the guard; the call that knows whether its own statement grows the file is
the one writing it, not the transaction wrapper around it.

## Spec: the migration transaction model

```go
type Migration struct {
    Name         string
    SQL          string
    Discard      bool
    Precondition func(context.Context, *sql.Tx) error
}
```

- **One transaction per step.** The step's SQL and its version bump
  (`schema_version.version = i+1`) commit together. A crash mid-step
  leaves the old version standing beside the old shape; there is no
  half-applied step to detect or repair.
- **Position is the version.** A step's index in the list, not a field on
  it, is the version it produces. A shipped step is never edited,
  renumbered, or reordered; the next step is only ever appended.
- **Precondition runs first, inside the step's own transaction**, before
  the step's SQL. It exists for a refusal a `CHECK` constraint can enforce
  but not explain: a constraint failure names which constraint failed, not
  which row; a precondition can read the offending row and name it, which
  is what an operator holding a durable database needs to act on the
  refusal instead of just seeing that a migration failed.
- **Discard requires Rebuildable.** A step that throws away existing data
  and rebuilds a table's shape from nothing (rather than migrating rows
  forward) is refused unless `Spec.Rebuildable` is true. Only the cache
  database sets it; the state and journal databases never discard, because
  nothing rebuilds what they hold.
- **Schema versioning.** `schema_version` is a one-row table
  (`id = 1 CHECK`), read once at the start of `migrate` and written after
  every step inside that step's own transaction. A stored version greater
  than the number of migrations this binary knows is `ErrSchemaAhead`; the
  file is not touched. This is a refusal, not a best-effort open, because a
  downgrade that silently wrote an old shape into a newer file is how a
  rollback turns into data loss.

## Spec: errors

```go
var (
    ErrSchemaAhead      = errors.New(...) // a newer binary wrote this file
    ErrMigrationFailed  = errors.New(...) // a migration step rolled back
    ErrWritesBlocked    = errors.New(...) // the size guard; reads continue
    ErrBusy             = errors.New(...) // busy_timeout expired
)
```

`ErrBusy` is surfaced rather than retried forever inside this package: a
caller that already waited five seconds (`busy_timeout`) for a lock wants
to know that something else is holding this file, not have the wait
silently repeated. `mapErr` recognizes only `SQLITE_BUSY` on the low byte
of the driver's extended result code and wraps it; every other driver
error passes through unchanged, because this package does not try to
build a general error taxonomy for a driver it does not own.

## Spec: closing, size, and vacuum

- `Close` runs `PRAGMA wal_checkpoint(TRUNCATE)` before closing the pool.
  `TRUNCATE`, not `PASSIVE`: passive gives up the moment another connection
  is mid-read, and shutdown is exactly the moment waiting is correct,
  because the goal is that the next start has nothing to replay, not that
  shutdown is instant.
- `SizeBytes` is `page_count * page_size`, which answers identically for a
  file-backed and an in-memory database and needs no filesystem call.
- `Vacuum` runs `PRAGMA incremental_vacuum`, which depends on the
  bootstrap's `auto_vacuum = INCREMENTAL` having landed before the schema
  existed; this is the concrete reason the bootstrap proof-read exists.

## Rationale

- **Pragma ordering is a correctness property, not a style preference.**
  The failure it prevents ("database is locked" on a fresh file under
  concurrent open) only appears under load, so an implementation that gets
  the order right by accident and drifts in a later edit fails silently
  until the day several connections race a first open.
- **Two phases, not one pass.** Per-connection and database-level pragmas
  have different lifetimes; conflating them either wastes a call SQLite
  ignores or risks a `CREATE TABLE` racing ahead of a pragma that only
  works before one exists.
- **The guard gates growth only.** A guard that could also refuse a delete
  would block the one operation that recovers a full volume, turning a
  disk-space incident into a stuck database.
- **Discard needs an explicit flag.** A migration step that silently
  discards data on a database nothing can rebuild is data loss with a
  version bump on top; requiring `Rebuildable` makes the two accidents
  (writing a discard step, running it against the wrong file) both need a
  deliberate act to combine.

## Deliberate changes

None. This package audited clean (`audit/foundation-persistence.md`,
`store/dbfile`), and the rebuild carries its design forward unchanged:
same pragma set and order, same two-phase open, same migration transaction
model. The only correction this document makes is to the package survey's
line count: the survey states roughly 409 lines for `store/dbfile`; the
audit found 445 (`dbfile.go` 313, `migrate.go` 96, `sql.go` 36), because
the survey's count omitted `sql.go`. The rebuilt package is a fresh
implementation of the same size class, not a line-for-line port, so this
is a note for anyone citing the old count, not a target to hit.

Import path moves from `internal/store/dbfile` to `engine/store/dbfile`.

## Tests

- Pragma order: assert `busy_timeout` is applied before `journal_mode` on
  every new connection (inspect via `PRAGMA` reads on a freshly opened
  connection); a regression test that opens N connections concurrently
  against a brand-new file and asserts none report "database is locked".
- Bootstrap: a fresh file gets `page_size = 4096` and `auto_vacuum = 2`
  (INCREMENTAL); re-opening an existing file does not re-apply and does
  not error even if the two now mismatch a newer default.
- A schema-version ahead of the compiled migration list is `ErrSchemaAhead`
  and the file is not modified (verify via a checksum or an untouched
  mtime).
- A migration step's SQL failing mid-step leaves the version at its prior
  value and leaves the step's SQL effects rolled back, in one assertion
  against the same transaction.
- `Discard: true` on a non-rebuildable spec refuses before running any SQL.
- `EnsureWritable` refuses after `SetWritesBlocked(true)` and admits again
  after `SetWritesBlocked(false)`; `Write` succeeds independent of the flag
  (the flag is consulted by callers, not by `Write` itself).
- `Write` serializes two concurrent callers (observe via an instrumented
  `fn` that records overlap, or via SQLite's own single-writer behavior
  under `_txlock=immediate`).
- `ErrBusy` surfaces from a write that could not acquire the lock inside
  `busy_timeout` (simulate with a held write lock from a second raw
  connection).
- `SizeBytes` matches `page_count * page_size` read independently.
- `Close` leaves no WAL file to replay on the next open (assert via the
  absence or zero-length of the `-wal` file after close).
