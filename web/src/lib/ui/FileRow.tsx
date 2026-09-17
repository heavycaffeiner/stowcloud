import { useRef } from 'react'
import type { HTMLAttributes, MouseEvent } from 'react'
import type { Entry } from '../api/types'
import { formatEntrySize } from '../format/entry-size'
import { formatDateNs } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { isVideoFile } from './media-utils'
import { Icon } from './Icon'
import { MiddleEllipsis } from './MiddleEllipsis'
import './browse-ui.css'

const DOUBLE_TAP_MS = 450
const DOUBLE_TAP_DISTANCE_PX = 30
const TAP_MOVE_PX = 12

type ActivationHandlers = Pick<HTMLAttributes<HTMLDivElement>, 'onClick' | 'onPointerDown' | 'onPointerMove' | 'onPointerUp' | 'onPointerCancel'>
type Tap = { path: string; pointerType: string; time: number; x: number; y: number }
type Gesture = Tap & { pointerId: number; valid: boolean; released: boolean }

export function getEntryIcon(entry: Entry): { name: string; color?: string } {
  if (entry.kind === 'dir') return { name: 'folder', color: '#5c93e8' }
  const dot = entry.name.lastIndexOf('.')
  const ext = dot > 0 ? entry.name.slice(dot + 1).toLowerCase() : ''
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext)) {
    return { name: 'folder-zip', color: '#76a9fa' }
  }
  if (ext === 'apk') {
    return { name: 'android', color: '#68d391' }
  }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v'].includes(ext) || isVideoFile(entry.name)) {
    return { name: 'movie', color: '#f87171' }
  }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'].includes(ext)) {
    return { name: 'audio-file', color: '#a78bfa' }
  }
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext) || entry.preview?.available) {
    return { name: 'image', color: '#4ecdc4' }
  }
  if (['js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'py', 'java', 'c', 'cpp', 'h', 'cs', 'rb', 'php', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'xml', 'html', 'css'].includes(ext)) {
    return { name: 'code', color: '#38bdf8' }
  }
  if (['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'odt', 'ods', 'odp', 'hwp', 'hwpx', 'csv'].includes(ext)) {
    return { name: 'description', color: '#60a5fa' }
  }
  return { name: 'draft', color: '#94a3b8' }
}

// Both file views select and activate from click, after the touch gesture has ended.
export function useFileActivation(onSelect: (entry: Entry, index: number, event: MouseEvent<HTMLDivElement>) => void, onOpen: (entry: Entry) => void) {
  const gestureRef = useRef<Gesture | null>(null)
  const lastClickRef = useRef<Tap | null>(null)
  const cancel = () => {
    gestureRef.current = null
    lastClickRef.current = null
  }
  const invalidate = () => {
    if (gestureRef.current) gestureRef.current.valid = false
    lastClickRef.current = null
  }
  const handlers = (entry: Entry, index: number): ActivationHandlers => ({
    onPointerDown(event) {
      if (!event.isPrimary || event.button !== 0) {
        invalidate()
        return
      }
      if (lastClickRef.current?.path !== entry.path) lastClickRef.current = null
      gestureRef.current = { path: entry.path, pointerType: event.pointerType, pointerId: event.pointerId, time: Date.now(), x: event.clientX, y: event.clientY, valid: true, released: false }
      if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) lastClickRef.current = null
    },
    onPointerMove(event) {
      const start = gestureRef.current
      if (start?.pointerId === event.pointerId && (Math.abs(event.clientX - start.x) >= TAP_MOVE_PX || Math.abs(event.clientY - start.y) >= TAP_MOVE_PX)) invalidate()
    },
    onPointerUp(event) {
      const start = gestureRef.current
      if (!start || start.pointerId !== event.pointerId) return
      start.released = true
      if (start.path !== entry.path || Math.abs(event.clientX - start.x) >= TAP_MOVE_PX || Math.abs(event.clientY - start.y) >= TAP_MOVE_PX || (start.pointerType === 'touch' && Date.now() - start.time >= DOUBLE_TAP_MS)) invalidate()
    },
    onPointerCancel: invalidate,
    onClick(event) {
      const gesture = gestureRef.current
      gestureRef.current = null
      const native = event.nativeEvent as globalThis.PointerEvent
      const pointerType = native.pointerType || gesture?.pointerType || 'mouse'
      if (gesture && (gesture.path !== entry.path || !gesture.valid || !gesture.released)) {
        lastClickRef.current = null
        return
      }
      const last = lastClickRef.current
      lastClickRef.current = null
      onSelect(entry, index, event)
      if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) return
      const now = Date.now()
      const sameEntry = last !== null && last.path === entry.path && last.pointerType === pointerType
      const doubleTap = pointerType === 'touch' && gesture && last && sameEntry && now - last.time < DOUBLE_TAP_MS && Math.abs(event.clientX - last.x) < DOUBLE_TAP_DISTANCE_PX && Math.abs(event.clientY - last.y) < DOUBLE_TAP_DISTANCE_PX
      const doubleClick = pointerType === 'mouse' && sameEntry && event.detail === 2
      if (doubleTap || doubleClick) onOpen(entry)
      else if (pointerType !== 'touch' || gesture) lastClickRef.current = { path: entry.path, pointerType, time: now, x: event.clientX, y: event.clientY }
    }
  })
  return { handlers, cancel }
}

