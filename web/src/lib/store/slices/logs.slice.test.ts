import { describe, expect, it, vi } from 'vitest'
import type {
  AdminLogPage,
  AdminLogQuery,
  AdminLogRecord,
  AdminLogsTimeline,
  AdminUser,
  AuditPage,
  AuditRow
} from '../../api/types'
import {
  DEBOUNCE_MS,
  EMPTY_FILTERS,
  MAX_RECORDS,
  PAGE_SIZE,
  TARGET_BUCKETS,
  createLogsStore,
  initialLogsState,
  logsReducer,
  pureActorLabel,
  pureAppendPage,
  pureBucketEndNs,
  pureBucketNs,
  pureFoldBuckets,
  pureFoldFactor,
  pureInterleave,
  pureKnownSubsystems,
  pureLocalToNs,
  pureRecordKey,
  pureServerOnlyFiltersActive,
  pureTimelineView,
  pureToAuditQuery,
  pureToQuery,
  type FetchAuditPage,
  type FetchLogPage,
  type FetchTimeline,
  type LogFilters,
  type LogsAction,
  type LogsSources
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

function auditRow(over: Partial<AuditRow> = {}): AuditRow {
  return {
    rowid: 1,
    ts_ns: '1788490917438300000',
    actor: 1,
    actor_name: 'Hyun Woo',
    event: 'auth.login',
    target: null,
    ip: '127.0.0.1',
    ok: true,
    detail: null,
    ...over
  }
}

function auditPage(over: Partial<AuditPage> = {}): AuditPage {
  return { rows: [], next: null, ...over }
}

function user(over: Partial<AdminUser> = {}): AdminUser {
  return { id: 1, name: 'hyun', display_name: 'Hyun Woo', ...over } as AdminUser
}

function timeline(over: Partial<AdminLogsTimeline> = {}): AdminLogsTimeline {
  return { bucket_ns: '60000000000', buckets: [], truncated: false, ...over }
}

/** The four sources, each a resolved empty answer unless a test replaces it.
 *  Spelled out here so a test names only the call it is about. */
function sources(over: Partial<LogsSources> = {}): Partial<LogsSources> {
  return {
    fetchPage: async () => page(),
    fetchAudit: async () => auditPage(),
    fetchTimeline: async () => timeline(),
    fetchUsers: async () => [],
    ...over
  }
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

  // The rule the timeline endpoint implements, mirrored exactly: only the
  // time bounds reach the audit half. Sending a level or a subsystem would
  // narrow the list under bars the server drew unnarrowed.
  it('sends only the time bounds to the audit route', () => {
    const filters: LogFilters = {
      ...EMPTY_FILTERS,
      levels: new Set(['ERROR']),
      text: 'refused',
      subsystem: 'dav',
      requestId: '01J000',
      since: '2026-09-04T12:30',
      until: '2026-09-04T13:30'
    }
    const q = pureToAuditQuery(filters)
    expect(q.since_ns).toBe(pureLocalToNs('2026-09-04T12:30'))
    expect(q.until_ns).toBe(pureLocalToNs('2026-09-04T13:30'))
    expect(q.limit).toBe(PAGE_SIZE)
    expect(q.before).toBeUndefined()
    expect(Object.keys(q).sort()).toEqual(['limit', 'since_ns', 'until_ns'])
  })

  it('carries an audit cursor only when there is one to carry', () => {
    expect(pureToAuditQuery(EMPTY_FILTERS, 42).before).toBe(42)
    expect(pureToAuditQuery(EMPTY_FILTERS, null).before).toBeUndefined()
  })

  it('reports which filters reach the server log alone', () => {
    expect(pureServerOnlyFiltersActive(EMPTY_FILTERS)).toBe(false)
    expect(pureServerOnlyFiltersActive({ ...EMPTY_FILTERS, since: '2026-09-04T12:30' })).toBe(false)
    expect(pureServerOnlyFiltersActive({ ...EMPTY_FILTERS, levels: new Set(['ERROR']) })).toBe(true)
    expect(pureServerOnlyFiltersActive({ ...EMPTY_FILTERS, text: 'x' })).toBe(true)
    // Whitespace is not a filter.
    expect(pureServerOnlyFiltersActive({ ...EMPTY_FILTERS, subsystem: '   ' })).toBe(false)
  })
})

