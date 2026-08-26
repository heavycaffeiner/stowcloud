# Document plan

> Derived from the three audit documents under [`audit/`](audit/) and the
> package survey ([01-package-survey.md](01-package-survey.md)). Like every
> document in this tree: the existing code is a behavioral specification
> only, and the new implementation is written completely from scratch;
> nothing is copied.

This is the consolidated list of rebuild specification documents, in writing
order. Phase 1 (core, `core/00` through `core/11`) is already written. The
audits found 65 defect citations across the tree; each document below
absorbs the findings for its packages, so a finding is fixed by
specification, not by a patch to the old code.

## Highest-severity audit findings, for orientation

The findings that change plans, as opposed to confirming them:

1. `auth/ratelimit.go`: the login limiter's map and slice have no mutex and
   are hit concurrently per request. Real data race.
2. `dav/lock.go`, `dav/lockmethod.go`: two lock-table TOCTOU windows.
3. `server/tls.go`: cert and key are two independent non-atomic writes that
   can disagree after a crash; `server/setup.go` and `server/probefile.go`
   are non-durable too.
4. `smbagent/sync_linux.go`: `smb.conf` and its candidate written with
   plain `os.WriteFile` while the durable primitive exists.
5. Grant SQL bypass in three places: `httpapi/handler`, `cmd/stowcloud`,
   and `emergency`-adjacent code call `acl.CreateGrant(ctx,
   st.State().SQL(), ...)` directly. Blocks a clean presentation rebuild.
6. `store/state`: the identity tuple is implemented three times (`cache`,
   `dav.go`, `favorite.go`); the size guard misses four insert paths; the
   real share-link SQL lives in `core/links.go`, not in the store.
7. `jail`: `ApplyLimits` and `SealDescriptors` are exported, documented as
   security controls, and never called. Dead code asserting protections
   that do not exist.
8. Username validation exists in three inconsistent spellings (`auth`,
   `smb`, `smbagent`); one bad account name fails whole-batch SMB renders.
9. `emergency` is a full presentation-layer HTTP surface living in the
   service tree, not just a stray import.
10. `compat/*`: a large tested WebDAV compat surface (chunking, trash,
    vendor properties) is built but never wired to a live route. The fiber
    compat phase must decide its scope before treating it as settled
    behavior.
11. Status mapping is hand-written three times (`apierr.Map`,
    `dav.StatusOf`, `ncwire.flowErr`).
12. `vfs` additionally hosts the preview worker's descriptor-passing IPC
    (`seqpacket.go`, `file.go`), a second intruder beside the
    atomic-replace primitives.

## Phase 0: foundation and persistence documents

Written in this order; the core implementation starts when 0.2 and 0.3 are
done. Directory: `docs/refactor/foundation/`.

