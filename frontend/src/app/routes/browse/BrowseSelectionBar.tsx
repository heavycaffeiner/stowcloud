import type { Entry } from '../../../lib/api/client'
import type { RowAction } from '../../../features/files/logic/row-actions'
import { formatBytes } from '../../../lib/format/bytes'
import { Icon } from '../../../ui/Icon'
import * as styles from './BrowseSelectionBar.css'
import * as iconButtonStyles from '../../../ui/IconButton.css'
import { cx } from '../../../ui/cx'

type SelectionBarState = {
  compact: boolean
  details: boolean
  count: number
  bytes: number
}

type SelectionBarActions = {
  onClear: () => void
  onToggleDetails: () => void
  actions: readonly RowAction[]
}

type BrowseSelectionBarProps = {
  state: SelectionBarState
  actions: SelectionBarActions
  t: (key: string, params?: Record<string, string | number>) => string
}

const actionIcons: Record<string, Parameters<typeof Icon>[0]['name']> = {
  edit: 'edit-document',
  download: 'download',
  share: 'link',
  rename: 'rename',
  transfer: 'move',
  duplicate: 'copy',
  delete: 'delete'
}

export function BrowseSelectionBar({ state, actions, t }: BrowseSelectionBarProps) {
  const { compact, details, count, bytes } = state
  return (
    <div className={styles.bar}>
      <div className={styles.barInner}>
        <button
          type="button"
          className={cx(styles.iconBtn, iconButtonStyles.root)}
          aria-label={t('browse.clear_selection')}
          onClick={actions.onClear}
        >
          <Icon name="close" size={16} />
        </button>
        <span className={styles.count}>
          {compact ? t('common.item_count', { count }) : t('browse.selected', { count, size: formatBytes(bytes) })}
        </span>
        <span className={styles.divider} aria-hidden="true" />
        <div className={styles.actions}>
          <button
            type="button"
            className={cx(styles.iconBtn, iconButtonStyles.root)}
            aria-label={details ? t('details.hide') : t('details.show')}
            title={details ? t('details.hide') : t('details.show')}
            onClick={actions.onToggleDetails}
          >
            <Icon name="info" size={18} />
          </button>
          {actions.actions.map((action) => (
            <button
              key={action.key}
              type="button"
              className={cx(styles.iconBtn, iconButtonStyles.root)}
              aria-label={action.label}
              title={action.label}
              onClick={action.run}
            >
              <Icon name={actionIcons[action.key] ?? 'more-vert'} size={18} />
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
