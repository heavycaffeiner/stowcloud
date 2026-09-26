// TUS 1.0.0 + Sc-Random-Access transport.
// Runs inside the dedicated upload Worker. Picks mock
// vs real the same way api/client.ts does, via VITE_API_MOCK, so the worker
// never needs a live backend during frontend development either.

import { classifyFailure, retryAfterMs, retryDelay } from './retry'

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api/v1'

// The session cookie is `__Host-sc_sid` (auth), and state-changing requests
// additionally need the `Sc-Csrf` header: the same requirement `api/http.ts`
// satisfies for every other endpoint. This module runs inside the dedicated
// upload Worker, a separate module realm from `http.ts`'s module-scoped
// `csrfToken`, so the token is passed over `worker.ts`'s message channel and
// lands here.
let csrfToken = ''
export function setCsrfToken(t: string): void {
  csrfToken = t
}

export class UploadHttpError extends Error {
  status: number
  /** What the server asked us to wait, when it said. Undefined otherwise. */
  retryAfterMs?: number
  constructor(status: number, message: string, retryAfterMs?: number) {
    super(message)
    this.status = status
    this.retryAfterMs = retryAfterMs
  }
}

/**
 * Sends a request, turning a dropped connection into a status of 0.
 *
 * fetch rejects rather than resolving when the connection never produced a
 * response, which is the ordinary failure behind a tunnel that closes idle or
 * long-running sockets. Callers classify by status, so the two failure shapes
 * are made one here.
 */
async function send(url: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init)
  } catch (err) {
    // An abort is the caller cancelling, not a transport fault: it must not be
    // retried, so it keeps its own identity.
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new UploadHttpError(0, `the connection failed: ${String(err)}`)
  }
}

/**
 * Runs one request until it succeeds or the policy gives up.
 *
 * `safe` says whether a retry can repeat the request's effect. A read is
 * always safe. A create is not: POST /uploads carries no client-chosen key, so
 * a request the server committed before the connection dropped becomes a
 * second session on retry, and each one reserves its declared length against
 * the account's budget until the sweep collects it. A flaky tunnel would
 * exhaust the budget rather than survive.
 *
 * An unsafe request therefore retries only on a refusal the server states,
 * where nothing was committed by definition: 429 and 503 say "not now", and
 * a status of 0 says nothing at all.
 */
async function withRetry<T>(attempt: () => Promise<T>, safe: boolean): Promise<T> {
  for (let tries = 0; ; tries++) {
    try {
      return await attempt()
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') throw err
      const status = err instanceof UploadHttpError ? err.status : 0
      if (!safe && status !== 429 && status !== 503) throw err
      // Shrinking is the caller's decision: it owns the chunk size.
      if (classifyFailure(status, tries).kind !== 'retry') throw err
      const hint = err instanceof UploadHttpError ? err.retryAfterMs : undefined
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, retryDelay(tries, hint))
      await promise
    }
  }
}

export class DirectUploadUnsupportedError extends Error {
  readonly unsupported = true
  readonly code?: string
  constructor(code?: string) {
    super(code ? `direct multipart upload is not supported: ${code}` : 'direct multipart upload is not supported')
    this.name = 'DirectUploadUnsupportedError'
    this.code = code
  }
}

export interface DirectCreateParams {
  path: string
  size: number
  checksum?: string
  ifMatch?: string
}

export interface DirectPart {
  partNumber: number
  size: number
  etag: string
  checksum?: string
}

export interface DirectReservation {
  id: string
  state: string
  size: number
  checksum?: string
  partSize: number
  expiresAt: string
  parts: DirectPart[]
}

export interface DirectPartURL {
  partNumber: number
  url: string
  headers: Record<string, string>
  expiresAt?: string
}

export interface DirectTransport {
  reserveDirect(p: DirectCreateParams): Promise<DirectReservation>
  directStatus(id: string): Promise<DirectReservation>
  directPart(id: string, partNumber: number, size: number, checksum: string): Promise<DirectPartURL>
  uploadDirectPart(
    part: DirectPartURL,
    body: Blob,
    signal?: AbortSignal,
    onProgress?: (bytesSent: number) => void,
  ): Promise<{ etag: string; checksum?: string }>
  completeDirect(id: string, parts: DirectPart[]): Promise<DirectReservation>
  cancelDirect(id: string): Promise<void>
}


export interface CreateSessionParams {
  filename: string
  totalSize: number
  chunkSize: number
  dest: string
  relativePath?: string
  mtimeNs?: string
}

