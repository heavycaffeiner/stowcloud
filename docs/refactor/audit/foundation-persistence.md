# Foundation and persistence audit

This audit covers the foundation kit (`num`, `clock`, `task`, `secret`,
`limits`, `unixprobe`), the domain infrastructure packages (`vfs`, `jail`),
and the persistence layer (`store` and its subpackages `dbfile`, `cache`,
`journal`, `state`), plus `acl`, `search` (and `search/index`,
`search/service`), and `watch`. It verifies the claims made in
`docs/refactor/01-package-survey.md` for these packages and records new
findings the survey does not mention.

## num

No findings. `num.Narrow` is the one integer narrowing helper, the round
trip plus sign check is correct, and `RangeError` carries enough to act on.
No call site in the packages read for this audit reimplements a narrowing
check by hand instead of calling it (`watch/watch.go`'s `rmWatch` uses
`num.Narrow[uint32]` correctly, for example). Clean; keep as-is per the
survey.

## clock

No findings. `System` and `Fixed` are the only two implementations, `Nanos`
clamps a pre-epoch reading once per clock instance rather than aborting.
Clean; keep as-is per the survey.

## task

No findings. `task.Go` is the only goroutine spawn point found in the
packages read; a repo-wide grep for a bare `go func(` outside
`internal/task/task.go` and test files returned nothing. `Recover` is reused
correctly for the one goroutine `net/http` starts on its own. Clean; keep
as-is per the survey.

## secret

No findings. `Secret` cannot be printed, cannot be JSON-marshaled without
redaction, and `Reveal` is the single accessor. The package's own doc
comment is honest about what it cannot do (zero a GC-made copy). Clean;
keep as-is per the survey.

## limits

