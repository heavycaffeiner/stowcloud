// @vitest-environment node
// Pins the predicates every layer shares.
import { describe, expect, it } from 'vitest'
import { classifyProperty, isAllowed, loadPolicy, onGrid, onScale, checksFor } from './policy.mjs'

describe('policy', () => {
  it('freezes what it hands out, so no layer can edit the policy for the others', () => {
    const p = loadPolicy()
    expect(Object.isFrozen(p)).toBe(true)
    expect(Object.isFrozen(p.spacingScale)).toBe(true)
  })

  it('returns the same object on repeated loads', () => {
    expect(loadPolicy()).toBe(loadPolicy())
  })
})

describe('classifyProperty', () => {
  it.each([
    ['padding-inline-start', 'spacing'],
    ['GAP', 'spacing'],
    ['  row-gap  ', 'spacing'],
    ['max-block-size', 'sizing'],
    ['inset-inline-end', 'sizing'],
    ['border-block-start-width', 'hairline'],
    ['outline-offset', 'hairline'],
    ['border-end-end-radius', 'radius'],
    ['line-height', 'typography']
  ])('classifies %s as %s', (prop, cls) => {
    expect(classifyProperty(prop)).toBe(cls)
  })

  it('returns null for properties the toolchain does not police', () => {
    expect(classifyProperty('font-size')).toBeNull()
    expect(classifyProperty('color')).toBeNull()
    expect(classifyProperty('letter-spacing')).toBeNull()
  })
})

describe('isAllowed', () => {
  it('holds spacing to the scale, not merely to the grid', () => {
    expect(isAllowed(16, 'spacing')).toBe(true)
    expect(isAllowed(64, 'spacing')).toBe(true)
    // 20 and 40 are multiples of 4 and still off the scale. This is the
    // tightening the whole proposal turns on.
    expect(isAllowed(20, 'spacing')).toBe(false)
    expect(isAllowed(40, 'spacing')).toBe(false)
    expect(isAllowed(3, 'spacing')).toBe(false)
  })

  it('lets sizing take any multiple of 4, plus the hairline values', () => {
    expect(isAllowed(360, 'sizing')).toBe(true)
    expect(isAllowed(56, 'sizing')).toBe(true)
    expect(isAllowed(1, 'sizing')).toBe(true)
    expect(isAllowed(905, 'sizing')).toBe(false)
  })

  it('allows the pill radius only for radius properties', () => {
    expect(isAllowed(9999, 'radius')).toBe(true)
    expect(isAllowed(9999, 'sizing')).toBe(false)
  })

  it('ignores sign, so an inward offset follows the same rule', () => {
    expect(isAllowed(-8, 'spacing')).toBe(true)
    expect(isAllowed(-2, 'hairline')).toBe(true)
    expect(isAllowed(-6, 'spacing')).toBe(false)
  })

  it('puts line-height on the grid', () => {
    expect(isAllowed(20, 'typography')).toBe(true)
    expect(isAllowed(21, 'typography')).toBe(false)
  })

  it('rejects non-finite input rather than letting NaN through', () => {
    expect(isAllowed(Number.NaN, 'spacing')).toBe(false)
    expect(isAllowed(Number.POSITIVE_INFINITY, 'sizing')).toBe(false)
  })
})

describe('onGrid', () => {
  it('accepts sub-pixel drift up to the tolerance and no further', () => {
    expect(onGrid(16)).toBe(true)
    expect(onGrid(16.5)).toBe(true)
    expect(onGrid(15.5)).toBe(true)
    expect(onGrid(16.51)).toBe(false)
    expect(onGrid(18)).toBe(false)
  })

  it('works the same below zero', () => {
    expect(onGrid(-12)).toBe(true)
    expect(onGrid(-13)).toBe(false)
  })
})

describe('onScale', () => {
  it('accepts a scale member within tolerance', () => {
    expect(onScale(24)).toBe(true)
    expect(onScale(24.4)).toBe(true)
    expect(onScale(0)).toBe(true)
    expect(onScale(20)).toBe(false)
  })
})

describe('checksFor', () => {
  it('names the checks each layer may emit', () => {
    expect(checksFor('runtime')).toContain('sibling-edges')
    expect(checksFor('static')).toContain('asymmetric-padding')
    expect(checksFor('nope')).toBeNull()
  })
})
