// web/src/lib/api/oidc.test.ts: the two pieces of `oidc.ts` that are pure
// logic and easy to break silently.
//
// `oidcErrorMessage` is a translation of §5-2 table B of
// `docs/proposals/stowcloud-0-oidc-login.md`. A row that goes missing does not
// fail loudly: it falls through to the generic message, which is a plausible
// enough sentence that nobody notices the specific one is gone. So the table
// itself is what these assertions are about, not the wording of any one
// message.
//
// Assertions are structural for the same reason. The catalogue defaults to
// Korean and either language may be copy-edited, so pinning exact strings
// would make this a test of the copy rather than of the mapping.
import { describe, expect, it } from 'vitest'
import { oidcErrorMessage, startOidcLogin } from './oidc'

/** Every code the callback can actually put in `?oidc_error=`, taken from the
 *  handlers that emit one (`go/internal/httpapi/handler/oidc_flow.go`). An
 *  expired flow and an unknown state are both `oidc.bad_state` there, so
 *  neither `oidc.expired` nor `oidc.already_linked` is in this list. */
const TABLE_B = [
  'oidc.disabled',
  'oidc.bad_request',
  'oidc.bad_state',
  'oidc.not_linked',
  'oidc.provider_unavailable',
  'oidc.access_denied',
  'oidc.link_session_changed',
  'oidc.subject_already_linked'
]

describe('oidcErrorMessage', () => {
  it('has nothing to say when there is no code', () => {
    expect(oidcErrorMessage(null)).toBeNull()
    expect(oidcErrorMessage(undefined)).toBeNull()
    expect(oidcErrorMessage('')).toBeNull()
  })

  it('gives every table B code a message of its own', () => {
    const generic = oidcErrorMessage('something-that-is-not-a-code')
    for (const code of TABLE_B) {
      const msg = oidcErrorMessage(code)
      expect(msg, code).toBeTruthy()
      // Not the fallback: a row that quietly disappeared would show up here.
      expect(msg, code).not.toBe(generic)
      // Not the key either, which is what `t()` returns for a catalogue miss.
      expect(msg, code).not.toContain('oidc.')
    }
  })

  it('never echoes an unrecognised code back to the screen', () => {
    // The value is reflected off a query parameter anybody can write. Putting
    // it on screen verbatim would be a way to make this server display an
    // attacker's text.
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

describe('startOidcLogin', () => {
  /** `window.location.href = ...` is a real navigation under jsdom, so the
   *  property is replaced with a plain writable one for the duration. */
  function captureNavigation(run: () => void): string {
    const original = Object.getOwnPropertyDescriptor(window, 'location')
    let href = ''
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        get href() {
          return href
        },
        set href(v: string) {
          href = v
        }
      }
    })
    try {
      run()
    } finally {
      if (original) Object.defineProperty(window, 'location', original)
    }
    return href
  }

  it('navigates to the start route with no query when there is no returnTo', () => {
    expect(captureNavigation(() => startOidcLogin())).toBe('/api/v1/auth/oidc/start')
    expect(captureNavigation(() => startOidcLogin(null))).toBe('/api/v1/auth/oidc/start')
  })

  it('percent-encodes returnTo so a path with a query survives the round trip', () => {
    const href = captureNavigation(() => startOidcLogin('/b/Docs?sort=name&order=desc'))
    expect(href).toBe('/api/v1/auth/oidc/start?returnTo=%2Fb%2FDocs%3Fsort%3Dname%26order%3Ddesc')
  })
})