No defects found. The package is a flat list of named constants, no
imports, no logic beyond `Exceed`/`Exceeded`. The survey's characterization
("mixes bounds from every layer... but it is a leaf with no imports, one
registry of bounds is the point") is accurate. Grouping the sections by
layer, as the survey proposes, is a mechanical reorganization with no code
change; no additional findings to add.

## unixprobe

No findings. `Syscalls()` and `Landlock()` reference every wrapper the
design depends on as values, so a removed or renamed wrapper fails the
build. Nothing calls either function at runtime, which is the stated
design. Clean; keep as-is per the survey.

## vfs

### Findings

1. `publish_linux.go` `PublishNew` and `publish_other.go` `PublishNew`: both
   take a plain `string` path, never resolve through a `ShareRoot` or a
   `SafePath`, and open the target directory with `unix.Open`/`os.Stat`
   rather than the package's own `openat2` resolver. This confirms the
   survey's claim exactly: the function is file persistence for a one-shot
   control-file write, not filesystem security (misplacement, confirmed).

2. `PublishNew` has no call site anywhere in the tree outside `vfs` itself
   (repo-wide grep for `PublishNew` found only the definition and its own
   doc comment). The survey's claim that it "serves control files" for a
   caller could not be verified against any current caller. The rebuild
   document for `store/fsatomic` should either name the actual future
   caller or drop the function (misplacement, plus a stale claim in the
   survey's description of who uses it).

3. `replace_linux.go` `ReplaceFileDurable` and `replace_other.go` same name:
   same shape as `PublishNew` (plain path, no share root). This one has
   real call sites: `auth/masterkey.go`, `auth/passdb.go`,
   `search/index/index.go`, `smbagent/accounts.go`. All write
   server-owned files, never share content. This matches the survey's
   "sound; repoint at store/fsatomic" verdict for those call sites, and
   confirms the misplacement of the function's current home in `vfs`
   (misplacement, confirmed).

4. `seqpacket.go` (`SocketPair`, `SendMessage`, `RecvMessage`) and
   `file.go`'s `SendJob`/`OSFile`: this is SCM_RIGHTS descriptor-passing IPC
   for the preview worker. Nothing in it touches a share root, a safe path,
   admission, or a durable write. Its only callers are
   `preview/pool.go` and `preview/worker/worker.go`, both in the preview
   subsystem, not the share filesystem. The file's own justification
   ("a raw descriptor must not leave this package") is a rule about the
   `*os.File` type, not a reason the socket wiring belongs in a
   filesystem-security package (misplacement).

5. `caps.go` (`Probe`, `Caps`, `Support`) is a runtime syscall-support probe
   with one caller, `cmd/stowcloud/caps_linux.go`'s `printCaps`, an operator
   diagnostic command. It is read by `require.go`'s `RequireResolver` to
   decide whether to start, so it is cohesive with the package's admission
   role even though it does not itself touch a share root or a path (none,
   noted only as a boundary case for rebuild scoping).

6. `durable.go` `WriteDurable`/`PublishPart`: fsync content, rename within
   the same directory, fsync the directory after rename. The cleanup path
   that unlinks an orphaned staging file on a failed write only logs a
   warning on unlink failure rather than returning an error, but the
   original write error is still returned unchanged; this is a deliberate,
   correctly reasoned design (an unlistable orphan is picked up by a
   sweep), not a swallowed error on the write path itself. The survey's
   "sound" verdict for the durable-write mechanism holds up (none).

7. `path.go` trust-boundary validation (`validateExisting`,
   `validateCreatable`, `splitValidated`, `ParseVpath`): rejects absolute
   paths, `.`/`..`, NUL, oversized components, oversized full paths, and
   reserved prefixes before any component reaches the resolver. No defect
   found (none).

8. `path.go`'s `stagingName` (random staging suffix via `crypto/rand`) is
   one of three independently written "reserved-prefix temp name" schemes
   in the tree: `upload/model.go`'s `cacheStagingName` and
   `upload/cache.go`'s `.scpart-w"+id` construction are the other two. The
   shared parts (`IsReservedName`, the `.scpart-` prefix) are correctly
   centralized; only the actual name-generation logic is repeated per
   package (duplication, low severity).

9. `errno.go`'s `mapErrno` is the single canonical errno-to-sentinel mapper
   inside `vfs`. Two service packages, `core/entry.go` (`mapVFSErr`) and
   `upload/engine.go` (`mapVFSErr`), independently re-map `vfs.Err*`
   sentinels into their own vocabularies with different coverage. This is
   expected layering, not a `vfs` defect, but it means the `vfs` sentinel
   set is a contract two packages must stay in sync with by hand (none,
   noted as a downstream stability concern).

### Rebuild notes

- Specify that `ReplaceFileDurable` moves to `store/fsatomic`, and list its
  confirmed call sites (`auth/masterkey.go`, `auth/passdb.go`,
  `search/index/index.go`, `smbagent/accounts.go`) so the fsatomic
  document is written against real callers.
- Specify that `PublishNew` moves to `store/fsatomic` too, but state
  explicitly which caller it is meant to serve, since none exists in the
  current tree; drop the function if no caller is planned.
- Decide explicitly where `seqpacket.go` and `file.go`'s
  `SendJob`/`OSFile` land: keep in `vfs` as a documented exception, or move
  to a small preview-owned or foundation-kit descriptor-passing package.
  State the reason in the rebuild document either way.
- Carry forward the admission (`admit.go`), safe-path (`path.go`), resolve
  (`open.go`, `norm.go`), durable-write and publish-part (`durable.go`),
  and creation-table (`path.go`) contracts as-is; nothing found here
  weakens them.
- Record the `.scpart-` staging-name convention once, in the rebuild
  document, as a naming convention shared by reference across `vfs`,
  `upload`, and `smb`, even though each package still needs its own name
  generator for its own id scheme.
- Keep the `vfs` error sentinel set (`errno.go`) small and stable, since
  `core` and `upload` both hand-maintain a downstream mapping against it.

## store

### Findings

1. `errors.go` re-exports `dbfile.Err*` under `store.Err*`. No issue
   (none).

2. `instancelock.go`, `instancelock_unix.go`, `instancelock_windows.go`:
   advisory whole-file lock (flock/LockFileEx), taken non-blocking,
   released only by close, the lock file itself left on disk on purpose.
   Matches its own doc comment about not being a PID file (none).

3. `open.go` `Open`: opens `state.db` before `cache.db`, because the cache
   consults the override table on every id it allocates. This is the
   runtime evidence that the `state`-`cache` coupling described below is
   real, not incidental (misplacement, tracked under `store/state`).

4. `open.go` `Open`: a `journal.db` that fails to open is only
   `slog.Warn`'d, not surfaced to the caller, and every `journal.DB` method
   is documented nil-receiver-safe. This is a deliberate degrade-not-fail
   path; the rebuild document must state explicitly that a nil journal is
   valid `Store` state (none, but must be specified).

5. `sizeguard.go` `RunGuard`/`Sample`: samples `unix.Statfs` and the three
   database file sizes, sets `blocked` on every `dbfile.DB`. Linux-only
   (`//go:build linux`); no equivalent exists for other OSes. Worth a
   rebuild-time decision on whether that is intended (none, a scope
   question rather than a defect given the build tag).

6. Survey states `store` is 455 lines. Confirmed exact: the sum of
   `errors.go`, `instancelock*.go`, `open.go`, `sizeguard.go` is 455.

### Rebuild notes

- Document the instance lock's actual guarantee: protection against a
  second concurrently running server, not against corruption, and
  deliberately not waited on.
- Document `Open`'s state-before-cache ordering as load-bearing, not
  incidental, until the `Ident` move removes the dependency that forces
  it.
- Document the nil-`Journal` degrade path and the nil-receiver-safety
  requirement on every `journal.DB` method.
- Specify the size guard's write-path coverage precisely (see
  `store/state` finding 4 for the current gaps).

## store/dbfile

### Findings

1. `dbfile.go`: single write mutex per file, pragmas applied in a specific
   order (`busy_timeout` before `journal_mode`), bootstrap pragmas applied
   once on a bare connection before any table exists. Correct and
   documented (none).

2. `dbfile.go` `Write`: transactions use `_txlock=immediate`, so a
   transaction takes the write lock up front; rollback after a successful
   commit is correctly treated as non-error via
   `errors.Is(rerr, sql.ErrTxDone)`. No leak, no swallowed error (none).

3. `migrate.go`: one transaction per migration step, `Discard` steps
   refused unless `spec.Rebuildable`, `Precondition` runs inside the same
   transaction as the step's SQL. Sound (none).

4. `sql.go`: every statement is a `const`, nothing assembled from parts
   (none).

5. Survey states `store/dbfile` is 409 lines. That number matches
   `dbfile.go` (313) plus `migrate.go` (96) but omits `sql.go` (36). Actual
   non-test total is 445 lines, not 409. Minor undercount in the survey.

### Rebuild notes

- State the pragma ordering (busy_timeout before journal_mode) as a hard
  requirement, with the failure mode it prevents (a "database is locked"
  error on first open under concurrent connections).
- Document the two-phase open (bare-connection bootstrap pragmas, then the
  pooled connector) explicitly; it is not visible from the exported API
  alone.
- Correct the line count when restating package sizes.

## store/cache

### Findings

1. `cache/id.go`'s `Ident`, `IdentOf`, `Equal`: the canonical identity
   tuple (`Share vfs.ShareID`, `Dev`, `Ino uint64`, `Btime *int64`), with a
   documented reason `Btime` is a pointer. This is the type the survey says
   should move to a neutral home (misplacement, confirmed; see
   `store/state` findings for the extent of the duplication this causes).

2. `cache/id.go`'s `Overrides` interface (`LookupFileID`,
   `LookupFileIDOwner`, `RecordFileIDs`): `cache` depends on this abstract
   interface rather than importing `store/state` directly, with the
   comment explaining why. This half of the relationship is already
   correct; only `state` importing `cache` is the actual violation, and it
   is confined to one file (`override.go`) (none, this direction is
   clean).

