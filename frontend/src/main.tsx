import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router-dom'
import { RootProviders } from './app/RootProviders'
import { router } from './app/router'
import { currentLocale, initLocale } from './lib/i18n/state'
import './lib/ui/logic/mdui'
import 'mdui/mdui.css'
import '@fontsource-variable/google-sans-flex/opsz.css'
import './styles/global/base.css.ts'

const root = document.getElementById('root')
if (!root) throw new Error('Missing #root application mount')

await initLocale()
document.documentElement.lang = currentLocale() === 'ko' ? 'ko-KR' : 'en-US'

createRoot(root).render(
  <StrictMode>
    <RootProviders>
      <RouterProvider router={router} />
    </RootProviders>
  </StrictMode>
)
