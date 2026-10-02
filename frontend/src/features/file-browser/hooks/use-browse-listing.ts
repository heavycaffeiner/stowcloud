import { useMemo } from 'react'
import { joinPath } from '../../../lib/path-utils'
import { useShareUnlocked } from '../../shares/e2ee-store'
import { useSelectionStore } from '../model/selection'
import { sortKey, sortOrder } from '../model/sorting'
import { filterItems, isFiltering, type BrowseFilter } from '../model/filtering'
import { toFileEntry, type FileEntry } from '../model/file-entry'
import { dirViewOf, useDirectory, useFolderSizes, useShareEncryption, type Entry } from '../api'
import { useSession } from '../../auth/api'

/**
 * The folder at `path` as the page shows it: the listing, the current selection, and what may be done here.
 * `filter` narrows the items shown; it never changes what is fetched.
 */
export function useBrowseListing(path: string, filter: BrowseFilter) {
  const session = useSession()
  const selectedNames = useSelectionStore((state) => state.names)
  const listing = useDirectory(path, { key: sortKey.value, order: sortOrder.value })
  const directory = useMemo(() => dirViewOf(listing.data?.pages), [listing.data?.pages])
  const entries = directory.entries
  const selected = useMemo(() => entries.filter((entry) => selectedNames.has(entry.name)), [entries, selectedNames])
  const encryption = useShareEncryption(path)
  const encrypted = encryption.data != null || encryption.isError
  const unlocked = useShareUnlocked(encryption.data?.salt)
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
  const { type, date } = filter
  const items = useMemo(() => filterItems(entries.map(toFileEntry), { type, date }, Date.now()), [entries, type, date])
  const byId = useMemo(() => new Map(entries.map((entry) => [entry.path, entry])), [entries])
  const rootLabel = path.split('/').filter(Boolean)[0]
  const root = session.data?.roots.find((entry) => entry.label === rootLabel)
  const noShares = (session.data?.roots.length ?? 0) === 0
  return {
    session,
    listing,
    directory,
    entries,
    /** What the views show: the loaded entries that pass the filter. */
    items,
    entryOf: (item: FileEntry): Entry | undefined => byId.get(item.id),
    filtered: isFiltering(filter),
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
