// Where the search surface is, what it was opened from, and the last answer.
//
// One experience with two shapes: a top sheet over the page you were on when
// there is room beside it, and a page of its own when there is not. The
// button that opens it and the sheet that draws it sit on opposite sides of
// the layout, which is why the open state is here rather than in either.
//
// The snapshot belongs here rather than in either surface because the mobile
// route unmounts when a result opens. Keeping the submitted question, answer,
// and scroll anchor in this one store lets that route remount without silently
// changing the question or losing the item the user just opened.
import type { SearchHit, SearchProgress } from '../api/client'
import { defineStore } from './create'
import { ui } from './ui.store'

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

export interface SearchState {
  /** The desktop top sheet. The phone route is not a state, it is a URL. */
  readonly open: boolean
  /** The folder the search was started from. It ranks that subtree up; it
   *  never confines the search, which always covers everything reachable. */
  readonly scope: string
  /** The latest surface state, retained while a result route is open. */
  readonly snapshot: SearchSnapshot | null
}

export const search = defineStore({ open: false, scope: '', snapshot: null } as SearchState, (set) => ({
  openSheet(scope = ''): void {
    set({ open: true, scope })
  },
  close(): void {
    set({ open: false })
  },
  saveSnapshot(snapshot: SearchSnapshot): void {
    set({ snapshot, scope: snapshot.scope })
  },
  clearSnapshot(): void {
    set({ snapshot: null })
  }
}))

/** Computes the destination for the search surface without changing state. */
export function searchTarget(scope = ''): string | null {
  return ui.getState().compact ? (scope ? `/search?path=${encodeURIComponent(scope)}` : '/search') : null
}

/** Opens search in whichever shape the viewport calls for. */
export function openSearch(scope = ''): void {
  search.openSheet(scope)
}