3. `cache/id.go`'s `derive`/`DeriveID`/`AllocateID`: collision handling
   records the base holder alongside the newcomer, consults the override
   before deriving, and bounds retries with `maxAttempts` against an
   infinite loop. No defect (none).

4. `cache/node.go`'s `toSQL`/`fromSQL` (int64/uint64 bit-pattern
   reinterpretation) and `identFromSQL` are reimplemented, each with its
   own repeated `//nolint:gosec` comment, in `store/state/dav.go`
   (`identToSQL`/`identFromSQL`) and `store/state/override.go`
   (`toSQL`/`fromSQL`). Three separate spellings of "SQLite has one signed
   integer type, reinterpret the bit pattern" (duplication).

5. `cache/resolve.go` `Resolve`: bounds the parent-chain walk with
   `maxResolveHops = 8192` against a cyclic chain, producing an explicit
   error rather than looping forever. Sound defensive coding at a boundary
   where stored data could be corrupt (none).

6. `cache/stmt.go`: statements are prepared once in `prepare`, closed
   explicitly in `close`. `cache.DB` has no `Close` method of its own; once
   `New` succeeds, prepared statements are invalidated when the parent
   `dbfile.DB` pool closes, which Go's `database/sql` handles without
   leaking descriptors. Confirmed no leak, but the rebuild document should
   say this explicitly since there is no `cache.DB.Close` to point at
   (none, but currently undocumented).

7. Survey states `store/cache` is roughly 900 lines. Actual total across
   all seven files is 950. Close enough to the stated approximation.

8. The package holds both identity/node-id derivation (`id.go`,
   `node.go`, `resolve.go`) and directory-etag caching (`diretag.go`). Both
   share the "rebuildable, may answer I do not know" contract the package
   doc states, so the name still fits; the rebuild document should still
   treat these as two sections rather than one flat API (none, a
   documentation-organization note only).

### Rebuild notes

- Specify the `Ident` type once, in the neutral home the survey proposes
  (`dbfile` or a new `store/ident`), and require `state/dav.go` and
  `state/favorite.go` to use that one type and its
  `toSQL`/`fromSQL`/`btimeColumns` helpers instead of re-deriving them (see
  `store/state` finding 2 below for the full extent).
- Document the `Overrides` interface boundary as the shape that keeps
  `cache` from importing `state`; preserve this direction.
- Split the rebuild document into an id-derivation section and a
  directory-etag section, since they have different call patterns and
  different guard semantics (etag writes are explicitly ungated).
- Preserve the `maxResolveHops`/`maxAttempts` bounds as explicit
  requirements, not incidental implementation detail.

## store/journal

### Findings

1. `journal.go`: package doc states three properties (not an audit log,
   capped by row count not age, not an activity stream), and the code
   matches: `Record` upserts and trims in one transaction, so the cap
   survives an immediate crash (none).

2. `journal.go`'s `DB` methods (`Enabled`, `Record`, `RecentSince`) are all
   nil-receiver-safe, matching the "a journal that could not open is a
   disabled feature, not a crash" contract (none).

3. `RecentSince` closes `rows` via a deferred `errors.Join` and checks
   `rows.Err()` after the loop. No leak (none).

4. `sql.go`: every statement is a constant, parameterized. `sqlTrimAccount`
   uses `LIMIT -1 OFFSET ?`, a documented SQLite idiom, not a dynamically
   built limit (none).

5. Survey states `store/journal` is 197 lines, matching `journal.go` alone
   but omitting `sql.go` (54 lines). Actual total is 251, not 197. Same
   class of undercount as `store/dbfile`.

### Rebuild notes

- This package is clean. Carry over the three stated properties (not an
  audit log, count-capped not age-capped, no activity stream) as hard
  requirements; each addresses a specific failure mode (RTC clock jump,
  unbounded disk growth).
- Correct the line count when restating package sizes.

## store/state

### Findings

1. `override.go` imports `store/cache` for `cache.Ident`, `cache.FileID`,
   `cache.Assignment`, used throughout the file. This is exactly the
   violation the survey names, confined to this one file (misplacement,
   confirmed).

