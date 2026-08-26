# Foundation: ACL evaluator

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/acl` (`eval.go`, `grant.go`, `perms.go`, `cache.go`) is
> referenced as a behavioral specification only. The new implementation is
> written completely new; nothing is copied. The SQL half of the old package
> (`store.go`, `sql.go`, `grant_storage.go`) is out of scope for this
> document: it moves to the grant aggregate in `store/state`
> (`foundation/state.md`, written in parallel by another agent). This
> document specifies only the loading contract the evaluator accepts, in
> its own domain types; converting the state aggregate's row shapes into
> those types is core-side orchestration, not this document's or
> `state.md`'s concern.

Target directory: `engine/service/acl/`. Service layer, per the target architecture
in `01-package-survey.md`. No non-stdlib import; in particular, no
`database/sql` import anywhere in this package.

## Purpose

The evaluator answers one question: what permission bits does a user hold
at a virtual path. It is the security decision every read, write, listing
and admin screen depends on, and `core/04-resolution.md`'s `Resolve` is
built directly on three of its methods (`Evaluate`, `Effective`, `Roots`).
This document is written so that dependency is exact: whatever `Resolve`
assumes about the evaluator's shape and guarantees, this document states.

Grant persistence (creating, updating, deleting a grant row) is not here.
The package this document specifies never writes to a database and never
imports one; it holds an in-memory grant and membership table, refreshed
wholesale from already-converted domain values the core hands it, and
answers permission questions against that table (`01-package-survey.md`,
`acl` verdict; `audit/foundation-persistence.md`, acl finding 1).

## Spec

### Perms

```go
type Perms uint16

const (
    Read     Perms = 1 << iota
    Write
    Create
    Delete
    Rename
    Move
    Share
    Download
)

func (p Perms) Has(want Perms) bool        // every bit in want is set
func (p Perms) Intersects(want Perms) bool // any bit in want is set
func (p Perms) Remove(other Perms) Perms   // clears the bits in other
func (p Perms) IsEmpty() bool
func (p Perms) String() string             // "read/write/...", "-" for zero
```

Eight bits, and two splits a reimplementation collapses by accident stay
separate: DOWNLOAD is not READ, so a view-only grant does not also hand out
the bytes; MOVE is not RENAME, so a grant scoped to one subtree does not
let its holder carry a file out of it. `orderedBits()` fixes one rendering
and probing order (Read, Write, Create, Delete, Rename, Move, Share,
Download) so `String()`, `Effective` and the admin surface's permission
list are the same order everywhere.

`PermByName`/`NamedPerms` round-trip a bit against its name for the admin
API, which receives permission names from a client. An unknown name is
refused, not ignored: silently dropping an unrecognized permission from a
grant a client asked for would store something weaker than what the screen
shows.

### Path

```go
type Path struct{ /* unexported components */ }

func NewPath(comps ...string) Path
func ParsePath(s string) Path       // "/"-separated, stored spelling
func (p Path) Components() []string
func (p Path) Len() int
func (p Path) Name() string         // last component, empty at root
func (p Path) String() string       // "/" + joined components
func (p Path) IsPrefixOf(q Path) bool
```

`Path` is the ACL package's own share-relative path vocabulary, built from
already-validated components; it does not touch a filesystem or repeat
`vfs`'s validation. The core crosses from its own `vfs.SafePath` into this
type at exactly one point (`aclPath` in `core/04-resolution.md`), and
crosses back nowhere: the evaluator never produces a `vfs` type.

### Grant

```go
type Grant struct {
    ID      int64
    User    int64  // 0 when Group is set
    Group   int64  // 0 when User is set
    Share   int64
    Subpath Path
    Allow   Perms
    Deny    Perms
    Inherit bool
    Label   string
    CreatedNs int64
}
```

Exactly one of `User` and `Group` is set; the write side (the grant
aggregate) enforces this before a row exists, so the evaluator does not
re-validate it. A grant that matches neither (both zero, which the write
side refuses) matches nobody: `grantMatches` checks `User != 0` first and
falls through to a group-membership scan otherwise, and a `Group` of zero
matches no membership row, so a malformed grant is inert rather than
wildly permissive. Fail closed is the default without extra code, not a
defect to guard against separately.

`Inherit` decides what the grant covers, not whether it applies at all:

- `Inherit == true`: the grant applies to its `Subpath` and to every path
  under it (`Subpath.IsPrefixOf(path)`).
- `Inherit == false`: the grant applies only to the path exactly equal to
  its `Subpath`, never to anything beneath it (`subpathEquals`, prefix plus
  equal length). A non-inheriting grant at `a/b` never contributes when
  evaluating `a/b/c`.

### Vpath and Decision

```go
type Vpath struct {
    Share int64
    Path  Path
}

