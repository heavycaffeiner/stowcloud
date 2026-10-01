// Where the server is and how a request reaches it: the base URL, the CSRF
// header, the timeout, and the error every failed call throws.
import createClient from 'openapi-fetch'
import type { paths } from './generated/schema'

/** The server's root, empty for the page's own origin. `VITE_API_BASE` points
 *  a development build at another server. */
export const serverRoot = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '')

const TIMEOUT_MS = 30_000

let csrfToken = ''

/** Installed from the session answer; every mutating request carries it. */
export function setCsrfToken(token: string): void {
  csrfToken = token
}

function mutating(method: string): boolean {
  return method !== 'GET' && method !== 'HEAD'
}

function withTimeout(signal: AbortSignal | null | undefined): AbortSignal {
  const timeout = AbortSignal.timeout(TIMEOUT_MS)
  return signal ? AbortSignal.any([signal, timeout]) : timeout
}

/** The typed client for every JSON route the server documents. */
export const client = createClient<paths>({
  // openapi-fetch builds a Request, which needs an absolute URL.
  baseUrl: new URL(serverRoot || '/', location.origin).href.replace(/\/+$/, ''),
  credentials: 'include',
  // Looked up per call so a replaced global fetch is the one used.
  fetch: (request) => fetch(request, { signal: withTimeout(request.signal) })
})

client.use({
  onRequest({ request }) {
    if (mutating(request.method) && csrfToken) request.headers.set('Sc-Csrf', csrfToken)
    return request
  }
})

/** The per-call `fetch` for a typed request whose length depends on the data
 *  rather than the network, so the timeout would cut it short. */
export const untimed = (request: Request): Promise<Response> => fetch(request)

/** `/api/v1` plus `path` plus the defined query values, for routes the browser
 *  navigates to or streams from rather than reads as JSON. */
export function apiUrl(path: string, query: Record<string, string | number | undefined> = {}): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) if (value !== undefined) params.set(key, String(value))
  const search = params.toString()
  return `${serverRoot}/api/v1${path}${search ? `?${search}` : ''}`
}

/** A raw request to a URL from `apiUrl` with the cookie, the CSRF header and
 *  the timeout, for the routes whose body is not JSON. An explicit
 *  `signal: null` drops the timeout, for a transfer whose length depends on the file. */
export function send(url: string, init: RequestInit = {}): Promise<Response> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (typeof init.body === 'string' && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json; charset=utf-8')
  }
  if (mutating(method) && csrfToken) headers.set('Sc-Csrf', csrfToken)
  const signal = init.signal === null ? null : withTimeout(init.signal)
  return fetch(url, { ...init, method, headers, signal, credentials: 'include' })
}

export interface ApiErrorBody {
  error: {
    code: string
    message: string
    detail?: Record<string, unknown>
  }
}

export class ApiError extends Error {
  code: string
  detail?: Record<string, unknown>
  status: number
  constructor(status: number, body: ApiErrorBody['error']) {
    super(body.message)
    this.status = status
    this.code = body.code
    this.detail = body.detail
  }

  /** One placeholder from `detail.reason_params`. They arrive as strings, so
   *  they render the same in every language. */
  reasonParam(name: string): string | undefined {
    const params = this.detail?.reason_params
    if (!params || typeof params !== 'object') return undefined
    const v = (params as Record<string, unknown>)[name]
    return typeof v === 'string' ? v : undefined
  }

  /** Same, parsed as a number. Undefined when absent or not numeric. */
  reasonNumber(name: string): number | undefined {
    const raw = this.reasonParam(name)
    if (raw === undefined) return undefined
    const n = Number(raw)
    return Number.isFinite(n) ? n : undefined
  }
}

/**
 * True when an error means the session no longer exists, as opposed to one
 * request being refused. A 401 counts only as `auth.required`: a mistyped
 * current password is also a 401 and must not sign the user out. A 404 counts
 * because the server hides routes a credential cannot reach. `request_failed`
 * is the refusal code older servers used.
 */
