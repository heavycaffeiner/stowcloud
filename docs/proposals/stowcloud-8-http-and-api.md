# HTTP and API - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The twelve-step middleware chain, the error envelope, the REST surface, the
static frontend, and the WebSocket channel. The largest phase, and the one that
closes F8: 9,012 lines in one routes file becomes a package with a seam per
resource.

## 2. Background & Motivation

`sc-http` is 17,543 lines, of which `routes.rs` is 9,012. Together with
`sc-server/src/bridge.rs` it is 13% of the tree in two files with no navigable
seam.

The middleware order is documented and load-bearing, and the current
implementation has to add layers in reverse because of how axum composes. Go's
`func(http.Handler) http.Handler` composes in the readable direction, so the
reversal note disappears and the source order matches the request order.

Two gates in `verify.sh` exist because a failure here was invisible to the test
suite, and both have Go counterparts that are not the same shape:

- `axum::serve` without `ConnectInfo` compiles, runs, serves happily, and denies
  the process any knowledge of where requests come from. Go's `http.Server`
  always has `RemoteAddr`, so that specific failure cannot happen.
- Go has its own version: `http.Server`'s timeout fields default to no limit, so
  an unset `ReadHeaderTimeout` is a slowloris that no test notices.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] The twelve steps in the documented order, composed in one file, with a
      test that asserts the order.
- [ ] The existing error envelope, with one mapping function and one place a
      status is chosen for the native REST surface.
- [ ] The existence rule enforced structurally: unlistable is 404 everywhere.
- [ ] Every `http.Server` timeout set explicitly, with a test (D13).
- [ ] The five REST changes in §4.4 and no others.
- [ ] No file over 1,500 lines (D19).
- [ ] The frontend embedded with a real dependency edge.

### 3.2 Non-Goals

- [ ] An HTTP framework. `net/http` plus explicit wrapping. A router is needed
      for path parameters and one small one is chosen in §6-2, not a framework
      with its own context type.
- [ ] HTTP/3. HTTP/2 comes free with `crypto/tls` and ALPN.
- [ ] A plaintext listener. One socket, always TLS, unchanged. The 308 from a
      plain request to the TLS port is a redirect, not a second listener.
- [ ] Redesigning the REST surface beyond §4.4. The frontend is not being
      rewritten and churn without a recorded reason costs it for nothing.
- [ ] **Server-rendered messages.** A refusal travels as a code plus
      parameters, and the browser owns the wording and the reader's language.
      This is the stance D15 makes structural, and the incident behind it is a
      settings screen printing a lower layer's reason raw, in Korean, whatever
      locale the reader had chosen.
- [ ] **A MIME type on a listing entry.** The server never states one. The icon
      is the client's business, and a guessed type invites a client to render
      what it should download, which is the same failure the separate content
      origin exists to prevent one layer down.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/httpapi
  chain.go        the twelve steps, composed, in source order
  mw/             one file per step
  apierr/         Code, MessageKey, Error, the native REST mapper
  route/          the table: method, pattern, required scope, handler
  handler/
    browse.go  upload.go  search.go  share.go  link.go  folder_share.go
    operation.go  trash.go  settings.go  session.go  recent.go  preview.go  archive.go
    setup.go   the first-run bootstrap, and the gate that closes it
  static/         the embedded SPA
  ws/             the change channel
internal/server
  tls.go  config.go  wire.go  shutdown.go  health.go
