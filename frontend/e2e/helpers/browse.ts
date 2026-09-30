import type { Locator, Page } from '@playwright/test'

/** Every rendered list row and grid card. Header rows and skeletons carry no aria-selected. */
export function fileEntries(page: Page): Locator {
  return page.getByRole('grid', { name: /^File (list|grid)$/ }).locator('[aria-selected]')
}

/** The list row or grid card of the entry with exactly this name. */
export function fileEntry(page: Page, name: string): Locator {
  return fileEntries(page).filter({ has: page.getByTitle(name, { exact: true }) })
}

/** The upload tray entry for this file name. */
export function uploadItem(page: Page, name: string): Locator {
  return page.getByRole('region', { name: 'Upload' }).getByRole('listitem').filter({ hasText: name })
}