type Decision struct {
    Allowed bool
    By      int64 // the deciding grant's id, 0 for the default-deny fallthrough
}
```

`Vpath` is the evaluator's own share-plus-path pair; `user`, `share` and
grant ids stay plain `int64` throughout this package, not the core's
`UserID`/`ShareID` types. This is deliberate: the core imports the
evaluator, never the reverse, so the evaluator cannot spell a core type
without creating the cycle. The core narrows and widens at its own
boundary (`aclPath`, and `ShareID` narrowing in `core/03-share-registry.md`).

`Decision.By` is carried on every answer, allowed or denied, not only when
a caller happens to want it. A denial that cannot name the grant that
produced it is undebuggable and unauditable: the API layer can say which
rule refused a request, and the audit log (once it exists) can record the
same id. `By == 0` names the one case with no deciding grant at all: the
path had no matching grant, or every level fell through, so the answer was
the algorithm's own default rather than any single rule's decision.

### The evaluation algorithm

```go
func (e *Evaluator) Evaluate(user int64, v Vpath, want Perms) Decision
```

The algorithm, over `candidates`, the grants that name `user` (directly, or
through group membership) on `v.Share` and apply to `v.Path` under the
`Inherit` rule above:

1. If there are no candidates, the answer is `Decision{Allowed: false, By:
   0}` immediately: no grant means no opinion, which composes with the
   existence rule (`ErrNotFound`, not `ErrDenied`, for a path outside every
   grant) at the layer above.
2. Walk depth from `v.Path.Len()` down to `0` (the share root). At each
   depth, `level` is the subset of `candidates` whose `Subpath.Len()`
   equals that depth (an inheriting grant's depth is its own subpath
   length, not the evaluated path's).
3. Within one non-empty level, in this order:
   - Any grant whose `Deny` intersects the remaining `want` decides the
     answer: `Decision{Allowed: false, By: grant.ID}`. **Same-depth DENY
     beats ALLOW**, unconditionally, before any ALLOW at that depth is
     considered.
   - Otherwise, the first grant (in grant-table order) whose `Allow` has
     every bit of `want` decides: `Decision{Allowed: true, By: grant.ID}`.
   - Otherwise, the first grant (in grant-table order) whose `Allow`
     intersects `want` at all removes its bits from `want`
     (`want = want.Remove(g.Allow)`). If `want` is now empty, that grant
     decides: `Decision{Allowed: true, By: g.ID}`. If bits remain, the
     search continues to the next shallower depth with the reduced `want`.
     **This is the one grant that contributes at this depth**: a second
     grant at the same depth that could cover the remaining bits is not
     consulted before the loop moves to a shallower level. Composition
     happens **across depths**, not within one depth with more than one
     partially-covering ALLOW grant (see the table test below, which pins
     this down explicitly).
4. If the loop reaches depth 0 with `want` still non-empty, or every level
   was empty, the answer is the default: `Decision{Allowed: false, By: 0}`.

This gives the properties the task names directly: **same-depth DENY beats
ALLOW** (step 3, first bullet, unconditional at that depth); **deeper
ALLOW beats shallower DENY** (a depth where an ALLOW fully or partially
satisfies `want` returns before the walk ever reaches a shallower depth's
DENY); **no principal-kind priority** (a user grant and a group grant at
the same depth are ordinary members of `level`; only depth and, within a
depth, grant-table order decide, never whether the grant names a user or a
group); **default deny** (step 1 and step 4); and **composition across
depths**, illustrated by the canonical example: READ granted at `/a/b` and
WRITE granted at `/a` together answer `Effective` at `/a/b` with
READ|WRITE, because the depth-`2` level supplies READ and reduces `want`
to WRITE, which the depth-`1` level then supplies.

`Evaluate` is memoized (see Cache below); `evaluate` (unexported) is the
uncached algorithm above.

### Effective

```go
func (e *Evaluator) Effective(user int64, v Vpath) Perms
```

The maximal bit set the user holds at `v`: the bitwise OR of `Evaluate`
called once per single bit, in the fixed `orderedBits()` order. This is
what `Resolved.Perms()` in `core/04-resolution.md` is filled from
(`c.acl.Effective(user, vpath)`, step 9 of `Resolve`), and it is why a
consuming operation never has to call `Evaluate` again for a bit it
already knows the answer to: `Resolved` carries the full set, and
`Resolved.Require` checks against it directly.

Probing bit by bit rather than evaluating `want` as one multi-bit value is
what makes composition across depths observable at all: a single
multi-bit `Evaluate` call stops at the first level that satisfies part of
`want` and only continues for what remains, so a caller that wants the
*complete* effective set needs one call per bit to see every contributing
grant. `Effective` is that caller.

### RootEntry and Roots

```go
type RootEntry struct {
    Label   string
    Share   int64
    Subpath Path
    Perms   Perms

    // Decorated by the core, not by this package. See below.
    TrashEnabled     bool
    SharedExternally bool
    BrokenReason     string
}

