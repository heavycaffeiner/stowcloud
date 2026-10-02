import * as fs from 'node:fs'
import * as path from 'node:path'
import * as zlib from 'node:zlib'
import { fileURLToPath } from 'node:url'
import type { Browser, Page } from '@playwright/test'
import { test, expect } from '../fixtures'

// Regenerates the README screenshots in docs/screenshots from a seeded real server. Run on demand,
// with a binary built after `pnpm build`: SC_README_SCREENSHOTS=1 SC_TEST_BIN=<binary> pnpm exec playwright test --project=chromium e2e/readme
const OUT_DIR = fileURLToPath(new URL('../../../docs/screenshots', import.meta.url))
const THEMES = ['light', 'dark'] as const
const DAY_MS = 86_400_000

const DEMO_CODE = `type UploadState = 'queued' | 'encrypted' | 'done'

const pendingCount: number = 2
const encrypted: boolean = true
const filename: string = 'family-photo.png'

export default pendingCount
`

// A flat-colour PNG, so the photo rows hold image bytes the server can thumbnail.
function png(width: number, height: number, rgb: readonly [number, number, number]): Buffer {
  const chunk = (type: string, data: Buffer) => {
    const body = Buffer.concat([Buffer.from(type, 'latin1'), data])
    const out = Buffer.alloc(body.length + 8)
    out.writeUInt32BE(data.length, 0)
    body.copy(out, 4)
    out.writeUInt32BE(zlib.crc32(body), body.length + 4)
    return out
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width, 0)
  header.writeUInt32BE(height, 4)
  header.set([8, 2, 0, 0, 0], 8)
  const row = Buffer.concat([Buffer.from([0]), Buffer.from(Array.from({ length: width }, () => rgb).flat())])
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', header),
    chunk('IDAT', zlib.deflateSync(Buffer.concat(Array.from({ length: height }, () => row)))),
    chunk('IEND', Buffer.alloc(0))
  ])
}

// Parents come before their children; a path ending in a slash is a folder.
const TREE: [string, number, (string | Buffer)?][] = [
  ['Documents/', 1],
  ['Documents/2026-예산안.xlsx', 2, Buffer.alloc(45_211)],
  ['Documents/제안서.docx', 6, Buffer.alloc(128_933)],
  ['Documents/meeting-notes.txt', 1, 'Weekly sync\n\n- Upload queue resumes after a dropped connection\n'],
  ['Documents/계약서/', 30],
  ['Documents/계약서/nda.pdf', 30, Buffer.alloc(88_213)],
  ['Documents/계약서/service-agreement.pdf', 45, Buffer.alloc(210_442)],
  ['Photos/', 3],
  ['Photos/휴가-2026-07-01.png', 26, png(640, 480, [74, 144, 196])],
  ['Photos/휴가-2026-07-02.png', 26, png(640, 480, [232, 170, 92])],
  ['Photos/여행사진.png', 100, png(640, 480, [96, 168, 120])],
  ['Videos/', 10],
  ['Videos/발표녹화.mp4', 15, Buffer.alloc(5_242_880)],
  ['Music/', 20],
  ['Music/playlist.m3u', 200, '#EXTM3U\n'],
  ['README.txt', 5, 'Files shared from the home server.\n']
]

function seed(root: string): void {
  for (const [relative, , content] of TREE) {
    const target = path.join(root, relative)
    if (relative.endsWith('/')) fs.mkdirSync(target, { recursive: true })
    else fs.writeFileSync(target, content ?? '')
  }
  // Children first, since writing into a folder moves its own modification time.
  for (const [relative, daysAgo] of TREE.toReversed()) {
    const when = new Date(Date.now() - daysAgo * DAY_MS)
    fs.utimesSync(path.join(root, relative), when, when)
  }
}

async function settle(page: Page): Promise<void> {
  await page.waitForTimeout(400)
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  })
  await page.waitForTimeout(200)
}

async function themedPage(browser: Browser, theme: (typeof THEMES)[number]): Promise<Page> {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: theme,
    reducedMotion: 'reduce',
    ignoreHTTPSErrors: true
  })
  await context.addInitScript((theme) => {
    localStorage.setItem('sc.locale', 'en')
    localStorage.setItem('sc.theme', theme)
  }, theme)
  return context.newPage()
}

