import * as fs from 'node:fs'
import * as path from 'node:path'
import type { Page } from '@playwright/test'
import { test, expect } from '../fixtures'
import { fileEntries } from '../helpers/browse'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

// The file views render a window of rows and move it as the view scrolls.
// Reading the scroll offset off the wrong element freezes that window at its
// first screenful, which no unit test sees: the maths is right and the input is
// wrong. These drive a real wheel over a real layout at several viewport sizes.
const VIEWPORTS = [
  [1920, 1080],
  [1440, 900],
  [1280, 720],
  [800, 600],
  [390, 844]
] as const
// Far more rows than the deepest wheel below reaches, in either view.
const ROWS = 5000
const FOLDER = 'bench'

async function wheelDown(page: Page, width: number, height: number): Promise<void> {
  await page.mouse.move(Math.round(width / 2), Math.round(height / 2))
  for (let step = 0; step < 60; step++) {
    await page.mouse.wheel(0, 800)
    await page.waitForTimeout(16)
  }
}

test.describe('virtual scrolling of the file views', () => {
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
    const bench = path.join(workerApp.shareDir, FOLDER)
    if (!fs.existsSync(bench)) {
      fs.mkdirSync(`${bench}.tmp`)
      for (let index = 0; index < ROWS; index++) {
        fs.writeFileSync(path.join(`${bench}.tmp`, `f${String(index).padStart(4, '0')}.txt`), '')
      }
      // Renamed into place whole, so a listing never sees a half-written folder.
      fs.renameSync(`${bench}.tmp`, bench)
    }
  })

  for (const [width, height] of VIEWPORTS) {
    for (const mode of ['list', 'grid'] as const) {
      test(`a deep wheel moves the rendered window at ${width}x${height} in ${mode} view`, async ({
        authedPage: page,
        workerApp,
        artifacts
      }) => {
        await page.setViewportSize({ width, height })
        await page.addInitScript((view) => localStorage.setItem('sc.view', view), mode)
        await page.goto(`${workerApp.baseURL}/b/docs/${FOLDER}`, { waitUntil: 'domcontentloaded' })

        const view = page.getByRole('grid', { name: mode === 'grid' ? 'File grid' : 'File list' })
        const first = fileEntries(page).first()
        await expect(first).toBeVisible()
        const before = await first.textContent()

        await wheelDown(page, width, height)

        // The view itself must own the scroll, or the wheel moved nothing it reads.
        expect(await view.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)
        await expect(first).not.toHaveText(before ?? '')
        expect(await fileEntries(page).count()).toBeGreaterThan(0)

        await assertNoUnexpectedErrors(artifacts)
      })
    }
  }

  test('Home in the list returns the view to its top', async ({ authedPage: page, workerApp, artifacts }) => {
    await page.setViewportSize({ width: 1280, height: 720 })
    await page.addInitScript(() => localStorage.setItem('sc.view', 'list'))
    await page.goto(`${workerApp.baseURL}/b/docs/${FOLDER}`, { waitUntil: 'domcontentloaded' })

    const list = page.getByRole('grid', { name: 'File list' })
    await fileEntries(page).first().click()
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('ArrowDown')
    await wheelDown(page, 1280, 720)
    expect(await list.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)

    await page.keyboard.press('Home')
    await expect.poll(() => list.evaluate((element) => element.scrollTop)).toBe(0)
    await expect(fileEntries(page).first()).toContainText('f0000.txt')

    await assertNoUnexpectedErrors(artifacts)
  })
})
