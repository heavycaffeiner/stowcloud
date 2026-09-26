import { test, expect } from '../fixtures';
import AxeBuilder from '@axe-core/playwright';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test.describe('Accessibility Scans and Accessible Names', () => {
  test('login page accessibility scan with axe-core', async ({ page, workerApp }) => {
    await page.goto(`${workerApp.baseURL}/login`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('form.sc-auth-card-login')).toBeVisible();

    const results = await new AxeBuilder({ page })
      .disableRules(['color-contrast'])
      .analyze();

    expect(results.violations).toEqual([]);
  });

  test('browse page accessibility scan and accessible names for icon buttons', async ({
    authedPage: page,
    workerApp,
    filesystem,
    grants,
    artifacts,
  }) => {
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

    await page.goto(`${workerApp.baseURL}/b/docs`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.sc-shell-header')).toBeVisible();

    // 1. Axe accessibility scan
    const results = await new AxeBuilder({ page })
      .disableRules(['color-contrast', 'aria-allowed-role', 'empty-table-header'])
      .analyze();
    expect(results.violations).toEqual([]);

    // 2. Critical icon controls have accessible names
    const searchBtn = page.locator('.sc-shell-header-search, button[aria-label*="검색"], button[aria-label*="Search"]').first();
    await expect(searchBtn).toHaveAttribute('aria-label', /.+/);

    const refreshBtn = page.locator('.sc-browse-action-btn[aria-label*="새로고침"], .sc-browse-action-btn[aria-label*="Refresh"]').first();
    if (await refreshBtn.isVisible()) {
      await expect(refreshBtn).toHaveAttribute('aria-label', /.+/);
    }

    const viewToggle = page.locator('button[aria-label*="보기"], button[aria-label*="view"]').first();
    await expect(viewToggle).toHaveAttribute('aria-label', /.+/);

    await assertNoUnexpectedErrors(artifacts);
  });
});
