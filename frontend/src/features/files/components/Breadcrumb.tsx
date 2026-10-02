import { Fragment } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { useCompact } from '@/hooks/use-compact'
import { cx, StowIconButton, StowMenuButton, StowMenuItem } from '@/shared/ui'
import * as styles from './Breadcrumb.css'

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

  const shouldCollapse = crumbs.length > (compact ? 2 : 4)
  const collapsedCrumbs = shouldCollapse ? crumbs.slice(compact ? 0 : 1, compact ? -1 : -2) : []
  const visibleCrumbs = shouldCollapse ? (compact ? crumbs.slice(-1) : [crumbs[0], ...crumbs.slice(-2)]) : crumbs
  const collapsedLabel = collapsedCrumbs.map((crumb) => crumb.label).join(' / ')

  return (
    <nav className={styles.root} aria-label={t('breadcrumb.path')}>
      <ol className={styles.list}>
        {visibleCrumbs.map((crumb, index) => (
          <Fragment key={crumb.path}>
            {shouldCollapse && index === (compact ? 0 : 1) && (
              <li className={cx(styles.item, styles.itemEllipsis)}>
                <StowMenuButton
                  label={t('breadcrumb.path')}
                  menu={(close) =>
                    collapsedCrumbs.map((crumb) => (
                      <StowMenuItem
                        key={crumb.path}
                        icon="folder"
                        title={crumb.label}
                        onClick={() => {
                          close()
                          onNavigate?.(crumb.path)
                        }}
                      >
                        {crumb.label}
                      </StowMenuItem>
                    ))
                  }
                >
                  <StowIconButton
                    label={`${t('browse.more')}: ${collapsedLabel}`}
                    icon="more-horiz"
                    onClick={(event) => event.stopPropagation()}
                  />
                </StowMenuButton>
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
    </nav>
  )
}
