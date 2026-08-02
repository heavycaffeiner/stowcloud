// web/src/lib/api/types.ts — shapes mirroring DESIGN-API.md.

export type Kind = 'file' | 'dir' | 'symlink' | 'other'

/** `crates/sc-http/src/core_api.rs::perms_to_json` — the wire shape of every
 *  `perms` object the server emits (an `Entry`'s, a session root's, a share
 *  link's): always exactly these eight keys. The previous version of this
 *  interface had only five (missing `rename`/`move`/`download`) — harmless
 *  as long as nothing read those fields, but the download feature needs
 *  `perms.download` to decide whether to offer the action at all, which is
 *  what surfaced the gap. */
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

export interface PreviewInfo {
  available: boolean
  width?: number
  height?: number
}

export interface SymlinkInfo {
  target: string
  openable: boolean
}

export interface Entry {
  name: string
  kind: Kind
  size: number
  mtime_ns: string
  etag: string
  perms: Perms
  /**
   * The stable fileid `POST /api/fs/link` needs as `fid`. **Absent far more
   * often than not** — `crates/sc-core/src/ops.rs::build_entry` only ever
   * *looks up* an existing id (`MetaStore::lookup_fileid`, never
   * `MetaStore::fileid`, the allocating one); `crates/sc-meta/src/node.rs`'s
   * own doc comment says a fileid is allocated lazily, only by "consumers
   * that actually need a stable id" (DAV rename tracking, share-link
   * creation) — "a web-UI-only deployment... creates zero rows". A plain
   * `list`/`stat` on a file nobody has ever shared or touched over WebDAV
   * comes back with no `id` at all (confirmed live: `GET /api/fs/stat`
   * omits the field entirely; `#[serde(skip_serializing_if =
   * "Option::is_none")]` on the Rust side). The UI has to treat this as a
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
  listing: string
  total: number
  cursor: string | null
  entries: Entry[]
  dir_etag: string
  stale?: boolean
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
}

export interface UserInfo {
  id: number
  name: string
  display_name: string
  is_admin: boolean
  /** `DESIGN-AUTH.md` §6: whether TOTP 2FA is currently active on this account. */
  totp_enabled: boolean
  /** `DESIGN-AUTH.md` §2.4 — the two self-service SMB toggles. */
  smb_opt_out: boolean
  smb_enabled: boolean
}

// ── settings (DESIGN-AUTH.md §2/§5/§6, DESIGN-FOOTPRINT.md §4) ──

export interface AppPasswordInfo {
  id: number
  name: string
  created_ns: string
  last_used_ns: string | null
  expires_ns: string | null
  /** `DESIGN-AUTH.md` §5.2 (`scope_perms`). Optional because today's server
   *  doesn't emit it yet — see `createScopedAppPassword`'s header comment in
   *  `client.ts` for the seam that will fill this in. Absent/`undefined` and
   *  `false` both render as an unscoped (full-access) password. */
  read_only?: boolean
}

/** One row of `GET /api/auth/sessions`. Named `ActiveSession`, not
 *  `SessionInfo` — that name is already `SessionInfo` above (the whole
 *  `GET /api/auth/session` envelope), and this is one *row* of the separate
 *  "your other devices" list (`DESIGN-AUTH.md` §3.2/§10, FEATURES #54). */
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

/** `GET /api/admin/storage` (`DESIGN-FOOTPRINT.md` §4.2). The server today
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

/** `GET`/`PATCH /api/admin/index/settings` (`FEATURES.md` #116) — the
 *  persisted (survives-restart, `index.db`-backed) runtime override for the
 *  off-by-default name index, independent of `config.toml`'s `[index]
 *  name_enabled`. */
export interface IndexSettings {
  name_enabled: boolean
}

/** `PATCH /api/admin/upload-settings` (`DESIGN-UPLOAD.md` §1.3) — the
 *  admin-write half of `SessionInfo.limits.chunk_min`/`chunk_size`: this
 *  changes the server-global, persisted value every account's
 *  `GET /api/auth/session` reads, not just this browser's own upload
 *  planner seed. */
export interface UploadSettingsReq {
  chunk_min: number
  chunk_default: number
}

export type UploadSettingsResp = UploadSettingsReq

/** One row of `GET /api/admin/users` (`FEATURES.md` #157) — every account on
 *  the deployment, from the admin's point of view. Never carries a password
 *  hash, a TOTP secret, or anything from the SMB secret table; the server
 *  structurally cannot serialize those into this shape
 *  (`crates/sc-http/src/routes.rs::AdminUserWire`). */
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
   *  (`FEATURES.md` #49, `DESIGN-COMPAT.md` §8). */
  quota_bytes: string | null
  /** Running usage ledger, as a string (same 2^53 reason). Not a live
   *  filesystem recomputation — see `sc_core::quota`'s module doc for how
   *  it's charged. */
  usage_bytes: string
}

