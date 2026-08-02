# Native API design

`sc-http`. The first-class API for the web UI. **Deliberately a different
shape** from the WebDAV/compat layer — protocol
compatibility never gets to bend the native API's design.

---

## 1. Conventions

- Base: `/api`. No version in the path — advertised instead via the `Sc-Api:
  1` response header. A breaking change gets `/api/v2`.
- Request and response bodies are `application/json; charset=utf-8` except
  streamed bodies.
- Every path parameter is a **virtual path** (`/{label}/sub/path`). Host
  paths never appear on the wire.
- State-changing methods require the `Sc-Csrf` header (`DESIGN-AUTH.md`
  §3.3).
- Timestamps are nanosecond integers (`i128`, serialized as a JSON *string*
  — a JSON number silently loses precision past 2^53, which a nanosecond
  epoch is nowhere near fitting under). Never a float.

### 1.1 Error envelope

```json
{ "error": {
    "code": "fs.conflict",
    "message": "the destination already exists",
    "detail": { "path": "/Photos/a.jpg", "etag": "3f2a…" }
} }
```

`code` is a **stable, machine-readable key**. The frontend branches on
`code`; `message` is display-only.

| Code | HTTP | Meaning |
|---|---|---|
| `auth.required` | 401 | |
| `auth.invalid_credentials` | 401 | Identical whether or not the account exists |
| `auth.totp_required` | 200 | Not an error — a flow step (§3) |
| `acl.denied` | 403 | `detail.by` names the denying grant |
| `fs.not_found` | 404 | |
| `fs.conflict` | 409 | Destination exists / name collision |
| `fs.precondition` | 412 | `If-Match` mismatch. `detail.current_etag` included |
| `fs.invalid_name` | 422 | Rejected by `SafePath` parsing. `detail.reason` |
| `fs.cross_device` | 200 | Not an error — advance notice that a move will become a copy |
| `quota.exceeded` | 507 | |
| `rate.limited` | 429 | `Retry-After` |
| `setup.completed` | 410 | First-run setup already finished; the route is gone for good (`DESIGN-AUTH.md` §8) |
| `setup.token_expired` | 403 | The 15-minute window closed; restart to reissue |
| `setup.invalid_token` | 403 | Setup token mismatch |
| `setup.invalid_username` | 422 | `detail.reason` (a fixed string, never an echo of the input) |
| `setup.weak_password` | 422 | `detail.min_length` |
| `internal` | 500 | `detail` is always empty (no information leak) |

**A `500` response never carries internal detail.** Only a correlation id
(`Sc-Trace: <uuid>`); the real cause goes to the server log.

---

## 2. Route table

