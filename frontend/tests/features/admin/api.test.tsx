// The admin client's wire contract: what the hooks put on the query string
// and how they read the answer.
import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import type { PropsWithChildren } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../src/api/fetcher'
import { useAdminLogs, useAdminTimeline, useSystemHealth } from '../../../src/features/admin/api'
import { EMPTY_FILTERS, PAGE_SIZE, pureLocalToNs, type LogFilters } from '../../../src/features/admin/logic/log-view'
import { createTestQueryClient } from '../../../src/test/test-utils'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function stubFetch(...bodies: unknown[]) {
  const fetchMock = vi.fn(async (_request: Request) => jsonResponse(200, bodies.shift()))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

/** The query string of the nth request made. */
function requestedQuery(fetchMock: ReturnType<typeof stubFetch>, n = 0): URLSearchParams {
  return new URL(fetchMock.mock.calls[n]![0].url).searchParams
}

function wrapper({ children }: PropsWithChildren) {
  return <QueryClientProvider client={createTestQueryClient()}>{children}</QueryClientProvider>
}

const FILTERS: LogFilters = {
  ...EMPTY_FILTERS,
  levels: new Set(['WARN', 'ERROR']),
  text: ' refused ',
  subsystem: 'dav',
  requestId: '01J000',
  since: '2026-09-01T10:00',
  until: '2026-09-02T10:00'
}

const emptyPage = { records: [], cursor: '', stored_bytes: '0', segments: 0 }

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useAdminLogs', () => {
  async function load(filters: LogFilters) {
    const { result } = renderHook(() => useAdminLogs(filters, true), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    return result
  }

  it('sends every filter under the name the route gives it', async () => {
    const fetchMock = stubFetch(emptyPage)
    await load(FILTERS)

    const q = requestedQuery(fetchMock)
    expect(q.get('since')).toBe(pureLocalToNs(FILTERS.since))
    expect(q.get('until')).toBe(pureLocalToNs(FILTERS.until))
    // Comma separated under `level`, singular, which is what the route reads.
    expect(q.get('level')).toBe('ERROR,WARN')
    expect(q.get('text')).toBe('refused')
    expect(q.get('subsystem')).toBe('dav')
    expect(q.get('request_id')).toBe('01J000')
    expect(q.get('limit')).toBe(String(PAGE_SIZE))
    expect(q.has('cursor')).toBe(false)
  })

  it('continues from the cursor the last full page returned', async () => {
    const record = { ts_ns: '1', level: 'INFO', msg: 'm', subsystem: 's', request_id: 'r', attrs: null }
    const fullPage = { ...emptyPage, records: Array.from({ length: PAGE_SIZE }, () => record), cursor: 'opaque' }
    const fetchMock = stubFetch(fullPage, emptyPage)
    const result = await load(EMPTY_FILTERS)

    expect(result.current.hasNextPage).toBe(true)
    await result.current.fetchNextPage()
    expect(requestedQuery(fetchMock, 1).get('cursor')).toBe('opaque')
  })

  it('omits an unset filter and an empty level set', async () => {
    const fetchMock = stubFetch(emptyPage)
    await load(EMPTY_FILTERS)

    const q = requestedQuery(fetchMock)
    expect(q.has('level')).toBe(false)
    expect(q.has('since')).toBe(false)
    expect(q.has('text')).toBe(false)
  })

  it('keeps ts_ns and stored_bytes as the exact strings the server sent', async () => {
    const tsNs = '1788490917438300001'
    const storedBytes = '9007199254740993'
    stubFetch({
      records: [{ ts_ns: tsNs, level: 'WARN', msg: 'refused', subsystem: 'dav', request_id: '01J000', attrs: {} }],
      cursor: '',
      stored_bytes: storedBytes,
      segments: 4
    })
    const result = await load(EMPTY_FILTERS)

    const page = result.current.data!.pages[0]!
    expect(page.records[0]!.ts_ns).toBe(tsNs)
    expect(page.stored_bytes).toBe(storedBytes)
    // Both would lose their last digit through a number.
    expect(String(Number(tsNs))).not.toBe(tsNs)
    expect(String(Number(storedBytes))).not.toBe(storedBytes)
  })

  it('reads a null record list as an empty page', async () => {
    stubFetch({ ...emptyPage, records: null })
    const result = await load(EMPTY_FILTERS)
    expect(result.current.data!.pages[0]!.records).toEqual([])
  })
})

