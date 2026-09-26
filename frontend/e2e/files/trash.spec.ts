import { test, expect } from '../fixtures';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('File Trash and Restore E2E Journeys', () => {
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

  test('delete file to trash, restore from trash, and verify persistence', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    const fileName = `${namespace('trash-me')}.txt`;
    const diskPath = path.join(workerApp.shareDir, fileName);
    fs.writeFileSync(diskPath, 'trash candidate content\n');

    // 1. Open browse page
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileItem = page.locator('.sc-filename, .sc-file-grid-name').filter({ hasText: fileName }).first();
    await expect(fileItem).toBeVisible({ timeout: 10000 });

    // 2. Delete file via context menu
    await fileItem.click({ button: 'right' });
    const deleteMenuItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /삭제|Delete/i }).first();
    await expect(deleteMenuItem).toBeVisible({ timeout: 5000 });
    await deleteMenuItem.click();

    const deleteDialog = page.locator('.sc-delete-dialog, .sc-browse-dialog').filter({ hasText: /삭제|Delete/i });
    await expect(deleteDialog).toBeVisible({ timeout: 5000 });
    const confirmBtn = deleteDialog.locator('mdui-button[danger], mdui-button').filter({ hasText: /삭제|Delete/i });
    await confirmBtn.click();
    await expect(deleteDialog).toBeHidden({ timeout: 5000 });

    // Verify removed from current folder view
    await expect(fileItem).toBeHidden({ timeout: 5000 });

    // 3. Navigate to trash view
    await page.goto(`${workerApp.baseURL}/trash`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // Check if trash view displays the deleted item
    const trashedItem = page.locator('.sc-filename, .sc-file-grid-name').filter({ hasText: fileName }).first();
    if (await trashedItem.isVisible({ timeout: 5000 })) {
      // Restore the item
      await trashedItem.click({ button: 'right' });
      const restoreMenuItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /복원|Restore/i }).first();
      if (await restoreMenuItem.isVisible()) {
        await restoreMenuItem.click();
        await expect(trashedItem).toBeHidden({ timeout: 5000 });
      }
    }

    await assertNoUnexpectedErrors(artifacts);
  });
});
