import { useMemo } from 'react'
import { joinPath } from '../../../lib/path-utils'
import { shareUnlocked } from '../../../lib/crypto/keyring'
import { useSelectionStore } from '../selection'
import { sortKey, sortOrder } from '../view-prefs'
import { filterDate, filterType } from '../browse-page'
import { matchesBrowseDate, matchesBrowseType } from '../logic/browse-listing-predicates'
import { dirViewOf, useDirectory, useFolderSizes, useShareEncryption, type Entry } from '../api'
import { useSession } from '../../auth/api'

/** The folder at `path` as the page shows it: the listing, the current selection, and what may be done here. */
export function useBrowseListing(path: string) {
  const session = useSession()
  const selectedNames = useSelectionStore((state) => state.names)
  const listing = useDirectory(path, { key: sortKey.value, order: sortOrder.value })
  const directory = useMemo(() => dirViewOf(listing.data?.pages), [listing.data?.pages])
  const entries = directory.entries
  const selected = useMemo(() => entries.filter((entry) => selectedNames.has(entry.name)), [entries, selectedNames])
  const encryption = useShareEncryption(path)
  const encrypted = encryption.data != null || encryption.isError
  const unlocked = encryption.data != null && shareUnlocked(encryption.data.salt)
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
  const type = filterType.value
  const date = filterDate.value
  const filteredEntries = useMemo(() => {
    const now = Date.now()
    return entries.filter((entry) => matchesBrowseType(entry, type) && matchesBrowseDate(entry, date, now))
  }, [entries, date, type])
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
    canCreate: directory.perms.create
  }
}
