import { useCallback, useRef } from 'react'
import { useRouteStore } from '../../use-route-store'
import type { BrowseState } from './types'
import { initialBrowseState } from './types'

export type BrowseActions = {
  patch: (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void
  closeContextMenus: () => void
  closeMenus: () => void
  setSnackbar: (message: string | null) => void
  openConflict: (name: string, retry: (kind: Parameters<NonNullable<BrowseState['conflictRetry']>>[0]) => void) => void
}

export function useBrowseState(): [BrowseState, BrowseActions] {
  const [state, patch] = useRouteStore<BrowseState>(initialBrowseState)
  const patchRef = useRef(patch)
  patchRef.current = patch
  const closeContextMenus = useCallback(() => patchRef.current({ contextMenu: null, blankMenu: null, menuTrigger: null }), [])
  const closeMenus = useCallback(() => patchRef.current({ newMenuOpen: false, overflowOpen: false, typeMenuOpen: false, dateMenuOpen: false }), [])
  const setSnackbar = useCallback((message: string | null) => patchRef.current({ snackbar: message }), [])
  const openConflict = useCallback((name: string, retry: BrowseState['conflictRetry']) => patchRef.current({ conflictName: name, conflictRetry: retry, conflictOpen: true }), [])
  return [state, { patch, closeContextMenus, closeMenus, setSnackbar, openConflict }]
}
