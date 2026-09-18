import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { swReady } from '../lib/crypto/download-sw'
import { useI18n } from '../lib/i18n/use-i18n'
import { startLiveInvalidation } from '../lib/query/live'
import { isUnauthenticated, logoutMutation, screenOf, sessionQuery, setupRequiredQuery } from '../lib/query/session'
import { openSearch as openSearchStore, search, searchTarget } from '../lib/store/search.store'
import { COMPACT_MAX_PX, ui } from '../lib/store/ui.store'
import { useStore } from '../lib/store/use-store'
import { JobTray } from '../lib/ui/JobTray'
import { NavigationBar, type NavigationBarItem } from '../lib/ui/NavigationBar'
import { NavigationDrawer, type NavItem, type RootItem } from '../lib/ui/NavigationDrawer'
import { SearchSheet } from '../lib/ui/SearchSheet'
import { Snackbar } from '../lib/ui/Snackbar'
import { Icon } from '../lib/ui/Icon'
import { UploadTray } from '../lib/ui/UploadTray'
import './shell.css'

/** Where the create menu should open, in viewport coordinates. `align` says
 *  which edge `x` refers to: a left-hand trigger anchors its left edge, a
 *  right-hand one its right. */
export interface NewActionAnchor {
  readonly x: number
  readonly y: number
  readonly align: 'start' | 'end'
}

function isBrowsePathname(pathname: string): boolean {
  return pathname === '/b' || pathname.startsWith('/b/')
}

function browsePathFromUrl(pathname: string): string | null {
  if (!isBrowsePathname(pathname)) return null
  try {
    return decodeURI(pathname.slice('/b'.length) || '/')
  } catch {
    return pathname.slice('/b'.length) || '/'
  }
}

