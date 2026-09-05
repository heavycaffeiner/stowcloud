// TypeScript's `no-default-lib` exclusion is program-wide, not per-file: it
// would drop `DOM.Iterable` for every other file in the project, not just
// this one, so it is omitted here.
/// <reference lib="webworker" />
/// <reference types="@sveltejs/kit" />

// web/src/service-worker.ts answers two synthetic same-origin prefixes on
// behalf of the page that registered them: one-shot downloads at
// /sc-download/<id>, and seekable, Range-capable media at /sc-media/<token>.
// See download-sw.ts for the page-side protocol. Precaches nothing.
// `self` is declared by both DOM and webworker lib now that DOM stays in
// the default lib set; the cast picks the worker-only shape this file uses.
const worker = self as unknown as ServiceWorkerGlobalScope

// A newly-installed worker takes over every open tab immediately rather
// than waiting for each to close and reopen: someone who has just unlocked
// a share for the first time should get a working download on the very
// next click, not after a second reload they have no reason to expect.
worker.addEventListener('install', () => {
  worker.skipWaiting()
})
worker.addEventListener('activate', (event) => {
  event.waitUntil(worker.clients.claim())
})

const DOWNLOAD_PREFIX = '/sc-download/'
const MEDIA_PREFIX = '/sc-media/'

// one-shot download: /sc-download/<id>

interface DownloadStartMessage {
  kind: 'sc-download'
  id: string
  filename: string
  size?: number
  stream: ReadableStream<Uint8Array>
}

function isDownloadStartMessage(data: unknown): data is DownloadStartMessage {
  return typeof data === 'object' && data !== null && 'kind' in data && data.kind === 'sc-download'
}

interface PendingDownload {
  filename: string
  size?: number
  stream: ReadableStream<Uint8Array>
  clientId: string
}

// Nothing here survives this worker's own termination, which is the point:
// there is nothing left to leak, and a page that reloads before claiming
// its id gets a clean 410 rather than a stream nobody will ever read.
const pendingDownloads = new Map<string, PendingDownload>()

/** Exported for its own test: records a transferred download exactly as the
 *  real `message` listener below does, without needing a real
 *  `ExtendableMessageEvent` to construct one. `clientId` is the client that
 *  posted it, the only one later allowed to claim it. */
export function receiveWorkerMessage(data: unknown, clientId: string): void {
  if (!isDownloadStartMessage(data)) return
  pendingDownloads.set(data.id, { filename: data.filename, size: data.size, stream: data.stream, clientId })
}

worker.addEventListener('message', (event: ExtendableMessageEvent) => {
  const source = event.source
  const clientId = source && 'id' in source ? source.id : ''
  receiveWorkerMessage(event.data, clientId)
})

/**
 * RFC 6266: a plain `filename=` ASCII fallback plus a `filename*=UTF-8''`
 * percent-encoded real name, in that order. This project normalises names
 * to NFC, not to ASCII, so the fallback exists only for a browser that reads
 * no further than the first form; every other browser reads the second and
 * gets the exact name.
 */
