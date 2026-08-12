# Preview - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Thumbnail generation: the parent-side pool, the wire protocol to the jailed
worker, decode limits, EXIF stripping, the cache, and archive listing. The jail
itself is [`stowcloud-4-jail-and-hardening.md`](stowcloud-4-jail-and-hardening.md);
this is what runs inside it and what talks to it.

## 2. Background & Motivation

`sc-preview` is 3,989 lines. It is the most dangerous code in the product,
because it is the only place where a byte sequence a stranger chose is fed to a
parser complex enough to have bugs, and that is why it is the only place with a
jail.

### 2.0 The two threats, and the two defences

| Threat | Vector | Defence |
|---|---|---|
| stored XSS | uploaded HTML, SVG or PDF rendered on the app origin, then session theft | content-origin separation |
| decoder RCE | a crafted JPEG or TIFF fed to a parser, then memory corruption | a jailed worker process |

They are separate defences for separate threats, and neither substitutes for
the other. The first is why **the app origin never returns user-content bytes**
and why access to content is a capability rather than a cookie
([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S9): a signed URL
that carries no session cannot be turned into one by anything the content
does. The second is [`4`](stowcloud-4-jail-and-hardening.md) §2.1.

One defence was considered and rejected with a reason worth keeping, because it
is the one a reviewer suggests first: **binding a signed URL to the requesting
IP address**. It false-positives constantly on mobile networks, where an
address changes mid-download, so it would break ordinary use to raise the cost
of an attack that etag binding and audit logging already raise. A control that
users route around is a control that is off.

The Go port changes the jail's shape and keeps its properties. Two things here
are genuinely new work rather than translation:

- **The decoders are Go's.** `image/jpeg`, `image/png` and `image/gif` are
  standard library; BMP, TIFF and WebP are `x/image`. Their limits, failure
  modes and memory behaviour are not the `image` crate's, so `DecodeLimits` has
  to be re-derived rather than copied.
- **The wire protocol is new.** `postcard` is replaced, and the replacement is
  deliberately not a reflective encoder, because the peer is the least trusted
  process in the system.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Thumbnails for the formats the current build accepts, with the one
      recorded reduction below.

**A3 is settled, and it splits in two.** On the decode side there is no
reduction for still images: `x/image/webp` v0.45.0 reads `VP8 ` (lossy),
`VP8L` (lossless) and `VP8X` with an `ALPH` chunk (alpha), which is the whole
of what the `image` crate accepts for a still WebP. What it does not read is an
animated WebP, where the current build decodes the first frame; a preview of
one is refused rather than produced, and that is the reduction. It is the same
shape as the GIF preset, which asks for the first frame only, so the loss is
one format's animation and not a format.

On the encode side the answer is not a reduction, it is a format change, and
it is recorded in `OPEN-QUESTIONS.md` rather than settled here: the current
pipeline writes lossless WebP (`crates/sc-preview/src/exif_strip.rs:31`) and
there is no WebP encoder in the standard library or in `x/image`. The thumbnail
output becomes PNG, which is lossless, keeps alpha, writes no EXIF, and is
standard library, so it is what this port's dependency table implies. What it
costs is cache footprint against the 2 GB default, and that is a measurement
Phase 9 owes.
- [ ] The worker never told a path; input and output arrive as descriptors.
- [ ] A wire codec with a fixed layout and no reflection.
- [ ] Decode limits as the graceful stop, with `RLIMIT_AS` as the hard one.
- [ ] EXIF stripped from every generated thumbnail.
- [ ] The cache, keyed so that a changed source produces a changed key.
- [ ] Archive listing that reads the archive's own directory rather than
      extracting.
- [ ] A worker death costing exactly one thumbnail.

### 3.2 Non-Goals

- [ ] **Video thumbnails.** A standing non-goal, because it means running
      ffmpeg: a large decoder surface, a process this jail's syscall list does
      not fit, and the end of the distroless base. The honest "not implemented"
      answer is kept, including over the wire.
- [ ] Linking C decoders into any process. Pure Go, which is also what keeps
      `CGO_ENABLED=0`.
- [ ] **Server-side PDF and office rendering.** A PDF renderer's attack surface
      is larger than an image decoder's, and office conversion needs a resident
      converter process. Rendering PDFs in the browser instead was considered
      and dropped too: `pdf.js` gzips to more than the frontend's entire
      initial-JS budget, for a file type this product does not otherwise treat
      as special. PDF and office documents are download-only and the viewer
      classifies them the way it classifies video.
- [ ] **Bundling `ffmpeg`.** It would end the distroless base for a feature
      that is already a non-goal.
- [ ] Generating previews eagerly on upload. On demand, cached.
- [ ] Rendering uploaded content inline anywhere. The content origin and the
      capability URLs are unchanged; §2.0 is why they exist.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/preview
  service.go   the public API, concurrency cap, negative caching
  pool.go      the parent half: exec, socket, dispatch, reap, replace
  wire.go      the fixed-layout codec
  cache.go     the on-disk thumbnail cache
  sniff.go     content sniffing, which decoder to ask for
  preset.go    sizes and formats
cmd/stowcloud (preview-worker subcommand)
  worker.go    the child half: the jail sequence, then the decode loop
  decode.go    the decoders and their limits
  exif.go      stripping
```

### 4.2 Data Model Changes

None in SQLite. The thumbnail cache is a directory of files keyed by a digest
of `(identity tuple, mtime, size, preset)`, so a source that changed produces a
different key and a stale thumbnail is unreachable rather than evicted.

### 4.3 Core Logic

#### 4.3.1 The wire protocol

Fixed layout over `encoding/binary`, big-endian, with an explicit version byte.
Not `encoding/gob`, not JSON, and the reason is the threat model rather than
performance: `gob` is reflective and allocates based on what the peer sends, and
the peer is a process that may already be executing an attacker's decoder bug.

```
request:  ver:u8 kind:u8 preset:u8 flags:u8 maxpix:u32 deadline_ms:u32
response: ver:u8 status:u8 w:u16 h:u16 nbytes:u32 errlen:u16 err:[errlen]u8
```

Both are fixed size except the error string, which is capped so the whole
message stays inside the 8 KiB `SOCK_SEQPACKET` bound (D5). A message that does
not parse **exactly** kills the job and the worker: a partially valid message
from the jailed process is not a thing to recover from.

Descriptors travel beside the message as `SCM_RIGHTS`. Exactly two, input and
output, and a message arriving with a different count is the same fatal case.

#### 4.3.2 The pool

The parent holds N worker processes, each on its own `SOCK_SEQPACKET` pair. A
job is dispatched to a free worker with a deadline from the caller's context.

Worker death is an ordinary event: a seccomp kill, an `RLIMIT_AS` OOM, a
segfault and the CPU limit all present identically as an empty read or
`ECONNRESET`. The parent reaps, fails that job with `ErrWorkerDied`, and execs a
replacement on the next job for that slot. Replacement is lazy rather than
eager so that a source that reliably kills workers cannot become a fork bomb.

A deadline that expires with the worker still alive is handled by killing it,
not by waiting: the worker is single-purpose and there is nothing to preserve.

#### 4.3.3 Decode limits

Two layers, and they do different jobs:

- `RLIMIT_AS` at 512 MiB is the hard stop. It kills the worker, which costs a
  thumbnail.
- `DecodeLimits` is the graceful stop: maximum source pixels, maximum
  dimension, maximum output pixels. It refuses the job with a typed error and
  the worker survives.

The graceful limit has to come first for the common case, because a 40,000 by
40,000 PNG is an ordinary thing to find in a photo library and killing a worker
for it is a bad trade. The values are re-derived for Go's decoders rather than
copied from the Rust ones: `image/png` and the `image` crate do not allocate the
same way, and a limit tuned for one is a guess for the other.

**Decoder-specific note.** Go's `image/gif` decodes every frame of an animation
into memory. The preset asks for the first frame only, and the limit is applied
to the logical screen size before decode, not after.

#### 4.3.4 EXIF

Stripped from every generated thumbnail, because a thumbnail derived from a
holiday photo otherwise carries its GPS coordinates to whoever the folder is
shared with. Go's encoders do not write EXIF, so this is not about removal so
much as about never carrying it across: the pipeline decodes to a pixel buffer
and encodes from the buffer, and the only metadata that survives is the
orientation, applied to the pixels and then discarded.

Orientation is read before the strip and applied as a rotation, because dropping
it without applying it turns every portrait photo sideways.

#### 4.3.5 Video

`JobKind.Video` exists and answers "video preview generation is not implemented
in this build". That is deliberate and it is kept, including over the wire: a
client asking for one gets an honest refusal rather than a generic failure, and
the negative result is cached so it is asked once.

#### 4.3.6 Archive listing

Listing a zip reads the archive's central directory, not its contents. The cost
bound is stated in the response, and the entry count is capped (D5) with the
truncation flagged.

`archive/zip`'s `OpenReader` needs an `io.ReaderAt` and a size, which a
[`stowcloud-3`](stowcloud-3-vfs-and-paths.md) `*File` provides through `pread`
without loading the archive. Nothing is extracted to list.

This runs in the **parent**, not the worker: it parses a directory structure
rather than decoding image data, and putting it in the worker would mean passing
a whole archive across the socket. The parser is bounded and fuzzed (D16)
instead, which is the appropriate control for a structure parser that does not
allocate per byte.

## 5. API Design

### 5-1. New / Modified

```go
package preview

// Get returns a thumbnail for e at preset, from cache when possible. A source
// that has failed before is refused from the negative cache without touching a
// worker, so a corrupt file in a folder does not cost a worker on every
// listing.
func (s *Service) Get(ctx context.Context, r core.Resolved, preset Preset) (Thumb, error)

// Pool.Generate is in stowcloud-4 §5-1: it takes two descriptors and never a
// path.

// ListArchive reads the central directory of a zip and reports its entries.
// It never extracts, it runs in this process rather than the worker, and it
// caps the entry count with the truncation reported rather than silent.
func ListArchive(ctx context.Context, f *vfs.File, size int64) (ArchiveListing, error)
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 404 | no preview for this entry and none can be made |
| 415 | a format this build does not decode |
| 501 | video, which is honestly not implemented |
| 503 | the pool is unavailable, or no worker free within the deadline |

| Error | Meaning |
|---|---|
| `ErrUnsupported` | the sniffer identified a format with no decoder |
| `ErrNotImplemented` | video |
| `ErrTooLarge` | a `DecodeLimits` bound refused; the worker survived |
| `ErrWorkerDied` | the worker was killed; one thumbnail lost |
| `ErrWorkerBusy` | no free worker within the caller's deadline |
| `ErrProtocol` | a wire message did not parse exactly; the worker is killed |

`ErrTooLarge` and `ErrWorkerDied` are both cached negatively, with different
lifetimes: a file too large now will be too large next time, while a worker
death might have been the machine rather than the file.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 9a | `wire.go` and its fuzz target | S | Phase 0 | heavycaffeiner |
| Phase 9b | `decode.go`, `exif.go`, `preset.go`, and the re-derived limits | M | Phase 0c (A3) | heavycaffeiner |
| Phase 9c | `worker.go`: the jail sequence from stowcloud-4 §4.3.2, then the loop | M | 9a, 9b, Phase 1 | heavycaffeiner |
| Phase 9d | `pool.go`: exec, dispatch, reap, replace | M | 9c | heavycaffeiner |
| Phase 9e | `service.go`, `cache.go`, `sniff.go` | M | 9d, Phase 4 | heavycaffeiner |
| Phase 9f | `ListArchive` and its fuzz target | S | Phase 4 | heavycaffeiner |
| Phase 9g | The `SECCOMP_RET_LOG` corpus run that produces the worker allow-list ([`4`](stowcloud-4-jail-and-hardening.md) §4.3.3) | S | 9d | heavycaffeiner |
| Phase 9h | The jail proof, on a real kernel ([`4`](stowcloud-4-jail-and-hardening.md) §4.3.6) | S | 9d | heavycaffeiner |

9f is independent of everything else in this phase. 9g and 9h are specified in
[`4`](stowcloud-4-jail-and-hardening.md) and scheduled here, because both need
a decoder to exercise and this is the phase that produces one.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `golang.org/x/image` | BMP, TIFF and WebP decoding |
| `golang.org/x/sys/unix` | the socket, `SCM_RIGHTS`, and the jail |

JPEG, PNG, GIF and zip are standard library, and so is the thumbnail encoder:
`x/image` has no encoder for any of the three formats it decodes here, which is
why §3.1 records the output moving to PNG. Assumption A3 in
[`stowcloud-2`](stowcloud-2-gate-and-toolchain.md) §4.4 is settled there.

## 7. References

- `crates/sc-preview/src/pipeline.rs`, `decode.rs`, `exif_strip.rs`,
  `sniff.rs`, `cache.rs`, `preset.rs`, `archive.rs`: the pipeline this
  translates.
- `crates/sc-http/src/content.rs`: the content origin and the signed URLs §2.0
  describes.
- `crates/sc-preview/src/worker/jailed/mod.rs`: the wire message shape and the
  worker-death handling §4.3.2 carries over.
- `crates/sc-preview/src/lib.rs:14`: the video non-goal and its honest answer.
- [`stowcloud-4-jail-and-hardening.md`](stowcloud-4-jail-and-hardening.md): the
  jail this runs inside.
