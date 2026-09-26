import { test, expect } from '../fixtures';
import { captureAndVerifyDownload } from '../helpers/downloads';
import { sha256 } from '../helpers/hashes';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';
import * as fs from 'node:fs';
import * as path from 'node:path';

test.describe('Public Share Download Link E2E', () => {
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

  test('anonymous visitor can access public link and download file', async ({
    browser,
    workerApp,
    api,
    artifacts,
  }) => {
    // 1. Admin creates link for /docs/a.txt
    const linkRes = await api.post<{ link?: { token: string }; token?: string }>('/api/v1/links', {
      path: '/docs/a.txt',
      perms: ['read', 'download'],
    });
    expect([200, 201]).toContain(linkRes.status);
    const token = linkRes.data.token || linkRes.data.link?.token;
    expect(token).toBeDefined();

    // Expected content and hash
    const sourceContent = fs.readFileSync(path.join(workerApp.shareDir, 'a.txt'));
    const expectedHash = sha256(sourceContent);

    // 2. Anonymous visitor context
    const anonContext = await browser.newContext({ ignoreHTTPSErrors: true });
    const anonPage = await anonContext.newPage();

    await anonPage.goto(`${workerApp.baseURL}/s/${token}`, { waitUntil: 'domcontentloaded' });
    await expect(anonPage.locator('text=a.txt').first()).toBeVisible({ timeout: 10000 });

    // Download button
    const downloadBtn = anonPage.locator('mdui-button').filter({ hasText: /다운로드|Download/i }).first();
    await expect(downloadBtn).toBeVisible({ timeout: 10000 });

    const downloadPromise = anonPage.waitForEvent('download');
    await downloadBtn.click();

    const downloaded = await captureAndVerifyDownload(downloadPromise, expectedHash);
    expect(downloaded.hash).toBe(expectedHash);

    await anonContext.close();
    await assertNoUnexpectedErrors(artifacts);
  });
});
