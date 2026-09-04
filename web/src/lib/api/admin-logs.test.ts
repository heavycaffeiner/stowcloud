// The admin log book's wire contract, from both ends: what `httpApi` puts on
// the query string, and what `mockApi` answers so the dashboard is
// developable without a server.
//
// `web/src/lib/api/http.test.ts` is owned elsewhere; this is its own file.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { httpApi } from './http'
import { mockApi } from './mock'
import type { AdminLogsTimeline } from './types'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  })
}

/** The query string of the one request the call made. */
function requestedQuery(fetchMock: ReturnType<typeof vi.fn>): URLSearchParams {
  return new URL(fetchMock.mock.calls[0][0] as string, 'https://example.test').searchParams
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('httpApi.adminListLogs query', () => {
  it('sends every filter under the name the route gives it', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, { records: [], cursor: '', stored_bytes: '0', segments: 0 })
    )
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.adminListLogs({
      since: '1788490917438300000',
      until: '1788490999999999999',
      levels: ['WARN', 'ERROR'],
      text: 'refused',
      subsystem: 'dav',
      request_id: '01J000',
      limit: 50,
      cursor: 'opaque-cursor'
    })

    const q = requestedQuery(fetchMock)
    expect(q.get('since')).toBe('1788490917438300000')
    expect(q.get('until')).toBe('1788490999999999999')
    // Comma separated under `level`, singular, which is what the route reads.
    expect(q.get('level')).toBe('WARN,ERROR')
    expect(q.get('text')).toBe('refused')
    expect(q.get('subsystem')).toBe('dav')
    expect(q.get('request_id')).toBe('01J000')
    expect(q.get('limit')).toBe('50')
    expect(q.get('cursor')).toBe('opaque-cursor')
  })

  // A bound sent as a rounded number is a different instant. These values are
  // past 2^53, so this is what proves nothing on the path parsed them.
  it('sends the time bounds digit for digit rather than through a number', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, { records: [], cursor: '', stored_bytes: '0', segments: 0 })
    )
    vi.stubGlobal('fetch', fetchMock)

    const since = '1788490917438300001'
    expect(String(Number(since))).not.toBe(since)

    await httpApi.adminListLogs({ since })
    expect(requestedQuery(fetchMock).get('since')).toBe(since)
  })

  it('omits an unset filter and an empty level set', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, { records: [], cursor: '', stored_bytes: '0', segments: 0 })
    )
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.adminListLogs({ levels: [] })

    const q = requestedQuery(fetchMock)
    expect(q.has('level')).toBe(false)
    expect(q.has('since')).toBe(false)
    expect(q.has('cursor')).toBe(false)
  })

  it('keeps ts_ns and stored_bytes as the exact strings the server sent', async () => {
    const tsNs = '1788490917438300001'
    const storedBytes = '9007199254740993'
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, {
          records: [
            {
              ts_ns: tsNs,
              level: 'WARN',
              msg: 'the write was refused',
              subsystem: 'dav',
              request_id: '01J000',
              attrs: { method: 'PUT', path: '/dav/x' }
            }
          ],
          cursor: '',
          stored_bytes: storedBytes,
          segments: 4
        })
      )
    )

    const res = await httpApi.adminListLogs()
    expect(res.records[0].ts_ns).toBe(tsNs)
    expect(res.stored_bytes).toBe(storedBytes)
    // Both would lose their last digit through a number.
    expect(String(Number(tsNs))).not.toBe(tsNs)
    expect(String(Number(storedBytes))).not.toBe(storedBytes)
  })

  it('reads a null record list as an empty page', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, { records: null, cursor: '', stored_bytes: '0', segments: 0 })
      )
    )

    const res = await httpApi.adminListLogs()
    expect(res.records).toEqual([])
  })
})

