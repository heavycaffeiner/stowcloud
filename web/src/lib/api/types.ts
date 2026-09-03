// web/src/lib/api/types.ts — shapes mirroring.

export type Kind = 'file' | 'dir' | 'symlink' | 'other'

/** The eight permission names, in the order the server emits them. */
export const PERM_NAMES = [
  'read',
  'write',
  'create',
  'delete',
  'rename',
  'move',
  'share',
  'download'
] as const

/** Widens the wire's granted-names array into the object the app reads.
 *
 *  Absent means denied: the server sends only what it granted, so anything
 *  missing from the array is false rather than unknown. An unrecognised name
 *  is ignored rather than refused, because a server that grows a ninth
 *  permission should not break a listing on an older client. */
export function permsFromNames(names: readonly string[] | null | undefined): Perms {
  const held = new Set(names ?? [])
  return {
    read: held.has('read'),
    write: held.has('write'),
    create: held.has('create'),
    delete: held.has('delete'),
    rename: held.has('rename'),
    move: held.has('move'),
    share: held.has('share'),
    download: held.has('download')
  }
}

/** Narrows the app's object back to the array the server accepts. */
export function permNamesOf(perms: Perms): string[] {
  return PERM_NAMES.filter((name) => perms[name])
}

/** The permission set a caller holds at a path, as the app reads it.
 *
 *  All eight keys are always present, so a caller can ask `perms.download`
 *  without testing for the field first. This is the app's shape, not the
 *  wire's: the server sends only the names it granted, as an array, and
 *  `permsFromNames` widens that at the boundary. Keeping the object here is
 *  what lets every call site stay a plain property read. */
export interface Perms {
  read: boolean
  write: boolean
  create: boolean
  delete: boolean
  rename: boolean
  move: boolean
  share: boolean
  download: boolean
}

/** What a listing row says about its thumbnail. Dimensions are not in it: the
 *  server reports whether it can re-encode the file, not what is inside it. */
export interface PreviewInfo {
  available: boolean
}

export interface SymlinkInfo {
  target: string
  openable: boolean
}

export interface Entry {
  name: string
  /** The entry's own path, as the server addresses it: `{label}/rest`. Sent
   *  on every listing row, and what the read and stat endpoints take. The
   *  type omitted it while the server had always sent it, so the client
   *  rebuilt paths by joining the browsed directory to the name. */
  path: string
  kind: Kind
  /**
   * Bytes. For a directory this is the recursive rollup the server keeps, not
   * the directory inode's own size, and it is bytes there too: no entry in
   * this response is ever expressed in blocks.
   */
  size: number
  mtime_ns: string
  /** Absent where the filesystem has no birth time to report. Zero is a real
   *  timestamp, so it cannot stand for "unknown". */
  btime_ns?: string
  etag: string
  /**
   * The change token is advisory rather than exact.
   *
   * Linux exposes no inode change version this server can derive an exact
   * token from, so a file's token comes from metadata that can repeat: two
   * different contents can carry the same one. A conditional write against a
   * token like this is refused by the server rather than accepted, because
   * accepting it would promise a guarantee the token cannot support.
   *
   * The consequence for this client is in the edit route: the refusal is
   * correct, retrying with the same token can never succeed, and only an
   * explicit choice by the person may retry without the condition.
   */
  etag_weak: boolean
  perms: Perms
  /**
   * The stable fileid `POST /api/fs/link` needs as `fid`. **Absent far more
   * often than not** — `go/internal/core/ops.go` only ever
   * *looks up* an existing id (`MetaStore::lookup_fileid`, never
   * `MetaStore::fileid`, the allocating one); `go/internal/store/state`
   * own doc comment says a fileid is allocated lazily, only by "consumers
   * that actually need a stable id" (DAV rename tracking, share-link
   * creation) — "a web-UI-only deployment... creates zero rows". A plain
   * `list`/`stat` on a file nobody has ever shared or touched over WebDAV
   * comes back with no `id` at all (confirmed live: `GET /api/fs/stat`
   * omits the field entirely rather than sending a null). The UI has to
   * treat this as a
   * real, common case — not a shape bug to paper over — and tell the user
   * why a download button doesn't work rather than send `fid: undefined`
   * to a handler that requires it.
   */
  id?: number
  preview?: PreviewInfo
  link?: SymlinkInfo
  confusable?: boolean
}

export type SortKey = 'name' | 'size' | 'mtime' | 'kind'
export type Order = 'asc' | 'desc'

export interface ListResponse {
  total: number
  /**
   * How many of `total` are directories. The server sorts folders ahead of
   * files whichever way the direction toggle runs, so this doubles as the
   * absolute index where files start.
   *
   * The grid view needs that boundary and cannot work it out for itself:
   * rows load in windows, so the entry either side of the split is usually
   * not in memory. Guessing it (or capping folders at "there won't be many")
   * puts a file card in the folder section the moment someone opens a
   * directory holding a few thousand subfolders.
   */
  dirs: number
  /**
   * The next page's cursor, or null on the final page.
   *
   * A walk, not an index: the server orders the whole directory and cuts the
   * page this names. There is no offset into a listing, because there is no
   * listing session on the server to offset into.
   */
  cursor: string | null
  entries: Entry[]
  dir_etag: string
  /** The same advisory rule as `Entry.etag_weak`, for the directory token. */
  dir_etag_weak: boolean
}

export interface ApiErrorBody {
  error: {
    code: string
    message: string
    detail?: Record<string, unknown>
  }
}

export class ApiError extends Error {
  code: string
  detail?: Record<string, unknown>
  status: number
  constructor(status: number, body: ApiErrorBody['error']) {
    super(body.message)
    this.status = status
    this.code = body.code
    this.detail = body.detail
  }

  /** One placeholder from `detail.reason_params`, where the server puts the
   *  values its catalogue key interpolates. They arrive as strings, because a
   *  placeholder is data and renders the same in every language. */
  reasonParam(name: string): string | undefined {
    const params = this.detail?.reason_params
    if (!params || typeof params !== 'object') return undefined
    const v = (params as Record<string, unknown>)[name]
    return typeof v === 'string' ? v : undefined
  }

