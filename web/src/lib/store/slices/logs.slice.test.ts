import { describe, expect, it, vi } from 'vitest'
import type { AdminLogPage, AdminLogQuery, AdminLogRecord } from '../../api/types'
import {
  DEBOUNCE_MS,
  EMPTY_FILTERS,
  MAX_RECORDS,
  PAGE_SIZE,
  createLogsStore,
  initialLogsState,
  logsReducer,
  pureAppendPage,
  pureKnownSubsystems,
  pureLocalToNs,
  pureRecordKey,
  pureToQuery,
  type FetchLogPage,
  type LogFilters
} from './logs.slice'

function record(over: Partial<AdminLogRecord> = {}): AdminLogRecord {
  return {
    ts_ns: '1788490917438300000',
    level: 'WARN',
    msg: 'the write was refused',
    subsystem: 'dav',
    request_id: '01J000',
    attrs: { method: 'PUT' },
    ...over
  }
}

function page(over: Partial<AdminLogPage> = {}): AdminLogPage {
  return { records: [], cursor: '', stored_bytes: '0', segments: 0, ...over }
}

describe('pure log filter projection', () => {
  // The reason the wire carries strings at all. 2^53 nanoseconds is about
  // 1970 plus 104 days, so every real timestamp is past it: a bound that
  // round-tripped through a number would land on a different instant.
  it('converts a local time to exact nanoseconds past 2^53', () => {
    const ns = pureLocalToNs('2026-09-04T12:30')
    expect(ns).toBeDefined()
    expect(BigInt(ns!)).toBeGreaterThan(BigInt(Number.MAX_SAFE_INTEGER))
    // The exact value survives: dividing back gives the same millisecond.
    expect(Number(BigInt(ns!) / 1_000_000n)).toBe(new Date('2026-09-04T12:30').getTime())
    // And it is not what an inexact number path would produce.
    expect(ns).toBe((BigInt(new Date('2026-09-04T12:30').getTime()) * 1_000_000n).toString())
  })

  it('reads an empty or unparsable bound as no bound', () => {
    expect(pureLocalToNs('')).toBeUndefined()
    expect(pureLocalToNs('not a time')).toBeUndefined()
  })

  it('omits every unset filter rather than sending an empty one', () => {
    const q = pureToQuery(EMPTY_FILTERS)
    expect(q.since).toBeUndefined()
    expect(q.until).toBeUndefined()
    expect(q.text).toBeUndefined()
    expect(q.subsystem).toBeUndefined()
    expect(q.request_id).toBeUndefined()
    expect(q.cursor).toBeUndefined()
    expect(q.levels).toEqual([])
    expect(q.limit).toBe(PAGE_SIZE)
  })

  it('trims typed filters and drops one that is only whitespace', () => {
    const filters: LogFilters = { ...EMPTY_FILTERS, text: '  refused  ', subsystem: '   ' }
    const q = pureToQuery(filters)
    expect(q.text).toBe('refused')
    expect(q.subsystem).toBeUndefined()
  })

  it('sorts levels so an equal set is an equal query', () => {
    const a = pureToQuery({ ...EMPTY_FILTERS, levels: new Set(['WARN', 'DEBUG']) })
    const b = pureToQuery({ ...EMPTY_FILTERS, levels: new Set(['DEBUG', 'WARN']) })
    expect(a.levels).toEqual(['DEBUG', 'WARN'])
    expect(a.levels).toEqual(b.levels)
  })

  it('carries a cursor only when there is one to carry', () => {
    expect(pureToQuery(EMPTY_FILTERS, 'abc').cursor).toBe('abc')
    expect(pureToQuery(EMPTY_FILTERS, '').cursor).toBeUndefined()
  })
})

