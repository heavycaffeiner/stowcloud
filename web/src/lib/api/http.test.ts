// web/src/lib/api/http.test.ts — httpApi's job wrappers. Every
// `fs_move`/`fs_copy`/`fs_delete`/`fs_archive` request always answers
// `202 { job }` (`go/internal/httpapi/handler`) — there is no synchronous
// fallback, so `copy`/`del`/`archive` (http.ts) hand that envelope straight
// back to the caller rather than polling it internally. `state/job-tray
// .svelte.ts` (via `state/jobs.ts::pollJob`) is the one that tracks it and
// shows the user live progress instead of the UI just hanging.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { httpApi } from './http'

/** A 204, which carries no body: the Response constructor refuses one. */
function noContent(): Response {
  return new Response(null, { status: 204 })
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('httpApi job wrappers', () => {
  // These four take one path pair per request, because that is what the
  // routes accept. A selection is a sequence of them, and each item's outcome
  // is recorded against its own path so a partial failure names what did not
  // go rather than failing the whole selection.

  it('del() deletes each path and reports per item', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(noContent())
      .mockResolvedValueOnce(noContent())
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.delete(['/a', '/b'])

    expect(result.results.map((r) => [r.path, r.ok])).toEqual([
      ['/a', true],
      ['/b', true]
    ])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string).path).toBe('/a')
  })

  // One refusal does not lose the rest. The dialogue names the path that
  // failed, which it cannot do if the whole call rejects.
  it('del() records a failure against its own path and continues', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(403, { error: { code: 'fs.denied', message: 'no' } }))
      .mockResolvedValueOnce(noContent())
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.delete(['/denied', '/fine'])

    expect(result.results[0]).toMatchObject({ path: '/denied', ok: false })
    expect(result.results[0].error?.code).toBe('fs.denied')
    expect(result.results[1]).toMatchObject({ path: '/fine', ok: true })
  })

  it('copy() names both ends and carries the job back', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(202, { id: 'J-4', path: 'b/a', started: true, skipped: false }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.copy({ paths: ['/a'], dest: '/b', on_conflict: 'fail' })

    expect(result.results).toEqual([{ path: '/a', ok: true }])
    expect(result.job).toBe('J-4')
    const sent = JSON.parse(fetchMock.mock.calls[0][1].body as string)
    // The item keeps its own name under the chosen folder.
    expect(sent).toMatchObject({ from: '/a', to: '/b/a', on_conflict: 'fail' })
  })

  // Nothing started means no job to poll. It was typed as always present, and
  // the caller polled a job named `undefined` once a second until it gave up.
  it('copy() carries no job when nothing started', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(jsonResponse(200, { path: 'b/a', started: false, skipped: true }))
    )

    const result = await httpApi.copy({ paths: ['/a'], dest: '/b', on_conflict: 'skip' })

    expect(result.job).toBeUndefined()
    expect(result.results[0].ok).toBe(true)
  })

  it('move() names both ends', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, { path: 'b/a', copied: false, skipped: false }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.move({ paths: ['/a'], dest: '/b', on_conflict: 'fail' })

    expect(result.results).toEqual([{ path: '/a', ok: true }])
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('/files/move')
    expect(JSON.parse(init.body as string)).toMatchObject({ from: '/a', to: '/b/a' })
  })

  // The server has no dry run. Answering with nothing rather than guessing is
  // what keeps the picker's notice honest: it shows one only when it has one.
  it('movePreflight() asks nothing and reports nothing', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.movePreflight({ paths: ['/a'], dest: '/b', on_conflict: 'fail' })

    expect(result.results).toEqual([])
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('archive() reads the streamed zip out of the response body', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(new Blob(['PK...']), { status: 200, headers: { 'Content-Type': 'application/zip' } }))
    vi.stubGlobal('fetch', fetchMock)

    const blob = await httpApi.archive(['/home/a.txt'], 'a.zip')

    expect(blob.size).toBeGreaterThan(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('/files/archive')
  })

  it('jobList() fetches GET /api/jobs and returns its jobs array', async () => {
    const openJob = {
      id: 'J-3',
      kind: 'delete',
      state: 'running',
      progress: '1',
      total: '5',
      message: '/c',
      results: [],
      attempting: []
    }
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, [openJob]))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.jobList()

    // The wire's counts are strings and three of its fields are named
    // differently, so what comes back is the app's shape rather than the
    // response echoed.
    expect(result.jobs).toHaveLength(1)
    expect(result.jobs[0]).toMatchObject({
      id: 'J-3',
      kind: 'delete',
      state: 'running',
      done: 1,
      total: 5,
      current: '/c'
    })
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('/jobs')
    expect(String(fetchMock.mock.calls[0]?.[0])).not.toContain('/jobs/')
  })
})

// A conditional write against an inexact change token is refused by the
// server, correctly. What matters here is what the client does next: retrying
// with the same token, or with the one the refusal returned, is refused again
// every time, so the overwrite has to ask for no condition at all.
describe('writeFile and the advisory change token', () => {
  function bodyOf(fetchMock: ReturnType<typeof vi.fn>): Record<string, unknown> {
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    return JSON.parse(String(init.body)) as Record<string, unknown>
  }

  it('sends the condition as If-Match when it is given one', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, { name: 'a.txt' }))
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.writeFile('/s/a.txt', 'hello', 'W/"abc"')

    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(new Headers(init.headers).get('If-Match')).toBe('W/"abc"')
    // The body is the file itself, not an envelope around it.
    expect(init.body).toBe('hello')
  })

  it('omits the condition entirely when it is not given one', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, { name: 'a.txt' }))
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.writeFile('/s/a.txt', 'hello')

    // Absent rather than empty: an empty condition is still a condition, and
    // the server would refuse it.
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(new Headers(init.headers).has('If-Match')).toBe(false)
  })

  function init0(fetchMock: ReturnType<typeof vi.fn>): string {
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    return String(init.body)
  }
})
