import { describe, expect, it } from 'vitest'
import type {
  AdminLogPage,
  AdminLogQuery,
  AdminLogRecord,
  AdminLogsTimeline,
  AdminUser,
  AuditPage,
  AuditRow
} from '../api/types'
import {
  EMPTY_FILTERS,
  PAGE_SIZE,
  MAX_RECORDS,
  TARGET_BUCKETS,
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
  type LogFilters
} from './log-view'

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


function user(over: Partial<AdminUser> = {}): AdminUser {
  return { id: 1, name: 'hyun', display_name: 'Hyun Woo', ...over } as AdminUser
}

function timeline(over: Partial<AdminLogsTimeline> = {}): AdminLogsTimeline {
  return { bucket_ns: '60000000000', buckets: [], truncated: false, ...over }
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
    // 5 minutes, which is 12 bars: round beats exact, since a bar 75s wide
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

