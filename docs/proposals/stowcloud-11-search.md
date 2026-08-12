# Search - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The tiered search: the parallel walk, the optional block-compressed trigram
index, the estimator that decides between them. The on-disk format does not
change, which turns most of this phase from a reimplementation into a
translation with a golden-file check.

## 2. Background & Motivation

`sc-search` is 5,546 lines and it is the phase with the most self-contained
algorithmic content: a trigram extractor, a Hangul folding table, a varint
codec, a block-compressed immutable segment format, a delta and tombstone
overlay, a HyperLogLog cardinality estimator and a ranking function.

Reimplementing seven algorithms from a specification is seven chances to be
subtly wrong in a way that a behavioural test does not catch, because a search
that returns slightly different results still looks like a search.

The on-disk format is private and is not changing, so there is a much stronger
check available: make the Rust implementation emit fixtures, and require the Go
implementation to read them and to produce byte-identical output when it writes
them. A byte-identical index is a much narrower claim than a passing test suite,
and it is checkable.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] The T2 parallel walk, ACL-filtered, bounded, cancellable.
- [ ] The T3 index: base segment, delta segments, tombstones, and
      `base ∪ Σdelta − tomb` as the query.
- [ ] The merge gate: rebuild when `Σdelta + tomb > merge_ratio × base`, at
      idle, never on the request path.
- [ ] The estimator, including the HyperLogLog term that makes a CJK corpus
      cost what it actually costs.
- [ ] The two fallback reasons reported as a type rather than an empty result:
      a query under three bytes has no trigram, and a query whose trigrams were
      all pruned would intersect over nothing.
- [ ] Byte-identical segment output against Rust-generated fixtures.

### 3.2 Non-Goals

- [ ] **Content indexing and OCR.** Not implemented, not planned, and the
      reason is structural rather than scheduling: a walk cannot substitute for
      it, because 12 TB cannot be grepped per query. It is therefore the one
      tier that would *require* an index and would need the heaviest
      guardrails, and every unbounded implementation of it gets punished at a
      few million files.
- [ ] Changing the on-disk format. The whole verification strategy depends on
      not changing it.
- [ ] A general-purpose query language. Name substring matching, folded.
- [ ] **Turning the index on by default.** It is an escalation taken when
      measurement says the walk is not fast enough, and the trigger is not a
      file count. Cold storage is a different world: the same corpus that walks
      in under a second warm takes tens of minutes on a cold 12 TB array, one
      seek per directory. Keeping the dentry cache warm costs roughly 192 bytes
      per file, so whether a tree stays warm depends on RAM, storage and
      whatever else competes for both, none of which is knowable in advance.
      A threshold would be a guess wearing a number.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/search
  walk.go       the T2 parallel walk
  fold.go       case and width folding, Hangul decomposition
  trigram.go    extraction
  varint.go     the codec
  hll.go        the cardinality estimator
  rank.go       scoring
  estimate.go   which tier answers this query
  index/
    base.go     the immutable block-compressed segment
    seg.go      delta segments and tombstones
    index.go    the union, the merge gate, the meta swap
```

On disk, unchanged:

```
data/search/names/
  base.idx        immutable, block-compressed
  delta.NNN.idx   append-only, lightly compressed, linearly scanned
  tomb.idx        deletions
  meta            generation, config, segment list; swapped by atomic rename
