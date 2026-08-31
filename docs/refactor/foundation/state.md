# Foundation: state

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/store/state`, plus the grant SQL in `go/internal/acl/store.go`,
> `sql.go`, `grant_storage.go`, the share-link SQL in
> `go/internal/core/links.go` and `links_sql.go`, and the quota ledger in
> `go/internal/core/quota.go`, is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## Purpose

`engine/store/state` is the durable half of the store: the data backup.
Nothing in it can be reconstructed from the filesystem, which is the
property that makes it a different kind of thing from
`engine/store/cache`. This document re-draws it one aggregate at a time,
gives every aggregate a consistent file-pair convention, and lands three
surfaces the core documents (`core/09`, `core/10`, `core/11`) require but
the current tree does not provide as store-layer code: the share-link
store, the quota ledger, and the grant write surface.

This is the largest document in phase 0 because `state.db` is the largest
and most heterogeneous of the three databases: eleven aggregates today,
three more added here.

## Spec: package shape

```go
package state

type DB struct {
    f *dbfile.DB
    overrides atomic.Int64 // cached count of fileid_override rows; -1 unknown
}

func Spec(path string) dbfile.Spec // Rebuildable: false
func New(f *dbfile.DB) *DB
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error
func (d *DB) SQL() *sql.DB
func (d *DB) File() *dbfile.DB
```

`Write` is the one serialized write path this database exposes; it is a
direct pass-through to `dbfile.DB.Write` (`foundation/dbfile.md`). Every
aggregate method in this document that mutates a row calls `d.Write`, never
`d.SQL().ExecContext` directly, so that every write to this file, from
every aggregate, takes the same single mutex and the same one-transaction
guarantee. Reads go through `d.SQL()` directly, unserialized, because
SQLite's WAL mode gives many concurrent readers against one writer for
free and a read gains nothing from the write mutex.

`Spec` sets `Rebuildable: false`. Nothing in this database may ever be
discarded by a migration step; a `Discard: true` step against this spec is
refused by `dbfile`'s migration runner regardless of what the step names
(`foundation/dbfile.md`).

## Spec: identity

Per `audit/foundation-persistence.md` (`store/cache` finding 1, `store/state`
finding 2), the identity tuple exists three times in the current tree, and
`foundation/cache.md` resolves the first of the three by moving it to a
neutral package:

**`engine/store/ident.Ident`** (`foundation/cache.md`) is the one identity
tuple. This document requires the other two representations to adopt it,
not just the import that already used `cache.Ident`:

- `dav.go`'s locally defined `Ident` type (four fields, `Share` typed
  `int64` rather than `vfs.ShareID`, with its own `identToSQL`/
  `identFromSQL`/`parts()`) is deleted. Every function in `dav.go`
  (`DavProps`, `SetDavProps`, `DropDavProps`, `DavLocks`, `PutDavLock`,
  `RefreshDavLock`, `DropDavLock`, `SweepDavLocks`) takes and returns
  `ident.Ident` and uses `ident.Ident.ToSQL`/`ident.FromSQL` at the SQL
  boundary. The stored column shapes (`share`, `dev`, `ino`,
  `btime_present`, `btime_ns`) are unchanged; only the Go type collapses to
  one.
- `favorite.go`'s `Favorite` struct stops inlining `Dev`, `Ino`, `Btime` as
  its own fields and instead carries an `ident.Ident` field:

  ```go
  type Favorite struct {
      Ident ident.Ident
      Path  string
  }
  ```

  `SetFavorite`/`Favorites` use `Ident.ToSQL()` at the boundary instead of
  a third hand-written `parts()`/`toSQL`/`fromSQL` set.

This closes both duplicated representations the audit found beyond the
`cache`-import one the survey already named, per the audit's explicit
instruction ("fixing only the import leaves two of three duplicated
identity representations in place").

## Spec: the size-guard coverage rule

Per `foundation/dbfile.md`: **`EnsureWritable` gates growth, never
recovery.** This document states the rule this database's aggregates apply
it with, precisely, so coverage is a checklist rather than a per-aggregate
judgment call:

> Every method that executes a statement which can add a new row to this
> database (an `INSERT` that is not "replace this row in place", an
> `INSERT ... ON CONFLICT DO UPDATE` whose first call for a given key
> inserts) calls `d.f.EnsureWritable()` before entering `d.Write`. A method
> whose statement only updates an existing row, deletes a row, or reclaims
> space does not.

Applying this mechanically across every method in this document finds:

**Compliant today**, carried forward unchanged: `upload.go`'s
`CreateUploadSession`, `RecordUploadInterval`, `TouchUploadDir`,
`WriteChunkSettings`, `WriteUploadCacheEnabled`; `dav.go`'s `SetDavProps`,
`PutDavLock`, `RefreshDavLock`, `DropDavLock`, `SweepDavLocks`;
`favorite.go`'s `SetFavorite`; `configsecret.go`'s `WriteConfigSecret`.

**Missing today**, per `audit/foundation-persistence.md` (`store/state`
finding 4), fixed in this rebuild:

1. `shares.go`'s `InsertShare` (new share row).
2. `operation.go`'s `CreateOp` (new operation and item rows).
3. `loginflow.go`'s `PutLoginFlow` (new flow row).
4. `override.go`'s `RecordFileIDs` (new override rows).

Each of these gains a leading `if err := d.f.EnsureWritable(); err != nil {
return err }` (or the multi-return equivalent for `InsertShare`, which
returns an id), before the call to `d.Write`. The guard is off by default
in the current deployment, which limits the practical impact today, but
the inconsistency is real: with the guard tripped, new shares, operations
and login flows could still be created while uploads, DAV locks, favorites
and config secrets were correctly refused.

**A fifth gap the audit's enumeration does not name**, found by applying
the rule mechanically rather than relying on the audit's list alone:
`settings.go`'s `MergeSettings` is an `INSERT ... ON CONFLICT DO UPDATE`
against a singleton row (`CHECK (id = 1)`). Its first-ever call for a fresh
database inserts a row and therefore grows the file; every call after that
updates in place. The rebuild adds the same guard check to `MergeSettings`,
because the rule as stated does not carve out an exception for a statement
that only grows the file once: a guard that must be re-derived per
statement by asking "does this happen to grow the file today" is exactly
the kind of implicit coverage this rule exists to replace with an explicit
one.

**Every new aggregate in this document** (share links, quota, grants) is
checked against the same rule as it is specified, not audited separately
after the fact; see each aggregate's section below.

## Spec: file-pair convention

Every aggregate gets exactly two files: `<aggregate>.go` (the Go surface,
the row shapes, the behavior) and `<aggregate>_sql.go` (every statement
that aggregate runs, as constants). `sql.go` at the package root holds
only the schema and its migration history; it holds no aggregate's active
statements.

The rule behind the split: **every SQL statement in this package is a
complete constant, never assembled from fragments at runtime.** No
`fmt.Sprintf`, no string concatenation, and no optional clause appended
conditionally; a statement that must vary by which fields are present
(`Update`, per-field, in the share-link aggregate below) gets one constant
per variant instead of one assembled statement. This was the old tree's
own discipline, tracked under its own internal decision numbering
(`go/internal/acl/sql.go`); it is restated here in full, rather than cited
by that number, to avoid colliding with this rebuild's own decision record
(`00-decisions.md`), which uses the same D-prefixed numbering for a
different, unrelated set of decisions.

Per `audit/foundation-persistence.md` (`store/state` finding 7 and finding
12), this fixes two aggregates that do not follow their own package's
convention today: `override.go` and `settings.go` currently share the
package's `sql.go` for their active statements instead of having their own
`_sql.go` file. This document gives them `override_sql.go` and
`settings_sql.go`.

The full file-pair list this document specifies:

| Aggregate | Logic file | SQL file | Status |
| --- | --- | --- | --- |
| Shares | `shares.go` | `shares_sql.go` | carried forward, size-guard fix |
| Operations | `operation.go` | `operation_sql.go` | carried forward, size-guard fix |
| Uploads | `upload.go` | `upload_sql.go` | carried forward unchanged |
| Settings | `settings.go` | `settings_sql.go` (new) | carried forward, gets its own SQL file, size-guard fix |
| Overrides | `override.go` | `override_sql.go` (new) | carried forward, gets its own SQL file, size-guard fix, drops the `cache` import |
| Favorites | `favorite.go` | `favorite_sql.go` | carried forward, adopts `ident.Ident` |
| Login flows | `loginflow.go` | `loginflow_sql.go` | carried forward, size-guard fix |
| DAV locks and dead properties | `dav.go` | `dav_sql.go` | carried forward, adopts `ident.Ident` |
| Config secrets | `configsecret.go` | `configsecret_sql.go` (split out) | carried forward, own SQL file per the convention |
| Active work | `activework.go` | (inline; two read-only counts, no write path) | carried forward unchanged |
| Share links (new) | `link.go` | `link_sql.go` | extracted from `core/links.go`/`links_sql.go`, matches `core/10`'s `LinkStore` |
| Quota (new) | `quota.go` | `quota_sql.go` | extracted from `core/quota.go`, matches `core/09`'s `QuotaSink` |
| Grants (new) | `grant.go` | `grant_sql.go` | extracted from `acl/store.go`, `sql.go`, `grant_storage.go`, matches `core/11`'s `PersistGrant` plus a full CRUD surface |

`activework.go` keeps no `_sql.go` companion: it holds two read-only
`COUNT` queries and no write path of its own, so splitting two constants
into a third file would not serve the convention's purpose (separating
schema history from an aggregate's active statements); its two constants
stay inline, as today.

## Spec: existing aggregates, unchanged behavior

The following aggregates carry their current behavior forward exactly,
modulo the identity and size-guard fixes above. They are listed for
completeness of the re-drawn map; none needs a design decision beyond what
is already stated.

- **Shares** (`shares.go`): `ListShares`, `InsertShare`, `UpdateShare`,
  `DeleteShare` over `share_definition`. `DeleteShare`'s cascade behavior
  toward the grant table is specified below under "Grant-to-share
  cascade", which changes its implementation without changing its
  signature. `core/03-share-registry.md` writes against `state.ShareRow`
  directly, so this document spells its fields:

  ```go
  type ShareRow struct {
      ID   int64
      Name string
      Host string
      // SharedExternally marks a folder another program also writes.
      // Nothing on a filesystem says so, which is why it is the operator
      // who says it.
      SharedExternally bool
      // TrashEnabled keeps deleted items in the share rather than
      // removing them. Off by default, because trash is disk somebody has
      // to reclaim.
      TrashEnabled bool
      // SymlinkPolicy is the share's own answer to a symlink, as the vfs
      // package spells it. Stored as its name rather than its number so a
      // renumbering of the enum cannot silently change what a share does.
      SymlinkPolicy string
      Created       int64
  }
  ```

- **Operations** (`operation.go`): the bounded, restart-visible history of
  long operations (`CreateOp`, `StartOpItem`, `UnfinishedOpItems`, `GetOp`,
  `SetOpProgress`, `RequestOpCancel`, `FinishOp`, `InterruptOp`, `ListOps`)
  over `operation`, `operation_item`, `operation_result`.
  `core/07-transfers.md` writes against `state.OpKind`, `state.OpState`,
  `state.OpResult`, `state.OpResultReason` and `state.ErrNoSuchOp`
  directly, so this document spells them:

  ```go
  // OpState is the terminal or interim machine state of one operation.
  type OpState int8

  const (
      OpRunning OpState = iota
      OpDone
      OpFailed
      OpCancelled
      // OpInterrupted is a run that was not finished when the process
      // stopped and that nothing resumes. A refreshed client gets an
      // honest terminal state with its progress and results preserved.
      OpInterrupted
  )

  // OpKind is what kind of work an operation is.
  type OpKind int8

  const (
      OpCopy OpKind = iota
      OpDelete
      OpArchive
      // OpIndexBuild walks every share to build the name index. Appended
      // rather than inserted: these are stored as numbers, so renumbering
      // would change what an already-written row means.
      OpIndexBuild
  )

  // OpResultReason is the typed reason an item-level result failed,
  // replacing a lower-layer error sentence.
  type OpResultReason int8

  const (
      ReasonItemOk OpResultReason = iota
      ReasonItemFailed
      ReasonItemDenied
      ReasonItemNotFound
      ReasonItemConflict
      ReasonItemSkipped
  )

  // OpResult is one item's outcome from a batch operation.
  type OpResult struct {
      Operation int64
      Idx       int64
      Path      string
      OK        bool
      Reason    OpResultReason
      Text      string
  }

  // ErrNoSuchOp is an operation id that holds no row.
  var ErrNoSuchOp = errors.New("no such operation")

  // CreateOp starts a fresh operation. The id it returns is what a client
  // reattaches with. createdNs is the durable stamp the caller's clock
  // provided. paths is what the operation was asked to do, recorded now
  // rather than as it goes: a job that stops short can then say which
  // items it never reached.
  func (d *DB) CreateOp(
      ctx context.Context, user int64, kind OpKind, total, createdNs int64, paths []string,
  ) (int64, error)
  ```

  `CreateOp`'s six arguments, in order: the context, the owning user, the
  operation kind, the total item count (`0` when unknown until the walk
  ends), the caller's clock stamp, and the paths the operation was asked
  to act on. `GetOp` answers an unknown id with `ErrNoSuchOp`, which the
  core maps to `ErrNotFound` at its own boundary
  (`core/07-transfers.md`).
- **Uploads** (`upload.go`): session lifecycle, intervals, aliases, touched
  directories, chunk and cache settings. The largest aggregate and the one
  the audit names as the positive counter-example on size-guard coverage;
  no change beyond what is already correct.
- **Settings** (`settings.go`): the one-JSON-document override store
  (`Settings`, `MergeSettings`, the search-section helpers, `FileBytes`).
  The read-merge-write-in-one-transaction pattern, and its documented
  regression fix (a save that dropped another section), are preserved as
  required behavior.
- **Overrides** (`override.go`): the fileid-collision authority
  (`LookupFileID(ctx, id ident.Ident) (ident.FileID, bool, error)`,
  `LookupFileIDOwner(ctx, id ident.FileID) (ident.Ident, bool, error)`,
  `RecordFileIDs(ctx, assignments ...ident.Assignment) error`,
  `CountFileIDOverrides`) consumed by `store/cache` through the
  `cache.Overrides` interface. It is `ident.Ident`, `ident.FileID` and
  `ident.Assignment` together, not `Ident` alone, that remove this file's
  `store/cache` import: `Ident` moving but `FileID` and `Assignment`
  staying in `cache` would still force this package to import `cache` for
  the other two shapes the interface's methods carry
  (`foundation/cache.md`'s "Spec: identity" documents the `cache` side of
  the same fix, moving all three types together).
- **Favorites** (`favorite.go`): stars keyed by identity, adopting
  `ident.Ident` as specified above.
- **Login flows** (`loginflow.go`): the device login flow's durable half.
  The race-closing pattern (the approval and the poll-interval check both
  live inside the `UPDATE ... WHERE` clause, never a read followed by a
  write) is preserved as required behavior; it is what makes one login URL
  opened twice mint exactly one credential.
  Phase 3 extends the row with delivery state, sealed credential bytes, key
  version, credential id and delivered timestamp. `ClaimLoginFlowDelivery`
  atomically chooses one first poller; `StoreLoginFlowDelivery` records the
  sealed result; later polls return the same row until expiry. No plaintext
  credential column exists. The migration is forward-only and old rows read
  as pending/undelivered.
- **DAV locks and dead properties** (`dav.go`): keyed by identity rather
  than by a cache-minted id, per the documented reasoning (a property keyed
  by file id would move when the cache rebuilds; one keyed by identity
  moves when the file does). Adopts `ident.Ident`.
  Phase 3 adds two neutral transactional operations: lock admission performs
  expiry cleanup, conflict/count checks and insert under the database's
  serialized writer; a lock snapshot returns all covering rows for a set of
  targets in one read transaction. DAV maps these neutral rows at its service
  adapter and never imports this package.
- **Config secrets** (`configsecret.go`): settings that are credentials,
  sealed under the master key before they reach this layer; this layer
  holds bytes and a key version and has no key of its own.
- **Active work** (`activework.go`): the two counts (`Uploads`, `Jobs`) a
  restart would interrupt, read fresh on every call with no cached state.

## Spec: share links (new aggregate)

Per `audit/foundation-persistence.md` (`store/state` finding 3), this is an
**extraction**, not an addition: the real share-link CRUD, key-version
lookup, and download-counter logic exist today, but in `core/links.go` and
`core/links_sql.go`, a service-layer package running raw `*sql.Tx`/`*sql.DB`
calls against `state.db` directly. `store/state`'s own `sharelink.go`
today holds only a migration precondition and an error sentinel. This
document moves the entire CRUD surface here, leaving `core` with
orchestration only, matching the shape `core/10-share-links.md` already
specifies and the shape the `acl` split leaves the evaluator.

### LinkStore, matching core/10 exactly

`core/10-share-links.md` defines the `LinkStore` interface the core
consumes and the `LinkRow`/`LinkRowPatch` shapes that cross the boundary.
This document implements that interface with no spelling delta:

```go
// link.go
type LinkRow struct { /* exactly as core/10-share-links.md specifies */ }
type LinkRowPatch struct { /* exactly as core/10-share-links.md specifies */ }

