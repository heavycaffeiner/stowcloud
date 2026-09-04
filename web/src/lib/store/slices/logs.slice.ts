import { createStore, type StoreApi } from 'zustand/vanilla'
import { api } from '../../api/client'
import type {
  AdminLogPage,
  AdminLogQuery,
  AdminLogRecord,
  AdminLogsTimeline,
  AdminLogsTimelineBucket,
  AdminLogsTimelineQuery,
  AdminUser,
  AuditPage,
  AuditQuery,
  AuditRow
} from '../../api/types'
import { setToggle } from '../core/fp'

/**
 * The admin log dashboard's state: what is being filtered, what has been
 * fetched for that filter, and which request is allowed to answer.
 *
 * Two server resources feed one screen. `GET /admin/logs` is what the server
 * recorded about itself, rotated and compressed; `GET /admin/audit` is what
 * accounts did, durable and low volume. They keep separate retention and
 * separate cursors, so they stay separate calls; the merge happens here.
 *
 * A third call, `GET /admin/logs/timeline`, counts both over the whole
 * filtered window. The graph reads that and never the loaded page: a page is
 * the newest 50 of each stream, and a bar chart drawn from it would be a
 * chart of the scroll position rather than of the window.
 *
 * Everything here is a pure transition over immutable data. The store owns
 * the three impure things a filtered, paged list needs and a component must
 * not: a debounce timer, an AbortController, and the request-generation
 * counter that decides whether a response still matters. All three now span
 * both streams and the graph: one generation, one controller, one timer, so
 * a filter change abandons three requests together rather than leaving the
 * graph answering for a window the list has already left.
 */

/** Which of the two streams the screen is showing.
 *
 *  A tri-state rather than two checkboxes because two checkboxes have an
 *  empty state that shows nothing and means nothing, and refusing to
 *  uncheck the last one is a control that lies about being a checkbox. */
export type LogSourceMode = 'all' | 'server' | 'audit'

/** The filters, exactly as the user has them set. Kept separate from the
 *  wire query: this is the form's state, `toQuery` is its projection onto the
 *  parameters the route names. */
export interface LogFilters {
  readonly sourceMode: LogSourceMode
  readonly levels: ReadonlySet<string>
  readonly text: string
  readonly subsystem: string
  readonly requestId: string
  /** `YYYY-MM-DDTHH:mm` in the reader's own zone, which is what a
   *  `datetime-local` control binds. Converted to unix nanoseconds once, on
   *  the way to the wire. */
  readonly since: string
  readonly until: string
}

export interface LogsState {
  readonly filters: LogFilters
  /** The server-log half of the list, newest first. */
  readonly records: readonly AdminLogRecord[]
  /** The audit half of the list, newest first. */
  readonly auditRows: readonly AuditRow[]
  /** Accounts, for the actor-name fallback. Loaded once and best-effort: a
   *  failure here costs a name, not the log. */
  readonly users: readonly AdminUser[]
  /** The server log's opaque cursor; `''` once its walk is exhausted. */
  readonly cursor: string
  /** The audit log's cursor, a `rowid`; `null` once its walk is exhausted. */
  readonly auditNext: number | null
  readonly storedBytes: string
  readonly segments: number
  /** Exact counts over the whole filtered window, for the graph. `null`
   *  before the first answer and while a failed one has nothing to draw. */
  readonly timeline: AdminLogsTimeline | null
  /** The graph failed on its own. Separate from `failed` so a server without
   *  the timeline route still renders the list it can serve. */
  readonly timelineFailed: boolean
  readonly loading: boolean
  readonly loadingMore: boolean
  readonly failed: boolean
  /** Which record has its attributes disclosed, by list key. One at a time:
   *  a record can carry many attributes and expanding all of them is a wall
   *  of text. */
  readonly expandedKey: string | null
  /** Which bucket the graph's roving tab stop sits on, and whose numbers the
   *  readout below the plot is showing. `null` until the reader moves it, so
   *  the first Tab lands on the newest bucket rather than wherever a previous
   *  window left it. */
  readonly focusedBucket: number | null
  /** Bumped for every fetch this store starts. A response carrying a stale
   *  generation is dropped rather than applied, so a slow answer for an
   *  abandoned filter can never overwrite a newer one. */
  readonly generation: number
  /** True once the accumulated list hit its cap with a cursor still left on
   *  either stream, so it stopped growing before the walk was exhausted. */
  readonly truncated: boolean
}

