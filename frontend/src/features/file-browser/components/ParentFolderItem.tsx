import type { CSSProperties } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { cx, Icon } from '@/shared/ui'
import { focusRing } from '@/shared/theme'
import type { FileItemView } from './FileItem'
import { MiddleEllipsis } from './MiddleEllipsis'
import * as itemStyles from './FileItem/FileItem.css'
import * as styles from './ParentFolderItem.css'

interface ParentFolderItemProps {
  onNavigate: () => void
  className?: string
  view?: FileItemView
  position?: number
  style?: CSSProperties
}

/** Parent navigation shares folder geometry without entering the selectable file entries. */
export function ParentFolderItem({ onNavigate, className, view, position, style }: ParentFolderItemProps) {
  const { t } = useI18n()
  if (view) {
    const list = view === 'list'
    const button = (
      <button
        type="button"
        className={styles.navigate}
        aria-label={t('browse.parent_folder')}
        onClick={onNavigate}
        onKeyDown={(event) => event.stopPropagation()}
        onContextMenu={(event) => event.stopPropagation()}
      >
        <span className={itemStyles.icon}>
          <Icon name="folder" size={20} />
        </span>
        {list ? (
          <span className={itemStyles.nameCopy}>
            <MiddleEllipsis name=".." className={itemStyles.rowName} />
            <span className={itemStyles.mobileMeta}>{t('details.folder')}</span>
          </span>
        ) : (
          <MiddleEllipsis name=".." className={itemStyles.cardName} />
        )}
      </button>
    )
    return list ? (
      <div
        className={cx(itemStyles.row, styles.navigationRoot, className)}
        role="row"
        aria-rowindex={position}
        style={style}
      >
        <span className={cx(itemStyles.cell, itemStyles.colSelect)} role="gridcell" />
        <span className={cx(itemStyles.cell, itemStyles.colName, styles.navigationName)} role="gridcell">
          {button}
        </span>
        <span className={cx(itemStyles.cell, itemStyles.colSize, itemStyles.detail)} role="gridcell">
          -
        </span>
        <span className={cx(itemStyles.cell, itemStyles.colModified, itemStyles.detail)} role="gridcell" />
        <span className={cx(itemStyles.cell, itemStyles.colActions)} role="gridcell" />
      </div>
    ) : (
      <div
        className={cx(itemStyles.card, itemStyles.cardFolder, className)}
        role="gridcell"
        aria-colindex={position}
        style={style}
      >
        <span className={itemStyles.check} aria-hidden="true" />
        {button}
        <span className={itemStyles.check} aria-hidden="true" />
      </div>
    )
  }
  return (
    <button
      type="button"
      className={cx(styles.root, focusRing, className)}
      aria-label={t('browse.parent_folder')}
      onClick={onNavigate}
      onContextMenu={(event) => event.stopPropagation()}
    >
      <Icon name="folder" size={20} />
      <span aria-hidden="true">..</span>
      <span className={styles.label}>{t('browse.parent_folder')}</span>
    </button>
  )
}
