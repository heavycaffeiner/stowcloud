// web/src/lib/api/http.test.ts — httpApi's job wrappers. Every
// `fs_move`/`fs_copy`/`fs_delete`/`fs_archive` request always answers
// `202 { job }` (`go/internal/httpapi/handler`) — there is no synchronous
// fallback, so `copy`/`del`/`archive` (http.ts) hand that envelope straight
// back to the caller rather than polling it internally. `state/job-tray
// .svelte.ts` (via `state/jobs.ts::pollJob`) is the one that tracks it and
// shows the user live progress instead of the UI just hanging.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { httpApi } from './http'

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
  it('del() returns the { job } envelope', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(202, { job: 'J-1' }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.delete(['/a', '/b'])

    expect(result).toEqual({ job: 'J-1' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('copy() returns the per-item results alongside the job', async () => {
    const body = { results: [{ path: '/b/a', ok: true }], job: 'J-4' }
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(202, body))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.copy({ paths: ['/a'], dest: '/b', on_conflict: 'fail' })

    expect(result).toEqual(body)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  // The destination is checked before a job exists, so a batch where nothing
  // started carries no job key. It was typed as always present, and the caller
  // handed `undefined` to the poller and asked about a job by that name once a
  // second until its own timeout.
  it('copy() omits the job when nothing started', async () => {
    const body = { results: [{ path: '/b/a', ok: true, skipped: true }] }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(jsonResponse(202, body)))

    const result = await httpApi.copy({ paths: ['/a'], dest: '/b', on_conflict: 'skip' })

    expect(result.job).toBeUndefined()
    expect(result.results[0].skipped).toBe(true)
  })

  it('move() returns the { job } envelope and never asks for a dry run', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(202, { job: 'J-5' }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.move({ paths: ['/a'], dest: '/b', on_conflict: 'fail' })

    expect(result).toEqual({ job: 'J-5' })
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('/files/move')
    // `dry_run` is forced, not merely defaulted: the same endpoint answers a
    // preflight object instead of `202 { job }` when it is set, so a caller
    // leaking it through would get an envelope with no job in it.
    expect(JSON.parse(init.body as string).dry_run).toBe(false)
  })

  it('movePreflight() asks the same endpoint for a dry run', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, { results: [{ path: '/a', ok: true, will_copy: true }] }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.movePreflight({ paths: ['/a'], dest: '/b', on_conflict: 'fail' })

    expect(result).toEqual({ results: [{ path: '/a', ok: true, will_copy: true }] })
    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string).dry_run).toBe(true)
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
