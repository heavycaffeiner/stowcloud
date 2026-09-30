import { test, expect } from '../fixtures'
import { assertNoUnexpectedErrors } from '../helpers/ux-invariants'

// React can mount and remove an element inside one task, so an mdui element
// may run its first update already disconnected. That must not throw.
test('mdui elements removed in the task that added them raise no page error', async ({
  page,
  workerApp,
  artifacts
}) => {
  await page.goto(`${workerApp.baseURL}/login`, { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()

  await page.evaluate(() => {
    for (const make of [
      () => document.createElement('mdui-checkbox'),
      () => document.createElement('mdui-switch'),
      () => Object.assign(document.createElement('mdui-list-item'), { href: '#' })
    ]) {
      const element = make()
      document.body.append(element)
      element.remove()
    }
  })
  // The first update is a microtask; a frame later it has run.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(resolve)))

  await assertNoUnexpectedErrors(artifacts)
})
