import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect } from '../fixtures'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

// These locate by role and accessible name only, so they keep holding while
// the component layer and its class names change underneath them.
test.describe('keyboard use of the file views', () => {
  test.beforeEach(async ({ page, filesystem, workerApp, grants }) => {
    await page.addInitScript(() => {
      localStorage.setItem('sc.locale', 'en')
      localStorage.setItem('sc.view', 'list')
    })
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
  })

  test('the list moves focus, opens menus and dialogs, and takes focus back', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('keys')
    fs.mkdirSync(path.join(workerApp.shareDir, folder))
    fs.writeFileSync(path.join(workerApp.shareDir, folder, 'a.txt'), 'a\n')
    fs.writeFileSync(path.join(workerApp.shareDir, folder, 'b.txt'), 'b\n')

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    const grid = page.getByRole('grid', { name: 'File list' })
    const rowA = grid.getByRole('row').filter({ hasText: 'a.txt' })
    const rowB = grid.getByRole('row').filter({ hasText: 'b.txt' })
    await expect(rowB).toBeVisible()

    await grid.focus()
    await page.keyboard.press('ArrowDown')
    await expect(grid).toHaveAttribute('aria-activedescendant', (await rowA.getAttribute('id')) ?? '')
    await page.keyboard.press('ArrowDown')
    await expect(grid).toHaveAttribute('aria-activedescendant', (await rowB.getAttribute('id')) ?? '')
    await page.keyboard.press('Space')
    await expect(rowB).toHaveAttribute('aria-selected', 'true')

    await page.keyboard.press('Shift+F10')
    const rename = page.getByRole('menuitem', { name: 'Rename' })
    await expect(rename).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(rename).toBeHidden()
    await expect(grid).toBeFocused()

    await page.keyboard.press('F2')
    const renameDialog = page.getByRole('alertdialog', { name: 'Rename' })
    await expect(renameDialog).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(renameDialog).toBeHidden()
    await expect(grid).toBeFocused()

    await page.keyboard.press('Delete')
    const deleteDialog = page.getByRole('alertdialog', { name: 'Delete?' })
    await expect(deleteDialog).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(deleteDialog).toBeHidden()
    await expect(grid).toBeFocused()
    expect(fs.existsSync(path.join(workerApp.shareDir, folder, 'b.txt'))).toBe(true)

    await assertNoUnexpectedErrors(artifacts)
  })

  test('the folder tree moves focus between its items', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('tree')
    fs.mkdirSync(path.join(workerApp.shareDir, folder, 'one'), { recursive: true })
    fs.mkdirSync(path.join(workerApp.shareDir, folder, 'two'))

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    // The destination picker holds the same tree the compact layout shows.
    await page
      .getByRole('grid', { name: 'File list' })
      .getByRole('row')
      .filter({ hasText: 'one' })
      .click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Move or copy' }).click()

    const tree = page.getByRole('tree', { name: 'Destination folder' })
    await tree.getByRole('button', { name: 'docs', exact: true }).click()
    const parent = tree.getByRole('button', { name: folder, exact: true })
    await parent.click()
    const one = tree.getByRole('button', { name: 'one', exact: true })
    const two = tree.getByRole('button', { name: 'two', exact: true })
    await expect(two).toBeVisible()

    await one.focus()
    await page.keyboard.press('ArrowDown')
    await expect(two).toBeFocused()
    await page.keyboard.press('ArrowUp')
    await expect(one).toBeFocused()
    await page.keyboard.press('ArrowLeft')
    await expect(parent).toBeFocused()

    await assertNoUnexpectedErrors(artifacts)
  })
})
