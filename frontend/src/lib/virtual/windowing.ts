// Pure windowing maths for every virtualized list, fixed-height and measured rows alike.
// Kept out of the UI components so it is testable without a DOM and so the
// maths that must stay bounded at 100k rows can be asserted directly.

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
 * Recorded for reference: the mapping itself kicks
 * in earlier, at SCALE_MAPPING_THRESHOLD_PX, so there is headroom below the
 * tighter (Firefox) ceiling even for browsers we haven't measured.
 */
export const FIREFOX_SCROLL_HEIGHT_LIMIT_PX = 17_800_000
export const CHROME_SCROLL_HEIGHT_LIMIT_PX = 33_500_000

/**
 * Above this natural height (itemCount * rowHeight), a real spacer element
 * would risk clamping by the browser. Past this
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

/** Where each row sits in natural (uncompressed) pixels. */
export interface RowLayout {
  readonly count: number
  /** Natural height of every row and the gaps between them. */
  readonly height: number
  top(index: number): number
  size(index: number): number
  /** The row covering offset `y`, clamped to the rows that exist. */
  indexAt(y: number): number
}

/** `count` rows of one height each. */
export function fixedRows(count: number, rowHeight: number): RowLayout {
  const n = Math.max(0, count)
  const h = Math.max(0, rowHeight)
  return {
    count: n,
    height: n * h,
    top: (index) => index * h,
    size: () => h,
    indexAt: (y) => (h > 0 ? Math.min(Math.max(0, Math.floor(y / h)), Math.max(0, n - 1)) : 0)
  }
}

/** Rows of the given heights with `gap` px between neighbours. */
export function measuredRows(sizes: readonly number[], gap: number): RowLayout {
  const tops = new Float64Array(sizes.length + 1)
  for (let index = 0; index < sizes.length; index++) tops[index + 1] = tops[index] + sizes[index] + gap
  return {
    count: sizes.length,
    height: sizes.length > 0 ? tops[sizes.length] - gap : 0,
    top: (index) => tops[index],
    size: (index) => sizes[index],
    indexAt: (y) => {
      let low = 0
      let high = sizes.length - 1
      while (low < high) {
        const middle = (low + high + 1) >> 1
        if (tops[middle] <= y) low = middle
        else high = middle - 1
      }
      return Math.max(0, low)
    }
  }
}

function mappingFor(naturalHeight: number): ScaleMapping {
  const natural = Math.max(0, naturalHeight)
  if (natural <= SCALE_MAPPING_THRESHOLD_PX) return { active: false, spacerHeight: natural, scale: 1 }
  return { active: true, spacerHeight: SCALE_MAPPING_THRESHOLD_PX, scale: SCALE_MAPPING_THRESHOLD_PX / natural }
}

/** Pure function: decides whether/how to compress itemCount rows of rowHeight px into a safe scrollable range. */
export function computeScaleMapping(itemCount: number, rowHeight: number): ScaleMapping {
  return mappingFor(Math.max(0, itemCount) * Math.max(0, rowHeight))
}

/** Maps a scrollTop expressed in *compressed* (spacer) coordinates back to the row index it represents. */
export function scrollTopToRowIndex(
  scrollTop: number,
  mapping: ScaleMapping,
  rowHeight: number,
  itemCount: number
): number {
  if (itemCount <= 0 || rowHeight <= 0) return 0
  return fixedRows(itemCount, rowHeight).indexAt(mapping.active ? scrollTop / mapping.scale : scrollTop)
}

/** Inverse of scrollTopToRowIndex: compressed-space Y offset (px) for a given row index. */
export function rowIndexToScrollTop(rowIndex: number, mapping: ScaleMapping, rowHeight: number): number {
  const natural = Math.max(0, rowIndex) * rowHeight
  return mapping.active ? natural * mapping.scale : natural
}

const EMPTY_WINDOW: WindowResult = { start: 0, count: 0, end: 0, padTop: 0, totalHeight: 0, scaled: false }

/** The rows to render for a viewport `viewportHeight` px tall, scrolled `scrollTop` px into the spacer. */
export function windowOver(rows: RowLayout, scrollTop: number, viewportHeight: number, overscan = 8): WindowResult {
  if (rows.count <= 0 || rows.height <= 0) return EMPTY_WINDOW
  const mapping = mappingFor(rows.height)
  const top = Math.max(0, mapping.active ? scrollTop / mapping.scale : scrollTop)
  const first = rows.indexAt(top)
  const bottom = top + Math.max(0, viewportHeight)
  // A row that starts exactly at the bottom edge is not visible yet.
  const below = rows.indexAt(bottom)
  const last = Math.max(first, below > 0 && rows.top(below) >= bottom ? below - 1 : below)
  const start = Math.max(0, first - overscan)
  const end = Math.min(rows.count, last + 1 + overscan)

  // Clamp so the rendered block's bottom edge never sits past the (possibly
  // compressed) spacer: otherwise the last rows would render beyond the scrollable area.
  const blockHeight = rows.top(end - 1) + rows.size(end - 1) - rows.top(start)
  const padTop = Math.min(rows.top(start) * mapping.scale, Math.max(0, mapping.spacerHeight - blockHeight))

  return { start, count: end - start, end, padTop, totalHeight: mapping.spacerHeight, scaled: mapping.active }
}

export function computeWindow(params: {
  scrollTop: number
  viewportHeight: number
  rowHeight: number
  itemCount: number
  overscan?: number
}): WindowResult {
  const { scrollTop, viewportHeight, rowHeight, itemCount, overscan } = params
  return windowOver(fixedRows(itemCount, rowHeight), scrollTop, viewportHeight, overscan)
}

/** True once itemCount * rowHeight would need the scale-factor fallback (computeScaleMapping(...).active). */
export function exceedsSafeScrollHeight(itemCount: number, rowHeight: number): boolean {
  return computeScaleMapping(itemCount, rowHeight).active
}

/**
 * File views used to be their own `overflow: auto` scroll container and read
 * `scrollTop` and `clientHeight` straight off the scroll event. A browser only
 * collapses its address bar chrome when the document scrolls, and this app's
 * document did not, so the address bar permanently ate part of every phone
 * screen. The document now owns scrolling, which turns `computeWindow`'s two
 * inputs into a call-site concern rather than an algorithm concern: the maths
 * below only need "how far down" and "how tall the visible area is", not which
 * element does the scrolling. These two pure functions are that translation,
 * kept out of the component so they are testable without mounting anything.
 *
 * `scrollTop` in document-scroll terms: how far the *viewport element's own
 * top edge* has scrolled past the top of the window, clamped at 0 for the
 * (normal, if the viewport sits below other page content) case where the
 * element hasn't reached the top of the screen yet: `computeWindow` treats
 * a negative scrollTop as "nothing scrolled" and this keeps callers from
 * having to reason about that themselves.
 *
 * @param windowScrollY `window.scrollY` at read time.
 * @param viewportDocumentTop The viewport element's own top edge, in
 *   document coordinates (`getBoundingClientRect().top + window.scrollY`,
 *   captured before scrollY moves it), not `offsetTop`, which is relative
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
export function effectiveViewportHeight(
  visualViewportHeight: number | undefined | null,
  windowInnerHeight: number
): number {
  return visualViewportHeight ?? windowInnerHeight
}
