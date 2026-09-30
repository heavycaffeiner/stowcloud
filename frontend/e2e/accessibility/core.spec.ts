import { test, expect } from '../fixtures'
import AxeBuilder from '@axe-core/playwright'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'
import { fileEntry } from '../helpers/browse'

test.describe('Accessibility Scans and Accessible Names', () => {
  test('login page accessibility scan with axe-core', async ({ page, workerApp }) => {
    await page.goto(`${workerApp.baseURL}/login`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()

    const results = await new AxeBuilder({ page }).disableRules(['color-contrast']).analyze()

    expect(results.violations).toEqual([])
  })

  test('browse page accessibility scan and accessible names for icon buttons', async ({
    authedPage: page,
    workerApp,
    filesystem,
    grants,
    artifacts
  }) => {
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

    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    // Scan the loaded listing, not the loading state.
    await expect(fileEntry(page, 'a.txt')).toBeVisible({ timeout: 10000 })

    const results = await new AxeBuilder({ page })
      .disableRules(['color-contrast', 'aria-allowed-role', 'empty-table-header'])
      .analyze()
    expect(results.violations).toEqual([])

    // Icon-only controls are reachable by their accessible names.
    await expect(page.getByRole('banner').getByRole('button', { name: 'Search', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Grid view', exact: true })).toBeVisible()

    await assertNoUnexpectedErrors(artifacts)
  })
})
