import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { MutableRefObject, RefObject } from 'react'
import { useEventListener } from '../../../hooks/use-event-listener'
import { resolveExtensions } from '../logic/filters'
import { saveSnapshot, snapshotFor, type SearchSnapshot } from '../state'
import {
  initialSearchState,
  toSnapshot,
  type CategoryId,
  type SearchPanelState,
  type SearchStatus
} from '../logic/search-state'
import {
  activeCategoryFor,
  activeFiltersFor,
  sortLabelKeyFor,
  statusFor,
  viewFor,
  windowFor
} from '../logic/search-selectors'
import type { WindowResult } from '../../../lib/virtual/windowing'
import { searchStream, type SearchDone, type SearchHit, type SearchProgress } from '../api'
const FLUSH_MS = 100

type StateSetter = <K extends keyof SearchPanelState>(
  key: K,
  value: SearchPanelState[K] | ((previous: SearchPanelState[K]) => SearchPanelState[K])
) => void

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

export function useSearchController({
  scope,
  resultsContainer,
  categoriesRef
}: SearchControllerOptions): SearchController {
  const [restored] = useState(() => snapshotFor(scope))
  const [state, setState] = useState(() => initialSearchState(restored))
  const set: StateSetter = useCallback((key, value) => {
    setState((current) => ({
      ...current,
      [key]:
        typeof value === 'function'
          ? (value as (previous: SearchPanelState[typeof key]) => SearchPanelState[typeof key])(current[key])
          : value
    }))
  }, [])
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
    const stopStream = searchStream(
      {
        query,
        kind: kind === 'any' ? undefined : kind,
        exts: resolveExtensions(presets, extQuery),
        scope: scope || undefined
      },
      (hit: SearchHit) => {
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

  // Vertical wheel scrolls the category strip sideways; React's onWheel is passive and cannot prevent the page scroll.
  useEventListener(
    categoriesRef,
    'wheel',
    (event) => {
      const strip = categoriesRef.current
      if (!strip || event.deltaX !== 0 || strip.scrollWidth <= strip.clientWidth) return
      event.preventDefault()
      strip.scrollLeft += event.deltaY
    },
    { passive: false }
  )

  useEffect(() => {
    const next = `${state.kind}|${state.presets.join(',')}|${state.extQuery.trim().toLowerCase()}`
    const previous = lastFiltersRef.current
    lastFiltersRef.current = next
    if (previous !== next && state.ran) start()
  }, [start, state.extQuery, state.kind, state.presets, state.ran])

  useEffect(() => {
    saveSnapshot(toSnapshot(scope, latestRef.current))
  }, [
    scope,
    state.query,
    state.kind,
    state.presets,
    state.extText,
    state.extQuery,
    state.sortKey,
    state.hits,
    state.running,
    state.ran,
    state.failure,
    state.truncated,
    state.elapsedMs,
    state.scanned,
    state.scrollTop
  ])

  const view = viewFor(state)
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

  useEffect(
    () => () => {
      generationRef.current += 1
      cancelStream()
      flush()
      const current = latestRef.current
      saveSnapshot(
        toSnapshot(scope, current, {
          hits: current.hits,
          running: false,
          failure: current.running ? 'stopped' : current.failure
        })
      )
      if (flushTimerRef.current !== null) clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    },
    [cancelStream, flush, scope]
  )

  const selectCategory = useCallback(
    (id: CategoryId): void => {
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
    },
    [set]
  )

  const activeCategory = activeCategoryFor(state)
  const activeFilters = activeFiltersFor(state)
  const sortLabelKey = sortLabelKeyFor(state)
  const status: SearchStatus = statusFor(state, view)
  const windowed = windowFor(state, view, resultsContainer.current?.clientHeight ?? 480)

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
    saveSnapshot: (overrides = {}) => saveSnapshot(toSnapshot(scope, latestRef.current, overrides))
  }
}
