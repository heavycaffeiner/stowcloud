// web/src/lib/api/setup.ts —'s one-time admin bootstrap.
// Standalone module, same reasoning as share.ts: it does NOT import
// ./client, ./mock, or ./http, so the not-yet-authenticated bundle (login +
// first-run screens) never pulls in the full fs mock or the rest of the
// authenticated app surface.
//
// The real backend for this is being written by another agent against the
// design doc *right now* — `POST /api/setup {token, username, password}`
// below is a best-effort guess at the eventual contract, not a read of
// working code. `createInitialAdmin` is THE seam: it is the only function in
// the entire frontend that knows this request shape, so retargeting it once
// the real route lands (different path, method, or field names) is a
// one-line change here — nothing else in the app needs to know.
import { t } from '../i18n'
import { ApiError, type ApiErrorBody } from './types'

export interface SetupCreateAdminReq {
  token: string
  username: string
  password: string
}

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api'

// Mock-only bridge to mock.ts's login(): sessionStorage is used instead of a
// direct import so this module stays standalone (see header comment) while
// still letting a dev-mode-created admin log back in with the exact
// credentials just typed into the first-run form. Keep this key in sync with
// mock.ts's MOCK_SETUP_ADMIN_KEY (duplicated by convention, not by import,
// for the same reason).
const MOCK_SETUP_ADMIN_KEY = 'sc.mock.setup_admin'

async function mockCreateAdmin(req: SetupCreateAdminReq): Promise<void> {
  await new Promise((r) => setTimeout(r, 200))
  if (!req.token.trim()) {
    throw new ApiError(401, { code: 'setup.invalid_token', message: t('setup.invalid_setup_token') })
  }
  try {
    sessionStorage.setItem(MOCK_SETUP_ADMIN_KEY, JSON.stringify({ username: req.username, password: req.password }))
  } catch {
    /* private browsing etc. — the mock auto-login after setup just won't work; non-fatal */
  }
}

async function httpCreateAdmin(req: SetupCreateAdminReq): Promise<void> {
  const res = await fetch(`${BASE}/setup`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    credentials: 'include',
    body: JSON.stringify(req)
  })
  if (res.ok || res.status === 204) return
  const body = await res.json().catch(() => ({}))
  const err = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
  throw new ApiError(res.status, err)
}

/** THE SEAM — see file header. */
export function createInitialAdmin(req: SetupCreateAdminReq): Promise<void> {
  return IS_MOCK ? mockCreateAdmin(req) : httpCreateAdmin(req)
}

/**
 * `GET /api/setup` → `{"required": bool}`. Unauthenticated and deliberately
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
    const res = await fetch(`${BASE}/setup`, {
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
