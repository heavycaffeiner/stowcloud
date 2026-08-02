// @vitest-environment node
// tools/stylelint-four-px.test.ts — exercises the custom sc/four-px-grid
// stylelint rule directly (DESIGN-FRONTEND.md §2.4, §11).
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

describe('sc/four-px-grid', () => {
  it('rejects a non-multiple-of-4 margin', async () => {
    const r = await lint('.a { margin: 5px; }')
    expect(r.warnings).toHaveLength(1)
    expect(r.warnings[0].text).toContain('5px')
  })

  it('rejects a non-multiple-of-4 width', async () => {
    const r = await lint('.a { width: 10px; }')
    expect(r.warnings).toHaveLength(1)
  })

  it('accepts multiples of 4 in spacing properties', async () => {
    const r = await lint('.a { margin: 4px 8px 12px 16px; padding: 0; gap: 24px; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('allows the 0/1/2/3px hairline-border exception', async () => {
    const r = await lint('.a { border: 1px solid red; outline-offset: 2px; padding: 3px; margin: 0; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('allows the 9999px pill-shape exception', async () => {
    const r = await lint('.a { border-radius: 9999px; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('treats negative offsets the same as their positive magnitude (inward focus rings)', async () => {
    const ok = await lint('.a { outline-offset: -2px; margin: -8px; }')
    expect(ok.warnings).toHaveLength(0)
    const bad = await lint('.a { outline-offset: -3px; }')
    expect(bad.warnings).toHaveLength(0) // -3 is within the 0/1/2/3 hairline exemption
    const flagged = await lint('.a { margin: -6px; }')
    expect(flagged.warnings).toHaveLength(1)
  })

  it('ignores px values inside calc()', async () => {
    const r = await lint('.a { width: calc(100% - 5px); margin: calc(4px + 3px); }')
    expect(r.warnings).toHaveLength(0)
  })

  it('does not police font-size (typescale sizes need not be grid-aligned)', async () => {
    const r = await lint('.a { font-size: 57px; }')
    expect(r.warnings).toHaveLength(0)
  })

  it('flags multiple violations in a shorthand declaration independently', async () => {
    const r = await lint('.a { margin: 5px 6px 4px 8px; }')
    expect(r.warnings).toHaveLength(2)
  })

  it('accepts percentage values (not px, exempt by construction)', async () => {
    const r = await lint('.a { width: 100%; }')
    expect(r.warnings).toHaveLength(0)
  })
})
