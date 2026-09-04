import { createStore, type StoreApi } from 'zustand/vanilla'
import { api } from '../../api/client'
import type { AdminLogPage, AdminLogQuery, AdminLogRecord } from '../../api/types'
import { setToggle } from '../core/fp'

/**
 * The admin log dashboard's state: what is being filtered, what has been
 * fetched for that filter, and which request is allowed to answer.
 *
 * Everything here is a pure transition over immutable data. The store owns
 * the three impure things a filtered, paged list needs and a component must
 * not: a debounce timer, an AbortController, and the request-generation
 * counter that decides whether a response still matters.
 */

/** The filters, exactly as the user has them set. Kept separate from the
 *  wire query: this is the form's state, `toQuery` is its projection onto the
 *  parameters the route names. */
export interface LogFilters {
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
  readonly records: readonly AdminLogRecord[]
  readonly cursor: string
  readonly storedBytes: string
  readonly segments: number
  readonly loading: boolean
  readonly loadingMore: boolean
  readonly failed: boolean
  /** Which record has its attributes disclosed, by list key. One at a time:
   *  a record can carry many attributes and expanding all of them is a wall
   *  of text. */
  readonly expandedKey: string | null
  /** Bumped for every fetch this store starts. A response carrying a stale
   *  generation is dropped rather than applied, so a slow answer for an
   *  abandoned filter can never overwrite a newer one. */
  readonly generation: number
  /** True once a page came back short of the cap, so the accumulated list
   *  stopped growing before the walk was exhausted. */
  readonly truncated: boolean
}

export type LogsAction =
  | { type: 'TOGGLE_LEVEL'; level: string }
  | { type: 'SET_TEXT'; text: string }
  | { type: 'SET_SUBSYSTEM'; subsystem: string }
  | { type: 'SET_REQUEST_ID'; requestId: string }
  | { type: 'SET_SINCE'; since: string }
  | { type: 'SET_UNTIL'; until: string }
  | { type: 'TOGGLE_EXPANDED'; key: string }
  | { type: 'FETCH_STARTED'; reset: boolean; generation: number }
  | { type: 'FETCH_FAILED'; generation: number }
  | { type: 'PAGE_RECEIVED'; reset: boolean; generation: number; page: AdminLogPage }

/** One page of records per request. Small enough that the first screen is
 *  quick with many operators on one server, large enough that load-more is
 *  not a treadmill. */
export const PAGE_SIZE = 50

/** Ceiling on the accumulated list. Load-more appends, and without a cap a
 *  long session walks the whole log book into one array and one DOM.
 *
 *  Records are newest first and each page is older than the last, so the cap
 *  is reached at the old end: what it drops is the continuation, not anything
 *  already read. `LogsState.truncated` says so, rather than the load-more
 *  quietly doing nothing. */
export const MAX_RECORDS = 1000

/** A settling window before a typed filter reaches the network. A keystroke
 *  is not a request: without this, "refused" is six requests, and with 1200
 *  operators on one server that multiplies. */
export const DEBOUNCE_MS = 250

/** The one call this store makes. Named rather than inferred from `api`, so
 *  a test can supply its own page source against a declared contract. */
export type FetchLogPage = (query: AdminLogQuery) => Promise<AdminLogPage>

export const EMPTY_FILTERS: LogFilters = {
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

/** The filters projected onto the parameters the route names. The cursor is
 *  the caller's, since only it knows whether this is a first page or a
 *  continuation. */
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

/**
 * Appends a page to what is already held, newest first, bounded.
 *
 * A plain concat is the whole operation: the server returns pages in order
 * and the walk never revisits, so there is nothing to deduplicate and no
 * reason to scan what is already held. That keeps appending linear in the
 * page rather than quadratic over the accumulation.
 */
export function pureAppendPage(
  held: readonly AdminLogRecord[],
  incoming: readonly AdminLogRecord[],
  maxRecords = MAX_RECORDS
): readonly AdminLogRecord[] {
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

/** Every filter action is the same immutable two-level patch, and there are
 *  six of them; spelled out per case a missed spread mutates shared state. */
function withFilters(state: LogsState, patch: Partial<LogFilters>): LogsState {
  return { ...state, filters: { ...state.filters, ...patch } }
}

export function logsReducer(state: LogsState, action: LogsAction): LogsState {
  switch (action.type) {
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
    case 'FETCH_STARTED':
      return {
        ...state,
        generation: action.generation,
        loading: action.reset,
        loadingMore: !action.reset,
        // A refetch clears the previous failure so a recovered request does
        // not render an error beside the records it just delivered.
        failed: false,
        // A new filter invalidates the disclosure: the record it named is not
        // in the incoming result.
        expandedKey: action.reset ? null : state.expandedKey
      }
    case 'FETCH_FAILED':
      // A late failure for an abandoned filter is not this list's failure.
      if (action.generation !== state.generation) return state
      return { ...state, loading: false, loadingMore: false, failed: true }
    case 'PAGE_RECEIVED': {
      // The guard that makes out-of-order responses safe. Without it a slow
      // first request answering after a fast second one replaces newer
      // records with older ones and the list silently disagrees with the form.
      if (action.generation !== state.generation) return state
      const records = action.reset
        ? action.page.records.slice(0, MAX_RECORDS)
        : pureAppendPage(state.records, action.page.records)
      return {
        ...state,
        records,
        cursor: action.page.cursor,
        storedBytes: action.page.stored_bytes,
        segments: action.page.segments,
        loading: false,
        loadingMore: false,
        failed: false,
        truncated: records.length >= MAX_RECORDS && action.page.cursor !== ''
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
    cursor: '',
    storedBytes: '0',
    segments: 0,
    loading: true,
    loadingMore: false,
    failed: false,
    expandedKey: null,
    generation: 0,
    truncated: false
  }
}

export interface LogsStore extends StoreApi<LogsState> {
  dispatch(action: LogsAction): void
  /** Fetch the first page for the filters as they now stand, immediately. */
  refresh(): void
  /** Apply a filter action and refetch after the settling window. For the
   *  typed and picked controls, where every intermediate value would
   *  otherwise be a request. */
  changeFilter(action: LogsAction): void
  /** Follow the cursor for one more page. Does nothing when the walk is
   *  exhausted, when one is already in flight, or at the retention cap. */
  loadMore(): void
  /** Cancel the timer and any request in flight. Called when the section
   *  unmounts, so a dashboard left behind stops costing the server. */
  dispose(): void
}

export function createLogsStore(fetchPage: FetchLogPage = api.adminListLogs): LogsStore {
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
    // A filter change abandons the request in flight rather than racing it.
    // The generation bump means even a response already past the abort check
    // is dropped by the reducer.
    abortInFlight()
    const controller = new AbortController()
    inFlight = controller
    generation += 1
    const mine = generation

    const { cursor, filters } = store.getState()
    apply({ type: 'FETCH_STARTED', reset, generation: mine })

    try {
      const page = await fetchPage({
        ...pureToQuery(filters, reset ? undefined : cursor),
        signal: controller.signal
      })
      apply({ type: 'PAGE_RECEIVED', reset, generation: mine, page })
    } catch {
      // An abort is the expected end of a superseded request, not a failure
      // to report. Its generation is already stale, so the reducer drops it
      // either way and no error reaches the screen.
      apply({ type: 'FETCH_FAILED', generation: mine })
    } finally {
      if (inFlight === controller) inFlight = null
    }
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
    const { cursor, loading, loadingMore, records } = store.getState()
    if (cursor === '' || loading || loadingMore || records.length >= MAX_RECORDS) return
    void run(false)
  }

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
