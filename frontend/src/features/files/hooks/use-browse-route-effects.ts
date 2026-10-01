import { useCallback, useEffect, useLayoutEffect, type RefObject } from 'react'
import { useCanGoBack, useNavigate, useRouter, useSearch } from '@tanstack/react-router'
import type { FileViewHandle } from '../FileTable'
import type { Entry } from '../api'
import { resetBrowsePage } from '../browse-page'
import type { BrowseSearch } from '../browse-search'
import { selection } from '../selection'

/** Each folder opens with nothing selected and the page state at its defaults, before the first paint. */
export function useBrowsePageReset(path: string): void {
  useLayoutEffect(() => {
    selection.reset()
    resetBrowsePage()
  }, [path])
}

/** The folder URL's params and a writer for them. Changes replace the history entry unless `push` is set. */
export function useBrowseSearch() {
  const search = useSearch({ from: '/_app/b/$' })
  const navigate = useNavigate()
  const update = useCallback(
    (patch: Partial<BrowseSearch>, push = false): void =>
      void navigate({ from: '/b/$', to: '.', search: (previous) => ({ ...previous, ...patch }), replace: !push }),
    [navigate]
  )
  return { search, update }
}

interface ListingProgress {
  isPending: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => Promise<unknown>
}

/** Focuses the row named by `?focus=`, loading more pages until it turns up, then drops the parameter. */
export function useFocusParam(view: RefObject<FileViewHandle | null>, listing: ListingProgress): void {
  const { search, update } = useBrowseSearch()
  const focusName = search.focus
  const { isPending, hasNextPage, isFetchingNextPage, fetchNextPage } = listing
  useEffect(() => {
    if (!focusName || isPending) return
    if (view.current?.focusEntry(focusName)) update({ focus: undefined })
    else if (hasNextPage && !isFetchingNextPage) void fetchNextPage()
  }, [focusName, isPending, hasNextPage, isFetchingNextPage, update, view, fetchNextPage])
}

/**
 * The file `?preview=` names, loading more pages until it turns up. A name the whole folder does not have
 * drops out of the URL. Closing goes back when the preview was opened here, so back and close agree.
 */
export function usePreviewParam(entries: readonly Entry[], listing: ListingProgress) {
  const { search, update } = useBrowseSearch()
  const router = useRouter()
  const canGoBack = useCanGoBack()
  const name = search.preview
  const entry = name ? (entries.find((item) => item.name === name && item.kind !== 'dir') ?? null) : null
  const { isPending, hasNextPage, isFetchingNextPage, fetchNextPage } = listing
  const missing = name !== undefined && entry === null && !isPending
  useEffect(() => {
    if (!missing) return
    if (hasNextPage) {
      if (!isFetchingNextPage) void fetchNextPage()
    } else update({ preview: undefined })
  }, [missing, hasNextPage, isFetchingNextPage, fetchNextPage, update])
  return {
    entry,
    show: (next: Entry): void => update({ preview: next.name }, entry === null),
    close: (): void => {
      if (canGoBack) router.history.back()
      else update({ preview: undefined })
    }
  }
}
