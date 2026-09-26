import { useCallback, useEffect, useMemo, useRef } from 'react'
import type { MutableRefObject, RefObject } from 'react'
import { createStore, type StoreApi } from 'zustand/vanilla'
import { useStore } from 'zustand'
import type { SearchDone, SearchHit, SearchProgress } from '../../lib/api/client'
import { api } from '../../lib/api/client'
import { EXTENSION_PRESETS, parseExtensions, resolveExtensions } from '../../lib/search/filters'
import { search, type SearchSnapshot } from '../../lib/store/search.store'
import { computeWindow, type WindowResult } from '../../lib/virtual/windowing'
import { initialSearchState, SORT_KEYS, sortHits, toSnapshot, type CategoryId, type SearchPanelState, type SearchStatus } from './search-state'
const FLUSH_MS = 100
const ROW_PX = 56

type StateSetter = <K extends keyof SearchPanelState>(key: K, value: SearchPanelState[K] | ((previous: SearchPanelState[K]) => SearchPanelState[K])) => void

export interface SearchControllerOptions {
  readonly scope: string
  readonly resultsContainer: RefObject<HTMLDivElement | null>
  readonly categoriesRef: RefObject<HTMLDivElement | null>
}

export interface SearchController {
  readonly state: SearchPanelState
  readonly set: StateSetter
  readonly inputRef: MutableRefObject<HTMLInputElement | null>
  readonly view: readonly SearchHit[]
  readonly rows: readonly SearchHit[]
  readonly windowed: WindowResult
  readonly activeCategory: CategoryId
  readonly activeFilters: string
  readonly sortLabelKey: string
  readonly status: SearchStatus
  readonly dirCount: number
  readonly fileCount: number
  readonly start: () => void
  readonly stop: () => void
  readonly clear: () => void
  readonly selectCategory: (id: CategoryId) => void
  readonly saveSnapshot: (overrides?: Partial<SearchSnapshot>) => void
}

function restoredSnapshot(scope: string): SearchSnapshot | null {
  const snapshot = search.getState().snapshot
  return snapshot?.scope === scope ? snapshot : null
}

