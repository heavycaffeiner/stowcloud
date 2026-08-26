# Engine rebuild program

This directory holds the design documents for the ground-up rebuild of the
Stowcloud engine. Every document in this tree follows the same rule, stated
here once and repeated in each document:

> The rebuild references the existing code under `go/` as a specification of
> behavior, but the new implementation is written completely from scratch. No
> file, function, or line is carried over verbatim. Where a document cites an
> existing file, the citation is a pointer to the behavior being preserved or
> deliberately changed, never to code being copied.

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
  statement), over a small foundation kit. The full package survey, the
  layer assignment for every current package, and the cross-layer
  violations to fix are in [01-package-survey.md](01-package-survey.md).
  Every package was then audited file by file; findings are under
  [audit/](audit/), and the consolidated document plan derived from them is
  [02-document-plan.md](02-document-plan.md).
- The rebuild order is dependency order: the pre-core foundations first
  (kit, vfs, persistence, the pure ACL evaluator; work order in the survey
  document), then the domain core, then the protocol layers on fiber, then
  compatibility surfaces (WebDAV, Nextcloud compat, SMB agent).

## Phase 0: foundations

What must be re-specified and rebuilt before the core implementation can
start. Scope and ordering in [01-package-survey.md](01-package-survey.md):
foundation kit (`num`, `clock`, `task`, `secret`, `limits`), `vfs`, the
persistence layer re-drawn per aggregate (including the grant, link and
quota surfaces the core documents extract), and the ACL evaluator stripped
of its SQL. Documents for these land under `foundation/` as they are
written.

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