func (e *Evaluator) Roots(user int64) []RootEntry
```

`Roots` is the projection `core/04-resolution.md` step 3 matches a client's
label against, and what `core/03-share-registry.md`'s `Core.Roots` wraps.
One entry per grant record that names `user` (directly or through group
membership) and whose `Allow` includes `Read`, labeled with the grant's
own label, or the subpath's last component, or `share-<id>` when neither
is available. A label collision (two grants that resolve to the same base
label) is disambiguated with a " (2)", " (3)" suffix in encounter order,
so two same-named shares stay distinct in a client's root listing.

`Perms` on each entry is the full effective set at the grant's own subpath
(`effectiveLocked`, the internal, already-locked twin of `Effective`), not
merely the raw `Allow` bits of the grant that earned the entry its listing.
This means an entry can appear in `Roots` (because some grant record
explicitly allows READ there) while its `Perms` lacks READ (because a
deeper or same-depth DENY elsewhere cancels it in the full evaluation).
`Roots` answers "which subtrees does this account have an explicit
read-granting rule for", not "what can this account actually do at each
one"; the true answer to the second question is `Perms`, and the true gate
for any operation is still `Evaluate`/`Resolve`, never membership in this
list. This is not a defect: the label lookup in `Resolve` only decides
*which share the path names*, and step 8 of `Resolve` re-evaluates the
actual requested permission afterward.

`TrashEnabled`, `SharedExternally` and `BrokenReason` are zero-valued by
this package. The core, which owns the share registry, fills them in per
entry after calling `Roots` (`core/03-share-registry.md`'s `Core.Roots`
method): the evaluator has no share registry to read them from, and
re-deriving them here would need this package to depend on the registry,
which is a layering violation the survey does not ask for. The fields are
carried on `RootEntry` rather than looked up separately so the core's
decoration is one pass over the slice `Roots` returned, not a second walk
of the grant table per entry.

### The in-memory store and reload discipline

```go
type Evaluator struct {
    // unexported: mu, grants []Grant, membership map[int64][]int64,
    // gen int64, cache *decisionCache
}

func NewEvaluator() *Evaluator
func (e *Evaluator) MembershipOf(user int64) []int64
```

One mutex (`sync.RWMutex`) protects `grants`, `membership` and `gen`
together; a decision cache with its own, separate mutex sits beside it
(see Cache). Every read (`matching`, `grantMatches`, `Roots`,
`MembershipOf`) takes the read lock; every replace takes the write lock.

#### Loading contract

The evaluator does not know SQL exists, and it does not define a row type
for either grants or memberships: it takes its own domain types directly.
`Grant` is the struct already defined above (Grant). `Membership` is a
small pair the evaluator defines for itself:

```go
// Membership is one user-to-group edge, in the evaluator's own domain
// shape.
type Membership struct {
    User  int64
    Group int64
}

