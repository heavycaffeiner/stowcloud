import { describe, expect, it } from 'vitest'
import { clear, reconcile, selectAll, selectOnly, selectRange, toggle } from './selection'

const NAMES = ['a.txt', 'b.txt', 'c.txt', 'd.txt', 'e.txt']

describe('selectOnly', () => {
  it('replaces the whole selection', () => {
    const sel = new Set(['x', 'y'])
    selectOnly(sel, 'a.txt')
    expect([...sel]).toEqual(['a.txt'])
  })
})

describe('toggle (ctrl-click)', () => {
  it('adds a name not yet selected', () => {
    const sel = new Set<string>()
    toggle(sel, 'a.txt')
    expect(sel.has('a.txt')).toBe(true)
  })

  it('removes a name already selected', () => {
    const sel = new Set(['a.txt'])
    toggle(sel, 'a.txt')
    expect(sel.has('a.txt')).toBe(false)
  })

  it('does not disturb other selected names', () => {
    const sel = new Set(['a.txt', 'b.txt'])
    toggle(sel, 'c.txt')
    expect([...sel].sort()).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })
})

describe('selectRange (shift-click)', () => {
  it('selects the contiguous forward range inclusive of both ends', () => {
    const sel = new Set<string>()
    selectRange(sel, NAMES, 'b.txt', 'd.txt')
    expect([...sel].sort()).toEqual(['b.txt', 'c.txt', 'd.txt'])
  })

  it('selects the contiguous backward range the same as forward', () => {
    const sel = new Set<string>()
    selectRange(sel, NAMES, 'd.txt', 'b.txt')
    expect([...sel].sort()).toEqual(['b.txt', 'c.txt', 'd.txt'])
  })

  it('replaces any prior selection rather than adding to it', () => {
    const sel = new Set(['e.txt'])
    selectRange(sel, NAMES, 'a.txt', 'b.txt')
    expect([...sel].sort()).toEqual(['a.txt', 'b.txt'])
  })

  it('falls back to selecting just the target if the anchor vanished', () => {
    const sel = new Set<string>()
    selectRange(sel, NAMES, 'missing.txt', 'c.txt')
    expect([...sel]).toEqual(['c.txt'])
  })
})

describe('selectAll (Ctrl+A)', () => {
  it('selects every name currently listed', () => {
    const sel = new Set<string>()
    selectAll(sel, NAMES)
    expect(sel.size).toBe(NAMES.length)
    for (const n of NAMES) expect(sel.has(n)).toBe(true)
  })
})

describe('clear', () => {
  it('empties the selection', () => {
    const sel = new Set(['a.txt'])
    clear(sel)
    expect(sel.size).toBe(0)
  })
})

describe('reconcile (survives a list refresh, by name)', () => {
  it('drops names that no longer exist and keeps the rest', () => {
    const sel = new Set(['a.txt', 'gone.txt', 'c.txt'])
    reconcile(sel, new Set(NAMES))
    expect([...sel].sort()).toEqual(['a.txt', 'c.txt'])
  })

  it('never re-maps selection to a different name at the same index', () => {
    // Simulates a resort where indices shuffle but names are stable identity.
    const sel = new Set(['c.txt'])
    const reordered = new Set(['e.txt', 'd.txt', 'c.txt', 'b.txt', 'a.txt'])
    reconcile(sel, reordered)
    expect([...sel]).toEqual(['c.txt'])
  })
})
