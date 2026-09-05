// web/src/lib/crypto/download-sw.ts: the page half of the download/media
// Service Worker (service-worker.ts). The worker is a dumb pipe; the
// unlocked key and the streams it serves live here instead, never in the
// worker. Two protocols: one-shot download (/sc-download/<id>) and
// reusable, Range-capable media (/sc-media/<token>).
import { equalBytes } from '@noble/ciphers/utils.js'
import { makeZip } from 'client-zip'
import { api } from '../api/client'
import type { Entry } from '../api/types'
import {
  ciphertextSpanForRange,
  decryptPlaintextRange,
  decryptStream,
  FileTooLargeError,
  isUnlocked,
  LockedSessionError,
  MAX_ENCRYPTABLE_BYTES,
  plaintextSizeFromCiphertextSize,
  RCLONE_CRYPT_MAGIC
} from './e2ee'
import { encryptionForLabel, shareLabelOf } from './encrypted-shares'
import { isSafeArchiveName } from './zip-listing'

const SERVICE_WORKER_URL = '/service-worker.js'
// Must match `DOWNLOAD_PREFIX`/`MEDIA_PREFIX` in web/src/service-worker.ts.
const DOWNLOAD_PREFIX = '/sc-download/'
const MEDIA_PREFIX = '/sc-media/'

// registration

let registration: Promise<ServiceWorkerRegistration | null> | null = null

/**
 * Registers the worker once and reuses the same promise for every later
 * call. Resolves `null` (never throws) when the browser has no service
 * worker support at all, when the page is not a secure context (a service
 * worker refuses to register outside one; `127.0.0.1` counts as secure,
 * plain HTTP on any other host does not), or when registration itself
 * fails, so a caller falls back to buffering instead of surfacing a raw
 * exception from browser plumbing nobody asked to see.
 */
export function swReady(): Promise<ServiceWorkerRegistration | null> {
  if (registration) return registration
  registration = (async () => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator) || !window.isSecureContext) return null
    try {
      // Vite's dev server serves `/service-worker.js` as a two-line ESM
      // shim that `import`s the real file, which only a module worker can
      // evaluate; the production build emits an ordinary classic-script
      // bundle instead. `import.meta.env.DEV` is Vite's own compile-time
      // flag for exactly this split, already used the same way for
      // `VITE_API_MOCK` elsewhere in this codebase.
      const reg = await navigator.serviceWorker.register(SERVICE_WORKER_URL, {
        type: import.meta.env.DEV ? 'module' : 'classic'
      })
      await navigator.serviceWorker.ready
      navigator.serviceWorker.addEventListener('message', onSwMessage)
      return reg
    } catch (err) {
      console.error('service worker registration failed; downloads fall back to buffering', err)
      return null
    }
  })()
  return registration
}

let transferableStreamsSupported: boolean | null = null

/** Whether this browser can transfer a `ReadableStream` through
 *  `postMessage`. Safari added it only in 17.4; feature-testing a real
 *  transfer (rather than checking a version string) is the only way to
 *  know for certain, since a browser that cannot do this throws a
 *  `DataCloneError`, not `undefined`. Probed once and cached: constructing
 *  a channel and a stream on every download would be wasteful for an answer
 *  that never changes for the life of the tab. */
function supportsTransferableStreams(): boolean {
  if (transferableStreamsSupported !== null) return transferableStreamsSupported
  try {
    const probe = new ReadableStream()
    new MessageChannel().port1.postMessage(probe, [probe as unknown as Transferable])
    transferableStreamsSupported = true
  } catch {
    transferableStreamsSupported = false
  }
  return transferableStreamsSupported
}

// one-shot download

interface DownloadStartMessage {
  kind: 'sc-download'
  id: string
  filename: string
  size?: number
  stream: ReadableStream<Uint8Array>
}

/** Clicks a hidden `<a download>`, exactly the way `format/download.ts`'s
 *  `triggerUrlDownload` does for the server-ticket download: a `download`
 *  attribute plus a same-tab click starts the browser's own download
 *  manager without navigating away from the app. Kept as its own small copy
 *  here rather than importing that module (it imports this one back to
 *  route an encrypted path to `downloadEncryptedFile`), and a static
 *  import the other way would cycle. */
