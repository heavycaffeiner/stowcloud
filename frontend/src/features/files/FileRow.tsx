import { useRef } from 'react'
import type { HTMLAttributes, MouseEvent } from 'react'
import type { Entry } from '../../lib/api/types'
import { formatEntrySize } from '../../lib/format/entry-size'
import { formatModifiedDateNs } from '../../lib/i18n'
import { useI18n } from '../../lib/i18n/use-i18n'
import { isVideoFile } from '../preview/media-utils'
import { Icon } from '../../lib/ui/Icon'
import { MiddleEllipsis } from './MiddleEllipsis'
import '../../styles/features/files/browse-ui.css.ts'

const TAP_MAX_MS = 450
const TAP_MOVE_PX = 12

type ActivationHandlers = Pick<HTMLAttributes<HTMLDivElement>, 'onClick' | 'onPointerDown' | 'onPointerMove' | 'onPointerUp' | 'onPointerCancel'>
type Gesture = { path: string; pointerType: string; pointerId: number; time: number; x: number; y: number; valid: boolean; released: boolean }

export function getEntryIcon(entry: Entry): { name: string; color?: string } {
  if (entry.kind === 'dir') return { name: 'folder', color: 'var(--sc-icon-color)' }
  const dot = entry.name.lastIndexOf('.')
  const ext = dot > 0 ? entry.name.slice(dot + 1).toLowerCase() : ''
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext)) {
    return { name: 'folder-zip', color: 'var(--sc-icon-color)' }
  }
  if (ext === 'apk') {
    return { name: 'android', color: 'var(--sc-icon-color)' }
  }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v'].includes(ext) || isVideoFile(entry.name)) {
    return { name: 'movie', color: 'var(--sc-icon-color)' }
  }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'].includes(ext)) {
    return { name: 'audio-file', color: 'var(--sc-icon-color)' }
  }
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext) || entry.preview?.available) {
    return { name: 'image', color: 'var(--sc-icon-color)' }
  }
  if (['js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'py', 'java', 'c', 'cpp', 'h', 'cs', 'rb', 'php', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'xml', 'html', 'css'].includes(ext)) {
    return { name: 'code', color: 'var(--sc-icon-color)' }
  }
  if (['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'odt', 'ods', 'odp', 'hwp', 'hwpx', 'csv'].includes(ext)) {
    return { name: 'description', color: 'var(--sc-icon-color)' }
  }
  return { name: 'draft', color: 'var(--sc-icon-color)' }
}

// Touch opens on one completed tap; pointer devices keep click-to-select and double-click-to-open.
export function useFileActivation(onSelect: (entry: Entry, index: number, event: MouseEvent<HTMLDivElement>) => void, onOpen: (entry: Entry) => void) {
  const gestureRef = useRef<Gesture | null>(null)
  const cancel = () => {
    gestureRef.current = null
  }
  const invalidate = () => {
    if (gestureRef.current) gestureRef.current.valid = false
  }
  const handlers = (entry: Entry, index: number): ActivationHandlers => ({
    onPointerDown(event) {
      if (!event.isPrimary || event.button !== 0) {
        invalidate()
        return
      }
      gestureRef.current = { path: entry.path, pointerType: event.pointerType, pointerId: event.pointerId, time: Date.now(), x: event.clientX, y: event.clientY, valid: true, released: false }
    },
    onPointerMove(event) {
      const start = gestureRef.current
      if (start?.pointerId === event.pointerId && (Math.abs(event.clientX - start.x) >= TAP_MOVE_PX || Math.abs(event.clientY - start.y) >= TAP_MOVE_PX)) invalidate()
    },
    onPointerUp(event) {
      const start = gestureRef.current
      if (!start || start.pointerId !== event.pointerId) return
      start.released = true
      if (start.path !== entry.path || Math.abs(event.clientX - start.x) >= TAP_MOVE_PX || Math.abs(event.clientY - start.y) >= TAP_MOVE_PX || (start.pointerType === 'touch' && Date.now() - start.time >= TAP_MAX_MS)) invalidate()
    },
    onPointerCancel: invalidate,
    onClick(event) {
      const gesture = gestureRef.current
      gestureRef.current = null
      const native = event.nativeEvent as globalThis.PointerEvent
      const pointerType = native.pointerType || gesture?.pointerType || 'mouse'
      if (gesture && (gesture.path !== entry.path || !gesture.valid || !gesture.released)) return
      if (pointerType === 'touch') {
        if (!event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey) onOpen(entry)
        return
      }
      onSelect(entry, index, event)
      if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) return
      if ((pointerType === 'mouse' || pointerType === 'pen') && event.detail === 2) onOpen(entry)
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
        className="sc-row-cell sc-row-cell--select sc-touch-target"
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
      <span className="sc-row-cell sc-row-cell--name" role="gridcell">
        <span className="sc-row-icon-badge" style={{ color: fileIcon.color }}>
          <Icon name={fileIcon.name} size={20} />
        </span>
        <span className="sc-row-name-copy">
          <MiddleEllipsis name={entry.name} className="sc-filename" />
          <span className="sc-row-mobile-meta">
            {entry.kind === 'dir' ? t('details.folder') : <>{formatEntrySize(entry.size, encrypted)}<span aria-hidden="true"> · </span>{formatModifiedDateNs(entry.mtime_ns)}</>}
          </span>
        </span>
        {entry.confusable ? (
          <span className="sc-row-badge" title={t('common.look_alike_characters')}>
            <Icon name="warning" />
          </span>
        ) : null}
      </span>
      <span className="sc-row-cell sc-row-cell--size" role="gridcell">
        {entry.kind === 'dir' ? '-' : formatEntrySize(entry.size, encrypted)}
      </span>
      <span className="sc-row-cell sc-row-cell--mtime" role="gridcell">
        {formatModifiedDateNs(entry.mtime_ns)}
      </span>
      <span className="sc-row-cell sc-row-cell--actions" role="gridcell">
        <button
          type="button"
          className="sc-row-more-btn sc-icon-button"
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