export interface CreatedSession {
  id: string
  offset: number
}

export interface Transport {
  createSession(p: CreateSessionParams): Promise<CreatedSession>
  patchChunk(
    id: string,
    offset: number,
    body: Blob,
    signal?: AbortSignal,
    onProgress?: (bytesSent: number) => void
  ): Promise<{ offset: number }>
  /** `chunkSize` is the session's server-fixed chunk size (`Sc-Chunk-Size`,
   * undefined only if the backend predates the header). */
  headSession(id: string): Promise<{ offset: number; totalSize: number; chunkSize?: number }>
  deleteSession(id: string): Promise<void>
  reserveDirect?: (p: DirectCreateParams) => Promise<DirectReservation>
  directStatus?: (id: string) => Promise<DirectReservation>
  directPart?: (id: string, partNumber: number, size: number, checksum: string) => Promise<DirectPartURL>
  uploadDirectPart?: (
    part: DirectPartURL,
    body: Blob,
    signal?: AbortSignal,
    onProgress?: (bytesSent: number) => void,
  ) => Promise<{ etag: string; checksum?: string }>
  completeDirect?: (id: string, parts: DirectPart[]) => Promise<DirectReservation>
  cancelDirect?: (id: string) => Promise<void>
}

// ── Real transport ──

function b64(s: string): string {
  return btoa(unescape(encodeURIComponent(s)))
}


