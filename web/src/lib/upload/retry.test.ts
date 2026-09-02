import { afterEach, describe, expect, it, vi } from 'vitest'
import { classifyFailure, MAX_RETRIES, retryAfterMs, retryDelay } from './retry'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('classifyFailure', () => {
  // A tunnel that closes a long-running socket produces no status at all. This
  // is the failure the whole policy exists for: it says nothing about whether
  // the server would refuse, so the request is worth sending again.
  it('retries a connection that produced no response', () => {
    expect(classifyFailure(0, 0)).toMatchObject({ kind: 'retry' })
  })

  it('retries what the server said to try later', () => {
    expect(classifyFailure(429, 0)).toMatchObject({ kind: 'retry' })
    expect(classifyFailure(503, 0)).toMatchObject({ kind: 'retry' })
    expect(classifyFailure(408, 0)).toMatchObject({ kind: 'retry' })
  })

  // A refusal is not a fault. Sending the same request again earns the same
  // refusal and spends the budget that a real outage needs.
  it('gives up on a refusal the next identical request would earn again', () => {
    for (const status of [400, 401, 403, 422]) {
      expect(classifyFailure(status, 0)).toMatchObject({ kind: 'give-up' })
    }
  })

  it('gives up when the session is gone, with nothing to resume from', () => {
    expect(classifyFailure(404, 0)).toEqual({ kind: 'give-up', reason: 'session-gone' })
    expect(classifyFailure(410, 0)).toEqual({ kind: 'give-up', reason: 'session-gone' })
  })

  it('gives up on a full disk rather than making the same demand again', () => {
    expect(classifyFailure(507, 0)).toEqual({ kind: 'give-up', reason: 'quota' })
  })

  // A smaller chunk is a different request, so it does not spend the budget.
  it('asks for a smaller chunk when a proxy refuses the body size', () => {
    expect(classifyFailure(413, MAX_RETRIES + 10)).toEqual({ kind: 'shrink' })
  })

  it('stops after the budget is spent', () => {
    expect(classifyFailure(0, MAX_RETRIES - 1)).toMatchObject({ kind: 'retry' })
    expect(classifyFailure(0, MAX_RETRIES)).toEqual({
      kind: 'give-up',
      reason: 'out-of-retries'
    })
  })

  // The failure that motivated the split. MAX_INFLIGHT chunks are on the wire
  // together and one dropped connection fails all of them, so a counter shared
  // by the file recorded a single outage as four failures and gave up after
  // one. Counted per chunk, each keeps its own budget.
  it('does not spend one chunk budget on another chunk failing beside it', () => {
    const inFlight = 4
    const tries: number[] = Array(inFlight).fill(0)

    // Every chunk fails together, repeatedly, as a flapping tunnel does.
    for (let outage = 0; outage < MAX_RETRIES; outage++) {
      for (let chunk = 0; chunk < inFlight; chunk++) {
        const verdict = classifyFailure(0, tries[chunk])
        expect(verdict.kind).toBe('retry')
        tries[chunk]++
      }
    }

    // A file-wide counter would have been at inFlight * MAX_RETRIES here and
    // given up long ago. Each chunk has spent exactly its own budget.
    expect(tries).toEqual(Array(inFlight).fill(MAX_RETRIES))
    expect(classifyFailure(0, tries[0])).toMatchObject({ kind: 'give-up' })
  })
})

describe('retryDelay', () => {
  // Every chunk in flight fails together when one connection drops. An
  // unjittered backoff sends them all again at the same instant, which is the
  // burst that dropped them.
  it('spreads simultaneous failures rather than replaying the burst', () => {
    const delays = new Set(Array.from({ length: 32 }, () => retryDelay(0)))
    expect(delays.size).toBeGreaterThan(1)
  })

  it('grows with each attempt', () => {
    vi.spyOn(Math, 'random').mockReturnValue(1)
    expect(retryDelay(1)).toBeGreaterThan(retryDelay(0))
    expect(retryDelay(3)).toBeGreaterThan(retryDelay(1))
  })

  it('honours what the server asked for over its own schedule', () => {
    vi.spyOn(Math, 'random').mockReturnValue(1)
    expect(retryDelay(0, 30_000)).toBe(30_000)
  })

  // A proxy naming an hour would park the upload. The schedule is a floor on
  // the client's own patience, not an instruction to obey without limit.
  it('caps a server delay that would park the upload', () => {
    vi.spyOn(Math, 'random').mockReturnValue(1)
    expect(retryDelay(0, 3_600_000)).toBeLessThanOrEqual(60_000)
  })
})

describe('retryAfterMs', () => {
  it('reads a count of seconds', () => {
    expect(retryAfterMs('5')).toBe(5000)
    expect(retryAfterMs(' 5 ')).toBe(5000)
  })

  it('reads an HTTP date as the delay until then', () => {
    const at = new Date(Date.now() + 10_000).toUTCString()
    const got = retryAfterMs(at)
    expect(got).toBeGreaterThan(8000)
    expect(got).toBeLessThanOrEqual(10_000)
  })

  it('reads a date already past as no delay', () => {
    expect(retryAfterMs(new Date(Date.now() - 10_000).toUTCString())).toBe(0)
  })

  // Absent or unparseable both mean the caller falls back to its own schedule.
  it('reads an absent or unparseable header as no instruction', () => {
    expect(retryAfterMs(null)).toBeUndefined()
    expect(retryAfterMs('soon')).toBeUndefined()
  })
})