export interface FileRowProps extends ActivationHandlers {
  entry: Entry
  rowIndex: number
  selected: boolean
  focused: boolean
  domId: string
  encrypted?: boolean
  onCancelActivation: () => void
  onContextMenu: (event: MouseEvent) => void
  onToggleCheck: () => void
}

export function FileRow({
  entry,
  rowIndex,
  selected,
  focused,
  domId,
  encrypted = false,
  onCancelActivation,
  onContextMenu,
  onToggleCheck,
  ...activationHandlers
}: FileRowProps) {
  const { t } = useI18n()
  const fileIcon = getEntryIcon(entry)

  return (
    <div
      {...activationHandlers}
      id={domId}
      className={`sc-row${selected ? ' sc-row--selected' : ''}${focused ? ' sc-row--focused' : ''}`}
      role="row"
      aria-rowindex={rowIndex}
      aria-selected={selected}
      onContextMenu={onContextMenu}
    >
      <span
        className="sc-row__cell sc-row__cell--select sc-touch-target"
        role="gridcell"
        onClick={(event) => {
          event.stopPropagation()
          onCancelActivation()
          onToggleCheck()
        }}
        onPointerUp={(event) => event.stopPropagation()}
        onPointerDown={(event) => {
          event.stopPropagation()
          onCancelActivation()
        }}
        onDoubleClick={(event) => event.stopPropagation()}
      >
        <span className={`sc-custom-checkbox${selected ? ' sc-custom-checkbox--checked' : ''}`} aria-hidden="true">
          {selected ? <Icon name="check" size={13} /> : null}
        </span>
        <span className="sc-sr-only">{t('common.select', { name: entry.name })}</span>
      </span>
      <span className="sc-row__cell sc-row__cell--name" role="gridcell">
        <span className="sc-row__icon-badge" style={{ color: fileIcon.color }}>
          <Icon name={fileIcon.name} size={20} />
        </span>
        <MiddleEllipsis name={entry.name} className="sc-filename" />
        {entry.confusable ? (
          <span className="sc-row__badge" title={t('common.look_alike_characters')}>
            <Icon name="warning" />
          </span>
        ) : null}
      </span>
      <span className="sc-row__cell sc-row__cell--size" role="gridcell">
        {entry.kind === 'dir' ? '-' : formatEntrySize(entry.size, encrypted)}
      </span>
      <span className="sc-row__cell sc-row__cell--mtime" role="gridcell">
        {formatDateNs(entry.mtime_ns)}
      </span>
      <span className="sc-row__cell sc-row__cell--actions" role="gridcell">
        <button
          type="button"
          className="sc-row__more-btn sc-icon-button"
          aria-label={t('browse.more')}
          onClick={(event) => {
            event.stopPropagation()
            onContextMenu(event)
          }}
        >
          <Icon name="more-vert" size={18} />
        </button>
      </span>
    </div>
  )
}