export function AppShell() {
  const { t } = useI18n()
  const location = useLocation()
  const navigate = useNavigate()
  const compact = useStore(ui, (state) => state.compact)
  const sidebarCollapsed = useStore(ui, (state) => state.sidebarCollapsed)
  const searchOpen = useStore(search, (state) => state.open)
  const searchScope = useStore(search, (state) => state.scope)
  const session = useQuery(sessionQuery())
  const definitiveFailure = session.isError && isUnauthenticated(session.error)
  const setup = useQuery(setupRequiredQuery(definitiveFailure))
  const screen = screenOf({
    hasSession: session.data !== undefined && !definitiveFailure,
    sessionFailed: definitiveFailure,
    setupPending: definitiveFailure && setup.isPending,
    setupRequired: setup.data === true
  })
  const [lastBrowsePath, setLastBrowsePath] = useState<string | null>(null)
  const [mobileDrawerOpen, setMobileDrawerOpen] = useState(false)
  const [folderSelectorOpen, setFolderSelectorOpen] = useState(false)
  const [accountMenuOpen, setAccountMenuOpen] = useState(false)
  const trayStackRef = useRef<HTMLDivElement | null>(null)
  const accountMenuRef = useRef<HTMLDivElement | null>(null)
  const logout = useMutation(logoutMutation())

  // A menu that outlives the click that opened it traps the pointer, so a
  // press anywhere else closes it.
  useEffect(() => {
    if (!accountMenuOpen) return
    const onPointerDown = (event: PointerEvent): void => {
      if (!accountMenuRef.current?.contains(event.target as Node)) setAccountMenuOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') setAccountMenuOpen(false)
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [accountMenuOpen])

  const signOut = (): void => {
    setAccountMenuOpen(false)
    logout.mutate(undefined, {
      onSettled: (result) => {
        // Single sign-on ends the provider's session through its own URL; a
        // local session just returns to the sign-in screen.
        if (result?.end_session_url) window.location.assign(result.end_session_url)
        else void navigate('/login', { replace: true })
      }
    })
  }

  useEffect(() => {
    const resize = (): void => ui.setCompact(window.innerWidth < COMPACT_MAX_PX)
    window.addEventListener('resize', resize)
    resize()
    return () => window.removeEventListener('resize', resize)
  }, [])

  useEffect(() => {
    setMobileDrawerOpen(false)
    setFolderSelectorOpen(false)
  }, [compact, location.pathname, location.search, screen])

  useEffect(() => {
    const browsePath = browsePathFromUrl(location.pathname)
    if (browsePath !== null) setLastBrowsePath(`${browsePath}${location.search}`)
  }, [location.pathname, location.search])

  useEffect(() => {
    if (screen !== 'browser') return
    const stop = startLiveInvalidation()
    void swReady()
    return stop
  }, [screen])

  const roots = useMemo<RootItem[]>(() => (session.data?.roots ?? []).map((root) => ({ id: root.label, label: root.label, icon: 'folder', brokenReason: root.broken_reason })), [session.data?.roots])
  const rootItems = roots
  const browsePath = browsePathFromUrl(location.pathname)
  const browseTarget = (): string => {
    const current = browsePath
    const rootLabels = new Set(rootItems.map((item) => item.id))
    const valid = (path: string | null): path is string => path === '/' || (!!path && rootLabels.has(path.split('/').filter(Boolean)[0] ?? ''))
    if (valid(current)) return current
    const previous = lastBrowsePath?.split('?')[0] ?? null
    if (valid(previous)) return previous
    return rootItems.length > 0 ? `/${rootItems[0].id}` : '/'
  }

  const browseHref = (path: string): string => path === '/' ? '/b' : `/b${path}`
  const browseScope = browsePath && browsePath !== '/' ? browsePath : lastBrowsePath?.split('?')[0] && lastBrowsePath?.split('?')[0] !== '/' ? lastBrowsePath.split('?')[0] : ''
  const activeRoot = browseScope.split('/').filter(Boolean)[0] ?? ''

  const openSearch = (): void => {
    setMobileDrawerOpen(false)
    setFolderSelectorOpen(false)
    const target = searchTarget(browseScope)
    if (target) void navigate(target)
    else openSearchStore(browseScope)
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (screen !== 'browser') return
      const isInput = event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || (event.target instanceof HTMLElement && event.target.isContentEditable)
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        openSearch()
      } else if (!isInput && event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey) {
        event.preventDefault()
        openSearch()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [location.pathname, navigate, screen, browseScope])

  useEffect(() => {
    const element = trayStackRef.current
    if (!element) return
    const publish = (): void => {
      const top = element.offsetHeight > 0 ? `${window.innerHeight - element.getBoundingClientRect().top + 12}px` : '0px'
      document.documentElement.style.setProperty('--sc-tray-stack-top', top)
    }
    const observer = new ResizeObserver(publish)
    observer.observe(element)
    publish()
    return () => {
      observer.disconnect()
      document.documentElement.style.removeProperty('--sc-tray-stack-top')
    }
  }, [screen])

  const navItems = useMemo<NavItem[]>(() => {
    const items: NavItem[] = [
      { id: 'files', label: t('nav.files'), icon: 'folder', href: browseHref(browseTarget()) },
      { id: 'recent', label: t('nav.recent'), icon: 'history', href: '/recent' },
      { id: 'trash', label: t('common.trash'), icon: 'delete', href: '/trash' },
      { id: 'links', label: t('nav.links'), icon: 'link', href: '/links' },
      { id: 'settings', label: t('common.settings'), icon: 'settings', href: '/settings' }
    ]
    if (session.data?.user.is_admin) items.push({ id: 'admin', label: t('nav.admin'), icon: 'admin_panel_settings', href: '/admin' })
    return items
  }, [browseTarget, session.data?.user.is_admin, t, location.pathname, location.search, lastBrowsePath])

  const activeNav = location.pathname.startsWith('/settings') ? 'settings' : location.pathname.startsWith('/admin') ? 'admin' : location.pathname.startsWith('/recent') ? 'recent' : location.pathname.startsWith('/trash') ? 'trash' : location.pathname.startsWith('/links') ? 'links' : 'files'

  const compactItems = useMemo<NavigationBarItem[]>(() => [
    { id: 'files', label: t('nav.files'), icon: 'folder', href: browseHref(browseTarget()) },
    { id: 'recent', label: t('nav.recent'), icon: 'history', href: '/recent' },
    { id: 'links', label: t('nav.links'), icon: 'link', href: '/links' },
    { id: 'more', label: t('nav.more'), icon: 'menu', popup: 'dialog', expanded: mobileDrawerOpen, controls: 'sc-shell-drawer' }
  ], [mobileDrawerOpen, browseTarget, t, location.pathname, lastBrowsePath])

  const compactActive = ['trash', 'settings', 'admin'].includes(activeNav) ? 'more' : activeNav

  const navigateTo = (id: string, href?: string): void => {
    if (id === 'files') {
      setMobileDrawerOpen(false)
      setFolderSelectorOpen(false)
      void navigate(browseHref(browseTarget()))
      return
    }
    if (id === 'more') {
      setFolderSelectorOpen(false)
      setMobileDrawerOpen((open) => !open)
      return
    }
    const target = href ?? navItems.find((item) => item.id === id)?.href
    if (target) {
      setMobileDrawerOpen(false)
      setFolderSelectorOpen(false)
      void navigate(target)
    }
  }

  // The browse page owns the create menu, so the sidebar hands over where its
  // button sits. Without a rect the menu opened at a fixed corner, detached
  // from the control that summoned it.
  const triggerNewAction = (trigger?: HTMLElement): void => {
    const rect = trigger?.getBoundingClientRect()
    // The sidebar sits on the left, so its menu anchors its left edge. Taking
    // the right edge instead placed the menu off-screen once the rail was
    // collapsed and that edge was only a few dozen pixels in.
    window.dispatchEvent(new CustomEvent<NewActionAnchor>('stowcloud:new', {
      detail: rect ? { x: rect.left, y: rect.bottom + 4, align: 'start' } : undefined
    }))
  }

  const userInitial = (session.data?.user.display_name || session.data?.user.name || 'S').slice(0, 1).toUpperCase()

  if (screen === 'login') return <Navigate to="/login" replace />
  if (screen === 'first-run') return <Navigate to="/setup" replace />
  if (screen !== 'browser') {
    return <div className="sc-app-shell__boot" role="status" aria-label={t('nav.checking_your_session')}><mdui-circular-progress></mdui-circular-progress></div>
  }

  return (
    <>
      <div className={compact ? 'sc-app-shell sc-app-shell--compact' : 'sc-app-shell'}>
        <header className="sc-shell-header">
          <div className="sc-shell-header__left">
            {!compact ? (
              <button
                type="button"
                className="sc-shell-header__menu-btn sc-icon-button"
                aria-label={t('nav.toggle_sidebar')}
                onClick={() => ui.toggleSidebar()}
              >
                <Icon name="menu" size={22} />
              </button>
            ) : null}
            <button
              type="button"
              className="sc-shell-header__brand-btn"
              onClick={() => navigateTo('files', browseHref(browseTarget()))}
            >
              <span className="sc-shell-header__brand">Stowcloud</span>
            </button>
          </div>

          <div className="sc-shell-header__center">
            <button
              className="sc-shell-header__search sc-focus-ring"
              type="button"
              onClick={openSearch}
              aria-label={t('common.search')}
            >
              <span className="sc-shell-header__search-icon"><Icon name="search" size={18} /></span>
              <span className="sc-shell-header__search-placeholder">{t('common.search')}</span>
              <span className="sc-shell-header__search-hints">
                <kbd className="sc-shell-header__shortcut">/</kbd>
                <span className="sc-shell-header__filter-icon" aria-hidden="true">
                  <Icon name="tune" size={16} />
                </span>
              </span>
            </button>
          </div>

          <div className="sc-shell-header__right">
            {!compact ? (
              <button
                type="button"
                className="sc-shell-header__icon-btn sc-icon-button"
                aria-label={t('nav.help')}
                onClick={() => window.open('https://github.com/heavycaffeiner/Stowcloud', '_blank', 'noopener,noreferrer')}
              >
                <Icon name="help" size={20} />
              </button>
            ) : null}
            {!compact ? (
              <button
                type="button"
                className="sc-shell-header__icon-btn sc-icon-button"
                aria-label={t('common.settings')}
                onClick={() => navigateTo('settings', '/settings')}
              >
                <Icon name="settings" size={20} />
              </button>
            ) : null}
            <div className="sc-shell-header__account-wrap" ref={accountMenuRef}>
              <button
                type="button"
                className="sc-shell-header__avatar-btn"
                aria-label={session.data?.user.display_name || session.data?.user.name || 'User'}
                aria-haspopup="menu"
                aria-expanded={accountMenuOpen}
                onClick={() => setAccountMenuOpen((open) => !open)}
              >
                <span className="sc-shell-header__avatar">{userInitial}</span>
              </button>
              {accountMenuOpen ? (
                <div className="sc-shell-header__account-menu" role="menu">
                  <div className="sc-shell-header__account-name">
                    {session.data?.user.display_name || session.data?.user.name}
                  </div>
                  <button type="button" role="menuitem" onClick={() => { setAccountMenuOpen(false); navigateTo('settings', '/settings') }}>
                    <Icon name="settings" size={18} />
                    {t('common.settings')}
                  </button>
                  <button type="button" role="menuitem" onClick={signOut}>
                    <Icon name="close" size={18} />
                    {t('common.sign_out')}
                  </button>
                </div>
              ) : null}
            </div>
          </div>
        </header>

        <div className="sc-shell-body">
          {!compact ? (
            <NavigationDrawer
              collapsed={sidebarCollapsed}
              navItems={navItems}
              activeNav={activeNav}
              items={rootItems}
              active={activeRoot}
              onselect={(root) => void navigate(`/b/${encodeURIComponent(root.id)}`)}
              onnavselect={(item) => navigateTo(item.id, item.href)}
              onsearch={openSearch}
              onNew={activeNav === 'files' ? triggerNewAction : undefined}
              userInitial={userInitial}
            />
          ) : null}

          <main className={`sc-app-shell__main${!compact && sidebarCollapsed ? ' sc-app-shell__main--collapsed' : !compact ? ' sc-app-shell__main--drawer' : ''}`}>
            <Outlet />
          </main>
        </div>

        {compact ? (
          <NavigationBar
            items={compactItems}
            active={compactActive}
            onselect={(id) => {
              if (id === 'more') setMobileDrawerOpen((open) => !open)
              else navigateTo(id, compactItems.find((item) => item.id === id)?.href)
            }}
          />
        ) : null}

        {compact && folderSelectorOpen ? (
          <NavigationDrawer
            items={rootItems}
            active={activeRoot}
            folderSelectorOnly
            overlay
            onclose={() => setFolderSelectorOpen(false)}
            onselect={(root) => {
              setFolderSelectorOpen(false)
              void navigate(`/b/${encodeURIComponent(root.id)}`)
            }}
          />
        ) : null}

        {compact && mobileDrawerOpen ? (
          <NavigationDrawer
            overlay
            navItems={navItems}
            activeNav={activeNav}
            items={rootItems}
            active={activeRoot}
            userInitial={userInitial}
            onclose={() => setMobileDrawerOpen(false)}
            onselect={(root) => {
              setMobileDrawerOpen(false)
              void navigate(`/b/${encodeURIComponent(root.id)}`)
            }}
            onnavselect={(item) => {
              setMobileDrawerOpen(false)
              navigateTo(item.id, item.href)
            }}
            onsearch={openSearch}
            onNew={activeNav === 'files' ? (trigger) => {
              setMobileDrawerOpen(false)
              triggerNewAction(trigger)
            } : undefined}
          />
        ) : null}
      </div>

      <div ref={trayStackRef} className={compact ? 'sc-tray-stack sc-tray-stack--compact' : 'sc-tray-stack'}>
        <JobTray />
        <UploadTray />
      </div>
      <Snackbar />
      <SearchSheet open={searchOpen && !compact} scope={searchScope} onclose={() => search.close()} />
    </>
  )
}
