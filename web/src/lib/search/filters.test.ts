import { describe, expect, it } from 'vitest'
import { EXTENSION_PRESETS, MAX_EXTS, extensionOf, parseExtensions, resolveExtensions } from './filters'

describe('the typed extension list', () => {
  it('accepts the separators people actually use', () => {
    expect(parseExtensions('pdf, txt;md  hwp\ndocx')).toEqual(['pdf', 'txt', 'md', 'hwp', 'docx'])
  })

  it('reads a dotted or wildcarded extension as the same extension', () => {
    expect(parseExtensions('.PDF *.txt *')).toEqual(['pdf', 'txt'])
  })

  it('keeps one of each', () => {
    expect(parseExtensions('pdf pdf PDF')).toEqual(['pdf'])
  })

  it('stops at the server ceiling rather than sending a list it refuses', () => {
    const many = Array.from({ length: MAX_EXTS + 10 }, (_, i) => `e${i}`).join(',')
    expect(parseExtensions(many)).toHaveLength(MAX_EXTS)
  })

  it('reads nothing out of an empty box', () => {
    expect(parseExtensions('   ')).toEqual([])
  })
})

describe('the extensions one search sends', () => {
  it('is the presets and the typed list together', () => {
    const got = resolveExtensions(['audio'], 'cue')
    expect(got).toContain('mp3')
    expect(got).toContain('cue')
  })

  it('never repeats an extension two presets share', () => {
    const got = resolveExtensions(['image', 'image'], 'png')
    expect(got.filter((e) => e === 'png')).toHaveLength(1)
  })

  it('ignores a preset id that no longer exists', () => {
    expect(resolveExtensions(['nonesuch'], 'pdf')).toEqual(['pdf'])
  })

  it('carries every extension of every group at once, with room to spare', () => {
    const everyPreset = EXTENSION_PRESETS.map((p) => p.id)
    const distinct = new Set(EXTENSION_PRESETS.flatMap((p) => p.exts))
    // Nothing is dropped: a filter cut down to fit would quietly leave
    // matching files out of a search that promises all of them.
    expect(resolveExtensions(everyPreset, '')).toHaveLength(distinct.size)
    expect(distinct.size).toBeLessThanOrEqual(MAX_EXTS)
  })

  it('keeps the two widest groups whole together', () => {
    const both = resolveExtensions(['document', 'code'], '')
    expect(both).toContain('hwp')
    expect(both).toContain('css')
  })

  it('is empty when nothing narrows it, which is what the server reads as any', () => {
    expect(resolveExtensions([], '')).toEqual([])
  })
})

describe('the extension of a name', () => {
  it('follows the dot convention the server does', () => {
    expect(extensionOf('archive.tar.gz')).toBe('gz')
    expect(extensionOf('REPORT.PDF')).toBe('pdf')
    expect(extensionOf('.bashrc')).toBe('')
    expect(extensionOf('report.')).toBe('')
    expect(extensionOf('report')).toBe('')
  })
})