describe('useAdminTimeline', () => {
  async function load(filters: LogFilters) {
    const { result } = renderHook(() => useAdminTimeline(filters, true), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    return result
  }

  it('sends the same filters the log route takes, plus the bucket width', async () => {
    const fetchMock = stubFetch({ bucket_ns: '60000000000', buckets: [], truncated: false })
    await load(FILTERS)

    const q = requestedQuery(fetchMock)
    expect(q.get('since')).toBe(pureLocalToNs(FILTERS.since))
    expect(q.get('until')).toBe(pureLocalToNs(FILTERS.until))
    expect(q.get('level')).toBe('ERROR,WARN')
    expect(q.get('text')).toBe('refused')
    expect(q.get('subsystem')).toBe('dav')
    expect(q.get('request_id')).toBe('01J000')
    expect(q.get('bucket_ns')).toMatch(/^\d+$/)
    // No paging: a timeline has no cursor.
    expect(q.has('cursor')).toBe(false)
    expect(q.has('limit')).toBe(false)
  })

  // Parsed as numbers these come back as different instants, and a bar would
  // be labelled with a time no event in it happened at.
  it('keeps bucket_ns and start_ns as the exact strings the server sent', async () => {
    const bucketNs = '60000000001'
    const startNs = '1788518100000000001'
    stubFetch({
      bucket_ns: bucketNs,
      buckets: [{ start_ns: startNs, server: { INFO: 12 }, audit: { ok: 2 } }],
      truncated: false
    })
    const result = await load(EMPTY_FILTERS)

    expect(result.current.data!.bucket_ns).toBe(bucketNs)
    expect(result.current.data!.buckets[0]!.start_ns).toBe(startNs)
    expect(String(Number(startNs))).not.toBe(startNs)
  })

  it('reads a null bucket list as an empty timeline', async () => {
    stubFetch({ bucket_ns: '0', buckets: null, truncated: false })
    const result = await load(EMPTY_FILTERS)
    expect(result.current.data!.buckets).toEqual([])
  })
})

// A 503 degraded is a real answer, not a failure to reach the server. The
// restart wait treats a rejection as the process being down, so which answers
// reject is what decides when a restart has finished.
describe('useSystemHealth', () => {
  async function probe(answer: () => Promise<Response>) {
    vi.stubGlobal('fetch', vi.fn(answer))
    const { result } = renderHook(() => useSystemHealth(false), { wrapper })
    await waitFor(() => expect(result.current.isPending).toBe(false))
    return result.current
  }

  it('passes a real answer through whatever status carried it', async () => {
    const got = await probe(async () => jsonResponse(503, { status: 'degraded', reasons: ['smb_agent'] }))
    expect(got.data).toEqual({ status: 'degraded', reasons: ['smb_agent'] })
  })

  it('rejects when the server cannot be reached', async () => {
    const got = await probe(() => Promise.reject(new TypeError('Failed to fetch')))
    expect(got.isError).toBe(true)
  })

  it('rejects a body it cannot read, which is what a proxy answers mid-restart', async () => {
    const got = await probe(async () => new Response('<html>502 Bad Gateway</html>', { status: 502 }))
    expect(got.error).toBeInstanceOf(ApiError)
  })

  // Read as an answer, this would say the server is up and end the wait.
  it('rejects a parsed body with no status', async () => {
    const got = await probe(async () => jsonResponse(200, { error: 'request_failed' }))
    expect(got.error).toBeInstanceOf(ApiError)
  })
})
