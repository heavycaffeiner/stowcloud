// The sentence for a failed single sign-on. The callback redirects with a
// symbolic code in `?oidc_error=` because a person arrives there in a browser.
import { t } from '../../lib/i18n'

/** An unknown code gets the generic failure: the value comes from a query
 *  parameter anybody can write, so showing it verbatim would let an attacker
 *  put text on this server's screen. */
export function oidcErrorMessage(code: string | null | undefined): string | null {
  if (!code) return null
  switch (code) {
    case 'oidc.disabled':
      return t('oidc.single_sign_not_configured')
    case 'oidc.bad_request':
      return t('oidc.sign_request_incomplete_try_again')
    case 'oidc.bad_state':
    // A token that came back and failed verification reads the same as an
    // unknown state: what came back could not be trusted.
    case 'auth.invalid_credentials':
      return t('oidc.sign_could_not_verified_start')
    case 'oidc.not_linked':
      // Also a disabled account, so the screen does not reveal which.
      return t('oidc.account_not_connected_single_sign')
    case 'oidc.provider_unavailable':
      return t('oidc.could_not_reach_identity_provider')
    case 'oidc.access_denied':
      return t('oidc.sign_cancelled_identity_provider')
    case 'oidc.link_session_changed':
      return t('oidc.you_signed_out_or_switched')
    case 'oidc.subject_already_linked':
      return t('oidc.identity_already_connected_another_account')
    default:
      return t('oidc.single_sign_did_not_complete')
  }
}
