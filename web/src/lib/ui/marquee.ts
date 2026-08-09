// marquee.ts: turning a dragged rectangle into the rows it covers.
//
// The listing is virtualized, so hit-testing DOM nodes would only ever find
// the handful of rows currently rendered and would miss everything the
// rectangle crosses while the page auto-scrolls under it. Rows are laid out on
// a fixed pitch, though, so the covered indices are arithmetic. That is what
// this module does, and it does it without a DOM so it can be tested directly.
//
// Coordinates are document coordinates throughout (client + scroll offset).
// Viewport coordinates would move under the drag every time it scrolls.

/** Below this much movement the gesture is a click, not a drag. */
export const DRAG_THRESHOLD_PX = 5

export interface Rect {
  left: number
  top: number
  right: number
  bottom: number
}

/** The rectangle between two points, whichever corner the drag started from. */
export function rectBetween(ax: number, ay: number, bx: number, by: number): Rect {
  return {
    left: Math.min(ax, bx),
    top: Math.min(ay, by),
    right: Math.max(ax, bx),
    bottom: Math.max(ay, by)
  }
}

export function movedFar(ax: number, ay: number, bx: number, by: number): boolean {
  return Math.abs(bx - ax) >= DRAG_THRESHOLD_PX || Math.abs(by - ay) >= DRAG_THRESHOLD_PX
}

/**
 * One run of same-height rows: a whole `FileTable`, or one of `FileGrid`'s two
 * card sections.
 */
export interface SectionGeometry {
  /** Document Y of the top of the section's first row. */
  top: number
  /** Document X of the left edge of column 0. */
  left: number
  /** Row pitch: the drawn height plus whatever separates one row from the next. */
  rowHeight: number
  /** Column pitch: cell width plus gap. Ignored when `columns` is 1. */
  columnPitch: number
  /** Drawn width of one cell. The rest of the pitch is the gap between them. */
  cellWidth: number
  columns: number
  /** Where this section's first entry sits in the listing as a whole. */
  startIndex: number
  count: number
}

/**
 * Every listing index whose cell the rectangle touches.
 *
 * A single-column section ignores the rectangle's x entirely: its rows span
 * the width of the list, so a drag anywhere across one has crossed it. Only a
 * real grid tests columns, and there the gaps between cards are dead space,
 * which is why the cell width and the column pitch are separate numbers.
 */
export function indicesInRect(rect: Rect, g: SectionGeometry): number[] {
  if (g.count <= 0 || g.columns <= 0 || g.rowHeight <= 0) return []

  const rowCount = Math.ceil(g.count / g.columns)
  const firstRow = Math.floor((rect.top - g.top) / g.rowHeight)
  const lastRow = Math.floor((rect.bottom - g.top) / g.rowHeight)
  const from = Math.max(0, firstRow)
  const to = Math.min(rowCount - 1, lastRow)
  if (to < from) return []

  const out: number[] = []
  for (let row = from; row <= to; row++) {
    for (let col = 0; col < g.columns; col++) {
      const offset = row * g.columns + col
      if (offset >= g.count) break
      if (g.columns > 1) {
        const cellLeft = g.left + col * g.columnPitch
        if (cellLeft + g.cellWidth < rect.left || cellLeft > rect.right) continue
      }
      out.push(g.startIndex + offset)
    }
  }
  return out
}

/**
 * How far to scroll this frame when the pointer is held near the top or bottom
 * of the viewport, so a drag can reach past one screenful. Zero anywhere in
 * the middle. Proportional to how deep into the edge zone the pointer is, so
 * nudging the edge creeps and pinning to it runs.
 *
 * `y` is in viewport coordinates: this is about where the pointer is on the
 * screen, not where it is in the document.
 */
export const AUTOSCROLL_ZONE_PX = 56
export const AUTOSCROLL_MAX_PX_PER_FRAME = 24

export function autoScrollStep(y: number, viewportHeight: number): number {
  if (y < AUTOSCROLL_ZONE_PX) {
    const depth = (AUTOSCROLL_ZONE_PX - y) / AUTOSCROLL_ZONE_PX
    return -Math.ceil(Math.min(1, depth) * AUTOSCROLL_MAX_PX_PER_FRAME)
  }
  const fromBottom = viewportHeight - y
  if (fromBottom < AUTOSCROLL_ZONE_PX) {
    const depth = (AUTOSCROLL_ZONE_PX - fromBottom) / AUTOSCROLL_ZONE_PX
    return Math.ceil(Math.min(1, depth) * AUTOSCROLL_MAX_PX_PER_FRAME)
  }
  return 0
}
