// @vitest-environment node
// Every path that must fail the build with
// a configuration error, plus the dead-waiver sweep.
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterAll, describe, expect, it } from 'vitest'
import {
  WaiverConfigError,
  deadWaivers,
  isWaived,
  loadWaivers,
  todayString,
  waiversFor
} from './waivers.mjs'

const dir = mkdtempSync(path.join(tmpdir(), 'sc-waivers-'))
let n = 0

/** Writes a waiver file and returns its path. */
function file(doc: unknown): string {
  const p = path.join(dir, `w${n++}.json`)
  writeFileSync(p, typeof doc === 'string' ? doc : JSON.stringify(doc))
  return p
}

const TODAY = '2026-08-10'

const good = {
  id: 'm3-checkbox-glyph',
  layer: 'runtime',
  check: 'grid-snap',
  selector: '.m3-checkbox svg',
  subtree: true,
  reason: 'm3-svelte 7.2.0 draws the checkbox glyph at 18px inside a 48px target we do not own.',
  expires: '2027-01-01'
}

function load(waivers: unknown[], today = TODAY) {
  return loadWaivers(today, file({ waivers }))
}

afterAll(() => {
  // The temp dir is small and the OS reclaims it; nothing to do.
})

describe('loadWaivers', () => {
  it('accepts a well-formed entry and defaults subtree to false', () => {
    const { subtree, ...noSubtree } = good
    const set = load([noSubtree])
    expect(set.list).toHaveLength(1)
    expect(set.list[0].subtree).toBe(false)
  })

  it('treats a missing file as an empty set, not an error', () => {
    const set = loadWaivers(TODAY, path.join(dir, 'does-not-exist.json'))
    expect(set.list).toEqual([])
  })

  it('rejects malformed JSON', () => {
    expect(() => loadWaivers(TODAY, file('{ nope'))).toThrow(WaiverConfigError)
  })

  it('rejects a document without a waivers array', () => {
    expect(() => loadWaivers(TODAY, file({ entries: [] }))).toThrow(/waivers" array/)
  })

  it('rejects a duplicate id', () => {
    expect(() => load([good, good])).toThrow(/duplicate id/)
  })

  it('rejects an unknown field, so a typo cannot be silently ignored', () => {
    expect(() => load([{ ...good, reasons: 'oops' }])).toThrow(/unknown field "reasons"/)
  })

  it('rejects an unknown layer', () => {
    expect(() => load([{ ...good, layer: 'visual' }])).toThrow(/"layer" must be one of/)
  })

  it('rejects a check the named layer does not emit', () => {
    expect(() => load([{ ...good, check: 'asymmetric-padding' }])).toThrow(/"check" must be/)
  })

  it('accepts the wildcard check', () => {
    expect(load([{ ...good, check: '*' }]).list[0].check).toBe('*')
  })

  it('rejects an empty selector', () => {
    expect(() => load([{ ...good, selector: '   ' }])).toThrow(/"selector" must be/)
  })

  it('rejects subtree on a layer that has no tree to walk', () => {
    expect(() =>
      load([{ ...good, layer: 'static', check: 'grid', subtree: true }])
    ).toThrow(/runtime layer only/)
  })

  it('rejects a reason too short to be a reason', () => {
    expect(() => load([{ ...good, reason: 'framework' }])).toThrow(/at least 30 characters/)
    expect(() => load([{ ...good, reason: '   '.repeat(20) }])).toThrow(/at least 30 characters/)
  })

  it('rejects a missing, unreal or past expiry', () => {
    expect(() => load([{ ...good, expires: undefined }])).toThrow(/real YYYY-MM-DD/)
    expect(() => load([{ ...good, expires: '2026-02-31' }])).toThrow(/real YYYY-MM-DD/)
    expect(() => load([{ ...good, expires: '10-08-2026' }])).toThrow(/real YYYY-MM-DD/)
    expect(() => load([{ ...good, expires: '2026-08-09' }])).toThrow(/expired on 2026-08-09/)
  })

  it('accepts an expiry of today', () => {
    expect(load([{ ...good, expires: TODAY }]).list).toHaveLength(1)
  })

  it('rejects a caller who passes a nonsense today', () => {
    expect(() => load([good], 'yesterday')).toThrow(/must be a real YYYY-MM-DD/)
  })
})

describe('isWaived', () => {
  it('matches a static waiver on its literal selector and marks it used', () => {
    const set = load([
      {
        id: 'row-inset',
        layer: 'static',
        check: 'asymmetric-padding',
        selector: 'src/lib/ui/FileRow.svelte#.sc-row#padding',
        reason: 'A leading icon and a trailing chevron want different insets by design.',
        expires: '2027-01-01'
      }
    ])
    const v = {
      layer: 'static',
      check: 'asymmetric-padding',
      selector: 'src/lib/ui/FileRow.svelte#.sc-row#padding'
    }
    expect(isWaived(set, v)).toBe(true)
    expect(deadWaivers(set, ['static'])).toEqual([])
  })

  it('does not match a different check on the same selector', () => {
    const set = load([
      {
        id: 'row-inset',
        layer: 'static',
        check: 'asymmetric-padding',
        selector: 'a#b#c',
        reason: 'A leading icon and a trailing chevron want different insets by design.',
        expires: '2027-01-01'
      }
    ])
    expect(isWaived(set, { layer: 'static', check: 'grid', selector: 'a#b#c' })).toBe(false)
  })

  it('honours a pre-resolved match from a layer that can run selectors itself', () => {
    const set = load([good])
    const v = { layer: 'runtime', check: 'grid-snap', selector: 'anything', waivedBy: 'm3-checkbox-glyph' }
    expect(isWaived(set, v)).toBe(true)
  })

  it('refuses a pre-resolved match that names an unknown waiver', () => {
    const set = load([good])
    expect(() =>
      isWaived(set, { layer: 'runtime', check: 'grid-snap', selector: 'x', waivedBy: 'ghost' })
    ).toThrow(/unknown waiver id/)
  })

  it('refuses a pre-resolved match whose waiver covers a different check', () => {
    const set = load([good])
    expect(() =>
      isWaived(set, { layer: 'runtime', check: 'overlap', selector: 'x', waivedBy: 'm3-checkbox-glyph' })
    ).toThrow(/cannot cover/)
  })

  it('lets a wildcard waiver cover any check on its layer', () => {
    const set = load([{ ...good, id: 'wild', check: '*' }])
    expect(
      isWaived(set, { layer: 'runtime', check: 'spacing-scale', selector: 'x', waivedBy: 'wild' })
    ).toBe(true)
  })
})

describe('deadWaivers', () => {
  it('reports a waiver nothing matched', () => {
    const set = load([good])
    expect(deadWaivers(set, ['runtime']).map((w) => w.id)).toEqual(['m3-checkbox-glyph'])
  })

  it('does not report a waiver whose layer did not run', () => {
    const set = load([good])
    expect(deadWaivers(set, ['static', 'component'])).toEqual([])
  })
})

describe('waiversFor', () => {
  it('partitions by layer in file order', () => {
    const set = load([good, { ...good, id: 'second' }])
    expect(waiversFor(set, 'runtime').map((w) => w.id)).toEqual(['m3-checkbox-glyph', 'second'])
    expect(waiversFor(set, 'static')).toEqual([])
  })
})

describe('todayString', () => {
  it('formats the local calendar date with padding', () => {
    expect(todayString(new Date(2026, 0, 5))).toBe('2026-01-05')
  })

  it('produces something loadWaivers accepts', () => {
    expect(() => load([good], todayString())).not.toThrow()
  })
})