export type LogsAction =
  | { type: 'SET_SOURCE_MODE'; mode: LogSourceMode }
  | { type: 'TOGGLE_LEVEL'; level: string }
  | { type: 'SET_TEXT'; text: string }
  | { type: 'SET_SUBSYSTEM'; subsystem: string }
  | { type: 'SET_REQUEST_ID'; requestId: string }
  | { type: 'SET_SINCE'; since: string }
  | { type: 'SET_UNTIL'; until: string }
  | { type: 'TOGGLE_EXPANDED'; key: string }
  | { type: 'FOCUS_BUCKET'; index: number }
  | { type: 'USERS_LOADED'; users: readonly AdminUser[] }
  | { type: 'FETCH_STARTED'; reset: boolean; generation: number }
  | {
      type: 'RESULT_RECEIVED'
      reset: boolean
      generation: number
      /** `null` means nothing new for this stream this round: it was not
       *  asked for, its walk was already exhausted, or it failed. The two
       *  `*Failed` flags say which of those it was. */
      server: AdminLogPage | null
      audit: AuditPage | null
      timeline: AdminLogsTimeline | null
      listFailed: boolean
      timelineFailed: boolean
    }

/** One page of records per request, per stream. Small enough that the first
 *  screen is quick with many operators on one server, large enough that
 *  load-more is not a treadmill. */
export const PAGE_SIZE = 50

/** Ceiling on the accumulated list. Load-more appends, and without a cap a
 *  long session walks the whole log book into one array and one DOM.
 *
 *  Both streams are newest first and each page is older than the last, so the
 *  cap is reached at the old end: what it drops is the continuation, not
 *  anything already read. `LogsState.truncated` says so, rather than the
 *  load-more quietly doing nothing. */
export const MAX_RECORDS = 1000

/** A settling window before a typed filter reaches the network. A keystroke
 *  is not a request: without this, "refused" is six requests, and with 1200
 *  operators on one server that multiplies by three calls each. */
export const DEBOUNCE_MS = 250

/** The four calls this store makes. Named rather than reached for through
 *  `api`, so a test can supply its own sources against a declared contract. */
export type FetchLogPage = (query: AdminLogQuery) => Promise<AdminLogPage>
export type FetchAuditPage = (query: AuditQuery) => Promise<AuditPage>
export type FetchTimeline = (query: AdminLogsTimelineQuery) => Promise<AdminLogsTimeline>
export type FetchAdminUsers = () => Promise<AdminUser[]>

export interface LogsSources {
  fetchPage: FetchLogPage
  fetchAudit: FetchAuditPage
  fetchTimeline: FetchTimeline
  fetchUsers: FetchAdminUsers
}

export const EMPTY_FILTERS: LogFilters = {
  sourceMode: 'all',
  levels: new Set<string>(),
  text: '',
  subsystem: '',
  requestId: '',
  since: '',
  until: ''
}

/** `YYYY-MM-DDTHH:mm` in local time to unix nanoseconds as a decimal string,
 *  or undefined for an empty or unparsable bound.
 *
 *  BigInt, not a multiply on a number: the millisecond epoch times a million
 *  is past 2^53, so the nanosecond value a number would carry is not the one
 *  the user picked, and a range bound that moves silently is worse than none. */
export function pureLocalToNs(local: string): string | undefined {
  if (local === '') return undefined
  const ms = new Date(local).getTime()
  if (!Number.isFinite(ms)) return undefined
  return (BigInt(ms) * 1_000_000n).toString()
}

/** True when a filter is set that narrows the server log but, by the
 *  timeline endpoint's contract, leaves audit rows untouched.
 *
 *  The screen shows this rather than implying it. An operator who types
 *  `level=ERROR` and still sees audit rows is owed the reason, and the
 *  alternative — quietly dropping every audit row whenever a level is
 *  picked — hides rows the server did not filter out. */
export function pureServerOnlyFiltersActive(filters: LogFilters): boolean {
  return (
    filters.levels.size > 0 ||
    filters.text.trim() !== '' ||
    filters.subsystem.trim() !== '' ||
    filters.requestId.trim() !== ''
  )
}

export function pureIncludesServer(mode: LogSourceMode): boolean {
  return mode !== 'audit'
}

export function pureIncludesAudit(mode: LogSourceMode): boolean {
  return mode !== 'server'
}

/** The filters projected onto the parameters the server log route names. The
 *  cursor is the caller's, since only it knows whether this is a first page
 *  or a continuation. */
