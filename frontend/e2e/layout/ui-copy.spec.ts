import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect } from '../fixtures'

for (const locale of ['en', 'ko']) {
  test(`specific actions and critical warnings remain available in ${locale}`, async ({
    browser,
    api,
    workerApp,
    filesystem,
    grants,
    namespace
  }, testInfo) => {
    const t: Record<string, string> = JSON.parse(
      fs.readFileSync(new URL(`../../src/i18n/${locale}.json`, import.meta.url), 'utf8')
    )
    const context = await browser.newContext({
      ...testInfo.project.use,
      ignoreHTTPSErrors: true,
      reducedMotion: 'reduce'
    })
    try {
      await context.addCookies(api.getPlaywrightCookies(workerApp.baseURL))
      await context.addInitScript((locale) => localStorage.setItem('sc.locale', locale), locale)
      const page = await context.newPage()
      const shareName = namespace('copy')
      const folder = path.join(workerApp.shareDir, shareName)
      fs.mkdirSync(folder)
      const share = await filesystem.createShare(shareName, folder)
      const existingGrants = await grants.listGrants()
      if (!existingGrants.some((grant) => grant.share === String(share.id)))
        await grants.createGrant({
          user: workerApp.adminUser.id,
          share: String(share.id),
          label: shareName,
          allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share']
        })

      await page.goto(`${workerApp.baseURL}/setup`)
      await expect(page.getByLabel(t['setup.setup_token'], { exact: true })).toBeVisible()
      await expect(page.getByText(t['setup.token_location'], { exact: true })).toBeVisible()

      await page.goto(`${workerApp.baseURL}/settings/security`)
      await page.getByRole('button', { name: t['app_password.new_app_password'], exact: true }).click()
      const appPassword = page.getByRole('dialog', { name: t['app_password.new_app_password'], exact: true })
      await expect(
        appPassword.getByRole('button', { name: t['app_password.new_app_password'], exact: true })
      ).toBeVisible()
      await expect(
        appPassword.getByRole('switch', { name: t['app_password.read_only_download_only_no'], exact: true })
      ).toBeVisible()
      await appPassword.getByRole('button', { name: t['common.cancel'], exact: true }).click()
      await page.getByRole('button', { name: t['totp.set_up_two_factor_authentication'], exact: true }).click()
      const factor = page.getByRole('dialog', { name: t['totp.set_up_two_factor_authentication'], exact: true })
      await expect(factor.getByRole('button', { name: t['totp.generate_key'], exact: true })).toBeVisible()
      await factor.getByRole('button', { name: t['common.cancel'], exact: true }).click()

      await page.goto(`${workerApp.baseURL}/settings/connections`)
      await expect(page.getByRole('heading', { name: 'WebDAV', exact: true })).toBeVisible()
      await expect(page.getByText(t['webdav.credentials_note'], { exact: true })).toBeVisible()
      await expect(page.getByText(t['webdav.rclone_crypt_warning'], { exact: true })).toBeVisible()

      await page.goto(`${workerApp.baseURL}/admin/shares`)
      await page
        .getByRole('button', { name: t['encryption.enable_title'].replace('{name}', shareName), exact: true })
        .click()
      const encryption = page.getByRole('alertdialog', {
        name: t['encryption.enable_title'].replace('{name}', shareName),
        exact: true
      })
      await expect(encryption.getByText(t['encryption.passphrase_warning'], { exact: true })).toBeVisible()
      await expect(encryption.getByText(t['encryption.verifier_note'], { exact: true })).toBeVisible()
      await expect(encryption.getByLabel(t['encryption.passphrase'], { exact: true })).toBeVisible()
      await encryption.getByRole('button', { name: t['common.cancel'], exact: true }).click()

      await page.goto(`${workerApp.baseURL}/admin/server`)
      await expect(page.getByText(t['server.homes_root_change_hint'], { exact: true })).toBeVisible()
      await expect(page.getByText(t['server.app_hosts_hint'], { exact: true })).toBeVisible()
      await expect(page.getByPlaceholder(t['settings.secret_is_write_only'], { exact: true })).toBeVisible()
      expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false)
    } finally {
      await context.close()
    }
  })
}
