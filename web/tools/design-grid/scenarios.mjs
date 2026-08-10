// tools/design-grid/scenarios.mjs — the states no URL reaches.
//
// A dialog, a menu and a tray are where alignment goes wrong most often and
// where a route-only audit is blindest: navigating to /b/home renders none of
// them. Each scenario drives the real UI, then names the selector that proves
// it arrived. A scenario that cannot reach its target is a harness failure and
// fails the run, because an overlay that silently never opened would report
// zero violations and read as a pass.
//
// Everything here addresses controls through their accessible name, resolved
// from the same catalogue the app renders. That is what makes one definition
// serve both locales.

import { Buffer } from 'node:buffer'

const ROW = '.sc-row'
const OPEN_TIMEOUT = 5000

/** Opens the row-actions menu on the first file row and waits for it. */
async function openRowMenu(page) {
  const row = page.locator(ROW).nth(1)
  await row.waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
  await row.click({ button: 'right' })
  await page.locator('[role="menu"], .m3-menu').first().waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
}

/**
 * Picks an item out of the open menu by its accessible name.
 *
 * `role=button`, not `role=menuitem`: m3-svelte's MenuItem renders a plain
 * <button> and only the container carries `role="menu"`. Scoped to the menu so
 * a label that also exists on the toolbar cannot be matched instead.
 */
async function pickMenuItem(page, name) {
  const menu = page.locator('[role="menu"]').last()
  await menu.waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
  await menu.getByRole('button', { name, exact: true }).first().click()
}

/**
 * Selects `count` rows through their checkboxes.
 *
 * The cell is clicked, not the input: m3-svelte paints its own `.checkbox-box`
 * over the real input, so a click aimed at the input is intercepted. The cell
 * is what a person hits anyway.
 */
async function selectRows(page, t, count) {
  for (let i = 0; i < count; i += 1) {
    const row = page.locator(ROW).nth(i)
    await row.waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
    await row.locator('.sc-row__cell--select').click()
    await row
      .locator('input[type="checkbox"]')
      .waitFor({ state: 'attached', timeout: OPEN_TIMEOUT })
  }
  await page.locator('.sc-browse__selection-bar').waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
  void t
}

/**
 * Clicks a control by accessible name, reaching into the overflow menu when it
 * is not on the bar. The toolbar folds different controls away at different
 * widths, so a scenario that only knew the desktop layout would fail on mobile
 * for a reason that has nothing to do with the grid.
 */
async function clickAction(page, t, name) {
  const direct = page.getByRole('button', { name, exact: true }).first()
  if (await direct.isVisible().catch(() => false)) {
    await direct.click()
    return
  }
  await page.getByRole('button', { name: t('browse.more'), exact: true }).first().click()
  await pickMenuItem(page, name)
}

const DIALOG = '[role="dialog"], dialog[open]'

export const scenarios = {
  gridView: {
    ready: '.sc-file-grid',
    async run(page, t) {
      await clickAction(page, t, t('browse.grid_view'))
    }
  },

  selectAndOpenDetails: {
    ready: '.sc-details',
    async run(page, t) {
      await selectRows(page, t, 1)
      // Clicked rather than seeded through localStorage: a stored preference
      // would survive into the next page audited in this context, and the
      // details panel would then appear where the matrix did not ask for it.
      await clickAction(page, t, t('details.show'))
    }
  },

  rowActionsMenu: {
    ready: '[role="menu"], .m3-menu',
    async run(page) {
      await openRowMenu(page)
    }
  },

  renameDialog: {
    ready: DIALOG,
    async run(page, t) {
      await openRowMenu(page)
      await pickMenuItem(page, t('common.rename'))
    }
  },

  deleteDialog: {
    ready: DIALOG,
    async run(page, t) {
      await openRowMenu(page)
      await pickMenuItem(page, t('common.delete'))
    }
  },

  shareManageDialog: {
    ready: DIALOG,
    async run(page, t) {
      await openRowMenu(page)
      await pickMenuItem(page, t('browse.manage_share_links'))
    }
  },

  destinationPicker: {
    ready: DIALOG,
    async run(page, t) {
      await openRowMenu(page)
      await pickMenuItem(page, t('dest.move_or_copy'))
    }
  },

  newFolderDialog: {
    ready: DIALOG,
    async run(page, t) {
      await clickAction(page, t, t('common.new_folder'))
    }
  },

  previewDialog: {
    ready: DIALOG,
    async run(page) {
      // /b/home/Photos, so the first row is a jpg with a preview.
      const row = page.locator(ROW).first()
      await row.waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
      await row.dblclick()
    }
  },

  uploadTray: {
    ready: '.sc-upload-tray',
    async run(page) {
      await page.locator('input[type="file"]').first().setInputFiles({
        name: 'design-grid-probe.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from('probe')
      })
    }
  },

  // No jobTray scenario. The job tray renders on
  // `jobTray.items.length > 0 || jobTray.stale`, and every mock job is already
  // terminal on its first poll by design (`makeMockJob` in mock.ts: "there is
  // no mock filesystem large enough for a fake delay to mean anything"). So
  // the tray never gets a live job to show, and covering it would mean
  // changing the mock, which is app code. Its geometry is not audited; see the
  // note the driver prints at the end of every run.

  // Every snackbar on the browse page is raised from an error path -- a
  // successful rename says nothing -- so this scenario has to fail on purpose.
  // Renaming the second row to the first row's own name is a `409 fs.conflict`
  // from the mock, which `doRename` catches straight into `snackbarMsg`. The
  // name is read from the DOM rather than hardcoded so the collation order of
  // the seed directory cannot break it.
  //
  // `.holder` is m3-svelte's snackbar host (containers/Snackbar.svelte); the
  // app has no class by that name. The message auto-dismisses after 4s, so
  // this is the one scenario with a clock on it. Arriving late fails the
  // readiness wait rather than measuring a page with no snackbar on it.
  snackbar: {
    ready: '.holder',
    async run(page, t) {
      const taken = (await page.locator(ROW).first().locator('.sc-filename').innerText()).trim()
      await openRowMenu(page)
      await pickMenuItem(page, t('common.rename'))
      const dialog = page.locator(DIALOG).first()
      await dialog.waitFor({ state: 'visible', timeout: OPEN_TIMEOUT })
      await dialog.locator('input').first().fill(taken)
      // The dialog confirms with OK, not with the verb that opened it.
      await dialog.getByRole('button', { name: t('common.ok'), exact: true }).first().click()
    }
  }
}
