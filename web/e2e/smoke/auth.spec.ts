import { test, expect } from '../fixtures';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('Authentication UI Smoke', () => {
  test('login with wrong password then valid credentials and sign out', async ({
    page,
    workerApp,
    artifacts,
  }) => {
    const admin = await workerApp.setupAdmin();

    await page.goto(`${workerApp.baseURL}/login`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('form.sc-auth-card--login')).toBeVisible();

    // 1. Wrong credentials
    const usernameInput = page.locator('mdui-text-field[autocomplete="username"] input, input[autocomplete="username"]').first();
    const passwordInput = page.locator('mdui-text-field[autocomplete="current-password"] input, input[type="password"]').first();
    const submitButton = page.locator('mdui-button[type="submit"], button[type="submit"]').first();

    await usernameInput.fill('nonexistent-user');
    await passwordInput.fill('wrong-password');
    await submitButton.click();

    const alert = page.locator('.sc-auth-card__error[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5000 });
    // 2. Correct credentials
    await usernameInput.fill(admin.name);
    await passwordInput.fill(admin.pass);
    await submitButton.click();

    // Should navigate to /b or dashboard
    await expect(page).toHaveURL(/\/b/, { timeout: 10000 });

    // 3. User is signed in and interface renders shell header
    await expect(page.locator('.sc-shell-header')).toBeVisible({ timeout: 10000 });

    // 4. Sign out flow
    await page.locator('.sc-shell-header__avatar-btn').click();
    const accountMenu = page.locator('.sc-shell-header__account-menu');
    await expect(accountMenu).toBeVisible();

    const signOutButton = accountMenu.locator('button[role="menuitem"]').last();
    await signOutButton.click();

    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
    await expect(page.locator('form.sc-auth-card--login')).toBeVisible();

    // Check artifacts
    await assertNoUnexpectedErrors(artifacts);
  });
});
