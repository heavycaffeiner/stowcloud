// web/src/lib/api/recovery-codes.test.ts — mockApi's recovery-code surface:
// the remaining-count read, and the reissue that must
// invalidate every old code. Kept in its own file for the same reason
// auth.test.ts is: vitest gives each test file its own module instance, so
// mock.ts's module-scoped `mockAuthState` starts fresh here rather than
// carrying over whatever another file's TOTP enrollment left it in.
import { describe, expect, it } from 'vitest'
import { mockApi } from './mock'

async function enrollTotp(): Promise<{ recovery_codes: string[] }> {
  const setup = await mockApi.totpSetup()
  return mockApi.totpEnroll('password12', setup.secret, '123456')
}

describe('mockApi recovery codes', () => {
  it('is zero while TOTP is off', async () => {
    // `totpDisable` is idempotent regardless of the file's execution order
    // relative to the other tests below.
    await mockApi.totpDisable('password12')
    const res = await mockApi.recoveryCodesRemaining()
    expect(res.remaining).toBe(0)
  })

  it('is ten immediately after enrollment', async () => {
    await enrollTotp()
    const res = await mockApi.recoveryCodesRemaining()
    expect(res.remaining).toBe(10)
  })

  it('reissue is refused when TOTP is not enabled', async () => {
    await mockApi.totpDisable('password12')
    await expect(mockApi.reissueRecoveryCodes('password12')).rejects.toMatchObject({
      code: 'auth.totp_not_enabled'
    })
  })

  it('reissue requires the correct password, and a wrong one changes nothing', async () => {
    await enrollTotp()
    await expect(mockApi.reissueRecoveryCodes('not-the-password')).rejects.toMatchObject({
      code: 'auth.invalid_credentials'
    })
    const res = await mockApi.recoveryCodesRemaining()
    expect(res.remaining).toBe(10)
  })

  // The regression this whole feature exists to prevent: a reissue that adds
  // to the set instead of replacing it, or that returns the same 10 codes
  // again, would leave the "old list is now worthless" promise the settings
  // UI makes to the user false.
  it('reissue mints a disjoint set of 10 -- the count stays at 10, not 20', async () => {
    const enrolled = await enrollTotp()
    const reissued = await mockApi.reissueRecoveryCodes('password12')
    expect(reissued.recovery_codes).toHaveLength(10)
    const overlap = reissued.recovery_codes.filter((c) => enrolled.recovery_codes.includes(c))
    expect(overlap).toHaveLength(0)
    const res = await mockApi.recoveryCodesRemaining()
    expect(res.remaining).toBe(10)
  })
})