2. `dav.go` defines a second, separately named `Ident` type (`state.Ident`,
   same four fields but `Share` typed `int64` instead of `vfs.ShareID`)
   with its own `identToSQL`/`identFromSQL`/`parts()` helpers, structurally
   identical to `cache.Ident`. `favorite.go`'s `Favorite` struct inlines
   the same four fields a third time with its own `parts()` method. The
   identity tuple concept exists three times in this package (via the
   `cache` import, via `state.Ident`, and via `Favorite`'s inline fields),
   each with its own present/absent-birth-time encoding. Moving `Ident` to
   a neutral package fixes only the first of these three copies unless
   `dav.go` and `favorite.go` are also rewritten (duplication, more
   extensive than the survey documents).

3. `sharelink.go` (67 lines) contains only `ErrLinkTargetMalformed` and a
   migration precondition (`checkShareLinkTargets`). No CRUD or read
   functions for share links exist in `store/state`. The actual share-link
   store (create, list, get by id, get by hash, delete, note-download,
   key-version lookup) lives in `core/links.go` and `core/links_sql.go`, a
   service-layer package, running raw `*sql.Tx`/`*sql.DB` calls against
   `state.db` directly. This is the same class of violation the survey
   documents for `acl` (evaluation mixed with SQL), but for share links,
   and it is not named in the survey's "Cross-layer violations found"
   list. The survey's persistence table does say the rebuild's `LinkStore`
   lands in `store/state`, which is consistent with this finding, but
   understates the scope: this is an extraction of an existing surface out
   of `core`, not an addition of a missing one (misplacement, larger in
   scope than the survey's violation list suggests).

4. Inconsistent size-guard coverage: `shares.go`'s `InsertShare`,
   `operation.go`'s `CreateOp`, `loginflow.go`'s `PutLoginFlow`, and
   `override.go`'s `RecordFileIDs` all insert new rows without calling
   `EnsureWritable`. By contrast, `upload.go`'s `CreateUploadSession`,
   `dav.go`'s `PutDavLock`/`SetDavProps`, `favorite.go`'s `SetFavorite`,
   and `configsecret.go`'s `WriteConfigSecret` all check it first. When the
   size guard trips, new shares, operations, and login flows can still be
   created while uploads, DAV locks, favorites, and config secrets are
   correctly refused. The guard is off by default today, which limits the
   practical impact, but the inconsistency is real (defect, moderate
   severity given the guard's current off-by-default status).

5. `settings.go` `MergeSettings`: read-merge-write inside one transaction,
   with a comment documenting a concrete regression this fixed (one
   administrator's save dropping another section's data). No defect
   (none).

6. `loginflow.go`'s `ApproveLoginFlow`/`TouchLoginFlowPoll`: the race
   between two concurrent approvals or polls is closed by putting the
   condition inside the `UPDATE ... WHERE` clause itself, not by a
   read-then-write pair. Correct handling of a real race (none).

7. `sql.go` (659 lines) holds every migration for the whole package, which
   is reasonable as one schema-history file, but it also holds the active
   statement constants for `override.go` and `settings.go` inline, while
   every other aggregate (`dav`, `favorite`, `loginflow`, `operation`,
   `shares`, `upload`) has its own `_sql.go` file. `sql.go` is
   simultaneously the schema history and the SQL for two aggregates that
   never got split out (naming/cohesion, minor).

8. `shares.go`'s `DeleteShare` comment states "grants on it are deleted by
   the caller, which owns the cascade policy." This is an unenforced
   cross-package contract: the `grant` table's `share` column has no
   foreign key to `share_definition` in `sql.go`. A caller that forgets to
   delete grants leaves orphaned rows referencing a deleted share id. Not
   exploitable as a security issue today since grants are evaluated
   against live share ids, but it is a latent data-integrity gap (defect,
   low to moderate severity).

9. `upload.go`/`upload_sql.go` (the largest aggregate) gets the size-guard
   discipline right consistently: every insert-new-row method calls
   `EnsureWritable`, and update/delete methods correctly do not. This is
   the positive counter-example to finding 4 (none).

10. `dav.go`/`dav_sql.go`: all SQL is parameterized, dead properties and
    locks are keyed by the identity tuple rather than a cache-minted id, per
    the documented reasoning (a property keyed by fileid would move when
    the cache rebuilds). No SQL injection risk found anywhere in this
    package; every statement in every `_sql.go` file is a constant with `?`
    placeholders. No dynamically assembled SQL string was found in
    `store/state`, `store/cache`, `store/journal`, `store/dbfile`, or
    `acl` (none, confirmed clean on this axis).

11. Survey states `store/state` is roughly 2,600 lines. Actual sum of all
    19 non-test files is 3,062 lines, about 18 percent larger than stated.
    The gap grows further once `core/links.go` (roughly 500 lines of SQL
    that belongs here per finding 3) is folded in. Meaningful undercount.

12. The survey's "grab-bag" list (shares, uploads, operations, settings,
    overrides, share links, favorites, login flows, DAV locks, config
    secrets, active work) is accurate as a list of concerns present in
    `state.db`'s schema, and mostly accurate as a list of file pairs,
    except that "share links" is not actually implemented here (finding 3)
    and "overrides"/"settings" share `sql.go` rather than having their own
    file pair (finding 7). The claim that each aggregate already has its
    own file pair holds for 6 of 11 aggregates and only partially for the
    other 3.

### Rebuild notes

- Move `Ident` and its birth-time encoding to the neutral home the survey
  proposes, and rewrite `dav.go`'s `Ident` and `favorite.go`'s inline
  fields to use that one type, not just `override.go`'s import. Fixing
  only the import leaves two of three duplicated identity representations
  in place.
- Treat the share-link aggregate as a full extraction from `core/links.go`
  and `core/links_sql.go`, not as adding a missing surface: the `LinkStore`
  document must specify moving the entire existing CRUD, key-version
  lookup, and download-counter logic into `store/state`, leaving `core`
  with only orchestration, the same shape the `acl` split leaves the
  evaluator.
- Specify one explicit rule for which mutating methods must call the
  size-guard check (every insert that can grow the database), and audit
  `InsertShare`, `CreateOp`, `PutLoginFlow`, and `RecordFileIDs` against
  it rather than leaving coverage to be discovered per aggregate.
- Decide and document the grant-to-share cascade: either a real foreign
  key with `ON DELETE CASCADE`/`RESTRICT`, or a documented two-step delete
  enforced inside the store's own `DeleteShare` rather than left to the
  caller.
- Give every aggregate a consistent `logic.go` + `logic_sql.go` file pair;
  give `override` and `settings` their own `_sql.go` files, so `sql.go` is
  only ever the schema and migration history.
- Correct the package's stated size in any restated survey text; it is
  materially larger than quoted, and larger still once the link store
  lands here.
- Preserve the login-flow and settings race-closing pattern (condition
  inside the `UPDATE`, not read-then-write) as required behavior.

## acl

### Findings

1. `store.go`, `sql.go`, `grant_storage.go` (the write half, the SQL
   constants, and the read half of the grant table) hold `database/sql`
   code against `state.db` inside a package the survey and the file
   comments both describe as meant to be "pure and dependency-free". This
   matches the survey's stated split exactly: `eval.go`, `grant.go`,
   `perms.go`, `cache.go` are pure; `store.go`, `sql.go`,
   `grant_storage.go` are the SQL half that the survey says moves to a
   grant aggregate in `store/state` (misplacement, confirmed as the
   survey describes it).

2. `sql.go`: every statement is a `const`, nothing built from parts,
   consistent with the project's D14 rule. No SQL injection risk in this
   package (none).

3. `grant_storage.go`'s `readGrants`/`readMemberships` correctly close
   `rows` via a deferred `joinErr` and check `rows.Err()`. No leak
   (none).

4. `eval.go`'s `Evaluator`: grants and memberships are wholesale-replaced
   under a generation counter (`ReplaceGrants`, `SetMemberships`, `bump`),
   which invalidates every cached decision without a sweep. The
   `decisionCache` (`cache.go`) is bounded with FIFO eviction, correctly
   sized against an attacker-generated path set. No race found: every
   field access goes through `e.mu`, and the cache's own mutex is separate
   and consistently held. No defect (none).

5. `perms.go` and `grant.go` (`Perms`, `Path`, `Grant`) are small, pure
   value types with no I/O. No defect (none).

6. Survey states `acl` is roughly 1,080 lines. Actual non-test total
   across all seven files is 782 lines. This is a meaningful overcount in
   the survey (roughly 28 percent), though the split it describes (pure
   evaluator vs SQL) is otherwise accurate.

### Rebuild notes

- Split as the survey specifies: the evaluator (`eval.go`, `grant.go`,
  `perms.go`, `cache.go`) stays a pure, dependency-free package; the SQL
  (`store.go`, `sql.go`, `grant_storage.go`) moves to a grant aggregate in
  `store/state`, following the same file-pair convention as the other
  `store/state` aggregates.
- Preserve the generation-counter cache-invalidation design and the FIFO
  bound on `decisionCache` as explicit requirements; they are the reason
  a grant change never needs a cache sweep.
- Correct the line count when restating package sizes; the survey
  overstates it.

## search

### Findings

1. `search/walk.go` defines `Source`, and both `search/scan.go`'s
   `ScanCorpus` and `walk.go`'s `Walk` consume it. `core/scan.go` builds
   `[]search.Source` directly, confirming the survey's claim: `core`
   depends on `search`'s vocabulary rather than the reverse. `Source` has
   no persistence-specific or index-specific fields (a `*vfs.ShareRoot`, a
   `vfs.SafePath` base, an `Allow` closure), so the survey's proposed fix,
   core owns the scan-source shape and search adapts, is a small, local
   change (misplacement, confirms survey).

2. `walk.go`'s `Walk` (parallel, worker pool) and `scan.go`'s `ScanCorpus`
   (sequential) both implement a stack-based tree walk over
   `vfs.SafePath`/`vfs.ShareRoot`. This is stated as deliberate in the
   comments (different cost profiles: one measures, one answers), not an
   accidental duplication. A third, similar walk exists in
   `search/service/build.go`'s `Build`. Not a defect given the documented
   rationale, but worth naming explicitly as a triplication for the
   rebuild document (duplication, low severity, informational).

3. `varint.go` carries the package's top-level doc comment
   (`// Package search implements...`), even though it is the smallest and
   least central file (LEB128 varint encoding). A reader looking for what
   the package does is more likely to open `walk.go` or `scan.go` first
   (naming, minor).

4. `trigram.go`'s `sortTrigrams`/`dedupTrigrams` on `[]Trigram` are
   duplicated byte-for-byte in `search/index/base.go` as functions
   operating on `[]search.Trigram`, even though `search/index` already
   imports `search` for the `Trigram` type itself (duplication).

5. `hll.go`'s `NewHLL` clamps precision silently to `[4,18]` rather than
   refusing, justified in the comment as an internal tuning constant, not
   a request value. The only caller is an internal constant in `scan.go`,
   so the clamp is safe as currently used; it would need revalidation if
   ever exposed to a configurable or request-supplied value (none, given
   current call sites).

6. `hll.go`'s `Hash64` (FNV-1a plus splitmix64, hand-rolled and
   documented as auditable on purpose) and `search/index/seg.go`'s
   `FNV1a32` are two distinct hash primitives for two distinct purposes
   (a distinct-count sketch input vs a corruption checksum), not a
   duplication to merge; flagged only so the rebuild does not
   accidentally consolidate them into one helper with one collision
   tolerance (none, informational).

7. `rank.go`, `fold.go` are clean: pure functions, no I/O, no shared
   mutable state (none).

### Rebuild notes

- Specify the core-owned scan-source type (share id, root, base, per-path
  allow closure) in the core documents, and specify search's adapter
  function that converts it into `search.Source`, so `core` stops
  importing `search`.
- Specify `Trigram` sort and dedup as one function in one place, reused by
  `search/index` rather than reimplemented.
- Move the package doc comment to a file read first (`walk.go` or a new
  `doc.go`).
- Document the walk/scan/build triplication as deliberate (different cost
  profiles), so a future maintainer does not collapse it into one shared
  walker that loses the estimator's affordability property.
- Keep `Hash64` and `FNV1a32` as two distinct, separately named
  primitives; do not generalize into one hash utility.

## search/index

### Findings

1. The survey's claim, "ReplaceFileDurable for the snapshots, O_APPEND for
   segments," is verified correct. `index.go`'s `publish()` and
   `rewriteTombstones()` both call `vfs.ReplaceFileDurable` for `base.idx`
   and `tomb.idx`; `seg.go`'s `AppendRecord` opens with
   `O_CREATE|O_WRONLY|O_APPEND`, writes, and calls `f.Sync()` before
   close, for delta segments (none, confirms survey).

2. `seg.go`'s `AppendRecord` and `TruncateTo` both correctly join a write
   error with the close error via `errors.Join`, never silently dropping
   the close failure (none).

3. `index.go` has a hand-written `min(a, b int) int` near the bottom of
   the file. Go's builtin `min` (available since Go 1.21; the module
   targets a newer version) makes this a dead reimplementation of a
   language builtin. The same pattern independently exists in
   `core/entry.go` (duplication, low severity).

