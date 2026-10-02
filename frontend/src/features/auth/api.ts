// Signing in: the session, the login steps, first-run setup and the anonymous
// half of single sign-on. Login and setup screens load this before there is a
// session, so it imports nothing from the signed-in app.
import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import {
  client,
  decimal,
  errorFrom,
  isSessionDead,
  malformed,
  oneOf,
  setCsrfToken,
  unwrap,
  unwrapOptional
} from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { keys } from '../../api/query-keys'
import { permsFromNames, type Perms } from '../files/perms'

type Schemas = components['schemas']

/** What works over SMB right now, with the deployment's TOTP policy folded in. */
export type SmbCredential = 'account' | 'dedicated' | 'none'

/** An SSO link is not one of these: a linked account may still hold an SMB
 *  password, and has no account password until it sets one. */
export type SmbUnavailableReason = 'not_set' | 'totp_blocked' | 'opted_out'

export interface UserInfo {
  id: number
  name: string
  display_name: string
  is_admin: boolean
  totp_enabled: boolean
  smb_opt_out: boolean
  smb_enabled: boolean
  smb_credential?: SmbCredential
  /** Present only with `'none'`. */
  smb_unavailable_reason?: SmbUnavailableReason
}

export interface RootEntry {
  label: string
  perms: Perms
  shared_externally: boolean
  /** Whether the share keeps deleted items, so the delete confirmation can
   *  say which of the two happens. */
  trash_enabled: boolean
  /** Why the folder cannot be opened now. It stays listed either way, so a
   *  missing disk does not read as a deleted folder. */
  broken_reason?: string
}

export interface ClientLimits {
  chunk_size: number
  /** The floor a 413 shrink must not go below. */
  chunk_min: number
  max_file_size: number | null
  parallel: number
}

export type SearchMode = 'walk' | 'name' | 'name+content'

export interface Features {
  webdav: boolean
  smb: boolean
  preview: boolean
  trash: boolean
  shares: boolean
  direct_uploads: boolean
  search: SearchMode
}

/** The caller's own SSO link. `subject_hint` is four characters from each end
 *  of the subject, enough to recognise which identity is attached. */
export interface SessionOidc {
  linked: boolean
  subject_hint?: string
  linked_ns?: string
}

export interface SessionInfo {
  user: UserInfo
  roots: RootEntry[]
  csrf: string
  limits: ClientLimits
  features: Features
  oidc: SessionOidc
}

/** All an anonymous caller learns about SSO: whether to draw the button and
 *  what to write on it. */
export interface OidcConfig {
  enabled: boolean
  display_name: string
}

/** A password that verified with no second factor answers with the identity;
 *  one that needs a code answers with the challenge. */
export type LoginResult =
  | { required?: undefined; id: string; login: string; admin: boolean; csrf: string }
  | { required: 'totp'; challenge: string; expires_in_seconds: number }

/** One check result from setup and the settings saves, as a catalogue key and
 *  its arguments so the client owns the wording. */
export interface SetupFinding {
  section: string
  field?: string
  reason: string
  args?: Record<string, string>
  blocking: boolean
}

export interface SetupCreateAdminReq {
  token: string
  username: string
  password: string
  /** The names this server answers for. Until one is saved the host guard
   *  admits the local network on the peer address alone. */
  app_hosts: string[]
  /** CIDR ranges whose forwarded headers are believed. Empty trusts none. */
  trusted_proxies: string[]
  /** The folder to start serving, if any. */
  first_share?: { name: string; host: string }
}

export interface SetupResult {
  warnings: SetupFinding[]
  /** The first shared folder could not be registered. The account exists and
   *  can retry it after signing in. */
  share_failed?: boolean
}

/** Setup refused the submitted network values and created no account. */
export class SetupValidationError extends Error {
  readonly findings: SetupFinding[]

  constructor(findings: SetupFinding[]) {
    super('setup validation failed')
    this.name = 'SetupValidationError'
    this.findings = findings
  }
}

const SMB_CREDENTIALS: readonly SmbCredential[] = ['account', 'dedicated', 'none']
const SMB_UNAVAILABLE: readonly SmbUnavailableReason[] = ['not_set', 'totp_blocked', 'opted_out']
const SEARCH_MODES: readonly SearchMode[] = ['walk', 'name', 'name+content']

