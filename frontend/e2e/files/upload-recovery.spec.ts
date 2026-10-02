import type { Route } from '@playwright/test'
import { test, expect } from '../fixtures'
import * as path from 'node:path'
import * as fs from 'node:fs'
import { createTempFixtureFile } from '../helpers/files'
import { fileEntry, uploadItem } from '../helpers/browse'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

const UPLOADS = '**/api/v1/uploads/**'

// Holds every chunk until release(), so the upload is still running when the tray is used.
function holdChunks() {
  const { promise: released, resolve: release } = Promise.withResolvers<void>()
  const route = async (route: Route) => {
    if (route.request().method() === 'PATCH') await released
    // A chunk the page aborted while it was held can no longer be continued.
    await route.continue().catch(() => undefined)
  }
  return { route, release }
}

test.describe('Upload State Machine and Recovery E2E', () => {
  test.beforeEach(async ({ filesystem, workerApp, grants }) => {
    const shares = await filesystem.listShares()
    let docsShare = shares.find((s) => s.name === 'docs')
    if (!docsShare) {
      docsShare = await filesystem.createShare('docs', workerApp.shareDir)
    }
    const existingGrants = await grants.listGrants()
    if (!existingGrants.some((g) => g.share === String(docsShare?.id))) {
      await grants.createGrant({
        user: workerApp.adminUser.id,
        share: String(docsShare.id),
        label: 'docs',
        allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share']
      })
    }
  })

  test('boundary sizes: empty file (0 B) and 1 B upload', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    const emptyName = `${namespace('empty')}.txt`
    const emptyFixture = createTempFixtureFile(emptyName, 0)

    const oneByteName = `${namespace('one')}.bin`
    const oneByteFixture = createTempFixtureFile(oneByteName, 1)

    try {
      const fileInput = page.locator('input[type="file"][multiple]')
      await fileInput.setInputFiles([emptyFixture.filePath, oneByteFixture.filePath])

      await expect(fileEntry(page, emptyName)).toBeVisible({ timeout: 15000 })
      await expect(fileEntry(page, oneByteName)).toBeVisible({ timeout: 15000 })

      await expect.poll(() => fs.existsSync(path.join(workerApp.shareDir, emptyName)), { timeout: 10000 }).toBe(true)
      expect(fs.statSync(path.join(workerApp.shareDir, emptyName)).size).toBe(0)

      await expect.poll(() => fs.existsSync(path.join(workerApp.shareDir, oneByteName)), { timeout: 10000 }).toBe(true)
      expect(fs.statSync(path.join(workerApp.shareDir, oneByteName)).size).toBe(1)
      await assertNoUnexpectedErrors(artifacts)
    } finally {
      emptyFixture.cleanup()
      oneByteFixture.cleanup()
    }
  })

  test('pause and resume upload in the upload queue', async ({ authedPage: page, workerApp, namespace, artifacts }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    const fileName = `${namespace('pause-resume')}.bin`
    const fixture = createTempFixtureFile(fileName, 15 * 1024 * 1024)
    const chunks = holdChunks()
    await page.context().route(UPLOADS, chunks.route)

    try {
      await page.locator('input[type="file"][multiple]').setInputFiles(fixture.filePath)

      const item = uploadItem(page, fileName)
      await item.getByRole('button', { name: 'Pause' }).click()
      await expect(item).toContainText('Paused')

      chunks.release()
      await item.getByRole('button', { name: 'Resume' }).click()
      await expect(fileEntry(page, fileName)).toBeVisible({ timeout: 30000 })
      expect(fs.statSync(path.join(workerApp.shareDir, fileName)).size).toBe(15 * 1024 * 1024)

      await assertNoUnexpectedErrors(artifacts)
    } finally {
      chunks.release()
      await page.context().unroute(UPLOADS, chunks.route)
      fixture.cleanup()
    }
  })

  test('cancel upload in the upload queue', async ({ authedPage: page, workerApp, namespace, artifacts }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    const fileName = `${namespace('cancel-me')}.bin`
    const fixture = createTempFixtureFile(fileName, 10 * 1024 * 1024)
    const chunks = holdChunks()
    await page.context().route(UPLOADS, chunks.route)

    try {
      await page.locator('input[type="file"][multiple]').setInputFiles(fixture.filePath)

      const item = uploadItem(page, fileName)
      await item.getByRole('button', { name: 'Cancel' }).click()
      // The tray reports the cancellation only after the server has dropped the session.
      await expect(item).toContainText('Cancelled')
      expect(fs.existsSync(path.join(workerApp.shareDir, fileName))).toBe(false)

      await assertNoUnexpectedErrors(artifacts)
    } finally {
      chunks.release()
      await page.context().unroute(UPLOADS, chunks.route)
      fixture.cleanup()
    }
  })
})
