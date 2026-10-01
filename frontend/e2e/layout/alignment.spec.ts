import * as fs from 'node:fs'
import * as path from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { test, expect } from '../fixtures'

// Measures rendered geometry: the content of every visible button sits on the
// button's centre line, sibling controls share an edge or a centre, and the
// date column keeps one width whichever language draws it.
type Catalogue = Record<string, string>

const FOLDER = 'align'

function catalogue(locale: string): Catalogue {
  return JSON.parse(fs.readFileSync(new URL(`../../src/lib/i18n/${locale}.json`, import.meta.url), 'utf8'))
}

async function settle(page: Page): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready
    await Promise.all(
      [...document.querySelectorAll('mdui-button, mdui-button-icon, mdui-segmented-button')].map(
        (element) => (element as Element & { updateComplete?: Promise<unknown> }).updateComplete
      )
    )
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  })
}

async function checkButtons(page: Page, root: Locator = page.locator('body')): Promise<void> {
  await settle(page)
  const result = await root.evaluateAll((roots) => {
    const errors: string[] = []
    let count = 0
    for (const root of roots) {
      for (const button of root.querySelectorAll('button, mdui-button, mdui-button-icon, mdui-segmented-button')) {
        const rect = button.getBoundingClientRect()
        if (!rect.width || !rect.height || !button.checkVisibility()) continue
        const parts: DOMRect[] = []
        for (const node of button.childNodes) {
          if (node.nodeType === Node.TEXT_NODE && node.textContent?.trim()) {
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
            const name = button.getAttribute('aria-label') ?? (button.textContent ?? '').trim().slice(0, 40)
            errors.push(`${button.tagName} ${name}: ${vertical ? 'x' : 'y'} offset ${(center - target).toFixed(2)}px`)
          }
        }
        count += 1
      }
    }
    return { errors, count }
  })
  expect(result.count, `no buttons exercised in ${root}`).toBeGreaterThan(0)
  expect(result.errors, `button content alignment in ${root}`).toEqual([])
}

async function checkGroup(groups: Locator, axis: 'x' | 'y', edge: 'start' | 'center' | 'end' = 'center') {
  const positions = await groups.evaluateAll(
    (elements, { axis, edge }) =>
      elements
        .map((group) =>
          [...group.children]
            .filter((element) => element.checkVisibility())
            .map((element) => element.getBoundingClientRect())
            .filter((rect) => rect.width && rect.height)
            .map((rect) =>
              axis === 'y'
                ? rect.y + rect.height / 2
                : edge === 'start'
                  ? rect.x
                  : edge === 'end'
                    ? rect.right
                    : rect.x + rect.width / 2
            )
        )
        .filter((values) => values.length > 1),
    { axis, edge }
  )
  expect(positions.length, `no populated group exercised: ${groups}`).toBeGreaterThan(0)
  for (const values of positions) {
    expect(
      Math.max(...values) - Math.min(...values),
      `${groups}: ${axis} ${edge} positions ${values.join(', ')}`
    ).toBeLessThanOrEqual(1)
  }
}

async function checkNewCenter(button: Locator): Promise<void> {
  const offsets = await button.evaluate((button) => {
    const rect = button.getBoundingClientRect()
    const parts = [...button.children].map((element) => element.getBoundingClientRect())
    const left = Math.min(...parts.map((part) => part.left))
    const right = Math.max(...parts.map((part) => part.right))
    return {
      x: (left + right) / 2 - rect.x - rect.width / 2,
      y: parts.map((part) => part.y + part.height / 2 - rect.y - rect.height / 2)
    }
  })
  expect(Math.abs(offsets.x), 'New content horizontal offset').toBeLessThanOrEqual(1)
  for (const offset of offsets.y) expect(Math.abs(offset), 'New content vertical offset').toBeLessThanOrEqual(1)
}

// The fourth column of the list view holds the modification time.
async function checkDateColumn(list: Locator): Promise<number> {
  const geometry = await list.evaluate((grid) => {
    const header = grid.querySelectorAll('[role="columnheader"]')[3].getBoundingClientRect()
    const cells = [...grid.querySelectorAll('[aria-selected]')].map((row) => {
      const cell = row.querySelectorAll('[role="gridcell"]')[3]
      const rect = cell.getBoundingClientRect()
      return { x: rect.x, width: rect.width, text: cell.textContent, clipped: cell.scrollWidth > cell.clientWidth }
    })
    return { header: { x: header.x, width: header.width }, cells }
  })
  expect(geometry.cells.length, 'the date column needs several rows').toBeGreaterThan(1)
  for (const cell of geometry.cells) {
    expect(cell.text).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(Math.abs(cell.x - geometry.header.x), 'date cells share the header position').toBeLessThanOrEqual(1)
    expect(Math.abs(cell.width - geometry.header.width), 'date cells share the header width').toBeLessThanOrEqual(1)
    expect(cell.clipped, 'date text fits without clipping').toBe(false)
  }
  return geometry.header.width
}

