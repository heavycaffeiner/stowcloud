// web/src/lib/state/auth.test.ts — the screen-selection state machine
// (file browser / login / first-run) and the task-#3 "any 401 lands you on
// login" rule, independent of any component.
import { beforeEach, describe, expect, it } from 'vitest'
import type { SessionInfo } from '../api/types'
import { authState, noteUnauthorized, setAnonymous, setAuthenticated, setFirstRun } from './auth.svelte'

const fakeSession: SessionInfo = {
  user: {
    id: 1,
    name: 'demo',
    display_name: '데모 사용자',
    is_admin: true,
    totp_enabled: false,
    smb_opt_out: false,
    smb_enabled: true
  },
  roots: [],
  csrf: 'csrf-token',
  limits: { chunk_size: 1, chunk_min: 1, max_file_size: null, parallel: 1 },
  features: {
    webdav: true,
    smb: false,
    preview: true,
    trash: true,
    shares: true,
    search: 'name'
  },
  oidc: { linked: false }
}

describe('auth screen state', () => {
  beforeEach(() => {
    authState.screen = 'loading'
    authState.session = null
  })

  it('setAuthenticated moves to the browser screen and stores the session', () => {
    setAuthenticated(fakeSession)
    expect(authState.screen).toBe('browser')
    // `$state` wraps stored objects in a reactive proxy, so this is a
    // different reference than `fakeSession` even though it holds the same
    // data — assert on content, not identity.
    expect(authState.session).toEqual(fakeSession)
  })

  it('setAnonymous goes to the login screen and clears the session', () => {
    setAuthenticated(fakeSession)
    setAnonymous()
    expect(authState.screen).toBe('login')
    expect(authState.session).toBeNull()
  })

  it('setFirstRun goes to the create-administrator screen', () => {
    setFirstRun()
    expect(authState.screen).toBe('first-run')
    expect(authState.session).toBeNull()
  })

  it('noteUnauthorized flips a live browser session to login', () => {
    setAuthenticated(fakeSession)
    noteUnauthorized()
    expect(authState.screen).toBe('login')
    expect(authState.session).toBeNull()
  })

  // A mid-session 401 can never mean first-run: a session only exists on a
  // server that already has an account. `GET /api/setup` is the only thing
  // that decides first-run, and only `bootstrapAuth` consults it.
  it('noteUnauthorized never lands on first-run, even from bootstrap', () => {
    authState.screen = 'loading'
    noteUnauthorized()
    expect(authState.screen).toBe('login')
  })

  it('noteUnauthorized is a no-op once already showing first-run', () => {
    setFirstRun()
    // A stray 401 racing the redirect must not yank the operator out of the
    // create-administrator form they are halfway through filling in.
    noteUnauthorized()
    expect(authState.screen).toBe('first-run')
  })
})