Matches `crates/sc-server/src/routes.rs::native_routes()`, the actual
registry — this table is descriptive of that source, not the other way
around. TUS (`/api/uploads/**`) and WebDAV (`/dav/**`) have their own design
docs and are omitted here; see `proposals/stowcloud-7-upload.md` and `proposals/stowcloud-8-compat.md`.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/capabilities` | Unauthenticated. Advertises server features/limits |
| `GET` | `/api/setup` | First-run bootstrap (`DESIGN-AUTH.md` §8) |
| `POST` | `/api/setup` | |
| `POST` | `/api/auth/login` | |
| `POST` | `/api/auth/login/totp` | |
| `POST` | `/api/auth/logout` | |
| `GET` | `/api/auth/session` | Current user, permissions, settings, CSRF token |
| `GET`/`POST` | `/api/auth/app-passwords` | |
| `DELETE` | `/api/auth/app-passwords/{id}` | |
| `POST` | `/api/auth/password` | |
| `POST` | `/api/auth/totp/setup` | Step 1 of enrollment — returns the seed, persists nothing |
| `POST` | `/api/auth/totp/enroll` | Step 2 — verifies a live code, activates, returns recovery codes |
| `POST` | `/api/auth/totp/disable` | |
| `GET`/`POST` | `/api/auth/totp/recovery-codes` | Remaining count / reissue (`DESIGN-AUTH.md` §6.2) |
| `GET` | `/api/auth/sessions` | |
| `DELETE` | `/api/auth/sessions/{id_hash}` | |
| `POST` | `/api/auth/smb` | Self-service `smb_opt_out`/`smb_enabled` toggles |
| `GET` | `/api/fs/list` | Directory listing (§4) |
| `GET` | `/api/fs/stat` | Single-entry metadata |
| `POST` | `/api/fs/mkdir` | |
| `POST` | `/api/fs/rename` | Same directory only |
| `POST` | `/api/fs/move` | Batch. Long-running per §6 — see that section's gap notice |
| `POST` | `/api/fs/copy` | Batch. Same |
| `POST` | `/api/fs/delete` | Batch. `permanent` flag |
| `GET` | `/api/fs/read` | Text editor. Size-capped |
| `PUT` | `/api/fs/write` | Small-file direct save. `If-Match` |
| `POST` | `/api/fs/link` | Mints a signed download/preview URL |
| `POST` | `/api/fs/archive` | Multi-select → streamed zip, directly in the response body |
| `GET` | `/api/trash` | |
| `POST` | `/api/trash/restore` | |
| `POST` | `/api/trash/purge` | |
| `GET`/`POST` | `/api/shares` | Share links |
| `GET`/`PATCH`/`DELETE` | `/api/shares/{id}` | |
| `GET` | `/api/search` | (`proposals/stowcloud-5-search.md`) |
| `GET` | `/api/search/stream` | |
| `GET`/`DELETE` | `/api/jobs/{id}` | Status / cancel (§6 — no producer exists yet) |
| `GET` | `/api/events` | WebSocket (§7) |
| `GET` | `/api/admin/storage` | DB/index/thumbnail-cache usage (`DESIGN-FOOTPRINT.md` §4.2) |
| `GET`/`POST` | `/api/admin/index/estimate` | Index-size estimate (`proposals/stowcloud-5-search.md`). No `/{job}` sub-route exists despite what an earlier draft of this table implied — both methods hit the same handler |
| `GET`/`PATCH` | `/api/admin/index/settings` | The T3 name-index on/off override (`proposals/stowcloud-5-search.md`). `PATCH { name_enabled }` persists to `index.db` and takes effect on the next request — no restart |
| `POST` | `/api/admin/index/build` | Starts the name-index crawl as a job → `202 { job }`. `501` while the toggle is off |
| `GET`/`POST` | `/api/admin/users` | |
| `PATCH`/`DELETE` | `/api/admin/users/{id}` | `PATCH` today accepts only `disabled` (`DESIGN-AUTH.md` §11) |
| `GET` | `/api/admin/shares` | The deployment's registered shares |
| `GET`/`POST` | `/api/admin/grants` | (`DESIGN-AUTH.md` §12) |
| `PATCH`/`DELETE` | `/api/admin/grants/{id}` | |
| `GET` | `/c/{token}` | Signed content URL (`proposals/stowcloud-6-preview-sharing.md`) |
| `GET` | `/s/{token}` | Public share link — unauthenticated, token is the whole story (`proposals/stowcloud-6-preview-sharing.md`) |
| `POST` | `/s/{token}/auth` | Link password |
| `POST` | `/s/{token}/download` | |
| `POST` | `/s/{token}/drop` | |

Every route above except `GET /api/capabilities`, `/api/setup`,
`/api/auth/login[/totp]`, `/c/**` and `/s/**` requires authentication.

This table reflects the actual router (`sc_http::routes::protected_routes`).
`sc-server`'s own `native_routes()` — which its module doc calls "the actual
registry" — currently under-reports two of these: it lists only `GET
/api/jobs/{id}` (the router also registers `DELETE`) and only `GET
/api/admin/index/estimate` (the router also registers `POST`). Worth fixing
in the source, not just here.

---

## 3. Auth responses

```rust
#[derive(Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum LoginResult {
    Ok { user: UserInfo },
    TotpRequired { challenge: String },      // one-shot token, 15 min
}

#[derive(Serialize)]
pub struct SessionInfo {
    pub user: UserInfo,
    pub roots: Vec<RootEntry>,               // virtual roots (`proposals/stowcloud-2-core-vfs.md`)
    pub csrf: String,                        // the value the client echoes in Sc-Csrf (DESIGN-AUTH.md §3.3)
    pub limits: ClientLimits,                // chunk_size, max_file_size, parallel
    pub features: Features,                  // only what's actually turned on server-side
}

#[derive(Serialize)]
pub struct RootEntry {
    pub label: String,
    pub perms: Perms,                        // effective permission on this root
    pub share_kind: ShareKind,               // Normal | Home | ReadOnly
    pub shared_externally: bool,             // flags a folder other services also touch
}
```

`shared_externally` is set by an admin when registering a share. Where it's
on, the UI shows a "this folder is also used elsewhere" badge and adds a
confirmation step to invasive actions like trash and rename.

`LoginResult` is deliberately thinner than `SessionInfo` — the login response
only needs to say "ok" or "need a TOTP code"; the app immediately re-fetches
`GET /api/auth/session` for everything else, including the CSRF token every
subsequent state-changing request needs.

---

## 4. Directory listing

Paginating a large directory (100k entries) predictably is the hardest part
of this API.

### 4.1 The problem

Filesystems don't offer a sorted iterator. `getdents64` order is unstable,
and a full sort requires reading every name. Re-reading and re-sorting on
every page is O(n) × page count.

### 4.2 Listing session

```rust
/// Server-side short-lived cache holding a sorted name vector.
pub struct Listing {
    dir_etag: Etag,                  // invalidation check
    sort:     SortKey,               // Name | Size | Mtime | Kind
    order:    Order,
    names:    Vec<CompactString>,    // pre-sorted. 100k entries ≈ 3 MB
    created:  Instant,               // TTL 60s
}
static LISTINGS: Lru<ListingId, Arc<Listing>>;   // capacity 4 per user
```

```
GET /api/fs/list?path=/Photos&sort=name&order=asc&limit=200
  → { "listing": "L-7f3a…", "total": 103421, "cursor": "eyJpIjoyMDB9",
      "entries": [ …200… ], "dir_etag": "3f2a…" }

GET /api/fs/list?listing=L-7f3a…&cursor=eyJpIjoyMDB9
  → next 200

GET /api/fs/list?listing=L-7f3a…&offset=50000&limit=33
  → 33 entries starting at index 50000 (random-access window)
```

- **`offset` is a separate random-access parameter from `cursor`.** `cursor`
  is treated as an opaque token pointing only to the next issued page —
  never relied on to look like `{i: N}` even where it happens to. Virtual
  scroll jumping to an arbitrary visible window (scroll to 50% → index
  ~50000) needs that window directly, not 250 chained `cursor` calls. Since
  `Listing` already holds a sorted vector (§4.2 above), this is a plain
  slice; `offset` wins over `cursor` when both are given, and can start a
  fresh session even without a `listing` id.
- **Name sort needs no stat** — sorting by `getdents64`'s names alone, then
  `statx`ing only the 200 entries on the current page. A 100k-entry
  directory's first page costs a few dozen syscalls.
- **Size/mtime sort needs a full stat pass.** Only then does `Listing`
  creation stat everything, switching to `202 Accepted` +
  `/api/jobs/{id}` polling for progress once the entry count exceeds
  `list.sync_stat_limit` (default 5000) — subject to §6's gap: no job is
  actually produced yet, so today this path can only hold the request open.
- A changed `dir_etag` discards the listing session and returns a fresh first
  page with `Sc-Listing-Stale: 1`. The client refreshes quietly, keeping
  scroll position.
- A `cursor` with no `listing` is treated as an expired session:
  `409 fs.listing_expired`.

### 4.3 Entry representation

```rust
#[derive(Serialize)]
pub struct Entry {
    pub name:     String,
    pub kind:     Kind,                  // file | dir | symlink | other
    pub size:     u64,
    pub mtime_ns: String,                // i128 as a JSON string
    pub etag:     Etag,
    pub perms:    Perms,                 // effective permission on this entry
    pub id:       FileId,                // stable id, for rename tracking
    #[serde(skip_serializing_if = "Option::is_none")]
    pub preview:  Option<PreviewInfo>,   // thumbnail availability/size
    #[serde(skip_serializing_if = "Option::is_none")]
    pub link:     Option<SymlinkInfo>,   // a symlink the policy won't follow
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub confusable: bool,                // homoglyph warning badge
}
```

MIME type is **never in the response**. Extension-based guessing belongs to
the client; the moment the server states a MIME type, something will trust
it for a serving decision (`proposals/stowcloud-6-preview-sharing.md`).

---

## 5. Mutating operations

### 5.1 Common conventions

```rust
#[derive(Deserialize)]
pub struct MoveReq {
    pub paths:       Vec<String>,
    pub dest:        String,
    pub on_conflict: OnConflict,     // Fail | Rename | Overwrite | Skip
    pub if_match:    Option<HashMap<String, Etag>>,   // path → etag
}
```

- **Overwrite is never allowed without `If-Match`.** `OnConflict::Overwrite`
  requires the target's etag; the web UI fills it in automatically. This is
  what stops a sync client from silently clobbering a file someone else just
  changed.
- Batch operations return **partial success** — one failure doesn't stop the
  rest, and the response carries a per-item result.

#### `copy_to` — naming a copy's destination is a separate operation

`copy_entries(paths, dest_dir)` copies a set of items **into** a directory,
names unchanged. That alone can't express **a single copy with a chosen
destination name**.

```rust
/// Copies one item to an explicitly named destination.
/// Unlike copy_entries, the destination here is a final path, not a directory.
pub fn copy_to(&self, user: UserId, src: &str, dst: &str,
               overwrite: bool) -> Result<Entry, CoreError>;
```

Without this, WebDAV `COPY` (whose destination is the final URL in the
`Destination` header) can't be implemented, and faking it with
`copy_entries` + `rename` **deletes the original when copying within the
same directory** — exactly the path macOS Finder's "Duplicate" takes. A real
defect the `sc-dav` COPY/MOVE test matrix caught during implementation, not a
hypothetical.

```json
{ "results": [
  {"path":"/a/1.jpg","status":"ok"},
  {"path":"/a/2.jpg","status":"error","error":{"code":"fs.precondition","detail":{"current_etag":"…"}}}
] }
```

### 5.2 Advance notice

If the move destination is on a different mount (`dev` comparison,
`DEPLOYMENT.md` §4), the caller is told before committing:

```
POST /api/fs/move  { …, "dry_run": true }
  → { "will_copy": true, "total_bytes": 12884901888, "reason": "cross_device" }
```

The UI shows "Moving to different storage. This copies then deletes, and
takes time."

---

## 6. Long-running jobs

`fs_move`, `fs_copy`, `fs_delete` and `fs_archive` always answer `202 { job }`
(`crate::state::JobStore`, `crate::routes::spawn_batch_job` /
`spawn_archive_job`) — there is no inline/synchronous path or size threshold
to cross. Every file operation is a durable background job so the work
outlives a browser refresh, and (via `jobs.db`) a server restart too.
`web/src/lib/api/http.ts`'s `copy`/`del`/`archive` wrappers hand the `{ job }`
envelope straight to the caller rather than awaiting it. `web/src/lib/ui/JobTray.svelte`
is the one place that tracks jobs to completion, via `web/src/lib/state/jobs.ts`'s
`pollJob` (REST poll, reconciled with the WebSocket `job` push) and
`web/src/lib/state/job-tray.svelte.ts`'s `JobTrayState`.

Copy, move, bulk delete, and archive-for-download don't fit a single
request/response cycle once they're large enough, and the caller shouldn't
have to guess in advance whether a given batch will.

```
POST /api/fs/copy  → 202 { "job": "J-4a2f…" }
GET  /api/jobs/J-4a2f…
  → { "id":"J-4a2f…", "kind":"copy", "state":"running", "done":412, "total":1204,
      "current":"/a/b.jpg", "errors":[…], "results":[…], "attempting":[…],
      "pending":[…], "download":false }
DELETE /api/jobs/J-4a2f…      # cancel (stops at the next item boundary)
GET  /api/jobs/J-4a2f…/download  # one-shot fetch of a finished archive's zip bytes
GET  /api/jobs
  → { "jobs": [ {"id":"J-4a2f…", "kind":"copy", "state":"running", …}, … ] }
```

`GET /api/jobs` lists the caller's own non-terminal jobs (`state` is
`running` or `interrupted`; `JobStore::list_open`) and is how the frontend
re-attaches to work in flight: `JobTray`'s `onMount` calls
`JobTrayState::attachOpenJobs`, which is deliberately not gated on any
client-side memory of job ids — a browser refresh loses `items`, and a
server restart (a Docker cutover) loses the server's in-memory runners too,
so `jobs.db` read fresh through this endpoint is the only durable record of
what's still open. A `running` job resumes live polling; an `interrupted`
job — a leftover `running` row the previous process never finished, found
and reclassified by `JobStore::open`'s startup sweep — is shown as its own
terminal status rather than folded into `cancelled`, since nobody asked to
cancel it. If the fetch itself fails (the server is unreachable, e.g.
mid-restart), the tray keeps whatever it last knew and flags itself stale
rather than clearing to empty, which would misreport "nothing running."

`results` mirrors the same per-item `OpResult` list the synchronous path
returns inline, populated once `state` is terminal. `download` is only ever
`true` for a finished `JobKind::Archive` job, and only until the one
`GET .../download` that consumes it — a repeat request 404s.

`results` + `attempting` + `pending` accounts for every path the original
request named, which is what makes an interrupted job actionable rather than
a dead entry. `attempting` is the item the runner had started but never
recorded an outcome for (it died between `begin_result` and
`finish_result`), so whether it happened is genuinely unknown and only the
destination can settle it. `pending` is the remainder the runner never
reached — recorded in full by `JobStore::insert`, in the same transaction as
the `jobs` row, before any work starts. Without it the only durable trace of
a job would be what it had already touched, so an interrupted 10-path move
could say "3 done, 1 unknown" but not *which* six were never attempted.
Those six are untouched on disk, so re-running the operation on exactly them
is safe; the tray labels the two groups apart for that reason. Both are
empty for `Archive`/`IndexBuild`, which have no per-path plan.

- Job state is durable (`JobStore`, `crates/sc-http/src/state.rs`, SQLite
  `jobs.db` with `synchronous = FULL`). A restart does not resume a job, and
  deliberately so: the `attempting` item's outcome is unknowable, and
  replaying it could duplicate a copy or destroy something recreated in the
  interim. `JobStore::open`'s startup sweep reclassifies every leftover
  `running` row to `JobState::Interrupted` instead, so the job surfaces in
  `GET /api/jobs` with its unfinished items named and the user decides.
- Progress also pushes over the WebSocket (§7); polling is the fallback.
- Cancellation only happens **at item boundaries** — stopping mid-file would
  leave a partial file behind, so an in-progress item is finished or its
  partial output removed before the job actually stops.

---

## 7. WebSocket invalidation

```
GET /api/events   (Upgrade: websocket)
```

Implemented as specified below (`crates/sc-http/src/ws.rs`). Two gaps
matter in practice:

> **Nothing publishes an `inval` today.** The pipeline from a filesystem
> change down to `WsHub::publish_inval` exists and is wired
> (`sc-server::app::start_watcher` forwards `sc_watch::InvalEvent`s onto it),
> but nothing in the workspace ever calls `sc_watch::Watcher::subscribe` or
> `touch` to register a directory for OS-level watching in the first place —
> so the debounce loop never fires, for any change, on any share. A client
> only ever learns of a change by re-listing. `send_job`/`send_quota` (the
> `job`/`quota` message types below) have the same problem: nothing calls
> them either, consistent with §6's job-producer gap.
>
> **`WsHub::revoke()` is implemented and unit-tested but never called from
> session revocation.** Logging out or an admin revoking a session does not
> close that session's WebSocket — the socket simply stops being useful
> (every subsequent `can_read` recheck below still applies), but the client
> never receives the `{"t":"revoked"}` message that would trigger an
> immediate logout.

Messages are one JSON line each. No binary framing — traffic volume doesn't
justify it.

```
C→S {"t":"sub","paths":["/Photos","/Photos/2026"]}      # only directories actually being viewed
C→S {"t":"unsub","paths":["/Photos/2026"]}
C→S {"t":"ping"}                                    # every 30s, ahead of CF's 100s idle timeout
S→C {"t":"pong"}
S→C {"t":"inval","path":"/Photos","etag":"9c1d…"}    # this directory changed → client re-fetches
S→C {"t":"job","id":"J-4a2f…","done":412,"total":1204}
S→C {"t":"quota","used":…,"limit":…}
S→C {"t":"revoked"}                                  # session revoked → log out immediately
```

- **`inval` says only "this changed," never what changed.** A delta would
  require the server to remember prior state, which doesn't hold once an
  external process can write to the same share. Re-fetching is simple and
  always correct.
- Subscriptions are limited to **directories currently in view plus expanded
  tree nodes**. Allowing a full-tree subscription would flood the client
  during a bulk copy.
- Events are **200ms debounced and coalesced per path** per connection
  before sending.
- READ permission on a subscribed path is rechecked **both** at subscribe
  time and at send time. A user whose access was revoked mid-subscription
  must not keep receiving change notifications — that would itself leak
  existence information.

---

## 8. Capabilities

Reachable unauthenticated, and **leaks nothing beyond "a server exists
here."** No user count, no share list, no version detail.

```json
{
  "product": "sc",
  "api": 1,
  "upload": { "chunk_size_min": 5242880, "chunk_size_default": 10485760,
              "chunk_size_advisory": 10485760, "chunk_size_max": null,
              "parallel": 4, "max_file_size": null },
  "features": { "webdav": true, "smb": false,
                "preview": true, "trash": true, "shares": true,
                "search": "walk",        // "walk" | "name" | "name+content" — search tiers
                "extensions": ["compat-nc"] },   // compatibility layers actually mounted
  "auth":     { "totp": true, "app_passwords": true },
  "content_origin": "https://content.example.com"
}
```

**Why `features.extensions` is a list of names, not one boolean per
vendor**: an early draft had `"<vendor>_compat": true`, which means **the
core API knows a vendor name** — a violation of the isolation contract
this codebase enforces with a CI grep gate. The core owns only the
protocol-neutral concept "names of the compatibility layers currently
enabled"; the string `"compat-nc"` itself is registered by `sc-compat-nc` at
startup. Adding another compatibility layer never touches the core.

`chunk_size_max: null` means the server enforces no hard cap.
`chunk_size_advisory` is an environment-aware **recommendation** (detects
things like Cloudflare) so the client doesn't need its own detection logic —
a client may use a larger value and handle an upstream `413` itself
(`proposals/stowcloud-7-upload.md`).

---

## 9. Middleware stack

Order affects security. Top to bottom, in actual request-processing order:

```
1. RequestId            → mints Sc-Trace
2. TrustedProxy         → resolves CF-Connecting-IP / X-Forwarded-For against
                          trusted_proxy_cidrs, else the raw peer. Applied
                          **once**, outside every mount — native API, DAV,
                          and every compat layer (sc_server::app::App::router)
3. HostGuard            → Host header allowlist. Mismatch → 421
4. SecurityHeaders      → CSP, nosniff, frame-ancestors 'none', Referrer-Policy
5. RateLimit            → per IP. Runs before Auth so a flood gets 429
                          without reaching Argon2/session lookups
6. BodyLimit            → per route. **Not applied to upload routes**
                          (no chunk cap, streamed)
7. Auth                 → cookie/Bearer/Basic (per-path allow matrix)
8. Csrf                 → state-changing methods, cookie auth only
                          (DESIGN-AUTH.md §3.3)
9. AclScope             → enforces an app password's sc_auth::Scope (see below)
10. Handler
11. ErrorMapper         → maps internal errors into the §1.1 envelope; strips
                          detail from every 5xx regardless of origin
12. AuditSink           → records failed and state-changing requests
```

axum layers wrap outside-in in the order they're *added*, so producing this
request-order requires adding them in reverse in `build_router`
(`crates/sc-http/src/lib.rs`): `error_mapper`/`audit_sink` innermost, then
`scope_gate`, `csrf`, `auth`, the body-limit router split, `rate_limit`,
`security_headers`, `host_guard`, `trusted_proxy`, `request_id` outermost.

**Step 9 is narrower than earlier drafts of this document claimed.**
Resolving a virtual path to `(ShareRoot, SafePath)` — the actual ACL
decision — happens **inline inside `sc-core`/`CoreApi`**, not as an HTTP
layer; see `core_api::Resolved`'s doc comment for why that lookup belongs
there and not here. What *is* a layer at this position
(`crates/sc-http/src/middleware.rs::scope_gate`) is the piece nothing
downstream used to check at all: an app password's `sc_auth::Scope`
(`DESIGN-AUTH.md` §5.2). Before this layer existed, every handler called
`state.core.<op>(principal.user, ...)` and silently discarded
`principal.scope` — a "read-only" app password had exactly the file access
of an unrestricted one. `scope_gate` closes the `scope_perms` half with a
static per-route table (`RouteScope`) and, via `share_scope_gate`, the
`scope_shares` half by inspecting the request's own path/body. The
WebDAV/compat equivalent is `sc-server::app::dav_authenticate`.

### 9.1 Security headers

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
  img-src 'self' https://content.example.com data: blob:; connect-src 'self' wss://…;
  frame-src https://content.example.com; frame-ancestors 'none'; base-uri 'none';
  form-action 'self'; object-src 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-site
Permissions-Policy: geolocation=(), camera=(), microphone=(), interest-cohort=()
```

`style-src 'unsafe-inline'` is a practical necessity for Svelte's scoped
styles and inline MD3 token injection. Never added to `script-src`. A
nonce-based approach is the intended long-term replacement.