4. `base.go`'s `sortTrigrams`/`dedupTrigrams` duplicate
   `search.sortTrigrams`/`search.dedupTrigrams` exactly, even though this
   file's package already imports `search` for `Trigram`, `Varint`,
   `PutVarint`, and `Fold` (duplication; same finding as `search` finding
   4 above, from the other side).

5. `index.go`'s `Open()` reads `base.idx` via `os.ReadFile` under a
   `//nolint:gosec // G304` comment justifying it as a fixed name under an
   operator-configured directory, not request input. `dir` traces back to
   server configuration through `service.OpenIndex`. No trust-boundary
   violation found (none).

6. `base.go`'s `OpenBase`: every header offset is validated via
   `num.Narrow[int]` and an internal `fits()` bounds check before any read
   through it, which is the correct trust boundary for a file a crash or
   disk corruption could have rewritten (none).

7. `index.go`'s merge design (build outside the lock, swap under the
   lock) is documented with its invariants (snapshot is copy-only, appends
   after seal go to a fresh delta file, tombstone comparison is inclusive
   on `snap.seq`). No race found on inspection; the invariants as
   commented match the code (none).

8. `index.go` is a single 995-line file mixing the `NameIndex` type's
   query and append API, the merge algorithm, and small utilities
   (`intersect`, `matchesFolded`, `makeHit`, `containsString`, `min`,
   `deltaNames`). Nothing here is misplaced across a layer boundary; this
   is a cohesion size observation for a future split (naming/cohesion,
   low to medium severity).

