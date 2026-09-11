import { Navigate, createBrowserRouter } from 'react-router-dom'
import { RouteErrorBoundary } from './RouteErrorBoundary'

function SettingsSecurityRedirect() {
  return <Navigate to={`/settings${window.location.search}#security`} replace />
}

// Route boundaries are intentionally loaded with React Router's lazy contract so
// public, authenticated, editor, settings, and admin graphs stay isolated.
export const router = createBrowserRouter([
  {
    path: '/',
    errorElement: <RouteErrorBoundary />,
    children: [
      { index: true, element: <Navigate to="/b/" replace /> },
      { path: 'login', lazy: async () => ({ Component: (await import('./routes/LoginPage')).LoginPage }) },
      { path: 'setup', lazy: async () => ({ Component: (await import('./routes/SetupPage')).SetupPage }) },
      { path: 'emergency', lazy: async () => ({ Component: (await import('./routes/EmergencyPage')).EmergencyPage }) },
      { path: 's/:token', lazy: async () => ({ Component: (await import('./routes/PublicSharePage')).PublicSharePage }) },
      {
        lazy: async () => ({ Component: (await import('./AppShell')).AppShell }),
        children: [
          { path: 'b/*', lazy: async () => ({ Component: (await import('./routes/BrowsePage')).BrowsePage }) },
          { path: 'edit/*', lazy: async () => ({ Component: (await import('./routes/EditPage')).EditPage }) },
          { path: 'links', lazy: async () => ({ Component: (await import('./routes/LinksPage')).LinksPage }) },
          { path: 'trash', lazy: async () => ({ Component: (await import('./routes/TrashPage')).TrashPage }) },
          { path: 'recent', lazy: async () => ({ Component: (await import('./routes/RecentPage')).RecentPage }) },
          { path: 'search', lazy: async () => ({ Component: (await import('./routes/SearchPage')).SearchPage }) },
          { path: 'settings/security', element: <SettingsSecurityRedirect /> },
          { path: 'settings', lazy: async () => ({ Component: (await import('./routes/SettingsPage')).SettingsPage }) },
          { path: 'admin', lazy: async () => ({ Component: (await import('./routes/AdminPage')).AdminPage }) }
        ]
      }
    ]
  }
])

