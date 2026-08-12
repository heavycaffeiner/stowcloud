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
- [ ] One error envelope, one mapping function, one place a status is chosen.
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
  apierr/         Code, MessageKey, Error, the single mapper
  route/          the table: method, pattern, required scope, handler
  handler/
    browse.go  upload.go  search.go  share.go  link.go  trash.go
    settings.go  session.go  recent.go  preview.go  archive.go
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
  its own rate-limit bucket, shared with nothing.

Hop parsing accepts the four shapes proxies actually emit: `1.2.3.4`,
`1.2.3.4:51234`, `[2001:db8::1]`, `[2001:db8::1]:443`. A fuzz target covers it
(D16).

**3. HostGuard** compares the request's host against a declared origin list.
The list is configuration, never inference: one server is reached under a LAN
address, a Tailscale name and a public name through a proxy, and a guard that
learned the origin from the request it is guarding is not a guard.

**6. BodyLimit** is `http.MaxBytesReader` at the D5 value for the route class,
applied before the handler reads a byte. XML routes get the smaller limit.

**9. ACLScope** enforces the app-password scope from the route table. The
virtual-path ACL check is not here: it happens inside `core.Resolve`, because
the check needs the resolved share and the two would otherwise disagree.

**11. ErrorMapper** is the only place a status code is chosen, and it takes a
typed error. **12. AuditSink** sees the handler's result before the mapper
turns it into a response, which is why both sit innermost.

#### 4.3.2 The envelope

```go
// Error is what reaches a client. Msg is a catalogue key with placeholders,
// never a sentence: the server does not decide what language a reader wants,
// and lower-layer error text never reaches the wire (D15).
type Error struct {
    Code    Code      `json:"code"`
    Msg     MessageKey `json:"msg"`
    Args    []Arg     `json:"args,omitempty"`
    TraceID string    `json:"trace"`
}
```

One mapper from domain error to `(status, Error)`. It is a `switch` over
`errors.Is` and it is the only function in the tree that names an HTTP status.

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

`golang.org/x/net/websocket` is not used; the channel is small enough that a
minimal handshake plus frame writer in-tree is smaller than the dependency, and
the traffic is server-to-client change notifications with no client frames
beyond pings. This is decided at Phase 5 with the alternative (`nhooyr`-lineage
`coder/websocket`) as the fallback if the in-tree version grows past a few
hundred lines.

### 4.4 The five REST changes

The envelope, the route shapes and the session model are kept. These five change
because an existing proposal records a defect, and nothing else changes:

| Area | Change | Recorded in |
|---|---|---|
| Share API paths | one vocabulary; the subpath is named explicitly rather than inferred | `stowcloud-15`, `stowcloud-19` |
| Recency query | ISO-8601 timestamps; the bare date literal that made both phone apps' query a 400 is gone | `stowcloud-21` |
| Folder size | one rollup field with a documented unit | `stowcloud-16` |
| Settings | typed values with declared ranges, and a refusal that names the field | `stowcloud-20` |
| Archive listing | listed from the file, with the cost bound stated in the response | `stowcloud-21` |

The compatibility mounts are not touched at all. Their shape is set by the
clients, and [`stowcloud-13-compat-nc.md`](stowcloud-13-compat-nc.md) owns them.

## 5. API Design

### 5-1. New / Modified

```go
package route

// Route is one entry in the table. Scope is required, not optional: a route
// that declares none is refused at startup, which is what makes step 9 a layer
// rather than something each handler remembers.
type Route struct {
    Method  string
    Pattern string
    Scope   auth.Scope
    Perms   acl.Perms
    Handler http.HandlerFunc
}

// Table is validated at startup: every route has a scope, no two routes have
// the same method and pattern, and every pattern parses.
func Table(s *State) []Route
```

```go
package apierr

// Map turns a domain error into a status and a wire error. It is the only
// function in the tree that names an HTTP status, and the only one that
// decides whether a refusal is visible as 403 or hidden as 404.
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
| 409 | conflict, carrying the current ETag |
| 412 | precondition failed |
| 413 | over a D5 limit, and the limit is in the response |
| 415 | unsupported media type |
| 416 | unsatisfiable range |
| 423 | locked |
| 429 | rate limited |
| 500 | unhandled; trace id only, never internal text |
| 503 | a named subsystem is unavailable |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 5a | `chain.go` and `mw/`: the twelve steps, the order test, the proxy fuzz target | M | Phase 3 | heavycaffeiner |
| Phase 5b | `apierr/`: the envelope, the mapper, the existence-rule test | S | 5a | heavycaffeiner |
| Phase 5c | `route/`: the table, startup validation, scope wiring | S | 5a, 5b | heavycaffeiner |
| Phase 5d | `handler/`: browse, session, settings, trash, link, share, recent | L | 5c, Phase 4 | heavycaffeiner |
| Phase 5e | `internal/server`: TLS, config, wiring, shutdown, health, the D13 test | M | 5a | heavycaffeiner |
| Phase 5f | `static/` and `ws/` | S | 5c | heavycaffeiner |

5e is independent of 5d.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| a routing library, or `net/http`'s own pattern matching | path parameters |

Go 1.22 gave `net/http.ServeMux` method and wildcard patterns, which covers this
surface, and Phase 5a's first task is confirming that against the real route
list rather than adding a router by reflex. If it does not cover it, one small
router is added and named here; a framework is not.

Everything else is standard library: `crypto/tls`, `crypto/x509`, `net/http`,
`encoding/json`, `embed`, `log/slog`.

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
