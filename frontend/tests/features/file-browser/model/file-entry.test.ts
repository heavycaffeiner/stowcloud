import { describe, expect, it } from 'vitest'
import type { Entry } from '../../../../src/features/file-browser/api'
import { extensionOf, fileTypeOf, toFileEntry } from '../../../../src/features/file-browser/model/file-entry'

describe('extensionOf', () => {
  it('lowercases the part after the last dot', () => {
    expect(extensionOf('Report.Final.PDF')).toBe('pdf')
  })

  it('finds none in a dotfile or a name without a dot', () => {
    expect(extensionOf('.bashrc')).toBe('')
    expect(extensionOf('Makefile')).toBe('')
  })
})

describe('fileTypeOf', () => {
  it('calls every directory a folder whatever its name', () => {
    expect(fileTypeOf('photos.zip', true)).toBe('folder')
  })

  it('judges a file by its extension', () => {
    expect(fileTypeOf('a.tar', false)).toBe('archive')
    expect(fileTypeOf('a.apk', false)).toBe('app')
    expect(fileTypeOf('a.flac', false)).toBe('audio')
    expect(fileTypeOf('a.mkv', false)).toBe('video')
    expect(fileTypeOf('a.HEIC', false)).toBe('image')
    expect(fileTypeOf('a.tsx', false)).toBe('code')
    expect(fileTypeOf('a.hwpx', false)).toBe('document')
  })

  it('takes an unknown extension as an image only when the server can preview it', () => {
    expect(fileTypeOf('scan.raw', false)).toBe('file')
    expect(fileTypeOf('scan.raw', false, true)).toBe('image')
  })
})

describe('toFileEntry', () => {
  const entry: Entry = {
    name: 'clip.mp4',
    path: '/media/clip.mp4',
    kind: 'file',
    size: 2048,
    mtime_ns: '1700000000123456789',
    etag: 'clip',
    etag_weak: false,
    perms: {
      read: true,
      write: true,
      create: true,
      delete: true,
      rename: true,
      move: true,
      share: true,
      download: true
    }
  }

  it('keys the item by its path and converts the time to milliseconds', () => {
    expect(toFileEntry(entry)).toEqual({
      id: '/media/clip.mp4',
      name: 'clip.mp4',
      type: 'video',
      size: 2048,
      modifiedAt: 1_700_000_000_123
    })
  })

  it('reads a missing time as zero', () => {
    expect(toFileEntry({ ...entry, mtime_ns: '' }).modifiedAt).toBe(0)
  })
})
