import { useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { parentOf } from '../../../lib/path-utils'
import { saveSnapshot } from '../state'
import { toSnapshot, type SearchPanelState } from '../logic/search-state'
import type { SearchHit } from '../api'

export interface SearchNavigationOptions {
  readonly scope: string
  readonly state: SearchPanelState
  readonly onNavigated?: () => void
}

export interface SearchNavigation {
  readonly openResult: (hit: SearchHit) => void
}

export function useSearchNavigation({ scope, state, onNavigated }: SearchNavigationOptions): SearchNavigation {
  const navigate = useNavigate()
  return {
    openResult: useCallback(
      (hit: SearchHit): void => {
        saveSnapshot(toSnapshot(scope, state))
        onNavigated?.()
        void navigate(`/b${parentOf(hit.path)}?focus=${encodeURIComponent(hit.entry.name)}`)
      },
      [navigate, onNavigated, scope, state]
    )
  }
}
