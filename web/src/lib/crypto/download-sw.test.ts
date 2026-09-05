// web/src/lib/crypto/download-sw.test.ts: the page side of the download and
// media Service Worker protocols. jsdom has no real `navigator.serviceWorker`
// by default, which this file uses rather than works around for the
// "no usable Service Worker" tests (the buffered fallback, `downloadEncryptedFile`/
// `downloadEncryptedFolder`'s own pipelines); a fake container is installed only
// for the media round-trip tests, in their own module instance
// (`vi.resetModules`) so the two groups never fight over the module-scoped
// registration cache `swReady` keeps.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Entry, ShareEncryption } from '../api/types'

const contentUrl = vi.fn((entry: { content?: string }) => `/api/v1/files/read?claim=${entry.content}`)
const list = vi.fn()
vi.mock('../api/client', () => ({
  api: {
    contentUrl: (entry: { content?: string }) => contentUrl(entry),
    list: (path: string, opts: unknown) => list(path, opts)
  }
}))

const encryptionForLabel = vi.fn()
vi.mock('./encrypted-shares', async () => {
  const actual = await vi.importActual<typeof import('./encrypted-shares')>('./encrypted-shares')
  return { ...actual, encryptionForLabel: (label: string) => encryptionForLabel(label) }
})

import { deriveKeys, encryptForUpload, FileTooLargeError, generateSalt, lock, LockedSessionError, makeVerifier, unlock } from './e2ee'
import { downloadEncryptedFile, downloadEncryptedFolder, registerMediaSource, releaseMediaSource, streamToDownload } from './download-sw'

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
    body: new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(bytes)
        controller.close()
      }
    }),
    arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
  } as unknown as Response
}

/** Serves ranged reads of one in-memory ciphertext buffer, the way the real
 *  `/files/read?claim=...` endpoint does (content.go). */
function rangedFetch(ciphertext: Uint8Array): typeof fetch {
  return (async (_url: RequestInfo | URL, init?: RequestInit) => {
    const headers = init?.headers as Record<string, string> | undefined
    const range = headers?.Range
    if (!range) return fakeResponse(ciphertext)
    const m = /^bytes=(\d+)-(\d+)$/.exec(range)
    if (!m) throw new Error(`test mock does not understand Range ${range}`)
    return fakeResponse(ciphertext.subarray(Number(m[1]), Number(m[2]) + 1))
  }) as unknown as typeof fetch
}

/** A Blob URL stand-in that hands the test the exact `Blob` it was asked to
 *  publish, so the assertion can `await blob.arrayBuffer()` directly rather
 *  than guessing when a background `.then()` has settled. */
function stubBlobUrlCapture(): { blob: () => Promise<Blob> } {
  const { promise, resolve } = Promise.withResolvers<Blob>()
  vi.stubGlobal('URL', {
    ...URL,
    createObjectURL: (b: Blob) => {
      resolve(b)
      return 'blob:fake'
    },
    revokeObjectURL: vi.fn()
  })
  return { blob: () => promise }
}

async function unlockedShare(): Promise<ShareEncryption> {
  const salt = generateSalt()
  const keys = await deriveKeys('correct horse battery staple', salt)
  const verifier = await makeVerifier(keys)
  await unlock('correct horse battery staple', salt, verifier)
  return { share: 1, labels: ['vault'], scheme: 'rclone-crypt-v1', salt, verifier, createdNs: 0 }
}

beforeEach(() => {
  contentUrl.mockClear()
  list.mockReset()
  encryptionForLabel.mockReset()
})

afterEach(() => {
  lock()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('registerMediaSource / releaseMediaSource', () => {
  it('mints a fresh token and a matching /sc-media/ URL each call', () => {
    const entry = makeEntry({ path: '/vault/a.bin', size: 100 })
    const a = registerMediaSource(entry, 'salt', 'video/mp4')
    const b = registerMediaSource(entry, 'salt', 'video/mp4')
    expect(a.token).not.toBe(b.token)
    expect(a.url).toBe(`/sc-media/${a.token}`)
    expect(b.url).toBe(`/sc-media/${b.token}`)
    releaseMediaSource(a.token)
    releaseMediaSource(b.token)
  })
})

describe('streamToDownload (no Service Worker in this environment: buffered fallback)', () => {
  function streamOf(bytes: number[]): ReadableStream<Uint8Array> {
    return new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(bytes))
        controller.close()
      }
    })
  }

  it('buffers the stream into a Blob and clicks a hidden download anchor', async () => {
    const capture = stubBlobUrlCapture()
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {})

    await streamToDownload('report.txt', streamOf([1, 2, 3]))

    const blob = await capture.blob()
    expect(Array.from(new Uint8Array(await blob.arrayBuffer()))).toEqual([1, 2, 3])
    expect(clickSpy).toHaveBeenCalledTimes(1)
    clickSpy.mockRestore()
  })

  it('refuses a stream declaring a size over MAX_ENCRYPTABLE_BYTES before reading anything', async () => {
    const { MAX_ENCRYPTABLE_BYTES } = await import('./e2ee')
    await expect(streamToDownload('big.bin', streamOf([1]), MAX_ENCRYPTABLE_BYTES + 1)).rejects.toThrow(FileTooLargeError)
  })

  it('refuses once the buffered total crosses MAX_ENCRYPTABLE_BYTES, even with no declared size', async () => {
    const { MAX_ENCRYPTABLE_BYTES } = await import('./e2ee')
    const chunk = new Uint8Array(1024 * 1024)
    const oversizedStream = new ReadableStream<Uint8Array>({
      pull(controller) {
        controller.enqueue(chunk)
      }
    })
    await expect(streamToDownload('big.bin', oversizedStream)).rejects.toThrow(FileTooLargeError)
    expect(chunk.length * 2).toBeLessThan(MAX_ENCRYPTABLE_BYTES) // sanity: the loop, not the first chunk, trips the refusal
  })
})

