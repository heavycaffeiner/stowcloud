import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, MouseEvent as ReactMouseEvent } from 'react'
import type { Entry, Perms, SortKey } from '../api/types'
import { useStore } from '../store/use-store'
import { selection } from '../store/selection.store'
import { ui } from '../store/ui.store'
import { view } from '../store/view.store'
import { useI18n } from '../i18n/use-i18n'
import { computeScaleMapping, computeWindow, documentScrollTop, effectiveViewportHeight, rowIndexToScrollTop } from '../virtual/windowing'
import { indicesInRect, type Rect } from './marquee'
import { FileRow, useFileActivation } from './FileRow'
import { FileRowSkeleton } from './FileRowSkeleton'
import './browse-ui.css'

export interface FileViewHandle {
  focusEntry: (name: string) => boolean
  entriesInRect: (rect: Rect) => Entry[]
  scrollBounds: () => { top: number; height: number }
  scrollBy: (delta: number) => void
}
export interface FileTableProps {
  entries: readonly Entry[]
  total: number
  dirs: number
  loading: boolean
  loadingMore: boolean
  requestMore: () => void
  perms: Perms
  onOpen: (entry: Entry) => void
  onContextMenu: (entry: Entry, event: ReactMouseEvent) => void
  onRename?: () => void
  onDelete?: () => void
  onSearchFocus?: () => void
  encrypted?: boolean
}