export function pureToQuery(filters: LogFilters, cursor?: string): AdminLogQuery {
  return {
    since: pureLocalToNs(filters.since),
    until: pureLocalToNs(filters.until),
    // Sorted so two equal level sets produce one query string, which is what
    // lets an HTTP cache and a staleness comparison both see them as equal.
    levels: [...filters.levels].sort(),
    text: filters.text.trim() || undefined,
    subsystem: filters.subsystem.trim() || undefined,
    request_id: filters.requestId.trim() || undefined,
    limit: PAGE_SIZE,
    ...(cursor === undefined || cursor === '' ? {} : { cursor })
  }
}

/** The filters projected onto the audit route's parameters.
 *
 *  Only the time bounds cross over, and that is the endpoint's rule rather
 *  than a simplification here: an audit row carries no level, no subsystem
 *  and no request id, and `/admin/logs/timeline` counts the audit half of
 *  every bucket narrowed by `since`/`until` alone. Sending anything else
 *  would make the list disagree with the bars above it. */
export function pureToAuditQuery(filters: LogFilters, before?: number | null): AuditQuery {
  return {
    since_ns: pureLocalToNs(filters.since),
    until_ns: pureLocalToNs(filters.until),
    limit: PAGE_SIZE,
    ...(before === undefined || before === null ? {} : { before })
  }
}

/** Candidate bucket widths, coarsest last: 1s, 5s, 15s, 30s, 1m, 5m, 15m,
 *  30m, 1h, 3h, 6h, 12h, 1d, 7d, 30d.
 *
 *  A ladder of round durations rather than `span / target`, because a bar
 *  three and a half minutes wide is a bar nobody can read a time off. */
export const BUCKET_LADDER_NS: readonly bigint[] = [
  1_000_000_000n,
  5_000_000_000n,
  15_000_000_000n,
  30_000_000_000n,
  60_000_000_000n,
  300_000_000_000n,
  900_000_000_000n,
  1_800_000_000_000n,
  3_600_000_000_000n,
  10_800_000_000_000n,
  21_600_000_000_000n,
  43_200_000_000_000n,
  86_400_000_000_000n,
  604_800_000_000_000n,
  2_592_000_000_000_000n
]

/** How many bars the plot is sized for. Enough that a spike is a bar and not
 *  a rounding, few enough that each one stays wide enough to hit and to
 *  arrow onto. */
export const TARGET_BUCKETS = 48

/**
 * The bucket width to ask for, as a decimal nanosecond string, or undefined
 * to let the server pick.
 *
 * Undefined for an open-ended window on purpose: with no bound on one side
 * the client does not know the span, and a guess here would be a worse guess
 * than the server's, which knows what it holds.
 *
 * All BigInt: the span between two real timestamps is past 2^53, so dividing
 * it as a number picks the step for a window that is not the one asked for.
 */
export function pureBucketNs(
  sinceNs?: string,
  untilNs?: string,
  target = TARGET_BUCKETS
): string | undefined {
  if (sinceNs === undefined || untilNs === undefined) return undefined
  const span = BigInt(untilNs) - BigInt(sinceNs)
  if (span <= 0n) return undefined
  const wanted = BigInt(Math.max(1, Math.trunc(target)))
  for (const step of BUCKET_LADDER_NS) {
    if (span / step <= wanted) return step.toString()
  }
  return BUCKET_LADDER_NS[BUCKET_LADDER_NS.length - 1].toString()
}

/** The instant a bucket ends, for the range its accessible name reads out.
 *  Exclusive: the server buckets on `start <= ts < start + width`. */
export function pureBucketEndNs(startNs: string, bucketNs: string): string {
  return (BigInt(startNs) + BigInt(bucketNs)).toString()
}

/** One stacked series. `key` is unique across both halves; `name` is the
 *  bare level or outcome, which is what the catalogue is keyed on. */
export interface TimelineSeries {
  readonly key: string
  readonly source: 'server' | 'audit'
  readonly name: string
}

/** One drawn slice of one bar. Zero-count series are absent rather than
 *  present at zero height: a slice with no height is a legend entry the
 *  reader cannot see, hit or arrow onto. */
export interface TimelineSegment {
  readonly key: string
  readonly source: 'server' | 'audit'
  readonly name: string
  readonly count: number
  /** Share of the tallest bucket's total, so the tallest bar fills the plot
   *  and every other bar is readable against it. */
  readonly percent: number
}

export interface TimelineBar {
  readonly startNs: string
  readonly endNs: string
  readonly total: number
  readonly segments: readonly TimelineSegment[]
}

export interface TimelineView {
  readonly bars: readonly TimelineBar[]
  readonly series: readonly TimelineSeries[]
  /** The tallest bucket's total, which is what the bars are scaled against. */
  readonly max: number
  /** Every counted event in the window. */
  readonly total: number
  readonly bucketNs: string
  readonly truncated: boolean
}