// LoadFromState replaces the evaluator's grants and memberships from
// already-converted domain values, as one atomic swap: both new tables
// are installed under a single lock acquisition and the generation
// counter is bumped exactly once. A caller (the core, or whatever
// assembles a reload) never observes new grants paired with old
// memberships, or the reverse, under any generation the cache can have
// cached against.
func (e *Evaluator) LoadFromState(grants []Grant, memberships []Membership) error
```

The evaluator never sees the store's row shape (`state.GrantRow`, with its
`User *int64` nil convention and `uint16` bit widths). Converting a
`state.GrantRow` into an `acl.Grant`, and a `state.MembershipRow` into an
`acl.Membership`, is core-side orchestration: the state package owns the
row shape, the ACL package owns the domain shape, and the core converts
between them (`core/11-homes-and-recent.md`). This package's only
remaining knowledge of persistence, after that conversion happens
upstream of it, is nothing at all; `LoadFromState` takes values already in
its own vocabulary.

`LoadFromState` installs the given `grants` and converts each
`Membership` into the internal `map[int64][]int64`, then installs both
under one `Lock`/`Unlock` and increments `gen` once, inside that same
critical section. This differs from the current code's two-step
`ReplaceGrants` then `SetMemberships` (each its own lock and its own
`bump`), which creates a real, if narrow, window where a concurrent
`Evaluate` can observe the new grants together with the old memberships
under a valid intermediate generation. The doc comment on the current
`LoadFromState` already states the intended guarantee ("membership and
grants move as one load so the two can never be read a generation apart");
this document makes the implementation match the stated guarantee exactly,
rather than approximate it across two public swaps.

`ReplaceGrants(grants []Grant)` and `SetMemberships(m map[int64][]int64)`
stay as lower-level primitives on the type, used directly by the
algorithm's own unit tests (which want to set up a grant table without
constructing rows) and by anything that only needs to change one half.
Production reload, from the core, goes through `LoadFromState` and gets
the atomic guarantee; a caller that calls `ReplaceGrants` and
`SetMemberships` separately gets the same two-generation window this
document deliberately closes for `LoadFromState`, and that is an accepted,
documented cost of using the split primitives directly.

`LoadFromState` carries no validation of its own beyond installing what it
is given: a `Grant` with `Allow`/`Deny` bits outside the eight defined, or
a malformed `Subpath`, would only occur from a conversion the core got
wrong or a row the grant aggregate itself did not produce correctly, since
the write side validates on `PersistGrant`
(`audit/foundation-persistence.md`, acl finding 1 area; the write-time
checks move to `foundation/state.md`'s grant aggregate). This package
trusts what it is handed, the same way `Resolve` trusts a narrowed
`ShareID` came from a real row.

#### Thread safety

`Evaluate`, `Effective`, `Roots` and `MembershipOf` are safe to call
concurrently with each other and with a `LoadFromState` reload. Every
access to `grants`/`membership`/`gen` is under `e.mu`; no method holds the
lock across a call back into another method that also takes it (`Roots`
takes the read lock once for its whole walk and calls the already-locked
`effectiveLocked`, not the public `Effective`, to avoid a nested-lock
deadlock). A reload is a small critical section (populate two Go values,
increment an integer) with no I/O inside it: the fetch of rows from the
state store happens entirely before `LoadFromState` is called, so the
lock is never held across a database read.

A `Decision` returned mid-reload reflects either the state entirely before
the reload or the state entirely after it, never a mix: the generation
counter it was computed and cached under identifies which.

### Cache (cache.go)

```go
type decisionCache struct { /* bounded, FIFO-evicted, gen-aware */ }

