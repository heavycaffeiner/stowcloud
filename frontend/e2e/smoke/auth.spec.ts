import { test, expect } from '../fixtures'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

test.describe('Authentication UI Smoke', () => {
  test('login with wrong password then valid credentials and sign out', async ({ page, workerApp, artifacts }) => {
    const admin = await workerApp.setupAdmin()

    await page.goto(`${workerApp.baseURL}/login`, { waitUntil: 'domcontentloaded' })
    const usernameInput = page.getByRole('textbox', { name: 'Username' })
    const passwordInput = page.getByLabel('Password', { exact: true })
    const submitButton = page.getByRole('button', { name: 'Sign in' })
    await expect(submitButton).toBeVisible()

    // 1. Wrong credentials
    await usernameInput.fill('nonexistent-user')
    await passwordInput.fill('wrong-password')
    await submitButton.click()
    await expect(page.getByRole('alert')).toBeVisible({ timeout: 5000 })

    // 2. Correct credentials
    await usernameInput.fill(admin.name)
    await passwordInput.fill(admin.pass)
    await submitButton.click()
    await expect(page).toHaveURL(/\/b/, { timeout: 10000 })

    // 3. The account button in the header is named after the signed-in user.
    const banner = page.getByRole('banner')
    await expect(banner).toBeVisible({ timeout: 10000 })
    await banner.getByRole('button', { name: admin.name, exact: true }).click()

    // 4. Sign out flow
    await page.getByRole('menu').getByRole('menuitem', { name: 'Sign out' }).click()
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 })
    await expect(submitButton).toBeVisible()

    // Check artifacts
    await assertNoUnexpectedErrors(artifacts)
  })
})
