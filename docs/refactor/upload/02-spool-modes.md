# Upload 02: spool modes, intervals, assembly

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/upload` (here `intervals.go`, `spool.go`, the write half
> of `engine.go`, the assembly half of `finalize.go`) is referenced as a
> behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## The two absolute invariants

Stated at the top because everything below serves them:

1. **A partial upload can never appear at the destination.** Publication
   is a rename, never a stream into the destination.
2. **No staging file is ever read back and rewritten.** Assembly is
   `copy_file_range`, never a userspace copy loop.

## The two modes

```go
const (
    SpoolOffsetAddressed SpoolMode = iota // chunks carry offsets; pwrite into the part file
    SpoolNameOrdered                      // chunks carry names; one file each, assembled at finalize
)
```

The names describe what a mode does, never which protocol wants it.
Offset-addressed serves TUS and anything that can speak ranges;
name-ordered serves the chunked protocol that carries only ordinals.

## The interval set

```go
type Range struct{ Lo, Hi uint64 } // half-open

type IntervalSet struct{ /* sorted, coalescing runs */ }
func NewIntervalSet() *IntervalSet
func FullIntervalSet(length uint64) *IntervalSet
func LoadIntervalSet(rows []Range) (*IntervalSet, error)
func (s *IntervalSet) Insert(lo, hi uint64) error
func (s *IntervalSet) ContiguousPrefix() uint64
func (s *IntervalSet) IsComplete(length uint64) bool
func (s *IntervalSet) Missing(length uint64) []Range
func (s *IntervalSet) Received() uint64
func (s *IntervalSet) Count() int
func (s *IntervalSet) Runs() []Range
```

- The set is **persisted, never derived from the part file's size**: a
  sparse file's size says where the last write landed, not what is in
  it. It answers both "where do I resume" (`ContiguousPrefix`: the end
  of the run starting at zero, or zero) and "is this file finished"
  (`IsComplete`).
- Insert merges with every run it overlaps **or touches**, so the set
  has exactly one normal form however the ranges arrived. An empty
  range changes nothing. An insert that would exceed
  `limits.UploadIntervalRuns` refuses with `ErrFragmented` and leaves
  the set untouched: the refusal costs the client one chunk, not the
  session.
- `LoadIntervalSet` **re-derives the invariant rather than trusting
  stored rows**: rows are inserted, not adopted, so an overlapping or
  unsorted pair coalesces into the same normal form a live insert would
  produce, and an empty or inverted row is corruption that refuses. A
  set that claims a range it does not hold becomes wrong offset
  arithmetic and then a hole the client resumes past.

## The write paths

```go
func (e *Engine) PatchAt(ctx context.Context, root *vfs.ShareRoot, id SessionID,
    user core.UserID, off uint64, body io.Reader, sum *Checksum) (uint64, error)
func (e *Engine) PutNamed(ctx context.Context, root *vfs.ShareRoot, id SessionID,
    user core.UserID, name uint32, body io.Reader, sum *Checksum) error
func (e *Engine) ListChunks(ctx context.Context, id SessionID, user core.UserID) ([]uint32, error)
```

- **The row lock covers the bookkeeping, never the body.** This is a
  named regression: holding the lock across the body read serialized
  concurrent chunks, and under HTTP/2 that deadlocks rather than
  queues; blocked handlers never read their streams, the connection's
  flow-control window fills, and the chunk holding the lock cannot
  receive its own body. Every upload stopped after its first chunk.
  Chunk bodies write concurrently through `pwrite` at their own
  offsets; only the interval-set update takes the lock.
- The body streams through a fixed 256 KiB buffer into `pwrite`;
  nothing buffers a whole chunk, so a 1 GiB chunk leaves resident
  memory unchanged.
- Trust-boundary checks, each refusing before any byte lands: the
  offset within the declared length (`validateOffset`,
  `checkWithinDeclared`), the chunk floor against the session's
  snapshot (`checkChunkFloor`, refusing `ChunkTooSmallError` except for
  the final chunk), a non-random-access session's chunk landing
  anywhere but the resumable offset (`ConflictError`).
- A per-chunk checksum, when presented, is verified **before** the
  interval set records the range: a chunk that fails verification never
  becomes resumable-past.
- Name-ordered chunks land as `.scpart-{hex8}` files, fixed-width hex
  of the ordinal, so on-disk order and numeric order agree and a
  listing needs no parsing to sort.

## Assembly

```go
func (e *Engine) Assemble(ctx context.Context, r core.Resolved, id SessionID) error
```

Name-ordered only: concatenate the chunk files into the part file in
ascending name order through `copy_file_range` (invariant 2), leaving
the session holding `FullIntervalSet(total)`. A missing ordinal refuses
with `IncompleteError` naming the gap. Assembly is idempotent over a
crash: re-running starts from the assembly cursor the row keeps, and a
chunk already copied is not copied twice.

## Deliberate changes

None beyond the overview's. The lock-scope rule, the buffer size, the
floor snapshot and both invariants are behavior-preserving requirements.

## Tests

- Interval set: insert merges overlapping, touching and contained
  ranges into one normal form regardless of arrival order (property
  test across shuffles); the run bound refuses leaving the set
  unchanged; load re-derives (unsorted, overlapping rows) and refuses
  inverted rows; prefix/complete/missing/received across sparse
  patterns; the zero-length file (complete when empty).
- PatchAt: a chunk past the declared length refuses; below the floor
  refuses except as the final chunk; at a non-resumable offset without
  random access refuses with the typed conflict; concurrent chunks at
  distinct offsets land in parallel (no serialization: assert with a
  blocking-reader body that a second chunk completes while the first
  is stalled, the HTTP/2 regression).
- A failed per-chunk checksum leaves the interval set unrecorded.
- PutNamed: ordinals round-trip; a repeated ordinal is one no-op landing
  (the row-lock rule); ListChunks reports what landed.
- Assemble: a gap refuses naming the missing ordinal; assembly after a
  crash resumes from the cursor; the assembled bytes equal the chunks
  in name order (content check); the part file is never read back
  (assert via a copy_file_range seam or strace-level fixture on the
  test's own build tag).
- Received versus Offset diverge after a write past a hole.
