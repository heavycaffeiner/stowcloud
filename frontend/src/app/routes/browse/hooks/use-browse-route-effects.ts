import { useEffect, type RefObject } from 'react'
import type { NavigateFunction } from 'react-router-dom'
import type { FileGridHandle } from '../../../../features/files/FileGrid'
import type { FileViewHandle } from '../../../../features/files/FileTable'
import { selection } from '../../../../lib/store/selection.store'

export function useBrowsePathSelection(path: string): void {
  useEffect(() => {
    selection.reset()
  }, [path])
}

type BrowseRouteFocusOptions = {
  focusName: string | null
  isPending: boolean
  hasNextPage: boolean | undefined
  isFetchingNextPage: boolean
  mode: 'list' | 'grid'
  pathname: string
  search: string
  navigate: NavigateFunction
  tableRef: RefObject<FileViewHandle | null>
  gridRef: RefObject<FileGridHandle | null>
  fetchNextPage: () => Promise<unknown>
}

export function useBrowseRouteFocus({
  focusName,
  isPending,
  hasNextPage,
  isFetchingNextPage,
  mode,
  pathname,
  search,
  navigate,
  tableRef,
  gridRef,
  fetchNextPage,
}: BrowseRouteFocusOptions): void {
  useEffect(() => {
    if (!focusName || isPending) return
    const target = mode === 'grid' ? gridRef.current : tableRef.current
    if (target?.focusEntry(focusName)) {
      const next = new URLSearchParams(search)
      next.delete('focus')
      void navigate({ pathname, search: next.toString() ? `?${next}` : '' }, { replace: true })
    } else if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [focusName, isPending, hasNextPage, isFetchingNextPage, mode, pathname, search, navigate, tableRef, gridRef, fetchNextPage])
}
