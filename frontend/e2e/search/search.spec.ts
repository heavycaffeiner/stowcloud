import { test, expect } from '../fixtures'
import { ApiClient } from '../fixtures/auth'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

test.describe('Search E2E and Permission Invariant', () => {
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

  test('search UI journey: open search, type query, receive streaming results', async ({
    authedPage: page,
    workerApp,
    artifacts
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await page.getByRole('banner').getByRole('button', { name: 'Search', exact: true }).click()

    const sheet = page.getByRole('dialog', { name: 'Search' })
    const searchInput = sheet.getByRole('searchbox')
    await expect(searchInput).toBeVisible({ timeout: 5000 })

    await searchInput.fill('a.txt')
    await searchInput.press('Enter')

    // Any hit with this exact name proves the stream delivered results.
    const resultItem = sheet.getByRole('listitem').filter({ has: page.getByText('a.txt', { exact: true }) })
    await expect(resultItem.first()).toBeVisible({ timeout: 10000 })

    await assertNoUnexpectedErrors(artifacts)
  })

  test('streaming search permission invariant: restricted account never receives forbidden paths', async ({
    workerApp,
    accounts,
    filesystem,
    grants,
    namespace
  }) => {
    // Create restricted user
    const userName = namespace('srch-user')
    const userPass = 'Password123!'
    const user = await accounts.createUser(userName, userPass, { admin: false })

    // User has NO grant on 'docs'
    const client = new ApiClient(workerApp.baseURL)
    await client.login(userName, userPass)

    // Query search stream via API as restricted user
    const res = await client.get('/api/v1/files/search/stream?q=a.txt')
    // Either forbidden/empty or results must contain 0 hits
    if (res.status === 200 && res.rawText) {
      expect(res.rawText).not.toContain('/docs/a.txt')
    } else {
      expect([200, 403, 404]).toContain(res.status)
    }
  })
})
