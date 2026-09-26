import { useMutation, useQuery } from '@tanstack/react-query'
import { useMemo, useRef } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useI18n } from '../lib/i18n/use-i18n'
import { isUnauthenticated, logoutMutation, screenOf, sessionQuery, setupRequiredQuery } from '../lib/query/session'
import { openSearch as openSearchStore, search, searchTarget } from '../lib/store/search.store'
import { ui } from '../lib/store/ui.store'
import { useStore } from '../lib/store/use-store'
import { JobTray } from '../features/jobs/JobTray'
import { NavigationBar, type NavigationBarItem } from '../lib/ui/NavigationBar'
import { NavigationDrawer, type NavItem, type RootItem } from '../features/account/navigation/NavigationDrawer'
import { SearchSheet } from '../features/search/SearchSheet'
import { Icon } from '../lib/ui/Icon'
import { UploadTray } from '../features/uploads/UploadTray'
import '../styles/app/shell.css.ts'
import { useRouteStore } from './use-route-store'
import {
  browsePathFromUrl,
  useAccountMenuDismissal,
  useBrowsePathState,
  useCompactResize,
  useShellKeyboardShortcuts,
  useShellLiveInvalidation,
  useShellRouteTransitions,
  useTrayGeometry,
  type ShellState
} from './shell/use-shell-lifecycle'

/** Where the create menu should open, in viewport coordinates. `align` says
 * which edge `x` refers to: a left-hand trigger anchors its left edge, a
 * right-hand one its right. */
export interface NewActionAnchor {
  readonly x: number
  readonly y: number
  readonly align: 'start' | 'end'
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
  const [shell, setShell] = useRouteStore<ShellState>({ lastBrowsePath: null, mobileDrawerOpen: false, folderSelectorOpen: false, accountMenuOpen: false })
  const { lastBrowsePath, mobileDrawerOpen, folderSelectorOpen, accountMenuOpen } = shell
  const trayStackRef = useRef<HTMLDivElement | null>(null)
  const accountMenuRef = useRef<HTMLDivElement | null>(null)
  const logout = useMutation(logoutMutation())

  useCompactResize()
  useAccountMenuDismissal(accountMenuOpen, accountMenuRef, setShell)
  useShellRouteTransitions(compact, location.pathname, location.search, screen, setShell)
  useBrowsePathState(location.pathname, location.search, setShell)
  useShellLiveInvalidation(screen)

  const signOut = (): void => {
    setShell({ accountMenuOpen: false })
    logout.mutate(undefined, {
      onSettled: (result) => {
        // Single sign-on ends the provider's session through its own URL; a
        // local session just returns to the sign-in screen.
        if (result?.end_session_url) window.location.assign(result.end_session_url)
        else void navigate('/login', { replace: true })
      }
    })
  }

  const roots = useMemo<RootItem[]>(() => (session.data?.roots ?? []).map((root) => ({ id: root.label, label: root.label, icon: 'folder', brokenReason: root.broken_reason })), [session.data?.roots])
  const rootItems = roots
  const browsePath = browsePathFromUrl(location.pathname)
  const browseTarget = (): string => {
    const rootLabels = new Set(rootItems.map((item) => item.id))
    const valid = (path: string | null): path is string => path === '/' || (!!path && rootLabels.has(path.split('/').filter(Boolean)[0] ?? ''))
    if (valid(browsePath)) return browsePath
    const previous = lastBrowsePath?.split('?')[0] ?? null
    if (valid(previous)) return previous
    return rootItems.length > 0 ? `/${rootItems[0].id}` : '/'
  }

  const browseHref = (path: string): string => path === '/' ? '/b' : `/b${path}`
  const browseScope = browsePath && browsePath !== '/' ? browsePath : lastBrowsePath?.split('?')[0] && lastBrowsePath?.split('?')[0] !== '/' ? lastBrowsePath.split('?')[0] : ''
  const activeRoot = browsePath?.split('/').filter(Boolean)[0] ?? ''

  const openSearch = (): void => {
    setShell({ mobileDrawerOpen: false, folderSelectorOpen: false })
    const target = searchTarget(browseScope)
    if (target) void navigate(target)
    else openSearchStore(browseScope)
  }

