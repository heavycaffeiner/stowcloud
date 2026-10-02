import type { RowAction } from '../logic/row-actions'
import { useI18n } from '../../../hooks/use-i18n'
import { formatBytes } from '../../../lib/format/bytes'
import { Icon } from '../../../ui/Icon'
import { useCompact } from '../../../ui/use-compact'
import { selection } from '../selection'
import { detailsPanel, toggleDetails } from '../view-prefs'
import * as styles from './BrowseSelectionBar.css'
import * as iconButtonStyles from '../../../ui/IconButton.css'
import { cx } from '../../../ui/cx'

interface BrowseSelectionBarProps {
  count: number
  bytes: number
  actions: readonly RowAction[]
}

export function BrowseSelectionBar({ count, bytes, actions }: BrowseSelectionBarProps) {
  const { t } = useI18n()
  const compact = useCompact()
  const details = detailsPanel.value === 'open'
  return (
    <div className={styles.bar}>
      <div className={styles.barInner}>
        <button
          type="button"
          className={cx(styles.iconBtn, iconButtonStyles.root)}
          aria-label={t('browse.clear_selection')}
          onClick={selection.clear}
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
            onClick={toggleDetails}
          >
            <Icon name="info" size={18} />
          </button>
          {actions.map((action) => (
            <button
              key={action.key}
              type="button"
              className={cx(styles.iconBtn, iconButtonStyles.root)}
              aria-label={action.label}
              title={action.label}
              onClick={action.run}
            >
              <Icon name={action.icon} size={18} />
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
