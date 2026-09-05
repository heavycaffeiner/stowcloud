// web/src/lib/crypto/e2ee.test.ts: the browser side of the rclone-crypt-v1
// round trip. Cross-implementation compatibility against real rclone is not
// asserted here: asserting encrypt against this module's own decrypt proves
// nothing about a second implementation.
import { afterEach, describe, expect, it } from 'vitest'
import {
  checkVerifier,
  ciphertextSpanForRange,
  decryptDownload,
  decryptPlaintextRange,
  decryptStream,
  deriveKeys,
  encryptForUpload,
  FileTooLargeError,
  generateSalt,
  isUnlocked,
  lock,
  LockedSessionError,
  makeVerifier,
  MAX_ENCRYPTABLE_BYTES,
  plaintextSizeFromCiphertextSize,
  unlock,
  WrongPassphraseError
} from './e2ee'

/** `checkVerifier` takes bytes, but the wire (and `makeVerifier`) carries
 *  standard base64: decodes it back for the one test that calls
 *  `checkVerifier` directly rather than through `unlock`. */
function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

afterEach(() => {
  lock()
})

/** An unlocked session for a fresh salt, shared by every describe block
 *  below that needs one rather than re-deriving its own keys and verifier. */
async function unlockedFixture(): Promise<{ salt: string; verifier: string }> {
  const salt = generateSalt()
  const keys = await deriveKeys('correct horse battery staple', salt)
  const verifier = await makeVerifier(keys)
  await unlock('correct horse battery staple', salt, verifier)
  return { salt, verifier }
}

describe('generateSalt', () => {
  it('produces exactly 22 base64url characters, matching the server-side check', () => {
    const salt = generateSalt()
    expect(salt).toHaveLength(22)
    expect(salt).toMatch(/^[A-Za-z0-9_-]{22}$/)
  })

  it('never repeats across calls', () => {
    const salts = new Set(Array.from({ length: 20 }, () => generateSalt()))
    expect(salts.size).toBe(20)
  })
})

describe('deriveKeys + makeVerifier + checkVerifier + unlock (the enable/unlock round trip)', () => {
  it('unlocks with the passphrase that made the verifier', async () => {
    const salt = generateSalt()
    const keys = await deriveKeys('correct horse battery staple', salt)
    const verifier = await makeVerifier(keys)

    expect(isUnlocked()).toBe(false)
    await unlock('correct horse battery staple', salt, verifier)
    expect(isUnlocked()).toBe(true)
  })

  it('rejects the wrong passphrase rather than unlocking under it', async () => {
    const salt = generateSalt()
    const keys = await deriveKeys('correct horse battery staple', salt)
    const verifier = await makeVerifier(keys)

    await expect(unlock('an entirely different passphrase', salt, verifier)).rejects.toThrow(WrongPassphraseError)
    expect(isUnlocked()).toBe(false)
  })

  it('rejects the right passphrase under the wrong salt', async () => {
    const keys = await deriveKeys('correct horse battery staple', generateSalt())
    const verifier = await makeVerifier(keys)

    await expect(unlock('correct horse battery staple', generateSalt(), verifier)).rejects.toThrow(WrongPassphraseError)
  })

  it('checkVerifier itself reports false rather than throwing for a mismatched key', async () => {
    const salt = generateSalt()
    const rightKeys = await deriveKeys('right passphrase', salt)
    const wrongKeys = await deriveKeys('wrong passphrase', salt)
    const verifier = base64ToBytes(await makeVerifier(rightKeys))

    expect(await checkVerifier(rightKeys, verifier)).toBe(true)
    expect(await checkVerifier(wrongKeys, verifier)).toBe(false)
  })

  it('lock() drops the unlocked state', async () => {
    const salt = generateSalt()
    const keys = await deriveKeys('correct horse battery staple', salt)
    const verifier = await makeVerifier(keys)
    await unlock('correct horse battery staple', salt, verifier)
    expect(isUnlocked()).toBe(true)

    lock()
    expect(isUnlocked()).toBe(false)
  })
})

