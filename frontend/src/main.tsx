import './shared/theme/styles'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { RootProviders } from './app/RootProviders'
import { router } from './app/router'
import { theme } from './features/settings/theme'
import { applyColorSchemeAttribute } from './shared/theme/color-scheme'
import { initLocale } from './i18n/state'
import { localeTag } from './i18n'
import '@fontsource-variable/google-sans-flex/opsz.css'

const root = document.getElementById('root')
if (!root) throw new Error('Missing #root application mount')
applyColorSchemeAttribute(theme.peek())

await initLocale()
document.documentElement.lang = localeTag()

createRoot(root).render(
  <StrictMode>
    <RootProviders>
      <RouterProvider router={router} />
    </RootProviders>
  </StrictMode>
)
