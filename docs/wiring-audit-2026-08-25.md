# Backend/frontend wiring audit

Date: 2026-08-25. Scope: every route the Go server mounts (`go/internal/server/routes.go`, 101 routes) against every call the web client makes (`web/src/lib`, `web/src/routes`), including the `.svelte` files that `tools/routecheck` cannot see. Compared per route: method, query params, request body fields, response fields, status codes, error codes, and the i18n keys that render them.

Baseline: `routecheck` and `contractcheck` both pass. Everything below is what those gates cannot see: body-shape drift, error-code drift, response-envelope drift, and dead wiring inside `.svelte` files.

Severity: **broken** = the feature does not work against the real backend. **degraded** = works but loses information or shows wrong text. **cosmetic** = dead code or latent drift with no current user impact.

---

## 1. Broken

### 1.1 `POST /api/fs/move` returns no job id; the UI treats every move as a failed job

- Backend `go/internal/httpapi/handler/fs.go:451-471` (`Move`): synchronous, answers `200 {"results":[...]}`. Never sets `job` (contrast `Copy`, `fs.go:560`).
- Frontend `web/src/lib/api/http.ts:183-185` types `move()` as `Promise<{job: string}>`; `web/src/routes/(app)/b/[...path]/+page.svelte:549-556` destructures `job` and calls `jobTray.track(job, 'move')`.
- Effect: `job` is `undefined`, the tray polls `GET /api/jobs/undefined`, gets 404, and every move shows as a failed job even though it succeeded. The conflict and quota branches at `+page.svelte:566-577` never fire for move.

### 1.2 `POST /api/fs/archive` streams a ZIP; the client expects `{job}` JSON

- Backend `go/internal/httpapi/handler/archive_create.go:33-73`: streams `application/zip` bytes directly. No archive job row is ever created anywhere (`state.OpArchive` has no `CreateOp` call in the repo).
- Frontend `web/src/lib/api/http.ts:220-222` routes `archive()` through the JSON `request()` helper; `+page.svelte:424` destructures `{job}` and tracks it.
- Effect: the ZIP bytes are discarded, `job` is `undefined`, the download-as-zip and multi-select download features do not work at all. Additionally `GET /api/jobs/{id}/download` is `notImplemented` (`ops.go:63`) and nothing ever sets `JobStatus.download: true`, so the whole client-side job-download flow (`http.ts:302-310`, `jobDownload`) is unreachable by design on the server.

### 1.3 Public share download: raw byte stream vs expected `{url}` JSON

- Backend `go/internal/httpapi/handler/link_public.go:46-70` (`POST /s/{token}/download`): streams the file as `application/octet-stream`.
- Frontend `web/src/lib/api/share.ts:206-220` (`requestShareDownload`): does `res.json()` expecting `{url}` and then navigates to `url`.
- Effect: `.json()` fails on binary, `url` is `undefined`, `window.location.href = undefined`. Downloading from the public share page (single file and per-row inside a folder share) is broken. The `GET /s/{token}/zip` folder path is fine.

### 1.4 App password scope silently dropped: a "read-only" token is minted with full access

- Frontend `web/src/lib/api/http.ts:465-476` (`createScopedAppPassword`) sends `{name, scope: {perms: {read:true, download:true}, shares}}`: a nested `scope` object with a boolean map.
- Backend `go/internal/httpapi/handler/session.go:349-353, 378-391` decodes top-level `Name string`, `Perms uint16` (bitmask), `Shares []string`. The `scope` key matches nothing and Go's decoder ignores unknown keys, so `Perms` stays `0`.
- With `Perms: 0`, the stored scope is `0`, which is not `mw.ScopeFull (0xFFFF)`, so `aclscope.go:40` denies everything: the token is not over-broad, it is unusable (every `AccessPerms` route 403s). Either way the feature does not work, and the `http.ts` doc comment claiming enforcement "verified against a running server" cannot be describing this code.
- Compounding: `web/src/lib/ui/settings/AppPasswordsSection.svelte:65-67` calls `api.createScopedAppPassword(newName.trim())` **without** passing `{readOnly: true}`, so even the frontend half never sends the scope the checkbox promised. The read-only checkbox is a no-op twice over. Also `422 auth.unknown_share` documented in `http.ts:457` is emitted nowhere in the backend.
- Severity: broken, and security-relevant in intent (a user believes a device token is restricted).

