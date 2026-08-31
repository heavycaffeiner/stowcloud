# Upload 04: verification, finalize, limits

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/upload` (here `verify.go`, `finalize.go`, `settings.go`)
> is referenced as a behavioral specification only. The new
> implementation is written completely from scratch; nothing is copied.

## Checksums

```go
const (
    AlgoCRC32C Algo = iota // Castagnoli, standard library
    AlgoBLAKE3             // the pure-Go module the directory ETag uses
)
func ParseAlgo(s string) (Algo, error)     // unknown refuses, never defaults
func Algorithms() []Algo                   // what the server advertises, preference order
type Checksum struct{ Algo Algo; Digest []byte }
func ParseChecksum(s string) (Checksum, error) // "algo digest-base64"
```

- Both algorithms are client-facing TUS header values; neither can be
  swapped for whatever the library offers. BLAKE3 stays the pure-Go
  module so `CGO_ENABLED=0` has nothing to fall back from.
- An unknown algorithm **refuses rather than defaults**: defaulting
  would verify a chunk against a digest the client never computed.
- A digest whose length cannot be the algorithm's output refuses at
  parse: a short digest would compare against a truncation of the real
  one and pass.
- Per-chunk digests are computed **streaming**, over the same slices
  that feed `pwrite`; a checksum costs a hasher, never a copy of the
  chunk.

## Whole-file verification

```go
type Verify struct{ Algo Algo; Digest []byte } // both, never one alone
func VerifyWholeFile(f *vfs.File, v Verify, length uint64) error
```

- `Verify` carries the algorithm **and** the expected digest as one
  value because the shape that shipped before carried only a selector:
  verification computed a digest and logged it, and could never fail
  whatever arrived on disk. The pair is the fix and the type is the
  enforcement.
- The read streams exactly `length` bytes from zero through a fixed
  256 KiB buffer (a 50 GiB upload costs that much memory), refuses on a
  short read, and compares in constant time (one comparison style for
  both algorithms rather than an argument about which needs it).
- The read goes through **the same descriptor that took the chunk
  writes**: this is the one holder of `vfs.IntentReadWrite` and the
  reason that intent exists. A read-only reopen would fail the
  verification it was opened for.

## Finalize

```go
func (e *Engine) Finalize(ctx context.Context, r core.Resolved, id SessionID) (core.Entry, error)
```

Order, each step refusing before bytes move:

1. Require `Write|Create`; require the owner.
2. **The destination re-check**: the resolved destination must equal the
   session's own recorded destination (`dest.Equal(r.Path())`). A
   session is bound to where it was created for; a finalize resolved
   somewhere else is refused before anything is touched.
3. Completeness: the interval set covers the declared length
   (`IncompleteError` naming the missing ranges); a deferred length must
   have been set.
4. Set `StateFinalizing` (01).
5. Whole-file verification when the session carries a `Verify`.
6. Publish through `core.PublishPart`: the rename, the mode and
   ownership rules, the quota charge and the journal row are the core's
   sequence, not this package's.
7. Delete the session row; close and forget the handle and the row
   lock.

## Settings

```go
func (e *Engine) ApplySettings(ctx context.Context, minBytes, defaultBytes *uint64) error
func (e *Engine) Settings() *Settings // Min(), Default() atomics
```

The ladder: the hard floor (`limits`) beats the persisted admin
override, which beats the config seed (`Options.ChunkMin/ChunkDefault`).
Sessions snapshot the floor at creation (01), so applying a new one
moves only future sessions. `SetChunkSettings` is dropped (overview
change 1); `ApplySettings` with nil-means-leave pointers is the one
write path.

## Account limits

`Create` refuses over the per-account bounds (open session count,
total declared bytes) with `ExhaustedError` naming which limit, and
refuses a declared length past the destination's free space (the
parent-directory measurement, 01). The bounds live in `kit/limits`.

## Deliberate changes

Only the overview's. The refuse-not-default parse, the digest-length
check, the pair-not-selector shape, the same-descriptor read and the
destination re-check are behavior-preserving requirements.

## Tests

- Parse: unknown algorithm refuses; wrong-length digests refuse per
  algorithm; the wire spelling round-trips.
- Known-answer digests for both algorithms (fixed vectors).
- Whole-file: a matching file passes; one flipped byte refuses; a file
  short of `length` refuses on the short read; the check reads through
  the session's own descriptor (fixture: replace the file after the
  handle opened; verification sees the handle's bytes).
- Finalize: wrong owner refuses; a destination other than the session's
  refuses before any filesystem effect (watch the part file); an
  incomplete set refuses naming gaps; deferred length unset refuses;
  verification failure leaves the part file in place and the session
  finalizing-not-done (recoverable by another finalize after repair);
  success publishes exactly the core's way (entry returned, row gone,
  locks gone).
- Settings: the ladder (floor beats override beats seed); nil leaves a
  side alone; applied settings move only new sessions.
- Limits: session count and declared-bytes bounds refuse with the typed
  error; free space measures the parent directory (fixture with a
  bind-mounted small volume or the vfs test double).
