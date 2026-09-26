import { test, expect } from '../fixtures';
import { createTempFixtureFile } from '../helpers/files';
import { captureAndVerifyDownload } from '../helpers/downloads';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('Core File Journey Smoke', () => {
  test('complete file lifecycle from UI with durable verification', async ({
    authedPage: page,
    workerApp,
    filesystem,
    grants,
    artifacts,
  }) => {
    // 1. Setup share 'docs' for the worker
    const shares = await filesystem.listShares();
    let docsShare = shares.find((s) => s.name === 'docs');
    if (!docsShare) {
      docsShare = await filesystem.createShare('docs', workerApp.shareDir);
    }

    const existingGrants = await grants.listGrants();
    const hasGrant = existingGrants.some((g) => g.share === String(docsShare?.id));
    if (!hasGrant && docsShare) {
      await grants.createGrant({
        user: workerApp.adminUser.id,
        share: String(docsShare.id),
        label: 'docs',
        allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share'],
      });
    }

    // 2. Open browse view
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible({ timeout: 10000 });

    // Verify seeded entries are visible
    await expect(page.locator('text=a.txt').first()).toBeVisible({ timeout: 10000 });

    // 3. List and grid navigation
    const viewToggle = page.locator('button[aria-label*="보기"], button[aria-label*="view"]').first();
    if (await viewToggle.isVisible()) {
      await viewToggle.click();
      await page.waitForTimeout(300);
      await viewToggle.click();
      await page.waitForTimeout(300);
    }

    // 4. Create New Folder through UI
    const folderName = `folder-${Date.now().toString().slice(-4)}`;
    // Trigger blank menu or shortcut to open new folder dialog
    await page.keyboard.press('Alt+Shift+N');
    const newFolderDialog = page.locator('.sc-browse-dialog').filter({ hasText: /새 폴더|New Folder/i });
    if (!(await newFolderDialog.isVisible())) {
      // Open via right-click or FAB
      const contentArea = page.locator('.sc-browse__table-body, .sc-browse__content').first();
      await contentArea.click({ button: 'right', position: { x: 50, y: 50 } });
      const newFolderMenuItem = page.locator('.sc-browse-new-menu button').filter({ hasText: /새 폴더|New folder/i });
      if (await newFolderMenuItem.isVisible()) {
        await newFolderMenuItem.click();
      }
    }

    if (await newFolderDialog.isVisible()) {
      const folderInput = newFolderDialog.locator('mdui-text-field input, input').first();
      await folderInput.fill(folderName);
      const createButton = newFolderDialog.locator('mdui-button').filter({ hasText: /만들기|Create|확인/i });
      await createButton.click();
      await expect(newFolderDialog).toBeHidden({ timeout: 5000 });
      await expect(page.locator(`text=${folderName}`).first()).toBeVisible({ timeout: 5000 });
    }

    // 5. Real file upload with file chooser / input
    const fixture = createTempFixtureFile(`upload-${Date.now().toString().slice(-4)}.txt`, 1024);
    try {
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      // Verify file appears in directory listing
      const filename = fixture.filePath.split('/').pop()!;
      const fileRow = page.locator('.sc-file-table [role="row"], .sc-file-grid [role="gridcell"]').filter({ hasText: filename }).first();
      await expect(fileRow).toBeVisible({ timeout: 10000 });
      const uploadedFilename = fileRow.locator('.sc-filename, .sc-file-grid__name').first();

      // 6. Download and SHA-256 verification
      // Clear any prior selection
      const clearSelectionBtn = page.locator('button[aria-label*="선택 해제"], button[aria-label*="Clear selection"]').first();
      if (await clearSelectionBtn.isVisible()) {
        await clearSelectionBtn.click();
      }

      // Right-click file row to open context menu for this specific file
      await fileRow.scrollIntoViewIfNeeded();
      await fileRow.click({ button: 'right' });
      const downloadMenuItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /다운로드|Download/i }).first();
      await expect(downloadMenuItem).toBeVisible({ timeout: 5000 });

      const downloadPromise = page.waitForEvent('download');
      await downloadMenuItem.click();
      const downloaded = await captureAndVerifyDownload(downloadPromise, fixture.hash);
      expect(downloaded.hash).toBe(fixture.hash);

      // 7. Rename file through UI context menu
      await fileRow.click({ button: 'right' });
      const renameMenuItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /이름 바꾸기|Rename/i }).first();
      await expect(renameMenuItem).toBeVisible({ timeout: 5000 });
      await renameMenuItem.click();

      const renameDialog = page.locator('.sc-browse-dialog').filter({ hasText: /이름 바꾸기|Rename/i });
      await expect(renameDialog).toBeVisible({ timeout: 5000 });

      const newName = `renamed-${Date.now().toString().slice(-4)}.txt`;
      const renameInput = renameDialog.locator('mdui-text-field input, input').first();
      await renameInput.fill(newName);
      const okBtn = renameDialog.locator('mdui-button').filter({ hasText: /확인|OK/i });
      await okBtn.click();

      await expect(renameDialog).toBeHidden({ timeout: 5000 });
      const renamedFilename = page.locator('.sc-filename').filter({ hasText: newName }).first();
      await expect(renamedFilename).toBeVisible({ timeout: 5000 });

      // 8. Delete file through UI context menu
      const renamedRow = page.locator('.sc-file-table [role="row"], .sc-file-grid [role="gridcell"]').filter({ hasText: newName }).first();
      await expect(renamedRow).toBeVisible({ timeout: 5000 });
      await renamedRow.click({ button: 'right' });
      const deleteMenuItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /삭제|Delete/i }).first();
      await deleteMenuItem.click();

      const deleteDialog = page.locator('.sc-delete-dialog, .sc-browse-dialog').filter({ hasText: /삭제|Delete/i });
      await expect(deleteDialog).toBeVisible({ timeout: 5000 });

      const confirmBtn = deleteDialog.locator('mdui-button[danger], mdui-button').filter({ hasText: /삭제|Delete/i });
      await confirmBtn.click();
      await expect(deleteDialog).toBeHidden({ timeout: 5000 });
      await expect(renamedFilename).toBeHidden({ timeout: 5000 });

      // 9. Reload persistence
      await page.reload({ waitUntil: 'domcontentloaded' });
      await expect(page.locator('.sc-shell-header')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('text=a.txt').first()).toBeVisible({ timeout: 10000 });

      // Clean artifacts
      await assertNoUnexpectedErrors(artifacts);
    } finally {
      fixture.cleanup();
    }
  });
});