  useShellKeyboardShortcuts(screen, openSearch)
  useTrayGeometry(trayStackRef, screen === 'browser')

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
      setShell({ mobileDrawerOpen: false, folderSelectorOpen: false })
      void navigate(browseHref(browseTarget()))
      return
    }
    if (id === 'more') {
      setShell((current) => ({ folderSelectorOpen: false, mobileDrawerOpen: !current.mobileDrawerOpen }))
      return
    }
    const target = href ?? navItems.find((item) => item.id === id)?.href
    if (target) {
      setShell({ mobileDrawerOpen: false, folderSelectorOpen: false })
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
    return <div className="sc-app-shell-boot" role="status" aria-label={t('nav.checking_your_session')}><mdui-circular-progress></mdui-circular-progress></div>
  }

  return (
    <>
      <div className={compact ? 'sc-app-shell sc-app-shell-compact' : 'sc-app-shell'}>
        <header className="sc-shell-header">
          <div className="sc-shell-header-left">
            {!compact ? (
              <button
                type="button"
                className="sc-shell-header-menu-btn sc-icon-button"
                aria-label={t('nav.toggle_sidebar')}
                onClick={() => ui.toggleSidebar()}
              >
                <Icon name="menu" size={22} />
              </button>
            ) : null}
            <button
              type="button"
              className="sc-shell-header-brand-btn"
              onClick={() => navigateTo('files', browseHref(browseTarget()))}
            >
              <span className="sc-shell-header-brand">Stowcloud</span>
            </button>
          </div>

          <div className="sc-shell-header-center">
            <button
              className="sc-shell-header-search sc-focus-ring"
              type="button"
              onClick={openSearch}
              aria-label={t('common.search')}
            >
              <span className="sc-shell-header-search-icon"><Icon name="search" size={18} /></span>
              <span className="sc-shell-header-search-placeholder">{t('common.search')}</span>
              <span className="sc-shell-header-search-hints">
                <kbd className="sc-shell-header-shortcut">/</kbd>
                <span className="sc-shell-header-filter-icon" aria-hidden="true">
                  <Icon name="tune" size={16} />
                </span>
              </span>
            </button>
          </div>

          <div className="sc-shell-header-right">
            {!compact ? (
              <button
                type="button"
                className="sc-shell-header-icon-btn sc-icon-button"
                aria-label={t('nav.help')}
                onClick={() => window.open('https://github.com/heavycaffeiner/Stowcloud', '_blank', 'noopener,noreferrer')}
              >
                <Icon name="help" size={20} />
              </button>
            ) : null}
            {!compact ? (
              <button
                type="button"
                className="sc-shell-header-icon-btn sc-icon-button"
                aria-label={t('common.settings')}
                onClick={() => navigateTo('settings', '/settings')}
              >
                <Icon name="settings" size={20} />
              </button>
            ) : null}
            <div className="sc-shell-header-account-wrap" ref={accountMenuRef}>
              <button
                type="button"
                className="sc-shell-header-avatar-btn"
                aria-label={session.data?.user.display_name || session.data?.user.name || 'User'}
                aria-haspopup="menu"
                aria-expanded={accountMenuOpen}
                onClick={() => setShell((current) => ({ accountMenuOpen: !current.accountMenuOpen }))}
              >
                <span className="sc-shell-header-avatar">{userInitial}</span>
              </button>
              {accountMenuOpen ? (
                <div className="sc-shell-header-account-menu" role="menu">
                  <div className="sc-shell-header-account-name">
                    {session.data?.user.display_name || session.data?.user.name}
                  </div>
                  <button type="button" role="menuitem" onClick={() => { setShell({ accountMenuOpen: false }); navigateTo('settings', '/settings') }}>
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

          <main className={`sc-app-shell-main${!compact && sidebarCollapsed ? ' sc-app-shell-main-collapsed' : !compact ? ' sc-app-shell-main-drawer' : ''}`}>
            <Outlet />
          </main>
        </div>

        {compact ? (
          <NavigationBar
            items={compactItems}
            active={compactActive}
            onselect={(id) => {
              if (id === 'more') setShell((current) => ({ mobileDrawerOpen: !current.mobileDrawerOpen }))
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
            onclose={() => setShell({ folderSelectorOpen: false })}
            onselect={(root) => {
              setShell({ folderSelectorOpen: false })
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
            onclose={() => setShell({ mobileDrawerOpen: false })}
            onselect={(root) => {
              setShell({ mobileDrawerOpen: false })
              void navigate(`/b/${encodeURIComponent(root.id)}`)
            }}
            onnavselect={(item) => {
              setShell({ mobileDrawerOpen: false })
              navigateTo(item.id, item.href)
            }}
            onsearch={openSearch}
            onNew={activeNav === 'files' ? (trigger) => {
              setShell({ mobileDrawerOpen: false })
              triggerNewAction(trigger)
            } : undefined}
          />
        ) : null}
      </div>

      <div ref={trayStackRef} className={compact ? 'sc-tray-stack sc-tray-stack-compact' : 'sc-tray-stack'}>
        <JobTray />
        <UploadTray />
      </div>
      <SearchSheet open={searchOpen && !compact} scope={searchScope} onclose={() => search.close()} />
    </>
  )
}
