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
