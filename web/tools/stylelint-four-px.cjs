// tools/stylelint-four-px.cjs — custom stylelint plugin, the static layer of
// the design grid gate. The 4 px grid is enforced, not a convention.
//
// Four things are checked, all driven by tools/design-grid/policy.json:
//
//   grid              px in a sizing, inset, border, radius or custom-property
//                     declaration must be a multiple of 4 (or a hairline).
//   spacing-scale     px in margin/padding/gap must be a member of the spacing
//                     scale. A multiple of 4 is no longer enough: 20px and
//                     40px divide by 4 and still have no place in the rhythm.
//   asymmetric-padding  left/right and top/bottom of a resolved padding or
//                     margin must match, so an inset that differs does so on
//                     purpose and carries a waiver saying why.
//   line-height-grid  a text block's line box governs the height of every box
//                     below it, so line-height lands on the grid too.
//
// Values inside calc()/min()/max()/clamp() are checked like any other: those
// functions used to be skipped wholesale, which left the one hole through
// which an arbitrary px literal could reach a stylesheet. var() is descended
// into for its fallback only; the custom-property name is not a length.
//
// No auto-fix. The correct multiple of 4 is a design decision, not something
// mechanically derivable from the wrong one.

const path = require('node:path')
const stylelint = require('stylelint')
const valueParser = require('postcss-value-parser')
const { loadPolicy, classifyProperty, isAllowed, onGrid } = require('./design-grid/policy.cjs')
const { sharedWaivers, isWaived } = require('./design-grid/waivers.cjs')

const ruleName = 'sc/four-px-grid'
const messages = stylelint.utils.ruleMessages(ruleName, {
  offGrid: (value, prop, key) =>
    `"${value}" in "${prop}" is not a multiple of 4px (sc/four-px-grid, waiver key: ${key})`,
  offScale: (value, prop, scale, key) =>
    `"${value}" in "${prop}" is not on the spacing scale [${scale}] (sc/four-px-grid, waiver key: ${key})`,
  lineHeight: (computed, detail, key) =>
    `line-height resolves to ${computed}px, which is off the 4px grid (${detail}) (sc/four-px-grid, waiver key: ${key})`,
  asymmetric: (prop, axis, a, b, key) =>
    `"${prop}" is asymmetric on the ${axis} axis: ${a} vs ${b} (sc/four-px-grid, waiver key: ${key})`
})

const meta = { url: 'https://internal/sc/four-px-grid' }

const WEB_ROOT = path.dirname(__dirname)

/** Repo-relative, forward-slashed source path, or a stable stand-in for inline code. */
function sourceFile(node) {
  const from = node.source && node.source.input && node.source.input.from
  if (!from) return '<unknown>'
  if (!path.isAbsolute(from)) return from.replace(/\\/g, '/')
  return path.relative(WEB_ROOT, from).replace(/\\/g, '/')
}

/** The selector a declaration sits under, for the waiver key. */
function selectorOf(decl) {
  const parent = decl.parent
  if (!parent) return '<root>'
  if (parent.type === 'rule') return parent.selector.replace(/\s+/g, ' ').trim()
  if (parent.type === 'atrule') return `@${parent.name} ${parent.params}`.trim()
  return '<root>'
}

function waiverKey(decl, prop) {
  return `${sourceFile(decl)}#${selectorOf(decl)}#${prop}`
}

const PX_RE = /^(-?\d*\.?\d+)px$/i
const BARE_NUMBER_RE = /^(-?\d*\.?\d+)$/
const PERCENT_RE = /^(-?\d*\.?\d+)%$/

/**
 * Calls `onPx(number, rawToken)` for every px literal in `value`.
 * Descends into every function except url(); for var() only the fallback
 * arguments are descended, never the custom-property name.
 */