  /** Same, parsed as a number. Undefined when absent or not numeric. */
  reasonNumber(name: string): number | undefined {
    const raw = this.reasonParam(name)
    if (raw === undefined) return undefined
    const n = Number(raw)
    return Number.isFinite(n) ? n : undefined
  }
}

export interface UserInfo {
  id: number
  name: string
  display_name: string
  is_admin: boolean
  /**: whether TOTP 2FA is currently active on this account. */
  totp_enabled: boolean
  /** — the two self-service SMB toggles. */
  smb_opt_out: boolean
  smb_enabled: boolean
  /**
   * What actually works over SMB right now, not which credential row exists:
   * the deployment's TOTP policy is folded in server-side, because the two can
   * disagree and a line reading "SMB uses a separate password you set" would
   * then be something the user can only disprove by failing to connect.
   */
  smb_credential?: SmbCredential
  /** Present only with `'none'`. */
  smb_unavailable_reason?: SmbUnavailableReason
}

export type SmbCredential = 'account' | 'dedicated' | 'none'

/**
 * An SSO link is deliberately not one of these. A linked account may hold and
 * use a separate SMB password; what the link removes is the account password,
 * which is `'not_set'` until the user sets one.
 */
export type SmbUnavailableReason = 'not_set' | 'totp_blocked' | 'opted_out'

// ── settings ──

export interface AppPasswordInfo {
  id: number
  name: string
  created_ns: string
  last_used_ns: string | null
  expires_ns: string | null
  /** Derived server-side from `scope_perms`: true when the scope holds no
   *  permission that changes anything. An unscoped password is false. */
  read_only?: boolean
}

/** One row of `GET /api/auth/sessions`. Named `ActiveSession`, not
 *  `SessionInfo` — that name is already `SessionInfo` above (the whole
 *  `GET /api/auth/session` envelope), and this is one *row* of the separate
 *  "your other devices" list. */
export interface ActiveSession {
  id_hash: string
  created_ns: string
  last_seen_ns: string
  absolute_expiry_ns: string
  ip_first: string | null
  ua_first: string | null
  current: boolean
}

export interface StorageShareUsage {
  label: string
  free_bytes: number
  total_bytes: number
}

/** `GET /api/admin/storage`. The server today
 *  answers only `db_bytes`/`shares` — the richer shape in the design doc
 *  (breakdown, growth, guard state, preview cache) isn't wired yet; see the
 *  admin settings section's own comment for how it degrades. */
export interface StorageReport {
  db_bytes: number
  shares: StorageShareUsage[]
}

/** `GET/POST /api/admin/index/estimate` — the
 *  projected cost of turning the (off-by-default) name index on. */
export interface IndexEstimate {
  files: number
  index_bytes: number
  /** Processor time. A real build runs only while the server is otherwise
   *  idle, so it finishes later than this. */
  build_secs: number
  /** `high` | `medium` | `low` — how much of the corpus was measured rather
   *  than extrapolated. A code, so the screen picks the wording. */
  confidence: string
}

/** `GET`/`PATCH /api/admin/index/settings` — the
 *  persisted (survives-restart, `index.db`-backed) runtime override for the
 *  off-by-default name index, independent of the stored `[index]
 *  name_enabled`. */
export interface IndexSettings {
  name_enabled: boolean
}

/** `POST /api/v1/files/archive` — where to fetch a selection.
 *
 *  No size: nothing is built until the fetch asks for it, so there is no
 *  figure to report and the download declares no length either. */
export interface ArchiveTicket {
  token: string
  name: string
  /** Absolute from the site root, built by the server so a client does not
   *  assemble the route and get it wrong. */
  url: string
}

/** `PATCH /api/admin/upload-settings` — the
 *  admin-write half of `SessionInfo.limits.chunk_min`/`chunk_size`: this
 *  changes the server-global, persisted value every account's
 *  `GET /api/auth/session` reads, not just this browser's own upload
 *  planner seed. */
export interface UploadSettingsReq {
  chunk_min: number
  chunk_default: number
  /** Spool chunks to the cache volume before they reach the destination.
   *  Omitted leaves it as it was, so saving the chunk sizes cannot silently
   *  turn it off. */
  cache_enabled?: boolean
}

export interface UploadSettingsResp {
  chunk_min: number
  chunk_default: number
  cache_enabled: boolean
  /** False when the deployment has no spool at all, in which case the switch
   *  is shown disabled rather than offered as something that does nothing. */
  cache_available: boolean
}

/** One row of `GET /api/admin/users` — every account on
 *  the deployment, from the admin's point of view. Never carries a password
 *  hash, a TOTP secret, or anything from the SMB secret table; the server
 *  structurally cannot serialize those into this shape
 *  (`go/internal/httpapi/handler/admin_users.go`). */
export interface AdminUser {
  id: number
  name: string
  display_name: string
  is_admin: boolean
  disabled: boolean
  totp_enabled: boolean
  smb_enabled: boolean
  created_ns: string
  /** Per-user quota cap in bytes, as a string (2^53 precision — same
   *  reason `created_ns` is a string). `null` means unlimited
   *. */
  quota_bytes: string | null
  /** Running usage ledger, as a string (same 2^53 reason). Not a live
   *  filesystem recomputation — see `go/internal/core/quota.go`'s module doc for how
   *  it's charged. */
  usage_bytes: string
}

/** One of the eight bits `go/internal/acl` defines (`sc-core::acl_store::PERM_NAMES`),
 *  spelled the way the (forthcoming) admin grant API sends and accepts them —
 *  lowercase, one word, always this exact set of eight. Kept as a union
 *  rather than a bare `string` so a typo in a literal (`"raed"`) is a
 *  compile-time error in this file, not a silent no-op grant. */
export type GrantPermName = 'read' | 'write' | 'create' | 'delete' | 'rename' | 'move' | 'share' | 'download'

