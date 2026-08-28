# Preview 02: the worker and its protocol

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/preview` (here `wire.go`, `pool.go`, `decode.go`,
> `exif.go`, `worker/worker.go`) and the seqpacket transport currently in
> `go/internal/vfs` is referenced as a behavioral specification only. The
> new implementation is written completely from scratch; nothing is
> copied.

## The trust model

The worker decodes hostile bytes, so the worker trusts nothing,
including its own parent. Paths never cross the boundary: the parent
opens the source and the output and passes **descriptors** over
`SCM_RIGHTS` on a seqpacket socketpair. The transport (message-boundary
framing, descriptor attachment) moves here from vfs as
`worker/transport.go`; vfs is the filesystem boundary and a socket codec
never belonged in it.

## The wire codec

```go
type Request struct {
    Kind       JobKind
    Preset     Preset
    Flags      uint8
    MaxPixels  uint32 // the parent's ceiling travels with the job
    DeadlineMs uint32
}
type Response struct {
    Status        Status
    Width, Height uint16
    Bytes         uint32 // written to the output descriptor
    Err           string // truncated at the wire bound
}
```

- Fixed-width fields; `Encode` cannot fail. The first byte is
  `WireVersion`, and a version mismatch reads as `ErrWorkerDied`: a
  worker from another build is a dead worker, not a negotiation.
- `DecodeRequest` is the worker-side trust boundary and is fuzzed.
- `MaxPixels` travels in the request so the parent's limit is not
  compiled into two places that can disagree, and the worker clamps it:
  a request can **lower** the compiled-in ceiling, never raise it
  (verified defence; a compromised parent cannot widen its worker).
- This file is an internal RPC codec between two halves of one process
  tree. The audit flags the name: it is **not** one of the
  presentation-layer "wire shapes", and it stays in this package.

## The worker

`worker.Run`, in order:

1. `runtime.GOMAXPROCS(1)`.
2. **The jail, before the socket**: `jail.Apply`, `jail.InstallSeccomp`,
   and now `jail.SealDescriptors` and `jail.ApplyLimits` (01's
   decision). The first message is read after the sandbox is live.
3. The loop: read a request, clamp its limits, read the input through
   `io.LimitReader(MaxInputBytes+1)` with the post-check (the worker's
   own 256 MiB ceiling, enforced independently of what the parent
   already checked, deliberate defence in depth), decode, encode PNG to
   the output descriptor, respond.

## Decoding

```go
type DecodeLimits struct {
    MaxPixels       uint64 // width x height; 64-bit to dodge the 32-bit overflow
    MaxDimension    int    // either side alone: a 1 x 500,000,000 image fits a
                           // pixel budget and no scaler handles it
    MaxOutputPixels uint64 // what a preset may produce
}
func DefaultDecodeLimits() DecodeLimits
func Sniff(data []byte) Format
func DecodeBounded(data []byte, lim DecodeLimits) (image.Image, error)
func EncodePNG(w io.Writer, img image.Image) error
```

- The format comes from magic bytes, never a declared name.
- Header dimensions are checked **before** the pixel buffer allocates,
  and the decoded image is checked **again** after, because a decoder
  that ignores its own header is exactly the decoder being defended
  against.
- GIF decodes one frame.

## EXIF

`ReadOrientation` parses just enough TIFF/JPEG structure for the
orientation tag, under the verified bounds: entry count capped, scanned
prefix capped, recursion depth capped at 2, every offset checked against
the slice before indexing. Fuzzed.

## The pool

- A buffered `free` channel as the semaphore, a per-slot mutex; a caller
  finding no free slot is refused, not queued (`ErrBusy` at the
  service).
- An empty read, a read error and a version mismatch are one answer:
  `ErrWorkerDied`. Dead workers are reaped and lazily replaced on next
  use; a hung worker is killed at its deadline.
- Every failure path in pool start closes what it opened (verified; a
  requirement).

## Deliberate changes

1. **The transport moves from vfs** (overview decision 2).
2. **The worker's startup gains the seal and the rlimits** (01's
   decision), in the order above.
3. Nothing else: the codec, the clamp direction, the double bounds
   check, the one-answer death rule and the reaping are
   behavior-preserving.

## Tests

- Codec round-trip; fuzz both decoders; the version mismatch reads as a
  dead worker; the error-string truncation bound.
- The clamp: a request with a larger MaxPixels than compiled decodes
  under the compiled bound (fixture image sized between the two).
- Decode: bombs refuse at the header; a lying header refuses after
  decode; each format's golden thumbnails; the one-frame GIF rule.
- EXIF: orientation vectors including each rotation; hostile structures
  (looping IFDs, out-of-range offsets) refuse under fuzz.
- Pool: a busy pool refuses; a killed worker is replaced on next use; a
  hung worker dies at the deadline; a closed pool refuses; no
  descriptor leak across worker death (count fds).
- The jailproof suite: a jailed worker survives many jobs; cannot open
  paths; reports the rlimits; holds no unexpected descriptors (the two
  new assertions from 01).
