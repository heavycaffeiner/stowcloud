import { describe, expect, it } from 'vitest'
import {
  autoScrollStep,
  AUTOSCROLL_MAX_PX_PER_FRAME,
  indicesInRect,
  movedFar,
  rectBetween,
  type SectionGeometry
} from './marquee'

/** A list: rows 48px tall starting at document y=100, one column. */
const list: SectionGeometry = {
  top: 100,
  left: 0,
  rowHeight: 48,
  columnPitch: 0,
  cellWidth: 0,
  columns: 1,
  startIndex: 0,
  count: 10
}

/** A grid: 4 columns of 200px cells on a 212px pitch, rows 220px tall. */
const grid: SectionGeometry = {
  top: 500,
  left: 16,
  rowHeight: 220,
  columnPitch: 212,
  cellWidth: 200,
  columns: 4,
  startIndex: 1000,
  count: 10
}

describe('rectBetween', () => {
  it('normalises whichever corner the drag started from', () => {
    expect(rectBetween(30, 40, 10, 20)).toEqual({ left: 10, top: 20, right: 30, bottom: 40 })
    expect(rectBetween(10, 20, 30, 40)).toEqual({ left: 10, top: 20, right: 30, bottom: 40 })
  })
})

describe('movedFar', () => {
  it('treats a few pixels of jitter as a click, not a drag', () => {
    expect(movedFar(100, 100, 102, 101)).toBe(false)
    expect(movedFar(100, 100, 106, 100)).toBe(true)
    expect(movedFar(100, 100, 100, 94)).toBe(true)
  })
})

describe('indicesInRect over a list', () => {
  it('takes every row the rectangle crosses', () => {
    // Rows 0..9 occupy y 100..580. 150..250 touches rows 1 and 3's edges.
    expect(indicesInRect({ left: 0, top: 150, right: 400, bottom: 250 }, list)).toEqual([1, 2, 3])
  })

  it('takes the single row a rectangle inside one row touches', () => {
    expect(indicesInRect({ left: 0, top: 110, right: 400, bottom: 120 }, list)).toEqual([0])
  })

  // Rows span the width of the list, so a drag down its right-hand margin has
  // still crossed them. Testing x here would select nothing at all.
  it('ignores x', () => {
    expect(indicesInRect({ left: 9000, top: 150, right: 9001, bottom: 200 }, list)).toEqual([1, 2])
  })

  it('clamps to the rows that exist', () => {
    expect(indicesInRect({ left: 0, top: -5000, right: 400, bottom: 5000 }, list)).toHaveLength(10)
    expect(indicesInRect({ left: 0, top: 0, right: 400, bottom: 50 }, list)).toEqual([])
    expect(indicesInRect({ left: 0, top: 700, right: 400, bottom: 800 }, list)).toEqual([])
  })

  it('finds nothing in an empty section', () => {
    expect(indicesInRect({ left: 0, top: 0, right: 999, bottom: 999 }, { ...list, count: 0 })).toEqual([])
  })
})

describe('indicesInRect over a grid', () => {
  it('offsets by the section start, so the answer indexes the whole listing', () => {
    expect(indicesInRect({ left: 0, top: 500, right: 9999, bottom: 600 }, grid)).toEqual([
      1000, 1001, 1002, 1003
    ])
  })

  it('takes only the columns the rectangle reaches', () => {
    // Column 1 spans x 228..428, column 2 spans 440..640.
    expect(indicesInRect({ left: 300, top: 510, right: 450, bottom: 520 }, grid)).toEqual([1001, 1002])
  })

  // The 12px between two cards belongs to neither of them. A rectangle that
  // only ever sat in a gap has touched no card.
  it('does not take a card from the gap beside it', () => {
    expect(indicesInRect({ left: 430, top: 510, right: 436, bottom: 520 }, grid)).toEqual([])
  })

  it('stops at the last entry of a short final row', () => {
    // 10 entries at 4 columns: the third row holds only 1008 and 1009.
    expect(indicesInRect({ left: 0, top: 940, right: 9999, bottom: 960 }, grid)).toEqual([1008, 1009])
  })
})

describe('autoScrollStep', () => {
  it('is still in the middle of the viewport', () => {
    expect(autoScrollStep(400, 800)).toBe(0)
    expect(autoScrollStep(56, 800)).toBe(0)
    expect(autoScrollStep(744, 800)).toBe(0)
  })

  it('runs up near the top and down near the bottom', () => {
    expect(autoScrollStep(50, 800)).toBeLessThan(0)
    expect(autoScrollStep(790, 800)).toBeGreaterThan(0)
  })

  it('creeps at the edge of the zone and runs at the edge of the screen', () => {
    expect(Math.abs(autoScrollStep(55, 800))).toBeLessThan(AUTOSCROLL_MAX_PX_PER_FRAME)
    expect(autoScrollStep(0, 800)).toBe(-AUTOSCROLL_MAX_PX_PER_FRAME)
    expect(autoScrollStep(800, 800)).toBe(AUTOSCROLL_MAX_PX_PER_FRAME)
  })

  it('does not exceed the cap past the edge of the screen', () => {
    expect(autoScrollStep(-500, 800)).toBe(-AUTOSCROLL_MAX_PX_PER_FRAME)
    expect(autoScrollStep(1500, 800)).toBe(AUTOSCROLL_MAX_PX_PER_FRAME)
  })
})
