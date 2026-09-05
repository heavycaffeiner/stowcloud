// web/src/lib/crypto/zip-listing.test.ts: lists a zip built with zip.js's
// own writer, encrypted with this module's own e2ee.ts, back out through
// `listEncryptedArchive`'s range reader, against a counted `fetch` stub
// standing in for `/api/v1/files/read`. The counting is the point: proving
// this never scales with the archive's own size is only a claim until
// something asserts a small, fixed number of ranged reads regardless of how
// large the encrypted file behind them is.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'
import { ZipWriter, Uint8ArrayWriter, Uint8ArrayReader } from '@zip.js/zip.js'
import type { Entry } from '../api/types'

const contentUrl = vi.fn((entry: { content?: string }) => `/api/v1/files/read?claim=${entry.content}`)
vi.mock('../api/client', () => ({
  api: { contentUrl: (entry: { content?: string }) => contentUrl(entry) }
}))

import { deriveKeys, encryptForUpload, generateSalt, lock, LockedSessionError, makeVerifier, unlock } from './e2ee'
import { listEncryptedArchive } from './zip-listing'

function makeEntry(overrides: Partial<Entry> & { path: string; size: number }): Entry {
  return {
    name: overrides.path.split('/').pop() ?? overrides.path,
    content: overrides.path,
    kind: 'file',
    mtime_ns: '0',
    etag: 'e',
    etag_weak: false,
    perms: { read: true, write: true, create: true, delete: true, rename: true, move: true, share: true, download: true },
    ...overrides
  }
}

function fakeResponse(bytes: Uint8Array): Response {
  return {
    ok: true,
    status: 200,
    body: {} as ReadableStream,
    arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
  } as unknown as Response
}

/** Serves ranged reads of one in-memory ciphertext buffer, the way the real
 *  `/files/read?claim=...` endpoint does (content.go), the same shape
 *  download-sw.test.ts's own `rangedFetch` uses, wrapped in a `vi.fn` here
 *  so the test can count how many times it was actually called. */
function countedRangedFetch(ciphertext: Uint8Array): Mock {
  return vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
    const headers = init?.headers as Record<string, string> | undefined
    const range = headers?.Range
    if (!range) return fakeResponse(ciphertext)
    const m = /^bytes=(\d+)-(\d+)$/.exec(range)
    if (!m) throw new Error(`test mock does not understand Range ${range}`)
    return fakeResponse(ciphertext.subarray(Number(m[1]), Number(m[2]) + 1))
  })
}

async function unlockedShare(): Promise<{ salt: string; verifier: string }> {
  const salt = generateSalt()
  const keys = await deriveKeys('correct horse battery staple', salt)
  const verifier = await makeVerifier(keys)
  await unlock('correct horse battery staple', salt, verifier)
  return { salt, verifier }
}

/** Brute-forces the CP949 (the `euc-kr` decoder label; browsers implement no
 *  legacy multi-byte *encoder*, only decoders, per the WHATWG Encoding
 *  Standard) byte pair for one character by trying every lead/trail byte
 *  until one decodes back to it. Test-fixture plumbing only: this is not
 *  how `zip-listing.ts` decodes anything; it is how this file writes a
 *  legacy-encoded name for `zip-listing.ts` to read back. */
function cp949Encode(text: string): Uint8Array {
  const out: number[] = []
  for (const ch of text) {
    const cp = ch.codePointAt(0) ?? 0
    if (cp < 0x80) {
      out.push(cp)
      continue
    }
    let found: [number, number] | null = null
    for (let lead = 0x81; lead <= 0xfe && !found; lead++) {
      for (let trail = 0x41; trail <= 0xfe; trail++) {
        if (trail === 0x7f) continue
        try {
          if (new TextDecoder('euc-kr', { fatal: true }).decode(new Uint8Array([lead, trail])) === ch) {
            found = [lead, trail]
            break
          }
        } catch {
          /* not a valid CP949 pair; keep searching */
        }
      }
    }
    if (!found) throw new Error(`no CP949 byte pair decodes to ${ch}`)
    out.push(...found)
  }
  return new Uint8Array(out)
}

/** Builds a zip (via zip.js's own writer) with a UTF-8 file, a directory
 *  entry, a CP949-named file (flag clear, raw bytes the legacy encoding),
 *  and one filler file of `fillerBytes` uncompressed bytes, stored rather
 *  than compressed so building a multi-megabyte fixture stays fast. */
async function buildZip(koreanName: string, fillerBytes: number): Promise<Uint8Array> {
  const writer = new ZipWriter(new Uint8ArrayWriter())
  // Uint8ArrayReader rather than TextReader: TextReader reads its string
  // through a Blob's own `.stream()`, which jsdom's Blob polyfill does not
  // implement.
  await writer.add('readme.txt', new Uint8ArrayReader(new TextEncoder().encode('hello world')))
  await writer.add('docs/', undefined)
  await writer.add(koreanName, new Uint8ArrayReader(new TextEncoder().encode('korean content')), {
    useUnicodeFileNames: false,
    encodeText: (text, type) => (type === 'filename' ? cp949Encode(text) : undefined)
  })
  const filler = new Uint8Array(fillerBytes)
  crypto.getRandomValues(filler.subarray(0, Math.min(filler.length, 65536)))
  await writer.add('big.bin', new Uint8ArrayReader(filler), { level: 0 })
  return writer.close()
}

