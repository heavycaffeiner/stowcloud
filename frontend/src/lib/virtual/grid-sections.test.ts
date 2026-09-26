import { describe, expect, it } from 'vitest'
import { cellPos, sectionRows, verticalTarget } from './grid-sections'

// A listing of 5 folders and 7 files at 4 columns:
//
//   folders  0 1 2 3
//            4
//   files    5 6 7 8
//            9 10 11
const DIRS = 5
const TOTAL = 12
const COLS = 4

describe('cellPos', () => {
  it('numbers rows from the top of each section, not the top of the grid', () => {
    expect(cellPos(4, DIRS, COLS)).toEqual({ section: 0, row: 1, col: 0 })
    expect(cellPos(5, DIRS, COLS)).toEqual({ section: 1, row: 0, col: 0 })
    expect(cellPos(11, DIRS, COLS)).toEqual({ section: 1, row: 1, col: 2 })
  })
})

describe('sectionRows', () => {
  it('is zero for an empty section and never fractional', () => {
    expect(sectionRows(0, COLS)).toBe(0)
    expect(sectionRows(1, COLS)).toBe(1)
    expect(sectionRows(9, COLS)).toBe(3)
  })
})

describe('verticalTarget', () => {
  it('moves a whole row within a section', () => {
    expect(verticalTarget(0, 1, DIRS, TOTAL, COLS)).toBe(4)
    expect(verticalTarget(9, -1, DIRS, TOTAL, COLS)).toBe(5)
  })

  // The reason this module exists. The seam is at index 5, which is not a
  // column boundary, so "index + columns" would step sideways exactly once and
  // exactly where it is most noticeable. Index 4 is the only folder in its
  // row; down goes to column 0 of the files, index 5.
  it('crosses into the file section at the same column, not the same index', () => {
    expect(verticalTarget(4, 1, DIRS, TOTAL, COLS)).toBe(5)
    // Four folders, so their one row is full and every column crosses.
    expect(verticalTarget(1, 1, 4, 11, COLS)).toBe(5)
    expect(verticalTarget(3, 1, 4, 11, COLS)).toBe(7)
  })

  // A short last row inside a section clamps rather than skipping ahead to
  // the next section: index 1 sits above a row that only holds index 4.
  it('clamps to the end of a short row instead of leaving the section early', () => {
    expect(verticalTarget(1, 1, DIRS, TOTAL, COLS)).toBe(4)
    expect(verticalTarget(3, 1, DIRS, TOTAL, COLS)).toBe(4)
  })

  it('crosses back into the last folder row at the same column', () => {
    // Column 0 of the first file row goes to index 4, the only cell in the
    // folder section's last row.
    expect(verticalTarget(5, -1, DIRS, TOTAL, COLS)).toBe(4)
    // Column 2 has no folder above it in that row, so it clamps to the last
    // folder rather than jumping a row further up.
    expect(verticalTarget(7, -1, DIRS, TOTAL, COLS)).toBe(4)
  })

  it('clamps to the last cell rather than falling off the end of a short row', () => {
    expect(verticalTarget(7, 1, DIRS, TOTAL, COLS)).toBe(11)
    expect(verticalTarget(8, 1, DIRS, TOTAL, COLS)).toBe(11)
  })

  it('stays put at the top and bottom edges of the grid', () => {
    expect(verticalTarget(0, -1, DIRS, TOTAL, COLS)).toBe(0)
    expect(verticalTarget(11, 1, DIRS, TOTAL, COLS)).toBe(11)
  })

  it('handles a folder-only and a file-only listing', () => {
    expect(verticalTarget(1, 1, 6, 6, COLS)).toBe(5)
    expect(verticalTarget(1, 1, 0, 6, COLS)).toBe(5)
    expect(verticalTarget(5, -1, 0, 6, COLS)).toBe(1)
  })

  it('answers 0 for an empty listing instead of a negative index', () => {
    expect(verticalTarget(0, 1, 0, 0, COLS)).toBe(0)
    expect(verticalTarget(0, -1, 0, 0, COLS)).toBe(0)
  })
})
