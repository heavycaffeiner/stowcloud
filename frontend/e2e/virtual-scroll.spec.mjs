// Run: pnpm test:virtual-scroll. Uses the development fixtures and real browser layout.
//
// The file views render a window of rows and move it as the viewport scrolls.
// Reading the scroll offset off the wrong element makes that window freeze at
// its first slice: the list renders one screenful, and everything past it stays
// blank however far the wheel turns. That is invisible to a unit test, because
// the maths is correct and it is the input that is wrong, so this drives a real
// wheel over a real layout at several viewport sizes.
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { createServer } from 'vite'

const VIEWPORTS = [
  [1920, 1080],
  [1440, 900],
  [1280, 720],
  [800, 600],
  [390, 844]
]
const WHEEL_STEPS = 60
const WHEEL_DELTA = 800

const server = await createServer({
  root: fileURLToPath(new URL('..', import.meta.url)),
  mode: 'development',
  define: { 'import.meta.env.VITE_API_MOCK': JSON.stringify('1') },
  server: { host: '127.0.0.1', port: 0, open: false },
  logLevel: 'error'
})
let browser
let checks = 0

async function wheelTo(page, width, height) {
  await page.mouse.move(Math.round(width / 2), Math.round(height / 2))
  for (let i = 0; i < WHEEL_STEPS; i++) {
    await page.mouse.wheel(0, WHEEL_DELTA)
    await page.waitForTimeout(16)
  }
  await page.waitForTimeout(400)
}

try {
  await server.listen()
  const base = server.resolvedUrls.local[0]
  browser = await chromium.launch()

  for (const [width, height] of VIEWPORTS) {
    for (const mode of ['list', 'grid']) {
      const context = await browser.newContext({ viewport: { width, height }, reducedMotion: 'reduce' })
      await context.addInitScript((view) => {
        localStorage.setItem('sc.locale', 'en')
        localStorage.setItem('sc.view', view)
      }, mode)
      const page = await context.newPage()

      // The seeded bench directory holds 100,000 rows, so the window has far
      // more to move through than any one viewport can render.
      await page.goto(`${base}b/home/bench`)
      // The view itself must own the scroll. Rows and cards are the only elements in it with aria-selected.
      const view = page.getByRole('grid', { name: mode === 'grid' ? 'File grid' : 'File list' })
      const cell = view.locator('[aria-selected]')
      await cell.first().waitFor()
      const before = await cell.first().textContent()

      await wheelTo(page, width, height)

      const offset = await view.evaluate((element) => element.scrollTop)
      const after = await cell.first().textContent()
      const rendered = await cell.count()
      const where = `${width}x${height} ${mode}`

      assert.ok(offset > 0, `${where}: the wheel scrolled nothing, so the view is not the scroll owner`)
      assert.notEqual(after, before, `${where}: the rendered window froze at its first slice`)
      assert.ok(rendered > 0, `${where}: nothing is rendered after scrolling`)
      checks += 1
      await context.close()
    }
  }

  console.log(`PASS: ${checks} deep-scroll checks across ${VIEWPORTS.length} viewports in both views`)
} finally {
  await browser?.close()
  await server.close()
}