func (d *DB) Insert(ctx context.Context, row LinkRow) (int64, error)
func (d *DB) ByID(ctx context.Context, id int64) (LinkRow, bool, error)
func (d *DB) ByHash(ctx context.Context, tokenHash []byte) (LinkRow, bool, error)
func (d *DB) ListByOwner(ctx context.Context, owner int64) ([]LinkRow, error)
func (d *DB) Delete(ctx context.Context, id, owner int64) error
func (d *DB) ConsumeDownload(ctx context.Context, id int64) (consumed bool, err error)
func (d *DB) PasswordHash(ctx context.Context, id int64) (*string, error)
func (d *DB) Update(ctx context.Context, id int64, patch LinkRowPatch) error
func (d *DB) KeyVersion(ctx context.Context) (uint32, error)
```

Because `*state.DB` already exposes these nine methods with these exact
signatures, it satisfies `core.LinkStore` with no adapter type; the seam
is `core.Options.Links`, which `core/10-share-links.md` names: the server
passes the `*state.DB` value there at construction (`core/10:443-446`),
the same field group as `ACL`.

No amendment to `core/10-share-links.md` is required: the interface
spelling matches as designed.

### Behavior carried over from `core/links.go`/`links_sql.go`

- `Insert` calls `d.f.EnsureWritable()` first (a new row): this is the one
  net-new insert path in this aggregate, and it is specified compliant
  from the start rather than audited in later.
- `ByID`/`ByHash` return `(LinkRow{}, false, nil)` for no match, never an
  error; the core's `ErrNotFound` mapping happens one layer up.
- `Delete` scopes by both `id` and `owner` in the same statement
  (`sqlDeleteLink = "DELETE FROM share_link WHERE id = ? AND owner = ?"`),
  so the ownership check and the delete cannot disagree; carried forward
  from the reference unchanged.
- `ConsumeDownload` is the conditional `UPDATE` from `core/10`
  (`downloads = downloads + 1 WHERE id = ? AND (max_downloads IS NULL OR
  downloads < max_downloads)`), the same atomic-cap pattern the quota
  ledger uses below. Zero rows affected is ambiguous by design (cap reached
  or row gone); the core disambiguates via a follow-up `ByID`, per
  `core/10`'s "Deliberate changes" item 4.
- `Update` is one constant statement per field
  (`sqlUpdateLinkPerms`, `sqlUpdateLinkPassword`, `sqlUpdateLinkExpiry`,
  `sqlUpdateLinkMaxDown`, `sqlUpdateLinkLabel`, `sqlUpdateLinkNote`),
  applied inside one `d.Write` transaction, one statement per present patch
  field. This is carried forward exactly per the reference and per
  `core/10`'s own rationale: a statement assembled from the fields a patch
  happens to carry has text that depends on input, which constant
  statements per field exist to prevent.
- `KeyVersion` reads the single `key_version` row the auth package
  maintains; a missing row (a deployment that has never sealed a link
  token) answers `0`, not an error.
- The identity-pin partial-row validation (the four `dev`/`ino`/
  `btime_present`/`btime_ns` columns must be all-set or all-absent, and the
  migration-2 `CHECK` constraint plus `checkShareLinkTargets`'s
  human-readable precondition are both preserved) stays exactly as
  specified in the current schema and migration history; this document
  changes no schema shape for share links, only where the Go code that
  reads and writes them lives.
- The row-to-domain conversion (`scanLink` today) and the opportunistic
  token decryption **do not move here**: `core/10`'s "Deliberate changes"
  item 3 keeps that conversion, including the decrypt attempt, as one core
  function, so the trust-boundary validation of a row has a single home in
  the package that also owns the cipher seam. This store only returns
  `LinkRow` values; it never sees a plaintext token or a plaintext
  password, and it never attempts to open a sealed one.

## Spec: quota ledger (new aggregate)

Per `core/09-quota-and-aggregates.md`'s "Deliberate changes" item 1, the
SQL ledger moves to this layer; `core.quota.go` keeps only the
`QuotaSink` interface, `AttachQuotaSink`, and `chargeQuota`.

`store/state` never imports the core, in either direction: `NewQuota`
returns the package-local concrete type, not `core.QuotaSink`. The wiring
site, not this package, is where the concrete type meets the interface:
`core.AttachQuotaSink(state.NewQuota(db))` type-checks because `*Quota`
happens to implement `core.QuotaSink`, which this package never states and
never needs to import to be true.

```go
// quota.go
type Quota struct { /* unexported: db *DB */ }

