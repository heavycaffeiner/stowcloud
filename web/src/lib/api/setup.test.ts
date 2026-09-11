// Mock branch of the
// first-run seam (createInitialAdmin). This is the ONE function that will
// need retargeting once the real /setup route lands; these tests pin down
// its current (mock) behavior so that change is visible in a diff.
//
// The mock branch is forced here rather than inherited from `web/.env`.
// `setup.ts` reads `VITE_API_MOCK` into a module-level const at import time,
// so these tests used to pass or fail depending on a file nobody edits with
// tests in mind: flipping `.env` to `0` to point the dev server at a real
// backend silently turned three "(mock)" tests into failing real-`fetch`
// calls. A test whose meaning depends on ambient configuration is not
// pinning anything down.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from './mock'
import type { createInitialAdmin as CreateInitialAdmin } from './setup'

let createInitialAdmin: typeof CreateInitialAdmin

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
      createInitialAdmin({ token: '   ', username: 'root', password: 'longenoughpw', app_hosts: ['localhost'], trusted_proxies: [] })
    ).rejects.toMatchObject({ code: 'setup.invalid_token' })
  })

  it('accepts a token and lets that exact account log in afterwards', async () => {
    await mockApi.logout()
    await createInitialAdmin({ token: 'DEV-TOKEN', username: 'root', password: 'longenoughpw', app_hosts: ['localhost'], trusted_proxies: [] })

    const result = await mockApi.login('root', 'longenoughpw')
    expect(result.required).toBeUndefined()
  })

  it('does not let a different password through for the created account', async () => {
    await mockApi.logout()
    await createInitialAdmin({ token: 'DEV-TOKEN', username: 'root2', password: 'longenoughpw', app_hosts: ['localhost'], trusted_proxies: [] })

    await expect(mockApi.login('root2', 'wrong-password')).rejects.toMatchObject({
      code: 'auth.invalid_credentials'
    })
  })
})

// Setup captures VITE_API_MOCK at module evaluation, so each branch must load
// a fresh module after selecting the transport.

describe('createInitialAdmin (HTTP)', () => {
  beforeEach(async () => {
    vi.stubEnv('VITE_API_MOCK', '0')
    vi.resetModules()
    ;({ createInitialAdmin } = await import('./setup'))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  it('preserves field findings when the server rejects setup validation', async () => {
    const findings = [
      { section: 'network', field: 'app_hosts', reason: 'host.invalid', blocking: true }
    ]
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        status: 422,
        ok: false,
        statusText: 'Unprocessable Entity',
        json: async () => ({ findings })
      })
    )

    await expect(
      createInitialAdmin({
        token: 'SETUP-TOKEN',
        username: 'root',
        password: 'longenoughpw',
        app_hosts: ['localhost'],
        trusted_proxies: []
      })
    ).rejects.toMatchObject({ name: 'SetupValidationError', findings })
  })

  it('reports a committed account separately when the optional first share failed', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        status: 200,
        ok: true,
        statusText: 'OK',
        json: async () => ({ warnings: [], share_failed: true })
      })
    )

    const result = await createInitialAdmin({
      token: 'SETUP-TOKEN',
      username: 'root',
      password: 'longenoughpw',
      app_hosts: ['localhost'],
      trusted_proxies: [],
      first_share: { name: 'Documents', host: '/srv/documents' }
    })
    expect(result).toEqual({ warnings: [], share_failed: true })
  })
})

