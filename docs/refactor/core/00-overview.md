# Core rebuild: overview

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` is referenced as a behavioral specification only. The new
> implementation is written completely new; nothing is copied.

## What the core is

The core is the protocol-agnostic domain API every protocol sits on: the web
API, WebDAV, the Nextcloud compat surface, and the SMB publisher all call the
same operations and render the same shapes. The core must not know any
protocol exists. In the new tree this is enforced the same way the old one
enforces it: an import-graph gate refuses any core import of a protocol
package, and specifically refuses any import of go-fiber anywhere below the
protocol layer.

Three invariants define the core, and the rebuild keeps all three:

1. **The single resolve gate.** `Resolve` is the only place the existence rule
   and the permission check are applied. Every operation takes a `Resolved`,
   a type whose fields are unexported, so no caller can reach a mutation
   without having passed the gate. This invariant is what forces the domain
   operations to live in one Go package.
2. **Protocol-agnostic errors.** No core error chooses an HTTP status. The
   sentinel set (01-errors.md) is mapped to wire status exactly once, in the
   protocol layer.
3. **Filesystem access only through the VFS.** No raw syscall, no `os.Open`
   against a share path. The share root handle (`vfs.ShareRoot`, an openat2
   handle underneath) is the only door to disk, which is what makes the
   symlink and escape guarantees hold.

## What is wrong with the current composition

The current `go/internal/core` is one package of roughly 6,800 lines across
23 files, and the file boundaries no longer match the responsibility
boundaries. The problems this rebuild fixes:

- **Persistence SQL inside domain files.** `links_sql.go` holds every
  `share_link` statement, `quota.go` holds the ledger UPDATEs, and `homes.go`
  holds a raw grant INSERT. The domain package writes SQL against three
  different tables it does not own. Schema knowledge belongs to the store
  layer; the core should speak to it through narrow interfaces.
- **Scattered concepts.** The share registry is split across `root.go`
  (registration, broken shares, probing), `share_admin.go` (CRUD, reload) and
  `scan.go` (search sources), with the `shares` map and its mutex touched
  from all three. Transfers are split across `ops.go` (Move, copyRecursive)
  and `operation.go` (StartCopy, the runner); the conflict policy type sits
  in `ops.go` but is consumed mostly by `operation.go`.
- **Helpers far from their callers.** `mapVFSErr` lives in `entry.go` but is
  called from every file. `lastDot` lives in `links.go` but serves
  `uniqueSiblingName` in `ops.go`. `min64`/`max64`/`satAdd` live in
  `stream.go`. `pathExists` lives in `ops.go` and is called from four files.
- **One grab-bag root.** `root.go` mixes package construction, the share
  registry, the user's projected root listing, the logging helper, `errf`,
  and instance-id minting.

None of this is a correctness problem. It is a cohesion problem: a reader
looking for "everything about share links" opens three files, and a change to
the link schema edits a domain package.

## The new composition

The user's directive for this phase: re-organize the monolithic composition
and raise cohesion; keep things together by default, but separate the parts
whose responsibilities genuinely differ.

### One domain package, deliberately

The domain stays **one package**. This is not inertia; the `Resolved`
invariant requires it (unexported fields are the capability), and splitting
domain logic into sub-packages would force either exported constructors that
break the guarantee or interface indirection that serves no second
implementation. Cohesion inside the package is raised by re-drawing file
boundaries so each file is one closed concept.

New tree (module layout for the rebuilt engine; final module path decided in
the engine bootstrap document of the next phase):

```
engine/
  core/                 the domain package (this phase)
    core.go             Core struct, Options, New, attach seams, logging
    errors.go           sentinels, typed errors, the VFS error crossing
    ident.go            UserID, ShareID, Token, instance id
    entry.go            Entry, Page, Cursor, SortKey, ListOptions
    etag.go             FileETag, aggregate etag hashing
    registry.go         share registry: register, broken, probe, unregister,
                        Shares/Share/ShareRoot accessors, Roots projection
    shareadmin.go       CreateShare/UpdateShare/DeleteShare/RetryShare,
                        ReloadPersistedShares, rejection kinds, scan sources
    resolve.go          Resolved, Resolve, ResolveUnder, EntryAt, vpath
                        crossings, path helpers (pathExists, creatable leaf,
                        unique sibling name)
    list.go             List/ListSorted, sorting, cursor, buildEntry
    read.go             Stream, RandomRead, OpenStream, OpenRandom,
                        ArchiveWalk
    write.go            Mkdir, CreateFile, Rename, Delete, PublishPart,
                        preconditions, journal recording
    transfer.go         OnConflict, MoveOpts/MoveResult, Move, copyRecursive,
                        WouldCopy, RefuseSelfDescendant
    operation.go        Operation, StartCopy, cancel gate, list/get/cancel
    trash.go            trashMove, TrashList, TrashRestore, TrashPurge
    quota.go            FreeSpace, QuotaSink interface, chargeQuota
    aggregate.go        Aggregate, computeAggregate, markDirty, invalidation
    link.go             Link, LinkSpec/LinkPatch, create/list/get/update/
                        delete, liveness rules
    linkaccess.go       LinkPublic, LinkStream, LinkBrowse, LinkDrop,
                        password check
    home.go             EnableHomes, ensureHome, template seeding
    recent.go           Recent, RecentHit, journal-backed listing
