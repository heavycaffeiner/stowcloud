// The admin log screen's three reads: the server log, the audit log, and the
// bucket counts the graph is drawn from.
//
// Each is keyed on the filters it was asked with, which is what replaces the
// hand-rolled debounce/abort/generation bookkeeping: a filter change is a new
// key, the old request is cancelled by the cache, and a late answer for an
// abandoned filter lands under a key nothing is observing.
import { infiniteQueryOptions, queryOptions } from '@tanstack/svelte-query'
import {
  PAGE_SIZE,
  pureBucketNs,
  pureLocalToNs,
  pureToAuditQuery,
  pureToQuery,
  type LogFilters
} from '../admin/log-view'
import { api } from '../api/client'
import type { AdminLogPage, AdminLogsTimelineQuery, AuditPage } from '../api/types'
import { keys } from './keys'

export function adminLogsQuery(filters: LogFilters, enabled: boolean) {
  const query = pureToQuery(filters)
  return infiniteQueryOptions({
    queryKey: keys.adminLogs(query),
    queryFn: ({ pageParam, signal }) => api.adminListLogs({ ...pureToQuery(filters, pageParam ?? undefined), signal }),
    initialPageParam: null as string | null,
    // The route's cursor is opaque and always present, so the end of the walk
    // is an empty page rather than a missing cursor.
    getNextPageParam: (last: AdminLogPage) => (last.records.length < PAGE_SIZE ? null : last.cursor || null),
    enabled,
    staleTime: 10_000
  })
}

export function adminAuditQuery(filters: LogFilters, enabled: boolean) {
  const query = pureToAuditQuery(filters)
  return infiniteQueryOptions({
    queryKey: keys.adminAudit(query),
    queryFn: ({ pageParam, signal }) => api.adminListAudit({ ...pureToAuditQuery(filters, pageParam), signal }),
    initialPageParam: null as number | null,
    getNextPageParam: (last: AuditPage) => last.next,
    enabled,
    staleTime: 10_000
  })
}

/** The bucket width is derived from the window rather than left to the
 *  server, so the plot always has about as many bars as it is sized for. */
function timelineQueryParams(filters: LogFilters): AdminLogsTimelineQuery {
  const since = pureLocalToNs(filters.since)
  const until = pureLocalToNs(filters.until)
  return {
    since,
    until,
    levels: [...filters.levels].sort(),
    text: filters.text.trim() || undefined,
    subsystem: filters.subsystem.trim() || undefined,
    request_id: filters.requestId.trim() || undefined,
    bucket_ns: pureBucketNs(since, until)
  }
}

export function adminTimelineQuery(filters: LogFilters, enabled: boolean) {
  const params = timelineQueryParams(filters)
  return queryOptions({
    queryKey: keys.adminTimeline(params),
    queryFn: ({ signal }) => api.adminLogsTimeline({ ...params, signal }),
    enabled,
    staleTime: 10_000,
    // A server without the timeline route still renders the list it can
    // serve, so this failing is not worth retrying.
    retry: false
  })
}
