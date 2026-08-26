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

## Branch strategy

- `refactor/core` is the phase 1 branch. Documents land first, then the new
  implementation, in the build order 00-overview defines.
- Later phases branch from the phase before them once that phase's documents
  and implementation are complete.
- Nothing merges to `master` until the phase builds, passes its gates, and
  its documents match what was built.
