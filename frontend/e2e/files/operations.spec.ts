import { test, expect } from '../fixtures';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('File Operations Journeys', () => {
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

  test('list and grid navigation journey', async ({ authedPage: page, workerApp, artifacts }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const viewToggle = page.locator('button[aria-label*="보기"], button[aria-label*="view"]').first();
    await expect(viewToggle).toBeVisible();

    // Verify list view elements
    await expect(page.locator('.sc-browse-table, [role="table"], [role="grid"]').first()).toBeVisible();
    await expect(page.locator('.sc-filename').filter({ hasText: 'a.txt' })).toBeVisible();

    // Toggle to grid
    await viewToggle.click();
    await expect(page.locator('.sc-file-grid, .sc-grid').first()).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.sc-filename, .sc-file-grid-name').filter({ hasText: 'a.txt' }).first()).toBeVisible({ timeout: 5000 });

    // Toggle back to list
    await viewToggle.click();
    await expect(page.locator('.sc-browse-table, [role="table"], [role="grid"]').first()).toBeVisible({ timeout: 5000 });

    await assertNoUnexpectedErrors(artifacts);
  });

  test('New Folder creation through UI journey and disk verification', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const folderName = namespace('folder');
    const newBtn = page.locator('.sc-nav-drawer-new-btn, button[aria-label*="새로 만들기"], button[aria-label*="New"]').first();
    await expect(newBtn).toBeVisible({ timeout: 5000 });
    await newBtn.click();

    const newFolderItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /새 폴더|New folder/i }).first();
    await expect(newFolderItem).toBeVisible({ timeout: 5000 });
    await newFolderItem.click();

    const dialog = page.locator('.sc-browse-dialog').filter({ hasText: /새 폴더|New Folder/i });
    await expect(dialog).toBeVisible();

    const input = dialog.locator('mdui-text-field input, input').first();
    await input.fill(folderName);

    const createBtn = dialog.locator('mdui-button').filter({ hasText: /만들기|Create|확인/i });
    await createBtn.click();
    await expect(dialog).toBeHidden({ timeout: 5000 });

    // Verify UI shows the new folder
    const folderLocator = page.locator('.sc-filename').filter({ hasText: folderName }).first();
    await expect(folderLocator).toBeVisible({ timeout: 5000 });

    // Verify folder on durable disk
    const diskPath = path.join(workerApp.shareDir, folderName);
    expect(fs.existsSync(diskPath)).toBe(true);
    expect(fs.statSync(diskPath).isDirectory()).toBe(true);

    await assertNoUnexpectedErrors(artifacts);
  });

  test('rename and reload persistence journey', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    // Create initial file on disk
    const initialName = namespace('init') + '.txt';
    fs.writeFileSync(path.join(workerApp.shareDir, initialName), 'test content\n');

    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const initialFile = page.locator('.sc-filename').filter({ hasText: initialName }).first();
    await expect(initialFile).toBeVisible({ timeout: 10000 });

    // Rename via context menu
    await initialFile.click({ button: 'right' });
    const renameMenuItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /이름 바꾸기|Rename/i }).first();
    await expect(renameMenuItem).toBeVisible({ timeout: 5000 });
    await renameMenuItem.click();

    const renameDialog = page.locator('.sc-browse-dialog').filter({ hasText: /이름 바꾸기|Rename/i });
    await expect(renameDialog).toBeVisible({ timeout: 5000 });

    const newName = namespace('renamed') + '.txt';
    const renameInput = renameDialog.locator('mdui-text-field input, input').first();
    await renameInput.fill(newName);
    const okBtn = renameDialog.locator('mdui-button').filter({ hasText: /확인|OK/i });
    await okBtn.click();

    await expect(renameDialog).toBeHidden({ timeout: 5000 });
    const renamedFile = page.locator('.sc-filename').filter({ hasText: newName }).first();
    await expect(renamedFile).toBeVisible({ timeout: 5000 });
    await expect(initialFile).toBeHidden();

    // Reload page and check persistence
    await page.reload({ waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-filename').filter({ hasText: newName }).first()).toBeVisible({ timeout: 10000 });

    // Verify durable disk state
    expect(fs.existsSync(path.join(workerApp.shareDir, newName))).toBe(true);
    expect(fs.existsSync(path.join(workerApp.shareDir, initialName))).toBe(false);

    await assertNoUnexpectedErrors(artifacts);
  });
});