function clickHiddenDownloadAnchor(url: string, filename: string): void {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
}

/**
 * Hands `stream` to the Service Worker and triggers a browser download of
 * it under `filename`. Falls back to buffering the whole stream into a
 * `Blob` and an object URL when the worker or transferable streams are not
 * available (`fallbackBufferedDownload`), which is why this, unlike
 * `decryptStream` itself, is bounded by `MAX_ENCRYPTABLE_BYTES`: the
 * fallback is the one path here that still holds a whole file in memory,
 * and it needs the same ceiling `encryptForUpload`/`decryptDownload` do for
 * the same reason.
 */
export async function streamToDownload(filename: string, stream: ReadableStream<Uint8Array>, size?: number): Promise<void> {
  const reg = await swReady()
  if (!reg || !reg.active || !supportsTransferableStreams()) {
    await fallbackBufferedDownload(filename, stream, size)
    return
  }
  const id = crypto.randomUUID()
  const message: DownloadStartMessage = { kind: 'sc-download', id, filename, size, stream }
  reg.active.postMessage(message, [stream])
  clickHiddenDownloadAnchor(`${DOWNLOAD_PREFIX}${id}`, filename)
}

/** The bound the buffered fallback refuses over. Streaming has none: that
 *  is the entire reason `decryptStream` and this Service Worker exist, but
 *  a `Blob` built by hand in this tab is exactly the whole-buffer situation
 *  `MAX_ENCRYPTABLE_BYTES` already exists to bound, so this path reuses it
 *  rather than inventing a second ceiling. */
async function fallbackBufferedDownload(filename: string, stream: ReadableStream<Uint8Array>, size?: number): Promise<void> {
  if (size !== undefined && size > MAX_ENCRYPTABLE_BYTES) throw new FileTooLargeError(size)
  const reader = stream.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    total += value.length
    if (total > MAX_ENCRYPTABLE_BYTES) throw new FileTooLargeError(total)
    chunks.push(value)
  }
  const url = URL.createObjectURL(new Blob(chunks as BlobPart[]))
  try {
    clickHiddenDownloadAnchor(url, filename)
  } finally {
    // Revoked on a delay rather than immediately: the click above starts
    // the browser's own download asynchronously, and revoking the object
    // URL before it has actually read the blob would abort the download it
    // just started.
    setTimeout(() => URL.revokeObjectURL(url), 30_000)
  }
}

// the two callers

/** Downloads one encrypted file: fetches its ciphertext and pipes it
 *  through `decryptStream` on the way to `streamToDownload`. The plaintext
 *  size is already known from the listing (`entry.size` is the file's
 *  on-disk, ciphertext, size; names and sizes stay in the clear under this
 *  share's encryption), so unlike the folder case below this download does
 *  get a `Content-Length`. */
export async function downloadEncryptedFile(entry: Entry): Promise<void> {
  const encryption = await encryptionForLabel(shareLabelOf(entry.path))
  if (!encryption) throw new Error(`downloadEncryptedFile called for ${entry.path}, which names no encrypted share`)
  // Throws LockedSessionError synchronously, before the fetch below starts,
  // so a caller learns it needs the passphrase without opening a connection
  // it would only have to abandon.
  const transform = decryptStream(encryption.salt, entry.size)

  const res = await fetch(api.contentUrl(entry))
  if (!res.ok || !res.body) throw new Error(`could not fetch ${entry.path}: HTTP ${res.status}`)
  await streamToDownload(entry.name, res.body.pipeThrough(transform), plaintextSizeFromCiphertextSize(entry.size))
}

/** Pages through every cursor of one directory level, yielding its file
 *  entries and recursing into its subdirectories, never assuming a
 *  directory fits in one page, and never flattening the folder's own shape
 *  away before `downloadEncryptedFolder` below turns it into zip entry
 *  names. */