describe('mockApi.adminListLogs', () => {
  it('answers the paged shape and walks the cursor without repeating', async () => {
    const first = await mockApi.adminListLogs({ limit: 50 })

    expect(first.records).toHaveLength(50)
    expect(first.cursor).not.toBe('')
    expect(typeof first.stored_bytes).toBe('string')
    expect(BigInt(first.stored_bytes)).toBeGreaterThan(0n)
    expect(first.segments).toBeGreaterThan(0)

    const second = await mockApi.adminListLogs({ limit: 50, cursor: first.cursor })
    expect(second.records).toHaveLength(50)
    // The totals ride every page, not only the first.
    expect(second.stored_bytes).toBe(first.stored_bytes)
    expect(second.segments).toBe(first.segments)

    const firstKeys = new Set(first.records.map((r) => `${r.ts_ns}:${r.attrs.seq}`))
    for (const r of second.records) {
      expect(firstKeys.has(`${r.ts_ns}:${r.attrs.seq}`)).toBe(false)
    }
  })

  it('returns records newest first as exact nanosecond strings', async () => {
    const { records } = await mockApi.adminListLogs({ limit: 10 })
    const stamps = records.map((r) => BigInt(r.ts_ns))
    for (let i = 1; i < stamps.length; i++) {
      expect(stamps[i]).toBeLessThan(stamps[i - 1])
    }
    // Past 2^53, so the mock is exercising the same exactness the server needs.
    expect(stamps[0]).toBeGreaterThan(BigInt(Number.MAX_SAFE_INTEGER))
  })

  it('filters by level, and an empty level list means every level', async () => {
    const errorsOnly = await mockApi.adminListLogs({ levels: ['ERROR'], limit: 50 })
    expect(errorsOnly.records.length).toBeGreaterThan(0)
    expect(errorsOnly.records.every((r) => r.level === 'ERROR')).toBe(true)

    const two = await mockApi.adminListLogs({ levels: ['ERROR', 'WARN'], limit: 50 })
    expect(new Set(two.records.map((r) => r.level))).toEqual(new Set(['ERROR', 'WARN']))

    const all = await mockApi.adminListLogs({ levels: [], limit: 50 })
    expect(new Set(all.records.map((r) => r.level)).size).toBeGreaterThan(1)
  })

  it('filters by subsystem and by request id exactly', async () => {
    const dav = await mockApi.adminListLogs({ subsystem: 'dav', limit: 50 })
    expect(dav.records.length).toBeGreaterThan(0)
    expect(dav.records.every((r) => r.subsystem === 'dav')).toBe(true)

    const req = await mockApi.adminListLogs({ request_id: '01J001', limit: 50 })
    expect(req.records.length).toBeGreaterThan(0)
    expect(req.records.every((r) => r.request_id === '01J001')).toBe(true)
  })

  it('searches message and attribute values case-insensitively', async () => {
    const byMessage = await mockApi.adminListLogs({ text: 'REFUSED', limit: 50 })
    expect(byMessage.records.length).toBeGreaterThan(0)
    expect(byMessage.records.every((r) => r.msg.toLowerCase().includes('refused'))).toBe(true)

    const byAttr = await mockApi.adminListLogs({ text: 'ENOSPC', limit: 50 })
    expect(byAttr.records.length).toBeGreaterThan(0)
    expect(byAttr.records.every((r) => Object.values(r.attrs).includes('ENOSPC'))).toBe(true)
  })

  it('filters by a time range on both sides', async () => {
    const all = await mockApi.adminListLogs({ limit: 500 })
    const newest = BigInt(all.records[0].ts_ns)
    const cut = (newest - 600n * 1_000_000_000n).toString()

    const recent = await mockApi.adminListLogs({ since: cut, limit: 500 })
    expect(recent.records.length).toBeGreaterThan(0)
    expect(recent.records.length).toBeLessThan(all.records.length)
    expect(recent.records.every((r) => BigInt(r.ts_ns) >= BigInt(cut))).toBe(true)

    const older = await mockApi.adminListLogs({ until: cut, limit: 500 })
    expect(older.records.every((r) => BigInt(r.ts_ns) <= BigInt(cut))).toBe(true)
  })

  it('ends the walk with an empty cursor and an empty record list', async () => {
    const none = await mockApi.adminListLogs({ text: 'no record says this', limit: 50 })
    expect(none.records).toEqual([])
    expect(none.cursor).toBe('')
    // The empty state is still a page: the totals are there to render.
    expect(BigInt(none.stored_bytes)).toBeGreaterThan(0n)
  })

  it('rejects an aborted request rather than resolving into a stale store', async () => {
    const controller = new AbortController()
    controller.abort()
    await expect(mockApi.adminListLogs({ signal: controller.signal })).rejects.toThrow()
  })
})