export function useSearchController({ scope, resultsContainer, categoriesRef }: SearchControllerOptions): SearchController {
  const restored = useMemo(() => restoredSnapshot(scope), [scope])
  const storeRef = useRef<StoreApi<SearchPanelState> | null>(null)
  if (storeRef.current === null) storeRef.current = createStore(() => initialSearchState(restored))
  const store = storeRef.current
  const state = useStore(store)
  const set: StateSetter = useCallback((key, value) => {
    store.setState((current) => ({
      [key]: typeof value === 'function'
        ? (value as (previous: SearchPanelState[typeof key]) => SearchPanelState[typeof key])(current[key])
        : value
    }))
  }, [store])
  const inputRef = useRef<HTMLInputElement | null>(null)
  const cancelRef = useRef<(() => void) | null>(null)
  const arrivingRef = useRef<SearchHit[]>([])
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const generationRef = useRef(0)
  const restoredScrollTopRef = useRef(restored?.scrollTop ?? 0)
  const lastFiltersRef = useRef(`${state.kind}|${state.presets.join(',')}|${state.extQuery.trim().toLowerCase()}`)
  const latestRef = useRef({ scope, ...state })
  latestRef.current = { scope, ...state }

  const flush = useCallback((): void => {
    if (flushTimerRef.current !== null) {
      clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    const arriving = arrivingRef.current.splice(0)
    if (arriving.length === 0) return
    const next = [...latestRef.current.hits, ...arriving]
    latestRef.current.hits = next
    set('hits', next)
  }, [set])

  const scheduleFlush = useCallback((): void => {
    if (flushTimerRef.current !== null) return
    flushTimerRef.current = setTimeout(() => {
      flushTimerRef.current = null
      flush()
    }, FLUSH_MS)
  }, [flush])

  const cancelStream = useCallback((): void => {
    cancelRef.current?.()
    cancelRef.current = null
  }, [])

  const start = useCallback((): void => {
    generationRef.current += 1
    const generation = generationRef.current
    cancelStream()
    if (flushTimerRef.current !== null) {
      clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    arrivingRef.current = []
    latestRef.current.hits = []
    set('hits', [])
    set('failure', null)
    set('truncated', false)
    set('elapsedMs', null)
    set('scanned', null)
    restoredScrollTopRef.current = 0
    set('scrollTop', 0)
    const query = latestRef.current.query.trim()
    if (query === '') {
      set('ran', false)
      set('running', false)
      return
    }
    set('ran', true)
    set('running', true)
    const { kind, presets, extQuery } = latestRef.current
    const stopStream = api.searchStream(
      { query, kind: kind === 'any' ? undefined : kind, exts: resolveExtensions(presets, extQuery), scope: scope || undefined },
      (hit) => {
        if (generation !== generationRef.current) return
        arrivingRef.current.push(hit)
        scheduleFlush()
      },
      (done: SearchDone) => {
        if (generation !== generationRef.current) return
        flush()
        set('running', false)
        cancelRef.current = null
        set('elapsedMs', done.elapsedMs ?? null)
        set('failure', done.error ?? null)
        set('truncated', done.truncated === true)
      },
      (progress: SearchProgress) => {
        if (generation !== generationRef.current) return
        set('scanned', progress)
      }
    )
    if (generation === generationRef.current) cancelRef.current = stopStream
    else stopStream()
  }, [cancelStream, flush, scheduleFlush, scope, set])

  const stop = useCallback((): void => {
    if (!latestRef.current.running) return
    generationRef.current += 1
    cancelStream()
    flush()
    set('running', false)
    set('failure', 'stopped')
    set('truncated', false)
  }, [cancelStream, flush, set])

  const clear = useCallback((): void => {
    generationRef.current += 1
    cancelStream()
    if (flushTimerRef.current !== null) {
      clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    arrivingRef.current = []
    latestRef.current.hits = []
    set('hits', [])
    set('running', false)
    set('ran', false)
    set('failure', null)
    set('truncated', false)
    set('elapsedMs', null)
    set('scanned', null)
    set('query', '')
    set('kind', 'any')
    set('presets', [])
    set('extText', '')
    set('extQuery', '')
    restoredScrollTopRef.current = 0
    set('scrollTop', 0)
    queueMicrotask(() => inputRef.current?.focus())
  }, [cancelStream, set])

  useEffect(() => {
    const strip = categoriesRef.current
    if (!strip) return
    const onWheel = (event: WheelEvent): void => {
      if (event.deltaX !== 0 || strip.scrollWidth <= strip.clientWidth) return
      event.preventDefault()
      strip.scrollLeft += event.deltaY
    }
    strip.addEventListener('wheel', onWheel, { passive: false })
    return () => strip.removeEventListener('wheel', onWheel)
  }, [categoriesRef])

  useEffect(() => {
    const next = `${state.kind}|${state.presets.join(',')}|${state.extQuery.trim().toLowerCase()}`
    const previous = lastFiltersRef.current
    lastFiltersRef.current = next
    if (previous !== next && state.ran) start()
  }, [start, state.extQuery, state.kind, state.presets, state.ran])

  useEffect(() => {
    search.saveSnapshot(toSnapshot(scope, latestRef.current))
  }, [scope, state.query, state.kind, state.presets, state.extText, state.extQuery, state.sortKey, state.hits, state.running, state.ran, state.failure, state.truncated, state.elapsedMs, state.scanned, state.scrollTop])

  const view = state.running ? state.hits : sortHits(state.hits, state.sortKey)
  useEffect(() => {
    const restoredScrollTop = restoredScrollTopRef.current
    if (restoredScrollTop <= 0 || view.length === 0 || !resultsContainer.current) return
    const frame = requestAnimationFrame(() => {
      if (!resultsContainer.current || restoredScrollTopRef.current !== restoredScrollTop) return
      resultsContainer.current.scrollTop = restoredScrollTop
      restoredScrollTopRef.current = 0
      set('scrollTop', restoredScrollTop)
    })
    return () => cancelAnimationFrame(frame)
  }, [resultsContainer, set, view.length])

  useEffect(() => () => {
    generationRef.current += 1
    cancelStream()
    flush()
    const current = latestRef.current
    search.saveSnapshot(toSnapshot(scope, current, {
      hits: current.hits,
      running: false,
      failure: current.running ? 'stopped' : current.failure
    }))
    if (flushTimerRef.current !== null) clearTimeout(flushTimerRef.current)
    flushTimerRef.current = null
  }, [cancelStream, flush, scope])

  const selectCategory = useCallback((id: CategoryId): void => {
    if (id === 'all') {
      set('kind', 'any')
      set('presets', [])
    } else if (id === 'dir') {
      set('kind', 'dir')
      set('presets', [])
    } else if (id === 'file') {
      set('kind', 'file')
      set('presets', [])
    } else {
      set('kind', 'file')
      set('presets', [id])
    }
    set('extText', '')
    set('extQuery', '')
  }, [set])

  const activeCategory: CategoryId = state.kind === 'dir'
    ? 'dir'
    : state.kind === 'file' && state.presets.length === 0
      ? 'file'
      : state.presets.length === 1 && ['document', 'image', 'video', 'audio', 'archive', 'code'].includes(state.presets[0] ?? '')
        ? state.presets[0] as CategoryId
        : 'all'
  const activeFilters = [
    ...state.presets.flatMap((id) => {
      const preset = EXTENSION_PRESETS.find((candidate) => candidate.id === id)
      return preset ? [preset.labelKey] : []
    }),
    ...parseExtensions(state.extQuery)
  ].join(', ')
  const sortLabelKey = SORT_KEYS.find(([key]) => key === state.sortKey)?.[1] ?? 'search.sort_relevance'
  const status: SearchStatus = state.running
    ? { key: /* i18n */ 'search.searching', values: { count: String(state.hits.length), dirs: String(state.scanned?.dirs ?? '') } }
    : !state.ran
      ? { key: '' }
      : state.truncated
        ? { key: /* i18n */ 'search.incomplete', values: { count: String(view.length) } }
        : state.failure === 'stopped'
          ? { key: /* i18n */ 'search.stopped', values: { count: String(view.length) } }
          : state.failure === 'busy'
            ? { key: /* i18n */ 'search.busy' }
            : state.failure === 'network'
              ? { key: /* i18n */ 'search.connection_lost', values: { count: String(view.length) } }
              : state.failure
                ? { key: /* i18n */ 'search.failed' }
                : state.elapsedMs !== null
                  ? { key: /* i18n */ 'search.found_in', values: { count: String(view.length), ms: String(state.elapsedMs) } }
                  : { key: /* i18n */ 'search.found', values: { count: String(view.length) } }
  const windowed = computeWindow({ scrollTop: state.scrollTop, viewportHeight: resultsContainer.current?.clientHeight ?? 480, rowHeight: ROW_PX, itemCount: view.length, overscan: 8 })

  return {
    state,
    set,
    inputRef,
    view,
    rows: view.slice(windowed.start, windowed.end),
    windowed,
    activeCategory,
    activeFilters,
    sortLabelKey,
    status,
    dirCount: view.filter((hit) => hit.entry.kind === 'dir').length,
    fileCount: view.filter((hit) => hit.entry.kind !== 'dir').length,
    start,
    stop,
    clear,
    selectCategory,
    saveSnapshot: (overrides = {}) => search.saveSnapshot(toSnapshot(scope, latestRef.current, overrides))
  }
}