```

`route/` is a table, not a tree of registrations scattered across handlers.
Every route declares its method, its pattern, the permission it needs and the
app-password scope it requires, and the scope layer reads that table. A route
with no declared scope is refused, which is the default-deny that makes step 9 a
layer rather than a habit.

### 4.2 Data Model Changes

None.

### 4.3 Core Logic

#### 4.3.1 The chain

```
1. RequestID   2. TrustedProxy  3. HostGuard    4. SecurityHeaders
5. RateLimit   6. BodyLimit     7. Auth         8. CSRF
9. ACLScope    10. Handler      11. ErrorMapper 12. AuditSink
```

```go
// Chain composes the steps in request order. Unlike the axum version this
// replaces, the source order here is the execution order, so the list above is
// the code rather than a comment about the code.
func Chain(s *State) func(http.Handler) http.Handler
```

**The order is a cost argument, not a taste.** Each step is placed so that the
cheap refusal happens before the expensive work it would otherwise pay for.
RateLimit is step 5 and Auth is step 7 because a flood must be rejected before
it costs an Argon2 invocation, and 48 MiB times the concurrency cap is what a
flood would otherwise reserve. BodyLimit is step 6 for the same reason one
level down: a body is refused by its declared length before a handler reads it.
Moving Auth above RateLimit compiles, passes every test, and hands an attacker
the container's memory budget.

Two other properties of this surface are inherited and easy to lose in a
rewrite:

- **Paging does not re-read the directory.** A directory can hold 100k entries
  and the filesystem offers no sorted iterator: `getdents64` order is unstable,
  and a full sort means reading every name, so re-sorting per page is the entry
  count times the page count. The cursor carries enough to resume, so page two
  does not pay for page one again.
- **Timestamps are nanoseconds that survive JavaScript.** A nanosecond value
  does not fit a double without loss, so it does not travel as a JSON number.
  Whatever encoding is chosen, the test is a round trip through the browser,
  not through Go.

**1. RequestID** mints a v4 UUID, puts it in the request context, and writes it
back as an `Sc-Trace` response header. The same value appears in the log line
that produced the response. It is not duplicated in the JSON body, because the
existing envelope has no trace field.

**2. TrustedProxy** is the one with fail-closed rules that break quietly when
reimplemented casually, so they are restated as testable statements:

- `X-Forwarded-For` is walked **right to left**, and the walk stops at the first
  hop that is not a configured trusted proxy. The leftmost entry is whatever the
  client sent and is attacker-controlled always.
- An **unparseable hop aborts the walk** and the peer address is used. Skipping
  the garbage would hand the choice of client address to whoever inserted it.
- A list consisting **entirely of trusted proxies** also yields no client
  address: there is no client in it, only infrastructure.
- A request with **no determinable source** is never treated as arriving from a
  trusted proxy, whatever the configuration says. Otherwise an operator who
  configured `0.0.0.0/0` (already a mistake) additionally lets an unattributable
  request pick its own address out of a header.
- The placeholder for an unattributable request is `0.0.0.0`, chosen because it
  is unroutable and therefore cannot collide with a real client's address. It is
  the shared rate-limit bucket for all unattributable requests and cannot
  collide with a real client's bucket.

Hop parsing accepts the four shapes proxies actually emit: `1.2.3.4`,
`1.2.3.4:51234`, `[2001:db8::1]`, `[2001:db8::1]:443`. A fuzz target covers it
(D16). A bare unbracketed IPv6 address also parses, because the reference
implementation's first attempt reads it as an address and a whitelist that
refused it would only reimplement the reference's second attempt less
reliably.

One more check the reference implements and this list does not name: the walk
is only entered when the **peer itself is in the trusted set**. Forwarding
headers from an untrusted source are attacker-supplied strings and are
discarded without being parsed, so a direct attacker cannot name their own
address. After the peer check, `CF-Connecting-IP` is read first when it
parses, because the edge sets that header rather than appends, and there is no
list to disambiguate.

**3. HostGuard** compares the request's host against a declared origin list.
The list is configuration, never inference: one server is reached under a LAN
address, a Tailscale name and a public name through a proxy, and a guard that
learned the origin from the request it is guarding is not a guard.

**6. BodyLimit** is `http.MaxBytesReader` at the D5 value for the route class,
applied before the handler reads a byte. XML routes get the smaller limit.

**9. ACLScope** enforces the app-password scope from the route table. The
virtual-path ACL check is not here: it happens inside `core.Resolve`, because
the check needs the resolved share and the two would otherwise disagree.

**11. ErrorMapper** is the only place a native REST status code is chosen, and
it takes a typed error. **12. AuditSink** sees the handler's result before the
mapper turns it into a response, which is why both sit innermost.

#### 4.3.2 The envelope

```go
// Envelope preserves the existing native REST wire shape. Message is a stable
// generic fallback, never lower-layer text. Localized data lives in Detail as
// reason_key and reason_params. Sc-Trace is a response header.
type Envelope struct {
    Error Error `json:"error"`
}

