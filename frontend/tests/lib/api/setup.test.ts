// The first-run seam (createInitialAdmin): how it reads the server's answers.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createInitialAdmin } from '../../../src/lib/api/setup'

describe('createInitialAdmin', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('preserves field findings when the server rejects setup validation', async () => {
    const findings = [{ section: 'network', field: 'app_hosts', reason: 'host.invalid', blocking: true }]
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