/** One of the eight bits `sc_acl::Perms` defines (`sc-core::acl_store::PERM_NAMES`),
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

/** A share this deployment has registered (`sc_core::Core::share_defs`) —
 *  used both by the grant-creation screen's picker (which only reads
 *  `id`/`name`) and by the share management screen (`FEATURES.md`
 *  #40/#157), which is why `host_path` is here — an admin adding/editing a
 *  folder share has to see and set where it points on the host. This is a
 *  deliberate, narrow exception to `sc-vfs`'s "never leak a host path" rule:
 *  that rule is about request-handling responses/errors/logs to non-admins,
 *  not this trusted admin-configuration screen. */
export interface AdminShare {
  id: number
  name: string
  host_path: string
  /** `true` for a share declared in `config.toml`. It is still renameable,
   *  repointable and trash-toggleable here — the backend keeps those as
   *  overrides in `shares.db` and reapplies them at startup — but it cannot
   *  be deleted, because the config entry would re-declare it on the next
   *  restart. Only the delete affordance is hidden. */
  config_defined: boolean
  /** Off by default for every share. */
  trash_enabled: boolean
}

/** `POST /api/admin/shares` body. */
export interface CreateShareReq {
  name: string
  host_path: string
}

/** `PATCH /api/admin/shares/{id}` body — all fields optional, so a rename
 *  need not resend the host path and vice versa, and either can be sent
 *  together with or without a trash toggle. */
export interface UpdateShareReq {
  name?: string
  host_path?: string
  trash_enabled?: boolean
}

/** Who a grant applies to — `sc_acl::Principal::Group`/`Principal::User`,
 *  the same union `GrantManagementSection.svelte` renders regardless of
 *  which one is reached from (`UserManagementSection`'s per-user entry point
 *  or `GroupManagementSection`'s per-group one). */
export interface GrantPrincipal {
  kind: 'user' | 'group'
  id: number
}

/** One row of `GET /api/admin/groups` (`FEATURES.md` #48,
 *  `crates/sc-http/src/routes.rs::AdminGroupWire`). `members` is a plain id
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

/** One row of `GET /api/admin/audit` (`FEATURES.md` #158,
 *  `crates/sc-http/src/routes.rs::AdminAuditRowWire`). Newest first. `actor`
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
 *  type mirrors, `sc_core::acl_store::GrantRecord` for the Rust shape it
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
 *  (mirrors `sc_core::acl_store::GrantPatch`'s `Option<Option<String>>`). */
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

// ── auth (DESIGN-AUTH.md §6.3) ──

/** The minimal user object `POST /api/auth/login[/totp]` returns — NOT the
 *  same shape as `SessionInfo.user` above (which carries `display_name`/
 *  `is_admin`). The login response is deliberately thin; the app re-fetches
 *  `GET /api/auth/session` right after a successful login to get the rest. */
export interface AuthUser {
  id: number
  name: string
}

/** `POST /api/auth/login` / `POST /api/auth/login/totp` response. Tagged on
 *  `status` to match the server's `#[serde(tag = "status")]` enum exactly
 *  (`crates/sc-http/src/routes.rs::LoginResp`). */
export type LoginResult = { status: 'ok'; user: AuthUser } | { status: 'totp_required'; challenge: string }

export type OnConflict = 'Fail' | 'Rename' | 'Overwrite' | 'Skip'

/** `crates/sc-http/src/core_api.rs::OpResult` — the one per-item result shape
 *  `/fs/delete`, `/fs/move`, `/fs/copy`, `/trash/restore`, and
 *  `/trash/purge` all share (`Json(serde_json::json!({ "results": results }))`
 *  over `Vec<OpResult>` in every one of those handlers). This used to be
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
}

export interface BatchResult {
  results: BatchItemResult[]
}

// ── trash (DESIGN-API.md §5, FEATURES.md #18) ──

/** One row of `GET /api/trash` (`crates/sc-http/src/core_api.rs::TrashEntry`).
 *  `id` is an opaque string (`"{share}:{uuid}"` — `sc-server/src/bridge.rs`'s
 *  `trash_list`), never a `FileId`: a trashed item has no live fileid to be
 *  addressed by, so it must be round-tripped verbatim to `/trash/restore`
 *  and `/trash/purge` rather than parsed. */
export interface TrashEntry {
  id: string
  name: string
  size: number
  /** Nanoseconds as a string, same rule as `Entry.mtime_ns`. */
  deleted_mtime_ns: string
}

// ── content links (DESIGN-PREVIEW.md §2/§8) ──

export type LinkDisposition = 'attachment' | 'inline_thumb' | 'stream'

