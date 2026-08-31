# Auth 04: the audit log

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/auth/audit.go` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## The append-only rule

Rows are never edited. The actor column is only ever nulled by the
account-deletion cascade, never by this code: the log deliberately
outlives the accounts it names, because "who did this" matters most for
the account that no longer exists.

## What is recorded, and by whom

```go
func (s *Service) Audit(ctx context.Context, actor sql.NullInt64,
    event, target, ip, ua string, result int) error
func (s *Service) Record(ctx context.Context, actor int64,
    event, target, ip, ua string, ok bool)
```

- The login path writes its own rows (01): a success with the account as
  actor, a failure with the account when known, and an unknown-name
  attempt with **no actor and the tried name as target**, because a run
  of guesses against one name is what the log is read to find.
- `Record` serves every other surface (admin changes, revocations). It is
  called **from the handlers, not from the write methods**, because only
  the handler knows who is acting: the same service method serves an
  administrator editing somebody else and an account editing itself.

## The never-fail-the-action contract

`Record` never fails the action it records, and the login path treats an
audit failure after the session was minted as a log line, not an error.
The reasoning is a shipped defect: the audit write's error was once
returned, the caller answered 401, and the person was told their
credentials were wrong while holding a session that worked. A change that
happened must not be reported as one that did not; a row that could not
be written is logged where an operator can see the log has a hole.

`Audit` (the exported low-level write) still returns its error, because a
caller that has not yet acted can refuse to act on a log it cannot keep;
the contract above is about actions already committed.

## The page reader

```go
type AuditFilter struct {
    Actor   int64
    Event   string
    SinceNs, UntilNs int64
    Before  int64 // previous page's last rowid, exclusive
    Limit   int   // default 100
}

type AuditRow struct {
    RowID int64   `json:"rowid"`
    TsNs  string  `json:"ts_ns"`
    Actor *int64  `json:"actor"`
    ActorName *string `json:"actor_name"`
    Event string  `json:"event"`
    Target, IP, Detail *string
    UA    string  `json:"ua"`
    OK    bool    `json:"ok"`
}

func (s *Service) AuditPage(ctx context.Context, f AuditFilter) ([]AuditRow, *int64, error)
```

- **Cursor-paged, never offset-paged**: the cursor is the previous page's
  last rowid, so a page boundary stays correct while new rows land ahead
  of it.
- **Filters apply in process over a bounded overscan** (20 rows read per
  row returned), because a statement composed from optional filter parts
  is exactly what every statement in this tree is a constant to avoid. A
  filter matching nothing reads the overscan bound and stops, not the
  whole log.
- The row shape is the client's contract: `ok` is a boolean (a numeric
  `result` renders as a blank outcome), the timestamp is a **string**
  because nanoseconds exceed what a JSON number carries exactly, and
  `ActorName` is best-effort (null for system rows and deleted accounts;
  a name lookup failure leaves actors unnamed rather than failing the
  page).
- Null-versus-empty is deliberate on `Target`: the screen tells "no
  target" from "a target whose name is blank".

## Deliberate changes

1. **The SQL moves to a state aggregate** (`audit.go` + `audit_sql.go` in
   `engine/store/state`). The aggregate owns the insert and the
   overscan page read; the filter logic stays in the service, since it
   is presentation-facing shape, not schema.
2. Nothing else.

## Tests

- A row round-trips every column; empty target/ip/detail read back null.
- The cursor pages a log correctly while rows are being appended
  concurrently (no skipped, no duplicated rows across pages).
- Each filter narrows; combined filters intersect; a filter matching
  nothing terminates after the overscan bound (counting statement
  double).
- `Record` with a failing store does not fail (observed via a log sink);
  `Audit` with a failing store returns the error.
- The timestamp serializes as a string carrying the exact nanosecond.
- A deleted account's rows survive with a null actor name.
