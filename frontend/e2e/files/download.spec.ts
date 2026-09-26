import { test, expect } from '../fixtures';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { generateDeterministicBytes } from '../helpers/files';
import { sha256 } from '../helpers/hashes';
import { captureAndVerifyDownload, isZipArchive } from '../helpers/downloads';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('File Download E2E Journeys', () => {
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

  test('single file browser download and SHA-256 verification', async ({
    authedPage: page,
    workerApp,
    namespace,
    artifacts,
  }) => {
    const fileName = `${namespace('dl-file')}.bin`;
    const buffer = generateDeterministicBytes(8192);
    const expectedHash = sha256(buffer);
    fs.writeFileSync(path.join(workerApp.shareDir, fileName), buffer);

    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileItem = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
    await expect(fileItem).toBeVisible({ timeout: 10000 });

    // Right-click file to open context menu and download
    await fileItem.click({ button: 'right' });
    const downloadItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /다운로드|Download/i }).first();
    await expect(downloadItem).toBeVisible({ timeout: 5000 });

    const downloadPromise = page.waitForEvent('download');
    await downloadItem.click();

    const downloaded = await captureAndVerifyDownload(downloadPromise, expectedHash);
    expect(downloaded.size).toBe(8192);
    expect(downloaded.hash).toBe(expectedHash);

    await assertNoUnexpectedErrors(artifacts);
  });

  test('archive download of directory with ZIP signature verification', async ({
    authedPage: page,
    workerApp,
    artifacts,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // 'sub' is a directory in workerApp.shareDir
    const folderItem = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: 'sub' }).first();
    await expect(folderItem).toBeVisible({ timeout: 10000 });

    await folderItem.click({ button: 'right' });
    const downloadItem = page.locator('.sc-browse-new-menu button[role="menuitem"]').filter({ hasText: /다운로드|Download/i }).first();
    await expect(downloadItem).toBeVisible({ timeout: 5000 });

    const downloadPromise = page.waitForEvent('download');
    await downloadItem.click();

    const downloaded = await captureAndVerifyDownload(downloadPromise);
    expect(downloaded.filename).toMatch(/\.zip$/i);
    expect(isZipArchive(downloaded.buffer)).toBe(true);

    await assertNoUnexpectedErrors(artifacts);
  });
});
