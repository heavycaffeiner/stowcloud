# Phase 2.5: contract corrections before auth

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then proposals
[`1`](../proposals/stowcloud-1-defensive-standard.md),
[`2`](../proposals/stowcloud-2-gate-and-toolchain.md),
[`3`](../proposals/stowcloud-3-vfs-and-paths.md),
[`5`](../proposals/stowcloud-5-store-and-schema.md),
[`6`](../proposals/stowcloud-6-auth-and-acl.md),
[`8`](../proposals/stowcloud-8-http-and-api.md),
[`15`](../proposals/stowcloud-15-deployment.md) and
[`17`](../proposals/stowcloud-17-parity-and-cutover.md), plus
[`OPEN-QUESTIONS.md`](../proposals/OPEN-QUESTIONS.md) Q5.

## Scope

Correct the Phase 0 to 2 implementation where the full proposal audit found
that contradictory documents had already produced unsafe or incomplete code.
This is a repair phase, not the start of auth. It changes the cache schema,
state share-link representation, collision authority, Rust import coverage and
publication, the cross-process data-directory lock, the Phase 0 REST error
contract, and Phase 1 declarations and gates.

Depends on Phases 1 and 2. Blocks Phase 3.

## Milestones

- **2.5a**: add cache schema version 2 with the two partial identity indexes;
  rebuild the cache through a discard migration and add the no-btime duplicate
  test.
- **2.5b**: add state schema version 2 with a coherent optional share-link
  identity; correct the Rust import's path-plus-identity translation, active
  WebDAV locks and reasoned drop report.
- **2.5c**: make `fileid_override` reserve ids as well as answer identities;
  record both sides of a first collision in one state transaction and extend
  the collision tests across rebuild orders and later arrivals.
- **2.5d**: finish the staged `migrate --from-rust` publication by giving each
  invocation its own reserved staging name; add the source manifest and shared
  instance lock, retain atomic no-clobber publication, and make concurrent,
  failed and interrupted paths retryable.
- **2.5e**: restore the existing native REST error envelope in `apierr` and its
  tests before handlers depend on the Phase 0 type.
- **2.5f**: correct the filesystem support declaration, the `SymlinkFollow`
  comment, the D11 gate description and the CI toolchain comment; add the
  durable private-file replacement boundary Phase 3 needs for key rotation.
- **2.5g**: run the full gate and review every Phase 0 to 2 public declaration
  against the corrected proposals.

2.5a, 2.5b, 2.5d, 2.5e and 2.5f are independent. 2.5c may reuse the cache
migration test fixtures from 2.5a but does not otherwise depend on it.

## Required corrections

### Cache identity uniqueness

The Phase 2 fix changed `schemaV1` in place. That creates the right indexes for
a new database, but an existing version 1 cache has already skipped that SQL
and remains on the old shape. Do not edit migration 1 again. Migration
positions are durable versions even before cutover, and changing one teaches
future maintainers that an applied migration is mutable.

Restore `schemaV1` to the SQL that originally shipped, including its single
`node_ident` index, then add the corrected shape as migration 2. A fresh file
runs both migrations before `Open` returns; an old file runs only the discard.

Add migration 2 as a rebuildable discard. It drops and recreates `node`,
`diretag`, `share_gen` and their indexes with these two identity constraints:

```sql
CREATE UNIQUE INDEX node_ident_with_btime
  ON node(share, dev, ino, btime_ns) WHERE btime_ns IS NOT NULL;
CREATE UNIQUE INDEX node_ident_without_btime
  ON node(share, dev, ino) WHERE btime_ns IS NULL;
```

The second index is not redundant. SQLite treats two `NULL` values as distinct
inside an ordinary unique index, so migration 1 admits duplicate identities on
every filesystem that reports no birth time. The cache is rebuildable, so
discarding version 1 rows is safer than trying to choose one duplicate as the
authority.

Tests must prove that an existing version 1 cache is rebuilt at version 2, that
its old rows are gone, and that inserting the same no-btime identity twice is
refused by the database rather than only by an application lookup.

### Share-link target representation

The Phase 2 state schema made every share-link identity column nullable but did
not constrain their combinations. The importer then translated a Rust
`fileid = NULL`, a missing metadata database and a missing node row to the same
fabricated `(dev, ino, btime_present, btime_ns) = (0, 0, 0, 0)` tuple. That
changes the existing contract. A Rust link stores a path and an optional file
id, and checks both when the id is present. It does not follow a rename.

Do not edit state migration 1. Add durable state migration 2. Rebuild
`share_link` inside one state transaction, preserving all columns and indexes,
add `token_key_ver`, and use `CHECK`s that permit exactly these
representations:

