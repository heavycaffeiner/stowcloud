import { test, expect } from '../fixtures'
import { assertNoUnexpectedErrors, assertTerminalLoadingState } from '../helpers/ux-invariants'

test.describe('Interaction and UX Invariants E2E', () => {
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

  test('compact viewport horizontal-overflow invariant (390x844)', async ({
    authedPage: page,
    workerApp,
    artifacts
  }) => {
    // Set compact mobile viewport
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    // Verify document does not have horizontal overflow
    const hasHorizontalOverflow = await page.evaluate(() => {
      const doc = document.documentElement
      return doc.scrollWidth > doc.clientWidth
    })
    expect(hasHorizontalOverflow).toBe(false)

    await assertNoUnexpectedErrors(artifacts)
  })

  test('Cancel state restoration: dialog resets input on cancel', async ({
    authedPage: page,
    workerApp,
    artifacts
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    const newButton = page
      .getByRole('navigation', { name: 'Main menu' })
      .getByRole('button', { name: 'New', exact: true })
    const newFolderItem = page.getByRole('menuitem', { name: 'New folder' })
    const dialog = page.getByRole('alertdialog', { name: 'New folder' })
    const input = dialog.getByRole('textbox', { name: 'Folder name' })
    const cancel = dialog.getByRole('button', { name: 'Cancel' })

    await newButton.click()
    await newFolderItem.click()
    await expect(dialog).toBeVisible()
    await input.fill('dirty-cancelled-name')
    await cancel.click()
    await expect(dialog).toBeHidden({ timeout: 5000 })

    await newButton.click()
    await newFolderItem.click()
    await expect(dialog).toBeVisible()
    await expect(input).not.toHaveValue('dirty-cancelled-name', { timeout: 5000 })
    await cancel.click()
    await expect(dialog).toBeHidden({ timeout: 5000 })

    await assertNoUnexpectedErrors(artifacts)
  })

  test('terminal loading state: no persistent stuck spinners', async ({ authedPage: page, workerApp, artifacts }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('banner')).toBeVisible()

    // Assert spinners settle
    await assertTerminalLoadingState(page, 10000)
    await assertNoUnexpectedErrors(artifacts)
  })
})
