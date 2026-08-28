# Search 00: the family

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/search` (the root package: `walk.go`, `scan.go`,
> `fold.go`, `trigram.go`, `rank.go`, `hll.go`, `estimate.go`,
> `varint.go`) is referenced as a behavioral specification only. The new
> implementation is written completely from scratch; nothing is copied.

## Shape of the family

Three packages, mirroring the old tree:

```
engine/service/search/          the vocabulary and the walk tier
engine/service/search/index     the trigram index (01-index-format.md)
engine/service/search/svc       tier selection, build, updates (02-service.md)
```

The design position: **search works with no index at all.** The walk is
the base tier; the index is an escalation taken when measurement says
the walk is not enough, and it is a cache whose directory can be deleted
with no data loss. Everything in this family serves that ordering.

## The inversion (already settled)

The old `core/scan.go` builds `[]search.Source`, making the core import
search vocabulary. `foundation/search-contract.md` inverted this and
phase 1 built it: the core owns `core.ScanSource` (share, root, base,
per-path `Allow` closure) and this package **adapts**:

```go
// sourceOf converts the core's shape into this package's walker input.
func sourceOf(s core.ScanSource) Source
```

The rebuilt search family imports `core` for exactly this adapter and
the resolver types; core imports nothing from search.

## The three walks stay three

The audit names a triplication: `Walk` (parallel worker-pool walk, the
query tier), `ScanCorpus` (sequential, the estimator), and the service's
`Build` walk (the ingester). They are **kept as three**, and this
paragraph is the requirement that stops a future consolidation: the
estimator exists to be affordable (it must not spin up the query walk's
worker pool to answer "how big is the corpus"), the query walk exists to
be fast, and the ingest walk streams into segment writes with its own
batching. One shared walker would couple three cost profiles that were
separated on purpose. What they may share is the leaf vocabulary
(`Source`, the reserved-name filter, the `Allow` check), nothing more.

## The vocabulary

```go
type Source struct {
    Share ShareID
    Root  *vfs.ShareRoot
    Base  vfs.SafePath
    Allow func(p vfs.SafePath, isDir bool) bool // nil: everything
}

func Fold(b []byte) []byte        // ASCII case-fold for matching
func FoldString(s string) []byte
func IsFoldedASCII(b []byte) bool
func ContainsASCIIFold(haystack, needle []byte) bool

type Trigram uint32
func AppendTrigrams(out []Trigram, b []byte) []Trigram
func DistinctTrigrams(b []byte) []Trigram
func SortTrigrams / DedupTrigrams            // one home, here (change)

func Score(i RankInput) float32              // the ranking function
func InScope(path, scope string) bool
func IsHidden(name []byte) bool

func PutVarint(out []byte, v uint64) []byte  // LEB128
func Varint(buf []byte) (uint64, int)

type HLL struct{ ... }                       // the distinct-count sketch
func NewHLL(p uint8) *HLL
func Hash64(b []byte) uint64                 // FNV-1a + splitmix64

func ScanCorpus(ctx context.Context, sources []Source, opt ScanOptions) (ScanResult, error)
func EstimateNameIndex(stats CorpusStats, blockSize uint32) IndexEstimate
func Walk(ctx context.Context, ...) ...      // the parallel query walk
```

## The two hashes stay two

`Hash64` (the sketch input: FNV-1a plus splitmix64, hand-rolled and
auditable on purpose) and the index's `FNV1a32` (a corruption checksum)
are distinct primitives with distinct collision tolerances. The audit
flags them so nobody consolidates them into one helper; this document is
that flag made normative. Keep both, separately named.

`NewHLL`'s silent precision clamp to `[4,18]` is acceptable **only
because its caller is an internal constant**; the clamp must be
revisited if precision ever becomes configurable. The comment carries
this condition.

## Deliberate changes

1. **The adapter replaces the reversed dependency** (audit finding 1;
   the core side landed in phase 1).
2. **Trigram sort/dedup get one home** (audit finding 4): defined here,
   used by the index package, which already imports this one.
3. **The package doc moves to `doc.go`** (audit finding 3): the LEB128
   file is the wrong front door.
4. The hand-rolled `min` dies where the builtin serves (index audit
   finding 3).

Everything else, including the fold table, the ranking inputs, the
varint encoding and the sketch, is behavior-preserving; the golden tests
pin the encodings.

## Tests

- Fold: golden vectors; fuzz never panics; fold is idempotent;
  `ContainsASCIIFold` agrees with fold-then-contains.
- Trigrams: extraction goldens; sort/dedup normal form; the one-home
  rule (the index package has no local copy: enforced by review, stated
  here).
- Varint: round-trip fuzz; truncation refuses.
- HLL: known cardinalities within tolerance; the clamp bounds.
- Ranking: the golden ordering fixtures (the old `golden_test.go`
  scenarios rebuilt).
- ScanCorpus versus Walk: same corpus, same set of names (the two walks
  agree on membership; cost differs by design).
- The adapter: a `core.ScanSource` with an `Allow` closure filters the
  same paths through both walks.
