import { UnstyledButton } from '@mantine/core'
import { Icon, type IconName } from '../Icon'
import * as styles from './PageTabs.css'

export interface StowPageTab<T extends string> {
  value: T
  label: string
  icon: IconName
}

export interface StowPageTabsProps<T extends string> {
  label: string
  items: readonly StowPageTab<T>[]
  active: T
  onSelect: (value: T) => void
}

/** Section links at the top of a page. The current one is marked for assistive technology as well as by color. */
export function StowPageTabs<T extends string>({ label, items, active, onSelect }: StowPageTabsProps<T>) {
  return (
    <nav className={styles.tabs} aria-label={label}>
      {items.map((item) => (
        <UnstyledButton
          key={item.value}
          className={styles.tab}
          aria-current={item.value === active ? 'page' : undefined}
          onClick={() => onSelect(item.value)}
        >
          <Icon name={item.icon} />
          {item.label}
        </UnstyledButton>
      ))}
    </nav>
  )
}
