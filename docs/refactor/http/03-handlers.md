# HTTP 03: native handlers and live transports

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/httpapi/handler`, `go/internal/httpapi/ws`, and
> `go/internal/archive` is referenced as a behavioral specification only.
> The new implementation is written completely from scratch; nothing is
> copied.

## Scope

Target packages `engine/http/handler` and `engine/http/archive`. This
document owns every native v1 handler, the unchanged `/s/{token}` public
surface, TUS wire behavior, search SSE, the browser invalidation WebSocket,
and the streaming Zip64 writer. The route names, methods and access classes
are normative in `09-api-consistency.md`.

Handlers translate trusted Fiber request values into service calls and
translate service results into wire values. They do not decide domain policy,
open a database, reload an evaluator, coordinate a listener, extract secret
settings, or assemble a cross-service view inline.

## Dependencies, not a grab bag

There is no replacement for the old 190-line `Deps`. Route constructors take
one cohesive interface each:

```go
type AuthAPI interface { /* login and account self-service projections */ }
type FileAPI interface { /* core operations, already ACL-checked */ }
type LinkAPI interface { /* owner and public-link operations */ }
type AdminAPI interface { /* users, groups, grants, shares and audit */ }
type SettingsAPI interface { /* snapshot and ApplySection */ }
type UploadAPI interface { /* TUS session operations */ }
type SearchAPI interface { /* query plus index administration */ }
type PreviewAPI interface { /* thumbnail */ }
type OperationsAPI interface { /* list, get, cancel */ }
```

Interfaces are declared beside their sole handler family. The server wires
concrete service methods. A family does not gain access to unrelated services
through an assembly struct.

Three old handler-owned projections move to service seams before code starts:

1. **Current user view.** Auth exposes one method returning account, session,
   TOTP/recovery state, OIDC-link state and SMB credential facts. Core adds
   roots and limits through a small server-level presenter, but failure of an
   optional SMB status remains a field-level unavailable value, not a failed
   session response. The assembly owns this projection, not the handler.
2. **Settings application.** `service/settings` owns secret extraction and
   returns `ApplyOutcome{Stored, Applied, RestartRequired, Findings}`.
   Listener swap and process restart are callbacks supplied to that service's
   runtime applier. The handler neither switches on section names nor knows
   that `oidc.client_secret` is secret.
3. **SMB propagation.** One service method owns "a share/grant/account change
   affects SMB": the SMB publisher's `AccessChanged` sink, wired into core and
   auth (`../smb/01-publish-and-agent-protocol.md`). A committed authorization
   write never fails retrospectively
   because the agent is down. It publishes synchronously, with a context
   detached from browser cancellation, reports the sidecar outcome in the
   write response, and updates health. This preserves the stale-access-window
   reasoning from old server assembly without leaving it in a closure.

The grant handlers call `core.CreateGrant`, `ListGrants`, `UpdateGrant` and
`DeleteGrant`; they never import ACL persistence or SQL. Share creation's
"grant creator full access" policy is already a core operation and is not
reconstructed by the wire layer.

## Common request and response discipline

- JSON bodies are decoded once from the bounded body supplied by middleware.
  Unknown fields are refused; trailing JSON is refused; there is no decode
  into a second `map[string]json.RawMessage`.
- PATCH request structs use `present.Value[T]` (a tiny presentation-local
  generic carrying `Set`, `Null`, `Value`) wherever omitted and explicit null
  differ. This one type replaces both old double-decode spellings.
- Query integers, ids, cursors, byte ranges and timestamps are parsed with
  overflow checks before narrowing. A route parameter is untrusted even when
  Fiber matched its shape.
- Virtual file paths are strings on the wire and parsed exactly once by the
  core-facing service method. Handlers do not import VFS path types.
- JSON fields are snake_case. Every nanosecond epoch and every integer that
  can exceed JavaScript's exact range is a decimal string. Null means absent;
  zero remains a real value.
- A success writer owns status and content type. Failure statuses come only
  from `apierr` (except protocol-local TUS).
- `Content-Disposition` uses one helper: a sanitized quoted fallback plus an
  RFC 5987 percent-encoded UTF-8 form. CR, LF, quote, backslash and controls
  cannot escape either form.
- `safeReturnTo` accepts a printable-ASCII local path beginning with one `/`
  and refuses `//`, control bytes and external URLs.

## Batch operations

Delete, move, copy, trash restore and trash purge use one generic batch
runner. It executes every requested item unless the operation's documented
preflight is all-or-nothing, and emits one stable result per input in input
order. A failed item carries `apierr.Wire`; one item's malformed path cannot
shift another item's result. Move/copy share one destination-resolution helper
and differ only in the service operation and required preflight.

The batch runner does not parallelize. Preserving input order and avoiding
several simultaneous destructive operations on the same directory is worth
more than request-local throughput.

