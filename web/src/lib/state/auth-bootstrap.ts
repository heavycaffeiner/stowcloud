// web/src/lib/state/auth-bootstrap.ts — glue between the api layer
// (api/client.ts) and the reactive screen state (state/auth.svelte.ts). Kept
// as a separate module (rather than folded into auth.svelte.ts) purely to
// avoid an import cycle — see that file's header comment for why.
import { api } from '../api/client'
import { setupRequired } from '../api/setup'
import { setAnonymous, setAuthenticated, setFirstRun } from './auth.svelte'
import { events } from './events'

/** Session bootstrapping: decides the initial screen on app load.
 *
 *  `GET /api/auth/session` answers the same `auth.required` 401 for "your
 *  session expired" and "this server has never had an account", so the error
 *  cannot distinguish them. Only on failure do we ask `GET /api/setup`, which
 *  reports exactly that one bit — no extra request on the common path where
 *  the user is already logged in.
 *
 *  Called once from the `(app)` route group's layout. */
export async function bootstrapAuth(): Promise<void> {
  try {
    const session = await api.session()
    setAuthenticated(session)
    // `events_ws` (`GET /api/events`) requires the same session cookie —
    // safe to open right away rather than waiting for the first directory
    // `subscribe()` (`state/browse.svelte.ts`), which is idempotent with
    // that call anyway (`EventsHub.ensureConnected`'s whole point).
    events.ensureConnected()
    return
  } catch {
    /* not authenticated — fall through to decide *which* screen */
  }
  if (await setupRequired()) {
    setFirstRun()
  } else {
    setAnonymous()
  }
}

/**
 * Called after `api.login()` / `api.loginTotp()` resolves `{status:'ok'}`
 * (and after the first-run screen's admin creation + auto-login). That
 * response only carries a minimal user object
 * (`AuthUser = {id, name}`) — this re-fetches the full session (roots,
 * csrf token, limits, features) the rest of the app expects, and flips the
 * screen to the file browser.
 */
export async function completeLogin(): Promise<void> {
  const session = await api.session()
  setAuthenticated(session)
  events.ensureConnected()
}
