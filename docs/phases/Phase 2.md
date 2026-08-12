# Phase 2: store and schema

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-5-store-and-schema.md`.

## Scope

Three SQLite databases split by what losing each one costs, a real migration
runner, and node ids derived from the file's identity.

Depends on Phase 0. Blocks Phase 2.5.

## Milestones

- **2a**: §4.5's derivation, `fileid_override`, the ordered cross-database
  allocation writes, and
  the rebuild-identity test.
- **2b**: `open.go`, `migrate.go`, the pragma order, the pool, the single
  serialised write path.
- **2c**: `cache/`: schema, `node` resolution, `diretag`, the
  rebuild-on-delete test.
- **2d**: `state/`: the tables in §4.2.2.
- **2e**: the driver measurement against §4.3.1's threshold, in the guest.
- **2f**: `stowcloud migrate --from-rust`.

2a blocks 2c and nothing else.

## Traps

- **2a's test is the one that matters in this phase.** Populate a cache from a
  tree, record every id, delete the file, rebuild from the same tree in a
  different walk order, assert every id is identical. A second test forces a
  collision with a truncated hash width and asserts the override row is written
  and reproduced.
- **`node.id` is supplied on insert, not assigned.** It stays
  `INTEGER PRIMARY KEY`; the insert provides the value.
- **A no-btime identity is unique too.** SQLite treats `NULL` values as
  distinct in an ordinary unique index. Use the two partial indexes from
  §4.2.1 and test two inserts of the same `(share, dev, ino, NULL)` identity.
- **Both sides of a collision are recorded, not recomputed.** Which file takes
  the base id depends on insertion order. Record the existing holder and the
  newcomer in one state transaction, consult overrides by identity first, and
  treat every override id as reserved even when its node is absent from the
  rebuilding cache.
- **`busy_timeout` leads the pragma batch.** `journal_mode` needs an exclusive
  lock, and setting the timeout after it is what produced `database is locked`
  on a fresh database in this codebase already.
- **`foreign_keys = ON` becomes uniform.** Three of the current databases have
  it. Every relationship whose parent also lives in `state.db` gets a real
  foreign key, including grants, sessions and upload sessions belonging to
  users. Share ids and filesystem identities have no parent table in this
  database and are validated at their own boundaries.
- **A schema version higher than this binary knows is a refusal to open.** A
  downgrade silently writing an old shape into a new file is how a rollback
  becomes data loss.
- **A migration and its version bump are one transaction.** A crash mid-way
  leaves the old version and the old shape.
- **`journal.db` is capped by row count, never by age.** An age window deletes
  the whole table when the clock jumps forward on a box with a dead RTC before
  NTP corrects it. The oldest rows go in the same transaction as the upsert.
- **An unopenable `journal.db` is a warning and a disabled feature**, not a
  refusal to start.
- **2e can send 2c back.** Its threshold is in §4.3.1 and the fallback is named
  there. Do not improvise a different one under time pressure.
- **The Rust import publishes atomically.** Build a staged database, close and
  checkpoint it, then no-clobber rename and fsync the parent. A failed import
  must be retryable and must not leave `state.db` behind.

## Done when

- The gate is green, including `-race`.
- The rebuild-identity test and the forced-collision test pass.
- The migration runner refuses a database written by a newer binary.
- Deleting `cache.db` and restarting rebuilds it with no loss.
- 2e's measurement is recorded against §4.3.1's threshold, with the verdict
  stated either way.
