// tools/design-grid/collect.js — the runtime layer's in-page collector.
//
// `auditDocument` is serialized into the browser by audit.mjs, so it must be
// self-contained: every helper is nested inside it and nothing outside the
// function body may be referenced. That is the reason for its size.
//
// It measures what the browser actually laid out, which is the only place the
// four relational checks can be answered at all. A stylesheet where every
// literal divides by 4 still produces misaligned siblings once percentages,
// intrinsic sizing and flex free space are resolved.

/**
 * Collects every laid-out box under document.body and applies the four runtime
 * checks. Scroll positions are normalized here, before anything is measured.
 *
 * @param {{policy: object, waivers: Array<{id: string, check: string, selector: string, subtree: boolean}>}} input
 * @returns {{elements: number, unchecked: number, violations: Array<object>, usedWaiverIds: Array<string>, selectorErrors: Array<string>}}
 */
export function auditDocument(input) {
  const { policy, waivers } = input
  const { gridUnit, tolerancePx, spacingScale } = policy
  const skipTags = new Set(policy.runtime.skipTags.map((t) => t.toUpperCase()))
  const hairline = new Set(policy.hairlineExemptPx)

  const START_PACKED = new Set(['normal', 'flex-start', 'start', 'left'])
  const FREE_SPACE = new Set(['space-between', 'space-around', 'space-evenly'])
  const BASELINE = new Set(['baseline', 'first baseline', 'last baseline'])
  const IN_FLOW = new Set(['static', 'relative', 'sticky'])

  const violations = []
  const usedWaiverIds = new Set()
  const selectorErrors = []
  let unchecked = 0

  // ── scroll normalization ────────────────────────────────────────────────
  // getBoundingClientRect is viewport-relative, so with every scroll port at
  // origin the coordinates it returns are absolute page coordinates. Without
  // this a scrolled list reports every row as off-grid by the scroll remainder.
  if (document.scrollingElement) {
    document.scrollingElement.scrollTop = 0
    document.scrollingElement.scrollLeft = 0
  }
  for (const el of document.querySelectorAll('*')) {
    if (el.scrollTop) el.scrollTop = 0
    if (el.scrollLeft) el.scrollLeft = 0
  }

  // ── waiver index ────────────────────────────────────────────────────────
  const waived = new Map() // Element -> Map<check, waiverId>

  const addWaiver = (el, check, id) => {
    let m = waived.get(el)
    if (!m) waived.set(el, (m = new Map()))
    if (!m.has(check)) m.set(check, id)
  }

  for (const w of waivers) {
    let matches
    try {
      matches = document.querySelectorAll(w.selector)
    } catch {
      selectorErrors.push(`waiver "${w.id}": "${w.selector}" is not a valid CSS selector`)
      continue
    }
    for (const el of matches) {
      addWaiver(el, w.check, w.id)
      if (!w.subtree) continue
      for (const kid of el.querySelectorAll('*')) addWaiver(kid, w.check, w.id)
    }
  }

  /** The waiver id covering `check` on `el`, or null. */
  const waiverFor = (el, check) => {
    const m = waived.get(el)
    if (!m) return null
    return m.get(check) ?? m.get('*') ?? null
  }

  // ── geometry predicates ─────────────────────────────────────────────────
  const onGrid = (v) => Math.abs(v - gridUnit * Math.round(v / gridUnit)) <= tolerancePx
  const onScale = (v) => spacingScale.some((s) => Math.abs(v - s) <= tolerancePx)
  const near = (a, b) => Math.abs(a - b) <= tolerancePx
  const round2 = (v) => Math.round(v * 100) / 100

  // ── selector paths, for a message a human can act on ────────────────────
  // Per-component style scopes: Svelte's own `svelte-xxxx` and the `s-XXXX`
  // form m3-svelte ships. Both rehash whenever that component's CSS changes,
  // so keeping them would make every path in every message churn on edits that
  // moved nothing. The length floor keeps m3-svelte's real one-letter size
  // classes (`s`, `xs`) out of it.
  const SVELTE_SCOPE = /^(svelte-[a-z0-9]+|s-[A-Za-z0-9_-]{8,})$/

  const pathOf = (el) => {
    const parts = []
    let cur = el
    while (cur && cur.nodeType === 1 && parts.length < 6) {
      if (cur.id) {
        parts.unshift(`${cur.tagName.toLowerCase()}#${cur.id}`)
        break
      }
      let s = cur.tagName.toLowerCase()
      const cls = (cur.getAttribute('class') || '')
        .trim()
        .split(/\s+/)
        .filter((c) => c && !SVELTE_SCOPE.test(c))
        .slice(0, 2)
      if (cls.length) s += `.${cls.join('.')}`
      const parent = cur.parentElement
      if (parent) s += `:nth-child(${[...parent.children].indexOf(cur) + 1})`
      parts.unshift(s)
      if (cur === document.body || !parent) break
      cur = parent
    }
    return parts.join(' > ')
  }

  const record = (el, check, detail) => {
    const waivedBy = waiverFor(el, check)
    if (waivedBy) {
      usedWaiverIds.add(waivedBy)
      return
    }
    violations.push({ layer: 'runtime', check, selector: pathOf(el), ...detail })
  }

  /** Same as `record`, but a waiver on either participant also covers it. */
  const recordPair = (owner, a, b, check, detail) => {
    const waivedBy = waiverFor(owner, check) ?? waiverFor(a, check) ?? waiverFor(b, check)
    if (waivedBy) {
      usedWaiverIds.add(waivedBy)
      return
    }
    violations.push({ layer: 'runtime', check, selector: pathOf(owner), ...detail })
  }

  // ── collection ──────────────────────────────────────────────────────────
  const boxes = []
  const vw = window.innerWidth
  const vh = window.innerHeight

  const walk = (el, parentIndex) => {
    const tag = el.tagName.toUpperCase()
    if (skipTags.has(tag)) return

    const cs = window.getComputedStyle(el)
    if (cs.display === 'none' || cs.visibility === 'hidden') return

    let index = parentIndex
    const r = el.getBoundingClientRect()
    const offscreen = r.right <= 0 || r.bottom <= 0 || r.left >= vw || r.top >= vh

    // `display: inline` boxes are text runs: their width is a sum of glyph
    // advances and will essentially never divide by 4. The height their line
    // boxes impose on the containing block is measured on that block instead,
    // and the static layer holds line-height to the grid.
    //
    // A zero-area box is collected rather than dropped, and marked. Dropping it
    // was wrong: a flex spacer (`flex: 1 1 auto` with no content) has no height
    // but does have width, and leaving it out of the sibling list turned its
    // width into a gap between its neighbours -- 1186px of "spacing" on the
    // preview toolbar. It takes part in ordering and gaps, and in nothing else.
    const zeroArea = r.width === 0 || r.height === 0
    const measurable = cs.display !== 'inline' && !offscreen

    if (measurable) {
      index = boxes.length
      boxes.push({
        el,
        parent: parentIndex,
        zeroArea,
        left: r.left,
        top: r.top,
        right: r.right,
        bottom: r.bottom,
        width: r.width,
        height: r.height,
        display: cs.display,
        position: cs.position,
        flexDirection: cs.flexDirection,
        gridAutoFlow: cs.gridAutoFlow,
        alignItems: cs.alignItems,
        justifyContent: cs.justifyContent,
        transform: cs.transform,
        translate: cs.translate,
        padTop: parseFloat(cs.paddingTop) || 0,
        padRight: parseFloat(cs.paddingRight) || 0,
        padBottom: parseFloat(cs.paddingBottom) || 0,
        padLeft: parseFloat(cs.paddingLeft) || 0,
        borderTop: parseFloat(cs.borderTopWidth) || 0,
        borderRight: parseFloat(cs.borderRightWidth) || 0,
        borderBottom: parseFloat(cs.borderBottomWidth) || 0,
        borderLeft: parseFloat(cs.borderLeftWidth) || 0,
        children: []
      })
      if (parentIndex >= 0) boxes[parentIndex].children.push(index)
    }

    for (const kid of el.children) walk(kid, index)
  }

  walk(document.body, -1)

  // ── check 1: grid snap ──────────────────────────────────────────────────
  //
  // Two narrowings, both about whose decision the number is.
  //
  // Size is checked only for a box with no text anywhere under it. Any box
  // that contains text is glyph-driven somewhere along its size derivation --
  // a <button> is as wide as its label, and a card is as tall as the line
  // boxes in it -- so holding it to the grid measures the font, not the
  // stylesheet. What remains is pure structure: icon wells, dividers,
  // thumbnails, progress bars, spacers, checkboxes. Those a stylesheet does
  // size, and they must be on the grid. The height a line box imposes is
  // covered instead by the static layer, which holds line-height to the grid.
  //
  // Position is checked only where it was authored, meaning position absolute
  // or fixed, and then as the offset from the containing block rather than as
  // an absolute page coordinate. A centred layout puts its subtree on
  // (container - content) / 2, fractional whenever the content is, and every
  // descendant inherits that fraction; nothing about it is a defect. The inset
  // from the containing block is the number a stylesheet actually wrote.
  // Where a sibling sits relative to its neighbours is covered by
  // sibling-edges, spacing-scale and optical-imbalance, all of which read
  // relative distances and are immune to a fractional origin.
  for (const b of boxes) {
    if (b.zeroArea) continue
    const textSized = b.el.textContent.trim() !== ''
    const positioned = b.position === 'absolute' || b.position === 'fixed'

    // A hairline is a hairline at runtime too. The static rule already exempts
    // 1, 2 and 3 for sizing, and a 1px <hr> failing here while passing there
    // would mean the two layers disagree about the same rule.
    const sizeOk = (v) => onGrid(v) || hairline.has(Math.abs(v))

    // Three more ways a size stops being the stylesheet's decision.
    //
    // An element wrapping an <svg> is as big as the icon's line box, which the
    // font and the viewBox decide. A grid item is as big as its track, and
    // `auto-fill`/`fr` tracks divide the free space. And an element that fills
    // its parent's content box is reporting the parent's number: checking it
    // again at every level of a stack turns one derived value into a dozen
    // violations pointing at the same cause. The parent is checked; this is not.
    const host = b.parent >= 0 ? boxes[b.parent] : null
    const gridItem = host !== null && (host.display === 'grid' || host.display === 'inline-grid')
    const iconSized = b.el.querySelector('svg') !== null

    const fills = (size, pad0, pad1, border0, border1) =>
      host !== null &&
      Math.abs(b[size] - (host[size] - host[pad0] - host[pad1] - host[border0] - host[border1])) <=
        tolerancePx

    const checks = []
    if (!textSized && !iconSized && !gridItem) {
      if (!fills('width', 'padLeft', 'padRight', 'borderLeft', 'borderRight')) {
        checks.push(['width', b.width])
      }
      if (!fills('height', 'padTop', 'padBottom', 'borderTop', 'borderBottom')) {
        checks.push(['height', b.height])
      }
    }
    // A transform moves the painted box away from where the inset put it, so
    // the rect no longer reports the authored number. `left: 50%` plus
    // `translateX(-50%)` -- the standard way to centre a fixed bar -- lands on
    // (viewport - width) / 2, fractional whenever the content is, and the
    // stylesheet said "50%", which is exactly on the grid in the only sense
    // available to it.
    const moved = (b.transform && b.transform !== 'none') || (b.translate && b.translate !== 'none')

    if (positioned && !moved) {
      // offsetParent is null for a fixed box, whose containing block is the
      // viewport, so the absolute coordinate is the authored one there.
      const host = b.el.offsetParent
      const origin = host ? host.getBoundingClientRect() : { left: 0, top: 0 }
      checks.push(['inset-left', b.left - origin.left], ['inset-top', b.top - origin.top])
    }

    for (const [edge, v] of checks) {
      if (sizeOk(v)) continue
      record(b.el, 'grid-snap', {
        actual: `${edge} ${round2(v)}px`,
        expected: `a multiple of ${gridUnit}px (nearest ${gridUnit * Math.round(v / gridUnit)}px)`
      })
    }
  }

  // ── checks 2 to 4: relations between siblings ───────────────────────────
  const isGrid = (b) => b.display === 'grid' || b.display === 'inline-grid'

  const mainAxisOf = (b) => {
    if (b.display === 'flex' || b.display === 'inline-flex') {
      return b.flexDirection.startsWith('row') ? 'x' : 'y'
    }
    // A grid is two-dimensional. Treating its rows as horizontal groups is the
    // only reading under which "the gap between these two" means anything.
    if (isGrid(b)) return 'x'
    return 'y'
  }

  const AX = {
    x: { start: 'left', end: 'right', size: 'width', cs: 'top', ce: 'bottom' },
    y: { start: 'top', end: 'bottom', size: 'height', cs: 'left', ce: 'right' }
  }

  /**
   * Splits children into the lines they visually occupy, by overlap on the
   * cross axis. A non-wrapping stack yields one line, which is what makes the
   * same code serve a column, a row and a wrapped grid.
   */
  const linesOf = (kids, ax) => {
    const sorted = [...kids].sort((p, q) => p[ax.cs] - q[ax.cs] || p[ax.start] - q[ax.start])
    const lines = []
    let line = null
    let lineEnd = -Infinity
    for (const k of sorted) {
      if (line && k[ax.cs] < lineEnd - tolerancePx) {
        line.push(k)
        lineEnd = Math.max(lineEnd, k[ax.ce])
        continue
      }
      line = [k]
      lineEnd = k[ax.ce]
      lines.push(line)
    }
    return lines
  }

  for (const parent of boxes) {
    if (parent.zeroArea) continue

    // ── check 3a: padding symmetry. Used values, so the comparison is exact.
    if (parent.padLeft !== parent.padRight) {
      record(parent.el, 'padding-asymmetry', {
        actual: `padding-left ${parent.padLeft}px vs padding-right ${parent.padRight}px`,
        expected: 'equal inline padding, or a waiver saying why not'
      })
    }
    if (parent.padTop !== parent.padBottom) {
      record(parent.el, 'padding-asymmetry', {
        actual: `padding-top ${parent.padTop}px vs padding-bottom ${parent.padBottom}px`,
        expected: 'equal block padding, or a waiver saying why not'
      })
    }

    const kids = parent.children.map((i) => boxes[i]).filter((k) => IN_FLOW.has(k.position))
    if (kids.length < 2) continue

    const axis = mainAxisOf(parent)
    const ax = AX[axis]
    const lines = linesOf(kids, ax)

    for (const line of lines) {
      if (line.length < 2) continue
      const ordered = [...line].sort((p, q) => p[ax.start] - q[ax.start])
      // A spacer has no cross extent to line up with anything; it is here for
      // the gap arithmetic below and nothing else.
      const solid = ordered.filter((k) => !k.zeroArea)

      // ── check 2: cross-axis edge coherence ──
      if (BASELINE.has(parent.alignItems)) {
        // Measuring a first-line baseline from script needs a probe injected
        // into the layout being measured, which changes it. Counted, not guessed.
        unchecked += 1
      } else if (solid.length >= 2) {
        const minStart = Math.min(...solid.map((k) => k[ax.cs]))
        const maxEnd = Math.max(...solid.map((k) => k[ax.ce]))
        const stretched = (k) => near(k[ax.cs], minStart) && near(k[ax.ce], maxEnd)

        const agrees = (pick) => {
          const rest = solid.filter((k) => !stretched(k))
          if (rest.length < 2) return true
          const first = pick(rest[0])
          return rest.every((k) => near(pick(k), first))
        }

        const byStart = (k) => k[ax.cs]
        const byEnd = (k) => k[ax.ce]
        const byCentre = (k) => (k[ax.cs] + k[ax.ce]) / 2

        if (!agrees(byStart) && !agrees(byEnd) && !agrees(byCentre)) {
          const rest = solid.filter((k) => !stretched(k))
          const base = byStart(rest[0])
          let worst = rest[0]
          for (const k of rest) {
            if (Math.abs(byStart(k) - base) > Math.abs(byStart(worst) - base)) worst = k
          }
          record(parent.el, 'sibling-edges', {
            actual: `${axis === 'x' ? 'top' : 'left'} edges ${rest.map((k) => round2(byStart(k))).join(', ')}px`,
            expected: `a shared start, end or centre on the ${axis === 'x' ? 'block' : 'inline'} axis`,
            offender: pathOf(worst.el)
          })
        }
      }

      // ── check 4: inter-sibling gaps against the spacing scale ──
      // space-* distributes free space, so its gaps are computed, not authored.
      if (FREE_SPACE.has(parent.justifyContent)) continue
      for (let i = 1; i < ordered.length; i += 1) {
        const prev = ordered[i - 1]
        const next = ordered[i]
        const gap = next[ax.start] - prev[ax.end]
        if (gap < -tolerancePx) {
          recordPair(parent.el, prev.el, next.el, 'overlap', {
            actual: `${round2(gap)}px between two in-flow siblings`,
            expected: 'no overlap',
            offender: pathOf(next.el)
          })
          continue
        }
        if (onScale(gap)) continue
        recordPair(parent.el, prev.el, next.el, 'spacing-scale', {
          actual: `${round2(gap)}px gap`,
          expected: `one of [${spacingScale.join(', ')}]`,
          offender: pathOf(next.el)
        })
      }
    }

    // ── check 3b: the leading inset is not paid twice ──
    //
    // Only the leading gap, never lead-against-trail. A container is routinely
    // larger than its content -- a nav rail is as tall as the window -- so the
    // trailing gap is free space and comparing the two reported every stretched
    // container in the app. What is checkable is that a start-packed
    // container's first child sits flush against the content box: anything
    // else is a margin adding space the padding already provides, which is the
    // classic reason a padded box looks top-heavy while every declared value
    // reads correct.
    if (!START_PACKED.has(parent.justifyContent)) continue
    // Spacers count here. A reserved but empty column -- a chip slot on a row
    // that has no chip -- has width and no height, and leaving it out made its
    // width read as leading space the container had put there.
    const first = [...kids].sort((p, q) => p[ax.start] - q[ax.start])[0]
    const contentStart =
      axis === 'x' ? parent.left + parent.borderLeft + parent.padLeft : parent.top + parent.borderTop + parent.padTop
    const lead = first[ax.start] - contentStart
    if (Math.abs(lead) > tolerancePx) {
      record(parent.el, 'optical-imbalance', {
        actual: `${round2(lead)}px between the content edge and the first child`,
        expected: 'the first child flush against the content box; the padding is the inset',
        offender: pathOf(first.el)
      })
    }
  }

  // ── check 5: a lone child sits centred in the box it was given ──────────
  //
  // Checks 2 to 4 all start with `kids.length < 2` and walk away, so nothing
  // ever asked whether a single child is centred in its parent. That is where
  // two of this app's visible defects were hiding: a 20px icon pinned to the
  // top-left corner of the 32px slot built for it, and a 32px switch pinned to
  // the top of the 40px cell a stretching flex row gave its wrapper.
  //
  // Only boxes strictly smaller than their parent's content box on both axes
  // are asked. Anything that fills its parent on either axis is doing what it
  // was told, and text is exempt because a line box is not a control.
  for (const parent of boxes) {
    if (parent.zeroArea) continue
    const kids = parent.children.map((i) => boxes[i]).filter((k) => IN_FLOW.has(k.position) && !k.zeroArea)
    if (kids.length !== 1) continue

    const kid = kids[0]
    if (kid.el.textContent.trim() !== '') continue

    const innerW = parent.width - parent.padLeft - parent.padRight - parent.borderLeft - parent.borderRight
    const innerH = parent.height - parent.padTop - parent.padBottom - parent.borderTop - parent.borderBottom
    if (kid.width >= innerW - tolerancePx || kid.height >= innerH - tolerancePx) continue

    for (const [axis, lead, trail] of [
      ['inline', kid.left - (parent.left + parent.borderLeft + parent.padLeft), (parent.right - parent.borderRight - parent.padRight) - kid.right],
      ['block', kid.top - (parent.top + parent.borderTop + parent.padTop), (parent.bottom - parent.borderBottom - parent.padBottom) - kid.bottom]
    ]) {
      if (Math.abs(lead - trail) <= tolerancePx) continue
      record(parent.el, 'box-centring', {
        actual: `${round2(lead)}px before and ${round2(trail)}px after its only child on the ${axis} axis`,
        expected: 'a lone child centred in the box it was given',
        offender: pathOf(kid.el)
      })
    }
  }

  // ── check 6: controls in a row share the row's centre ───────────────────
  //
  // A row's own children can agree with each other and still stagger, because
  // a control nested one level deeper is nobody's sibling. The admin user row
  // centred its switch, chip and buttons on the row and left the badge on the
  // headline's line, 8px above all of them, and check 2 saw two children that
  // agreed.
  //
  // Form controls only. A name and a supporting line are meant to stack, and
  // asking a paragraph to sit on the row's centre would be asking for the
  // wrong thing.
  const CONTROL_TAGS = new Set(['BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'LABEL'])
  const CENTRING_ALIGN = new Set(['center', 'normal', 'stretch', 'anchor-center'])

  for (const parent of boxes) {
    if (parent.zeroArea) continue
    if (parent.display !== 'flex' && parent.display !== 'inline-flex') continue
    if (!parent.flexDirection.startsWith('row')) continue
    // An explicit start, end or baseline alignment is an answer, not an
    // accident. The audit log's filter bar bottom-aligns a 56px field against
    // a 40px button on purpose, which is what `align-items: flex-end` says.
    if (!CENTRING_ALIGN.has(parent.alignItems)) continue

    const centre = (parent.top + parent.bottom) / 2

    // Only the controls this row itself lays out. Without the nearest-ancestor
    // test, a page-level flex row collected every control on the page and
    // measured all of them against its own centre: 220 violations that all
    // named the same two containers.
    const isRow = (el) => {
      const cs = window.getComputedStyle(el)
      return (
        (cs.display === 'flex' || cs.display === 'inline-flex') && cs.flexDirection.startsWith('row')
      )
    }
    const ownedByThisRow = (el) => {
      for (let a = el.parentElement; a && a !== parent.el; a = a.parentElement) {
        if (isRow(a)) return false
      }
      return true
    }

    const controls = [...parent.el.querySelectorAll('button, input, select, textarea, label')].filter(
      (el) => ownedByThisRow(el)
    )
    if (controls.length < 2) continue

    for (const el of controls) {
      // A control that wraps another is reported through the inner one.
      if (controls.some((other) => other !== el && el.contains(other))) continue
      const r = el.getBoundingClientRect()
      if (r.width === 0 || r.height === 0) continue
      // Taller than the row means it is what sets the row's height, and a row
      // several times its tallest control is a layout column, not a row.
      if (r.height >= parent.height - tolerancePx) continue
      if (parent.height > r.height * 3) continue
      const off = (r.top + r.bottom) / 2 - centre
      if (Math.abs(off) <= tolerancePx) continue
      record(el, 'control-centring', {
        actual: `${round2(off)}px off the row's centre`,
        expected: "every control in a row on the row's own centre",
        offender: pathOf(parent.el)
      })
    }
  }

  const INTERACTIVE = /^(BUTTON|A|INPUT|SELECT|TEXTAREA|SUMMARY)$/

  /** Where the text inside `el` actually starts or stops, or null if it has none. */
  const textEdge = (el, side) => {
    const range = document.createRange()
    range.selectNodeContents(el)
    const rects = [...range.getClientRects()].filter((r) => r.height > 0 && r.width > 0)
    range.detach()
    if (rects.length === 0) return null
    return side === 'top'
      ? Math.min(...rects.map((r) => r.top))
      : Math.max(...rects.map((r) => r.bottom))
  }

  // ── check 7: stacked text blocks are not flush against each other ───────
  //
  // `spacing-scale` accepts 0, because two boxes really are meant to touch in
  // a bordered list or a segmented control. It therefore says nothing about a
  // caption printed hard against the control it describes, which is what a
  // person means by "too tight". Two text blocks stacked with nothing between
  // them -- no border, no background of their own -- have no reason to touch.
  for (const parent of boxes) {
    if (parent.zeroArea) continue
    const kids = parent.children
      .map((i) => boxes[i])
      .filter((k) => IN_FLOW.has(k.position) && !k.zeroArea)
    if (kids.length < 2) continue
    if (mainAxisOf(parent) !== 'y') continue
    // Block flow only. A flex column with `gap: 0` is stacking lines on
    // purpose -- a list item's headline and its supporting line touch because
    // their line boxes carry the leading, and that is right.
    if (parent.display !== 'block') continue

    const ordered = [...kids].sort((p, q) => p.top - q.top)
    for (let i = 1; i < ordered.length; i += 1) {
      const prev = ordered[i - 1]
      const next = ordered[i]
      if (next.top - prev.bottom > tolerancePx) continue
      if (prev.el.textContent.trim() === '' || next.el.textContent.trim() === '') continue
      // A stack of buttons or links is a menu, and its rows abut on purpose:
      // each one's own padding is the space around its label, and a gap
      // between them would break the run of hit targets.
      if (INTERACTIVE.test(prev.el.tagName) || INTERACTIVE.test(next.el.tagName)) continue
      if (prev.el.querySelector('button, a[href]') || next.el.querySelector('button, a[href]')) continue
      const a = window.getComputedStyle(prev.el)
      const b = window.getComputedStyle(next.el)
      const separated =
        parseFloat(a.borderBottomWidth) > 0 ||
        parseFloat(b.borderTopWidth) > 0 ||
        a.backgroundColor !== b.backgroundColor
      if (separated) continue

      // Boxes touching is not the same as text touching. A 56px drawer header
      // with one centred line has 18px of slack inside it, and a grid section
      // starts with a label whose own padding holds it clear, so neither looks
      // crowded even though the two boxes share an edge. Measured with a Range
      // over the text itself rather than over element boxes: a box with
      // padding still begins at its edge, which is what made the first version
      // of this report both of those.
      const inkBottom = textEdge(prev.el, 'bottom')
      const inkTop = textEdge(next.el, 'top')
      if (inkBottom === null || inkTop === null) continue
      const inked = inkTop - inkBottom
      if (inked > tolerancePx) continue

      recordPair(parent.el, prev.el, next.el, 'crowding', {
        actual: `${round2(inked)}px between what the two blocks actually draw`,
        expected: 'space between them, or a border or background that separates them',
        offender: pathOf(next.el)
      })
    }
  }

  // ── check 8: column coherence across repeated rows ──────────────────────
  //
  // The one a person points at first. In a list of repeated rows, the same
  // control has to sit in the same column on every row; when one row drops an
  // optional badge or gains an extra action, everything after it slides and the
  // eye reads a zigzag down the right-hand side.
  //
  // Checks 2 to 4 are all blind to it, and provably so: they compare siblings
  // inside one container, and the elements that disagree here are in different
  // rows. Every row is internally correct -- its own gaps are on the scale, its
  // own children share an edge -- and the rows only disagree with each other.
  //
  // Rows are matched by tag plus class signature, and elements inside them by
  // depth plus tag plus class signature. A signature seen in two or more rows
  // must occupy one column: either every instance shares a start edge or every
  // instance shares an end edge. Sharing the start is enough on its own, which
  // is what lets a name cell be as wide as its text.
  const SIGNIFICANT_TAGS = new Set(['BUTTON', 'INPUT', 'A', 'IMG', 'LABEL', 'SELECT', 'TEXTAREA'])

  const classSig = (el) =>
    (el.getAttribute('class') || '')
      .split(/\s+/)
      .filter((c) => c && !SVELTE_SCOPE.test(c))
      .sort()
      .join('.')

  for (const parent of boxes) {
    if (parent.zeroArea) continue
    const kids = parent.children
      .map((i) => boxes[i])
      .filter((k) => IN_FLOW.has(k.position) && !k.zeroArea)
    if (kids.length < 2) continue

    const ax = AX[mainAxisOf(parent)]

    // A repeated row is not merely a sibling with the same class: it has the
    // same shape. Keying on tag and class alone made every <section> on the
    // settings page a "row" of its neighbours, and then compared the first
    // button of the account section against the first button of the theme
    // section, which are not the same control and were never meant to line up.
    // Requiring an identical sequence of direct-child signatures keeps the
    // group to things that really are repetitions of one template.
    const shapeOf = (el) =>
      [...el.children].map((c) => `${c.tagName}.${classSig(c)}`).join(',')

    const rowGroups = new Map()
    for (const k of kids) {
      const key = `${k.el.tagName}|${classSig(k.el)}|${shapeOf(k.el)}`
      if (!rowGroups.has(key)) rowGroups.set(key, [])
      rowGroups.get(key).push(k)
    }

    for (const rows of rowGroups.values()) {
      if (rows.length < 2) continue

      const bySig = new Map()
      rows.forEach((row, ri) => {
        const stack = [[row.el, 0]]
        while (stack.length > 0) {
          const [el, depth] = stack.pop()
          for (const child of el.children) {
            const sig = classSig(child)
            // The parent's classes are part of the identity. Without them a
            // chip in the state column and a chip in the quota column shared
            // one signature, and the ordinal pairing then matched the state
            // chip of one row against the quota chip of the next.
            const under = classSig(el)
            if (sig !== '' || SIGNIFICANT_TAGS.has(child.tagName)) {
              const r = child.getBoundingClientRect()
              // An inline box sits wherever the text before it ended, so its
              // start edge is a sentence length, not a column. The audit log's
              // result badge follows a variable-length message and was reported
              // on every row for doing exactly what inline flow does.
              const inline = window.getComputedStyle(child).display.startsWith('inline')
              if (!inline && r.width > 0 && r.height > 0) {
                const key = `${depth}|${under}|${child.tagName}|${sig}`
                if (!bySig.has(key)) bySig.set(key, new Map())
                const perRow = bySig.get(key)
                if (!perRow.has(ri)) perRow.set(ri, [])
                const before = child.previousElementSibling
                perRow.get(ri).push({
                  el: child,
                  r,
                  // How much room the thing in front of it took. When that
                  // differs from row to row, this element's start edge is the
                  // length of a sentence rather than a column: the audit log's
                  // result badge follows the event name and is meant to.
                  before: before ? before.getBoundingClientRect().width : 0
                })
              }
            }
          }
          // Pushed in reverse so `pop` hands them back in document order.
          // A stack walks children last-to-first, which made "the first button
          // in this row" mean the edit button on one row and the delete button
          // on the next, and the two columns then never agreed.
          for (let i = el.children.length - 1; i >= 0; i -= 1) {
            stack.push([el.children[i], depth + 1])
          }
        }
      })

      // Shallowest first, and a signature whose ancestor already reported is
      // skipped. One shifted control drags its whole subtree with it, and
      // reporting the wrapper, the label, the switch, its container, its input
      // and its handle turns a single defect into eight lines that all say the
      // same number.
      const reported = []
      const ordered = [...bySig.entries()].sort(
        (a, b) => Number(a[0].split('|')[0]) - Number(b[0].split('|')[0])
      )

      for (const [key, perRow] of ordered) {
        if (perRow.size < 2) continue

        // Compared by ordinal within the row, not as one pool. A row with an
        // extra action button holds two elements of the same signature; pooling
        // them made the second one's column read as the first one's being
        // wrong. The k-th instance of a signature is what has to line up with
        // the k-th instance on every other row.
        const widest = Math.max(...[...perRow.values()].map((v) => v.length))
        for (let k = 0; k < widest; k += 1) {
          const all = []
          for (const instances of perRow.values()) if (instances[k]) all.push(instances[k])
          if (all.length < 2) continue

          const starts = all.map((x) => x.r[ax.cs])
          const ends = all.map((x) => x.r[ax.ce])
          const spread = (xs) => Math.max(...xs) - Math.min(...xs)
          if (spread(starts) <= tolerancePx || spread(ends) <= tolerancePx) continue
          // Pushed along by something whose own width is content, so its start
          // was never a column to begin with.
          if (spread(all.map((x) => x.before)) > tolerancePx) continue
          if (all.some((x) => reported.some((r) => r !== x.el && r.contains(x.el)))) continue

          const worst = all.reduce((acc, x) =>
            Math.abs(x.r[ax.cs] - starts[0]) > Math.abs(acc.r[ax.cs] - starts[0]) ? x : acc
          )
          recordPair(parent.el, all[0].el, worst.el, 'column-coherence', {
            actual: `"${key.split('|').slice(2).join(' ')}"${widest > 1 ? ` #${k + 1}` : ''} starts at ${[...new Set(starts.map(round2))].sort((a, b) => a - b).join(', ')}px and ends at ${[...new Set(ends.map(round2))].sort((a, b) => a - b).join(', ')}px across ${all.length} rows`,
            expected: 'one column: a shared start edge, or a shared end edge',
            offender: pathOf(worst.el)
          })
          for (const x of all) reported.push(x.el)
        }
      }
    }
  }

  return {
    elements: boxes.length,
    unchecked,
    violations,
    usedWaiverIds: [...usedWaiverIds],
    selectorErrors
  }
}
