// web/src/lib/api/http.test.ts — httpApi's job wrappers. Every
// `fs_move`/`fs_copy`/`fs_delete`/`fs_archive` request always answers
// `202 { job }` (`crates/sc-http/src/routes.rs`) — there is no synchronous
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
      .mockResolvedValueOnce(jsonResponse(200, { will_copy: true, total_bytes: 1024, reason: 'cross_device' }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.movePreflight({ paths: ['/a'], dest: '/b', on_conflict: 'Fail' })

    expect(result).toEqual({ will_copy: true, total_bytes: 1024, reason: 'cross_device' })
    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string).dry_run).toBe(true)
  })

  it('archive() returns the { job } envelope, never a Blob', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(202, { job: 'J-2' }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await httpApi.archive(['/big'])

    expect(result).toEqual({ job: 'J-2' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("jobDownload() fetches the finished archive job's bytes from the download endpoint", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(new Blob(['PK...']), { status: 200, headers: { 'Content-Type': 'application/zip' } }))
    vi.stubGlobal('fetch', fetchMock)

    const blob = await httpApi.jobDownload('J-2')

    expect(blob.size).toBeGreaterThan(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('/jobs/J-2/download')
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