1. all four identity columns are `NULL`, meaning path-only;
2. all four are non-`NULL`, `btime_present` is 1, and `dev` plus `ino` are not
   both zero.

This stricter rule is specific to public share links. Other legacy durable
metadata may still represent absent birth time, but a link must distinguish
the original inode from reuse after deletion to uphold its replacement
contract.

`token_enc` and `token_key_ver` must likewise be both `NULL` or both present,
with a non-negative version. Version 0 is the explicit legacy state for a Rust
ciphertext whose AAD had no version; use it for imported and already imported
rows. A link with no recoverable ciphertext keeps both fields `NULL`. Phase 3
must eliminate version 0 before Phase 4 starts.

Refuse the all-zero tuple emitted by the current Phase 2 importer. The broken
importer used that one value both for a legitimate Rust `fileid = NULL` and for
a non-`NULL` id it could not resolve, so migration cannot safely infer
path-only. Name the affected link and instruct the operator to retain or move
aside the generated `state.db`, then rerun the corrected importer from the
untouched Rust sources. Refuse every other partial or malformed tuple as
durable-state corruption too; do not drop or weaken a link silently.

Correct `copyShareLinks` separately. Rust `fileid = NULL` maps to four SQL
`NULL`s. A non-`NULL` file id must resolve through the Rust metadata row and
maps to a real tuple. If its metadata database or node row is missing, fail the
import with the link id and file id named. Also fail when that node has no birth
time. Silently weakening such a link to path-only or `(dev, ino)` can make a
replacement at the same path publicly accessible.

Tests must cover a path-only root link, a path-plus-identity link, refusal of
the current ambiguous all-zero tuple, refusal of a partial tuple, and refusal to import
a non-`NULL` Rust file id whose node cannot be resolved or has no birth time.
They also cover the `token_enc` and `token_key_ver` pairing. Core Phase 4 tests must later prove
that either a rename or a replacement at the stored path makes an
identity-bearing link gone.

### Complete and truthful Rust import

The current importer declares WebDAV locks "not imported by design" even
though proposal 10 makes them durable specifically so a restart cannot erase
an active exclusion. A cutover is not permission to erase it. Open
`dav-locks.db` query-only and copy every unexpired `dav_lock`, translating its
non-null `fileid` through `meta.db` to the destination identity tuple. Parse the
Rust `expires_ns` text as a checked integer and narrow it explicitly to the Go
schema's range. Drop an expired lock with reason `expired`. Refuse an active
lock with an unknown principal, missing metadata source or unresolved node.

Replace `Report.Dropped map[string]int` with a representation that includes a
stable reason per table. At minimum distinguish unknown user, unknown group,
missing node, expired row and corrupt interval encoding. The current writer
prints "unknown account" for every drop even when the code discarded a missing
node or corrupt interval. The report is an operator-facing data-loss boundary
and must not guess why a row disappeared.

Tests import an active lock, omit an expired lock with the correct reason,
refuse an unresolved active lock, and assert every existing drop path prints
its real reason. Remove the claim that clients simply retake all locks.

Add a checked disposition inventory for every table in `auth.db`, `acl.db`,
`links.db`, `upload.db`, `settings.db`, `meta.db`, `dav-locks.db`,
`compat-nc.db`, `shares.db`, `jobs.db`, `index.db` and `journal.db`. Mark
later-owned durable mappings with their phase. If such a table currently holds
rows, this Phase 2.5 binary refuses migration and names the required later
phase. In particular, never report success while dropping `user_smb_secret`,
an active upload alias, the compat instance identity, an admin-created share,
a share override, a job record or the persisted name-index switch. Validate
the Rust `journal.db` schema and report that its `write_event` rows are retained
in place. Unknown source tables also refuse migration so schema drift cannot
become silent data loss.

Make the Go journal schema use the Rust stored column and index names: `user`,
not `account`, and `write_event_by_user`. The public Go field may still be named
`Account`. A Rust journal predates `schema_version`, so the journal opener must
first inspect `sqlite_schema`. If the exact legacy table, unique constraint and
index are present, add only the migration metadata and mark version 1 applied.
Do not execute migration 1's table creation against an existing table. Refuse
to adopt a partial or unexpected shape. Tests open a populated Rust fixture,
retain every row, record and trim through the Go API, close, reopen and read it
again. An incompatible journal follows the documented warning-and-disabled
policy without changing the file.

Make the offline-source precondition enforceable in command help and tests. The
Rust and Go servers hold an exclusive advisory lock on
`<data-dir>/.stowcloud-instance.lock` for their lifetime. Migration acquires the
same lock without waiting and holds it through publication. The lock file is
not a PID file and stale contents never imply a running process. The Rust
process must be stopped because its state spans separate WAL databases and
SQLite offers no atomic snapshot across them. A SQLite `busy` check alone is
insufficient because a live server may be idle. Test refusal while the Rust
server holds the lock and success after it exits. The atomic staged destination
does not solve source consistency.

