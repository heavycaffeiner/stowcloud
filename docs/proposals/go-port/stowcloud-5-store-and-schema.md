# Store and schema - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Two SQLite databases split by whether their contents can be regenerated from the
filesystem, a real migration runner, and the pure-Go driver decision with the
measurement that would reverse it. One open question is raised rather than
answered: whether a node id has to survive a cache rebuild, because it is
`oc:fileid` on the wire.

## 2. Background & Motivation

The current tree has four stores: `sc-meta`'s metadata cache, `sc-auth`'s
accounts, `sc-upload`'s sessions, and the search index directory. They are split
by which crate owns them, and the split that actually matters cuts across all
four.

Principle 1 says the database is a cache you can delete and rebuild. The
architecture document then has to carve out the exception in the same paragraph:
accounts, grants and share links are not reconstructible from the tree and must
be backed up. That exception is correct and it is invisible on disk, so the
backup instruction is "back up these files but not those" and the way to get it
wrong is to follow the principle.

Three further problems come with it:

- Every schema is `CREATE TABLE IF NOT EXISTS`, which cannot express a change.
  There is no version column anywhere, so a shape change is a manual migration
  or a deleted database.
- `sc-meta`'s `node.flags` carries a `PINNED` bit set by `set_prop` so that GC
  does not reap a row something else references by `fileid`. Its own comment
  says nothing clears it automatically. It exists because a durable fact (a dead
  WebDAV property) lives in a rebuildable store and has to pin part of it.
- `busy_timeout` ordering is a landmine that has already fired once: the current
  code's comment records `database is locked` on a fresh database in `sc-auth`
  because `journal_mode` was set before the timeout that governs contention.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] `cache.db` deletable at any time with no loss, and `state.db` the only
      file in the backup instruction.
- [ ] A numbered migration runner with a version table and a refusal to open a
      database written by a newer binary.
- [ ] `CGO_ENABLED=0` preserved, which constrains the driver choice.
- [ ] The `PINNED` bit deleted, by moving what pins to the store that keeps it.
- [ ] The pragma set applied in the order that does not deadlock, once, with the
      reason attached.
- [ ] A one-shot migration from the Rust-era files.
- [ ] The fileid question settled before a schema is written.

### 3.2 Non-Goals

- [ ] A server database. Single node, and `docs/proposals/stowcloud-12` records
      the decision.
- [ ] An ORM or a query builder. D14 wants package-level constant statements,
      prepared once, and a builder is exactly the thing that makes a query
      string dynamic.
- [ ] Changing the `node` table's two load-bearing properties: no path column
      and no index besides `node_ident`.
- [ ] Moving the search index into SQLite. It is a cache directory with its own
      format and [`stowcloud-11-search.md`](stowcloud-11-search.md) owns it.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/store
  open.go        open, pragmas, the pool
  migrate.go     the runner and the version table
  cache/         node, diretag, share_gen
  state/         users, sessions, grants, links, props, locks, uploads,
                 settings, audit, oidc
  sql.go         every statement, as package-level constants (D14)
```

Two files on disk under the data directory:

```
data/
  cache.db       deletable; rebuilt on demand
  state.db       the backup instruction, and the whole of it
  search/        the index cache directory
  secrets/       the master key, unchanged
```

### 4.2 Data Model Changes

#### 4.2.1 `cache.db`

Carried over unchanged in shape, because the shape is right:

```sql
CREATE TABLE node (
  id       INTEGER PRIMARY KEY,
  share    INTEGER NOT NULL,
  parent   INTEGER NOT NULL,
  name     TEXT    NOT NULL,
  dev      INTEGER NOT NULL,
  ino      INTEGER NOT NULL,
  btime_ns INTEGER,
  flags    INTEGER NOT NULL,
  size     INTEGER,
  mtime_ns INTEGER
);
CREATE UNIQUE INDEX node_ident ON node(share, dev, ino, btime_ns);

CREATE TABLE diretag (
  share  INTEGER NOT NULL,
  fileid INTEGER NOT NULL,
  etag   TEXT    NOT NULL,
  rsize  INTEGER NOT NULL,
  rcount INTEGER NOT NULL,
  gen    INTEGER NOT NULL,
  valid  INTEGER NOT NULL,
  PRIMARY KEY (share, fileid)
) WITHOUT ROWID;

