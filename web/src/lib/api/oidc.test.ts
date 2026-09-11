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
import { describe, expect, it, vi } from 'vitest'
import { oidcErrorMessage, startOidcLogin } from './oidc'

/** Every code the callback can actually put in `?oidc_error=`, taken from the
 *  handlers that emit one (`go/engine/lifecycle/oidc.go`). An
 *  expired flow and an unknown state are both `oidc.bad_state` there, so
 *  neither `oidc.expired` nor `oidc.already_linked` is in this list. The
 *  defect-15 discovery-time refusals (HS256-only, no usable client
 *  authentication method) answer the existing `oidc.provider_unavailable`
 *  rather than a code of their own.
 *  `auth.invalid_credentials` (a callback whose token failed verification,
 *  e.g. no `kid`) is a table B row too, not table A: it lands here via the
 *  same `?oidc_error=` redirect, never as a JSON envelope. */
const TABLE_B = [
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
  /** `window.location.href` is a real navigation under jsdom, so the
   * property is replaced with a plain writable one for the duration. */
  async function captureNavigation(run: () => Promise<void>): Promise<string> {
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
      await run()
    } finally {
      if (original) Object.defineProperty(window, 'location', original)
    }
    return href
  }

  it('validates the JSON response and navigates to its authorization URL', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ authorize_url: 'https://idp.example.test/authorize?state=abc' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        })
      )
    )
    await expect(
      captureNavigation(() => startOidcLogin())
    ).resolves.toBe('https://idp.example.test/authorize?state=abc')
  })

  it('does not navigate when the response has no valid authorization URL', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ authorize_url: 'javascript:alert(1)' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        })
      )
    )
    await expect(captureNavigation(() => startOidcLogin())).resolves.toBe('')
  })

  it('percent-encodes returnTo in the start request', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ authorize_url: 'https://idp.example.test/authorize' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      })
    )
    vi.stubGlobal('fetch', fetchMock)
    await captureNavigation(() => startOidcLogin('/b/Docs?sort=name&order=desc'))
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/auth/oidc/start?return_to=%2Fb%2FDocs%3Fsort%3Dname%26order%3Ddesc',
      expect.objectContaining({ credentials: 'include' })
    )
  })
})