type Error struct {
    Code    Code           `json:"code"`
    Message string         `json:"message"`
    Detail  map[string]any `json:"detail,omitempty"`
}
```

`MessageKey` and `Arg` remain useful internal types. The responder translates
them to `detail.reason_key` and `detail.reason_params`, preserving the client
contract instead of introducing a second envelope. The browser never renders
`message` or `detail.reason`. An internal 500 has no `detail`; correlation uses
the `Sc-Trace` header.

One mapper from domain error to `(status, Error)`, a `switch` over `errors.Is`,
and the only place a status is chosen **for this surface**.

Three other surfaces choose their own, and they are named here so that
"one mapper" is not read as a claim it cannot keep:
[`9`](stowcloud-9-upload.md) §5-2 for TUS, [`10`](stowcloud-10-webdav.md) §5-2
for WebDAV, and [`13`](stowcloud-13-compat-nc.md) §5-2 for the compat mounts.
Each has a status vocabulary set outside this repository, so folding them into
this switch would mean this function knowing what OCS is, which principle 4
forbids. What they share is the layer below: every one of them maps from the
same domain errors, so a new error kind reaches all four mappers or none.

The existence rule is applied here and nowhere else: `ErrNotFound` and the
subset of `ErrDenied` that the caller may not know about both map to 404 with
the same body. The test is a table over every domain error asserting the status
and the key, and a second test asserting that a path outside a grant and a path
that does not exist produce byte-identical responses.

#### 4.3.3 The server

```go
srv := &http.Server{
    Handler:           chain(mux),
    TLSConfig:         tlsConfig,          // TLS 1.2 floor, ALPN h2 and http/1.1
    ReadHeaderTimeout: 10 * time.Second,   // D13: zero means no limit
    ReadTimeout:       0,                  // deliberate: uploads stream
    WriteTimeout:      0,                  // deliberate: downloads stream
    IdleTimeout:       120 * time.Second,
    MaxHeaderBytes:    64 << 10,
}
```

`ReadTimeout` and `WriteTimeout` at zero are the one place a Go default is kept,
and it is a decision rather than an omission: a whole-request deadline breaks
large uploads and downloads. The protection that replaces them is
`ReadHeaderTimeout` for the slowloris case and a per-read idle deadline inside
the streaming handlers, which is what actually distinguishes a slow client from
a stalled one. The D13 test asserts `ReadHeaderTimeout`, `IdleTimeout` and
`MaxHeaderBytes` are non-zero and that the two stream timeouts are zero **on
purpose**, so that a later edit setting them has to change the test.

Certificate handling is carried over: self-signed, generated into
`data/tls` on first start, with `127.0.0.1` always in the SAN list because the
healthcheck dials it and verifies properly rather than skipping verification.

#### 4.3.3a First-run setup

The one route reachable with no credential that creates one, so it gets stated
rather than assumed.

A one-time token is generated at first start, printed to stdout and written to
`setup-token` in the data directory with mode `0600`. `POST /api/setup` spends
it and creates the first administrator. Four properties:

- **Single use, and fifteen minutes.** Both, not either: a token that is only
  single-use sits valid in a log forever until someone finds it.
- **It stops existing the moment an administrator does.** Not "is refused":
  the gate closes permanently, so a token recovered from a log or a backup of
  the data directory after setup is worth nothing.
- **No environment variable for the first password.** Anything passed that way
  is visible in `docker inspect` and in the process list, which is the same
  reasoning the master key uses ([`6`](stowcloud-6-auth-and-acl.md) §4.3.10) and
  the same reason it is not merely discouraged.
- **`GET /api/setup` answers a bare boolean**, whether setup is still open, and
  nothing else. The login screen needs it to decide what to draw, and it is the
  one thing an unauthenticated caller may learn about this server's state.

`stowcloud setup` re-prints and re-persists a token from the command line, for
the case where the first one scrolled out of a log before anyone read it.

#### 4.3.4 Static frontend

`//go:embed` behind the `embed_ui` build tag. The compiler creates a real
dependency edge to `web/build`, so the `cargo clean -p sc-http` hazard has no
counterpart: after `npm run build`, the next `go build` picks up the new files
or fails.

Uploaded content is never rendered inline. It is served from a separate content
origin that carries no session cookie and is reached by a capability URL, so
that a stored HTML or SVG file executing in a browser has no session to steal
([`stowcloud-12`](stowcloud-12-preview.md) §2.0).

#### 4.3.5 WebSocket

**The channel is bidirectional and stateful.** An earlier draft called it
push-only with no client frames beyond pings, and used that to argue for a
hand-rolled frame layer. The premise was wrong, so the conclusion has to be
re-derived rather than kept.

What the protocol actually carries:

- **`sub` and `unsub` frames from the client, each with a list of paths.** A
  browser tab subscribes to what it is looking at and unsubscribes when it
  navigates away.
