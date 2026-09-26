import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router-dom'
import { RootProviders } from './app/RootProviders'
import { router } from './app/router'
import './lib/ui/mdui'
import './react-app.css'

const root = document.getElementById('root')
if (!root) throw new Error('Missing #root application mount')

createRoot(root).render(
  <StrictMode>
    <RootProviders>
      <RouterProvider router={router} />
    </RootProviders>
  </StrictMode>
)