9. `base.go`'s `WriteBase` materializes the whole postings table for a
   corpus in a Go map before emitting sorted output. This matches the
   documented cost profile (merge is the one heavy operation, gated, never
   on a request path), so it is an accepted tradeoff, not a defect (none).

10. `codec.go`'s `Decompress`/`DecompressHint` bound decompression output
    via `MaxDecompressed` (64 MiB) before or during decode, guarding
    against a corrupt or hostile length prefix inflating memory. Good
    trust-boundary handling for the corrupted-disk case (none).

### Rebuild notes

- Preserve the write-mechanism split (ReplaceFileDurable for base/tomb,
  append and fsync for delta) as the documented, correct design.
- Specify one shared sort and dedup helper for `Trigram`, used by both
  `search` and `search/index`, instead of two copies.
- Drop the hand-written `min` in favor of the language builtin.
- Split `index.go` along its three seams (query and write API, merge
  algorithm, small helpers) in the rebuild document for readability.
- Document the merge concurrency invariants (immutable base, append-only
  overlay, snapshot then build then swap, inclusive tombstone comparison)
  as an explicit invariant list.
- Keep `MaxDecompressed` and `MaxRecord` as explicit, named bounds in the
  format spec; they are the trust boundary against a corrupted segment.

## search/service

### Findings

1. `build.go`'s `Build()` walks every source with its own stack-based walk
   over `vfs.SafePath`, a third independent implementation alongside
   `search.ScanCorpus` and `search.Walk`. Each has a distinct purpose
   (measure, query, ingest), consistent with the project's own reasoning
   elsewhere for keeping distinct cost profiles separate, but worth naming
   as a triplication rather than a duplication (duplication, medium: three
   copies of "stack-walk a ShareRoot honoring HideReserved and Allow").

2. `service.go`'s `pathUnder()` revalidates a path stored in the index
   against the source's current `Base` (`JoinExisting` plus `Under`)
   before trusting it, and is reused correctly from `update.go`'s
   `reconcile()`. This is the correct pattern for the trust boundary that
   index-stored data is untrusted relative to today's filesystem state
   (none).

3. `service.go`'s `Query()` takes a non-blocking concurrency slot
   (`s.slots <- struct{}{}` with a `default: return ErrBusy` branch),
   correct backpressure with no blocking (none).

4. `update.go`'s `Updater.Offer()` silently drops an event on a full queue,
   logging a warning. This is documented as an accepted tradeoff (a
   dropped update costs a stale entry, compensated by walk fallback and
   stat revalidation elsewhere). Not a defect given the documented
   compensating chain, but that chain is load-bearing: removing either
   compensating mechanism would turn this into real data loss (none, but
   the compensating chain must stay explicit in the rebuild document).

5. `open.go`'s `OpenIndex()` degrades to nil (walk-only mode) both on
   `ErrIndexCorrupt`/`ErrCorrupt` and on any other `index.Open` error,
   with only the log message differing. This means a permission error, a
   disk-full error, or a transient I/O failure on open is treated
   identically to "the index format is corrupt", losing the distinction
   between "needs a rebuild" and "may recover on its own". Not a
   data-loss defect (search still works via walk), but it can mask an
   operational problem as if it required a rebuild (defect, low severity:
   error classification).