describe('pure page accumulation', () => {
  it('appends in order without rescanning what is held', () => {
    const held = [record({ msg: 'a' }), record({ msg: 'b' })]
    const next = pureAppendPage(held, [record({ msg: 'c' })])
    expect(next.map((r) => r.msg)).toEqual(['a', 'b', 'c'])
    // The input is not mutated.
    expect(held).toHaveLength(2)
  })

  it('caps the accumulation and keeps the newest end', () => {
    const held = Array.from({ length: 5 }, (_, i) => record({ msg: `held-${i}` }))
    const incoming = Array.from({ length: 5 }, (_, i) => record({ msg: `new-${i}` }))
    const next = pureAppendPage(held, incoming, 7)
    expect(next).toHaveLength(7)
    expect(next[0].msg).toBe('held-0')
    expect(next[6].msg).toBe('new-1')
  })

  it('lists the subsystems on screen once, sorted, ignoring blanks', () => {
    const records = [
      record({ subsystem: 'dav' }),
      record({ subsystem: '' }),
      record({ subsystem: 'auth' }),
      record({ subsystem: 'dav' })
    ]
    expect(pureKnownSubsystems(records)).toEqual(['auth', 'dav'])
  })

  it('keys two records sharing a nanosecond apart', () => {
    const r = record()
    expect(pureRecordKey(r, 0)).not.toBe(pureRecordKey(r, 1))
  })
})

describe('logsReducer', () => {
  it('toggles a level without mutating the previous set', () => {
    const before = initialLogsState()
    const on = logsReducer(before, { type: 'TOGGLE_LEVEL', level: 'ERROR' })
    expect(on.filters.levels.has('ERROR')).toBe(true)
    expect(before.filters.levels.has('ERROR')).toBe(false)

    const off = logsReducer(on, { type: 'TOGGLE_LEVEL', level: 'ERROR' })
    expect(off.filters.levels.has('ERROR')).toBe(false)
  })

  it('clears the disclosure on a refetch but keeps it while paging', () => {
    const withOpen = { ...initialLogsState(), expandedKey: 'k' }
    expect(logsReducer(withOpen, { type: 'FETCH_STARTED', reset: true, generation: 1 }).expandedKey).toBeNull()
    expect(logsReducer(withOpen, { type: 'FETCH_STARTED', reset: false, generation: 1 }).expandedKey).toBe('k')
  })

  // The whole point of the generation counter. A slow first request landing
  // after a fast second one must not replace the newer records.
  it('drops a page whose generation is no longer current', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 2 })
    const stale = logsReducer(started, {
      type: 'PAGE_RECEIVED',
      reset: true,
      generation: 1,
      page: page({ records: [record({ msg: 'stale' })] })
    })
    expect(stale.records).toHaveLength(0)
    expect(stale).toBe(started)

    const fresh = logsReducer(started, {
      type: 'PAGE_RECEIVED',
      reset: true,
      generation: 2,
      page: page({ records: [record({ msg: 'fresh' })] })
    })
    expect(fresh.records.map((r) => r.msg)).toEqual(['fresh'])
  })

  it('drops a failure whose generation is no longer current', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 3 })
    expect(logsReducer(started, { type: 'FETCH_FAILED', generation: 2 }).failed).toBe(false)
    expect(logsReducer(started, { type: 'FETCH_FAILED', generation: 3 }).failed).toBe(true)
  })

  it('keeps the totals current from every page, not only the first', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 1 })
    const first = logsReducer(started, {
      type: 'PAGE_RECEIVED',
      reset: true,
      generation: 1,
      page: page({ records: [record()], cursor: 'c1', stored_bytes: '1000', segments: 2 })
    })
    expect(first.storedBytes).toBe('1000')

    const paging = logsReducer(first, { type: 'FETCH_STARTED', reset: false, generation: 2 })
    const second = logsReducer(paging, {
      type: 'PAGE_RECEIVED',
      reset: false,
      generation: 2,
      page: page({ records: [record()], cursor: '', stored_bytes: '2048', segments: 3 })
    })
    expect(second.storedBytes).toBe('2048')
    expect(second.segments).toBe(3)
    expect(second.records).toHaveLength(2)
    expect(second.cursor).toBe('')
  })

  it('reports truncation only when the cap was hit with a cursor left', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 1 })
    const full = logsReducer(started, {
      type: 'PAGE_RECEIVED',
      reset: true,
      generation: 1,
      page: page({ records: Array.from({ length: MAX_RECORDS + 10 }, () => record()), cursor: 'more' })
    })
    expect(full.records).toHaveLength(MAX_RECORDS)
    expect(full.truncated).toBe(true)
  })
})

