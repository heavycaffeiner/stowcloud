# Phase 8: search

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-11-search.md`.

## Scope

The tiered search: the parallel walk, the optional block-compressed trigram
index, the estimator that chooses between them.

Depends on Phase 4. Blocks Phase 10. Independent of Phases 6, 7 and 9.

## Milestones

- **8a**: the Rust-side golden-file generator and the committed fixtures.
- **8b**: `varint.go`, `fold.go`, `trigram.go`, `hll.go`, `rank.go` against
  those fixtures.
- **8c**: `index/base.go`: read and write, byte-identical.
- **8d**: `index/seg.go`, `index/index.go`: the overlay, the union, the merge
  gate.
- **8e**: `walk.go`, `estimate.go`, the service.

## Do 8a early, out of order

**8a runs against the Rust tree, which Phase 13 deletes.** It depends on nothing
and can be done before Phase 0. It emits the fixtures the entire search port is
verified against, and losing the chance means falling back from "byte-identical
output" to "a passing behavioural test", which is a much weaker claim for seven
hand-written algorithms.

## Traps

- **The on-disk format does not change.** That is what turns seven algorithm
  ports into a diff instead of a judgement. If a byte-identical write is
  impossible, make the writer's ordering explicit rather than weakening the
  check.
- **Never `statx` for a name-only query.** Published measurement puts metadata
  at roughly half the cost of a walk, so a name query that stats is double
  price for information nobody asked for.
- **The ACL check happens before an entry is scored**, and a query matching many
  entries the caller cannot see must not be measurably slower than one matching
  none. That is a test, not an aspiration: search sweeps the whole tree, so it
  is the broadest place to leak.
- **Cancellation is checked once per directory**, not once per entry. A search
  the client abandoned must stop walking 12 TB without checking a context a
  million times.
- **The two fallback reasons are a type on the return.** An empty slice cannot
  distinguish "the index looked and found nothing" from "the index declined to
  look", and the caller must run a walk in the second case and must not in the
  first.
- **The walker is written in-tree.** Anything built on `filepath.WalkDir`
  resolves a path per entry and reintroduces symlink escape and TOCTOU. Walking
  through the share's own directory handles is also faster, so the boundary is
  the performance win here rather than a trade against it.
- **The index is a cache.** A corrupt segment disables it and search continues
  on the walk. It never fails a query.
- **`meta` is swapped by atomic rename** through the durable helper, parent
  fsync included.
- **Unmap segments explicitly.** A mapping held across a merge is a file that
  cannot be deleted.
- **The merge runs under the idle gate, never on a request path.**
- **Bounds are storage-class dependent** and admin-mutable within the D5 outer
  bound: 4 concurrent searches and a 3 second deadline on SSD, 2 and 8 seconds
  on rotational.

## Done when

- The gate is green, including `-race`.
- Every golden fixture reads correctly, and re-writing `base.idx` from the same
  corpus produces identical bytes.
- The timing-leak test passes.
- A deliberately corrupted segment disables the index and leaves search working.
