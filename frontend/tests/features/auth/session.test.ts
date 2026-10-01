import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../src/api/fetcher'
import { isUnauthenticated, resolveAuthScreen, SessionUnreachableError } from '../../../src/features/auth/api'
import { createTestQueryClient } from '../../../src/test/test-utils'

const REFUSED = { status: 401, body: { error: { code: 'auth.required', message: 'no' } } }
const SIGNED_IN = {
  status: 200,
  body: { id: '1', login: 'ada', admin: false, csrf: 'token', roots: [], features: { search: 'walk' } }
}

/** Answers each API path with a fixed reply, or a network fault when there is none. */
function stubServer(replies: Record<string, { status: number; body: unknown }>) {
  const fetchMock = vi.fn(async (request: Request) => {
    const reply = replies[new URL(request.url).pathname]
    if (!reply) throw new TypeError('network down')
    return new Response(JSON.stringify(reply.body), {
      status: reply.status,
      headers: { 'Content-Type': 'application/json' }
    })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('which screen the app opens on', () => {
  it('opens the browser when there is a session', async () => {
    stubServer({ '/api/v1/auth/session': SIGNED_IN })
    await expect(resolveAuthScreen(createTestQueryClient())).resolves.toBe('browser')
  })

  // The session route answers the same 401 whether the session expired or the
  // server has never had an account, so the follow-up question decides.
  it('offers the create-administrator screen on a server with no account', async () => {
    stubServer({ '/api/v1/auth/session': REFUSED, '/api/v1/system/setup': { status: 200, body: { required: true } } })
    await expect(resolveAuthScreen(createTestQueryClient())).resolves.toBe('first-run')
  })

  it('offers login on a server that has one', async () => {
    stubServer({ '/api/v1/auth/session': REFUSED, '/api/v1/system/setup': { status: 200, body: { required: false } } })
    await expect(resolveAuthScreen(createTestQueryClient())).resolves.toBe('login')
  })

  it('falls back to login when the first-run question fails', async () => {
    stubServer({ '/api/v1/auth/session': REFUSED })
    await expect(resolveAuthScreen(createTestQueryClient())).resolves.toBe('login')
  })

  it('reports an unreachable server instead of signing the user out', async () => {
    stubServer({})
    await expect(resolveAuthScreen(createTestQueryClient())).rejects.toBeInstanceOf(SessionUnreachableError)
  })

  it('opens at once on a cached session and refreshes it behind the page', async () => {
    const fetchMock = stubServer({ '/api/v1/auth/session': SIGNED_IN })
    const queryClient = createTestQueryClient()
    await resolveAuthScreen(queryClient)
    fetchMock.mockImplementation(() => new Promise<Response>(() => undefined))

    await expect(resolveAuthScreen(queryClient)).resolves.toBe('browser')
  })
})

describe('telling "not signed in" apart from "cannot reach the server"', () => {
  it('reads a refusal as not signed in', () => {
    expect(isUnauthenticated(new ApiError(401, { code: 'auth.required', message: 'no' }))).toBe(true)
    expect(isUnauthenticated(new ApiError(404, { code: 'request_failed', message: 'no' }))).toBe(true)
  })

  it('does not read an unreachable server as a sign-out', () => {
    expect(isUnauthenticated(new ApiError(503, { code: 'unavailable', message: 'no' }))).toBe(false)
    expect(isUnauthenticated(new TypeError('network down'))).toBe(false)
  })

  it('does not treat a credential reconfirmation refusal as session death', () => {
    expect(isUnauthenticated(new ApiError(401, { code: 'auth.invalid_credentials', message: 'no' }))).toBe(false)
  })
})