export const ALL_GRANT_PERMS: GrantPermName[] = [
  'read',
  'write',
  'create',
  'delete',
  'rename',
  'move',
  'share',
  'download'
]

/** A share this deployment has registered (`go/internal/core`) —
 *  used both by the grant-creation screen's picker (which only reads
 *  `id`/`name`) and by the share management screen, which is why the host path is here — an admin adding/editing a
 *  folder share has to see and set where it points on the host. This is a
 *  deliberate, narrow exception to `sc-vfs`'s "never leak a host path" rule:
 *  that rule is about request-handling responses/errors/logs to non-admins,
 *  not this trusted admin-configuration screen. */
export interface AdminShare {
  id: number
  name: string
  /** Where the share points on the server's disk. Sent only by the
   *  administrative routes, which are administrator-only and session-only, so
   *  an app password never sees it and neither does any surface an ordinary
   *  account reads. */
  host: string
  /** Off by default for every share. */
  trash_enabled: boolean
  /** Why this share cannot be served right now, or absent when it can. A
   *  broken share is still listed: dropping it made a disk that did not come
   *  back look exactly like a share somebody had deleted, with the only trace
   *  a line on the health endpoint. */
  broken_reason?: string
  /** What the SMB republish this write triggered did. Only on write
   *  responses, and only when the deployment has a sidecar. */
  smb?: SMBOutcome
}

/** What a republish to the SMB sidecar did, carried on the response of the
 *  write that triggered it.
 *
 *  It exists because the write never fails on a publish failure: the row is
 *  committed and this server is already serving the change, so refusing would
 *  report a change that happened as one that did not. What was missing was
 *  saying so at the moment it happens rather than on the health page later. */
export interface SMBOutcome {
  /** `applied` is the daemon serving it. `unreachable` is the configuration
   *  written with nothing having applied it. `warnings` is an apply that
   *  happened and found something to fix. */
  state: 'applied' | 'unreachable' | 'warnings'
  /** Where the agent was expected, present only when it could not be reached:
   *  "rendered and nothing applied it" and "the agent answered with a
   *  failure" are different things to go and look at. */
  socket?: string
  report?: SmbApplyReport
}

/** The sidecar's own answer to an apply.
 *
 *  Everything worth knowing about an SMB change is true in the other
 *  container, so this is the daemon's report rather than this server's guess.
 *  `missingPaths` in particular cannot be seen from here: without it the
 *  symptom is a client being told the network name is invalid while every
 *  file on the server's side looks right. */
export interface SmbApplyReport {
  ok: boolean
  shares: string[]
  interfaces: string
  hostsAllow: string
  action: string
  /** Share paths named in the configuration that do not exist where the
   *  daemon runs. */
  missingPaths: string[]
  /** Accounts the credential import produced nothing for, so they cannot
   *  authenticate over SMB. */
  missingCredentials: string[]
  message?: string
}

/** `POST /api/admin/shares` body. */
export interface CreateShareReq {
  name: string
  /** Where the folder lives on the server's disk. The wire calls this `host`;
   *  an earlier spelling of `host_path` was refused by the decoder, which does
   *  not accept unknown fields, so no share could be created from the screen. */
  host: string
}

/** `PATCH /api/admin/shares/{id}` body — all fields optional, so a rename
 *  need not resend the host path and vice versa, and either can be sent
 *  together with or without a trash toggle. */
export interface UpdateShareReq {
  name?: string
  host?: string
  trash_enabled?: boolean
}

/** Who a grant applies to — `go/internal/acl`/`Principal::User`,
 *  the same union `GrantManagementSection.svelte` renders regardless of
 *  which one is reached from (`UserManagementSection`'s per-user entry point
 *  or `GroupManagementSection`'s per-group one). */
export interface GrantPrincipal {
  kind: 'user' | 'group'
  id: number
}

/** One row of `GET /api/admin/groups` (`go/internal/httpapi/handler/admin_users.go`). `members` is a plain id
 *  list, not full `AdminUser` rows — the group screen resolves names against
 *  the user list it already loaded, same as `AdminGrant.share` is an id
 *  resolved against `AdminShare[]` rather than embedded. */
export interface AdminGroup {
  id: number
  name: string
  members: number[]
}

/** `POST /api/admin/groups` body. */
export interface CreateGroupReq {
  name: string
}

/** `PATCH /api/admin/groups/{id}` body — rename only; membership goes
 *  through the `/members` sub-routes instead (`addGroupMember`/
 *  `removeGroupMember`). */
export interface UpdateGroupReq {
  name: string
}

/** One row of `GET /api/admin/audit` (`go/internal/auth/audit.go`). Newest first. `actor`
 *  is `null` for a system-attributed row (e.g. an anonymous share-link
 *  action); `actor_name` is a best-effort resolved display name, `null` for
 *  a since-deleted account too. */
export interface AuditRow {
  rowid: number
  /** Nanosecond timestamp, as a string (2^53 precision — same reason
   *  `AdminUser.created_ns` is a string). */
  ts_ns: string
  actor: number | null
  actor_name: string | null
  event: string
  target: string | null
  ip: string | null
  ok: boolean
  detail: string | null
}

/** `GET /api/admin/audit` query — every field optional/unfiltered when
 *  omitted. `before` is the previous page's last `rowid` (exclusive),
 *  cursor-style rather than offset so a page boundary stays correct even
 *  while new rows keep landing ahead of it. */
export interface AuditQuery {
  actor?: number
  event?: string
  since_ns?: number
  until_ns?: number
  before?: number
  limit?: number
}

/** `GET /api/admin/audit` response shape. `next` is `rows[rows.length -
 *  1].rowid`, present only when the page came back full — pass it as
 *  `before` to fetch the next page; its absence means there is nothing
 *  older left. */
export interface AuditPage {
  rows: AuditRow[]
  next: number | null
}