6. `service.go`, `update.go`, `build.go`, `open.go` show clean layering
   overall: tier selection, watcher-driven incremental sync, full ingest,
   and graceful degradation on corruption each get their own file, with no
   cohesion problems found.

### Rebuild notes

- Name explicitly, in the rebuild spec, that there are three independent
  walk implementations in the search family (`ScanCorpus`, `Walk`,
  `Build`), and decide whether to keep three or factor the common
  stack-walk skeleton (directory read, `HideReserved`, `Allow` check,
  child push) behind one internal helper.
- Preserve `pathUnder`'s revalidation as a named, documented invariant:
  any hit sourced from persisted index data must be revalidated against
  the current `ShareRoot` before being trusted.
- Decide in the rebuild whether `OpenIndex` needs to distinguish "corrupt
  format" from "transient I/O error" from "directory does not exist yet",
  rather than one degrade-to-nil path for everything.
- Keep the compensating chain (drop, walk fallback, stat revalidation)
  explicit as an invariant the persistence and service boundary depends
  on.

## jail

### Findings

1. `apply.go`'s `Apply`/`apply`: the `steps` struct (kernel-facing calls
   behind an unexported struct) is a clean seam for testing sequencing
   without a kernel that refuses. No defect (none).

2. `rlimit.go`'s `Limits`, `DefaultLimits()`, `ApplyLimits()` are exported
   but have no call sites anywhere in the tree (a repo-wide grep for
   `ApplyLimits`, `DefaultLimits`, and `jail.Limits`, including test
   files, found only the definitions). The preview worker relies entirely
   on `preview.DecodeLimits`, an in-process pixel-count limit; RLIMIT_AS
   is mentioned only in comments as an assumed backstop and is never set
   anywhere. This is dead exported API describing a security control that
   comments across `preview/pool.go`, `preview/decode.go`, and
   `jail/rlimit.go` itself claim is in effect but is not wired up
   (defect: comments describe a control that does not exist in the
   running system; also unused exported surface).

3. `reexec.go`'s `SealDescriptors()` is also exported with no call sites
   anywhere in the tree. Its doc comment says it matters as much as the
   syscall filters, because RLIMIT_NOFILE does not touch descriptors the
   worker inherits at fork (listening sockets, share roots, database
   handles). Since nothing calls it, the worker as actually started
   (`preview/pool.go`'s `cmd.ExtraFiles` plus `exec.Command(...).Start()`)
   never seals inherited descriptors beyond what `os/exec`'s own CLOEXEC
   defaults provide for descriptors not passed via `ExtraFiles`. This is
   an unverified gap between what the comments claim and what actually
   runs (defect, medium: dead security-relevant code with a comment
   claiming importance; needs explicit verification or removal).

4. `landlock.go`'s `restrict()` and `addPathBeneath()`: correctly checks
   `handled == 0` before proceeding, narrows `PR_SET_NO_NEW_PRIVS` before
   `restrictSelf`, opens each grant path `O_RDONLY|O_PATH|O_CLOEXEC` with a
   matching deferred close, and calls `runtime.KeepAlive` after each
   syscall referencing an unsafe pointer into a Go value. No resource leak
   found; careful, well-documented code (none).

5. `seccomp.go`'s `assembleFor` bounds-checks the refusal jump index
   against BPF's 8-bit jump offset limit before assembling, and
   `offsetTo` refuses backward jumps. Appropriate defensive validation for
   a self-generated, compile-time-sized program (none).

6. `seccomp.go`'s `allowedSyscalls()` documents, per entry, why each
   syscall is on the allow list (including three non-obvious ones such as
   `PR_SET_VMA_ANON_NAME`), based on measurement rather than guesswork.
   Worth preserving the methodology, not the list itself, in the rebuild
   (none, a positive practice to carry forward).

7. `policy.go`'s `ParsePolicy` is the trust boundary for the configured
   policy string, correctly rejecting anything outside
   `{"required","preferred","off"}` (none).

8. `jail`'s only internal import across every file read is `num`,
   confirmed by grep. Matches the survey's placement of `jail` as
   dependency-clean domain infrastructure (none, confirms survey).

9. File-by-file cohesion is otherwise clean: each file matches its name
   (`apply.go` sequencing, `landlock.go` Landlock, `policy.go` the policy
   enum and parsing, `reexec.go` the re-exec mechanism, `rlimit.go`
   rlimits, `seccomp.go` BPF assembly). The only cohesion issue is the
   unused exported surface in `reexec.go` and `rlimit.go` (naming, tied to
   findings 2 and 3).

### Rebuild notes

- Decide explicitly whether `Limits`/`DefaultLimits`/`ApplyLimits`
  (`rlimit.go`) is a real requirement (wire it into
  `preview/worker/worker.go`) or drop it; do not carry forward unused
  exported API that comments describe as load-bearing.
- Same decision for `SealDescriptors`: wire it into worker startup, or
  drop it, and document explicitly which inherited descriptors are
  actually a risk given `os/exec`'s own CLOEXEC defaults.
- Preserve the `allowedSyscalls()` methodology (measured, documented
  reasoning per entry) as a required practice for the rebuild's own
  allow-list.
- Preserve the `steps`-struct test seam in `apply.go`.
- Keep jail's dependency-clean status (only `num`) as an explicit
  constraint in the rebuild spec.

## watch

### Findings

1. `watch.go` carries `//go:build linux` at the top of the file; the rest
   of the package (`config.go`, `event.go`, `hotset.go`) has no build tag
   and compiles on any OS. `GOOS=darwin go vet ./internal/watch/...`
   succeeds cleanly with `watch.go` excluded from that build, and
   `GOOS=darwin go build ./internal/watch/...` also succeeds, confirming
   the package is buildable (with a stub-free reduced surface) outside
   Linux even though its core logic (`Watcher`, `Start`) is Linux-only via
   inotify. This matches the survey's "clean deps already" characterization
   for `watch`; no cross-OS defect found because nothing calls the
   Linux-only surface from a non-Linux build (none).

2. `watch.go`'s `Watcher.Close()` closes the inotify file descriptor and
   waits for all three loop goroutines (`read`, `flush`, `rescan`) via
   `sync.Once` and `wg.Wait()`, guaranteeing no leaked goroutine or
   descriptor on shutdown. No leak found (none).

3. `watch.go`'s `addWatch`/`rmWatch` correctly use `SyscallConn` rather
   than `Fd()`, explicitly to keep the descriptor in the runtime poller so
   `Close` can unblock the reader; the comment explaining why is accurate
   against the code (none).

4. `watch.go`'s `consume()` parses inotify records directly out of a byte
   buffer with manual offset arithmetic (`binary.NativeEndian.Uint32` at
   fixed offsets), and bounds every read against `len(buf)` before
   advancing (`size <= 0 || off+size > len(buf)` returns early). This is
   the trust boundary for a kernel-supplied buffer, and it fails closed
   rather than reading past the buffer. No defect (none).

5. `hotset.go`'s two-tier structure (a refcounted sticky set fed by
   external subscriptions, plus an LRU-evicted recent set) is a
   nontrivial piece of state; `evictFor` deliberately exceeds the cap
   rather than evicting a pinned (actively viewed) directory, which the
   comment states as an intentional tradeoff. No race found: every
   mutating method on `Watcher` and `hotSet` is called under `w.mu` (none).

