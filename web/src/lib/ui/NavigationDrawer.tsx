import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { describeApiError } from '../api/error-text'
import { useI18n } from '../i18n/use-i18n'
import { setRootOrderMutation } from '../query/account'
import { Icon } from './Icon'
import { IconButton } from './IconButton'

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
  folderSelectorOnly = false
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
      { id: 'files', label: t('nav.files'), icon: 'home', href: '/b' },
      { id: 'recent', label: t('nav.recent'), icon: 'history', href: '/recent' },
      { id: 'trash', label: t('common.trash'), icon: 'delete_sweep', href: '/trash' },
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

  const content = (
    <>
      {!overlay ? (
        <div className="sc-nav-drawer__brand">
          <span className="sc-nav-drawer__app-name">Stowcloud</span>
          {onsearch ? (
            <IconButton label={t('common.search')} onClick={onsearch}><Icon name="search" /></IconButton>
          ) : null}
        </div>
      ) : null}
      <div className="sc-nav-drawer__body">
        {!folderSelectorOnly ? (
          <>
            <div className="sc-nav-drawer__section-title">{t('nav.files')}</div>
            <ul className="sc-nav-drawer__list">
              {destinations
                .filter((item) => item.id === 'files')
                .map((item) => (
                  <li key={item.id} className="sc-nav-drawer__entry">
                    <button
                      className={activeNav === item.id ? 'sc-nav-drawer__item sc-nav-drawer__item--active' : 'sc-nav-drawer__item'}
                      type="button"
                      aria-current={activeNav === item.id ? 'page' : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className="sc-nav-drawer__item-icon"><Icon name={item.icon} /></span>
                      <span className="sc-nav-drawer__item-label">{item.label}</span>
                    </button>
                  </li>
                ))}
            </ul>
          </>
        ) : null}

        {!folderSelectorOnly ? <div className="sc-nav-drawer__section-title">{t('nav.folders')}</div> : null}
        <ul className="sc-nav-drawer__list" aria-label={t('nav.folder_selector')}>
          <li className="sc-nav-drawer__entry">
            {displayRoots.length > 0 ? (
              <ul className={overlay ? 'sc-nav-drawer__sublist sc-nav-drawer__sublist--overlay' : 'sc-nav-drawer__sublist'}>
                <li className="sc-nav-drawer__reorder-row">
                  <button
                    type="button"
                    className="sc-nav-drawer__reorder-toggle"
                    aria-pressed={reordering}
                    onClick={() => setReordering((value) => !value)}
                  >
                    {reordering ? t('nav.reorder_done') : t('nav.reorder')}
                  </button>
                </li>
                {displayRoots.map((root, index) => (
                  <li key={root.id}>
                    {reordering ? (
                      <div className="sc-nav-drawer__subitem sc-nav-drawer__subitem--reorder">
                        <span className="sc-nav-drawer__item-icon"><Icon name={root.icon ?? 'folder'} /></span>
                        <span className="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
                        <span className="sc-nav-drawer__reorder-actions">
                          <IconButton
                            label={t('nav.move_up', { name: root.label })}
                            disabled={index === 0}
                            onClick={() => moveRoot(index, -1)}
                          ><Icon name="chevron_left" /></IconButton>
                          <IconButton
                            label={t('nav.move_down', { name: root.label })}
                            disabled={index === displayRoots.length - 1}
                            onClick={() => moveRoot(index, 1)}
                          ><Icon name="chevron_right" /></IconButton>
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
                        <span className="sc-nav-drawer__item-icon"><Icon name={root.icon ?? 'folder'} /></span>
                        <span className="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
                      </button>
                    )}
                  </li>
                ))}
                {orderError ? <li className="sc-nav-drawer__reorder-error" role="alert">{orderError}</li> : null}
              </ul>
            ) : null}
          </li>
        </ul>

        {!folderSelectorOnly ? (
          <>
            <ul className="sc-nav-drawer__list">
              {destinations
                .filter((item) => ['recent', 'trash', 'links'].includes(item.id))
                .map((item) => (
                  <li key={item.id} className="sc-nav-drawer__entry">
                    <button
                      type="button"
                      className={activeNav === item.id ? 'sc-nav-drawer__item sc-nav-drawer__item--active' : 'sc-nav-drawer__item'}
                      aria-current={activeNav === item.id ? 'page' : undefined}
                      onClick={() => selectDestination(item)}
                    >
                      <span className="sc-nav-drawer__item-icon"><Icon name={item.icon} /></span>
                      <span className="sc-nav-drawer__item-label">{item.label}</span>
                    </button>
                  </li>
                ))}
            </ul>
            <div className="sc-nav-drawer__section-title">{t('common.settings')}</div>
            <ul className="sc-nav-drawer__list">
              {destinations
                .filter((item) => ['admin', 'settings'].includes(item.id))
                .map((item) => (
                  <li key={item.id} className="sc-nav-drawer__entry">
                    <button type="button" className={activeNav === item.id ? 'sc-nav-drawer__item sc-nav-drawer__item--active' : 'sc-nav-drawer__item'} aria-current={activeNav === item.id ? 'page' : undefined} onClick={() => selectDestination(item)}>
                      <span className="sc-nav-drawer__item-icon"><Icon name={item.icon} /></span>
                      <span className="sc-nav-drawer__item-label">{item.id === 'admin' ? t('nav.admin') : item.label}</span>
                    </button>
                  </li>
                ))}

            </ul>
          </>
        ) : null}
      </div>
    </>
  )

  if (!overlay) {
    return <nav className="sc-nav-drawer" aria-label={t('common.main_menu')}>{content}</nav>
  }

  return (
    <dialog
      ref={dialogRef}
      className="sc-nav-drawer sc-nav-drawer--overlay"
      aria-label={folderSelectorOnly ? t('nav.folder_selector') : t('common.main_menu')}
      onClick={(event) => {
        if (event.target === event.currentTarget) onclose?.()
      }}
      onCancel={(event) => {
        event.preventDefault()
        onclose?.()
      }}
      onClose={() => onclose?.()}
    >
      <div className="sc-nav-drawer__overlay-header">
        <span className="sc-nav-drawer__app-name">{folderSelectorOnly ? t('nav.folders') : 'Stowcloud'}</span>
        <IconButton label={t('common.close')} onClick={onclose}><Icon name="close" /></IconButton>
      </div>
      {content}
    </dialog>
  )
}
