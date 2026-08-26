# The v1 API

> This document describes a from-scratch rebuild. The existing route table
> (`go/internal/server/routes.go`) and the client's call sites
> (`web/src/lib/api/`) are referenced as a behavioral specification only.
> The new implementation is written completely from scratch; nothing is
> copied.

Written ahead of the rest of phase 3 because it changes the contract every
fiber handler is built against. The decision it records: the native web API
is redesigned wholesale under `/api/v1`, every endpoint is renamed into one
consistent scheme, and the shipped frontend is rewired to match in the same
phase. No compatibility routes for the old spellings; the old API dies with
the old stack. The compat surfaces (WebDAV, Nextcloud OCS, `/s/{token}`)
are out of scope and keep their wire shapes.

## Why wholesale

The current surface grew route by route and shows it:

- `/api/shares` means the caller's public links while `/api/admin/shares`
  means the admin's shared folders: two unrelated resources under one noun.
- Three operations have two spellings each, mounted side by side and
  documented in `routes.server-only` as load-bearing: `/api/fs/link` beside
  `/api/shares` (same four handlers), `POST` beside `PUT /api/fs/write`,
  `DELETE /api/jobs/{id}` beside `POST /api/jobs/{id}/cancel`.
- RPC verbs (`/fs/copy`) and REST resources (`PATCH /admin/users/{id}`)
  mix without a rule saying which applies where.
- `/api/auth/*` holds both authentication (login, session) and account
  self-management (password, TOTP, app passwords, SMB credential).
- Settings live in three places: `/admin/server-settings/{section}`,
  `/admin/upload-settings`, `/admin/index/settings`.
- `/api/recent`, `/api/trash`, `/api/setup`, `/api/health`, `/api/events`
  sit uncategorized at the root.

## Rules

Every v1 route follows all of these; the route table test enforces the
mechanical ones.

1. **Base and version.** Everything the web client calls lives under
   `/api/v1`. The version is in the path so the next breaking change is a
   `v2` beside `v1`, not another flag day.
2. **One spelling per operation.** No aliases, no second verb for the same
   handler. The "second spelling" section of `routes.server-only` ends
   empty.
3. **Resources are plural nouns** with standard CRUD: `GET`/`POST` on the
   collection, `GET`/`PATCH`/`DELETE` on `/{id}`.
4. **Non-CRUD actions are `POST /{resource}/{id}/{action}`** (or
   `POST /{resource}/{action}` for collection-level actions). Actions are
   verbs: `retry`, `wipe`, `cancel`, `restore`, `purge`, `apply`, `build`.
5. **File operations are RPC verbs** under `files/`: a file path is an
   argument, never a URL segment. Reads carry `?path=`; writes carry the
   path in the JSON body. This is deliberate against path-based REST,
   which loses to slash encoding and cannot express batch operations.
6. **Path segments are kebab-case; JSON fields are snake_case.**
7. **Errors are one envelope**, the apierr shape, mapped once
   (`http/02-error-mapping.md`).
8. **Access class follows category.** The route table declares a default
   per category and only exceptions per route: `admin/*` is session-only;
   `auth/*` and `account/*` are session-only except the login and OIDC
   entry points, which are public; `files/*`, `links/*`, `trash/*`,
   `jobs/*`, `uploads/*`, `search/*` are permission-scoped and reachable
   with an app password holding the bits; `system/health` is public,
   `system/setup` is the setup gate's own rule, `events` is any
   authenticated caller.

## The category map

Nine categories. Every current route appears exactly once in the tables
below; the old spelling is the behavioral reference for the new one.

### auth: authentication itself

| v1 | Replaces | Notes |
| --- | --- | --- |
| `POST /api/v1/auth/login` | `POST /api/auth/login` | |
| `POST /api/v1/auth/login/totp` | `POST /api/auth/login/totp` | second factor step |
| `POST /api/v1/auth/logout` | `POST /api/auth/logout` | |
| `GET /api/v1/auth/session` | `GET /api/auth/session` | |
| `GET /api/v1/auth/oidc/config` | `GET /api/auth/oidc/config` | |
| `GET /api/v1/auth/oidc/start` | `GET /api/auth/oidc/start` | browser navigation |
| `GET /api/v1/auth/oidc/callback` | `GET /api/auth/oidc/callback` | browser navigation |

### account: the caller's own account (split out of auth)

