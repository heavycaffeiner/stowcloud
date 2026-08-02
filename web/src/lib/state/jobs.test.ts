// web/src/lib/state/jobs.test.ts — pollJob's terminal-state resolution,
// error/timeout rejection, and its WebSocket-push shortcut ("progress is also pushed over the websocket; polling is the
// fallback"). Independent of a live server or a real WebSocket:
// `api.jobStatus` is spied on directly (`api`
// is a plain object — no module mock/reset needed), and the WebSocket push
// is simulated by intercepting `events.onJob`'s registration — see
// `events.test.ts` for the hub's own transport-level tests.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { ApiError } from '../api/types'
import type { JobStatus } from '../api/types'
import { events } from './events'
import { JobFailedError, JobTimeoutError, pollJob } from './jobs'

function status(partial: Partial<JobStatus>): JobStatus {
  return {
    id: 'J-x',
    kind: 'delete',
    state: 'running',
    done: 0,
    total: 0,
    current: null,
    errors: [],
    results: [],
    attempting: [],
    pending: [],
    download: false,
    ...partial
  }
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('pollJob', () => {
  it('resolves with the final status once the job reaches done, reporting progress along the way', async () => {
    vi.spyOn(api, 'jobStatus')
      .mockResolvedValueOnce(status({ state: 'running', done: 1, total: 2 }))
      .mockResolvedValueOnce(status({ state: 'done', done: 2, total: 2 }))
    const onProgress = vi.fn()

    const promise = pollJob('J-1', 'delete', onProgress)
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(1000)
    const result = await promise

    expect(result.state).toBe('done')
    expect(onProgress).toHaveBeenCalledWith(expect.objectContaining({ done: 1 }))
    expect(onProgress).toHaveBeenCalledWith(expect.objectContaining({ state: 'done' }))
  })

  it('rejects with JobFailedError, carrying the server error message, for a terminal error state', async () => {
    vi.spyOn(api, 'jobStatus').mockResolvedValueOnce(status({ state: 'error', errors: ['disk full'] }))
    const promise = pollJob('J-2', 'delete')
    promise.catch(() => {}) // attach before advancing time, or Node flags this as an unhandled rejection
    await vi.advanceTimersByTimeAsync(0)
    await expect(promise).rejects.toBeInstanceOf(JobFailedError)
    await expect(promise).rejects.toThrow('disk full')
  })

  it('treats cancelled/interrupted as failure too, not as success', async () => {
    vi.spyOn(api, 'jobStatus').mockResolvedValueOnce(status({ state: 'cancelled' }))
    const promise = pollJob('J-2b', 'delete')
    promise.catch(() => {})
    await vi.advanceTimersByTimeAsync(0)
    await expect(promise).rejects.toBeInstanceOf(JobFailedError)
  })

  it("a 404 mid-poll (row GC'd, or a cancel that raced this tick) fails fast rather than polling forever", async () => {
    vi.spyOn(api, 'jobStatus').mockRejectedValueOnce(new ApiError(404, { code: 'fs.not_found', message: 'gone' }))
    const promise = pollJob('J-3', 'delete')
    promise.catch(() => {})
    await vi.advanceTimersByTimeAsync(0)
    await expect(promise).rejects.toBeInstanceOf(JobFailedError)
  })

  it('keeps polling through a transient (non-404) error instead of failing the whole wait', async () => {
    vi.spyOn(api, 'jobStatus').mockRejectedValueOnce(new Error('network blip')).mockResolvedValueOnce(status({ state: 'done' }))
    const promise = pollJob('J-4', 'delete')
    await vi.advanceTimersByTimeAsync(0) // first tick: throws, swallowed
    await vi.advanceTimersByTimeAsync(1000) // second tick: succeeds
    await expect(promise).resolves.toMatchObject({ state: 'done' })
  })

  it('a job push over the WebSocket updates progress immediately, without waiting for the next poll tick', async () => {
    vi.spyOn(api, 'jobStatus').mockResolvedValue(status({ state: 'running', done: 0, total: 10 }))
    const onProgress = vi.fn()
    let pushed: ((id: string, done: number, total: number) => void) | undefined
    vi.spyOn(events, 'onJob').mockImplementation((cb) => {
      pushed = cb
      return () => {}
    })

    const promise = pollJob('J-5', 'delete', onProgress)
    pushed?.('some-other-job', 99, 100) // must be ignored — wrong id
    expect(onProgress).not.toHaveBeenCalled()
    pushed?.('J-5', 4, 10)
    expect(onProgress).toHaveBeenCalledWith({
      id: 'J-5',
      kind: 'delete',
      state: 'running',
      done: 4,
      total: 10,
      current: null,
      errors: [],
      results: [],
      attempting: [],
      pending: [],
      download: false
    })

    vi.spyOn(api, 'jobStatus').mockResolvedValueOnce(status({ state: 'done' }))
    await vi.advanceTimersByTimeAsync(1000)
    await expect(promise).resolves.toMatchObject({ state: 'done' })
  })

  it('rejects with JobTimeoutError if the job never reaches a terminal state', async () => {
    vi.spyOn(api, 'jobStatus').mockResolvedValue(status({ state: 'running' }))
    const promise = pollJob('J-6', 'delete')
    promise.catch(() => {})
    await vi.advanceTimersByTimeAsync(21 * 60 * 1000)
    await expect(promise).rejects.toBeInstanceOf(JobTimeoutError)
  })
})
