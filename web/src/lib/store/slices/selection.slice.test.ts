import { describe, expect, it } from 'vitest'
import {
  createSelectionStore,
  pureClear,
  pureReconcile,
  pureSelectAll,
  pureSelectOnly,
  pureSelectRange,
  pureToggle,
  selectionReducer
} from './selection.slice'

const NAMES = ['a.txt', 'b.txt', 'c.txt', 'd.txt', 'e.txt']

describe('pure selection functions', () => {
  it('selectOnly replaces selection with single item', () => {
    const prev = new Set(['x', 'y'])
    const next = pureSelectOnly(prev, 'a.txt')
    expect(Array.from(next)).toEqual(['a.txt'])
    expect(prev.size).toBe(2)
  })

  it('toggle adds and removes cleanly without mutation', () => {
    const empty = new Set<string>()
    const added = pureToggle(empty, 'a.txt')
    expect(added.has('a.txt')).toBe(true)
    expect(empty.has('a.txt')).toBe(false)

    const removed = pureToggle(added, 'a.txt')
    expect(removed.has('a.txt')).toBe(false)
  })

  it('selectRange handles forward and backward ranges', () => {
    const fwd = pureSelectRange(new Set(), NAMES, 'b.txt', 'd.txt')
    expect(Array.from(fwd)).toEqual(['b.txt', 'c.txt', 'd.txt'])

    const bwd = pureSelectRange(new Set(), NAMES, 'd.txt', 'b.txt')
    expect(Array.from(bwd)).toEqual(['b.txt', 'c.txt', 'd.txt'])

    const fallback = pureSelectRange(new Set(), NAMES, 'missing.txt', 'c.txt')
    expect(Array.from(fallback)).toEqual(['c.txt'])
  })

  it('selectAll selects all provided names', () => {
    const all = pureSelectAll(NAMES)
    expect(all.size).toBe(NAMES.length)
  })

  it('clear empties selection', () => {
    const cleared = pureClear()
    expect(cleared.size).toBe(0)
  })

  it('reconcile keeps only items present in the incoming set', () => {
    const sel = new Set(['a.txt', 'gone.txt', 'c.txt'])
    const reconciled = pureReconcile(sel, new Set(NAMES))
    expect(Array.from(reconciled).sort()).toEqual(['a.txt', 'c.txt'])
  })
})

describe('selectionReducer and store', () => {
  it('updates state via dispatched actions', () => {
    const store = createSelectionStore()
    expect(store.getState().selected.size).toBe(0)

    store.dispatch({ type: 'SELECT_ONLY', name: 'a.txt' })
    expect(Array.from(store.getState().selected)).toEqual(['a.txt'])

    store.dispatch({ type: 'TOGGLE', name: 'b.txt' })
    expect(Array.from(store.getState().selected).sort()).toEqual(['a.txt', 'b.txt'])

    store.dispatch({ type: 'CLEAR' })
    expect(store.getState().selected.size).toBe(0)
  })
})