func NewQuota(db *DB) *Quota

func (q *Quota) Reserve(ctx context.Context, user int64, additional uint64) (ok bool, err error)
func (q *Quota) Commit(ctx context.Context, user int64, additional uint64) error
func (q *Quota) Release(ctx context.Context, user int64, delta int64) error
```

This matches `core/09`'s interface exactly, including the signature change
from the current `core/quota.go` (which returns `error` alone from
`Reserve` and answers a refusal as `ErrQuotaExceeded`). `core/09`'s
"Deliberate changes" item 2 already specifies this: the store cannot import
the core (the core imports the store), so `Reserve` cannot return the
core's sentinel; it returns `(ok bool, err error)`, and the core's write
path maps `ok == false, err == nil` to `ErrQuotaExceeded` exactly once, at
the call site. This document's job is to implement that contract, not to
restate the decision.

Behavior:

- **Reserve** is the guarded `UPDATE` from `core/09`, unchanged:

  ```sql
  UPDATE user
  SET usage_bytes = usage_bytes + ?
  WHERE id = ?
    AND (quota_bytes IS NULL OR usage_bytes + ? <= quota_bytes)
  ```

  One row affected is `ok = true`. Zero rows affected is `ok = false, err
  = nil`, deliberately not distinguishing a missing user from a user at
  the cap, per `core/09`'s rationale (a missing user has no headroom
  either). `additional` that does not fit the signed column
  (`num.Narrow[int64]`) is an error, not a refusal.
- **Reserve never calls `EnsureWritable`**: it is a pure `UPDATE` against
  an existing user row and never inserts one, so per this document's
  size-guard rule it is not gated. This matters specifically because
  `Reserve` is the ledger's hot path (every upload calls it), and gating a
  pure update under the size guard would refuse legitimate uploads on a
  volume that is full of everything except headroom in the user table,
  which is exactly backward: reserving bytes against an existing cap
  cannot itself grow the database.
- **Commit** is a no-op, returning `nil` unconditionally: `Reserve` already
  booked the bytes durably, so `Commit` exists only to keep the caller's
  intent explicit at the call site, per `core/09`.
- **Release** is the zero-clamped credit:

  ```sql
  UPDATE user SET usage_bytes = max(0, usage_bytes - ?) WHERE id = ?
  ```

  A zero `delta` is a no-op that still returns `nil` without a write (no
  reason to take the write mutex for a statement that changes nothing). A
  negative `delta` is an error, not silently booked: `core/09` states the
  write path reserves and the delete path credits, so a negative value
  reaching `Release` is a caller bug and is reported as one.
- All three methods use `num.Narrow` at every crossing from `uint64`/
  `int64` into the ledger's signed column, since `UserID` and byte counts
  cross this interface as primitives per `core/09`'s import-direction
  rationale (the store cannot name `core.UserID`).

## Spec: grants (new aggregate)

Per `audit/foundation-persistence.md` (`acl` finding 1) and
`01-package-survey.md`, the grant table's write half (`acl/store.go`,
`sql.go`, `grant_storage.go`) moves here; `acl` keeps only the pure
evaluator (`eval.go`, `grant.go`, `perms.go`, `cache.go`,
`foundation/acl-evaluator.md`). Per `audit/presentation.md`'s grant SQL
bypass finding, this is also the surface that ends the three-site direct
call to `acl.CreateGrant(ctx, st.State().SQL(), acl.Grant{...})`
(`httpapi/handler/admin_grants.go`, `httpapi/handler/shares.go`,
`cmd/stowcloud/serve.go`'s `grantEveryShare`).

### GrantRow, matching core/11's PersistGrant signature

`core/11-homes-and-recent.md` specifies `PersistGrant`'s signature and the
shape it needs, describing `GrantRow` only loosely ("mirrors the ACL
package's `Grant` value"). This document spells `GrantRow` out fully as
the store's own row shape and extends the surface to a full CRUD API,
since `core/11` only had to specify the one insert path homes needs, while
the presentation-layer bypass needs list, update and delete as well.
`state.GrantRow` is not the same type as `acl.Grant`
(`foundation/acl-evaluator.md`): `User`/`Group` are `*int64` here (nil
means unset) against `acl.Grant`'s zero-sentinel `int64`, and `Allow`/
`Deny` are `uint16` here against `acl.Grant`'s `Perms` (`uint16`-backed but
a distinct named type). The state package owns this row shape, the ACL
package owns the domain shape, and the core converts between them
(`core/11-homes-and-recent.md`), exactly as it already does for the
membership row below.

```go
// grant.go
type GrantRow struct {
    ID      int64
    User    *int64 // nil means group-scoped; exactly one of User, Group set
    Group   *int64
    Share   int64
    Subpath string // the ACL package's Path, string-spelled
    Allow   uint16
    Deny    uint16
    Inherit bool
    Label   string
    CreatedNs int64
}

