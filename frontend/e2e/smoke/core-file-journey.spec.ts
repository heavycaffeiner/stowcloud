import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect } from '../fixtures'
import { createTempFixtureFile } from '../helpers/files'
import { captureAndVerifyDownload } from '../helpers/downloads'
import { fileEntry } from '../helpers/browse'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

test.describe('Core File Journey Smoke', () => {
  test('complete file lifecycle from UI with durable verification', async ({
    authedPage: page,
    workerApp,
    filesystem,
    grants,
    namespace,
    artifacts
  }) => {
    // 1. Setup share 'docs' for the worker
    const shares = await filesystem.listShares()
    let docsShare = shares.find((s) => s.name === 'docs')
    if (!docsShare) {
      docsShare = await filesystem.createShare('docs', workerApp.shareDir)
    }

    const existingGrants = await grants.listGrants()
    const hasGrant = existingGrants.some((g) => g.share === String(docsShare?.id))
    if (!hasGrant && docsShare) {
      await grants.createGrant({
        user: workerApp.adminUser.id,
        share: String(docsShare.id),
        label: 'docs',
        allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share']
      })
    }

    // Keep the blank area available even when earlier tests filled the worker's share.
    const journey = namespace('journey')
    const root = path.join(workerApp.shareDir, journey)
    fs.mkdirSync(path.join(root, 'sub'), { recursive: true })
    fs.writeFileSync(path.join(root, 'a.txt'), 'hello\n')
    fs.writeFileSync(path.join(root, 'sub', 'b.txt'), 'world\n')

    // 2. Open browse view
    await page.goto(`${workerApp.baseURL}/b/docs/${journey}`, { waitUntil: 'domcontentloaded' })
    const list = page.getByRole('grid', { name: 'File list' })
    await expect(fileEntry(page, 'a.txt')).toBeVisible({ timeout: 10000 })

    // 3. List and grid navigation
    await page.getByRole('button', { name: 'Grid view' }).click()
    await expect(page.getByRole('grid', { name: 'File grid' })).toBeVisible()
    await page.getByRole('button', { name: 'List view' }).click()
    await expect(list).toBeVisible()

    // 4. Create a folder from the menu on the empty part of the list
    const folderName = `folder-${Date.now().toString().slice(-4)}`
    const listBox = await list.boundingBox()
    expect(listBox).not.toBeNull()
    await list.click({ button: 'right', position: { x: 20, y: listBox!.height - 20 } })
    await page.getByRole('menuitem', { name: 'New folder' }).click()
    const newFolderDialog = page.getByRole('alertdialog', { name: 'New folder' })
    await newFolderDialog.getByRole('textbox', { name: 'Folder name' }).fill(folderName)
    await newFolderDialog.getByRole('button', { name: 'Create' }).click()
    await expect(newFolderDialog).toBeHidden({ timeout: 5000 })
    await expect(fileEntry(page, folderName)).toBeVisible({ timeout: 5000 })

    // 5. Real file upload with file chooser / input
    const fixture = createTempFixtureFile(`upload-${Date.now().toString().slice(-4)}.txt`, 1024)
    try {
      const fileInput = page.locator('input[type="file"][multiple]')
      await fileInput.setInputFiles(fixture.filePath)

      const filename = fixture.filePath.split('/').pop()!
      const fileRow = fileEntry(page, filename)
      await expect(fileRow).toBeVisible({ timeout: 10000 })

      // 6. Download and SHA-256 verification
      await fileRow.click({ button: 'right' })
      const downloadPromise = page.waitForEvent('download')
      await page.getByRole('menuitem', { name: 'Download' }).click()
      const downloaded = await captureAndVerifyDownload(downloadPromise, fixture.hash)
      expect(downloaded.hash).toBe(fixture.hash)

      // 7. Rename file through UI context menu
      await fileRow.click({ button: 'right' })
      await page.getByRole('menuitem', { name: 'Rename' }).click()
      const renameDialog = page.getByRole('alertdialog', { name: 'Rename' })
      await expect(renameDialog).toBeVisible({ timeout: 5000 })
      const newName = `renamed-${Date.now().toString().slice(-4)}.txt`
      await renameDialog.getByRole('textbox', { name: 'New name' }).fill(newName)
      await renameDialog.getByRole('button', { name: 'OK' }).click()
      await expect(renameDialog).toBeHidden({ timeout: 5000 })
      const renamedRow = fileEntry(page, newName)
      await expect(renamedRow).toBeVisible({ timeout: 5000 })

      // 8. Delete file through UI context menu
      await renamedRow.click({ button: 'right' })
      await page.getByRole('menuitem', { name: 'Delete' }).click()
      const deleteDialog = page.getByRole('alertdialog', { name: 'Delete?' })
      await expect(deleteDialog).toBeVisible({ timeout: 5000 })
      await deleteDialog.getByRole('button', { name: 'Delete' }).click()
      await expect(deleteDialog).toBeHidden({ timeout: 5000 })
      await expect(renamedRow).toBeHidden({ timeout: 5000 })

      // 9. Reload persistence
      await page.reload({ waitUntil: 'domcontentloaded' })
      await expect(fileEntry(page, 'a.txt')).toBeVisible({ timeout: 10000 })
      await expect(fileEntry(page, folderName)).toBeVisible()
      await expect(renamedRow).toBeHidden()

      // Clean artifacts
      await assertNoUnexpectedErrors(artifacts)
    } finally {
      fixture.cleanup()
    }
  })
})
