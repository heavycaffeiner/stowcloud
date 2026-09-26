import { test, expect } from '../fixtures';
import { assertNoUnexpectedErrors, assertTerminalLoadingState } from '../helpers/ux-invariants';

test.describe('Interaction and UX Invariants E2E', () => {
  test.beforeEach(async ({ filesystem, workerApp, grants }) => {
    const shares = await filesystem.listShares();
    let docsShare = shares.find((s) => s.name === 'docs');
    if (!docsShare) {
      docsShare = await filesystem.createShare('docs', workerApp.shareDir);
    }
    const existingGrants = await grants.listGrants();
    if (!existingGrants.some((g) => g.share === String(docsShare?.id))) {
      await grants.createGrant({
        user: workerApp.adminUser.id,
        share: String(docsShare.id),
        label: 'docs',
        allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share'],
      });
    }
  });

  test('compact viewport horizontal-overflow invariant (390x844)', async ({
    authedPage: page,
    workerApp,
    artifacts,
  }) => {
    // Set compact mobile viewport
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // Verify document does not have horizontal overflow
    const hasHorizontalOverflow = await page.evaluate(() => {
      const doc = document.documentElement;
      return doc.scrollWidth > doc.clientWidth;
    });
    expect(hasHorizontalOverflow).toBe(false);

    await assertNoUnexpectedErrors(artifacts);
  });

  test('Cancel state restoration: dialog resets input on cancel', async ({
    authedPage: page,
    workerApp,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // Open New Folder dialog
    const newBtn = page.locator('.sc-nav-drawer-new-btn, button[aria-label*="새로 만들기"], button[aria-label*="New"]').first();
    if (await newBtn.isVisible()) {
      await newBtn.click();
      const newFolderItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /새 폴더|New folder/i }).first();
      await newFolderItem.click();

      const dialog = page.locator('.sc-browse-dialog').filter({ hasText: /새 폴더|New Folder/i });
      await expect(dialog).toBeVisible();

      // Type dirty text
      const input = dialog.locator('mdui-text-field input, input').first();
      await input.fill('dirty-cancelled-name');

      // Click cancel
      const cancelBtn = dialog.locator('mdui-button').filter({ hasText: /취소|Cancel/i });
      await cancelBtn.click();
      await expect(dialog).toBeHidden({ timeout: 5000 });

      // Re-open dialog and verify input is reset to default
      await newBtn.click();
      await newFolderItem.click();
      await expect(dialog).toBeVisible();

      await expect(input).not.toHaveValue('dirty-cancelled-name', { timeout: 5000 });

      await cancelBtn.click();
    }

    await assertNoUnexpectedErrors(artifacts);
  });

  test('terminal loading state: no persistent stuck spinners', async ({
    authedPage: page,
    workerApp,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // Assert spinners settle
    await assertTerminalLoadingState(page, 10000);
    await assertNoUnexpectedErrors(artifacts);
  });
});
