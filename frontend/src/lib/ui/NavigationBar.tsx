import { useI18n } from '../i18n/use-i18n'
import { Icon } from './Icon'

export interface NavigationBarItem {
  readonly id: string
  readonly label: string
  readonly icon: string
  readonly href?: string
  readonly popup?: 'dialog'
  readonly expanded?: boolean
  readonly controls?: string
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
            aria-current={selected && !item.popup ? 'page' : undefined}
            aria-haspopup={item.popup}
            aria-expanded={item.popup ? item.expanded : undefined}
            aria-controls={item.controls}
            onClick={(event) => {
              if (item.popup) event.currentTarget.focus()
              onselect(item.id)
            }}
          >
            <span className="sc-nav-bar__icon"><Icon name={item.icon} /></span>
            <span className="sc-nav-bar__label">{item.label}</span>
          </button>
        )
      })}
    </nav>
  )
}