async function dragBetween(page: Page, items: Locator): Promise<void> {
  expect(await items.count(), `${items}: drag selection needs two items`).toBeGreaterThanOrEqual(2)
  const [first, second] = await Promise.all([items.nth(0).boundingBox(), items.nth(1).boundingBox()])
  if (!first || !second) throw new Error(`${items}: drag selection items must be visible`)
  await page.mouse.move(first.x + first.width / 2, first.y + first.height / 2)
  await page.mouse.down()
  await page.mouse.move(second.x + second.width / 2, second.y + second.height / 2, { steps: 5 })
  await page.mouse.up()
  await settle(page)
  const selected = await items.evaluateAll(
    (elements) => elements.filter((element) => element.getAttribute('aria-selected') === 'true').length
  )
  expect(selected, `${items}: drag selection must persist after pointerup`).toBeGreaterThanOrEqual(2)
}

async function clearSelection(page: Page, t: Catalogue): Promise<void> {
  const clear = page.getByRole('button', { name: t['browse.clear_selection'], exact: true })
  await clear.click()
  await clear.waitFor({ state: 'hidden' })
}

async function checkDragSelection(page: Page, t: Catalogue): Promise<void> {
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
  expect(backgrounds.length, 'grid drag selection must include several cards').toBeGreaterThanOrEqual(2)
  expect(new Set(backgrounds).size, 'a hovered selected card keeps its selected background').toBe(1)
  await clearSelection(page, t)
  await page.getByRole('button', { name: t['browse.list_view'], exact: true }).click()
  await list.locator('[aria-selected]').first().waitFor()
}

test.describe('alignment of buttons, groups and the date column', () => {
  test.beforeEach(async ({ filesystem, workerApp, grants }) => {
    const shares = await filesystem.listShares()
    const docs =
      shares.find((share) => share.name === 'docs') ?? (await filesystem.createShare('docs', workerApp.shareDir))
    const existing = await grants.listGrants()
    if (!existing.some((grant) => grant.share === String(docs.id))) {
      await grants.createGrant({
        user: workerApp.adminUser.id,
        share: String(docs.id),
        label: 'docs',
        allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share']
      })
    }
    const folder = path.join(workerApp.shareDir, FOLDER)
    fs.mkdirSync(path.join(folder, 'notes'), { recursive: true })
    fs.writeFileSync(path.join(folder, 'report.txt'), 'report\n')
    fs.writeFileSync(path.join(folder, 'summary.md'), '# Summary\n')
  })

  test('controls line up in English and Korean at every width', async ({ browser, browserName, api, workerApp }) => {
    // Firefox boxes Hangul text with the fallback font's line metrics, 2px off a centre that renders true.
    test.skip(browserName !== 'chromium', 'text box geometry is measured against Chromium layout')
    test.slow()
    const widths: number[] = []
    for (const locale of ['en', 'ko']) {
      const t = catalogue(locale)
      const context = await browser.newContext({
        viewport: { width: 1440, height: 1000 },
        reducedMotion: 'reduce',
        ignoreHTTPSErrors: true
      })
      await context.addCookies(api.getPlaywrightCookies(workerApp.baseURL))
      await context.addInitScript((locale) => {
        localStorage.setItem('sc.locale', locale)
        localStorage.setItem('sc.theme', 'light')
      }, locale)
      const page = await context.newPage()
      const list = page.getByRole('grid', { name: t['table.file_list'], exact: true })
      const drawer = page.getByRole('navigation', { name: t['common.main_menu'], exact: true })
      const newButton = drawer.getByRole('button', { name: t['browse.new'], exact: true })
      const sidebarToggle = page.getByRole('banner').getByRole('button', { name: t['nav.toggle_sidebar'], exact: true })
      const browse = `${workerApp.baseURL}/b/docs/${FOLDER}`

      await page.goto(browse)
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

      await page.goto(`${workerApp.baseURL}/settings`)
      const settingsTabs = page.getByRole('navigation', { name: t['common.settings'], exact: true })
      await settingsTabs.getByRole('button').first().waitFor()
      await checkButtons(page)
      await checkGroup(settingsTabs, 'y')
      for (const tab of await settingsTabs.getByRole('button').all()) {
        await tab.click()
        await checkButtons(page)
      }

      await page.goto(browse)
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
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        'the compact layout must not overflow horizontally'
      ).toBe(false)
      await context.close()
    }
    expect(widths[0], 'the date column width must not depend on the language').toBe(widths[1])
  })
})
