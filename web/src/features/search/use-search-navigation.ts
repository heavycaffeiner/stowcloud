import { useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { parentOf } from '../../lib/api/path-utils'
import type { SearchHit } from '../../lib/api/client'
import { search } from '../../lib/store/search.store'
import type { SearchPanelState } from './search-state'

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
    openResult: useCallback((hit: SearchHit): void => {
      search.saveSnapshot({
        scope,
        query: state.query,
        kind: state.kind,
        presets: state.presets,
        extText: state.extText,
        extQuery: state.extQuery,
        sortKey: state.sortKey,
        hits: state.hits,
        running: state.running,
        ran: state.ran,
        failure: state.failure,
        truncated: state.truncated,
        elapsedMs: state.elapsedMs,
        scanned: state.scanned,
        scrollTop: state.scrollTop
      })
      onNavigated?.()
      void navigate(`/b${parentOf(hit.path)}?focus=${encodeURIComponent(hit.entry.name)}`)
    }, [navigate, onNavigated, scope, state])
  }
}
