# 03: Share registry and share administration

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here: `root.go`, `share_admin.go`, `scan.go`) is
> referenced as a behavioral specification only. The new implementation is
> written completely new; nothing is copied.

Target files: `engine/core/registry.go` and `engine/core/shareadmin.go`.
Both are `//go:build linux`, like every non-pure core file.

## Purpose

The registry is the process's live map from share id to an open share root.
Every resolution, every probe and every admin screen reads it; registration,
retry, edit and removal write it. The admin surface is the durable CRUD over
the same map: a share is a row in the state store, and the registry entry is
that row made live.

The old code spreads this one concept over three files (`root.go`,
`share_admin.go`, `scan.go`), with the `shares` map and its mutex touched
from all three. The rebuild draws the boundary in two files inside one
package:

- `registry.go`: the in-memory registry. The map, the mutex, registration,
  broken entries, probing, unregistration, the accessors, and the `Roots`
  projection.
- `shareadmin.go`: everything that touches the durable share rows or serves
  the admin API. CreateShare, UpdateShare, DeleteShare, RetryShare,
  ReloadPersistedShares, the id scheme, RejectedShare, RejectionKind, and the
  scan sources.

## Spec

### Types

```go
// ShareID aliases the VFS share id; it is the only id scheme this package
// recognises a share by.
type ShareID = vfs.ShareID

type Share struct {
    ID   ShareID
    Name string
    // Host is the on-disk path. Trusted server-side configuration; it must
    // never reach a client response.
    Host             string
    Policy           vfs.SharePolicy
    TrashEnabled     bool
    SharedExternally bool
    // BrokenReason is a token naming why the share cannot be served right
    // now, or empty. Vocabulary shared with the health surface:
    // "missing", "unreadable", "unavailable", or an admission type name.
    BrokenReason string
}

// ShareDef is the internal spelling of Share, kept a distinct name so the
// config layer's registration surface is not the admin API's.
type ShareDef = Share

type ShareSpec struct {
    Name string
    Host string
}

// SharePatch distinguishes "field absent" from "field cleared" with
// pointers, which is the difference between leaving trash alone and
// disabling it.
type SharePatch struct {
    Name         *string
    Host         *string
    TrashEnabled *bool
}
```

The registry entry is unexported:

```go
// shareEntry is one registered share: its definition and the live root.
// root is nil when the share is broken; brokenErr says why, or is nil.
type shareEntry struct {
    def       ShareDef
    root      *vfs.ShareRoot
    brokenErr error
}
```

On `Core` (the struct itself is defined in `core.go`):

```go
sharesMu sync.RWMutex
shares   map[ShareID]*shareEntry
```

### Data model

- One map entry per share id, live or broken. A broken share stays in the
  map with `root == nil` and `brokenErr` set. This is deliberate: the old
  behavior of dropping an unopenable share made a disk that did not come
  back look exactly like a share somebody deleted. It was absent from the
  admin list and from every user's root list, with the only trace a line on
  the health endpoint. Broken entries stay listed, marked, with the reason
  attached.
- `BrokenReason` in the definition and `brokenErr` in the entry carry the
  same fact in two vocabularies: the reason token for screens and health,
  the error for logs and for `ShareBrokenError` (see 04-resolution.md).
- The reason tokens are produced by `RejectionKind` and are exactly:
  the admission error's filesystem type name when the failure is a
  `*vfs.AdmissionError`, otherwise `"missing"` for `vfs.ErrNotFound`,
  `"unreadable"` for `vfs.ErrDenied`, `"unavailable"` for everything else.

### registry.go behaviors

```go
func (c *Core) RegisterShare(ctx context.Context, def ShareDef) error
```

Opens `def.Host` as a share root through `vfs.RegisterShareRoot(def.ID,
def.Host, def.Policy)`, which runs the filesystem admission gate. A share
this design cannot hold its contracts on is refused here, at registration,
not at the first operation. An admission warning (`Admission.Warn`) is
logged at warn level with the share name. On success the entry is installed
with `BrokenReason` cleared. Re-registering an already registered id
replaces the entry, which is what restart reload, retry and edit all do.

