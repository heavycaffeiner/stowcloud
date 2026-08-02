// tools/stylelint-four-px.cjs — custom stylelint plugin.
// Rejects px values that are not multiples of 4 in spacing/sizing
// properties. The 4 px grid is enforced, not a convention.
//
// Allowlist: 0, 1px, 2px, 3px (borders/hairlines), 9999px (pill shape),
// 100% (not px, trivially exempt), and anything inside calc() (mixed
// units make static validation unreliable, so calc() is exempt wholesale).

const stylelint = require('stylelint')
const valueParser = require('postcss-value-parser')

const ruleName = 'sc/four-px-grid'
const messages = stylelint.utils.ruleMessages(ruleName, {
  rejected: (value, prop) =>
    `"${value}" in "${prop}" is not a multiple of 4px (sc/four-px-grid)`
})

const meta = {
  url: 'https://internal/sc/four-px-grid'
}

// Properties where px values represent spacing/sizing on the 4px grid.
// Typography (font-size) is intentionally excluded: line-height, which is
// already 4px-aligned, is what affects layout — not glyph size.
const TARGET_PROP_RE =
  /^(margin|padding|gap|row-gap|column-gap)(-(top|right|bottom|left|inline|block|inline-start|inline-end|block-start|block-end))?$|^(width|height|min-width|max-width|min-height|max-height|inline-size|block-size|min-inline-size|max-inline-size|min-block-size|max-block-size)$|^(top|right|bottom|left|inset)(-(inline|block|inline-start|inline-end|block-start|block-end))?$|^(border-radius|border-top-left-radius|border-top-right-radius|border-bottom-left-radius|border-bottom-right-radius)$|^(border|border-top|border-bottom|border-left|border-right)(-width)?$|^(outline-offset)$|^(scroll-margin|scroll-padding)(-(top|right|bottom|left))?$/i

const EXEMPT_PX = new Set([0, 1, 2, 3, 9999])

function isMultipleOf4(n) {
  // Compare by magnitude: negative offsets (e.g. an inward focus ring,
  // `outline-offset: -2px`) follow the same 4px-grid / hairline-exception
  // rules as their positive counterparts — only the sign differs.
  const abs = Math.abs(n)
  return EXEMPT_PX.has(abs) || (Number.isFinite(abs) && abs % 4 === 0)
}

/** Walk a value node tree, skipping the contents of calc()/min()/max()/clamp() entirely. */
function collectPxViolations(value) {
  const parsed = valueParser(value)
  const violations = []

  parsed.walk((node) => {
    if (node.type === 'function' && /^(calc|min|max|clamp|var)$/i.test(node.value)) {
      return false // do not descend — exempt by design
    }
    if (node.type === 'word') {
      const m = /^(-?\d*\.?\d+)px$/.exec(node.value)
      if (m) {
        const n = parseFloat(m[1])
        if (!isMultipleOf4(n)) violations.push(node.value)
      }
    }
    return undefined
  })

  return violations
}

const plugin = stylelint.createPlugin(ruleName, (enabled, _options, context) => {
  return (root, result) => {
    if (!enabled) return

    root.walkDecls((decl) => {
      const prop = decl.prop.toLowerCase()
      if (!TARGET_PROP_RE.test(prop)) return
      // border/border-* shorthand also carries color/style keywords — fine,
      // the word-matcher above only flags tokens ending in "px".

      const violations = collectPxViolations(decl.value)
      for (const v of violations) {
        if (context.fix) {
          // No safe auto-fix — the correct multiple of 4 is a design
          // decision, not mechanically derivable. Report only.
        }
        stylelint.utils.report({
          message: messages.rejected(v, decl.prop),
          node: decl,
          result,
          ruleName
        })
      }
    })
  }
})

plugin.ruleName = ruleName
plugin.messages = messages
plugin.meta = meta

module.exports = plugin
