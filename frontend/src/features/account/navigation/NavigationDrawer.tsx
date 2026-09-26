import { useEffect, useMemo, useRef } from 'react'
import { useMutation } from '@tanstack/react-query'
import { describeApiError } from '../../../lib/api/error-text'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { useComponentState } from '../../../lib/store/use-component-state'
import { setRootOrderMutation } from '../../../lib/query/account'
import { Icon } from '../../../lib/ui/Icon'
import { IconButton } from '../../../lib/ui/IconButton'
import { VirtualList } from '../../../lib/ui/VirtualList'

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
  readonly onselect?: (item: RootItem) => void
  readonly onnavselect?: (item: NavItem) => void
  readonly onsearch?: () => void
  readonly overlay?: boolean
  readonly onclose?: () => void
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
  onselect,
  onnavselect,
  onsearch,
  overlay = false,
  onclose,
  folderSelectorOnly = false,
  collapsed = false,
  onNew,
  userInitial = 'S'
}: NavigationDrawerProps) {
  const { t } = useI18n()
  const dialogRef = useRef<HTMLDialogElement | null>(null)
  const orderMutation = useMutation(setRootOrderMutation())
  const [orderState, setOrderState] = useComponentState<{ reordering: boolean; pendingOrder: RootItem[] | null; error: string | null }>({ reordering: false, pendingOrder: null, error: null })
  const { reordering, pendingOrder, error: orderError } = orderState
  const displayRoots = pendingOrder ?? items

  useEffect(() => {
    setOrderState((state) => state.pendingOrder === null ? state : { ...state, pendingOrder: null })
  }, [items, setOrderState])

  useEffect(() => {
    if (!overlay || !dialogRef.current) return
    const dialog = dialogRef.current
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    try {
      if (!dialog.open) dialog.showModal()
    } catch {
      dialog.setAttribute('open', '')
    }
    return () => {
      if (dialog.open) dialog.close()
      else dialog.removeAttribute('open')
      trigger?.focus()
    }
  }, [overlay])

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
    if (overlay) onclose?.()
  }

  const selectDestination = (item: NavItem): void => {
    onnavselect?.(item)
    closeOverlay()
  }

  const moveRoot = (index: number, direction: -1 | 1): void => {
    const target = index + direction
    if (target < 0 || target >= displayRoots.length) return
    const next = [...displayRoots]
    ;[next[index], next[target]] = [next[target], next[index]]
    setOrderState({ reordering: true, pendingOrder: next, error: null })
    orderMutation.mutate(next.map((root) => root.id), {
      onError: (error) => {
        setOrderState({ reordering: false, pendingOrder: null, error: describeApiError(error, t('nav.could_not_save_order')) })
      },
      onSettled: () => setOrderState((state) => ({ ...state, reordering: false }))
    })
  }

  const fileNavItems = destinations.filter((item) => ['recent', 'trash', 'links'].includes(item.id))
  const settingNavItems = destinations.filter((item) => ['settings', 'admin'].includes(item.id))

  const drawerClass = overlay
    ? 'sc-nav-drawer sc-nav-drawer-overlay'
    : collapsed
      ? 'sc-nav-drawer sc-nav-drawer-collapsed'
      : 'sc-nav-drawer'

  const content = (
    <>
      {overlay ? (
        <div className="sc-nav-drawer-overlay-header">
          <button
            type="button"
            className="sc-nav-drawer-overlay-close sc-icon-button"
            aria-label={t('common.close')}
            onClick={onclose}
          >
            <Icon name="close" />
          </button>
          <span className="sc-nav-drawer-app-name">{folderSelectorOnly ? t('nav.browse_folders') : 'Stowcloud'}</span>
          <span className="sc-nav-drawer-user-avatar" aria-hidden="true">{userInitial}</span>
        </div>
      ) : null}

      <div className="sc-nav-drawer-body">
        {onNew && !folderSelectorOnly ? (
          <div className="sc-nav-drawer-new-wrap">
            <button
              type="button"
              className={collapsed ? 'sc-nav-drawer-new-btn sc-nav-drawer-new-btn-collapsed' : 'sc-nav-drawer-new-btn'}
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
            {!collapsed && fileNavItems.length > 0 ? <div className="sc-nav-drawer-divider" role="separator" /> : null}
            <ul className="sc-nav-drawer-list">
              {fileNavItems.map((item) => {
                const isActive = activeNav === item.id
                return (
                  <li key={item.id} className="sc-nav-drawer-entry">
                    <button
                      className={isActive ? 'sc-nav-drawer-item sc-nav-drawer-item-active' : 'sc-nav-drawer-item'}
                      type="button"
                      aria-current={isActive ? 'page' : undefined}
                      aria-label={item.label}
                      title={collapsed ? item.label : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className="sc-nav-drawer-item-icon"><Icon name={item.icon} size={20} /></span>
                      {!collapsed ? <span className="sc-nav-drawer-item-label">{item.label}</span> : null}
                    </button>
                  </li>
                )
              })}
            </ul>
          </>
        ) : null}

        {(folderSelectorOnly || (!collapsed && displayRoots.length > 0)) ? (
          <div className="sc-nav-drawer-roots-section">
            <div className="sc-nav-drawer-section-header">
              <span className="sc-nav-drawer-section-title">{t('nav.folders')}</span>
              {displayRoots.length > 0 ? (
                <button
                  type="button"
                  className="sc-nav-drawer-reorder-toggle"
                  aria-pressed={reordering}
                  onClick={() => setOrderState((state) => ({ ...state, reordering: !state.reordering }))}
                >
                  {reordering ? t('nav.reorder_done') : t('nav.reorder')}
                </button>
              ) : null}
            </div>
            <ul className="sc-nav-drawer-list" aria-label={t('nav.folder_selector')}>
              <li className="sc-nav-drawer-entry">
                <VirtualList
                  className={overlay ? 'sc-nav-drawer-sublist sc-nav-drawer-sublist-overlay' : 'sc-nav-drawer-sublist'}
                  items={displayRoots}
                  itemKey={(root) => root.id}
                  estimateSize={reordering ? 48 : overlay ? 48 : 40}
                  renderItem={(root, index) => reordering ? (
                    <div className="sc-nav-drawer-subitem sc-nav-drawer-subitem-reorder">
                      <span className="sc-nav-drawer-item-icon"><Icon name={root.icon ?? 'folder'} size={18} /></span>
                      <span className="sc-nav-drawer-subitem-label sc-filename">{root.label}</span>
                      <span className="sc-nav-drawer-reorder-actions">
                        <IconButton
                          label={t('nav.move_up', { name: root.label })}
                          disabled={index === 0}
                          onClick={() => moveRoot(index, -1)}
                        >
                          <span className="sc-nav-drawer-reorder-chevron sc-nav-drawer-reorder-chevron-up">
                            <Icon name="chevron_right" size={16} />
                          </span>
                        </IconButton>
                        <IconButton
                          label={t('nav.move_down', { name: root.label })}
                          disabled={index === displayRoots.length - 1}
                          onClick={() => moveRoot(index, 1)}
                        >
                          <span className="sc-nav-drawer-reorder-chevron sc-nav-drawer-reorder-chevron-down">
                            <Icon name="chevron_right" size={16} />
                          </span>
                        </IconButton>
                      </span>
                    </div>
                  ) : (
                    <button
                      type="button"
                      className={active === root.id ? 'sc-nav-drawer-subitem sc-nav-drawer-subitem-active' : 'sc-nav-drawer-subitem'}
                      aria-current={active === root.id ? 'location' : undefined}
                      onClick={() => {
                        onselect?.(root)
                        closeOverlay()
                      }}
                    >
                      <span className="sc-nav-drawer-item-icon"><Icon name={root.icon ?? 'folder'} size={18} /></span>
                      <span className="sc-nav-drawer-subitem-label sc-filename">{root.label}</span>
                    </button>
                  )}
                />
                {orderError ? <p className="sc-nav-drawer-reorder-error" role="alert">{orderError}</p> : null}
              </li>
            </ul>
          </div>
        ) : null}

        {!folderSelectorOnly ? (
          <>
            <div className="sc-nav-drawer-divider" role="separator" />
            {!collapsed ? <div className="sc-nav-drawer-section-title">{t('common.settings')}</div> : null}
            <ul className="sc-nav-drawer-list">
              {settingNavItems.map((item) => {
                const isActive = activeNav === item.id
                return (
                  <li key={item.id} className="sc-nav-drawer-entry">
                    <button
                      type="button"
                      className={isActive ? 'sc-nav-drawer-item sc-nav-drawer-item-active' : 'sc-nav-drawer-item'}
                      aria-current={isActive ? 'page' : undefined}
                      aria-label={item.label}
                      title={collapsed ? item.label : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className="sc-nav-drawer-item-icon"><Icon name={item.icon} size={20} /></span>
                      {!collapsed ? <span className="sc-nav-drawer-item-label">{item.id === 'admin' ? t('nav.admin') : item.label}</span> : null}
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
    return <nav className={drawerClass} aria-label={t('common.main_menu')}>{content}</nav>
  }

  return (
    <dialog
      id={folderSelectorOnly ? 'sc-folder-selector' : 'sc-shell-drawer'}
      ref={dialogRef}
      className={drawerClass}
      aria-label={folderSelectorOnly ? t('nav.folder_selector') : t('common.main_menu')}
      onClick={(event) => {
        if (event.target === event.currentTarget) onclose?.()
      }}
      onCancel={(event) => {
        event.preventDefault()
        onclose?.()
      }}
      onClose={() => { if (!dialogRef.current?.open) onclose?.() }}
    >
      {content}
    </dialog>
  )
}