/** One row of `GET /api/admin/grants` (not yet wired server-side — see
 *  `GrantManagementSection.svelte`'s top comment for the exact contract this
 *  type mirrors, `go/internal/acl` for the server shape it
 *  comes from). `sc-acl`'s depth-first evaluation
 * is keyed on exactly these fields: which
 *  `share`/`subpath` this rule covers, whether it `inherit`s to
 *  descendants, and which bits it `allow`s/`deny`s — same-depth `deny`
 *  always wins over `allow`. */
export interface AdminGrant {
  id: number
  principal: GrantPrincipal
  share: number
  /** Share-relative; `''` means the share's own root. */
  subpath: string
  allow: GrantPermName[]
  deny: GrantPermName[]
  inherit: boolean
  label: string | null
  created_ns: string
}

/** `POST /api/admin/grants` body. */
export interface CreateGrantReq {
  principal: GrantPrincipal
  share: number
  subpath: string
  allow: GrantPermName[]
  deny: GrantPermName[]
  inherit: boolean
  label?: string | null
}

/** `PATCH /api/admin/grants/{id}` body — every field optional, so only what
 *  actually changed needs to be sent. `label: null` explicitly clears it
 *  back to the subpath-basename fallback; omitting the key leaves it alone
 *  (mirrors `go/internal/acl`'s `Option<Option<String>>`). */
export interface UpdateGrantReq {
  allow?: GrantPermName[]
  deny?: GrantPermName[]
  inherit?: boolean
  label?: string | null
}

export interface RootEntry {
  label: string
  perms: Perms
  share_kind: 'Normal' | 'Home' | 'ReadOnly'
  shared_externally: boolean
  /** Whether this share keeps deleted items. Off by default, so the delete
   *  confirmation has to say which of the two it is. */
  trash_enabled: boolean
  /** Why this folder cannot be opened right now, or absent when it can. The
   *  folder stays in the list either way: one whose disk did not come back
   *  used to vanish, which reads as somebody having deleted it rather than as
   *  hardware that needs looking at. */
  broken_reason?: string
}

export interface ClientLimits {
  chunk_size: number
  /** The hard floor a client's 413 shrink-adaptation must not go below
   *  (`chunk-planner.ts::shrinkChunkSize`). */
  chunk_min: number
  max_file_size: number | null
  parallel: number
}

export interface Features {
  webdav: boolean
  smb: boolean
  preview: boolean
  trash: boolean
  shares: boolean
  search: 'walk' | 'name' | 'name+content'
}

/** The caller's own OIDC link, from `GET /api/auth/session`'s `oidc` object
 *  (`SessionOidcWire`, `docs/proposals/stowcloud-0-oidc-login.md` §5-1).
 *
 *  `subject_hint` is four characters from each end of the `sub`, never the
 *  whole identifier. That is enough to recognise which identity is attached, which is
 *  the only question the settings screen asks. The other two fields are absent
 *  (not `null`) when nothing is linked, so both are optional here.
 *
 *  `linked_ns` is a decimal *string* for the reason every other nanosecond
 *  field in this file is: a nanosecond epoch is ~1.8e18 and a JSON number
 *  loses precision past 2^53. */
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

/** `GET /api/auth/oidc/config`. The only thing an anonymous caller learns
 *  about single sign-on: whether to draw the button and what to write on it.
 *  The issuer URL and the client id are deliberately withheld (§5-1). */
export interface OidcConfig {
  enabled: boolean
  display_name: string
}

/** `GET /api/admin/users/{id}/oidc`. The *full* subject, unlike
 *  [`SessionOidc`]'s hint: an administrator working out why somebody cannot
 *  sign in needs the exact string to compare against what the IdP shows.
 *  Every field but `linked` is `null` when the account has no identity. */
export interface AdminUserOidc {
  linked: boolean
  issuer: string | null
  subject: string | null
  linked_ns: string | null
  last_login_ns: string | null
}

/** `DELETE /api/admin/users/{id}/oidc` answers `200` with this, not the `204`
 *  §5-1 first wrote: an admin unlink has no plaintext password, so it cannot
 *  re-derive the SMB NT hash that linking deleted (§4.3.6), and a `204` has no
 *  body to say so in. The wording an admin reads is this app's, keyed off the
 *  flags; the server sends no prose of its own. */
export interface AdminOidcUnlinkResult {
  smb_nt_restored: boolean
  oidc_sessions_revoked: number
}

// ── auth ──

/** The minimal user object `POST /api/auth/login[/totp]` returns — NOT the
 *  same shape as `SessionInfo.user` above (which carries `display_name`/
 *  `is_admin`). The login response is deliberately thin; the app re-fetches
 *  `GET /api/auth/session` right after a successful login to get the rest. */
export interface AuthUser {
  id: number
  name: string
}

/** One check result from `POST /api/v1/system/setup` and the settings saves.
 *
 *  The key and its arguments rather than a rendered sentence: the client owns
 *  the wording and the language. `settings.check_passed` is what the checker
 *  emits when it found nothing to say, so it is the absence of a finding
 *  rather than one. */
export interface SetupFinding {
  section: string
  field?: string
  reason: string
  args?: Record<string, string>
  blocking: boolean
}

/** `POST /api/v1/auth/login` and `.../login/totp`.
 *
 *  Neither response carries a `status` field. A password that verified with no
 *  second factor answers with the session itself, and one that needs a code
 *  answers with `required: "totp"` and the challenge. The discriminator is the
 *  presence of `required`, which is how the two are told apart.
 *
 *  An earlier version declared `{ status: 'ok' } | { status: 'totp_required' }`,
 *  a shape no response has ever had, so `status === 'ok'` was never true and
 *  the first-run screen concluded its own sign-in had failed. */
export type LoginResult =
  | { required?: undefined; id: string; login: string; admin: boolean; csrf: string }
  | { required: 'totp'; challenge: string; expires_in_seconds: number }

/**
 * What a move or a copy does when the destination name is taken.
 *
 * Lowercase, which is what the server parses. It was capitalised here, and the
 * server compared against the lowercase spelling: every answer other than the
 * default was read as "fail", so choosing overwrite in the conflict dialog
 * re-opened the same dialog forever and "keep both" never renamed anything.
 */