async function* walkFiles(dirPath: string): AsyncGenerator<Entry> {
  let cursor: string | undefined
  do {
    const page = await api.list(dirPath, { cursor, sort: 'name', order: 'asc' })
    for (const child of page.entries) {
      if (child.kind === 'dir') {
        yield* walkFiles(child.path)
      } else {
        yield child
      }
    }
    cursor = page.cursor ?? undefined
  } while (cursor)
}

/** Downloads a whole folder from an encrypted share as a zip, built in the
 *  browser: the server holds no key, so it cannot build this zip itself the
 *  way it does for a plain share's archive ticket. Every file is decrypted
 *  through `decryptStream` as `client-zip` reads it, so this never holds
 *  more than one file's worth of ciphertext-sized buffering at a time
 *  regardless of how large the folder is: the same property `decryptStream`
 *  itself has, and the reason a whole-buffer approach was never an option
 *  here. The zip's own length is not known in advance (compression, its own
 *  directory record), so unlike `downloadEncryptedFile` this download gets
 *  no `Content-Length`. */
export async function downloadEncryptedFolder(vpath: string): Promise<void> {
  const encryption = await encryptionForLabel(shareLabelOf(vpath))
  if (!encryption) throw new Error(`downloadEncryptedFolder called for ${vpath}, which names no encrypted share`)
  // Same eager check as downloadEncryptedFile: refuse before the first
  // directory page is even fetched, not partway through the zip.
  if (!isUnlocked(encryption.salt)) throw new LockedSessionError()
  const salt = encryption.salt

  const folderName = vpath.split('/').filter(Boolean).pop() ?? 'download'
  const base = vpath.endsWith('/') ? vpath : `${vpath}/`

  async function* zipEntries(): AsyncGenerator<{ name: string; input: ReadableStream<Uint8Array> }> {
    for await (const file of walkFiles(vpath)) {
      const relative = file.path.startsWith(base) ? file.path.slice(base.length) : file.name
      // A hostile listing response could otherwise smuggle a `../` or
      // absolute-path entry name straight into the zip on disk.
      if (!isSafeArchiveName(relative)) continue
      const res = await fetch(api.contentUrl(file))
      if (!res.ok || !res.body) throw new Error(`could not fetch ${file.path}: HTTP ${res.status}`)
      yield { name: relative, input: res.body.pipeThrough(decryptStream(salt, file.size)) }
    }
  }

  await streamToDownload(`${folderName}.zip`, makeZip(zipEntries()))
}

// reusable, Range-capable media

interface MediaSource {
  entry: Entry
  salt: string
  contentType: string
}

const mediaSources = new Map<string, MediaSource>()
// The file's own first-block nonce, cached per token once read: a media
// source typically answers many range requests (a `<video>` seeking around,
// a thumbnail re-fetched), and the nonce never changes for the life of the
// file, so only the very first request pays for reading it.
const nonceCache = new Map<string, Uint8Array>()

/**
 * Registers `entry` (from the encrypted share `salt` unlocks) as a
 * repeatedly-readable, Range-capable media source, and returns the token
 * and the `/sc-media/<token>` URL a `<video src>`, an `<img src>`, or a
 * plain `fetch` may use for it. `contentType` is the caller's own: this
 * module has no MIME table of its own, and the preview UI that will call
 * this already keeps one for choosing how to render an entry in the first
 * place.
 *
 * The token is valid until `releaseMediaSource` is called with it; nothing
 * here expires one on its own; a caller MUST release a token once its
 * `<video>`/`<img>` is done with it, or the entry (and its cached nonce)
 * leaks for the rest of the tab's life.
 */
export function registerMediaSource(entry: Entry, salt: string, contentType: string): { token: string; url: string } {
  const token = crypto.randomUUID()
  mediaSources.set(token, { entry, salt, contentType })
  return { token, url: `${MEDIA_PREFIX}${token}` }
}

/** Forgets a token: it answers 404 through the worker from this point on. */
export function releaseMediaSource(token: string): void {
  mediaSources.delete(token)
  nonceCache.delete(token)
}

