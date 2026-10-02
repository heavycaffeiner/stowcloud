import type { RowAction } from '../logic/row-actions'
import { useI18n } from '../../../hooks/use-i18n'
import { formatBytes } from '../../../lib/format/bytes'
import { StowIconButton } from '@/shared/ui'
import { useCompact } from '@/hooks/use-compact'
import { selection } from '../selection'
import { detailsPanel, toggleDetails } from '../view-prefs'
import * as styles from './BrowseSelectionBar.css'

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
        <StowIconButton label={t('browse.clear_selection')} icon="close" onClick={selection.clear} />
        <span className={styles.count}>
          {compact ? t('common.item_count', { count }) : t('browse.selected', { count, size: formatBytes(bytes) })}
        </span>
        <span className={styles.divider} aria-hidden="true" />
        <div className={styles.actions}>
          <StowIconButton label={details ? t('details.hide') : t('details.show')} icon="info" onClick={toggleDetails} />
          {actions.map((action) => (
            <StowIconButton key={action.key} label={action.label} icon={action.icon} onClick={action.run} />
          ))}
        </div>
      </div>
    </div>
  )
}