6. `watch.go`'s `flushLoop`/`invalidateEverything`: a kernel queue
   overflow or a pending-set size above `FullThreshold` escalates to a
   whole-share invalidation rather than continuing to track individual
   directories, matching the package doc's own stated trade (a stale
   answer is always detectable and self-correcting, not immediate). No
   defect (none).

### Rebuild notes

- Confirm the Linux-only backend and the OS-independent config/event/
  hotset types as a deliberate split, and preserve it: only the transport
  (`watch.go`) needs a build tag, not the whole package.
- Preserve the fail-closed bounds check in `consume()` as a named
  trust-boundary requirement: the inotify buffer is kernel-supplied but
  still parsed defensively.
- Preserve the two-tier hot-set design (sticky plus LRU, sticky never
  auto-evicted) and the "exceed the cap rather than evict something
  actively viewed" tradeoff as explicit, named requirements, since the
  package doc identifies a previous silent failure mode this design
  fixes (an LRU-only implementation would silently starve exactly the
  directory a user is looking at).
- Preserve the whole-share escalation on kernel overflow or pending-set
  overrun as the documented, correct degrade path.

## Documents required

- `docs/refactor/foundation/kit.md`: `num`, `clock`, `task`, `secret`,
  `limits` as one grouping document (directory move only, no interface
  change), plus the layer-grouped reorganization of `limits`'s constant
  sections.
- `docs/refactor/foundation/vfs.md`: admission, safe paths, resolve
  mechanics, durable write and publish-part, the creation table, and the
  escape test matrix, written to the contract every core document already
  assumes; must state the decision on `seqpacket.go`/`file.go`'s
  descriptor-passing surface (keep as a documented exception or move out).
- `docs/refactor/foundation/fsatomic.md`: `ReplaceFileDurable` and
  `PublishNew` extracted from `vfs`, with every real call site listed
  (`auth/masterkey.go`, `auth/passdb.go`, `search/index/index.go`,
  `smbagent/accounts.go`), and an explicit decision on whether `PublishNew`
  has a caller worth keeping.
- `docs/refactor/foundation/dbfile.md`: pragma ordering, the two-phase
  open, and the migration transaction model.
- `docs/refactor/foundation/cache.md`: the id-derivation contract and the
  directory-etag contract as two sections, plus the `Ident` neutral-home
  decision shared with `store/state`.
- `docs/refactor/foundation/journal.md`: the three stated properties
  (not an audit log, count-capped, no activity stream) as hard
  requirements.
- `docs/refactor/foundation/state.md`: one aggregate at a time, each with
  its own file-pair convention including `override` and `settings`; the
  `Ident` unification across `dav.go` and `favorite.go`; the share-link
  extraction from `core/links.go`; the size-guard coverage rule; and the
  grant-to-share cascade decision.
- `docs/refactor/foundation/acl-evaluator.md`: the pure evaluator only
  (`eval.go`, `grant.go`, `perms.go`, `cache.go`), with the grant SQL
  moved into the `state.md` document's grant aggregate section.
- `docs/refactor/foundation/search-family.md`: the tiered walk and index
  architecture, the core scan-source inversion, and the decision on the
  walk and scan and build triplication.
- `docs/refactor/foundation/search-index-format.md`: the on-disk segment
  format, the durability mechanism per segment type, the merge invariants,
  and the trigram sort and dedup consolidation.
- `docs/refactor/foundation/search-service.md`: tier selection, the
  updater's compensating-mechanism chain, and the `OpenIndex` error
  classification decision.
- `docs/refactor/foundation/jail.md`: Landlock, seccomp, and rlimit
  sequencing, the syscall allow-list measurement methodology, and an
  explicit decision on `Limits`/`ApplyLimits`/`SealDescriptors`.
- `docs/refactor/foundation/watch.md`: the two-tier hot set, the
  fail-closed inotify buffer parsing, and the whole-share escalation
  behavior on overflow.
