// web/src/lib/virtual/windowing.ts — pure virtual-scroll windowing maths.
// DESIGN-FRONTEND.md §5. Kept out of the Svelte component so it is testable
// without a DOM and so the maths that must "stay bounded at 100k rows" can
// be asserted directly.

export interface WindowResult {
  /** Index of the first rendered row (inclusive). */
  start: number
  /** Number of rows rendered (bounded by viewport + overscan, never itemCount). */
  count: number
  /** Index one past the last rendered row (exclusive). */
  end: number
  /** translate3d Y offset (px) for the rendered window's wrapper. */
  padTop: number
  /** Height (px) actually applied to the spacer element. Equal to
   *  itemCount * rowHeight unless the scale-factor fallback is active, in
   *  which case it is clamped to SCALE_MAPPING_THRESHOLD_PX. */
  totalHeight: number
  /** True when itemCount * rowHeight exceeds the safe scroll-height ceiling
   *  and scroll position had to be remapped through a scale factor. */
  scaled: boolean
}

/**
 * Browser scroll-height ceilings: Chrome ~33.5M px, Firefox ~17.8M px.
 * Recorded for reference (DESIGN-FRONTEND.md §5) — the mapping itself kicks
 * in earlier, at SCALE_MAPPING_THRESHOLD_PX, so there is headroom below the
 * tighter (Firefox) ceiling even for browsers we haven't measured.
 */
export const FIREFOX_SCROLL_HEIGHT_LIMIT_PX = 17_800_000
export const CHROME_SCROLL_HEIGHT_LIMIT_PX = 33_500_000

/**
 * Above this natural height (itemCount * rowHeight), a real spacer element
 * would risk clamping by the browser (DESIGN-FRONTEND.md §5). Past this
 * point we cap the spacer at this height and remap scroll position to row
 * index through a linear scale factor instead of pretending scrollTop can
 * address itemCount * rowHeight px of real scrollable space.
 */
export const SCALE_MAPPING_THRESHOLD_PX = 15_000_000

export interface ScaleMapping {
  /** True once itemCount * rowHeight exceeds SCALE_MAPPING_THRESHOLD_PX. */
  active: boolean
  /** Height (px) actually applied to the spacer element. */
  spacerHeight: number
  /** spacerHeight / naturalHeight. 1 when inactive; < 1 once remapping. */
  scale: number
}

/** Pure function: decides whether/how to compress itemCount rows of rowHeight px into a safe scrollable range. */
export function computeScaleMapping(itemCount: number, rowHeight: number): ScaleMapping {
  const natural = Math.max(0, itemCount) * Math.max(0, rowHeight)
  if (natural <= SCALE_MAPPING_THRESHOLD_PX) {
    return { active: false, spacerHeight: natural, scale: 1 }
  }
  return {
    active: true,
    spacerHeight: SCALE_MAPPING_THRESHOLD_PX,
    scale: SCALE_MAPPING_THRESHOLD_PX / natural
  }
}

/** Maps a scrollTop expressed in *compressed* (spacer) coordinates back to the row index it represents. */
export function scrollTopToRowIndex(
  scrollTop: number,
  mapping: ScaleMapping,
  rowHeight: number,
  itemCount: number
): number {
  if (itemCount <= 0 || rowHeight <= 0) return 0
  const virtualScrollTop = mapping.active ? scrollTop / mapping.scale : scrollTop
  const idx = Math.floor(virtualScrollTop / rowHeight)
  return Math.min(Math.max(0, idx), Math.max(0, itemCount - 1))
}

/** Inverse of scrollTopToRowIndex: compressed-space Y offset (px) for a given row index. */
export function rowIndexToScrollTop(rowIndex: number, mapping: ScaleMapping, rowHeight: number): number {
  const natural = Math.max(0, rowIndex) * rowHeight
  return mapping.active ? natural * mapping.scale : natural
}

