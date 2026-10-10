import * as fs from 'node:fs'
import * as path from 'node:path'
import type { Locator } from '@playwright/test'
import { test, expect } from '../fixtures'

const FOLDER = 'parent-alignment'

async function geometry(entry: Locator) {
  return entry.evaluate((element) => {
    const box = element.getBoundingClientRect()
    const icon = element.querySelector<SVGElement>('svg[width]')!.getBoundingClientRect()
    const name = element.querySelector<HTMLElement>('[title]')!.getBoundingClientRect()
    const style = getComputedStyle(element)
    return {
      x: box.x,
      y: box.y,
      width: box.width,
      height: box.height,
      iconX: icon.x - box.x,
      iconY: icon.y - box.y,
      nameX: name.x - box.x,
      nameY: name.y - box.y,
      background: style.backgroundColor,
      radius: style.borderRadius,
      font: style.font
    }
  })
}

for (const view of ['list', 'grid'] as const) {
  for (const density of ['compact', 'comfortable', 'spacious'] as const) {
    test(`${view} parent navigation matches folders at ${density} density`, async ({
      authedPage: page,
      filesystem,
      workerApp,
      grants
    }) => {
      const shares = await filesystem.listShares()
      const docs =
        shares.find((share) => share.name === 'docs') ?? (await filesystem.createShare('docs', workerApp.shareDir))
      const existing = await grants.listGrants()
      if (!existing.some((grant) => grant.share === String(docs.id))) {
        await grants.createGrant({
          user: workerApp.adminUser.id,
          share: String(docs.id),
          label: 'docs',
          allow: ['read', 'write', 'create', 'delete', 'download', 'rename', 'move', 'share']
        })
      }
      const folder = path.join(workerApp.shareDir, FOLDER)
      for (let index = 0; index < 9; index++) fs.mkdirSync(path.join(folder, `folder-${index}`), { recursive: true })
      fs.writeFileSync(path.join(folder, 'report.txt'), 'report\n')
      await page.addInitScript(
        ({ view, density }) => {
          localStorage.setItem('sc.view', view)
          localStorage.setItem('sc.density', density)
        },
        { view, density }
      )
      await page.goto(`${workerApp.baseURL}/b/docs/${FOLDER}`)
      const grid = page.getByRole('grid', { name: view === 'list' ? 'File list' : 'File grid', exact: true })
      const button = grid.getByRole('button', { name: 'Go to parent folder', exact: true })
      await button.waitFor()
      await grid.locator('[aria-selected]').last().waitFor()
      await page.evaluate(async () => {
        await document.fonts.ready
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
      })
      const parent = button.locator(
        view === 'list' ? 'xpath=ancestor::*[@role="row"]' : 'xpath=ancestor::*[@role="gridcell"]'
      )
      const folderEntry = grid.locator('[aria-selected]').filter({ has: page.locator('[title="folder-0"]') })
      await expect(folderEntry).toBeVisible()
      const [parentBox, folderBox] = await Promise.all([geometry(parent), geometry(folderEntry)])
      for (const property of ['width', 'height', 'iconX', 'iconY', 'nameX', 'nameY'] as const) {
        expect(Math.abs(parentBox[property] - folderBox[property]), property).toBeLessThanOrEqual(0.5)
      }
      expect(parentBox.background).toBe(folderBox.background)
      expect(parentBox.radius).toBe(folderBox.radius)
      expect(parentBox.font).toBe(folderBox.font)
      expect(await parent.getAttribute('aria-selected')).toBeNull()
      await expect(parent.getByRole('checkbox')).toHaveCount(0)
      await expect(parent.getByRole('button')).toHaveCount(1)
      if (view === 'list') {
        expect(parentBox.x).toBe(folderBox.x)
        expect(folderBox.y - parentBox.y).toBe(parentBox.height)
      } else {
        await expect(grid.getByRole('rowgroup', { name: 'Folders', exact: true }).getByRole('row')).not.toHaveCount(1)
      }

      await grid.focus()
      await page.keyboard.press('ArrowDown')
      await button.focus()
      await page.keyboard.press('Enter')
      await expect(page).toHaveURL(`${workerApp.baseURL}/b/docs`)
      await expect(page.getByRole('button', { name: 'Go to parent folder', exact: true })).toHaveCount(0)

      await page.goto(`${workerApp.baseURL}/b/docs/${FOLDER}/folder-0`)
      const emptyParent = page.getByRole('button', { name: 'Go to parent folder', exact: true })
      await expect(emptyParent).toBeVisible()
      await expect(page.getByText('This folder is empty', { exact: true })).toBeVisible()
      const emptyEntry = emptyParent.locator(
        view === 'list' ? 'xpath=ancestor::*[@role="row"]' : 'xpath=ancestor::*[@role="gridcell"]'
      )
      const box = await emptyEntry.boundingBox()
      if (!box) throw new Error('Parent navigation must remain visible in an empty folder')
      // The reserved checkbox area is part of the navigation target, not a selectable entry.
      await page.mouse.click(box.x + 12, box.y + box.height / 2)
      await expect(page).toHaveURL(`${workerApp.baseURL}/b/docs/${FOLDER}`)
    })
  }
}
