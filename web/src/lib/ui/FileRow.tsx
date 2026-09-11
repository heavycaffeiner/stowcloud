import { useRef } from 'react'
import type { Entry } from '../api/types'
import { formatEntrySize } from '../format/entry-size'
import { formatDateNs } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { isVideoFile } from './media-utils'
import { Icon } from './Icon'
import './browse-ui.css'

export interface FileRowProps {
  entry: Entry
  rowIndex: number
  selected: boolean
  focused: boolean
  domId: string
  encrypted?: boolean
  compact?: boolean
  onClick: (event: React.MouseEvent) => void
  onDoubleClick?: () => void
  onContextMenu: (event: React.MouseEvent) => void
  onToggleCheck: () => void
  onCompactOpen?: () => void
}

export function FileRow({ entry, rowIndex, selected, focused, domId, encrypted = false, compact = false, onClick, onDoubleClick, onContextMenu, onToggleCheck, onCompactOpen }: FileRowProps) {
  const { t } = useI18n()
  const suppress = useRef(false)
  const icon = entry.kind === 'dir' ? 'folder' : isVideoFile(entry.name) ? 'movie' : entry.preview?.available ? 'image' : 'draft'
  const onNamePointerDown = (event: React.PointerEvent) => {
    if (!compact || (event.pointerType !== 'touch' && event.pointerType !== 'pen') || event.button !== 0) return
    suppress.current = true
    event.preventDefault()
    event.stopPropagation()
    onCompactOpen?.()
  }
  const stopSuppressed = (event: React.MouseEvent) => {
    if (!suppress.current) return
    suppress.current = false
    event.preventDefault()
    event.stopPropagation()
  }
  return (
    <div id={domId} className={`sc-row${selected ? ' sc-row--selected' : ''}${focused ? ' sc-row--focused' : ''}`} role="row" aria-rowindex={rowIndex} aria-selected={selected} onClick={onClick} onDoubleClick={onDoubleClick} onContextMenu={onContextMenu}>
      <span className="sc-row__cell sc-row__cell--select sc-touch-target" role="gridcell" onClick={(event) => { event.stopPropagation(); onToggleCheck() }} onDoubleClick={(event) => event.stopPropagation()}>
        <input type="checkbox" checked={selected} readOnly tabIndex={-1} aria-label={t('common.select', { name: entry.name })} />
      </span>
      <span className="sc-row__cell sc-row__cell--name" role="gridcell" onPointerDown={onNamePointerDown} onClick={stopSuppressed} onDoubleClick={stopSuppressed}>
        <Icon name={icon} />
        <span className="sc-filename"><bdi>{entry.name}</bdi></span>
        {entry.confusable ? <span className="sc-row__badge" title={t('common.look_alike_characters')}><Icon name="warning" /></span> : null}
      </span>
      <span className="sc-row__cell sc-row__cell--size" role="gridcell">{entry.kind === 'dir' ? '-' : formatEntrySize(entry.size, encrypted)}</span>
      <span className="sc-row__cell sc-row__cell--mtime" role="gridcell">{formatDateNs(entry.mtime_ns)}</span>
    </div>
  )
}
