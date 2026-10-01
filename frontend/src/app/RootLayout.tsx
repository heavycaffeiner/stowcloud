import { useEffect } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { overlay, OverlayProvider } from 'overlay-kit'

/** Hosts overlays inside the router, so they can navigate. An overlay belongs to the page that opened it. */
export function RootLayout() {
  const { pathname } = useLocation()
  useEffect(() => () => overlay.unmountAll(), [pathname])
  return (
    <OverlayProvider>
      <Outlet />
    </OverlayProvider>
  )
}
