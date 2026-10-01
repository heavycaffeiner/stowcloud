import { useCallback } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { parentOf } from '../../../lib/path-utils'
import { splatOf } from '../../files/browse-search'
import { saveSnapshot } from '../state'
import { toSnapshot, type SearchPanelState } from '../logic/search-state'
import type { SearchHit } from '../api'

export interface SearchNavigationOptions {
  readonly scope: string
  readonly state: SearchPanelState
}

export interface SearchNavigation {
  readonly openResult: (hit: SearchHit) => void
}

/** Opens a result in its folder. Leaving the search params behind closes search, and back reopens it. */
export function useSearchNavigation({ scope, state }: SearchNavigationOptions): SearchNavigation {
  const navigate = useNavigate()
  return {
    openResult: useCallback(
      (hit: SearchHit): void => {
        saveSnapshot(toSnapshot(scope, state))
        void navigate({ to: '/b/$', params: splatOf(parentOf(hit.path)), search: { focus: hit.entry.name } })
      },
      [navigate, scope, state]
    )
  }
}
