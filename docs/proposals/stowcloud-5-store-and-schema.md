# Store and schema - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Three SQLite databases split by what losing each one costs: a rebuild, an
account, or a listing. A real migration runner, and the pure-Go driver decision
with the measurement that would reverse it. Node ids stop being rowids and become a
derivation from the file's identity, so that deleting the cache costs a rebuild
and not a full reconciliation for every attached sync client, which is what it
costs today.

## 2. Background & Motivation

### 2.0 Why a cache exists at all, given principle 1

Worth restating before the split, because it is the thing that makes the split
legitimate rather than a convenience. Two facts a POSIX filesystem will not
give you:

- **A stable id for a file across renames.** Sync clients key their entire
  local journal on it.
- **Whether anything under this directory changed.** The sync algorithm is "if
  a directory's ETag is unchanged, skip its subtree", and without it every sync
  is a full crawl and the client is unusable on a real tree.

Both are reconstructible by walking, which is exactly what makes them
compatible with "the database is a cache you can delete". Accounts, grants and
share links are not reconstructible, which is what makes them a different kind
of thing that has been living in the same kind of file. §4.2 is that
distinction made physical.

§4.5 is the same argument taken one step further: a stable id that is not
stable across a rebuild is only half of what a sync client was promised.

### 2.1 What is there now

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

- [ ] `cache.db` deletable at any time with no loss, `state.db` the only file
      in the backup instruction, and `journal.db` losable without either.
- [ ] A numbered migration runner with a version table and a refusal to open a
      database written by a newer binary.
- [ ] `CGO_ENABLED=0` preserved, which constrains the driver choice.
- [ ] The `PINNED` bit deleted, by moving what pins to the store that keeps it.
- [ ] The pragma set applied in the order that does not deadlock, once, with the
      reason attached.
- [ ] A one-shot migration from the Rust-era files.
- [ ] Node ids derived deterministically from a file's identity, so a deleted
      cache rebuilds to the same ids, with the collision case handled durably
      and never by conflating two files.

### 3.2 Non-Goals

- [ ] A server database. This is a single-node product and SQLite in WAL mode
      is the recorded choice; a network database would add an operational
      dependency to a thing whose selling point is that it is one binary.
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
  open.go        the data directory, and which file gets which schema
  errors.go      the four errors in §5-2
  dbfile/        one SQLite file: the pragmas, the migration runner, the
                 version table, and the single serialised write path
  cache/         node, diretag, share_gen
  state/         users, sessions, grants, links, props, locks, uploads,
                 settings, audit, oidc, fileid_override
  journal/       one row per (account, file); §4.2.3
```

Every package holds its own `sql.go` of package-level statement constants
(D14). The shared file mechanism is a package of its own rather than
`open.go` and `migrate.go` beside the two halves, because `cache/` and
`state/` need it and neither may import its parent: Go has no upward import
and `store` already imports both.

Under the data directory:

```
data/
  cache.db       deletable; rebuilt on demand
  state.db       the backup instruction, and the whole of it
  journal.db     what this server did per account; §4.2.3
  search/        the index cache directory
  tls/           the self-signed certificate, generated on first run
  setup-token    present only until the first administrator exists
