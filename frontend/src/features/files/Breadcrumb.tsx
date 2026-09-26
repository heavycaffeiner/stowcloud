import { Fragment, useEffect, useId, useRef } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { useComponentState } from '../../hooks/use-component-state'
import { useI18n } from '../../hooks/use-i18n'
import { useStore } from '../../hooks/use-store'
import { ui } from '../../lib/store/ui.store'
import { Menu } from '../../lib/ui/Menu'
import { Icon } from '../../lib/ui/Icon'

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
  const [menu, setMenu] = useComponentState({ open: false, pos: { x: 0, y: 0 } })

  useEffect(() => {
    if (!menu.open) return
    let cancelled = false
    const menuElement = menuRef.current?.closest('mdui-menu') as (HTMLElement & { updateComplete?: Promise<boolean> }) | null
    void Promise.resolve(menuElement?.updateComplete).then(() => {
      if (!cancelled) menuRef.current?.querySelector<HTMLButtonElement>('button')?.focus()
    })
    return () => { cancelled = true }
  }, [menu.open])

  const shouldCollapse = crumbs.length > (compact ? 2 : 4)
  const collapsedCrumbs = shouldCollapse ? crumbs.slice(compact ? 0 : 1, compact ? -1 : -2) : []
  const visibleCrumbs = shouldCollapse
    ? compact ? crumbs.slice(-1) : [crumbs[0], ...crumbs.slice(-2)]
    : crumbs
  const collapsedLabel = collapsedCrumbs.map((crumb) => crumb.label).join(' / ')

  const openCollapsedMenu = (event: ReactMouseEvent<HTMLButtonElement>) => {
    event.stopPropagation()
    const rect = event.currentTarget.getBoundingClientRect()
    setMenu({ open: true, pos: { x: Math.min(rect.left, window.innerWidth - 336), y: rect.bottom + 4 } })
  }

  return (
    <nav className="sc-breadcrumb" aria-label={t('breadcrumb.path')}>
      <ol className="sc-breadcrumb-list">
        {visibleCrumbs.map((crumb, index) => (
          <Fragment key={crumb.path}>
            {shouldCollapse && index === (compact ? 0 : 1) && (
              <li className="sc-breadcrumb-item sc-breadcrumb-item-ellipsis">
                <button
                  type="button"
                  className="sc-breadcrumb-ellipsis-btn"
                  aria-haspopup="menu"
                  aria-expanded={menu.open}
                  aria-controls={menuId}
                  aria-label={`${t('browse.more')}: ${collapsedLabel}`}
                  title={collapsedLabel}
                  onClick={openCollapsedMenu}
                >
                  …
                </button>
                <span className="sc-breadcrumb-sep" aria-hidden="true">/</span>
              </li>
            )}
            <li className={`sc-breadcrumb-item sc-breadcrumb-item-${index === visibleCrumbs.length - 1 ? 'current' : index === 0 ? 'root' : index === visibleCrumbs.length - 2 ? 'parent' : 'ancestor'}`}>
              {index < visibleCrumbs.length - 1 ? (
                <>
                  <button
                    type="button"
                    className="sc-breadcrumb-link"
                    title={crumb.label}
                    onClick={() => onNavigate?.(crumb.path)}
                  >
                    <span className="sc-breadcrumb-label">{crumb.label}</span>
                  </button>
                  <span className="sc-breadcrumb-sep" aria-hidden="true">/</span>
                </>
              ) : (
                <span
                  className="sc-breadcrumb-current"
                  aria-current="page"
                  title={crumb.label}
                >
                  <span className="sc-breadcrumb-label">{crumb.label}</span>
                </span>
              )}
            </li>
          </Fragment>
        ))}
      </ol>

      {shouldCollapse && (
        <Menu compact={compact} open={menu.open} onClose={() => setMenu((state) => ({ ...state, open: false }))} x={menu.pos.x} y={menu.pos.y}>
          <div ref={menuRef} id={menuId} className="sc-browse-new-menu sc-breadcrumb-menu" role="menu" aria-label={t('breadcrumb.path')}>
            {collapsedCrumbs.map((c) => (
              <button
                key={c.path}
                type="button"
                role="menuitem"
                title={c.label}
                onClick={() => {
                  setMenu((state) => ({ ...state, open: false }))
                  onNavigate?.(c.path)
                }}
              >
                <Icon name="folder" size={18} />
                <span className="sc-breadcrumb-menu-label">{c.label}</span>
              </button>
            ))}
          </div>
        </Menu>
      )}
    </nav>
  )
}
