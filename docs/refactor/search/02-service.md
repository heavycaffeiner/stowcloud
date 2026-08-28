# Search 02: the service

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/search/service` is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## Shape

Four files, four jobs, as the old package (its audit found the layering
clean):

| File | Job |
| --- | --- |
| `service.go` | tier selection, the query gate, storage classes |
| `build.go` | full ingest: walk the sources into a fresh base |
| `update.go` | watcher-driven incremental sync |
| `open.go` | startup open with graceful degradation |

Target `engine/service/search/svc`. Sources arrive as `[]core.ScanSource`
through the adapter (00); the ACL-filtered variant (`UserScanSources`)
is what a user query passes, and the admin variant is what build and
update pass.

## Tier selection and the gate

```go
type StorageClass int // SSD, HDD
func (s StorageClass) Concurrency() int
func (s StorageClass) Deadline() time.Duration
func (s StorageClass) Threads(cpus int) int

type QueryOptions struct {
    Query string
    Limit int
    Scope string
    WithMetadata bool // resolve size/time only for survivors
}

type Results struct{ Hits []Hit; Tier Tier; ... }

func (s *Service) Query(ctx context.Context, sources []search.Source, opt QueryOptions) (Results, error)
```

- The storage class moves **both** numbers (walk concurrency and the
  deadline), because a walk on NVMe and a walk on a spinning disk are
  different machines; one knob moving two numbers keeps them consistent.
- The query length is bounded (`limits.SearchQueryBytes`); the limit is
  clamped to `limits.SearchResults`.
- **The concurrency gate is non-blocking**: a full service answers
  `ErrBusy` at the cost of a channel send, before any directory read.
  Backpressure, never queueing.
- The index answers when it can; `MustFallBack` routes to the walk, and
  `Results.Tier` says which tier answered, so a caller can surface
  "this was a full scan" honestly.
- **The `pathUnder` invariant** (named, normative): any hit sourced
  from persisted index data is revalidated against the source's current
  base (`JoinExisting` + `Under`) before it is trusted. Index rows are
  yesterday's filesystem; today's decides.

## Build

`Build` walks every source (the third walk, deliberately its own: it
streams into segment writes with its own batching) into a fresh base
under the merge gate. It honors the entry ceiling and records
incompleteness so the index reports `FallbackIncomplete` rather than
silent shortness.

## Updates and the compensating chain

`Updater.Offer` accepts watcher events into a bounded queue and
**silently drops on overflow**, logging a warning. This is acceptable
only because of the compensating chain, which is load-bearing and must
survive the rebuild intact:

1. a dropped update costs a stale index entry, nothing more;
2. the walk tier still finds the file (the index is never the only
   answer);
3. stat revalidation hides a deleted file the index still names.

Removing any link turns the drop into real data loss. The chain is
stated here so no future change removes a link without noticing what it
held up.

`reconcile` uses the same `pathUnder` revalidation as the query path.

## Open: the degradation ladder (change)

The old `OpenIndex` folds every failure into one degrade-to-nil path,
distinguishing corrupt from other errors only in the log line (audit
finding 5: a permission error reads as "needs a rebuild"). The rebuild
classifies:

| Condition | Behavior |
| --- | --- |
| no directory yet | quiet nil: the index was never built; not a warning |
| corrupt (header, checksum) | warn "disabled until rebuilt", nil; leave the evidence on disk |
| anything else (permissions, I/O) | warn "could not open, may recover", nil; **do not** suggest a rebuild |

All three still degrade to the walk: a broken cache costs speed, never
answers. What changes is only that the operator reading the log can tell
which of the three worlds they are in.

## Deliberate changes

1. **The open ladder above** (audit finding 5).
2. **Sources arrive through the core adapter** (00; the inversion).
3. Nothing else: the gate, the storage classes, the drop-with-chain and
   the revalidation are behavior-preserving requirements.

## Tests

- Tier: a short query walks; a trigram query with an index answers from
  it; a pruned or incomplete index falls back; `Results.Tier` reports
  truthfully.
- The gate: a saturated service answers `ErrBusy` without touching a
  directory (fixture source that counts reads).
- `pathUnder`: an index hit whose path no longer joins under the base
  is dropped; a hit for a deleted file is dropped by the stat; a hit
  outside the user's `Allow` closure never surfaces.
- Build then query round-trip on a real corpus; the ceiling produces
  `FallbackIncomplete`.
- Updates: create/rename/delete events land; a full queue drops without
  blocking the watcher (and the walk still finds the file: the chain
  test).
- Open: the three ladder cases produce their three distinct behaviors
  (fixtures: absent dir, corrupted header, permission-denied dir).
- The storage class moves both numbers together.
