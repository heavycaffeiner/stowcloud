# HTTP 01: middleware and route requirements

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/httpapi/chain.go`, `go/internal/httpapi/mw`, and
> `go/internal/httpapi/route` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch; nothing
> is copied.

## The chain is data

Target `engine/http/middleware`. The chain remains an ordered data table,
not a pile of `app.Use` calls spread across assembly files. Source order is
request order and a replay test records entry and exit around every step.

```text
1  RequestID
2  TrustedProxy
3  HostAndOriginBoundary
4  SecurityHeaders
5  RateLimit
6  BodyLimit
7  Auth
8  CSRF
9  ACLScope
10 AuditSink
11 ErrorMapper
```

`AuditSink` wraps `ErrorMapper` so the audit event sees the final status.
`ErrorMapper` is innermost so an ordinary handler error cannot escape the
one mapper. Fiber's global `ErrorHandler` is only the last panic/error safety
net for framework errors; handler errors still pass through this table.

`HostGuard` and the origin half of CSRF become one
`HostAndOriginBoundary` step. This closes the audit's coupling concern:
the first-boot origin bypass cannot be mounted without the private-network
host gate it relies on.

## Route metadata

```go
type Access uint8
const (
    AccessUnset Access = iota
    AccessPublic
    AccessSession
    AccessAnyCredential
    AccessPerms
)

type Requirement struct {
    Access Access
    Perms  acl.Perms
}

type BodyClass uint8
const (
    BodyNone BodyClass = iota
    BodyJSON
    BodyDAVXML
    BodyStream
)