describe('httpApi.adminLogsTimeline query', () => {
  it('sends the same filters the log route takes, plus the bucket width', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { bucket_ns: '60000000000', buckets: [], truncated: false }))
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.adminLogsTimeline({
      since: '1788490917438300000',
      until: '1788490999999999999',
      levels: ['WARN', 'ERROR'],
      text: 'refused',
      subsystem: 'dav',
      request_id: '01J000',
      bucket_ns: '300000000000'
    })

    const q = requestedQuery(fetchMock)
    expect(q.get('since')).toBe('1788490917438300000')
    expect(q.get('until')).toBe('1788490999999999999')
    // The level set crosses as one comma separated parameter, same as the
    // log route, so the two calls filter the same window.
    expect(q.get('level')).toBe('WARN,ERROR')
    expect(q.get('text')).toBe('refused')
    expect(q.get('subsystem')).toBe('dav')
    expect(q.get('request_id')).toBe('01J000')
    expect(q.get('bucket_ns')).toBe('300000000000')
    // No paging: a timeline has no cursor.
    expect(q.has('cursor')).toBe(false)
    expect(q.has('limit')).toBe(false)
  })

  it('omits an unset filter and an empty level set', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { bucket_ns: '0', buckets: [], truncated: false }))
    vi.stubGlobal('fetch', fetchMock)

    await httpApi.adminLogsTimeline({ levels: [] })

    const q = requestedQuery(fetchMock)
    expect(q.has('level')).toBe(false)
    expect(q.has('bucket_ns')).toBe(false)
    expect(q.has('since')).toBe(false)
  })

  // The two nanosecond fields are past 2^53. Parsed as numbers they come
  // back as different instants, and a bar would be labelled with a time no
  // event in it happened at.
  it('keeps bucket_ns and start_ns as the exact strings the server sent', async () => {
    const bucketNs = '60000000001'
    const startNs = '1788518100000000001'
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, {
          bucket_ns: bucketNs,
          buckets: [{ start_ns: startNs, server: { INFO: 12 }, audit: { ok: 2 } }],
          truncated: false
        })
      )
    )

    const res = await httpApi.adminLogsTimeline()
    expect(res.bucket_ns).toBe(bucketNs)
    expect(res.buckets[0].start_ns).toBe(startNs)
    expect(String(Number(startNs))).not.toBe(startNs)
  })

  it('reads a null bucket list as an empty, untruncated timeline', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, {})))

    const res = await httpApi.adminLogsTimeline()
    expect(res.buckets).toEqual([])
    expect(res.truncated).toBe(false)
  })
})

describe('mockApi.adminLogsTimeline', () => {
  it('answers oldest first with no holes, counting both sources', async () => {
    const res = await mockApi.adminLogsTimeline()

    expect(res.buckets.length).toBeGreaterThan(1)
    const starts = res.buckets.map((b) => BigInt(b.start_ns))
    for (let i = 1; i < starts.length; i++) {
      expect(starts[i]).toBeGreaterThan(starts[i - 1])
      // Every step is exactly one bucket wide, which is what "no holes" means.
      expect(starts[i] - starts[i - 1]).toBe(BigInt(res.bucket_ns))
    }
    // Both halves are represented, so the chart stacks two sources rather
    // than one padded with zeros.
    expect(res.buckets.some((b) => Object.keys(b.server).length > 0)).toBe(true)
    expect(res.buckets.some((b) => Object.keys(b.audit).length > 0)).toBe(true)
  })

  it('honours the requested bucket width exactly', async () => {
    const res = await mockApi.adminLogsTimeline({ bucket_ns: '300000000000' })
    expect(res.bucket_ns).toBe('300000000000')
  })

  // The endpoint's rule, which the client mirrors: level, text, subsystem and
  // request id narrow the server half only. An audit row carries none of
  // those fields, so its counts stand for the whole window.
  it('narrows the server half by level while leaving the audit half whole', async () => {
    const all = await mockApi.adminLogsTimeline()
    const errors = await mockApi.adminLogsTimeline({ levels: ['ERROR'] })

    const serverTotal = (t: AdminLogsTimeline): number =>
      t.buckets.reduce((n, b) => n + Object.values(b.server).reduce((m, c) => m + c, 0), 0)
    const auditTotal = (t: AdminLogsTimeline): number =>
      t.buckets.reduce((n, b) => n + Object.values(b.audit).reduce((m, c) => m + c, 0), 0)

    expect(serverTotal(errors)).toBeGreaterThan(0)
    expect(serverTotal(errors)).toBeLessThan(serverTotal(all))
    expect(errors.buckets.every((b) => Object.keys(b.server).every((k) => k === 'ERROR'))).toBe(true)
    expect(auditTotal(errors)).toBe(auditTotal(all))
  })

  it('narrows both halves by the time range', async () => {
    const all = await mockApi.adminLogsTimeline()
    const newest = BigInt(all.buckets[all.buckets.length - 1].start_ns)
    const cut = (newest - 600n * 1_000_000_000n).toString()

    const recent = await mockApi.adminLogsTimeline({ since: cut })
    expect(recent.buckets.length).toBeLessThan(all.buckets.length)
    expect(recent.buckets.every((b) => BigInt(b.start_ns) >= BigInt(cut))).toBe(true)
  })

  it('rejects an aborted request rather than resolving into a stale store', async () => {
    const controller = new AbortController()
    controller.abort()
    await expect(mockApi.adminLogsTimeline({ signal: controller.signal })).rejects.toThrow()
  })
})
