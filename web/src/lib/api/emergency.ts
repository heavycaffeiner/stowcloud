// The safe-mode settings editor's client.
//
// Standalone, like setup.ts and for a sharper version of the same reason: this
// module is loaded on a server whose engine may not have come up at all, so it
// must not pull in ./client, ./mock or ./http and with them the whole
// authenticated surface. The four calls below are the entire contract, and
// they are the only routes the emergency mux mounts.
import { ApiError, type ApiErrorBody } from './types'

const BASE = '/emergency/api'

/** What the probes said about a change that was let through. The one that
 *  matters here is the lockout: this screen is where somebody goes to repair a
 *  host list that already locked them out, so it warns rather than blocks. */
export interface EmergencyFinding {
  level: 'block' | 'warn' | 'ok'
  field?: string
  reason_key: string
  reason_params?: Record<string, string>
}

/** What the screen reads before anybody has signed in. */
export interface EmergencyDoor {
  /** No administrator exists yet, so there is nothing to authenticate and the
   *  screen points at the first-run form instead of drawing a login. */
  setup_required: boolean
  /** Why the door is being fronted, or empty when the deployment is healthy
   *  and this is only the always-on route. */
  reason: string
}

export interface EmergencySettings {
  /** The settings document as stored. Whole rather than a rendered field list:
   *  the engine that builds that list may not be running, and what is in the
   *  database is true in every mode. */
  stored: Record<string, Record<string, unknown>>
  sections: string[]
  /** The two values a repair is usually about, resolved. */
  listen: string
  app_hosts: string[]
}

export interface EmergencySave {
  applied: 'restart_required'
  warnings: EmergencyFinding[]
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json; charset=utf-8', ...(init?.headers ?? {}) },
    credentials: 'include'
  })
  const body = await res.json().catch(() => ({}))
  if (res.ok) return body as T
  const err = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
  throw new ApiError(res.status, err)
}

export function emergencyDoor(): Promise<EmergencyDoor> {
  return call<EmergencyDoor>('/state')
}

/** Signs in. `totp_required` is the password having been right with a code
 *  still to come, which is not a refusal: reporting it as one leaves an
 *  enrolled administrator with no way to send the code. */
export function emergencyLogin(
  username: string,
  password: string,
  factor?: string
): Promise<{ status: 'ok' | 'totp_required' }> {
  return call('/login', {
    method: 'POST',
    body: JSON.stringify({ username, password, factor: factor ?? '' })
  })
}

export function emergencySettings(): Promise<EmergencySettings> {
  return call<EmergencySettings>('/settings')
}

export function emergencySave(section: string, body: unknown): Promise<EmergencySave> {
  return call<EmergencySave>(`/settings/${encodeURIComponent(section)}`, {
    method: 'PATCH',
    body: JSON.stringify(body)
  })
}

/** Asks the process to exit so a supervisor starts it again, which is how a
 *  repair takes effect. `restarting: false` is a deployment with no supervisor
 *  to come back from, which is worth saying rather than pretending. */
export function emergencyRestart(): Promise<{ restarting: boolean }> {
  return call('/restart', { method: 'POST' })
}
