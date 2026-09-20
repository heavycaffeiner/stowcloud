import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect } from '../fixtures';
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants';

test('selecting a share keeps permission dialogs open and creates the grant', async ({
  authedPage: page,
  workerApp,
  accounts,
  filesystem,
  namespace,
  artifacts,
}) => {
  const docsDir = path.join(workerApp.shareDir, 'docs-root');
  const archiveDir = path.join(workerApp.shareDir, 'archive-root');
  fs.mkdirSync(docsDir, { recursive: true });
  fs.mkdirSync(archiveDir, { recursive: true });

  await filesystem.createShare(namespace('docs'), docsDir);
  const archive = await filesystem.createShare(namespace('archive'), archiveDir);
  const user = await accounts.createUser(namespace('grant-user'), 'Password123!', { admin: false });

  await page.addInitScript(() => localStorage.setItem('sc.locale', 'en'));
  await page.goto(`${workerApp.baseURL}/admin#users`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: `Manage folders visible to ${user.name}` }).click();

  const grantsDialog = page.locator(`mdui-dialog[headline="Folders visible to ${user.name}"]`);
  await expect(grantsDialog).toBeVisible();
  await grantsDialog.getByRole('button', { name: 'Add folder' }).click();

  const addDialog = page.locator('mdui-dialog[headline="Add folder"]');
  const shareSelect = addDialog.locator('mdui-select');
  await shareSelect.click();
  await shareSelect.locator(`mdui-menu-item[value="${archive.id}"]`).click();

  await expect(grantsDialog).toBeVisible();
  await expect(addDialog).toBeVisible();
  await expect(shareSelect).toHaveJSProperty('value', String(archive.id));

  await addDialog.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(addDialog).not.toBeVisible();
  await expect(grantsDialog).toContainText(archive.name);
  await assertNoUnexpectedErrors(artifacts);
});
