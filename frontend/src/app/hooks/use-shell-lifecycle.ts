import { useEffect, type RefObject } from 'react'
import { swReady } from '../../lib/crypto/download-sw'
import { startLiveInvalidation } from '../../lib/query/live'
import { COMPACT_MAX_PX, ui } from '../../lib/store/ui.store'
import type { RoutePatch } from './use-route-store'

/** State that belongs to the mounted application shell, not to a route. */
export interface ShellState {
  readonly lastBrowsePath: string | null
  readonly mobileDrawerOpen: boolean
  readonly folderSelectorOpen: boolean
  readonly accountMenuOpen: boolean
}

export type SetShellState = (patch: RoutePatch<ShellState>) => void

export function isBrowsePathname(pathname: string): boolean {
  return pathname === '/b' || pathname.startsWith('/b/')
}

export function browsePathFromUrl(pathname: string): string | null {
  if (!isBrowsePathname(pathname)) return null
  try {
    return decodeURI(pathname.slice('/b'.length) || '/')
  } catch {
    return pathname.slice('/b'.length) || '/'
  }
}

/** Keeps the compact layout flag synchronized with the viewport. */
export function useCompactResize(): void {
  useEffect(() => {
    const resize = (): void => ui.setCompact(window.innerWidth < COMPACT_MAX_PX)
    window.addEventListener('resize', resize)
    resize()
    return () => window.removeEventListener('resize', resize)
  }, [])
}

/** Closes the account menu when focus moves outside it or Escape is pressed. */
export function useAccountMenuDismissal(
  open: boolean,
  menuRef: RefObject<HTMLDivElement | null>,
  setShell: SetShellState
): void {
  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: PointerEvent): void => {
      if (!menuRef.current?.contains(event.target as Node)) setShell({ accountMenuOpen: false })
    }
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') setShell({ accountMenuOpen: false })
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [menuRef, open, setShell])
}

/** Resets transient drawers when navigation or the layout mode changes. */
export function useShellRouteTransitions(
  compact: boolean,
  pathname: string,
  search: string,
  screen: string,
  setShell: SetShellState
): void {
  useEffect(() => {
    setShell({ mobileDrawerOpen: false, folderSelectorOpen: false })
  }, [compact, pathname, search, screen, setShell])
}

/** Remembers the last valid browse path for navigation back to Files. */
export function useBrowsePathState(pathname: string, search: string, setShell: SetShellState): void {
  useEffect(() => {
    const browsePath = browsePathFromUrl(pathname)
    if (browsePath !== null) setShell({ lastBrowsePath: `${browsePath}${search}` })
  }, [pathname, search, setShell])
}

/** Starts authenticated live invalidation and download service-worker support. */
export function useShellLiveInvalidation(screen: string): void {
  useEffect(() => {
    if (screen !== 'browser') return
    const stop = startLiveInvalidation()
    void swReady()
    return stop
  }, [screen])
}

/** Binds the global search shortcuts while the browser shell is active. */
export function useShellKeyboardShortcuts(screen: string, openSearch: () => void): void {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (screen !== 'browser') return
      const isInput = event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || (event.target instanceof HTMLElement && event.target.isContentEditable)
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        openSearch()
      } else if (!isInput && event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey) {
        event.preventDefault()
        openSearch()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [openSearch, screen])
}

/** Publishes the tray stack's top edge for overlays that need to clear it. */
export function useTrayGeometry(trayStackRef: RefObject<HTMLDivElement | null>, enabled: boolean): void {
  useEffect(() => {
    const element = trayStackRef.current
    if (!element) return
    const publish = (): void => {
      const top = element.offsetHeight > 0 ? `${window.innerHeight - element.getBoundingClientRect().top + 12}px` : '0px'
      document.documentElement.style.setProperty('--sc-tray-stack-top', top)
    }
    const observer = new ResizeObserver(publish)
    observer.observe(element)
    publish()
    return () => {
      observer.disconnect()
      document.documentElement.style.removeProperty('--sc-tray-stack-top')
    }
  }, [enabled, trayStackRef])
}
