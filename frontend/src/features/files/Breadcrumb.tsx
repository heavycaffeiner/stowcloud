import { Fragment, useId, useState } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { useCompact } from '../../ui/use-compact'
import { Menu, MenuItem, MenuList } from '../../ui/Menu'
import { Icon } from '../../ui/Icon'
import * as styles from './Breadcrumb.css'
import { cx } from '../../ui/cx'

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
  const compact = useCompact()
  const menuId = useId()
  const [menu, setMenu] = useState({ open: false, pos: { x: 0, y: 0 } })

  const shouldCollapse = crumbs.length > (compact ? 2 : 4)
  const collapsedCrumbs = shouldCollapse ? crumbs.slice(compact ? 0 : 1, compact ? -1 : -2) : []
  const visibleCrumbs = shouldCollapse ? (compact ? crumbs.slice(-1) : [crumbs[0], ...crumbs.slice(-2)]) : crumbs
  const collapsedLabel = collapsedCrumbs.map((crumb) => crumb.label).join(' / ')

  const openCollapsedMenu = (event: ReactMouseEvent<HTMLButtonElement>) => {
    event.stopPropagation()
    const rect = event.currentTarget.getBoundingClientRect()
    setMenu({ open: true, pos: { x: Math.min(rect.left, window.innerWidth - 336), y: rect.bottom + 4 } })
  }

  return (
    <nav className={styles.root} aria-label={t('breadcrumb.path')}>
      <ol className={styles.list}>
        {visibleCrumbs.map((crumb, index) => (
          <Fragment key={crumb.path}>
            {shouldCollapse && index === (compact ? 0 : 1) && (
              <li className={cx(styles.item, styles.itemEllipsis)}>
                <button
                  type="button"
                  className={styles.ellipsisBtn}
                  aria-haspopup="menu"
                  aria-expanded={menu.open}
                  aria-controls={menuId}
                  aria-label={`${t('browse.more')}: ${collapsedLabel}`}
                  title={collapsedLabel}
                  onClick={openCollapsedMenu}
                >
                  …
                </button>
                <span className={styles.sep} aria-hidden="true">
                  /
                </span>
              </li>
            )}
            <li
              className={cx(
                styles.item,
                index === visibleCrumbs.length - 1
                  ? styles.itemCurrent
                  : index === 0
                    ? styles.itemRoot
                    : index === visibleCrumbs.length - 2
                      ? styles.itemParent
                      : styles.itemAncestor
              )}
            >
              {index < visibleCrumbs.length - 1 ? (
                <>
                  <button
                    type="button"
                    className={styles.link}
                    title={crumb.label}
                    onClick={() => onNavigate?.(crumb.path)}
                  >
                    <span className={styles.label}>{crumb.label}</span>
                  </button>
                  <span className={styles.sep} aria-hidden="true">
                    /
                  </span>
                </>
              ) : (
                <span className={styles.current} aria-current="page" title={crumb.label}>
                  <span className={styles.label}>{crumb.label}</span>
                </span>
              )}
            </li>
          </Fragment>
        ))}
      </ol>

      {shouldCollapse && (
        <Menu
          compact={compact}
          open={menu.open}
          onClose={() => setMenu((state) => ({ ...state, open: false }))}
          x={menu.pos.x}
          y={menu.pos.y}
        >
          <MenuList id={menuId} className={styles.menu} label={t('breadcrumb.path')}>
            {collapsedCrumbs.map((c) => (
              <MenuItem
                key={c.path}
                className={styles.menuItem}
                title={c.label}
                onClick={() => {
                  setMenu((state) => ({ ...state, open: false }))
                  onNavigate?.(c.path)
                }}
              >
                <Icon name="folder" size={18} className={styles.menuIcon} />
                <span className={styles.menuLabel}>{c.label}</span>
              </MenuItem>
            ))}
          </MenuList>
        </Menu>
      )}
    </nav>
  )
}
