// web/src/lib/api/http.ts — real HTTP implementation of the same surface as
// mock.ts. Talks to the real server. This module is only
// ever exercised once VITE_API_MOCK is unset/0 — it is untested against a
// live server here (the backend does not exist yet) but the shape mirrors
// exactly so swapping the flag is the only integration step.
import {
  ApiError,
  type ActiveSession,
  type AdminGrant,
  type AdminGroup,
  type AdminOidcUnlinkResult,
  type AdminShare,
  type AdminUser,
  type AdminUserOidc,
  type ApiErrorBody,
  type AppPasswordInfo,
  type ApplyOutcome,
  type ArchiveListing,
  type ArchiveSettingsReq,
  type RateSettingsReq,
  type AuditPage,
  type AuditQuery,
  type BatchResult,
  type CreateGrantReq,
  type CreateGroupReq,
  type CreateShareReq,
  type DbSettingsReq,
  type Entry,
  type HomesSettingsReq,
  type IndexEstimate,
  type IndexSettings,
  type JobListResponse,
  type JobStatus,
  type ListResponse,
  type LoginResult,
  type MovePreflight,
  type MoveReq,
  type NetworkSettingsReq,
  type OidcSettingsReq,
  type PathsSettingsReq,
  type FolderSize,
  type ReadFileResponse,
  type RecentHit,
  type RecentQuery,
  type SearchSettingsReq,
  type SessionInfo,
  type SettingsSnapshot,
  type SettingsSectionId,
  type BatchItemResult,
  type CopyResult,
  type ShareLinkCreateReq,
  type ShareLinkInfo,
  type ShareLinkPatchReq,
  type SmbSettingsReq,
  type StorageReport,
  type SymlinkPolicyReq,
  type TrashEntry,
  type UpdateGrantReq,
  type UpdateGroupReq,
  type UpdateShareReq,
  type SMBOutcome,
  type UploadSettingsReq,
  type UploadSettingsResp,
  type WatchSettingsReq,
  type Kind,
  permsFromNames
} from './types'
import type { ListOpts, SearchDone, SearchHit } from './mock'
import { noteUnauthorized } from '../state/auth.svelte'
import { normalizePath } from './path-utils'

const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api/v1'

let csrfToken = ''
export function setCsrfToken(t: string): void {
  csrfToken = t
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (!headers.has('Content-Type') && init.body && typeof init.body === 'string') {
    headers.set('Content-Type', 'application/json; charset=utf-8')
  }
  if (method !== 'GET' && method !== 'HEAD' && csrfToken) {
    headers.set('Sc-Csrf', csrfToken)
  }

  const res = await fetch(`${BASE}${path}`, { ...init, method, headers, credentials: 'include' })
  if (res.status === 204) return undefined as T
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw errorFrom(res, body)
  return body as T
}

/**
 * Turns a failed response into the error every caller expects.
 *
 * Split out of `request` so the calls that read a body other than JSON, like
 * the file read, refuse the same way: the same `ApiError`, and the same 401
 * handling, which is the one place a dead session becomes the login screen.
 */
function errorFrom(res: Response, body: unknown): ApiError {
  const errBody = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
  const err = new ApiError(res.status, errBody)
  // Task: "make a 401 mean something" — every call goes through this one
  // function, so this is the single place a dead/missing session turns
  // into "show the login screen" instead of an inline error string
  // bubbling up into whatever list/table happened to be rendering.
  //
  // Gated on `code`, not just `status`: `auth.invalid_credentials` is also
  // a 401, but it means "this specific re-confirmation was wrong" (a bad
  // *current* password on `/account/password`, a bad TOTP code on
  // `/account/totp/enroll`), not "the session cookie is gone". The settings
  // screens for those actions (`PasswordSection`/`TotpSection`) need that
  // error to stay a rejected promise they show inline — bouncing the whole
  // app to the login screen because someone mistyped their *current*
  // password while already logged in would be exactly the "silent
  // failure" this task explicitly rules out.
  if (res.status === 401 && errBody.code === 'auth.required') noteUnauthorized()
  return err
}

/**
 * The listing shapes as they arrive, before the app's own shapes are built.
 *
 * Two fields differ from what the app reads, and both differ for a reason.
 * `size` is a decimal string because a file past 2^53 bytes loses exactness as
 * a JavaScript number, and a size that comes back wrong is a download that
 * comes back wrong. `perms` is the granted names only, so absent means denied.
 */
interface WireEntry {
  name: string
  path: string
  kind: Kind
  is_dir: boolean
  size: string
  mtime_ns: string
  btime_ns?: string
  etag: string
  etag_weak: boolean
  perms: string[]
}

interface WireListResponse {
  entries: WireEntry[]
  dirs: number
  total: number
  dir_etag: string
  dir_etag_weak: boolean
  next?: string
}

/**
 * Widens one wire entry into the shape the app reads.
 *
 * The size is parsed with Number rather than kept as a string because every
 * consumer formats or compares it arithmetically. Past 2^53 that loses the
 * exact byte count, which is why the wire carries the string; nothing in this
 * interface displays a number that large without rounding it anyway.
 */
function entryFromWire(w: WireEntry): Entry {
  return {
    name: w.name,
    path: w.path,
    kind: w.kind,
    size: Number(w.size ?? 0),
    mtime_ns: w.mtime_ns ?? '0',
    etag: w.etag ?? '',
    etag_weak: w.etag_weak ?? false,
    perms: permsFromNames(w.perms)
  }
}

