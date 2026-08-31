# Foundation: journal

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/store/journal` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## Purpose

`engine/store/journal` holds one row per (account, file): the last thing
that account did to it. It is what a "recent files" listing reads
(`core/11-homes-and-recent.md`, `Recent`). It is a third database file
rather than a table inside `state` or `cache` specifically so the
distinction is visible to whoever decides what to back up and what to
recreate: state is the data backup, cache is a rebuild away, and journal is
neither, it is a convenience listing whose loss costs one feature and
nothing else.

Per `audit/foundation-persistence.md`, `store/journal`, this package
audited clean. This document restates its three stated properties as hard
requirements, because they are the whole design: everything else in the
package follows from them.

## Spec: the three hard requirements

Per the package's own stated design, arrived at by thinking through a
specific failure for each:

### 1. Not an audit log

The file write this journal records has already succeeded by the time a
row is written. A failure to write the row is logged and dropped; nothing
downstream may treat the absence of a journal row as evidence that a write
did not happen. `Record`'s own errors, and every failure mode of a nil
journal (below), follow directly from this: recording is a best-effort
side channel on a write path that has already committed, never a gate the
write itself depends on.

### 2. Capped by row count, never by age

The retention mechanism deletes the oldest rows past a fixed count per
account (`limits.JournalRowsPerAccount`), inside the same transaction as
the row being inserted. It never compares a stored timestamp against "now"
to decide what to prune.

The reason is a concrete failure mode: a prune keyed on age deletes the
entire table the moment the system clock jumps forward, which is an
ordinary event on a small box with a dead real-time clock before NTP
corrects it on boot. A row-count cap has no such failure mode; it is
insensitive to what the clock says, only to how many rows exist.

### 3. Not an activity stream

There is no per-event history: `Record` **upserts** on
`(user, share, path)`, so a second write to the same file by the same
account replaces the row rather than adding to it. There is no reader other
than the account whose rows they are; nothing in this package or above it
exposes one account's journal to another, and nothing keeps a full history
of every write, only the latest one per file.

## Spec: shapes and behavior

### Event

```go
type Op uint8

const (
    OpUpload Op = iota
    OpEdit
    OpCopy
    OpMove
    OpRestore
)