### 1.5 `GET /api/auth/sessions`: envelope and every field name mismatch

- Backend `go/internal/httpapi/handler/session.go:417-454`: writes `{"sessions":[{id, last_seen_ns, ip, ua, current}]}`; `last_seen_ns` is a JSON number.
- Frontend `web/src/lib/api/http.ts:494-496` types the body as bare `ActiveSession[]`; `web/src/lib/api/types.ts:182-190` expects `id_hash`, `created_ns`, `last_seen_ns` (string), `absolute_expiry_ns`, `ip_first`, `ua_first`, `current`. `SessionsSection.svelte` keys rows on `s.id_hash` and revokes with it.
- Effect: the devices list cannot render (object instead of array) and revoke would call `DELETE /api/auth/sessions/undefined`. Three simultaneous breaks: envelope, field names, number-vs-string ns.

### 1.6 `GET /api/auth/app-passwords`: same envelope mismatch plus missing fields

- Backend `session.go:373-377`: writes `{"app_passwords":[{id, name}]}`.
- Frontend `http.ts:437-439` expects bare `AppPasswordInfo[]`; `AppPasswordsSection.svelte:160-164` reads `p.created_ns`, `p.last_used_ns`, `p.read_only`, none of which the server sends (`types.ts:165-176` declares them required except `read_only`).
- Effect: the list never renders; even unwrapped, issued/last-used dates are `undefined`.

### 1.7 `PATCH /api/admin/grants/{id}` drops `label`

- Frontend `web/src/lib/ui/admin/GrantManagementSection.svelte:214-219` sends `label` on every grant edit; `types.ts:425-431` (`UpdateGrantReq`) documents it as clearable.
- Backend `go/internal/httpapi/handler/admin_grants.go:192-196` decodes only `allow`, `deny`, `inherit`; `go/internal/acl` `UpdateGrant` never writes a label.
- Effect: the edit dialog accepts a label, the server answers 200, and the label reverts on reload. A silently ignored write path.

### 1.8 SMB `service_uid` setting does not exist on the backend

- Frontend `ServerSettingsSection.svelte` requires and sends `service_uid` in every SMB section save (`types.ts:981`, `SmbSettingsReq`), and client-side validation blocks the save if it is not a non-negative integer.
- Backend `go/internal/runtimecfg/runtimecfg.go:83-93, 262-271`: the SMB config has only `service_gid`; nothing reads `smb.service_uid`. SMB UIDs are derived as `SMBBaseUid + rowID` (`go/internal/auth/passdb.go:30-37`).
- Effect: a form field that gates the save button, is presented as persisted configuration, and does nothing.

### 1.9 `GET /api/shares?path=` filter ignored

- Backend `go/internal/httpapi/handler/link.go:59-75`: GET calls `d.Core.ListLinks(ctx, uid, nil)`, path scope hardcoded `nil`.
- Frontend `http.ts:328-330` sends `?path=`; `ShareManageDialog.svelte:141` expects only the selected item's links.
- Effect: the per-file share dialog lists every link the account owns, next to a create form for the current file. Misleading and an information-scoping bug.

### 1.10 Job wire enums and per-item result shape mismatch

