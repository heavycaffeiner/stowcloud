// Where search is open and the last answer it gave.
//
// One experience with two shapes: a top sheet over the page you were on when
// there is room beside it, and the shell's page area when there is not. Both
// open from the URL, so back closes search and a reload brings it back.
//
// The snapshot outlives both surfaces because they unmount when a result
// opens. Keeping the question, answer, and scroll anchor here lets back return
// to the same list without running the question again.
import { signal } from '@preact/signals-react'
import { useCanGoBack, useNavigate, useRouter, useSearch } from '@tanstack/react-router'
import type { SearchHit, SearchProgress } from './api'

export type SearchKind = 'any' | 'file' | 'dir'
export type SearchSortKey = 'relevance' | 'name' | 'size' | 'date'

export interface SearchSnapshot {
  /** The folder used for ranking, never a result filter. */
  readonly scope: string
  readonly query: string
  /** The question the hits answer, which the field may have moved on from. */
  readonly submitted: string
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

const snapshot = signal<SearchSnapshot | null>(null)

export function saveSnapshot(next: SearchSnapshot): void {
  snapshot.value = next
}

/** The saved snapshot, if it was taken for this scope and question. */
export function snapshotFor(scope: string, query: string): SearchSnapshot | null {
  const saved = snapshot.peek()
  return saved?.scope === scope && saved.submitted === query ? saved : null
}

/** The open search from the URL: the folder it ranks up, '' for none, and the submitted question. */
export function useOpenedSearch(): { readonly scope: string | undefined; readonly query: string } {
  const scope = useSearch({ from: '/_app', select: (search) => search.search })
  const query = useSearch({ from: '/_app', select: (search) => search.q ?? '' })
  return { scope, query }
}

/** Opens search from `scope` over the current page. The folder ranks results up; it never confines them. */
export function useOpenSearch(): (scope: string) => void {
  const navigate = useNavigate()
  const opened = useSearch({ from: '/_app', select: (search) => search.search !== undefined })
  return (scope) => {
    if (!opened) void navigate({ to: '.', search: (previous) => ({ ...previous, search: scope, q: undefined }) })
  }
}

/** Closes search. Back undoes the visit that opened it, so forward can reopen it. */
export function useCloseSearch(): () => void {
  const router = useRouter()
  const navigate = useNavigate()
  const canGoBack = useCanGoBack()
  return () => {
    if (canGoBack) router.history.back()
    else
      void navigate({
        to: '.',
        search: (previous) => ({ ...previous, search: undefined, q: undefined }),
        replace: true
      })
  }
}

/** Records the submitted question in the URL without adding a history entry. */
export function useSubmittedQuery(): (query: string) => void {
  const navigate = useNavigate()
  return (query) =>
    void navigate({ to: '.', search: (previous) => ({ ...previous, q: query || undefined }), replace: true })
}