type Route struct {
    Method, Path, Name string
    Requirement Requirement
    Body BodyClass
    Handler fiber.Handler
}
```

Every route declares all fields and validation runs before any listener is
bound. `AccessUnset`, a missing name/method/path, an unknown body class, a
duplicate method plus path, a path outside its mount, or a public route that
mutates without an explicit public-flow designation is a startup error. Every
Path is a canonical literal beginning with `/`; Fiber named parameters use
`:id` and tails use `*`, so server registration translates the documents'
`{id}`/`{path...}` notation exactly once. Handlers read params by the
canonical parameter name.

Registration attaches the route's requirement and body class to the Fiber
route. The middleware reads metadata from the route Fiber matched. There is
no rebuilt equivalent of `route.Match`, because a parallel matcher is the
thing that can disagree with dispatch. Wildcard semantics are tested against
Fiber itself.

## Credential resolution

Credential order is exact:

1. HTTP Basic app password (the username half is ignored; the token names
   its account).
2. Bearer app password.
3. The `__Host-sc_sid` browser session cookie.

Basic precedes Bearer to preserve WebDAV and sync-client behavior where a
library may leave both headers represented. The public-read special case
attempts **only the session cookie**, so a signed-in browser sees personalized
state, ignores a stale cookie, and never turns a public page into an auth
failure. Public mutations do not resolve an ambient session unless their
flow explicitly requires one (login-flow consent is a session route, not a
public mutation).

The session cookie value is hex-decoded before lookup. The auth store hashes
the raw token bytes, not their printable hex form. An invalid/expired session
is cleared and answers `auth.required`; a Basic/Bearer value that was
presented but failed answers `auth.invalid_credentials`. Expired versus forged
is never distinguished.

WebDAV and compat file prefixes are **credential-required challenge mounts**,
not public mounts. Auth attempts the credential but lets an absent or bad one
reach the mount, which emits the protocol's Basic challenge. `OPTIONS` reaches
protocol discovery without a credential.

## Protocol path declaration

Protocol mounts supply data, never imports:

```go
type ProtocolPaths struct {
    FilePrefixes    []string
    PublicReads     []MethodPath
    CredentialFlows []MethodPath
}
```

Validation requires the three sets to be disjoint, requires every credential
flow to be POST, and refuses a state-changing `PublicReads` entry. A path
under a file prefix can never be classified as a static asset. Native public
routes come from the route table, not a second hardcoded switch.

## Trusted proxies

The project resolver is retained as a pure function over parsed prefixes,
peer address, `CF-Connecting-IP` and `X-Forwarded-For`. Fiber's proxy/IP
helpers are not used. The rules are individually load-bearing:

1. An untrusted peer's forwarding headers are discarded **without parsing**.
2. An unparseable XFF hop aborts the right-to-left walk; garbage is never
   skipped to continue trust past it.
3. An XFF list made only of trusted proxies yields no client address; it does
   not promote a proxy to client.

No valid peer maps to the canonical unroutable `0.0.0.0` placeholder and
shares that one bounded rate-limit bucket. Address forms accepted are bare
IPv4/IPv6, host-port IPv4 and bracketed IPv6, with a numeric port in range.
The live trusted-prefix holder is replaced atomically after save-time
validation; malformed stored prefixes are dropped with a warning at boot,
per `../settings/00-runtimecfg.md`.

## Host and origin boundary

Declared app and content hosts are live and read per request. A named
deployment admits only a case-insensitive Host match (port ignored), records
`OriginApp` or `OriginContent`, and dispatches only that origin's route set. A
host cannot appear in both lists. A refusal is 421 and asks the connection to
close. Content-origin requests never enter Auth or CSRF because their one
capability route authenticates the encrypted claim; app-origin middleware
cannot be mounted there.

An empty host list means first boot. The request is admitted only when the
resolved client address is private (`kit/netzone`). For a state-changing,
cookie-authenticated request, `Origin` is mandatory. With named hosts it must
name one of them; with no named host it is accepted **only in the same step
that has already admitted the private client**. `Referer` is never substituted.

The WebSocket route is a named GET exception that **requires an Origin match
before upgrade**. Ordinary safe-method requests do not need an Origin, but a
browser can attach ambient cookies to an upgrade. Auth alone is not a
cross-site WebSocket defense and the adapter's origin callback is never the
authority.

## CSRF

The session response carries
`hex(HMAC-SHA256(csrfKey, SHA256(printableSessionToken)))`. Mutating
cookie-authenticated requests send it as `Sc-Csrf`, compared with
`hmac.Equal`. App passwords skip CSRF because an Authorization header is not
ambient browser authority. No principal also skips it because Auth already
refused or the route is a token-authorized public flow.

The CSRF key is random per deployment and durable. Sessions are durable in the
rebuilt auth service, so retaining the old process-random key would strand
every still-valid session after restart. It lives in persisted auth key
material, sealed under the master key and loaded or minted durably
(`../auth/02-master-key-and-crypto.md`). Existing sessions continue to make
mutations after restart.

## Rate and body limits

The request limiter is a clock-injected token bucket keyed by resolved client.
Its map is capped at 65,536 entries; overflow evicts an arbitrary bucket by
design. Runtime rate and burst values update atomically.

Body bounds come from route metadata:

| Class | Rule |
| --- | --- |
| `BodyNone` | no request body is read; a non-empty body may be ignored where the protocol allows it |
| `BodyJSON` | 1 MiB outer bound, then one strict JSON decode with trailing-data refusal |
| `BodyDAVXML` | 256 KiB raw bound plus DAV structural limits |
| `BodyStream` | no shared buffering; the owning protocol applies its own declared size and rate rules |

The old second length check after fully reading JSON dies. A route may choose
a lower limit, never a higher limit than the compiled outer bound without a
documented protocol class. TUS PATCH is `BodyStream`.

## Security headers and request identity

A UUID v4 request id is minted directly from 16 bytes of `crypto/rand`; a
failure panics into the process-level safety path because secure sessions
cannot be minted either. It returns as `Sc-Trace` and keys the log line.

Headers are set before any handler runs and therefore appear on successes and
errors. The application CSP explicitly includes:

- hashes computed from the embedded hydration scripts;
- `font-src 'self' data:`;
- `worker-src 'self' blob:`;
- no `unsafe-inline`; no content-host source is admitted into app script,
  frame or worker sources.

Protocol responses may add stricter local headers, never loosen the app CSP.
The stored `AllowedOrigins` list does not enter this chain's authentication,
Host or CSRF decisions. A protocol-local response may consult it for CORS only
where its own document permits browser-readable cross-origin data.

## ACL scope

Route scope checks only the credential class and app-password permission mask.
It never resolves a file path. Path-specific ACL evaluation remains inside
`core.Resolve`; reproducing it here would create two authorities. Admin and
self-service routes accept sessions only. Bookkeeping routes accept either
credential. Permission routes require every declared bit.

## Audit, errors and streams

Audit records the final status, trace id, resolved client and principal if
known. It never logs credentials, cookies, CSRF tokens, public-link tokens,
login-flow tokens or request bodies.

Panic recovery converts an uncommitted ordinary request to the canonical 500.
For SSE/WebSocket/body streams, the owning handler catches stream-time errors
and logs them because no second status can be sent. Fiber response middleware
must not materialize a stream to inspect it.

## Deliberate changes

1. **HostGuard and the origin boundary merge into one step**, removing the
   first-boot invariant that previously existed only in two comments and chain
   order.
2. **Route scope uses Fiber's matched route metadata**, eliminating the
   parallel `route.Match` implementation.
3. **The protocol-path sets are validated for overlap and mutation safety**;
   old code accepted any supplied lists.
4. **The CSRF key becomes durable** because rebuilt sessions survive restart.
5. **One route-owned body class replaces the duplicate middleware/handler
   length checks.**

All credential ordering, challenge behavior, proxy rules, cookie attributes,
CSP directives and the 65,536-bucket bound otherwise carry whole.

## Tests

- Replay the chain table and assert exact enter/exit order; specifically,
  AuditSink sees ErrorMapper's status and no handler error escapes ErrorMapper.
- Startup validation refuses every omitted/duplicate/invalid metadata case and
  every overlapping protocol-path declaration.
- Historical auth regressions: raw hex is decoded before session lookup; DAV
  still emits a Basic challenge; a GET under every file prefix is never a
  public static asset.
- Credential table: Basic, Bearer, session, absent, malformed, expired and
  disabled accounts, including the exact precedence and public-read behavior.
- Proxy table covers the three fail-closed rules, every accepted address form,
  mapped IPv4, malformed ports and no-peer fallback.
- First boot: public client refused, private client admitted; its setup POST
  passes the combined origin rule. Reordering cannot separate these checks.
- A cross-origin WebSocket upgrade is refused before the adapter; same-origin
  succeeds. This browser endpoint also refuses a missing Origin.
- App/content host cross-routing is always 421; duplicate membership in both
  lists refuses startup and save.
- Host and CSRF holders update live on the next request.
- CSRF survives a process restart while the session remains valid; wrong,
  missing and cross-origin tokens refuse byte-identically as malformed input.
- Body classes: oversized JSON maps to 413 without full buffering; DAV gets its
  lower bound; TUS streams beyond 1 MiB without entering the shared reader.
- CSP fixtures cover hydration, data font and blob worker; uploaded content is
  absent from all source lists.
- WebSocket upgrade and SSE delivery work through the complete chain; neither
  response is buffered by audit or error handling.
