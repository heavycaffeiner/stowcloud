// web/src/lib/api/auth.test.ts — mockApi's login/session/logout surface
// (DESIGN-AUTH.md §6.3), same pattern as mock.test.ts. Kept in its own file
// (rather than appended to mock.test.ts) so a logged-out state produced
// partway through never leaks into the unrelated fs tests there — vitest
// gives each test *file* its own module instance by default, so mock.ts's
// module-scoped auth state starts fresh here.
import { beforeEach, describe, expect, it } from 'vitest'
import { mockApi } from './mock'

describe('mockApi auth', () => {
  it('is logged in out of the box — existing dev workflows are unaffected', async () => {
    const s = await mockApi.session()
    expect(s.user.name).toBe('demo')
  })

  it('rejects an unknown credential pair', async () => {
    await expect(mockApi.login('nobody', 'wrong-password')).rejects.toMatchObject({
      code: 'auth.invalid_credentials'
    })
  })

  it('logs the demo user back in after a logout', async () => {
    await mockApi.logout()
    await expect(mockApi.session()).rejects.toMatchObject({ code: 'auth.required' })

    const result = await mockApi.login('demo', 'password12')
    expect(result.status).toBe('ok')
    const s = await mockApi.session()
    expect(s.user.name).toBe('demo')
  })

  describe('two-step (TOTP) login', () => {
    beforeEach(async () => {
      await mockApi.logout()
    })

    it('challenges instead of logging in directly', async () => {
      const result = await mockApi.login('totp-demo', 'password12')
      expect(result.status).toBe('totp_required')
    })

    it('rejects a wrong code without completing the login', async () => {
      const first = await mockApi.login('totp-demo', 'password12')
      if (first.status !== 'totp_required') throw new Error('expected a challenge')
      await expect(mockApi.loginTotp(first.challenge, '000000')).rejects.toMatchObject({
        code: 'auth.invalid_credentials'
      })
      await expect(mockApi.session()).rejects.toMatchObject({ code: 'auth.required' })
    })

    it('completes the login with the right code', async () => {
      const first = await mockApi.login('totp-demo', 'password12')
      if (first.status !== 'totp_required') throw new Error('expected a challenge')
      const second = await mockApi.loginTotp(first.challenge, '123456')
      expect(second.status).toBe('ok')
      const s = await mockApi.session()
      expect(s.user.name).toBe('demo')
    })
  })
})
