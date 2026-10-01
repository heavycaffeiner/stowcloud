import { Navigate, createBrowserRouter } from 'react-router-dom'
import { RootLayout } from './RootLayout'
import { RouteErrorBoundary } from './RouteErrorBoundary'

function SettingsSecurityRedirect() {
  return <Navigate to={`/settings${window.location.search}#security`} replace />
}

// Route boundaries are intentionally loaded with React Router's lazy contract so
// public, authenticated, editor, settings, and admin graphs stay isolated.
export const router = createBrowserRouter([
  {
    path: '/',
    element: <RootLayout />,
    errorElement: <RouteErrorBoundary />,
    children: [
      { index: true, element: <Navigate to="/b/" replace /> },
      {
        path: 'login',
        lazy: async () => ({ Component: (await import('../features/auth/routes/LoginPage')).LoginPage })
      },
      {
        path: 'setup',
        lazy: async () => ({ Component: (await import('../features/auth/routes/SetupPage')).SetupPage })
      },
      {
        path: 'emergency',
        lazy: async () => ({ Component: (await import('../features/emergency/routes/EmergencyPage')).EmergencyPage })
      },
      {
        path: 's/:token',
        lazy: async () => ({ Component: (await import('../features/links/routes/PublicSharePage')).PublicSharePage })
      },
      {
        lazy: async () => ({ Component: (await import('./shell/AppShell')).AppShell }),
        children: [
          {
            // A page that fails keeps the shell, so navigation and the transfer trays stay usable.
            errorElement: <RouteErrorBoundary embedded />,
            children: [
              {
                path: 'b/*',
                lazy: async () => ({ Component: (await import('../features/files/routes/BrowseRoute')).BrowseRoute })
              },
              {
                path: 'edit/*',
                lazy: async () => ({ Component: (await import('../features/files/routes/EditPage')).EditPage })
              },
              {
                path: 'links',
                lazy: async () => ({ Component: (await import('../features/links/routes/LinksPage')).LinksPage })
              },
              {
                path: 'trash',
                lazy: async () => ({ Component: (await import('../features/trash/routes/TrashPage')).TrashPage })
              },
              {
                path: 'recent',
                lazy: async () => ({ Component: (await import('../features/recent/routes/RecentPage')).RecentPage })
              },
              {
                path: 'search',
                lazy: async () => ({ Component: (await import('../features/search/routes/SearchPage')).SearchPage })
              },
              { path: 'settings/security', element: <SettingsSecurityRedirect /> },
              {
                path: 'settings',
                lazy: async () => ({
                  Component: (await import('../features/settings/routes/SettingsPage')).SettingsPage
                })
              },
              {
                path: 'admin',
                lazy: async () => ({ Component: (await import('../features/admin/routes/AdminPage')).AdminPage })
              }
            ]
          }
        ]
      }
    ]
  }
])