export class HttpTransport implements Transport {
  async reserveDirect(p: DirectCreateParams): Promise<DirectReservation> {
    return withRetry(async () => {
      const res = await send(`${BASE}/direct-uploads`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', 'Sc-Csrf': csrfToken },
        body: JSON.stringify({
          path: p.path,
          size: String(p.size),
          ...(p.checksum !== undefined ? { checksum: p.checksum } : {}),
          ...(p.ifMatch !== undefined ? { if_match: p.ifMatch } : {})
        })
      })
      const responseBody = (await res.json().catch(() => null)) as Record<string, unknown> | null
      const error = responseBody && typeof responseBody.error === 'object' && responseBody.error !== null ? (responseBody.error as Record<string, unknown>) : null
      const errorCode = error && typeof error.code === 'string' ? error.code : undefined
      if (res.status === 501 || (res.status === 422 && errorCode === 'transfer.unsupported_size')) throw new DirectUploadUnsupportedError(errorCode)
      if (!res.ok) throw new UploadHttpError(res.status, `direct upload reservation failed: ${res.status}`, retryAfterMs(res.headers.get('Retry-After')))
      const body = responseBody
      if (!body || body.capability !== true || typeof body.id !== 'string') throw new DirectUploadUnsupportedError()
      const size = Number(body.size)
      const partSize = Number(body.part_size)
      if (!Number.isSafeInteger(size) || size < 0 || !Number.isSafeInteger(partSize) || partSize <= 0 || typeof body.state !== 'string' || typeof body.expires_at !== 'string') {
        throw new UploadHttpError(502, 'direct upload reservation was malformed')
      }
      return {
        id: body.id,
        state: body.state,
        size,
        ...(typeof body.checksum === 'string' ? { checksum: body.checksum } : {}),
        partSize,
        expiresAt: body.expires_at,
        parts: Array.isArray(body.parts) ? body.parts.flatMap((part) => {
          if (!part || typeof part !== 'object') return []
          const row = part as Record<string, unknown>
          const partNumber = Number(row.part_number)
          const rowSize = Number(row.size)
          if (!Number.isSafeInteger(partNumber) || partNumber < 1 || !Number.isSafeInteger(rowSize) || rowSize < 0 || typeof row.etag !== 'string') return []
          return [{ partNumber, size: rowSize, etag: row.etag, ...(typeof row.checksum === 'string' ? { checksum: row.checksum } : {}) }]
        }) : []
      }
    }, false)
  }

  async directStatus(id: string): Promise<DirectReservation> {
    const res = await send(`${BASE}/direct-uploads/${encodeURIComponent(id)}`, { credentials: 'include' })
    if (!res.ok) throw new UploadHttpError(res.status, `direct upload status failed: ${res.status}`)
    const body = (await res.json().catch(() => null)) as Record<string, unknown> | null
    if (!body || typeof body.id !== 'string' || typeof body.state !== 'string') throw new UploadHttpError(502, 'direct upload status was malformed')
    const size = Number(body.size)
    const partSize = Number(body.part_size)
    if (!Number.isSafeInteger(size) || size < 0 || !Number.isSafeInteger(partSize) || partSize <= 0 || typeof body.expires_at !== 'string') throw new UploadHttpError(502, 'direct upload status was malformed')
    return {
      id: body.id,
      state: body.state,
      size,
      ...(typeof body.checksum === 'string' ? { checksum: body.checksum } : {}),
      partSize,
      expiresAt: body.expires_at,
      parts: Array.isArray(body.parts) ? body.parts.flatMap((part) => {
        if (!part || typeof part !== 'object') return []
        const row = part as Record<string, unknown>
        const partNumber = Number(row.part_number)
        const rowSize = Number(row.size)
        if (!Number.isSafeInteger(partNumber) || partNumber < 1 || !Number.isSafeInteger(rowSize) || rowSize < 0 || typeof row.etag !== 'string') return []
        return [{ partNumber, size: rowSize, etag: row.etag, ...(typeof row.checksum === 'string' ? { checksum: row.checksum } : {}) }]
      }) : []
    }
  }

  async directPart(id: string, partNumber: number, size: number, checksum: string): Promise<DirectPartURL> {
    const res = await send(`${BASE}/direct-uploads/${encodeURIComponent(id)}/parts`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', 'Sc-Csrf': csrfToken },
      body: JSON.stringify({ part_number: String(partNumber), size: String(size), checksum })
    })
    if (!res.ok) throw new UploadHttpError(res.status, `direct upload part URL failed: ${res.status}`, retryAfterMs(res.headers.get('Retry-After')))
    const body = (await res.json().catch(() => null)) as Record<string, unknown> | null
    if (!body || typeof body.url !== 'string' || typeof body.headers !== 'object' || body.headers === null) throw new UploadHttpError(502, 'direct upload part URL was malformed')
    const headers: Record<string, string> = {}
    for (const [key, value] of Object.entries(body.headers as Record<string, unknown>)) if (typeof value === 'string') headers[key] = value
    return {
      partNumber: Number(body.part_number),
      url: body.url,
      headers,
      ...(typeof body.expires_at === 'string' ? { expiresAt: body.expires_at } : {})
    }
  }

  async uploadDirectPart(part: DirectPartURL, body: Blob, signal?: AbortSignal): Promise<{ etag: string; checksum?: string }> {
    return withRetry(async () => {
      const res = await send(part.url, { method: 'PUT', headers: part.headers, body, signal })
      if (!res.ok) throw new UploadHttpError(res.status, `direct upload part failed: ${res.status}`, retryAfterMs(res.headers.get('Retry-After')))
      const etag = res.headers.get('ETag') ?? res.headers.get('etag')
      if (!etag) throw new UploadHttpError(502, 'direct upload part did not return an ETag')
      const checksum = res.headers.get('x-amz-checksum-sha256') ?? res.headers.get('X-Checksum-Sha256') ?? undefined
      return { etag, ...(checksum ? { checksum } : {}) }
    }, true)
  }

  async completeDirect(id: string, parts: DirectPart[]): Promise<DirectReservation> {
    const res = await send(`${BASE}/direct-uploads/${encodeURIComponent(id)}/complete`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', 'Sc-Csrf': csrfToken },
      body: JSON.stringify({ parts: parts.map((part) => ({ part_number: String(part.partNumber), size: String(part.size), etag: part.etag, ...(part.checksum !== undefined ? { checksum: part.checksum } : {}) })) })
    })
    if (!res.ok) throw new UploadHttpError(res.status, `direct upload completion failed: ${res.status}`, retryAfterMs(res.headers.get('Retry-After')))
    return this.directResponse(await res.json().catch(() => null))
  }

  async cancelDirect(id: string): Promise<void> {
    const res = await send(`${BASE}/direct-uploads/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Sc-Csrf': csrfToken }
    })
    if (!res.ok && res.status !== 404 && res.status !== 410) throw new UploadHttpError(res.status, `direct upload cancellation failed: ${res.status}`, retryAfterMs(res.headers.get('Retry-After')))
  }

  private directResponse(raw: unknown): DirectReservation {
    if (!raw || typeof raw !== 'object') throw new UploadHttpError(502, 'direct upload response was malformed')
    const body = raw as Record<string, unknown>
    const size = Number(body.size)
    const partSize = Number(body.part_size)
    if (typeof body.id !== 'string' || typeof body.state !== 'string' || !Number.isSafeInteger(size) || size < 0 || !Number.isSafeInteger(partSize) || partSize <= 0 || typeof body.expires_at !== 'string') throw new UploadHttpError(502, 'direct upload response was malformed')
    return { id: body.id, state: body.state, size, ...(typeof body.checksum === 'string' ? { checksum: body.checksum } : {}), partSize, expiresAt: body.expires_at, parts: [] }
  }
  async createSession(p: CreateSessionParams): Promise<{ id: string; offset: number }> {
    const metaParts = [
      `filename ${b64(p.filename)}`,
      `dest ${b64(p.dest)}`
    ]
    if (p.relativePath) metaParts.push(`relativePath ${b64(p.relativePath)}`)
    if (p.mtimeNs) metaParts.push(`mtime ${b64(p.mtimeNs)}`)

    // A batch of files creates its sessions in one burst, which is exactly
    // when the server refuses with 429. Retried for that, and only that:
    // creation is not safe to repeat blindly.
    return withRetry(async () => {
      const res = await send(`${BASE}/uploads`, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Tus-Resumable': '1.0.0',
          'Upload-Length': String(p.totalSize),
          'Upload-Metadata': metaParts.join(','),
          'Sc-Random-Access': '1',
          'Sc-Csrf': csrfToken
        }
      })
      if (!res.ok) {
        throw new UploadHttpError(
          res.status,
          `createSession failed: ${res.status}`,
          retryAfterMs(res.headers.get('Retry-After'))
        )
      }
      const location = res.headers.get('Location') ?? ''
      return {
        id: location.split('/').pop() ?? '',
        offset: Number(res.headers.get('Upload-Offset') ?? '0')
      }
    }, false)
  }

  async patchChunk(
    id: string,
    offset: number,
    body: Blob,
    signal?: AbortSignal,
    onProgress?: (bytesSent: number) => void
  ): Promise<{ offset: number }> {
    // XMLHttpRequest exposes upload progress events inside dedicated workers.
    if (typeof XMLHttpRequest !== 'undefined') {
      const { promise, resolve, reject } = Promise.withResolvers<{ offset: number }>()
      const xhr = new XMLHttpRequest()
      xhr.open('PATCH', `${BASE}/uploads/${id}`)
      xhr.withCredentials = true
      xhr.setRequestHeader('Tus-Resumable', '1.0.0')
      xhr.setRequestHeader('Content-Type', 'application/offset+octet-stream')
      xhr.setRequestHeader('Upload-Offset', String(offset))
      xhr.setRequestHeader('Sc-Csrf', csrfToken)

      if (signal) {
        if (signal.aborted) {
          reject(new DOMException('The user aborted a request.', 'AbortError'))
          return promise
        }
        signal.addEventListener('abort', () => xhr.abort(), { once: true })
      }

      if (xhr.upload && onProgress) {
        xhr.upload.onprogress = (e) => {
          if (e.lengthComputable) onProgress(e.loaded)
        }
      }

      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          const rawOffset = xhr.getResponseHeader('Upload-Offset')
          resolve({ offset: Number(rawOffset ?? offset) })
        } else {
          const retryHeader = xhr.getResponseHeader('Retry-After')
          reject(
            new UploadHttpError(
              xhr.status,
              `patch failed: ${xhr.status}`,
              retryAfterMs(retryHeader)
            )
          )
        }
      }

      xhr.onerror = () => {
        reject(new UploadHttpError(0, 'the connection failed'))
      }

      xhr.onabort = () => {
        reject(new DOMException('The user aborted a request.', 'AbortError'))
      }

      xhr.send(body)
      return promise
    }

    const res = await send(`${BASE}/uploads/${id}`, {
      method: 'PATCH',
      credentials: 'include',
      headers: {
        'Tus-Resumable': '1.0.0',
        'Content-Type': 'application/offset+octet-stream',
        'Upload-Offset': String(offset),
        'Sc-Csrf': csrfToken
      },
      body,
      signal
    })
    if (!res.ok) {
      throw new UploadHttpError(
        res.status,
        `patch failed: ${res.status}`,
        retryAfterMs(res.headers.get('Retry-After'))
      )
    }
    return { offset: Number(res.headers.get('Upload-Offset') ?? offset) }
  }

  async headSession(id: string): Promise<{ offset: number; totalSize: number; chunkSize?: number }> {
    return withRetry(async () => {
      const res = await send(`${BASE}/uploads/${id}`, {
        method: 'HEAD',
        credentials: 'include',
        headers: { 'Tus-Resumable': '1.0.0' }
      })
      if (!res.ok) {
        throw new UploadHttpError(
          res.status,
          `head failed: ${res.status}`,
          retryAfterMs(res.headers.get('Retry-After'))
        )
      }
      const chunkSizeHeader = res.headers.get('Sc-Chunk-Size')
      return {
        offset: Number(res.headers.get('Upload-Offset') ?? '0'),
        totalSize: Number(res.headers.get('Upload-Length') ?? '0'),
        chunkSize: chunkSizeHeader ? Number(chunkSizeHeader) : undefined
      }
    }, true)
  }

  async deleteSession(id: string): Promise<void> {
    await withRetry(async () => {
      const res = await send(`${BASE}/uploads/${id}`, {
        method: 'DELETE',
        credentials: 'include',
        headers: { 'Tus-Resumable': '1.0.0', 'Sc-Csrf': csrfToken }
      })
      // A missing session is already in the desired terminal state. The
      // worker treats this as confirmed cancellation.
      if (res.status === 404 || res.status === 410) return
      if (!res.ok) {
        throw new UploadHttpError(
          res.status,
          `delete failed: ${res.status}`,
          retryAfterMs(res.headers.get('Retry-After'))
        )
      }
    }, true)
  }
}

// ── Mock transport (in-memory, lives inside the worker realm) ──

interface MockSession {
  id: string
  dest: string
  filename: string
  totalSize: number
  chunkSize: number // fixed at creation, mirrors the real session's server-fixed value
  received: number // contiguous prefix length; sufficient since real-world chunk
  // completion order is near-sequential and out-of-order gaps are rare in the
  // mock (no IntervalSet needed client-side either, see chunk-planner.ts).
  gaps: Map<number, number> // offset -> length, for out-of-order chunks awaiting the gap to close
}

const mockSessions = new Map<string, MockSession>()

function absorbGaps(s: MockSession): void {
  let advanced = true
  while (advanced) {
    advanced = false
    for (const [offset, length] of s.gaps) {
      if (offset === s.received) {
        s.received += length
        s.gaps.delete(offset)
        advanced = true
      }
    }
  }
}

class MockTransport implements Transport {
  async createSession(p: CreateSessionParams): Promise<{ id: string; offset: number }> {
    const id = `mock-${Math.random().toString(36).slice(2, 10)}`
    mockSessions.set(id, {
      id,
      dest: p.dest,
      filename: p.filename,
      totalSize: p.totalSize,
      chunkSize: p.chunkSize,
      received: 0,
      gaps: new Map()
    })
    return { id, offset: 0 }
  }

  async patchChunk(
    id: string,
    offset: number,
    body: Blob,
    signal?: AbortSignal,
    onProgress?: (bytesSent: number) => void
  ): Promise<{ offset: number }> {
    const s = mockSessions.get(id)
    if (!s) throw new UploadHttpError(404, 'no such mock session')
    // simulate network latency proportional to chunk size (~50 MB/s)
    const durationMs = Math.max(5, (body.size / (50 * 1024 * 1024)) * 1000)
    const steps = Math.min(10, Math.max(1, Math.floor(durationMs / 5)))
    const stepMs = durationMs / steps
    for (let i = 1; i <= steps; i++) {
      if (signal?.aborted) {
        throw new DOMException('The user aborted a request.', 'AbortError')
      }
      await new Promise((r) => setTimeout(r, stepMs))
      onProgress?.(Math.round((body.size * i) / steps))
    }
    if (offset === s.received) {
      s.received += body.size
      absorbGaps(s)
    } else if (offset > s.received) {
      s.gaps.set(offset, body.size)
    } // offset < received: duplicate resend, ignore
    return { offset: s.received }
  }

  async headSession(id: string): Promise<{ offset: number; totalSize: number; chunkSize?: number }> {
    const s = mockSessions.get(id)
    if (!s) throw new UploadHttpError(404, 'no such mock session')
    return { offset: s.received, totalSize: s.totalSize, chunkSize: s.chunkSize }
  }

  async deleteSession(id: string): Promise<void> {
    mockSessions.delete(id)
  }

  getSession(id: string): MockSession | undefined {
    return mockSessions.get(id)
  }
}

export const mockTransport = new MockTransport()
export const transport: Transport = IS_MOCK ? mockTransport : new HttpTransport()
