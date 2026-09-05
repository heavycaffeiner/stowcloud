import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/types'
import { isUnauthenticated, screenOf } from './session'

describe('which screen the app is on', () => {
  it('shows the browser as soon as there is a session', () => {
    const screen = screenOf({ hasSession: true, sessionFailed: false, setupPending: false, setupRequired: true })
    expect(screen).toBe('browser')
  })

  it('waits while the session is still being checked', () => {
    expect(screenOf({ hasSession: false, sessionFailed: false, setupPending: false, setupRequired: false })).toBe('loading')
  })

  // The session route answers the same 401 whether the session expired or the
  // server has never had an account, so the follow-up question decides. Landing
  // on login first would flash a sign-in form at somebody with no account.
  it('keeps waiting while the first-run question is still out', () => {
    expect(screenOf({ hasSession: false, sessionFailed: true, setupPending: true, setupRequired: false })).toBe('loading')
  })

  it('offers the create-administrator screen on a server with no account', () => {
    expect(screenOf({ hasSession: false, sessionFailed: true, setupPending: false, setupRequired: true })).toBe('first-run')
  })

  it('offers login on a server that has one', () => {
    expect(screenOf({ hasSession: false, sessionFailed: true, setupPending: false, setupRequired: false })).toBe('login')
  })
})

describe('telling "not signed in" apart from "cannot reach the server"', () => {
  it('reads a refusal as not signed in', () => {
    expect(isUnauthenticated(new ApiError(401, { code: 'auth.required', message: 'no' }))).toBe(true)
    expect(isUnauthenticated(new ApiError(404, { code: 'request_failed', message: 'no' }))).toBe(true)
  })

  it('does not read an unreachable server as a sign-out', () => {
    expect(isUnauthenticated(new ApiError(503, { code: 'unavailable', message: 'no' }))).toBe(false)
    expect(isUnauthenticated(new TypeError('network down'))).toBe(false)
  })
})
