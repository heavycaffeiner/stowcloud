import { useEffect, useLayoutEffect, type RefObject } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import type { FileViewHandle } from '../FileTable'
import { resetBrowsePage } from '../browse-page'
import { selection } from '../selection'

/** Each folder opens with nothing selected and the page state at its defaults, before the first paint. */
export function useBrowsePageReset(path: string): void {
  useLayoutEffect(() => {
    selection.reset()
    resetBrowsePage()
  }, [path])
}

interface ListingProgress {
  isPending: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => Promise<unknown>
}

/** Focuses the row named by `?focus=`, loading more pages until it turns up, then drops the parameter. */
export function useFocusParam(view: RefObject<FileViewHandle | null>, listing: ListingProgress): void {
  const navigate = useNavigate()
  const { pathname, search } = useLocation()
  const focusName = new URLSearchParams(search).get('focus')
  const { isPending, hasNextPage, isFetchingNextPage, fetchNextPage } = listing
  useEffect(() => {
    if (!focusName || isPending) return
    if (view.current?.focusEntry(focusName)) {
      const next = new URLSearchParams(search)
      next.delete('focus')
      void navigate({ pathname, search: next.toString() ? `?${next}` : '' }, { replace: true })
    } else if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [focusName, isPending, hasNextPage, isFetchingNextPage, pathname, search, navigate, view, fetchNextPage])
}