type Event struct {
    Account uint32       // the auth layer's user id
    Share   vfs.ShareID
    Path    vfs.SharePath
    Op      Op
    AtNs    int64        // filled on the way out; Record stamps its own
}
```

`Op` is stored as its string label (`"upload"`, `"edit"`, `"copy"`,
`"move"`, `"restore"`), not its numeric value. `ParseOp` on an unrecognized
label answers `OpUpload` rather than dropping the row: a row written by a
later binary version with an op this one does not know is still evidence
that something happened to the file, and losing the whole row over an
unfamiliar word loses more information than defaulting the op does.

### Record

```go
func (d *DB) Record(ctx context.Context, e Event) error
```

Stamps `AtNs` from the journal's own injected clock (never the caller's),
upserts the `(user, share, path)` row, and in the same transaction deletes
every row past the cap for that account, ordered oldest first by
`(at_ns DESC, rowid DESC)` (i.e. everything after the newest
`JournalRowsPerAccount` rows). Both the upsert and the trim commit
together, so the cap holds even against a crash immediately after: there
is no window where the table has grown past the cap on disk, however
briefly, because it was never written that way.

### Record and RecentSince contracts

```go
func (d *DB) Recent(ctx context.Context, account uint32, limit int) ([]Event, error)
func (d *DB) RecentSince(ctx context.Context, account uint32, sinceNs int64, limit int) ([]Event, error)
```

- `Recent` is `RecentSince` with `sinceNs = 0`, meaning no window.
- `sinceNs` is an instant, not a duration or a day count, and the caller
  (the core's `Recent`, `core/11-homes-and-recent.md`) is responsible for
  resolving any relative window against a clock before calling in. A
  duration resolved inside the journal would be resolved against this
  package's clock, and a client-relative "last 7 days" spans two different
  windows depending on whose clock does the arithmetic if resolved on the
  wrong side of a call boundary.
- `limit` is clamped to `[1, JournalRowsPerAccount]`: a caller cannot ask
  for more than the table is ever allowed to hold for one account, and a
  non-positive limit takes the cap as its default rather than erroring,
  since "give me everything you have, bounded" is the sensible reading of
  an unset limit.
- Rows return newest first, ordered by `(at_ns DESC, rowid DESC)`; the
  `rowid` tiebreak makes the order deterministic when two writes land in
  the same nanosecond, which two writes issued back to back easily can.
- Every returned row is re-parsed at the trust boundary on the way out:
  the stored share id is narrowed back to `vfs.ShareID` and the stored path
  string is re-parsed with `vfs.ParseSharePath`. A row this server would no
  longer accept as a valid path (written by an older or different binary,
  or corrupted on disk) is a hard error for that read, not a silently
  skipped or repaired row, because the journal itself has no way to tell
  "this row is corrupt" from "this row is stale"; that judgment belongs to
  the caller, which is exactly what `core.Recent`'s per-row revalidation
  does one layer up.

### Nil-receiver safety

```go
func (d *DB) Enabled() bool
```

Every method on `*DB` (`Enabled`, `Record`, `Recent`, `RecentSince`) is
safe to call on a nil receiver. `Record` on a nil `*DB` is a silent no-op;
`Recent`/`RecentSince` on a nil `*DB` answer an empty slice with no error.
This is the concrete mechanism behind the aggregator's degrade-not-fail
policy (`audit/foundation-persistence.md`, `store` finding 4): a
`journal.db` that fails to open is logged as a warning by the layer above
this package and the resulting `*journal.DB` is left nil, and every caller
above that, all the way to `core.Recent`, needs no branch to check whether
a journal exists. A deployment that lost this file, or never had write
access to create it, still serves every other feature; it only answers
"what did I write recently" with an honest empty list instead of a crash
or a degraded mode that has to be specially handled at every call site.

This document states the nil-receiver requirement explicitly, per the
audit's rebuild note, because it is not otherwise visible from the exported
API: a reader has to know to check, and every method's doc comment must
say so.

## Spec: retention

Retention is entirely the per-account row-count cap enforced inside
`Record`, described above; there is no separate sweep, cron, or
time-based expiry job for this database. A row is retired only when a new
write for the same account pushes the count over the cap, which means an
account that writes nothing keeps its journal forever (bounded, since the
cap still applies per write) and an account that writes constantly always
sees its most recent `JournalRowsPerAccount` writes. This is the intended
behavior, not a gap to close with an additional age-based sweep: adding one
would reintroduce exactly the RTC-clock-jump failure mode requirement 2
above exists to avoid.

## Rationale

- **All three properties trace to a named failure, not a preference.** Not
  an audit log follows from the write path already having committed
  before this package sees the event; count-capped follows from a real
  clock-jump incident class; not an activity stream follows from there
  being exactly one legitimate reader of a row (the account that made it).
  A rebuild that "improves" any one of these without re-deriving the
  failure it prevents is likely to reintroduce it.
- **Upsert over append.** An append-only event log would make "what is the
  latest state of this file" a query over history instead of a row read,
  and would need its own cap-and-trim logic identical to what the current
  upsert-plus-trim already does in one statement pair.
- **The clock is injected, not global.** Tests hold time still and assert
  the trim boundary exactly; a package that called `time.Now()` directly
  could not assert "the row exactly at the cap survives, the row one past
  it does not" without a real, unbounded wait.

## Deliberate changes

None to observable behavior. The rebuild carries forward the schema
(`write_event`, one row per `(user, share, path)`), the upsert-plus-trim
transaction, the nil-receiver safety, and the `LIMIT -1 OFFSET ?` SQLite
idiom for "delete everything past position n" unchanged, because the audit
found this package clean end to end.

The one correction, matching `foundation/dbfile.md`'s pattern: the package
survey states `store/journal` at 197 lines, which matches `journal.go`
alone; the audit found the true non-test total to be 251 lines once
`sql.go` (54 lines) is included. Noted for anyone citing the old count; the
rebuild is a fresh implementation, not sized to match either number.

Import path moves from `internal/store/journal` to `engine/store/journal`.

## Tests

- `Record` twice for the same `(account, share, path)` leaves one row, with
  the second write's `Op` and a newer `AtNs`.
- Writing past `JournalRowsPerAccount` for one account trims the oldest row
  in the same transaction as the write that crossed the cap; a crash
  simulated immediately after (kill before the next operation) leaves the
  cap already enforced on reopen.
- Two accounts each writing past the cap do not affect each other's row
  counts (per-account isolation of the trim).
- `RecentSince` with `sinceNs` after every row's `AtNs` returns empty, not
  an error.
- `Recent`/`RecentSince` order newest first, with a stable tiebreak for two
  rows sharing an `AtNs` (assert via a fixed clock forcing a collision).
- `limit <= 0` and `limit > JournalRowsPerAccount` both clamp to
  `JournalRowsPerAccount`.
- A row containing a path this server's `vfs.ParseSharePath` now refuses
  (written directly into the table for the test) errors the read rather
  than being silently dropped.
- `ParseOp` on an unrecognized stored label answers `OpUpload` rather than
  an error or a zero value that renders as unlabeled.
- Every method is safe to call on a nil `*DB`: `Enabled()` is false,
  `Record` returns nil, `Recent`/`RecentSince` return `(nil, nil)`.
- A journal database that fails to open (permission-denied fixture)
  produces a nil `*DB` one layer up, and every method above it (through
  `core.Recent`) behaves per the nil-journal case in
  `core/11-homes-and-recent.md` (empty list, no error).
