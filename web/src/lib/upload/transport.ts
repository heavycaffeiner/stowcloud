// TUS 1.0.0 + Sc-Random-Access transport.
// Runs inside the dedicated upload Worker. Picks mock
// vs real the same way api/client.ts does, via VITE_API_MOCK, so the worker
// never needs a live backend during frontend development either.

import { classifyFailure, retryAfterMs, retryDelay } from './retry'

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api/v1'

// The session cookie is `__Host-sc_sid` (auth) and state-changing requests
// additionally need the `Sc-Csrf` header: the same
// requirement `api/http.ts` satisfies for every other endpoint. This module
// runs inside the dedicated upload Worker though, a separate module realm
// from `http.ts`'s module-scoped `csrfToken`, so the token can't just be
// imported: `upload-tray.svelte.ts` posts it in over `worker.ts`'s message
// channel instead (see that file's `csrf` Cmd) and it lands here.
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
  patchChunk(id: string, offset: number, body: Blob, signal?: AbortSignal): Promise<{ offset: number }>
  /** `chunkSize` is the session's server-fixed chunk size (`Sc-Chunk-Size`,
   * ): undefined only if the backend predates the
   *  header, in which case the caller falls back to its own remembered
   *  value. */
  headSession(id: string): Promise<{ offset: number; totalSize: number; chunkSize?: number }>
  deleteSession(id: string): Promise<void>
}

// ── Real transport ──

function b64(s: string): string {
  return btoa(unescape(encodeURIComponent(s)))
}

class HttpTransport implements Transport {
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

  async patchChunk(id: string, offset: number, body: Blob, signal?: AbortSignal): Promise<{ offset: number }> {
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
      // Cancel aborts the transfer rather than letting it run to completion
      // and fail afterwards. Without it a cancelled multi-gigabyte upload
      // kept sending until every chunk in flight had finished.
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
    await fetch(`${BASE}/uploads/${id}`, {
      method: 'DELETE',
      credentials: 'include',
      headers: { 'Tus-Resumable': '1.0.0', 'Sc-Csrf': csrfToken }
    })
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

  async patchChunk(id: string, offset: number, body: Blob, _signal?: AbortSignal): Promise<{ offset: number }> {
    const s = mockSessions.get(id)
    if (!s) throw new UploadHttpError(404, 'no such mock session')
    // simulate network latency proportional to chunk size (~50 MB/s)
    await new Promise((r) => setTimeout(r, Math.max(5, body.size / (50 * 1024 * 1024)) * 1000))
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
