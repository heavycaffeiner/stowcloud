import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { swReady } from '../lib/crypto/download-sw'
import { useI18n } from '../lib/i18n/use-i18n'
import { startLiveInvalidation } from '../lib/query/live'
import { isUnauthenticated, screenOf, sessionQuery, setupRequiredQuery } from '../lib/query/session'
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
  const [moreOpen, setMoreOpen] = useState(false)
  const [folderSelectorOpen, setFolderSelectorOpen] = useState(false)
  const moreDialogRef = useRef<HTMLDialogElement | null>(null)
  const trayStackRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const resize = (): void => ui.setCompact(window.innerWidth < COMPACT_MAX_PX)
    window.addEventListener('resize', resize)
    resize()
    return () => window.removeEventListener('resize', resize)
  }, [])

  useEffect(() => {
    setMoreOpen(false)
    setFolderSelectorOpen(false)
  }, [compact, location.pathname, location.search, screen])

  useEffect(() => {
    const dialog = moreDialogRef.current
    if (!compact || !moreOpen || screen !== 'browser' || !dialog) return
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    dialog.showModal()
    dialog.querySelector<HTMLButtonElement>('button')?.focus()
    return () => {
      if (dialog.open) dialog.close()
      if (trigger?.isConnected) trigger.focus()
    }
  }, [compact, moreOpen, screen])

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

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (screen !== 'browser' || !(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== 'k') return
      event.preventDefault()
      setMoreOpen(false)
      setFolderSelectorOpen(false)
      const scope = browsePathFromUrl(location.pathname)
      const target = searchTarget(scope && scope !== '/' ? scope : '')
      if (target) void navigate(target)
      else openSearchStore(scope && scope !== '/' ? scope : '')
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [location.pathname, navigate, screen])

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
  const openSearch = (): void => {
    setMoreOpen(false)
    setFolderSelectorOpen(false)
    const target = searchTarget(browseScope)
    if (target) void navigate(target)
    else openSearchStore(browseScope)
  }

  const navItems = useMemo<NavItem[]>(() => {
    const items: NavItem[] = [
      { id: 'files', label: t('nav.files'), icon: 'home', href: browseHref(browseTarget()) },
      { id: 'recent', label: t('nav.recent'), icon: 'history', href: '/recent' },
      { id: 'trash', label: t('common.trash'), icon: 'delete_sweep', href: '/trash' },
      { id: 'links', label: t('nav.links'), icon: 'link', href: '/links' },
      { id: 'settings', label: t('common.settings'), icon: 'settings', href: '/settings' }
    ]
    if (session.data?.user.is_admin) items.push({ id: 'admin', label: t('nav.admin'), icon: 'admin_panel_settings', href: '/admin' })
    return items
  }, [browseTarget, session.data?.user.is_admin, t, location.pathname, location.search, lastBrowsePath])

  const activeNav = location.pathname.startsWith('/settings') ? 'settings' : location.pathname.startsWith('/admin') ? 'admin' : location.pathname.startsWith('/recent') ? 'recent' : location.pathname.startsWith('/trash') ? 'trash' : location.pathname.startsWith('/links') ? 'links' : 'files'
  const compactItems = useMemo<NavigationBarItem[]>(() => [
    navItems.find((item) => item.id === 'files')!,
    navItems.find((item) => item.id === 'recent')!,
    navItems.find((item) => item.id === 'trash')!,
    navItems.find((item) => item.id === 'links')!,
    { id: 'more', label: t('nav.more'), icon: 'menu', popup: 'dialog', expanded: moreOpen, controls: 'sc-shell-more' }
  ], [moreOpen, navItems, t])
  const compactActive = ['settings', 'admin'].includes(activeNav) ? 'more' : activeNav

  const navigateTo = (id: string, href?: string): void => {
    if (id === 'files') {
      setMoreOpen(false)
      setFolderSelectorOpen(false)
      void navigate(browseHref(browseTarget()))
      return
    }
    if (id === 'more') {
      setFolderSelectorOpen(false)
      setMoreOpen((open) => !open)
      return
    }
    const target = href ?? navItems.find((item) => item.id === id)?.href
    if (target) {
      setMoreOpen(false)
      setFolderSelectorOpen(false)
      void navigate(target)
    }
  }

  if (screen === 'login') return <Navigate to="/login" replace />
  if (screen === 'first-run') return <Navigate to="/setup" replace />
  if (screen !== 'browser') {
    return <div className="sc-app-shell__boot" role="status" aria-label={t('nav.checking_your_session')}><mdui-circular-progress></mdui-circular-progress></div>
  }

  return (
    <>
      <div className={compact ? 'sc-app-shell sc-app-shell--compact' : 'sc-app-shell'}>
        {!compact ? <NavigationDrawer navItems={navItems} activeNav={activeNav} items={rootItems} active={browsePath?.split('/').filter(Boolean)[0] ?? ''} onselect={(root) => void navigate(`/b/${encodeURIComponent(root.id)}`)} onnavselect={(item) => navigateTo(item.id, item.href)} onsearch={openSearch} /> : null}
        <main className={compact ? 'sc-app-shell__main' : 'sc-app-shell__main sc-app-shell__main--drawer'}>
          <header className="sc-app-shell__topbar">
            <button className="sc-shell__search sc-focus-ring" type="button" onClick={openSearch}>
              <Icon name="search" />
              <span>{t('search.scope_all_accessible')}</span>
              <kbd>Ctrl K</kbd>
            </button>
          </header>
          <Outlet />
        </main>
        {compact ? <NavigationBar items={compactItems} active={compactActive} onselect={(id) => navigateTo(id, compactItems.find((item) => item.id === id)?.href)} /> : null}
        {compact && folderSelectorOpen ? <NavigationDrawer items={rootItems} active={browsePath?.split('/').filter(Boolean)[0] ?? ''} folderSelectorOnly overlay onclose={() => setFolderSelectorOpen(false)} onselect={(root) => { setFolderSelectorOpen(false); void navigate(`/b/${encodeURIComponent(root.id)}`) }} /> : null}
        {compact && moreOpen ? (
          <dialog
            ref={moreDialogRef}
            id="sc-shell-more"
            className="sc-shell__more"
            aria-label={t('nav.more')}
            aria-modal="true"
            onCancel={(event) => {
              event.preventDefault()
              setMoreOpen(false)
            }}
            onClose={() => setMoreOpen(false)}
            onClick={(event) => {
              if (event.target !== event.currentTarget) return
              const bounds = event.currentTarget.getBoundingClientRect()
              if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) setMoreOpen(false)
            }}
          >
              <button type="button" onClick={openSearch}><Icon name="search" />{t('common.search')}</button>
              <button type="button" onClick={() => { setMoreOpen(false); setFolderSelectorOpen(true) }}><Icon name="folder" />{t('nav.browse_folders')}</button>
              <button type="button" onClick={() => navigateTo('links', '/links')}><Icon name="link" />{t('nav.links')}</button>
              <button type="button" onClick={() => navigateTo('settings', '/settings')}><Icon name="settings" />{t('common.settings')}</button>
              {session.data?.user.is_admin ? <button type="button" onClick={() => navigateTo('admin', '/admin')}><Icon name="admin_panel_settings" />{t('nav.admin')}</button> : null}
          </dialog>
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
