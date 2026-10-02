// The files client: how it spells each request and reads each answer.
import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook } from '@testing-library/react'
import type { PropsWithChildren } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, isSessionDead } from '../../../src/api/fetcher'
import {
  download,
  listPage,
  stat,
  useArchiveTicket,
  useCopyFiles,
  useDeleteFiles,
  useMoveFiles,
  useWriteFile
} from '../../../src/features/files/api'
import { invalidateEncryptedShares } from '../../../src/features/shares/encrypted-shares'
import { batchErrorKey, describeApiError } from '../../../src/api/error-text'
import { t } from '../../../src/i18n'
import { createTestQueryClient } from '../../../src/test/test-utils'

// Every case here is about a plain share. The set is empty rather than
// mocked away, because an unreachable set fails closed.
vi.mock('../../../src/features/shares/api', () => ({ fetchShareEncryptions: () => Promise.resolve([]) }))

/** A 204, which carries no body: the Response constructor refuses one. */
function noContent(): Response {
  return new Response(null, { status: 204 })
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function stubFetch(...responses: Response[]) {
  const fetchMock = vi.fn(async (_input: Request | string, _init?: RequestInit) => {
    const next = responses.shift()
    if (!next) throw new Error('unexpected request')
    return next
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

/** The nth typed request, which openapi-fetch hands over as a Request. */
function requestAt(fetchMock: ReturnType<typeof stubFetch>, n: number): Request {
  const input = fetchMock.mock.calls[n]![0]
  if (typeof input === 'string') throw new Error('expected a Request')
  return input
}

function wrapper({ children }: PropsWithChildren) {
  return <QueryClientProvider client={createTestQueryClient()}>{children}</QueryClientProvider>
}

function mutationOf<T>(hook: () => T): T {
  return renderHook(hook, { wrapper }).result.current
}

beforeEach(() => {
  invalidateEncryptedShares()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// The routes take one path pair per request. A selection is a sequence of
// them, and each outcome is recorded against its own path so a partial
// failure names what did not go.
describe('transfers', () => {
  it('deletes each path and reports per item', async () => {
    const fetchMock = stubFetch(noContent(), noContent())

    const result = await mutationOf(useDeleteFiles).mutateAsync(['/a', '/b'])

    expect(result.results.map((r) => [r.path, r.ok])).toEqual([
      ['/a', true],
      ['/b', true]
    ])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(await requestAt(fetchMock, 0).json()).toEqual({ path: '/a' })
  })

  it('records a failure against its own path and continues', async () => {
    stubFetch(jsonResponse(403, { error: { code: 'fs.denied', message: 'no' } }), noContent())

    const result = await mutationOf(useDeleteFiles).mutateAsync(['/denied', '/fine'])

    expect(result.results[0]).toMatchObject({ path: '/denied', ok: false })
    expect(result.results[0]!.error?.code).toBe('fs.denied')
    expect(result.results[1]).toMatchObject({ path: '/fine', ok: true })
  })

  it('keeps every copy outcome, destination and started job id', async () => {
    const fetchMock = stubFetch(
      jsonResponse(202, { id: 'J-4', path: '/b/a', started: true, skipped: false }),
      jsonResponse(200, { path: '/b/b', started: false, skipped: true }),
      jsonResponse(403, { error: { code: 'fs.denied', message: 'no' } })
    )

    const result = await mutationOf(useCopyFiles).mutateAsync({
      paths: ['/a', '/b', '/c'],
      dest: '/b',
      onConflict: 'skip'
    })

    expect(result.results).toEqual([
      { path: '/a', ok: true, destination: '/b/a', started: true, skipped: false, job: 'J-4' },
      { path: '/b', ok: true, destination: '/b/b', started: false, skipped: true },
      { path: '/c', ok: false, error: { code: 'fs.denied', message: 'no', detail: undefined } }
    ])
    expect(result.jobs).toEqual(['J-4'])
    expect(await requestAt(fetchMock, 0).json()).toEqual({ from: '/a', to: '/b/a', on_conflict: 'skip' })
  })

  it('carries no job list when no copy started one', async () => {
    stubFetch(jsonResponse(200, { path: '/b/a', started: false, skipped: true }))

    const result = await mutationOf(useCopyFiles).mutateAsync({ paths: ['/a'], dest: '/b', onConflict: 'skip' })

    expect(result.jobs).toBeUndefined()
  })

  it('keeps a move destination and its copy and skip outcome', async () => {
    const fetchMock = stubFetch(jsonResponse(200, { path: '/b/a (2)', copied: true, skipped: false }))

    const result = await mutationOf(useMoveFiles).mutateAsync({ paths: ['/a'], dest: '/b', onConflict: 'rename' })

    expect(result.results).toEqual([{ path: '/a', ok: true, destination: '/b/a (2)', copied: true, skipped: false }])
    const request = requestAt(fetchMock, 0)
    expect(request.url).toContain('/api/v1/files/move')
    expect(await request.json()).toEqual({ from: '/a', to: '/b/a', on_conflict: 'rename' })
  })

  // One request each: the bytes come from the navigation the ticket names,
  // which the browser owns, not from a second fetch the tab has to buffer.
  it('answers an archive ticket without reading any archive', async () => {
    const ticket = { token: 't', name: 'a.zip', url: '/api/v1/files/archive/fetch?token=t' }
    const fetchMock = stubFetch(jsonResponse(200, ticket))

    const got = await mutationOf(useArchiveTicket).mutateAsync({ paths: ['/home/a.txt'], name: 'a.zip' })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(requestAt(fetchMock, 0).url).toContain('/api/v1/files/archive')
    expect(got.url).toBe(ticket.url)
  })

  it('posts a download path and answers the ticket without reading any bytes', async () => {
    const ticket = { token: 't', name: 'a.txt', url: '/api/v1/files/download/fetch?token=t' }
    const fetchMock = stubFetch(jsonResponse(200, ticket))

    const got = await download('/home/a.txt')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = requestAt(fetchMock, 0)
    expect(request.url).toContain('/api/v1/files/download')
    expect(await request.json()).toEqual({ path: '/home/a.txt' })
    expect(got).toEqual(ticket)
  })
})

// Every change token this server mints is weak, and it refuses a conditional
// write against a weak one, so a save that sent the condition could never land.
describe('writing a file', () => {
  it('sends the file as the body and never a condition', async () => {
    const fetchMock = stubFetch(
      jsonResponse(200, {
        name: 'a.txt',
        path: 's/a.txt',
        kind: 'file',
        size: '5',
        mtime_ns: '0',
        etag: 'e',
        etag_weak: false,
        perms: []
      })
    )

    await mutationOf(useWriteFile).mutateAsync({ path: '/s/a.txt', content: 'hello' })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]![1]!
    expect(new Headers(init.headers).has('If-Match')).toBe(false)
    expect(init.body).toBe('hello')
  })
})

// Each optional field is copied by name, and a dropped one compiles: the app
// reads undefined with nothing failing where the field went missing. A row's
// references are the only way to address its bytes, so a dropped one means
// nothing can fetch the file.
describe('the wire entry', () => {
  const wireEntry = {
    name: 'photo.png',
    path: 'media/photo.png',
    kind: 'file',
    size: '7508',
    mtime_ns: '1788537863904474873',
    btime_ns: '1788537863904474000',
    etag: 'abc',
    etag_weak: true,
    perms: ['read', 'download'],
    preview: { available: true },
    content: 'claim-for-the-bytes',
    thumb: 'claim-for-the-preview'
  }

  const page = (entries: unknown[], extra: Record<string, unknown> = {}) =>
    jsonResponse(200, { entries, dirs: 0, total: entries.length, dir_etag: 'd', dir_etag_weak: false, ...extra })

  it('carries every field the server sent', async () => {
    stubFetch(jsonResponse(200, wireEntry))

    const got = await stat('/media/photo.png')

    expect(got.content).toBe('claim-for-the-bytes')
    expect(got.thumb).toBe('claim-for-the-preview')
    expect(got.preview?.available).toBe(true)
    expect(got.btime_ns).toBe('1788537863904474000')
    expect(got.size).toBe(7508)
  })

  it('carries them through a listing too', async () => {
    stubFetch(page([wireEntry]))

    const got = (await listPage('/media')).entries[0]!

    expect(got.content).toBe('claim-for-the-bytes')
    expect(got.thumb).toBe('claim-for-the-preview')
  })

  // An empty src on an <img> resolves to the page itself and renders broken,
  // and zero is a real timestamp, so absent has to stay absent.
  it('leaves an absent field absent', async () => {
    const { content, thumb, preview, btime_ns, ...bare } = wireEntry
    stubFetch(jsonResponse(200, { ...bare, kind: 'dir' }))

    const got = await stat('/media')

    expect('content' in got).toBe(false)
    expect('thumb' in got).toBe(false)
    expect('preview' in got).toBe(false)
    expect('btime_ns' in got).toBe(false)
  })

  it('keeps a false preview hint false', async () => {
    stubFetch(page([{ ...wireEntry, preview: { available: false } }]))
    expect((await listPage('/media')).entries[0]!.preview?.available).toBe(false)
  })

  it('rejects an unsafe size rather than rounding it', async () => {
    stubFetch(page([{ ...wireEntry, size: '9007199254740993' }]))
    await expect(listPage('/media')).rejects.toMatchObject({ code: 'server.malformed_response' })
  })

  // A first page looks exactly like a complete listing, so a misread cursor
  // stops every large folder after one page with nothing failing.
  it('carries the cursor the server ended the page with', async () => {
    stubFetch(page([], { cursor: 'opaque-cursor-value' }))
    expect((await listPage('/media')).cursor).toBe('opaque-cursor-value')
  })

  it('reports the final page as a null cursor, absent or empty', async () => {
    stubFetch(page([]), page([], { cursor: '' }))
    expect((await listPage('/media')).cursor).toBeNull()
    expect((await listPage('/media')).cursor).toBeNull()
  })
})

// The server hides a route the caller may not reach behind the 404 a missing
// address gets. Every path this client calls exists, so that answer means the
// session is gone.
describe('a refusal disguised as a missing address', () => {
  async function refusal(status: number, body: unknown): Promise<unknown> {
    stubFetch(jsonResponse(status, body))
    return listPage('/Files').catch((e: unknown) => e)
  }

  it('reads the bare not_found and legacy request_failed bodies as a dead session', async () => {
    expect(isSessionDead(await refusal(404, { error: 'not_found' }))).toBe(true)
    const legacy = await refusal(404, { error: 'request_failed' })
    expect(legacy).toBeInstanceOf(ApiError)
    expect((legacy as ApiError).code).toBe('request_failed')
    expect(isSessionDead(legacy)).toBe(true)
  })

  it('leaves a genuine not-found alone', async () => {
    expect(isSessionDead(await refusal(404, { error: { code: 'fs.not_found', message: 'not found' } }))).toBe(false)
  })

  // A mistyped current password is also a 401 and must stay inline.
  it('separates an expired session from a rejected re-confirmation', async () => {
    const rejected = await refusal(401, { error: { code: 'auth.invalid_credentials', message: 'no' } })
    expect(isSessionDead(rejected)).toBe(false)
    const expired = await refusal(401, { error: { code: 'auth.required', message: 'no' } })
    expect(isSessionDead(expired)).toBe(true)
  })
})

describe('the denied refusal', () => {
  it('reads as the ACL message, alone and in a batch', () => {
    expect(batchErrorKey({ code: 'fs.denied' })).toEqual({ key: 'error.acl_denied', params: undefined })
    const err = new ApiError(403, { code: 'fs.denied', message: 'not permitted' })
    expect(describeApiError(err, 'fallback')).toBe(t('error.acl_denied'))
  })
})
