# Engine bootstrap

> This document settles the decisions every other rebuild document defers to
> it: where the new code lives, how it coexists with the old tree while the
> rebuild is in progress, which gates cover it, and what the rebuilt engine
> owes an existing deployment's data. Like every document in this tree: the
> existing code is a behavioral specification only, and the new
> implementation is written completely from scratch; nothing is copied.

## Module and directory layout

The rebuilt engine lives at `go/engine/`, inside the existing Go module
`github.com/heavycaffeiner/stowcloud/go`. Not a second module.

The directory tree spells the layers. Each of the five top-level
directories under `engine/` is one tier of the architecture, so the layer
of any package is readable from its path, and the layer gate's rule is a
prefix check rather than a lookup table:

```
go/
  engine/
    kit/           foundation: any layer may import; imports stdlib only
      num/  clock/  task/  secret/  limits/  netzone/  unixprobe/
    infra/         domain infrastructure: below service, beside store
      vfs/         the filesystem security package (phase 0)
      jail/        the process sandbox (preview phase)
    store/         persistence: databases and file persistence
      fsatomic/  ident/  dbfile/  cache/  journal/  state/
    service/       the service layer, one package per subsystem
      acl/         the pure permission evaluator (phase 0)
      core/        the domain core (phase 1)
      auth/  oidc/  upload/  search/  preview/  watch/
      settings/  smb/          (each in its own phase)
    http/          presentation: the only tree that may import fiber
      middleware/  handler/  dav/  compat/  apierr/  archive/  server/
  internal/        the old tree, deleted phase by phase
  cmd/
  tools/
```

Import direction is strictly downward through the tiers:

| Tier | May import |
| --- | --- |
| `kit` | stdlib only |
| `infra`, `store` | `kit` |
| `service` | `infra`, `store`, `kit` |
| `http` | `service`, `kit` (and fiber; nothing else may) |

Sideways imports inside a tier point only downward in build order, and the
legal list is explicit:

- `service/acl` imports nothing else in `service/` (it is the tier's
  bottom, pure evaluation).
- `service/core` imports `service/acl` and nothing else in `service/`
  (`Resolve` is built on `Evaluate`/`Effective`/`Roots`).
- `service/search` imports `service/core` (the scan-source inversion);
  later service packages declare their sideways imports in their own
  phase documents, always downward, never toward `http`.
- `store/state` imports `store/ident` and `store/dbfile`, never
  `store/cache`; `store/cache` and `store/journal` import `store/ident`
  and `store/dbfile` likewise.
- `infra/vfs` and `infra/jail` import nothing else in `infra/` and
  nothing in `store/`.

Why one module:

- The gates (`deps.allow`, `nolint.budget`, koscan, vetgo, vetsecret,
  `verify.sh`) operate on this module. A second module means a second gate
  wiring, and a gap between the two is where a rule quietly stops holding.
- The cutover is a change of imports in `cmd/`, not a module graph surgery.
- A shared module makes the one temporary crossing that is allowed (below)
  visible to the import gate instead of hidden behind a replace directive.

Import paths are therefore
`github.com/heavycaffeiner/stowcloud/go/engine/<tier>/<pkg>`. Every phase 0
and phase 1 document spells engine paths in this layout already
(`engine/infra/vfs`, `engine/service/acl`, `engine/service/core`,
`engine/kit/...`, `engine/store/...`).

## Coexistence rules during the rebuild

- **Old does not import new, new does not import old.** The two trees share
  the module and nothing else. The rebuild documents are the only bridge:
  behavior crosses by specification, never by import. One exception is
  allowed for exactly as long as a phase needs it: a `cmd/` binary or an
  integration test may import both trees to run them side by side. The
  exception lives in `cmd/` and `*_test.go` only, never in either tree.
- **The old tree stays green.** `verify.sh` keeps running the old tree's
  tests and gates until the phase that deletes each package. A rebuild
  phase that breaks the old tree has changed something it does not own.
