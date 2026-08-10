// tools/design-grid/component.test.ts — the design gate's component layer.
//
// jsdom computes no layout, so nothing here measures geometry. It covers the
// three things it genuinely can, all of which are invisible to the other two
// layers: a px value a script wrote into the `style` attribute (no stylesheet
// holds it, so the static rule cannot see it), the tokens such a value is
// allowed to reference, and the shape a component renders across the prop
// combinations no browser matrix reaches.
import './jsdom-shims'
import { cleanup, render } from '@testing-library/svelte'
import { createRawSnippet } from 'svelte'
import { afterEach, describe, expect, it } from 'vitest'
import { classifyProperty, isAllowed, loadPolicy } from './policy.mjs'
import { deadWaivers, isWaived, sharedWaivers } from './waivers.mjs'

import Button from '../../src/lib/ui/Button.svelte'
import Checkbox from '../../src/lib/ui/Checkbox.svelte'
import Chip from '../../src/lib/ui/Chip.svelte'
import Divider from '../../src/lib/ui/Divider.svelte'
import IconButton from '../../src/lib/ui/IconButton.svelte'
import ProgressCircular from '../../src/lib/ui/ProgressCircular.svelte'
import ProgressLinear from '../../src/lib/ui/ProgressLinear.svelte'
import Switch from '../../src/lib/ui/Switch.svelte'
import TextField from '../../src/lib/ui/TextField.svelte'

const policy = loadPolicy()
const waivers = sharedWaivers()

const text = (s: string) => createRawSnippet(() => ({ render: () => `<span>${s}</span>` }))

/** One entry per component; `props` is the matrix rendered for it. */
const MATRIX: Array<{ name: string; component: unknown; props: Array<[string, Record<string, unknown>]> }> = [
  {
    name: 'Button',
    component: Button,
    props: [
      ['filled', { children: text('Save') }],
      ['long label', { children: text('Move or copy the selected items somewhere else') }],
      ['with icon', { children: text('Save'), icon: text('+') }],
      ['danger', { children: text('Purge'), danger: true }],
      ['disabled', { children: text('Save'), disabled: true }],
      ['loading', { children: text('Save'), loading: true }],
      ['outlined pressed', { children: text('Grid'), variant: 'outlined', pressed: true }],
      ['square', { children: text('A'), square: true }]
    ]
  },
  {
    name: 'IconButton',
    component: IconButton,
    props: [
      ['plain', { label: 'Refresh', children: text('R') }],
      ['selected', { label: 'Folder tree', selected: true, children: text('T') }],
      ['expanded', { label: 'Details', expanded: true, children: text('D') }],
      ['disabled', { label: 'Delete', disabled: true, children: text('X') }]
    ]
  },
  {
    name: 'Chip',
    component: Chip,
    props: [
      ['assist', { children: text('PDF') }],
      ['filter selected', { variant: 'filter', selected: true, children: text('Images') }],
      ['input removable', { variant: 'input', onremove: () => {}, children: text('tag') }]
    ]
  },
  {
    name: 'Checkbox',
    component: Checkbox,
    props: [
      ['unchecked', { label: 'Select report.pdf' }],
      ['checked', { checked: true, label: 'Select report.pdf' }],
      ['indeterminate', { indeterminate: true, label: 'Select all' }],
      ['label hidden', { label: 'Select report.pdf', hideLabel: true }]
    ]
  },
  {
    name: 'Switch',
    component: Switch,
    props: [
      ['off', { label: 'Enable trash' }],
      ['on', { checked: true, label: 'Enable trash' }],
      ['label hidden', { label: 'Enable the account someone', showLabel: false }]
    ]
  },
  {
    name: 'Divider',
    component: Divider,
    props: [
      ['plain', {}],
      ['inset', { inset: true }]
    ]
  },
  {
    name: 'ProgressLinear',
    component: ProgressLinear,
    props: [
      ['indeterminate', {}],
      ['half', { value: 0.5 }],
      ['weak tone', { value: 0.2, tone: 'weak' }],
      ['fair tone', { value: 0.6, tone: 'fair' }]
    ]
  },
  {
    name: 'ProgressCircular',
    component: ProgressCircular,
    props: [
      ['indeterminate', {}],
      ['sized', { size: 48 }],
      ['determinate', { value: 0.75 }]
    ]
  },
  {
    name: 'TextField',
    component: TextField,
    props: [
      ['empty', { label: 'Name' }],
      ['filled', { value: 'report.pdf', label: 'Name' }],
      ['error', { value: 'x', label: 'Name', error: 'That name is taken' }],
      ['outlined', { label: 'Name', variant: 'outlined' }]
    ]
  }
]

