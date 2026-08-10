// @vitest-environment node
// tools/stylelint-four-px.test.ts — exercises the custom sc/four-px-grid
// stylelint rule directly: the 4 px grid is enforced, not a convention.
// Runs in the `node` environment (not jsdom): stylelint's environment
// detection mis-fires under jsdom because a global `document` exists
// without a usable `document.baseURI`.
import path from 'node:path'
import stylelint from 'stylelint'
import { describe, expect, it } from 'vitest'

const pluginPath = path.resolve(process.cwd(), 'tools/stylelint-four-px.cjs')

const config = {
  plugins: [pluginPath],
  rules: { 'sc/four-px-grid': true }
}

async function lint(code: string) {
  const result = await stylelint.lint({ code, config })
  return result.results[0]
}

async function texts(code: string) {
  return (await lint(code)).warnings.map((w) => w.text)
}

describe('sc/four-px-grid: the grid', () => {
  it('rejects a non-multiple-of-4 width', async () => {
    const r = await lint('.a { width: 10px; }')
    expect(r.warnings).toHaveLength(1)
    expect(r.warnings[0].text).toContain('10px')
  })

  it('accepts any multiple of 4 for sizing, including values off the spacing scale', async () => {
    const r = await lint('.a { width: 360px; height: 56px; max-width: 640px; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('allows the 0/1/2/3px hairline exception on borders and outlines', async () => {
    const r = await lint('.a { border: 1px solid red; outline-offset: 2px; border-top-width: 3px; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('allows the 9999px pill-shape exception on radius only', async () => {
    expect((await lint('.a { border-radius: 9999px; }')).warnings).toHaveLength(0)
    expect((await lint('.a { width: 9999px; }')).warnings).toHaveLength(1)
  })

  it('treats negative offsets the same as their positive magnitude', async () => {
    expect((await lint('.a { outline-offset: -2px; }')).warnings).toHaveLength(0)
    expect((await lint('.a { top: -8px; bottom: -8px; }')).warnings).toHaveLength(0)
    expect((await lint('.a { top: -6px; }')).warnings).toHaveLength(1)
  })

  it('accepts percentage values (not px, exempt by construction)', async () => {
    expect((await lint('.a { width: 100%; }')).warnings).toHaveLength(0)
  })

  it('does not police font-size (typescale sizes need not be grid-aligned)', async () => {
    expect((await lint('.a { font-size: 57px; }')).warnings).toHaveLength(0)
  })

  it('holds custom properties to the grid rather than the scale', async () => {
    expect((await lint(':root { --sc-nav-rail-width: 96px; }')).warnings).toHaveLength(0)
    expect((await lint(':root { --sc-nav-rail-width: 97px; }')).warnings).toHaveLength(1)
  })
})

describe('sc/four-px-grid: the spacing scale', () => {
  it('accepts scale members', async () => {
    const r = await lint('.a { margin: 8px; padding: 16px; gap: 24px; row-gap: 0; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('rejects a multiple of 4 that is not on the scale', async () => {
    const r = await lint('.a { gap: 20px; }')
    expect(r.warnings).toHaveLength(1)
    expect(r.warnings[0].text).toContain('spacing scale')
  })

  it('rejects the old hairline exemption in a spacing slot', async () => {
    // `padding: 3px` used to pass on the border hairline allowance. Padding is
    // spacing; 3px is not a rhythm value.
    expect((await lint('.a { padding: 3px; }')).warnings).toHaveLength(1)
  })

  it('flags each off-scale component of a shorthand independently', async () => {
    const r = await lint('.a { margin: 5px 6px 5px 6px; }')
    expect(r.warnings.filter((w) => w.text.includes('spacing scale'))).toHaveLength(4)
  })
})

describe('sc/four-px-grid: function descent', () => {
  it('checks px literals inside calc()', async () => {
    expect((await lint('.a { width: calc(100% - 12px); }')).warnings).toHaveLength(0)
    expect((await lint('.a { width: calc(100% - 13px); }')).warnings).toHaveLength(1)
  })

  it('checks px literals inside min(), max() and clamp()', async () => {
    expect((await lint('.a { width: max(100%, 48px); }')).warnings).toHaveLength(0)
    expect((await lint('.a { width: clamp(16px, 50%, 47px); }')).warnings).toHaveLength(1)
    expect((await lint('.a { gap: min(20px, 5%); }')).warnings).toHaveLength(1)
  })

  it('checks a var() fallback but never the custom-property name', async () => {
    expect((await lint('.a { gap: var(--sc-page-pad); }')).warnings).toHaveLength(0)
    expect((await lint('.a { gap: var(--sc-page-pad, 16px); }')).warnings).toHaveLength(0)
    expect((await lint('.a { gap: var(--sc-page-pad, 13px); }')).warnings).toHaveLength(1)
  })

  it('descends through nested functions', async () => {
    expect((await lint('.a { width: calc(100% - max(8px, 13px)); }')).warnings).toHaveLength(1)
  })

  it('ignores url() contents', async () => {
    expect((await lint('.a { background: url(a-5px-b.png); }')).warnings).toHaveLength(0)
  })
})

describe('sc/four-px-grid: padding and margin symmetry', () => {
  it('accepts a symmetric shorthand', async () => {
    expect((await lint('.a { padding: 8px 16px; }')).warnings).toHaveLength(0)
    expect((await lint('.a { margin: 0; }')).warnings).toHaveLength(0)
    expect((await lint('.a { padding: 8px 16px 8px 16px; }')).warnings).toHaveLength(0)
  })

  it('flags an asymmetric inline axis', async () => {
    const t = await texts('.a { padding: 8px 16px 8px 32px; }')
    expect(t.filter((s) => s.includes('inline axis'))).toHaveLength(1)
  })

  it('flags an asymmetric block axis', async () => {
    const t = await texts('.a { padding: 8px 16px 24px 16px; }')
    expect(t.filter((s) => s.includes('block axis'))).toHaveLength(1)
  })

  it('flags both axes of a padding shorthand that matches on neither', async () => {
    const t = await texts('.a { padding: 4px 8px 12px 16px; }')
    expect(t.filter((s) => s.includes('inline axis'))).toHaveLength(1)
    expect(t.filter((s) => s.includes('block axis'))).toHaveLength(1)
  })

  it('leaves margin alone on the block axis', async () => {
    // `margin: 0 0 8px` on a heading means "space after me", the ordinary way
    // flow spacing is written. Checking it flagged that idiom 38 times in this
    // codebase and no real defect once.
    expect((await lint('.a { margin: 0 0 8px; }')).warnings).toHaveLength(0)
    const t = await texts('.a { margin: 4px 8px 12px 16px; }')
    expect(t.filter((s) => s.includes('block axis'))).toHaveLength(0)
    expect(t.filter((s) => s.includes('inline axis'))).toHaveLength(1)
  })

  it('lets a longhand override the shorthand, as the cascade does', async () => {
    expect((await lint('.a { padding: 8px 32px; padding-left: 32px; }')).warnings).toHaveLength(0)
    const t = await texts('.a { padding: 16px; padding-left: 32px; }')
    expect(t.filter((s) => s.includes('inline axis'))).toHaveLength(1)
  })

  it('resolves the logical shorthands', async () => {
    expect((await lint('.a { padding-block: 8px; padding-inline: 16px; }')).warnings).toHaveLength(0)
    const t = await texts('.a { padding-inline: 8px 32px; }')
    expect(t.filter((s) => s.includes('inline axis'))).toHaveLength(1)
  })

  it('treats a bare 0 as 0px', async () => {
    expect((await lint('.a { padding: 0 16px; }')).warnings).toHaveLength(0)
    const t = await texts('.a { padding: 0 0 8px 0; }')
    expect(t.filter((s) => s.includes('block axis'))).toHaveLength(1)
  })

  it('skips an axis it cannot resolve rather than guessing', async () => {
    expect((await lint('.a { margin: 0 auto; }')).warnings).toHaveLength(0)
    expect((await lint('.a { padding: 8px var(--sc-page-pad); }')).warnings).toHaveLength(0)
    expect((await lint('.a { padding-inline-start: 50%; padding-inline-end: 16px; }')).warnings)
      .toHaveLength(0)
  })

  it('does not attribute a nested rule\'s padding to its parent', async () => {
    const r = await lint('.a { padding: 16px; .b { padding-left: 32px; } }')
    expect(r.warnings).toHaveLength(0)
  })

  it('checks a block inside a media query', async () => {
    const t = await texts('@media (min-width: 600px) { .a { padding: 8px 16px 8px 32px; } }')
    expect(t.filter((s) => s.includes('inline axis'))).toHaveLength(1)
  })
})

describe('sc/four-px-grid: line-height', () => {
  it('accepts a px line-height on the grid', async () => {
    expect((await lint('.a { line-height: 20px; }')).warnings).toHaveLength(0)
  })

  it('rejects a px line-height off the grid', async () => {
    expect((await lint('.a { line-height: 21px; }')).warnings).toHaveLength(1)
  })

  it('resolves a unitless line-height against a px font-size in the same block', async () => {
    expect((await lint('.a { font-size: 16px; line-height: 1.5; }')).warnings).toHaveLength(0)
    const r = await lint('.a { font-size: 14px; line-height: 1.5; }')
    expect(r.warnings).toHaveLength(1)
    expect(r.warnings[0].text).toContain('21px')
  })

  it('resolves a percentage line-height the same way', async () => {
    expect((await lint('.a { font-size: 16px; line-height: 150%; }')).warnings).toHaveLength(0)
    expect((await lint('.a { font-size: 14px; line-height: 150%; }')).warnings).toHaveLength(1)
  })

  it('skips a unitless line-height it cannot resolve', async () => {
    expect((await lint('.a { line-height: 1.4; }')).warnings).toHaveLength(0)
    expect((await lint('.a { font-size: 1rem; line-height: 1.4; }')).warnings).toHaveLength(0)
  })
})

describe('sc/four-px-grid: waiver keys', () => {
  it('puts a copy-pasteable waiver key in every message', async () => {
    const r = await lint('.sc-row > .cell { gap: 20px; }')
    expect(r.warnings[0].text).toMatch(/waiver key: .+#\.sc-row > \.cell#gap/)
  })
})
