import type { CSSProperties, MouseEvent, PointerEvent, ReactNode } from 'react'
import { useI18n } from '../../../../hooks/use-i18n'
import { formatModifiedDate } from '../../../../i18n'
import { cx, Icon, StowCheckboxIndicator, StowIconButton, type IconName } from '@/shared/ui'
import { touchTarget } from '@/shared/theme'
import { formatEntrySize } from '../../entry-size'
import type { ActivationHandlers } from '../../hooks/use-file-activation'
import type { FileEntry, FileType } from '../../model/file-entry'
import { MiddleEllipsis } from '../MiddleEllipsis'
import * as styles from './FileItem.css'

export type FileItemView = 'grid' | 'list'

const ICONS: Record<FileType, IconName> = {
  folder: 'folder',
  archive: 'folder-zip',
  app: 'android',
  audio: 'audio-file',
  video: 'movie',
  image: 'image',
  code: 'code',
  document: 'description',
  file: 'draft'
}

export function fileIcon(type: FileType): IconName {
  return ICONS[type]
}

export interface FileItemProps extends ActivationHandlers {
  item: FileEntry
  view: FileItemView
  selected?: boolean
  /** The keyboard's position in the view. */
  focused?: boolean
  id?: string
  encrypted?: boolean
  /** A file card's preview. The file type's icon stands in without one. */
  thumbnail?: ReactNode
  /** The row's place in the list, or the card's column in its grid row, counting from 1. */
  position?: number
  style?: CSSProperties
  onContextMenu?: (event: MouseEvent<HTMLElement>) => void
  onMenu?: (event: MouseEvent<HTMLElement>) => void
  onToggle?: () => void
  /** Drops a tap in progress when the press lands on the check or the menu button instead. */
  onCancelActivation?: () => void
}

/** One file or folder, as a list row or a grid card. */
export function FileItem({
  item,
  view,
  selected = false,
  focused = false,
  id,
  encrypted = false,
  thumbnail,
  position,
  style,
  onContextMenu,
  onMenu,
  onToggle,
  onCancelActivation,
  ...activation
}: FileItemProps) {
  const { t } = useI18n()
  const list = view === 'list'
  const folder = item.type === 'folder'
  const size = formatEntrySize(item.size, encrypted)
  const modified = formatModifiedDate(item.modifiedAt)
  // Presses on the check and the menu button are theirs, not the item's tap or double click.
  const control = {
    onPointerDown: (event: PointerEvent) => {
      event.stopPropagation()
      onCancelActivation?.()
    },
    onPointerUp: (event: PointerEvent) => event.stopPropagation(),
    onDoubleClick: (event: MouseEvent) => event.stopPropagation()
  }
  const check = (
    <div
      className={cx(list ? cx(styles.cell, styles.colSelect) : styles.check, touchTarget)}
      role={list ? 'gridcell' : undefined}
      {...control}
      onClick={(event) => {
        event.stopPropagation()
        onCancelActivation?.()
        onToggle?.()
      }}
    >
      <StowCheckboxIndicator checked={selected} label={t('common.select', { name: item.name })} />
    </div>
  )
  const kebab = (
    <StowIconButton
      className={cx(styles.kebab, list && styles.rowKebab)}
      label={t('grid.more_actions', { name: item.name })}
      icon="more-vert"
      tabIndex={-1}
      aria-haspopup="menu"
      {...control}
      onClick={(event) => {
        event.stopPropagation()
        onCancelActivation?.()
        onMenu?.(event)
      }}
    />
  )
  const shared = {
    ...activation,
    id,
    'aria-selected': selected,
    style,
    onContextMenu
  }

  if (list)
    return (
      <div
        {...shared}
        className={cx(styles.row, selected && styles.selected, focused && styles.focused)}
        role="row"
        aria-rowindex={position}
      >
        {check}
        <span className={cx(styles.cell, styles.colName)} role="gridcell">
          <span className={styles.icon}>
            <Icon name={fileIcon(item.type)} size={20} />
          </span>
          <span className={styles.nameCopy}>
            <MiddleEllipsis name={item.name} className={styles.rowName} />
            <span className={styles.mobileMeta}>
              {folder ? (
                t('details.folder')
              ) : (
                <>
                  {size}
                  <span aria-hidden="true">, </span>
                  {modified}
                </>
              )}
            </span>
          </span>
        </span>
        <span className={cx(styles.cell, styles.colSize, styles.detail)} role="gridcell">
          {folder ? '-' : size}
        </span>
        <span className={cx(styles.cell, styles.colModified, styles.detail)} role="gridcell">
          {modified}
        </span>
        <span className={cx(styles.cell, styles.colActions)} role="gridcell">
          {kebab}
        </span>
      </div>
    )

  const name = <MiddleEllipsis name={item.name} className={styles.cardName} />
  return (
    <div
      {...shared}
      className={cx(styles.card, folder && styles.cardFolder, selected && styles.selected, focused && styles.focused)}
      role="gridcell"
      aria-colindex={position}
    >
      {folder ? (
        <>
          {check}
          <span className={styles.icon}>
            <Icon name="folder" />
          </span>
          {name}
          {kebab}
        </>
      ) : (
        <>
          <div className={styles.cardHead}>
            {check}
            {name}
            {kebab}
          </div>
          <div className={styles.thumb}>{thumbnail ?? <Icon name={fileIcon(item.type)} size={40} />}</div>
          <span className={styles.cardMeta}>{size}</span>
        </>
      )}
    </div>
  )
}

/** The place of an item that has not loaded yet. */
export function FileItemSkeleton({
  view,
  folder = false,
  position,
  style
}: {
  view: FileItemView
  /** A folder card, which has no preview. */
  folder?: boolean
  position?: number
  style?: CSSProperties
}) {
  if (view === 'list')
    return (
      <div
        className={cx(styles.row, styles.placeholderRow)}
        role="row"
        aria-rowindex={position}
        aria-busy
        style={style}
      >
        <span className={cx(styles.cell, styles.colSelect)} role="gridcell" />
        <span className={cx(styles.cell, styles.colName)} role="gridcell">
          <span className={cx(styles.bar, styles.barIcon)} />
          <span className={cx(styles.bar, styles.barLong)} />
        </span>
        <span className={cx(styles.cell, styles.colSize)} role="gridcell">
          <span className={cx(styles.bar, styles.barShort)} />
        </span>
        <span className={cx(styles.cell, styles.colModified)} role="gridcell">
          <span className={cx(styles.bar, styles.barLong)} />
        </span>
      </div>
    )
  return (
    <div
      className={cx(styles.card, styles.placeholderCard)}
      role="gridcell"
      aria-colindex={position}
      aria-busy
      style={style}
    >
      <span className={cx(styles.bar, styles.barLong)} />
      {folder ? null : <span className={cx(styles.bar, styles.barBlock)} />}
    </div>
  )
}
