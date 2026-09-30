import { beforeEach, describe, expect, it } from 'vitest'
import { selection, useSelectionStore } from '../../../src/lib/store/selection.store'

const NAMES = ['a.txt', 'b.txt', 'c.txt', 'd.txt']

beforeEach(() => {
  selection.reset()
})

describe('the browse selection', () => {
  it('replaces the whole selection with a single row', () => {
    selection.all(NAMES)
    selection.only('c.txt', 2)
    expect([...useSelectionStore.getState().names]).toEqual(['c.txt'])
    expect(useSelectionStore.getState().focused).toBe(2)
  })

  it('adds and removes one row without touching the rest', () => {
    selection.only('a.txt', 0)
    selection.toggle('c.txt', 2)
    expect([...useSelectionStore.getState().names].sort()).toEqual(['a.txt', 'c.txt'])
    selection.toggle('c.txt', 2)
    expect([...useSelectionStore.getState().names]).toEqual(['a.txt'])
  })

  it('covers a range in either direction from the anchor', () => {
    selection.only('b.txt', 1)
    selection.range(NAMES, 'd.txt')
    expect([...useSelectionStore.getState().names]).toEqual(['b.txt', 'c.txt', 'd.txt'])

    selection.only('c.txt', 2)
    selection.range(NAMES, 'a.txt')
    expect([...useSelectionStore.getState().names]).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })

  it('degrades to a single row when the anchor is no longer listed', () => {
    selection.only('gone.txt', 9)
    selection.range(NAMES, 'b.txt')
    expect([...useSelectionStore.getState().names]).toEqual(['b.txt'])
    expect(useSelectionStore.getState().anchor).toBe('b.txt')
  })

  it('drops rows the rubber band has shrunk past', () => {
    selection.replace(['a.txt', 'b.txt', 'c.txt'])
    selection.replace(['a.txt'])
    expect([...useSelectionStore.getState().names]).toEqual(['a.txt'])
  })

  it('forgets the anchor when cleared, so the next range starts fresh', () => {
    selection.only('b.txt', 1)
    selection.clear()
    expect(useSelectionStore.getState().anchor).toBeNull()
    selection.range(NAMES, 'd.txt')
    expect([...useSelectionStore.getState().names]).toEqual(['d.txt'])
  })

  it('never mutates the set it hands out', () => {
    selection.only('a.txt', 0)
    const before = useSelectionStore.getState().names
    selection.toggle('b.txt', 1)
    expect([...before]).toEqual(['a.txt'])
  })
})