export type OnConflict = 'fail' | 'rename' | 'overwrite' | 'skip'

/** `go/internal/httpapi/handler/ops.go` — the one per-item result shape
 *  `/fs/delete`, `/fs/move`, `/fs/copy`, `/trash/restore` and `/trash/purge`
 *  all share, sent as `{"results": [...]}`. This used to be
 *  declared as `{ path, status: 'ok' | 'error', error? }` — a shape nothing
 *  on the server has ever sent; the real field is `ok: boolean`, confirmed
 *  live (`{"results":[{"ok":true,"path":"..."}]}` /
 *  `{"results":[{"error":{"message":"not found"},"ok":false,"path":"..."}]}`).
 *  `(app)/b/[...path]/+page.svelte`'s `duplicate()` checked
 *  `item.status === 'error'` to decide whether to open the conflict-resolve
 *  dialog — against the real backend that was always `undefined === 'error'`
 *  (false), so a copy conflict never opened the dialog it exists for. Fixed
 *  alongside this type. */
export interface BatchItemResult {
  path: string
  ok: boolean
  error?: ApiErrorBody['error']
  /** Only ever `true` (server omits the key otherwise, `skip_serializing_if
   *  = "std::ops::Not::not"`) — `CoreError::CrossDevice`'s cheap same-call
   *  signal that a move degraded into a copy. */
  will_copy?: boolean
  /** The destination was taken and `on_conflict: 'skip'` left it alone. Rides
   *  beside `ok: true`, because nothing failed and nothing was written: a
   *  screen reporting what happened has to tell the two apart. */
  skipped?: boolean
}

export interface BatchResult {
  results: BatchItemResult[]
}

/**
 * `POST /api/fs/copy`. The destination is checked before any job exists, so a
 * conflict, a denial or a quota refusal is here rather than in a job that has
 * already started copying.
 *
 * `job` is absent when nothing started: every item refused, or every item
 * skipped because its destination was taken and the request said to leave it.
 * `jobs` carries every id when the batch had several sources; `job` is the
 * first, which is the one the tray tracks.
 */
export interface CopyResult {
  results: BatchItemResult[]
  job?: string
  jobs?: number[]
}

// ── trash ──

/** One row of `GET /api/trash` (`go/internal/httpapi/handler/trash.go`).
 *  `id` is an opaque string (`"{share}:{uuid}"` — `go/internal/httpapi/handler/trash.go`
 *  `trash_list`), never a `FileId`: a trashed item has no live fileid to be
 *  addressed by, so it must be round-tripped verbatim to `/trash/restore`
 *  and `/trash/purge` rather than parsed. */
export interface TrashEntry {
  id: string
  name: string
  /** A directory's own size is its listing's inode bytes, not what it holds,
   *  so the screen does not show it. */
  is_dir: boolean
  size: number
  /** When it was moved into the trash. Nanoseconds as a string, same rule as
   *  `Entry.mtime_ns`. Was `deleted_mtime_ns`, which carried the file's own
   *  mtime: a move does not touch that, so a file edited a year ago and
   *  deleted a minute ago listed as deleted a year ago. */
  deleted_at_ns: string
}

// ── archive listing ──

/** One entry of `GET /api/fs/archive/list`. Not openable: opening an entry
 *  means extraction, which this server does not do. */
export interface ArchiveEntry {
  name: string
  size: number
  kind: 'file' | 'dir'
}

/** `GET /api/fs/archive/list`. */
export interface ArchiveListing {
  entries: ArchiveEntry[]
  /**
   * The listing stopped at `limit` rather than being refused, so what is here
   * is the first `limit` entries and not the whole archive. Saying so is the
   * point: a truncated listing that does not admit it reads as an archive with
   * fewer files in it.
   */
  truncated: boolean
  /** The bound `truncated` was measured against. */
  limit: number
  /** Entries the server left out because their names cannot be handed out
   *  safely: a path escape, a raw Windows separator, a symlink, a device
   *  node. Counted rather than fatal, so one odd entry does not hide the
   *  archive. Absent on an older server. */
  skipped?: number
}

// ── folder size ──

/** `GET /api/fs/size`. No directory count: the server keeps a single recursive
 *  count with no file/directory split, so reporting one would be a number it
 *  does not have. */
export interface FolderSize {
  bytes: number
  files: number
}

// ── recent files ──

export type RecentOp = 'upload' | 'edit' | 'copy' | 'move' | 'restore'

/**
 * `GET /api/recent` query.
 *
 * The window is an instant rather than a number of days. A day count has to be
 * resolved against somebody's clock, and the two ends of this wire are in
 * different time zones as often as not, so "the last 7 days" meant two
 * different windows depending on which side did the arithmetic.
 */
export interface RecentQuery {
  /**
   * The oldest write to return, as an ISO-8601 instant with an offset. The
   * server compares it against a stored instant; neither side converts to a
   * local calendar date on the way.
   */
  since?: string
  limit?: number
  scope?: string
}

export interface RecentHit {
  /** Navigable virtual path, `{label}/{rest}`. */
  vpath: string
  share: string
  /**
   * The path within the share, sent explicitly rather than left for this
   * client to cut out of `vpath`.
   *
   * Splitting on the first separator is wrong the moment a share label
   * contains one, and the client cannot tell which separators are part of the
   * label. One vocabulary, decided by the side that knows.
   */
  subpath: string
  name: string
  size: number
  /** Nanoseconds as a string, same rule as `Entry.mtime_ns`. */
  mtime_ns: string
  /** When the write happened, which is not `mtime_ns` for a restore or a copy
   *  that preserved timestamps. */
  at_ns: string
  op: RecentOp
}

// ── content links ──

// ── share links, owner side ──

/** `POST/PATCH /api/shares[/:id]` request body's `perms` field
 *  (`go/internal/httpapi/handler/shares.go`) — every key optional/defaults
 *  to `false` server-side, so only the bits actually being granted need to be
 *  sent. Deliberately not reusing `Perms` (whose 8 keys are all required) —
 *  a share link request is a sparse grant, not a full permission snapshot. */
