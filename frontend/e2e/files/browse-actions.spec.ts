import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect } from '../fixtures'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'
import { fileEntries, fileEntry } from '../helpers/browse'

test.describe('browse actions reach the server', () => {
  test.beforeEach(async ({ page, filesystem, workerApp, grants }) => {
    await page.addInitScript(() => localStorage.setItem('sc.view', 'list'))
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

  test('parent navigation works in empty lists and grids and stops at each share root', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('parent')
    const child = 'empty # 폴더'
    fs.mkdirSync(path.join(workerApp.shareDir, folder, child), { recursive: true })
    await page.goto(`${workerApp.baseURL}/b/docs`)
    const parent = page.getByRole('button', { name: 'Go to parent folder', exact: true })
    await expect(parent).toHaveCount(0)
    for (const mode of ['list', 'grid']) {
      if (mode === 'grid') {
        const toggle = page.getByRole('button', { name: 'Grid view', exact: true })
        if (await toggle.isVisible()) {
          await toggle.click()
        } else {
          await page.getByRole('button', { name: 'More', exact: true }).first().click()
          await page.getByRole('menuitem', { name: 'Grid view', exact: true }).click()
        }
      }
      await fileEntry(page, folder).dblclick()
      await fileEntry(page, child).dblclick()
      await expect(fileEntries(page)).toHaveCount(0)
      await expect(parent).toBeVisible()
      await parent.click()
      await expect(page).toHaveURL(`${workerApp.baseURL}/b/docs/${folder}`)
      await parent.focus()
      await page.keyboard.press('Enter')
      await expect(page).toHaveURL(`${workerApp.baseURL}/b/docs`)
      await expect(parent).toHaveCount(0)
    }
    await assertNoUnexpectedErrors(artifacts)
  })

  test('a file moved through the destination picker lands in the chosen folder', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('move')
    const source = path.join(workerApp.shareDir, folder, 'm.txt')
    fs.mkdirSync(path.join(workerApp.shareDir, folder, 'target'), { recursive: true })
    fs.writeFileSync(source, 'moved\n')

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    const grid = page.getByRole('grid', { name: 'File list' })
    await grid.getByRole('row').filter({ hasText: 'm.txt' }).click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Move or copy' }).click()

    const dialog = page.getByRole('alertdialog', { name: 'Move or copy' })
    await dialog.getByRole('button', { name: 'docs', exact: true }).click()
    await dialog.getByRole('button', { name: folder, exact: true }).click()
    await dialog.getByRole('button', { name: 'target', exact: true }).click()
    const move = dialog.getByRole('button', { name: 'Move', exact: true })
    await expect(move).toBeEnabled()
    await move.click()

    await expect(grid.getByRole('row').filter({ hasText: 'm.txt' })).toBeHidden()
    expect(fs.existsSync(path.join(workerApp.shareDir, folder, 'target', 'm.txt'))).toBe(true)
    expect(fs.existsSync(source)).toBe(false)

    await assertNoUnexpectedErrors(artifacts)
  })

  // The server pages a listing, so rows past the first page exist only after
  // the list asks for them while it scrolls.
  test('a folder longer than one page scrolls to its last entry', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('long')
    const count = 450
    fs.mkdirSync(path.join(workerApp.shareDir, folder))
    for (let index = 0; index < count; index++) {
      fs.writeFileSync(path.join(workerApp.shareDir, folder, `f${String(index).padStart(3, '0')}.txt`), '')
    }

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    const grid = page.getByRole('grid', { name: 'File list' })
    await expect(grid.getByRole('row').filter({ hasText: 'f000.txt' })).toBeVisible()
    await expect(grid).toHaveAttribute('aria-rowcount', String(count + 1))

    const last = grid.getByRole('row').filter({ hasText: `f${count - 1}.txt` })
    await expect(async () => {
      await grid.evaluate((element) => {
        element.scrollTop = element.scrollHeight
      })
      await expect(last).toBeVisible({ timeout: 1000 })
    }).toPass({ timeout: 15000 })

    await assertNoUnexpectedErrors(artifacts)
  })

  test('the details panel downloads the selected file', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('details')
    fs.mkdirSync(path.join(workerApp.shareDir, folder))
    fs.writeFileSync(path.join(workerApp.shareDir, folder, 'd.txt'), 'details\n')

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    await page.getByRole('grid', { name: 'File list' }).getByRole('row').filter({ hasText: 'd.txt' }).click()
    const details = page.getByRole('button', { name: 'Show details' }).first()
    if (await details.isVisible()) {
      await details.click()
    } else {
      // Compact layouts put details in the overflow menu.
      await page.getByRole('button', { name: 'More', exact: true }).first().click()
      await page.getByRole('menuitem', { name: 'Show details', exact: true }).click()
    }

    const download = page.waitForEvent('download')
    const panel = page.getByRole('complementary', { name: 'Details' }).or(page.getByRole('dialog', { name: 'Details' }))
    await panel.getByRole('button', { name: 'Download' }).click()
    expect((await download).suggestedFilename()).toBe('d.txt')

    await assertNoUnexpectedErrors(artifacts)
  })
})