func newDecisionCache(capacity int) *decisionCache
func (c *decisionCache) lookup(k cacheKey, gen int64) (bool, Decision)
func (c *decisionCache) store(k cacheKey, gen int64, d Decision)
```

What is memoized: `Decision` values, keyed by `(user, share, path string,
want)`. Nothing else in the package is cached; `Effective` and `Roots`
compose from repeated `Evaluate`/`evaluate` calls and get the cache's
benefit for free through those calls.

Invalidation is generational, not swept: every cache entry carries the
`gen` value current at the time it was computed, and `lookup` treats an
entry whose `gen` does not match the evaluator's current `gen` as a miss.
A `LoadFromState` reload never walks the cache to evict stale entries; it
only bumps `gen`, which makes every entry computed under an earlier
generation unreachable by the lookup path without deleting it. The bound
(`4096` entries, FIFO eviction on insert past capacity) exists
independently of invalidation: it is a memory bound against a client that
can generate many distinct paths, holding regardless of how often grants
change.

The cache's own mutex is separate from the evaluator's `e.mu`, so a
`lookup`/`store` never blocks a concurrent `LoadFromState`'s critical
section and vice versa; the two are correct together because `lookup`
only trusts an entry whose `gen` matches a `gen` value the caller already
read from `e.mu` under lock (`Evaluate`'s `genValue()`), never a value the
cache invents on its own.

## Rationale

- **Pure by construction, not by convention.** The package holds no I/O
  type anywhere in its exported or unexported surface: `Grant` and
  `Membership` are plain structs with primitive fields, not a database
  row scanner and not a `store/state` type. This is what makes "no
  `database/sql` import" a property of the whole dependency graph, not
  just of the files that happen not to call it today
  (`01-package-survey.md`'s `acl` verdict; `audit/foundation-persistence.md`
  acl finding 1).
- **`int64` throughout, not the core's typed ids.** Keeping `user`,
  `share` and grant/group ids as plain `int64` is what keeps the import
  direction correct (core imports acl, never the reverse) without forcing
  either side to define a shared id package purely for this crossing.
- **Same-depth-one-grant composition is preserved as specified, not
  "fixed".** The algorithm's real behavior (only the first matching ALLOW
  at a given depth reduces `want`; a second same-depth grant covering the
  remainder is not consulted before moving to a shallower depth) is kept
  exactly, because changing it would be a behavioral change to a
  security-critical evaluator with no audit finding calling it a defect.
  It is documented explicitly and pinned with a test instead of silently
  carried or silently altered.
- **`LoadFromState`'s atomicity is tightened, not preserved verbatim.**
  The task and the current code's own doc comment both state the
  guarantee "grants and memberships move as one load, never a generation
  apart." The current two-call implementation (`ReplaceGrants` then
  `SetMemberships`, each its own lock and `bump`) does not fully deliver
  that guarantee; the rebuild's `LoadFromState` does, under one lock and
  one `bump`, closing the gap between the stated contract and the
  implementation.

## Deliberate changes

1. **The SQL half leaves the package.** `store.go`, `sql.go` and
   `grant_storage.go` (grant CRUD, the SQL constants, `readGrants` and
   `readMemberships`) move to the grant aggregate in `store/state`, per
   `01-package-survey.md`'s `acl` verdict and confirmed by
   `audit/foundation-persistence.md`'s acl finding 1. What remains
   (`eval.go`, `grant.go`, `perms.go`, `cache.go`) is exactly the survey's
   "pure and dependency-free" half.
2. **The loading contract now takes domain values, not `database/sql`
   rows and not row-shaped structs of its own.** The old
   `LoadFromState(ctx, db readDB)` took a `QueryContext` interface and ran
   two `SELECT`s itself. The new `LoadFromState(grants []Grant,
   memberships []Membership) error` takes already-converted values in the
   evaluator's own vocabulary, so this package's only remaining knowledge
   of persistence is nothing: it defines no row shape at all. Converting
   the state store's row shape (`state.GrantRow`, `state.MembershipRow`)
   into these domain values is out of scope for this document; it is
   core-side orchestration, specified wherever the core's reload path is
   documented (`core/11-homes-and-recent.md` already describes one
   caller: after `PersistGrant`, the core reloads the evaluator).
3. **`LoadFromState` is a single atomic swap.** One lock acquisition, one
   `gen` bump, closing the two-generation window the current two-call
   implementation leaves open (see Rationale). `ReplaceGrants` and
   `SetMemberships` remain as separate primitives for direct test setup
   and partial updates, with the narrower guarantee that entails.
4. **Grant/Deny narrowing on load is explicit, not a `//nolint`-annotated
   cast.** The current `readGrants` does `Perms(allow)` with a comment
   trusting the stored value is one of the eight bits. The rebuild states
   the same trust explicitly as part of the loading contract (see Loading
   contract above) rather than leaving it as an inline suppression; the
   underlying behavior (trust the row, no independent revalidation) is
   unchanged, since the aggregate already validates on write.
5. **No behavioral change to the algorithm, `Effective`, `Roots`'s
   labeling and disambiguation, or the cache's memoization and eviction
   policy.** Every observable answer `Evaluate`/`Effective`/`Roots`
   produces for a given grant table is the same as today's.

## Tests

Algorithm table tests (`evaluate`/`Evaluate`), each a small grant table and
an assertion on `Decision`:

1. **Same-depth DENY beats ALLOW.** A DENY and an ALLOW of the same bit at
   the same depth: DENY wins regardless of grant-table order.
2. **Deeper ALLOW beats shallower DENY.** An ALLOW at depth 2 and a DENY of
   the same bit at depth 1 (or the root): the deeper ALLOW answers first
   and the shallower DENY is never reached.
