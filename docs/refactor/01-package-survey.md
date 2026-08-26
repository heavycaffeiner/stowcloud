# Package survey and pre-core work order

> This document surveys every package under `go/internal` as it exists today,
> assigns each to a layer of the target architecture, and derives the order of
> work that must precede the core rebuild. Like every document in this tree:
> the existing code is a behavioral specification only, and the new
> implementation is written completely from scratch; nothing is copied.

## Target architecture

The rebuilt engine is a 3-layer design:

| Layer | Role | May import |
| --- | --- | --- |
| Presentation | fiber HTTP surface, WebDAV, compat, wire shapes, status mapping | service, foundation |
| Service | domain logic: core, ACL evaluation, auth, search, upload, preview | persistence, foundation |
| Persistence | the three SQLite databases and every SQL statement | foundation |

Plus a foundation tier that any layer may use: small, dependency-free kit
packages (numeric narrowing, clock, secrets, task spawn, bounds) and the two
domain infrastructures that sit below the service layer proper (`vfs`, the
filesystem door; `jail`, the sandbox).

Import direction is strictly downward. Sideways imports inside a layer are
allowed only downward-in-build-order and never cyclic. The import-graph gate
enforces this.

## Inventory

Sizes are non-test lines. "Layer" is the target assignment, not the current
location.

### Foundation kit

| Package | Lines | Verdict |
| --- | --- | --- |
| `num` | 53 | Keep as-is. The one legal integer narrowing. |
| `clock` | 61 | Keep as-is. Injectable time. |
| `task` | 40 | Keep as-is. The one legal goroutine spawn. |
| `secret` | 66 | Keep as-is. Zeroizable secret container. |
| `limits` | 287 | Keep as one constants package. It mixes bounds from every layer (HTTP body sizes beside path bounds beside DB row caps), but it is a leaf with no imports, and one registry of bounds is the point of it. Regroup its sections by layer for readability. |
| `unixprobe` | 65 | Keep as-is. Compile-time syscall probe, nothing imports it. |

New grouped home: `engine/kit/{num,clock,task,secret,limits}`. The grouping
is a directory move, not a merge; each stays its own package.

### Domain infrastructure (below service)

| Package | Lines | Verdict |
| --- | --- | --- |
| `vfs` | 3,226 | Rebuild before core. The security foundation: openat2 share roots, safe paths, admission, durable writes, publish. Its API is the contract every core document already writes against. Composition is cohesive except one intruder: `ReplaceFileDurable`/`PublishNew` take plain paths, never a share root, and serve control files; they move out to `store/fsatomic` (see Persistence). After the move, everything `vfs` exports goes through a `ShareRoot` or a path type. |
| `jail` | 1,034 | Not a core dependency. Rebuild in the preview phase. |

### Persistence

The persistence layer is not only SQLite. The engine also persists
application state as plain files, and the rebuild treats both as one layer:

- **Databases**: the three SQLite files under `store/`.
- **File persistence**: the atomic-replace primitive and every file the
  engine writes about itself (keys, rendered configs, index segments,
  cache entries, tokens, certificates).

What file persistence does **not** cover is the share trees. User files are
the domain itself, written only through `vfs.ShareRoot`, which enforces the
admission, symlink and escape rules; wrapping that behind a persistence
abstraction would scatter the security gate across a layer boundary. The
share tree is also an external world other programs write into, not a store
this engine owns. `vfs` therefore stays domain infrastructure.

| Package | Lines | Verdict |
| --- | --- | --- |
| `store/fsatomic` (new) | ~150 | Extract before core. `ReplaceFileDurable` and `PublishNew` currently live inside `vfs` (`replace_linux.go`, `publish_linux.go`) although they take plain paths and never touch a share root: control-file writing parked inside the filesystem-security package. They move to a persistence-layer primitive package every file-writing subsystem uses. |
| `store/dbfile` | 409 | Rebuild before core. SQLite open, migrate. |
| `store/cache` | ~900 | Rebuild before core. Idents, dir etags, resolve. |
| `store/journal` | 197 | Rebuild before core. Write journal. |
| `store/state` | ~2,600 | Rebuild before core, re-drawn per aggregate. Today it is a grab-bag: shares, uploads, operations, settings, overrides, share links, favorites, login flows, DAV locks, config secrets, active work. Each aggregate gets its own file pair (logic + SQL) as it mostly already has; the three new surfaces the core documents require land here: the `LinkStore` (10-share-links.md), the quota ledger (09-quota-and-aggregates.md), and the grant write surface (amended, see below). |
| `store` | 455 | Rebuild before core. Aggregator, size guard, instance lock. |