```

### What is separated out

Three responsibilities leave the domain package, because they change for
reasons the domain does not:

1. **Link persistence.** The `share_link` SQL moves behind a `LinkStore`
   interface defined in `core/link.go` and implemented in the store layer
   (`engine/store/state`). The core hands it domain values (a token hash, a
   sealed token, a spec) and receives rows back; the schema, the statements
   and the scanning live with the schema's owner. Detail in 10-share-links.md.
2. **The quota ledger.** `QuotaSink` stays a core interface; the SQL
   implementation (`sqlQuota` over the user row) moves to the store layer.
   The core keeps only the interface, the attach seam and the charge helper.
   Detail in 09-quota-and-aggregates.md.
3. **Grant persistence.** The raw grant INSERT in `homes.go` moves into a
   grant aggregate in the state store, beside the other aggregates. The ACL
   package stays a pure evaluator; the core persists a grant through the
   store and then reloads the evaluator. It never spells the grant table's
   columns. Detail in 11-homes-and-recent.md and 01-package-survey.md.

Everything else stays in the package and moves to the file that owns its
concept. Shared helpers get one home each: the VFS error crossing in
`errors.go`, the path existence and naming helpers in `resolve.go`, the
numeric clamps beside their single caller or in the file that owns the
concept they clamp.

### What is deliberately not separated

- **The registry and the admin CRUD** stay in the same package and adjacent
  files, because both mutate the same `shares` map under the same mutex, and
  a split across packages would export that map's internals.
- **Trash** stays in the package: it is a delete policy, it constructs
  `Resolved` values internally, and its quota crediting is entangled with the
  delete path.
- **Aggregates** stay in the package: `markDirty` is called from every
  mutation, and the rollup constructs cache rows through the store's cache
  surface, which is already a separate package.
- **No operation interfaces.** The core is concrete. There is exactly one
  implementation of the domain; interfaces exist only at the seams where a
  second implementation really exists (store, clock, quota sink, link store,
  link cipher).

## Dependencies of the core

The core depends on, and only on:

| Dependency | Role |
| --- | --- |
| `engine/vfs` | share roots, safe paths, stats, durable writes |
| `engine/acl` | the permission evaluator (pure; no SQL) |
| `engine/store` | cache DB (aggregates, idents), state DB (ops, shares, links, quota, grants), journal DB |
| `engine/kit/clock` | injectable time for tests |
| `engine/kit/secret` | zeroizable secret container for link tokens |
| `engine/kit/task` | the one legal goroutine spawn, for long operations |
| `engine/kit/num`, `engine/kit/limits` | integer narrowing, bounds |
| stdlib + blake3 | hashing for etags and aggregates |

The current core also imports `internal/search` to build scan sources for
the index. That dependency inverts in the rebuild: the core exposes its own
scan-source shape (share id, root, base path, per-path allow closure), and
the search service adapts it into its walker's input. The rebuilt core
imports nothing from search (01-package-survey.md).

The `vfs`, `acl`, `store` and kit packages are re-specified before the core
is built; the pre-core work order and the package survey are in
`../01-package-survey.md`. Until those documents land, the existing behavior
under `go/internal/` is the contract the core writes against.

Nothing in the core imports fiber, `net/http`, or any protocol package. The
gate that checks the import graph is part of the phase 1 acceptance.

## Build order within phase 1

Each step compiles and passes its own tests before the next begins. The order
is dependency order inside the package:

1. `errors.go`, `ident.go`, `etag.go`, `entry.go` (01, 02): pure types and
   functions, no `Core` yet.
2. `core.go`, `registry.go` (03): construction and the share registry, over
   stub stores.
3. `resolve.go` (04): the gate. This is the security-critical step and gets
   the escape and existence-rule tests before anything consumes it.
4. `list.go`, `read.go` (05): the read side.
5. `write.go` (06): mutations, preconditions, journal.
6. `transfer.go`, `operation.go` (07): moves, copies, long operations.
7. `trash.go` (08).
8. `quota.go`, `aggregate.go` (09), including the store-side sink.
9. `link.go`, `linkaccess.go` (10), including the store-side `LinkStore`.
10. `home.go`, `recent.go` (11), including the ACL-side grant persistence.
11. `shareadmin.go` (03) last among the share pieces, because it needs the
    state store's share rows.

## Behavioral compatibility

The rebuild preserves observable behavior except where a document explicitly
lists a change under a "Deliberate changes" heading. The existing test files
under `go/internal/core/*_test.go` are treated as executable specification:
each rebuilt unit gets new tests covering at least the behaviors the old
tests assert, written fresh against the new API.

## Platform

Like the current core, the rebuilt core is Linux-only (`//go:build linux`)
except for the pure files (`errors.go`, `etag.go`, `ident.go`, `entry.go`),
because a share root is an openat2 handle underneath.