| v1 | Replaces |
| --- | --- |
| `POST /api/v1/account/password` | `POST /api/auth/password` |
| `GET /api/v1/account/sessions` | `GET /api/auth/sessions` |
| `DELETE /api/v1/account/sessions/{id}` | `DELETE /api/auth/sessions/{id}` |
| `GET /api/v1/account/app-passwords` | `GET /api/auth/app-passwords` |
| `POST /api/v1/account/app-passwords` | `POST /api/auth/app-passwords` |
| `DELETE /api/v1/account/app-passwords/{id}` | `DELETE /api/auth/app-passwords/{id}` |
| `POST /api/v1/account/app-passwords/{id}/wipe` | `POST /api/auth/app-passwords/{id}/wipe` |
| `POST /api/v1/account/totp/setup` | `POST /api/auth/totp/setup` |
| `POST /api/v1/account/totp/enroll` | `POST /api/auth/totp/enroll` |
| `POST /api/v1/account/totp/disable` | `POST /api/auth/totp/disable` |
| `GET /api/v1/account/totp/recovery-codes` | `GET /api/auth/totp/recovery-codes` |
| `POST /api/v1/account/totp/recovery-codes` | `POST /api/auth/totp/recovery-codes` |
| `POST /api/v1/account/smb` | `POST /api/auth/smb` |
| `POST /api/v1/account/smb/password` | `POST /api/auth/smb/password` |
| `DELETE /api/v1/account/smb/password` | `DELETE /api/auth/smb/password` |
| `POST /api/v1/account/oidc-link/start` | `POST /api/auth/oidc/link/start` |
| `DELETE /api/v1/account/oidc-link` | `DELETE /api/auth/oidc/link` |

### files: file operations, RPC verbs

| v1 | Replaces | Notes |
| --- | --- | --- |
| `GET /api/v1/files/list` | `GET /api/fs/list` | |
| `GET /api/v1/files/stat` | `GET /api/fs/stat` | |
| `GET /api/v1/files/read` | `GET /api/fs/read` | `?download=1` unchanged |
| `GET /api/v1/files/size` | `GET /api/fs/size` | the aggregate rollup |
| `GET /api/v1/files/thumbnail` | `GET /api/fs/thumb` | renamed whole word |
| `POST /api/v1/files/mkdir` | `POST /api/fs/mkdir` | |
| `POST /api/v1/files/write` | `POST` and `PUT /api/fs/write` | one verb; `PUT` alias dies |
| `POST /api/v1/files/delete` | `POST /api/fs/delete` | batch |
| `POST /api/v1/files/move` | `POST /api/fs/move` | batch, preflight in body |
| `POST /api/v1/files/copy` | `POST /api/fs/copy` | batch |
| `POST /api/v1/files/rename` | `POST /api/fs/rename` | |
| `POST /api/v1/files/archive` | `POST /api/fs/archive` | zip stream |
| `GET /api/v1/files/archive/list` | `GET /api/fs/archive/list` | zip listing |
| `GET /api/v1/files/recent` | `GET /api/recent` | moved into its category |

### links: the caller's public share links (renamed resource)

The collision breaker. "Share" now means only the admin's shared folder;
the thing a user mints for a stranger is a link, matching the core's own
vocabulary (`core/10-share-links.md`).

| v1 | Replaces |
| --- | --- |
| `GET /api/v1/links` | `GET /api/shares` and `GET /api/fs/link` |
| `POST /api/v1/links` | `POST /api/shares` and `POST /api/fs/link` |
| `PATCH /api/v1/links/{id}` | `PATCH /api/shares/{id}` and `PATCH /api/fs/link/{id}` |
| `DELETE /api/v1/links/{id}` | `DELETE /api/shares/{id}` and `DELETE /api/fs/link/{id}` |

### trash

| v1 | Replaces |
| --- | --- |
| `GET /api/v1/trash` | `GET /api/trash` |
| `POST /api/v1/trash/restore` | `POST /api/trash/restore` |
| `POST /api/v1/trash/purge` | `POST /api/trash/purge` |

### jobs: long operations

| v1 | Replaces | Notes |
| --- | --- | --- |
| `GET /api/v1/jobs` | `GET /api/jobs` | |
| `GET /api/v1/jobs/{id}` | `GET /api/jobs/{id}` | |
| `POST /api/v1/jobs/{id}/cancel` | `POST /api/jobs/{id}/cancel` and `DELETE /api/jobs/{id}` | one spelling; `DELETE` dies |

### uploads: the resumable upload protocol

| v1 | Replaces | Notes |
| --- | --- | --- |
| `OPTIONS`/`POST /api/v1/uploads` | same at `/api/uploads` | TUS discovery and create |
| `HEAD`/`PATCH`/`DELETE`/`OPTIONS /api/v1/uploads/{id}` | same at `/api/uploads/{id}` | TUS; methods are the protocol's own |

### search

| v1 | Replaces |
| --- | --- |
| `GET /api/v1/search/stream` | `GET /api/search/stream` |

### admin

