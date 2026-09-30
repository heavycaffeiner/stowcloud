import { fileURLToPath } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'
import { chromium } from 'playwright'
import { createServer } from 'vite'

const WEB_ROOT = fileURLToPath(new URL('..', import.meta.url))
const OUT_DIR = fileURLToPath(new URL('../../docs/screenshots', import.meta.url))
fs.mkdirSync(OUT_DIR, { recursive: true })

const server = await createServer({
  root: WEB_ROOT,
  mode: 'development',
  define: { 'import.meta.env.VITE_API_MOCK': JSON.stringify('1') },
  server: { host: '127.0.0.1', port: 0, open: false, proxy: {} },
  logLevel: 'error'
})
await server.listen()
const base = server.resolvedUrls.local[0]
console.log('Vite server running at', base)

const browser = await chromium.launch()

async function setupContext(theme) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: theme,
    reducedMotion: 'reduce'
  })
  await context.addInitScript((t) => {
    localStorage.setItem('sc.locale', 'en')
    localStorage.setItem('sc.theme', t)
  }, theme)
  return context
}

// List rows are the only elements in the file list that carry aria-selected.
const fileRows = (page) => page.getByRole('grid', { name: 'File list', exact: true }).locator('[aria-selected]')

async function settle(page, ms = 400) {
  await page.waitForTimeout(ms)
  await page.evaluate(async () => {
    if (document.fonts?.ready) await document.fonts.ready
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  })
  await page.waitForTimeout(200)
}

const DEMO_CODE = `type UploadState = 'queued' | 'encrypted' | 'done'

const pendingCount: number = 2
const encrypted: boolean = true
const filename: string = 'family-photo.png'

export default pendingCount
`

for (const theme of ['light', 'dark']) {
  console.log(`\n=== Capturing screenshots for theme: ${theme} ===`)
  const context = await setupContext(theme)
  const page = await context.newPage()

  // 1. Setup screen (fullPage, 1440x1385)
  console.log(`[${theme}] Capturing setup...`)
  await page.setViewportSize({ width: 1440, height: 1385 })
  await page.goto(`${base}setup`)
  const setupToken = page.getByRole('textbox', { name: 'Setup token', exact: true })
  await setupToken.waitFor()
  await setupToken.focus()
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `setup-${theme}.png`) })

  // Return to 1440x900 viewport for remaining screens
  await page.setViewportSize({ width: 1440, height: 900 })

  // 2. Browse screen (home folder)
  console.log(`[${theme}] Capturing browse...`)
  await page.goto(`${base}b/home`)
  await fileRows(page).first().waitFor()
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `browse-${theme}.png`) })

  // 3. Subfolder navigation view (Documents folder)
  console.log(`[${theme}] Capturing tree / subfolder...`)
  await page.goto(`${base}b/home/Documents`)
  await fileRows(page).first().waitFor()
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `tree-${theme}.png`) })

  // 4. Search sheet
  console.log(`[${theme}] Capturing search...`)
  await page.goto(`${base}b/home/Documents`)
  await fileRows(page).first().waitFor()
  await page.getByRole('banner').getByRole('button', { name: 'Search', exact: true }).click()
  const searchInput = page.getByRole('dialog', { name: 'Search', exact: true }).getByRole('searchbox')
  await searchInput.waitFor()
  await searchInput.fill('2026')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(800)
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `search-${theme}.png`) })
  await page.keyboard.press('Escape')

  // 5. Share link dialog
  console.log(`[${theme}] Capturing share-link...`)
  await page.goto(`${base}b/home/Documents`)
  await fileRows(page).first().waitFor()
  const meetingRow = fileRows(page).filter({ has: page.getByTitle('meeting-notes.txt', { exact: true }) })
  await meetingRow.waitFor()
  await meetingRow.getByRole('gridcell', { name: 'Select meeting-notes.txt', exact: true }).click()
  await page.waitForTimeout(300)
  await page.getByRole('button', { name: 'Manage share links', exact: true }).click()
  const shareDialog = page.getByRole('dialog', { name: 'Share links: meeting-notes.txt', exact: true })
  await shareDialog.waitFor()
  const createBtn = shareDialog.getByRole('button', { name: 'Create a new link', exact: true })
  if (await createBtn.isVisible()) {
    await createBtn.click()
    await page.waitForTimeout(400)
    await shareDialog.getByRole('button', { name: 'Create', exact: true }).click()
    await shareDialog.getByRole('textbox', { name: 'Copy link', exact: true }).waitFor()
  }
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `share-link-${theme}.png`) })
  await page.keyboard.press('Escape')

  // 6. Public share page (via client-side navigation)
  console.log(`[${theme}] Capturing share-public...`)
  await page.evaluate(() => {
    window.history.pushState({}, '', '/s/photos')
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
  await page.getByRole('main').getByText('public share link').waitFor()
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `share-public-${theme}.png`) })

  // 7. Folder grants dialog
  console.log(`[${theme}] Capturing folder-grants...`)
  await page.goto(`${base}admin#users`)
  await page.getByRole('button', { name: 'Manage folders visible to sujin', exact: true }).click()
  await page.getByRole('alertdialog').waitFor()
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `folder-grants-${theme}.png`) })
  await page.keyboard.press('Escape')

  // 8. Editor page
  console.log(`[${theme}] Capturing editor...`)
  await page.goto(`${base}b/home`)
  await fileRows(page).first().waitFor()
  await page.evaluate(async (demoCode) => {
    const { api } = await import('/src/lib/api/client.ts')
    await api.writeFile('/home/stowcloud-editor-demo.ts', demoCode)
  }, DEMO_CODE)
  await page.evaluate(() => {
    window.history.pushState({}, '', '/edit/home/stowcloud-editor-demo.ts')
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
  const editor = page.getByRole('textbox', { name: 'stowcloud-editor-demo.ts', exact: true })
  await editor.waitFor()
  await editor.focus()
  await page.keyboard.press('End')
  await page.keyboard.type(' ')
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `editor-${theme}.png`) })

  // 9. Trash page
  console.log(`[${theme}] Capturing trash...`)
  await page.evaluate(async () => {
    const { api } = await import('/src/lib/api/client.ts')
    await api.delete(['/home/Photos/여행사진.png', '/home/Photos/휴가-2026-07-02.jpg'])
  })
  await page.evaluate(() => {
    window.history.pushState({}, '', '/trash')
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
  await page.getByRole('button', { name: 'Restore', exact: true }).first().waitFor()
  await settle(page)
  await page.screenshot({ path: path.join(OUT_DIR, `trash-${theme}.png`) })

  await context.close()
}

await browser.close()
await server.close()
console.log('\nAll screenshots captured successfully!')
