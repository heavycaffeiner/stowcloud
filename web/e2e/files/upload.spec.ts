import { test, expect } from '../fixtures';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { createTempFixtureFile } from '../helpers/files';
import { sha256 } from '../helpers/hashes';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('File Upload E2E Journeys', () => {
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

  test('real file input upload with uploaded-byte verification against disk', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('file')}.txt`;
    const fixture = createTempFixtureFile(fileName, 4096);

    try {
      // Trigger upload through real file input
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      // Verify file appears in UI
      const uploadedItem = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploadedItem).toBeVisible({ timeout: 15000 });

      // Verify on durable server disk
      const serverFilePath = path.join(workerApp.shareDir, fileName);
      await expect.poll(() => fs.existsSync(serverFilePath), { timeout: 10000 }).toBe(true);

      const serverBytes = fs.readFileSync(serverFilePath);
      expect(serverBytes.length).toBe(4096);
      expect(sha256(serverBytes)).toBe(fixture.hash);

      await assertNoUnexpectedErrors(artifacts);
    } finally {
      fixture.cleanup();
    }
  });

  test('boundary chunk-size upload (5 MiB) and byte verification', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('chunk-5m')}.bin`;
    const size = 5 * 1024 * 1024; // 5 MiB chunk boundary
    const fixture = createTempFixtureFile(fileName, size);

    try {
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const uploadedItem = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploadedItem).toBeVisible({ timeout: 30000 });

      const serverFilePath = path.join(workerApp.shareDir, fileName);
      await expect.poll(() => fs.existsSync(serverFilePath), { timeout: 15000 }).toBe(true);

      const serverBytes = fs.readFileSync(serverFilePath);
      expect(serverBytes.length).toBe(size);
      expect(sha256(serverBytes)).toBe(fixture.hash);

      await assertNoUnexpectedErrors(artifacts);
    } finally {
      fixture.cleanup();
    }
  });
});