/** The server's levels in severity order, which is the order they stack in.
 *  Local to the bucket maths rather than imported from `ALL_LOG_LEVELS`: the
 *  chart also has to place a level this build has not heard of, so the
 *  canonical list is a prefix here, not the whole set. */
const CANONICAL_LEVELS: readonly string[] = ['DEBUG', 'INFO', 'WARN', 'ERROR']
/** The audit outcomes, worst last for the same reason. */
const CANONICAL_OUTCOMES: readonly string[] = ['ok', 'failed']

function seriesKey(source: 'server' | 'audit', name: string): string {
  return `${source}.${name}`
}

/**
 * Folds adjacent buckets in groups of `n` so a window the server bucketed
 * finer than the plot can draw still draws.
 *
 * Needed because the client cannot always name the width it wants: an
 * open-ended window has no span here, so `bucket_ns` goes unsent and the
 * server answers at whatever width it holds. A day at one minute is 1441
 * bars, which is a plot of four-pixel slivers nobody can hit or arrow onto.
 *
 * Summing counts is exact, which is what makes this safe to do on the client
 * at all: the graph is still counted over the whole window by the server, and
 * folding only changes how finely it is reported. The widened `bucket_ns` and
 * the group's first `start_ns` keep every bar's announced range true.
 *
 * `truncated` rides through untouched: a fold does not un-truncate a walk the
 * server ended early.
 */
export function pureFoldBuckets(timeline: AdminLogsTimeline, groupSize: number): AdminLogsTimeline {
  const n = Math.max(1, Math.trunc(groupSize))
  if (n === 1 || timeline.buckets.length === 0) return timeline

  const buckets: AdminLogsTimelineBucket[] = []
  for (let i = 0; i < timeline.buckets.length; i += n) {
    const server: Record<string, number> = {}
    const audit: Record<string, number> = {}
    const end = Math.min(i + n, timeline.buckets.length)
    for (let j = i; j < end; j++) {
      const b = timeline.buckets[j]
      for (const [k, v] of Object.entries(b.server)) server[k] = (server[k] ?? 0) + v
      for (const [k, v] of Object.entries(b.audit)) audit[k] = (audit[k] ?? 0) + v
    }
    buckets.push({ start_ns: timeline.buckets[i].start_ns, server, audit })
  }
  return {
    bucket_ns: (BigInt(timeline.bucket_ns) * BigInt(n)).toString(),
    buckets,
    truncated: timeline.truncated
  }
}

/** The fold factor that brings a bucket count down to at most `target`.
 *  Ceiling division, so the result never leaves more bars than asked for. */
export function pureFoldFactor(bucketCount: number, target = TARGET_BUCKETS): number {
  const cap = Math.max(1, Math.trunc(target))
  return bucketCount <= cap ? 1 : Math.ceil(bucketCount / cap)
}

/**
 * Buckets to bars: which series are present, how tall each bar is relative to
 * the tallest, and what every number in the plot is.
 *
 * One pass over the buckets collecting per-series totals, then one pass
 * building the bars, rather than a scan per series: the window can be
 * hundreds of buckets and the series count is small but not fixed, so the
 * quadratic version is the one that gets slow exactly when the window is
 * interesting.
 *
 * `mode` drops a half entirely rather than zeroing it. A reader who asked for
 * the audit log alone should not be shown four empty server series in the
 * legend, and a zeroed series is indistinguishable from a real zero.
 *
 * Series with no events anywhere in the window are left out for the same
 * reason: a legend entry that names nothing on the plot is noise, and the
 * data table below already reports the zero.
 *
 * A response finer than `maxBars` is folded first. The server picks the width
 * when the client could not name one, and its answer is frequently far finer
 * than a plot can draw; summing adjacent buckets is exact, so this costs
 * resolution and nothing else.
 */
