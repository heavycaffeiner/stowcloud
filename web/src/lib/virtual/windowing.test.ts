import { describe, expect, it } from 'vitest'
import {
  computeScaleMapping,
  computeWindow,
  documentScrollTop,
  effectiveViewportHeight,
  exceedsSafeScrollHeight,
  rowIndexToScrollTop,
  scrollTopToRowIndex,
  SCALE_MAPPING_THRESHOLD_PX
} from './windowing'

describe('computeWindow', () => {
  it('starts at 0 with no negative overscan underflow', () => {
    const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: 48, itemCount: 100_000 })
    expect(w.start).toBe(0)
    expect(w.padTop).toBe(0)
  })

  it('stays bounded regardless of itemCount (100k rows)', () => {
    const w = computeWindow({ scrollTop: 500_000, viewportHeight: 800, rowHeight: 48, itemCount: 100_000, overscan: 8 })
    // visible rows = ceil(800/48) = 17, + overscan*2 = 16 → 33 max
    expect(w.count).toBeLessThanOrEqual(33)
    expect(w.count).toBeGreaterThan(0)
  })

  it('clamps the end of the window to itemCount', () => {
    const w = computeWindow({ scrollTop: 1_000_000_000, viewportHeight: 800, rowHeight: 48, itemCount: 100_000 })
    expect(w.end).toBeLessThanOrEqual(100_000)
    expect(w.start).toBeLessThan(100_000)
  })

  it('computes translate3d offset as start * rowHeight', () => {
    const w = computeWindow({ scrollTop: 4800, viewportHeight: 480, rowHeight: 48, itemCount: 1000, overscan: 0 })
    expect(w.start).toBe(100)
    expect(w.padTop).toBe(4800)
  })

  it('handles empty lists', () => {
    const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: 48, itemCount: 0 })
    expect(w).toEqual({ start: 0, count: 0, end: 0, padTop: 0, totalHeight: 0, scaled: false })
  })

  it('totalHeight scales linearly with itemCount', () => {
    const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: 48, itemCount: 100_000 })
    expect(w.totalHeight).toBe(4_800_000)
  })

  it('sizes the spacer to the FULL total immediately, not just the loaded rows', () => {
    // Regression test for the bug this refactor fixes: the spacer must be
    // sized from the directory's total row count on the very first render,
    // not grown page-by-page as more rows happen to load.
    const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: 48, itemCount: 100_000 })
    expect(w.totalHeight).toBe(100_000 * 48)
    expect(w.totalHeight).not.toBe(200 * 48)
  })

  it('applies overscan symmetrically around the visible viewport', () => {
    const w = computeWindow({ scrollTop: 4800, viewportHeight: 480, rowHeight: 48, itemCount: 100_000, overscan: 8 })
    // visible rows = 10, start should be 100 - 8 = 92
    expect(w.start).toBe(92)
    expect(w.count).toBe(10 + 8 * 2)
  })

  it('is not scaled for a 100k row / 48px directory (well under the threshold)', () => {
    const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: 48, itemCount: 100_000 })
    expect(w.scaled).toBe(false)
  })

  describe('scale-factor fallback (> ~15M px natural height)', () => {
    const ITEM_COUNT = 1_000_000 // 1M rows * 48px = 48M px natural, well past the threshold
    const ROW_HEIGHT = 48

    it('activates once natural height exceeds the threshold', () => {
      const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: ROW_HEIGHT, itemCount: ITEM_COUNT })
      expect(w.scaled).toBe(true)
      expect(w.totalHeight).toBe(SCALE_MAPPING_THRESHOLD_PX)
      expect(w.totalHeight).toBeLessThan(ITEM_COUNT * ROW_HEIGHT)
    })

    it('keeps the spacer under every recorded browser scroll-height ceiling', () => {
      const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: ROW_HEIGHT, itemCount: ITEM_COUNT })
      expect(w.totalHeight).toBeLessThan(17_800_000) // Firefox
      expect(w.totalHeight).toBeLessThan(33_500_000) // Chrome
    })

    it('maps scrollTop 0 to row 0', () => {
      const w = computeWindow({ scrollTop: 0, viewportHeight: 800, rowHeight: ROW_HEIGHT, itemCount: ITEM_COUNT, overscan: 0 })
      expect(w.start).toBe(0)
      expect(w.padTop).toBe(0)
    })

    it('maps a scrollTop at the very bottom of the compressed spacer to the last rows', () => {
      const w = computeWindow({
        scrollTop: SCALE_MAPPING_THRESHOLD_PX,
        viewportHeight: 800,
        rowHeight: ROW_HEIGHT,
        itemCount: ITEM_COUNT,
        overscan: 0
      })
      expect(w.end).toBeLessThanOrEqual(ITEM_COUNT)
      expect(w.start).toBeGreaterThan(ITEM_COUNT - 100) // near the tail
    })

    it('maps scrollTop proportionally: 50% of the spacer lands near the 50% row index', () => {
      const w = computeWindow({
        scrollTop: SCALE_MAPPING_THRESHOLD_PX / 2,
        viewportHeight: 800,
        rowHeight: ROW_HEIGHT,
        itemCount: ITEM_COUNT,
        overscan: 0
      })
      const expectedIndex = Math.floor(ITEM_COUNT / 2)
      expect(w.start).toBeGreaterThanOrEqual(expectedIndex - 5)
      expect(w.start).toBeLessThanOrEqual(expectedIndex + 5)
    })

    it('never renders more DOM rows than the viewport + overscan even while scaled', () => {
      const w = computeWindow({
        scrollTop: SCALE_MAPPING_THRESHOLD_PX / 3,
        viewportHeight: 800,
        rowHeight: ROW_HEIGHT,
        itemCount: ITEM_COUNT,
        overscan: 8
      })
      expect(w.count).toBeLessThanOrEqual(33)
    })

    it('never positions the rendered window past the spacer bottom', () => {
      const w = computeWindow({
        scrollTop: SCALE_MAPPING_THRESHOLD_PX,
        viewportHeight: 800,
        rowHeight: ROW_HEIGHT,
        itemCount: ITEM_COUNT,
        overscan: 8
      })
      expect(w.padTop + w.count * ROW_HEIGHT).toBeLessThanOrEqual(w.totalHeight)
    })
  })
})