- **A READ recheck at subscribe time, and again at send.** A grant revoked
  between the two must not leak the next event, and checking only at subscribe
  is the bug that makes a revoked grant keep delivering.
- **A 200 ms debounce, coalescing per `(connection, path)`.** A recursive copy
  produces thousands of events for one directory and the tab needs one.
- **Refcounted watch registration.** Subscribing pins the directory into the
  watcher's sticky set and unsubscribing releases it
  ([`3`](stowcloud-3-vfs-and-paths.md) §4.4). This is the load-bearing part: the
  subscription is *why* the directory a user is looking at stays watched, so a
  port that drops the refcount leaves the sticky set with no input and
  invalidations stop arriving for exactly the folder in front of them.
- **Revocation at two grains**, per user and per session, so signing out one
  device does not disconnect the others.

**The dependency decision is therefore reopened, not settled.** A hand-rolled
frame layer was argued for on the strength of "no client frames"; with client
frames, control-frame handling, close codes and fragmentation all come back. The
Phase 5f task is to choose against the real protocol, and the honest default is
a maintained WebSocket module rather than an in-tree writer.

### 4.4 The five REST surface adaptations

The envelope, the route shapes and the session model are kept. These five change
because an existing proposal records a defect, and nothing else changes:

| Area | Change | The defect it fixes |
|---|---|---|
| Share API paths | one vocabulary; the subpath is named explicitly rather than inferred | a share label was prefixed onto a path that already carried the grant's subpath, and a link had no way to name a subpath at all |
| Recency query | ISO-8601 timestamps | a bare date literal made the query a 400 on both phone apps |
| Folder listing | one rollup field with a documented unit, plus `etag_weak` for the existing ETag field | no client read the recursive size correctly, and a metadata token was presented as a strong validator |
| Settings | typed values with declared ranges, and a refusal that names the field | nine defects on the settings screen, including a lower layer's reason printed raw in one language |
| Archive listing | listed from the file, with the cost bound stated in the response | the listing read the directory instead of the archive |

Each defect is stated rather than cited, because the documents that recorded
them specified the Rust backend and were retired with it.

**Two things about settings that no other section owns**, and both cut across
rules stated elsewhere:

- **Some D5 limits are admin-mutable at runtime.** Search concurrency, both
  walk deadlines, the search rate, archive concurrency and the watcher's
  hot-set cap are patchable and are applied to live components after they
  persist. D5's table calls them named constants and its prose says nothing
  takes a limit a caller could widen. Both are true of a *request* and neither
  is true of an administrator. The reconciliation: a D5 constant is the
  **compiled-in default and the outer bound**, an administrator may move a
  value within it, and no request path may move any of them. A patch outside
  the bound is refused naming the field, which is what the declared range is
  for.
- **A setting reports whether it took effect.** Some apply live and some need a
  restart, so the response carries the running value alongside the stored one
  rather than implying they agree.

**The proxy trust boundary is live-editable too**, and it is the one worth being
nervous about: the trusted-proxy ranges, the allowed origins, and the app,
content and public host lists are all patchable by an authenticated
administrator, which §4.3.1 step 2 and step 3 treat as boot configuration. Two
rules keep that from being a foot-gun, and they deliberately disagree with each
other. A malformed entry is **refused at save**, where an administrator is
watching and can fix it. The same entry is **dropped with a warning at boot**,
because refusing there would make a server unbootable over a typo committed
weeks ago. Validation therefore lives on both paths and does different things.

The compatibility mounts keep their route and payload vocabulary. Their shape
is set by the clients, and
[`stowcloud-13-compat-nc.md`](stowcloud-13-compat-nc.md) owns them. They still
inherit the core's standards correction for file validators: the same metadata
token is emitted as weak rather than falsely strong.

## 5. API Design

### 5-1. New / Modified

```go
package route

// Requirement is what a route demands of the credential that reaches it.
// Access is a three-way split that auth.Scope cannot express: an app
// password's scope is a filesystem-capability mask, and no combination of
// filesystem bits means "and also administer the account", so the
// self-service and admin surface is a kind of its own.
type Requirement struct {
    Access Access  // AccessSelfAdmin, AccessAny, or AccessPerms
    Perms  acl.Perms // the bits AccessPerms demands, in acl terms
}

// Route is one entry in the table. Req is required, not optional: a route
// that declares none is refused at startup, which is what makes step 9 a layer
// rather than something each handler remembers. The AccessUnset zero value is
// exactly that refusal.
type Route struct {
    Method  string
    Pattern string
    Req     Requirement
    Handler http.HandlerFunc
}

// Table is validated at startup: every route declares a requirement, no two
// routes share a method and pattern, and every pattern parses.
func Table(s *State) []Route
```