The source manifest also covers the set of Stowcloud-owned SQLite files, not
only the tables in filenames this importer already knows. Exclude SQLite WAL
and shared-memory sidecars. Recognized unpublished `state.db` staging names are
reported and ignored as control artifacts, consistently with the no-delete
rule below. An unknown application database blocks migration with its filename.

### Collision authority

The current code records only the newcomer after a collision and considers a
candidate occupied only when `node` currently holds it. That is not durable
authority. After deleting `cache.db`, an override owner may not have appeared
in the walk yet, so another identity can take its reserved id.

Change the override boundary so it supports both lookups:

```go
LookupFileID(ctx context.Context, ident Ident) (FileID, bool, error)
LookupFileIDOwner(ctx context.Context, id FileID) (Ident, bool, error)
```

On the first collision, write the existing holder's current id and the
newcomer's alternate id in one `state.db` transaction. Existing matching rows
are idempotent; any disagreement is corruption and a refusal. Commit that state
transaction before the cache transaction inserts the newcomer. Candidate
selection rejects an id held by either a different node or a different override
identity.

The forced-width test needs at least three identities. It must cover:

1. initial allocation in one order;
2. deleting the cache and rebuilding in every permutation;
3. an override owner appearing last;
4. a new colliding identity arriving before an old owner;
5. every old identity retaining its id and every id remaining unique.

### Atomic Rust import

The Phase 2 fix now builds beside `state.db` and publishes with a Linux
no-clobber rename plus a parent-directory sync. Keep that work. Its remaining
problem is the single fixed `state.db.importing` name: one invocation treats
another invocation's live staging database as stale and removes it. A crashed
run and a concurrent run are indistinguishable.

Create the database under a random reserved staging name in the same directory.
Copy every source inside the existing destination transaction, checkpoint and
close the staged database, then publish it atomically with no replacement and
fsync the parent directory. A destination that appeared meanwhile is
mapped from the filesystem's no-clobber error to `ErrStateExists`; it is never
overwritten. Remove only the staging files this invocation created, and surface
cleanup errors alongside the original failure.
Do not delete another staging name on startup: an unpublished file from a dead
process is inert, while a name owned by a concurrent process is live and cannot
be distinguished safely from its name alone. The instance lock prevents a new
concurrent owner but does not prove who created an older file, so it is not
permission to remove arbitrary staging names.

Use `vfs.PublishNew` for the no-clobber publication and parent sync. It is the
named D11 boundary for an already-complete non-share database. Do not route this
through `WriteDurable`, which owns staged share content, and do not add an ad
hoc rename outside `internal/vfs`.

Tests must inject a copy failure after at least one table, assert that
`state.db` does not exist, repair the source and succeed on the next invocation.
They must also assert that a pre-existing destination is byte-for-byte
unchanged, that an unrelated stale staging file is ignored, and that concurrent
importers never remove or write each other's staging files. Exactly one acquires
the instance lock; the other refuses with "data directory in use" before
creating a staging database. The winner still uses no-clobber publication as
defense against a destination created by a non-cooperating process.

### Native REST error compatibility

Phase 0 implemented `apierr.Error` as an unwrapped
`{code,msg,args,trace}` object. That is not the Rust or frontend contract and is
not one of the five authorized REST adaptations. Restore the exact native REST
shape before Phase 5 builds handlers on it:

```json
{"error":{"code":"fs.invalid_name","message":"invalid name","detail":{"reason_key":"share.name_empty","reason_params":{"field":"name"}}}}
```

Keep `MessageKey` and typed arguments internally if they remain useful, but the
responder serializes them as `detail.reason_key` and
`detail.reason_params`. `message` is a stable generic fallback and never
contains a lower-layer error string. The browser does not render it as
localized copy. An internal 500 omits `detail`. The request id travels only in
the `Sc-Trace` header, not in the JSON body.

Update the Phase 0 marshal tests to assert the outer `error` object, exact
field names, omission of absent detail, and absence of `trace`, `msg` and
`args`. Add a test proving an internal error cannot serialize arbitrary detail.

### Phase 0 and 1 declarations

Delete `FsType.ForcesPathIDs`. The Go store has no path identity variant, and
leaving this method in Phase 1 tells Phase 11 that a fallback exists when it
does not. Q5 records the compatibility decision and its reopen point.

Delete the unused `FsType.WatchUnreliable` declaration too. Every type it marks
is now rejected, so leaving it public falsely suggests a supported degraded
mode. Periodic rescan for admitted local filesystems remains a watch-capacity
fallback and does not need a filesystem-type predicate.

