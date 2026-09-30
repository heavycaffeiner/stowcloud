import { Icon } from '../../ui/Icon'
import * as styles from './PageTabs.css'

export interface PageTabItem<T extends string> {
  value: T
  label: string
  icon: string
}

interface PageTabsProps<T extends string> {
  label: string
  items: readonly PageTabItem<T>[]
  active: T
  onSelect: (value: T) => void
}

export function PageTabs<T extends string>({ label, items, active, onSelect }: PageTabsProps<T>) {
  return (
    <nav className={styles.tabs} aria-label={label}>
      {items.map((item) => (
        <button
          key={item.value}
          type="button"
          className={styles.tab}
          aria-current={item.value === active ? 'page' : undefined}
          onClick={() => onSelect(item.value)}
        >
          <Icon name={item.icon} />
          {item.label}
        </button>
      ))}
    </nav>
  )
}