beforeEach(() => {
  contentUrl.mockClear()
})

afterEach(() => {
  lock()
})

describe('listEncryptedArchive', () => {
  it('lists entry names, sizes and directory flags, and decodes a legacy CP949 name as Korean', async () => {
    const { salt } = await unlockedShare()
    // \uD55C\uAE00\uD30C\uC77C.txt is Korean; escaped so no source file
    // outside the i18n catalogues carries literal Korean text.
    const koreanName = '\uD55C\uAE00\uD30C\uC77C.txt'
    const zipBytes = await buildZip(koreanName, 1024)
    const ciphertext = await encryptForUpload(zipBytes, salt)
    vi.stubGlobal('fetch', countedRangedFetch(ciphertext))

    const entry = makeEntry({ path: '/vault/archive.zip', size: ciphertext.byteLength })
    const listing = await listEncryptedArchive(entry, salt)

    expect(listing.truncated).toBe(false)
    expect(listing.skipped).toBe(0)
    const byName = new Map(listing.entries.map((e) => [e.name, e]))
    expect(byName.get('readme.txt')).toMatchObject({ kind: 'file', size: 11 })
    expect(byName.get('docs/')).toMatchObject({ kind: 'dir', size: 0 })
    expect(byName.get('big.bin')).toMatchObject({ kind: 'file', size: 1024 })
    expect(byName.get(koreanName)).toMatchObject({ kind: 'file', size: 14 })
  })

  it('costs a small, fixed number of ranged reads that does not grow with the archive size', async () => {
    const small = await unlockedShare()
    // \uC791\uC740\uD30C\uC77C.txt is Korean; escaped for the same reason
    // as koreanName above.
    const smallZip = await buildZip('\uC791\uC740\uD30C\uC77C.txt', 4096)
    const smallCiphertext = await encryptForUpload(smallZip, small.salt)
    const smallFetch = countedRangedFetch(smallCiphertext)
    vi.stubGlobal('fetch', smallFetch)
    const smallEntry = makeEntry({ path: '/vault/small.zip', size: smallCiphertext.byteLength })
    await listEncryptedArchive(smallEntry, small.salt)
    const smallCalls = smallFetch.mock.calls.length

    lock()
    const big = await unlockedShare()
    // Several times BLOCK_SIZE (65536) of filler so the central directory
    // sits many blocks away from the file's start: a reader that fetched
    // per-block rather than per-request, or that re-fetched the directory
    // it had already read once, would show it here as a call count that
    // grows with this number rather than staying flat.
    // \uD070\uD30C\uC77C.txt is Korean; escaped for the same reason as
    // koreanName above.
    const bigZip = await buildZip('\uD070\uD30C\uC77C.txt', 20 * 65536 + 12345)
    const bigCiphertext = await encryptForUpload(bigZip, big.salt)
    const bigFetch = countedRangedFetch(bigCiphertext)
    vi.stubGlobal('fetch', bigFetch)
    const bigEntry = makeEntry({ path: '/vault/big.zip', size: bigCiphertext.byteLength })
    const listing = await listEncryptedArchive(bigEntry, big.salt)

    expect(listing.entries.map((e) => e.name)).toContain('big.bin')
    expect(smallCalls).toBeLessThanOrEqual(4)
    expect(bigFetch.mock.calls.length).toBeLessThanOrEqual(4)
    expect(bigFetch.mock.calls.length).toBe(smallCalls)
  })

  it('throws LockedSessionError when the share is not unlocked', async () => {
    const { salt } = await unlockedShare() // unlock to encrypt the fixture...
    const zipBytes = await buildZip('locked.txt', 512)
    const ciphertext = await encryptForUpload(zipBytes, salt)
    lock() // ...then lock before listing, like a fresh tab that never unlocked this share
    vi.stubGlobal('fetch', countedRangedFetch(ciphertext))

    const entry = makeEntry({ path: '/vault/locked.zip', size: ciphertext.byteLength })
    await expect(listEncryptedArchive(entry, salt)).rejects.toThrow(LockedSessionError)
  })

  it('reports an unsafe entry name as skipped rather than displaying or refusing it', async () => {
    const { salt } = await unlockedShare()
    const writer = new ZipWriter(new Uint8ArrayWriter())
    await writer.add('readme.txt', new Uint8ArrayReader(new TextEncoder().encode('hello')))
    // A path-escaping name: zip.js's own default validation would refuse to
    // even hand this back, which is why listEncryptedArchive asks for
    // 'tolerant' filename validation and applies its own display filter
    // instead, the same division of labor archive.go has with Go's
    // archive/zip (which never refuses a name at all).
    await writer.add('../evil.txt', new Uint8ArrayReader(new TextEncoder().encode('escape')))
    const zipBytes = await writer.close()
    const ciphertext = await encryptForUpload(zipBytes, salt)
    vi.stubGlobal('fetch', countedRangedFetch(ciphertext))

    const entry = makeEntry({ path: '/vault/unsafe.zip', size: ciphertext.byteLength })
    const listing = await listEncryptedArchive(entry, salt)

    expect(listing.skipped).toBe(1)
    expect(listing.entries.map((e) => e.name)).toEqual(['readme.txt'])
  })
})
