// The first-run form.
// Standalone module, same reasoning as share.ts: it does NOT import
// ./client, ./mock, or ./http, so the not-yet-authenticated bundle (login +
// first-run screens) never pulls in the full fs mock or the rest of the
// authenticated app surface.
//
// `createInitialAdmin` is THE seam: it is the only function in the entire
// frontend that knows this request shape.
import { t } from '../i18n'
import { ApiError, type ApiErrorBody, type SetupFinding } from './types'

export interface SetupCreateAdminReq {
  token: string
  username: string
  password: string
  /** The names this server will answer for. Required: until one is saved the
   *  host guard is in its first-boot mode, admitting the local network on the
   *  strength of the peer address alone. */
  app_hosts: string[]
  /** CIDR ranges whose forwarded headers are believed. Empty trusts none. */
  trusted_proxies: string[]
  /** host:port the listener moves to. Empty leaves it where it is. */
  bind?: string
  /** The folder to start serving. Omitted lands on the empty home, which
   *  offers the same thing one click later. */
  first_share?: { name: string; host: string }
}

/** What setup noticed and did not refuse over. The one that matters is a host
 *  list that does not contain the address the operator is browsing from: it is
 *  correct behind a proxy and a lockout otherwise, and no rule can tell the
 *  two apart.
 *
 *  Declared in types.ts, where the contract check reads it against the Go
 *  struct that answers it. Re-exported here because this module is the seam
 *  every setup caller imports from.
 */
export type { SetupFinding } from './types'

export interface SetupResult {
  warnings: SetupFinding[]
  /** True when the listener could not move to the address that was asked
   *  for. The old address is still serving, which is why this is a field
   *  rather than a failure. */
  bind_failed?: boolean
}

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api/v1'

// Mock-only bridge to mock.ts's login(): sessionStorage is used instead of a
// direct import so this module stays standalone (see header comment) while
// still letting a dev-mode-created admin log back in with the exact
// credentials just typed into the first-run form. Keep this key in sync with
// mock.ts's MOCK_SETUP_ADMIN_KEY (duplicated by convention, not by import,
// for the same reason).
const MOCK_SETUP_ADMIN_KEY = 'sc.mock.setup_admin'

async function mockCreateAdmin(req: SetupCreateAdminReq): Promise<SetupResult> {
  await new Promise((r) => setTimeout(r, 200))
  if (!req.token.trim()) {
    throw new ApiError(401, { code: 'setup.invalid_token', message: t('setup.invalid_setup_token') })
  }
  try {
    sessionStorage.setItem(MOCK_SETUP_ADMIN_KEY, JSON.stringify({ username: req.username, password: req.password }))
  } catch {
    /* private browsing etc. — the mock auto-login after setup just won't work; non-fatal */
  }
  // The one warning a browser can work out for itself, which is also the one
  // that matters: the list does not name where this page is being read from.
  const self = location.hostname
  if (req.app_hosts.length > 0 && !req.app_hosts.some((h) => h.toLowerCase() === self.toLowerCase())) {
    return {
      warnings: [
        { section: 'network', field: 'app_hosts', reason: 'settings.would_lock_you_out', args: { host: self }, blocking: false }
      ]
    }
  }
  return { warnings: [] }
}

async function httpCreateAdmin(req: SetupCreateAdminReq): Promise<SetupResult> {
  const res = await fetch(`${BASE}/system/setup`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    credentials: 'include',
    body: JSON.stringify(req)
  })
  if (res.status === 204) return { warnings: [] }
  const body = await res.json().catch(() => ({}))
  if (res.ok) {
    const done = body as { warnings?: SetupFinding[]; bind_failed?: boolean }
    // `settings.check_passed` is what the checker emits when it found nothing
    // to say: it is the absence of a warning, not one. Counted as a warning it
    // stops this screen on every successful setup, and the person has to press
    // the button a second time to get past a panel reporting that all is well.
    const warnings = (done.warnings ?? []).filter((w) => w.reason !== 'settings.check_passed')
    return { warnings, bind_failed: done.bind_failed }
  }
  const err = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
  throw new ApiError(res.status, err)
}

/** THE SEAM — see file header. */
export function createInitialAdmin(req: SetupCreateAdminReq): Promise<SetupResult> {
  return IS_MOCK ? mockCreateAdmin(req) : httpCreateAdmin(req)
}

/**
 * `GET /api/v1/system/setup` → `{"required": bool}`. Unauthenticated and deliberately
 * one field: it says only whether an account exists, which a junk `POST`
 * already reveals by answering `410` rather than `403`. It goes false forever
 * once the first account is created.
 *
 * This is how the app tells "nobody has ever logged in here" apart from "your
 * session expired" — the two produce the same `401` on `GET /api/auth/session`
 * and want completely different screens.
 *
 * Never throws: if this call fails we cannot conclude the server needs
 * setting up, and guessing wrong sends a normal user to a create-admin form.
 * Failure means `false`, which lands on the login screen — and that screen
 * carries a manual `/setup` link for the case where we guessed wrong.
 */
export async function setupRequired(): Promise<boolean> {
  if (IS_MOCK) return false
  try {
    const res = await fetch(`${BASE}/system/setup`, {
      method: 'GET',
      headers: { Accept: 'application/json' },
      credentials: 'include'
    })
    if (!res.ok) return false
    const body: unknown = await res.json()
    return (body as { required?: unknown }).required === true
  } catch {
    return false
  }
}
