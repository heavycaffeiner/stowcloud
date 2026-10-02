import type { QueryClient } from '@tanstack/react-query'
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  redirect
} from '@tanstack/react-router'
import { adminTabs } from '../features/admin/tabs'
import { resolveAuthScreen } from '../features/auth/api'
import { splatOf, validateBrowseSearch } from '../features/files/browse-search'
import { validateSearchParams } from '../features/search/search-params'
import { settingsTabs } from '../features/settings/tabs'
import { queryClient } from '../api/query-client'
import { parseSearch, stringifySearch, textParam } from '../lib/url-search'
import { RootLayout } from './RootLayout'
import { AppError, RouteProblem, SessionPending } from './RouteFallbacks'

// Every page loads lazily, so the public, sign-in, signed-in, editor, settings
// and admin graphs stay in separate chunks.

/** The tab a URL should name instead, or null when it is right. An old `#tab` link moves into the path. */
function tabFix(tabs: readonly string[], tab: string | undefined, hash: string): { tab: string | undefined } | null {
  if (tab === undefined) return tabs.includes(hash) ? { tab: hash } : null
  return tabs.includes(tab) ? null : { tab: undefined }
}

const rootRoute = createRootRouteWithContext<{ queryClient: QueryClient }>()({ component: RootLayout })

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/b/$', params: { _splat: '' }, replace: true })
  }
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: 'login',
  validateSearch: (raw): { returnTo?: string; oidc_error?: string } => ({
    returnTo: textParam(raw.returnTo),
    oidc_error: textParam(raw.oidc_error)
  }),
  component: lazyRouteComponent(() => import('../features/auth/routes/LoginPage'), 'LoginPage')
})

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: 'setup',
  component: lazyRouteComponent(() => import('../features/auth/routes/SetupPage'), 'SetupPage')
})

const emergencyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: 'emergency',
  component: lazyRouteComponent(() => import('../features/emergency/routes/EmergencyPage'), 'EmergencyPage')
})

const shareRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: 's/$token',
  validateSearch: (raw): { path?: string } => ({ path: textParam(raw.path) || undefined }),
  component: lazyRouteComponent(() => import('../features/links/routes/PublicSharePage'), 'PublicSharePage')
})

/** The signed-in app. Everyone else goes to sign-in, or to first-run setup when there is no account yet. */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_app',
  validateSearch: validateSearchParams,
  beforeLoad: async ({ context }) => {
    const screen = await resolveAuthScreen(context.queryClient)
    if (screen === 'login') throw redirect({ to: '/login', replace: true })
    if (screen === 'first-run') throw redirect({ to: '/setup', replace: true })
  },
  pendingComponent: SessionPending,
  pendingMs: 0,
  pendingMinMs: 0,
  errorComponent: AppError,
  notFoundComponent: () => <RouteProblem notFound embedded />,
  component: lazyRouteComponent(() => import('./shell/AppShell'), 'AppShell')
})

// A page that fails keeps the shell, so navigation and the transfer trays stay usable.
const ShellPageError = () => <RouteProblem embedded />

const browseRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'b/$',
  validateSearch: validateBrowseSearch,
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/files/routes/BrowseRoute'), 'BrowseRoute')
})

const editRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'edit/$',
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/files/routes/EditPage'), 'EditPage')
})

const linksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'links',
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/links/routes/LinksPage'), 'LinksPage')
})

const trashRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'trash',
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/trash/routes/TrashPage'), 'TrashPage')
})

const recentRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'recent',
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/recent/routes/RecentPage'), 'RecentPage')
})

const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'settings/{-$tab}',
  validateSearch: (raw): { oidc_error?: string } => ({ oidc_error: textParam(raw.oidc_error) }),
  beforeLoad: ({ params, location, search }) => {
    const fix = tabFix(settingsTabs, params.tab, location.hash)
    if (fix) throw redirect({ to: '/settings/{-$tab}', params: fix, search, replace: true })
  },
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/settings/routes/SettingsPage'), 'SettingsPage')
})

const adminRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'admin/{-$tab}',
  beforeLoad: ({ params, location, search }) => {
    const fix = tabFix(adminTabs, params.tab, location.hash)
    if (fix) throw redirect({ to: '/admin/{-$tab}', params: fix, search, replace: true })
  },
  errorComponent: ShellPageError,
  component: lazyRouteComponent(() => import('../features/admin/routes/AdminPage'), 'AdminPage')
})

/** Search used to have a page of its own. It now opens over the folder it ranks. */
const legacySearchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'search',
  validateSearch: (raw): { path?: string } => ({ path: textParam(raw.path) }),
  beforeLoad: ({ search }) => {
    const scope = search.path ?? ''
    throw redirect({ to: '/b/$', params: splatOf(scope), search: { search: scope }, replace: true })
  }
})

export const router = createRouter({
  routeTree: rootRoute.addChildren([
    indexRoute,
    loginRoute,
    setupRoute,
    emergencyRoute,
    shareRoute,
    appRoute.addChildren([
      browseRoute,
      editRoute,
      linksRoute,
      trashRoute,
      recentRoute,
      settingsRoute,
      adminRoute,
      legacySearchRoute
    ])
  ]),
  context: { queryClient },
  parseSearch,
  stringifySearch,
  defaultErrorComponent: () => <RouteProblem />,
  defaultNotFoundComponent: () => <RouteProblem notFound />
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