```

**The master key should not be in this directory**, and where it is is
configuration rather than layout. It lives wherever `SC_MASTER_KEY_FILE` points,
defaulting to `master.key` here, and the shipped compose file puts it on its own
mount instead: backing up the data directory should not back up the key that
decrypts it. Resolving inside the data directory is a startup warning rather
than a refusal, because it is the default and refusing would make an unconfigured
install fail to start. [`6`](stowcloud-6-auth-and-acl.md) §4.3.10 owns the rest
of the key's lifecycle.

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

**`node.id` is supplied, not assigned.** The column stays
`INTEGER PRIMARY KEY`, so it is still SQLite's rowid, but the insert provides
the value instead of letting SQLite pick the next one. The value is derived from
the file's identity by §4.5, which is what makes a rebuilt cache produce the
same ids as the one it replaced.

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
| `fileid_override` | new; §4.5 |

`dav_prop`, `dav_lock`, `favorite` and `share_link`'s target are the ones that
move, and moving them is what deletes the `PINNED` bit. Today they key by
`fileid`, which only `cache.db` mints, so a durable row points into a
rebuildable store and the store has to be told not to reap it. In `state.db`
they key by the identity tuple `(share, dev, ino, btime_ns)`, which is a fact
about the file rather than about the cache. Deleting `cache.db` then costs a
lookup, not a dangling row, and nothing has to be pinned.

`share_link` is the fourth because the current tree gives it a `fileid` column
for the same purpose, so that a link survives a rename, and that column is the
same dangling reference as the other three.

**A grant's principal is two nullable columns and a `CHECK`, not a kind and an
id.** §4.3.2 wants a grant to carry a foreign key to the user it belongs to, and
a polymorphic reference cannot carry one; the failure it is there for, a grant
outliving the account it was made for, is exactly the one nothing currently
catches.

The list is what this phase builds and not what the product ends with. A table
whose only caller is a later phase, `key_version` and the TOTP replay set among
them, arrives with that phase as a migration, which is what the runner is for.

#### 4.2.3 `journal.db`, and why it is neither of the other two

One row per `(account, file)` holding the last thing that account did to it. It
is what the Recent Files destination reads, and it is a third file rather than a
table in either of the two above, for a reason on each side:

- **Not in `cache.db`**, because it cannot be rebuilt from anything. There is no
  way to reconstruct who wrote what before the record existed.
- **Not in `state.db`**, because losing it costs a listing and not an account.
  Keeping it separate makes that difference visible in the data directory, where
  an operator deciding what to back up can see it.

Three properties are carried over exactly, because each was arrived at by
thinking about a failure:

1. **It is not an audit log.** The file write has already succeeded by the time
   a row is written, so a failure here is logged and dropped. Nothing may treat
   the absence of a row as evidence that a write did not happen, and the audit
   log in `state.db` is the thing that may.
2. **It is capped by row count per account, not by age.** A prune comparing a
   stored timestamp against now deletes the whole table when the clock jumps
   forward, which is an ordinary event on a small box with a dead RTC before NTP
   corrects it. The cap is deterministic and clock-independent, and the oldest
   rows beyond it are deleted in the same transaction as the upsert. This is D8
   applied to a retention policy rather than to a timestamp.
3. **It is not an activity stream.** No per-event history, and no reader other
   than the account itself. Rebuilding the reference server's Activity app is a
   recorded non-goal, and the name avoids inviting it.

A `journal.db` that cannot be opened is a warning and a disabled feature, not a
refusal to start. The server still serves files; it just stops recording what it
did with them.

#### 4.2.4 Migrations

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
unmigratable and have the runner discard what is there and rebuild. That is
legitimate here and nowhere else, and it is what makes cache schema changes
cheap.

Discarding drops every table and index inside the migration's own transaction
rather than unlinking the file. Unlinking cannot be part of the transaction
that bumps the version, so a crash between the two leaves a fresh empty file
claiming the new version, and reopening would have to reapply the pragmas on a
pool that is already handing out connections.

### 4.3 Core Logic

#### 4.3.1 The driver

`modernc.org/sqlite`, a pure-Go translation of SQLite that works under
`CGO_ENABLED=0`. The alternative is a cgo driver wrapping the C library, which
costs the static binary, the toolchain-free cross build, and the whole of
document 2 §4.3.1.

**This is the one decision in the port that a measurement can reverse**, and the
criteria are set here rather than argued about later:

- The workload is the resource-budget one from
  [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3 F4: a cold walk
  populating `node` for a multi-million-file tree on rotational storage, then
  steady-state ETag invalidation as the watcher feeds changes in.
- The measurement is taken in the Linux VM at Phase 2, against the Rust
  implementation's numbers on the same tree. The loop that makes that possible
  is assumption A4, and it is settled: the Windows box cross-compiles a test
  binary with `go test -c` and the guest runs it with no Go toolchain and no
  libc dependency of its own
  ([`2`](stowcloud-2-gate-and-toolchain.md) §4.3.3). The benchmark that decides
  this driver is one of those binaries, so nothing about it waits on the guest
  growing a toolchain.
- The threshold is a cold-populate that takes more than **three times** as long,
  or a steady-state invalidation that cannot keep up with the walk feeding it.
- The fallback, if it trips, is stated now so it is not improvised: `cache.db`
  moves to a purpose-built append-only file with an in-memory index (which is
  what it wants to be anyway, since it has one lookup pattern and one write
  pattern), and SQLite stays for `state.db`, where the write rate is a handful
  of rows per login.

#### 4.3.1.1 The measurement, taken at Phase 2

**Verdict: the driver stands.** Both halves of the threshold pass, and the
numbers are below with the one thing that could not be measured named rather
than glossed.

The workload is `internal/store/cache`'s `TestDriverMeasurement` and
`crates/sc-meta/tests/measure.rs`, which do the same three things: a cold walk
allocating an id per file under a directory eight deep, the same walk with a
walker's batching, and steady-state invalidation of that eight-deep chain with
the directory's aggregate stored again. Each is one transaction per file,
because that is what the implementation being compared against does.

| Where | Rows | Cold populate | Batched | Invalidation |
|---|---|---|---|---|
| Guest, Linux 6.12, 4 vCPU, Go | 2,000,000 | 124.87 s (16,017/s) | 45.40 s (44,050/s) | 33,403/s |
| Host, Windows, Go | 2,000,000 | 335.74 s (5,957/s) | 119.50 s (16,737/s) | 18,718/s |
| Host, Windows, Rust | 2,000,000 | 115.11 s (17,375/s) | not available | 12,070/s |
| Host, Windows, Go | 200,000 | 15.94 s (12,543/s) | 4.73 s (42,277/s) | 20,869/s |
| Host, Windows, Rust | 200,000 | 7.40 s (27,012/s) | not available | 14,973/s |

**Against the three-times threshold: 2.92x at two million rows and 2.15x at two
hundred thousand**, both on the host, where the two implementations can be run
side by side.

**Against "keeps up with the walk feeding it": 33,403 invalidations a second
against a walk feeding 16,017**, in the guest. It keeps up with margin, and the
implementation being replaced is the slower of the two on that half.

Two things this did not measure, said plainly rather than left to be assumed:

- **The comparison is not in the guest.** Running the Rust half there needs a
  musl cross build, and the toolchain for that (`zig`) is not on the box the
  port is being built on. So the ratio comes from the host and the absolute
  numbers from the guest. The host is the pessimistic side of that split: the
  same Go binary walks the same two million files in 124.87 s in the guest and
  335.74 s on the host, so the ratio measured there is an upper bound rather
  than a flattering one.
- **The disk is not rotational.** The guest's volume is a virtual disk on the
  host's SSD. Rotational storage would slow both implementations, and the
  cold-populate ratio is what the threshold is written in.

Two changes came out of taking the measurement rather than out of a preference,
and both are in the commit that records it. The override table is consulted for
every id allocated, so an empty one is now answered from a counter loaded once
instead of a query against a second database per file. And every statement in
`cache/` is prepared once rather than compiled on each call, which is what D14
asked for in the first place; between them the cold walk went from 3.67x to
2.15x on the same host and the same rows.

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

`foreign_keys = ON` is **uniform**, which is the change. SQLite defaults it off
per connection, and today three of the current databases set it and the rest do
not. The tables that gain it by being folded into `state.db` are `grant`,
`dav_prop`, `dav_lock`, `upload_session` and `settings`, all of which have real
referential structure that nothing currently enforces: a grant belongs to a user
and a share, an upload session to a user.

#### 4.3.3 Concurrency

WAL gives one writer and many readers. The pool is sized for readers, and every
write goes through a single serialised path rather than relying on
`busy_timeout` to sort out contention between eight connections. `busy_timeout`
stays as the backstop for the external writer case: the search index and a
second process are not the only things that can touch these files.

Every statement is a package-level constant prepared once per connection (D14).
Nothing builds a query string.

#### 4.3.4 The write-blocked guard, and why it is off by default

The current tree has a database-size guard that trips and reports `degraded`
through `GET /api/health`. It is carried over unchanged in behaviour, including
the part that matters: a tripped guard blocks writes and keeps serving reads,
because the failure it prevents is a full disk taking the whole store with it.

It is also **off by default**, and that is deliberate rather than an oversight
waiting to be tidied up. It is
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S11's second half:
an instance that stops accepting writes because a cache grew is worse than one
that uses more disk than expected. The first half of that stance points the
other way, and D3 makes hardening a startup refusal, so the two look
inconsistent until the split is named. It is not "fail closed" versus "fail
open", it is **fail closed on a security control and fail open on the user's
own data**. A sandbox that silently did not apply is a lie about the product's
guarantees; a cache that grew past a guess is an inconvenience, and refusing
writes turns it into an outage.

### 4.4 Migration from the Rust tree

`stowcloud migrate --from-rust <data-dir>` reads the old auth and upload
databases read-only and writes `state.db`. It does not touch the old files, does
not run automatically at startup, and refuses if `state.db` already exists. It
is removed from the tree one release after cutover.

The metadata cache is not migrated. It regenerates, which is what it is for, and
that is the whole argument for the split in §4.2.

### 4.5 Node ids are derived, not assigned

**Decided.** `node.id` is derived from the file's identity, so a deleted cache
rebuilds to the same ids.

#### 4.5.1 Why it has to be

`node.id` is handed to sync clients as `oc:fileid`
(`crates/sc-compat-nc/src/props.rs:282`) and, with the instance id, as `oc:id`.
Today it is a rowid: minted in insertion order, and re-minted from scratch when
the cache is deleted.

So "delete the cache and it rebuilds" is true for this server and false for
every sync client attached to it. After a rebuild every file has a new fileid,
and a client's reconciliation of that is at best a full rescan and at worst a
re-download of everything it holds. Principle 1 is currently conditional in a
way no document admits, and deriving the id is what makes it unconditional.

#### 4.5.2 The derivation

```
key = "stowcloud/fileid/v1"
     || u64be(share) || u64be(dev) || u64be(ino)
     || u8(btime_present) || u64be(btime_ns_or_zero)
     || u32be(attempt)

