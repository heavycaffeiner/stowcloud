import { useMemo } from 'react'
import { joinPath } from '../../../lib/path-utils'
import { isUnlocked } from '../../../lib/crypto/e2ee'
import { useSelectionStore } from '../../../lib/store/selection.store'
import { useViewStore } from '../../../lib/store/view.store'
import type { BrowseFilterDate, BrowseFilterType } from '../logic/browse-types'
import { matchesBrowseDate, matchesBrowseType } from '../logic/browse-listing-predicates'
import { dirViewOf, useDirectory, useFolderSizes, useShareEncryption, type Entry } from '../api'
import { useSession } from '../../auth/api'

export function useBrowseListing(path: string, filterType: BrowseFilterType, filterDate: BrowseFilterDate) {
  const session = useSession()
  const sortKey = useViewStore((state) => state.sortKey)
  const sortOrder = useViewStore((state) => state.sortOrder)
  const selectedNames = useSelectionStore((state) => state.names)
  const listing = useDirectory(path, { key: sortKey, order: sortOrder })
  const directory = useMemo(() => dirViewOf(listing.data?.pages), [listing.data?.pages])
  const entries = directory.entries
  const selected = useMemo(() => entries.filter((entry) => selectedNames.has(entry.name)), [entries, selectedNames])
  const encryption = useShareEncryption(path)
  const encrypted = encryption.data != null || encryption.isError
  const unlocked = encryption.data != null && isUnlocked(encryption.data.salt)
  const measured = useFolderSizes(
    selected.length
      ? selected.filter((entry) => entry.kind === 'dir').map((entry) => joinPath(path, entry.name))
      : path === '/'
        ? []
        : [path]
  )
  const selectionBytes =
    selected.reduce((sum, entry) => sum + (entry.kind === 'dir' ? 0 : entry.size), 0) +
    measured.reduce((sum, query) => sum + (query.data?.bytes ?? 0), 0)
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
    sortOrder
  }
}
