import { type Dispatch, useMemo, useReducer } from 'react'
import type { BrowseState } from '../logic/browse-types'
import { initialBrowseState } from '../logic/browse-types'
import { mergeState, type StatePatch } from '../../../lib/merge-state'

export type BrowseActions = {
  patch: Dispatch<StatePatch<BrowseState>>
  closeContextMenus: () => void
  closeMenus: () => void
  setSnackbar: (message: string | null) => void
  openConflict: (name: string, retry: (kind: Parameters<NonNullable<BrowseState['conflictRetry']>>[0]) => void) => void
}

export function useBrowseState(): [BrowseState, BrowseActions] {
  const [state, patch] = useReducer(mergeState<BrowseState>, initialBrowseState)
  const actions = useMemo<BrowseActions>(
    () => ({
      patch,
      closeContextMenus: () => patch({ contextMenu: null, blankMenu: null, menuTrigger: null }),
      closeMenus: () => patch({ newMenuOpen: false, overflowOpen: false, typeMenuOpen: false, dateMenuOpen: false }),
      setSnackbar: (message) => patch({ snackbar: message }),
      openConflict: (name, retry) => patch({ conflictName: name, conflictRetry: retry, conflictOpen: true })
    }),
    []
  )
  return [state, actions]
}
