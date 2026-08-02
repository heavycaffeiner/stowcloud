// web/src/lib/state/auth.svelte.ts — "which screen is the app showing right
// now": the file browser, the login screen, or the first-run admin-bootstrap
// screen (DESIGN-AUTH.md §8). Svelte 5 runes, no store library, same
// convention as ui.svelte.ts.
//
// Deliberately imports nothing from api/client.ts (mock.ts/http.ts
// included): http.ts's request() calls `noteUnauthorized` below on every
// 401 (see that file), so if this module imported client.ts back we'd have
// a cycle (client.ts -> http.ts -> auth.svelte.ts -> client.ts). The glue
// that needs both sides (calling `api.session()` and updating this state)
// lives in auth-bootstrap.ts instead.
import { ApiError, type SessionInfo } from '../api/types'

export type AuthScreen = 'loading' | 'browser' | 'login' | 'first-run'

export const authState = $state<{ screen: AuthScreen; session: SessionInfo | null }>({
  screen: 'loading',
  session: null
})

export function setAuthenticated(session: SessionInfo): void {
  authState.session = session
  authState.screen = 'browser'
}

/**
 * Logged out. Not first-run — `GET /api/auth/session` answers a plain
 * `auth.required` 401 whether or not the server has ever had an account, so
 * the error alone cannot tell the two apart. `bootstrapAuth` asks
 * `GET /api/setup` for that and calls [`setFirstRun`] instead.
 */
export function setAnonymous(): void {
  authState.session = null
  authState.screen = 'login'
}

/** No account has ever existed here: show the create-administrator screen. */
export function setFirstRun(): void {
  authState.session = null
  authState.screen = 'first-run'
}

/**
 * Task "make a 401 mean something": the one place a 401 from any API call —
 * made anywhere in the app, after the initial bootstrap has already decided
 * a screen — turns into "show the login screen" instead of an inline error
 * string bubbling up into whatever was rendering. Idempotent: if we're
 * already showing login/first-run, this is a no-op.
 *
 * Always the login screen, never first-run: this fires mid-session, and a
 * session can only exist on a server that already has an account. Sending
 * someone whose session merely expired to a create-administrator form would
 * be both wrong and alarming.
 */
export function noteUnauthorized(): void {
  if (authState.screen === 'browser' || authState.screen === 'loading') {
    setAnonymous()
  }
}
