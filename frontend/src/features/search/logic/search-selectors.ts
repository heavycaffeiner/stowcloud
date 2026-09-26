import { EXTENSION_PRESETS, parseExtensions } from '../../../lib/search/filters'
import { computeWindow, type WindowResult } from '../../../lib/virtual/windowing'
import { SORT_KEYS, sortHits, type CategoryId, type SearchPanelState, type SearchStatus } from './search-state'
import type { SearchHit } from '../../../lib/api/client'

export function activeCategoryFor(state: SearchPanelState): CategoryId {
  if (state.kind === 'dir') return 'dir'
  if (state.kind === 'file' && state.presets.length === 0) return 'file'
  if (state.presets.length === 1 && ['document', 'image', 'video', 'audio', 'archive', 'code'].includes(state.presets[0] ?? '')) {
    return state.presets[0] as CategoryId
  }
  return 'all'
}

export function activeFiltersFor(state: SearchPanelState): string {
  return [
    ...state.presets.flatMap((id) => {
      const preset = EXTENSION_PRESETS.find((candidate) => candidate.id === id)
      return preset ? [preset.labelKey] : []
    }),
    ...parseExtensions(state.extQuery)
  ].join(', ')
}

export function sortLabelKeyFor(state: SearchPanelState): string {
  return SORT_KEYS.find(([key]) => key === state.sortKey)?.[1] ?? 'search.sort_relevance'
}

export function viewFor(state: SearchPanelState): readonly SearchHit[] {
  return state.running ? state.hits : sortHits(state.hits, state.sortKey)
}

export function statusFor(state: SearchPanelState, view: readonly SearchHit[]): SearchStatus {
  if (state.running) return { key: /* i18n */ 'search.searching', values: { count: String(state.hits.length), dirs: String(state.scanned?.dirs ?? '') } }
  if (!state.ran) return { key: '' }
  if (state.truncated) return { key: /* i18n */ 'search.incomplete', values: { count: String(view.length) } }
  if (state.failure === 'stopped') return { key: /* i18n */ 'search.stopped', values: { count: String(view.length) } }
  if (state.failure === 'busy') return { key: /* i18n */ 'search.busy' }
  if (state.failure === 'network') return { key: /* i18n */ 'search.connection_lost', values: { count: String(view.length) } }
  if (state.failure) return { key: /* i18n */ 'search.failed' }
  if (state.elapsedMs !== null) return { key: /* i18n */ 'search.found_in', values: { count: String(view.length), ms: String(state.elapsedMs) } }
  return { key: /* i18n */ 'search.found', values: { count: String(view.length) } }
}

export function windowFor(state: SearchPanelState, view: readonly SearchHit[], viewportHeight: number): WindowResult {
  return computeWindow({ scrollTop: state.scrollTop, viewportHeight, rowHeight: 56, itemCount: view.length, overscan: 8 })
}
