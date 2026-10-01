import { useEffect } from 'react'
import { Outlet, useLocation } from '@tanstack/react-router'
import { overlay, OverlayProvider } from 'overlay-kit'

/** Hosts overlays inside the router, so they can navigate. An overlay belongs to the page that opened it. */
export function RootLayout() {
  const pathname = useLocation({ select: (location) => location.pathname })
  useEffect(() => () => overlay.unmountAll(), [pathname])
  return (
    <OverlayProvider>
      <Outlet />
    </OverlayProvider>
  )
}