One smell inside the layer: `store/state` imports `store/cache` (for the
identity tuple type). The rebuild moves the shared `Ident` tuple to a neutral
spot (either `dbfile` or a tiny `store/ident` file) so the two databases do
not depend on each other.

`state.db` keeping protocol-flavored aggregates (DAV locks and dead
properties) is acceptable: persistence serves every layer above it, and the
alternative is a fourth database. The files are named by aggregate, which
already says what they are.

#### File persistence inventory

Every place the engine writes its own state as a file today, with its
current mechanism:

| What | Where today | Mechanism | Verdict |
| --- | --- | --- | --- |
| Master key ring | `auth/masterkey.go` | `vfs.ReplaceFileDurable` | Sound; repoint at `store/fsatomic` in the auth phase. |
| NT-hash passdb sidecar | `auth/passdb.go` | `vfs.ReplaceFileDurable` | Sound; same repoint. |
| SMB rendered configs | `smbpublish/publish.go`, `smbagent` | Mostly `ReplaceFileDurable`; `smbagent/sync_linux.go` writes `smb.conf` and its candidate with plain `os.WriteFile` | Fix in the SMB phase: every rendered file goes through the primitive. |
| Search index (base, tombstones, segments) | `search/index` | `ReplaceFileDurable` for the snapshots, `O_APPEND` for segments | Sound; the append log is its own format and stays with the index. |
| Preview cache entries | `preview/cache.go` | `ReplaceFileDurable` | Sound; same repoint. |
| Upload spool and parts | `upload` | VFS control names inside the destination share | Correct as-is: parts live in the share tree on purpose, so they publish by same-directory rename. Not file persistence. |
| Setup token | `server/setup.go` | Plain `os.WriteFile` | Non-durable; rebuild through the primitive. |
| TLS self-signed cert and key | `server/tls.go` | Plain `os.WriteFile` | Non-durable; rebuild through the primitive. Key material especially must never be a torn file. |
| Startup probe file | `server/probefile.go` | Hand-rolled tmp plus rename, no fsync | Replace with the primitive; a second hand-rolled replace is exactly what the primitive exists to prevent. |
| Homes host directory | `core/homes.go` | `os.MkdirAll` 0750 | Stays in the core: creating the homes root is a domain decision (11-homes-and-recent.md), and a directory create is idempotent and has no torn state. |

The subsystem-owned on-disk stores (index segments, preview cache, spool)
keep their formats and their owning packages; the layer assignment only
names their file halves as persistence and their primitive as shared.

### Service

| Package | Lines | Verdict |
| --- | --- | --- |
| `core` | 5,190 | Phase 1 target. Documents 00-11 written. |
| `acl` | ~1,080 | Split before core. Today one package holds the evaluator (`eval.go`, `grant.go`, `perms.go`, `cache.go`, pure and dependency-free) and the grant SQL (`store.go`, `sql.go`, `grant_storage.go`, `database/sql` against state.db). Under 3-layer the SQL moves to `store/state` as a grant aggregate; `acl` keeps evaluation only and loads from rows the store hands it. This amends 11-homes-and-recent.md, which had placed grant persistence inside the ACL package. |
| `search` | 1,272 | The walker and the index. Not rebuilt before core, but the core's dependency on it must be inverted first: see below. |
| `auth` | 4,353 | Own phase, after core. Two violations to fix then: it carries its own SQL (`sql.go` and friends move to a state aggregate), and it imports `smb` (`passdb.go` maintains the NT-hash sidecar file; that write moves behind a seam the smb phase owns). |
| `upload` | 3,928 | Own phase. Its durable half already lives in `store/state/upload.go`, which is the right shape. |
| `preview` (+worker) | ~2,600 | Own phase, with `jail`. |
| `watch` | 789 | Own phase (websocket/events). Clean deps already. |
| `oidc` | 1,372 | Auth phase. Clean deps already. |
| `runtimecfg` | 629 | Settings phase. |
| `settingscheck` | 575 | Settings phase. Imports `apierr` today, which is a service package reaching a presentation shape; the probe should return domain errors and let the handler map them. |
| `emergency` | 509 | Settings phase. Imports `httpapi/mw` today, a service package mounting presentation middleware; in the rebuild the emergency server is presentation-layer wiring that borrows service logic, not the reverse. |
| `smb`, `smbpublish`, `smbagent` | ~2,900 | SMB phase. `smb` (config rendering) is effectively a leaf and clean. |

### Presentation