```go
func (c *Core) RegisterBroken(def ShareDef, cause error)
```

Installs an entry with a nil root, `brokenErr = cause`, and
`def.BrokenReason = RejectionKind(cause)`.

```go
func (c *Core) replaceEntry(e *shareEntry)
```

Installs `e` under `e.def.ID`, then closes the root the previous entry held,
if there was one and it is not the same pointer as the new entry's root.
A failed close is logged at warn level, never returned: the replacement
already happened. Closing on replacement is what makes re-registration safe
to call repeatedly; without it, retry and edit would each leak the
descriptor the previous attempt opened.

```go
func (c *Core) ShareBroken(id ShareID) error
```

Returns the entry's `brokenErr`, nil when the share is live, and nil when
the id is not registered at all (an unregistered share is not "broken", it
is absent, and the caller that cares gets false from the accessors).

```go
func (c *Core) ProbeShares(ctx context.Context) (broke, healed []ShareDef)
```

Re-checks every registered share in both directions:

- A live share's root is probed with `root.Alive()`. On failure the share
  is moved to broken (`RegisterBroken`), and its def, with `BrokenReason`
  filled in, is appended to `broke`.
- A broken share is retried by full re-registration (`RegisterShare`), not
  by merely re-opening: the admission gate runs again, so a path that came
  back on a filesystem this server refuses stays broken. On success the
  def, with `BrokenReason` cleared, is appended to `healed`.

Both directions matter. A root whose filesystem was unmounted underneath it
keeps a descriptor that opens nothing, so without the probe the share fails
one request at a time and nothing notices. A broken share whose disk came
back has to start working again without anybody pressing anything. The
return value carries only the transitions, so the caller logs a change
rather than the steady state.

The probe iterates over a snapshot from `Shares()`; it never holds the
mutex across a probe or a registration.

```go
func (c *Core) UnregisterShare(id ShareID)
```

Removes the entry and closes its root. A broken entry has no root; the nil
check is load-bearing, because dereferencing it is what made removing a
broken share answer 500 in an earlier revision, leaving the one share
nothing would re-probe stuck as a permanent degradation. A failed close is
logged at warn level. Unregistering an unknown id is a no-op.

```go
func (c *Core) ShareRoot(id ShareID) (*vfs.ShareRoot, bool)
```

The live root, or `(nil, false)` when the id is unregistered or broken.
A broken share hands out no root; handing out a nil one would move the
failure to whoever dereferenced it.

```go
func (c *Core) Share(id ShareID) (ShareDef, bool)
func (c *Core) Shares() []ShareDef
```

`Share` returns the definition for a registered id, broken or not.
`Shares` lists every registered def, broken included, sorted ascending by
id. Both return copies; nothing hands out the entry pointer.

```go
func (c *Core) shareEntry(id ShareID) (*shareEntry, bool)
```

The package-internal entry lookup `Resolve` uses (04-resolution.md). It
lives here beside the map it reads.

```go
func (c *Core) Roots(user UserID) []acl.RootEntry
```