```go
package apierr

// Map turns a domain error into a status and a wire error. It is the only
// function for the native REST surface that names an HTTP status, and the only
// one on that surface that decides whether a refusal is visible as 403 or
// hidden as 404. TUS, WebDAV and compatibility each have their own mapper.
func Map(err error) (int, *Error)
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 400 | malformed request; the field is named and the value is not echoed |
| 401 | no session, expired session, failed credential |
| 403 | authenticated and refused, only where the caller may know the target exists |
| 404 | not found, or not listable by this caller |
| 405 | method not allowed on this mount |
| 409 | state or namespace conflict without a supplied validator |
| 410 | a formerly valid public or setup capability is permanently gone |
| 412 | supplied precondition failed, carrying the current ETag |
| 413 | over a D5 limit, and the limit is in the response |
| 415 | unsupported media type |
| 416 | unsatisfiable range |
| 422 | syntactically valid input that fails a named field or domain constraint |
| 423 | locked |
| 429 | rate limited |
| 500 | unhandled; no detail or internal text, correlation through `Sc-Trace` |
| 501 | a recognized operation that this build does not implement |
| 503 | a named subsystem is unavailable |
| 507 | quota or the configured free-space floor refused the write |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 5a | `chain.go` and `mw/`: the twelve steps, the order test, the proxy fuzz target | M | Phase 3 | heavycaffeiner |
| Phase 5b | `apierr/`: the envelope, the mapper, the existence-rule test | S | 5a | heavycaffeiner |
| Phase 5c | `route/`: the table, startup validation, scope wiring | S | 5a, 5b | heavycaffeiner |
| Phase 5d | `handler/`: browse, session, settings, trash, share-link, admin folder-share, operation, recent, archive, setup | L | 5c, Phase 4 | heavycaffeiner |
| Phase 5e | `internal/server`: TLS, config, wiring, shutdown, health, the D13 test | M | 5a | heavycaffeiner |
| Phase 5f | `static/` and `ws/` | S | 5c | heavycaffeiner |

5e is independent of 5d.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `github.com/BurntSushi/toml` | `sc.toml`, parsed in `internal/server/config.go`. This phase is where it enters the module graph, which is why it is listed here rather than left implicit in [`0`](stowcloud-0-motivation-and-findings.md) §6-2's table |
| `github.com/gorilla/websocket` | the `/api/events` change channel. The dependency decision §4.3.5 reopened settled on a maintained module: the protocol carries client frames, so a hand-rolled frame layer would re-implement control-frame handling, close codes and fragmentation it already owns |
| a routing library, **only if needed** | path parameters |

Go 1.22 gave `net/http.ServeMux` method and wildcard patterns, which covers this
surface, and Phase 5a's first task is confirming that against the real route
list rather than adding a router by reflex. The confirmation is recorded here:
`ServeMux` covers every route on the table, so no routing library is added. If
it had not covered one, a small router would have been added and named here and
in [`0`](stowcloud-0-motivation-and-findings.md) §6-2; a framework never would
be. The conditional entry is the honest shape: a dependency this document has
not committed to should not appear in a list that reads as committed.

Everything else is standard library: `crypto/tls`, `crypto/x509`, `net/http`,
`encoding/json`, `embed`, `log/slog`.

**Config parsing is a trust boundary** (D20), and TOML being a module rather
than the standard library does not change where the validation happens. The
parser produces a struct of raw values; one validating constructor turns that
into the typed configuration every other package accepts, and an out-of-range
value is a startup refusal naming the key.

## 7. References

- `crates/sc-http/src/error.rs`, `state.rs`, `routes.rs`, `core_api.rs`: the
  envelope, the state and the route surface this translates.
- `crates/sc-http/src/config.rs`: the declared origin list step 3 checks
  against, and the trusted-proxy CIDRs step 2 reads.
- `crates/sc-http/src/content.rs`, `content_api.rs`: the content origin and the
  capability URLs §4.3.4 refers to.
- `crates/sc-http/src/middleware.rs`: the proxy rules §4.3.1 restates, with the
  reasoning for each.
- `scripts/verify.sh:173`: the bind-site gate, and why a test could not catch
  what it catches.
- [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3: F8, F9.
