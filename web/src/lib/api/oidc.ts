// web/src/lib/api/oidc.ts: the OIDC surface that has to work before anyone
// is logged in. Standalone, the same reasoning as setup.ts: it does NOT
// import ./client, ./mock or ./http, so the unauthenticated bundle (the login
// screen) never pulls in the full fs mock or the rest of the authenticated
// app just to decide whether to draw a button.
//
// The authenticated half of OIDC (linking, unlinking, the admin routes) does
// go through the normal api client, because it needs the CSRF token and the
// session that client already manages.
import { t } from '../i18n'
import type { OidcConfig } from './types'

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api'

/** What the mock backend answers for `GET /api/auth/oidc/config`.
 *
 *  Enabled, so both screens render their single-sign-on surface under
 *  `VITE_API_MOCK=1` and can be worked on without a server. There is no
 *  identity provider behind it: `mock.ts::oidcLinkStart` refuses with
 *  `oidc.provider_unavailable` rather than inventing an authorize URL that
 *  would navigate the browser out of the app. */
const MOCK_CONFIG: OidcConfig = { enabled: true, display_name: 'Mock IdP' }

/**
 * `GET /api/auth/oidc/config`, unauthenticated by necessity: the login
 * screen has to decide whether to draw the button before anybody has a
 * credential.
 *
 * Never throws, for the same reason `setupRequired` does not: a failure here
 * cannot be told apart from "this deployment has no IdP", and the safe reading
 * of both is "no button". The password form is always there either way, so
 * guessing wrong costs a login method, not the login screen.
 */
export async function fetchOidcConfig(): Promise<OidcConfig> {
  if (IS_MOCK) return MOCK_CONFIG
  try {
    const res = await fetch(`${BASE}/auth/oidc/config`, {
      method: 'GET',
      headers: { Accept: 'application/json' },
      credentials: 'include'
    })
    if (!res.ok) return { enabled: false, display_name: '' }
    const body: unknown = await res.json()
    const o = body as { enabled?: unknown; display_name?: unknown }
    if (o.enabled !== true) return { enabled: false, display_name: '' }
    return { enabled: true, display_name: typeof o.display_name === 'string' ? o.display_name : '' }
  } catch {
    return { enabled: false, display_name: '' }
  }
}

/**
 * Hands the browser to `GET /api/auth/oidc/start`, which answers a `302` to
 * the provider.
 *
 * A full navigation, never `fetch`: an XHR follows the redirect in the
 * background and the browser never actually goes anywhere, so the person never
 * sees the IdP's own login page. That page is the entire point of the flow.
 *
 * `returnTo` is passed through untouched. The server validates it again with
 * the same rules this app's `safeReturnTo` applies plus one it cannot skip
 * (every byte printable ASCII, since the value ends up in a `Location`
 * header), and quietly substitutes its default when the value fails.
 */
export function startOidcLogin(returnTo?: string | null): void {
  const q = returnTo ? `?returnTo=${encodeURIComponent(returnTo)}` : ''
  window.location.href = `${BASE}/auth/oidc/start${q}`
}

/**
 * §5-2 table B: the callback never answers with JSON, because a person
 * arrives at it in a browser. It redirects to `/login` or `/settings/security`
 * with the symbolic code in `?oidc_error=`, and this turns that code into a
 * sentence.
 *
 * An unrecognised code falls back to the generic failure rather than being
 * rendered as-is. The value is reflected off a query parameter anybody can
 * write, so putting it on screen verbatim would be a way to make this server
 * display an attacker's text.
 */
export function oidcErrorMessage(code: string | null | undefined): string | null {
  if (!code) return null
  switch (code) {
    case 'oidc.disabled':
      return t('oidc.single_sign_not_configured')
    case 'oidc.bad_request':
      return t('oidc.sign_request_incomplete_try_again')
    case 'oidc.bad_state':
      // Deliberately one message for "unknown state" and "the flow cookie is
      // missing or does not match" (§4.3.1). The second is what stops somebody
      // delivering a legitimate callback URL to another person's browser, and
      // that person should be told to start again, not told which check caught
      // it.
      return t('oidc.sign_could_not_verified_start')
    case 'oidc.expired':
      return t('oidc.sign_took_long_try_again')
    case 'oidc.not_linked':
      // Covers "no local account has this identity" *and* "the account is
      // disabled". §5-2 gives them the same code on purpose, the same account
      // enumeration defence as everywhere else in this product; the audit log
      // is where the two are told apart.
      return t('oidc.account_not_connected_single_sign')
    case 'oidc.provider_unavailable':
      return t('oidc.could_not_reach_identity_provider')
    case 'oidc.access_denied':
      return t('oidc.sign_cancelled_identity_provider')
    case 'oidc.link_session_changed':
      return t('oidc.you_signed_out_or_switched')
    case 'oidc.subject_already_linked':
      return t('oidc.identity_already_connected_another_account')
    case 'oidc.already_linked':
      return t('oidc.account_already_has_connected_identity')
    default:
      return t('oidc.single_sign_did_not_complete')
  }
}
