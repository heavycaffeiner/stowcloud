// Run: pnpm test:alignment. Uses the development fixtures and real browser layout.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
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
const catalogue = (locale) =>
  JSON.parse(readFileSync(new URL(`../src/lib/i18n/${locale}.json`, import.meta.url), 'utf8'))
let browser
let checks = 0

async function settle(page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    await Promise.all(
      [...document.querySelectorAll('mdui-button, mdui-button-icon, mdui-segmented-button')].map(
        (el) => el.updateComplete
      )
    )
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  })
}

async function checkButtons(page, root = page.locator('body')) {
  await settle(page)
  const result = await root.evaluateAll((roots) => {
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
          } else if (node instanceof Element && node.checkVisibility() && !node.matches('[slot="icon"]:empty')) {
            parts.push(node.getBoundingClientRect())
          }
        }
        // Visually hidden text is clipped to a 1px box and takes no part in the layout.
        const visible = parts.filter((part) => part.width > 1 || part.height > 1)
        const vertical = getComputedStyle(button).flexDirection === 'column'
        const target = vertical ? rect.x + rect.width / 2 : rect.y + rect.height / 2
        for (const part of visible) {
          const center = vertical ? part.x + part.width / 2 : part.y + part.height / 2
          if (Math.abs(center - target) > 1) {
            const name = button.getAttribute('aria-label') ?? button.textContent.trim().slice(0, 40)
            errors.push(`${button.tagName} ${name}: ${vertical ? 'x' : 'y'} offset ${(center - target).toFixed(2)}px`)
          }
        }
        count += 1
      }
    }
    return { errors, count }
  })
  assert.ok(result.count > 0, `No buttons exercised in ${root}`)
  assert.deepEqual(result.errors, [], `Button content alignment in ${root}`)
  checks += result.count
}

async function checkGroup(groups, axis, edge = 'center') {
  const positions = await groups.evaluateAll(
    (elements, { axis, edge }) =>
      elements
        .map((group) => {
          const children = [...group.children]
            .filter((el) => el.checkVisibility())
            .map((el) => el.getBoundingClientRect())
            .filter((r) => r.width && r.height)
          return children.map((r) =>
            axis === 'y' ? r.y + r.height / 2 : edge === 'start' ? r.x : edge === 'end' ? r.right : r.x + r.width / 2
          )
        })
        .filter((values) => values.length > 1),
    { axis, edge }
  )
  assert.ok(positions.length > 0, `No populated group exercised: ${groups}`)
  for (const values of positions)
    assert.ok(
      Math.max(...values) - Math.min(...values) <= 1,
      `${groups}: ${axis} ${edge} positions ${values.join(', ')}`
    )
  checks += positions.length
}

async function checkNewCenter(button) {
  const offsets = await button.evaluate((button) => {
    const rect = button.getBoundingClientRect()
    const parts = [...button.children].map((el) => el.getBoundingClientRect())
    const left = Math.min(...parts.map((r) => r.left))
    const right = Math.max(...parts.map((r) => r.right))
    return {
      x: (left + right) / 2 - rect.x - rect.width / 2,
      y: parts.map((r) => r.y + r.height / 2 - rect.y - rect.height / 2)
    }
  })
  assert.ok(Math.abs(offsets.x) <= 1, `New content horizontal offset: ${offsets.x}px`)
  assert.ok(
    offsets.y.every((offset) => Math.abs(offset) <= 1),
    `New content vertical offsets: ${offsets.y}`
  )
  checks += 1
}

