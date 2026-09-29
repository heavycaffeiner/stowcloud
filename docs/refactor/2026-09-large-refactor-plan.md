# Stowcloud large refactor plan

Status: proposal. Backward compatibility with the current code is not a goal.
Every number below was measured on the tree as of 2026-09-29.

## 0. Baseline

| Area | Source files | Source lines | Test files | Test lines |
|---|---|---|---|---|
| backend/internal/app | 11 | 2,399 | 56 | 24,148 |
| backend/internal/feature | 129 | 33,029 | 91 | 28,668 |
| backend/internal/http | 151 | 31,544 | 58 | 13,391 |
| backend/internal/store | 74 | 10,489 | 24 | 8,760 |
| backend/internal/storage (excluding the vault interop fixture) | 26 | ~3,900 | 13 | 3,439 |
| frontend/src (TS/TSX) | 247 | ~43,000 | 0 | 0 |
| frontend/tests + frontend/e2e | n/a | n/a | ~80 | ~10,000 |

Backend: 2,564 `func Test` functions. `internal/app` has 10 lines of test for every line of code (24,148 vs 2,399).
Note: `storage/vault` shows 163k lines only because of `testdata/interop/hash_sha512.hc` (82k lines). It is a fixture, not code.

---

## 1. Backend

### 1.1 Problems

#### B1. The entrypoint cannot be followed by reading forward

The path from `main` to a handler:

```
cmd/sc-engine/main.go  run()
  -> hanami.Spec{Modules: app.Module(...)}        (fx + hanami, reflection-driven)
     -> app/module.go   fx.Provide(Open) / fx.Provide(listener.New) / fx.Invoke(...)
        -> app/open.go  Open(): 827 lines, builds ~30 services by hand
        -> app/mount.go Engine.Mount()
             -> mountNative -> server.Bind(Table(), handlers(), humaNames, Chain())
                  handlers(): map["files.list"] = filesHandler.List ... (150 lines of string keys)
                  huma.go: humaNames []string + a second registration path
             -> dav.Mount, publicLinks.Mount, mountNCTagged (build tag), spa.Install
```

Specific problems:
- Two DI mechanisms stacked: `go.uber.org/fx` and a hand-written `Open()` constructor. fx provides exactly one thing (`*Engine`), so it adds reflection with no benefit.
- Routes are joined to handlers by **string name** (`"files.list"`) across three files: `http/server/v1table.go` (path + access), `app/mount.go` (`handlers()` map), `app/huma.go` (`humaNames`). "Where is `GET /api/v1/files` handled?" takes three greps and cannot be answered with go-to-definition.
- Two HTTP handler styles live together: raw Gin handlers and Huma typed operations (`humabridge`), picked per route with no rule.
- Handler dependencies are passed as function fields (`Fail`, `Refuse`, `Decode`, `WriteJSON: func(c, status, v) { c.JSON(status, v) }`, `Owner`) re-wrapped at every call site in `mount.go`. That is dependency injection of the standard library.

#### B2. Folder and package names mislead

