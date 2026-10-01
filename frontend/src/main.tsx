import './ui/styles'
import { effect } from '@preact/signals-react'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router-dom'
import { RootProviders } from './app/RootProviders'
import { router } from './app/router'
import { theme } from './features/settings/theme'
import { currentLocale, initLocale } from './lib/i18n/state'
import { applyMduiTheme, initMdui } from './ui/mdui-runtime'
import '@fontsource-variable/google-sans-flex/opsz.css'

const root = document.getElementById('root')
if (!root) throw new Error('Missing #root application mount')

await initLocale()
document.documentElement.lang = currentLocale() === 'ko' ? 'ko-KR' : 'en-US'
initMdui()
effect(() => applyMduiTheme(theme.value))

createRoot(root).render(
  <StrictMode>
    <RootProviders>
      <RouterProvider router={router} />
    </RootProviders>
  </StrictMode>
)