- `go/internal/httpapi/handler/ops.go:102-116` emits state `"failed"`; `types.ts:762` (`JobState`) has no `"failed"`, so `jobs.ts:20,110` never sees the job as terminal and polls for 20 minutes until `JobTimeoutError`. A failed copy is reported as a timeout.
- `ops.go:118-128` emits kind `"index-build"` (hyphen); `types.ts:757` and `job-tray.svelte.ts:42-46` match `'index_build'` (underscore). Currently unreachable because index build answers `notImplemented` (`index_build.go:30`), but the literal is wrong on the day it ships.
- `ops.go:23-30` emits `results: [{path, status}]` with status strings `ok|denied|not_found|conflict|skipped|failed`; the client (`types.ts:800-816`, `jobs.ts:29-38`) reads `{path, ok, error:{code,...}}`. `r.ok` and `r.error` are always `undefined`, so per-item failure reporting through `GET /api/jobs/{id}` never works, and the conflict/quota detection at `+page.svelte:566-577` cannot match on job results.
- Also `POST /api/admin/index/build` (`index_build.go:103`) writes `{"job": id}` as a JSON number where every other job creator formats a string (`fs.go:557-560`); `http.ts:638-640` types it `{job: string}`.

### 1.11 tus upload metadata dropped: `mtime` and `ifMatch` never applied

- Frontend `web/src/lib/upload/transport.ts:57-76` encodes `filename`, `dest`, `relativePath`, `mtime`, `ifMatch` into `Upload-Metadata`.
- Backend `go/internal/httpapi/handler/uploads.go:99-119`: `SessionSpec.Meta` is never populated; `uploadDest()` (`uploads.go:352-370`) consumes `dest`/`relativePath`/`filename` only to build the destination path. `finalize.go:129-133` would apply `MtimeNs` but it is always nil.
- Effect: every uploaded file gets the upload time instead of the source file's mtime, defeating the explicit intent in `worker.ts:138`. The `ifMatch` metadata key is read by nothing (the backend reads a real `If-Match` header, which the frontend never sends), so upload optimistic concurrency is non-functional in both directions (currently latent: no UI path sets `ifMatch`).

### 1.12 Dead error-code branches backed by missing server validation

The frontend branches on codes the backend never emits, and in each case the server-side validation the code implies is also absent:

| Frontend branch | Where | Backend reality |
|---|---|---|
| `admin.last_admin` | `UserManagementSection.svelte:207,236` | No last-admin guard exists at all. `admin_users.go:100-140` only refuses self-delete/self-disable (`fs.conflict` + `admin.cannot_delete_self/_disable_self`, codes the frontend never checks). `go/internal/auth/users.go` `DeleteUser`/`DisableAccount` have no admin-count check. The client-side `isLastActiveAdmin()` (`:155-162`) is the only protection and is bypassable via direct API. The component comment "the server has final say" is false. |
| `admin.invalid_quota` | `UserManagementSection.svelte:124` | `admin_users.go:155-159` passes any int64 to `SetQuota` (`auth/admin.go:111-120`, bare UPDATE). Zero and negative quotas are stored unvalidated. |
| `auth.weak_password` | `UserManagementSection.svelte:189`, `PasswordSection.svelte:64`, `SmbSection.svelte:134` | No handler emits it. Admin create (`admin_users.go:44-79`) and reset (`:160-163`) check only non-empty; `POST /api/auth/password` and `/api/auth/smb/password` (`account.go`) likewise. A 1-character password is accepted server-side everywhere except first-run setup (`setup.go:119-122`, which emits `setup.weak_password`, a different code). The client's `MIN_PASSWORD_LEN = 10` is unenforced at the trust boundary. |

Severity: broken as error UX, and the missing last-admin guard and password-length checks are real trust-boundary gaps, not just dead branches.

### 1.13 Session-expiry 401 never routes to the login screen

- Frontend `web/src/lib/api/http.ts:118`: only `code === 'auth.required'` triggers `noteUnauthorized()` (the login redirect). The comment explains this is to keep `auth.invalid_credentials` inline.
- Backend `go/internal/httpapi/mw/auth.go:120-135`: a browser holding an expired or revoked session cookie always presents it and always gets `auth.invalid_credentials`; `auth.required` is emitted only when no credential is present at all.
- Effect: mid-session expiry surfaces as inline "could not load" errors on every screen instead of the login page, which is exactly the failure the `errorFrom` comment says it prevents. (Compounded by 2.6: the WS `revoked` push that would cover this is also never sent.)

