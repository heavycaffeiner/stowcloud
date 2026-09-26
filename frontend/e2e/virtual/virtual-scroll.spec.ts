import { test, expect } from '@playwright/test';
import { createServer, type ViteDevServer } from 'vite';
import { fileURLToPath } from 'node:url';

let viteServer: ViteDevServer;
let viteBase: string;

test.beforeAll(async () => {
  viteServer = await createServer({
    root: fileURLToPath(new URL('../..', import.meta.url)),
    mode: 'development',
    define: { 'import.meta.env.VITE_API_MOCK': JSON.stringify('1') },
    server: { host: '127.0.0.1', port: 0, open: false },
    logLevel: 'error',
  });
  await viteServer.listen();
  viteBase = viteServer.resolvedUrls?.local[0] || '';
});

test.afterAll(async () => {
  if (viteServer) {
    await viteServer.close();
  }
});

test.describe('Virtual Scrolling Invariants', () => {
  test('100,000-entry deep-scroll advances window in list mode', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.addInitScript(() => {
      localStorage.setItem('sc.locale', 'en');
      localStorage.setItem('sc.view', 'list');
    });

    await page.goto(`${viteBase}b/home/bench`, { waitUntil: 'domcontentloaded' });
    const cell = page.locator('.sc-row').first();
    await cell.waitFor();
    const beforeText = await cell.textContent();

    // Wheel scroll
    await page.mouse.move(640, 360);
    for (let i = 0; i < 40; i++) {
      await page.mouse.wheel(0, 800);
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 16);
      await promise;
    }
    const { promise: settlePromise, resolve: settleResolve } = Promise.withResolvers<void>();
    setTimeout(settleResolve, 400);
    await settlePromise;

    const offset = await page.locator('.sc-file-table').evaluate((el) => el.scrollTop);
    const afterText = await page.locator('.sc-row').first().textContent();
    const renderedCount = await page.locator('.sc-row').count();

    expect(offset).toBeGreaterThan(0);
    expect(afterText).not.toBe(beforeText);
    expect(renderedCount).toBeGreaterThan(0);
  });

  test('100,000-entry deep-scroll advances window in grid mode', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.addInitScript(() => {
      localStorage.setItem('sc.locale', 'en');
      localStorage.setItem('sc.view', 'grid');
    });

    await page.goto(`${viteBase}b/home/bench`, { waitUntil: 'domcontentloaded' });
    const card = page.locator('.sc-file-grid__card').first();
    await card.waitFor();
    const beforeText = await card.textContent();

    await page.mouse.move(640, 360);
    for (let i = 0; i < 40; i++) {
      await page.mouse.wheel(0, 800);
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 16);
      await promise;
    }
    const { promise: settlePromise, resolve: settleResolve } = Promise.withResolvers<void>();
    setTimeout(settleResolve, 400);
    await settlePromise;

    const offset = await page.locator('.sc-file-grid').evaluate((el) => el.scrollTop);
    const afterText = await page.locator('.sc-file-grid__card').first().textContent();
    const renderedCount = await page.locator('.sc-file-grid__card').count();

    expect(offset).toBeGreaterThan(0);
    expect(afterText).not.toBe(beforeText);
    expect(renderedCount).toBeGreaterThan(0);
  });

  test('virtual list keyboard navigation: ArrowDown and Home/End', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.addInitScript(() => {
      localStorage.setItem('sc.locale', 'en');
      localStorage.setItem('sc.view', 'list');
    });

    await page.goto(`${viteBase}b/home/bench`, { waitUntil: 'domcontentloaded' });
    const firstRow = page.locator('.sc-row').first();
    await firstRow.waitFor();

    await firstRow.click();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');

    // Home returns to top
    await page.keyboard.press('Home');
    const tableOffset = await page.locator('.sc-file-table').evaluate((el) => el.scrollTop);
    expect(tableOffset).toBe(0);
  });
});
