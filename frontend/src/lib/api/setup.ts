// The first-run form.
// Standalone module, same reasoning as share.ts: it does NOT import ./client
// or ./http, so the not-yet-authenticated bundle (login + first-run screens)
// never pulls in the authenticated app surface.
//
// `createInitialAdmin` is THE seam: it is the only function in the entire
// frontend that knows this request shape.
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
  /** True when the optional first shared folder was requested but could not
   *  be registered. The administrator account still exists and can retry it
   *  after signing in. */
  share_failed?: boolean
}
/** A setup request whose account has not been created because the submitted
 *  network values were refused. The findings carry the same catalogue keys
 *  settings uses, so the first-run form can name the field-specific correction
 *  instead of reducing a 422 response to a generic network failure. */
export class SetupValidationError extends Error {
  readonly findings: SetupFinding[]

  constructor(findings: SetupFinding[]) {
    super('setup validation failed')
    this.name = 'SetupValidationError'
    this.findings = findings
  }
}
function isSetupFinding(value: unknown): value is SetupFinding {
  if (!value || typeof value !== 'object') return false
  return (
    'section' in value &&
    typeof value.section === 'string' &&
    'reason' in value &&
    typeof value.reason === 'string' &&
    'blocking' in value &&
    typeof value.blocking === 'boolean'
  )
}

function setupFindings(body: unknown): SetupFinding[] | null {
  if (!body || typeof body !== 'object' || !('findings' in body) || !Array.isArray(body.findings)) return null
  const findings = body.findings.filter(isSetupFinding)
  return findings.length === body.findings.length ? findings : null
}

const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api/v1'

/** THE SEAM: see file header. */
export async function createInitialAdmin(req: SetupCreateAdminReq): Promise<SetupResult> {
  const res = await fetch(`${BASE}/system/setup`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    credentials: 'include',
    body: JSON.stringify(req)
  })
  if (res.status === 204) return { warnings: [] }
  const body = await res.json().catch(() => ({}))
  if (res.ok) {
    const done = body as { warnings?: SetupFinding[]; share_failed?: boolean }
    // `settings.check_passed` is what the checker emits when it found nothing
    // to say: it is the absence of a warning, not one. Counted as a warning it
    // stops this screen on every successful setup, and the person has to press
    // the button a second time to get past a panel reporting that all is well.
    const warnings = (done.warnings ?? []).filter((w) => w.reason !== 'settings.check_passed')
    return { warnings, share_failed: done.share_failed }
  }
  const findings = setupFindings(body)
  if (res.status === 422 && findings) {
    throw new SetupValidationError(findings)
  }
  const err = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
  throw new ApiError(res.status, err)
}

/**
 * `GET /api/v1/system/setup` → `{"required": bool}`. Unauthenticated and deliberately
 * one field: it says only whether an account exists, which a junk `POST`
 * already reveals by answering `410` rather than `403`. It goes false forever
 * once the first account is created.
 *
 * This is how the app tells "nobody has ever logged in here" apart from "your
 * session expired"; the two produce the same `401` on `GET /api/auth/session`
 * and want completely different screens.
 *
 * Never throws: if this call fails we cannot conclude the server needs
 * setting up, and guessing wrong sends a normal user to a create-admin form.
 * Failure means `false`, which lands on the login screen, and that screen
 * carries a manual `/setup` link for the case where we guessed wrong.
 */
export async function setupRequired(): Promise<boolean> {
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
