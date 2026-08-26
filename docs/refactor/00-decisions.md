# Decision record

The decisions that shape the rebuild, each with its reasoning, recorded so
no later phase re-litigates them without new facts. Detail lives in the
documents each entry cites; this file is the index of what was decided and
why.

## D1. Ground-up rebuild, documents first

The engine is rebuilt from the bottom up, one layer at a time, on dedicated
branches. Every document states, and every implementation follows, the same
rule: the existing code under `go/internal` is a behavioral specification
only; the new implementation is written completely from scratch, nothing
copied. A phase's documents are written immediately before that phase's
implementation, not all up front, so later documents absorb what earlier
implementation taught.

## D2. The HTTP engine moves to go-fiber

The presentation layer is rebuilt on go-fiber, replacing the hand-rolled
`net/http` stack (`internal/httpapi`, `internal/server`). Everything below
the presentation layer stays HTTP-agnostic: fiber is importable only under
`engine/http/`, enforced by the layer gate. fiber and its fasthttp
transitive enter `deps.allow` in phase 3 and not before.
(`03-engine-bootstrap.md`, `02-document-plan.md` phase 3.)

## D3. Three layers, five directories

The target is a 3-layer architecture (presentation, service, persistence)
over a foundation kit, and the directory tree spells the tiers so a
package's layer is readable from its path:

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

Import direction is strictly downward (`kit` from nothing, `infra` and
`store` from `kit`, `service` from `infra`/`store`/`kit`, `http` from
`service`/`kit`); intra-tier sideways imports are an explicit short list.
`tools/layercheck` enforces the table as a path prefix rule.
(`03-engine-bootstrap.md` for the table and exceptions,
`01-package-survey.md` for the per-package assignment.)

## D4. One module, two trees, phase-wise deletion

The engine lives at `go/engine/` inside the existing module; no second
module. The old tree stays green and is deleted package by package, each in
the same change set that wires its replacement. Old and new never import
each other; only `cmd/` and tests may hold both during a phase.
(`03-engine-bootstrap.md`.)

## D5. The domain core stays one package

`engine/service/core` is a single Go package, deliberately: the `Resolved`
capability (unexported fields, obtainable only through `Resolve`) is the
ACL guarantee, and a package split would force exported constructors or
purposeless interfaces. Cohesion is raised by re-drawing file boundaries,
one closed concept per file. (`core/00-overview.md`.)

## D6. Three extractions from the core

Code that changes for storage reasons leaves the domain package:

- Share-link SQL moves behind a `LinkStore` interface implemented in
  `store/state` (`core/10`, `foundation/state.md`).
- The quota ledger SQL moves to `store/state`; the core keeps the
  `QuotaSink` interface, whose `Reserve` returns `(ok, err)` because the
  store cannot import the core's sentinel (`core/09`,
  `foundation/state.md`).
- Grant persistence moves to a grant aggregate in `store/state`, not into
  the ACL package: the evaluator stays pure. The core gains `grants.go`,
  thin CRUD wrappers plus one evaluator reload each, which ends the
  three-site raw-SQL bypass the audits found (`core/11`,
  `foundation/state.md`, `audit/presentation.md`).

## D7. The scan-source inversion

The current core imports `internal/search` to build `[]search.Source`; the
dependency points the wrong way. The rebuilt core owns a `ScanSource` type
and the search service adapts it. The rebuilt core imports nothing from
search. (`core/03`, `foundation/search-contract.md`.)

## D8. Persistence includes files, not only SQLite

The persistence layer is the three SQLite databases plus every file the
engine writes about itself: keys, rendered configs, index snapshots, cache
entries, tokens, certificates. The atomic-replace primitives
(`ReplaceFileDurable`, and `PublishNew`, which grep showed has no caller
and is dropped) move from `vfs` to `store/fsatomic`, which also gains a
multi-file durable unit for writes that must land together (the TLS
cert/key pair). The share trees are explicitly not persistence: user files
are the domain itself, written only through `vfs.ShareRoot`, whose
admission and escape rules must not sit behind a storage abstraction.
(`01-package-survey.md` persistence section, `foundation/fsatomic.md`.)

## D9. vfs keeps only share-filesystem security

Two intruders leave `vfs`: the atomic-replace primitives (D8) and the
seqpacket descriptor-passing IPC, which serves only the preview worker and
moves to the preview phase's worker transport. After the moves, everything
`vfs` exports goes through a `ShareRoot` or a path type.
(`foundation/vfs.md`.)

## D10. The identity tuple has one home

The (share, dev, ino, btime) identity tuple exists three times today
(`store/cache`, `state/dav.go`, `state/favorite.go`) and forces
`store/state` to import `store/cache`. It moves to `engine/store/ident`, a
dependency-free package both databases import. (`foundation/cache.md` for
the decision, `foundation/state.md` for the unification.)

## D11. No grant-to-share foreign key

The grant table's `share` column gets no FK to `share_definition`, because
not every valid share id has a row there: the homes share is registered
under the reserved id `999_999` and never inserted, so an FK would refuse
every home grant. The cascade is enforced instead inside the store's own
`DeleteShare(ctx, rowid, shareID)`, in the same transaction as the share
row's deletion. (`foundation/state.md` cascade section, `core/03`,
`core/11`.)

## D12. Data compatibility is a hard contract

The rebuilt engine opens what the current build wrote: same database
schemas and `schema_version` history, same key-ring format and AEAD
binding, same instance-id, file-id and change-token derivations, so no
attached client re-syncs. No importer, no adoption path, consistent with
the cutover's one-way door. A durable-shape change is a forward migration
step argued in the phase document that needs it. (`03-engine-bootstrap.md`.)

## D13. Audit findings are fixed by specification

Every package was audited file by file before phase 0 was specified
(`audit/`, 65 defect citations). A finding is absorbed into the rebuild
document that owns the code, as a "Deliberate changes" entry citing the
audit; nothing is patched in the old tree. The headline findings and the
document each lands in are indexed in `02-document-plan.md`. The ones that
change behavior: the login limiter gets its missing mutex (auth phase), the
two DAV lock TOCTOU windows close (`http/04`), the TLS pair becomes one
durable unit (`http/07`), username validation becomes one rule
(`auth/05`), jail's dead security exports are wired or deleted
(`preview/01`), and the Nextcloud compat scope is decided before any
unwired code is treated as spec (`http/05`).

## D14. Gates from the first commit

The engine tree is covered by the existing gates (koscan, vetgo,
vetsecret, deps.allow, verify.sh) from its first commit, plus the new
`tools/layercheck`. `engine/`'s nolint budget starts at zero; the old
tree's budget is frozen and may only go down. (`03-engine-bootstrap.md`.)

## D15. Implementer discipline

The rules binding anyone who writes code for the rebuild are spelled out
in the README's "Instructions to implementers" section: build exactly the
normative tree, write everything from scratch with the old tree read-only,
follow each specification document whole including its test list, respect
the import rules, build in dependency order, and keep the gates green from
the first commit. An agent given a rebuild task starts by reading that
section, the bootstrap document, and the specification document for the
package it is building. (README, `03-engine-bootstrap.md`.)

## D16. Phase order

Phase 0 builds `kit`, `infra/vfs`, `store/*`, `service/acl` in that
dependency order (critical path: vfs). Phase 1 builds `service/core` per
`core/00`'s build order. Phase 2 covers the remaining service packages,
auth first because sessions gate every protocol. Phase 3 is the fiber
presentation layer and the cutover, which switches `cmd/stowcloud` to
`engine/http/server` and deletes the last of `internal/`.
(`01-package-survey.md` work order, `02-document-plan.md` phases.)
