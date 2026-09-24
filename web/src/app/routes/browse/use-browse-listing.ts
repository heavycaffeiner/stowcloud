import { useInfiniteQuery, useQueries, useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import type { Entry } from '../../../lib/api/client'
import { joinPath } from '../../../lib/api/path-utils'
import { isUnlocked } from '../../../lib/crypto/e2ee'
import { dirListQuery, dirViewOf, folderSizeQuery, shareEncryptionQuery } from '../../../lib/query/files'
import { sessionQuery } from '../../../lib/query/session'
import { useStore } from '../../../lib/store/use-store'
import { selection } from '../../../lib/store/selection.store'
import { view } from '../../../lib/store/view.store'
import { isVideoFile } from '../../../features/preview/media-utils'
import type { BrowseFilterDate, BrowseFilterType } from './types'

const DOCUMENT_EXTENSIONS = new Set(['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'hwp', 'hwpx', 'csv'])
const IMAGE_EXTENSIONS = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'heic', 'heif', 'bmp', 'svg', 'avif'])
const VIDEO_EXTENSIONS = new Set(['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg'])
const AUDIO_EXTENSIONS = new Set(['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'])
const ARCHIVE_EXTENSIONS = new Set(['zip', '7z', 'rar', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'iso'])

function matchesType(entry: Entry, filter: BrowseFilterType): boolean {
  if (filter === 'all') return true
  if (filter === 'folders') return entry.kind === 'dir'
  if (entry.kind === 'dir') return false
  const dot = entry.name.lastIndexOf('.')
  const ext = dot > 0 ? entry.name.slice(dot + 1).toLowerCase() : ''
  if (filter === 'documents') return DOCUMENT_EXTENSIONS.has(ext)
  if (filter === 'images') return IMAGE_EXTENSIONS.has(ext)
  if (filter === 'videos') return VIDEO_EXTENSIONS.has(ext) || isVideoFile(entry.name)
  if (filter === 'audio') return AUDIO_EXTENSIONS.has(ext)
  if (filter === 'archives') return ARCHIVE_EXTENSIONS.has(ext)
  return false
}

function matchesDate(entry: Entry, filter: BrowseFilterDate, now: number): boolean {
  if (filter === 'any') return true
  const mtimeMs = Number(BigInt(entry.mtime_ns || '0') / 1000000n)
  if (mtimeMs <= 0) return true
  if (filter === 'this_year') return new Date(mtimeMs).getFullYear() === new Date(now).getFullYear()
  const diff = now - mtimeMs
  if (filter === 'today') return diff <= 86400000
  if (filter === '7days') return diff <= 7 * 86400000
  if (filter === '30days') return diff <= 30 * 86400000
  return true
}

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
    return entries.filter((entry) => matchesType(entry, filterType) && matchesDate(entry, filterDate, now))
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