describe('createLogsStore request bookkeeping', () => {
  it('debounces a burst of keystrokes into one request', async () => {
    vi.useFakeTimers()
    const fetchPage = vi.fn<FetchLogPage>(async () => page())
    const store = createLogsStore(fetchPage)

    for (const text of ['r', 're', 'ref', 'refu']) {
      store.changeFilter({ type: 'SET_TEXT', text })
    }
    expect(fetchPage).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS)
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(fetchPage.mock.calls[0][0].text).toBe('refu')

    store.dispose()
    vi.useRealTimers()
  })

  // A request that never settles on its own, so the abort is what ends it.
  // Resolving immediately would leave nothing in flight to cancel and the
  // assertion would pass for the wrong reason.
  it('aborts the request in flight when the filter moves on', async () => {
    vi.useFakeTimers()
    const seen: AdminLogQuery[] = []
    const fetchPage = vi.fn<FetchLogPage>((q) => {
      const { promise, resolve, reject } = Promise.withResolvers<AdminLogPage>()
      seen.push(q)
      q.signal?.addEventListener('abort', () => reject(new Error('aborted')), { once: true })
      if (seen.length > 1) resolve(page())
      return promise
    })
    const store = createLogsStore(fetchPage)

    store.refresh()
    const first = seen[0].signal
    expect(first?.aborted).toBe(false)

    store.changeFilter({ type: 'SET_SUBSYSTEM', subsystem: 'dav' })
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS)

    expect(first?.aborted).toBe(true)
    expect(seen[1].subsystem).toBe('dav')
    // The abandoned request's rejection is not reported as a failure: its
    // generation is stale, so the reducer drops it.
    expect(store.getState().failed).toBe(false)

    store.dispose()
    vi.useRealTimers()
  })

  it('does not follow the cursor once the walk is exhausted', async () => {
    const fetchPage = vi.fn<FetchLogPage>(async () => page({ records: [record()], cursor: '' }))
    const store = createLogsStore(fetchPage)

    store.refresh()
    await vi.waitFor(() => expect(store.getState().loading).toBe(false))
    expect(store.getState().cursor).toBe('')

    store.loadMore()
    expect(fetchPage).toHaveBeenCalledTimes(1)

    store.dispose()
  })

  it('follows the cursor for one bounded page and appends it', async () => {
    const fetchPage = vi
      .fn<(q: AdminLogQuery) => Promise<AdminLogPage>>()
      .mockResolvedValueOnce(page({ records: [record({ msg: 'first' })], cursor: 'c1' }))
      .mockResolvedValueOnce(page({ records: [record({ msg: 'second' })], cursor: '' }))
    const store = createLogsStore(fetchPage)

    store.refresh()
    await vi.waitFor(() => expect(store.getState().records).toHaveLength(1))

    store.loadMore()
    await vi.waitFor(() => expect(store.getState().records).toHaveLength(2))

    expect(store.getState().records.map((r) => r.msg)).toEqual(['first', 'second'])
    // The continuation carried the cursor, and asked for one page, not all.
    expect(fetchPage.mock.calls[1][0].cursor).toBe('c1')
    expect(fetchPage.mock.calls[1][0].limit).toBe(PAGE_SIZE)
    expect(fetchPage).toHaveBeenCalledTimes(2)

    store.dispose()
  })

  it('stops the timer and the request when disposed', async () => {
    vi.useFakeTimers()
    const fetchPage = vi.fn<FetchLogPage>(async () => page())
    const store = createLogsStore(fetchPage)

    store.changeFilter({ type: 'SET_TEXT', text: 'x' })
    store.dispose()
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 4)

    expect(fetchPage).not.toHaveBeenCalled()
    vi.useRealTimers()
  })
})
