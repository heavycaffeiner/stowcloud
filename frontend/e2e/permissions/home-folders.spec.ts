import * as fs from 'node:fs'
import * as path from 'node:path'
import { test, expect } from '../fixtures'

test('new accounts receive Home without requiring a shared folder permission', async ({
  authedPage: page,
  workerApp,
  api,
  filesystem,
  namespace
}) => {
  const root = path.join(workerApp.dataDir, 'homes')
  const name = namespace('home-user')
  expect((await api.patch('/api/v1/admin/settings/homes', { enabled: true })).status).toBe(200)
  try {
    await filesystem.createShare(namespace('shared'), workerApp.shareDir)
    await page.goto(`${workerApp.baseURL}/admin/users`)
    await page.getByRole('button', { name: 'Add user', exact: true }).click()
    const create = page.getByRole('alertdialog', { name: 'Add user', exact: true })
    await expect(create.getByText('A private Home folder is provided automatically.', { exact: false })).toBeVisible()
    await create.getByLabel('Username', { exact: true }).fill(name)
    await create.getByLabel('Password', { exact: true }).fill('Password123!')
    await create.getByRole('button', { name: 'Add', exact: true }).click()
    await expect(create).toBeHidden()
    await expect(page.getByRole('alertdialog')).toHaveCount(0)
    expect(fs.statSync(path.join(root, name)).isDirectory()).toBe(true)
    await page.getByRole('button', { name: 'Add shared folder permissions', exact: true }).click()
    const grants = page.getByRole('alertdialog', { name: `Folders visible to ${name}` })
    await expect(grants.getByText('Personal · automatic', { exact: true })).toBeVisible()
    await expect(grants.getByRole('button', { name: 'Retry Home preparation' })).toHaveCount(0)
    await grants.getByRole('button', { name: 'Add folder', exact: true }).click()
    const add = page.getByRole('alertdialog', { name: 'Add folder', exact: true })
    const options = await add.getByRole('combobox', { name: 'Share', exact: true }).locator('option').allTextContents()
    expect(options).not.toContain('Home')
    await add.getByRole('button', { name: 'Cancel', exact: true }).click()
    await grants.getByRole('button', { name: 'Close', exact: true }).click()
    await page.goto(`${workerApp.baseURL}/settings/connections`)
    await expect(page.getByText('SMB is off on the server', { exact: true }).first()).toBeVisible()
    await expect(page.getByText('Personal space', { exact: true })).toBeVisible()
  } finally {
    await api.patch('/api/v1/admin/settings/homes', { enabled: false })
  }
})

test('a failed Home preparation is visible and can be retried without recreating the account', async ({
  authedPage: page,
  workerApp,
  api,
  namespace
}) => {
  const root = path.join(workerApp.dataDir, 'homes')
  const name = namespace('pending-home')
  expect((await api.patch('/api/v1/admin/settings/homes', { enabled: true })).status).toBe(200)
  const blocked = path.join(root, name)
  fs.writeFileSync(blocked, 'a file blocks this directory')
  try {
    const user = await api.post<{ id: string; home: { ready: boolean } }>('/api/v1/admin/users', {
      login: name,
      password: 'Password123!'
    })
    expect(user.status).toBe(201)
    expect(user.data.home.ready).toBe(false)
    await page.goto(`${workerApp.baseURL}/admin/users`)
    await page.getByRole('button', { name: `Manage folders visible to ${name}` }).click()
    const grants = page.getByRole('alertdialog', { name: `Folders visible to ${name}` })
    const retry = grants.getByRole('button', { name: 'Retry Home preparation' })
    await expect(retry).toBeVisible()
    fs.unlinkSync(blocked)
    await retry.click()
    await expect(retry).toBeHidden()
    expect(fs.statSync(blocked).isDirectory()).toBe(true)
    const users = await api.get<{ id: string; name: string; home: { ready: boolean } }[]>('/api/v1/admin/users')
    expect(users.data.find((entry) => entry.id === user.data.id)?.home.ready).toBe(true)
  } finally {
    await api.patch('/api/v1/admin/settings/homes', { enabled: false })
  }
})