export const FileTable = forwardRef<FileViewHandle, FileTableProps>(function FileTable({ entries, total, loading, loadingMore, requestMore, onOpen, onContextMenu, onRename, onDelete, onSearchFocus, encrypted = false }, ref) {
  const { t } = useI18n()
  const compact = useStore(ui, (state) => state.compact)
  const density = useStore(view, (state) => state.density)
  const sortKey = useStore(view, (state) => state.sortKey)
  const sortOrder = useStore(view, (state) => state.sortOrder)
  const names = useStore(selection, (state) => state.names)
  const focused = useStore(selection, (state) => state.focused)
  const [measure, setMeasure] = useState({ top: 0, scroll: 0, height: 0 })
  const viewport = useRef<HTMLDivElement>(null)
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const focusSnapshot = useRef<{ names: string[]; focusedName: string | null }>({ names: [], focusedName: null })
  const HEADER_HEIGHT = 40
  const rowHeight = density === 'compact' ? 40 : density === 'spacious' ? 56 : 48
  const localScrollTop = compact ? measure.scroll : Math.max(0, documentScrollTop(measure.scroll, measure.top) - HEADER_HEIGHT)
  const win = computeWindow({ scrollTop: localScrollTop, viewportHeight: Math.max(0, measure.height - HEADER_HEIGHT), rowHeight, itemCount: total, overscan: 8 })
  const loadedNames = useMemo(() => entries.map((entry) => entry.name), [entries])
  const focusedName = focused === null ? null : entries[focused]?.name ?? null
  const domId = (name: string) => `sc-row-${encodeURIComponent(name).replace(/%/g, '_')}`
  const sortLabel = (key: SortKey) => key === 'name' ? t('browse.sort_by_name') : key === 'size' ? t('browse.sort_by_size') : key === 'mtime' ? t('browse.sort_by_modified') : t('browse.sort_by_kind')
  const chooseSort = (key: SortKey) => view.setSort(key, sortKey === key && sortOrder === 'asc' ? 'desc' : 'asc')

  const update = () => {
    if (!viewport.current) return
    setMeasure(compact
      ? { top: 0, scroll: viewport.current.scrollTop, height: viewport.current.clientHeight }
      : { top: viewport.current.getBoundingClientRect().top + window.scrollY, scroll: window.scrollY, height: effectiveViewportHeight(window.visualViewport?.height, window.innerHeight) })
  }
  useEffect(() => {
    update()
    const scrollTarget = compact ? viewport.current : window
    scrollTarget?.addEventListener('scroll', update, { passive: true })
    window.addEventListener('resize', update)
    window.visualViewport?.addEventListener('resize', update)
    const element = viewport.current
    if (element && typeof ResizeObserver !== 'undefined') {
      const observer = new ResizeObserver(update)
      resizeObserverRef.current = observer
      observer.observe(element)
    }
    return () => {
      scrollTarget?.removeEventListener('scroll', update)
      window.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('resize', update)
      resizeObserverRef.current?.disconnect()
      resizeObserverRef.current = null
    }
  }, [entries, density, total, compact])
  useEffect(() => {
    const namesNow = entries.map((entry) => entry.name)
    const previous = focusSnapshot.current
    const overlap = Math.min(previous.names.length, namesNow.length)
    const reordered = overlap > 0 && previous.names.slice(0, overlap).some((name, index) => namesNow[index] !== name)
    if (reordered && previous.focusedName) {
      const next = namesNow.indexOf(previous.focusedName)
      if (next >= 0 && next !== focused) selection.focus(next)
    }
    focusSnapshot.current = { names: namesNow, focusedName: reordered && previous.focusedName && namesNow.includes(previous.focusedName) ? previous.focusedName : focusedName }
  }, [entries, focused, focusedName])
  useEffect(() => {
    if (!loadingMore && win.end > entries.length) requestMore()
  }, [loadingMore, win.end, entries.length, requestMore])

  const scrollIntoView = (index: number) => {
    const mapping = computeScaleMapping(total, rowHeight)
    const top = rowIndexToScrollTop(index, mapping, rowHeight)
    const bottom = rowIndexToScrollTop(index + 1, mapping, rowHeight)
    const current = localScrollTop
    const viewportHeight = Math.max(0, measure.height - HEADER_HEIGHT)
    const scrollTarget = compact ? viewport.current : window
    const offset = compact ? 0 : measure.top + HEADER_HEIGHT
    if (top < current) scrollTarget?.scrollTo({ top: offset + top })
    else if (bottom > current + viewportHeight) scrollTarget?.scrollTo({ top: offset + bottom - viewportHeight })
  }
  const openMenuForFocused = () => {
    if (focused === null) return
    const entry = entries[focused]
    const element = entry ? document.getElementById(domId(entry.name)) : null
    if (!entry || !element) return
    const box = element.getBoundingClientRect()
    onContextMenu(entry, new window.MouseEvent('contextmenu', { clientX: Math.round(box.left + box.width / 2), clientY: Math.round(box.top + box.height / 2) }) as unknown as ReactMouseEvent)
  }
  useImperativeHandle(ref, () => ({
    focusEntry(name) {
      const index = entries.findIndex((entry) => entry.name === name)
      if (index < 0) return false
      selection.only(name, index)
      viewport.current?.focus()
      scrollIntoView(index)
      return true
    },
    entriesInRect(rect) {
      const box = viewport.current?.getBoundingClientRect()
      if (!box) return []
      return indicesInRect(rect, { top: box.top + window.scrollY + HEADER_HEIGHT - (compact ? viewport.current?.scrollTop ?? 0 : 0), left: box.left + window.scrollX, rowHeight, cellHeight: rowHeight, columnPitch: 0, cellWidth: box.width, columns: 1, startIndex: 0, count: total }).map((index) => entries[index]).filter((entry): entry is Entry => Boolean(entry))
    },
    scrollBounds() {
      if (!compact || !viewport.current) return { top: 0, height: window.visualViewport?.height ?? window.innerHeight }
      const box = viewport.current.getBoundingClientRect()
      return { top: box.top, height: box.height }
    },
    scrollBy(delta) {
      if (compact && viewport.current) viewport.current.scrollTop += delta
      else window.scrollBy(0, delta)
    }
  }), [entries, total, rowHeight, measure, compact])

  const moveFocus = (delta: number, extend: boolean) => {
    const next = focused === null ? (delta < 0 ? total - 1 : 0) : Math.min(Math.max(focused + delta, 0), total - 1)
    selection.focus(next)
    const name = entries[next]?.name
    if (extend && name) selection.range(loadedNames, name)
    scrollIntoView(next)
  }
  const activation = useFileActivation((entry, index, event) => {
    viewport.current?.focus()
    if (event.shiftKey) selection.range(loadedNames, entry.name)
    else if (event.ctrlKey || event.metaKey) selection.toggle(entry.name, index)
    else selection.only(entry.name, index)
  }, onOpen)
  const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    activation.cancel()
    if (!total) return
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); moveFocus(event.key === 'ArrowDown' ? (focused === null ? 0 : 1) : -1, event.shiftKey); return }
    if (event.key === ' ') { event.preventDefault(); if (focused !== null && entries[focused]) selection.toggle(entries[focused].name, focused); return }
    if ((event.key === 'a' || event.key === 'A') && (event.ctrlKey || event.metaKey)) { event.preventDefault(); selection.all(loadedNames); return }
    if (event.key === 'Enter' && focused !== null && entries[focused]) { onOpen(entries[focused]); return }
    if (event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey)) { event.preventDefault(); openMenuForFocused(); return }
    if (event.key === 'F2') { event.preventDefault(); onRename?.(); return }
    if (event.key === 'Delete') { event.preventDefault(); onDelete?.(); return }
    if (event.key === '/') { event.preventDefault(); onSearchFocus?.(); return }
    if (event.key === 'Escape' && names.size) { event.preventDefault(); selection.clear() }
  }
  const rows = Array.from({ length: Math.max(0, win.end - win.start) }, (_, offset) => { const index = win.start + offset; return { index, entry: entries[index] } })
  const active = focusedName && rows.some((row) => row.entry?.name === focusedName) ? domId(focusedName) : undefined

  return <div ref={viewport} className={`sc-file-table${compact ? ' sc-file-table--contained' : ''}${names.size ? ' sc-file-table--reserve-selection' : ''}`} style={{ touchAction: 'manipulation' }} data-density={density} role="grid" aria-multiselectable="true" aria-rowcount={total + 1} aria-label={t('table.file_list')} aria-activedescendant={active} aria-busy={loadingMore} tabIndex={0} onKeyDown={keyDown} onPointerDown={(event) => { if (!(event.target as HTMLElement).closest('[aria-selected]')) activation.cancel() }} onContextMenu={activation.cancel}>
    {total === 0 && !loading ? <p className="sc-file-table__empty">{t('common.folder_empty')}</p> : <>
      <div className="sc-file-table__header" role="row" aria-rowindex={1}>
        <span className="sc-file-table__header-cell sc-file-table__header-cell--select" role="columnheader" aria-label={t('common.select', { name: t('table.file_list') })} />
        {(['name', 'size', 'mtime'] as const).map((key) => <span key={key} className={`sc-file-table__header-cell sc-file-table__header-cell--${key}`} role="columnheader" aria-sort={sortKey === key ? sortOrder === 'asc' ? 'ascending' : 'descending' : 'none'}>
          <button type="button" className={`sc-file-table__header-button${sortKey === key ? ' sc-file-table__header-button--active' : ''}`} onClick={() => chooseSort(key)} aria-label={`${sortLabel(key)}${sortKey === key ? `, ${sortOrder === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending')}` : ''}`}>
            <span>{sortLabel(key)}</span>
            {sortKey === key ? <span className="sc-file-table__header-sort" aria-hidden="true">{sortOrder === 'asc' ? '↑' : '↓'}</span> : null}
          </button>
        </span>)}
      </div>
      <div className="sc-file-table__spacer" style={{ height: win.totalHeight }}><div className="sc-file-table__window" style={{ transform: `translate3d(0,${win.padTop}px,0)` }}>{rows.map(({ index, entry }) => entry ? <FileRow key={entry.path} entry={entry} rowIndex={index + 2} selected={names.has(entry.name)} focused={focusedName === entry.name} domId={domId(entry.name)} encrypted={encrypted} {...activation.handlers(entry, index)} onCancelActivation={activation.cancel} onContextMenu={(event) => { activation.cancel(); event.preventDefault(); event.stopPropagation(); onContextMenu(entry, event) }} onToggleCheck={() => selection.toggle(entry.name, index)} /> : <FileRowSkeleton key={`skeleton-${index}`} rowIndex={index + 2} />)}</div></div>
    </>}
  </div>
})
