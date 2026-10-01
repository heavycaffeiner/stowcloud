// Where the search surface is open and the last answer it gave.
//
// One experience with two shapes: a top sheet over the page you were on when
// there is room beside it, and a page of its own when there is not. The
// button that opens it and the sheet that draws it sit on opposite sides of
// the layout, which is why the open state is a signal rather than either's own.
//
// The snapshot outlives both surfaces because the mobile route unmounts when a
// result opens. Keeping the submitted question, answer, and scroll anchor here
// lets that route remount without silently changing the question or losing
// the item the user just opened.
import { signal } from '@preact/signals-react'
import { useNavigate } from 'react-router-dom'
import { useCompact } from '../../ui/use-compact'
import type { SearchHit, SearchProgress } from './api'

export type SearchKind = 'any' | 'file' | 'dir'
export type SearchSortKey = 'relevance' | 'name' | 'size' | 'date'

export interface SearchSnapshot {
  /** The folder used for ranking, never a result filter. */
  readonly scope: string
  readonly query: string
  readonly kind: SearchKind
  readonly presets: readonly string[]
  readonly extText: string
  readonly extQuery: string
  readonly sortKey: SearchSortKey
  readonly hits: readonly SearchHit[]
  readonly running: boolean
  readonly ran: boolean
  readonly failure: string | null
  /** True when the server stopped before checking every accessible folder. */
  readonly truncated: boolean
  readonly elapsedMs: number | null
  readonly scanned: SearchProgress | null
  /** Scroll offset in the virtualized result list. */
  readonly scrollTop: number
}

/** The folder the desktop sheet was opened from, or null while it is closed.
 *  It ranks that subtree up; it never confines the search. */
export const sheetScope = signal<string | null>(null)

const snapshot = signal<SearchSnapshot | null>(null)

export function saveSnapshot(next: SearchSnapshot): void {
  snapshot.value = next
}

/** The saved snapshot, if it was taken for this scope. */
export function snapshotFor(scope: string): SearchSnapshot | null {
  const saved = snapshot.peek()
  return saved?.scope === scope ? saved : null
}

/** Opens search from `scope`: the sheet where there is room for it, its own route on a phone. */
export function useOpenSearch(): (scope: string) => void {
  const compact = useCompact()
  const navigate = useNavigate()
  return (scope) => {
    if (!compact) sheetScope.value = scope
    else void navigate(scope ? `/search?path=${encodeURIComponent(scope)}` : '/search')
  }
}
