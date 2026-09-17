import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { describeApiError } from '../api/error-text'
import { useI18n } from '../i18n/use-i18n'
import { setRootOrderMutation } from '../query/account'
import { Icon } from './Icon'
import { IconButton } from './IconButton'
import { VirtualList } from './VirtualList'

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
  readonly onNew?: () => void
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
  const [reordering, setReordering] = useState(false)
  const [pendingOrder, setPendingOrder] = useState<RootItem[] | null>(null)
  const [orderError, setOrderError] = useState<string | null>(null)
  const displayRoots = pendingOrder ?? items

  useEffect(() => {
    setPendingOrder(null)
  }, [items])

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
      { id: 'files', label: t('browse.home'), icon: 'home', href: '/b' },
      { id: 'folders', label: t('nav.shared_folders'), icon: 'folder_shared', href: '/b' },
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
    setPendingOrder(next)
    setOrderError(null)
    orderMutation.mutate(next.map((root) => root.id), {
      onError: (error) => {
        setPendingOrder(null)
        setOrderError(describeApiError(error, t('nav.could_not_save_order')))
      }
    })
  }

  const fileNavItems = destinations.filter((item) => ['files', 'folders', 'recent', 'trash', 'links'].includes(item.id))
  const settingNavItems = destinations.filter((item) => ['settings', 'admin'].includes(item.id))

  const drawerClass = overlay
    ? 'sc-nav-drawer sc-nav-drawer--overlay'
    : collapsed
      ? 'sc-nav-drawer sc-nav-drawer--collapsed'
      : 'sc-nav-drawer'

  const content = (
    <>
      {overlay ? (
        <div className="sc-nav-drawer__overlay-header">
          <button
            type="button"
            className="sc-nav-drawer__overlay-close sc-icon-button"
            aria-label={t('common.close')}
            onClick={onclose}
          >
            <Icon name="close" />
          </button>
          <span className="sc-nav-drawer__app-name">{folderSelectorOnly ? t('nav.browse_folders') : 'Stowcloud'}</span>
          <span className="sc-nav-drawer__user-avatar" aria-hidden="true">{userInitial}</span>
        </div>
      ) : null}

      <div className="sc-nav-drawer__body">
        {onNew && !folderSelectorOnly ? (
          <div className="sc-nav-drawer__new-wrap">
            <button
              type="button"
              className={collapsed ? 'sc-nav-drawer__new-btn sc-nav-drawer__new-btn--collapsed' : 'sc-nav-drawer__new-btn'}
              aria-label={t('browse.new')}
              title={t('browse.new')}
              onClick={() => {
                onNew()
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
            {!collapsed ? <div className="sc-nav-drawer__section-title">{t('nav.files')}</div> : null}
            <ul className="sc-nav-drawer__list">
              {fileNavItems.map((item) => {
                const isActive = activeNav === item.id
                return (
                  <li key={item.id} className="sc-nav-drawer__entry">
                    <button
                      className={isActive ? 'sc-nav-drawer__item sc-nav-drawer__item--active' : 'sc-nav-drawer__item'}
                      type="button"
                      aria-current={isActive ? 'page' : undefined}
                      title={collapsed ? item.label : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className="sc-nav-drawer__item-icon"><Icon name={item.icon} size={20} /></span>
                      {!collapsed ? <span className="sc-nav-drawer__item-label">{item.label}</span> : null}
                    </button>
                  </li>
                )
              })}
            </ul>
          </>
        ) : null}

        {(folderSelectorOnly || (!collapsed && displayRoots.length > 0)) ? (
          <div className="sc-nav-drawer__roots-section">
            <div className="sc-nav-drawer__section-header">
              <span className="sc-nav-drawer__section-title">{t('nav.folders')}</span>
              {displayRoots.length > 0 ? (
                <button
                  type="button"
                  className="sc-nav-drawer__reorder-toggle"
                  aria-pressed={reordering}
                  onClick={() => setReordering((value) => !value)}
                >
                  {reordering ? t('nav.reorder_done') : t('nav.reorder')}
                </button>
              ) : null}
            </div>
            <ul className="sc-nav-drawer__list" aria-label={t('nav.folder_selector')}>
              <li className="sc-nav-drawer__entry">
                <VirtualList
                  className={overlay ? 'sc-nav-drawer__sublist sc-nav-drawer__sublist--overlay' : 'sc-nav-drawer__sublist'}
                  items={displayRoots}
                  itemKey={(root) => root.id}
                  estimateSize={overlay ? 48 : 40}
                  renderItem={(root, index) => reordering ? (
                    <div className="sc-nav-drawer__subitem sc-nav-drawer__subitem--reorder">
                      <span className="sc-nav-drawer__item-icon"><Icon name={root.icon ?? 'folder'} size={18} /></span>
                      <span className="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
                      <span className="sc-nav-drawer__reorder-actions">
                        <IconButton
                          label={t('nav.move_up', { name: root.label })}
                          disabled={index === 0}
                          onClick={() => moveRoot(index, -1)}
                        >
                          <span className="sc-nav-drawer__reorder-chevron sc-nav-drawer__reorder-chevron--up">
                            <Icon name="chevron_right" size={16} />
                          </span>
                        </IconButton>
                        <IconButton
                          label={t('nav.move_down', { name: root.label })}
                          disabled={index === displayRoots.length - 1}
                          onClick={() => moveRoot(index, 1)}
                        >
                          <span className="sc-nav-drawer__reorder-chevron sc-nav-drawer__reorder-chevron--down">
                            <Icon name="chevron_right" size={16} />
                          </span>
                        </IconButton>
                      </span>
                    </div>
                  ) : (
                    <button
                      type="button"
                      className={active === root.id ? 'sc-nav-drawer__subitem sc-nav-drawer__subitem--active' : 'sc-nav-drawer__subitem'}
                      aria-current={active === root.id ? 'page' : undefined}
                      onClick={() => {
                        onselect?.(root)
                        closeOverlay()
                      }}
                    >
                      <span className="sc-nav-drawer__item-icon"><Icon name={root.icon ?? 'folder'} size={18} /></span>
                      <span className="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
                    </button>
                  )}
                />
                {orderError ? <p className="sc-nav-drawer__reorder-error" role="alert">{orderError}</p> : null}
              </li>
            </ul>
          </div>
        ) : null}

        {!folderSelectorOnly ? (
          <>
            <div className="sc-nav-drawer__divider" role="separator" />
            {!collapsed ? <div className="sc-nav-drawer__section-title">{t('common.settings')}</div> : null}
            <ul className="sc-nav-drawer__list">
              {settingNavItems.map((item) => {
                const isActive = activeNav === item.id
                return (
                  <li key={item.id} className="sc-nav-drawer__entry">
                    <button
                      type="button"
                      className={isActive ? 'sc-nav-drawer__item sc-nav-drawer__item--active' : 'sc-nav-drawer__item'}
                      aria-current={isActive ? 'page' : undefined}
                      title={collapsed ? item.label : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className="sc-nav-drawer__item-icon"><Icon name={item.icon} size={20} /></span>
                      {!collapsed ? <span className="sc-nav-drawer__item-label">{item.id === 'admin' ? t('nav.admin') : item.label}</span> : null}
                    </button>
                  </li>
                )
              })}
            </ul>
          </>
        ) : null}

        {!folderSelectorOnly ? (
          <div className="sc-nav-drawer__storage-wrap">
            {collapsed ? (
              <div className="sc-nav-drawer__storage-collapsed" title={`${t('nav.storage')}: 68% (102.4 GB / 150 GB)`}>
                <span className="sc-nav-drawer__storage-collapsed-icon">
                  <Icon name="cloud" size={22} />
                </span>
                <span className="sc-nav-drawer__storage-pct-collapsed">68%</span>
              </div>
            ) : (
              <div className="sc-nav-drawer__storage-card">
                <div className="sc-nav-drawer__storage-header">
                  <span className="sc-nav-drawer__storage-title">
                    <Icon name="cloud" size={18} />
                    <span>{t('nav.storage')}</span>
                  </span>
                  <span className="sc-nav-drawer__storage-pct">68%</span>
                </div>
                <div className="sc-nav-drawer__storage-bar">
                  <div className="sc-nav-drawer__storage-progress" style={{ width: '68%' }} />
                </div>
                <div className="sc-nav-drawer__storage-desc">
                  {t('nav.storage_used_desc', { used: '102.4 GB', total: '150 GB' })}
                </div>
              </div>
            )}
          </div>
        ) : null}
      </div>
    </>
  )

  if (!overlay) {
    return <nav className={drawerClass} aria-label={t('common.main_menu')}>{content}</nav>
  }

  return (
    <dialog
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
