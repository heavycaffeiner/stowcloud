import { test, expect } from '../fixtures';
import { ApiClient } from '../fixtures/auth';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('Permission Matrix and Enforcement E2E', () => {
  test.beforeEach(async ({ filesystem, workerApp, grants }) => {
    const shares = await filesystem.listShares();
    let docsShare = shares.find((s) => s.name === 'docs');
    if (!docsShare) {
      docsShare = await filesystem.createShare('docs', workerApp.shareDir);
    }
  });

  test('non-admin user without grant cannot see or access share', async ({
    browser,
    workerApp,
    accounts,
    namespace,
  }) => {
    const userName = namespace('user-nogrant');
    const userPass = 'Password123!';
    const user = await accounts.createUser(userName, userPass, { admin: false });

    // Login as user in isolated browser context
    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();
    const client = new ApiClient(workerApp.baseURL);
    await client.login(userName, userPass);
    await context.addCookies(client.getPlaywrightCookies(workerApp.baseURL));

    // 1. UI: docs share should not be visible
    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('text=a.txt')).toBeHidden({ timeout: 5000 });

    // 2. Server API: direct access should return 404 (indistinguishable from missing)
    const listRes = await client.get('/api/v1/files/list?path=docs');
    expect(listRes.status).toBe(404);

    // 3. Admin routes must be refused
    const adminRes = await client.get('/api/v1/admin/users');
    expect(adminRes.status).toBe(403);

    await context.close();
  });

  test('read-only grant allows browsing but refuses mutations in UI and server', async ({
    browser,
    workerApp,
    accounts,
    filesystem,
    grants,
    namespace,
  }) => {
    const userName = namespace('user-ro');
    const userPass = 'Password123!';
    const user = await accounts.createUser(userName, userPass, { admin: false });

    const shares = await filesystem.listShares();
    const docs = shares.find((s) => s.name === 'docs')!;

    // Grant read only
    await grants.createGrant({
      user: String(user.id),
      share: String(docs.id),
      allow: ['read'],
      deny: ['write', 'create', 'delete'],
      label: 'docs-ro',
    });

    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();
    const client = new ApiClient(workerApp.baseURL);
    await client.login(userName, userPass);
    await context.addCookies(client.getPlaywrightCookies(workerApp.baseURL));

    // UI: can browse
    await page.goto(`${workerApp.baseURL}/b/docs-ro`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('text=a.txt').first()).toBeVisible({ timeout: 10000 });

    // Server API: write and mkdir must be refused with 403
    const mkdirRes = await client.post('/api/v1/files/mkdir', { path: '/docs-ro/forbidden-dir' });
    expect(mkdirRes.status).toBe(403);

    const writeRes = await client.post('/api/v1/files/write?path=docs-ro/forbidden.txt', {
      content: 'hello',
      encoding: 'utf8',
    });
    expect(writeRes.status).toBe(403);

    await context.close();
  });

  test('grant propagation across two browser contexts without server restart', async ({
    browser,
    workerApp,
    accounts,
    filesystem,
    grants,
    namespace,
  }) => {
    const userName = namespace('user-propagate');
    const userPass = 'Password123!';
    const user = await accounts.createUser(userName, userPass, { admin: false });

    const shares = await filesystem.listShares();
    const docs = shares.find((s) => s.name === 'docs')!;

    // User context opens browser
    const userContext = await browser.newContext({ ignoreHTTPSErrors: true });
    const userPage = await userContext.newPage();
    const userClient = new ApiClient(workerApp.baseURL);
    await userClient.login(userName, userPass);
    await userContext.addCookies(userClient.getPlaywrightCookies(workerApp.baseURL));

    // Initially ungranted
    const initRes = await userClient.get('/api/v1/files/list?path=docs-prop');
    expect(initRes.status).toBe(404);

    // Admin creates grant dynamically
    const grant = await grants.createGrant({
      user: String(user.id),
      share: String(docs.id),
      allow: ['read', 'download'],
      label: 'docs-prop',
    });

    // User context can now immediately read without server restart!
    const grantedRes = await userClient.get('/api/v1/files/list?path=docs-prop');
    expect(grantedRes.status).toBe(200);

    // User UI shows content
    await userPage.goto(`${workerApp.baseURL}/b/docs-prop`, { waitUntil: 'domcontentloaded' });
    await expect(userPage.locator('text=a.txt').first()).toBeVisible({ timeout: 10000 });

    // Admin revokes grant
    await grants.deleteGrant(String(grant.id));

    // Access is gone immediately
    const revokedRes = await userClient.get('/api/v1/files/list?path=docs-prop');
    expect(revokedRes.status).toBe(404);

    await userContext.close();
  });
});