describe('pure bucket maths', () => {
  const HOUR_NS = 3_600_000_000_000n
  const base = 1_788_490_917_438_300_000n

  it('picks a round width that keeps the plot near its target bar count', () => {
    // One hour across 48 bars wants 75s; the ladder's next round step up is
    // 5 minutes, which is 12 bars — round beats exact, since a bar 75s wide
    // is a bar nobody can read a time off.
    const width = pureBucketNs(base.toString(), (base + HOUR_NS).toString())
    expect(width).toBe('300000000000')
    const span = HOUR_NS / BigInt(width!)
    expect(Number(span)).toBeLessThanOrEqual(TARGET_BUCKETS)
  })

  it('divides the span exactly rather than through a number', () => {
    // A span of 48 seconds one nanosecond short of a second boundary. As a
    // number the endpoints collapse onto each other and the division is of a
    // span that is not the one asked for.
    const until = base + 48_000_000_000n - 1n
    expect(Number(until) - Number(base)).not.toBe(Number(until - base))
    expect(pureBucketNs(base.toString(), until.toString())).toBe('1000000000')
  })

  it('scales up to the coarsest step for a window far past the ladder', () => {
    const tenYears = 10n * 365n * 24n * HOUR_NS
    expect(pureBucketNs(base.toString(), (base + tenYears).toString())).toBe('2592000000000000')
  })

  it('leaves the width to the server for an open-ended or empty window', () => {
    expect(pureBucketNs(undefined, base.toString())).toBeUndefined()
    expect(pureBucketNs(base.toString(), undefined)).toBeUndefined()
    expect(pureBucketNs(base.toString(), base.toString())).toBeUndefined()
    // A backwards range is not a window either.
    expect(pureBucketNs((base + HOUR_NS).toString(), base.toString())).toBeUndefined()
  })

  it('honours a smaller target by choosing a coarser step', () => {
    const wide = pureBucketNs(base.toString(), (base + HOUR_NS).toString(), 4)
    expect(BigInt(wide!)).toBeGreaterThan(BigInt(pureBucketNs(base.toString(), (base + HOUR_NS).toString())!))
  })

  it('ends a bucket exactly one width past its start', () => {
    expect(pureBucketEndNs(base.toString(), '60000000000')).toBe((base + 60_000_000_000n).toString())
    // And exactly, not as a number would.
    expect(pureBucketEndNs('9007199254740993', '1')).toBe('9007199254740994')
  })

  it('needs no fold while the bucket count already fits the plot', () => {
    expect(pureFoldFactor(10, 48)).toBe(1)
    expect(pureFoldFactor(48, 48)).toBe(1)
  })

  it('folds just enough to bring the bar count under the target', () => {
    // A day at one minute is what the server answers for an open-ended
    // window, and 1441 four-pixel slivers is not a plot.
    const factor = pureFoldFactor(1441, 48)
    expect(factor).toBe(31)
    expect(Math.ceil(1441 / factor)).toBeLessThanOrEqual(48)
  })

  it('sums folded buckets exactly and widens the reported width', () => {
    const folded = pureFoldBuckets(
      timeline({
        bucket_ns: '60000000000',
        buckets: [
          { start_ns: '1000000000000', server: { INFO: 1 }, audit: { ok: 1 } },
          { start_ns: '1060000000000', server: { INFO: 2, ERROR: 1 }, audit: {} },
          { start_ns: '1120000000000', server: { ERROR: 4 }, audit: { failed: 3 } }
        ]
      }),
      2
    )
    expect(folded.bucket_ns).toBe('120000000000')
    expect(folded.buckets).toHaveLength(2)
    // Counts are summed, not sampled: folding costs resolution and nothing else.
    expect(folded.buckets[0]).toEqual({
      start_ns: '1000000000000',
      server: { INFO: 3, ERROR: 1 },
      audit: { ok: 1 }
    })
    // The trailing partial group is still a bucket.
    expect(folded.buckets[1]).toEqual({
      start_ns: '1120000000000',
      server: { ERROR: 4 },
      audit: { failed: 3 }
    })
  })

  it('widens the bucket width exactly rather than through a number', () => {
    const folded = pureFoldBuckets(
      timeline({ bucket_ns: '9007199254740993', buckets: [{ start_ns: '0', server: {}, audit: {} }] }),
      3
    )
    expect(folded.bucket_ns).toBe('27021597764222979')
  })

  it('returns the timeline untouched when there is nothing to fold', () => {
    const t = timeline({ buckets: [{ start_ns: '0', server: { INFO: 1 }, audit: {} }] })
    expect(pureFoldBuckets(t, 1)).toBe(t)
    expect(pureFoldBuckets(timeline(), 8).buckets).toEqual([])
  })

  it('does not un-truncate a walk the server ended early', () => {
    const folded = pureFoldBuckets(
      timeline({
        truncated: true,
        buckets: [
          { start_ns: '0', server: {}, audit: {} },
          { start_ns: '60000000000', server: {}, audit: {} }
        ]
      }),
      2
    )
    expect(folded.truncated).toBe(true)
  })
})

