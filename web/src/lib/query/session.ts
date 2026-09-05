// The session, and the one decision the whole shell hangs off it.
import { createQuery, mutationOptions, queryOptions, type CreateQueryResult } from '@tanstack/svelte-query'
import { api, ApiError } from '../api/client'
import { fetchOidcConfig } from '../api/oidc'
import { setupRequired } from '../api/setup'
import type { SessionInfo } from '../api/types'
import { lock } from '../crypto/e2ee'
import { invalidateEncryptedShares } from '../crypto/encrypted-shares'
import { queryClient } from './client'
import { keys } from './keys'

/** What every screen that needs the signed-in account calls. One cache entry
 *  behind them all, so the tenth caller costs nothing. */
export function createSession(): CreateQueryResult<SessionInfo, Error> {
  return createQuery(() => sessionQuery())
}

/** `api.session()` also installs the CSRF token every write needs, so this is
 *  the query the rest of the app waits on before it can change anything. */
function sessionQuery() {
  return queryOptions({
    queryKey: keys.session(),
    queryFn: () => api.session(),
    staleTime: 5 * 60_000,
    // A refused session is an answer, not a fault: retrying it delays the
    // login screen by exactly as long as the retries take.
    retry: false
  })
}

/**
 * Whether this server has ever had an account.
 *
 * `GET /api/auth/session` answers the same `auth.required` 401 for "your
 * session expired" and "nobody has ever signed in here", so only a failed
 * session makes this worth asking; `enabled` is what keeps it off the common
 * path where the user is already signed in.
 */
export function setupRequiredQuery(enabled: boolean) {
  return queryOptions({
    queryKey: keys.setupRequired(),
    queryFn: setupRequired,
    enabled,
    staleTime: Infinity,
    retry: false
  })
}

export function oidcConfigQuery() {
  return queryOptions({ queryKey: keys.oidcConfig(), queryFn: fetchOidcConfig, staleTime: Infinity, retry: false })
}

export type AuthScreen = 'loading' | 'browser' | 'login' | 'first-run'

/**
 * Which screen the app is on.
 *
 * `setupPending` keeps the loading state up while the follow-up question is
 * still out: deciding "login" first and correcting to "first-run" a moment
 * later would flash a sign-in form at somebody who has no account to sign in
 * with.
 */
export function screenOf(state: {
  hasSession: boolean
  sessionFailed: boolean
  setupPending: boolean
  setupRequired: boolean
}): AuthScreen {
  if (state.hasSession) return 'browser'
  if (!state.sessionFailed) return 'loading'
  if (state.setupPending) return 'loading'
  return state.setupRequired ? 'first-run' : 'login'
}

export function loginMutation() {
  return mutationOptions({
    mutationFn: ({ username, password }: { username: string; password: string }) => api.login(username, password),
    // A TOTP challenge is a half-finished login: there is no session to read
    // yet and the caller stays on the form.
    onSuccess: (result) => {
      if (result.required === undefined) void queryClient.invalidateQueries({ queryKey: keys.session() })
    }
  })
}

export function loginTotpMutation() {
  return mutationOptions({
    mutationFn: ({ challenge, code }: { challenge: string; code: string }) => api.loginTotp(challenge, code),
    onSuccess: (result) => {
      if (result.required === undefined) void queryClient.invalidateQueries({ queryKey: keys.session() })
    }
  })
}

export function logoutMutation() {
  return mutationOptions({
    mutationFn: () => api.logout(),
    onSettled: () => {
      // Everything in the cache belonged to the account that just left, and so
      // does the unlocked share key: without dropping it the next account
      // signing in on this tab would encrypt under the previous one's key.
      queryClient.clear()
      lock()
      invalidateEncryptedShares()
    }
  })
}

/** True when a failed session query means "not signed in" rather than "the
 *  server could not be reached", which is a different screen. */
export function isUnauthenticated(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 401 || error.status === 404)
}
