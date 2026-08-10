// web/src/lib/api/http.ts — real HTTP implementation of the same surface as
// mock.ts. Talks to the Rust backend per. This module is only
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
  type ArchiveSettingsReq,
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
  type LinkDisposition,
  type LinkResponse,
  type ListResponse,
  type LoginResult,
  type MovePreflight,
  type MoveReq,
  type NetworkSettingsReq,
  type OidcSettingsReq,
  type PathsSettingsReq,
  type ReadFileResponse,
  type SearchSettingsReq,
  type SessionInfo,
  type SettingsSnapshot,
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
  type UploadSettingsReq,
  type UploadSettingsResp,
  type WatchSettingsReq
} from './types'
import type { ListOpts, SearchHit } from './mock'
import { noteUnauthorized } from '../state/auth.svelte'
import { normalizePath } from './path-utils'

const BASE = (import.meta.env.VITE_API_BASE ?? '') + '/api'

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
  if (!res.ok) {
    const errBody = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
    const err = new ApiError(res.status, errBody)
    // Task: "make a 401 mean something" — every call goes through this one
    // function, so this is the single place a dead/missing session turns
    // into "show the login screen" instead of an inline error string
    // bubbling up into whatever list/table happened to be rendering.
    //
    // Gated on `code`, not just `status`: `auth.invalid_credentials` is also
    // a 401, but it means "this specific re-confirmation was wrong" (a bad
    // *current* password on `/auth/password`, a bad TOTP code on
    // `/auth/totp/enroll`), not "the session cookie is gone". The settings
    // screens for those actions (`PasswordSection`/`TotpSection`) need that
    // error to stay a rejected promise they show inline — bouncing the whole
    // app to the login screen because someone mistyped their *current*
    // password while already logged in would be exactly the "silent
    // failure" this task explicitly rules out.
    if (res.status === 401 && errBody.code === 'auth.required') noteUnauthorized()
    throw err
  }
  return body as T
}