3. **No principal-kind priority.** A user grant and a group grant at the
   same depth, one DENY and one ALLOW of the same bit, in both orderings
   of which is the user grant: the outcome depends only on which is DENY,
   never on which is the user grant.
4. **Ties broken by depth alone.** Two ALLOW grants at different depths,
   neither denying anything: the deeper one decides (`Decision.By` names
   the deeper grant), even when the shallower grant would also satisfy
   `want`.
5. **Default deny.** A path with no matching grant returns
   `Decision{Allowed: false, By: 0}`, distinguished from a denied path by
   `By`.
6. **Composition across depths.** READ at `/a/b`, WRITE at `/a`: `Effective`
   at `/a/b/c` includes both bits, and `Evaluate(..., Read|Write)` also
   succeeds (composed across the two `Evaluate(bit)` calls `Effective`
   makes, and directly through the multi-bit algorithm's own depth walk).
7. **Same-depth partial-allow does not combine.** Two grants at the exact
   same depth, one allowing READ and the other allowing WRITE, nothing
   else in the table: `Evaluate(..., Read|Write)` is denied by default
   (`By == 0`), because only the first matching grant at that depth
   reduces `want` and the other is never consulted; `Evaluate(...,
   Read)` alone and `Evaluate(..., Write)` alone are each allowed. This
   pins the exact rule down against a future reimplementation combining
   same-depth grants by accident.
8. **Inherit true vs false.** A non-inheriting grant at `a/b` matches
   `Evaluate` at exactly `a/b` and not at `a/b/c`; an inheriting grant at
   `a/b` matches both.
9. **Group membership.** A group grant applies to a member and not to a
   non-member; membership changes take effect only after a reload (no
   live mutation of the membership map outside `LoadFromState`/
   `SetMemberships`).
10. **Existence-rule composition.** A path outside every grant returns
    zero `Effective` and an unallowed `Evaluate` for every bit, matching
    the "no grant is the same answer as no path" rule the layer above
    relies on for its 404-not-403 behavior.

`Roots`:

11. Label from the grant's own label, then subpath name, then
    `share-<id>`, in that priority.
12. Label collision gets " (2)", " (3)" in encounter order.
13. A grant with `Deny` only and no `Allow` contributes no root entry.
14. An entry's `Perms` can lack a bit the entry's own triggering grant's
    raw `Allow` included, when a same-depth or deeper DENY cancels it in
    the full evaluation (the documented Roots-versus-Perms distinction
    above), while the entry itself is still listed.

Reload atomicity:

15. `LoadFromState` with a fresh grant table and a fresh membership table:
    after the call, `Evaluate` sees exactly the new state, never a mix of
    old and new.
16. A concurrent `Evaluate` racing `LoadFromState` (built with `-race` and
    interleaved by a test hook or many goroutines) never observes new
    grants paired with old memberships or the reverse: every observed
    `Decision` is explainable entirely from the state before the call or
    entirely from the state after it. This is the test that the closed
    two-generation window (Deliberate change 3) actually stays closed.
17. Cache correctness across reload: a `Decision` cached before
    `LoadFromState` is not returned after it, even for the identical
    `(user, share, path, want)` key, when the answer would differ under
    the new table; a decision that happens to be identical under the new
    table is recomputed, not reused past the generation boundary (the
    test asserts the recompute happened, e.g. via a call counter on a
    wrapped evaluate path, not just that the answer matches).

Concurrency smoke:

18. N goroutines calling `Evaluate`/`Effective`/`Roots` against a
    stationary grant table, run under `-race`, with no data race and
    identical answers to a single-goroutine run.
19. N goroutines calling `Evaluate` while one goroutine repeatedly calls
    `LoadFromState` with alternating grant tables, under `-race`: no data
    race, and every returned `Decision` is valid for one of the two
    tables (never a spliced answer).

`Perms`/`Path`/`Grant` value tests:

20. `Has`/`Intersects`/`Remove`/`IsEmpty` table over representative bit
    combinations.
21. `Read != Download` and `Rename != Move` (the two splits that must not
    collapse).
22. `ParsePath`/`Path.String()` round-trip, including the empty-path and
    single-slash cases folding to the root.
23. `PermByName`/`NamedPerms` round-trip every bit; an unknown name is
    refused, not silently dropped.