- **Deletion is per phase, at the end.** When phase 3's assembly switches
  `cmd/stowcloud` to the engine, the replaced `internal/` packages are
  deleted in the same change set, so there is never a third state where
  both are wired.

## Gates over the engine tree

The existing discipline extends to `go/engine/` from the first commit:

- **koscan, vetgo, vetsecret** run over `engine/` exactly as over
  `internal/`. `task.Go` moves to `engine/kit/task`; vetgo's allowed spawn
  location gains that path (and loses `internal/task` when phase 3 deletes
  it).
- **nolint budget**: `engine/` starts at zero and gets its own count in
  `nolint.budget`. The old tree's budget is frozen; it may only go down.
- **deps.allow**: phases 0 and 1 add no module. The blake3, sqlite, x/sys,
  x/crypto, x/text entries already cover the engine's needs. go-fiber (and
  its fasthttp transitive) enters `deps.allow` in phase 3, with its version
  and the justification on the line, per that file's own rule. Nothing else
  is anticipated; a phase that wants a new module argues for it in its
  documents first.
- **The layer gate is new.** A small tool (`tools/layercheck`) reads the
  import graph of `engine/` and enforces the tier table above as a path
  prefix rule: a package's tier is the first path element under `engine/`,
  and an import is legal only when the importing tier lists the imported
  tier. The intra-tier build-order exceptions (`service/search` over
  `service/core`, `store/state` over `store/ident` and `store/dbfile`) are
  the tool's one explicit list. `http` is the only tree that may import
  fiber, and nothing under `engine/` except `http` may import `net/http`
  either. The audit's violation catalogue (`02-document-plan.md`) is the
  tool's initial test fixture: each entry is reproduced as a
  refused-import test.
- **contractcheck and routecheck** stay pointed at the old tree until
  phase 3, whose documents re-point them at the fiber handlers as part of
  the assembly.

## What the engine owes existing data

The rebuilt engine opens what the current build wrote. This is a hard
compatibility contract, stated once here so no phase document has to
re-derive it:

- **The three databases.** `state.db`, `cache.db`, `journal.db` open
  unchanged: same schemas, same `schema_version` history, same pragma
  expectations. `foundation/state.md` and its siblings re-specify the
  schemas from the current tree for exactly this reason. A schema change a
  phase needs is a forward migration step under `foundation/dbfile.md`'s
  runner, never an adoption path or a parallel format.
- **The key artifacts.** The master key ring file and everything sealed
  under it (link tokens, config secrets) open unchanged: same file format,
  same AEAD construction, same AAD binding. The auth phase re-specifies
  the format; it does not get to change it without a rotation step that
  reads the old and writes the new.
- **Client-visible identity.** Instance id, file ids (identity-derived, not
  row-derived), and change tokens keep their derivations, so no attached
  client re-syncs because of the rebuild. The derivations are specified in
  `foundation/cache.md` and `core/02-domain-types.md`.
- **No importer.** Consistent with the cutover's one-way door
  (`docs/CUTOVER.md`): there is no adoption or conversion code for any
  older format. What this engine opens is what the current build wrote,
  because the rebuild holds the formats fixed.

Anything a phase wants to change in a durable shape is a deliberate change
in that phase's document plus a migration step, and the phase carries the
compatibility argument.

## Branch and merge discipline

- `refactor/core` carries phase 0 and phase 1: documents, then
  implementation in the build order the documents define
  (`01-package-survey.md` for phase 0, `core/00-overview.md` for phase 1).
- Each later phase branches from the previous phase's merged result.
- A phase merges to `master` when: the engine packages it adds build and
  pass their tests, `verify.sh` passes whole (old tree included), the layer
  gate passes, and the phase's documents match what was built.

## Deferred documents

For completeness against `02-document-plan.md`: every phase 2 and phase 3
document (auth, oidc, upload, search, preview, watch, settings, smb,
`http/00` through `http/08`) is deliberately deferred to its phase, per the
working rules there. The audits under `audit/` hold their findings until
then. No other document blocks phases 0 and 1.
