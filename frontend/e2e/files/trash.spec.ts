import { test, expect } from '../fixtures'
import * as path from 'node:path'
import * as fs from 'node:fs'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'
import { fileEntry } from '../helpers/browse'

test.describe('File Trash and Restore E2E Journeys', () => {
  test.beforeEach(async ({ filesystem, workerApp, grants }) => {
    const shares = await filesystem.listShares()
    let docsShare = shares.find((s) => s.name === 'docs')
    if (!docsShare) {
      docsShare = await filesystem.createShare('docs', workerApp.shareDir)
    }
    // Shares start with the trash off, which would make a delete permanent.
    if (!docsShare.trash) {
      docsShare = await filesystem.setTrash(docsShare.id, true)
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

  test('delete file to trash, restore from trash, and verify persistence', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const fileName = `${namespace('trash-me')}.txt`
    const diskPath = path.join(workerApp.shareDir, fileName)
    fs.writeFileSync(diskPath, 'trash candidate content\n')

    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    const fileItem = fileEntry(page, fileName)
    await expect(fileItem).toBeVisible({ timeout: 10000 })

    await fileItem.click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Delete' }).click()
    const deleteDialog = page.getByRole('alertdialog', { name: 'Delete?' })
    await deleteDialog.getByRole('button', { name: 'Delete' }).click()
    await expect(deleteDialog).toBeHidden({ timeout: 5000 })
    await expect(fileItem).toBeHidden({ timeout: 5000 })
    expect(fs.existsSync(diskPath)).toBe(false)

    await page.goto(`${workerApp.baseURL}/trash`, { waitUntil: 'domcontentloaded' })
    const trashedItem = page.getByRole('listitem').filter({ has: page.getByTitle(fileName, { exact: true }) })
    await expect(trashedItem).toBeVisible({ timeout: 10000 })

    await trashedItem.getByRole('button', { name: 'Restore' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Restored 1 item' })).toBeVisible({ timeout: 5000 })
    await expect(trashedItem).toBeHidden({ timeout: 5000 })
    expect(fs.readFileSync(diskPath, 'utf8')).toBe('trash candidate content\n')

    await assertNoUnexpectedErrors(artifacts)
  })
})
