import { useRef } from 'react'
import type { HTMLAttributes, MouseEvent } from 'react'
import type { Entry } from '../api/types'
import { formatEntrySize } from '../format/entry-size'
import { formatDateNs } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { isVideoFile } from './media-utils'
import { Icon } from './Icon'
import './browse-ui.css'

const DOUBLE_TAP_MS = 450
const DOUBLE_TAP_DISTANCE_PX = 30
const TAP_MOVE_PX = 12

type ActivationHandlers = Pick<HTMLAttributes<HTMLDivElement>, 'onClick' | 'onPointerDown' | 'onPointerMove' | 'onPointerUp' | 'onPointerCancel'>
type Tap = { path: string; pointerType: string; time: number; x: number; y: number }
type Gesture = Tap & { pointerId: number; valid: boolean; released: boolean }

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
  const icon = entry.kind === 'dir' ? 'folder' : isVideoFile(entry.name) ? 'movie' : entry.preview?.available ? 'image' : 'draft'

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
        <mdui-checkbox checked={selected} tabIndex={-1}><span className="sc-sr-only">{t('common.select', { name: entry.name })}</span></mdui-checkbox>
      </span>
      <span className="sc-row__cell sc-row__cell--name" role="gridcell">
        <Icon name={icon} />
        <span className="sc-filename"><bdi>{entry.name}</bdi></span>
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
    </div>
  )
}