```

### 4.2 Data Model Changes

None. That is the point.

### 4.3 Core Logic

#### 4.3.1 The golden-file strategy

Before any Go code is written, the Rust implementation gains a test-only
generator that emits, into `go/testdata/golden/search/`:

| Fixture | Checks |
|---|---|
| `varint.bin` | the codec, over a value set including every boundary |
| `fold.tsv` | input name, folded output, over Latin, CJK and Hangul cases |
| `trigram.tsv` | input name, extracted trigram set, in order |
| `base.idx` | a full base segment built from a fixed name corpus |
| `delta.000.idx`, `tomb.idx` | an overlay over that base |
| `query.tsv` | query, expected hits in expected order, with scores |
| `hll.tsv` | insert sequence, expected estimate |

The Go implementation must read every one of them and produce identical results,
and when it writes `base.idx` from the same corpus it must produce the same
bytes. Where a byte-identical write is impossible for a defensible reason (a map
iteration order reaching the output, say), the fix is to make the writer's order
explicit, not to weaken the check.

**The generator exists**, at `crates/sc-search/examples/golden.rs`, and the
fixtures are committed with a `README.md` describing each format. It is an
example rather than a test because it writes into the repository, it reads only
the crate's public API, and it changes nothing in `sc-search`: a golden file
records what the implementation does, so an implementation bent to make
generation easier records the bend instead. Two consecutive runs produce
byte-identical output.

**One byte-identity claim does not survive, and it is not an ordering
difference.** The block payloads are zstd frames, and zstd's format specifies
what a decoder accepts rather than what an encoder emits, so two independent
encoders agreeing byte for byte would be a coincidence rather than a property.
The header, the block directory, the trigram dictionary and the posting lists
are the format's own bytes and are compared exactly; the payloads are compared
through what they decompress to. `OPEN-QUESTIONS.md` Q2 carries the options and
what would reopen it.

This is worth the setup cost because it converts "does the Go search work" from
a judgement into a diff.

#### 4.3.2 The T2 walk

A bounded worker pool over goroutines, replacing `rayon` and the `crossbeam`
deque. Each worker walks a subtree through
[`stowcloud-3`](stowcloud-3-vfs-and-paths.md)'s streaming `ReadDirFunc`, so a
huge directory does not materialise (F4), and checks the caller's ACL before an
entry is scored.

Cancellation is the request's context, checked once per directory rather than
once per entry. A search that the client abandoned must stop walking a 12 TB
tree, and it must not check a context a million times to do so.

The result set is capped (D5) and a truncated result says so in the response
rather than looking like a complete one.

**The bounds depend on the storage class**, and that is the same argument §3.2
makes about the index: a walk on an NVMe array and a walk on a cold rotational
one are different operations wearing one name. The classification comes from
the filesystem gate ([`15`](stowcloud-15-deployment.md) §4.3), and it moves two
numbers:

| | SSD or NVMe | rotational or network |
|---|---|---|
| concurrent searches, server-wide | 4 | 2 |
| walk deadline | 3 s | 8 s |

A lower concurrency on slow storage is not a smaller allowance, it is a larger
one: four concurrent seek-bound walks on one array finish later than two do,
and they take the interactive listing requests down with them. The longer
deadline on the same storage is the matching admission that the work genuinely
takes longer there.

**Two stances decide the shape of the walk**, and both are easy to lose:

- **No existence leak, in results or in response time**
  ([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S2). Search
  sweeps the whole tree, so it is the broadest possible place to leak. The ACL
  check happens before an entry is scored, and a query that would match many
  entries the caller cannot see must not be measurably slower than one that
  would match none. That is a test, not an aspiration.
- **Never `statx` for a name-only query.** Published measurement puts metadata
  at roughly half the cost of a walk, so a name query that stats is twice the
  price for information nobody asked for. Size and time are resolved only for
  the entries that survive filtering and ranking.

The walker is written in-tree rather than taken off the shelf, and that is a
security decision before it is a performance one. `filepath.WalkDir` and every
library built on it resolve a path per entry, which reintroduces symlink escape
and TOCTOU by bypassing the `openat2` invariant. Walking through the share's own
directory handles also avoids the whole-path re-resolution a path-based walker
pays per entry, so the boundary is also the faster option
([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S13).

#### 4.3.3 The T3 index

**The split is a necessity, not an optimisation.** An immutable block-compressed
index cannot be upserted: a name cannot be inserted into the middle of a
compressed 32-name block, and a block id cannot be inserted into a delta-encoded
posting list. `plocate` sidesteps this by rebuilding nightly, and this server
cannot, because "filesystem changes are reflected immediately" is a requirement.
So writes go to a delta segment in constant time and the expensive rebuild
happens under a gate, at idle.

The `meta` file is swapped by atomic rename, which means it goes through
[`stowcloud-3`](stowcloud-3-vfs-and-paths.md) §4.3.5's helper like every other
durable publish, including the parent directory `fsync` that makes the new name
survive a power cut.

Segment reads are `unix.Mmap`, and the mapping is explicitly unmapped rather
than left to a finalizer, because a mapping held open across a merge is a file
that cannot be deleted.

#### 4.3.4 The estimator

Decides whether a query is answered by the walk or the index, and the decision
depends on how many distinct trigrams the corpus has, which is what the
HyperLogLog is for. A CJK corpus has vastly more distinct trigrams than a Latin
one at the same file count, and the estimator getting that wrong is how a search
that should have taken the index takes a full walk.

The two fallback reasons are a type on the return, not an empty result:

| Reason | Meaning |
|---|---|
| `QueryTooShort` | under three bytes; no trigram exists |
| `AllTrigramsPruned` | every trigram was dropped by high-document-frequency pruning, so the intersection is over nothing |

A bare empty slice cannot distinguish "the index looked and found nothing" from
"the index declined to look", and the caller must run a walk in the second case
and must not in the first.

#### 4.3.5 Revalidation

The index is a cache and it stores names only, not sizes or times. A hit is
promoted into a full result by a `stat` performed **after** the caller's ACL
check, and that stat doubles as the staleness check: an index entry for a file
that no longer exists is dropped from the result rather than returned.

## 5. API Design

### 5-1. New / Modified

```go
package search

