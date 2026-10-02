import { expect, type Page } from '@playwright/test'
import type { ArtifactCollector } from '../fixtures/app'

export async function assertNoUnexpectedErrors(collector: ArtifactCollector): Promise<void> {
  collector.assertClean()
}

export async function assertTerminalLoadingState(page: Page, timeout = 10000): Promise<void> {
  const busy = page.getByRole('progressbar').or(page.locator('[aria-busy="true"]'))
  await expect(busy.filter({ visible: true })).toHaveCount(0, { timeout })
}
