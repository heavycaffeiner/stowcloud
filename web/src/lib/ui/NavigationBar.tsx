import { useI18n } from '../i18n/use-i18n'
import { Icon } from './Icon'

export interface NavigationBarItem {
  readonly id: string
  readonly label: string
  readonly icon: string
  readonly href?: string
}

export interface NavigationBarProps {
  readonly items: readonly NavigationBarItem[]
  readonly active: string
  readonly onselect: (id: string) => void
}

export function NavigationBar({ items, active, onselect }: NavigationBarProps) {
  const { t } = useI18n()
  return (
    <nav className="sc-nav-bar" aria-label={t('common.main_menu')}>
      {items.map((item) => {
        const selected = item.id === active
        return (
          <button
            key={item.id}
            type="button"
            className={selected ? 'sc-nav-bar__item is-active' : 'sc-nav-bar__item'}
            aria-current={selected ? 'page' : undefined}
            onClick={() => onselect(item.id)}
          >
            <Icon name={item.icon} />
            <span>{item.label}</span>
          </button>
        )
      })}
    </nav>
  )
}
