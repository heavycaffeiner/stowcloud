import { test, expect } from '../fixtures';
import { createTempFixtureFile } from '../helpers/files';

test.describe('HTTP Transfer Fault Injection', () => {
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

  test('HTTP 429 rate limit with Retry-After recovery', async ({
    authedPage: page,
    workerApp,
    namespace,
    faults,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('fault-429')}.txt`;
    const fixture = createTempFixtureFile(fileName, 2048);

    try {
      await faults.failRequests('**/api/v1/uploads/**', 429, {
        headers: { 'Retry-After': '1' },
        times: 1,
        body: { error: 'rate_limited' },
      });

      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const uploaded = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploaded).toBeVisible({ timeout: 15000 });
    } finally {
      fixture.cleanup();
    }
  });

  test('HTTP 503 retry and recovery', async ({
    authedPage: page,
    workerApp,
    namespace,
    faults,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('fault-503')}.txt`;
    const fixture = createTempFixtureFile(fileName, 2048);

    try {
      await faults.failRequests('**/api/v1/uploads/**', 503, {
        times: 1,
        body: { error: 'temporarily_unavailable' },
      });

      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const uploaded = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploaded).toBeVisible({ timeout: 15000 });
    } finally {
      fixture.cleanup();
    }
  });

  test('HTTP 403 terminal permission refusal surfaced in UI', async ({
    authedPage: page,
    workerApp,
    namespace,
    faults,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('fault-403')}.txt`;
    const fixture = createTempFixtureFile(fileName, 2048);

    try {
      await faults.failRequests('**/api/v1/uploads/**', 403, {
        body: { error: { code: 'permission_denied', message: 'Permission denied' } },
      });

      const failingUpload = page.waitForResponse((response) => {
        const request = response.request();
        return request.method() === 'PATCH' && response.url().includes('/api/v1/uploads/') && response.status() === 403;
      });
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const response = await failingUpload;
      expect(response.status()).toBe(403);

      const item = page.locator('.sc-upload-tray__item').filter({
        has: page.locator('.sc-upload-tray__name', { hasText: fileName }),
      });
      await expect(item).toBeVisible();
      await expect(item.locator('.sc-upload-tray__message')).toBeVisible();
    } finally {
      fixture.cleanup();
    }
  });

  test('HTTP 507 terminal quota failure surfaced in UI', async ({
    authedPage: page,
    workerApp,
    namespace,
    faults,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('fault-507')}.txt`;
    const fixture = createTempFixtureFile(fileName, 2048);

    try {
      await faults.failRequests('**/api/v1/uploads/**', 507, {
        body: { error: { code: 'insufficient_storage', message: 'Quota exceeded' } },
      });

      const failingUpload = page.waitForResponse((response) => {
        const request = response.request();
        return request.method() === 'PATCH' && response.url().includes('/api/v1/uploads/') && response.status() === 507;
      });
      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const response = await failingUpload;
      expect(response.status()).toBe(507);

      const item = page.locator('.sc-upload-tray__item').filter({
        has: page.locator('.sc-upload-tray__name', { hasText: fileName }),
      });
      await expect(item).toBeVisible();
      await expect(item.locator('.sc-upload-tray__message')).toBeVisible();
    } finally {
      fixture.cleanup();
    }
  });

  test('connection abort recovery scenario', async ({
    authedPage: page,
    workerApp,
    namespace,
    faults,
  }) => {
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    const fileName = `${namespace('fault-abort')}.txt`;
    const fixture = createTempFixtureFile(fileName, 2048);

    try {
      await faults.abortRequests('**/api/v1/uploads/**', 'connectionreset', 1);

      const fileInput = page.locator('input[type="file"][multiple]');
      await fileInput.setInputFiles(fixture.filePath);

      const uploaded = page.locator('.sc-filename, .sc-file-grid__name').filter({ hasText: fileName }).first();
      await expect(uploaded).toBeVisible({ timeout: 20000 });
    } finally {
      fixture.cleanup();
    }
  });
});