export function computeWindow(params: {
  scrollTop: number
  viewportHeight: number
  rowHeight: number
  itemCount: number
  overscan?: number
}): WindowResult {
  const { scrollTop, viewportHeight, rowHeight, itemCount, overscan = 8 } = params

  if (itemCount <= 0 || rowHeight <= 0) {
    return { start: 0, count: 0, end: 0, padTop: 0, totalHeight: 0, scaled: false }
  }

  const mapping = computeScaleMapping(itemCount, rowHeight)

  const centerIndex = scrollTopToRowIndex(scrollTop, mapping, rowHeight, itemCount)
  const rawStart = centerIndex - overscan
  const start = Math.min(Math.max(0, rawStart), Math.max(0, itemCount - 1))

  const visibleRows = Math.ceil(viewportHeight / rowHeight)
  const rawCount = visibleRows + overscan * 2
  const count = Math.max(0, Math.min(rawCount, itemCount - start))

  // Clamp so the rendered window's bottom edge never sits past the
  // (possibly compressed) spacer height — otherwise the last rows would
  // render beyond the scrollable area once the scale-factor fallback is active.
  const naturalPadTop = start * rowHeight
  const rawPadTop = mapping.active ? naturalPadTop * mapping.scale : naturalPadTop
  const maxPadTop = Math.max(0, mapping.spacerHeight - count * rowHeight)
  const padTop = Math.min(rawPadTop, maxPadTop)

  return {
    start,
    count,
    end: start + count,
    padTop,
    totalHeight: mapping.spacerHeight,
    scaled: mapping.active
  }
}

/** True once itemCount * rowHeight would need the scale-factor fallback (computeScaleMapping(...).active). */
export function exceedsSafeScrollHeight(itemCount: number, rowHeight: number): boolean {
  return computeScaleMapping(itemCount, rowHeight).active
}

/**
 * FileTable/FileGrid used to be their own `overflow: auto` scroll container
 * and read `scrollTop`/`clientHeight` straight off the scroll event — see
 * the note above `.sc-file-table` in FileTable.svelte. A browser only
 * collapses its address bar chrome when *the document* scrolls, and this
 * app's document never did (the shell clipped everything with
 * `overflow: hidden` and scrolled an inner box instead) — so the address
 * bar permanently ate ~56px of every phone screen. Fixing that means the
 * document has to become the scroller, which turns `computeWindow`'s two
 * inputs into a call-site problem rather than an algorithm problem: the
 * maths below only need "how far down" and "how tall the visible area is",
 * not which element does the scrolling. These two pure functions are that
 * translation, kept out of the component (like the rest of this file) so
 * they're testable without mounting anything.
 */

/**
 * `scrollTop` in document-scroll terms: how far the *viewport element's own
 * top edge* has scrolled past the top of the window, clamped at 0 for the
 * (normal, if the viewport sits below other page content) case where the
 * element hasn't reached the top of the screen yet — `computeWindow` treats
 * a negative scrollTop as "nothing scrolled" and this keeps callers from
 * having to reason about that themselves.
 *
 * @param windowScrollY `window.scrollY` at read time.
 * @param viewportDocumentTop The viewport element's own top edge, in
 *   document coordinates (`getBoundingClientRect().top + window.scrollY`,
 *   captured before scrollY moves it) — not `offsetTop`, which is relative
 *   to the nearest *positioned* ancestor and would silently give the wrong
 *   number the day something between here and the shell gets
 *   `position: relative` for an unrelated reason.
 */
export function documentScrollTop(windowScrollY: number, viewportDocumentTop: number): number {
  return Math.max(0, windowScrollY - viewportDocumentTop)
}

/**
 * The height available to render rows in, in document-scroll terms.
 *
 * `window.innerHeight` is the layout viewport and is what most call sites
 * should reach for by default, but it is not the number that tracks a
 * mobile browser's chrome mid-collapse: `visualViewport` exists specifically
 * because `innerHeight` historically lagged (or on some engines, never
 * updated at all) while the address bar was animating, so a virtualized
 * list sized off `innerHeight` alone would either under-render (a gap
 * appears at the bottom as the chrome shrinks and the true visible area
 * grows past what was measured) or hold stale overscan a beat too long.
 * `visualViewport.height` is preferred whenever it exists; `innerHeight` is
 * the fallback for engines (or the jsdom test environment) that lack it.
 */
export function effectiveViewportHeight(visualViewportHeight: number | undefined | null, windowInnerHeight: number): number {
  return visualViewportHeight ?? windowInnerHeight
}