/** `POST /api/fs/link` response (`crates/sc-http/src/routes.rs::fs_link`). */
export interface LinkResponse {
  url: string
}

// ── share links, owner side (DESIGN-PREVIEW.md §7, DESIGN-API.md) ──

/** `POST/PATCH /api/shares[/:id]` request body's `perms` field
 *  (`crates/sc-http/src/core_api.rs::PermsReq`) — every key optional/defaults
 *  to `false` server-side, so only the bits actually being granted need to be
 *  sent. Deliberately not reusing `Perms` (whose 8 keys are all required) —
 *  a share link request is a sparse grant, not a full permission snapshot. */
export type PermsReq = Partial<Perms>

/** One share link as its owner sees it (`GET/POST/PATCH /api/shares[/:id]`,
 *  `crates/sc-http/src/core_api.rs::ShareLinkInfo`). `token`/`url` are only
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

/** `POST /api/shares` body (`crates/sc-http/src/core_api.rs::ShareLinkCreate`). */
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
  if_match?: Record<string, string>
  dry_run?: boolean
}

/**
 * What `POST /api/fs/move` answers instead of `202 { job }` when `dry_run` is
 * set (`crates/sc-http/src/routes.rs::fs_move`). A move whose source and
 * destination sit on different filesystems cannot be a rename — the server
 * falls back to copy-then-delete, which reads and rewrites every byte and
 * bills the copy against quota until the source is gone. That turns an
 * instant operation into a minutes-long job, so the picker asks first and
 * tells the user before they commit.
 */
export interface MovePreflight {
  will_copy: boolean
  /** Bytes that will actually be rewritten — 0 unless `will_copy`. */
  total_bytes: number
  /** `"cross_device"`, or empty when `will_copy` is false. */
  reason: string
}

// ── long-running jobs (DESIGN-API.md §6) ──

export type JobState = 'running' | 'done' | 'error' | 'cancelled' | 'interrupted'

/** Wire values of `crates/sc-http/src/state.rs::JobKind::as_str()`. */
export type JobKindWire = 'copy' | 'move' | 'delete' | 'archive' | 'index_build'

/** `GET /api/jobs/{id}` (`crates/sc-http/src/routes.rs::job_status`,
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
  /** Whether `GET /api/jobs/{id}/download` has bytes waiting. Only ever
   *  `true` for a finished `JobKind::Archive`. */
  download: boolean
}

/** `GET /api/jobs` (`crates/sc-http/src/routes.rs::job_list`) — every
 *  non-terminal job (`running`/`interrupted`) the caller owns. `JobTray`
 *  fetches this once on mount to re-attach across a browser refresh *or* a
 *  server restart (a Docker cutover), since `jobs.db` — not the browser — is
 *  the durable record. */
export interface JobListResponse {
  jobs: JobStatus[]
}

// ── live change notifications (`GET /api/events`, DESIGN-API.md §7,
// `crates/sc-http/src/ws.rs::ServerMsg`/`ClientMsg`) — a WebSocket, not SSE,
// despite `search/stream` above using SSE for a superficially similar
// "server pushes named events" shape: this one is bidirectional (the client
// sends `sub`/`unsub`/`ping`), which SSE cannot do. ──

export type ServerMsg =
  | { t: 'inval'; path: string; etag: string }
  | { t: 'job'; id: string; done: number; total: number }
  | { t: 'quota'; used: number; limit: number | null }
  | { t: 'revoked' }
  | { t: 'pong' }

export type ClientMsg = { t: 'sub'; paths: string[] } | { t: 'unsub'; paths: string[] } | { t: 'ping' }

// ── text editor (`/edit/[...path]`, DESIGN-FRONTEND.md §7) ──

/** `GET /api/fs/read` response shape. No etag on purpose — the server
 *  derives it fresh from `stat` at write time (`AppError::precondition`), so
 *  the editor calls `api.stat(path)` alongside this to get one to send back
 *  as `If-Match`. */
export interface ReadFileResponse {
  content: string
}

// ── server settings (`crates/sc-http/src/settings_api.rs`) — the admin
// screen's parity with every operator-settable `config.toml` field. One flat
// field list, keyed with the same dotted path `config.toml` itself uses, so
// an operator can cross-reference the two; each field carries where its
// current value came from and whether changing it needs a restart. ──

/** `SettingsSource` (`settings_api.rs`), `#[serde(rename_all = "snake_case")]`. */
export type SettingsSource = 'builtin_default' | 'config_file' | 'admin_override'

/** One row of `GET /api/admin/server-settings`. A field this screen can't
 *  safely change is still listed, never hidden; `readonly_reason_key` is then
 *  a catalogue key (`api/error-text.ts`'s allowlist), not display text — the
 *  server has no idea which language the reader picked. */