interface MediaRangeRequest {
  kind: 'sc-media-request'
  token: string
  start?: number
  end?: number
  suffixLength?: number
}

type MediaReply =
  | { ok: true; start: number; end: number; totalSize: number; contentType: string; stream: ReadableStream<Uint8Array> }
  | { ok: false; reason: string }

/** Reads and caches a media source's first-block nonce: bytes 8..32 of its
 *  ciphertext. A single small ranged fetch, separate from the (also single,
 *  ranged) fetch each `resolveMediaRange` call below makes for the blocks a
 *  request actually touches; the header is not re-fetched on every seek. */
async function nonceForToken(token: string, entry: Entry): Promise<Uint8Array> {
  const cached = nonceCache.get(token)
  if (cached) return cached
  const res = await fetch(api.contentUrl(entry), { headers: { Range: 'bytes=0-31' } })
  if (!res.ok || !res.body) throw new Error(`could not read the rclone-crypt header for ${entry.path}: HTTP ${res.status}`)
  const header = new Uint8Array(await res.arrayBuffer())
  if (header.length < 32 || !equalBytes(header.subarray(0, 8), RCLONE_CRYPT_MAGIC)) {
    throw new Error(`${entry.path} is not an rclone-crypt file: missing or wrong 8-byte header`)
  }
  const nonce0 = header.subarray(8, 32)
  nonceCache.set(token, nonce0)
  return nonce0
}

async function resolveMediaRange(req: MediaRangeRequest): Promise<MediaReply> {
  const source = mediaSources.get(req.token)
  if (!source) return { ok: false, reason: 'unknown-token' }
  const { entry, salt, contentType } = source

  try {
    const plaintextSize = plaintextSizeFromCiphertextSize(entry.size)
    if (plaintextSize === 0) {
      return {
        ok: true,
        start: 0,
        end: -1,
        totalSize: 0,
        contentType,
        stream: new ReadableStream({ start: (c) => c.close() })
      }
    }

    const start = req.suffixLength !== undefined ? Math.max(0, plaintextSize - req.suffixLength) : (req.start ?? 0)
    const end = Math.min(req.suffixLength !== undefined ? plaintextSize - 1 : (req.end ?? plaintextSize - 1), plaintextSize - 1)
    if (start < 0 || start > end) return { ok: false, reason: 'unsatisfiable-range' }

    const nonce0 = await nonceForToken(req.token, entry)
    const span = ciphertextSpanForRange(start, end + 1, plaintextSize)
    const res = await fetch(api.contentUrl(entry), {
      headers: { Range: `bytes=${span.offset}-${span.offset + span.length - 1}` }
    })
    if (!res.ok || !res.body) return { ok: false, reason: 'ciphertext-fetch-failed' }
    const cipherBytes = new Uint8Array(await res.arrayBuffer())
    const plaintext = decryptPlaintextRange(salt, nonce0, start, end + 1, cipherBytes)

    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(plaintext)
        controller.close()
      }
    })
    return { ok: true, start, end, totalSize: plaintextSize, contentType, stream }
  } catch (err) {
    if (err instanceof LockedSessionError) return { ok: false, reason: 'locked' }
    return { ok: false, reason: 'error' }
  }
}

/** Answers the worker's `sc-media-request` messages, each delivered with a
 *  reply port in `event.ports[0]`: resolved with a transferred stream on
 *  success, or `{ok:false}` (never a throw across the port) when the token
 *  is unknown, the range is unsatisfiable, or the share is locked. Wired up
 *  once `swReady()` has a live registration; nothing here fires before
 *  then, since the worker has no page to ask until one exists. */
function onSwMessage(event: MessageEvent<MediaRangeRequest>): void {
  if (event.data?.kind !== 'sc-media-request') return
  const port = event.ports[0]
  if (!port) return
  resolveMediaRange(event.data).then(
    (reply) => port.postMessage(reply, reply.ok ? [reply.stream] : []),
    () => port.postMessage({ ok: false, reason: 'error' })
  )
}