function qs(params: Record<string, string | number | undefined>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

async function session(): Promise<SessionInfo> {
  const s = await request<SessionInfo>('/auth/session')
  setCsrfToken(s.csrf)
  return s
}

async function login(username: string, password: string): Promise<LoginResult> {
  return request('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })
}

async function loginTotp(challenge: string, code: string): Promise<LoginResult> {
  return request('/auth/login/totp', { method: 'POST', body: JSON.stringify({ challenge, code }) })
}

async function logout(): Promise<void> {
  await request('/auth/logout', { method: 'POST' })
}

async function list(path: string, opts: ListOpts): Promise<ListResponse> {
  if (opts.listing) {
    // `offset` takes precedence over `cursor` server-side ('s session already holds the sorted vector, so a random-access
    // slice by index needs no cursor walk) — used for scroll-driven windowed
    // fetches instead of chaining `more()` one page at a time.
    return request(
      `/fs/list${qs({ listing: opts.listing, cursor: opts.cursor, offset: opts.offset, limit: opts.limit })}`,
      { signal: opts.signal }
    )
  }
  return request(`/fs/list${qs({ path, sort: opts.sort, order: opts.order, offset: opts.offset, limit: opts.limit })}`, {
    signal: opts.signal
  })
}

async function stat(path: string): Promise<Entry> {
  return request(`/fs/stat${qs({ path })}`)
}

async function mkdir(path: string): Promise<Entry> {
  return request('/fs/mkdir', { method: 'POST', body: JSON.stringify({ path }) })
}

async function rename(path: string, newName: string): Promise<Entry> {
  return request('/fs/rename', { method: 'POST', body: JSON.stringify({ path, name: newName }) })
}

async function copy(req: MoveReq): Promise<{ job: string }> {
  return request('/fs/copy', { method: 'POST', body: JSON.stringify(req) })
}

async function move(req: MoveReq): Promise<{ job: string }> {
  return request('/fs/move', { method: 'POST', body: JSON.stringify({ ...req, dry_run: false }) })
}

/** The same endpoint with `dry_run`, which answers `MovePreflight` inline
 *  rather than `202 { job }` — see that type for why the caller asks. */
async function movePreflight(req: MoveReq): Promise<MovePreflight> {
  return request('/fs/move', { method: 'POST', body: JSON.stringify({ ...req, dry_run: true }) })
}

async function del(paths: string[], permanent = false): Promise<{ job: string }> {
  return request('/fs/delete', { method: 'POST', body: JSON.stringify({ paths, permanent }) })
}

// ── content links & archive download (§8) ──

/**
 * `POST /api/fs/link` — mints a signed, cookie-free content-origin URL for a
 * single file's bytes (`crates/sc-http/src/routes.rs::fs_link`). Requires
 * `fid`, i.e. `entry.id` — see that field's doc comment in `types.ts` for why
 * it is frequently `undefined` and what the caller should do then (not call
 * this at all; there is no path-based alternative on this endpoint).
 */
async function link(fid: number, disposition: LinkDisposition = 'attachment', dim?: [number, number]): Promise<LinkResponse> {
  return request('/fs/link', {
    method: 'POST',
    body: JSON.stringify({ fid, disposition, dim })
  })
}

/**
 * `POST /api/fs/archive` — always answers `202 { job }` now (`fs_archive`,
 * `crates/sc-http/src/routes.rs`): every archive request is a durable job
 * regardless of size, so there is no synchronous zip stream left to branch
 * on here. `state/job-tray.svelte.ts` tracks the job and fetches the bytes
 * with `jobDownload` once it reports `download: true`.
 */
async function archive(paths: string[]): Promise<{ job: string }> {
  return request('/fs/archive', { method: 'POST', body: JSON.stringify({ paths }) })
}

// ── long-running jobs ──
// Every `fs_move`/`fs_copy`/`fs_delete`/`fs_archive` request always answers
// `202 { job }` (`crates/sc-http/src/routes.rs`) — there is no size/count
// threshold and no synchronous fallback. `copy`/`del`/`archive` above return
// that envelope as-is; `state/job-tray.svelte.ts` is the one caller that
// wraps `state/jobs.ts::pollJob` (REST poll + WS `job` push reconciled) to
// show live progress and calls `jobCancel` on user request.

/** `GET /api/jobs` — every non-terminal job the caller owns. `JobTray` calls
 *  this once on mount to re-attach across a refresh or a server restart
 *  (`crates/sc-http/src/routes.rs::job_list`, `JobStore::list_open`). */
async function jobList(): Promise<JobListResponse> {
  return request('/jobs')
}

async function jobStatus(id: string): Promise<JobStatus> {
  return request(`/jobs/${encodeURIComponent(id)}`)
}

async function jobCancel(id: string): Promise<void> {
  await request(`/jobs/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/** `GET /api/jobs/{id}/download` — one-shot fetch of a finished archive job's
 *  zip bytes, once its tracked status reports `download: true`. */
async function jobDownload(id: string): Promise<Blob> {
  const res = await fetch(`${BASE}/jobs/${encodeURIComponent(id)}/download`, { credentials: 'include' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    const errBody = (body as ApiErrorBody).error ?? { code: 'internal', message: res.statusText }
    throw new ApiError(res.status, errBody)
  }
  return res.blob()
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
  return request(`/shares${qs({ path })}`)
}

async function shareCreate(req: ShareLinkCreateReq): Promise<ShareLinkInfo> {
  return request('/shares', { method: 'POST', body: JSON.stringify(req) })
}

async function shareUpdate(id: number, patch: ShareLinkPatchReq): Promise<ShareLinkInfo> {
  return request(`/shares/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

async function shareDelete(id: number): Promise<void> {
  await request(`/shares/${id}`, { method: 'DELETE' })
}

// ── text editor (`/edit/[...path]`) ──

async function readFile(path: string): Promise<ReadFileResponse> {
  return request(`/fs/read${qs({ path })}`)
}

/** `PUT /api/fs/write`. `ifMatch` undefined means "create, or overwrite
 *  unconditionally" server-side only when the file doesn't exist yet — if it
 *  does exist, the server demands a match and answers `412 fs.precondition`
 *  with the current etag (`CoreError::Precondition`, `crates/sc-core/src/ops.rs`
 *  `write_text`). The editor always has an etag once a file has been opened,
 *  so in practice this is only ever called without one for a brand-new file. */
async function writeFile(path: string, content: string, ifMatch?: string): Promise<Entry> {
  return request('/fs/write', {
    method: 'PUT',
    body: JSON.stringify({ path, content, if_match: ifMatch })
  })
}

// ── settings ──

async function changePassword(currentPassword: string, newPassword: string, revokeOtherSessions: boolean): Promise<void> {
  await request('/auth/password', {
    method: 'POST',
    body: JSON.stringify({
      current_password: currentPassword,
      new_password: newPassword,
      revoke_other_sessions: revokeOtherSessions
    })
  })
}

async function totpSetup(): Promise<{ secret: string; otpauth_url: string }> {
  return request('/auth/totp/setup', { method: 'POST' })
}

async function totpEnroll(password: string, secret: string, code: string): Promise<{ recovery_codes: string[] }> {
  return request('/auth/totp/enroll', { method: 'POST', body: JSON.stringify({ password, secret, code }) })
}

async function totpDisable(password: string): Promise<void> {
  await request('/auth/totp/disable', { method: 'POST', body: JSON.stringify({ password }) })
}

/**
 * how many of the 10 recovery codes minted at
 * enrollment (or at the last reissue) are still unused. Not a secret from
 * the account's own owner — unlike the codes themselves, which are never
 * returned again after the moment they're minted — so this is a plain `GET`
 * with no re-confirmation, the same as `session()` above.
 */
async function recoveryCodesRemaining(): Promise<{ remaining: number }> {
  return request('/auth/totp/recovery-codes')
}

/**
 * Re-confirms `password`, then replaces every recovery code on the account
 * with a fresh set of 10 — the old list stops working the instant this
 * resolves. Returned exactly once, same handling as `totpEnroll`'s
 * `recovery_codes`: the caller must show them now, there is no way to fetch
 * them again later (only `recoveryCodesRemaining`'s count survives).
 */
async function reissueRecoveryCodes(password: string): Promise<{ recovery_codes: string[] }> {
  return request('/auth/totp/recovery-codes', { method: 'POST', body: JSON.stringify({ password }) })
}

async function listAppPasswords(): Promise<AppPasswordInfo[]> {
  return request('/auth/app-passwords')
}

async function createAppPassword(name: string): Promise<{ id: number; token: string }> {
  return request('/auth/app-passwords', { method: 'POST', body: JSON.stringify({ name }) })
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
 * (`422 auth.unknown_share`) rather than silently dropped — a token that
 * quietly ends up broader than asked for is the failure worth preventing here.
 *
 * Enforcement is real, not advisory: verified against a running server, a
 * token scoped to one share is refused on another over both the native API
 * (403) and WebDAV (403), and a read-only one is refused a `PUT` even on the
 * share it does own.
 */
async function createScopedAppPassword(
  name: string,
  opts: { readOnly?: boolean; shares?: string[] } = {}
): Promise<{ id: number; token: string }> {
  const scope: Record<string, unknown> = {}
  if (opts.readOnly) scope.perms = { read: true, download: true }
  if (opts.shares?.length) scope.shares = opts.shares
  return request('/auth/app-passwords', {
    method: 'POST',
    body: JSON.stringify(Object.keys(scope).length ? { name, scope } : { name })
  })
}

async function revokeAppPassword(id: number): Promise<void> {
  await request(`/auth/app-passwords/${id}`, { method: 'DELETE' })
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
  await request(`/auth/app-passwords/${id}/wipe`, { method: 'POST' })
}

async function listSessions(): Promise<ActiveSession[]> {
  return request('/auth/sessions')
}

async function revokeSession(idHash: string): Promise<void> {
  await request(`/auth/sessions/${encodeURIComponent(idHash)}`, { method: 'DELETE' })
}

async function updateSmbSettings(optOut: boolean, enabled: boolean): Promise<void> {
  await request('/auth/smb', { method: 'POST', body: JSON.stringify({ opt_out: optOut, enabled }) })
}

// ── single sign-on, self-service (`docs/proposals/stowcloud-0-oidc-login.md`
// §4.3.2) ──
//
// `GET /api/auth/oidc/config` and `/start` are not here: they have to work
// before there is a session, so they live in the standalone `api/oidc.ts`
// alongside the login screen's own bundle.

/**
 * `POST /api/auth/oidc/link/start`. Re-confirms `password`, records a
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
  return request('/auth/oidc/link/start', {
    method: 'POST',
    body: JSON.stringify(returnTo ? { password, returnTo } : { password })
  })
}

/**
 * `DELETE /api/auth/oidc/link`. Removes the identity, re-derives the SMB NT
 * hash from this password, and revokes every session the IdP issued.
 *
 * The password is not only re-confirmation. §4.3.6 deletes the account
 * password's NT hash when the identity is attached, and the plaintext is the
 * only thing that can put it back, which is why the admin unlink cannot.
 */
async function oidcUnlink(password: string): Promise<void> {
  await request('/auth/oidc/link', { method: 'DELETE', body: JSON.stringify({ password }) })
}

// ── admin: single sign-on links (§5-1's three admin routes) ──

async function adminGetUserOidc(id: number): Promise<AdminUserOidc> {
  return request(`/admin/users/${id}/oidc`)
}

/**
 * `PUT /api/admin/users/{id}/oidc`. Attaches an identity by hand.
 *
 * Only the `subject` is sent: the issuer is this deployment's configured one,
 * never a request field, so that a manual link and one made through the real
 * flow are the same row. It does not contradict "no JIT provisioning" because
 * it creates no account, and it is the recovery path for somebody who does not
 * know their own password and so cannot drive `oidcLinkStart`.
 */
async function adminLinkUserOidc(id: number, subject: string): Promise<void> {
  await request(`/admin/users/${id}/oidc`, { method: 'PUT', body: JSON.stringify({ subject }) })
}

/**
 * `DELETE /api/admin/users/{id}/oidc`. Answers `200` with a body, not `204`:
 * an administrator has no plaintext password, so the NT hash linking deleted
 * cannot be re-derived here and `smb_nt_restored` is always `false`. The
 * caller has to say so to the operator rather than leave SMB quietly broken
 * (§4.3.6).
 */
async function adminUnlinkUserOidc(id: number): Promise<AdminOidcUnlinkResult> {
  return request(`/admin/users/${id}/oidc`, { method: 'DELETE' })
}

async function adminStorage(): Promise<StorageReport> {
  return request('/admin/storage')
}

async function adminIndexEstimate(): Promise<IndexEstimate> {
  return request('/admin/index/estimate')
}

async function adminIndexSettings(): Promise<IndexSettings> {
  return request('/admin/index/settings')
}

async function adminSetIndexSettings(nameEnabled: boolean): Promise<IndexSettings> {
  return request('/admin/index/settings', { method: 'PATCH', body: JSON.stringify({ name_enabled: nameEnabled }) })
}

/** `POST /api/admin/index/build` — always crosses the
 *  job threshold (a build walks the whole share by design, `bridge.rs`'s
 *  `CrawlThrottle`), so unlike `copy`/`del`/`archive` there is no inline-result
 *  branch here: the server answers `202 { job }` every time. */
async function adminBuildIndex(): Promise<{ job: string }> {
  return request('/admin/index/build', { method: 'POST' })
}

/** `PATCH /api/admin/upload-settings` — sets the
 *  server-global chunk floor/default every account's `GET /api/auth/session`
 *  reads, persisted across restarts. */
async function adminSetUploadSettings(req: UploadSettingsReq): Promise<UploadSettingsResp> {
  return request('/admin/upload-settings', { method: 'PATCH', body: JSON.stringify(req) })
}

// ── admin: server settings (`crates/sc-http/src/settings_api.rs`) — parity
// with every operator-settable config.toml field, live-apply where possible,
// restart-required where not. ──

async function adminGetServerSettings(): Promise<SettingsSnapshot> {
  return request('/admin/server-settings')
}

async function adminSetSmbSettings(req: SmbSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/smb', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetSearchSettings(req: SearchSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/search', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetArchiveSettings(req: ArchiveSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/archive', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetNetworkSettings(req: NetworkSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/network', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetDbSettings(req: DbSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/db', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetSymlinkPolicySettings(req: SymlinkPolicyReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/symlink-policy', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetHomesSettings(req: HomesSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/homes', { method: 'PATCH', body: JSON.stringify(req) })
}

async function adminSetWatchSettings(req: WatchSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/watch', { method: 'PATCH', body: JSON.stringify(req) })
}

/** All of it restart-required, and two of the ten `oidc.*` settings are
 *  missing from the body on purpose. See `OidcSettingsReq`. */
async function adminSetOidcSettings(req: OidcSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/oidc', { method: 'PATCH', body: JSON.stringify(req) })
}

/** Rejects with `422` and a Korean reason whenever the change would leave the
 *  server unable to restart — an unwritable or unmigrated `data_dir`, a
 *  `master_key_file` that is a different key. The reason is meant to be shown
 *  verbatim. */
async function adminSetPathsSettings(req: PathsSettingsReq): Promise<ApplyOutcome> {
  return request('/admin/server-settings/paths', { method: 'PATCH', body: JSON.stringify(req) })
}

/** `POST /api/admin/server-settings/restart` — refuses `409 restart.busy`
 *  (detail: `{active_uploads, running_jobs}`) unless `force`. The confirm
 *  dialog's first attempt is always `force: false`; a second, explicit call
 *  with `force: true` only follows the admin accepting that exact refusal. */
async function adminRestartServer(force: boolean): Promise<void> {
  await request('/admin/server-settings/restart', { method: 'POST', body: JSON.stringify({ force }) })
}

// ── admin: user management ──

async function adminListUsers(): Promise<AdminUser[]> {
  return request('/admin/users')
}

async function adminCreateUser(name: string, password: string): Promise<AdminUser> {
  return request('/admin/users', { method: 'POST', body: JSON.stringify({ name, password }) })
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

// ── admin: grant management ──
// GET /api/admin/shares, GET/POST /api/admin/grants,
// PATCH/DELETE /api/admin/grants/{id}. The deny-by-default model this
// product is built around: a user has no access to anything until an admin
// grants a specific folder, so these four routes are what turns "created a
// user" into "and here is what they can see".

async function adminListShares(): Promise<AdminShare[]> {
  return request('/admin/shares')
}

/** `POST /api/admin/shares` — register a new folder share ("there is no setting to add folders"). */
async function adminCreateShare(req: CreateShareReq): Promise<AdminShare> {
  return request('/admin/shares', { method: 'POST', body: JSON.stringify(req) })
}

async function adminUpdateShare(id: number, patch: UpdateShareReq): Promise<AdminShare> {
  return request(`/admin/shares/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

async function adminDeleteShare(id: number): Promise<void> {
  await request(`/admin/shares/${id}`, { method: 'DELETE' })
}

/** `opts.userId`/`opts.groupId` narrow to one principal's grants — used by
 *  the per-user grant editor; omitted, this is the deployment's whole grant
 *  table. */
async function adminListGrants(opts: { userId?: number; groupId?: number; share?: number } = {}): Promise<AdminGrant[]> {
  return request(`/admin/grants${qs({ user: opts.userId, group: opts.groupId, share: opts.share })}`)
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
// GET/POST /api/admin/groups, PATCH/DELETE /api/admin/groups/{id},
// POST /api/admin/groups/{id}/members, DELETE /api/admin/groups/{id}/members/{user}.

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

/** `GET /api/admin/audit[?actor=&event=&since_ns=&until_ns=&before=&limit=]`
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
      kind: raw.is_dir ? 'dir' : 'file',
      size: raw.size ?? 0,
      mtime_ns: raw.mtime_ns ?? '0',
      etag: '',
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

function searchStream(query: string, onHit: (hit: SearchHit) => void, onDone: () => void): () => void {
  const es = new EventSource(`${BASE}/search/stream${qs({ q: query })}`, { withCredentials: true })
  es.addEventListener('hit', (ev: MessageEvent) => {
    onHit(toSearchHit(JSON.parse((ev as MessageEvent).data)))
  })
  es.addEventListener('done', () => {
    onDone()
    es.close()
  })
  es.onerror = () => {
    es.close()
    onDone()
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
  link,
  archive,
  jobList,
  jobStatus,
  jobCancel,
  jobDownload,
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
  updateSmbSettings,
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
  adminSetNetworkSettings,
  adminSetDbSettings,
  adminSetSymlinkPolicySettings,
  adminSetHomesSettings,
  adminSetWatchSettings,
  adminSetOidcSettings,
  adminSetPathsSettings,
  adminRestartServer,
  adminListUsers,
  adminCreateUser,
  adminSetUserDisabled,
  adminSetUserQuota,
  adminDeleteUser,
  adminGetUserOidc,
  adminLinkUserOidc,
  adminUnlinkUserOidc,
  adminListShares,
  adminCreateShare,
  adminUpdateShare,
  adminDeleteShare,
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
