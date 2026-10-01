// The admin log book's wire contract: what `httpApi` puts on the query string
// and how it reads the answer.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { httpApi } from '../../../src/lib/api/http'
import type { AdminLogsTimeline } from '../../../src/lib/api/types'

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
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { records: [], cursor: '', stored_bytes: '0', segments: 0 }))
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
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { records: [], cursor: '', stored_bytes: '0', segments: 0 }))
    vi.stubGlobal('fetch', fetchMock)

    const since = '1788490917438300001'
    expect(String(Number(since))).not.toBe(since)

    await httpApi.adminListLogs({ since })
    expect(requestedQuery(fetchMock).get('since')).toBe(since)
  })

  it('omits an unset filter and an empty level set', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { records: [], cursor: '', stored_bytes: '0', segments: 0 }))
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
      vi.fn().mockResolvedValue(jsonResponse(200, { records: null, cursor: '', stored_bytes: '0', segments: 0 }))
    )

    const res = await httpApi.adminListLogs()
    expect(res.records).toEqual([])
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
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { bucket_ns: '0', buckets: [], truncated: false }))
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