describe('encryptForUpload + decryptDownload', () => {
  it('round-trips arbitrary bytes through an unlocked session', async () => {
    const { salt } = await unlockedFixture()
    const original = new TextEncoder().encode('the quick brown fox jumps over the lazy dog')

    const ciphertext = await encryptForUpload(original, salt)
    expect(Array.from(ciphertext)).not.toEqual(Array.from(original))

    const plaintext = await decryptDownload(ciphertext, salt)
    // `Array.from`, not a direct Uint8Array `toEqual`: `TextEncoder.encode`
    // returns a Uint8Array from a different realm than this module's under
    // jsdom, and chai's deep equality tells those apart by prototype even
    // when every byte matches.
    expect(Array.from(plaintext)).toEqual(Array.from(original))
  })

  // Exercises the block boundary and the nonce carry rclone-crypt requires:
  // a plaintext just over one 65536-byte block forces a second block, whose
  // nonce is the first one's incremented, not a fresh random one.
  it('round-trips a file spanning multiple 65536-byte blocks', async () => {
    const { salt } = await unlockedFixture()
    const original = new Uint8Array(65536 + 100)
    for (let i = 0; i < original.length; i++) original[i] = i & 0xff

    const ciphertext = await encryptForUpload(original, salt)
    // header(32) + two blocks: 65536+16 and 100+16
    expect(ciphertext.byteLength).toBe(32 + (65536 + 16) + (100 + 16))

    const plaintext = await decryptDownload(ciphertext, salt)
    expect(plaintext).toEqual(original)
  })

  it('round-trips a zero-length file as a bare 32-byte header', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new Uint8Array(0), salt)
    expect(ciphertext.byteLength).toBe(32)

    const plaintext = await decryptDownload(ciphertext, salt)
    expect(plaintext.byteLength).toBe(0)
  })

  it('refuses to encrypt without an unlocked session', async () => {
    expect(isUnlocked()).toBe(false)
    await expect(encryptForUpload(new Uint8Array([1, 2, 3]), generateSalt()))
      .rejects.toThrow(LockedSessionError)
  })

  it('refuses to decrypt without an unlocked session, distinctly from a corrupt file', async () => {
    expect(isUnlocked()).toBe(false)
    await expect(decryptDownload(new Uint8Array(40), generateSalt()))
      .rejects.toThrow(LockedSessionError)
  })

  // The silent data-loss case this binding exists for: with one share
  // unlocked, encrypting for a different one must refuse rather than use the
  // key in hand, which would write a file the other share's own passphrase
  // could never open.
  it('refuses to encrypt for a share other than the unlocked one', async () => {
    const { salt } = await unlockedFixture()
    const other = generateSalt()
    expect(other).not.toBe(salt)
    expect(isUnlocked(salt)).toBe(true)
    expect(isUnlocked(other)).toBe(false)
    await expect(encryptForUpload(new Uint8Array([1, 2, 3]), other))
      .rejects.toThrow(LockedSessionError)
    await expect(decryptDownload(new Uint8Array(40), other))
      .rejects.toThrow(LockedSessionError)
  })

  it('fails a tampered ciphertext rather than returning corrupted plaintext', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new TextEncoder().encode('sensitive contents'), salt)
    const tampered = ciphertext.slice()
    tampered[tampered.length - 1] ^= 0xff // flip a bit inside the last block's tag/ciphertext

    await expect(decryptDownload(tampered, salt)).rejects.toThrow()
  })

  it('refuses a body over MAX_ENCRYPTABLE_BYTES before touching any cipher', async () => {
    const { salt } = await unlockedFixture()
    const oversized = new Uint8Array(MAX_ENCRYPTABLE_BYTES + 1)
    await expect(encryptForUpload(oversized, salt)).rejects.toThrow(FileTooLargeError)
  })
})

async function collectStream(stream: ReadableStream<Uint8Array>): Promise<Uint8Array> {
  const reader = stream.getReader()
  const parts: Uint8Array[] = []
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    parts.push(value)
  }
  const total = parts.reduce((n, p) => n + p.length, 0)
  const out = new Uint8Array(total)
  let offset = 0
  for (const p of parts) {
    out.set(p, offset)
    offset += p.length
  }
  return out
}

/** Delivers `bytes` to a stream in fixed-size pieces rather than as one
 *  chunk, so `decryptStream`'s own buffering is what reassembles the
 *  65552-byte blocks, not an accidental one-chunk-per-block alignment. */
function streamFrom(bytes: Uint8Array, chunkSize = 4096): ReadableStream<Uint8Array> {
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (let i = 0; i < bytes.length; i += chunkSize) {
        controller.enqueue(bytes.subarray(i, Math.min(i + chunkSize, bytes.length)))
      }
      controller.close()
    }
  })
}