The user's virtual root: one entry per readable grant, labeled, as the ACL
evaluator projects it. Before projecting, `ensureHome` runs eagerly and
best-effort; a failure is logged and the listing continues (a home hiccup
must not hide the user's other shares). For each projected entry whose
share id is registered, the registry fills in `TrashEnabled`,
`SharedExternally` and `BrokenReason` from the def. A broken share stays in
the listing, carrying why; dropping it is what made a share vanish from the
browser with no explanation anywhere a user could see.

### shareadmin.go behaviors

#### The id scheme

```go
const dynamicShareIDBase = 1_000_000

func shareIDOf(rowid int64) (ShareID, error)  // base + rowid, checked into uint32
func rowIDOf(id ShareID) int64                // the inverse
```

A share's external id is its state-store rowid plus 1,000,000. The offset
must be preserved exactly: deployments that predate the single registry
minted ids in this range, and the grants, share links and cache rows that
reference those ids are still on disk. Any other mapping makes a restart
resolve old references to the wrong share or to nothing.

`shareIDOf` checks the sum into `uint32` (the `ShareID` width); an
overflowing rowid is corruption and is refused with a wrapped error, never
truncated.

#### The reserved home share id

The homes feature (11-homes-and-recent.md) registers its single share under
the fixed id `999_999`. That id sits below `dynamicShareIDBase`, and rowids
are positive, so no stored share can ever mint it: dynamic ids start at
1,000,001. The constant lives in `home.go`; this document records the
reservation because the id scheme here is what guarantees it never
collides.

#### CRUD

```go
func (c *Core) CreateShare(ctx context.Context, spec ShareSpec) (Share, error)
```

1. Refuse a duplicate name with `ErrConflict` (linear scan of `Shares()`;
   the registry is small and already sorted).
2. Insert the row through `state.DB.InsertShare` with the default symlink
   policy name (`vfs.SymlinkDeny.String()`) and the clock's nanos.
3. Mint the id with `shareIDOf`. On overflow, best-effort delete of the row
   just written, return the overflow error.
4. `RegisterShare` with `vfs.DefaultSharePolicy()`. On failure, best-effort
   delete of the row: the durable write committed, and a row with no live
   share is rolled back rather than left dangling. The registration failure
   is the returned error; the rollback's own error is discarded, because
   the failure the caller can act on is the first one.

```go
func (c *Core) UpdateShare(ctx context.Context, id ShareID, patch SharePatch) (Share, error)
```

1. Unknown id: `ErrNotFound`.
2. Apply the non-nil patch fields to a copy of the current def.
3. Persist through `state.DB.UpdateShare(rowIDOf(id), row)`, carrying name,
   host, the external flag, the trash flag, and the symlink policy name.
4. Re-register under the same id. An in-flight request holding the old
   `*vfs.ShareRoot` finishes against it; every request after sees the new
   one. If registration fails, the share is registered broken against the
   path that was just saved, and the error is returned. The row stays
   written: dropping the entry would hide the edit that caused the failure,
   and refusing the write would make a repointed path unfixable when the
   old path is also gone.

```go
func (c *Core) RetryShare(ctx context.Context, id ShareID) (Share, error)
```

Unknown id: `ErrNotFound`. Otherwise the same full registration the startup
path runs, so the admission gate runs again. On failure the share is
re-marked broken and the error returned; on success the def is returned
with `BrokenReason` cleared.

```go
func (c *Core) DeleteShare(ctx context.Context, id ShareID) error
```

Unknown id: `ErrNotFound`. Delete the durable row first, then unregister
the live entry. Grants naming the share are the admin store's cascade; a
dangling grant is default-deny anyway.

#### Startup reload

```go
func (c *Core) ReloadPersistedShares(ctx context.Context) (rejected []RejectedShare, err error)
```

Lists every row through `state.DB.ListShares` and registers each, computing
the same id `CreateShare` minted. A restart must land on the same ids the
running process used, because the cache, the grants and the links all
reference them. Per row:

- An id overflow aborts the reload with the error; it is corruption, not a
  share to skip.
- A stored symlink policy name this build cannot parse falls to the
  restrictive default with an error log line, never a refused start: the
  alternative is a share that follows links because nobody could read the
  word saying it should not.
- A registration failure registers the share broken, appends a
  `RejectedShare{Name, Kind: RejectionKind(err), Err: err}`, logs at error
  level, and continues. One share this server cannot serve is not an outage
  of every other share.

```go
type RejectedShare struct {
    Name string
    Kind string // the health surface's token; the sentence is in Err
    Err  error
}

func RejectionKind(err error) string
```

`RejectionKind` stays exported: the assembly layer registers shares too and
carries the same tokens to the health surface.

#### Scan sources

These move here from the old `scan.go`; they are read-side projections of
the registry for the search feature and sit beside the admin surface that
owns the share vocabulary.

```go
func (c *Core) ScanSources() []search.Source
```

Every registered share as a `search.Source{Share, Root: e.root, Base:
vfs.RootPath()}`. Administrator-scoped by design: the index covers every
share, so sizing it against one account's view would report a number the
built index does not match. The caller checks who is asking. A broken
entry's nil root is passed through as the old code does; the search walker
owns skipping an unopenable source.

```go
func (c *Core) UserScanSources(user UserID) []search.Source
```

The same, with an `Allow` closure per source that evaluates `acl.Read` per
entry through the evaluator. Per entry rather than per share, because a
grant can start partway down a tree; a share-level answer would either hide
a readable subtree or count an unreadable one.

```go
func (c *Core) ShareLabel(user UserID, share uint32) string
```

The label this account navigates the share under, from the grant
projection, empty when the account cannot see the share. A caller rendering
a search hit has already checked that it can; an empty answer there means
the grant went away between the search and the render.

### Concurrency rules

- `sharesMu` protects exactly the `shares` map: every read of the map takes
  `RLock`, every insert or delete takes `Lock`. Nothing else is under it.
- No I/O under the mutex. `vfs.RegisterShareRoot` (an open plus statfs plus
  statx) runs before `replaceEntry` takes the lock. `root.Alive()` and
  `root.Close()` run outside it.
- `replaceEntry` swaps the map entry under the write lock, then closes the
  old root after releasing it. Two reasons: `Close` is a syscall that must
  not serialize every registry read behind it, and a close that logs must
  not log while holding the lock. Correctness does not need the lock there;
  the old entry is already unreachable through the map, and an in-flight
  request that grabbed the old root before the swap holds its own reference,
  so its operations finish against the descriptor it has.
- `UnregisterShare` follows the same shape: delete under the lock, close
  outside it.
- `ProbeShares` never holds the lock across an iteration; it snapshots defs
  with `Shares()` and re-reads per share. A share registered or removed
  concurrently is picked up next probe, which is fine for a scheduled
  health pass.
- Accessors return copies (`ShareDef` values, cloned slices), never the
  `*shareEntry` itself, so no caller can mutate registry state outside the
  lock. The one exception is `shareEntry(id)`, which is package-internal
  and read-only by convention; `Resolve` reads `def`, `root` and
  `brokenErr` from it and writes nothing.

### Store access

All persistence goes through the existing `state.DB` surface:
`ListShares`, `InsertShare`, `UpdateShare`, `DeleteShare`, over
`state.ShareRow`. The core spells no SQL against the share table; the
schema, the statements and the scanning live in the store layer that owns
the schema. This is already true of the old `share_admin.go` and the
rebuild keeps it. (The raw grant INSERT that `homes.go` carries is a
separate problem, fixed in 11-homes-and-recent.md.)

## Rationale for the cohesion decisions

**One package.** The registry and the admin CRUD mutate the same `shares`
map under the same mutex. A package split would force either exporting the
map's internals or an interface between two things with exactly one
implementation each. Also, `Resolve` reads `shareEntry` directly, and
`Resolved`'s unexported-fields guarantee (00-overview.md) pins the whole
domain into one package anyway.

**Two files.** They change for different reasons. `registry.go` is runtime
state: what is open, what is broken, what a probe found. `shareadmin.go` is
the durable definition and the operator's API: rows, ids, create and edit
and delete, the startup reload, and the projections the admin-adjacent
search feature reads. A reader asking "why does this share answer broken"
opens `registry.go`; a reader asking "where do share ids come from" opens
`shareadmin.go`; neither has to read the other end to end.

**Scan sources beside the admin surface.** `ScanSources`,
`UserScanSources` and `ShareLabel` are consumed by the search feature's
admin-facing sizing and indexing paths. They read the registry but do not
manage it, and in the old tree they were a third file touching the map. In
the new tree they are ordinary read-side functions in `shareadmin.go`,
using the same locked read pattern as the accessors.

**warn and errf in core.go.** The `warn` helper (log a failure that must
not fail the operation) and `errf` (wrap a sentinel with context) are used
across the whole package, not by the registry specifically. They move to
`core.go`, the file that owns construction and logging.

## Deliberate changes

No behavioral changes. The observable behavior of registration, breaking,
probing, retry, CRUD, reload, id minting, rejection tokens, root listing
and scan sources is preserved exactly.

Cohesion moves (file placement only):

- `ScanSources`, `UserScanSources` and `ShareLabel` move from the old
  `scan.go` into `shareadmin.go`.
- The `warn` helper and `errf` move from the old `root.go` into `core.go`.
- Package construction (`Core`, `Options`, `New`), `NewInstanceID` and the
  logging setup move from the old `root.go` into `core.go`; they are not
  part of this document's spec.

## Tests

New tests, written fresh against the new API, covering at least what the
old suite asserts (`brokenshare_linux_test.go` and the registration paths
exercised by `resolve_test.go` and others):

1. **Register and accessors.** A registered share appears in `Share`,
   `Shares` (sorted by id), and `ShareRoot`; `ShareBroken` is nil.
2. **Broken registration.** `RegisterBroken` yields: listed in `Shares`
   with the reason token, `ShareRoot` returns false, `ShareBroken` returns
   the cause. Reason tokens: a missing path is `"missing"`, an unreadable
   one `"unreadable"`, an admission refusal carries the filesystem type
   name, anything else `"unavailable"`.
3. **Replacement closes the old root.** Registering the same id twice
   closes the first root: the process's open descriptor count does not
   grow across N re-registrations of the same share. Replacing an entry
   with itself (same root pointer) does not close it.
4. **Probe breaks.** A live share whose directory is removed or unmounted
   moves to broken on `ProbeShares` and is reported in `broke` with its
   reason. (The unmount case is the privileged subtest the old
   `TestAnUnmountedShareGoesBrokenAndComesBack` runs; keep the skip when
   the environment cannot mount.)
5. **Probe heals.** A broken share whose path reappears moves back to live
   and is reported in `healed`, and the probe returns only transitions:
   a second probe with nothing changed returns two empty slices.
6. **Probe re-runs admission.** A broken share whose path comes back on a
   refused filesystem stays broken.
7. **Unregister.** Removes the entry and closes the root; unregistering a
   broken share does not panic and removes the entry; unregistering an
   unknown id is a no-op.
8. **Roots projection.** A user's `Roots` carries `TrashEnabled`,
   `SharedExternally` and `BrokenReason` from the registry; a broken share
   stays listed with its reason (the old
   `TestAShareWithAMissingPathIsStillListed`).
9. **CreateShare.** Mints id rowid + 1,000,000; refuses a duplicate name
   with `ErrConflict`; rolls the row back when registration fails, so a
   subsequent `ReloadPersistedShares` does not resurrect it.
10. **UpdateShare.** Patch semantics (nil leaves alone, pointer sets);
    persists then re-registers; a failing new host leaves the share listed
    broken and returns the error, and the row holds the new host.
11. **RetryShare.** Heals a fixed path; re-refuses a still-broken one;
    `ErrNotFound` for an unknown id.
12. **DeleteShare.** Removes row and entry; works on a broken share (the
    old `TestABrokenShareCanBeRemoved`); `ErrNotFound` for an unknown id.
13. **ReloadPersistedShares.** Round-trips ids across a simulated restart
    (create, reload into a fresh Core over the same store, same ids); an
    unparseable stored symlink policy falls to deny and still serves; an
    unopenable row is registered broken, reported in `rejected`, and does
    not stop the other rows.
14. **Id scheme edge.** `shareIDOf` refuses a rowid that would overflow
    uint32; `rowIDOf(shareIDOf(n)) == n` for representative n.
15. **Home id reservation.** `shareIDOf(1)` is 1,000,001 and every minted
    id is strictly greater than 999,999.
16. **Scan sources.** `ScanSources` returns one source per registered
    share; `UserScanSources` filters per entry through the evaluator
    (a grant on a subtree admits the subtree and refuses a sibling);
    `ShareLabel` returns the grant label and empty for an invisible share.
17. **Concurrency smoke.** Parallel `Shares`/`ShareRoot` reads racing
    `RegisterShare`/`UnregisterShare` under `-race`.
