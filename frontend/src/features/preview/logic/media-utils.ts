// Media classification helpers for file preview and thumbnail rendering.

export const VIDEO_EXT: Record<string, true> = {
  mp4: true,
  webm: true,
  ogg: true,
  mov: true,
  mkv: true,
  avi: true,
  m4v: true,
  flv: true,
  wmv: true,
  '3gp': true
}

export const IMAGE_EXT: Record<string, true> = {
  jpg: true,
  jpeg: true,
  png: true,
  gif: true,
  webp: true,
  svg: true,
  bmp: true,
  ico: true,
  avif: true,
  tif: true,
  tiff: true
}

export function extensionOf(name: string): string {
  const i = name.lastIndexOf('.')
  return i <= 0 ? name.toLowerCase() : name.slice(i + 1).toLowerCase()
}

export function isVideoFile(name: string): boolean {
  return extensionOf(name) in VIDEO_EXT
}

export function isImageFile(name: string): boolean {
  return extensionOf(name) in IMAGE_EXT
}

/** MIME type for the browser's own `src`/`Content-Type`, keyed on exactly
 *  the extensions `IMAGE_EXT`/`VIDEO_EXT` already recognize: not a second
 *  opinion on which files are media, only what to call the ones that are. */
const MIME_BY_EXT: Record<string, string> = {
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  png: 'image/png',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  bmp: 'image/bmp',
  ico: 'image/x-icon',
  avif: 'image/avif',
  tif: 'image/tiff',
  tiff: 'image/tiff',
  mp4: 'video/mp4',
  webm: 'video/webm',
  ogg: 'video/ogg',
  mov: 'video/quicktime',
  mkv: 'video/x-matroska',
  avi: 'video/x-msvideo',
  m4v: 'video/x-m4v',
  flv: 'video/x-flv',
  wmv: 'video/x-ms-wmv',
  '3gp': 'video/3gpp'
}

/** `null` for an extension outside `IMAGE_EXT`/`VIDEO_EXT`: a caller
 *  serving an encrypted file's decrypted bytes still needs some value, so
 *  it falls back to `application/octet-stream` itself rather than this
 *  function guessing at a type for a kind of file it does not classify as
 *  media in the first place. */
export function mimeTypeOf(name: string): string | null {
  return MIME_BY_EXT[extensionOf(name)] ?? null
}
