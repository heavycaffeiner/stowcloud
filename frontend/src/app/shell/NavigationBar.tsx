import { useI18n } from '../../hooks/use-i18n'
import { cx, Icon, type IconName } from '@/shared/ui'
import * as styles from './NavigationBar.css'

export interface NavigationBarItem {
  readonly id: string
  readonly label: string
  readonly icon: IconName
  readonly href?: string
  readonly popup?: 'dialog'
  readonly expanded?: boolean
  readonly controls?: string
}

export interface NavigationBarProps {
  readonly items: readonly NavigationBarItem[]
  readonly active: string
  readonly onSelect: (id: string) => void
}

/** The compact layout's bottom bar of top-level destinations. */
export function NavigationBar({ items, active, onSelect }: NavigationBarProps) {
  const { t } = useI18n()
  return (
    <nav className={styles.root} aria-label={t('common.main_menu')}>
      {items.map((item) => {
        const selected = item.id === active
        return (
          <button
            key={item.id}
            type="button"
            className={cx(styles.item, selected && styles.itemActive)}
            aria-current={selected && !item.popup ? 'page' : undefined}
            aria-haspopup={item.popup}
            aria-expanded={item.popup ? item.expanded : undefined}
            aria-controls={item.controls}
            onClick={(event) => {
              if (item.popup) event.currentTarget.focus()
              onSelect(item.id)
            }}
          >
            <span className={styles.icon}>
              <Icon name={item.icon} />
            </span>
            <span className={styles.label}>{item.label}</span>
          </button>
        )
      })}
    </nav>
  )
}