CREATE TABLE share_gen (
  share INTEGER PRIMARY KEY,
  gen   INTEGER NOT NULL
) WITHOUT ROWID;
```

**No path column, and no index besides `node_ident`.** Path resolution walks the
`parent` chain. That is what makes a directory rename one row update instead of
a subtree fan-out, and it is the single most consequential thing in this schema.

`node.flags` keeps its `IS_DIR` bit and loses `PINNED` (§4.2.2).

#### 4.2.2 `state.db`

Everything the filesystem cannot regenerate:

| Table | From |
|---|---|
| `user`, `group`, `membership` | `sc-auth` |
| `session`, `app_password`, `totp_secret`, `recovery_code` | `sc-auth` |
| `oidc_link` | `sc-auth` |
| `grant` | `sc-acl` |
| `share_link` | `sc-core` |
| `dav_prop`, `dav_lock` | `sc-meta`, `sc-dav` |
| `favorite` | `sc-compat-nc` |
| `upload_session`, `upload_interval` | `sc-upload` |
| `settings` | `sc-server` |
| `audit` | `sc-auth` |

`dav_prop`, `dav_lock` and `favorite` are the ones that move, and moving them is
what deletes the `PINNED` bit. Today they key by `fileid`, which only `cache.db`
mints, so a durable row points into a rebuildable store and the store has to be
told not to reap it. In `state.db` they key by the identity tuple
`(share, dev, ino, btime_ns)`, which is a fact about the file rather than about
the cache. Deleting `cache.db` then costs a lookup, not a dangling row, and
nothing has to be pinned.

#### 4.2.3 Migrations

```sql
CREATE TABLE schema_version (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL
);
```

One ordered slice of migration functions per database. On open:

1. Read the version; absent means 0.
2. If it is **higher** than this binary knows, refuse to open with
   `ErrSchemaAhead`. A downgrade quietly writing an old shape into a new file is
   how a rollback turns into data loss.
3. Apply each pending migration and write the new version **in the same
   transaction**, so a crash mid-migration leaves the old version and the old
   shape rather than a half-applied one.

For `cache.db` there is a second option a migration may take: declare itself
unmigratable and have the runner delete and rebuild the file. That is legitimate
here and nowhere else, and it is what makes cache schema changes cheap.

### 4.3 Core Logic

#### 4.3.1 The driver

`modernc.org/sqlite`, a pure-Go translation of SQLite that works under
`CGO_ENABLED=0`. The alternative is a cgo driver wrapping the C library, which
costs the static binary, the toolchain-free cross build, and the whole of
document 2 §4.3.1.

**This is the one decision in the port that a measurement can reverse**, and the
criteria are set here rather than argued about later:

- The workload is `docs/proposals/stowcloud-11-footprint.md`'s: a cold walk
  populating `node` for a large tree, then steady-state ETag invalidation.
- The measurement is taken in the Linux VM at Phase 2, against the Rust
  implementation's numbers on the same tree.
- The threshold is a cold-populate that takes more than **three times** as long,
  or a steady-state invalidation that cannot keep up with the walk feeding it.
- The fallback, if it trips, is stated now so it is not improvised: `cache.db`
  moves to a purpose-built append-only file with an in-memory index (which is
  what it wants to be anyway, since it has one lookup pattern and one write
  pattern), and SQLite stays for `state.db`, where the write rate is a handful
  of rows per login.

#### 4.3.2 Pragmas

Applied on **every** pooled connection, in this order:

```sql
PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA wal_autocheckpoint = 1000;
PRAGMA journal_size_limit = 67108864;
PRAGMA cache_size = -16000;
PRAGMA temp_store = MEMORY;
PRAGMA foreign_keys = ON;
```

`busy_timeout` leads, and it is not a stylistic ordering. It governs how every
statement after it behaves under contention, and `journal_mode` is the one that
needs an exclusive lock. Setting the timeout afterwards means the pragma most
likely to contend runs without it, which is what produced `database is locked` on
a fresh database in the current tree.

`page_size` and `auto_vacuum` are database-level, must be set before the first
table exists, and are applied once on a bootstrap connection.

`foreign_keys = ON` is new. `state.db` has real referential structure (a session
belongs to a user, a grant to a user and a share) and SQLite defaults it off per
connection.

#### 4.3.3 Concurrency

WAL gives one writer and many readers. The pool is sized for readers, and every
write goes through a single serialised path rather than relying on
`busy_timeout` to sort out contention between eight connections. `busy_timeout`
stays as the backstop for the external writer case: the search index and a
second process are not the only things that can touch these files.

Every statement is a package-level constant prepared once per connection (D14).
Nothing builds a query string.

#### 4.3.4 The write-blocked guard

The current tree has a database-size guard that trips and reports `degraded`
through `GET /api/health`. It is carried over unchanged in behaviour, including
the part that matters: a tripped guard blocks writes and keeps serving reads,
because the failure it prevents is a full disk taking the whole store with it.

### 4.4 Migration from the Rust tree

`stowcloud migrate --from-rust <data-dir>` reads the old auth and upload
databases read-only and writes `state.db`. It does not touch the old files, does
not run automatically at startup, and refuses if `state.db` already exists. It
is removed from the tree one release after cutover.

The metadata cache is not migrated. It regenerates, which is what it is for, and
that is the whole argument for the split in §4.2.

### 4.5 The open question: is a node id allowed to change?

**Raised, not answered.** This is contradiction C4 from the folder README, and
Phase 2 must settle it before it writes a line of schema.

`node.id` is handed to sync clients as `oc:fileid`
(`crates/sc-compat-nc/src/props.rs:282`) and, with the instance id, as `oc:id`.
Today it is a SQLite `INTEGER PRIMARY KEY`, which is a rowid: minted in
insertion order, and re-minted from scratch when `cache.db` is deleted.

So "delete the cache and it rebuilds" is true for this server and false for
every sync client attached to it. After a rebuild, every file has a new fileid,
and a Nextcloud client's reconciliation of that is at best a full rescan and at
worst a re-download of everything it holds.

Two candidate answers:

1. **Accept it, and say so.** A cache rebuild becomes an operator action with a
   documented consequence, not a free one. Cheapest, and it makes principle 1
   conditional in a way the documentation currently does not admit.
2. **Derive the id deterministically** from `(share, dev, ino, btime_ns)`, so a
   rebuild produces the same ids. This makes the id stable across a rebuild but
   not across a filesystem restore that changes inode numbers, and it needs a
   64-bit derivation with a collision story, because `oc:fileid` is an integer
   and a hash collision here is two files sharing an identity.

What decides between them is evidence, not preference: what a current Nextcloud
client actually does when a file's id changes but its path, size, mtime and
ETag do not. `crates/sc-server/tests/compat_fileid_uniqueness.rs` exists and is
the place that question is already partly answered.

Whichever is chosen, it is written into
[`stowcloud-13-compat-nc.md`](stowcloud-13-compat-nc.md) and into the operator
documentation, because the current situation is that neither says anything.

## 5. API Design

### 5-1. New / Modified

```go
package store