export type PermsReq = Partial<Perms>

/** One share link as its owner sees it (`GET/POST/PATCH /api/shares[/:id]`,
 *  `go/internal/httpapi/handler/shares.go`). `token`/`url` are only
 *  ever populated on the response to the `POST` that created it — the
 *  plaintext token is generated once and never persisted, so no later `GET`
 *  can produce it again. */
export interface ShareLinkInfo {
  id: number
  path: string
  perms: Perms
  expires_ns: string | null
  max_downloads: number | null
  downloads: number
  label: string | null
  has_password: boolean
  created_ns: string
  token?: string
  url?: string
}

/** `POST /api/shares` body (`go/internal/httpapi/handler/shares.go`). */
export interface ShareLinkCreateReq {
  path: string
  perms?: PermsReq
  password?: string
  /** Nanoseconds as a string. */
  expires_ns?: string
  max_downloads?: number
  label?: string
}

/**
 * `PATCH /api/shares/{id}` body (`ShareLinkPatch`). Every field is a
 * *double* option server-side (`Option<Option<T>>`): the key being **absent**
 * means "leave alone", present with a value means "set it", present as
 * `null` means "clear it". `JSON.stringify` already gives this for free —
 * an `undefined` value drops the key, a `null` value keeps it — so callers
 * just leave a field `undefined` to not touch it and pass `null` to clear
 * `password`/`expires_ns`/`max_downloads`/`label`.
 */
export interface ShareLinkPatchReq {
  perms?: PermsReq
  password?: string | null
  expires_ns?: string | null
  max_downloads?: number | null
  label?: string | null
}

export interface MoveReq {
  paths: string[]
  dest: string
  on_conflict: OnConflict
  dry_run?: boolean
}

/**
 * What `POST /api/fs/move` answers when `dry_run` is
 * set (`go/internal/httpapi/handler/fs.go`). A move whose source and
 * destination sit on different filesystems cannot be a rename — the server
 * falls back to copy-then-delete, which reads and rewrites every byte and
 * bills the copy against quota until the source is gone. That turns an
 * instant operation into a minutes-long job, so the picker asks first and
 * tells the user before they commit.
 */
export interface MovePreflight {
  /** One entry per requested path, in request order. `will_copy` on an item
   *  is the cross-device warning; `ok: false` is a path the move would refuse
   *  outright, which the picker can show before anybody commits. */
  results: BatchItemResult[]
}

// ── long-running jobs ──

export type JobState = 'running' | 'done' | 'error' | 'cancelled' | 'interrupted'

/** Wire values of `go/internal/httpapi/handler/admin_ops.go`. */
export type JobKindWire = 'copy' | 'move' | 'delete' | 'archive' | 'index_build'

/** `GET /api/jobs/{id}` (`go/internal/httpapi/handler/admin_ops.go`,
 *  `JobStatus::done_total_json`). Every `fs_move`/`fs_copy`/`fs_delete`/
 *  `fs_archive` request always answers `202 { "job": "J-…" }` — there is no
 *  synchronous fallback, so this envelope is the *only* shape a caller of
 *  those endpoints ever sees. `http.ts`'s wrappers hand the id straight to
 *  `jobTray.track()`; `pollJob` (`state/jobs.ts`) is the lower-level piece
 *  that actually polls this endpoint until a terminal state. */
export interface JobStatus {
  id: string
  kind: JobKindWire
  state: JobState
  done: number
  total: number
  current: string | null
  errors: string[]
  /** Same per-item shape the synchronous copy/move/delete endpoints used to
   *  return inline — populated once `state` is terminal. */
  results: BatchItemResult[]
  /** Paths `begin_result` recorded but the process never reached
   *  `finish_result` for — only ever non-empty on an `interrupted` job. */
  attempting: string[]
  /** Paths recorded when the job was created that the runner never reached —
   *  non-empty on an `interrupted` or `cancelled` job. `results` +
   *  `attempting` + `pending` accounts for every path the request asked for,
   *  which is what lets the tray say *what* is left to redo rather than only
   *  how many items are missing. */
  pending: string[]
}

/** `GET /api/jobs` (`go/internal/httpapi/handler/ops.go`) — every
 *  non-terminal job (`running`/`interrupted`) the caller owns. `JobTray`
 *  fetches this once on mount to re-attach across a browser refresh *or* a
 *  server restart (a Docker cutover), since `jobs.db` — not the browser — is
 *  the durable record. */
export interface JobListResponse {
  jobs: JobStatus[]
}

// ── live change notifications (`GET /api/events`,,
// `go/internal/httpapi/ws/ws.go`) — a WebSocket, not SSE,
// despite `search/stream` above using SSE for a superficially similar
// "server pushes named events" shape: this one is bidirectional (the client
// sends `sub`/`unsub`/`ping`), which SSE cannot do. ──

// What the hub actually sends today is `inval` and `pong`
// (`go/internal/httpapi/ws/ws.go`). The other three were declared here and
// handled in `state/events.ts` against a server that never sent them, which
// made the polling fallback look like a redundancy rather than the only path.
export type ServerMsg =
  // No etag on the frame: the hub sends the path that changed and the client
  // re-reads it, because the token it would carry is one directory read old
  // by the time it arrives.
  | { t: 'inval'; path: string }
  | { t: 'pong' }

export type ClientMsg = { t: 'sub'; paths: string[] } | { t: 'unsub'; paths: string[] } | { t: 'ping' }

// ── text editor (`/edit/[...path]`) ──

/** `GET /api/v1/files/read` response shape. No etag on purpose — the server
 *  derives it fresh from `stat` at write time (`AppError::precondition`), so
 *  the editor calls `api.stat(path)` alongside this to get one to send back
 *  as `If-Match`. */
export interface ReadFileResponse {
  content: string
}