| v1 | Replaces | Notes |
| --- | --- | --- |
| `GET`/`POST /api/v1/admin/users` | same at `/api/admin/users` | |
| `PATCH`/`DELETE /api/v1/admin/users/{id}` | same | |
| `GET /api/v1/admin/users/{id}/oidc` | same | |
| `DELETE /api/v1/admin/users/{id}/oidc` | same | |
| `GET`/`POST /api/v1/admin/groups` | same | |
| `PATCH`/`DELETE /api/v1/admin/groups/{id}` | same | |
| `POST /api/v1/admin/groups/{id}/members` | same (was `{gid}`) | id spelling unified |
| `DELETE /api/v1/admin/groups/{id}/members/{user}` | same | |
| `GET`/`POST /api/v1/admin/grants` | same | |
| `PATCH`/`DELETE /api/v1/admin/grants/{id}` | same | |
| `GET`/`POST /api/v1/admin/shares` | same | now the only "shares" |
| `PATCH`/`DELETE /api/v1/admin/shares/{id}` | same | |
| `POST /api/v1/admin/shares/{id}/retry` | same | |
| `GET /api/v1/admin/audit` | same | |
| `GET /api/v1/admin/storage` | same | |
| `POST /api/v1/admin/smb/apply` | same | operator's apply-now |
| `POST /api/v1/admin/index/build` | same | |
| `GET /api/v1/admin/index/estimate` | same | |
| `GET /api/v1/admin/settings` | `GET /api/admin/server-settings` | whole snapshot |
| `PATCH /api/v1/admin/settings/{section}` | `PATCH /api/admin/server-settings/{section}` | |
| `PATCH /api/v1/admin/settings/upload` | `PATCH /api/admin/upload-settings` | folded in as a section |
| `GET /api/v1/admin/settings` (carries `index`) | `GET /api/admin/index/settings` | folded into the snapshot |
| `PATCH /api/v1/admin/settings/index` | `PATCH /api/admin/index/settings` | folded in as a section |

Settings become one resource with sections; the three scattered settings
surfaces end. The snapshot carries every section, `upload` and `index`
included, and each section PATCHes at one place.

### system

| v1 | Replaces | Notes |
| --- | --- | --- |
| `GET /api/v1/system/health` | `GET /api/health` | container probe; deploy config updates |
| `GET /api/v1/system/setup` | `GET /api/setup` | |
| `POST /api/v1/system/setup` | `POST /api/setup` | |
| `GET /api/v1/events` | `GET /api/events` | WebSocket; kept at category root, it is its own transport |

### Out of scope, unchanged

- `/s/{token}`, `/s/{token}/auth`, `/s/{token}/download`, `/s/{token}/zip`,
  `/s/{token}/drop`: the public link surface a stranger's browser hits.
  Short URLs are the product feature; no version prefix.
- WebDAV and the Nextcloud compat surface: their wire shapes are their
  specs (`http/04`, `http/05`).
- The emergency server's `/emergency/api`: its own tiny surface
  (`settings/02-emergency.md`).

## What dies

- Every `/api/*` route without the `/v1`: no compatibility mounts, no
  redirects. The frontend is rewired in the same phase, and external
  callers of the native API (app passwords over the JSON surface) follow
  the same flag day. WebDAV is the stable machine surface; the native API
  is versioned from v1 onward exactly so this is the last unversioned
  break.
- The three double spellings (write `PUT`, jobs `DELETE`, the whole
  `fs/link` family).
- The `/api/admin/server-settings` and `/api/admin/upload-settings` and
  `/api/admin/index/settings` split.
- `routes.server-only` shrinks to the genuinely server-only set: health,
  events, OIDC navigation, `/s/*`, TUS discovery, `smb/apply`.

## Frontend rewiring

Phase 3 work, same change set as the route table:

- `web/src/lib/api/http.ts`: `BASE` becomes `/api/v1`; every path literal
  updated per the tables above.
- `web/src/lib/api/share.ts` splits its two concerns: the `/s/{token}`
  public-visitor calls stay; the link-management calls move to a renamed
  `links.ts`.
- The mock backend (`mock.ts`) mirrors the same paths, so the dev flip
  stays honest.
- `contractcheck` and `routecheck` re-point at the fiber table and the new
  client paths; both must pass with `routes.allow` empty, as today.

## Rationale

- **Version in the path** over a header: cacheable, greppable, and the
  route table's own test can assert every pattern starts with `/api/v1`.
- **RPC verbs for files** over path-based REST: file paths contain every
  character URLs fight with; `?path=` and JSON bodies sidestep encoding
  entirely, batch operations (delete, move, copy already take lists) have
  no REST spelling worth having, and the current API already made this
  choice; v1 makes it a rule instead of a habit.
- **`links` over `shares`** for the public-link resource: the admin
  resource keeps the noun that matches the core's `ShareDef`, the user
  resource takes the noun that matches the core's `Link`, and the
  collision that produced two spellings of four routes ends.
- **Category-default access classes** turn the per-route requirement
  declarations into exceptions, which is less to get wrong; the startup
  validation that refuses an undeclared route stays.

## Tests

- Route table: every pattern starts `/api/v1/`; no two routes share
  method+pattern; every route's category prefix matches its access class
  or carries an explicit exception; the table contains exactly the routes
  this document lists (the document's tables are the fixture).
- `routecheck` green against the rewired client with `routes.allow` empty.
- `contractcheck` green against the rewired client's types.
- A denial test per category default: app password on an `admin/*` route,
  no credential on an `account/*` route, scoped app password missing a bit
  on a `files/*` route.