## Handler families

### Authentication and account

- Login and second-factor login use the auth limiter, decoy work and exact
  challenge state specified in the auth phase. A successful browser login
  sets `__Host-sc_sid` with Secure, HttpOnly, Path `/`, SameSite=Lax, and
  returns the durable CSRF derivation from 01.
- Logout reports a session revocation database failure. It clears the cookie
  only after the server-side revoke succeeds or the auth service confirms the
  session is already absent. The old blanket swallow is fixed.
- Password, TOTP, recovery-code, app-password, session, SMB credential and
  OIDC-link endpoints reconfirm credentials where the auth specs require it.
- An administrator cannot disable or delete their own account. The service
  also enforces last-admin protection so non-HTTP callers get the same rule.
- OIDC navigation binds state to a short-lived Secure/HttpOnly/SameSite=Lax
  cookie and applies `safeReturnTo`. Callback errors join a local return path,
  never an arbitrary Location value.

### Files, links and trash

- List/stat/read/aggregate/thumbnail/mutation behavior is exactly the owning
  core and preview specs. Range support remains one range only; malformed or
  multi-range input is refused rather than silently reduced.
- File read, public link download and thumbnail resolve all status-selecting
  work before setting a body stream. A read failure after commitment is logged
  and terminates the stream.
- Public link password authorization is checked through one `linkFor` gate on
  every route. The unlock cookie is Secure, HttpOnly, SameSite=Lax, scoped to
  the one token path and bounded by the link/authorization lifetime.
- `/s/{token}` content negotiation remains: explicit JSON asks for data;
  explicit HTML asks for the page; when neither is named, prefer the page.
  A build without an embedded UI falls back to JSON for a valid link.
- Link archive and ordinary archive validate the complete selection and
  response filename before first output. A source file that vanishes mid-Zip
  closes its entry at bytes actually read; the archive remains structurally
  valid and the disappearance is logged.
- A drop link never exposes a listing or read capability. Its write path goes
  through core link liveness/password/cap checks on every request.

### Content capabilities

`GET /c/{claim}` is mounted only for `OriginContent`. The claim is an
authenticated-encrypted presentation value containing version, purpose
(`thumb` or `download`), user id, virtual path, optional dimensions, issued
time and expiry. It carries no reusable session/app-password and lives at most
5 minutes. Opening uses purpose-bound AAD and rejects unknown versions,
expired values, malformed values and wrong-purpose payloads as one 404.

Fetch re-resolves the path under the named user with current Read+Download
permission. Revocation therefore invalidates an unexpired URL. Download emits
octet-stream, nosniff, attachment disposition, exact length/range/ETag;
thumbnail invokes preview and emits only the server-generated PNG. Responses
are `private, no-store`. The content host has no other route, no CORS, no HTML
fallback and no cookie authentication.

### Administration and settings

- User, group, grant and share operations call service surfaces only. Every
  authorization change reloads the evaluator in the same service operation
  before returning success.
- Audit listing never returns secret values or raw credential-bearing
  metadata.
- Settings are one sectioned resource. Save-time findings come from
  `service/settings/check`; blocking findings refuse, warnings save and return
  with the outcome. The handler does not reproduce probes.
- Stored versus running values are distinct fields. A failed live apply says
  `stored: true, applied: false`; it never reports a clean application.
- A restart-required save reports active upload/job counts. Forced restart is
  an explicit request, never inferred from repeating the same PATCH.
- SMB apply returns the agent report in the stable `SMBOutcome` projection.

### Health and setup

Health is unauthenticated and returns only `status` plus a stably sorted,
deduplicated list of fixed reason tokens. No path, address, username or error
text enters it. Setup behavior and durable token handling are in 07. The HTTP
surface returns a bare required-state projection and applies the common setup
error classes.

## TUS

The resumable-upload mount keeps its protocol contract and is a named
exception to shared JSON/error middleware:

- `OPTIONS` is public; create requires Create+Write; session methods require
  their v1 route permissions.
- `Tus-Resumable`, `Upload-Offset`, `Upload-Length`, `Upload-Defer-Length`,
  `Upload-Metadata`, checksum and expiration headers retain their old spelling
  and status behavior.
- PATCH streams directly to `upload.PatchAt`; the row lock never covers the
  request-body read (`../upload/01-session-lifecycle.md`).
- The shared 1 MiB body limit does not run. Upload/service limits remain the
  authority.
- Checksum mismatch remains status 460. Cache-full/retryable responses carry
  the documented `Retry-After`.
- Every response, including protocol errors, carries `Tus-Resumable` where the
  protocol requires it.

## Search SSE

`GET /api/v1/search/stream` uses Fiber/fasthttp's stream writer. Headers are
`text/event-stream`, `Cache-Control: no-store`, and
`X-Accel-Buffering: no`. It writes an initial comment immediately so the
client and proxy see an established stream.