| Package | Lines | Verdict |
| --- | --- | --- |
| `httpapi` (+`handler`, `mw`, `route`, `spa`, `ws`) | ~large | Replaced wholesale by the fiber-based presentation layer, in the protocol phase. |
| `dav` | 4,176 | Rebuilt on the fiber stack in the protocol phase. Parsing rules carry over as spec. |
| `apierr` | 399 | Presentation: the wire error shape and the sentinel-to-status mapping. Its imports of `core`/`auth`/`vfs` are correct direction (presentation reads service errors). The two service packages that import it today (`emergency`, `settingscheck`) are the violation, fixed on their side. |
| `archive` | 317 | The zip writer. Wire format, so presentation; already clean (zero internal imports). |
| `server` | 2,389 | The composition root. Rebuilt last, as the fiber app assembly. |
| `compat/*` | large | Nextcloud compat surface, protocol phase. |

## Cross-layer violations found

1. **`core` imports `search`** (`scan.go` builds `[]search.Source`). A
   service-layer sideways dependency in the wrong direction: search is a
   consumer of the core's shares, not a vocabulary provider to it. Fix by
   inversion: the core exposes its own scan-source shape (share id, root,
   base, per-path allow closure), and the search service, which already sits
   above the core, adapts it into its walker's input. This deletes the one
   non-store, non-kit import the core has.
2. **`acl` mixes evaluation and SQL.** Split as described above.
3. **`auth` mixes service, SQL and the smb sidecar write.** Deferred to the
   auth phase; noted so the state-store re-draw reserves room for the auth
   aggregates.
4. **`emergency` imports `httpapi/mw`**, **`settingscheck` imports
   `apierr`.** Service reaching into presentation. Deferred to the settings
   phase; both fixes are mechanical once the layers exist.
5. **`store/state` imports `store/cache`.** Fixed in the persistence
   rebuild by moving the shared identity tuple down.
6. **`vfs` exports the control-file replace primitives.**
   `ReplaceFileDurable` and `PublishNew` are file persistence, not share
   filesystem security; they move to `store/fsatomic`, and the three
   call sites that bypass them today (`server/setup.go`, `server/tls.go`,
   `server/probefile.go`, plus `smbagent`'s plain writes) are rebuilt on
   top of it in their phases. |

## Amendments to the phase 1 documents

- `11-homes-and-recent.md`: grant persistence moves to a grant aggregate in
  `store/state`, not into the ACL package. The evaluator stays pure; the
  core calls the store's grant write surface and then asks the evaluator to
  reload. The `PersistGrant` contract specified there transfers to the store
  surface unchanged in shape.
- `00-overview.md` dependency table: `search` leaves the list; the scan
  source shape becomes a core-owned type.

## Pre-core work order

What must exist before the core implementation can start, in build order.
Each step is documented before it is built, same as phase 1.

| Step | Scope | Why it blocks the core |
| --- | --- | --- |
| 0.1 | Foundation kit: `kit/num`, `kit/clock`, `kit/task`, `kit/secret`, `kit/limits` | Every later package imports them. Near-verbatim re-specification; the work is naming, grouping, and the limits regrouping. Small. |
| 0.2 | `vfs`, minus the extracted primitives | Every core operation acts through it. The largest and most security-critical blocker; its documents need the same rigor as the core's (admission, safe paths, the creation table, durable write, publish, escape tests). |
| 0.3 | Persistence: `store/fsatomic` (extracted from `vfs`), `store/dbfile`, `store/cache`, `store/journal`, `store/state` re-drawn per aggregate, plus the three new surfaces (links, quota ledger, grants) and the `Ident` move | The core's construction takes the store; the three SQL extractions the core documents promise need their receiving surfaces to exist. `fsatomic` is documented with 0.2 since its spec is carved out of the vfs documents. |
| 0.4 | `acl` evaluator, pure | `Resolve` is built directly on `Evaluate`/`Effective`/`Roots`. Loading comes from the new grant aggregate. |
| 0.5 | Core scan-source inversion | A one-document decision (the type definition lands in the core docs); no separate package to build, but it must be settled before `shareadmin.go` is written. |
| 1 | Core, per documents 00-11 | Starts when 0.1 through 0.5 are done. |

Steps 0.1 and 0.4 are independent of each other and of 0.2; 0.3 depends on
0.1 only. The critical path is 0.2 (`vfs`), which should be documented
first.

## What deliberately waits

Everything not listed above waits for its own phase: auth (+oidc), upload,
search internals, preview (+jail), watch, settings (runtimecfg,
settingscheck, emergency), SMB (smb, smbpublish, smbagent), and the whole
presentation layer on fiber (httpapi replacement, dav, compat, archive,
apierr, server assembly). Their violations are recorded here so each phase
document starts from this list rather than rediscovering it.
