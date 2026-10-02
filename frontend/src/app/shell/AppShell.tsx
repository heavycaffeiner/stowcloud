import { useEffect, useState } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useRefEffect } from 'react-simplikit'
import { useI18n } from '../../hooks/use-i18n'
import { isUnauthenticated, useSession } from '../../features/auth/api'
import { JobTray } from '../../features/jobs/components/JobTray'
import { SearchPage } from '../../features/search/components/SearchPage'
import { SearchSheet } from '../../features/search/components/SearchSheet'
import { useOpenedSearch } from '../../features/search/state'
import { UploadTray } from '../../features/uploads/components/UploadTray'
import { swReady } from '../../features/files/download-sw'
import { startLiveInvalidation } from '../../api/live'
import { cx, ErrorBoundary } from '@/shared/ui'
import { NavigationBar, type NavigationBarItem } from './NavigationBar'
import { cssVarName } from '@/lib/css-var'
import { trayStackTop } from '@/shared/theme'
import { useCompact } from '@/hooks/use-compact'
import { NavigationDrawer } from './NavigationDrawer'
import { ShellHeader } from './ShellHeader'
import { useNavigation, type NavId } from './navigation'
import { sidebar } from './sidebar'
import * as styles from './AppShell.css'

/** The signed-in app. A session that ends while it is open goes back to sign-in. */
export function AppShell() {
  const session = useSession()
  if (session.isError && isUnauthenticated(session.error)) return <Navigate to="/login" replace />
  return <ShellLayout />
}

function ShellLayout() {
  const compact = useCompact()
  const collapsed = sidebar.value === 'collapsed'
  const { scope } = useOpenedSearch()
  useEffect(() => {
    const stop = startLiveInvalidation()
    void swReady()
    return stop
  }, [])
  return (
    <>
      <div className={cx(styles.root, compact && styles.compact)}>
        <ShellHeader />
        <div className={styles.body}>
          {!compact ? <NavigationDrawer /> : null}
          <main className={cx(styles.main, !compact && (collapsed ? styles.mainCollapsed : styles.mainDrawer))}>
            {compact && scope !== undefined ? <SearchPage scope={scope} /> : <Outlet />}
          </main>
        </div>
        {compact ? <CompactNav /> : null}
      </div>
      <TrayStack compact={compact} />
      {!compact ? <SearchSheet scope={scope} /> : null}
    </>
  )
}

const BAR_DESTINATIONS: readonly NavId[] = ['files', 'recent', 'links']

/** The bottom bar. Whatever does not fit on it lives in a drawer that closes on any navigation. */
function CompactNav() {
  const { t } = useI18n()
  const navigate = useNavigate()
  // Each history entry has its own key, so the drawer closes on any navigation, a search param included.
  const key = useLocation({ select: (location) => location.state.__TSR_key ?? location.href })
  const { items, active } = useNavigation()
  const [openedAt, setOpenedAt] = useState<string | null>(null)
  const drawerOpen = openedAt === key
  const bar: readonly NavigationBarItem[] = [
    ...items.filter((item) => BAR_DESTINATIONS.includes(item.id)),
    {
      id: 'more',
      label: t('nav.more'),
      icon: 'menu',
      popup: 'dialog',
      expanded: drawerOpen,
      controls: 'sc-shell-drawer'
    }
  ]
  const select = (id: string): void => {
    if (id === 'more') setOpenedAt(drawerOpen ? null : key)
    else {
      const href = bar.find((item) => item.id === id)?.href
      if (href) void navigate({ href })
    }
  }
  return (
    <>
      <NavigationBar items={bar} active={BAR_DESTINATIONS.includes(active) ? active : 'more'} onSelect={select} />
      {drawerOpen ? <NavigationDrawer overlay onClose={() => setOpenedAt(null)} /> : null}
    </>
  )
}

/** The job and upload trays. Publishes its top edge so floating controls can sit above it. */
function TrayStack({ compact }: { readonly compact: boolean }) {
  const ref = useRefEffect<HTMLDivElement>((element) => {
    const root = document.documentElement.style
    const publish = (): void => {
      const top =
        element.offsetHeight > 0 ? `${window.innerHeight - element.getBoundingClientRect().top + 12}px` : '0px'
      root.setProperty(cssVarName(trayStackTop), top)
    }
    const observer = new ResizeObserver(publish)
    observer.observe(element)
    publish()
    return () => {
      observer.disconnect()
      root.removeProperty(cssVarName(trayStackTop))
    }
  }, [])
  return (
    <div ref={ref} className={cx(styles.trayStack, compact && styles.trayStackCompact)}>
      <ErrorBoundary>
        <JobTray />
      </ErrorBoundary>
      <ErrorBoundary>
        <UploadTray />
      </ErrorBoundary>
    </div>
  )
}
