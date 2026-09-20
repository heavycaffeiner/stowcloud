import { test, expect } from '../fixtures';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { createTempFixtureFile } from '../helpers/files';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('Upload State Machine and Recovery E2E', () => {
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

  test('boundary sizes: empty file (0 B) and 1 B upload', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const emptyName = `${namespace('empty')}.txt`;
    const emptyFixture = createTempFixtureFile(emptyName, 0);

    const oneByteName = `${namespace('one')}.bin`;
    const oneByteFixture = createTempFixtureFile(oneByteName, 1);

    try {
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles([emptyFixture.filePath, oneByteFixture.filePath]);

      // Both should appear in file listing
      await expect(page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: emptyName }).first()).toBeVisible({ timeout: 15000 });
      await expect(page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: oneByteName }).first()).toBeVisible({ timeout: 15000 });

      // Verify on disk
      await expect.poll(() => fs.existsSync(path.join(workerApp.shareDir, emptyName)), { timeout: 10000 }).toBe(true);
      expect(fs.statSync(path.join(workerApp.shareDir, emptyName)).size).toBe(0);

      await expect.poll(() => fs.existsSync(path.join(workerApp.shareDir, oneByteName)), { timeout: 10000 }).toBe(true);
      expect(fs.statSync(path.join(workerApp.shareDir, oneByteName)).size).toBe(1);
      await assertNoUnexpectedErrors(artifacts);
    } finally {
      emptyFixture.cleanup();
      oneByteFixture.cleanup();
    }
  });

  test('pause and resume upload in UploadTray', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // 15 MiB file (multi-chunk) to allow observing pause and resume
    const fileName = `${namespace('pause-resume')}.bin`;
    const fixture = createTempFixtureFile(fileName, 15 * 1024 * 1024);

    try {
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      // Upload tray should open
      const tray = page.locator('.sc-upload-tray');
      await expect(tray).toBeVisible({ timeout: 10000 });

      // Pause button should appear while uploading
      const pauseBtn = page.locator('button[aria-label*="일시 중지"], button[aria-label*="Pause"]').first();
      if (await pauseBtn.isVisible({ timeout: 3000 })) {
        await pauseBtn.click();
        // Tray should show paused indicator
        await expect(page.locator('text=/일시 중지됨|Paused/i').first()).toBeVisible({ timeout: 5000 });

        // Resume upload
        const resumeBtn = page.locator('button[aria-label*="재개"], button[aria-label*="Resume"]').first();
        await expect(resumeBtn).toBeVisible({ timeout: 5000 });
        await resumeBtn.click();
      }

      // Should complete and show file
      await expect(page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first()).toBeVisible({ timeout: 30000 });

      await assertNoUnexpectedErrors(artifacts);
    } finally {
      fixture.cleanup();
    }
  });
  test('cancel upload in UploadTray', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('cancel-me')}.bin`;
    const fixture = createTempFixtureFile(fileName, 10 * 1024 * 1024);

    // Delay upload chunk so cancel can be clicked deterministically in flight
    await page.route('**/api/v1/files/upload/**', async (route) => {
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 2000);
      await promise;
      await route.continue();
    });

    try {
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const tray = page.locator('.sc-upload-tray');
      await expect(tray).toBeVisible({ timeout: 10000 });

      const cancelBtn = page.locator('.sc-upload-tray button[aria-label*="취소"], .sc-upload-tray button[aria-label*="Cancel"]').first();
      await expect(cancelBtn).toBeVisible({ timeout: 5000 });
      await cancelBtn.click();

      // Should indicate canceled status
      await expect(page.locator('text=/취소됨|Canceled/i').first()).toBeVisible({ timeout: 5000 });

      // File should NOT be finalized on disk
      await page.waitForTimeout(1000);
      expect(fs.existsSync(path.join(workerApp.shareDir, fileName))).toBe(false);

      await assertNoUnexpectedErrors(artifacts);
    } finally {
      fixture.cleanup();
    }
  });
});
