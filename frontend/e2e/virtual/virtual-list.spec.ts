import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect, pinEnglish } from '../fixtures'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

// Every list outside the file views goes through one windowed list component.
// A public folder link is the shortest way to put a long one on screen.
const ROWS = 300
const FOLDER = 'many-rows'

test.describe('the windowed list', () => {
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
    if (!fs.existsSync(folder)) {
      fs.mkdirSync(`${folder}.tmp`)
      for (let index = 0; index < ROWS; index++) {
        fs.writeFileSync(path.join(`${folder}.tmp`, `f${String(index).padStart(3, '0')}.txt`), '')
      }
      fs.renameSync(`${folder}.tmp`, folder)
    }
  })

  test('mounts a window that follows the wheel and the keyboard', async ({ browser, workerApp, api, artifacts }) => {
    const created = await api.post<{ link?: { token: string }; token?: string }>('/api/v1/links', {
      path: `/docs/${FOLDER}`,
      perms: ['read', 'download']
    })
    const token = created.data.token || created.data.link?.token
    expect(token).toBeDefined()

    const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 720 } })
    await pinEnglish(context)
    const page = await context.newPage()
    await page.goto(`${workerApp.baseURL}/s/${token}`, { waitUntil: 'domcontentloaded' })

    const list = page.getByRole('list').filter({ has: page.getByText('f000.txt', { exact: true }) })
    const row = (name: string) => list.getByRole('listitem').filter({ hasText: name })
    await expect(row('f000.txt')).toBeInViewport()
    await expect(row('f000.txt')).toHaveAttribute('aria-setsize', String(ROWS))
    await expect(row('f150.txt')).toHaveCount(0)
    expect(await list.getByRole('listitem').count()).toBeLessThan(60)

    await page.mouse.move(640, 400)
    for (let step = 0; step < 10; step++) await page.mouse.wheel(0, 800)
    await expect(row('f000.txt')).not.toBeInViewport()
    await expect.poll(() => list.getByRole('listitem').count()).toBeLessThan(60)

    await row('f000.txt').getByText('f000.txt', { exact: true }).click()
    await page.keyboard.press('End')
    await expect(row('f299.txt').getByRole('button', { name: 'Download' })).toBeFocused()
    await expect(row('f299.txt')).toBeInViewport()
    await expect(row('f150.txt')).toHaveCount(0)

    await row('f299.txt').getByText('f299.txt', { exact: true }).click()
    await page.keyboard.press('Home')
    await expect(row('f000.txt').getByRole('button', { name: 'Download' })).toBeFocused()
    await expect(row('f000.txt')).toBeInViewport()

    await page.keyboard.press('Tab')
    await expect(row('f001.txt').getByRole('button', { name: 'Download' })).toBeFocused()

    await context.close()
    await assertNoUnexpectedErrors(artifacts)
  })
})
