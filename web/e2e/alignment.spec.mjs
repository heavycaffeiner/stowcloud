// Run: pnpm test:alignment. Uses the development fixtures and real browser layout.
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { createServer } from 'vite'

const server = await createServer({
  root: fileURLToPath(new URL('..', import.meta.url)),
  mode: 'development',
  define: { 'import.meta.env.VITE_API_MOCK': JSON.stringify('1') },
  server: { host: '127.0.0.1', port: 0, open: false },
  logLevel: 'error'
})
let browser
let checks = 0

async function settle(page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    await Promise.all([...document.querySelectorAll('mdui-button, mdui-button-icon, mdui-segmented-button')].map(el => el.updateComplete))
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  })
}

async function checkButtons(page, scope = 'body') {
  await settle(page)
  const result = await page.locator(scope).evaluateAll(roots => {
    const errors = []
    let count = 0
    for (const root of roots) {
      for (const button of root.querySelectorAll('button, mdui-button, mdui-button-icon, mdui-segmented-button')) {
        const rect = button.getBoundingClientRect()
        if (!rect.width || !rect.height || !button.checkVisibility()) continue
        const parts = []
        for (const node of button.childNodes) {
          if (node.nodeType === Node.TEXT_NODE && node.textContent.trim()) {
            const range = document.createRange()
            range.selectNodeContents(node)
            parts.push(range.getBoundingClientRect())
          } else if (node instanceof Element && node.checkVisibility() && !node.matches('.sc-sr-only, [slot="icon"]:empty')) {
            parts.push(node.getBoundingClientRect())
          }
        }
        const visible = parts.filter(part => part.width > 0 && part.height > 0)
        const vertical = getComputedStyle(button).flexDirection === 'column'
        const target = vertical ? rect.x + rect.width / 2 : rect.y + rect.height / 2
        for (const part of visible) {
          const center = vertical ? part.x + part.width / 2 : part.y + part.height / 2
          if (Math.abs(center - target) > 1) {
            errors.push(`${button.tagName}.${button.className} ${button.textContent.trim().slice(0, 40)}: ${vertical ? 'x' : 'y'} offset ${(center - target).toFixed(2)}px`)
          }
        }
        count += 1
      }
    }
    return { errors, count }
  })
  assert.ok(result.count > 0, `No buttons exercised in ${scope}`)
  assert.deepEqual(result.errors, [], `Button content alignment in ${scope}`)
  checks += result.count
}

async function checkGroup(page, selector, axis, edge = 'center') {
  const groups = await page.locator(selector).evaluateAll((elements, { axis, edge }) => elements.map(group => {
    const children = [...group.children].filter(el => el.checkVisibility()).map(el => el.getBoundingClientRect()).filter(r => r.width && r.height)
    return children.map(r => axis === 'y' ? r.y + r.height / 2 : edge === 'start' ? r.x : edge === 'end' ? r.right : r.x + r.width / 2)
  }).filter(values => values.length > 1), { axis, edge })
  assert.ok(groups.length > 0, `No populated group exercised: ${selector}`)
  for (const values of groups) assert.ok(Math.max(...values) - Math.min(...values) <= 1, `${selector}: ${axis} ${edge} positions ${values.join(', ')}`)
  checks += groups.length
}

async function checkNewCenter(page) {
  const offsets = await page.locator('.sc-nav-drawer__new-btn').evaluate(button => {
    const rect = button.getBoundingClientRect()
    const parts = [...button.children].map(el => el.getBoundingClientRect())
    const left = Math.min(...parts.map(r => r.left))
    const right = Math.max(...parts.map(r => r.right))
    return { x: (left + right) / 2 - rect.x - rect.width / 2, y: parts.map(r => r.y + r.height / 2 - rect.y - rect.height / 2) }
  })
  assert.ok(Math.abs(offsets.x) <= 1, `New content horizontal offset: ${offsets.x}px`)
  assert.ok(offsets.y.every(offset => Math.abs(offset) <= 1), `New content vertical offsets: ${offsets.y}`)
  checks += 1
}

