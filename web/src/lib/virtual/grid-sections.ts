// grid-sections.ts: where a flat listing index sits once the grid view has
// split the listing into a folder section and a file section.
//
// The server sorts folders ahead of files, so the listing is still one array
// addressed by one index; the grid just draws it as two blocks with different
// card heights. Everything that has to reason about "the cell below this one"
// therefore has to know about the seam, and that reasoning is here rather than
// in the component so it can be tested without a DOM.
//
// Both sections lay out on the same column count (the two card types share a
// width), which is what makes "same column, next section" a meaningful move.

export interface CellPos {
  /** 0 = folders, 1 = files. */
  section: 0 | 1
  /** Row within that section, not within the grid as a whole. */
  row: number
  col: number
}

/** How many rows of `columns` cells `count` items need. */
export function sectionRows(count: number, columns: number): number {
  if (count <= 0 || columns <= 0) return 0
  return Math.ceil(count / columns)
}

export function cellPos(index: number, dirs: number, columns: number): CellPos {
  if (columns <= 0) return { section: 0, row: 0, col: 0 }
  if (index < dirs) return { section: 0, row: Math.floor(index / columns), col: index % columns }
  const j = index - dirs
  return { section: 1, row: Math.floor(j / columns), col: j % columns }
}

/**
 * The index an up/down arrow should land on, `dir` being +1 for down.
 *
 * A move that leaves its own section crosses into the other one at the same
 * column rather than at the same index: the seam almost never falls on a
 * column boundary, so "index + columns" would step diagonally exactly once,
 * at the one place a user is most likely to notice. A move that leaves the
 * grid entirely stays put.
 */
export function verticalTarget(
  index: number,
  dir: 1 | -1,
  dirs: number,
  total: number,
  columns: number
): number {
  if (total <= 0 || columns <= 0) return 0
  const from = Math.min(Math.max(index, 0), total - 1)
  const files = total - dirs
  const pos = cellPos(from, dirs, columns)
  const start = pos.section === 0 ? 0 : dirs
  const count = pos.section === 0 ? dirs : files

  const nextRow = pos.row + dir
  if (nextRow >= 0 && nextRow < sectionRows(count, columns)) {
    return Math.min(start + nextRow * columns + pos.col, start + count - 1)
  }
  if (dir === 1 && pos.section === 0 && files > 0) {
    return dirs + Math.min(pos.col, files - 1)
  }
  if (dir === -1 && pos.section === 1 && dirs > 0) {
    const lastRow = sectionRows(dirs, columns) - 1
    return Math.min(lastRow * columns + pos.col, dirs - 1)
  }
  return from
}
