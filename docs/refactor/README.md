# Engine rebuild program

This directory holds the design documents for the ground-up rebuild of the
Stowcloud engine. Every document in this tree follows the same rule, stated
here once and repeated in each document:

> The rebuild references the existing code under `go/` as a specification of
> behavior, but the new implementation is written completely from scratch. No
> file, function, or line is carried over verbatim. Where a document cites an
> existing file, the citation is a pointer to the behavior being preserved or
> deliberately changed, never to code being copied.

## Instructions to implementers

Binding on anyone, human or agent, who writes code for this rebuild. Read
this section before touching the tree.

1. **Build exactly the tree below.** New code goes under `go/engine/`, in
   the tier directory the layout names, at the exact path the owning
   document names. Do not invent a directory, do not add a tier, do not
   place a package at a path no document names. If the right location is
   genuinely unclear, the layout in [03-engine-bootstrap.md](03-engine-bootstrap.md)
   decides; if it does not decide, stop and ask rather than guess.
2. **Write everything from scratch.** Never copy, move, or adapt a file,
   function, type, comment, or SQL statement from `go/internal/`. Open the
   old code to learn behavior, close it, and write new code from the
   specification document. If the document and the old code disagree, the
   document wins; if the disagreement looks like a documentation bug,
   stop and ask rather than silently following either.

   This covers comments, and comments are where it was broken. An audit
   during phase 2 found 2596 comment lines in `go/engine/` that were
   byte-identical to lines in `go/internal/`, against zero shared
   non-trivial code lines: the code was rewritten and the prose was moved.
   The cost is not only provenance. A carried comment describes the system
   it was written for, and the preview decoder reached the engine still
   claiming a 512 MiB address-space limit that measurement had already
   replaced with 2 GiB. Restate the explanation in your own words, and
   check as you write that it still describes what the new code does.
   `tools/freshscan` runs in `scripts/verify.sh` and fails on any carried
   line over thirty characters.
3. **Never edit the old tree.** `go/internal/` is read-only for the whole
   rebuild: no fixes, no refactors, no formatting, however small. A defect
   found there is fixed in the new implementation by its document's
   "Deliberate changes" section, not in place. Deletion of old packages
   happens only at a phase boundary, in the same change set that wires
   their replacement.
4. **Follow the document, whole.** Each package is implemented from its
   specification document: the listed files, the exact signatures, the
   "Deliberate changes", and the full test list. A spec test that is not
   written is a step that is not done. Do not add surface the document
   does not name; YAGNI holds.
5. **Respect the import rules.** The tier table and the intra-tier
   exception list in [03-engine-bootstrap.md](03-engine-bootstrap.md) are
   exhaustive. No import of `go/internal/` from `go/engine/`, ever. No
   fiber or `net/http` outside `engine/http/`.
6. **Build order is dependency order.** Phase 0 in the order kit, vfs and
   fsatomic, store, acl ([01-package-survey.md](01-package-survey.md) work
   order); phase 1 per [core/00-overview.md](core/00-overview.md); each
   step compiles and passes its tests before the next begins.
7. **Gates from the first commit.** koscan, vetgo, vetsecret and
   `tools/layercheck` cover `engine/` from the start; `engine/`'s nolint
   budget is zero. A change that needs a nolint or a new dependency is a
   change to argue in a document first.

## Scope and direction

- The engine is rebuilt bottom-up, one layer at a time, on dedicated
  branches. Each layer is documented before it is built.
- The HTTP layer moves from the current hand-rolled `net/http` stack
  (`go/internal/httpapi`, `go/internal/server`) to go-fiber. The domain core
  stays HTTP-agnostic: nothing below the protocol layer may import fiber, and
  the rebuild enforces that with the same import-graph gate the current tree
  uses.