// Query runs a search for user across every share they can see. It uses the
// index where the estimator says the index is worth it and the index does not
// decline, and walks otherwise. The result reports which tier answered, so the
// caller can surface "this was a full scan" rather than leaving a slow query
// unexplained.
func (s *Service) Query(ctx context.Context, user UserID, q string, opt Options) (Results, error)

package index

// Query returns hits, or a Fallback naming why the index declined. A caller
// receiving a Fallback must run a T2 walk; a caller receiving zero hits and no
// Fallback must not.
func (i *NameIndex) Query(q string) (hits []Hit, fb *FallbackReason, err error)

// Merge rebuilds the base segment from base plus the deltas minus the
// tombstones and swaps meta by atomic rename. It runs under the idle gate and
// never on a request path.
func (i *NameIndex) Merge(ctx context.Context) error
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 400 | a query that cannot be parsed, or options out of range |
| 200 | a truncated result, flagged in the body rather than raised as an error |
| 503 | the index is mid-merge and the walk fallback is also unavailable |

| Error | Meaning |
|---|---|
| `ErrIndexCorrupt` | a segment failed its header or checksum; the index is disabled and rebuilt, and search continues on the walk |
| `ErrCanceled` | the caller's context ended; not logged as a failure |

`ErrIndexCorrupt` degrading to the walk rather than failing the query is the
cache principle applied: a broken cache costs speed, never answers.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 8a | The Rust-side golden-file generator and the committed fixtures | S | none | heavycaffeiner |
| Phase 8b | `varint.go`, `fold.go`, `trigram.go`, `hll.go`, `rank.go` against the fixtures | M | 8a | heavycaffeiner |
| Phase 8c | `index/base.go`: read and write, byte-identical | L | 8b | heavycaffeiner |
| Phase 8d | `index/seg.go`, `index/index.go`: the overlay, the union, the merge gate | M | 8c | heavycaffeiner |
| Phase 8e | `walk.go`, `estimate.go`, the service | M | 8b, Phase 4 | heavycaffeiner |

8a runs against the Rust tree and can happen at any time, including before
Phase 0. 8e is the only part that needs the core.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `github.com/klauspost/compress/zstd` | block compression in the base segment |
| `golang.org/x/sync/errgroup` | the bounded walk pool |
| `golang.org/x/sys/unix` | `Mmap` for segment reads |

The zstd module has to produce output the Rust `zstd` crate's fixtures decode,
and vice versa. That is a format guarantee rather than a library one, so it is
checked by 8c's byte-identical requirement rather than assumed.

## 7. References

- `crates/sc-search/src/walker.rs`, `index/`, `estimate.rs`, `fold.rs`,
  `trigram.rs`, `varint.rs`, `hll.rs`, `rank.rs`: the implementation §4.3.1
  generates fixtures from.
- `crates/sc-search/src/index/mod.rs`: the segment layout and the reason the
  split is a necessity, quoted in §4.3.3.
- `crates/sc-search/src/estimate.rs`: the HyperLogLog term and the CJK case.
- `crates/sc-server/src/nc_search.rs`: the mobile search surface the compat
  layer needs from this.