export function pureTimelineView(
  timeline: AdminLogsTimeline | null,
  mode: LogSourceMode = 'all',
  maxBars = TARGET_BUCKETS
): TimelineView | null {
  if (timeline === null) return null
  const folded = pureFoldBuckets(timeline, pureFoldFactor(timeline.buckets.length, maxBars))

  const wantServer = pureIncludesServer(mode)
  const wantAudit = pureIncludesAudit(mode)

  // Series totals over the window, so an all-zero series never reaches the
  // legend, and the order they were first seen in for the unknown ones.
  const totals = new Map<string, number>()
  const unknownLevels: string[] = []
  const unknownOutcomes: string[] = []
  let max = 0
  let total = 0

  for (const bucket of folded.buckets) {
    let bucketTotal = 0
    if (wantServer) {
      for (const [level, count] of Object.entries(bucket.server)) {
        if (count <= 0) continue
        const key = seriesKey('server', level)
        if (!totals.has(key) && !CANONICAL_LEVELS.includes(level)) unknownLevels.push(level)
        totals.set(key, (totals.get(key) ?? 0) + count)
        bucketTotal += count
      }
    }
    if (wantAudit) {
      for (const [outcome, count] of Object.entries(bucket.audit)) {
        if (count <= 0) continue
        const key = seriesKey('audit', outcome)
        if (!totals.has(key) && !CANONICAL_OUTCOMES.includes(outcome)) unknownOutcomes.push(outcome)
        totals.set(key, (totals.get(key) ?? 0) + count)
        bucketTotal += count
      }
    }
    if (bucketTotal > max) max = bucketTotal
    total += bucketTotal
  }

  // Canonical first so the stack order is stable as filters move, then
  // whatever a newer server sent, sorted so it is at least deterministic.
  const series: TimelineSeries[] = []
  if (wantServer) {
    for (const level of CANONICAL_LEVELS) {
      const key = seriesKey('server', level)
      if (totals.has(key)) series.push({ key, source: 'server', name: level })
    }
    for (const level of [...new Set(unknownLevels)].sort()) {
      series.push({ key: seriesKey('server', level), source: 'server', name: level })
    }
  }
  if (wantAudit) {
    for (const outcome of CANONICAL_OUTCOMES) {
      const key = seriesKey('audit', outcome)
      if (totals.has(key)) series.push({ key, source: 'audit', name: outcome })
    }
    for (const outcome of [...new Set(unknownOutcomes)].sort()) {
      series.push({ key: seriesKey('audit', outcome), source: 'audit', name: outcome })
    }
  }

  const bars: TimelineBar[] = folded.buckets.map((bucket) => {
    const segments: TimelineSegment[] = []
    let bucketTotal = 0
    for (const s of series) {
      const count = (s.source === 'server' ? bucket.server[s.name] : bucket.audit[s.name]) ?? 0
      if (count <= 0) continue
      segments.push({
        key: s.key,
        source: s.source,
        name: s.name,
        count,
        // Guarded rather than assumed non-zero: an all-empty window has a
        // max of 0, and every bar in it is a bar of nothing.
        percent: max === 0 ? 0 : (count / max) * 100
      })
      bucketTotal += count
    }
    return {
      startNs: bucket.start_ns,
      endNs: pureBucketEndNs(bucket.start_ns, folded.bucket_ns),
      total: bucketTotal,
      segments
    }
  })

  return { bars, series, max, total, bucketNs: folded.bucket_ns, truncated: folded.truncated }
}

/**
 * Appends a page to what is already held, newest first, bounded.
 *
 * A plain concat is the whole operation: the server returns pages in order
 * and the walk never revisits, so there is nothing to deduplicate and no
 * reason to scan what is already held. That keeps appending linear in the
 * page rather than quadratic over the accumulation.
 */
export function pureAppendPage<T>(
  held: readonly T[],
  incoming: readonly T[],
  maxRecords = MAX_RECORDS
): readonly T[] {
  if (held.length === 0) return incoming.slice(0, maxRecords)
  const combined = held.concat(incoming)
  return combined.length <= maxRecords ? combined : combined.slice(0, maxRecords)
}

/** The subsystems present in what is on screen, for the suggestion list.
 *  Sorted so appending a page does not reshuffle the suggestions. */
export function pureKnownSubsystems(records: readonly AdminLogRecord[]): readonly string[] {
  return [...new Set(records.map((r) => r.subsystem).filter((s) => s !== ''))].sort()
}

/** A stable list key. `ts_ns` alone repeats when two records share a
 *  nanosecond, so the position in the accumulated walk disambiguates.
 *
 *  A function rather than the expression inline because two call sites have
 *  to agree exactly: the key the list renders each row under, and the key the
 *  disclosure is stored as. Spelled twice they drift, and the disclosure
 *  silently opens nothing. */
export function pureRecordKey(record: AdminLogRecord, index: number): string {
  return `${record.ts_ns}:${index}`
}

/** One row of the merged list, carrying which stream it came from. The tag is
 *  not cosmetic: the two halves obey different filters, so a reader has to be
 *  able to tell at a glance which rows the level filter reached. */
