import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect } from '../fixtures'
import { fileEntry } from '../helpers/browse'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

// The address owns what a page shows, so Back, reload and a shared link all
// land on the same view.
test.describe('the URL holds the view', () => {
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

  test('Back closes the preview and stays in the folder', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('preview')
    fs.mkdirSync(path.join(workerApp.shareDir, folder))
    fs.writeFileSync(path.join(workerApp.shareDir, folder, 'p #1.txt'), 'preview\n')

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    await fileEntry(page, 'p #1.txt').dblclick()
    const preview = page.getByRole('dialog', { name: 'p #1.txt' })
    await expect(preview).toBeVisible()
    expect(new URL(page.url()).searchParams.get('preview')).toBe('p #1.txt')

    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(preview).toBeVisible()

    await page.goBack()
    await expect(preview).toBeHidden()
    expect(new URL(page.url()).pathname).toBe(`/b/docs/${folder}`)
    expect(new URL(page.url()).searchParams.has('preview')).toBe(false)
    await expect(fileEntry(page, 'p #1.txt')).toBeVisible()

    await assertNoUnexpectedErrors(artifacts)
  })

  test('a filtered folder reloads to the same filter', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts
  }) => {
    const folder = namespace('filter')
    fs.mkdirSync(path.join(workerApp.shareDir, folder, 'sub'), { recursive: true })
    fs.writeFileSync(path.join(workerApp.shareDir, folder, 'f.txt'), 'file\n')

    await page.goto(`${workerApp.baseURL}/b/docs/${folder}`, { waitUntil: 'domcontentloaded' })
    await expect(fileEntry(page, 'f.txt')).toBeVisible()
    await page.getByRole('button', { name: 'Type', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Folders' }).click()
    await expect(fileEntry(page, 'f.txt')).toBeHidden()
    expect(new URL(page.url()).searchParams.get('type')).toBe('folders')

    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(fileEntry(page, 'sub')).toBeVisible()
    await expect(fileEntry(page, 'f.txt')).toBeHidden()
    await expect(page.getByRole('button', { name: 'Type', exact: true })).toContainText('Folders')

    await assertNoUnexpectedErrors(artifacts)
  })

  test('changing a settings tab replaces the history entry', async ({ authedPage: page, workerApp, artifacts }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await page.goto(`${workerApp.baseURL}/settings/account`, { waitUntil: 'domcontentloaded' })
    const tabs = page.getByRole('main').getByRole('navigation', { name: 'Settings' })
    await tabs.getByRole('button', { name: 'Security' }).click()
    await expect(page).toHaveURL(/\/settings\/security$/)
    await expect(tabs.getByRole('button', { name: 'Security' })).toHaveAttribute('aria-current', 'page')

    await page.goBack()
    await expect(page).toHaveURL(/\/b\/docs$/)

    await assertNoUnexpectedErrors(artifacts)
  })

  test('a session that dies under the page sends it to sign-in', async ({
    authedPage: page,
    context,
    workerApp,
    artifacts
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('grid', { name: 'File list' })).toBeVisible()

    await context.clearCookies()
    await page.getByRole('button', { name: 'Refresh' }).click()

    await expect(page).toHaveURL(/\/login$/, { timeout: 10000 })
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()

    await assertNoUnexpectedErrors(artifacts)
  })
})
