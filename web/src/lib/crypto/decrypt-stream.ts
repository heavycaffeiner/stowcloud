// web/src/lib/crypto/decrypt-stream.ts: a small buffered byte accumulator,
// deliberately knowing nothing about rclone-crypt: it exists to turn a
// sequence of arbitrarily-sized byte chunks into exact-size slices on
// request, which is the one piece of machinery every block-framed reader of
// this share's ciphertext needs and none of them should reimplement.
//
// Two callers, both decrypting the same 65552-byte (16-byte tag + up to
// 65536 ciphertext) SecretBox blocks but driven in opposite directions:
//   - `e2ee.ts`'s `decryptStream` is *pushed* chunks by a `TransformStream`
//     (`transform(chunk, controller)`), and drains full blocks as they
//     become available.
//   - `download-sw.ts`'s media-range responder *pulls* chunks from a
//     `fetch()` response's reader in a loop, decrypting one bounded ciphertext
//     span rather than an unbounded stream.
// Both push whatever they receive into one of these and take back exactly
// the byte counts rclone-crypt's own framing calls for, however many
// pushes it took to gather them.
import { concatBytes } from '@noble/ciphers/utils.js'

export class ByteAccumulator {
  private chunks: Uint8Array[] = []
  private total = 0

  /** Bytes currently held, however many chunks they arrived in. */
  get length(): number {
    return this.total
  }

  push(chunk: Uint8Array): void {
    if (chunk.length === 0) return
    this.chunks.push(chunk)
    this.total += chunk.length
  }

  /**
   * Removes and returns exactly `n` bytes from the front of the buffer.
   *
   * Throws rather than returning a short read: every caller here already
   * checks `length` first (a `TransformStream`'s `transform` waits for more
   * input, a stream's `flush` treats a short remainder as truncation), so a
   * `take` past what is buffered is this module's own bug, not an input to
   * handle gracefully.
   */
  take(n: number): Uint8Array {
    if (n > this.total) {
      throw new RangeError(`ByteAccumulator.take(${n}): only ${this.total} bytes are buffered`)
    }
    const merged = this.chunks.length === 1 ? this.chunks[0] : concatBytes(...this.chunks)
    const taken = merged.subarray(0, n)
    const rest = merged.subarray(n)
    this.chunks = rest.length > 0 ? [rest] : []
    this.total = rest.length
    return taken
  }

  /** Removes and returns every buffered byte, however many remain: used at
   *  end of input for a final, possibly short, block. */
  drain(): Uint8Array {
    return this.take(this.total)
  }
}