export type UnifiedLogItem =
  | {
      readonly source: 'server'
      readonly key: string
      readonly tsNs: string
      readonly record: AdminLogRecord
    }
  | { readonly source: 'audit'; readonly key: string; readonly tsNs: string; readonly row: AuditRow }

/**
 * The two streams merged into one list, newest first, bounded.
 *
 * Both inputs already arrive newest first, so this is the merge step of a
 * merge sort and nothing else: linear in what it emits, no sort, no scan of
 * what is already placed. Concatenating and sorting would be the version that
 * re-sorts a thousand rows every time fifty arrive.
 *
 * Ties go to the server record. Arbitrary but fixed: a stable order is what
 * keeps a load-more from reshuffling rows the reader is looking at, and two
 * events in the same nanosecond have no true order to recover.
 *
 * Each `ts_ns` is parsed once per advance rather than once per comparison.
 * A merge compares the same head repeatedly and BigInt parsing is the
 * expensive half of the comparison.
 */
export function pureInterleave(
  records: readonly AdminLogRecord[],
  rows: readonly AuditRow[],
  maxItems = MAX_RECORDS
): readonly UnifiedLogItem[] {
  const cap = Math.min(maxItems, records.length + rows.length)
  if (cap <= 0) return []
  const out: UnifiedLogItem[] = []
  let i = 0
  let j = 0
  let left = i < records.length ? BigInt(records[i].ts_ns) : 0n
  let right = j < rows.length ? BigInt(rows[j].ts_ns) : 0n

  while (out.length < cap) {
    const takeServer = j >= rows.length || (i < records.length && left >= right)
    if (takeServer) {
      const record = records[i]
      out.push({
        source: 'server',
        key: `server:${pureRecordKey(record, i)}`,
        tsNs: record.ts_ns,
        record
      })
      i += 1
      if (i < records.length) left = BigInt(records[i].ts_ns)
    } else {
      const row = rows[j]
      // `rowid` is unique per row, so unlike a server record it needs no
      // position to disambiguate and keeps its key across a load-more.
      out.push({ source: 'audit', key: `audit:${row.rowid}`, tsNs: row.ts_ns, row })
      j += 1
      if (j < rows.length) right = BigInt(rows[j].ts_ns)
    }
  }
  return out
}

/** How an audit row's actor should be named, before any of it is translated.
 *  A descriptor rather than a string so the chain stays pure and testable and
 *  the catalogue stays in the component. */
export type ActorLabel =
  | { readonly kind: 'system' }
  | { readonly kind: 'name'; readonly name: string }
  | { readonly kind: 'id'; readonly id: number }

/**
 * Who a row is attributed to, in the order the sources are trustworthy.
 *
 * 1. `actor_name`, the server's own join, which is the only source that knows
 *    about an account this admin list does not carry.
 * 2. the loaded admin users, `display_name` then `name`, for a row whose
 *    `actor_name` the server could not fill in.
 * 3. the bare id, which is the honest answer when nothing else has a name.
 *
 * The step that used to be missing is the first one, and the step that used
 * to be wrong is the second: reading `display_name` alone means an account
 * with a blank display name falls all the way through to `User #1` while the
 * server was sending its name the whole time.
 */
export function pureActorLabel(row: AuditRow, users: readonly AdminUser[]): ActorLabel {
  if (row.actor === null) return { kind: 'system' }
  const fromServer = (row.actor_name ?? '').trim()
  if (fromServer !== '') return { kind: 'name', name: fromServer }
  const user = users.find((u) => u.id === row.actor)
  const local = (user?.display_name ?? '').trim() || (user?.name ?? '').trim()
  if (local !== '') return { kind: 'name', name: local }
  return { kind: 'id', id: row.actor }
}

/** Every filter action is the same immutable two-level patch, and there are
 *  seven of them; spelled out per case a missed spread mutates shared state. */
function withFilters(state: LogsState, patch: Partial<LogFilters>): LogsState {
  return { ...state, filters: { ...state.filters, ...patch } }
}

