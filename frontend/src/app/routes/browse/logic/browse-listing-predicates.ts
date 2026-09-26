import type { Entry } from '../../../../lib/api/client'
import { isVideoFile } from '../../../../features/preview/logic/media-utils'
import type { BrowseFilterDate, BrowseFilterType } from './types'

const DOCUMENT_EXTENSIONS = new Set(['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'hwp', 'hwpx', 'csv'])
const IMAGE_EXTENSIONS = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'heic', 'heif', 'bmp', 'svg', 'avif'])
const VIDEO_EXTENSIONS = new Set(['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg'])
const AUDIO_EXTENSIONS = new Set(['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'])
const ARCHIVE_EXTENSIONS = new Set(['zip', '7z', 'rar', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'iso'])

export function matchesBrowseType(entry: Entry, filter: BrowseFilterType): boolean {
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

export function matchesBrowseDate(entry: Entry, filter: BrowseFilterDate, now: number): boolean {
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