export function contentDisposition(filename: string): string {
  const ascii = filename.replace(/[^\x20-\x7e]/g, '_').replace(/"/g, "'")
  return `attachment; filename="${ascii}"; filename*=UTF-8''${encodeURIComponent(filename)}`
}

function textResponse(status: number, message: string): Response {
  return new Response(message, { status, headers: { 'Content-Type': 'text/plain; charset=utf-8' } })
}

/** Exported for its own test: builds the same `Response` the `fetch`
 *  listener answers `/sc-download/<id>` with, given only the id: state
 *  seeded through `receiveWorkerMessage` above rather than a real
 *  `postMessage` round trip. Refuses a claim from any client other than the
 *  one that posted the download, without consuming it, so the owning page
 *  can still claim it after an attacker's guess is refused. */
export function handleDownload(id: string, clientId: string): Response {
  const pending = pendingDownloads.get(id)
  if (!pending) return textResponse(410, 'This download link has already been used, or the page that started it reloaded first.')
  if (pending.clientId !== clientId) return textResponse(403, 'This download link belongs to a different page.')
  pendingDownloads.delete(id)
  const headers = new Headers({
    'Content-Type': 'application/octet-stream',
    'Content-Disposition': contentDisposition(pending.filename),
    'X-Content-Type-Options': 'nosniff'
  })
  if (pending.size !== undefined) headers.set('Content-Length', String(pending.size))
  return new Response(pending.stream, { headers })
}

// reusable, Range-capable media: /sc-media/<token>

/** A single-range `bytes=` `Range` header, resolved as far as it can be
 *  without knowing the resource's total size: an explicit end stays
 *  explicit, an open end or a suffix length is left for the page to resolve
 *  once it knows the plaintext size. `null` for no header at all (the whole
 *  body), and also for anything this worker does not parse as one range of
 *  bytes (a multi-range request or a non-byte unit is served whole rather
 *  than refused), since the reply is a freshly-built stream either way, not
 *  a cached resource a partial match would save work on. */
export interface ParsedRange {
  start?: number
  end?: number
  suffixLength?: number
}

export function parseRangeHeader(header: string | null): ParsedRange | null {
  if (!header) return null
  const m = /^bytes=(\d*)-(\d*)$/.exec(header.trim())
  if (!m) return null
  const [, firstStr, lastStr] = m
  if (firstStr === '' && lastStr === '') return null
  if (firstStr === '') return { suffixLength: Number(lastStr) }
  return { start: Number(firstStr), end: lastStr === '' ? undefined : Number(lastStr) }
}

interface MediaReplySuccess {
  ok: true
  start: number
  end: number
  totalSize: number
  contentType: string
  stream: ReadableStream<Uint8Array>
}
interface MediaReplyFailure {
  ok: false
  reason: string
}
type MediaReply = MediaReplySuccess | MediaReplyFailure

// Content types this worker declares as-is: the raster image and video
// formats media-utils.ts's mimeTypeOf actually returns. Anything else,
// image/svg+xml above all (a vector format a browser executes as script on
// direct navigation), is served as application/octet-stream instead.
const RASTER_CONTENT_TYPES: Record<string, true> = {
  'image/jpeg': true,
  'image/png': true,
  'image/gif': true,
  'image/webp': true,
  'image/bmp': true,
  'image/x-icon': true,
  'image/avif': true,
  'image/tiff': true,
  'video/mp4': true,
  'video/webm': true,
  'video/ogg': true,
  'video/quicktime': true,
  'video/x-matroska': true,
  'video/x-msvideo': true,
  'video/x-m4v': true,
  'video/x-flv': true,
  'video/x-ms-wmv': true,
  'video/3gpp': true
}

/**
 * The status and headers a successful media reply answers with: pure, and
 * so tested directly, apart from the `MessageChannel`/`fetch` plumbing that
 * produces the reply in the first place.
 *
 * A zero-length file ignores `Range` per RFC 9110 (there is no byte to
 * range over) and always answers whole. Otherwise: 200 for no incoming
 * `Range` header, 206 with `Content-Range` for one, even though the
 * resolved `[start, end]` might happen to be the whole file: what decides
 * the status is whether the request asked for a range, not whether the
 * answer covers all of it.
 */
export function mediaSuccessResponseInit(reply: MediaReplySuccess, hadRangeHeader: boolean): ResponseInit {
  const contentType = reply.contentType in RASTER_CONTENT_TYPES ? reply.contentType : 'application/octet-stream'
  const headers: Record<string, string> = {
    'Content-Type': contentType,
    'Accept-Ranges': 'bytes',
    'X-Content-Type-Options': 'nosniff'
  }
  if (reply.totalSize === 0) {
    return { status: 200, headers: { ...headers, 'Content-Length': '0' } }
  }
  headers['Content-Length'] = String(reply.end - reply.start + 1)
  if (!hadRangeHeader) return { status: 200, headers }
  headers['Content-Range'] = `bytes ${reply.start}-${reply.end}/${reply.totalSize}`
  return { status: 206, headers }
}

const MEDIA_REPLY_TIMEOUT_MS = 10_000

async function requestMediaFromClient(client: Client, token: string, range: ParsedRange | null): Promise<MediaReply | null> {
  const channel = new MessageChannel()
  return new Promise<MediaReply | null>((resolve) => {
    const timer = setTimeout(() => resolve(null), MEDIA_REPLY_TIMEOUT_MS)
    channel.port1.onmessage = (ev: MessageEvent<MediaReply>) => {
      clearTimeout(timer)
      resolve(ev.data)
    }
    client.postMessage(
      { kind: 'sc-media-request', token, start: range?.start, end: range?.end, suffixLength: range?.suffixLength },
      [channel.port2]
    )
  })
}

async function handleMedia(token: string, rangeHeader: string | null, clientId: string): Promise<Response> {
  const client = await worker.clients.get(clientId)
  if (!client) return textResponse(404, 'No page is open to answer this media request.')

  const range = parseRangeHeader(rangeHeader)
  const reply = await requestMediaFromClient(client, token, range)
  if (reply === null) return textResponse(503, 'The page did not answer this media request in time.')
  if (!reply.ok) return textResponse(404, `No such media source (${reply.reason}).`)

  return new Response(reply.stream, mediaSuccessResponseInit(reply, range !== null))
}

worker.addEventListener('fetch', (event: FetchEvent) => {
  const url = new URL(event.request.url)
  if (url.pathname.startsWith(DOWNLOAD_PREFIX)) {
    event.respondWith(handleDownload(url.pathname.slice(DOWNLOAD_PREFIX.length), event.clientId))
    return
  }
  if (url.pathname.startsWith(MEDIA_PREFIX)) {
    event.respondWith(handleMedia(url.pathname.slice(MEDIA_PREFIX.length), event.request.headers.get('Range'), event.clientId))
  }
  // Every other request is not this worker's concern: falls through to the
  // network exactly as if this worker did not exist.
})
