import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, MouseEvent as ReactMouseEvent } from 'react'
import type { Entry, Perms } from '../api/types'
import { useStore } from '../store/use-store'
import { selection } from '../store/selection.store'
import { ui } from '../store/ui.store'
import { view } from '../store/view.store'
import { useI18n } from '../i18n/use-i18n'
import { computeScaleMapping, computeWindow, documentScrollTop, effectiveViewportHeight, rowIndexToScrollTop } from '../virtual/windowing'
import { indicesInRect, type Rect } from './marquee'
import { FileRow } from './FileRow'
import { FileRowSkeleton } from './FileRowSkeleton'
import './browse-ui.css'

export interface FileViewHandle { focusEntry: (name: string) => boolean; entriesInRect: (rect: Rect) => Entry[] }
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
  const names = useStore(selection, (state) => state.names)
  const focused = useStore(selection, (state) => state.focused)
  const [measure, setMeasure] = useState({ top: 0, scroll: 0, height: 0 })
  const viewport = useRef<HTMLDivElement>(null)
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const focusSnapshot = useRef<{ names: string[]; focusedName: string | null }>({ names: [], focusedName: null })
  const rowHeight = density === 'compact' ? 40 : density === 'spacious' ? 56 : 48
  const win = computeWindow({ scrollTop: documentScrollTop(measure.scroll, measure.top), viewportHeight: measure.height, rowHeight, itemCount: total, overscan: 8 })
  const loadedNames = useMemo(() => entries.map((entry) => entry.name), [entries])
  const focusedName = focused === null ? null : entries[focused]?.name ?? null
  const domId = (name: string) => `sc-row-${encodeURIComponent(name).replace(/%/g, '_')}`

  const update = () => {
    if (!viewport.current) return
    setMeasure({ top: viewport.current.getBoundingClientRect().top + window.scrollY, scroll: window.scrollY, height: effectiveViewportHeight(window.visualViewport?.height, window.innerHeight) })
  }
  useEffect(() => {
    update()
    window.addEventListener('scroll', update, { passive: true })
    window.addEventListener('resize', update)
    window.visualViewport?.addEventListener('resize', update)
    const element = viewport.current
    if (element && typeof ResizeObserver !== 'undefined') {
      const observer = new ResizeObserver(update)
      resizeObserverRef.current = observer
      observer.observe(element)
    }
    return () => {
      window.removeEventListener('scroll', update)
      window.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('resize', update)
      resizeObserverRef.current?.disconnect()
      resizeObserverRef.current = null
    }
  }, [entries, density, total])
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
    const current = documentScrollTop(measure.scroll, measure.top)
    if (top < current) window.scrollTo({ top: measure.top + top })
    else if (bottom > current + measure.height) window.scrollTo({ top: measure.top + bottom - measure.height })
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
      return indicesInRect(rect, { top: box.top + window.scrollY, left: box.left + window.scrollX, rowHeight, cellHeight: rowHeight, columnPitch: 0, cellWidth: box.width, columns: 1, startIndex: 0, count: total }).map((index) => entries[index]).filter((entry): entry is Entry => Boolean(entry))
    }
  }), [entries, total, rowHeight, measure])

  const moveFocus = (delta: number, extend: boolean) => {
    const next = focused === null ? (delta < 0 ? total - 1 : 0) : Math.min(Math.max(focused + delta, 0), total - 1)
    selection.focus(next)
    const name = entries[next]?.name
    if (extend && name) selection.range(loadedNames, name)
    scrollIntoView(next)
  }
  const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
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

  return <div ref={viewport} className={`sc-file-table${compact ? ' sc-file-table--reserve-bar' : ''}${names.size ? ' sc-file-table--reserve-selection' : ''}`} data-density={density} role="grid" aria-multiselectable="true" aria-rowcount={total} aria-label={t('table.file_list')} aria-activedescendant={active} aria-busy={loadingMore} tabIndex={0} onKeyDown={keyDown}>
    {total === 0 && !loading ? <p className="sc-file-table__empty">{t('common.folder_empty')}</p> : <div className="sc-file-table__spacer" style={{ height: win.totalHeight }}><div className="sc-file-table__window" style={{ transform: `translate3d(0,${win.padTop}px,0)` }}>{rows.map(({ index, entry }) => entry ? <FileRow key={entry.name} entry={entry} rowIndex={index + 1} selected={names.has(entry.name)} focused={focusedName === entry.name} domId={domId(entry.name)} encrypted={encrypted} compact={compact} onClick={(event) => { viewport.current?.focus(); if (event.shiftKey) selection.range(loadedNames, entry.name); else if (event.ctrlKey || event.metaKey) selection.toggle(entry.name, index); else selection.only(entry.name, index) }} onDoubleClick={() => onOpen(entry)} onCompactOpen={() => { viewport.current?.focus(); onOpen(entry) }} onContextMenu={(event) => { event.preventDefault(); event.stopPropagation(); onContextMenu(entry, event) }} onToggleCheck={() => selection.toggle(entry.name, index)} /> : <FileRowSkeleton key={`skeleton-${index}`} rowIndex={index + 1} />)}</div></div>}
  </div>
})
