import { Fragment, useEffect, useId, useRef, useState } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { useI18n } from '../i18n/use-i18n'
import { useStore } from '../store/use-store'
import { ui } from '../store/ui.store'
import { Menu } from './Menu'
import { Icon } from './Icon'

export interface BreadcrumbCrumb {
  label: string
  path: string
}

export interface BreadcrumbProps {
  crumbs: BreadcrumbCrumb[]
  onNavigate?: (path: string) => void
}

export function Breadcrumb({ crumbs, onNavigate }: BreadcrumbProps) {
  const { t } = useI18n()
  const compact = useStore(ui, (state) => state.compact)
  const menuId = useId()
  const menuRef = useRef<HTMLDivElement>(null)
  const [menuOpen, setMenuOpen] = useState(false)
  const [menuPos, setMenuPos] = useState({ x: 0, y: 0 })

  useEffect(() => {
    if (!menuOpen) return
    let cancelled = false
    const menu = menuRef.current?.closest('mdui-menu') as (HTMLElement & { updateComplete?: Promise<boolean> }) | null
    void Promise.resolve(menu?.updateComplete).then(() => {
      if (!cancelled) menuRef.current?.querySelector<HTMLButtonElement>('button')?.focus()
    })
    return () => { cancelled = true }
  }, [menuOpen])

  const shouldCollapse = crumbs.length > (compact ? 2 : 4)
  const collapsedCrumbs = shouldCollapse ? crumbs.slice(compact ? 0 : 1, compact ? -1 : -2) : []
  const visibleCrumbs = shouldCollapse
    ? compact ? crumbs.slice(-1) : [crumbs[0], ...crumbs.slice(-2)]
    : crumbs
  const collapsedLabel = collapsedCrumbs.map((crumb) => crumb.label).join(' / ')

  const openCollapsedMenu = (event: ReactMouseEvent<HTMLButtonElement>) => {
    event.stopPropagation()
    const rect = event.currentTarget.getBoundingClientRect()
    setMenuPos({ x: Math.min(rect.left, window.innerWidth - 336), y: rect.bottom + 4 })
    setMenuOpen(true)
  }

  return (
    <nav className="sc-breadcrumb" aria-label={t('breadcrumb.path')}>
      <ol className="sc-breadcrumb__list">
        {visibleCrumbs.map((crumb, index) => (
          <Fragment key={crumb.path}>
            {shouldCollapse && index === (compact ? 0 : 1) && (
              <li className="sc-breadcrumb__item sc-breadcrumb__item--ellipsis">
                <button
                  type="button"
                  className="sc-breadcrumb__ellipsis-btn"
                  aria-haspopup="menu"
                  aria-expanded={menuOpen}
                  aria-controls={menuId}
                  aria-label={`${t('browse.more')}: ${collapsedLabel}`}
                  title={collapsedLabel}
                  onClick={openCollapsedMenu}
                >
                  …
                </button>
                <span className="sc-breadcrumb__sep" aria-hidden="true">/</span>
              </li>
            )}
            <li className={`sc-breadcrumb__item sc-breadcrumb__item--${index === visibleCrumbs.length - 1 ? 'current' : index === 0 ? 'root' : index === visibleCrumbs.length - 2 ? 'parent' : 'ancestor'}`}>
              {index < visibleCrumbs.length - 1 ? (
                <>
                  <button
                    type="button"
                    className="sc-breadcrumb__link"
                    title={crumb.label}
                    onClick={() => onNavigate?.(crumb.path)}
                  >
                    <span className="sc-breadcrumb__label">{crumb.label}</span>
                  </button>
                  <span className="sc-breadcrumb__sep" aria-hidden="true">/</span>
                </>
              ) : (
                <span
                  className="sc-breadcrumb__current"
                  aria-current="page"
                  title={crumb.label}
                >
                  <span className="sc-breadcrumb__label">{crumb.label}</span>
                </span>
              )}
            </li>
          </Fragment>
        ))}
      </ol>

      {shouldCollapse && (
        <Menu open={menuOpen} onClose={() => setMenuOpen(false)} x={menuPos.x} y={menuPos.y}>
          <div ref={menuRef} id={menuId} className="sc-browse-new-menu sc-breadcrumb__menu" role="menu" aria-label={t('breadcrumb.path')}>
            {collapsedCrumbs.map((c) => (
              <button
                key={c.path}
                type="button"
                role="menuitem"
                title={c.label}
                onClick={() => {
                  setMenuOpen(false)
                  onNavigate?.(c.path)
                }}
              >
                <Icon name="folder" size={18} />
                <span className="sc-breadcrumb__menu-label">{c.label}</span>
              </button>
            ))}
          </div>
        </Menu>
      )}
    </nav>
  )
}