describe('pureTimelineView', () => {
  const t0 = '1788490917438300000'
  const t1 = '1788490977438300000'

  const twoBuckets = timeline({
    buckets: [
      { start_ns: t0, server: { INFO: 2, ERROR: 1 }, audit: { ok: 1 } },
      { start_ns: t1, server: { INFO: 6 }, audit: { ok: 2, failed: 2 } }
    ]
  })

  it('has nothing to draw before the first answer', () => {
    expect(pureTimelineView(null)).toBeNull()
  })

  it('stacks both sources and scales every bar against the tallest', () => {
    const view = pureTimelineView(twoBuckets)!
    expect(view.max).toBe(10)
    expect(view.total).toBe(14)
    expect(view.bars).toHaveLength(2)
    expect(view.bars[0].total).toBe(4)
    expect(view.bars[1].total).toBe(10)
    // The tallest bar fills the plot; the other is its true share of it.
    expect(view.bars[1].segments.reduce((n, s) => n + s.percent, 0)).toBeCloseTo(100)
    expect(view.bars[0].segments.reduce((n, s) => n + s.percent, 0)).toBeCloseTo(40)
  })

  it('orders series by severity and keeps the audit half last', () => {
    const view = pureTimelineView(twoBuckets)!
    expect(view.series.map((s) => s.key)).toEqual([
      'server.INFO',
      'server.ERROR',
      'audit.ok',
      'audit.failed'
    ])
  })

  it('leaves out a series with no events anywhere in the window', () => {
    const view = pureTimelineView(twoBuckets)!
    // DEBUG and WARN never appear, so they are not legend entries either.
    expect(view.series.some((s) => s.name === 'DEBUG' || s.name === 'WARN')).toBe(false)
  })

  it('carries the bucket span, so a bar can name its own range', () => {
    const view = pureTimelineView(twoBuckets)!
    expect(view.bucketNs).toBe('60000000000')
    expect(view.bars[0].endNs).toBe(t1)
  })

  it('places a level this build has not heard of rather than dropping it', () => {
    const view = pureTimelineView(
      timeline({ buckets: [{ start_ns: t0, server: { TRACE: 3, INFO: 1 }, audit: {} }] })
    )!
    expect(view.series.map((s) => s.name)).toEqual(['INFO', 'TRACE'])
    expect(view.total).toBe(4)
  })

  // The half a reader did not ask for is dropped, not zeroed: a zeroed series
  // is a legend entry naming nothing, indistinguishable from a real zero.
  it('drops the half the source mode excludes', () => {
    const serverOnly = pureTimelineView(twoBuckets, 'server')!
    expect(serverOnly.series.every((s) => s.source === 'server')).toBe(true)
    expect(serverOnly.total).toBe(9)
    expect(serverOnly.max).toBe(6)

    const auditOnly = pureTimelineView(twoBuckets, 'audit')!
    expect(auditOnly.series.every((s) => s.source === 'audit')).toBe(true)
    expect(auditOnly.total).toBe(5)
  })

  it('keeps every empty bucket as a bucket so the plot has no holes', () => {
    const view = pureTimelineView(
      timeline({ buckets: [{ start_ns: t0, server: {}, audit: {} }, { start_ns: t1, server: { INFO: 1 }, audit: {} }] })
    )!
    expect(view.bars).toHaveLength(2)
    expect(view.bars[0].total).toBe(0)
    expect(view.bars[0].segments).toEqual([])
  })

  it('does not divide by a zero maximum in an entirely empty window', () => {
    const view = pureTimelineView(timeline({ buckets: [{ start_ns: t0, server: {}, audit: {} }] }))!
    expect(view.max).toBe(0)
    expect(view.total).toBe(0)
    expect(view.bars[0].segments).toEqual([])
  })

  it('passes the truncation flag through rather than hiding it', () => {
    expect(pureTimelineView(timeline({ truncated: true }))!.truncated).toBe(true)
  })
})