- `internal/feature/files` declares `package core`. `internal/feature/uploads` declares `package upload`. Imports need aliases (`core "…/feature/files"`), so a reader cannot map an identifier to a directory.
- `internal/app/settings` is imported as `live`.
- `store` vs `storage`: two top-level directories with near-identical names. `store` holds SQLite DBs (`state`, `cache`, `journal`); `storage` holds filesystem/VFS/object store. Layercheck even allows `store -> storage` and `storage -> store`, so they are one layer split in two.
- Three `limits` packages: `storage/limits`, `store/limits`, `feature/uploads/limits`.
- DAV is split across `feature/dav` and `http/dav`. Public links across `feature/files/link*.go`, `feature/shares`, `http/publiclinks`, `http/api/links`.
- `http/api/handler` is a 54-file grab bag (auth, account, admin users, admin fs, settings, oidc projection).
- `store/state` is one package of 71 files (every table's Go API plus a `*_sql.go` twin per table). It is cohesive by design (one database, one transaction owner), so keep it one package; the problem is only its name beside `storage`.
- `runtime` (events, listener, restart, tasks) is a tier of its own for 1,145 lines.

#### B3. Tests and logic are mixed, and the test volume is out of proportion

- `internal/app` holds 11 wiring files and 56 test files. These are end-to-end HTTP tests (41 call `Open`, then `serve()` behind a real listener) that live beside the wiring because that is where `Open` is. They test features (DAV, Nextcloud, OIDC, trash, uploads), not `app`.
- Fixtures are private per package (`app/davfixture_test.go`, `contentShare`, `contentShareGrant`, `contentShareAt`, `bootWithUser` ...) so each package grows its own harness.
- Source-text tests pin implementation instead of behavior (15 files use `go/parser` on production code). Examples:
  - `cmd/sc-engine/bootstrap_source_test.go` parses `main.go` and asserts `app.Module` is called inside a literal. Any refactor of `main` breaks it; no user-visible bug does.
  - `http/apierr/sentinels_test.go`, `store/state/davlock_test.go`, `store/state/davsnapshot_test.go`, `store/state/loginflowdelivery_test.go`, `feature/admin/settings/check/parity_test.go`, `http/dav/oneescaper_test.go`.
- 54 assertions on `strings.Contains(err.Error(), …)` pin wording.
- Per-variant functions where a table would do: `filesread_test.go` has `TestReadingARange`, `TestReadingAnOpenEndedRange`, `TestReadingASuffixRange`, `TestARangePastTheEndIsRefused`, `TestAMultiRangeRequestIsRefused`, each booting a full engine for one Range header.
- Unit tests in `feature/*` re-test at the service level what `app/*_test.go` tests over HTTP (e.g. `feature/files/transfer_test.go` 25 KB and `app/filesread_test.go` copy/move sections).
- `backend/testdata/results/*.json` (26 files) are benchmark output from `tools/namesearchbench`, committed.

#### B4. Ceremony around the code

- `backend/nolint.budget`: 541 lines of prose history to hold one number.
- `routes.allow`, `routes.server-only`, `deps.allow`, plus 10 custom tools in `backend/tools` (`koscan`, `freshscan`, `vetsecret`, `vetgo`, `speccheck`, `settingscheck`, `contractcheck`, `routecheck`, `layercheck`, `enginedrive`). Each has its own test file. Several duplicate `golangci-lint` rules (`vetsecret` vs `gosec`/gitleaks; `vetgo` vs `go vet`).
- Comments: most exported fields carry 3 to 8 lines of history and narrative (`open.go` `Options` is 45 lines for 9 fields). The `defaultRatePerSecond` comment is 7 lines for 2 constants.
- `backend/sc-engine` (65 MB) and `.dev/sc-engine` binaries in the working tree (ignored, but left in the directory a new hire opens first).

### 1.2 Target structure

Rule: one directory per concern; package name equals directory name; an HTTP route is found by grepping its path; tests stay beside code only when they are unit tests of that package.

```
backend/
  cmd/
    stowcloud/main.go          # flags -> config.Load -> server.Run. ~60 lines.
    smb-agent/main.go
  internal/
    config/                    # flags, env, sc.toml, defaults (was bootstrap/args + app/settings)
    server/                    # the only wiring package
      server.go                # Run(ctx, cfg): open stores, build services, build router, listen
      routes.go                # every route, written as r.GET("/api/v1/files", files.List) groups
      middleware/              # was http/middleware
    db/                        # was store/*: sqlite open, migrations, one file per table group
      state/  cache/  journal/
    fs/                        # was storage/*: vfs, pathnames, objstore, vault, adapters
    auth/                      # service + http handlers (was feature/auth + http/api/handler/auth_*)
    oidc/
    files/                     # package files (not core): service + handlers + archive + trash
    uploads/                   # package uploads (not upload), directtransfer merged in
    shares/                    # shares + public links + acl (was feature/shares, http/publiclinks, http/api/links, feature/files/link*.go)
    search/
    preview/
    admin/                     # users, groups, grants, settings, logs, storage
    dav/                       # feature/dav + http/dav
    nextcloud/                 # http/nextcloud + app/nc*.go
    smb/
    emergency/
    jobs/                      # was runtime/tasks + events
    platform/                  # clock, security, system/jail, network (unchanged, no deps)
    web/                       # SPA embed (was http/spa)
  test/
    e2e/                       # HTTP-level tests, one shared harness
      harness.go               # Boot(t) -> *Client with Login, AsUser, Upload helpers
      files_test.go  dav_test.go  nextcloud_test.go  auth_test.go  security_test.go ...
    testdata/
```

Decisions:
- **Drop fx and hanami module composition from the product path.** `server.Run` builds dependencies in plain Go, in order, in one function. If hanami must stay for listener generations and restart, call it from `server.Run` as a library, not as the composition root.
- **Pick one handler style.** Keep Huma for typed JSON (it gives OpenAPI for free). Streaming endpoints (download, upload, events, DAV) stay raw Gin. Delete `humabridge`, the string-keyed `server.Handlers` map, `humaNames`, and `v1table.go`'s name indirection. Access class becomes middleware on the route group (`admin := r.Group("/api/v1/admin", requireAdmin)`), so the table and the policy are one file.
- **Handlers take a struct of real dependencies**, not function fields. `handler.Fail`/`Refuse`/`Decode` become ordinary package functions in `server/httpx` imported directly.
- **Each feature package owns its handlers.** `files.Handler` sits in `internal/files/http.go`. No separate `http/api/<feature>` mirror tree.
- Merge `store` and `storage` responsibilities into `db` (SQL) and `fs` (bytes). Remove the tier map from layercheck; the remaining rule is "`platform` imports nothing internal; feature packages don't import `server`". `go vet` + a 20-line import check suffices.

### 1.3 Test plan

Keep, and move to the new layout:
- Security and access-control regressions: `app/security_regression_test.go`, cross-account tests (`TestADownloadTicketDoesNotCrossAccounts`, `TestAContentReferenceDoesNotCrossAccounts`, `TestAForgedContentReferenceIsRefused`), path traversal in `storage/pathnames`, OIDC provider, account security.
- Data-loss paths: conditional write (`TestAConditionalWriteIsRefusedAfterTheFileChanges`), move onto taken name, `TestClosingTheEngineWaitsForACopyToRecordItsOutcome`, upload finalize/spool, vault interop against the real `.hc` fixture, journal/db durability.
- Protocol conformance with external clients: DAV lock/propfind/proppatch, Nextcloud client flows.
- Pure logic units: range parsing, etag, intervals, pathnames, quota math, settings validation.

Delete:
- Every test that parses production source with `go/parser` or reads `.go` files (15 files).
- Tests of the custom `backend/tools` that are themselves deleted (see 1.4).
- Assertions on error wording; replace with `errors.Is` or status code checks where the behavior matters, delete where it does not.
- Duplicates: when a behavior is tested over HTTP in `test/e2e`, delete the service-level copy unless it covers a branch HTTP cannot reach.
- `backend/testdata/results/` (benchmark output) and `tools/namesearchbench` if the benchmark is not run in CI.

Merge:
- Per-variant functions become one table test sharing one booted engine (`filesread_test.go` range cases: 5 engines to 1).
- All `contentShare*`, `bootWithUser`, `davfixture` helpers become `test/e2e/harness.go`.

Target: backend test lines from ~80k to roughly 30k to 35k [INFERENCE: estimate from sampled files; confirm per package during the move]. Test run time drops with it, because most of the removed cost is one full engine boot per function.

### 1.4 Tooling and noise

- Delete `nolint.budget`; configure `golangci-lint` `nolintlint` with `require-explanation: true` instead.
- Delete `koscan`, `freshscan`, `vetgo`, `vetsecret`, `speccheck` unless a concrete defect they caught in the last 90 days can be named. `freshscan` and `speccheck` exist to police the previous rebuild program (carried comments, phase documents matching the tree); with that program retired (see 1.5) they have nothing left to check. `speccheck` already SKIPs in `verify.sh` for lack of spec inputs.
- `routecheck`, `contractcheck`, `settingscheck` guard real defects: the client calling a path the server does not mount, the client reading a field the server never sends, a saved setting nothing loads. They are a hand-built substitute for a shared schema. Delete them in the same phase that generates the client from OpenAPI (section 2.2), not earlier; until then they are the only thing holding the contract. `routes.allow`/`routes.server-only` go with `routecheck`.
- Keep `deps.allow`: `supply-chain.yml` enforces it independently of `verify.sh`, so it is policy, not ceremony. Drop its duplicate check from `verify.sh`.
- Comment pass: comments hold what and why in one or two lines. Remove history ("the old tree's count is 276", "It shipped exactly once").

### 1.5 Findings the first draft missed

#### B5. A previous rebuild program is still in the tree and contradicts the code

`docs/internal/refactor/` (README, `00-decisions.md` D1 to D21, per-area specs) is a full rebuild plan with binding "Instructions to implementers". Its decisions no longer describe the code:
- D2/D19: "The HTTP engine moves to go-fiber". `go.mod` has no fiber; the code is Gin + Huma + hanami.
- D3/D4: target tree `go/engine/{kit,infra,store,service,http}`. The code is `backend/internal/{platform,storage,store,feature,http,runtime,bootstrap,app}`.
- D14: gates `koscan`, `vetgo`, `vetsecret`, `freshscan`, `speccheck` exist to enforce that program.

A new hire reading the docs gets a third architecture beside the code and this plan. `docs/internal/frontend-react-rework.md` is the same for the frontend: its target tree is `web/src/…`, which is the empty `web/` directory at the root.

Decision: this plan supersedes both. The entire `docs/internal/` tree is ignored and has no tracked files, so Git does not preserve either plan. Phase 0 leaves these local files in place; archive them explicitly before any deletion. `ARCHITECTURE.md` states which decisions carry over. Decisions worth keeping, because they are correct and the code already follows them:
- D5: the domain core stays one package, because `Resolved` (unexported fields, only obtainable through `Resolve`) is the ACL guarantee. The target layout in 1.2 must keep `files` as one package for the same reason, and not split `Resolve` out.
- D10: one home for the identity tuple (`store/ident`).
- D12: data compatibility is a hard contract. See B6.
- D15: the `/api/v1` route naming rules.

#### B6. "No backward compatibility" must not reach the data directory

The user said backward compatibility with existing *code* is not a concern. The on-disk formats are a different contract: `state.db` schema history (`store/state/sql.go` schemaV1 to V3+, migrations are append-only by index in `dbfile/migrate.go`), the key ring and sealed values (`token_enc`, `key_ver`, TOTP and SMB secrets), instance id and file id derivations (Nextcloud and DAV clients re-sync if these change), and upload resume records. Package moves must carry these byte-for-byte. Rule for phase 2: move `sql.go` files unchanged; a schema change is a new appended migration in its own commit, never an edit to an existing step. Add a test that opens a `state.db` produced by the current release (checked-in fixture) with the new binary.

#### B7. Configuration has three entry points and one of them is fake

- CLI flags in `main.go` (`-addr`, `-data`, `-plain`) and again in `serve.go` via `bootstrap/args.ParseServeArgs` (`--data-dir`): two spellings for the data directory (`-data` vs `--data-dir`).
- The settings document stored as JSON in the `settings` table, edited through the `settings` subcommand and the admin UI, loaded by `feature/admin/settings/runtimecfg`, applied live by `app/settings` (`live.Coordinator`), with a catalogue in `feature/admin/settings/catalogue` and a validator in `.../check`. Four packages for one document.
- `.dev/sc.toml` exists, and the admin UI hint says "anything settable in config.toml can…", but no Go code reads a TOML file (no match for `sc.toml` or `config.toml` under `backend/`).

Target: `internal/config` owns flags (one spelling), the settings document type, defaults, validation and the catalogue; `server` applies it. Remove the TOML references or implement them; do not keep a documented file nothing reads.

#### B8. Logger is optional everywhere

17 constructors do `if logger == nil { logger = slog.Default() }` (`feature/files/core.go`, `auth/auth.go`, `oidc/client.go`, `search/svc/open.go`, `smb/publish/publish.go`, …). The engine then fans out to the log book in `open.go`. A service built without the engine's logger writes to the default handler and skips the admin log view silently. Make the logger a required constructor argument; tests pass `slog.New(slog.DiscardHandler)`.

#### B9. hanami is a hidden framework

`hanami` (the author's own module, v0.2.0) owns the process: config load, Linux security re-exec, fx composition, listener generations, restart protocol. 7 files import it across `cmd`, `app`, `runtime/listener`, `runtime/restart`, `bootstrap/sandbox`. Its behavior is not visible from this repository. Dropping fx (1.2) does not remove hanami; the plan needs an explicit boundary: `cmd/stowcloud/main.go` calls `hanami.Run` with one `Start(ctx) error` function supplied by `server`, and nothing under `internal/` except `server/listener.go` imports hanami. Document in `ARCHITECTURE.md` what hanami does on start, reload and restart.

#### B10. Error model is sound; keep it

`http/apierr` has one classifier (`Classify`) with REST, DAV and OCS writers, and `handler.Fail` funnels service errors through it. The problem is only that `Fail`/`Refuse`/`Decode` are passed as function fields (B1). Keep `apierr` as is and import it directly.

---

## 2. Frontend

### 2.1 Problems

#### F1. Zustand is wrapped until it stops being Zustand

- `lib/store/create.ts` `defineStore()` wraps `createStore` and adds `peek`, `reset`, `api`. `hooks/use-store.ts` wraps `useStore` again. Components call `useStore(ui, s => s.compact)` then call actions as `ui.setDetails(...)` on the module object: two access paths for one store.
- Local component state is implemented as a **per-component Zustand store**: `hooks/use-component-state.ts` (`useComponentState`, `usePatchState`) and `app/hooks/use-route-store.ts` (`useRouteStore`). React's `useState` is used exactly **once** in 247 files. This replaces a built-in hook with a heavier one and gives no cross-component sharing, which is the only reason to use a store.
- `useRouteStore` subscribes to the whole state object (`useStore(storeRef.current)` with no selector), so every patch rerenders the route.
- Store modules read each other directly: `search.store.ts` `searchTarget()` calls `ui.getState().compact`.

#### F2. Too much state, most of it derivable or UI-transient

- `BrowseState` (`app/routes/browse/logic/types.ts`) has **39 fields** in one patch-object: 10 menu open flags + 6 menu positions + 4 trigger `HTMLElement`s, `conflictRetry` callback, `snackbar`, `previewOpen` + `previewIndex`, `filterType`/`filterDate` that belong in the URL, `marqueeRect`/`marqueeScroll` that belong to a pointer hook.
- DOM elements (`menuTrigger`, `newMenuTrigger`, `overflowTrigger`, `sortMenuTrigger`) and callbacks (`conflictRetry`, `UnlockTarget.retry`) are stored in state.
- `AppShell` keeps `lastBrowsePath`, `mobileDrawerOpen`, `folderSelectorOpen`, `accountMenuOpen` in a route store and recomputes nav items with `useMemo` whose deps include a function recreated every render (`browseTarget`), so the memo never hits.

#### F3. Imperative code where declarative code fits

- Custom window events as a message bus: `stowcloud:new` (AppShell -> use-browse-menus) is app code. `sc:lock`/`sc:unlock`/`sc:before-lock` are dispatched by the external `@stowcloud/rclone-crypt` package itself (`dist/index.js` lines 133, 139, 174), so the app cannot delete them; it can only stop consuming them in components.
- 105 `useEffect` calls; ~25 of them attach `window` listeners by hand for outside-click, Escape and resize, duplicated in `Menu.tsx`, `IconButton.tsx`, `use-browse-menus.ts`, `use-shell-lifecycle.ts`, `PreviewDialog.tsx`.
- Focus management by global query: `document.querySelector('[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]')?.focus()` in `Dialog.tsx`, `FileTree.tsx`, `PreviewDialog.tsx`; `document.querySelector('.sc-browse-new-menu button')` in `use-browse-menus.ts`.
- `FileGrid`/`FileTable` synthesize a `new window.MouseEvent('contextmenu', …) as unknown as ReactMouseEvent` to open a menu from the keyboard.
- `PreviewDialog` checks `document.querySelector('mdui-dialog[open]:not(.sc-preview-dialog)')` to decide whether to handle a key.
- Label mapping by nested ternaries: `BrowseRoute.tsx` lines 68-71 (sort, density, filter labels), `AppShell.tsx` line 118 (active nav).

#### F4. Hooks exist but do not encapsulate

- `BrowseRoute` calls 6 route hooks that each take the whole `state` + `patch` (`useBrowseActions({ path, entries, selected, contextEntry, renameTarget, canCreate, navigate, t, patch })`). The hooks are file splits of one component, not units with their own state and API.
- `use-shell-lifecycle.ts` exports 7 hooks that all receive `setShell`.
- Missing generic hooks, so each file re-implements them: `useOutsideDismiss`, `useEventListener`, `useMediaQuery` (resize -> `ui.setCompact`), `useBeforeUnload`, `useHashTab` (`use-admin-tab.ts` and `use-settings-tabs.ts` both parse `location.hash`).

#### F5. Router state is split across React Router, Zustand and `window`

- Route params: React Router (`b/*`, `edit/*`).
- Search panel: `search.store` (`open`, `scope`, `snapshot`) plus the `/search?path=` route, and the decision between them reads `ui.getState().compact`.
- Tabs: `window.location.hash` read by hand in admin and settings with `hashchange` listeners; `/settings/security` redirects by string-concatenating `window.location.search`.
- Browse filters, sort, view mode: sort/view in `view.store` (persisted), filters in `BrowseState`; neither in the URL, so a filtered view cannot be linked or restored with Back.
- Preview index: `BrowseState.previewIndex`, not URL, so Back does not close the preview.
- `lastBrowsePath` is tracked in AppShell state to rebuild the Files link.

Why it cannot be cleanly put into Zustand: the URL is already a store with its own history, and mirroring it into Zustand creates two sources of truth that must be kept in sync by effects. The fix is the opposite direction: move navigational state *into the URL* with typed params and keep Zustand for state that is not addressable.

#### F6. React Query is present but half-used

What is already right: `lib/query/*.ts` defines `queryOptions`/`mutationOptions` factories and a `keys` module; `live.ts` turns WebSocket frames into `invalidateQueries`.

Problems:
- Components compose `useQuery(sessionQuery())`, `useMutation(logoutMutation())` themselves; there is no `useSession()` / `useLogout()` hook, so the same `isUnauthenticated`/`definitiveFailure` logic lives in `AppShell` and elsewhere.
- 29 direct `api.*()` calls outside `lib/query` (`PreviewDialog.tsx` 6, `download-sw.ts` 5, ...), bypassing cache and error handling.
- `invalidateDirs` imports the singleton `queryClient`; mutations invalidate via module import instead of the `onSuccess` context client.
- `lib/api` is **8,650 lines** hand-written: `http.ts` 2,384, `mock.ts` 3,235, `types.ts` 1,656. `http.ts` still says "the backend does not exist yet". The backend already serves an OpenAPI document (`admin.openapi` via Huma), so types and client can be generated.
- `mock.ts` + `mock-seed.ts` is a second fake backend (3.3k lines) kept in sync by hand and toggled by `VITE_API_MOCK`.

#### F7. No component layer; mdui leaks everywhere

- `lib/ui` wraps some mdui elements (`Button`, `Dialog`, `TextField`, `Switch`, ...), but 63 raw `<mdui-*>` tags remain in 35 files: `<mdui-dialog>` in `EditPage`, `EmergencyPage`, `TrashPage`, `PathPickerDialog`, `PreviewDialog`, `EditConflictDialog`; `<mdui-button>` in 8 files; `<mdui-circular-progress>` in 9 files even though `ProgressCircular.tsx` exists; `<mdui-segmented-button-group>` with `event.currentTarget as HTMLElement & { value }` casts in `SettingsPanels.tsx`.
- `lib/ui/logic/mdui.ts` registers 32 mdui components globally, whether or not a screen uses them.
- Styles: 8,095 lines of `vanilla-extract` used only through `globalStyle(".sc-…")`. That is global CSS with a build step: no scoping, no type-checked class names, and class names like `sc-browse-context` are hand-typed in TSX (957 `className=` uses).
- The `sc-` class prefix and `--mdui-*` tokens are spread through every file, so both the design system and the naming scheme are visible at call sites.
- Components are written as single enormous lines: 136 JSX lines over 300 chars; the longest is 8,239 chars (`LogsSection.tsx`), then `PreviewDialog.tsx` 3,566, `PublicSharePage.tsx` 3,082. These are unreadable and undiffable.
- `/* i18n */ 'logs.level_debug'` string statements exist only to satisfy `tools/i18n-check.mjs`.

#### F8. Other frontend findings

- `Dialog` accepts both `onClose` and `onclose`.
- Two zip libraries: `@zip.js/zip.js` (reading archives in `zip-listing.ts`) and `client-zip` (streaming a zip for download in `download-sw.ts`). They do different jobs; merging onto `@zip.js/zip.js` for both is possible but is a crypto-path change, so only do it with the download tests green.
- Two virtualization engines: `lib/ui/VirtualList.tsx` uses `@tanstack/react-virtual`; `FileTable`, `FileGrid` and `SearchResults` use the hand-written `lib/virtual/windowing.ts` (`computeWindow`, `computeScaleMapping`). One component family should own windowing. Keep the hand-written one only if the scale mapping (row counts beyond the browser's max element height) is something react-virtual cannot do; otherwise move all lists to react-virtual.
- The API base URL and mock switch are re-declared in 5 modules: `const IS_MOCK = …` and `const BASE = … + '/api/v1'` in `oidc.ts`, `setup.ts`, `share.ts`, `upload/transport.ts`, plus `http.ts`. `client.ts` calls itself "the ONE flip switch" while four other files flip it themselves.
- `lib/crypto/encrypted-shares.ts` receives its fetcher through `setEncryptedSharesSource()` from `client.ts` to break an import cycle (`client` -> `http` -> `encrypted-shares` -> `client`). That is a symptom of `lib/api` mixing transport with domain logic; with per-feature query hooks the cycle disappears.
- i18n: `i18next` + `react-i18next` are installed, but `use-i18n.ts` calls `useTranslation` only to subscribe to re-renders and returns a module-level `t` that bypasses the hook. `tp()` hand-rolls plural suffixes (`_one`/`_other`), which i18next already does with `count`. `formatModifiedDateNs` prints `YYYY-DD-MM` (day before month) for every locale, which is non-standard in both Korean and English. Catalogues are 104 KB (ko) and 90 KB (en) of flat keys generated from English sentences (`setup.on_server_s_first_start`, `browse.press_this_button_to_set_up_your_first_folder`), and both are bundled up front.
- Upload pipeline: `lib/upload/worker.ts` (997 lines) mixes the command protocol, chunk scheduling, resume records, retry budgets and the direct-to-object-store path. `upload.store` holds the tray rows; `queue.ts` is the bridge. The shape is right (worker off the main thread) but the worker needs the same split as a backend service: protocol types, scheduler, transport, persistence.
- `tsconfig.json` sets `allowJs` + `checkJs` over `src` and `tests` together with `WebWorker` and `DOM` libs in one program. Service worker, upload worker and page code share one global type space, which is why `service-worker.ts` needs `self as unknown as ServiceWorkerGlobalScope`. Use TS project references: `app`, `worker`, `sw`, `node` (config and tools).
- `vite.config.ts` resolves React Router's `production` build by string-replacing paths in `node_modules` (`reactRouterProduction = reactRouterDevelopment.replace(...)`) and forces `production` in `resolve.conditions` for dev too. Fragile against any React Router release; disappears with the router migration.
- Error handling UI: one `RouteErrorBoundary` whose fallback title is the literal `Stowcloud` and whose message is the raw `error.message`. No boundary per feature, so a crash in the job tray takes down the whole shell. No global mutation error surface: each screen invents its own snackbar/alert state.
- `lib/` holds feature code (`lib/admin/log-view.ts`, `lib/upload/*`, `lib/search`) next to generic code (`lib/format`, `lib/ui`), so `lib` has no meaning.
- `app/routes/<page>/{hooks,logic}` and `features/<x>/{hooks}` both exist; a feature's code is split between `app/routes/x`, `features/x`, `lib/query/x`, `lib/store/x`, `styles/features/x`.
- No ESLint (no `react-hooks/exhaustive-deps`), which is how `AppShell`'s dead memos got through.
- Tests: `tests/lib/api/mock.test.ts` (26 KB) tests the mock backend; `http.test.ts` (20.6 KB) tests request plumbing. Components have no tests at all; e2e covers flows.

### 2.2 Target architecture

```
frontend/src/
  main.tsx
  app/
    providers.tsx              # QueryClientProvider, RouterProvider, theme
    router.tsx                 # route tree (TanStack Router, file or code based)
    shell/                     # AppShell, NavigationDrawer, NavigationBar, AccountMenu
  api/
    generated/                 # openapi-typescript output + typed fetch client; never edited
    fetcher.ts                 # base URL, CSRF, ApiError, session-death hook
  features/
    files/
      api.ts                   # useDirectory, useStat, useMoveFiles, useDeleteFiles ... (hooks)
      components/              # FileTable, FileGrid, FileTree, Breadcrumb, dialogs
      hooks/                   # useSelection, useMarquee, useFileKeyboard
      routes/                  # BrowsePage, EditPage
      store.ts                 # only if state is shared across the feature and not in the URL
    search/  uploads/  shares/  preview/  trash/  recent/  links/  settings/  admin/  auth/
  ui/                          # design system: the ONLY place that imports mdui
    Button.tsx  IconButton.tsx  Dialog.tsx  ConfirmDialog.tsx  Menu.tsx  TextField.tsx
    SegmentedControl.tsx  Spinner.tsx  Progress.tsx  Checkbox.tsx  Switch.tsx  Snackbar.tsx
    Page.tsx  EmptyState.tsx  ErrorState.tsx  LoadingState.tsx  VirtualList.tsx
  hooks/                       # generic only: useEventListener, useOutsideDismiss, useMediaQuery, useBeforeUnload
  i18n/
  lib/                         # pure functions only: format, path, crypto, zip
  workers/
```

Rules:
1. **Server state lives only in React Query**, exposed as one hook per endpoint in `features/<x>/api.ts`. Components never import `queryOptions`, `keys`, `queryClient` or `api`. Mutations invalidate through `useQueryClient()` in `onSuccess`.
2. **URL state lives only in the router.** Browse path, preview target, filters, sort, search query and scope, admin/settings tab: typed search params. Back/forward and deep links work without code.
3. **Zustand only for cross-component client state that is not in the URL**: theme, sidebar collapsed, upload queue, job tray, E2EE unlock state (replaces `sc:lock` events). Use plain `create()` with selectors; no `defineStore`, no `use-store.ts`, no `getState()` in components.
4. **Local state is `useState`/`useReducer`.** Delete `useComponentState`, `usePatchState`, `useRouteStore`.
5. **mdui is imported only in `ui/`.** Feature code writes `<ConfirmDialog open onConfirm>`, never `<mdui-dialog>`. Each `ui/` component imports its own mdui module instead of the global 32-import registry.
6. **Styles are colocated and scoped**: `Component.css.ts` with `style()` (not `globalStyle`) exporting class names, or CSS Modules. Pick one; vanilla-extract `style()` keeps the current build. Design tokens in `ui/theme.css.ts` via `createThemeContract`. No `sc-` prefix: scoping makes it unnecessary.
7. Formatter + lint: Prettier (print width 120) and ESLint with `react-hooks`. The 8,000-char lines go away on the first format run.

Router decision: **TanStack Router**. It has typed, validated search params (`validateSearch`), loaders that call `queryClient.ensureQueryData`, and route-level `beforeLoad` for the auth gate (replaces `screenOf` + `<Navigate>` in `AppShell`). React Router 7 can do most of this in framework mode, but typed search params are what this app needs most (filters, sort, preview, tabs). Staying on React Router is acceptable if the team prefers; the rule "URL is the source of truth" matters more than the library.

Example shapes:

```ts
// features/files/api.ts
export function useDirectory(path: string, sort: Sort) {
  return useInfiniteQuery({ queryKey: ['dir', path, sort], queryFn: ({ pageParam }) => client.GET('/files', { params: { query: { path, sort, cursor: pageParam } } }), ... })
}
export function useMoveFiles() {
  const qc = useQueryClient()
  return useMutation({ mutationFn: moveFiles, onSuccess: (_, v) => qc.invalidateQueries({ queryKey: ['dir', v.dest] }) })
}
```

```ts
// features/files/routes/browse.tsx
export const browseRoute = createRoute({
  path: '/b/$',
  validateSearch: z.object({ type: fileType.default('all'), date: dateRange.default('any'), preview: z.string().optional(), focus: z.string().optional() }),
})
```

```tsx
// BrowsePage keeps only transient UI state
const [dialog, setDialog] = useState<BrowseDialog | null>(null)   // one discriminated union replaces 12 flags
const menu = useMenu()                                             // anchor + open, outside dismiss inside the hook
```

`BrowseDialog = { kind: 'rename', entry } | { kind: 'delete' } | { kind: 'move', sources } | { kind: 'conflict', name, retry } | { kind: 'share', entry } | { kind: 'unlock', target }`. Only one dialog can be open, so the type states it.

Cross-component events: `stowcloud:new` becomes a prop or a small `useCreateMenu` store. For `sc:lock`/`sc:unlock`, subscribe once in one module (`features/shares/e2ee-store.ts`) that mirrors the library's state into a Zustand store (`{ unlockedSalts, generation }`), and have components read that store. Upstream change in `@stowcloud/rclone-crypt` (a `subscribe(listener)` API instead of window events) is the proper fix, since the package is ours.

API layer: generate `api/generated` from the backend OpenAPI document (`openapi-typescript` + `openapi-fetch`, small and typed). This requires the backend change in 1.2 (all JSON endpoints on Huma). Streaming endpoints (upload chunks, download, events WebSocket) keep a small hand-written module. Delete `lib/api/http.ts`, `types.ts`, `client.ts`.

Mock backend: the first draft of this plan said "delete it; development already uses the real engine". That is wrong. `frontend/.env.development` sets `VITE_API_MOCK=1`, so `pnpm dev` runs against `mock.ts` by default, and 4 checks depend on it: `e2e/alignment.spec.mjs`, `e2e/virtual-scroll.spec.mjs`, `e2e/virtual/virtual-scroll.spec.ts`, `e2e/capture_readme_screenshots.mjs`. The CI steps `test:alignment` and `test:virtual-scroll` run on the mock.

Decision: replace `mock.ts` (3,235 lines, hand-kept in sync with `http.ts`) with MSW handlers generated or typed from the same OpenAPI schema, so the mock cannot drift from the contract. Order: (1) make `pnpm dev` default to the real engine through the Vite proxy (`scripts/dev.sh` already seeds shares), (2) move the alignment and virtual-scroll specs to the Playwright suite that `e2e.sh` runs against the real binary, seeding enough rows through the API, (3) delete `mock.ts`, `mock-seed.ts`, `isMock`, and the `setup.mock_mode_any_token_value` UI string. Keep MSW only if offline UI work is still wanted after that.

### 2.3 Frontend tests

- Keep: `lib/crypto/*` (E2EE, download SW, zip listing), `lib/upload/{chunk-planner,retry,queue}`, `virtual/windowing`, `format/*`, `search/filters`, `admin/log-view` pure functions.
- Delete: `api/mock.test.ts`, most of `api/http.test.ts` (generated client), `query/keys.test.ts`, `store/*.test.ts` (stores get simpler; test through components if at all), `ui/{Select,Switch,TextField}.test.tsx` (wrapper plumbing).
- Add only where a plausible bug lives: browse selection/marquee reducer, `BrowseDialog` transitions, route search-param validation.
- e2e (Playwright) stays as the flow-level safety net during the rewrite. Run it before and after each phase.

### 2.4 Behavior that must survive the rewrite

Listed because a rewrite that "only restructures" is where these silently break. Each needs an e2e or unit check before the phase that touches it:
- Session death from any query or mutation clears account state and locks E2EE (`query/client.ts` `noteSessionDeath`).
- Live invalidation only watches directories with an observer, coalesces per 100 ms, and backs off to 30 s (`query/live.ts`).
- Editor and emergency drafts block in-app navigation and `beforeunload`.
- Settings/admin tab changes replace history instead of pushing.
- Public share, setup, login and emergency routes do not import the authenticated shell graph (bundle split). Check with the Vite manifest, not by reading imports.
- Encrypted shares fail closed: an unavailable encryption lookup is never treated as plaintext.
- Keyboard: grid/tree roving focus, context menu from keyboard, focus return after every dialog.

---

## 3. Repository

- Root has two empty directories (`web/`, `admin/`), `scripts/__pycache__`, `test-results/`, `frontend/test-results/`, `frontend/playwright-report/`, 65 MB `backend/sc-engine`, `.dev/` binaries. All ignored, but they are what a new hire sees. Add `just clean`. `web/` is the leftover target of the previous frontend plan (B5).
- Four end-to-end systems: `scripts/e2e.sh` (real binary, 4 standalone `.mjs` specs plus Playwright), Playwright in `frontend/e2e` (5 browser projects configured, CI runs only `chromium`), Python podman scripts in `scripts/` (`podman_s3_e2e.py`, `test_podman_e2e.py`), and an LLM-driven "agentic" suite (`tools/jev-e2e`, `scripts/e2e-agentic.sh`) that runs on every push in `verify.yml` with an OpenRouter key. Keep Playwright against the real binary as the one e2e system. Fold the standalone `.mjs` specs into it. Move the agentic suite to the nightly workflow: a nondeterministic, paid, network-dependent job should not gate every push. Keep the podman scripts only for the S3/SMB container matrix and move them to `deploy/testbed`.
- `checks/consumers/{go,web}` verify that the released `github.com/stowcloud/*` modules and the browser package work from outside this repository. Useful, but it belongs with those modules' own CI, not this app's.
- CI runs the Windows job with the comment "the mandated development platform", but the engine is `//go:build linux` throughout and the filesystem code does not build there. A new hire on Windows cannot run the server. Either state "develop in WSL2 or a Linux container" in `CONTRIBUTING.md` and drop the Windows job to a frontend-only job, or keep it and say what it proves.
- `scripts/` mixes bash (verify 38 KB, dev, e2e), Python (two podman e2e scripts), Node (`capture_ui.mjs`, `gen-notices.mjs`) and Go (`tlsproxy.go`). No task runner. Add one `justfile` (or `Makefile`) with `dev`, `build`, `test`, `e2e`, `lint`, `clean`; CI calls the same targets. `verify.sh` shrinks to what the targets do not already run.
- `docs/internal/` has 18 dated audit/cutover files and 5 subdirectories. It is ignored and absent from Git history. Preserve local copies until they have an explicit archive. Keep `docs/ARCHITECTURE.md` (the entrypoint walk and folder map from 1.2 and 2.2) and `CONTRIBUTING.md` with the `just` commands.
- `frontend/package.json` carries `//engines`, `//packageManager`, `//overrides` comment keys with paragraph-length notes. Move the reasons to commit messages.

---

## 4. Execution order

Each phase ends green on the kept e2e suite. Phases 2 and 3 can run in parallel after phase 1. Ordering constraints that the first draft missed: the data-compat fixture exists before any backend move; the mock is removed before the generated client replaces `lib/api`; the contract-check tools are deleted only after the generated client lands.

| Phase | Scope | Exit criterion |
|---|---|---|
| 0 | Remove generated junk and empty `web/` and `admin/`; preserve ignored `docs/internal/` until it has an explicit archive. Add `justfile`, Prettier + ESLint on frontend, check gofmt, remove the push/PR agentic job (nightly already exists). Check in a `state.db` + key ring fixture written by the v0.19.1 release | `just test` and `just e2e` pass; product behavior is unchanged |
| 1 | Backend test triage: delete source-text tests, wording assertions, and the tests of tools being deleted (`koscan`, `freshscan`, `vetgo`, `vetsecret`, `speccheck`); create `test/e2e/harness.go`; move `internal/app/*_test.go` there | same e2e green; backend test lines reported before/after; per-package coverage not lower |
| 2 | Backend restructure: package renames (`core`->`files`, `upload`->`uploads`), `store`/`storage` renamed to `db`/`fs` (contents unchanged), feature packages own handlers, `server/routes.go`, drop fx composition and string handler map, hanami behind `server/listener.go`, required loggers, `internal/config` with one flag spelling, all JSON on Huma | `grep -r '"/api/v1/files"'` lands on the handler registration; OpenAPI covers every JSON route; the release fixture opens unchanged |
| 3 | Frontend foundation: `ui/` layer with every mdui use behind it, scoped styles, generic hooks, per-feature error boundaries, delete `useComponentState`/`usePatchState`/`useRouteStore`/`defineStore`; TS project references (app, worker, sw, node) | zero `<mdui-` outside `ui/`; zero `globalStyle` outside `ui/theme`; no `as unknown as` in `workers/` |
| 4a | Mock removal: `pnpm dev` against the real engine, alignment and virtual-scroll specs moved to the real-binary Playwright run | `VITE_API_MOCK` has no reader |
| 4b | Generated API client from OpenAPI + per-feature query hooks; one base URL module; delete `lib/api/http.ts`, `mock.ts`, `types.ts`, then `routecheck`, `contractcheck`, `settingscheck` | zero `api.` calls outside `features/*/api.ts`; `pnpm check` fails when the server drops a field the client reads |
| 5 | Router migration: URL-owned state (filters, preview, tabs, search), auth gate in `beforeLoad`, `BrowseState` split into `BrowseDialog` union + hooks; delete the React Router alias hack in `vite.config.ts` | Back closes preview; filtered URLs reload to the same view; every item in 2.4 has a passing check |
| 6 | Feature-by-feature move into `features/<x>/{api,components,hooks,routes}`; E2EE store over the library events; one virtualization engine; upload worker split; delete `lib/store/*` except the stores listed in rule 3 | `lib/` holds only pure functions |
| 7 | Docs: `ARCHITECTURE.md` (including what hanami does), `CONTRIBUTING.md` with the supported dev OS, prune `docs/internal`, comment pass on backend; i18n keys renamed to screen-scoped names | new hire can follow `main` to a handler with go-to-definition only |

Risks:
- Phase 2 touches every import. Do it as mechanical moves first (`gopls` rename), behavior changes second, never in the same commit.
- Deleting service-level tests before the e2e harness covers the same branch loses coverage. Move tests first, delete duplicates after comparing coverage (`go test -coverprofile`) per package.
- mdui web components and React 19 event typing: the `ui/` layer must own the `onChange` casts so they exist once.
- Generated client depends on the backend exposing every JSON route through Huma; until phase 2 finishes, phase 4b generates for the typed subset and keeps a thin hand-written module for the rest.
- The `@stowcloud/rclone-crypt` event API is owned by this project but lives in another repository; the E2EE store in phase 6 works with the current events, and the upstream `subscribe()` change is optional follow-up there.