export function logsReducer(state: LogsState, action: LogsAction): LogsState {
  switch (action.type) {
    case 'SET_SOURCE_MODE':
      return withFilters(state, { sourceMode: action.mode })
    case 'TOGGLE_LEVEL':
      return withFilters(state, { levels: setToggle(state.filters.levels, action.level) })
    case 'SET_TEXT':
      return withFilters(state, { text: action.text })
    case 'SET_SUBSYSTEM':
      return withFilters(state, { subsystem: action.subsystem })
    case 'SET_REQUEST_ID':
      return withFilters(state, { requestId: action.requestId })
    case 'SET_SINCE':
      return withFilters(state, { since: action.since })
    case 'SET_UNTIL':
      return withFilters(state, { until: action.until })
    case 'TOGGLE_EXPANDED':
      return { ...state, expandedKey: state.expandedKey === action.key ? null : action.key }
    case 'FOCUS_BUCKET':
      return { ...state, focusedBucket: action.index }
    case 'USERS_LOADED':
      return { ...state, users: action.users }
    case 'FETCH_STARTED':
      return {
        ...state,
        generation: action.generation,
        loading: action.reset,
        loadingMore: !action.reset,
        // A refetch clears the previous failure so a recovered request does
        // not render an error beside the records it just delivered.
        failed: false,
        timelineFailed: action.reset ? false : state.timelineFailed,
        // A new filter invalidates both pointers into the previous result:
        // the disclosed record and the focused bucket are not in the incoming
        // one, and a stale bucket index reads out numbers from another window.
        expandedKey: action.reset ? null : state.expandedKey,
        focusedBucket: action.reset ? null : state.focusedBucket
      }
    case 'RESULT_RECEIVED': {
      // The guard that makes out-of-order responses safe. Without it a slow
      // first request answering after a fast second one replaces newer
      // records with older ones and the list silently disagrees with the form.
      if (action.generation !== state.generation) return state

      const records =
        action.server === null
          ? action.reset
            ? []
            : state.records
          : action.reset
            ? action.server.records.slice(0, MAX_RECORDS)
            : pureAppendPage(state.records, action.server.records)
      const auditRows =
        action.audit === null
          ? action.reset
            ? []
            : state.auditRows
          : action.reset
            ? action.audit.rows.slice(0, MAX_RECORDS)
            : pureAppendPage(state.auditRows, action.audit.rows)

      // A stream that answered reports its own cursor. One that did not keeps
      // the cursor it had, except on a reset, where an unasked-for stream has
      // no walk left to continue.
      const cursor = action.server !== null ? action.server.cursor : action.reset ? '' : state.cursor
      const auditNext =
        action.audit !== null ? action.audit.next : action.reset ? null : state.auditNext

      const held = records.length + auditRows.length
      return {
        ...state,
        records,
        auditRows,
        cursor,
        auditNext,
        storedBytes: action.server !== null ? action.server.stored_bytes : state.storedBytes,
        segments: action.server !== null ? action.server.segments : state.segments,
        // A load-more leaves the graph alone: it is already exact over the
        // window, and one more page of the list changes nothing about it.
        timeline: action.timeline ?? (action.reset && action.timelineFailed ? null : state.timeline),
        timelineFailed: action.timelineFailed,
        loading: false,
        loadingMore: false,
        failed: action.listFailed,
        truncated: held >= MAX_RECORDS && (cursor !== '' || auditNext !== null)
      }
    }
    default:
      return state
  }
}

export function initialLogsState(): LogsState {
  return {
    filters: EMPTY_FILTERS,
    records: [],
    auditRows: [],
    users: [],
    cursor: '',
    auditNext: null,
    storedBytes: '0',
    segments: 0,
    timeline: null,
    timelineFailed: false,
    loading: true,
    loadingMore: false,
    failed: false,
    expandedKey: null,
    focusedBucket: null,
    generation: 0,
    truncated: false
  }
}

export interface LogsStore extends StoreApi<LogsState> {
  dispatch(action: LogsAction): void
  /** Fetch the first page of both streams and the graph for the filters as
   *  they now stand, immediately. */
  refresh(): void
  /** Apply a filter action and refetch after the settling window. For the
   *  typed and picked controls, where every intermediate value would
   *  otherwise be three requests. */
  changeFilter(action: LogsAction): void
  /** Follow both cursors for one more page each. Does nothing when both walks
   *  are exhausted, when one is already in flight, or at the retention cap. */
  loadMore(): void
  /** Cancel the timer and any request in flight. Called when the section
   *  unmounts, so a dashboard left behind stops costing the server. */
  dispose(): void
}

/** A rejection that is only an abort. The three calls are cancelled together,
 *  and an abort is the expected end of a superseded request, not a failure to
 *  put on the screen. */
function isAbort(reason: unknown): boolean {
  return reason instanceof DOMException
    ? reason.name === 'AbortError'
    : reason instanceof Error && reason.name === 'AbortError'
}

