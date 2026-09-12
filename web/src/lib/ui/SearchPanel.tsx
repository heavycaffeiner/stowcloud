import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import type { FormEvent, KeyboardEvent, ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import type { SearchDone, SearchHit, SearchProgress } from '../api/client'
import { api } from '../api/client'
import { parentOf } from '../api/path-utils'
import { formatBytes } from '../format/bytes'
import { formatDateNs, formatNumber } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { EXTENSION_PRESETS, parseExtensions, resolveExtensions } from '../search/filters'
import { search, type SearchSnapshot, type SearchSortKey } from '../store/search.store'
import { computeWindow } from '../virtual/windowing'
import { Button } from './Button'
import { TextField } from './TextField'
import { Icon } from './Icon'
import './SearchPanel.css'

export interface SearchPanelHandle {
  focus: () => void
}

export interface SearchPanelProps {
  /** The folder search started from. It ranks that subtree but never narrows the search. */
  scope?: string
  autofocus?: boolean
  /** Called immediately before navigating to a result. */
  onnavigated?: () => void
  /** Content rendered at the end of the query row, such as a sheet close button. */
  trailing?: ReactNode
}

type Kind = 'any' | 'file' | 'dir'
type SortKey = SearchSortKey
type SearchFailure = string | null

const FLUSH_MS = 100
const ROW_PX = 60
const SORT_KEYS: readonly [SortKey, string][] = [
  ['relevance', /* i18n */ 'search.sort_relevance'],
  ['name', /* i18n */ 'search.sort_name'],
  ['size', /* i18n */ 'search.sort_size'],
  ['date', /* i18n */ 'search.sort_date']
]

function restoredSnapshot(scope: string): SearchSnapshot | null {
  const snapshot = search.getState().snapshot
  return snapshot?.scope === scope ? snapshot : null
}

function folderOf(path: string): string {
  return parentOf(path)
}

function sortHits(list: readonly SearchHit[], key: SortKey): readonly SearchHit[] {
  if (key === 'relevance') return list
  const result = [...list]
  if (key === 'name') {
    result.sort((a, b) => a.entry.name.localeCompare(b.entry.name))
  } else if (key === 'size') {
    result.sort((a, b) => b.entry.size - a.entry.size)
  } else {
    result.sort((a, b) => {
      const left = BigInt(b.entry.mtime_ns || '0')
      const right = BigInt(a.entry.mtime_ns || '0')
      return left === right ? 0 : left > right ? 1 : -1
    })
  }
  return result
}

export const SearchPanel = forwardRef<SearchPanelHandle, SearchPanelProps>(function SearchPanel({
  scope = '',
  autofocus = false,
  onnavigated,
  trailing
}, ref) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const restored = useMemo(() => restoredSnapshot(scope), [scope])
  const [query, setQuery] = useState(restored?.query ?? '')
  const [kind, setKindState] = useState<Kind>(restored?.kind ?? 'any')
  const [presets, setPresets] = useState<readonly string[]>(restored ? [...restored.presets] : [])
  const [extText, setExtText] = useState(restored?.extText ?? '')
  const [extQuery, setExtQuery] = useState(restored?.extQuery ?? '')
  const [sortKey, setSortKey] = useState<SortKey>(restored?.sortKey ?? 'relevance')
  const [hits, setHits] = useState<readonly SearchHit[]>(restored?.hits ?? [])
  const [running, setRunning] = useState(restored?.running ?? false)
  const [ran, setRan] = useState(restored?.ran ?? false)
  const [failure, setFailure] = useState<SearchFailure>(restored?.running ? 'stopped' : restored?.failure ?? null)
  const [truncated, setTruncated] = useState(restored?.truncated ?? false)
  const [elapsedMs, setElapsedMs] = useState<number | null>(restored?.elapsedMs ?? null)
  const [scanned, setScanned] = useState<SearchProgress | null>(restored?.scanned ?? null)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [sortOpen, setSortOpen] = useState(false)
  const [scrollTop, setScrollTop] = useState(restored?.scrollTop ?? 0)

  const fieldContainer = useRef<HTMLDivElement>(null)
  const resultsContainer = useRef<HTMLDivElement>(null)
  const cancelRef = useRef<(() => void) | null>(null)
  const arrivingRef = useRef<SearchHit[]>([])
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const generationRef = useRef(0)
  const restoredScrollTopRef = useRef(restored?.scrollTop ?? 0)
  const lastFiltersRef = useRef(`${kind}|${presets.join(',')}|${extQuery.trim().toLowerCase()}`)
  const latestRef = useRef({
    scope,
    query,
    kind,
    presets,
    extText,
    extQuery,
    sortKey,
    hits,
    running,
    ran,
    failure,
    truncated,
    elapsedMs,
    scanned,
    scrollTop
  })
  latestRef.current = {
    scope,
    query,
    kind,
    presets,
    extText,
    extQuery,
    sortKey,
    hits,
    running,
    ran,
    failure,
    truncated,
    elapsedMs,
    scanned,
    scrollTop
  }

  useImperativeHandle(ref, () => ({
    focus: () => {
      const input = fieldContainer.current?.querySelector<HTMLElement>('input')
      input?.focus()
    }
  }), [])

  const flush = (): void => {
    if (flushTimerRef.current !== null) {
      clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    const arriving = arrivingRef.current.splice(0)
    if (arriving.length > 0) {
      const next = [...latestRef.current.hits, ...arriving]
      latestRef.current.hits = next
      setHits(next)
    }
  }

  const scheduleFlush = (): void => {
    if (flushTimerRef.current !== null) return
    flushTimerRef.current = setTimeout(() => {
      flushTimerRef.current = null
      flush()
    }, FLUSH_MS)
  }

  const cancelStream = (): void => {
    cancelRef.current?.()
    cancelRef.current = null
  }

  const start = (): void => {
    generationRef.current += 1
    const generation = generationRef.current
    cancelStream()
    if (flushTimerRef.current !== null) {
      clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    arrivingRef.current = []
    latestRef.current.hits = []
    setHits([])
    setFailure(null)
    setTruncated(false)
    setElapsedMs(null)
    setScanned(null)
    restoredScrollTopRef.current = 0
    setScrollTop(0)
    if (query.trim() === '') {
      setRan(false)
      setRunning(false)
      return
    }

    setRan(true)
    setRunning(true)
    const stopStream = api.searchStream(
      {
        query: query.trim(),
        kind: kind === 'any' ? undefined : kind,
        exts: resolveExtensions(presets, extQuery),
        scope: scope || undefined
      },
      (hit) => {
        if (generation !== generationRef.current) return
        arrivingRef.current.push(hit)
        scheduleFlush()
      },
      (done: SearchDone) => {
        if (generation !== generationRef.current) return
        flush()
        setRunning(false)
        cancelRef.current = null
        setElapsedMs(done.elapsedMs ?? null)
        setFailure(done.error ?? null)
        setTruncated(done.truncated === true)
      },
      (progress: SearchProgress) => {
        if (generation !== generationRef.current) return
        setScanned(progress)
      }
    )
    if (generation === generationRef.current) cancelRef.current = stopStream
    else stopStream()
  }

  const stop = (): void => {
    if (!running) return
    generationRef.current += 1
    cancelStream()
    flush()
    setRunning(false)
    setFailure('stopped')
    setTruncated(false)
  }

  const clear = (): void => {
    generationRef.current += 1
    cancelStream()
    if (flushTimerRef.current !== null) {
      clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    arrivingRef.current = []
    latestRef.current.hits = []
    setHits([])
    setRunning(false)
    setRan(false)
    setFailure(null)
    setTruncated(false)
    setElapsedMs(null)
    setScanned(null)
    setQuery('')
    setKindState('any')
    setPresets([])
    setExtText('')
    setExtQuery('')
    restoredScrollTopRef.current = 0
    setScrollTop(0)
    queueMicrotask(() => fieldContainer.current?.querySelector<HTMLElement>('input')?.focus())
  }

  useEffect(() => {
    const next = `${kind}|${presets.join(',')}|${extQuery.trim().toLowerCase()}`
    const previous = lastFiltersRef.current
    lastFiltersRef.current = next
    if (previous !== next && ran) start()
  }, [kind, presets, extQuery, ran])


  const chooseKind = (next: Kind): void => {
    setKindState(next)
    if (next === 'dir') {
      setPresets([])
      setExtText('')
      setExtQuery('')
    }
  }

  const commitExtensions = (): void => setExtQuery(extText)

  const view = running ? hits : sortHits(hits, sortKey)
  const dirCount = view.filter((hit) => hit.entry.kind === 'dir').length
  const fileCount = view.length - dirCount
  const activeFilters = [
    ...presets.flatMap((id) => {
      const preset = EXTENSION_PRESETS.find((candidate) => candidate.id === id)
      return preset ? [t(preset.labelKey)] : []
    }),
    ...parseExtensions(extQuery)
  ].join(', ')
  const sortLabel = t(SORT_KEYS.find(([key]) => key === sortKey)?.[1] ?? 'search.sort_relevance')

  const statusText = running
    ? `${t('search.searching', { count: hits.length })}${scanned ? `, ${t('search.scanning', { dirs: formatNumber(scanned.dirs) })}` : ''}`
    : !ran
      ? ''
      : truncated
        ? t('search.incomplete', { count: view.length })
        : failure === 'stopped'
          ? t('search.stopped', { count: view.length })
          : failure === 'busy'
            ? t('search.busy')
            : failure === 'network'
              ? t('search.connection_lost', { count: view.length })
              : failure
                ? t('search.failed')
                : elapsedMs !== null
                  ? t('search.found_in', { count: view.length, ms: elapsedMs })
                  : t('search.found', { count: view.length })

  const viewportHeight = resultsContainer.current?.clientHeight ?? 480
  const windowed = computeWindow({
    scrollTop,
    viewportHeight,
    rowHeight: ROW_PX,
    itemCount: view.length,
    overscan: 8
  })
  const rows = view.slice(windowed.start, windowed.end)

  const snapshot = (overrides: Partial<SearchSnapshot> = {}): SearchSnapshot => ({
    scope,
    query,
    kind,
    presets,
    extText,
    extQuery,
    sortKey,
    hits,
    running,
    ran,
    failure,
    truncated,
    elapsedMs,
    scanned,
    scrollTop,
    ...overrides
  })

  useEffect(() => {
    search.saveSnapshot(snapshot())
  }, [scope, query, kind, presets, extText, extQuery, sortKey, hits, running, ran, failure, truncated, elapsedMs, scanned, scrollTop])

  useEffect(() => {
    const restoredScrollTop = restoredScrollTopRef.current
    if (restoredScrollTop <= 0 || view.length === 0 || !resultsContainer.current) return
    const frame = requestAnimationFrame(() => {
      if (!resultsContainer.current || restoredScrollTopRef.current !== restoredScrollTop) return
      resultsContainer.current.scrollTop = restoredScrollTop
      restoredScrollTopRef.current = 0
      setScrollTop(restoredScrollTop)
    })
    return () => cancelAnimationFrame(frame)
  }, [view.length])

  useEffect(() => {
    return () => {
      generationRef.current += 1
      cancelStream()
      flush()
      const current = latestRef.current
      search.saveSnapshot({
        scope: current.scope,
        query: current.query,
        kind: current.kind,
        presets: current.presets,
        extText: current.extText,
        extQuery: current.extQuery,
        sortKey: current.sortKey,
        hits: latestRef.current.hits,
        running: false,
        ran: current.ran,
        failure: current.running ? 'stopped' : current.failure,
        truncated: current.truncated,
        elapsedMs: current.elapsedMs,
        scanned: current.scanned,
        scrollTop: current.scrollTop
      })
      if (flushTimerRef.current !== null) clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
  }, [])

  const onSubmit = (event: FormEvent): void => {
    event.preventDefault()
    start()
  }

  const onQueryKeyDown = (event: KeyboardEvent<HTMLElement>): void => {
    if (event.key === 'Enter') {
      event.preventDefault()
      start()
    }
  }

  const openResult = (hit: SearchHit): void => {
    search.saveSnapshot(snapshot())
    onnavigated?.()
    void navigate(`/b${folderOf(hit.path)}?focus=${encodeURIComponent(hit.entry.name)}`)
  }

  return (
    <div className="sc-search">
      <form className="sc-search__query" onSubmit={onSubmit}>
        <span className="sc-search__query-icon" aria-hidden="true"><Icon name="search" /></span>
        <div className="sc-search__field" ref={fieldContainer}>
          <TextField
            value={query}
            type="search"
            label={t('search.placeholder')}
            autoFocus={autofocus}
            onValueChange={setQuery}
            onKeyDown={onQueryKeyDown}
          />
        </div>
        <Button type="submit" variant="filled">{t('search.run')}</Button>
        {ran ? <Button variant="text" onClick={clear}>{t('search.clear')}</Button> : null}
        {trailing}
      </form>

      <div className="sc-search__scope" aria-describedby="sc-search-scope-note">
        <span className="sc-search__scope-label">{t('search.scope_all_accessible')}</span>
        <span id="sc-search-scope-note">{scope ? t('search.scope_current_prioritized', { folder: scope }) : t('search.scope_explanation')}</span>
      </div>

      <div className="sc-search__filters">
        <span className="sc-search__label" id="sc-search-kind">{t('search.kind_label')}</span>
        <div className="sc-search__kind" role="group" aria-labelledby="sc-search-kind">
          <button type="button" className={kind === 'any' ? 'sc-search__filter-button sc-search__filter-button--selected' : 'sc-search__filter-button'} aria-pressed={kind === 'any'} onClick={() => chooseKind('any')}>{t('search.kind_any')}</button>
          <button type="button" className={kind === 'file' ? 'sc-search__filter-button sc-search__filter-button--selected' : 'sc-search__filter-button'} aria-pressed={kind === 'file'} onClick={() => chooseKind('file')}>{t('search.kind_file')}</button>
          <button type="button" className={kind === 'dir' ? 'sc-search__filter-button sc-search__filter-button--selected' : 'sc-search__filter-button'} aria-pressed={kind === 'dir'} onClick={() => chooseKind('dir')}>{t('search.kind_dir')}</button>
        </div>
        {kind !== 'dir' ? (
          <>
            <button type="button" className="sc-search__filter-menu-trigger" aria-expanded={filtersOpen} onClick={() => setFiltersOpen((open) => !open)}>
              <Icon name="filter_list" />{t('search.file_type')}
            </button>
            {presets.map((id) => {
              const preset = EXTENSION_PRESETS.find((candidate) => candidate.id === id)
              if (!preset) return null
              return <button key={id} type="button" className="sc-search__chip" aria-label={t('search.remove_filter', { label: t(preset.labelKey) })} onClick={() => setPresets((current) => current.filter((value) => value !== id))}>{t(preset.labelKey)} <span aria-hidden="true">×</span></button>
            })}
            <div className="sc-search__exts" onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) commitExtensions() }}>
              <TextField value={extText} label={t('search.extensions')} placeholder={t('search.extensions')} onValueChange={setExtText} onKeyDown={(event) => { if (event.key === 'Enter') commitExtensions() }} />
            </div>
            {filtersOpen ? (
              <div className="sc-search__menu" role="menu">
                {EXTENSION_PRESETS.map((preset) => {
                  const selected = presets.includes(preset.id)
                  return <button key={preset.id} type="button" role="menuitemcheckbox" aria-checked={selected} aria-label={selected ? t('search.filter_selected', { label: t(preset.labelKey) }) : undefined} onClick={() => setPresets((current) => selected ? current.filter((id) => id !== preset.id) : [...current, preset.id])}>{selected ? '✓ ' : ''}{t(preset.labelKey)}</button>
                })}
              </div>
            ) : null}
          </>
        ) : null}
      </div>

      <div className="sc-search__status">
        <span className="sc-search__count" aria-hidden={running}>{running ? <span className="sc-search__progress" role="img" aria-label={t('search.searching_label')}><mdui-circular-progress /></span> : null}{statusText}</span>
        <span className="sc-search__spoken" role="status" aria-live="polite">{running ? '' : statusText}</span>
        <span className="sc-search__status-actions">
          {!running && fileCount > 0 && dirCount > 0 ? <span className="sc-search__breakdown">{t('search.summary', { files: fileCount, folders: dirCount })}</span> : null}
          {running ? <Button variant="text" onClick={stop}>{t('search.stop')}</Button> : null}
          {ran && view.length > 0 ? (
            <span className="sc-search__sort-wrap">
              <button type="button" className="sc-search__sort-trigger" aria-expanded={sortOpen} aria-label={t('search.sort_by', { key: sortLabel })} onClick={() => setSortOpen((open) => !open)}><Icon name="sort" />{sortLabel}</button>
              {sortOpen ? <div className="sc-search__menu sc-search__menu--end" role="menu">{SORT_KEYS.map(([key, labelKey]) => <button key={key} type="button" role="menuitemradio" aria-checked={sortKey === key} onClick={() => { setSortKey(key); setSortOpen(false) }}>{sortKey === key ? '✓ ' : ''}{t(labelKey)}</button>)}</div> : null}
            </span>
          ) : null}
        </span>
      </div>

      <div className="sc-search__results" ref={resultsContainer} onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)} tabIndex={-1}>
        {!ran ? <p className="sc-search__note">{t('search.type_and_press_enter')}</p> : view.length === 0 && !running ? <p className="sc-search__note">{t('search.no_results')} {activeFilters ? <span className="sc-search__hint">{t('search.filtered_by', { filters: activeFilters })}</span> : null}</p> : (
          <div className="sc-search__spacer" style={{ height: windowed.totalHeight }}>
            <ul className="sc-search__rows" style={{ transform: `translate3d(0, ${windowed.padTop}px, 0)` }}>
              {rows.map((hit) => (
                <li key={hit.path}>
                  <button type="button" className="sc-search__row" onClick={() => openResult(hit)}>
                    <Icon name={hit.entry.kind === 'dir' ? 'folder' : 'draft'} />
                    <span className="sc-search__text"><span className="sc-search__name">{hit.entry.name}</span><span className="sc-search__folder">{folderOf(hit.path)}</span></span>
                    <span className="sc-search__cell">{hit.entry.kind !== 'dir' ? <span className="sc-search__size">{formatBytes(hit.entry.size)}</span> : null}<span className="sc-search__date">{formatDateNs(hit.entry.mtime_ns)}</span></span>
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  )
})