---

## 2. Degraded

### 2.1 Share-folder rejection reason never reaches the admin

- Backend `go/internal/httpapi/handler/shares.go:150-164` (`shareRefused`): the only bad-`host_path` refusal path, always `422 fs.invalid_request` with key `admin.share_rejected` and `reason` in {missing, unreadable, unavailable, AdmissionError type} (`core/share_admin.go:254-266`).
- Frontend `web/src/lib/api/error-text.ts:22-75`: `admin.share_rejected` is in neither `SERVER_KEYS` nor `CODE_KEYS`; the eight `share.path_*`/`share.name_*` keys that ARE allowlisted are emitted by no live backend path (they exist only in `apierr` doc comments and tests). So `ShareManagementSection.svelte:96,141` always shows the generic "could not add folder" and the computed reason (missing vs unreadable vs wrong filesystem) is discarded. The dead `share.*` allowlist entries mask the gap.

### 2.2 Settings readonly reasons render as raw dotted keys

- Backend `settings.go` emits six `readonly_reason_key` values: `settings.readonly_bind_address` (:133), `settings.readonly_data_dir` (:140), `settings.not_in_this_build` (:102), `settings.unknown_watch_backend` (:203), `settings.readonly_smb_agent_socket` (:403), `settings.readonly_needs_restart_oidc` (:420).
- Frontend `error-text.ts` `SERVER_KEYS` is missing three of them: `readonly_bind_address`, `readonly_data_dir`, `readonly_needs_restart_oidc`. `serverKeyText()` (`error-text.ts:105-107`) then renders the raw key string next to the bind, data-dir, and every OIDC field on the settings screen. The i18n catalogue entries exist (`en.json:570-573`); only the allowlist is stale. Conversely 19 `settings.*` entries in `SERVER_KEYS` (`stale_client`, `master_key_*`, `oidc_redirect_*`, `readonly_secret_file_path`, `readonly_owned_by_*`, `readonly_static_*`, `readonly_smb_interfaces`, `readonly_local_password_login`, `unknown_symlink_policy`, `unknown_oidc_smb_policy`, ...) are emitted by no backend path: dead entries.
- `settings.invalid_cidr` is rendered correctly in the check-preview panel but is missing from `SERVER_KEYS`, so an actual save with a bad CIDR shows only the generic fallback while the preview names the problem.

### 2.3 SMB opt-out/enabled: two switches, one stored bit

- Backend `session.go:204-207` derives `smb_opt_out = !smb_enabled`; `account.go` `SMBSettings` collapses the request to `SetSMBAccess(enabled && !optOut)`.
- Frontend `SmbSection.svelte` models them as independent toggles. "Enabled off but not opted out" is unrepresentable and reads back as opted-out after reload.

### 2.4 `oidcLinkStart` return field name mismatch

- Frontend `http.ts:569-574` sends `returnTo`; backend `oidc_link.go:56-59` decodes `return_to`. Always empty, so after linking a provider the browser lands on `/` instead of back on the settings page.

### 2.5 Setup screen error mapping wrong for the two most common refusals

- `web/src/routes/setup/+page.svelte:48-63` handles `auth.invalid_token`, `fs.gone`, `setup.disabled`, `auth.required` (none of which this route emits) but not `setup.completed` (410, `setup.go:107`) or `setup.token_expired` (403, `setup.go:110`), which both fall to the generic failure text. `setup.invalid_token` and the 422 codes do match.

### 2.6 WS event kinds `job`, `quota`, `revoked` never sent