- The target architecture is a 3-layer design: presentation (fiber, WebDAV,
  compat, wire shapes), service (core, ACL evaluation, auth, search, upload,
  preview), persistence (the three SQLite databases and every SQL
  statement), over a small foundation kit. The directory tree spells the
  tiers, so a package's layer is readable from its path:

  ```
  go/engine/
    kit/       foundation        num, clock, task, secret, limits, netzone, unixprobe
    infra/     domain infra      vfs, jail
    store/     persistence       fsatomic, ident, dbfile, cache, journal, state
    service/   service layer     acl, core, auth, oidc, upload, search, preview,
                                 watch, settings, smb
    http/      presentation      middleware, handler, dav, compat, apierr,
                                 archive, server
  ```

  The full package survey, the layer assignment for every current package,
  and the cross-layer violations to fix are in
  [01-package-survey.md](01-package-survey.md). The decisions that shape
  the rebuild, each with its reasoning, are indexed in
  [00-decisions.md](00-decisions.md).
  Every package was then audited file by file; findings are under
  [audit/](audit/), and the consolidated document plan derived from them is
  [02-document-plan.md](02-document-plan.md). Module layout, coexistence
  rules, gates over the new tree, and the data-compatibility contract are
  settled in [03-engine-bootstrap.md](03-engine-bootstrap.md).
- The rebuild order is dependency order: the pre-core foundations first
  (kit, vfs, persistence, the pure ACL evaluator; work order in the survey
  document), then the domain core, then the protocol layers on fiber, then
  compatibility surfaces (WebDAV, Nextcloud compat, SMB agent).

## Phase 0: foundations

What must be re-specified and rebuilt before the core implementation can
start. Scope and ordering in [01-package-survey.md](01-package-survey.md).
All phase 0 documents are written, under [`foundation/`](foundation/):

| Document | Contents |
| --- | --- |
| [kit.md](foundation/kit.md) | `num`, `clock`, `task`, `secret`, `limits`, `netzone` under `engine/kit/` |
| [vfs.md](foundation/vfs.md) | The filesystem security package: admission, paths, resolve, durable writes |
| [fsatomic.md](foundation/fsatomic.md) | Atomic file replace, extracted from vfs; the multi-file durable unit |
| [dbfile.md](foundation/dbfile.md) | SQLite open discipline, pragmas, migrations |
| [state.md](foundation/state.md) | The state DB per aggregate, including the new link, quota and grant surfaces |
| [cache.md](foundation/cache.md) | The rebuildable cache DB, id derivation, directory etags, the `Ident` home |
| [journal.md](foundation/journal.md) | The write journal's three hard properties |
| [acl-evaluator.md](foundation/acl-evaluator.md) | The pure permission evaluator, SQL-free |
| [search-contract.md](foundation/search-contract.md) | The core-owned `ScanSource` shape and the inversion |

## Phase 1: core

The first phase rebuilds the domain core, currently `go/internal/core`. The
governing principle for this phase:

**Re-organize the monolithic composition and raise cohesion. Keep the core one
package by default, but separate the parts whose responsibilities demand
separation.**

Concretely: code that changes for the same reason lands in one unit; SQL that
belongs to storage leaves the domain files; helper functions live beside their
only caller or in the one shared unit that owns their concept; and clusters
that grew by accretion (share registry, transfers, links) are re-drawn along
their real seams.

The core documents live in [`core/`](core/):

| Document | Contents |
| --- | --- |
| [00-overview.md](core/00-overview.md) | Architecture, package layout, cohesion decisions, build order |
| [01-errors.md](core/01-errors.md) | Error taxonomy and the VFS error crossing |
| [02-domain-types.md](core/02-domain-types.md) | Identifiers, Entry, ETag, pages, cursors, sort keys |
| [03-share-registry.md](core/03-share-registry.md) | Share registry, broken shares, admin CRUD, reload, probing, scan sources |
| [04-resolution.md](core/04-resolution.md) | Resolved, the single resolve gate, descent, the vpath crossings |
| [05-listing-and-read.md](core/05-listing-and-read.md) | Directory listing, stat, streams, random reads, archive walk |
| [06-mutations.md](core/06-mutations.md) | Mkdir, file creation, rename, delete, publish, preconditions, journal |
| [07-transfers.md](core/07-transfers.md) | Move, copy, conflict policy, cross-device rules, long operations |
| [08-trash.md](core/08-trash.md) | Per-share trash: move, list, restore, purge |
| [09-quota-and-aggregates.md](core/09-quota-and-aggregates.md) | Free-space floor, per-user quota ledger, directory aggregates, invalidation |
| [10-share-links.md](core/10-share-links.md) | Public share links: minting, liveness, browse, drop, passwords |
| [11-homes-and-recent.md](core/11-homes-and-recent.md) | Per-user homes, the recent-writes surface |

## Phase 2: services