export function isSessionDead(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  if (error.status === 401) return error.code === 'auth.required'
  return error.status === 404 && (error.code === 'not_found' || error.code === 'request_failed')
}

export function malformed(status: number, what: string): ApiError {
  return new ApiError(status, { code: 'server.malformed_response', message: `The server returned ${what}` })
}

/**
 * Reads the refusal out of an error body. Handlers write
 * `{"error":{"code","message","detail"}}` and the middleware chain writes
 * `{"error":"request_failed"}`; both become one shape. The body is untrusted,
 * so every field is narrowed.
 */
function classifiedOf(body: unknown, fallbackMessage: string): ApiErrorBody['error'] {
  const raw = body !== null && typeof body === 'object' && 'error' in body ? body.error : undefined
  if (typeof raw === 'string') return { code: raw, message: fallbackMessage }
  if (raw === null || typeof raw !== 'object') return { code: 'internal', message: fallbackMessage }
  const code = 'code' in raw && typeof raw.code === 'string' ? raw.code : 'internal'
  const message = 'message' in raw && typeof raw.message === 'string' ? raw.message : fallbackMessage
  const detail =
    'detail' in raw && raw.detail !== null && typeof raw.detail === 'object'
      ? Object.fromEntries(Object.entries(raw.detail))
      : undefined
  return { code, message, detail }
}

/** The error a failed raw response stands for. */
export function errorFrom(res: Response, body: unknown): ApiError {
  return new ApiError(res.status, classifiedOf(body, res.statusText))
}

/** Throws the refusal a failed raw response carries. */
export async function ensureOk(res: Response): Promise<void> {
  if (!res.ok) throw errorFrom(res, await res.json().catch(() => ({})))
}

interface Answer<T> {
  data?: T
  error?: unknown
  response: Response
}

/** The typed answer whatever its status, for the routes whose refusal still
 *  carries a document the caller reads. */
export async function settle<T>(pending: Promise<Answer<T>>): Promise<Answer<T>> {
  try {
    return await pending
  } catch (error) {
    // openapi-fetch parses the body itself and lets a parse failure escape.
    if (error instanceof SyntaxError) throw malformed(502, 'a body that is not JSON')
    throw error
  }
}

/** The document a typed call answered with, or its refusal as an `ApiError`. */
export async function unwrap<T>(pending: Promise<Answer<T>>): Promise<T> {
  const { data, error, response } = await settle(pending)
  if (!response.ok) throw errorFrom(response, error)
  if (data === undefined) throw malformed(response.status, 'an empty body')
  return data
}

/** The document, or undefined for a success that carried none. */
export async function unwrapOptional<T>(pending: Promise<Answer<T>>): Promise<T | undefined> {
  const { data, error, response } = await settle(pending)
  if (!response.ok) throw errorFrom(response, error)
  return data
}

/** For a route that answers no document: resolves on success and throws the
 *  refusal, so no caller can read a field off an absent body. */
export async function unwrapEmpty(pending: Promise<Answer<unknown>>): Promise<void> {
  const { error, response } = await settle(pending)
  if (!response.ok) throw errorFrom(response, error)
}

/** An int64 the server sends as a decimal string, as a safe JavaScript number. */
export function decimal(raw: unknown, field: string): number {
  if (typeof raw === 'number' && Number.isSafeInteger(raw) && raw >= 0) return raw
  if (typeof raw !== 'string' || !/^(?:0|[1-9]\d*)$/.test(raw)) throw malformed(502, `an invalid ${field}`)
  const value = Number(raw)
  if (!Number.isSafeInteger(value)) throw malformed(502, `an unsafe ${field}`)
  return value
}

/** A string the server documents as one of `allowed`. Without a fallback an
 *  unknown value is a malformed answer. */
export function oneOf<T extends string>(raw: string, allowed: readonly T[], field: string, fallback?: T): T {
  if ((allowed as readonly string[]).includes(raw)) return raw as T
  if (fallback !== undefined) return fallback
  throw malformed(502, `an unknown ${field}`)
}