- Backend `go/internal/httpapi/ws/ws.go` only ever constructs `pong` (:165) and `inval` (:266); `Hub.Close()` is never called.
- Frontend declares and handles all four kinds (`types.ts` `ServerMsg`, `state/events.ts:97-113`, `state/jobs.ts:69-79`). Fallbacks (polling, next-401) work, so nothing breaks, but job progress only moves at the 1s poll and a revoked session is detected late. Together with 1.13 this means session revocation has no fast path at all.

### 2.7 Rate-limit settings section unreachable from the UI

- Backend exposes `rate.per_sec` and `rate.burst` as an editable section (`settings.go:145-155`, `admin_settings.go:31-35`).
- Frontend `ServerSettingsSection.svelte` has the section id in `SECTIONS` but no form, no request type, no save path; `sectionName()` and `otherFields` are computed and never rendered. The backend section exists specifically because the screen once offered a control that 404'd; now the control is gone instead. Also the `rate` section label says "per minute" while the fields are per-second.

### 2.8 Drop-link upload limit never announced

- Backend `link.go:203-233` (`GET /s/{token}`): never includes `max_upload_bytes`.
- Frontend `share.ts:129,162` reads it (null fallback), and the public drop page would show the limit and pre-check file size. On a real backend the hint never appears and oversized uploads are only refused after streaming.

### 2.9 Search truncation flag discarded

- Backend `search.go:120-125`: the SSE `done` event carries `{truncated, tier}`.
- Frontend `http.ts:895-908`: the `done` listener ignores `ev.data`. A deadline-truncated search renders identically to a complete one.

### 2.10 Ratelimit middleware answers plain text, not the error envelope

- `go/internal/httpapi/mw/ratelimit.go:124`: `http.Error(w, "rate limited", 429)` with no JSON envelope, while `+page.svelte:432` branches on `err.code === 'rate.limited'`. `errorFrom` (`http.ts:102`) falls back to `code: 'internal'` when the body is not the envelope, so a real 429 from the limiter renders as a generic internal error and the dedicated branch only fires for the auth-service's own `auth.ErrRateLimited` path (which does go through `apierr.Map`).

### 2.11 `admin.chunk_below_floor` refusal shows the wrong message

- Backend `admin_ops.go:246-248` refuses with `400 fs.invalid_request` (key `admin.chunk_below_floor`); frontend `UploadSettingsSection.svelte:71` checks `fs.invalid_name`. Falls to generic "could not save".

### 2.12 `totpDisable` / `oidcUnlink` hardcode `smb_password_replaced: true`

- `account.go:255`, `oidc_link.go:137`: always `true` regardless of whether a dedicated SMB password existed; the UI then announces a replacement that may not have happened (`http.ts:406-411,584`).

---

## 3. Cosmetic / latent

- **`roots[].perms` number vs object.** `session.go:134` sends a uint16 bitmask; `types.ts:432-435` declares `Perms` (boolean object). No code currently reads `roots[].perms`, so latent only; entry-level `perms` from `/fs/list` is the boolean object and is fine (`fs.go:54-63`).
- **`POST /api/auth/app-passwords` response `id` always 0** (`session.go:391`, no lookup of the inserted row). Callers only read `token` today.
- **`loginRequest.Factor`** (`session.go:39`) is never sent by the client; second factor goes through `/api/auth/login/totp`. Dead field.
- **`oidc.ts` dead branches**: `oidc.expired` and `oidc.already_linked` (`oidc.ts:96,112`) are never emitted; expiry maps to `oidc.bad_state`, already-linked to `oidc.subject_already_linked` (handled).
- **`admin.name_taken` reason key discarded**: `apierr/map.go:105-109` attaches it to `fs.conflict`, but the user/group screens branch on the code and render their own text, and `admin.name_taken` is not in `en.json`. Functionally fine, redundant payload.
- **`MoveReq.if_match`** (`types.ts:731`) has no backend counterpart (`fs.go:392-397`) and no caller sets it.
- **`PreviewInfo.width/height`** (`types.ts:29-32`, read by `DetailsPanel.svelte:84-89`) is never sent (`thumb.go` `previewJSON` has only `available`); the Dimensions row can never render.
- **Stale comment** above `fs.go:161-168` claims `sort`/`order` are read by nothing; the code below reads and honors both.
- **`POST /api/admin/smb/apply`** has no frontend caller: recorded intentionally in `go/routes.server-only`, consistent.
- **`GET /api/fs/thumb`** is flagged by routecheck as uncalled, but that is the tool's `.ts`-only blind spot: `Thumbnail.svelte` and `PreviewDialog.svelte` call `api.thumbUrl`, and the size mapping (`http.ts:256-258`: <=256 small, <=512 medium, else large) matches `thumb.go`'s presets. Consider adding a note in `routes.server-only` or teaching routecheck to scan `.svelte`.
- **`DELETE /api/admin/shares/{id}` on a config-defined share** returns `403 acl.denied` (via `core.ErrDenied`), not the `share.config_defined_not_deletable` key the allowlist implies; unreachable from the UI because the delete button is hidden for those rows.

