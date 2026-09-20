import { test, expect } from '../fixtures';
import { createTempFixtureFile } from '../helpers/files';
import * as path from 'node:path';
import * as fs from 'node:fs';

test.describe('Network Transport Fault Invariants', () => {
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

  test('bandwidth-constrained upload progress throttling', async ({
    authedPage: page,
    workerApp,
    namespace,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('throttled')}.bin`;
    const fixture = createTempFixtureFile(fileName, 64 * 1024);

    try {
      // Emulate bandwidth limit using CDP session
      const client = await page.context().newCDPSession(page);
      await client.send('Network.emulateNetworkConditions', {
        offline: false,
        latency: 20,
        downloadThroughput: (500 * 1024) / 8,
        uploadThroughput: (200 * 1024) / 8, // 200 KB/s
      });

      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      // Verify file eventually completes even under bandwidth constraint
      const uploaded = page.locator('.sc-browse__content .sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploaded).toBeVisible({ timeout: 20000 });
      await client.send('Network.emulateNetworkConditions', {
        offline: false,
        latency: 0,
        downloadThroughput: -1,
        uploadThroughput: -1,
      });
    } finally {
      fixture.cleanup();
    }
  });
  test('temporary outage and recovery: upload succeeds after network restored', async ({
    authedPage: page,
    workerApp,
    namespace,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('outage')}.txt`;
    const fixture = createTempFixtureFile(fileName, 4096);

    try {
      let outageActive = true;
      const { promise: delayPromise, resolve: delayResolve } = Promise.withResolvers<void>();
      setTimeout(() => {
        outageActive = false;
        delayResolve();
      }, 1000);

      await page.route('**/api/v1/uploads/**', async (route) => {
        if (outageActive) {
          await route.abort('connectionreset');
        } else {
          await route.continue();
        }
      });

      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      await delayPromise;

      // Upload retries after temporary outage and completes
      const uploaded = page.locator('.sc-browse__content .sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploaded).toBeVisible({ timeout: 25000 });

      await expect.poll(() => fs.existsSync(path.join(workerApp.shareDir, fileName)), { timeout: 10000 }).toBe(true);
    } finally {
      fixture.cleanup();
    }
  });
});
