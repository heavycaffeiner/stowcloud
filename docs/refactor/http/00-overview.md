# HTTP 00: presentation overview

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/httpapi`, `go/internal/dav`, `go/internal/apierr`,
> `go/internal/archive`, `go/internal/server`, `go/internal/compat`, and
> the assembly under `go/cmd/stowcloud` is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## Scope

Phase 3 builds the complete presentation layer and performs the cutover.
It is the only phase allowed to import Fiber or `net/http`. The native API
moves to Fiber v2, WebDAV and the Nextcloud compatibility surface are
mounted beside it, the application bundle remains embedded, and the
composition root switches `cmd/stowcloud` from `internal` to `engine`.

Target tree:

```text
engine/http/
  apierr/       one classification table and the native JSON envelope
  archive/      the streaming Zip64 writer
  middleware/   request boundary, authentication and route scope
  handler/      native v1 handlers, public links, SSE and WebSocket
  dav/          WebDAV parsing, locking and method semantics
  compat/       Nextcloud OCS, DAV vocabulary and login flow v2
  spa/          optional embedded application bundle and CSP hashes
  server/       route table, mounting, TLS, listeners and assembly
```

The emergency handler remains the independent `net/http` surface specified
by `../settings/02-emergency.md`. It is deliberately not adapted through
Fiber and never shares the main application's listener or middleware.

## Feature inventory

Every old exported surface is owned below. Rows group symbols that form one
concept. An omitted export, route family or protocol feature is a documented
defect rather than a silent loss.

| Old surface | Owning document |
| --- | --- |
| `httpapi.Step`, `Chain`, `State`, `Runtime` | 00, 01 |
| `route.Access`, `Requirement`, `Route`, `Lookup`, `From`, `Match`, `Validate` | 00, 01, 09 |
| `mw.Auth`, `SessionCookie`, `ProtocolPaths`, public-path predicates | 01 |
| `mw.TrustedSet`, `ResolveClient`, `ClientFrom`, `ResolvedClient` | 01 |
| `mw.HostSet`, `HostGuard`, `CSRF`, `DeriveCSRFToken` | 01 |
| `mw.RateLimiter`, request id, audit sink, body limit, security headers, ACL scope, context accessors | 01 |
| `apierr.Code`, `Error`, `Wire`, request errors, `Map`, `Write` | 02 |
| Every native handler under `httpapi/handler`, including public links, setup, health, uploads, SSE and events | 03, with routes in 09 |
| Handler projections/constants (`HealthState` and methods, `HealthReason`, `SMBOutcome`, setup outcomes/errors, `ActiveWork`, `Finding`) | 03, 07 |
| `ws.Frame`, `Hub`, `NewHub` | 03 |
| `archive.Writer`, `NewWriter`, `AddBytes`, `AddDir`, `AddFile`, `Err`, `Close` | 03 |
| Every DAV parser and wire type (`Limits`, `Name`, `PropFind`, `PatchOp`, `IfHeader`, `Multistatus`, and related values) | 04 |
| DAV locks (`ActiveLock`, `LockStore`, `Locks`, `LockRequest`, timeout/depth/token helpers) | 04 |
| DAV method handler (`Handler`, `Options`, `New`, `Allow`, all `Serve*` methods, `StatusOf`) and mount resolution | 04 |
| DAV value methods (`Name.IsDav`/`String`, `IfHeader.Evaluate`/`Tokens`, `Multistatus.Write`/`Count`/`Close`) | 04 |
| DAV extension ports (`PropSource`, `QuerySource`, `UploadCollection`, upload headers/path parser) | 04, 05 |
| Nextcloud path parser, chunking, trash, shares, properties, previews, direct URLs, search, recent, favorites and user documents | 05 |
| Nextcloud OCS value tree, envelope, errors, format/version negotiation and exact XML/JSON writers | 05 |
| `ncport` seam and `ncwire.Build` adapters | 05 |
| Login flow tokens, record/store/auth ports, `LoginFlow`, begin/approve/poll/sweep | 06 |
| SPA `Handler`, `InlineScriptHashes`, build-tag behavior | 00, 01, 07 |
| Server `Config`, `FromValues`, OIDC secret helpers, share probes and health state | 03, 07 |
| `SetupGate` and setup outcome/error vocabulary | 03, 07 |
| Probe snapshot (`Probe`, `WriteProbe`, `ReadProbe`) | 07 |
| Listener supervisor (`Serve`, `NewServe`, swap and stop) | 07 |
| `server.Options`, `New`, `ServeEmergency`, `StartWatch`, `WatchShares`, command assembly and cutover | 07 |
| `SecretOIDCClient`, `OpenOIDCSecret`, `StoreOIDCSecret` | replaced by settings/auth seams in 03, 07 |
| Every documented historical regression | 08 |
| Every native and public-link route | 09 |

`cmd/sc-smb-agent` is already owned by the SMB phase. Phase 3 only changes
its imports from old SMB packages to the engine equivalents during cutover;
the sequencing contract remains `../smb/03-agent-runtime.md`.

## Application shape

Fiber v2 is used directly. There is one `*fiber.App` per listener generation,
not one process-global app. A listener swap builds and binds the replacement
before draining the old generation (`07-server-assembly.md`). Route metadata
is registered through this phase's own small table, and each table entry
installs the Fiber handler and its access requirement together. There is no
second pattern matcher: Fiber's matched route carries the requirement in
locals for the scope step.

Handlers follow one rule: **return an error before committing a response, or
own the committed stream until it ends**. Ordinary handlers return domain or
request errors to the one mapper. The named streaming exceptions are file
reads, archives, public downloads, TUS request bodies, SSE and WebSocket.
Each resolves every failure that can still choose a status before the first
body byte.

## Fiber-specific boundary rules

The rewrite preserves guarantees, not `net/http` mechanisms:

- Fiber's `BodyLimit` default is not trusted. The route table assigns an
  explicit request-body class. TUS PATCH is streaming and exempt; DAV XML
  has its own 256 KiB raw limit and structural limits.
- Fiber wildcard and optional-tail semantics are not assumed equal to
  `ServeMux`. Every native wildcard, public-link token, DAV tail and compat
  tail has edge-case route tests, including empty tails and trailing slashes.
- Fiber's proxy helpers are not used to establish the client address. The
  three fail-closed forwarding rules in 01 are implemented by the project's
  own pure resolver.
- Fiber's CORS middleware is not installed globally. The app origin is
  same-origin only. The compatibility OCS wildcard response is a protocol-
  local exception documented in 05.
- fasthttp does not expose `http.Flusher` or `http.Hijacker`. SSE uses
  `SetBodyStreamWriter`; WebSocket uses the Fiber-compatible websocket
  adapter; neither is wrapped in a `ResponseWriter` recorder.
- Request data borrowed from `fiber.Ctx` is never retained after the handler
  returns. Any string or byte slice crossing into a task, cache or long-lived
  connection is copied first.

## Dependencies

Phase 3 adds `github.com/gofiber/fiber/v2` and its fasthttp transitives to
`deps.allow`. It also replaces `github.com/gorilla/websocket` with
`github.com/gofiber/contrib/websocket`; a protocol-complete websocket stack
is not reimplemented locally. Both dependencies are confined to
`engine/http/`, and the layer gate rejects them everywhere else.

## Import and build order

The presentation tier's explicit sideways order is:

1. `apierr`, `archive`, `spa` (independent leaves)
2. `middleware` (may import `apierr`)
3. `dav` (may import `apierr`)
4. `compat` (may import `dav` and `apierr`)
5. `handler` (may import `apierr`, `archive`, `middleware`)
6. `server` (may import every package above)

No package under `engine/http` imports `engine/store` or `engine/infra`.
Durable facts needed by DAV or compat cross through narrow service ports.
The required service amendments are named in 03 through 06 and must be
added to the owning service documents before implementation.

## Content-origin separation

Uploaded content is never served from the SPA/static mount. Ordinary
authenticated downloads and public-link downloads may stream from the app
host, but only as `application/octet-stream` with `nosniff` and a hardened
Content-Disposition; they are never interpreted as an application document.

The compatibility preview/direct-URL features need a credential-free URL a
second process can open. They use `/c/{claim}` **only on a configured content
host**. The content host mounts no SPA, native API, DAV, compat, public-link or
emergency route and receives no `__Host-sc_sid` cookie because it is a
different host. A short-lived encrypted claim identifies one user/path and
operation; fetch rechecks current ACL before serving. A request for `/c/*` on
an app host or for any app route on a content host is 421. No convenience
route may collapse those host roles.

## Deliberate changes

1. **The native API becomes `/api/v1` and every old unversioned route dies**
   (`09-api-consistency.md`). The shipped frontend changes in the same cutover.
2. **The complete Nextcloud surface ships**: OCS plus the previously unwired
   OCS share CRUD, DAV aliases, chunked upload v2, trash and vendor properties
   (`05-compat-scope.md`). Feature completeness, not accidental old wiring,
   defines the scope.
3. **Fiber replaces the main `net/http` stack**, while the emergency door
   deliberately stays independent on `net/http`.
4. **The route requirement comes from Fiber's matched registration**, not a
   parallel matcher.
5. **`gorilla/websocket` is replaced by the Fiber adapter**; the wire frames
   and authorization behavior stay unchanged.
6. **No presentation package imports persistence directly.** DAV and compat
   storage access moves behind service-owned ports.
7. **The old aspirational content-origin comments become a real content-host
   capability mount** for compatibility preview/direct URLs.

## Tests

- The feature inventory is mechanically checked against old exported symbols
  and route fixtures until the old tree is deleted.
- `tools/layercheck` permits Fiber and `net/http` only under `engine/http`,
  rejects every presentation-to-store/infra import, and enforces the sideways
  list above.
- A build with `embed_ui` serves the application and computes its inline
  script hashes; a build without it leaves the application mount absent while
  APIs and protocols remain usable.
- A build with `compat_nc` mounts the complete feature matrix in 05; a build
  without it compiles none of the vendor package and advertises none of its
  methods or properties.
- Every streaming surface works through panic recovery and audit recording
  without buffering the full response.
- Uploaded HTML and SVG are never interpreted by the SPA/static mount; the
  content host exposes only an attachment/preview capability and no app route.
- `verify.sh`, layercheck, koscan, vetgo, vetsecret, routecheck and
  contractcheck pass after the cutover; the engine nolint budget remains zero.