describe('computeScaleMapping', () => {
  it('is inactive with scale 1 under the threshold', () => {
    const m = computeScaleMapping(100_000, 48)
    expect(m.active).toBe(false)
    expect(m.scale).toBe(1)
    expect(m.spacerHeight).toBe(100_000 * 48)
  })

  it('activates and computes a sub-1 scale once past the threshold', () => {
    const m = computeScaleMapping(1_000_000, 48) // natural = 48,000,000
    expect(m.active).toBe(true)
    expect(m.spacerHeight).toBe(SCALE_MAPPING_THRESHOLD_PX)
    expect(m.scale).toBeCloseTo(SCALE_MAPPING_THRESHOLD_PX / 48_000_000, 10)
  })

  it('handles itemCount 0 without dividing by zero', () => {
    const m = computeScaleMapping(0, 48)
    expect(m.active).toBe(false)
    expect(m.spacerHeight).toBe(0)
  })
})

describe('scrollTopToRowIndex / rowIndexToScrollTop', () => {
  it('round-trips (approximately) when inactive', () => {
    const mapping = computeScaleMapping(100_000, 48)
    const idx = scrollTopToRowIndex(4800, mapping, 48, 100_000)
    expect(idx).toBe(100)
    expect(rowIndexToScrollTop(idx, mapping, 48)).toBe(4800)
  })

  it('round-trips (approximately) once scaled', () => {
    const itemCount = 1_000_000
    const mapping = computeScaleMapping(itemCount, 48)
    const targetIndex = 500_000
    const st = rowIndexToScrollTop(targetIndex, mapping, 48)
    const recovered = scrollTopToRowIndex(st, mapping, 48, itemCount)
    expect(recovered).toBeGreaterThanOrEqual(targetIndex - 1)
    expect(recovered).toBeLessThanOrEqual(targetIndex + 1)
  })

  it('clamps the row index to itemCount - 1', () => {
    const mapping = computeScaleMapping(100_000, 48)
    expect(scrollTopToRowIndex(10_000_000_000, mapping, 48, 100_000)).toBe(99_999)
  })
})

describe('exceedsSafeScrollHeight', () => {
  it('is false for a typical 100k row directory at 48px rows', () => {
    expect(exceedsSafeScrollHeight(100_000, 48)).toBe(false)
  })

  it('is true once total height exceeds the scale-mapping threshold', () => {
    expect(exceedsSafeScrollHeight(400_000, 48)).toBe(true)
  })
})

// Document-scroll call-site helpers (the address-bar-collapse fix). These
// replace reading `scrollTop`/`clientHeight` off the table's own (removed)
// `overflow: auto` box -- see the doc comment above them in windowing.ts.
describe('documentScrollTop', () => {
  it('is 0 before the viewport element reaches the top of the window', () => {
    // Page hasn't been scrolled far enough yet for the table to reach the
    // top -- there is nothing "behind" it to have scrolled past.
    expect(documentScrollTop(0, 400)).toBe(0)
    expect(documentScrollTop(100, 400)).toBe(0)
  })

  it('is the distance scrolled past the viewport element once it reaches the top', () => {
    // Table's document top sits at 400px; once window.scrollY passes that,
    // scrollTop should read exactly how far past.
    expect(documentScrollTop(400, 400)).toBe(0)
    expect(documentScrollTop(900, 400)).toBe(500)
  })

  it('never goes negative', () => {
    expect(documentScrollTop(0, 10_000)).toBe(0)
  })
})

describe('effectiveViewportHeight', () => {
  it('prefers visualViewport.height when present', () => {
    // The number that actually tracks a mid-collapse mobile chrome
    // animation -- see the doc comment in windowing.ts for why innerHeight
    // alone is not safe to use here.
    expect(effectiveViewportHeight(650, 844)).toBe(650)
  })

  it('falls back to window.innerHeight when visualViewport is unavailable', () => {
    // No `window.visualViewport` (older engines, or the jsdom test
    // environment) and no explicit visualViewport.height reading yet.
    expect(effectiveViewportHeight(undefined, 844)).toBe(844)
    expect(effectiveViewportHeight(null, 844)).toBe(844)
  })
})