function scanPx(value, onPx) {
  const walk = (nodes) => {
    for (const node of nodes) {
      if (node.type === 'function') {
        const name = node.value.toLowerCase()
        if (name === 'url') continue
        if (name === 'var') {
          const comma = node.nodes.findIndex((n) => n.type === 'div' && n.value === ',')
          if (comma >= 0) walk(node.nodes.slice(comma + 1))
          continue
        }
        walk(node.nodes)
        continue
      }
      if (node.type === 'word') {
        const m = PX_RE.exec(node.value)
        if (m) onPx(parseFloat(m[1]), node.value)
      }
    }
  }
  walk(valueParser(value).nodes)
}

/** Top-level whitespace-separated components of a shorthand value. */
function shorthandParts(value) {
  const parsed = valueParser(value)
  const parts = []
  let current = []
  for (const node of parsed.nodes) {
    if (node.type === 'space') {
      if (current.length) parts.push(current)
      current = []
      continue
    }
    current.push(node)
  }
  if (current.length) parts.push(current)
  return parts.map((nodes) => valueParser.stringify(nodes).trim()).filter(Boolean)
}

/**
 * A shorthand component as a number of px, or null when it is not a px length.
 * A bare `0` counts as 0px; everything else without a px unit (auto, %, var(),
 * calc()) makes its axis unresolvable rather than wrong.
 */
function asPx(token) {
  const m = PX_RE.exec(token)
  if (m) return parseFloat(m[1])
  if (token === '0') return 0
  return null
}

// [name, startSideIndex, endSideIndex] against the [top, right, bottom, left] order
const AXES = [
  ['inline', 3, 1],
  ['block', 0, 2]
]

// side order is [top, right, bottom, left]
const SHORTHAND_SIDES = {
  padding: [0, 1, 2, 3],
  margin: [0, 1, 2, 3]
}

const LONGHAND_SIDE = {
  top: 0,
  right: 1,
  bottom: 2,
  left: 3,
  'block-start': 0,
  'inline-end': 1,
  'block-end': 2,
  'inline-start': 3
}

/**
 * Folds every padding (or margin) declaration in one rule block into a
 * resolved [top, right, bottom, left], in source order, so longhands override
 * the shorthand exactly as the cascade does.
 */
function resolveBox(decls, base) {
  const sides = [null, null, null, null]
  const owner = [null, null, null, null]
  let touched = false

  const set = (i, value, decl) => {
    sides[i] = value
    owner[i] = decl
    touched = true
  }

  for (const decl of decls) {
    const prop = decl.prop.toLowerCase()
    if (prop !== base && !prop.startsWith(`${base}-`)) continue
    const suffix = prop === base ? '' : prop.slice(base.length + 1)
    const parts = shorthandParts(decl.value)
    if (parts.length === 0) continue

    if (suffix === '') {
      const [a, b = a, c = a, d = b] = parts.map(asPx)
      const order = SHORTHAND_SIDES[base]
      set(order[0], a, decl)
      set(order[1], b, decl)
      set(order[2], c, decl)
      set(order[3], d, decl)
      continue
    }
    if (suffix === 'block' || suffix === 'inline') {
      const [a, b = a] = parts.map(asPx)
      if (suffix === 'block') {
        set(0, a, decl)
        set(2, b, decl)
      } else {
        set(3, a, decl)
        set(1, b, decl)
      }
      continue
    }
    const side = LONGHAND_SIDE[suffix]
    if (side === undefined) continue
    set(side, asPx(parts[0]), decl)
  }

  return touched ? { sides, owner } : null
}

