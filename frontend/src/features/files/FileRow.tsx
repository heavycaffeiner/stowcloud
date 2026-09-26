import type { MouseEvent as ReactMouseEvent } from 'react'
import type { Entry } from '../../lib/api/types'
import type { ActivationHandlers } from './use-file-activation'
import { formatEntrySize } from '../../lib/format/entry-size'
import { formatModifiedDateNs } from '../../lib/i18n'
import { useI18n } from '../../lib/i18n/use-i18n'
import { isVideoFile } from '../preview/media-utils'
import { Icon } from '../../lib/ui/Icon'
import { MiddleEllipsis } from './MiddleEllipsis'
import '../../styles/features/files/browse-ui.css.ts'

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
export interface FileRowProps extends ActivationHandlers {
  entry: Entry
  rowIndex: number
  selected: boolean
  focused: boolean
  domId: string
  encrypted?: boolean
  onContextMenu: (event: ReactMouseEvent<HTMLElement>) => void
  onCancelActivation: () => void
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
      className={`sc-row${selected ? ' sc-row-selected' : ''}${focused ? ' sc-row-focused' : ''}`}
      role="row"
      aria-rowindex={rowIndex}
      aria-selected={selected}
      onContextMenu={onContextMenu}
    >
      <span
        className="sc-row-cell sc-row-cell-select sc-touch-target"
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
        <span className={`sc-custom-checkbox${selected ? ' sc-custom-checkbox-checked' : ''}`} aria-hidden="true">
          {selected ? <Icon name="check" size={13} /> : null}
        </span>
        <span className="sc-sr-only">{t('common.select', { name: entry.name })}</span>
      </span>
      <span className="sc-row-cell sc-row-cell-name" role="gridcell">
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
      <span className="sc-row-cell sc-row-cell-size" role="gridcell">
        {entry.kind === 'dir' ? '-' : formatEntrySize(entry.size, encrypted)}
      </span>
      <span className="sc-row-cell sc-row-cell-mtime" role="gridcell">
        {formatModifiedDateNs(entry.mtime_ns)}
      </span>
      <span className="sc-row-cell sc-row-cell-actions" role="gridcell">
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