describe('decryptStream', () => {
  it.each([0, 1, 65535, 65536, 65537, 1024 * 1024 + 777])(
    'matches decryptDownload byte-for-byte for a %i-byte plaintext',
    async (size) => {
      const { salt } = await unlockedFixture()
      const original = new Uint8Array(size)
      for (let i = 0; i < original.length; i++) original[i] = i & 0xff
      const ciphertext = await encryptForUpload(original, salt)

      const viaWhole = await decryptDownload(ciphertext, salt)
      const viaStream = await collectStream(streamFrom(ciphertext).pipeThrough(decryptStream(salt)))

      expect(Array.from(viaStream)).toEqual(Array.from(viaWhole))
      expect(Array.from(viaStream)).toEqual(Array.from(original))
    }
  )

  it('rejects a tampered ciphertext rather than emitting corrupted plaintext', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new TextEncoder().encode('sensitive contents'), salt)
    const tampered = ciphertext.slice()
    tampered[tampered.length - 1] ^= 0xff

    await expect(collectStream(streamFrom(tampered).pipeThrough(decryptStream(salt)))).rejects.toThrow()
  })

  it('rejects input truncated mid-block (a full block cut a few bytes short)', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new Uint8Array(65536 + 100), salt)
    const truncated = ciphertext.slice(0, ciphertext.length - 5)

    await expect(collectStream(streamFrom(truncated).pipeThrough(decryptStream(salt)))).rejects.toThrow()
  })

  it('rejects a final fragment shorter than the 17-byte minimum block (16-byte tag + 1 plaintext byte)', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new Uint8Array(5), salt) // 32 + 16 + 5 = 53 bytes total
    const truncated = ciphertext.slice(0, 40) // header(32) + 8 bytes: short of a real block

    await expect(collectStream(streamFrom(truncated).pipeThrough(decryptStream(salt)))).rejects.toThrow()
  })

  it('rejects a stream that ends before the 32-byte header is complete', async () => {
    const { salt } = await unlockedFixture()
    await expect(collectStream(streamFrom(new Uint8Array(10)).pipeThrough(decryptStream(salt)))).rejects.toThrow()
  })

  it('refuses synchronously, at construction, when the share named is not the unlocked one', async () => {
    expect(isUnlocked()).toBe(false)
    expect(() => decryptStream(generateSalt())).toThrow(LockedSessionError)

    const { salt } = await unlockedFixture()
    const other = generateSalt()
    expect(other).not.toBe(salt)
    expect(() => decryptStream(other)).toThrow(LockedSessionError)
  })

  it('rejects a stream truncated exactly on a block boundary when the true size is known', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new Uint8Array(65536 * 2), salt) // two full blocks, no short tail
    const truncated = ciphertext.slice(0, 32 + (65536 + 16)) // header + only the first full block

    await expect(
      collectStream(streamFrom(truncated).pipeThrough(decryptStream(salt, ciphertext.length)))
    ).rejects.toThrow()
  })

  it('rejects a stream that stops right after the header when the true size is known', async () => {
    const { salt } = await unlockedFixture()
    const ciphertext = await encryptForUpload(new Uint8Array(100), salt)
    const truncated = ciphertext.slice(0, 32) // header + nonce, no blocks: looks like an empty file

    await expect(
      collectStream(streamFrom(truncated).pipeThrough(decryptStream(salt, ciphertext.length)))
    ).rejects.toThrow()
  })

  it('accepts a complete stream when the true ciphertext size is given', async () => {
    const { salt } = await unlockedFixture()
    const original = new Uint8Array(65536 + 100)
    for (let i = 0; i < original.length; i++) original[i] = i & 0xff
    const ciphertext = await encryptForUpload(original, salt)

    const plaintext = await collectStream(streamFrom(ciphertext).pipeThrough(decryptStream(salt, ciphertext.length)))
    expect(Array.from(plaintext)).toEqual(Array.from(original))
  })
})

describe('plaintextSizeFromCiphertextSize', () => {
  it("is the inverse of encryptForUpload's own output length, across block boundaries", async () => {
    const { salt } = await unlockedFixture()
    for (const size of [0, 1, 65535, 65536, 65537, 1024 * 1024 + 777]) {
      const ciphertext = await encryptForUpload(new Uint8Array(size), salt)
      expect(plaintextSizeFromCiphertextSize(ciphertext.byteLength)).toBe(size)
    }
  })

  it('throws for a ciphertext shorter than the 32-byte header+nonce', () => {
    expect(() => plaintextSizeFromCiphertextSize(31)).toThrow()
  })

  it('throws when the remainder after the header or after full blocks is too short to be a partial block (1..16 bytes)', () => {
    const headerSize = 32
    const fullBlockCiphertextSize = 65536 + 16
    for (const tail of [1, 8, 15, 16]) {
      expect(() => plaintextSizeFromCiphertextSize(headerSize + tail)).toThrow()
      expect(() => plaintextSizeFromCiphertextSize(headerSize + fullBlockCiphertextSize + tail)).toThrow()
    }
  })
})