describe('pureInterleave', () => {
  const ns = (n: bigint): string => n.toString()
  const base = 1_788_490_917_438_300_000n

  it('merges both streams newest first', () => {
    const records = [record({ ts_ns: ns(base + 300n), msg: 'a' }), record({ ts_ns: ns(base + 100n), msg: 'b' })]
    const rows = [auditRow({ rowid: 9, ts_ns: ns(base + 200n) }), auditRow({ rowid: 8, ts_ns: ns(base) })]
    const merged = pureInterleave(records, rows)
    expect(merged.map((m) => m.tsNs)).toEqual([
      ns(base + 300n),
      ns(base + 200n),
      ns(base + 100n),
      ns(base)
    ])
    expect(merged.map((m) => m.source)).toEqual(['server', 'audit', 'server', 'audit'])
  })

  // Both timestamps are past 2^53 and one nanosecond apart. Compared as
  // numbers they are equal, so the merge would order them by tie-break rather
  // than by time and the newest row would sort second.
  it('orders by exact nanoseconds rather than through a number', () => {
    const older = base
    const newer = base + 1n
    expect(Number(older)).toBe(Number(newer))
    const merged = pureInterleave([record({ ts_ns: ns(older) })], [auditRow({ ts_ns: ns(newer) })])
    expect(merged[0].source).toBe('audit')
    expect(merged[0].tsNs).toBe(ns(newer))
  })

  it('breaks a true tie for the server record, stably', () => {
    const merged = pureInterleave([record({ ts_ns: ns(base) })], [auditRow({ ts_ns: ns(base) })])
    expect(merged.map((m) => m.source)).toEqual(['server', 'audit'])
  })

  it('drains whichever stream is left when the other is exhausted', () => {
    const records = [record({ ts_ns: ns(base + 100n) })]
    const rows = [auditRow({ rowid: 3, ts_ns: ns(base + 50n) }), auditRow({ rowid: 2, ts_ns: ns(base) })]
    expect(pureInterleave(records, rows)).toHaveLength(3)
    expect(pureInterleave([], rows).map((m) => m.source)).toEqual(['audit', 'audit'])
    expect(pureInterleave(records, []).map((m) => m.source)).toEqual(['server'])
    expect(pureInterleave([], [])).toEqual([])
  })

  it('keys every row distinctly, including two records sharing a nanosecond', () => {
    const merged = pureInterleave(
      [record({ ts_ns: ns(base) }), record({ ts_ns: ns(base) })],
      [auditRow({ rowid: 7, ts_ns: ns(base) }), auditRow({ rowid: 6, ts_ns: ns(base) })]
    )
    expect(new Set(merged.map((m) => m.key)).size).toBe(4)
    // Every row says which log it came from.
    expect(merged.filter((m) => m.source === 'audit')).toHaveLength(2)
  })

  it('caps the merged list at the newest end', () => {
    const records = Array.from({ length: 5 }, (_, i) => record({ ts_ns: ns(base + BigInt(10 - i)) }))
    const rows = Array.from({ length: 5 }, (_, i) =>
      auditRow({ rowid: 5 - i, ts_ns: ns(base + BigInt(5 - i)) })
    )
    const merged = pureInterleave(records, rows, 4)
    expect(merged).toHaveLength(4)
    expect(merged.map((m) => m.tsNs)).toEqual([ns(base + 10n), ns(base + 9n), ns(base + 8n), ns(base + 7n)])
  })
})

