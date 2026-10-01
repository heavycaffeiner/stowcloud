// A code that loses its row falls through to the generic message, which reads
// plausibly enough that nobody notices. So these pin the table, not the copy.
import { describe, expect, it } from 'vitest'
import { oidcErrorMessage } from '../../../src/features/auth/oidc-error'

/** Every code the server's callback can put in `?oidc_error=`. An expired flow
 *  and an unknown state are both `oidc.bad_state`; a token that failed
 *  verification arrives as `auth.invalid_credentials` through the same redirect. */
const CALLBACK_CODES = [
  'oidc.disabled',
  'oidc.bad_request',
  'oidc.bad_state',
  'oidc.not_linked',
  'oidc.provider_unavailable',
  'oidc.access_denied',
  'oidc.link_session_changed',
  'oidc.subject_already_linked',
  'auth.invalid_credentials'
]

describe('oidcErrorMessage', () => {
  it('has nothing to say when there is no code', () => {
    expect(oidcErrorMessage(null)).toBeNull()
    expect(oidcErrorMessage(undefined)).toBeNull()
    expect(oidcErrorMessage('')).toBeNull()
  })

  it('gives every callback code a message of its own', () => {
    const generic = oidcErrorMessage('something-that-is-not-a-code')
    for (const code of CALLBACK_CODES) {
      const msg = oidcErrorMessage(code)
      expect(msg, code).toBeTruthy()
      expect(msg, code).not.toBe(generic)
      // `t()` returns the key itself for a catalogue miss.
      expect(msg, code).not.toContain('oidc.')
    }
  })

  it('never echoes an unrecognised code back to the screen', () => {
    const hostile = '<img src=x onerror=alert(1)>'
    const msg = oidcErrorMessage(hostile)
    expect(msg).toBeTruthy()
    expect(msg).not.toContain(hostile)
    expect(msg).toBe(oidcErrorMessage('some-other-unknown-code'))
  })

  it('answers internal failures without leaking that they were internal', () => {
    expect(oidcErrorMessage('internal')).toBe(oidcErrorMessage('unknown'))
  })
})