describe('ciphertextSpanForRange', () => {
  // Two full 65536-byte blocks plus a short 100-byte last block: block 0
  // covers ciphertext [32, 65584), block 1 [65584, 131136), block 2 (the
  // last, short one) [131136, 131252).
  const PLAINTEXT_SIZE = 65536 * 2 + 100

  it('the first block: a small range at the very start fetches the whole first block', () => {
    expect(ciphertextSpanForRange(0, 100, PLAINTEXT_SIZE)).toEqual({ offset: 32, length: 65536 + 16 })
  })

  it('a mid-file range entirely inside one later block fetches only that block', () => {
    expect(ciphertextSpanForRange(70000, 70100, PLAINTEXT_SIZE)).toEqual({ offset: 32 + (65536 + 16), length: 65536 + 16 })
  })

  it('a range spanning a block boundary fetches both blocks it touches', () => {
    expect(ciphertextSpanForRange(65500, 65600, PLAINTEXT_SIZE)).toEqual({ offset: 32, length: (65536 + 16) * 2 })
  })

  it('a range ending exactly on a block boundary does not pull in the next block', () => {
    // [65486, 65536) ends exactly at the first/second block boundary; the
    // half-open range excludes byte 65536 itself, so only block 0 is named.
    expect(ciphertextSpanForRange(65536 - 50, 65536, PLAINTEXT_SIZE)).toEqual({ offset: 32, length: 65536 + 16 })
  })

  it('the last, short block is fetched at its own real (not full) ciphertext length', () => {
    expect(ciphertextSpanForRange(131100, 131150, PLAINTEXT_SIZE)).toEqual({
      offset: 32 + (65536 + 16) * 2,
      length: 16 + 100
    })
  })

  it('throws on an empty or out-of-bounds range', () => {
    expect(() => ciphertextSpanForRange(10, 10, PLAINTEXT_SIZE)).toThrow(RangeError)
    expect(() => ciphertextSpanForRange(-1, 10, PLAINTEXT_SIZE)).toThrow(RangeError)
    expect(() => ciphertextSpanForRange(0, PLAINTEXT_SIZE + 1, PLAINTEXT_SIZE)).toThrow(RangeError)
  })
})

describe('decryptPlaintextRange', () => {
  const PLAINTEXT_SIZE = 65536 * 2 + 100

  async function encryptedFixture(): Promise<{ salt: string; original: Uint8Array; ciphertext: Uint8Array; nonce0: Uint8Array }> {
    const { salt } = await unlockedFixture()
    const original = new Uint8Array(PLAINTEXT_SIZE)
    for (let i = 0; i < original.length; i++) original[i] = i & 0xff
    const ciphertext = await encryptForUpload(original, salt)
    return { salt, original, ciphertext, nonce0: ciphertext.subarray(8, 32) }
  }

  it.each([
    [0, 100],
    [70000, 70100],
    [65500, 65600],
    [65486, 65536],
    [131100, 131150]
  ])('decrypts and trims [%i, %i) to exactly the requested plaintext bytes', async (start, end) => {
    const { salt, original, ciphertext, nonce0 } = await encryptedFixture()
    const span = ciphertextSpanForRange(start, end, PLAINTEXT_SIZE)
    const spanBytes = ciphertext.subarray(span.offset, span.offset + span.length)

    const got = decryptPlaintextRange(salt, nonce0, start, end, spanBytes)

    expect(Array.from(got)).toEqual(Array.from(original.subarray(start, end)))
  })

  it('refuses when the salt named is not the unlocked share', async () => {
    const { ciphertext, nonce0 } = await encryptedFixture()
    const span = ciphertextSpanForRange(0, 100, PLAINTEXT_SIZE)
    const spanBytes = ciphertext.subarray(span.offset, span.offset + span.length)

    expect(() => decryptPlaintextRange(generateSalt(), nonce0, 0, 100, spanBytes)).toThrow(LockedSessionError)
  })

  it('rejects a span truncated exactly on a block boundary rather than returning short plaintext', async () => {
    const { salt, ciphertext, nonce0 } = await encryptedFixture()
    const span = ciphertextSpanForRange(0, PLAINTEXT_SIZE, PLAINTEXT_SIZE)
    const spanBytes = ciphertext.subarray(span.offset, span.offset + span.length)
    const lastBlockCiphertextSize = 16 + 100
    const truncatedSpan = spanBytes.subarray(0, spanBytes.length - lastBlockCiphertextSize)

    expect(() => decryptPlaintextRange(salt, nonce0, 0, PLAINTEXT_SIZE, truncatedSpan)).toThrow()
  })
})
