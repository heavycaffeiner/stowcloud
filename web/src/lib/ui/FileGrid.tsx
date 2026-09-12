import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent } from 'react'
import type { Entry, Perms } from '../api/types'
import { formatEntrySize } from '../format/entry-size'
import { useI18n } from '../i18n/use-i18n'
import { useStore } from '../store/use-store'
import { selection } from '../store/selection.store'
import { ui } from '../store/ui.store'
import { view } from '../store/view.store'
import { computeScaleMapping, computeWindow, rowIndexToScrollTop } from '../virtual/windowing'
import { cellPos, sectionRows, verticalTarget } from '../virtual/grid-sections'
import { indicesInRect, type Rect } from './marquee'
import { isVideoFile } from './media-utils'
import { Thumbnail } from './Thumbnail'
import { useFileActivation } from './FileRow'
import { Icon } from './Icon'
import './browse-ui.css'

export interface FileGridProps {
  entries: readonly Entry[]
  total: number
  dirs: number
  loading: boolean
  loadingMore: boolean
  requestMore: () => void
  perms: Perms
  onOpen: (entry: Entry) => void
  onContextMenu: (entry: Entry, event: ReactMouseEvent) => void
  menuFor?: string | null
  onRename?: () => void
  onDelete?: () => void
  onSearchFocus?: () => void
  encrypted?: boolean
}
export interface FileGridHandle { focusEntry: (name: string) => boolean; entriesInRect: (rect: Rect) => Entry[] }