export interface SettingsField {
  key: string
  value: unknown
  source: SettingsSource
  restart_required: boolean
  readonly_reason_key: string | null
}

export interface SettingsSnapshot {
  fields: SettingsField[]
  /** `sc_smb::SmbOrchestrator::public_bind_warning_active()` — the same
   *  permanent banner `smb-sync`'s own log line describes. */
  smb_public_bind_warning: boolean
  /** Grants Samba was handed more permissively than they were written here,
   *  because `smb.conf` cannot express the difference. Empty in the ordinary
   *  case; older servers omit the field entirely. */
  smb_overgrants?: SmbOvergrant[]
}

/** `key` is a catalogue key, `detail` its placeholders — the server never
 *  sends the sentence. */
export interface SmbOvergrant {
  share: string
  user: string
  key: string
  detail: string[]
}

/** What applying a patch tells the caller — whether it already took effect
 *  or needs the restart flow. */
export interface ApplyOutcome {
  applied_live: boolean
  restart_required: boolean
}

/** `PATCH /api/admin/server-settings/smb` body (`SmbPatch`). `enabled` needs
 *  a restart; everything else here applies live. */
export interface SmbSettingsReq {
  enabled: boolean
  workgroup: string
  service_user: string
  allow_public_bind: boolean
  totp_policy: 'require_separate' | 'block'
  service_uid: number
  service_gid: number
}

/** `PATCH /api/admin/server-settings/search` body (`SearchPatch`) — fully
 *  live. */
export interface SearchSettingsReq {
  max_concurrent_fast: number
  max_concurrent_slow: number
  walk_deadline_fast_ms: number
  walk_deadline_slow_ms: number
  rate_per_minute: number
}

/** `PATCH /api/admin/server-settings/archive` body (`ArchivePatch`) — fully
 *  live. */
export interface ArchiveSettingsReq {
  max_concurrent: number
}

/** `PATCH /api/admin/server-settings/network` body (`NetworkPatch`) —
 *  restart-required. */
export interface NetworkSettingsReq {
  bind: string
  app_hosts: string[]
  content_hosts: string[]
  allowed_origins: string[]
  trusted_proxies: string[]
  compat_canonical_url: string | null
}

/** `PATCH /api/admin/server-settings/db` body (`DbPatch`) —
 *  restart-required. */
export interface DbSettingsReq {
  size_guard: boolean
  max_bytes: number
  min_free_bytes: number
}

/** `PATCH /api/admin/server-settings/symlink-policy` body (`SymlinkPatch`) —
 *  restart-required. */
export interface SymlinkPolicyReq {
  policy: 'deny' | 'within_share' | 'follow'
}

/** `PATCH /api/admin/server-settings/homes` body (`HomesPatch`) —
 *  restart-required. */
export interface HomesSettingsReq {
  enabled: boolean
  root: string | null
}

/** `PATCH /api/admin/server-settings/watch` body (`WatchPatch`) —
 *  restart-required. */
export interface WatchSettingsReq {
  backend: 'auto' | 'hotset' | 'inotify_full' | 'fanotify'
  hot_set_max: number
  full_threshold: number
}

/** `PATCH /api/admin/server-settings/oidc` body (`OidcPatch`): the eight
 *  rows §6-4 marks UI-editable, all restart-required (the relying party, its
 *  TLS client and its two caches are assembled once at startup).
 *
 *  The other two `oidc.*` settings are not here on purpose.
 *  `oidc.client_secret_file` is the path to a secret, and
 *  `oidc.local_password_login` would be unrecoverable if this screen could
 *  write it: an admin override beats `config.toml` on every boot, so setting
 *  it to `deny` here and then losing the IdP would lock everyone out with no
 *  way back in (§4.3.5). The server refuses both fields; this type refuses to
 *  offer them. */
export interface OidcSettingsReq {
  enabled: boolean
  issuer: string
  client_id: string
  /** Must match what is registered at the IdP byte for byte. Empty, or
   *  anything not starting with `https://`, and the server keeps OIDC off. */
  redirect_uri: string
  /** `openid` is always included server-side whether or not it is listed. */
  scopes: string[]
  display_name: string
  allow_private_endpoints: boolean
  /** `"block"` is the only accepted value (§4.3.6). */
  smb_policy: 'block'
}

/** `PATCH /api/admin/server-settings/paths` body (`PathsPatch`) —
 *  restart-required, and the one group the server can refuse outright: it
 *  will not accept a `data_dir` that does not already hold the databases, nor
 *  a `master_key_file` whose bytes differ from the current key. Both are
 *  relocation settings, not "point somewhere new and start fresh". */
export interface PathsSettingsReq {
  data_dir: string
  /** `null` means `<data_dir>/master.key`. */
  master_key_file: string | null
  smb_config_dir: string
}