describe('pureActorLabel', () => {
  // The bug this exists for. `display_name` is blank, so the old chain fell
  // through to `User #1` while the server was sending the name all along.
  it('prefers the name the server resolved over the local list', () => {
    const label = pureActorLabel(auditRow({ actor: 1, actor_name: 'Hyun Woo' }), [
      user({ id: 1, name: 'hyun', display_name: '' })
    ])
    expect(label).toEqual({ kind: 'name', name: 'Hyun Woo' })
  })

  it('falls back to the local display name when the server sent none', () => {
    const label = pureActorLabel(auditRow({ actor: 2, actor_name: null }), [
      user({ id: 2, name: 'yuna', display_name: 'Yuna Kim' })
    ])
    expect(label).toEqual({ kind: 'name', name: 'Yuna Kim' })
  })

  it('falls back to the account name when the display name is blank', () => {
    const label = pureActorLabel(auditRow({ actor: 2, actor_name: null }), [
      user({ id: 2, name: 'yuna', display_name: '   ' })
    ])
    expect(label).toEqual({ kind: 'name', name: 'yuna' })
  })

  it('reaches the id only when nothing anywhere has a name', () => {
    expect(pureActorLabel(auditRow({ actor: 7, actor_name: null }), [])).toEqual({ kind: 'id', id: 7 })
    // A since-deleted account is not in the list either.
    expect(pureActorLabel(auditRow({ actor: 7, actor_name: '  ' }), [user({ id: 1 })])).toEqual({
      kind: 'id',
      id: 7
    })
  })

  it('names a row with no actor as the system rather than as user null', () => {
    expect(pureActorLabel(auditRow({ actor: null, actor_name: null }), [])).toEqual({ kind: 'system' })
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
  function received(over: Partial<Extract<LogsAction, { type: 'RESULT_RECEIVED' }>> = {}) {
    return {
      type: 'RESULT_RECEIVED' as const,
      reset: true,
      generation: 1,
      server: page(),
      audit: auditPage(),
      timeline: timeline(),
      listFailed: false,
      timelineFailed: false,
      ...over
    }
  }

  it('toggles a level without mutating the previous set', () => {
    const before = initialLogsState()
    const on = logsReducer(before, { type: 'TOGGLE_LEVEL', level: 'ERROR' })
    expect(on.filters.levels.has('ERROR')).toBe(true)
    expect(before.filters.levels.has('ERROR')).toBe(false)

    const off = logsReducer(on, { type: 'TOGGLE_LEVEL', level: 'ERROR' })
    expect(off.filters.levels.has('ERROR')).toBe(false)
  })

  it('switches which streams the screen is showing', () => {
    const next = logsReducer(initialLogsState(), { type: 'SET_SOURCE_MODE', mode: 'audit' })
    expect(next.filters.sourceMode).toBe('audit')
  })

  it('clears the disclosure and the focused bucket on a refetch, not while paging', () => {
    const withPointers = { ...initialLogsState(), expandedKey: 'k', focusedBucket: 3 }
    const refetched = logsReducer(withPointers, { type: 'FETCH_STARTED', reset: true, generation: 1 })
    expect(refetched.expandedKey).toBeNull()
    expect(refetched.focusedBucket).toBeNull()

    const paging = logsReducer(withPointers, { type: 'FETCH_STARTED', reset: false, generation: 1 })
    expect(paging.expandedKey).toBe('k')
    expect(paging.focusedBucket).toBe(3)
  })

  // The whole point of the generation counter. A slow first request landing
  // after a fast second one must not replace the newer records.
  it('drops a result whose generation is no longer current', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 2 })
    const stale = logsReducer(
      started,
      received({ generation: 1, server: page({ records: [record({ msg: 'stale' })] }) })
    )
    expect(stale.records).toHaveLength(0)
    expect(stale).toBe(started)

    const fresh = logsReducer(
      started,
      received({ generation: 2, server: page({ records: [record({ msg: 'fresh' })] }) })
    )
    expect(fresh.records.map((r) => r.msg)).toEqual(['fresh'])
  })

  it('reports a failure only for the generation that failed', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 3 })
    expect(logsReducer(started, received({ generation: 2, listFailed: true })).failed).toBe(false)
    expect(logsReducer(started, received({ generation: 3, listFailed: true })).failed).toBe(true)
  })

  it('holds both cursors and appends both streams on a continuation', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 1 })
    const first = logsReducer(
      started,
      received({
        server: page({ records: [record({ msg: 'log-1' })], cursor: 'c1', stored_bytes: '1000', segments: 2 }),
        audit: auditPage({ rows: [auditRow({ rowid: 9 })], next: 9 })
      })
    )
    expect(first.cursor).toBe('c1')
    expect(first.auditNext).toBe(9)
    expect(first.storedBytes).toBe('1000')

    const paging = logsReducer(first, { type: 'FETCH_STARTED', reset: false, generation: 2 })
    const second = logsReducer(
      paging,
      received({
        reset: false,
        generation: 2,
        server: page({ records: [record({ msg: 'log-2' })], cursor: '', stored_bytes: '2048', segments: 3 }),
        audit: auditPage({ rows: [auditRow({ rowid: 8 })], next: null }),
        timeline: null
      })
    )
    expect(second.records.map((r) => r.msg)).toEqual(['log-1', 'log-2'])
    expect(second.auditRows.map((r) => r.rowid)).toEqual([9, 8])
    expect(second.cursor).toBe('')
    expect(second.auditNext).toBeNull()
    expect(second.segments).toBe(3)
  })

  // A load-more does not move the graph: it is exact over the window already,
  // and one more page of the list changes nothing about it.
  it('keeps the graph across a continuation that did not refetch it', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 1 })
    const drawn = logsReducer(
      started,
      received({ timeline: timeline({ buckets: [{ start_ns: '1', server: { INFO: 1 }, audit: {} }] }) })
    )
    expect(drawn.timeline?.buckets).toHaveLength(1)

    const paging = logsReducer(drawn, { type: 'FETCH_STARTED', reset: false, generation: 2 })
    const after = logsReducer(paging, received({ reset: false, generation: 2, timeline: null }))
    expect(after.timeline).toBe(drawn.timeline)
  })

  // A server that cannot answer the timeline still serves the list. Losing
  // the chart must not empty the screen.
  it('fails the graph on its own without failing the list', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 1 })
    const next = logsReducer(
      started,
      received({ timeline: null, timelineFailed: true, server: page({ records: [record()] }) })
    )
    expect(next.timelineFailed).toBe(true)
    expect(next.timeline).toBeNull()
    expect(next.failed).toBe(false)
    expect(next.records).toHaveLength(1)
  })

  it('reports truncation when the cap was hit with either walk left', () => {
    const started = logsReducer(initialLogsState(), { type: 'FETCH_STARTED', reset: true, generation: 1 })
    const full = logsReducer(
      started,
      received({
        server: page({ records: Array.from({ length: MAX_RECORDS + 10 }, () => record()), cursor: 'more' })
      })
    )
    expect(full.records).toHaveLength(MAX_RECORDS)
    expect(full.truncated).toBe(true)

    // The audit cursor alone is enough to make it a truncation too.
    const auditLeft = logsReducer(
      started,
      received({
        server: page({ records: Array.from({ length: MAX_RECORDS }, () => record()), cursor: '' }),
        audit: auditPage({ rows: [auditRow()], next: 4 })
      })
    )
    expect(auditLeft.truncated).toBe(true)
  })

  it('holds the account list for the actor-name fallback', () => {
    const next = logsReducer(initialLogsState(), { type: 'USERS_LOADED', users: [user()] })
    expect(next.users).toHaveLength(1)
  })

  it('moves the focused bucket of the graph', () => {
    expect(logsReducer(initialLogsState(), { type: 'FOCUS_BUCKET', index: 4 }).focusedBucket).toBe(4)
  })
})