// ── server settings (`go/internal/httpapi/handler/settings.go`) — the admin
// screen's parity with every operator-settable field. One flat
// field list, keyed with the same dotted path the store uses, so
// an operator can cross-reference the two; each field carries where its
// current value came from and whether changing it needs a restart. ──

/** Where a settings value came from (`go/internal/httpapi/handler/settings.go`). */
export type SettingsSource = 'builtin_default' | 'admin_override'

/** One row of `GET /api/admin/server-settings`. A field this screen can't
 *  safely change is still listed, never hidden; `readonly_reason_key` is then
 *  a catalogue key (`api/error-text.ts`'s allowlist), not display text — the
 *  server has no idea which language the reader picked. */
/**
 * The declared range of a settable field, discriminated by the kind of value
 * it holds.
 *
 * The bound is sent rather than compiled into this screen. Every bound is a
 * named constant on the server and a patch outside one is refused naming the
 * field, so a client carrying its own copy of the number is a client that
 * disagrees with the server the first time one of them moves.
 */
export type SettingRange =
  | { kind: 'int'; min: number; max: number }
  | { kind: 'float'; min: number; max: number }
  | { kind: 'duration_ms'; min: number; max: number }
  | { kind: 'bool' }
  | { kind: 'string'; max_len?: number }
  | { kind: 'string_list'; max_items?: number }
  /** A string with a fixed set of accepted values. The server sends the set,
   *  so a client never offers an option the save would refuse. */
  | { kind: 'choice'; choices: string[] }

export interface SettingsField {
  key: string
  value: unknown
  /**
   * What this field will accept. Absent for a field with no declared range,
   * which the screen renders without a validator rather than inventing one.
   */
  range?: SettingRange
  source: SettingsSource
  /** A static property of the field, not a statement about this value. What
   *  the process is actually on is `running_value`. */
  restart_required: boolean
  /** Present only when the saved value differs from the one the running
   *  process is using, because the change needs a restart that has not
   *  happened. Absent in the ordinary case, and on older servers. */
  running_value?: unknown
  /** What leaving this field empty does, for the fields where empty is a
   *  setting rather than a gap: an empty proxy list trusts no proxy, an empty
   *  NetBIOS name turns the name service off. A catalogue key. Absent on
   *  every other field, where empty just means unset. */
  empty_means_key?: string
  /** Absent when this build's catalogue does not attach a reason to a
   *  read-only field, and absent on every editable field. `null` and
   *  absent are read alike: the screen shows no reason either way. */
  readonly_reason_key?: string | null
}

/** The setting groups this server recognises. */
export type SettingsSectionId =
  | 'network'
  | 'db'
  | 'symlink-policy'
  | 'homes'
  | 'smb'
  | 'search'
  | 'archive'
  | 'watch'
  | 'paths'
  | 'oidc'
  | 'rate'
  | 'security'

/** One thing the server learned by trying the change rather than describing
 *  it. `blocking` refuses the save; anything else is saved and reported. */
export interface SettingsFinding {
  /** The group the finding is about. */
  section: string
  /** The input to put this beside, absent when it is about the whole group. */
  field?: string
  /** A catalogue key, not a sentence: the server has no idea which language
   *  the reader picked. */
  reason: string
  /** Placeholders for the key above. */
  args?: Record<string, string>
  blocking: boolean
}

export interface SettingsSnapshot {
  fields: SettingsField[]
  /** `go/internal/smb` — the same
   *  permanent banner `smb-sync`'s own log line describes. */
  smb_public_bind_warning: boolean
  /** Grants Samba was handed more permissively than they were written here,
   *  because `smb.conf` cannot express the difference. Empty in the ordinary
   *  case; older servers omit the field entirely. */
  smb_overgrants?: SmbOvergrant[]
  /** What the SMB agent did with the files the last render produced.
   *  Absent when SMB is off, when no agent is deployed, or on an older
   *  server. Everything in it is true of the machine smbd runs on, which is
   *  not the one that rendered the files. */
  smb_agent?: SmbAgentReport
}

/** One answer from `sc-smb-agent`. `key` is a catalogue key; `detail` is a
 *  diagnostic from testparm, pdbedit or the agent itself and is shown
 *  verbatim, not translated. */
export interface SmbAgentReport {
  key: string
  ok: boolean
  /** The `[section]` names smbd is serving. */
  shares: string[]
  /** The addresses smbd ended up bound to, after the agent expanded the
   *  loopback-only baseline this server renders. */
  interfaces: string
  hosts_allow: string
  /** `unchanged`, `reloaded`, `restarted`, `started`, `stopped` or `failed`. */
  smbd: string
  /** Share paths that do not exist where smbd runs. A client asking for one
   *  of these is told the network name is invalid. */
  missing_paths: string[]
  /** Accounts with no passdb entry, which cannot authenticate. */
  missing_passdb: string[]
  detail?: string | null
}

/** `key` is a catalogue key, `detail` its placeholders — the server never
 *  sends the sentence. */
export interface SmbOvergrant {
  share: string
  user: string
  key: string
  detail: string[]
}

/** What a save did.
 *
 *  Stored and applied are separate facts: a save can reach the database and
 *  fail to reach the running process, and somebody told only "saved" would
 *  believe the change is live. `restart_required` is the third outcome, not a
 *  failure to apply, and folding it into either of the others loses it. */
export interface ApplyOutcome {
  /** The change reached the database. */
  stored: boolean
  /** The running process took it. */
  applied: boolean
  /** It needs a restart to take effect. */
  restart_required: boolean
  /** What a restart would interrupt, present only when one is required, so
   *  the operator decides rather than the server deciding for them. */
  active_uploads?: number
  active_jobs?: number
  findings: SettingsFinding[]
}

/** `GET /api/v1/system/health` — the container probe's own projection.
 *  Unauthenticated and unauthenticated-safe: a fixed status plus a fixed
 *  vocabulary of reason tokens, nothing that names a path or an account.
 *  Polled by the restart dialog because the socket is expected to drop and
 *  come back while the process re-execs itself. */
