import { useEffect, useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { describeApiError } from '../../../lib/api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { setRootOrderMutation } from '../../../lib/query/account'
import { Icon } from '../../../ui/Icon'
import { IconButton } from '../../../ui/IconButton'
import { Modal } from '../../../ui/Modal'
import { VirtualList } from '../../../ui/VirtualList'
import * as styles from './NavigationDrawer.css'
import * as iconButtonStyles from '../../../ui/IconButton.css'
import { cx } from '../../../ui/cx'

export interface NavItem {
  readonly id: string
  readonly label: string
  readonly icon: string
  readonly href?: string
}

export interface RootItem {
  readonly id: string
  readonly label: string
  readonly icon?: string
  readonly brokenReason?: string
}

export interface NavigationDrawerProps {
  readonly navItems?: readonly NavItem[]
  readonly activeNav?: string
  readonly items?: readonly RootItem[]
  readonly active?: string
  readonly onSelect?: (item: RootItem) => void
  readonly onNavSelect?: (item: NavItem) => void
  readonly onSearch?: () => void
  readonly overlay?: boolean
  readonly onClose?: () => void
  readonly folderSelectorOnly?: boolean
  readonly collapsed?: boolean
  readonly onNew?: (trigger: HTMLElement) => void
  readonly userInitial?: string
}

export function NavigationDrawer({
  navItems = [],
  activeNav = 'files',
  items = [],
  active = '',
  onSelect,
  onNavSelect,
  onSearch,
  overlay = false,
  onClose,
  folderSelectorOnly = false,
  collapsed = false,
  onNew,
  userInitial = 'S'
}: NavigationDrawerProps) {
  const { t } = useI18n()
  const orderMutation = useMutation(setRootOrderMutation())
  const [orderState, setOrderState] = useState<{
    reordering: boolean
    pendingOrder: RootItem[] | null
    error: string | null
  }>({ reordering: false, pendingOrder: null, error: null })
  const { reordering, pendingOrder, error: orderError } = orderState
  const displayRoots = pendingOrder ?? items

  useEffect(() => {
    setOrderState((state) => (state.pendingOrder === null ? state : { ...state, pendingOrder: null }))
  }, [items, setOrderState])

  const destinations = useMemo(() => {
    const fallback: NavItem[] = [
      { id: 'files', label: t('nav.files'), icon: 'folder', href: '/b' },
      { id: 'recent', label: t('nav.recent'), icon: 'history', href: '/recent' },
      { id: 'trash', label: t('common.trash'), icon: 'delete', href: '/trash' },
      { id: 'links', label: t('nav.links'), icon: 'link', href: '/links' },
      { id: 'settings', label: t('common.settings'), icon: 'settings', href: '/settings' }
    ]
    return navItems.length > 0 ? navItems : fallback
  }, [navItems, t])

  const closeOverlay = (): void => {
    if (overlay) onClose?.()
  }

  const selectDestination = (item: NavItem): void => {
    onNavSelect?.(item)
    closeOverlay()
  }

  const moveRoot = (index: number, direction: -1 | 1): void => {
    const target = index + direction
    if (target < 0 || target >= displayRoots.length) return
    const next = [...displayRoots]
    ;[next[index], next[target]] = [next[target], next[index]]
    setOrderState({ reordering: true, pendingOrder: next, error: null })
    orderMutation.mutate(
      next.map((root) => root.id),
      {
        onError: (error) => {
          setOrderState({
            reordering: false,
            pendingOrder: null,
            error: describeApiError(error, t('nav.could_not_save_order'))
          })
        },
        onSettled: () => setOrderState((state) => ({ ...state, reordering: false }))
      }
    )
  }

  const fileNavItems = destinations.filter((item) => ['recent', 'trash', 'links'].includes(item.id))
  const settingNavItems = destinations.filter((item) => ['settings', 'admin'].includes(item.id))

  const drawerClass = cx(styles.root, overlay ? styles.overlay : collapsed && styles.collapsed)

  const content = (
    <>
      {overlay ? (
        <div className={styles.overlayHeader}>
          <button
            type="button"
            className={cx(styles.overlayClose, iconButtonStyles.root)}
            aria-label={t('common.close')}
            onClick={onClose}
          >
            <Icon name="close" />
          </button>
          <span className={styles.appName}>{folderSelectorOnly ? t('nav.browse_folders') : 'Stowcloud'}</span>
          <span className={styles.userAvatar} aria-hidden="true">
            {userInitial}
          </span>
        </div>
      ) : null}

      <div className={styles.body}>
        {onNew && !folderSelectorOnly ? (
          <div className={styles.newWrap}>
            <button
              type="button"
              className={cx(styles.newBtn, collapsed && styles.newBtnCollapsed)}
              aria-label={t('browse.new')}
              title={t('browse.new')}
              onClick={(event) => {
                onNew(event.currentTarget)
                closeOverlay()
              }}
            >
              <Icon name="add" size={20} />
              {!collapsed ? <span>{t('browse.new')}</span> : null}
            </button>
          </div>
        ) : null}

        {!folderSelectorOnly ? (
          <>
            {!collapsed && fileNavItems.length > 0 ? <div className={styles.divider} role="separator" /> : null}
            <ul className={styles.list}>
              {fileNavItems.map((item) => {
                const isActive = activeNav === item.id
                return (
                  <li key={item.id} className={styles.entry}>
                    <button
                      className={cx(styles.item, isActive && styles.itemActive)}
                      type="button"
                      aria-current={isActive ? 'page' : undefined}
                      aria-label={item.label}
                      title={collapsed ? item.label : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className={styles.itemIcon}>
                        <Icon name={item.icon} size={20} />
                      </span>
                      {!collapsed ? <span className={styles.itemLabel}>{item.label}</span> : null}
                    </button>
                  </li>
                )
              })}
            </ul>
          </>
        ) : null}

        {folderSelectorOnly || (!collapsed && displayRoots.length > 0) ? (
          <div className={styles.rootsSection}>
            <div className={styles.sectionHeader}>
              <span className={styles.sectionTitle}>{t('nav.folders')}</span>
              {displayRoots.length > 0 ? (
                <button
                  type="button"
                  className={styles.reorderToggle}
                  aria-pressed={reordering}
                  onClick={() => setOrderState((state) => ({ ...state, reordering: !state.reordering }))}
                >
                  {reordering ? t('nav.reorder_done') : t('nav.reorder')}
                </button>
              ) : null}
            </div>
            <ul className={styles.list} aria-label={t('nav.folder_selector')}>
              <li className={styles.entry}>
                <VirtualList
                  className={styles.sublist}
                  items={displayRoots}
                  itemKey={(root) => root.id}
                  estimateSize={reordering ? 48 : overlay ? 48 : 40}
                  renderItem={(root, index) =>
                    reordering ? (
                      <div className={cx(styles.subitem, styles.subitemReorder)}>
                        <span className={styles.itemIcon}>
                          <Icon name={root.icon ?? 'folder'} size={18} />
                        </span>
                        <span className={styles.subitemLabel}>{root.label}</span>
                        <span className={styles.reorderActions}>
                          <IconButton
                            label={t('nav.move_up', { name: root.label })}
                            disabled={index === 0}
                            onClick={() => moveRoot(index, -1)}
                          >
                            <span className={cx(styles.reorderChevron, styles.reorderChevronUp)}>
                              <Icon name="chevron_right" size={16} />
                            </span>
                          </IconButton>
                          <IconButton
                            label={t('nav.move_down', { name: root.label })}
                            disabled={index === displayRoots.length - 1}
                            onClick={() => moveRoot(index, 1)}
                          >
                            <span className={cx(styles.reorderChevron, styles.reorderChevronDown)}>
                              <Icon name="chevron_right" size={16} />
                            </span>
                          </IconButton>
                        </span>
                      </div>
                    ) : (
                      <button
                        type="button"
                        className={cx(styles.subitem, active === root.id && styles.subitemActive)}
                        aria-current={active === root.id ? 'location' : undefined}
                        onClick={() => {
                          onSelect?.(root)
                          closeOverlay()
                        }}
                      >
                        <span className={styles.itemIcon}>
                          <Icon name={root.icon ?? 'folder'} size={18} />
                        </span>
                        <span className={styles.subitemLabel}>{root.label}</span>
                      </button>
                    )
                  }
                />
                {orderError ? (
                  <p className={styles.reorderError} role="alert">
                    {orderError}
                  </p>
                ) : null}
              </li>
            </ul>
          </div>
        ) : null}

        {!folderSelectorOnly ? (
          <>
            <div className={styles.divider} role="separator" />
            {!collapsed ? <div className={styles.sectionTitle}>{t('common.settings')}</div> : null}
            <ul className={styles.list}>
              {settingNavItems.map((item) => {
                const isActive = activeNav === item.id
                return (
                  <li key={item.id} className={styles.entry}>
                    <button
                      type="button"
                      className={cx(styles.item, isActive && styles.itemActive)}
                      aria-current={isActive ? 'page' : undefined}
                      aria-label={item.label}
                      title={collapsed ? item.label : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className={styles.itemIcon}>
                        <Icon name={item.icon} size={20} />
                      </span>
                      {!collapsed ? (
                        <span className={styles.itemLabel}>{item.id === 'admin' ? t('nav.admin') : item.label}</span>
                      ) : null}
                    </button>
                  </li>
                )
              })}
            </ul>
          </>
        ) : null}
      </div>
    </>
  )

  if (!overlay) {
    return (
      <nav className={drawerClass} aria-label={t('common.main_menu')}>
        {content}
      </nav>
    )
  }

  return (
    <Modal
      open
      id={folderSelectorOnly ? 'sc-folder-selector' : 'sc-shell-drawer'}
      className={drawerClass}
      label={folderSelectorOnly ? t('nav.folder_selector') : t('common.main_menu')}
      onClose={() => onClose?.()}
    >
      {content}
    </Modal>
  )
}