Events remain:

```text
event: hit   data: one permission-filtered result
event: done  data: {truncated, tier} or {error}
```

The query is validated and the search service acquired before commitment.
Each hit carries share label and share-relative path separately, nullable
metadata, and nanoseconds as decimal strings. A post-commit search failure
logs and emits `done.error`; it never attempts a 500. Stream cancellation
propagates to the service.

## WebSocket invalidations

The wire shape remains content-free invalidation:

```json
{"t":"sub","paths":["docs"]}
{"t":"unsub","paths":["docs"]}
{"t":"ping"}
{"t":"pong"}
{"t":"inval","path":"docs"}
```

The endpoint is authenticated through the complete middleware chain. The
combined host/origin boundary requires the upgrade Origin to match an app
host even though GET is ordinarily a safe method. The WebSocket adapter's own
callback is not trusted as the security boundary. The route cannot be
registered as public.

Every subscribe resolves with Read permission and pins the watcher. Every
delivery re-resolves with Read after the 200 ms per-path debounce. Revocation
between subscribe and delivery drops the event and subscription. Frames never
carry file content, read tokens, directory etags or metadata; the client
re-fetches. Disconnect cleanup removes the hub entry and releases **every
watcher subscription**, fixing the old leak where `close` removed the socket
but did not call `Unsubscribe` for its remaining paths.

Frame bytes and path count are bounded before decode. Only one writer owns a
connection. Ping/pong/read deadlines close dead peers. Every Fiber-borrowed
value copied into the connection survives independently of the request.

## Archive writer

`engine/http/archive` stays dependency-free. It writes streaming Zip64 with
the UTF-8 flag, stored entries, data descriptors and a central directory. A
sticky first error makes later calls no-ops returning that error. DOS times
before 1980 clamp to the epoch. Names are normalized and cannot emit absolute
paths, `..` components, NUL or backslash traversal.

## Accessibility

Server-rendered setup, login-flow consent and public-link fallback pages have
one `<main>`, explicit labels, keyboard-reachable controls, visible focus,
programmatic error summaries and no color-only status. A failed form moves
focus to the summary; successful consent does not leave an active submit
control. The embedded SPA keeps its own accessibility tests, and API rewiring
must not remove accessible names or focus behavior from existing components.

## Deliberate changes

1. **Raw SQL and ACL persistence leave handlers** (presentation audit handler
   findings 1 and 2).
2. **The monolithic `Deps` breaks into cohesive ports** (finding 4).
3. **Settings lifecycle and secret extraction move to the settings service**
   (findings 5 and 6).
4. **The current-user projection moves out of the session handler** (finding
   7).
5. **One present-value type and one batch runner replace duplicated decoding
   and loops** (findings 8, 9 and 20).
6. **The duplicate post-read body bound dies** (finding 10).
7. **Logout no longer swallows a database revoke failure** (finding 17).
8. **Route-owned body classes replace one universal JSON-shaped bound**
   (finding 19).
9. **WebSocket close releases watcher subscriptions**, an additional defect
   found while specifying the lifecycle.

Every native wire shape not deliberately changed by 09, TUS protocol status,
public-link short URL and archive format otherwise carry whole.

## Tests

- One request/response contract test per route in 09, generated from the route
  fixture, including malformed input and denied scope.
- No handler package imports store, infra/VFS, raw ACL persistence, server or
  Fiber internals outside its boundary helpers.
- Strict JSON: unknown fields, trailing document, omitted versus null PATCH
  fields, numeric overflows and body limit.
- Batch ordering and one-item failure behavior across every batch family.
- Header-injection fixtures for every Content-Disposition caller.
- Streaming contract: all pre-stream errors choose a normal status; injected
  post-commit read/write errors only log/terminate and never append an error
  envelope.
- Logout fixture: database failure retains the cookie and reports failure;
  already-absent session clears it successfully.
- Public-link matrix: every route rechecks password/liveness; content
  negotiation defaults to HTML; no-UI build returns JSON.
- Content claims: tamper, expiry, wrong purpose/version/host, ACL revocation,
  path disclosure scan, download headers and generated-PNG-only preview.
- TUS protocol suite, including >1 MiB PATCH, checksum 460, Retry-After and
  a stalled concurrent body-read lock-scope regression through direct
  HTTP/1.1 and an HTTP/2 reverse proxy.
- SSE client sees the initial bytes before the query completes, every hit in
  order and exactly one terminal `done` event.
- WebSocket: subscribe and delivery ACL checks, revoke between them, debounce,
  content-free frames, frame bounds, disconnect cleanup and no task leak.
- Archive golden files open in standard tools; Zip64, pre-1980 clamp, sticky
  errors, mid-entry disappearance and traversal-name refusal.
- Accessibility tests for each server-rendered page with keyboard-only flow
  and automated name/label checks.
