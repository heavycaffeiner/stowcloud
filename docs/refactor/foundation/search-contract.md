# Foundation: search contract

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core/scan.go` and `go/internal/search/walk.go` is referenced
> as a behavioral specification only. The new implementation is written
> completely new; nothing is copied.

## Purpose

This is not the search rebuild. It settles the one thing the core needs
fixed before `engine/core/shareadmin.go` is written: the shape the core
hands to the search service, and the direction of the dependency between
them. The type itself is already specified in
`core/03-share-registry.md`'s "Scan sources" section; this document exists
so the search side of the same seam has one place to be defined, cited
from `02-document-plan.md`'s item 0.5, without waiting for the rest of the
search rebuild.

Everything about how search actually walks, indexes, tiers or ranks is a
later phase (`02-document-plan.md`, phase 2, `search/00-family.md` and
following; `audit/foundation-persistence.md`'s search sections name the
findings those documents absorb). None of that is settled here.

## Spec

### ScanSource (core-owned)

Defined in `engine/core`, exactly as `core/03-share-registry.md` specifies
it:

```go
// ScanSource is one share as the search walker consumes it. Defined here,
// adapted by the search service into its own input type.
type ScanSource struct {
    Share uint32
    Root  *vfs.ShareRoot
    Base  vfs.SafePath
    // Allow reports whether the caller may see a path. Nil means
    // everything, which is the administrator-scoped form.
    Allow func(p vfs.SafePath, isDir bool) bool
}
```

The core produces `[]ScanSource` through two functions, also specified in
`core/03-share-registry.md`:

```go
func (c *Core) ScanSources() []ScanSource               // administrator-scoped, Allow nil
func (c *Core) UserScanSources(user UserID) []ScanSource // Allow checks acl.Read per entry
```

`ScanSource` carries no persistence-specific or index-specific field: no
index handle, no segment reference, nothing from `search/index`. It names
exactly what a walk over one share needs (which share, its open root,
where under it to start, and what the caller may see), which is what
keeps this type a core vocabulary item rather than a search one.

### The inversion

Today, `core/scan.go` builds `[]search.Source` directly: the core imports
`search` to speak its vocabulary. `01-package-survey.md`'s cross-layer
violation 1 and `audit/foundation-persistence.md`'s search finding 1 both
name this as backwards: search is a consumer of the core's shares, not a
vocabulary provider to the core, so the dependency should point from
search to core, not the reverse.

The inversion:

- **`core` defines `ScanSource`** and exposes `ScanSources`/
  `UserScanSources`. `core` imports nothing from `search`.
- **`search` imports `core`** (for `core.ScanSource` and, where its own
  entry points need it, `vfs.ShareRoot`/`vfs.SafePath`, which both
  packages already depend on independently). The search service is the
  one that knows what its own walker's input type looks like
  (`search.Source`, `walk.go`), and it is the one that writes the small
  adaptation function converting a `[]core.ScanSource` into whatever its
  internals want.
- This direction never reverses. A future search document that wants a
  richer per-source field (a per-share index tier hint, for example) adds
  it to `search`'s own adapted type, not to `core.ScanSource`; growing
  `core.ScanSource` for search's internal convenience would pull search
  concerns back into the core.

### The adaptation contract

The conversion from `[]core.ScanSource` to the search walker's own input
is search's function to write, not specified here beyond its shape and
its two obligations:

1. **Field-for-field, no core reinterpretation.** `Share`, `Root` and
   `Base` map straight across. `Allow` is not called, wrapped in a way
   that changes its answer, or cached by the adapter itself; whatever
   wrapping the walker's own input type wants (for example, a
   `Prefix string` field the current `search.Source` carries for
   rendering hit paths) is added by the adapter, never by asking the core
   for something extra.
2. **One direction only.** The adapter lives in the search package. The
   core never imports the search package's input type, current or future,
   and never calls into search directly; whatever assembles a search
   request (the search service, or its caller) is the one that holds both
   a `[]core.ScanSource` and the adapter, and calls the walker with the
   adapted result.

### What the walker may assume about Allow

`ScanSource.Allow` is a per-entry, not a per-share, gate. The current
walker (`search/walk.go`, `Walk`'s `visit`) already calls it exactly this
way, and the contract for a future walker built against this document is
the same:

- **Called per entry, before scoring.** `Allow` is checked as each
  directory entry is read, before the entry is matched against a query or
  otherwise scored, for both files and directories. This is a security
  property, not a performance one first: search sweeps the whole tree a
  source names, so it is the broadest place an existence leak could open
  if a caller matched or scored an entry the requester cannot see before
  checking whether they can see it. That the check also happens to be the
  cheap thing to do early is a secondary benefit.
- **Must be cheap.** A walk visits every entry under every source; a
  closure that does non-trivial work per call (a second filesystem stat,
  a network round trip) turns a linear directory read into something far
  worse. The evaluator-backed `Allow` this contract expects
  (`UserScanSources`, below) is cheap by construction: it is a call into
  `foundation/acl-evaluator.md`'s in-memory, cache-backed `Evaluate`, not
  an I/O operation.
- **Nil means everything.** `Allow == nil` is the administrator-scoped
  form (`ScanSources`): every entry is visible, and the walker skips the
  call entirely rather than treating a nil closure as a call that always
  returns true. This matters for the cheap-call requirement above: the
  administrator path pays no per-entry closure cost at all.
- **May be called concurrently.** A parallel walker (the current one is;
  see `search/walk.go`'s worker-pool design) calls `Allow` from multiple
  goroutines at once, for the same source and for different sources in
  the same `[]ScanSource` slice, with no synchronization the walker itself
  provides. The closure must be safe for that on its own.

### The concurrency requirement on the evaluator

`UserScanSources(user)` builds its `Allow` closures as calls into the ACL
evaluator's `Evaluate` (`acl.Read`, per path, per entry). This is exactly
the concurrent-`Evaluate` guarantee `foundation/acl-evaluator.md` states
under "Thread safety": `Evaluate` is safe to call concurrently with other
`Evaluate` calls and with a `LoadFromState` reload, because every access
to the evaluator's shared state goes through its own lock and the
decision cache has its own separate lock. The search contract adds
nothing new here; it is the reason the walker's "may be called
concurrently" assumption above is safe to make against an
evaluator-backed `Allow` without the search package doing any locking of
its own. A `Allow` closure built over anything else (a future gate that is
not the ACL evaluator) inherits the same obligation: safe for concurrent
calls, or the walker's concurrency model breaks silently under load
rather than failing a test.

## Deliberate changes

1. **The core stops importing `search`.** `core/scan.go`'s
   `[]search.Source` return type is replaced by `[]ScanSource`, defined in
   `core`. This deletes the one non-store, non-kit import the core has
   (`01-package-survey.md`, cross-layer violation 1).
2. **`00-overview.md`'s dependency table drops `search`.** Already
   recorded as an amendment in `01-package-survey.md`.
3. No change to `Allow`'s calling convention, `ScanSources`'s
   administrator scoping, or `UserScanSources`'s per-entry evaluation: the
   behavior the current `core/scan.go` and `search/walk.go` already share
   carries over exactly, only the type's owning package changes.

## What is deliberately not settled here

- The search walker's internal implementation (worker pool shape, stat
  phase, ranking), the adapted input type's exact fields beyond what
  Section "The adaptation contract" requires, and how the adapter is
  wired into the search service's own entry points: `search/00-family.md`
  (`02-document-plan.md`, phase 2, search).
- The on-disk index format, segment durability, and merge invariants:
  `search/01-index-format.md`.
- Tier selection, the incremental updater, and the `OpenIndex` error
  classification decision `audit/foundation-persistence.md`'s
  `search/service` section raises: `search/02-service.md`.
- The walk/scan/build triplication (`ScanCorpus`, `Walk`, `Build`) and
  whether the rebuild keeps three independent stack-walk implementations
  or factors a shared skeleton: also `search/00-family.md`, per the
  audit's explicit call-out.

## Tests

This document specifies a type and a calling contract, not a package with
its own test suite. The tests that exercise it live where the type and
its consumers are implemented:

- `core/03-share-registry.md`'s own test list already covers
  `ScanSources` (one source per registered share, broken share's nil root
  passed through) and `UserScanSources` (per-entry evaluator filtering: a
  subtree grant admits the subtree and refuses a sibling), which is the
  core side of this contract.
- The search phase's own documents own testing the adapter (a
  `[]core.ScanSource` converts to the walker's input with every field
  correct) and the walker's actual concurrent-call behavior against
  `Allow` (many goroutines calling one `Allow` closure concurrently,
  under `-race`, matching the "may be called concurrently" clause above).
- `foundation/acl-evaluator.md`'s own concurrency smoke tests are what
  back the claim in this document that an evaluator-backed `Allow` is
  safe under concurrent walker calls; this document does not duplicate
  them.
