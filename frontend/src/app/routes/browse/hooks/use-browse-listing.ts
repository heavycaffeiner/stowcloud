import { useInfiniteQuery, useQueries, useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import type { Entry } from '../../../../lib/api/client'
import { joinPath } from '../../../../lib/api/path-utils'
import { isUnlocked } from '../../../../lib/crypto/e2ee'
import { dirListQuery, dirViewOf, folderSizeQuery, shareEncryptionQuery } from '../../../../lib/query/files'
import { sessionQuery } from '../../../../lib/query/session'
import { useStore } from '../../../../hooks/use-store'
import { selection } from '../../../../lib/store/selection.store'
import { view } from '../../../../lib/store/view.store'
import type { BrowseFilterDate, BrowseFilterType } from '../logic/types'
import { matchesBrowseDate, matchesBrowseType } from '../logic/browse-listing-predicates'

export function useBrowseListing(path: string, filterType: BrowseFilterType, filterDate: BrowseFilterDate) {
  const session = useQuery(sessionQuery())
  const sortKey = useStore(view, (state) => state.sortKey)
  const sortOrder = useStore(view, (state) => state.sortOrder)
  const selectedNames = useStore(selection, (state) => state.names)
  const listing = useInfiniteQuery(dirListQuery(path, { key: sortKey, order: sortOrder }))
  const directory = useMemo(() => dirViewOf(listing.data?.pages), [listing.data?.pages])
  const entries = directory.entries
  const selected = useMemo(() => entries.filter((entry) => selectedNames.has(entry.name)), [entries, selectedNames])
  const encryption = useQuery(shareEncryptionQuery(path))
  const encrypted = encryption.data != null || encryption.isError
  const unlocked = encryption.data != null && isUnlocked(encryption.data.salt)
  const measured = useQueries({
    queries: (selected.length
      ? selected.filter((entry) => entry.kind === 'dir').map((entry) => joinPath(path, entry.name))
      : path === '/' ? [] : [path]
    ).map((currentPath) => folderSizeQuery(currentPath))
  })
  const selectionBytes = selected.reduce((sum, entry) => sum + (entry.kind === 'dir' ? 0 : entry.size), 0) + measured.reduce((sum, query) => sum + (query.data?.bytes ?? 0), 0)
  const filteredEntries = useMemo(() => {
    const now = Date.now()
    return entries.filter((entry) => matchesBrowseType(entry, filterType) && matchesBrowseDate(entry, filterDate, now))
  }, [entries, filterDate, filterType])
  const rootLabel = path.split('/').filter(Boolean)[0]
  const root = session.data?.roots.find((entry) => entry.label === rootLabel)
  const noShares = (session.data?.roots.length ?? 0) === 0
  return {
    session,
    listing,
    directory,
    entries,
    filteredEntries,
    selected,
    selectedNames,
    encryption,
    encrypted,
    unlocked,
    root,
    noShares,
    selectionBytes,
    canCreate: directory.perms.create,
    sortKey,
    sortOrder,
  }
}