export const FileGrid = forwardRef<FileGridHandle, FileGridProps>(function FileGrid({ entries, total, dirs, loading, loadingMore, requestMore, onOpen, onContextMenu, menuFor, onRename, onDelete, onSearchFocus, encrypted = false }, ref) {
  const { t } = useI18n()
  const compact = useStore(ui, (state) => state.compact)
  const density = useStore(view, (state) => state.density)
  const names = useStore(selection, (state) => state.names)
  const focused = useStore(selection, (state) => state.focused)
  const viewport = useRef<HTMLDivElement>(null)
  const folderEl = useRef<HTMLDivElement>(null)
  const fileEl = useRef<HTMLDivElement>(null)
  const [metrics, setMetrics] = useState({ width: 0, scroll: 0, height: 0, foldersTop: 0, filesTop: 0 })
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const card = density === 'compact' ? { w: 192, folderH: 44, fileH: 176, gap: 8 } : density === 'spacious' ? { w: 256, folderH: 60, fileH: 244, gap: 16 } : { w: 224, folderH: 52, fileH: 208, gap: 12 }
  const availableW = Math.max(card.w, metrics.width - 32)
  const columns = Math.max(1, Math.floor((availableW + card.gap) / (card.w + card.gap)))
  const cardW = Math.max(120, Math.floor((availableW - (columns - 1) * card.gap) / columns))
  const folderCount = Math.min(dirs, total)
  const fileCount = Math.max(0, total - folderCount)
  const folderRows = sectionRows(folderCount, columns)
  const fileRows = sectionRows(fileCount, columns)
  const folderRowH = card.folderH + card.gap
  const fileRowH = card.fileH + card.gap
  const folderWin = computeWindow({ scrollTop: Math.max(0, metrics.scroll - metrics.foldersTop), viewportHeight: metrics.height, rowHeight: folderRowH, itemCount: folderRows, overscan: 3 })
  const fileWin = computeWindow({ scrollTop: Math.max(0, metrics.scroll - metrics.filesTop), viewportHeight: metrics.height, rowHeight: fileRowH, itemCount: fileRows, overscan: 3 })

  const update = () => {
    if (!viewport.current) return
    const scroll = window.scrollY
    setMetrics({
      width: viewport.current.clientWidth,
      scroll,
      height: window.visualViewport?.height ?? window.innerHeight,
      foldersTop: folderEl.current ? folderEl.current.getBoundingClientRect().top + scroll : 0,
      filesTop: fileEl.current ? fileEl.current.getBoundingClientRect().top + scroll : 0
    })
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
    if (!loadingMore && Math.max(folderWin.end * columns, folderCount + fileWin.end * columns) > entries.length) requestMore()
  }, [folderWin.end, fileWin.end, columns, folderCount, entries.length, loadingMore, requestMore])

  const loadedNames = useMemo(() => entries.map((entry) => entry.name), [entries])
  const focusSnapshot = useRef<{ names: string[]; focusedName: string | null }>({ names: [], focusedName: null })
  const focusedName = focused === null ? null : entries[focused]?.name ?? null
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

  const domId = (name: string) => `sc-grid-cell-${encodeURIComponent(name).replace(/%/g, '_')}`
  const scrollIntoView = (index: number) => {
    const p = cellPos(index, folderCount, columns)
    const rowH = p.section === 0 ? folderRowH : fileRowH
    const rowCount = p.section === 0 ? folderRows : fileRows
    const top = (p.section === 0 ? metrics.foldersTop : metrics.filesTop) + rowIndexToScrollTop(p.row, computeScaleMapping(rowCount, rowH), rowH)
    const bottom = top + rowH
    if (top < window.scrollY) window.scrollTo({ top })
    else if (bottom > window.scrollY + metrics.height) window.scrollTo({ top: bottom - metrics.height })
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
      const left = (viewport.current?.getBoundingClientRect().left ?? 0) + window.scrollX + 16
      const common = { left, columnPitch: cardW + card.gap, cellWidth: cardW, columns }
      const hits = [
        ...indicesInRect(rect, { ...common, top: (folderEl.current?.getBoundingClientRect().top ?? 0) + window.scrollY, rowHeight: folderRowH, cellHeight: card.folderH, startIndex: 0, count: folderCount }),
        ...indicesInRect(rect, { ...common, top: (fileEl.current?.getBoundingClientRect().top ?? 0) + window.scrollY, rowHeight: fileRowH, cellHeight: card.fileH, startIndex: folderCount, count: fileCount })
      ]
      return hits.map((index) => entries[index]).filter((entry): entry is Entry => Boolean(entry))
    }
  }), [entries, folderCount, fileCount, columns, cardW, card, folderRowH, fileRowH, metrics])

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
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (focused === null) moveFocus(0, event.shiftKey)
      else {
        const next = verticalTarget(focused, event.key === 'ArrowDown' ? 1 : -1, folderCount, total, columns)
        moveFocus(next - focused, event.shiftKey)
      }
      return
    }
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault()
      moveFocus(focused === null ? 0 : event.key === 'ArrowRight' ? 1 : -1, event.shiftKey)
      return
    }
    if (event.key === ' ') { event.preventDefault(); if (focused !== null && entries[focused]) selection.toggle(entries[focused].name, focused); return }
    if ((event.key === 'a' || event.key === 'A') && (event.ctrlKey || event.metaKey)) { event.preventDefault(); selection.all(loadedNames); return }
    if (event.key === 'Enter' && focused !== null && entries[focused]) { onOpen(entries[focused]); return }
    if (event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey)) { event.preventDefault(); openMenuForFocused(); return }
    if (event.key === 'F2') { event.preventDefault(); onRename?.(); return }
    if (event.key === 'Delete') { event.preventDefault(); onDelete?.(); return }
    if (event.key === '/') { event.preventDefault(); onSearchFocus?.(); return }
    if (event.key === 'Escape' && names.size) { event.preventDefault(); selection.clear() }
  }

  const cardElement = (entry: Entry | undefined, index: number, isFolder: boolean, col: number) => {
    if (!entry) return <div key={`skeleton-${index}`} className={`sc-file-grid__card sc-file-grid__card--${isFolder ? 'folder' : 'file'} sc-file-grid__skeleton`} style={{ width: cardW }} aria-busy="true"><span className="sc-file-grid__skeleton-line" />{!isFolder ? <span className="sc-file-grid__skeleton-block" /> : null}</div>
    const selected = names.has(entry.name)
    const focusedCard = focusedName === entry.name
    const icon = entry.kind === 'dir' ? 'folder' : isVideoFile(entry.name) ? 'video' : entry.preview?.available ? 'image' : 'file'
    const cancelControlPointer = (event: PointerEvent) => {
      event.stopPropagation()
      activation.cancel()
    }
    return (
      <div id={domId(entry.name)} key={entry.path} className={`sc-file-grid__card sc-file-grid__card--${isFolder ? 'folder' : 'file'}${selected ? ' sc-file-grid__card--selected' : ''}${focusedCard ? ' sc-file-grid__card--focused' : ''}`} role="gridcell" aria-colindex={col + 1} aria-selected={selected} style={{ width: cardW }} {...activation.handlers(entry, index)} onContextMenu={(event) => { activation.cancel(); event.preventDefault(); event.stopPropagation(); onContextMenu(entry, event) }}>
        {isFolder ? (
          <>
            <span className="sc-file-grid__check sc-touch-target" onClick={(event) => { event.stopPropagation(); activation.cancel(); selection.toggle(entry.name, index) }} onPointerDown={cancelControlPointer} onPointerUp={(event) => event.stopPropagation()} onDoubleClick={(event) => event.stopPropagation()}><mdui-checkbox checked={selected} tabIndex={-1}><span className="sc-sr-only">{t('common.select', { name: entry.name })}</span></mdui-checkbox></span>
            <span className="sc-file-grid__type"><Icon name="folder" /></span>
            <span className="sc-file-grid__name"><bdi>{entry.name}</bdi></span>
            {entry.confusable ? <span className="sc-file-grid__badge" title={t('common.look_alike_characters')}><Icon name="warning" /></span> : null}
            <button type="button" className="sc-file-grid__kebab" tabIndex={-1} aria-expanded={menuFor === entry.name} aria-label={t('grid.more_actions', { name: entry.name })} onClick={(event) => { event.stopPropagation(); activation.cancel(); onContextMenu(entry, event) }} onPointerDown={cancelControlPointer} onPointerUp={(event) => event.stopPropagation()} onDoubleClick={(event) => event.stopPropagation()}><Icon name="more-vert" size={18} /></button>
          </>
        ) : (
          <>
            <div className="sc-file-grid__head">
              <span className="sc-file-grid__check sc-touch-target" onClick={(event) => { event.stopPropagation(); activation.cancel(); selection.toggle(entry.name, index) }} onPointerDown={cancelControlPointer} onPointerUp={(event) => event.stopPropagation()} onDoubleClick={(event) => event.stopPropagation()}><mdui-checkbox checked={selected} tabIndex={-1}><span className="sc-sr-only">{t('common.select', { name: entry.name })}</span></mdui-checkbox></span>
              <span className="sc-file-grid__name"><bdi>{entry.name}</bdi></span>
              {entry.confusable ? <span className="sc-file-grid__badge" title={t('common.look_alike_characters')}><Icon name="warning" /></span> : null}
              <button type="button" className="sc-file-grid__kebab" tabIndex={-1} aria-expanded={menuFor === entry.name} aria-label={t('grid.more_actions', { name: entry.name })} onClick={(event) => { event.stopPropagation(); activation.cancel(); onContextMenu(entry, event) }} onPointerDown={cancelControlPointer} onPointerUp={(event) => event.stopPropagation()} onDoubleClick={(event) => event.stopPropagation()}><Icon name="more-vert" size={18} /></button>
            </div>
            <div className="sc-file-grid__thumb"><Thumbnail entry={entry} dim={512} fallback={icon} iconSize={40} /></div>
            <span className="sc-file-grid__meta">{formatEntrySize(entry.size, encrypted)}</span>
          </>
        )}
      </div>
    )
  }

  const renderSection = (isFolder: boolean) => {
    const start = isFolder ? folderWin.start : fileWin.start
    const end = isFolder ? folderWin.end : fileWin.end
    const count = isFolder ? folderCount : fileCount
    const offset = isFolder ? 0 : folderCount
    return Array.from({ length: Math.max(0, end - start) }, (_, r) => {
      const row = start + r
      return <div key={`${isFolder ? 'd' : 'f'}-${row}`} className="sc-file-grid__row" role="row" aria-rowindex={(isFolder ? row : folderRows + row) + 1} style={{ height: isFolder ? card.folderH : card.fileH, gap: card.gap, paddingBottom: card.gap }}>{Array.from({ length: columns }, (_, col) => { const index = offset + row * columns + col; return index < offset + count ? cardElement(entries[index], index, isFolder, col) : null })}</div>
    })
  }

  const active = focusedName && document.getElementById(domId(focusedName)) ? domId(focusedName) : undefined
  return (
    <div ref={viewport} data-density={density} className={`sc-file-grid${compact ? ' sc-file-grid--reserve-bar' : ''}${names.size ? ' sc-file-grid--reserve-selection' : ''}`} style={{ touchAction: 'manipulation' }} role="grid" aria-multiselectable="true" aria-rowcount={folderRows + fileRows} aria-colcount={columns} aria-label={t('grid.file_grid')} aria-activedescendant={active} aria-busy={loadingMore} tabIndex={0} onKeyDown={keyDown} onPointerDown={(event) => { if (!(event.target as HTMLElement).closest('[aria-selected]')) activation.cancel() }} onContextMenu={activation.cancel}>
      {total === 0 && !loading ? <p className="sc-file-grid__empty">{t('common.folder_empty')}</p> : <>
        {folderCount > 0 ? <><p className="sc-file-grid__group" aria-hidden="true">{t('grid.folders')}</p><div ref={folderEl} className="sc-file-grid__section" role="rowgroup" aria-label={t('grid.folders')} style={{ height: folderWin.totalHeight }}><div className="sc-file-grid__window" style={{ transform: `translate3d(0,${folderWin.padTop}px,0)` }}>{renderSection(true)}</div></div></> : null}
        {fileCount > 0 ? <><p className="sc-file-grid__group" aria-hidden="true">{t('grid.files')}</p><div ref={fileEl} className="sc-file-grid__section" role="rowgroup" aria-label={t('grid.files')} style={{ height: fileWin.totalHeight }}><div className="sc-file-grid__window" style={{ transform: `translate3d(0,${fileWin.padTop}px,0)` }}>{renderSection(false)}</div></div></> : null}
      </>}
    </div>
  )
})