id  = 1 + (be64(SHA-256(key)[0:8]) & (2^63 - 1)) % (2^63 - 1)
```

Four properties, each chosen rather than inherited:

- **The identity tuple is `(share, dev, ino, btime_ns)`**, the same one
  `node_ident` already keys on. All four are needed, and the reason is
  recorded in this codebase rather than theoretical: `dev` and `ino` alone are
  not enough on a backend that cannot distinguish two directories, and
  `btime_ns` alone is not either, since two directories can share a creation
  tick or report none.
- **A present and an absent btime are domain-separated by the flag byte**, so a
  file with `btime_ns = 0` and a file with no btime at all do not derive the
  same id.
- **63 bits, and never zero.** `oc:fileid` is consumed as a signed 64-bit
  integer, so an id with the top bit set would reach some clients as negative.
  Zero is the "no id" sentinel the property emitter already uses.
- **`attempt` exists for §4.5.3** and is zero in every normal case.

**The hash is `crypto/sha256`, and it was BLAKE3 until this phase built it.**
BLAKE3 needed no argument in the Rust tree, where it was already a dependency.
In the Go tree it is a module that
[`0`](stowcloud-0-motivation-and-findings.md) §6-2 admits at Phase 6 and only
because a TUS `Upload-Checksum` header is a client-facing algorithm that
cannot be swapped for a stdlib hash; the same table sends every internal digest
to `crypto/sha256`, and [`9`](stowcloud-9-upload.md) §4.3.3 records that split
as correction C1. Nothing outside this server ever recomputes a `node.id`, so
this hash is internal, and §6-2 here says the only dependency this phase takes
is the driver.

The choice is a one-way door: the hash is part of the derivation, so changing
it changes every id. `OPEN-QUESTIONS.md` Q4 records that, and the version
string in the key is what a deliberate change would move.

#### 4.5.3 The collision case, which is not theoretical

Sixty-three bits gives a birthday collision at roughly 3e9 files. At ten million
files the probability is about 5e-6 and at a hundred million about 5e-4. Small,
and **not** small enough to wave away, because of what a collision does here.

It has already happened here once. The portable filesystem backend returned a
hardcoded `(0, 0)` for `dev`/`ino`, so a share root and one of its own
subdirectories derived the same identity and reported the same fileid over
WebDAV. A sync client keys its journal on that id, so to the client two
different resources **were** the same file. The fix was to read real volume and
file-index identity; the failure mode is what matters here, and a hash
collision reproduces it exactly, persistently rather than transiently.

So the derivation is a proposal and the table is the authority:

```sql
CREATE TABLE fileid_override (
  share         INTEGER NOT NULL,
  dev           INTEGER NOT NULL,
  ino           INTEGER NOT NULL,
  btime_present INTEGER NOT NULL,
  btime_ns      INTEGER NOT NULL,
  id            INTEGER NOT NULL UNIQUE,
  PRIMARY KEY (share, dev, ino, btime_present, btime_ns)
) WITHOUT ROWID;
```

**The btime is two columns here and one in `node`**, and the difference is
SQLite's rather than a choice: a `WITHOUT ROWID` table enforces `NOT NULL` on
every column of its primary key, so the nullable `btime_ns` this table was
first written with refuses exactly the rows a filesystem carrying no birth time
produces. The pair is the derivation's own flag byte, stored, so an absent
btime and a zero one stay different facts on disk as well as in the hash.

Allocation:

1. Look up `fileid_override` for the identity. A hit is the answer, always, and
   it is consulted first precisely so that a past decision is never revisited.
2. Otherwise derive with `attempt = 0`. If the id is free in `node`, or already
   held by this same identity, take it.
3. If it is held by a **different** identity, increment `attempt` and derive
   again until an id is free, then write `(identity, id)` into
   `fileid_override` before returning it.

**Step 3's write commits before the `node` row does, and the order is the
whole of the crash story.** The two rows are in two files, and SQLite's
cross-database atomic commit does not exist in WAL mode, so there is no
transaction that covers both. Committing the override first means a crash
between them leaves an override with no node, which is the state every rebuild
starts from anyway. The other order leaves a node holding an id that nothing
records, and the next rebuild races the collision again and may answer
differently.

Step 3's outcome depends on which of the two colliding files was seen first,
which is insertion order, which a rebuild does not reproduce. That is exactly
why the result is recorded: the order decides once, the row makes it permanent,
and every later rebuild reads the row instead of racing again.

`fileid_override` lives in `state.db` because it is the one part of the
assignment that computation cannot reproduce, and §4.2's rule is that anything
not reconstructible belongs in the durable half. It is expected to be empty. A
row appearing in it is worth an operator-visible log line, not because anything
is wrong but because it is the first evidence that the corpus has reached the
size where the estimate above stops being abstract.

#### 4.5.4 What the id is stable against, and what it is not

| Event | Id survives |
|---|---|
| deleting `cache.db` and rebuilding | yes, and that is the point |
| renaming or moving a file within a share | yes; identity is not the path |
| restarting, upgrading, migrating the schema | yes |
| the filesystem's device number changing (some dm and LVM setups renumber across reboots) | **no** |
| a restore or a copy that changes inode numbers | **no** |
| re-registering a share under a different share id | **no** |

The three "no" rows are not a regression: `node_ident` already keys on `dev`, so
every one of them already invalidates every row in the current tree. The
difference is that they are now written down.

#### 4.5.5 The one-time cost at cutover

The Rust build assigns rowids and the Go build derives, so **every fileid
changes once, at cutover**, and every attached sync client performs a full
reconciliation. That is stated here and repeated in
[`stowcloud-17`](stowcloud-17-parity-and-cutover.md) §4.4 rather than
discovered by whoever runs it.

Carrying the old assignments across is possible and is deliberately not built:
`stowcloud migrate --from-rust` could read the old `node` table and write every
`(identity, id)` into `fileid_override`, which would preserve every id exactly.
The cost is that the override table becomes large and permanent for that
install, which defeats the reason the derivation exists. The mechanism is
already in place if a real deployment turns out to need it; adding the import is
a select and an insert loop in a subcommand that already opens both databases.

#### 4.5.6 Where else this is written down

[`stowcloud-13-compat-nc.md`](stowcloud-13-compat-nc.md) §4.3 for the client
consequence, and the operator documentation, because the current situation is
that neither says anything at all.

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

// Journal is the third file, and nil when it could not be opened: §4.2.3 makes
// that a disabled feature rather than a refusal to start.
func (s *Store) Journal() *journal.DB
```

