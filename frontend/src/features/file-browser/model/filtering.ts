// Narrowing a folder's listing by kind and age. It runs on what has loaded; the server never filters.
import type { FileEntry, FileType } from './file-entry'

export const FILTER_TYPES = ['all', 'folders', 'documents', 'images', 'videos', 'audio', 'archives'] as const
export const FILTER_DATES = ['any', 'today', '7days', '30days', 'this_year'] as const
export type BrowseFilterType = (typeof FILTER_TYPES)[number]
export type BrowseFilterDate = (typeof FILTER_DATES)[number]

export interface BrowseFilter {
  readonly type: BrowseFilterType
  readonly date: BrowseFilterDate
}

const TYPE_OF_FILTER: Record<Exclude<BrowseFilterType, 'all'>, FileType> = {
  folders: 'folder',
  documents: 'document',
  images: 'image',
  videos: 'video',
  audio: 'audio',
  archives: 'archive'
}

const DAY_MS = 86_400_000

export function isFiltering(filter: BrowseFilter): boolean {
  return filter.type !== 'all' || filter.date !== 'any'
}

export function matchesType(item: FileEntry, filter: BrowseFilterType): boolean {
  return filter === 'all' || item.type === TYPE_OF_FILTER[filter]
}

/** An item without a modification time passes every date filter. */
export function matchesDate(item: FileEntry, filter: BrowseFilterDate, now: number): boolean {
  if (filter === 'any' || item.modifiedAt <= 0) return true
  if (filter === 'this_year') return new Date(item.modifiedAt).getFullYear() === new Date(now).getFullYear()
  const days = filter === 'today' ? 1 : filter === '7days' ? 7 : 30
  return now - item.modifiedAt <= days * DAY_MS
}

export function filterItems(items: readonly FileEntry[], filter: BrowseFilter, now: number): readonly FileEntry[] {
  if (!isFiltering(filter)) return items
  return items.filter((item) => matchesType(item, filter.type) && matchesDate(item, filter.date, now))
}
