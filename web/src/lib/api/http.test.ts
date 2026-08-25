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

  it('copy() returns the { job } envelope', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(202, { job: 'J-4' }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.copy({ paths: ['/a'], dest: '/b', on_conflict: 'Fail' })

    expect(result).toEqual({ job: 'J-4' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('move() returns the { job } envelope and never asks for a dry run', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(202, { job: 'J-5' }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.move({ paths: ['/a'], dest: '/b', on_conflict: 'Fail' })

    expect(result).toEqual({ job: 'J-5' })
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('/fs/move')
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

    const result = await httpApi.movePreflight({ paths: ['/a'], dest: '/b', on_conflict: 'Fail' })

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
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('/fs/archive')
  })

  it('jobList() fetches GET /api/jobs and returns its jobs array', async () => {
    const openJob = {
      id: 'J-3',
      kind: 'delete',
      state: 'running',
      done: 1,
      total: 5,
      current: '/c',
      errors: [],
      results: [],
      attempting: [],
      download: false
    }
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, { jobs: [openJob] }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.jobList()

    expect(result).toEqual({ jobs: [openJob] })
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

  it('sends the condition when it is given one', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, { name: 'a.txt' }))
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.writeFile('/s/a.txt', 'hello', 'W/"abc"')

    expect(bodyOf(fetchMock).if_match).toBe('W/"abc"')
  })

  it('omits the condition entirely when it is not given one', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(200, { name: 'a.txt' }))
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.writeFile('/s/a.txt', 'hello')

    // Undefined rather than an empty string: an empty condition is still a
    // condition, and the server would refuse it.
    expect(bodyOf(fetchMock).if_match).toBeUndefined()
    expect(String(init0(fetchMock))).not.toContain('if_match":"')
  })

  function init0(fetchMock: ReturnType<typeof vi.fn>): string {
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    return String(init.body)
  }
})
