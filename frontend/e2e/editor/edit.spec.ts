import { test, expect } from '../fixtures'
import * as path from 'node:path'
import * as fs from 'node:fs'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

test.describe('Editor Workflow E2E', () => {
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

  test('open file in editor, modify text, save, and verify disk persistence', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const fileName = `${namespace('edit-file')}.txt`
    const fullPath = path.join(workerApp.shareDir, fileName)
    fs.writeFileSync(fullPath, 'initial content\n')

    await page.goto(`${workerApp.baseURL}/edit/docs/${fileName}`, { waitUntil: 'domcontentloaded' })

    const editor = page.getByRole('textbox', { name: fileName, exact: true })
    await expect(editor).toBeVisible({ timeout: 10000 })
    await expect(editor).toContainText('initial content', { timeout: 10000 })

    await editor.click()
    await page.keyboard.press('End')
    await page.keyboard.type(' added by e2e')

    const saveBtn = page.getByRole('button', { name: 'Save (Ctrl+S)' })
    await expect(saveBtn).toBeEnabled({ timeout: 5000 })
    await saveBtn.click()

    // Verify on disk that changes were saved
    await expect.poll(() => fs.readFileSync(fullPath, 'utf8'), { timeout: 10000 }).toContain('added by e2e')

    await assertNoUnexpectedErrors(artifacts)
  })

  test('unsaved changes hold both an in-app navigation and a page unload', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const fileName = `${namespace('leave-file')}.txt`
    const fullPath = path.join(workerApp.shareDir, fileName)
    fs.writeFileSync(fullPath, 'kept\n')

    await page.goto(`${workerApp.baseURL}/edit/docs/${fileName}`, { waitUntil: 'domcontentloaded' })
    const editor = page.getByRole('textbox', { name: fileName, exact: true })
    await expect(editor).toContainText('kept', { timeout: 10000 })
    await editor.click()
    await page.keyboard.press('End')
    await page.keyboard.type(' draft')

    const prompt = page.waitForEvent('dialog')
    await page.close({ runBeforeUnload: true })
    const unload = await prompt
    expect(unload.type()).toBe('beforeunload')
    await unload.dismiss()
    expect(page.isClosed()).toBe(false)

    const leave = page.getByRole('alertdialog', { name: 'Unsaved changes' })
    await page.getByRole('button', { name: 'Go back' }).click()
    await expect(leave).toBeVisible()
    await leave.getByRole('button', { name: 'Stay' }).click()
    await expect(leave).toBeHidden()
    expect(new URL(page.url()).pathname).toBe(`/edit/docs/${fileName}`)

    await page.getByRole('button', { name: 'Go back' }).click()
    await leave.getByRole('button', { name: 'Discard and leave' }).click()
    await expect(page).toHaveURL(/\/b\/docs$/)
    expect(fs.readFileSync(fullPath, 'utf8')).toBe('kept\n')

    await assertNoUnexpectedErrors(artifacts)
  })
})