// The fourth column of the list view holds the modification time.
async function checkDateColumn(list) {
  const geometry = await list.evaluate((grid) => {
    const header = grid.querySelectorAll('[role="columnheader"]')[3].getBoundingClientRect()
    const cells = [...grid.querySelectorAll('[aria-selected]')].map((row) => {
      const cell = row.querySelectorAll('[role="gridcell"]')[3]
      const rect = cell.getBoundingClientRect()
      return { x: rect.x, width: rect.width, text: cell.textContent, clipped: cell.scrollWidth > cell.clientWidth }
    })
    return { header: { x: header.x, width: header.width }, cells }
  })
  assert.ok(geometry.cells.length > 1, 'Date column needs multiple rows')
  for (const cell of geometry.cells) {
    assert.match(cell.text, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    assert.ok(
      Math.abs(cell.x - geometry.header.x) <= 1 && Math.abs(cell.width - geometry.header.width) <= 1,
      'Date cells must share header position and width'
    )
    assert.equal(cell.clipped, false, 'Date text must fit without clipping')
  }
  checks += geometry.cells.length
  return geometry.header.width
}

async function dragBetween(page, items) {
  assert.ok((await items.count()) >= 2, `${items}: drag selection needs two items`)
  const [first, second] = await Promise.all([items.nth(0).boundingBox(), items.nth(1).boundingBox()])
  assert.ok(first && second, `${items}: drag selection items must be visible`)
  await page.mouse.move(first.x + first.width / 2, first.y + first.height / 2)
  await page.mouse.down()
  await page.mouse.move(second.x + second.width / 2, second.y + second.height / 2, { steps: 5 })
  await page.mouse.up()
  await settle(page)
  const selected = await items.evaluateAll(
    (elements) => elements.filter((element) => element.getAttribute('aria-selected') === 'true').length
  )
  assert.ok(selected >= 2, `${items}: drag selection must persist after pointerup`)
  checks += 1
}

async function clearSelection(page, t) {
  const clear = page.getByRole('button', { name: t['browse.clear_selection'], exact: true })
  await clear.click()
  await clear.waitFor({ state: 'hidden' })
}

async function checkDragSelection(page, t) {
  const list = page.getByRole('grid', { name: t['table.file_list'], exact: true })
  const grid = page.getByRole('grid', { name: t['grid.file_grid'], exact: true })
  await dragBetween(page, list.locator('[aria-selected]'))
  await clearSelection(page, t)

  await page.getByRole('button', { name: t['browse.grid_view'], exact: true }).click()
  await grid.locator('[aria-selected]').first().waitFor()
  await dragBetween(page, grid.locator('[aria-selected]'))
  const backgrounds = await grid
    .locator('[aria-selected="true"]')
    .evaluateAll((cards) => cards.map((card) => getComputedStyle(card).backgroundColor))
  assert.ok(backgrounds.length >= 2, 'Grid drag selection must include multiple cards')
  assert.equal(new Set(backgrounds).size, 1, 'Hovered selected grid card must retain its selected background')
  checks += 1
  await clearSelection(page, t)
  await page.getByRole('button', { name: t['browse.list_view'], exact: true }).click()
  await list.locator('[aria-selected]').first().waitFor()
}

try {
  await server.listen()
  const base = server.resolvedUrls.local[0]
  browser = await chromium.launch()
  const widths = []
  for (const locale of ['en', 'ko']) {
    const t = catalogue(locale)
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
    await context.addInitScript((locale) => {
      localStorage.setItem('sc.locale', locale)
      localStorage.setItem('sc.theme', 'light')
    }, locale)
    const page = await context.newPage()
    const list = page.getByRole('grid', { name: t['table.file_list'], exact: true })
    const drawer = page.getByRole('navigation', { name: t['common.main_menu'], exact: true })
    const newButton = drawer.getByRole('button', { name: t['browse.new'], exact: true })
    const sidebarToggle = page.getByRole('banner').getByRole('button', { name: t['nav.toggle_sidebar'], exact: true })

    await page.goto(`${base}b/home`)
    await list.locator('[aria-selected]').first().waitFor()
    await checkButtons(page)
    await checkNewCenter(newButton)
    await checkGroup(page.getByRole('button', { name: t['common.refresh'], exact: true }).locator('..'), 'y')
    await checkGroup(drawer.getByRole('list'), 'x', 'start')
    widths.push(await checkDateColumn(list))
    await checkDragSelection(page, t)

    await newButton.focus()
    await page.keyboard.press('Enter')
    const newMenu = page.getByRole('menu')
    await newMenu.getByRole('menuitem').first().waitFor()
    await checkButtons(page, newMenu)
    await checkGroup(newMenu, 'x', 'start')
    await newMenu.getByRole('menuitem').first().click()
    const dialog = page.getByRole('alertdialog')
    await dialog.waitFor()
    await checkButtons(page, dialog)
    await checkGroup(dialog.locator('[slot="action"]'), 'y')
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'hidden' })

    await sidebarToggle.click()
    await page
      .getByRole('banner')
      .getByRole('button', { name: t['nav.toggle_sidebar'], exact: true, expanded: false })
      .waitFor()
    await checkNewCenter(newButton)
    await checkGroup(drawer.locator(':scope > div'), 'x')
    await checkButtons(page)
    await sidebarToggle.click()

    await page.goto(`${base}settings`)
    const settingsTabs = page.getByRole('navigation', { name: t['common.settings'], exact: true })
    await settingsTabs.getByRole('button').first().waitFor()
    await checkButtons(page)
    await checkGroup(settingsTabs, 'y')
    for (const tab of await settingsTabs.getByRole('button').all()) {
      await tab.click()
      await checkButtons(page)
    }

    await page.goto(`${base}b/home`)
    await list.locator('[aria-selected]').first().waitFor()
    await page.getByRole('banner').getByRole('button', { name: t['common.search'], exact: true }).click()
    const search = page.getByRole('dialog', { name: t['search.title'], exact: true })
    const categories = search.getByRole('group', { name: t['search.kind_label'], exact: true })
    await categories.getByRole('button').first().waitFor()
    await checkButtons(page, search)
    await checkGroup(categories, 'y')
    await page.keyboard.press('Escape')

    for (const width of [800, 390]) {
      await page.setViewportSize({ width, height: 900 })
      const navBar = page.getByRole('navigation', { name: t['common.main_menu'], exact: true })
      const more = navBar.getByRole('button', { name: t['nav.more'], exact: true })
      await more.waitFor()
      await checkButtons(page)
      await checkGroup(navBar, 'y')
      await more.click()
      const overlay = page.getByRole('dialog', { name: t['common.main_menu'], exact: true })
      await overlay.waitFor()
      await checkNewCenter(overlay.getByRole('button', { name: t['browse.new'], exact: true }))
      await checkButtons(page, overlay)
      await checkGroup(overlay.getByRole('list'), 'x', 'start')
      await page.keyboard.press('Escape')
      await overlay.waitFor({ state: 'hidden' })
    }
    assert.equal(
      await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
      false,
      'Compact layout must not overflow horizontally'
    )
    await context.close()
  }
  assert.equal(widths[0], widths[1], 'Date column width must not depend on language')
  console.log(`PASS: ${checks} button, group and date geometry checks in English and Korean`)
} finally {
  await browser?.close()
  await server.close()
}