export function createLogsStore(sources: Partial<LogsSources> = {}): LogsStore {
  const fetchPage = sources.fetchPage ?? api.adminListLogs
  const fetchAudit = sources.fetchAudit ?? api.adminListAudit
  const fetchTimeline = sources.fetchTimeline ?? api.adminLogsTimeline
  const fetchUsers = sources.fetchUsers ?? api.adminListUsers

  const store = createStore<LogsState>(() => initialLogsState())

  // The three impure things, held here and nowhere else. A component never
  // sees them; the reducer never touches them.
  let timer: number | null = null
  let inFlight: AbortController | null = null
  let generation = 0

  function apply(action: LogsAction): void {
    store.setState((prev) => logsReducer(prev, action))
  }

  function cancelTimer(): void {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  function abortInFlight(): void {
    if (inFlight !== null) {
      inFlight.abort()
      inFlight = null
    }
  }

  async function run(reset: boolean): Promise<void> {
    // A filter change abandons the requests in flight rather than racing
    // them. The generation bump means even a response already past the abort
    // check is dropped by the reducer.
    abortInFlight()
    const controller = new AbortController()
    inFlight = controller
    generation += 1
    const mine = generation

    const { cursor, auditNext, filters } = store.getState()
    const wantServer = pureIncludesServer(filters.sourceMode)
    const wantAudit = pureIncludesAudit(filters.sourceMode)
    apply({ type: 'FETCH_STARTED', reset, generation: mine })

    // A continuation only asks the streams that still have a walk left, so a
    // load-more with one side exhausted is one request, not two.
    const askServer = wantServer && (reset || cursor !== '')
    const askAudit = wantAudit && (reset || auditNext !== null)

    const query = pureToQuery(filters, reset ? undefined : cursor)
    const serverCall: Promise<AdminLogPage | null> = askServer
      ? fetchPage({ ...query, signal: controller.signal })
      : Promise.resolve(null)
    const auditCall: Promise<AuditPage | null> = askAudit
      ? fetchAudit({
          ...pureToAuditQuery(filters, reset ? undefined : auditNext),
          signal: controller.signal
        })
      : Promise.resolve(null)
    // The graph is exact over the window, so a continuation does not move it.
    const timelineCall: Promise<AdminLogsTimeline | null> = reset
      ? fetchTimeline({
          since: query.since,
          until: query.until,
          levels: query.levels,
          text: query.text,
          subsystem: query.subsystem,
          request_id: query.request_id,
          bucket_ns: pureBucketNs(query.since, query.until),
          signal: controller.signal
        })
      : Promise.resolve(null)

    const [serverRes, auditRes, timelineRes] = await Promise.allSettled([
      serverCall,
      auditCall,
      timelineCall
    ])

    // Nothing to report for a request the store itself cancelled: either a
    // newer one is already running, or the section is gone.
    if (controller.signal.aborted) {
      if (inFlight === controller) inFlight = null
      return
    }

    const failed = (r: PromiseSettledResult<unknown>): boolean =>
      r.status === 'rejected' && !isAbort(r.reason)

    apply({
      type: 'RESULT_RECEIVED',
      reset,
      generation: mine,
      server: serverRes.status === 'fulfilled' ? serverRes.value : null,
      audit: auditRes.status === 'fulfilled' ? auditRes.value : null,
      timeline: timelineRes.status === 'fulfilled' ? timelineRes.value : null,
      // The list fails when a stream that was asked for did not answer. The
      // graph is judged separately: a server without the timeline route still
      // serves the list, and losing the chart must not empty the screen.
      listFailed: failed(serverRes) || failed(auditRes),
      timelineFailed: failed(timelineRes)
    })

    if (inFlight === controller) inFlight = null
  }

  function refresh(): void {
    cancelTimer()
    void run(true)
  }

  function changeFilter(action: LogsAction): void {
    apply(action)
    cancelTimer()
    timer = window.setTimeout(() => {
      timer = null
      void run(true)
    }, DEBOUNCE_MS)
  }

  function loadMore(): void {
    const { cursor, auditNext, loading, loadingMore, records, auditRows } = store.getState()
    if (cursor === '' && auditNext === null) return
    if (loading || loadingMore) return
    if (records.length + auditRows.length >= MAX_RECORDS) return
    void run(false)
  }

  // Best-effort and outside the generation counter: the account list is not
  // part of any filter, it only supplies a fallback name, and a failure here
  // must not read as the log failing to load.
  void fetchUsers()
    .then((users) => apply({ type: 'USERS_LOADED', users }))
    .catch(() => {})

  return {
    ...store,
    dispatch(action: LogsAction): void {
      store.setState((prev) => logsReducer(prev, action))
    },
    refresh,
    changeFilter,
    loadMore,
    dispose(): void {
      cancelTimer()
      abortInFlight()
    }
  }
}
