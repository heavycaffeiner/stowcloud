import { test, expect } from '../fixtures';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('Editor Workflow E2E', () => {
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

  test('open file in editor, modify text, save, and verify disk persistence', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    const fileName = `${namespace('edit-file')}.txt`;
    const fullPath = path.join(workerApp.shareDir, fileName);
    fs.writeFileSync(fullPath, 'initial content\n');

    await page.goto(`${workerApp.baseURL}/edit/docs/${fileName}`, { waitUntil: 'domcontentloaded' });

    // Verify editor mounts
    const editor = page.locator('.cm-content, [role="textbox"]').first();
    await expect(editor).toBeVisible({ timeout: 10000 });
    await expect(editor).toContainText('initial content', { timeout: 10000 });

    // Focus editor and type additional content
    await editor.click();
    await page.keyboard.press('End');
    await page.keyboard.type(' added by e2e');

    // Save button should become enabled
    const saveBtn = page.locator('mdui-button').filter({ hasText: /저장|Save/i }).first();
    await expect(saveBtn).toBeVisible({ timeout: 5000 });
    await saveBtn.click();

    // Verify on disk that changes were saved
    await expect.poll(() => fs.readFileSync(fullPath, 'utf8'), { timeout: 10000 }).toContain('added by e2e');

    await assertNoUnexpectedErrors(artifacts);
  });
});
