import { describe, expect, it } from 'vitest'
import { isVideoFile, isImageFile, extensionOf, VIDEO_EXT, IMAGE_EXT } from './media-utils'
const TEXT_EXT: Record<string, true> = {
  txt: true, md: true, markdown: true, log: true, csv: true, tsv: true, json: true, yaml: true, yml: true, toml: true, ini: true, conf: true,
  cfg: true, xml: true, html: true, htm: true, css: true, scss: true, js: true, ts: true, jsx: true, tsx: true, svelte: true, vue: true, rs: true,
  go: true, py: true, rb: true, php: true, java: true, kt: true, c: true, h: true, cpp: true, hpp: true, cs: true, sh: true, bash: true, zsh: true,
  sql: true, env: true, gitignore: true, dockerfile: true, makefile: true
}


function classify(name: string, previewAvailable = false): 'image' | 'video' | 'archive' | 'text' | 'none' {
  const ext = extensionOf(name)
  if (ext in VIDEO_EXT) return 'video'
  if (ext in IMAGE_EXT || previewAvailable) return 'image'
  if (ext === 'zip') return 'archive'
  if (ext in TEXT_EXT) return 'text'
  return 'none'
}

describe('preview media classification', () => {
  it('classifies video extensions as video', () => {
    for (const ext of ['mp4', 'webm', 'mov', 'mkv', 'avi', 'ogg', 'm4v', '3gp']) {
      expect(classify(`sample.${ext}`)).toBe('video')
      expect(classify(`SAMPLE.${ext.toUpperCase()}`)).toBe('video')
    }
  })

  it('classifies common web image extensions as image', () => {
    for (const ext of ['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico', 'avif']) {
      expect(classify(`photo.${ext}`)).toBe('image')
    }
  })

  it('classifies server previewable entries as image even without known extension', () => {
    expect(classify('blob.custom', true)).toBe('image')
  })

  it('classifies archives and text files correctly', () => {
    expect(classify('archive.zip')).toBe('archive')
    expect(classify('document.txt')).toBe('text')
    expect(classify('script.py')).toBe('text')
  })

  it('classifies unknown binary files as none', () => {
    expect(classify('program.exe')).toBe('none')
    expect(classify('firmware.bin')).toBe('none')
  })
})

describe('Thumbnail isVideoFile helper', () => {
  it('detects video extensions correctly', () => {
    for (const ext of Object.keys(VIDEO_EXT)) {
      expect(isVideoFile(`file.${ext}`)).toBe(true)
      expect(isVideoFile(`file.${ext.toUpperCase()}`)).toBe(true)
    }
  })

  it('returns false for non-video files', () => {
    for (const name of ['photo.jpg', 'doc.pdf', 'script.py', 'archive.zip', 'file']) {
      expect(isVideoFile(name)).toBe(false)
    }
  })
})