export interface SystemHealth {
  status: 'ok' | 'degraded' | 'failing'
  reasons: string[]
}

/** `POST /api/v1/admin/system/restart` response. `202` before the process
 *  goes down, so the counts describe what is already being interrupted by
 *  the time this is read, not a preview to decide against — the decision
 *  already happened at the confirm dialog. */
export interface SystemRestartResult {
  restarting: boolean
  active_uploads: number
  active_jobs: number
}

/** `PATCH /api/v1/admin/settings/smb` body. The publisher is assembled once,
 *  so this section is stored and waits for the next start; the response says
 *  what a restart would interrupt and the operator takes it. */
export interface SmbSettingsReq {
  enabled: boolean
  workgroup: string
  /** NetBIOS name clients can open `\\NAME\share` by. Required, like every
   *  other field: it used to be optional, and the server filling in the
   *  absent value froze the name into the stored override on the first save
   *  of any other SMB setting. */
  server_name: string
  service_user: string
  allow_public_bind: boolean
  totp_policy: 'require_separate' | 'block'
  service_gid: number
  /** Pins which addresses the sidecar binds, replacing its own device
   *  detection. Empty leaves detection in charge. */
  interfaces: string[]
}

/** `PATCH /api/admin/server-settings/search` body (`SearchPatch`) — fully
 *  live. */
export interface SearchSettingsReq {
  max_concurrent_fast: number
  walk_deadline_fast_ms: number
}

/** `PATCH /api/admin/server-settings/archive` body (`ArchivePatch`) — fully
 *  live. */
export interface ArchiveSettingsReq {
  max_concurrent: number
}

/** `PATCH /api/admin/server-settings/rate` body. Applies live: the limiter is
 *  the same instance the chain holds. */
export interface RateSettingsReq {
  per_sec: number
  burst: number
}

/**
 * `PATCH /api/admin/server-settings/network` body.
 *
 * Every field here is a holder the request chain reads live, so a save
 * applies at once with no restart. `bind` is the one exception in kind
 * rather than in timing: saving it moves the socket, binding the new
 * address before it drops the old one, so a refused bind leaves the server
 * reachable where it was.
 */
export interface NetworkSettingsReq {
  /** The host names this server answers on, which is also the origin check
   *  every state-changing request passes. An empty list admits nothing. */
  app_hosts: string[]
  /** The cookie-free compatibility content origin(s), disjoint from
   *  `app_hosts`: one TLS name cannot carry both roles. */
  content_hosts: string[]
  /** CORS-readable public compatibility responses only. Never widens the
   *  host guard and never confers credential trust. */
  allowed_origins: string[]
  /** The fallback origin used when a request's own is unavailable. Must
   *  name one of `app_hosts`. */
  compat_canonical_url: string
  /** CIDR ranges whose `X-Forwarded-For` is believed. Empty trusts no proxy,
   *  so the peer address is the client address. */
  trusted_proxies: string[]
  /** host:port the listener binds. Omitted leaves it where it is. */
  bind?: string
}

/** `PATCH /admin/settings/db` body (`DbPatch`). The switch and both bounds
 *  reach the running size-guard sampler directly, so a save applies without
 *  a restart. */
export interface DbSettingsReq {
  size_guard: boolean
  max_bytes: number
  min_free_bytes: number
}

/** `PATCH /admin/settings/thumbnail` body. Controls whether thumbnails are generated
 *  and overrides the storage directory. */
export interface ThumbnailSettingsReq {
  enabled: boolean
  dir?: string
}

// symlink-policy has no client request type. `symlinkPolicy` is a per-share
// row (`AdminShare`'s own creation flow decides it), read from `share_definition`
// rather than the settings document; a PATCH to `/admin/settings/symlink-policy`
// stores a value nothing loads. See `go/tools/settingscheck`'s allow-list.

/** `PATCH /api/admin/server-settings/homes` body (`HomesPatch`). The homes
 *  share is registered at startup, so this restarts the process. */
export interface HomesSettingsReq {
  enabled: boolean
  root: string | null
}

/** `PATCH /api/admin/server-settings/watch` body (`WatchPatch`). Both bounds
 *  reach the running watcher directly, so a save applies without a restart.
 *  `backend` is not here: this build implements exactly one transport
 *  (inotify), nothing reads a stored choice, and the catalogue does not
 *  report the field. */
export interface WatchSettingsReq {
  hot_set_max: number
  full_threshold: number
}

/** `PATCH /api/admin/server-settings/oidc` body (`OidcPatch`): the eight
 *  rows §6-4 marks UI-editable. The provider is rebuilt when settings load,
 *  so a save applies without a restart.
 *
 *  The other two `oidc.*` settings are not here on purpose.
 *  `oidc.client_secret_file` is the path to a secret, and
 *  `oidc.local_password_login` would be unrecoverable if this screen could
 *  write it: an admin override beats the compiled-in default on every boot, so setting
 *  it to `deny` here and then losing the IdP would lock everyone out with no
 *  way back in (§4.3.5). The server refuses both fields; this type refuses to
 *  offer them. */
export interface OidcSettingsReq {
  enabled: boolean
  issuer: string
  client_id: string
  /** Each must match what is registered at the IdP byte for byte, start with
   *  `https://`, and name a host `app_hosts` admits. The entry matching the
   *  request's `Host` is used, the first otherwise. */
  redirect_uris: string[]
  /** `openid` is always included server-side whether or not it is listed. */
  scopes: string[]
  display_name: string
  allow_private_endpoints: boolean
  /** `"block"` is the only accepted value (§4.3.6). */
  smb_policy: 'block'
}

// paths has no client request type either. `data_dir` arrives as a process
// argument, and `smb.config_dir` is read from the `smb` section (see
// `SmbSettingsReq`), not from a `paths` section: a write to
// `/admin/settings/paths` is accepted, stored, and read by nothing. Both
// stay reported read-only, under `PATH_KEYS` in
// `ui/admin/ServerSettingsSection.svelte`.
