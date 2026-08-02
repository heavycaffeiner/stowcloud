// web/src/lib/api/setup.test.ts — mock branch of the
// first-run seam (createInitialAdmin). This is the ONE function that will
// need retargeting once the real /setup route lands — these tests pin down
// its current (mock) behavior so that change is visible in a diff.
//
// The mock branch is forced here rather than inherited from `web/.env`.
// `setup.ts` reads `VITE_API_MOCK` into a module-level const at import time,
// so these tests used to pass or fail depending on a file nobody edits with
// tests in mind: flipping `.env` to `0` to point the dev server at a real
// backend silently turned three "(mock)" tests into failing real-`fetch`
// calls. A test whose meaning depends on ambient configuration is not
// pinning anything down.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from './mock'

let createInitialAdmin: typeof import('./setup').createInitialAdmin

describe('createInitialAdmin (mock)', () => {
  beforeEach(async () => {
    sessionStorage.clear()
    vi.stubEnv('VITE_API_MOCK', '1')
    // The const is captured at module scope, so the stub only takes effect on
    // a fresh module instance.
    vi.resetModules()
    ;({ createInitialAdmin } = await import('./setup'))
  })

  it('rejects a blank installation token', async () => {
    await expect(
      createInitialAdmin({ token: '   ', username: 'root', password: 'longenoughpw' })
    ).rejects.toMatchObject({ code: 'setup.invalid_token' })
  })

  it('accepts a token and lets that exact account log in afterwards', async () => {
    await mockApi.logout()
    await createInitialAdmin({ token: 'DEV-TOKEN', username: 'root', password: 'longenoughpw' })

    const result = await mockApi.login('root', 'longenoughpw')
    expect(result.status).toBe('ok')
  })

  it('does not let a different password through for the created account', async () => {
    await mockApi.logout()
    await createInitialAdmin({ token: 'DEV-TOKEN', username: 'root2', password: 'longenoughpw' })

    await expect(mockApi.login('root2', 'wrong-password')).rejects.toMatchObject({
      code: 'auth.invalid_credentials'
    })
  })
})