/** Widens one page, and maps `next` onto the cursor the app carries. */
function listFromWire(w: WireListResponse): ListResponse {
  return {
    entries: (w.entries ?? []).map(entryFromWire),
    dirs: w.dirs ?? 0,
    total: w.total ?? 0,
    // Absent on the final page. The app's field is nullable, so the end of the
    // walk is a null rather than an empty string that reads as a real cursor.
    cursor: w.next ? w.next : null,
    dir_etag: w.dir_etag ?? '',
    dir_etag_weak: w.dir_etag_weak ?? false
  }
}

function qs(params: Record<string, string | number | undefined>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

/**
 * The session, as the wire sends it: the identity's fields sit at the top
 * level rather than under a `user` object, and permissions are name arrays.
 */
interface WireSession {
  id: string
  login: string
  display?: string
  admin: boolean
  csrf: string
  totp_enabled: boolean
  smb_opt_out: boolean
  smb_enabled: boolean
  smb_credential?: SessionInfo['user']['smb_credential']
  smb_unavailable_reason?: SessionInfo['user']['smb_unavailable_reason']
  roots: Array<{
    label: string
    perms: string[]
    shared_externally: boolean
    trash_enabled: boolean
    broken_reason?: string
  }>
  limits: SessionInfo['limits']
  features: SessionInfo['features']
}

async function session(): Promise<SessionInfo> {
  const w = await request<WireSession>('/auth/session')
  setCsrfToken(w.csrf)

  const s: SessionInfo = {
    user: {
      // The wire carries the id as a decimal string, because an account id
      // past 2^53 is not exact as a JavaScript number. Nothing here does
      // arithmetic on it, so it is parsed once for the shape the app reads.
      id: Number(w.id),
      name: w.login,
      display_name: w.display ?? '',
      is_admin: w.admin,
      totp_enabled: w.totp_enabled,
      smb_opt_out: w.smb_opt_out,
      smb_enabled: w.smb_enabled,
      smb_credential: w.smb_credential,
      smb_unavailable_reason: w.smb_unavailable_reason
    },
    roots: (w.roots ?? []).map((r) => ({
      label: r.label,
      perms: permsFromNames(r.perms),
      // Not on the wire. The kind a share was created as is an operator's
      // concern; what the interface needs from a root is what it may do
      // there, which `perms` already says.
      share_kind: 'Normal',
      shared_externally: r.shared_externally,
      trash_enabled: r.trash_enabled,
      broken_reason: r.broken_reason
    })),
    csrf: w.csrf,
    limits: w.limits,
    features: w.features,
    // The engine serves no OIDC link state on the session yet. Reported as
    // unlinked rather than omitted, so the settings screen renders its
    // "not linked" state instead of failing on a missing object.
    oidc: { linked: false }
  }
  return s
}

async function login(username: string, password: string): Promise<LoginResult> {
  // `login`, not `username`: the field the handler decodes. A body with the
  // wrong key decodes as an empty login, which is refused as a bad credential
  // rather than as a malformed request, so the screen would say the password
  // was wrong no matter what was typed.
  return request('/auth/login', { method: 'POST', body: JSON.stringify({ login: username, password }) })
}

async function loginTotp(challenge: string, code: string): Promise<LoginResult> {
  return request('/auth/login/totp', { method: 'POST', body: JSON.stringify({ challenge, code }) })
}

async function logout(): Promise<void> {
  await request('/auth/logout', { method: 'POST' })
}

/**
 * A directory page.
 *
 * Paging is a cursor walk, not a random-access window. The server orders the
 * whole directory and cuts the page the cursor names, so an offset into a
 * listing has nothing to index: there is no server-side listing session to
 * hold one. `next` is absent on the final page, which is what ends the walk.
 */
async function list(path: string, opts: ListOpts): Promise<ListResponse> {
  const page = await request<WireListResponse>(
    `/files/list${qs({
      path,
      cursor: opts.cursor,
      limit: opts.limit,
      sort: opts.sort,
      order: opts.order
    })}`,
    { signal: opts.signal }
  )
  return listFromWire(page)
}

async function stat(path: string): Promise<Entry> {
  return entryFromWire(await request<WireEntry>(`/files/stat${qs({ path })}`))
}

async function mkdir(path: string): Promise<Entry> {
  return entryFromWire(
    await request<WireEntry>('/files/mkdir', { method: 'POST', body: JSON.stringify({ path }) })
  )
}

async function rename(path: string, newName: string): Promise<Entry> {
  // `new_name`, which is what the handler decodes. It sent `name`, so every
  // rename decoded as an empty new name and came back 422 invalid_name with
  // an empty component: the dialogue closed on nothing.
  return entryFromWire(
    await request<WireEntry>('/files/rename', {
      method: 'POST',
      body: JSON.stringify({ path, new_name: newName })
    })
  )
}

/**
 * `POST /api/v1/files/copy` answers per-item results and, when at least one item
 * actually started, the job to poll.
 *
 * `job` is absent when nothing started: every item was refused, or every item
 * was skipped because its destination was taken. This was typed as always
 * present, so a caller destructured `undefined` and polled a job by that name
 * until its own timeout fired.
 */
async function copy(req: MoveReq): Promise<CopyResult> {
  return request('/files/copy', { method: 'POST', body: JSON.stringify(req) })
}

/**
 * `POST /api/v1/files/move` answers inline, not with a job: a move is a rename
 * within one filesystem, which finishes in the request. Only the cross-device
 * case copies bytes, and the server reports that per item as `will_copy`
 * rather than deferring the whole batch.
 */
async function move(req: MoveReq): Promise<BatchResult> {
  return request('/files/move', { method: 'POST', body: JSON.stringify({ ...req, dry_run: false }) })
}

/** The same endpoint with `dry_run`, which reports what each item would do
 *  without doing it: what the destination picker asks before it commits. */
async function movePreflight(req: MoveReq): Promise<MovePreflight> {
  return request('/files/move', { method: 'POST', body: JSON.stringify({ ...req, dry_run: true }) })
}

async function del(paths: string[], permanent = false): Promise<{ results: BatchItemResult[] }> {
  return request('/files/delete', { method: 'POST', body: JSON.stringify({ paths, permanent }) })
}

// ── content links & archive download (§8) ──

/**
 * `POST /api/v1/files/archive` streams the ZIP itself. The server never stores one,
 * so there is no job to poll and no second request for the bytes: they arrive
 * as this response's body, and the caller saves it.
 *
 * Not a plain navigation, because the request is a POST carrying the path list
 * and needs the CSRF header a form submission cannot send.
 */
async function archive(paths: string[], name?: string): Promise<Blob> {
  const headers = new Headers({ 'Content-Type': 'application/json' })
  if (csrfToken) headers.set('Sc-Csrf', csrfToken)
  const res = await fetch(`${BASE}/files/archive`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({ paths, name })
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw errorFrom(res, body)
  }
  return res.blob()
}

/**
 * `GET /api/v1/files/archive/list` — every entry in a ZIP archive.
 *
 * Nothing in the result is openable: opening an entry means extraction, which
 * this server does not do. A path the caller cannot list and a file that is
 * not a zip are the same `404`.
 */
async function archiveList(path: string): Promise<ArchiveListing> {
  return request(`/files/archive/list${qs({ path })}`)
}

/**
 * `GET /api/v1/files/size` — one folder's recursive size, on demand.
 *
 * Deliberately not folded into `stat`, which every selection already calls:
 * a size column on a listing row would start one tree walk per row. A folder
 * containing a subtree this account is denied answers `403` with
 * `detail.reason = 'denies_below'` rather than a byte count covering data the
 * caller cannot read.
 */
/**
 * `GET /api/v1/files/thumbnail` — the URL of a re-encoded thumbnail, by path.
 *
 * A URL rather than a fetch: the <img> does the loading, so nothing here holds
 * the bytes and the browser's own cache applies. Same origin, so the session
 * cookie rides along; the answer is `private, immutable` and keyed on the
 * file's identity, mtime and size, so a changed file is a different URL.
 *
 * `dim` picks the preset rather than being sent verbatim: the server re-encodes
 * into fixed boxes, and a caller naming arbitrary pixels would be asking for a
 * cache entry per layout.
 */
function thumbUrl(path: string, dim: number): string {
  const size = dim <= 256 ? 'small' : dim <= 512 ? 'medium' : 'large'
  return `${BASE}/files/thumbnail${qs({ path, size })}`
}

async function folderSize(path: string): Promise<FolderSize> {
  return request(`/files/size${qs({ path })}`)
}

/**
 * `GET /api/v1/files/recent` — every file this account wrote through this server inside
 * the window, newest first. Exact: there is no walk to truncate.
 */
async function recentList(opts: RecentQuery = {}): Promise<{ hits: RecentHit[] }> {
  // An instant, not a day count. A day count has to be resolved against
  // somebody's clock, and the two ends of this wire are in different time
  // zones often enough that the same request meant two different windows
  // depending on which side did the arithmetic.
  return request(`/files/recent${qs({ limit: opts.limit, since: opts.since, scope: opts.scope })}`)
}

// ── long-running jobs ──
// A copy, a delete over the inline threshold and an index build answer
// `202 { job }`; a move and an archive finish in the request itself.
// `state/job-tray.svelte.ts` is the one caller that wraps
// `state/jobs.ts::pollJob` (REST poll plus the WS `job` push) to show live
// progress and calls `jobCancel` on user request.

/** `GET /api/v1/jobs` — every non-terminal job the caller owns. `JobTray` calls
 *  this once on mount to re-attach across a refresh or a server restart
 *  (`go/internal/httpapi/handler/admin_ops.go`). */
async function jobList(): Promise<JobListResponse> {
  return request('/jobs')
}

async function jobStatus(id: string): Promise<JobStatus> {
  return request(`/jobs/${encodeURIComponent(id)}`)
}

/** One spelling for a cancel: the DELETE alias is gone, and cancelling is an
 *  action on the job rather than a deletion of it. */
async function jobCancel(id: string): Promise<void> {
  await request(`/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' })
}

// ── trash ──

async function trashList(): Promise<TrashEntry[]> {
  return request('/trash')
}

async function trashRestore(ids: string[]): Promise<BatchResult> {
  return request('/trash/restore', { method: 'POST', body: JSON.stringify({ ids }) })
}

async function trashPurge(ids: string[]): Promise<BatchResult> {
  return request('/trash/purge', { method: 'POST', body: JSON.stringify({ ids }) })
}

// ── share links, owner side ──

async function sharesList(path?: string): Promise<ShareLinkInfo[]> {
  return request(`/links${qs({ path })}`)
}

async function shareCreate(req: ShareLinkCreateReq): Promise<ShareLinkInfo> {
  return request('/links', { method: 'POST', body: JSON.stringify(req) })
}

async function shareUpdate(id: number, patch: ShareLinkPatchReq): Promise<ShareLinkInfo> {
  return request(`/links/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

async function shareDelete(id: number): Promise<void> {
  await request(`/links/${id}`, { method: 'DELETE' })
}

// ── text editor (`/edit/[...path]`) ──

/**
 * `GET /api/v1/files/read` streams the file's own bytes, not a JSON envelope around
 * them. This went through `request()`, which parses JSON: every text preview
 * and every open of the editor threw on the first byte of the file, and the
 * card showed its failure state for a file that had been read perfectly well.
 *
 * Decoded as UTF-8 with replacement characters rather than refused, because
 * the caller is a text view: showing a file with a few replacement marks in it
 * is more use than refusing to show it at all, and the editor's own save path
 * is conditional, so nothing here can silently rewrite bytes it misread.
 */
async function readFile(path: string): Promise<ReadFileResponse> {
  const res = await fetch(`${BASE}/files/read${qs({ path })}`, { credentials: 'include' })
  if (!res.ok) throw errorFrom(res, await res.json().catch(() => ({})))
  return { content: await res.text() }
}

/**
 * `POST /api/v1/files/write`.
 *
 * Omitting `ifMatch` writes without a condition. Two callers do it, and both
 * on purpose: a brand-new file has no version to condition on, and the
 * editor's overwrite action deliberately drops the condition after a refusal.
 *
 * That second one matters. This server derives a file's change token from
 * metadata, which cannot be exact, and it refuses a conditional write against
 * an inexact token rather than accepting one it cannot honour. Retrying with
 * either the original token or the one the refusal returned is refused again,
 * every time, so the only way past it is a request that asks for no condition
 * at all, made because somebody chose to.
 */
async function writeFile(path: string, content: string, ifMatch?: string): Promise<Entry> {
  // POST. The PUT alias for this handler is gone: one operation, one spelling.
  return entryFromWire(
    await request<WireEntry>('/files/write', {
      method: 'POST',
      body: JSON.stringify({ path, content, if_match: ifMatch })
    })
  )
}

// ── settings ──

async function changePassword(currentPassword: string, newPassword: string, revokeOtherSessions: boolean): Promise<void> {
  await request('/account/password', {
    method: 'POST',
    body: JSON.stringify({
      current_password: currentPassword,
      new_password: newPassword,
      revoke_other_sessions: revokeOtherSessions
    })
  })
}

async function totpSetup(): Promise<{ secret: string; otpauth_url: string }> {
  return request('/account/totp/setup', { method: 'POST' })
}

async function totpEnroll(password: string, secret: string, code: string): Promise<{ recovery_codes: string[] }> {
  return request('/account/totp/enroll', { method: 'POST', body: JSON.stringify({ password, secret, code }) })
}

/**
 * `smb_password_replaced` says a separate SMB password the user had set was
 * removed and replaced by one derived from the password this call just
 * re-confirmed. Turning TOTP off is the exact undo of the event that closed
 * the account password as an SMB credential, so it restores what preceded it.
 */
async function totpDisable(password: string): Promise<{ smb_password_replaced: boolean }> {
  return request('/account/totp/disable', { method: 'POST', body: JSON.stringify({ password }) })
}

/**
 * how many of the 10 recovery codes minted at
 * enrollment (or at the last reissue) are still unused. Not a secret from
 * the account's own owner — unlike the codes themselves, which are never
 * returned again after the moment they're minted — so this is a plain `GET`
 * with no re-confirmation, the same as `session()` above.
 */
async function recoveryCodesRemaining(): Promise<{ remaining: number }> {
  return request('/account/totp/recovery-codes')
}

/**
 * Re-confirms `password`, then replaces every recovery code on the account
 * with a fresh set of 10 — the old list stops working the instant this
 * resolves. Returned exactly once, same handling as `totpEnroll`'s
 * `recovery_codes`: the caller must show them now, there is no way to fetch
 * them again later (only `recoveryCodesRemaining`'s count survives).
 */
async function reissueRecoveryCodes(password: string): Promise<{ recovery_codes: string[] }> {
  return request('/account/totp/recovery-codes', { method: 'POST', body: JSON.stringify({ password }) })
}

async function listAppPasswords(): Promise<AppPasswordInfo[]> {
  return request('/account/app-passwords')
}

async function createAppPassword(name: string): Promise<{ id: number; token: string }> {
  return request('/account/app-passwords', { method: 'POST', body: JSON.stringify({ name }) })
}

/**
 * Scoped app passwords — This is the only place in the
 * frontend that knows the request shape.
 *
 * The server takes a `scope` object: `perms` is the same permission-flag shape
 * as a share link's, and `shares` is a list of **root labels** — the very
 * strings `GET /auth/session` hands back as `roots[].label`. Omitting `scope`
 * means unrestricted, which is what `createAppPassword` above does, so the two
 * differ only by this field.
 *
 * `shares` naming labels rather than ids is deliberate on the server side: a
 * label is what the user actually sees, and an unknown one is refused
 * (`422 auth.unknown_share`) rather than silently dropped: a token that
 * quietly ends up broader than asked for is the failure worth preventing here.
 *
 * A scope with no permission at all is refused too (`422`), because a token
 * that can reach nothing is not a restriction anybody asked for.
 */
async function createScopedAppPassword(
  name: string,
  opts: { readOnly?: boolean; shares?: string[] } = {}
): Promise<{ id: number; token: string }> {
  const scope: Record<string, unknown> = {}
  if (opts.readOnly) scope.perms = { read: true, download: true }
  if (opts.shares?.length) scope.shares = opts.shares
  return request('/account/app-passwords', {
    method: 'POST',
    body: JSON.stringify(Object.keys(scope).length ? { name, scope } : { name })
  })
}

async function revokeAppPassword(id: number): Promise<void> {
  await request(`/account/app-passwords/${id}`, { method: 'DELETE' })
}

/**
 * Mark the device holding this app password as lost.
 *
 * Deliberately not a revoke. A revoked credential can no longer authenticate,
 * so it can no longer ask the server whether it should erase its local copies,
 * and the files on the lost device stay where they are. The credential keeps
 * working until the device reports it is done, and the server retires it then.
 */
async function wipeAppPassword(id: number): Promise<void> {
  await request(`/account/app-passwords/${id}/wipe`, { method: 'POST' })
}

async function listSessions(): Promise<ActiveSession[]> {
  return request('/account/sessions')
}

async function revokeSession(idHash: string): Promise<void> {
  await request(`/account/sessions/${encodeURIComponent(idHash)}`, { method: 'DELETE' })
}

async function updateSmbSettings(optOut: boolean, enabled: boolean): Promise<void> {
  await request('/account/smb', { method: 'POST', body: JSON.stringify({ opt_out: optOut, enabled }) })
}

/**
 * `POST /api/v1/account/smb/password`. Sets an SMB-only password.
 *
 * `currentPassword` is the account password, re-confirmed for the same reason
 * enabling TOTP and linking SSO re-confirm it: a live session alone must not
 * be enough to add a permanent credential.
 *
 * `smb_toggles_cleared` says this account's own SMB switches were off and have
 * been turned back on, because a credential that is never published is not
 * what the request asked for.
 */
async function setSmbPassword(
  currentPassword: string,
  smbPassword: string
): Promise<{ smb_toggles_cleared: boolean }> {
  return request('/account/smb/password', {
    method: 'POST',
    body: JSON.stringify({ current_password: currentPassword, smb_password: smbPassword })
  })
}

/**
 * `DELETE /api/v1/account/smb/password`.
 *
 * `reverted_to_account_password` is `false` for an account that is
 * TOTP-enrolled, OIDC-linked or opted out, which is the case where clearing
 * the separate password means losing SMB access altogether.
 */
async function clearSmbPassword(
  currentPassword: string
): Promise<{ reverted_to_account_password: boolean }> {
  return request('/account/smb/password', {
    method: 'DELETE',
    body: JSON.stringify({ current_password: currentPassword })
  })
}

// ── single sign-on, self-service (`docs/proposals/stowcloud-0-oidc-login.md`
// §4.3.2) ──
//
// `GET /api/v1/auth/oidc/config` and `/start` are not here: they have to work
// before there is a session, so they live in the standalone `api/oidc.ts`
// alongside the login screen's own bundle.

/**
 * `POST /api/v1/account/oidc-link/start`. Re-confirms `password`, records a
 * link-mode flow, and answers the URL to send the browser to.
 *
 * The password is what makes this a `POST` with a body rather than the plain
 * `GET` the login path uses. Linking **adds a permanent credential** to the
 * account, so somebody with a few seconds at an unlocked screen must not be
 * able to attach their own identity and keep coming back after the victim
 * changes their password and revokes every session.
 * charges a password for enabling *and* disabling TOTP for the same reason.
 *
 * The caller does the navigation itself (`window.location.href = authorize_url`)
 * because this response is JSON, not a redirect: unlike `/start` this call
 * carries a password and a CSRF header, so it has to be a `fetch`.
 *
 * Notably absent: any "you are already linked" check. That verdict is the
 * callback's, from `link_oidc_identity`'s return value, and asking here would
 * be TOCTOU against the whole IdP round trip.
 */
async function oidcLinkStart(password: string, returnTo?: string): Promise<{ authorize_url: string }> {
  return request('/account/oidc-link/start', {
    method: 'POST',
    // `return_to`, which is the key the server decodes. It was camelCase here,
    // so the value never arrived and a finished link flow landed on the root
    // instead of the screen it started from.
    body: JSON.stringify(returnTo ? { password, return_to: returnTo } : { password })
  })
}

/**
 * `DELETE /api/v1/account/oidc-link`. Removes the identity, re-derives the SMB NT
 * hash from this password, and revokes every session the IdP issued.
 *
 * The password is not only re-confirmation. §4.3.6 deletes the account
 * password's NT hash when the identity is attached, and the plaintext is the
 * only thing that can put it back, which is why the admin unlink cannot.
 */
async function oidcUnlink(password: string): Promise<{ smb_password_replaced: boolean }> {
  return request('/account/oidc-link', { method: 'DELETE', body: JSON.stringify({ password }) })
}

// ── admin: single sign-on links (§5-1's three admin routes) ──

async function adminGetUserOidc(id: number): Promise<AdminUserOidc> {
  return request(`/admin/users/${id}/oidc`)
}

async function adminUnlinkUserOidc(id: number): Promise<AdminOidcUnlinkResult> {
  return request(`/admin/users/${id}/oidc`, { method: 'DELETE' })
}

/** Storage as the wire sends it: byte counts are decimal strings, because a
 *  volume past 2^53 bytes is not exact as a JavaScript number. */
interface WireStorage {
  db_bytes: string
  shares: Array<{ share: string; label: string; total_bytes: string; free_bytes: string }>
}

async function adminStorage(): Promise<StorageReport> {
  const w = await request<WireStorage>('/admin/storage')
  return {
    db_bytes: Number(w.db_bytes ?? 0),
    shares: (w.shares ?? []).map((s) => ({
      label: s.label,
      free_bytes: Number(s.free_bytes ?? 0),
      total_bytes: Number(s.total_bytes ?? 0)
    }))
  }
}

async function adminIndexEstimate(): Promise<IndexEstimate> {
  return request('/admin/index/estimate')
}

async function adminIndexSettings(): Promise<IndexSettings> {
  // The whole snapshot, of which the index is one section. Settings became one
  // sectioned resource, so there is no endpoint that answers this section
  // alone.
  const snap = await request<{ index?: IndexSettings }>('/admin/settings')
  return snap.index ?? { name_enabled: false }
}

async function adminSetIndexSettings(nameEnabled: boolean): Promise<IndexSettings> {
  await request('/admin/settings/index', {
    method: 'PATCH',
    body: JSON.stringify({ name_enabled: nameEnabled })
  })
  return { name_enabled: nameEnabled }
}

/** `POST /api/v1/admin/index/build` — always crosses the
 *  job threshold (a build walks the whole share by design, `go/internal/search`'s
 *  `CrawlThrottle`), so unlike `copy`/`del`/`archive` there is no inline-result
 *  branch here: the server answers `202 { job }` every time. */
async function adminBuildIndex(): Promise<{ job: string }> {
  return request('/admin/index/build', { method: 'POST' })
}

/** `PATCH /api/v1/admin/settings/upload` — sets the
 *  server-global chunk floor/default every account's `GET /api/v1/auth/session`
 *  reads, persisted across restarts. */
async function adminSetUploadSettings(req: UploadSettingsReq): Promise<UploadSettingsResp> {
  return request('/admin/settings/upload', { method: 'PATCH', body: JSON.stringify(req) })
}

// ── admin: server settings (`go/internal/httpapi/handler/settings.go`) — parity
// with every operator-settable field, live-apply where possible,
// restart-required where not. ──

async function adminGetServerSettings(): Promise<SettingsSnapshot> {
  return request('/admin/settings')
}

async function adminSetSmbSettings(req: SmbSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/smb', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetSearchSettings(req: SearchSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/search', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetArchiveSettings(req: ArchiveSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/archive', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetRateSettings(req: RateSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/rate', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetNetworkSettings(req: NetworkSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/network', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetDbSettings(req: DbSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/db', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetSymlinkPolicySettings(req: SymlinkPolicyReq): Promise<ApplyOutcome> {
  return request('/admin/settings/symlink-policy', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetHomesSettings(req: HomesSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/homes', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetWatchSettings(req: WatchSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/watch', { method: 'PATCH', body: JSON.stringify(req) })
}

/** All of it restart-required, and two of the ten `oidc.*` settings are
 *  missing from the body on purpose. See `OidcSettingsReq`. */
async function adminSetOidcSettings(req: OidcSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/oidc', { method: 'PATCH', body: JSON.stringify(req) })
}

/** Rejects with `422` and a Korean reason whenever the change would leave the
 *  server unable to restart — an unwritable or unmigrated `data_dir`, a
 *  `master_key_file` that is a different key. The reason is meant to be shown
 *  verbatim. */
async function adminSetPathsSettings(req: PathsSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/settings/paths', { method: 'PATCH', body: JSON.stringify(req) })
}

// ── admin: user management ──

/**
 * One account as the admin routes send it.
 *
 * The names are the server's own vocabulary, which is shorter than the app's:
 * `login` rather than `name`, and the flags carry no `_enabled` suffix. Ids
 * and byte counts are decimal strings, for the same 2^53 reason as everywhere
 * else on this API.
 */
interface WireAdminUser {
  id: string
  login: string
  display?: string
  admin: boolean
  disabled: boolean
  totp: boolean
  smb: boolean
  created_ns: string
  quota_bytes?: string | null
  usage_bytes: string
}

function adminUserFromWire(w: WireAdminUser): AdminUser {
  return {
    id: Number(w.id),
    name: w.login,
    display_name: w.display ?? '',
    is_admin: w.admin,
    disabled: w.disabled,
    totp_enabled: w.totp,
    smb_enabled: w.smb,
    created_ns: w.created_ns,
    quota_bytes: w.quota_bytes ?? null,
    usage_bytes: w.usage_bytes ?? '0'
  }
}

async function adminListUsers(): Promise<AdminUser[]> {
  const rows = await request<WireAdminUser[]>('/admin/users')
  return (rows ?? []).map(adminUserFromWire)
}

async function adminCreateUser(name: string, password: string): Promise<AdminUser> {
  return adminUserFromWire(
    await request<WireAdminUser>('/admin/users', {
      method: 'POST',
      body: JSON.stringify({ login: name, password })
    })
  )
}

async function adminSetUserDisabled(id: number, disabled: boolean): Promise<AdminUser> {
  return request(`/admin/users/${id}`, { method: 'PATCH', body: JSON.stringify({ disabled }) })
}

/** `null` clears the quota back to unlimited. */
async function adminSetUserQuota(id: number, quotaBytes: number | null): Promise<AdminUser> {
  return request(`/admin/users/${id}`, { method: 'PATCH', body: JSON.stringify({ quota_bytes: quotaBytes }) })
}

async function adminDeleteUser(id: number): Promise<void> {
  await request(`/admin/users/${id}`, { method: 'DELETE' })
}

/** `PATCH /api/v1/admin/users/{id}` with a password. An administrator resetting an
 *  account they do not have the current password for, which is the only way
 *  back in for somebody who forgot theirs: there is no mail to send a reset
 *  link with. Every session the old password opened is ended server-side. */
async function adminSetUserPassword(id: number, password: string): Promise<AdminUser> {
  return request(`/admin/users/${id}`, { method: 'PATCH', body: JSON.stringify({ password }) })
}

// ── admin: grant management ──
// GET /api/v1/admin/shares, GET/POST /api/v1/admin/grants,
// PATCH/DELETE /api/v1/admin/grants/{id}. The deny-by-default model this
// product is built around: a user has no access to anything until an admin
// grants a specific folder, so these four routes are what turns "created a
// user" into "and here is what they can see".

/** One share as the admin routes send it: string ids, and `broken` for the
 *  reason the app calls `broken_reason`. */
interface WireAdminShare {
  id: string
  name: string
  trash: boolean
  shared_externally?: boolean
  broken?: string
  smb?: AdminShare['smb']
}

function adminShareFromWire(w: WireAdminShare): AdminShare {
  return {
    id: Number(w.id),
    name: w.name,
    trash_enabled: w.trash,
    broken_reason: w.broken,
    smb: w.smb
  }
}

async function adminListShares(): Promise<AdminShare[]> {
  const rows = await request<WireAdminShare[]>('/admin/shares')
  return (rows ?? []).map(adminShareFromWire)
}

/** `POST /api/v1/admin/shares` — register a new folder share ("there is no setting to add folders"). */
async function adminCreateShare(req: CreateShareReq): Promise<AdminShare> {
  return adminShareFromWire(
    await request<WireAdminShare>('/admin/shares', { method: 'POST', body: JSON.stringify(req) })
  )
}

async function adminUpdateShare(id: number, patch: UpdateShareReq): Promise<AdminShare> {
  return adminShareFromWire(
    await request<WireAdminShare>(`/admin/shares/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(patch)
    })
  )
}

async function adminDeleteShare(id: number): Promise<{ smb?: SMBOutcome }> {
  // A delete answers with the republish outcome when there is a sidecar and
  // 204 when there is not, so the body may legitimately be absent.
  return (await request<{ smb?: SMBOutcome } | undefined>(`/admin/shares/${id}`, { method: 'DELETE' })) ?? {}
}

/** `POST /api/v1/admin/shares/{id}/retry` — re-open a share whose disk came
 *  back. Its own route because the ordinary repair is a remount that changes
 *  nothing about the share: making somebody retype a path that was always
 *  right, to prove it, is a screen built around the implementation. */
async function adminRetryShare(id: number): Promise<AdminShare> {
  return request(`/admin/shares/${id}/retry`, { method: 'POST' })
}

/**
 * One grant as the wire sends it.
 *
 * The principal is two optional fields rather than a tagged object: exactly
 * one of `user` and `group` is present, and which one it is says the kind.
 */
interface WireGrant {
  id: string
  user?: string
  group?: string
  share: string
  subpath?: string
  allow: AdminGrant['allow']
  deny: AdminGrant['deny']
  inherit: boolean
  label?: string
  created_ns: string
}

function grantFromWire(w: WireGrant): AdminGrant {
  return {
    id: Number(w.id),
    principal: w.group !== undefined
      ? { kind: 'group', id: Number(w.group) }
      : { kind: 'user', id: Number(w.user ?? 0) },
    share: Number(w.share),
    subpath: w.subpath ?? '',
    allow: w.allow ?? [],
    deny: w.deny ?? [],
    inherit: w.inherit,
    // The app distinguishes "no label" from an empty one, and the wire omits
    // the field rather than sending an empty string.
    label: w.label ?? null,
    created_ns: w.created_ns
  }
}

/** `opts.userId`/`opts.groupId` narrow to one principal's grants, used by the
 *  per-user grant editor; omitted, this is the whole grant table. */
async function adminListGrants(opts: { userId?: number; groupId?: number; share?: number } = {}): Promise<AdminGrant[]> {
  const rows = await request<WireGrant[]>(
    `/admin/grants${qs({ user: opts.userId, group: opts.groupId, share: opts.share })}`
  )
  return (rows ?? []).map(grantFromWire)
}

async function adminCreateGrant(req: CreateGrantReq): Promise<AdminGrant> {
  return request('/admin/grants', { method: 'POST', body: JSON.stringify(req) })
}

async function adminUpdateGrant(id: number, patch: UpdateGrantReq): Promise<AdminGrant> {
  return request(`/admin/grants/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

async function adminDeleteGrant(id: number): Promise<void> {
  await request(`/admin/grants/${id}`, { method: 'DELETE' })
}

// ── admin: group management ──
// GET/POST /api/v1/admin/groups, PATCH/DELETE /api/v1/admin/groups/{id},
// POST /api/v1/admin/groups/{id}/members, DELETE /api/v1/admin/groups/{id}/members/{user}.

async function adminListGroups(): Promise<AdminGroup[]> {
  return request('/admin/groups')
}

async function adminCreateGroup(req: CreateGroupReq): Promise<AdminGroup> {
  return request('/admin/groups', { method: 'POST', body: JSON.stringify(req) })
}

async function adminRenameGroup(id: number, patch: UpdateGroupReq): Promise<AdminGroup> {
  return request(`/admin/groups/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

async function adminDeleteGroup(id: number): Promise<void> {
  await request(`/admin/groups/${id}`, { method: 'DELETE' })
}

async function adminAddGroupMember(id: number, userId: number): Promise<void> {
  await request(`/admin/groups/${id}/members`, { method: 'POST', body: JSON.stringify({ user: userId }) })
}

async function adminRemoveGroupMember(id: number, userId: number): Promise<void> {
  await request(`/admin/groups/${id}/members/${userId}`, { method: 'DELETE' })
}

/** `GET /api/v1/admin/audit[?actor=&event=&since_ns=&until_ns=&before=&limit=]`
 * Every filter field is optional and unfiltered when
 *  omitted; `query.before` is the previous page's `AuditPage.next` for
 *  cursor pagination. */
async function adminListAudit(query: AuditQuery = {}): Promise<AuditPage> {
  return request(
    `/admin/audit${qs({
      actor: query.actor,
      event: query.event,
      since_ns: query.since_ns,
      until_ns: query.until_ns,
      before: query.before,
      limit: query.limit
    })}`
  )
}

/** The real `hit` SSE event (`sc-http::routes::hit_json`) is flat --
 *  `{path, name, is_dir, size, mtime_ns, score}` -- not the `SearchHit`
 *  (`{path, entry: Entry}`) shape the UI reads (mock.ts's `searchStream`
 *  built its hits from full directory-listing `Entry` objects, since the mock
 *  answers search by re-scanning its own seeded tree). Passing the raw event
 *  straight through as `onHit(JSON.parse(...))` left `hit.entry` `undefined`
 *  against the real backend -- every hit's `.entry.kind`/`.name` read as
 *  undefined, so results silently never rendered (the search UI showed
 *  "No results" for a query that had 100+ real matches).
 *  `etag`/`perms`/`id` aren't in the wire shape at all; search doesn't carry
 *  them, and nothing downstream of a search hit (only `onSearchResultClick`,
 *  which reads just `.path`) needs them, so they're synthesized placeholders
 *  rather than guesses at real values. */
interface RawSearchHit {
  share: string
  path: string
  name: string
  is_dir: boolean
  size: number | null
  mtime_ns: string | null
  score: number
}

function toSearchHit(raw: RawSearchHit): SearchHit {
  return {
    // `/{label}/sub/path` is the virtual path the whole UI navigates in.
    // The wire shape keeps the share separate from the share-relative path,
    // so this is where the two are put back together.
    path: raw.share ? normalizePath(`/${raw.share}/${raw.path}`) : normalizePath(raw.path),
    entry: {
      name: raw.name,
      path: raw.share ? `${raw.share}/${raw.path}` : raw.path,
      kind: raw.is_dir ? 'dir' : 'file',
      size: raw.size ?? 0,
      mtime_ns: raw.mtime_ns ?? '0',
      // A search hit carries no change token, so there is nothing to be exact
      // about and nothing here may be used for a conditional write.
      etag: '',
      etag_weak: true,
      perms: { read: true, write: false, create: false, delete: false, rename: false, move: false, share: false, download: false },
      // No numeric fid in the wire shape (see this file's header comment on
      // `RawSearchHit`) — left `undefined` rather than guessed, same as a
      // plain `list`/`stat` result with no allocated fileid. A download
      // action reached from a search result degrades the same honest way it
      // does everywhere else `entry.id` is missing.
      id: undefined
    }
  }
}

/**
 * `GET /api/v1/search/stream`. The `done` event carries `{truncated, tier}`:
 * truncated means the walk hit its deadline, so what arrived is a prefix of
 * the matches and not all of them. Discarding it made a cut-short search
 * render identically to a complete one.
 */
function searchStream(query: string, onHit: (hit: SearchHit) => void, onDone: (done: SearchDone) => void): () => void {
  const es = new EventSource(`${BASE}/search/stream${qs({ q: query })}`, { withCredentials: true })
  es.addEventListener('hit', (ev: MessageEvent) => {
    onHit(toSearchHit(JSON.parse((ev as MessageEvent).data)))
  })
  es.addEventListener('done', (ev: MessageEvent) => {
    let done: SearchDone = { truncated: false }
    try {
      const raw = JSON.parse(ev.data) as { truncated?: unknown; tier?: unknown }
      done = { truncated: raw.truncated === true, tier: typeof raw.tier === 'string' ? raw.tier : undefined }
    } catch {
      // A `done` with no parsable payload still ends the search. Reporting
      // it as complete is the safe read: it claims less, not more.
    }
    onDone(done)
    es.close()
  })
  es.onerror = () => {
    es.close()
    // The stream broke rather than finished, so the result list is a prefix
    // whatever the server would have said.
    onDone({ truncated: true })
  }
  return () => es.close()
}

export const httpApi = {
  session,
  login,
  loginTotp,
  logout,
  list,
  stat,
  mkdir,
  rename,
  copy,
  move,
  movePreflight,
  delete: del,
  archive,
  jobList,
  jobStatus,
  jobCancel,
  trashList,
  trashRestore,
  trashPurge,
  sharesList,
  shareCreate,
  shareUpdate,
  shareDelete,
  readFile,
  writeFile,
  searchStream,
  changePassword,
  totpSetup,
  totpEnroll,
  totpDisable,
  recoveryCodesRemaining,
  reissueRecoveryCodes,
  listAppPasswords,
  createAppPassword,
  createScopedAppPassword,
  revokeAppPassword,
  wipeAppPassword,
  listSessions,
  revokeSession,
  archiveList,
  folderSize,
  thumbUrl,
  recentList,
  updateSmbSettings,
  setSmbPassword,
  clearSmbPassword,
  oidcLinkStart,
  oidcUnlink,
  adminStorage,
  adminIndexEstimate,
  adminIndexSettings,
  adminSetIndexSettings,
  adminBuildIndex,
  adminSetUploadSettings,
  adminGetServerSettings,
  adminSetSmbSettings,
  adminSetSearchSettings,
  adminSetArchiveSettings,
  adminSetRateSettings,
  adminSetNetworkSettings,
  adminSetDbSettings,
  adminSetSymlinkPolicySettings,
  adminSetHomesSettings,
  adminSetWatchSettings,
  adminSetOidcSettings,
  adminSetPathsSettings,
  adminListUsers,
  adminCreateUser,
  adminSetUserDisabled,
  adminSetUserQuota,
  adminDeleteUser,
  adminSetUserPassword,
  adminGetUserOidc,
  adminUnlinkUserOidc,
  adminListShares,
  adminCreateShare,
  adminUpdateShare,
  adminDeleteShare,
  adminRetryShare,
  adminListGrants,
  adminCreateGrant,
  adminUpdateGrant,
  adminDeleteGrant,
  adminListGroups,
  adminCreateGroup,
  adminRenameGroup,
  adminDeleteGroup,
  adminAddGroupMember,
  adminRemoveGroupMember,
  adminListAudit,
  registerUploadedEntry(): void {
    // no-op for the real backend: the server's own state is authoritative;
    // the browse UI calls refresh() after an upload completes instead.
  }
}
