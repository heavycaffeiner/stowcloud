import type { SearchHit, SearchProgress } from '../../lib/api/client'
import type { SearchSnapshot, SearchSortKey } from '../../lib/store/search.store'

export type Kind = 'any' | 'file' | 'dir'
export type SortKey = SearchSortKey
export type SearchFailure = string | null
export type CategoryId = 'all' | 'file' | 'dir' | 'document' | 'image' | 'video' | 'audio' | 'archive' | 'code'

export interface SearchPanelState {
  query: string
  kind: Kind
  presets: readonly string[]
  extText: string
  extQuery: string
  sortKey: SortKey
  hits: readonly SearchHit[]
  running: boolean
  ran: boolean
  failure: SearchFailure
  truncated: boolean
  elapsedMs: number | null
  scanned: SearchProgress | null
  sortOpen: boolean
  scrollTop: number
}

export const CATEGORIES: readonly { id: CategoryId; labelKey: string; icon: string }[] = [
  { id: 'all', labelKey: /* i18n */ 'search.kind_any', icon: 'search' },
  { id: 'file', labelKey: /* i18n */ 'search.kind_file', icon: 'draft' },
  { id: 'dir', labelKey: /* i18n */ 'search.kind_dir', icon: 'folder' },
  { id: 'document', labelKey: 'search.preset_document', icon: 'description' },
  { id: 'image', labelKey: 'search.preset_image', icon: 'image' },
  { id: 'video', labelKey: 'search.preset_video', icon: 'movie' },
  { id: 'audio', labelKey: 'search.preset_audio', icon: 'audio-file' },
  { id: 'archive', labelKey: 'search.preset_archive', icon: 'folder-zip' },
  { id: 'code', labelKey: 'search.preset_code', icon: 'code' }
]

export const SORT_KEYS: readonly [SortKey, string][] = [
  ['relevance', /* i18n */ 'search.sort_relevance'],
  ['name', /* i18n */ 'search.sort_name'],
  ['size', /* i18n */ 'search.sort_size'],
  ['date', /* i18n */ 'search.sort_date']
]

export function initialSearchState(snapshot: SearchSnapshot | null): SearchPanelState {
  return {
    query: snapshot?.query ?? '',
    kind: snapshot?.kind ?? 'any',
    presets: snapshot ? [...snapshot.presets] : [],
    extText: snapshot?.extText ?? '',
    extQuery: snapshot?.extQuery ?? '',
    sortKey: snapshot?.sortKey ?? 'relevance',
    hits: snapshot?.hits ?? [],
    running: snapshot?.running ?? false,
    ran: snapshot?.ran ?? false,
    failure: snapshot?.running ? 'stopped' : snapshot?.failure ?? null,
    truncated: snapshot?.truncated ?? false,
    elapsedMs: snapshot?.elapsedMs ?? null,
    scanned: snapshot?.scanned ?? null,
    sortOpen: false,
    scrollTop: snapshot?.scrollTop ?? 0
  }
}

export function toSnapshot(scope: string, state: SearchPanelState, overrides: Partial<SearchSnapshot> = {}): SearchSnapshot {
  return {
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
    scrollTop: state.scrollTop,
    ...overrides
  }
}

export function sortHits(list: readonly SearchHit[], key: SortKey): readonly SearchHit[] {
  if (key === 'relevance') return list
  const result = [...list]
  if (key === 'name') result.sort((a, b) => a.entry.name.localeCompare(b.entry.name))
  else if (key === 'size') result.sort((a, b) => b.entry.size - a.entry.size)
  else result.sort((a, b) => {
    const left = BigInt(b.entry.mtime_ns || '0')
    const right = BigInt(a.entry.mtime_ns || '0')
    return left === right ? 0 : left > right ? 1 : -1
  })
  return result
}
export interface SearchStatus {
  readonly key: string
  readonly values?: Record<string, string>
}

export interface SearchWindow {
  readonly totalHeight: number
  readonly start: number
  readonly end: number
  readonly padTop: number
}