const VAR_RE = /var\(\s*(--[A-Za-z0-9_-]+)/g
const PX_RE = /(-?\d*\.?\d+)px/g

/** Svelte's per-component style scope, rehashed on every CSS edit. */
const SVELTE_SCOPE = /^svelte-[a-z0-9]+$/

/** Attributes that carry a component's state, so a snapshot separates them. */
const STATE_ATTRS = [
  'disabled',
  'hidden',
  'checked',
  'aria-disabled',
  'aria-pressed',
  'aria-expanded',
  'aria-checked',
  'aria-invalid',
  'aria-label'
]

function classesOf(el: Element): string[] {
  return (el.getAttribute('class') ?? '')
    .split(/\s+/)
    .filter((c) => c && !SVELTE_SCOPE.test(c))
}

interface Finding {
  layer: 'component'
  check: string
  selector: string
  detail: string
}

/** Splits an inline style attribute into [property, value] pairs. */
function declarations(style: string): Array<[string, string]> {
  return style
    .split(';')
    .map((d) => d.trim())
    .filter(Boolean)
    .map((d) => {
      const i = d.indexOf(':')
      return [d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).trim()] as [string, string]
    })
    .filter(([p]) => p)
}

/** A path good enough to find the element again, and stable across renders. */
function pathOf(el: Element, component: string, variant: string): string {
  const cls = classesOf(el).slice(0, 2).join('.')
  return `${component}[${variant}] ${el.tagName.toLowerCase()}${cls ? `.${cls}` : ''}`
}

function inspect(root: ParentNode, component: string, variant: string): Finding[] {
  const found: Finding[] = []

  for (const el of root.querySelectorAll('[style]')) {
    const selector = pathOf(el, component, variant)
    for (const [prop, value] of declarations(el.getAttribute('style') ?? '')) {
      // A custom property in an inline style is a role remap, not geometry
      // (Button's danger palette, ProgressLinear's tone). Its px values, if
      // any, are still held to the grid the way the static layer holds them.
      const cls = prop.startsWith('--') ? 'sizing' : classifyProperty(prop)
      if (!cls) continue

      for (const m of value.matchAll(PX_RE)) {
        const n = Number.parseFloat(m[1])
        if (isAllowed(n, cls === 'typography' ? 'typography' : cls)) continue
        found.push({
          layer: 'component',
          check: 'inline-geometry',
          selector,
          detail: `"${m[0]}" in inline "${prop}"`
        })
      }

      for (const m of value.matchAll(VAR_RE)) {
        const token = m[1]
        // The framework's own tokens are its to define. Ours have to be declared.
        if (!token.startsWith('--sc-')) continue
        if (policy.approvedSpacingTokens.includes(token)) continue
        found.push({
          layer: 'component',
          check: 'unapproved-token',
          selector,
          detail: `inline "${prop}" reads ${token}, which is not in approvedSpacingTokens`
        })
      }
    }
  }

  return found.filter((f) => !isWaived(waivers, f))
}

/**
 * Tag, classes, state attributes and the geometry subset of any inline style,
 * in tree order. State attributes are in because without them `disabled` and
 * `loading` produced byte-identical records: a snapshot that cannot tell two
 * states apart cannot detect a regression in either.
 */
function shapeOf(root: ParentNode): string[] {
  const lines: string[] = []
  for (const el of root.querySelectorAll('*')) {
    const cls = classesOf(el).sort().join(' ')
    const state = STATE_ATTRS.filter((a) => el.hasAttribute(a))
      .map((a) => `${a}=${el.getAttribute(a)}`)
      .join(' ')
    const geometry = declarations(el.getAttribute('style') ?? '')
      .filter(([p]) => classifyProperty(p) !== null)
      .map(([p, v]) => `${p}:${v}`)
      .join(';')
    lines.push([el.tagName.toLowerCase(), cls, state, geometry].join('|').replace(/\|+$/, ''))
  }
  return lines
}

afterEach(cleanup)

describe.each(MATRIX)('$name', ({ name, component, props }) => {
  it.each(props)('%s renders no off-grid inline geometry', (variant, p) => {
    const { container } = render(component as never, p as never)
    const findings = inspect(container, name, variant)
    expect(findings.map((f) => `${f.check}: ${f.selector} ${f.detail}`)).toEqual([])
  })

  it.each(props)('%s keeps its rendered shape', (variant, p) => {
    const { container } = render(component as never, p as never)
    expect(shapeOf(container)).toMatchSnapshot()
  })
})

describe('component waivers', () => {
  it('has no waiver that matched nothing', () => {
    expect(deadWaivers(waivers, ['component']).map((w) => w.id)).toEqual([])
  })
})
