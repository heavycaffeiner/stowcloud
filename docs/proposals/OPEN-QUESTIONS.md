# Open questions

Decisions the port surfaced that only the maintainer can make. Each one records
the options, what would decide between them, and the answer taken in the
meantime so that no phase stalls on it. Taking an answer is not settling the
question: the entry stays until it is settled, and the phase named under
**Reopen by** is where the evidence to settle it arrives.

A question leaves this file by being answered in the document that owns it, not
by being deleted.

---

## Q1. The thumbnail output format, now that Go has no WebP encoder

**Raised by** Phase 0c, settling assumption A3
([`2`](stowcloud-2-gate-and-toolchain.md) §4.4).

**What was found.** A3 asked whether Go's decoders cover what the current build
accepts, and they do. What the question did not ask, and the compile answered
anyway, is the other direction: the preview pipeline's *output* is lossless
WebP (`crates/sc-preview/src/exif_strip.rs:31`, encoded with
`WebPEncoder::new_lossless`, cached as `<fid>-<w>x<h>-<etag8>.webp` in
`cache.rs:113`). `golang.org/x/image/webp` is a decoder. There is no WebP
encoder in the standard library or anywhere in `x/image`.

**Options.**

| # | Output format | Cost |
|---|---|---|
| 1 | PNG, standard library | lossless and keeps alpha, like today. Larger files than lossless WebP, against a 2 GB default thumbnail cache |
| 2 | JPEG, standard library | much smaller, and the wrong answer for the icons and screenshots a file browser is full of: lossy, and no alpha at all |
| 3 | A third-party pure-Go WebP encoder | keeps the format and the cache footprint. Adds a direct dependency the parent proposal's table does not carry, in a codec, which is the class of dependency this port spent §6-2 minimising |
| 4 | Write one | a still-image WebP encoder is VP8L plus a RIFF container. This is the category the house rules say not to hand-roll |

**What decides between them.** The measured size ratio at the preset
dimensions actually generated, against the 2 GB cache budget and the eviction
rate that budget implies. If PNG at those sizes is within a small factor of
lossless WebP, option 1 costs nothing anyone notices. If it is several times
larger, the cache holds proportionally fewer thumbnails and option 3 becomes
worth a dependency.

**Taken for now: option 1, PNG.** It is what the surrounding documents imply
rather than a preference: [`0`](stowcloud-0-motivation-and-findings.md) §6-2
commits to a direct-module list that has no encoder in it and says everything
else is standard library, [`12`](stowcloud-12-preview.md) §4.3.4 needs an
encoder that writes no EXIF and Go's do not, and the current output is
lossless, which PNG is and JPEG is not. The cache filename extension follows
the format.

**Reopen by** Phase 9, which is the first phase with a decoder and an encoder to
measure, and which owes the numbers either way.

**What is not in question.** The decode side. Still WebP reads in all three
shapes it ships in; animated WebP does not, and that reduction is recorded in
[`12`](stowcloud-12-preview.md) §3.1 rather than left to be discovered.

---

## Q2. What "byte-identical `base.idx`" can mean across two zstd encoders

**Raised by** milestone 8a, building the golden fixtures
([`11`](stowcloud-11-search.md) §4.3.1).

**What was found.** §4.3.1 asks that the Go implementation, writing `base.idx`
from the same corpus, produce the same bytes, and says that where a
byte-identical write is impossible the fix is to make the writer's order
explicit rather than to weaken the check. That instruction is the right one for
every difference it anticipated, and there is one it does not reach: the block
payloads are zstd frames. The Rust side produces them with the `zstd` crate at
level 6; the Go side will produce them with `klauspost/compress`.

zstd's format specifies what a decoder must accept, not what an encoder must
emit. Block splitting, match finding and entropy table selection are all the
encoder's to choose, and libzstd's numeric levels have no defined counterpart
in another implementation. Two independent encoders agreeing byte for byte
would be a coincidence, and one that any version bump on either side would end.
No ordering fix reaches this, because it is not an ordering difference.

**Options.**

| # | The check becomes | Cost |
|---|---|---|
| 1 | byte-identical for the header, the block directory, the dictionary and the postings; identical after decompression for the block payloads | the compressed bytes go unchecked, so a difference in compression ratio is invisible to the fixture |
| 2 | byte-identical everywhere, with the block payloads stored uncompressed | gives up the reason the index is small, which is three quarters of the design |
| 3 | byte-identical everywhere, with a cgo zstd on the Go side | contradicts `CGO_ENABLED=0`, which is the whole static-binary story |
| 4 | behavioural only: same corpus in, same query answers out | this is what the golden-file strategy exists to be stronger than |

**What decides between it and option 2.** Only the measured size difference, and
only if it turns out that the pure-Go encoder's ratio on filename corpora is far
enough off libzstd's to matter against the 4 GB metadata budget. That is a
Phase 8c measurement and it is a different question from this one.

**Taken for now: option 1.** The bytes a format defines are checked exactly; the
bytes an encoder chooses are checked through what they decode to. That keeps
every claim §4.3.1 makes about ordering, layout, dictionary construction and
posting encoding, which is where the seven hand-written algorithms live, and
gives up only the claim that two compressors agree. The block directory records
each block's uncompressed length, so a payload that decompresses to the wrong
size is still caught by the format's own check.

**Reopen by** Phase 8c, which writes `base.idx` in Go and is where the
comparison is actually run.

