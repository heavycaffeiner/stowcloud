import type { MouseEvent as ReactMouseEvent } from 'react'
import type { ActivationHandlers } from '../hooks/use-file-activation'
import { formatEntrySize } from '../entry-size'
import { formatModifiedDateNs } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { isVideoFile } from '../../preview/logic/media-utils'
import { cx, Icon, StowCheckboxIndicator, StowIconButton, type IconName } from '@/shared/ui'
import { MiddleEllipsis } from './MiddleEllipsis'
import * as styles from './FileRow.css'
import { touchTarget, vars } from '@/shared/theme'
import type { Entry } from '../api'

export function getEntryIcon(entry: Entry): { name: IconName; color?: string } {
  if (entry.kind === 'dir') return { name: 'folder', color: vars.color.text.icon }
  const dot = entry.name.lastIndexOf('.')
  const ext = dot > 0 ? entry.name.slice(dot + 1).toLowerCase() : ''
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext)) {
    return { name: 'folder-zip', color: vars.color.text.icon }
  }
  if (ext === 'apk') {
    return { name: 'android', color: vars.color.text.icon }
  }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v'].includes(ext) || isVideoFile(entry.name)) {
    return { name: 'movie', color: vars.color.text.icon }
  }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'].includes(ext)) {
    return { name: 'audio-file', color: vars.color.text.icon }
  }
  if (
    ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext) ||
    entry.preview?.available
  ) {
    return { name: 'image', color: vars.color.text.icon }
  }
  if (
    [
      'js',
      'ts',
      'tsx',
      'jsx',
      'go',
      'rs',
      'py',
      'java',
      'c',
      'cpp',
      'h',
      'cs',
      'rb',
      'php',
      'sh',
      'sql',
      'json',
      'yaml',
      'yml',
      'toml',
      'xml',
      'html',
      'css'
    ].includes(ext)
  ) {
    return { name: 'code', color: vars.color.text.icon }
  }
  if (
    [
      'pdf',
      'doc',
      'docx',
      'xls',
      'xlsx',
      'ppt',
      'pptx',
      'txt',
      'md',
      'rtf',
      'odt',
      'ods',
      'odp',
      'hwp',
      'hwpx',
      'csv'
    ].includes(ext)
  ) {
    return { name: 'description', color: vars.color.text.icon }
  }
  return { name: 'draft', color: vars.color.text.icon }
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
      className={cx(styles.root, selected && styles.selected, focused && styles.focused)}
      role="row"
      aria-rowindex={rowIndex}
      aria-selected={selected}
      onContextMenu={onContextMenu}
    >
      <div
        className={cx(styles.cell, styles.cellSelect, touchTarget)}
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
        <StowCheckboxIndicator checked={selected} label={t('common.select', { name: entry.name })} />
      </div>
      <span className={cx(styles.cell, styles.cellName)} role="gridcell">
        <span className={styles.iconBadge} style={{ color: fileIcon.color }}>
          <Icon name={fileIcon.name} size={20} />
        </span>
        <span className={styles.nameCopy}>
          <MiddleEllipsis name={entry.name} className={styles.name} />
          <span className={styles.mobileMeta}>
            {entry.kind === 'dir' ? (
              t('details.folder')
            ) : (
              <>
                {formatEntrySize(entry.size, encrypted)}
                <span aria-hidden="true">, </span>
                {formatModifiedDateNs(entry.mtime_ns)}
              </>
            )}
          </span>
        </span>
      </span>
      <span className={cx(styles.cell, styles.cellSize)} role="gridcell">
        {entry.kind === 'dir' ? '-' : formatEntrySize(entry.size, encrypted)}
      </span>
      <span className={cx(styles.cell, styles.cellMtime)} role="gridcell">
        {formatModifiedDateNs(entry.mtime_ns)}
      </span>
      <span className={cx(styles.cell, styles.cellActions)} role="gridcell">
        <StowIconButton
          className={styles.moreBtn}
          label={t('browse.more')}
          icon="more-vert"
          onClick={(event) => {
            event.stopPropagation()
            onContextMenu(event)
          }}
        />
      </span>
    </div>
  )
}