---

## 4. Verified consistent (spot-checked both sides, no drift found)

Auth: `POST /api/auth/login`, `POST /api/auth/login/totp`, `POST /api/auth/logout`, `GET /api/auth/session` envelope (except 2.3, 3-roots-perms), `POST /api/auth/password` request, TOTP setup/enroll/disable, recovery codes, `POST /api/auth/smb` request shape, SMB password set/clear shapes, app-password delete/wipe call shapes, `GET /api/auth/oidc/config`, OIDC start/callback redirect protocol, `DELETE /api/auth/oidc/link`, `GET/POST /api/setup` request shapes.

Filesystem: `fs/list`, `fs/stat`, `fs/read` (ETag/Range/download=1), `fs/mkdir`, `fs/rename`, `fs/delete` (per-item batch shape agrees on the synchronous path), `fs/write` POST+PUT with `if_match`, `fs/thumb`, `fs/size`, `fs/link` CRUD, `/api/shares` create/patch/delete bodies, `GET /s/{token}` core fields, `/s/{token}/auth`, `/s/{token}/zip`, `/s/{token}/drop`, trash list/restore/purge, `fs/archive/list`, `GET /api/recent`, jobs envelope field names and the dual cancel mounting, tus core chunk protocol (versions, offsets, checksum, 413/404/410/460 handling).

Admin: users list/create/patch/delete shapes (including `quota_bytes` string-out/number-in asymmetry, which is correct), user OIDC link/unlink shapes and codes, groups CRUD and membership, grants list/create shapes, storage report, audit page and query params, index estimate/settings, upload-settings body, server-settings snapshot shape, section names both directions (`smb`, `search`, `archive`, `homes`, `watch`, `db`, `symlink-policy`, `oidc`, `paths`, `network`, `rate` all recognized), settings check result shape, restart `{force}` + `restart.busy` detail, admin shares CRUD shapes, health.

---

## 5. Suggested fix order

1. Data-loss and security first: 1.12 (server-side last-admin guard, password minimum, quota bound), 1.4 (app-password scope contract, pick one wire shape), 1.13 (map expired-cookie to a distinguishable code or branch on 401+both codes).
2. Feature-dead paths: 1.1 move/job contract, 1.2 archive contract (either stream-and-save on the client or implement the job), 1.3 share download, 1.5/1.6 session and app-password lists, 1.9 shares path filter, 1.10 job state/kind/result shapes, 1.11 upload mtime.
3. Contract hygiene: 1.7 grant label, 1.8 service_uid, section 2 error-text/i18n allowlist sync, 2.4 return_to, 2.5 setup codes, 2.10 ratelimit envelope.
4. Consider extending `tools/routecheck`/`contractcheck` to scan `.svelte` files and to compare error-code literals (`err.code === '...'`) against `apierr` emitters; nearly every finding in section 1.12 and 2.x would have been caught by that gate.