| # | Document | Scope, and the audit findings it absorbs |
| --- | --- | --- |
| 0.1 | `foundation/kit.md` | `num`, `clock`, `task`, `secret`, `limits` grouped under `kit/`; the layer-grouped reorganization of `limits`; the `smb/bind.go` private-network classifier moves here (used by `httpapi/mw` and `emergency`, nothing SMB about it). |
| 0.2a | `foundation/vfs.md` | Admission, safe paths, resolve mechanics, durable write, publish-part, the creation table, the escape test matrix. Removes both intruders: the atomic-replace primitives (to `fsatomic`) and the seqpacket descriptor-passing IPC (to the preview phase's worker transport). |
| 0.2b | `foundation/fsatomic.md` | `ReplaceFileDurable` and `PublishNew` extracted from `vfs`; every call site listed; the multi-file durable unit (for the TLS cert/key pair); the decision on whether `PublishNew` keeps a caller. |
| 0.3a | `foundation/dbfile.md` | Pragma ordering, two-phase open, migration transaction model. |
| 0.3b | `foundation/state.md` | The state DB re-drawn one aggregate at a time: shares, ops, uploads, settings, overrides, favorites, login flows, DAV locks, config secrets, plus the three new aggregates the core documents extract (links, quota ledger, grants). The identity-tuple unification (one `Ident`, neutral home), the size-guard coverage rule (every insert path), the grant-to-share cascade decision, and the grant write surface that ends the three-site SQL bypass. |
| 0.3c | `foundation/cache.md` | Id derivation and directory-etag contracts; consumes the `Ident` decision from 0.3b. |
| 0.3d | `foundation/journal.md` | The three stated properties (not an audit log, count-capped, no activity stream) as hard requirements. |
| 0.4 | `foundation/acl-evaluator.md` | The pure evaluator only; grant persistence lives in 0.3b; loading from rows the store hands it. |
| 0.5 | `foundation/search-contract.md` | Only what the core needs settled now: the core-owned `ScanSource` shape and the inversion (already reflected in `core/03`). The search internals get their own phase documents later (`search-family`, index format, service tiering, per the foundation audit). |

## Phase 1: core

Written: `core/00` through `core/11`. Amendments already applied: grant
persistence to the store, scan-source inversion, kit paths. One more
amendment lands when 0.3b is written: the `LinkStore` methods must match
the state document's aggregate spelling.

## Phase 2: service phases, each with its own documents

Order among these is negotiable after phase 1; auth first is recommended
because sessions gate every protocol. Directories as named.

| Phase | Documents | Key audit findings absorbed |
| --- | --- | --- |
| auth | `auth/00-overview.md`, `01-credentials-and-sessions.md`, `02-master-key-and-crypto.md`, `03-oidc-integration.md`, `04-audit-log.md`, `05-username-policy.md` | The limiter race (a fix, not a port); auth SQL moves to state aggregates; the passdb sidecar write goes behind an SMB-phase seam; one canonical username rule shared by `auth`, `smb`, `smbagent`. |
| oidc | `oidc/00-relying-party.md` | Clean package; behavioral transcription (discovery validation, JWKS caching, SSRF guard). |
| upload | `upload/00-overview.md`, `01-session-lifecycle.md`, `02-spool-modes.md`, `03-cache-spool.md`, `04-verification-and-limits.md` | Durability invariants as normative; the cache spool's fake-share-id question (non-share safe-root capability in vfs, or spool under persistence); `StateFinalizing` resolution. |
| search | `search/00-family.md`, `01-index-format.md`, `02-service.md` | Walk/scan/build triplication; segment durability; trigram consolidation; tier selection. Target package `engine/service/search`; the core-facing contract is already settled in `foundation/search-contract.md`. |
| preview | `preview/00-overview.md`, `01-jail.md`, `02-worker-protocol.md`, `03-cache.md`, `04-archive-listing.md` | Jail's dead exports become wired or deleted; the seqpacket transport moves here from vfs; cache repoints at fsatomic. |
| watch | `watch/00-watch.md` (small, single doc) | Two-tier hot set, fail-closed inotify parsing, whole-share escalation. Target package `engine/service/watch`. |
| settings | `settings/00-runtimecfg.md`, `01-settingscheck.md`, `02-emergency.md` | `settingscheck` returns domain errors (no `apierr` import); `emergency` re-homed as presentation wiring with its invariants preserved (404-not-403 gate, audit-on-every-write, write-then-restart). |
| smb | `smb/00-config-rendering.md`, `01-publish-and-agent-protocol.md`, `02-agent-durable-writes.md`, `03-agent-package-split.md` | Refuse-not-escape rendering rules; the `bind.go` move (to kit, 0.1); the two plain writes fixed; the agent split into protocol, supervision, scope, reconciliation. |

## Phase 3: presentation on fiber

Directory: `docs/refactor/http/`.

| Document | Scope, and the audit findings absorbed |
| --- | --- | 
| `http/00-overview.md` | The fiber application shape, layer rules, what replaces `httpapi`/`server`; fiber-specific hazards the audit lists (fasthttp `Flusher`/`Hijacker` differences, wildcard route semantics, CORS and body-limit defaults). |
| `http/01-middleware-chain.md` | The ordered chain as data (the current `chain.go` discipline), auth credential-resolution order, CSRF and HostGuard coupling, the three fail-closed proxy-trust rules, public-path rules; each named historical regression becomes a named test. |
| `http/02-error-mapping.md` | One canonical error-class enumeration with per-protocol adapters, replacing the three hand-written mappers. |
| `http/03-handlers.md` | The native REST surface over the service layer; handlers stop reaching into `acl`+SQL (they call the grant surface from 0.3b). |
| `http/04-webdav.md` | RFC 4918 parsing rules, the If-header grammar, the lock conflict matrix, the two TOCTOU fixes. |
| `http/05-compat-scope.md` | The Nextcloud compat decision: what of the built-but-unwired surface ships, feature by feature, before any of it is treated as spec. |
| `http/06-login-flow-v2.md` | The two-token state machine, digest-only storage, POST-only approval; standalone because it is reusable security design. |
| `http/07-server-assembly.md` | Composition root on fiber: supervisor/hot-swap semantics (bind-new-before-touching-old, bounded drain), TLS material through fsatomic as one durable unit, setup token and probe file through fsatomic. |
| `http/08-regression-tests.md` | The transcription of every documented past-incident comment (`httpapi/mw`, `server`, `compat/nc`) into named test cases. |
| `http/09-api-consistency.md` | Written ahead of the rest of phase 3: the v1 API. Everything moves under `/api/v1` in nine categories (auth, account, files, links, trash, jobs, uploads, search, admin, system), every endpoint renamed into one scheme, all aliases dead, the settings split folded into one resource, and the frontend rewired in the same phase. The old API dies with the old stack. |

## Working rules

- A document is written immediately before its implementation step, not all
  up front: phase 0 documents now, phase 2 and 3 documents at their phases.
  The audit documents hold the findings until then.
- Every document carries the from-scratch blockquote and a "Deliberate
  changes" section; audit-found defects land there as changes, with the
  audit line cited.
- The import-graph gate (`tools/layercheck`) grows with each phase. The
  tier table and the intra-tier exceptions are fixed in
  `03-engine-bootstrap.md`; the violations the audits catalogued are the
  gate's initial test list.