test.describe('README screenshots', () => {
  test.skip(!process.env.SC_README_SCREENSHOTS, 'set SC_README_SCREENSHOTS=1 to regenerate docs/screenshots')

  test('captures every README screen in both themes', async ({
    browser,
    api,
    accounts,
    filesystem,
    grants,
    workerApp
  }) => {
    test.setTimeout(180_000)
    const root = path.join(workerApp.shareDir, 'home')
    fs.mkdirSync(path.join(root, 'Photos'), { recursive: true })
    fs.mkdirSync(path.join(root, 'Documents'), { recursive: true })
    const home = await filesystem.setTrash((await filesystem.createShare('home', root)).id, true)
    // Creating a share may already grant it to the admin who made it.
    if (!(await grants.listGrants()).some((grant) => grant.share === String(home.id))) {
      const allow = ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share']
      await grants.createGrant({ user: workerApp.adminUser.id, share: String(home.id), label: 'home', allow })
    }
    const sujin = await accounts.createUser('sujin')
    await grants.createGrant({ user: sujin.id, share: String(home.id), label: 'home', allow: ['read', 'download'] })

    // Trashed before the seed dates its folders, so both themes show the same bin and the same dates.
    for (const name of ['Photos/휴가-2026-06-30.png', 'Documents/2025-예산안.xlsx']) {
      fs.writeFileSync(
        path.join(root, name),
        name.endsWith('.png') ? png(640, 480, [180, 120, 200]) : Buffer.alloc(41_032)
      )
      const res = await api.post('/api/v1/files/delete', { path: `/home/${name}` })
      expect(res.status, res.rawText).toBeLessThan(300)
    }
    const link = await api.post<{ link?: { token: string }; token?: string }>('/api/v1/links', {
      path: '/home/Photos',
      perms: ['read', 'download']
    })
    expect(link.status, link.rawText).toBeLessThan(300)
    const token = link.data.token ?? link.data.link?.token
    seed(root)

    fs.mkdirSync(OUT_DIR, { recursive: true })
    const shot = (page: Page, name: string, theme: string) =>
      page.screenshot({ path: path.join(OUT_DIR, `${name}-${theme}.png`) })

    for (const theme of THEMES) {
      const page = await themedPage(browser, theme)
      const fileRows = page.getByRole('grid', { name: 'File list', exact: true }).locator('[aria-selected]')

      await page.setViewportSize({ width: 1440, height: 1385 })
      await page.goto(`${workerApp.baseURL}/setup`)
      const setupToken = page.getByRole('textbox', { name: 'Setup token', exact: true })
      await setupToken.waitFor()
      await setupToken.focus()
      await settle(page)
      await shot(page, 'setup', theme)

      await page.context().addCookies(api.getPlaywrightCookies(workerApp.baseURL))
      await page.setViewportSize({ width: 1440, height: 900 })

      await page.goto(`${workerApp.baseURL}/b/home`)
      await fileRows.first().waitFor()
      await settle(page)
      await shot(page, 'browse', theme)

      await page.goto(`${workerApp.baseURL}/b/home/Documents`)
      await fileRows.first().waitFor()
      await settle(page)
      await shot(page, 'tree', theme)

      await page.getByRole('banner').getByRole('button', { name: 'Search', exact: true }).click()
      const searchInput = page.getByRole('dialog', { name: 'Search', exact: true }).getByRole('searchbox')
      await searchInput.fill('2026')
      await searchInput.press('Enter')
      await page.getByRole('dialog', { name: 'Search', exact: true }).getByRole('listitem').first().waitFor()
      await settle(page)
      await shot(page, 'search', theme)
      await page.keyboard.press('Escape')

      await page.goto(`${workerApp.baseURL}/b/home/Documents`)
      const meetingRow = fileRows.filter({ has: page.getByTitle('meeting-notes.txt', { exact: true }) })
      await meetingRow.getByRole('gridcell', { name: 'Select meeting-notes.txt', exact: true }).click()
      await page.getByRole('button', { name: 'Manage share links', exact: true }).click()
      const shareDialog = page.getByRole('dialog', { name: 'Share links: meeting-notes.txt', exact: true })
      await shareDialog.getByRole('button', { name: 'Create a new link', exact: true }).click()
      await shareDialog.getByRole('button', { name: 'Create', exact: true }).click()
      await shareDialog.getByRole('textbox', { name: 'Copy link', exact: true }).waitFor()
      await settle(page)
      await shot(page, 'share-link', theme)
      await page.keyboard.press('Escape')

      await page.goto(`${workerApp.baseURL}/s/${token}`)
      await page.getByRole('main').getByText('public share link').waitFor()
      await settle(page)
      await shot(page, 'share-public', theme)

      await page.goto(`${workerApp.baseURL}/admin/users`)
      await page.getByRole('button', { name: 'Manage folders visible to sujin', exact: true }).click()
      await page.getByRole('alertdialog').waitFor()
      await settle(page)
      await shot(page, 'folder-grants', theme)
      await page.keyboard.press('Escape')

      await page.goto(`${workerApp.baseURL}/trash`)
      await page.getByRole('button', { name: 'Restore', exact: true }).first().waitFor()
      await settle(page)
      await shot(page, 'trash', theme)

      // Last, because the edit below leaves unsaved changes that would hold up a navigation.
      const demo = path.join(root, 'stowcloud-editor-demo.ts')
      fs.writeFileSync(demo, DEMO_CODE)
      await page.goto(`${workerApp.baseURL}/edit/home/stowcloud-editor-demo.ts`)
      const editor = page.getByRole('textbox', { name: 'stowcloud-editor-demo.ts', exact: true })
      await editor.waitFor()
      await editor.focus()
      await page.keyboard.press('End')
      await page.keyboard.type(' ')
      await settle(page)
      await shot(page, 'editor', theme)

      await page.context().close()
      fs.rmSync(demo)
    }
  })
})