const plugin = stylelint.createPlugin(ruleName, (enabled) => {
  return (root, result) => {
    if (!enabled) return

    const policy = loadPolicy()
    const waivers = sharedWaivers()

    const report = (decl, check, prop, message) => {
      const key = waiverKey(decl, prop)
      if (isWaived(waivers, { layer: 'static', check, selector: key })) return
      stylelint.utils.report({ message: message(key), node: decl, result, ruleName })
    }

    // ── per declaration: every px literal against its property's predicate ──
    root.walkDecls((decl) => {
      const prop = decl.prop.trim().toLowerCase()

      if (prop.startsWith('--')) {
        // A custom property carries no property class of its own, so it is held
        // to the grid rather than to the spacing scale: --sc-nav-rail-width is
        // 96px and legitimately off the scale. Misuse of a token in a spacing
        // slot is caught by the runtime layer, which sees the used value.
        scanPx(decl.value, (n, raw) => {
          if (isAllowed(n, 'sizing')) return
          report(decl, 'grid', prop, (key) => messages.offGrid(raw, prop, key))
        })
        return
      }

      const cls = classifyProperty(prop)
      if (!cls) return

      if (cls === 'typography') {
        checkLineHeight(decl, prop, report)
        return
      }

      scanPx(decl.value, (n, raw) => {
        if (isAllowed(n, cls)) return
        if (cls === 'spacing') {
          report(decl, 'spacing-scale', prop, (key) =>
            messages.offScale(raw, prop, policy.spacingScale.join(', '), key)
          )
        } else {
          report(decl, 'grid', prop, (key) => messages.offGrid(raw, prop, key))
        }
      })
    })

    // ── per rule block: resolved padding/margin symmetry ──
    const blocks = []
    root.walkRules((rule) => blocks.push(rule))
    root.walkAtRules((at) => {
      if (at.nodes && at.nodes.some((n) => n.type === 'decl')) blocks.push(at)
    })

    for (const block of blocks) {
      // Own declarations only: a nested rule is its own block and is visited
      // separately, so descending would attribute a child's padding to its parent.
      const decls = block.nodes.filter((n) => n.type === 'decl')
      if (decls.length === 0) continue

      // Padding is checked on both axes; margin only on the inline one.
      // A block-axis margin asymmetry is how flow spacing is written in CSS --
      // `margin: 0 0 8px` on a heading means "space after me", not a container
      // that sits too high. Checking it flagged 38 of those and nothing else,
      // which would have filled the waiver file with one idiom. Inline-axis
      // margin still matters: that is a block sitting off centre.
      for (const [base, axes] of [
        ['padding', AXES],
        ['margin', AXES.slice(0, 1)]
      ]) {
        const resolved = resolveBox(decls, base)
        if (!resolved) continue
        const { sides, owner } = resolved

        for (const [axis, a, b] of axes) {
          if (sides[a] === null || sides[b] === null) continue
          if (sides[a] === sides[b]) continue
          const decl = owner[b] ?? owner[a]
          report(decl, 'asymmetric-padding', base, (key) =>
            messages.asymmetric(base, axis, `${sides[a]}px`, `${sides[b]}px`, key)
          )
        }
      }
    }
  }
})

/**
 * line-height is on the grid when it is a px multiple of 4, or when a px
 * font-size in the same block resolves it to one. A unitless value with no
 * resolvable font-size is skipped: the block's real height is measured by the
 * runtime layer, which is where the answer actually is.
 */
function checkLineHeight(decl, prop, report) {
  const value = decl.value.trim()

  const px = PX_RE.exec(value)
  if (px) {
    const n = parseFloat(px[1])
    if (isAllowed(n, 'typography')) return
    report(decl, 'line-height-grid', prop, (key) => messages.offGrid(value, prop, key))
    return
  }

  let factor = null
  const bare = BARE_NUMBER_RE.exec(value)
  const pct = PERCENT_RE.exec(value)
  if (bare) factor = parseFloat(bare[1])
  else if (pct) factor = parseFloat(pct[1]) / 100
  if (factor === null) return

  const block = decl.parent
  if (!block || !block.nodes) return
  let fontSize = null
  for (const n of block.nodes) {
    if (n.type !== 'decl' || n.prop.trim().toLowerCase() !== 'font-size') continue
    const m = PX_RE.exec(n.value.trim())
    fontSize = m ? parseFloat(m[1]) : null
  }
  if (fontSize === null) return

  const computed = Math.round(fontSize * factor * 1000) / 1000
  if (onGrid(computed)) return
  report(decl, 'line-height-grid', prop, (key) =>
    messages.lineHeight(computed, `${fontSize}px x ${value}`, key)
  )
}

plugin.ruleName = ruleName
plugin.messages = messages
plugin.meta = meta

module.exports = plugin