async function checkDateColumn(page) {
  const geometry = await page.evaluate(() => {
    const header = document.querySelector('.sc-file-table__header-cell--mtime').getBoundingClientRect()
    const cells = [...document.querySelectorAll('.sc-row__cell--mtime')].map(el => {
      const rect = el.getBoundingClientRect()
      return { x: rect.x, width: rect.width, text: el.textContent, clipped: el.scrollWidth > el.clientWidth }
    })
    return { header: { x: header.x, width: header.width }, cells }
  })
  assert.ok(geometry.cells.length > 1, 'Date column needs multiple rows')
  for (const cell of geometry.cells) {
    assert.match(cell.text, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    assert.ok(Math.abs(cell.x - geometry.header.x) <= 1 && Math.abs(cell.width - geometry.header.width) <= 1, 'Date cells must share header position and width')
    assert.equal(cell.clipped, false, 'Date text must fit without clipping')
  }
  checks += geometry.cells.length
  return geometry.header.width
}

async function dragBetween(page, selector) {
  const items = page.locator(selector)
  assert.ok(await items.count() >= 2, `${selector}: drag selection needs two items`)
  const [first, second] = await Promise.all([items.nth(0).boundingBox(), items.nth(1).boundingBox()])
  assert.ok(first && second, `${selector}: drag selection items must be visible`)
  await page.mouse.move(first.x + first.width / 2, first.y + first.height / 2)
  await page.mouse.down()
  await page.mouse.move(second.x + second.width / 2, second.y + second.height / 2, { steps: 5 })
  await page.mouse.up()
  await settle(page)
  const selected = await items.evaluateAll(elements => elements.filter(element => element.getAttribute('aria-selected') === 'true').length)
  assert.ok(selected >= 2, `${selector}: drag selection must persist after pointerup`)
  checks += 1
}

async function checkDragSelection(page) {
  await dragBetween(page, '.sc-row')
  await page.locator('.sc-browse__selection-close-btn').click()
  await page.locator('.sc-browse__selection-bar').waitFor({ state: 'hidden' })

  const viewToggle = page.locator('.sc-browse__toolbar-actions > .sc-browse__action-btn').nth(1)
  await viewToggle.click()
  await page.locator('.sc-file-grid__card').first().waitFor()
  await dragBetween(page, '.sc-file-grid__card')
  const backgrounds = await page.locator('.sc-file-grid__card[aria-selected="true"]').evaluateAll(cards => cards.map(card => getComputedStyle(card).backgroundColor))
  assert.ok(backgrounds.length >= 2, 'Grid drag selection must include multiple cards')
  assert.equal(new Set(backgrounds).size, 1, 'Hovered selected grid card must retain its selected background')
  checks += 1
  await page.locator('.sc-browse__selection-close-btn').click()
  await page.locator('.sc-browse__selection-bar').waitFor({ state: 'hidden' })
  await viewToggle.click()
  await page.locator('.sc-row__cell--mtime').first().waitFor()
}

try {
  await server.listen()
  const base = server.resolvedUrls.local[0]
  browser = await chromium.launch()
  const widths = []
  for (const locale of ['en', 'ko']) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
    await context.addInitScript(locale => {
      localStorage.setItem('sc.locale', locale)
      localStorage.setItem('sc.theme', 'light')
    }, locale)
    const page = await context.newPage()
    await page.goto(`${base}b/home`)
    await page.locator('.sc-row__cell--mtime').first().waitFor()
    await checkButtons(page)
    await checkNewCenter(page)
    await checkGroup(page, '.sc-browse__toolbar-actions', 'y')
    await checkGroup(page, '.sc-nav-drawer__list', 'x', 'start')
    widths.push(await checkDateColumn(page))
    await checkDragSelection(page)

    await page.locator('.sc-nav-drawer__new-btn').focus()
    await page.keyboard.press('Enter')
    await page.locator('.sc-browse-new-menu button').first().waitFor()
    await checkButtons(page, '.sc-browse-new-menu')
    await checkGroup(page, '.sc-browse-new-menu', 'x', 'start')
    await page.locator('.sc-browse-new-menu button').first().click()
    await page.locator('mdui-dialog[open]').waitFor()
    await checkButtons(page, 'mdui-dialog[open]')
    await checkGroup(page, 'mdui-dialog[open] [slot="action"]', 'y')
    await page.keyboard.press('Escape')
    await page.locator('mdui-dialog[open]').waitFor({ state: 'hidden' })

    await page.locator('.sc-shell-header__menu-btn').click()
    await page.locator('.sc-nav-drawer--collapsed').waitFor()
    await checkNewCenter(page)
    await checkGroup(page, '.sc-nav-drawer__body', 'x')
    await checkButtons(page)
    await page.locator('.sc-shell-header__menu-btn').click()

    await page.goto(`${base}settings`)
    await page.locator('.sc-settings-page').waitFor()
    await checkButtons(page)
    await checkGroup(page, '.sc-settings-page__tabs', 'y')
    for (const tab of await page.locator('.sc-settings-page__tab').all()) {
      await tab.click()
      await checkButtons(page)
    }

    await page.goto(`${base}b/home`)
    await page.locator('.sc-row__cell--mtime').first().waitFor()
    await page.locator('.sc-shell-header__search').click()
    await page.locator('.sc-search__category-pill').first().waitFor()
    await checkButtons(page, '.sc-search')
    await checkGroup(page, '.sc-search__categories', 'y')
    await page.keyboard.press('Escape')

    for (const width of [800, 390]) {
      await page.setViewportSize({ width, height: 900 })
      await page.locator('.sc-app-shell--compact').waitFor()
      await checkButtons(page)
      await checkGroup(page, '.sc-nav-bar', 'y')
      await page.locator('.sc-nav-bar__item[aria-haspopup="dialog"]').click()
      await page.locator('.sc-nav-drawer--overlay').waitFor()
      await checkNewCenter(page)
      await checkButtons(page, '.sc-nav-drawer--overlay')
      await checkGroup(page, '.sc-nav-drawer__list', 'x', 'start')
      await page.keyboard.press('Escape')
    }
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'Compact layout must not overflow horizontally')
    await context.close()
  }
  assert.equal(widths[0], widths[1], 'Date column width must not depend on language')
  console.log(`PASS: ${checks} button, group and date geometry checks in English and Korean`)
} finally {
  await browser?.close()
  await server.close()
}
