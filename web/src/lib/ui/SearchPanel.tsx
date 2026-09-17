import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import type { FormEvent, KeyboardEvent, ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import type { SearchDone, SearchHit, SearchProgress } from '../api/client'
import { api } from '../api/client'
import { parentOf } from '../api/path-utils'
import { formatBytes } from '../format/bytes'
import { formatDateNs } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { EXTENSION_PRESETS, extensionOf, parseExtensions, resolveExtensions } from '../search/filters'
import { search, type SearchSnapshot, type SearchSortKey } from '../store/search.store'
import { computeWindow } from '../virtual/windowing'
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

type CategoryId = 'all' | 'file' | 'dir' | 'document' | 'image' | 'video' | 'audio' | 'archive' | 'code'

const CATEGORIES: readonly { id: CategoryId; labelKey: string; icon: string }[] = [
  { id: 'all', labelKey: /* i18n */ 'search.kind_any', icon: 'search' },
  { id: 'file', labelKey: /* i18n */ 'search.kind_file', icon: 'draft' },
  { id: 'dir', labelKey: /* i18n */ 'search.kind_dir', icon: 'folder' },
  { id: 'document', labelKey: /* i18n */ 'search.preset_document', icon: 'description' },
  { id: 'image', labelKey: /* i18n */ 'search.preset_image', icon: 'image' },
  { id: 'video', labelKey: /* i18n */ 'search.preset_video', icon: 'movie' },
  { id: 'audio', labelKey: /* i18n */ 'search.preset_audio', icon: 'audio-file' },
  { id: 'archive', labelKey: /* i18n */ 'search.preset_archive', icon: 'folder-zip' },
  { id: 'code', labelKey: /* i18n */ 'search.preset_code', icon: 'code' }
]

const FLUSH_MS = 100
const ROW_PX = 56
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

function getHitIcon(hit: SearchHit): { name: string; color?: string } {
  if (hit.entry.kind === 'dir') return { name: 'folder', color: '#5c93e8' }
  const ext = extensionOf(hit.entry.name).toLowerCase()
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext)) {
    return { name: 'folder-zip', color: '#e8a85c' }
  }
  if (ext === 'apk') {
    return { name: 'android', color: '#68d391' }
  }
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext)) {
    return { name: 'image', color: '#4ecdc4' }
  }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg', 'flv'].includes(ext)) {
    return { name: 'movie', color: '#f87171' }
  }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'oga', 'm4a', 'opus', 'wma'].includes(ext)) {
    return { name: 'audio-file', color: '#a78bfa' }
  }
  if (['js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'py', 'java', 'c', 'cpp', 'h', 'cs', 'rb', 'php', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'xml', 'html', 'css'].includes(ext)) {
    return { name: 'code', color: '#38bdf8' }
  }
  if (['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'odt', 'ods', 'odp', 'hwp', 'hwpx', 'csv'].includes(ext)) {
    return { name: 'description', color: '#60a5fa' }
  }
  return { name: 'draft', color: '#94a3b8' }
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
  const [sortOpen, setSortOpen] = useState(false)
  const [scrollTop, setScrollTop] = useState(restored?.scrollTop ?? 0)

  const inputRef = useRef<HTMLInputElement>(null)
  const resultsContainer = useRef<HTMLDivElement>(null)
  const categoriesRef = useRef<HTMLDivElement>(null)
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
      inputRef.current?.focus()
    }
  }), [])

  // A pointer device only emits vertical deltas, so the pill strip never moved
  // on a desktop. The row takes that delta as horizontal movement, and only
  // while it actually overflows, so a trackpad's own sideways gesture and the
  // page's vertical scroll are both left alone.
  useEffect(() => {
    const strip = categoriesRef.current
    if (!strip) return
    const onWheel = (event: WheelEvent): void => {
      if (event.deltaX !== 0) return
      if (strip.scrollWidth <= strip.clientWidth) return
      event.preventDefault()
      strip.scrollLeft += event.deltaY
    }
    strip.addEventListener('wheel', onWheel, { passive: false })
    return () => strip.removeEventListener('wheel', onWheel)
  }, [])

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
    queueMicrotask(() => inputRef.current?.focus())
  }

  useEffect(() => {
    const next = `${kind}|${presets.join(',')}|${extQuery.trim().toLowerCase()}`
    const previous = lastFiltersRef.current
    lastFiltersRef.current = next
    if (previous !== next && ran) start()
  }, [kind, presets, extQuery, ran])

  const selectCategory = (id: CategoryId): void => {
    if (id === 'all') {
      setKindState('any')
      setPresets([])
    } else if (id === 'dir') {
      setKindState('dir')
      setPresets([])
    } else if (id === 'file') {
      setKindState('file')
      setPresets([])
    } else {
      setKindState('file')
      setPresets([id])
    }
    setExtText('')
    setExtQuery('')
  }

  const activeCategory: CategoryId =
    kind === 'dir'
      ? 'dir'
      : kind === 'file' && presets.length === 0
        ? 'file'
        : presets.length === 1 && CATEGORIES.some((c) => c.id === presets[0])
          ? (presets[0] as CategoryId)
          : 'all'

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
    ? `${t('search.searching', { count: hits.length })}${scanned ? `, ${t('search.scanning', { dirs: String(scanned.dirs) })}` : ''}`
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

  const onQueryKeyDown = (event: KeyboardEvent<HTMLInputElement>): void => {
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
      <form className="sc-search__query-bar" onSubmit={onSubmit}>
        <span className="sc-search__query-icon" aria-hidden="true"><Icon name="search" size={20} /></span>
        <input
          ref={inputRef}
          className="sc-search__input"
          type="search"
          value={query}
          placeholder={t('search.placeholder')}
          autoFocus={autofocus}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={onQueryKeyDown}
        />
        {query.trim() ? (
          <button
            type="button"
            className="sc-search__clear-btn sc-icon-button"
            aria-label={t('search.clear')}
            onClick={clear}
          >
            <Icon name="close" size={16} />
          </button>
        ) : null}
        <button
          type="submit"
          className="sc-search__submit-btn"
          aria-label={t('search.run')}
        >
          {t('search.run')}
        </button>
        {trailing}
      </form>

      <div className="sc-search__filter-bar">
        <div
          className="sc-search__categories"
          role="tablist"
          aria-label={t('search.kind_label')}
          ref={categoriesRef}
        >
          {CATEGORIES.map((cat) => {
            const isSelected = activeCategory === cat.id
            return (
              <button
                key={cat.id}
                type="button"
                className={`sc-search__category-pill${isSelected ? ' sc-search__category-pill--active' : ''}`}
                aria-pressed={isSelected}
                onClick={() => selectCategory(cat.id)}
              >
                <Icon name={cat.icon} size={15} />
                <span>{t(cat.labelKey)}</span>
              </button>
            )
          })}
        </div>

        <div
          className="sc-search__scope-pill"
          title={scope ? t('search.scope_current_prioritized', { folder: scope }) : t('search.scope_explanation')}
        >
          <Icon name={scope ? 'folder' : 'search'} size={14} />
          <span>{scope ? (scope.split('/').filter(Boolean).at(-1) ?? scope) : t('search.scope_all_accessible')}</span>
        </div>

        <div className="sc-search__sort-wrap">
          <button
            type="button"
            className="sc-search__sort-btn"
            aria-expanded={sortOpen}
            aria-label={t('search.sort_by', { key: sortLabel })}
            onClick={() => setSortOpen((open) => !open)}
          >
            <Icon name="sort" size={15} />
            <span>{sortLabel}</span>
          </button>
          {sortOpen ? (
            <div className="sc-search__menu sc-search__menu--end" role="menu">
              {SORT_KEYS.map(([key, labelKey]) => (
                <button
                  key={key}
                  type="button"
                  role="menuitemradio"
                  aria-checked={sortKey === key}
                  onClick={() => {
                    setSortKey(key)
                    setSortOpen(false)
                  }}
                >
                  {sortKey === key ? '✓ ' : ''}{t(labelKey)}
                </button>
              ))}
            </div>
          ) : null}
        </div>
      </div>

      <div className="sc-search__status-bar">
        <span className="sc-search__status-info" aria-hidden={running}>
          {running ? (
            <span className="sc-search__progress" role="img" aria-label={t('search.searching_label')}>
              <mdui-circular-progress />
            </span>
          ) : null}
          <span>{statusText}</span>
        </span>
        <span className="sc-search__spoken" role="status" aria-live="polite">
          {running ? '' : statusText}
        </span>
        <span className="sc-search__status-actions">
          {!running && fileCount > 0 && dirCount > 0 ? (
            <span className="sc-search__breakdown">{t('search.summary', { files: fileCount, folders: dirCount })}</span>
          ) : null}
          {running ? (
            <button type="button" className="sc-search__stop-btn" onClick={stop}>
              {t('search.stop')}
            </button>
          ) : null}
        </span>
      </div>

      <div className="sc-search__results" ref={resultsContainer} onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)} tabIndex={-1}>
        {!ran ? (
          <p className="sc-search__note">{t('search.type_and_press_enter')}</p>
        ) : view.length === 0 && !running ? (
          <p className="sc-search__note">
            {t('search.no_results')} {activeFilters ? <span className="sc-search__hint">{t('search.filtered_by', { filters: activeFilters })}</span> : null}
          </p>
        ) : (
          <div className="sc-search__spacer" style={{ height: windowed.totalHeight }}>
            <ul className="sc-search__rows" style={{ transform: `translate3d(0, ${windowed.padTop}px, 0)` }}>
              {rows.map((hit) => {
                const hitIcon = getHitIcon(hit)
                return (
                  <li key={hit.path}>
                    <button type="button" className="sc-search__row" onClick={() => openResult(hit)}>
                      <span className="sc-search__row-icon" style={{ color: hitIcon.color }}>
                        <Icon name={hitIcon.name} size={20} />
                      </span>
                      <span className="sc-search__text">
                        <span className="sc-search__name">{hit.entry.name}</span>
                        <span className="sc-search__folder">{folderOf(hit.path)}</span>
                      </span>
                      <span className="sc-search__cell">
                        {hit.entry.kind !== 'dir' ? (
                          <span className="sc-search__size">{formatBytes(hit.entry.size)}</span>
                        ) : null}
                        <span className="sc-search__date">{formatDateNs(hit.entry.mtime_ns)}</span>
                      </span>
                    </button>
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </div>
    </div>
  )
})
