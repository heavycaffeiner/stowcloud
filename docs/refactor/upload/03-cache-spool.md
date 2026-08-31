# Upload 03: the cache spool

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/upload/cache.go` is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## What the cache is

A chunk normally lands straight in the part file, and that stays the
default. When the network is faster than the destination disk, chunks
queue behind it; the cache is somewhere else to put them: a directory
under the data dir an operator can mount tmpfs or NVMe at.

**The cache is a window over the file, never a copy of it.** A 200 GB
upload must work with a 128 GB cache volume, so data merges into the
destination and leaves the cache while the upload still runs. What the
cache holds at any moment is bounded by a share of its volume's free
space, measured live.

## The safe-root capability (the fake share id dies)

The old code opens the spool as `vfs.OpenShareRoot(vfs.ShareID(0), ...)`
with a comment admitting the id is a lie (audit finding 12). The rebuild
adds one vfs constructor:

```go
// In engine/infra/vfs: a rooted handle over a directory that is not a
// share. Same admission gate, same safe-path discipline, no share id.
// For server-owned scratch space; nothing resolves request paths in it.
func OpenScratchRoot(dir string, policy SharePolicy) (*ShareRoot, error)
```

The handle type stays `*ShareRoot` so every safe-path method works
unchanged; what disappears is the synthetic id and the implication that
the spool participates in the share domain. The vfs-side change is this
one constructor and its admission test; `foundation/vfs.md` gains the
constructor by amendment when this phase lands.

## Budgets and accounting

- The budget is 20% of the spool volume's **live free space**
  (`cacheFreeFraction`); the rest belongs to whatever else runs there.
- `used` is maintained by the writers and the merger, never measured per
  chunk: measuring is a directory walk per chunk and the number is only
  compared against a budget. Startup recovery (`RecoverCache`)
  re-measures once from disk.
- A chunk that does not fit is refused with `CacheFullError` carrying
  `Retry-After` material (`cacheRetryAfter`, 5 s): what it waits for is
  a disk write already in progress.
- A writer may instead wait for room, bounded by `cacheWaitMax` (30 s):
  reaching that bound means the merge is not failing loudly but not
  moving either, which is a destination problem, and the request is
  answered rather than held open forever.
- Tests may override the budget and step bounds through unexported
  atomics, because proving behavior at a real volume's bounds means
  moving that much data per test. Production never sets them.

## The merger

One goroutine per cached session in flight, spawned through `task.Go`
under the engine's merge context so `Close` stops them all.

- The merger drains **contiguous** cached data into the part file, in
  steps bounded by `mergeCopyMax` (64 MiB), so a huge chunk drains
  across several steps and the loop sees its cancellation between them.
  An unbounded step is one copy a shutdown has to wait out.
- **The wake/progress protocol.** A writer nudges the merger through a
  one-slot channel that never blocks (a nudge to a busy merger is
  absorbed; it looks again anyway). The merger closes and replaces a
  `progress` channel at the end of every round; a writer waiting for
  room subscribes to the channel **before** checking the budget, so a
  round landing between the check and the wait wakes it rather than
  being missed. This ordering is the protocol's correctness and is a
  named test.
- Merged bytes are deleted from the cache in the same round, returning
  their budget.

## Layout and recovery

- A cached session's chunks live in `.scpart-{id}.c/` inside the spool,
  named by their offset (`cacheChunkName`/`parseCacheChunkName`); the
  parse refuses foreign names, so a stray file cannot be merged as
  data.
- `RecoverCache` at startup: walk the spool, rebuild `used`, restart a
  merger per surviving session directory, and hand orphaned directories
  (no session row) to the sweep's accounting.
- Chunk writes inside the spool stage under an unlisted name, fsync,
  then rename into place (audit finding 9 verified this pattern); the
  rebuild keeps it exactly.

## The admin switch

```go
func (e *Engine) CacheAvailable() bool  // a spool exists (CacheDir was set)
func (e *Engine) CacheEnabled() bool
func (e *Engine) SetCacheEnabled(ctx context.Context, on bool) error
```

The switch is read **when a session is created and never afterwards**: a
session in flight keeps the mode it started in, because its bytes are in
one place or the other and a switch cannot move them. The setting
persists through the state store's settings aggregate.

## Deliberate changes

1. **`OpenScratchRoot` replaces the fake share id** (overview change 4).
2. **The file splits in three** (overview change 6): layout/recovery,
   the merger, the admin surface.

The budget fraction, both wait bounds, the step bound, the accounting
discipline and the wake/progress ordering are behavior-preserving
requirements.

## Tests

- The budget refuses at the bound and admits after a merge round frees
  room (test bounds via the overrides).
- The wait path: a writer waiting for room is woken by a round that
  lands between its budget check and its wait (the subscribe-first
  ordering; drive the merger by hand).
- The wait bound answers rather than hangs when the merger cannot move
  (a destination that refuses writes).
- Merge steps are bounded: a chunk larger than the step drains across
  rounds and honors cancellation between them.
- Recovery rebuilds `used` to the sum of surviving chunks; a foreign
  file in the spool is not merged and not counted as a chunk.
- The staged-rename write pattern: a crash between stage and rename
  leaves no half chunk visible to the merger.
- The switch: created-with stays; toggling affects only new sessions.
- Close stops every merger (no goroutine leak under the race detector).
- `OpenScratchRoot` refuses the same trees `OpenShareRoot` refuses
  (admission parity test), and the spool handle never appears in any
  share accessor.