function sessionFromWire(w: Schemas['WhoAmIView']): SessionInfo {
  return {
    user: {
      id: decimal(w.id, 'session id'),
      name: w.login,
      display_name: w.display ?? '',
      is_admin: w.admin,
      totp_enabled: w.totp_enabled,
      smb_opt_out: w.smb_opt_out,
      smb_enabled: w.smb_enabled,
      smb_credential:
        w.smb_credential === undefined ? undefined : oneOf(w.smb_credential, SMB_CREDENTIALS, 'SMB credential', 'none'),
      smb_unavailable_reason:
        w.smb_unavailable_reason === undefined
          ? undefined
          : oneOf(w.smb_unavailable_reason, SMB_UNAVAILABLE, 'SMB unavailable reason', 'not_set')
    },
    roots: (w.roots ?? []).map((r) => ({
      label: r.label,
      perms: permsFromNames(r.perms),
      shared_externally: r.shared_externally,
      trash_enabled: r.trash_enabled,
      broken_reason: r.broken_reason
    })),
    csrf: w.csrf,
    limits: w.limits,
    features: { ...w.features, search: oneOf(w.features.search, SEARCH_MODES, 'search mode', 'walk') },
    oidc: w.oidc
  }
}

/** Also installs the CSRF token every write needs. */
async function fetchSession(): Promise<SessionInfo> {
  const w = await unwrap(client.GET('/api/v1/auth/session'))
  setCsrfToken(w.csrf)
  return sessionFromWire(w)
}

function identityOf(w: { id?: string; login?: string; admin?: boolean; csrf?: string }) {
  return { id: w.id ?? '', login: w.login ?? '', admin: w.admin === true, csrf: w.csrf ?? '' }
}

async function login(username: string, password: string): Promise<LoginResult> {
  const out = await unwrap(client.POST('/api/v1/auth/login', { body: { login: username, password } }))
  if (out.required === undefined) return identityOf(out)
  if (out.required !== 'totp' || !out.challenge) throw malformed(200, 'an unknown login step')
  return { required: 'totp', challenge: out.challenge, expires_in_seconds: out.expires_in_seconds ?? 0 }
}

async function loginTotp(challenge: string, code: string): Promise<LoginResult> {
  return identityOf(await unwrap(client.POST('/api/v1/auth/login/totp', { body: { challenge, code } })))
}

/** A session established through SSO answers with the provider's logout URL,
 *  so the browser can end the provider's session too. */
export async function logout(): Promise<{ end_session_url?: string }> {
  const out = await unwrapOptional(client.POST('/api/v1/auth/logout'))
  return out?.end_session_url ? { end_session_url: out.end_session_url } : {}
}

/** Never throws: a failure cannot be told from "no IdP configured", and the
 *  safe reading of both is no button. The password form is always there. */
async function fetchOidcConfig(): Promise<OidcConfig> {
  try {
    const { data } = await client.GET('/api/v1/auth/oidc/config')
    if (data?.enabled !== true) return { enabled: false, display_name: '' }
    return { enabled: true, display_name: data.display_name ?? '' }
  } catch {
    return { enabled: false, display_name: '' }
  }
}

/** Provider discovery is HTTPS only, so relative, plain-HTTP and script URLs
 *  are not authorization endpoints and the browser is not sent to them. */
export function isProviderUrl(raw: string): boolean {
  if (!URL.canParse(raw)) return false
  const target = new URL(raw)
  return target.protocol === 'https:' && target.hostname !== '' && !target.username && !target.password
}

/** Starts the anonymous SSO flow. The endpoint answers JSON so the provider
 *  URL can be checked before the browser goes there. */
