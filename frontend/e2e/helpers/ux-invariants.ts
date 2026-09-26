import { expect, type Page } from '@playwright/test';
import type { ArtifactCollector } from '../fixtures/app';

export async function assertNoUnexpectedErrors(collector: ArtifactCollector): Promise<void> {
  collector.assertClean();
}

export async function assertToastMessage(
  page: Page,
  expected: string | RegExp,
  timeout = 5000,
): Promise<void> {
  const toast = page.locator('.mdui-snackbar, [role="alert"], .toast, .snackbar').first();
  await expect(toast).toContainText(expected, { timeout });
}

export async function assertTerminalLoadingState(page: Page, timeout = 10000): Promise<void> {
  const spinner = page.locator('mdui-circular-progress, [aria-busy="true"], .loading-indicator').first();
  await expect(spinner).toBeHidden({ timeout });
}
