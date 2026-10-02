import type { RowAction } from '../../logic/row-actions'
import { useI18n } from '../../../../hooks/use-i18n'
import { formatBytes } from '../../../../lib/format/bytes'
import { StowIconButton, StowMenuButton, StowMenuItem } from '@/shared/ui'
import { media } from '@/shared/theme'
import { useCompact } from '@/hooks/use-compact'
import { useMediaQuery } from '@/hooks/use-media-query'
import { selection } from '../../model/selection'
import { detailsPanel, toggleDetails } from '../../model/view-prefs'
import * as styles from './SelectionToolbar.css'

// A phone fits three buttons beside the count. Past that, the first two actions stay and the rest go in a menu.
const PHONE_SLOTS = 3

interface SelectionToolbarProps {
  count: number
  bytes: number
  actions: readonly RowAction[]
}

export function SelectionToolbar({ count, bytes, actions }: SelectionToolbarProps) {
  const { t } = useI18n()
  const compact = useCompact()
  const phone = useMediaQuery(media.compact)
  const details = detailsPanel.value === 'open'
  const info: RowAction = {
    key: 'info',
    label: details ? t('details.hide') : t('details.show'),
    icon: 'info',
    run: toggleDetails
  }
  const fold = phone && actions.length + 1 > PHONE_SLOTS
  const shown = fold ? actions.slice(0, PHONE_SLOTS - 1) : [info, ...actions]
  const folded = fold ? [info, ...actions.slice(PHONE_SLOTS - 1)] : []
  return (
    <div className={styles.bar}>
      <div className={styles.barInner}>
        <StowIconButton label={t('browse.clear_selection')} icon="close" onClick={selection.clear} />
        <span className={styles.count}>
          {compact ? t('common.item_count', { count }) : t('browse.selected', { count, size: formatBytes(bytes) })}
        </span>
        <span className={styles.divider} aria-hidden="true" />
        <div className={styles.actions}>
          {shown.map((action) => (
            <StowIconButton key={action.key} label={action.label} icon={action.icon} onClick={action.run} />
          ))}
          {folded.length > 0 ? (
            <StowMenuButton
              label={t('browse.more')}
              align="end"
              menu={(close) =>
                folded.map((action) => (
                  <StowMenuItem
                    key={action.key}
                    icon={action.icon}
                    onClick={() => {
                      close()
                      action.run()
                    }}
                  >
                    {action.label}
                  </StowMenuItem>
                ))
              }
            >
              <StowIconButton label={t('browse.more')} icon="more-vert" />
            </StowMenuButton>
          ) : null}
        </div>
      </div>
    </div>
  )
}