export async function startOidcLogin(returnTo?: string | null): Promise<void> {
  try {
    const { data } = await client.GET('/api/v1/auth/oidc/start', {
      params: { query: { return_to: returnTo || undefined } }
    })
    const raw = data?.authorize_url
    if (raw && isProviderUrl(raw)) window.location.href = raw
  } catch {
    // A provider or network refusal leaves the login form in place.
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

/** The only request that creates the first administrator. */
async function createInitialAdmin(req: SetupCreateAdminReq): Promise<SetupResult> {
  const { data, error, response } = await client.POST('/api/v1/system/setup', { body: req })
  if (response.ok) {
    // `settings.check_passed` is the checker saying it found nothing; counted
    // as a warning it would stop this screen on every successful setup.
    const warnings = (data?.warnings ?? []).filter((w) => w.reason !== 'settings.check_passed')
    return { warnings, share_failed: data?.share_failed }
  }
  const findings = setupFindings(error)
  if (response.status === 422 && findings) throw new SetupValidationError(findings)
  throw errorFrom(response, error)
}

/** Whether this server has ever had an account, which is how "nobody has
 *  signed in here" is told from "your session expired": both are the same 401.
 *  Never throws: guessing true would send a normal user to a create-admin
 *  form, and the login screen links to setup for the other case. */
async function fetchSetupRequired(): Promise<boolean> {
  try {
    const { data } = await client.GET('/api/v1/system/setup')
    return data?.required === true
  } catch {
    return false
  }
}

const sessionOptions = {
  queryKey: keys.session(),
  queryFn: fetchSession,
  staleTime: 5 * 60_000,
  // A refused session is an answer; retrying only delays the login screen.
  retry: false
} as const

/** The query the rest of the app waits on before it can change anything. */
export function useSession() {
  return useQuery(sessionOptions)
}

/** Reads the session now, for a flow that must hold the CSRF token before its
 *  next write. */
export function useLoadSession(): () => Promise<SessionInfo> {
  const queryClient = useQueryClient()
  return useCallback(() => queryClient.fetchQuery({ ...sessionOptions, staleTime: 0 }), [queryClient])
}

const setupRequiredOptions = {
  queryKey: keys.setupRequired(),
  queryFn: fetchSetupRequired,
  staleTime: Infinity,
  retry: false
} as const

export function useOidcConfig() {
  return useQuery({ queryKey: keys.oidcConfig(), queryFn: fetchOidcConfig, staleTime: Infinity, retry: false })
}

/** A TOTP challenge is a half-finished login with no session to read yet. */
function useRefreshAfterLogin(): (result: LoginResult) => void {
  const queryClient = useQueryClient()
  return (result) => {
    if (result.required === undefined) void queryClient.invalidateQueries({ queryKey: keys.session() })
  }
}

export function useLogin() {
  const onSuccess = useRefreshAfterLogin()
  return useMutation({
    mutationFn: ({ username, password }: { username: string; password: string }) => login(username, password),
    onSuccess
  })
}

export function useLoginTotp() {
  const onSuccess = useRefreshAfterLogin()
  return useMutation({
    mutationFn: ({ challenge, code }: { challenge: string; code: string }) => loginTotp(challenge, code),
    onSuccess
  })
}

export function useCreateInitialAdmin() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: createInitialAdmin,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.session() })
      void queryClient.invalidateQueries({ queryKey: keys.setupRequired() })
    }
  })
}

/** The session check got no answer: the server is unreachable or failed. */
export class SessionUnreachableError extends Error {}

/**
 * Where a visit to the signed-in app goes. A session already in hand lets it
 * through at once and refreshes behind it when stale. Asks about first-run
 * setup only after the session is refused, so a sign-in form never shows to
 * somebody with no account to sign in with.
 */
export async function resolveAuthScreen(queryClient: QueryClient): Promise<'browser' | 'login' | 'first-run'> {
  const state = queryClient.getQueryState(sessionOptions.queryKey)
  try {
    if (state?.status === 'success' && !state.isInvalidated) void queryClient.prefetchQuery(sessionOptions)
    else await queryClient.fetchQuery(sessionOptions)
    return 'browser'
  } catch (error) {
    if (!isUnauthenticated(error)) throw new SessionUnreachableError('session check failed', { cause: error })
    return (await queryClient.fetchQuery(setupRequiredOptions)) ? 'first-run' : 'login'
  }
}

/** True when a failed session means "not signed in" rather than "the server
 *  could not be reached", which is a different screen. */
export function isUnauthenticated(error: unknown): boolean {
  return isSessionDead(error)
}
