import { afterEach, describe, expect, it, vi } from 'vitest'
import { emergencyLogin } from './emergency'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('emergencyLogin', () => {
  it('sends a trimmed authenticator or recovery factor without weakening the password flow', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ status: 'ok' }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await emergencyLogin('root', 'correct horse battery staple', '  RECOVERY-CODE  ')

    expect(fetchMock).toHaveBeenCalledWith(
      '/emergency/api/login',
      expect.objectContaining({
        method: 'POST',
        credentials: 'include',
        body: JSON.stringify({
          username: 'root',
          password: 'correct horse battery staple',
          factor: 'RECOVERY-CODE'
        })
      })
    )
  })
})