Replace the current overlay-only `FsType.Rejected` declaration with a
fail-closed support decision. Only ext4, btrfs, XFS, ZFS, f2fs and tmpfs are
admitted; tmpfs carries its existing data-loss warning. Overlayfs, FUSE, NFS,
CIFS or SMB, squashfs, NTFS and an unknown magic are refused. Prefer a
`Supported` predicate, or make `Rejected` return true by default, so a new enum
value cannot become supported by omission. Phase 11 owns registration UX, but
the Phase 1 type must not make a false support promise in the meantime. Phase
11 also requires birth-time capability on each admitted mount; the enum alone
cannot answer that per-filesystem-instance fact.

Change only the `SymlinkFollow` comment, not its flags or wire value. It follows
relative symlinks under `RESOLVE_BENEATH`; it does not follow an absolute target
or escape the share. `SymlinkWithinShare` is the mode that rebases an absolute
target through `RESOLVE_IN_ROOT`.

Change the D11 gate comment and diagnostic to describe what it enforces: raw
rename calls are confined to `internal/vfs`. `WriteDurable`,
`ShareRoot.Rename`, `PublishNew` and `ReplaceFileDurable` are the named
operations with distinct contracts. The gate does not prove that one helper
handles every mutation.

Add `ReplaceFileDurable` for a trusted private control file. It creates a
random same-directory staging file with `O_EXCL` and the requested private
mode, writes and syncs it, atomically replaces the final name, and syncs the
parent directory. It never accepts a share path or user input. Tests cover a
failed writer leaving the old file unchanged, exact `0600` mode despite umask,
successful replacement, and no staging residue on Linux. A non-Linux file
exists only so ordinary control-file callers and tests compile on the
development host; it is not a shipping durability claim. Phase 3 uses the
Linux implementation for the recoverable master-key rotation protocol.

Correct the CI comment that says `GO_VERSION` is the minimum in `go.mod`. The
module directive is the dependency floor, currently Go 1.25, while CI may pin a
newer current stable release. They do not have to move together.

Correct the Phase 2 store comments that call `state.db` the entire backup
instruction or say a share link follows a rename. `state.db` is the durable
data backup; the master key has a separate protected backup lifecycle. A share
link keeps a path plus optional identity and dies on a rename or replacement.
The comments in `store/open.go`, `store/state` and `store/fromrust` must match
the schema and importer tests after this phase.

## Traps

- **Do not repair duplicate no-btime rows in place.** There is no principled row
  to keep, and the cache is explicitly disposable.
- **Do not reserve only the alternate id.** The original holder's base id is
  also an insertion-order decision once a collision exists.
- **Do not check override reservations after inserting the node.** The state
  decision commits first because the two WAL databases have no atomic joint
  commit.
- **Do not publish the import by replacing a path.** `state.db` may belong to a
  concurrent or earlier successful run.
- **Do not silently unlink a failed final destination.** Only a validated
  staging name created by this invocation is cleanup material.
- **Do not implement path ids in this phase.** That requires a second durable
  target representation and a decision that Q5 deliberately leaves for Phase
  11 evidence.
- **Do not weaken an unresolved identity-bearing share link to path-only.** A
  file later created at the same path would inherit public access.
- **Do not add a second REST error envelope.** Phase 13 needs one frontend build
  to speak to both backends, and the existing shape already carries localized
  keys without exposing lower-layer prose.
- **Do not begin Phase 3.** The point of this boundary is to make the store a
  trustworthy base before credentials and grants depend on it.

## Done when

- The full gate is green, including the race step where a C compiler is
  reachable.
- A version 1 cache opens as an empty version 2 cache with both partial identity
  indexes.
- The database refuses duplicate no-btime identities.
- State migration 2 admits only path-only or coherent birth-time-bearing share
  links, refuses the old ambiguous zero identity, and the importer never
  fabricates one.
- The three-identity collision test passes across every rebuild permutation and
  later arrival, with all prior ids unchanged.
- A failed Rust import leaves no `state.db`, a retry succeeds, and an existing
  destination is never modified.
- Migration refuses while either server holds the instance lock, inventories
  every owned source database and table, preserves active WebDAV locks, reports
  each omission by its real reason, and adopts a populated Rust `journal.db`
  without losing a row.
- Native REST errors preserve `{error:{code,message,detail?}}`, with request
  correlation only in `Sc-Trace`.
- Filesystem support fails closed for every unsupported or unknown type, and
  the D11 gate describes its package boundary accurately.
- No Go symbol or comment claims that path ids or unconditional symlink
  following exist.
- `git diff --check` passes and the corrected proposals remain the contract for
  every changed declaration.
