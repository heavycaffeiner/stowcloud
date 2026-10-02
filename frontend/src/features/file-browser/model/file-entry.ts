// The file browser's view of a directory entry. Components take this rather than the API's Entry,
// so the server's wire format stays out of the UI.
import { isVideoFile } from '../../preview/logic/media-utils'
import type { Entry } from '../api'

export type FileType = 'folder' | 'archive' | 'app' | 'audio' | 'video' | 'image' | 'code' | 'document' | 'file'

export interface FileEntry {
  /** Unique across folders, unlike the name. */
  readonly id: string
  readonly name: string
  readonly type: FileType
  /** Bytes; for a folder, everything inside it. */
  readonly size: number
  /** Milliseconds since the epoch. */
  readonly modifiedAt: number
}

const EXTENSIONS: readonly (readonly [FileType, ReadonlySet<string>])[] = [
  ['archive', new Set(['zip', '7z', 'rar', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'iso'])],
  ['app', new Set(['apk'])],
  ['audio', new Set(['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'])],
  ['video', new Set(['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg'])],
  ['image', new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'heif', 'avif'])],
  [
    'code',
    new Set([
      'js',
      'ts',
      'tsx',
      'jsx',
      'go',
      'rs',
      'py',
      'java',
      'c',
      'cpp',
      'h',
      'cs',
      'rb',
      'php',
      'sh',
      'sql',
      'json',
      'yaml',
      'yml',
      'toml',
      'xml',
      'html',
      'css'
    ])
  ],
  [
    'document',
    new Set([
      'pdf',
      'doc',
      'docx',
      'xls',
      'xlsx',
      'ppt',
      'pptx',
      'odt',
      'ods',
      'odp',
      'txt',
      'md',
      'rtf',
      'hwp',
      'hwpx',
      'csv'
    ])
  ]
]

/** The lowercased extension, or '' when the name has none. A leading dot does not start one. */
export function extensionOf(name: string): string {
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : ''
}

/** What a file is, judged by its name. `previewable` marks a file the server can render as an image. */
export function fileTypeOf(name: string, folder: boolean, previewable = false): FileType {
  if (folder) return 'folder'
  const known = EXTENSIONS.find(([, extensions]) => extensions.has(extensionOf(name)))?.[0]
  if (known) return known
  if (isVideoFile(name)) return 'video'
  return previewable ? 'image' : 'file'
}

export function toFileEntry(entry: Entry): FileEntry {
  return {
    id: entry.path,
    name: entry.name,
    type: fileTypeOf(entry.name, entry.kind === 'dir', entry.preview?.available === true),
    size: entry.size,
    modifiedAt: Number(BigInt(entry.mtime_ns || '0') / 1_000_000n)
  }
}
