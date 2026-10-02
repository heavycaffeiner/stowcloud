import { describe, expect, it } from 'vitest'
import type { FileEntry } from '../../../../src/features/file-browser/model/file-entry'
import { filterItems, isFiltering, matchesDate } from '../../../../src/features/file-browser/model/filtering'

const DAY = 86_400_000
const NOW = Date.UTC(2026, 5, 15, 12)

function item(name: string, type: FileEntry['type'], age: number): FileEntry {
  return { id: `/${name}`, name, type, size: 0, modifiedAt: NOW - age }
}

const ITEMS = [
  item('Projects', 'folder', 2 * DAY),
  item('notes.md', 'document', DAY / 2),
  item('photo.jpg', 'image', 10 * DAY),
  item('song.mp3', 'audio', 40 * DAY)
]

describe('filterItems', () => {
  it('hands back the same list when nothing is filtered', () => {
    expect(isFiltering({ type: 'all', date: 'any' })).toBe(false)
    expect(filterItems(ITEMS, { type: 'all', date: 'any' }, NOW)).toBe(ITEMS)
  })

  it('keeps one kind', () => {
    const names = (type: 'folders' | 'images') => filterItems(ITEMS, { type, date: 'any' }, NOW).map((i) => i.name)
    expect(names('folders')).toEqual(['Projects'])
    expect(names('images')).toEqual(['photo.jpg'])
  })

  it('keeps items changed within the period', () => {
    const names = (date: 'today' | '7days' | '30days') =>
      filterItems(ITEMS, { type: 'all', date }, NOW).map((i) => i.name)
    expect(names('today')).toEqual(['notes.md'])
    expect(names('7days')).toEqual(['Projects', 'notes.md'])
    expect(names('30days')).toEqual(['Projects', 'notes.md', 'photo.jpg'])
  })

  it('applies the kind and the period together', () => {
    expect(filterItems(ITEMS, { type: 'documents', date: '7days' }, NOW).map((i) => i.name)).toEqual(['notes.md'])
    expect(filterItems(ITEMS, { type: 'images', date: '7days' }, NOW)).toEqual([])
  })
})

describe('matchesDate', () => {
  it('compares calendar years for this year', () => {
    expect(matchesDate(item('a', 'file', 150 * DAY), 'this_year', NOW)).toBe(true)
    expect(matchesDate(item('a', 'file', 200 * DAY), 'this_year', NOW)).toBe(false)
  })

  it('lets an item without a time through every period', () => {
    expect(matchesDate({ ...ITEMS[1], modifiedAt: 0 }, 'today', NOW)).toBe(true)
  })
})
