import { test, expect } from '../fixtures'
import * as path from 'node:path'
import * as fs from 'node:fs'
import { fileEntry } from '../helpers/browse'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

test.describe('File Operations Journeys', () => {
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

  test('list and grid navigation journey', async ({ authedPage: page, workerApp, artifacts }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    const list = page.getByRole('grid', { name: 'File list' })
    await expect(list).toBeVisible()
    await expect(fileEntry(page, 'a.txt')).toBeVisible()

    await page.getByRole('button', { name: 'Grid view' }).click()
    await expect(page.getByRole('grid', { name: 'File grid' })).toBeVisible({ timeout: 5000 })
    await expect(list).toBeHidden()
    await expect(fileEntry(page, 'a.txt')).toBeVisible({ timeout: 5000 })

    await page.getByRole('button', { name: 'List view' }).click()
    await expect(list).toBeVisible({ timeout: 5000 })

    await assertNoUnexpectedErrors(artifacts)
  })

  test('New Folder creation through UI journey and disk verification', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    const folderName = namespace('folder')
    await page.getByRole('navigation', { name: 'Main menu' }).getByRole('button', { name: 'New', exact: true }).click()
    await page.getByRole('menuitem', { name: 'New folder' }).click()

    const dialog = page.getByRole('alertdialog', { name: 'New folder' })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('textbox', { name: 'Folder name' }).fill(folderName)
    await dialog.getByRole('button', { name: 'Create' }).click()
    await expect(dialog).toBeHidden({ timeout: 5000 })

    await expect(fileEntry(page, folderName)).toBeVisible({ timeout: 5000 })

    // Verify folder on durable disk
    const diskPath = path.join(workerApp.shareDir, folderName)
    expect(fs.existsSync(diskPath)).toBe(true)
    expect(fs.statSync(diskPath).isDirectory()).toBe(true)

    await assertNoUnexpectedErrors(artifacts)
  })

  test('rename and reload persistence journey', async ({ authedPage: page, workerApp, namespace, artifacts }) => {
    // Create initial file on disk
    const initialName = namespace('init') + '.txt'
    fs.writeFileSync(path.join(workerApp.shareDir, initialName), 'test content\n')

    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    const initialFile = fileEntry(page, initialName)
    await expect(initialFile).toBeVisible({ timeout: 10000 })

    await initialFile.click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    const renameDialog = page.getByRole('alertdialog', { name: 'Rename' })
    await expect(renameDialog).toBeVisible({ timeout: 5000 })
    const newName = namespace('renamed') + '.txt'
    await renameDialog.getByRole('textbox', { name: 'New name' }).fill(newName)
    await renameDialog.getByRole('button', { name: 'OK' }).click()

    await expect(renameDialog).toBeHidden({ timeout: 5000 })
    await expect(fileEntry(page, newName)).toBeVisible({ timeout: 5000 })
    await expect(initialFile).toBeHidden()

    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(fileEntry(page, newName)).toBeVisible({ timeout: 10000 })

    // Verify durable disk state
    expect(fs.existsSync(path.join(workerApp.shareDir, newName))).toBe(true)
    expect(fs.existsSync(path.join(workerApp.shareDir, initialName))).toBe(false)

    await assertNoUnexpectedErrors(artifacts)
  })
})