type GrantFilter struct {
    User, Group, Share int64 // zero is not a filter
}

// PersistGrant validates the grant (exactly one of User and Group set, a
// share id present, a non-empty allow set), writes one grant row through
// the state database's serialized write path, stamps CreatedNs, and
// returns the stored grant's id. It is the one insert path for this
// aggregate: home creation (core/11) and admin-facing grant creation both
// call it. It does not touch the evaluator; reloading stays the caller's
// explicit separate step, so a caller creating several grants reloads once.
func (d *DB) PersistGrant(ctx context.Context, g GrantRow, nowNs int64) (int64, error)

// ListGrants returns the stored grants, optionally narrowed by filter.
func (d *DB) ListGrants(ctx context.Context, filter GrantFilter) ([]GrantRow, error)

// UpdateGrant replaces the permission bits, the inheritance and the label
// of one grant. It cannot change who the grant is for or which share it
// covers, because those identify the grant rather than describe it, and
// changing them under one id would make an audit trail read as though a
// permission moved when a different rule replaced it.
func (d *DB) UpdateGrant(ctx context.Context, id int64, allow, deny uint16, inherit bool, label string) error

// DeleteGrant removes one grant.
func (d *DB) DeleteGrant(ctx context.Context, id int64) error

// MembershipRow is one (user, group) pairing, in the store's own row
// shape.
type MembershipRow struct {
    User, Group int64
}