// Open opens both databases under dir, applies pragmas in the order §4.3.2
// requires, and runs pending migrations. It returns ErrSchemaAhead if either
// file was written by a newer binary, because a downgrade writing an old shape
// into a new file loses data silently.
func Open(dir string, opt Options) (*Store, error)

// Cache is the rebuildable half. Every method on it is allowed to answer "I do
// not know" and have the caller fall back to the filesystem; nothing may treat
// a missing row as a missing file.
func (s *Store) Cache() *cache.DB

// State is the durable half. It is the entire backup instruction.
func (s *Store) State() *state.DB

// Write runs fn in the single serialised write path. Readers are not blocked.
func (s *Store) Write(ctx context.Context, fn func(*sql.Tx) error) error
```

```go
package cache

// Ident is a file's identity as the kernel reports it. It is what durable rows
// in state.db reference, so that deleting this database costs a lookup rather
// than leaving a dangling row.
type Ident struct {
    Share ShareID
    Dev   uint64
    Ino   uint64
    Btime *int64 // nil where the filesystem carries no btime
}

// Resolve walks the parent chain to a virtual path. There is no path column
// and there will not be one: a directory rename is one UPDATE because of it.
func (d *DB) Resolve(id FileID) (SharePath, error)
```

### 5-2. Error Handling

| Error | Meaning |
|---|---|
| `ErrSchemaAhead` | the file was written by a newer binary; startup refuses |
| `ErrMigrationFailed` | a migration failed; the transaction rolled back and the old version stands |
| `ErrWritesBlocked` | the size guard tripped; reads continue, writes refuse, health reports `degraded` |
| `ErrBusy` | `busy_timeout` expired; surfaced rather than retried forever |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 2a | §4.5 settled, with the evidence and the answer written into two documents | S | none | heavycaffeiner |
| Phase 2b | `open.go`, `migrate.go`, the pragma order, the pool, the write path | M | Phase 0 | heavycaffeiner |
| Phase 2c | `cache/`: schema, `node` resolution, `diretag`, the rebuild-on-delete test | M | 2a, 2b | heavycaffeiner |
| Phase 2d | `state/`: schema and the tables in §4.2.2 | M | 2b | heavycaffeiner |
| Phase 2e | The driver measurement against §4.3.1's threshold, in the Linux VM | S | 2c | heavycaffeiner |
| Phase 2f | `stowcloud migrate --from-rust` | S | 2d | heavycaffeiner |

2a blocks 2c and nothing else. 2e can only run once 2c exists, and its outcome
is allowed to send 2c back.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `modernc.org/sqlite` | the driver, under `database/sql` |

`database/sql` and its pooling are standard library. Nothing else.

## 7. References

- `crates/sc-meta/src/lib.rs`: the schema this carries over, the `PINNED` bit
  this deletes, and the `busy_timeout` ordering comment quoted in §4.3.2.
- `crates/sc-compat-nc/src/props.rs:282`: `oc:fileid`, the reason §4.5 exists.
- `crates/sc-server/tests/compat_fileid_uniqueness.rs`: where the fileid
  question is already partly answered.
- `docs/proposals/stowcloud-11-footprint.md`: the workload §4.3.1 measures
  against, and the two findings that shaped this schema.
- `docs/proposals/stowcloud-2-core-vfs.md`: directory ETags and the aggregate.
- SQLite: WAL mode, `busy_timeout`, `WITHOUT ROWID`, the `INTEGER PRIMARY KEY`
  rowid alias §4.5 turns on.