```go
// Write runs fn in this database's single serialised write path. Readers are
// not blocked. It is a method on each of the three handles rather than on
// Store, because there are three files and one serialised path per file, and a
// Write on the facade could not say which one it locks.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error
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

// AllocateID returns the id for ident, deriving it per §4.5.2 and consulting
// the override table first. It is the only function that may write an id. tx is
// the cache's write transaction; an override row is committed to state.db
// before this returns, in the order §4.5.3 gives and for the reason it gives.
//
// A caller must not cache the result across a share re-registration: the share
// id is part of the derivation.
//
// It takes the caller's context as well as the transaction, because the
// override write is a second database's transaction and a caller that gave up
// should not be left waiting on it.
func (d *DB) AllocateID(ctx context.Context, tx *sql.Tx, ident Ident) (FileID, error)

// Upsert returns the stable id for the file st names, allocating on first
// sight and otherwise refreshing what a rename or a write moved. It is the
// only thing that inserts into node, so a deployment that never asks for a
// stable id creates no rows at all.
func (d *DB) Upsert(ctx context.Context, tx *sql.Tx,
    share vfs.ShareID, parent FileID, name string, st vfs.Stat) (FileID, error)

// DeriveID is the pure half of AllocateID: no I/O, no uniqueness check. It is
// exported so the rebuild-identity test can assert the derivation directly
// rather than through the table.
func DeriveID(ident Ident, attempt uint32) FileID
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
| Phase 2a | §4.5: the derivation, `fileid_override`, the allocation transaction, and the rebuild-identity test | S | Phase 0 | heavycaffeiner |
| Phase 2b | `open.go`, `migrate.go`, the pragma order, the pool, the write path | M | Phase 0 | heavycaffeiner |
| Phase 2c | `cache/`: schema, `node` resolution, `diretag`, the rebuild-on-delete test | M | 2a, 2b | heavycaffeiner |
| Phase 2d | `state/`: schema and the tables in §4.2.2 | M | 2b | heavycaffeiner |
| Phase 2e | The driver measurement against §4.3.1's threshold, in the Linux VM | S | 2c | heavycaffeiner |
| Phase 2f | `stowcloud migrate --from-rust` | S | 2d | heavycaffeiner |

2a blocks 2c and nothing else. Its test is the one that matters most in this
phase: populate a cache from a tree, record every id, delete the file, rebuild
from the same tree in a different walk order, and assert every id is identical.
A second test forces a collision by deriving with a truncated hash width, and
asserts the override row is written and that a rebuild reproduces the same
assignment.

2e can only run once 2c exists, and its outcome is allowed to send 2c back.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `modernc.org/sqlite` | the driver, under `database/sql` |

`database/sql` and its pooling are standard library. Nothing else.

## 7. References

- `crates/sc-meta/src/lib.rs`: the schema this carries over, the `PINNED` bit
  this deletes, and the `busy_timeout` ordering comment quoted in §4.3.2.
- `crates/sc-compat-nc/src/props.rs:282`: `oc:fileid`, the reason §4.5 exists.
- `crates/sc-meta/src/node.rs`: the identity tuple and the allocation this
  replaces with a derivation.
- `crates/sc-server/tests/compat_fileid_uniqueness.rs`: the existing uniqueness
  test, which §4.5.3's collision test extends rather than replaces, and the
  record of the incident §4.5.3 refuses to reproduce.
- `crates/sc-meta/src/etag.rs`, `admin.rs`: the aggregate and the size guard
  §4.3.4 carries over.
- `crates/sc-auth/src/db.rs`, `crates/sc-upload/src/db.rs`: the two stores
  §4.4's migration reads.
- SQLite: WAL mode, `busy_timeout`, `WITHOUT ROWID`, the `INTEGER PRIMARY KEY`
  rowid alias §4.5 turns on.