// Memberships returns every (user, group) pairing, for the evaluator's
// reload. It is grouped with the grant aggregate rather than given a file
// of its own because the evaluator loads both in one call and a group
// with no grant naming it is not a concept this layer otherwise needs.
func (d *DB) Memberships(ctx context.Context) ([]MembershipRow, error)
```

`Memberships` returns a flat `[]MembershipRow`, not a
`map[int64][]int64`: grouping by user is a shape the evaluator's own
internal representation wants, not one this row-shaped store surface
should pre-impose, and a slice of pairs is what a `SELECT` naturally scans
into without an intermediate grouping step in this package.

`ListGrants(ctx, GrantFilter{})` (no filter) and `Memberships(ctx)`
together are what the core's reload path calls for the unfiltered load:
the core converts each returned `GrantRow` into an `acl.Grant` and each
`MembershipRow` into an `acl.Membership`, then calls
`Evaluator.LoadFromState` with the converted slices
(`foundation/acl-evaluator.md`). This store does not feed
`LoadFromState` directly: `ListGrants`/`Memberships` return this
package's own row shapes, and the core owns the conversion into the
evaluator's domain types, per `core/11`'s "the state package owns the
row shape, the ACL package owns the domain shape, and the core converts
between them." The admin listing surface calls `ListGrants` with a
populated filter instead. Filtering is applied in Go, not assembled into
the `WHERE` clause, per the same reasoning the current `acl.ListGrants`
already uses and this document preserves: the table is small enough to
hold in memory for the evaluator anyway, and a statement built from
optional filter parts is exactly what every statement in this package
being a constant exists to prevent.

### Behavior

- `PersistGrant` calls `d.f.EnsureWritable()` first: it is this
  aggregate's one insert path. `UpdateGrant` and `DeleteGrant` do not,
  per the size-guard rule (neither adds a row).
- `PersistGrant`'s validation (exactly one of `User`/`Group`, a non-zero
  `Share`, a non-empty `Allow`) runs before the write, inside the same
  function, and refuses with a descriptive error rather than relying on
  the schema's own `CHECK ((user IS NULL) <> ("group" IS NULL))` to catch
  the first two: a `CHECK` failure names the constraint, not the caller's
  mistake, and this is exactly the reasoning `foundation/dbfile.md`'s
  `Precondition` mechanism uses for migrations, applied here to a
  regular write path instead.
- `UpdateGrant`'s statement is unchanged from the current
  `acl.UpdateGrant`: `UPDATE "grant" SET allow = ?, deny = ?, inherit = ?,
  label = ? WHERE id = ?`, refusing `n == 0` as "no such grant".
- `DeleteGrant` is a plain `DELETE ... WHERE id = ?`, refusing `n == 0` the
  same way.
- None of these four methods touches the evaluator. Reloading
  (`Evaluator.LoadFromState`) is always the caller's next explicit step,
  exactly as the current `createHomeGrant` and the current handler code
  both already do it as two calls; this document does not fold reload into
  the store, because a caller creating several grants in one request
  (the "grant every share to the first administrator" bootstrap path,
  `cmd/stowcloud/serve.go`'s `grantEveryShare`) should reload once after
  all of them, not once per grant.

### Ending the three-site bypass

This document's contribution to ending the bypass
(`audit/presentation.md`'s grant SQL bypass finding, and its "Documents
required" item 8, "Grant write surface spec") is providing the store-side
surface that a real service-level grant API needs: without `PersistGrant`,
`ListGrants`, `UpdateGrant`, `DeleteGrant` living in `store/state` with no
`database/sql` handle crossing a layer boundary, there is nothing else for
`httpapi/handler`, `cmd/stowcloud`, or any other future caller to call
instead of reaching for `acl.CreateGrant(ctx, st.State().SQL(), ...)`.

The remaining half, a core-level wrapper method that calls this surface
and then reloads the evaluator in one call for handlers and the bootstrap
CLI to use, is not specified here: it is service-layer orchestration.
`core/00-overview.md` already names it: `grants.go`, exposing
`Core.CreateGrant`/`ListGrants`/`UpdateGrant`/`DeleteGrant`, each a thin
wrapper over this aggregate plus one evaluator reload, in the build-order
slot `00-overview.md`'s file layout lists. `httpapi/handler` and
`cmd/stowcloud` call these wrappers in their own rebuilds instead of
touching `store/state` or `acl` directly. This document's aggregate is
what that wrapper calls; the wrapper itself is `core/00`'s to specify, not
this document's.

## Spec: the grant-to-share cascade decision

Per `audit/foundation-persistence.md` (`store/state` finding 8), the grant
table's `share` column carries no foreign key to `share_definition`, and
`shares.go`'s `DeleteShare` comment states the cascade is the caller's
responsibility. This is a real, unenforced cross-package contract.

**Decision: no schema-level foreign key on `grant.share`. The cascade is
enforced inside this store's own `DeleteShare`, in the same transaction as
the share row's deletion.**

This is the opposite of the first instinct (add
`REFERENCES share_definition(id) ON DELETE CASCADE`, now that
`foreign_keys = ON` is a hard pragma requirement per
`foundation/dbfile.md`), and the reason is a real structural fact about
this codebase that a schema-level FK cannot accommodate: **not every valid
`grant.share` value is ever a row in `share_definition`.** The homes share
(`core/11-homes-and-recent.md`) is registered under the reserved id
`999_999`, a constant deliberately chosen below `dynamicShareIDBase`
(`1_000_000`, `core/03-share-registry.md`) so it can never collide with an
id derived from a `share_definition` row id. It is registered live
(`RegisterShare`) but it is never inserted into `share_definition`, because
it is not a share an administrator created through the admin CRUD path;
it is the one directory this process manages outright. A home grant
(`core/11`'s `PersistGrant` call from `ensureHome`) therefore legitimately
carries `share = 999_999`, a value a foreign key to `share_definition`
would reject on every single insert, because `foreign_keys = ON` makes
SQLite enforce a referencing insert immediately, not just on delete.

The state package does not itself know the mapping from a row id to the
external `ShareID` a grant's `share` column stores: that arithmetic
(`dynamicShareIDBase + rowid`) is core's id scheme
(`core/03-share-registry.md`), and `state` must not import `core` to reach
it. `DeleteShare`'s signature therefore takes the external id as an
explicit second argument, which its one caller (`core.DeleteShare`)
already has as the value it was called with:

```go
func (d *DB) DeleteShare(ctx context.Context, rowid, shareID int64) error
```

Given that, the two remaining options from the audit's rebuild note are:

1. A real foreign key with `ON DELETE CASCADE`/`RESTRICT`: ruled out by
   the home-grant case above; it would need a `share_definition` row
   minted for the reserved home id too, which the id scheme
   (`dynamicShareIDBase + rowid`, always `> 1_000_000`) cannot produce for
   `999_999` without a special case in the id derivation that exists for
   no other reason.
2. A documented two-step delete enforced inside the store's own
   `DeleteShare`, rather than left to the caller: **this is the chosen
   option.**

`DeleteShare`'s implementation becomes:

```go
func (d *DB) DeleteShare(ctx context.Context, rowid, shareID int64) error {
    return d.Write(ctx, func(tx *sql.Tx) error {
        if _, err := tx.ExecContext(ctx, sqlDeleteGrantsForShare, shareID); err != nil {
            return err
        }
        _, err := tx.ExecContext(ctx, sqlDeleteShare, rowid)
        return err
    })
}
```

Both deletes commit in the same transaction as today's single-statement
`DeleteShare`, so the cascade is atomic: a crash between the two
statements is impossible because they are one transaction, not two calls.
This closes the actual gap the audit found (a caller that forgets the
cascade leaves orphaned grant rows) without requiring the id scheme to
move into this package, and without weakening `foreign_keys = ON` for
every other reference this database does use it for (`user`, `group`,
`session`, and every other `ON DELETE CASCADE` already in the schema, none
of which has this reserved-id problem: those all reference rows that are
always real table rows).

This is a signature change from the current `DeleteShare(ctx, rowid)`, and
it is listed under "Deliberate changes" below rather than folded silently
into the cascade note, since it is a second, smaller change riding along
with the cascade fix: `core.DeleteShare` gains one argument to pass
through.

The homes share itself has no delete path (`EnableHomes` has no
corresponding disable, per `core/11`'s "What homes deliberately do not
do"), so `DeleteShare` is never called with the home share's id in the
first place; this decision is about correctness for the shares that do
delete, not a special case carved out for the one that cannot.

**`core/03-share-registry.md` already reflects this.** Its spec for
`Core.DeleteShare` names the two-argument store call
(`state.DB.DeleteShare(ctx, rowid, shareID)`, passing `rowIDOf(id)` and
`int64(id)`) and states that a dangling grant can no longer outlive its
share. Its test list (item 12, "DeleteShare") covers the cascade: grants
naming the share are gone after the call, observed through the evaluator
after reload. No pending change to `core/03` remains from this decision.

`share_link.share` has the identical unenforced-reference shape (a link
can be created on the home share, since `homePerms` includes `acl.Share`),
and is left as-is for the same reason: no schema-level FK is possible for
the same reserved-id conflict. This document does not add a cascading
delete for links on share deletion, because the current reference does not
have one either and no audit finding raised it; it is noted here only so a
future reader does not treat the grant decision as a general rule this
document silently also applied to links.

## Rationale

- **One `Ident`, three consumers.** Fixing only the `cache` import and
  leaving `dav.go`/`favorite.go` with their own copies would satisfy the
  survey's literal wording while leaving two of the three duplications in
  place; the audit is explicit that this is a bigger fix than the survey
  states, and this document does the bigger fix.
- **A mechanical size-guard rule, not a per-method judgment.** The four
  missing paths the audit found are exactly the kind of gap a rule stated
  once and applied to every method, rather than decided per aggregate as
  each one was written, prevents from recurring. Finding a fifth gap
  (`MergeSettings`) by applying the same rule mechanically, rather than
  stopping at the audit's list, is the point of stating the rule instead
  of just fixing the four named instances.
- **Extraction, not addition, for share links.** The audit is explicit
  that the existing CRUD in `core/links.go` is the real implementation;
  treating this as "adding a `LinkStore`" rather than "moving one" would
  understate the change and risk a rebuild that reinvents behavior
  (liveness rules, the identity pin, the conditional download counter)
  that already has a correct specification in `core/10`.
- **The quota interface follows the import direction, not the reference.**
  `Reserve` returning `(ok, err)` instead of a sentinel is not a stylistic
  preference; it is forced by the store being unable to import the core's
  error type, and `core/09` already made this decision. This document's
  job is fidelity to that decision, not re-litigating it.
- **No FK where the id space does not support one.** A foreign key that
  looks like the "more correct" choice but rejects a legitimate insert
  (every home grant) is worse than a documented, atomic, application-level
  cascade: it would either force a special-cased row into
  `share_definition` for a share that is deliberately not one, or silently
  break the homes feature the moment `foreign_keys = ON` actually took
  effect for this table.

## Deliberate changes

1. **`Ident` moves to `engine/store/ident`**, and `dav.go`/`favorite.go`
   adopt it, retiring their own copies (`audit/foundation-persistence.md`,
   `store/state` finding 2; `foundation/cache.md` specifies the
   `store/cache` side of the same fix).
2. **`override.go` drops its `store/cache` import**, consuming
   `ident.Ident`, `ident.FileID` and `ident.Assignment` instead of
   `cache.Ident`, `cache.FileID` and `cache.Assignment`. All three moving
   together is what removes the import: `Ident` alone would leave
   `FileID` and `Assignment` still needing `cache`. This is the other
   half of the `store/state`-imports-`store/cache` violation
   (`01-package-survey.md` cross-layer violation 5).
3. **Size guard added to four existing paths** (`InsertShare`, `CreateOp`,
   `PutLoginFlow`, `RecordFileIDs`) and one this document additionally
   finds (`MergeSettings`), per `audit/foundation-persistence.md`,
   `store/state` finding 4.
4. **`override.go` and `settings.go` get their own `_sql.go` files**
   (`override_sql.go`, `settings_sql.go`), and `configsecret.go`'s inline
   constants move to `configsecret_sql.go`, completing the file-pair
   convention for every aggregate (`store/state` finding 7).
5. **Share links extracted from `core/links.go`/`links_sql.go` into
   `link.go`/`link_sql.go`**, implementing `core/10-share-links.md`'s
   `LinkStore` with no spelling delta (`store/state` finding 3).
6. **The quota ledger extracted from `core/quota.go` into
   `quota.go`/`quota_sql.go`**, implementing `core/09-quota-and-aggregates.md`'s
   `QuotaSink` with the `(ok bool, err error)` `Reserve` signature that
   document already specifies (`core/09` "Deliberate changes" item 1 and
   2).
7. **The grant write surface extracted from `acl/store.go`, `sql.go`,
   `grant_storage.go` into `grant.go`/`grant_sql.go`**, implementing
   `core/11-homes-and-recent.md`'s `PersistGrant` signature exactly and
   spelling `GrantRow` and `MembershipRow` out fully as this package's own
   row shapes (distinct from `acl.Grant` and `acl.Membership`, which the
   core converts to and from), extending the surface to
   `ListGrants`/`UpdateGrant`/`DeleteGrant`/`Memberships`, ending the
   store-side half of the three-site grant SQL bypass
   (`audit/presentation.md`, `httpapi/handler` findings 1 and 2,
   `cmd/stowcloud` finding 2; `01-package-survey.md` amendment on
   `11-homes-and-recent.md`).
8. **The core-level `Core.CreateGrant`/`ListGrants`/`UpdateGrant`/
   `DeleteGrant` wrapper is `core/00-overview.md`'s `grants.go`**, not
   this document's to write: this document provides the aggregate that
   wrapper calls.
9. **Grant-to-share cascade**: no foreign key; `DeleteShare` deletes the
   share's grants in the same transaction as the share row, per "Spec: the
   grant-to-share cascade decision" above (`store/state` finding 8).
   `DeleteShare` gains a `shareID int64` parameter (the external id) so
   this package can delete the matching grants without importing core's
   id-scheme arithmetic.
10. Import path moves from `internal/store/state` to `engine/store/state`;
    the grant aggregate's import path moves from `internal/acl` (the SQL
    portion only) to `engine/store/state`; the share-link aggregate's
    import path moves from `internal/core` (the SQL portion only) to
    `engine/store/state`; the quota aggregate's import path moves from
    `internal/core` (the SQL portion only) to `engine/store/state`.
11. **Phase 3 forward migrations extend two existing aggregates.**
    `compat_login_flow` gains sealed delivery columns and atomic claim/store
    methods for `http/06-login-flow-v2.md`; `dav_lock` gains transactional
    admission/snapshot methods for `http/04-webdav.md`. Existing rows remain
    readable and no plaintext credential or protocol HTTP type enters state.

    As built: migration 12 adds `claimed_ns`, `sealed_result`,
    `sealed_key_ver`, `credential_id` and `delivered_ns`, all with defaults so
    existing rows read unchanged. `ClaimLoginFlowDelivery` guards on
    `claimed_ns = 0 AND approved_user IS NOT NULL` inside the statement, so
    exactly one poll may mint; 200 concurrent claims against a real database
    yield one. `AdmitDavLock` and `SnapshotDavLocks` each run in one
    transaction, checked structurally against the source because the write
    path serializes and a timing test cannot separate one transaction from
    two. A key version outside `uint32` is refused rather than truncated,
    since a truncated version names a different key.

No other observable behavior changes: every carried-forward aggregate's
schema, statements, and race-closing patterns (login flow approval and
poll, the settings merge) are unchanged from the reference.

## Tests

Cross-cutting:

- Every method identified as needing `EnsureWritable` refuses under
  `SetWritesBlocked(true)` and succeeds once cleared, for all five fixed
  paths (`InsertShare`, `CreateOp`, `PutLoginFlow`, `RecordFileIDs`,
  `MergeSettings`'s first-ever write) and every already-compliant path,
  as a table test over every insert-shaped method in the package.
- Every method identified as not needing it succeeds regardless of the
  guard state (`UpdateShare`, `DeleteShare`, `UpdateGrant`, `DeleteGrant`,
  `Quota.Reserve`, `Quota.Release`, and the rest of the update/delete
  surface), as the negative half of the same table test.
- `dav.go` and `favorite.go` round-trip through `ident.Ident` identically
  to their current column layout (a fixture written with the old shape's
  column values reads back as an equal `ident.Ident`).
- Login-flow migration reads an old pending/approved row, atomically admits
  one delivery claimant, stores only sealed bytes, returns the same delivery
  row to retries and sweeps it at expiry.
- DAV transactional admission under concurrent exclusive/shared requests and
  one-snapshot covering-lock reads, using neutral state shapes.

Share links:

- Every test `core/10-share-links.md`'s "Tests: Store" section lists
  (round-trip of every column including NULLs, the partial-pin rule,
  narrowing validation, `Delete` requiring both id and owner, `Update`'s
  per-column constant statements, `ConsumeDownload`'s conditional update
  under concurrency, `KeyVersion` defaulting to zero) run against this
  package's implementation directly, with no core involved.

Quota:

- Every test `core/09-quota-and-aggregates.md`'s "Tests: Store side" list
  (Reserve refuses at the cap and admits below it, NULL cap always admits,
  missing user refuses, N goroutines racing Reserve for one admit exactly
  one, Commit is idempotent, Release clamps at zero, Release with a
  negative delta errors, Release of zero is a no-op) run against this
  package directly.
- `Reserve` succeeds with the size guard tripped (the deliberate
  non-gating, since it never inserts a row).

Grants:

- `PersistGrant` refuses a grant with neither or both of `User`/`Group`
  set, a zero `Share`, or an empty `Allow`.
- `PersistGrant` round-trips through `ListGrants` and stamps `CreatedNs`
  from the passed clock value, not a stored default.
- `PersistGrant` with the home share's reserved id (`999_999`) succeeds
  with no corresponding `share_definition` row (the no-FK regression test
  this document's cascade decision depends on).
- `ListGrants` with each of `User`, `Group`, `Share` set narrows correctly;
  an empty filter returns every grant.
- `UpdateGrant` cannot change `User`, `Group`, or `Share` (the statement
  has no columns for them); a wrong id is "no such grant".
- `DeleteGrant` on a wrong id is "no such grant"; on a real id, removes
  exactly that row.
- `DeleteShare` on a share with grants removes every grant referencing it
  and the share row, atomically (a fault injected between the two
  statements, via a failing second statement in a test double, leaves
  neither committed).
- `Memberships` returns every `(user, group)` pair grouped by user, feeding
  a fake evaluator reload correctly in a round-trip test.