describe('downloadEncryptedFile', () => {
  it('fetches the ciphertext, decrypts it through decryptStream, and hands the plaintext to the download', async () => {
    const encryption = await unlockedShare()
    encryptionForLabel.mockResolvedValue(encryption)
    const original = new TextEncoder().encode('the quick brown fox jumps over the lazy dog')
    const ciphertext = await encryptForUpload(original, encryption.salt)
    vi.stubGlobal('fetch', rangedFetch(ciphertext))

    const capture = stubBlobUrlCapture()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {})

    const entry = makeEntry({ path: '/vault/message.txt', size: ciphertext.byteLength })
    await downloadEncryptedFile(entry)

    const blob = await capture.blob()
    expect(Array.from(new Uint8Array(await blob.arrayBuffer()))).toEqual(Array.from(original))
  })

  it('refuses before fetching anything when the share is not the unlocked one', async () => {
    lock()
    encryptionForLabel.mockResolvedValue({
      share: 1,
      labels: ['vault'],
      scheme: 'rclone-crypt-v1',
      salt: generateSalt(),
      verifier: 'x',
      createdNs: 0
    })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const entry = makeEntry({ path: '/vault/message.txt', size: 100 })
    await expect(downloadEncryptedFile(entry)).rejects.toThrow(LockedSessionError)
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('downloadEncryptedFolder', () => {
  it('walks every page of the folder and zips every file it finds, decrypted, under its relative name', async () => {
    const encryption = await unlockedShare()
    encryptionForLabel.mockResolvedValue(encryption)

    const fileA = new TextEncoder().encode('file a contents')
    const fileB = new TextEncoder().encode('file b contents, in a subfolder')
    const cipherA = await encryptForUpload(fileA, encryption.salt)
    const cipherB = await encryptForUpload(fileB, encryption.salt)

    const entryA = makeEntry({ path: '/vault/folder/a.txt', size: cipherA.byteLength })
    const entryB = makeEntry({ path: '/vault/folder/sub/b.txt', size: cipherB.byteLength })
    const subDir = makeEntry({ path: '/vault/folder/sub', size: 0, kind: 'dir' })

    list.mockImplementation(async (path: string) => {
      if (path === '/vault/folder') {
        return { entries: [entryA, subDir], cursor: null, total: 2, dirs: 1, dir_etag: 'e', dir_etag_weak: false, dir_perms: [] }
      }
      if (path === '/vault/folder/sub') {
        return { entries: [entryB], cursor: null, total: 1, dirs: 0, dir_etag: 'e', dir_etag_weak: false, dir_perms: [] }
      }
      throw new Error(`unexpected list(${path})`)
    })

    vi.stubGlobal(
      'fetch',
      (async (url: RequestInfo | URL) => {
        const s = String(url)
        if (s.includes(entryA.content!)) return fakeResponse(cipherA)
        if (s.includes(entryB.content!)) return fakeResponse(cipherB)
        throw new Error(`unexpected fetch ${s}`)
      }) as unknown as typeof fetch
    )

    const capture = stubBlobUrlCapture()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {})

    await downloadEncryptedFolder('/vault/folder')

    const blob = await capture.blob()
    const bytes = new Uint8Array(await blob.arrayBuffer())
    // A real zip: local file header signature "PK\x03\x04" at the start.
    expect(Array.from(bytes.subarray(0, 4))).toEqual([0x50, 0x4b, 0x03, 0x04])
    const text = new TextDecoder('latin1').decode(bytes)
    expect(text).toContain('a.txt')
    expect(text).toContain('sub/b.txt')
  })

  it('filters an unsafe entry name out of the zip rather than embedding a path traversal', async () => {
    const encryption = await unlockedShare()
    encryptionForLabel.mockResolvedValue(encryption)

    const fileGood = new TextEncoder().encode('good file contents')
    const fileEvil = new TextEncoder().encode('evil file contents')
    const cipherGood = await encryptForUpload(fileGood, encryption.salt)
    const cipherEvil = await encryptForUpload(fileEvil, encryption.salt)

    const entryGood = makeEntry({ path: '/vault/folder/good.txt', size: cipherGood.byteLength })
    // A hostile listing entry escaping the folder via a `../` segment: the
    // relative name this produces must never reach the zip, or the fetch
    // below (which the filter should also skip).
    const entryEvil = makeEntry({ path: '/vault/folder/../evil.txt', size: cipherEvil.byteLength })

    list.mockImplementation(async (path: string) => {
      if (path === '/vault/folder') {
        return { entries: [entryGood, entryEvil], cursor: null, total: 2, dirs: 0, dir_etag: 'e', dir_etag_weak: false, dir_perms: [] }
      }
      throw new Error(`unexpected list(${path})`)
    })

    vi.stubGlobal(
      'fetch',
      (async (url: RequestInfo | URL) => {
        const s = String(url)
        if (s.includes(entryGood.content!)) return fakeResponse(cipherGood)
        throw new Error(`unexpected fetch ${s}`)
      }) as unknown as typeof fetch
    )

    const capture = stubBlobUrlCapture()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {})

    await downloadEncryptedFolder('/vault/folder')

    const blob = await capture.blob()
    const bytes = new Uint8Array(await blob.arrayBuffer())
    const text = new TextDecoder('latin1').decode(bytes)
    expect(text).toContain('good.txt')
    expect(text).not.toContain('evil.txt')
  })
})

describe('the media Range responder (a fake navigator.serviceWorker)', () => {
  function installFakeServiceWorker() {
    const fakeRegistration = { active: {} }
    let listener: ((event: MessageEvent) => void) | null = null
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        register: async () => fakeRegistration,
        ready: Promise.resolve(fakeRegistration),
        addEventListener: (type: string, l: (event: MessageEvent) => void) => {
          if (type === 'message') listener = l
        }
      }
    })
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
    return { deliver: (data: unknown, port: MessagePort) => listener!({ data, ports: [port] } as unknown as MessageEvent) }
  }

  /** A `MessagePort` stand-in whose `reply` resolves with whatever the
   *  worker's `onSwMessage` posts back, so the test awaits that real signal
   *  instead of a guessed delay. */
  function fakePort(): { port: MessagePort; reply: Promise<unknown> } {
    const { promise, resolve } = Promise.withResolvers<unknown>()
    return { port: { postMessage: (msg: unknown) => resolve(msg) } as unknown as MessagePort, reply: promise }
  }

  async function freshModules(): Promise<{ e2ee: typeof import('./e2ee'); downloadSw: typeof import('./download-sw') }> {
    vi.resetModules()
    const e2ee = await import('./e2ee')
    const downloadSw = await import('./download-sw')
    return { e2ee, downloadSw }
  }

  it('answers a sub-range request with the decrypted, trimmed bytes and the right bounds', async () => {
    const sw = installFakeServiceWorker()
    const { e2ee, downloadSw } = await freshModules()
    await downloadSw.swReady()

    const salt = e2ee.generateSalt()
    const keys = await e2ee.deriveKeys('correct horse battery staple', salt)
    const verifier = await e2ee.makeVerifier(keys)
    await e2ee.unlock('correct horse battery staple', salt, verifier)

    const original = new Uint8Array(65536 + 500)
    for (let i = 0; i < original.length; i++) original[i] = i & 0xff
    const ciphertext = await e2ee.encryptForUpload(original, salt)
    vi.stubGlobal('fetch', rangedFetch(ciphertext))

    const entry = makeEntry({ path: '/vault/video.bin', size: ciphertext.byteLength })
    const { token } = downloadSw.registerMediaSource(entry, salt, 'video/mp4')

    const { port, reply } = fakePort()
    sw.deliver({ kind: 'sc-media-request', token, start: 65500, end: 65600 }, port)
    const got = (await reply) as {
      ok: true
      start: number
      end: number
      totalSize: number
      contentType: string
      stream: ReadableStream<Uint8Array>
    }

    expect(got.ok).toBe(true)
    expect(got.start).toBe(65500)
    expect(got.end).toBe(65600)
    expect(got.totalSize).toBe(original.length)
    expect(got.contentType).toBe('video/mp4')

    const reader = got.stream.getReader()
    const parts: Uint8Array[] = []
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      parts.push(value)
    }
    const merged = parts.length === 1 ? parts[0] : parts.reduce((acc, p) => Uint8Array.from([...acc, ...p]), new Uint8Array())
    expect(Array.from(merged)).toEqual(Array.from(original.subarray(65500, 65601)))
  })

  it('answers a failure reply for an unknown or released token', async () => {
    const sw = installFakeServiceWorker()
    const { downloadSw } = await freshModules()
    await downloadSw.swReady()

    const entry = makeEntry({ path: '/vault/video.bin', size: 200 })
    const { token } = downloadSw.registerMediaSource(entry, 'salt', 'video/mp4')
    downloadSw.releaseMediaSource(token)

    const { port, reply } = fakePort()
    sw.deliver({ kind: 'sc-media-request', token }, port)

    expect(await reply).toEqual({ ok: false, reason: 'unknown-token' })
  })
})