describe('createLogsStore request bookkeeping', () => {
  it('debounces a burst of keystrokes into one round of requests', async () => {
    vi.useFakeTimers()
    const fetchPage = vi.fn<FetchLogPage>(async () => page())
    const fetchAudit = vi.fn<FetchAuditPage>(async () => auditPage())
    const fetchTimeline = vi.fn<FetchTimeline>(async () => timeline())
    const store = createLogsStore(sources({ fetchPage, fetchAudit, fetchTimeline }))

    for (const text of ['r', 're', 'ref', 'refu']) {
      store.changeFilter({ type: 'SET_TEXT', text })
    }
    expect(fetchPage).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS)
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(fetchAudit).toHaveBeenCalledTimes(1)
    expect(fetchTimeline).toHaveBeenCalledTimes(1)
    expect(fetchPage.mock.calls[0][0].text).toBe('refu')
    // The same window reaches the graph, so the bars describe the list.
    expect(fetchTimeline.mock.calls[0][0].text).toBe('refu')

    store.dispose()
    vi.useRealTimers()
  })

  // Requests that never settle on their own, so the abort is what ends them.
  // Resolving immediately would leave nothing in flight to cancel and the
  // assertion would pass for the wrong reason.
  it('aborts all three requests in flight when the filter moves on', async () => {
    vi.useFakeTimers()
    // The first call per source never settles on its own; later ones answer
    // normally. The rejector is wired only for the call that actually hands
    // back the hanging promise, so no abandoned promise is left to reject
    // unobserved when the store is disposed.
    const hang = <T>(seen: AbortSignal[], answer: T) =>
      vi.fn(async (q: { signal?: AbortSignal }): Promise<T> => {
        if (q.signal) seen.push(q.signal)
        if (seen.length > 1) return answer
        const { promise, reject } = Promise.withResolvers<T>()
        q.signal?.addEventListener(
          'abort',
          () => reject(new DOMException('Aborted', 'AbortError')),
          { once: true }
        )
        return promise
      })
    const logSignals: AbortSignal[] = []
    const auditSignals: AbortSignal[] = []
    const timelineSignals: AbortSignal[] = []
    const store = createLogsStore(
      sources({
        fetchPage: hang<AdminLogPage>(logSignals, page()) as unknown as FetchLogPage,
        fetchAudit: hang<AuditPage>(auditSignals, auditPage()) as unknown as FetchAuditPage,
        fetchTimeline: hang<AdminLogsTimeline>(timelineSignals, timeline()) as unknown as FetchTimeline
      })
    )

    store.refresh()
    expect(logSignals[0].aborted).toBe(false)
    expect(auditSignals[0].aborted).toBe(false)
    expect(timelineSignals[0].aborted).toBe(false)

    store.changeFilter({ type: 'SET_SUBSYSTEM', subsystem: 'dav' })
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS)

    // One filter change abandons all three together.
    expect(logSignals[0].aborted).toBe(true)
    expect(auditSignals[0].aborted).toBe(true)
    expect(timelineSignals[0].aborted).toBe(true)
    // The abandoned round's rejections are not reported as a failure.
    expect(store.getState().failed).toBe(false)
    expect(store.getState().timelineFailed).toBe(false)

    store.dispose()
    vi.useRealTimers()
  })

  it('does not follow the cursors once both walks are exhausted', async () => {
    const fetchPage = vi.fn<FetchLogPage>(async () => page({ records: [record()], cursor: '' }))
    const fetchAudit = vi.fn<FetchAuditPage>(async () => auditPage({ rows: [auditRow()], next: null }))
    const store = createLogsStore(sources({ fetchPage, fetchAudit }))

    store.refresh()
    await vi.waitFor(() => expect(store.getState().loading).toBe(false))

    store.loadMore()
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(fetchAudit).toHaveBeenCalledTimes(1)

    store.dispose()
  })

  it('advances both streams on one load-more and appends both', async () => {
    const fetchPage = vi
      .fn<FetchLogPage>()
      .mockResolvedValueOnce(page({ records: [record({ msg: 'log-1' })], cursor: 'c1' }))
      .mockResolvedValueOnce(page({ records: [record({ msg: 'log-2' })], cursor: '' }))
    const fetchAudit = vi
      .fn<FetchAuditPage>()
      .mockResolvedValueOnce(auditPage({ rows: [auditRow({ rowid: 9 })], next: 9 }))
      .mockResolvedValueOnce(auditPage({ rows: [auditRow({ rowid: 8 })], next: null }))
    const fetchTimeline = vi.fn<FetchTimeline>(async () => timeline())
    const store = createLogsStore(sources({ fetchPage, fetchAudit, fetchTimeline }))

    store.refresh()
    await vi.waitFor(() => expect(store.getState().records).toHaveLength(1))

    store.loadMore()
    await vi.waitFor(() => expect(store.getState().records).toHaveLength(2))

    expect(store.getState().records.map((r) => r.msg)).toEqual(['log-1', 'log-2'])
    expect(store.getState().auditRows.map((r) => r.rowid)).toEqual([9, 8])
    // Each continuation carried its own stream's cursor, and asked for one page.
    expect(fetchPage.mock.calls[1][0].cursor).toBe('c1')
    expect(fetchPage.mock.calls[1][0].limit).toBe(PAGE_SIZE)
    expect(fetchAudit.mock.calls[1][0].before).toBe(9)
    expect(fetchAudit.mock.calls[1][0].limit).toBe(PAGE_SIZE)
    // The graph is exact over the window, so a continuation leaves it alone.
    expect(fetchTimeline).toHaveBeenCalledTimes(1)

    store.dispose()
  })

  it('asks only the stream that still has a walk left', async () => {
    const fetchPage = vi
      .fn<FetchLogPage>()
      .mockResolvedValueOnce(page({ records: [record()], cursor: '' }))
    const fetchAudit = vi
      .fn<FetchAuditPage>()
      .mockResolvedValueOnce(auditPage({ rows: [auditRow({ rowid: 9 })], next: 9 }))
      .mockResolvedValueOnce(auditPage({ rows: [auditRow({ rowid: 8 })], next: null }))
    const store = createLogsStore(sources({ fetchPage, fetchAudit }))

    store.refresh()
    await vi.waitFor(() => expect(store.getState().loading).toBe(false))

    store.loadMore()
    await vi.waitFor(() => expect(store.getState().auditRows).toHaveLength(2))
    // The server log's walk was already exhausted, so it was not asked again.
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(fetchAudit).toHaveBeenCalledTimes(2)

    store.dispose()
  })

  it('asks only the sources the mode selects', async () => {
    const fetchPage = vi.fn<FetchLogPage>(async () => page())
    const fetchAudit = vi.fn<FetchAuditPage>(async () => auditPage())
    const store = createLogsStore(sources({ fetchPage, fetchAudit }))

    store.dispatch({ type: 'SET_SOURCE_MODE', mode: 'audit' })
    store.refresh()
    await vi.waitFor(() => expect(store.getState().loading).toBe(false))

    expect(fetchPage).not.toHaveBeenCalled()
    expect(fetchAudit).toHaveBeenCalledTimes(1)

    store.dispose()
  })

  it('loads the account list once, and survives its failure', async () => {
    const fetchUsers = vi.fn(async () => {
      throw new Error('no')
    })
    const store = createLogsStore(sources({ fetchUsers }))

    store.refresh()
    await vi.waitFor(() => expect(store.getState().loading).toBe(false))
    // A missing account list costs a name, not the log.
    expect(store.getState().failed).toBe(false)
    expect(store.getState().users).toEqual([])

    store.dispose()
  })

  it('stops the timer and the requests when disposed', async () => {
    vi.useFakeTimers()
    const fetchPage = vi.fn<FetchLogPage>(async () => page())
    const store = createLogsStore(sources({ fetchPage }))

    store.changeFilter({ type: 'SET_TEXT', text: 'x' })
    store.dispose()
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 4)

    expect(fetchPage).not.toHaveBeenCalled()
    vi.useRealTimers()
  })
})

describe('AdminLogQuery contract', () => {
  it('threads the abort signal onto every call', async () => {
    const seen: AdminLogQuery[] = []
    const store = createLogsStore(
      sources({
        fetchPage: async (q) => {
          seen.push(q)
          return page()
        }
      })
    )
    store.refresh()
    await vi.waitFor(() => expect(seen).toHaveLength(1))
    expect(seen[0].signal).toBeInstanceOf(AbortSignal)
    store.dispose()
  })
})