The remaining service specifications are complete:

| Area | Documents |
| --- | --- |
| auth | [`auth/00-overview.md`](auth/00-overview.md) through [`auth/05-username-policy.md`](auth/05-username-policy.md) |
| oidc | [`oidc/00-relying-party.md`](oidc/00-relying-party.md) |
| upload | [`upload/00-overview.md`](upload/00-overview.md) through [`upload/04-verification-and-limits.md`](upload/04-verification-and-limits.md) |
| search | [`search/00-family.md`](search/00-family.md) through [`search/02-service.md`](search/02-service.md) |
| preview | [`preview/00-overview.md`](preview/00-overview.md) through [`preview/04-archive-listing.md`](preview/04-archive-listing.md) |
| watch | [`watch/00-watch.md`](watch/00-watch.md) |
| settings | [`settings/00-runtimecfg.md`](settings/00-runtimecfg.md) through [`settings/02-emergency.md`](settings/02-emergency.md) |
| smb | [`smb/00-config-rendering.md`](smb/00-config-rendering.md) through [`smb/03-agent-runtime.md`](smb/03-agent-runtime.md) |

### Phase 0 through 2 audit

Audited against the tree before phase 3's implementation began, on the two
things the documents can be measured on.

**Declared signatures.** Every `func` in every phase 0 through 2 document,
344 of them, compared with what the engine implements. Seven had drifted.
Six were the document being stale and were corrected in place: `Assemble`,
`NewKeyRing`, preview's `NewService`, smb's `Render`, `Varint` and `Walk`.
One was the code being wrong: `PutNamed` carried a `sum *Checksum` it never
read, so a name-ordered upload accepted a chunk whose digest did not match
while an offset-addressed one refused it. Fixed, with the deliberate change
recorded in [upload/02-spool-modes.md](upload/02-spool-modes.md).

Twenty-one documented names have no implementation. All twenty-one are
deferred by their own documents: nineteen under core's "Phase 3 protocol
seam amendment", settings' section-application service, smb's access-change
sink, plus `PublishNew`, which [foundation/fsatomic.md](foundation/fsatomic.md)
decides to drop rather than carry.

**Deliberate changes.** `tools/speccheck` had been pointed at the phase 2
areas only, leaving foundation's and core's 175 numbered changes checked by
nothing. Both are in the gate now. The seventeen identifiers that surfaced
were all documents naming the old tree's spelling in order to say what
replaced it; the tool now scopes that skip to one identifier rather than a
whole entry, and the six needing a judgement are in its ignore list with
what replaced them.

One test named in a document was missing rather than merely unnamed: the
mutations document requires that a failing journal not fail the write, and
only the nil-journal case was covered. The behavior was already correct.

## Phase 3: presentation and cutover

The presentation specifications live under [`http/`](http/):

| Document | Contents |
| --- | --- |
| [00-overview.md](http/00-overview.md) | Fiber shape, package inventory, content-host split and build order |
| [01-middleware-chain.md](http/01-middleware-chain.md) | Route metadata, auth, proxy trust, host/origin, CSRF and limits |
| [02-error-mapping.md](http/02-error-mapping.md) | One classifier with REST, DAV and OCS adapters |
| [03-handlers.md](http/03-handlers.md) | Native v1, public links, TUS, SSE, WebSocket and archive streams |
| [04-webdav.md](http/04-webdav.md) | XML/If parsing, methods, locks and the two race fixes |
| [05-compat-scope.md](http/05-compat-scope.md) | Complete Nextcloud OCS and DAV compatibility matrix |
| [06-login-flow-v2.md](http/06-login-flow-v2.md) | Two-token approval and retryable sealed credential delivery |
| [07-server-assembly.md](http/07-server-assembly.md) | Listener generations, TLS/setup durability, process assembly and cutover |
| [08-regression-tests.md](http/08-regression-tests.md) | Named tests for every historical presentation regression |
| [09-api-consistency.md](http/09-api-consistency.md) | The `/api/v1` route contract and frontend rewiring |

## Branch strategy

- `refactor/core` is the phase 1 branch. Documents land first, then the new
  implementation, in the build order 00-overview defines.
- Later phases branch from the phase before them once that phase's documents
  and implementation are complete.
- Nothing merges to `master` until the phase builds, passes its gates, and
  its documents match what was built.
