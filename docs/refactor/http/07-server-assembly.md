# HTTP 07: server assembly and cutover

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/server` and `go/cmd/stowcloud` is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## Ownership

Target `engine/http/server` plus the composition root in `cmd/stowcloud`.
`server` owns typed presentation configuration, route registration, the main
TLS listener, listener generations, setup, health/probe snapshots and
mounting. The command owns process order: sandbox, store, keys, services,
presentation, signals and degradation.

The composition root assembles values only. Revocation-to-SMB policy, settings
application, current-user projection and grant creation live in services as
specified by 03. There are no multi-paragraph policy closures in `wire.go`.

## Main application assembly

Construction order is explicit and test-visible:

1. Load typed runtime values and open/mint persistent presentation material
   (CSRF key, TLS pair, setup state).
2. Build live holders for trusted proxies, app hosts and request-rate bounds.
3. Build handler family ports from service methods.
4. Construct DAV, then optional compat sources/routes, then native handlers.
5. Validate the complete route/mount table and protocol-path declarations.
6. Construct the ordered middleware chain.
7. Construct the Fiber app, install routes, then a final SPA fallback that can
   never claim `/api`, `/dav`, `/s`, `/emergency` or compat prefixes.
8. Bind the listener only after every earlier step succeeds.

The same listener may answer several TLS hostnames, but route roots are split
by the origin resolved in 01. App hosts receive the product mounts. Content
hosts receive only `GET /c/{claim}`. Certificate SANs cover every declared app
and content host. A host in both sets refuses configuration.

The emergency door is not a fallback route inside Fiber. On a healthy server
it is served by an independent `net/http` handler selected before Fiber at the
listener boundary; in degraded mode it owns the listener. This keeps its
dependencies and middleware independent as required by
`../settings/02-emergency.md`.

## Fiber transport constraint

Fiber v2/fasthttp is HTTP/1.1-only. The old `net/http` listener advertised h2.
There is no honest switch that makes fasthttp speak HTTP/2, and adapting the
entire Fiber app through `net/http` would buffer or break the very streaming,
upgrade and body-lifetime semantics this phase is meant to define. Therefore
the direct application listener advertises **`http/1.1` only**. Browsers,
WebDAV clients and Nextcloud clients negotiate/fall back automatically; all
product features and concurrent requests remain available. Deployments that
require HTTP/2 terminate it at a reverse proxy and forward HTTP/1.1 over the
declared trusted-proxy boundary.

This is an explicit transport compatibility change, not an accidental Fiber
default. The upload lock-scope regression test runs over a streaming client
whose body blocks independently of protocol version; the invariant is about
not holding a row lock while waiting for body bytes.

## Listener generations and live swap

`Serve` owns one current generation:

```go
type Generation struct {
    Addr string
    App  *fiber.App
    Ln   net.Listener
    Done <-chan struct{}
}

type Serve struct { /* serialized current generation */ }
```

Startup and swap use the same sequence:

1. Build the complete replacement app and TLS config.
2. Bind the new TCP socket.
3. Start serving and confirm the serve task reached its accept loop.
4. Atomically publish the new generation as current.
5. Only then stop accepting on the old generation and drain it in a detached
   background context for 15 seconds; force-close after the deadline.

A build or bind failure changes nothing. The old listener remains reachable
and the settings save returns a named refusal. Two simultaneous swaps are
serialized. A no-op address/certificate generation does nothing. Shutdown
marks the supervisor stopped, refuses further swaps and drains the current
generation once.

Fiber's shutdown function operates on an app, so each generation needs its own
app. Reusing one app across sockets would make draining the old generation
stop the new one too.

## TLS material

On first use, generate ECDSA P-256 material with a random 128-bit serial,
10-year lifetime, one-hour NotBefore skew, server-auth EKU, `localhost`, both
loopback addresses, and every configured app/content host. TLS minimum is 1.2.
Health verifies against the persisted certificate; verification is never
disabled.

Certificate and key publish through `store/fsatomic.ReplaceFilesDurable`, in
**key then certificate** rename order, exactly as
`../foundation/fsatomic.md` specifies. Startup behavior:

- a matching, parseable pair that covers all declared hosts is reused;
- a first-boot pair that no longer covers the newly configured app host is
  regenerated before anything could have pinned that host;
- a mismatched pair or key-newer-than-certificate crash signature is treated
  as the one recoverable torn-publish state and both are regenerated;
- any other corrupt/stale pair is a startup refusal, not silent identity
  replacement.

Modes are 0600 and the directory is 0700. Private-key bytes never enter logs.

## Setup gate

The setup token is 32 CSPRNG bytes rendered as hex, valid 15 minutes and usable
once. Its plaintext exists only long enough to publish
`data/setup-token` durably via `fsatomic.ReplaceFileDurable` (0600) and print
to the selected terminal/log. Memory stores only SHA-256 and expiry after that.
Comparison is constant-time and expiry is checked first.

The durable gate is `auth.CountUsers == 0`; a database read failure closes
setup. One mutex covers verification, first-admin creation and token use, so
two requests cannot create two first administrators. Username validation calls
the canonical auth rule from `../auth/05-username-policy.md`; the old broader
setup-only spelling dies. Password uses auth's normal minimum and policy,
not a second setup constant.

First-admin creation invokes one core operation that grants all currently
registered shares with every permission and reloads ACL before success. No
command or setup object writes grant SQL. Failure after account creation is
reported as a degraded partial setup with a repairable audit event; repeating
setup cannot create another admin because the durable gate is now closed.

Completion/removal of the token file is best-effort and not an authorization
fact: an account existing closes setup, and a stale plaintext file cannot pass
an unissued in-memory gate. Reissue is CLI-only and refuses after any account
exists.

## Probe snapshot and healthcheck

`.probe.json` contains only the settled listen address and first app host.
Write uses `fsatomic.ReplaceFileDurable` (0600), so a concurrent probe sees old
or new and a crash cannot lose the promoted directory entry. A write failure
logs and does not stop a serving listener. Read failure/invalid JSON falls back
to `0.0.0.0:8443` and no host.

The healthcheck command dials loopback, verifies the server certificate against
`data/tls/cert.pem`, bounds the response at 64 KiB, calls
`/api/v1/system/health`, and exits 0 for both `ok` and `degraded`; only no valid
answer is unhealthy. Restart-looping a configuration degradation would make it
worse. It prints fixed reason tokens for a human invocation.

## Share probe and periodic maintenance

Every 30 seconds, one core probe pass rechecks all share roots. It logs and
updates health only on transitions in both directions. Host paths may appear
in operator logs but never in the public health document. This task shares no
request context.

The server's periodic task table also includes:

- DAV expired-lock sweep;
- login-flow expiry sweep;
- upload sweep;
- auth/session/audit maintenance already named by the auth phase;
- search/cache/watch maintenance from their owning phases.

The table is data with task name, interval and function; startup tests assert
every owning document's required task appears exactly once.

## Stored configuration and live values

`Config` is a typed startup snapshot from `runtimecfg.Values`. Construction
does not perform a third refusal: save time refused bad input and boot time
clamped/dropped malformed stored values with warnings. Live holders update
rate bounds, host list, proxy set and search bounds on the next request. A
value pinned by listener, sandbox or process layout reports restart required.

OIDC secret open/store goes through auth master-key methods and the settings
service. The server sees plaintext only at OIDC client construction, never
returns it, and distinguishes absent from unreadable.

`content_hosts` is a typed network setting. At least one is required before
compat preview/direct capability routes are advertised. The presentation
claim key is a durable 32-byte secret obtained from auth's presentation-key
surface, and claims are AEAD-sealed with purpose/version AAD. Rotation retains
the key version needed to open an unexpired five-minute claim.

## Sandbox and command order

`cmd/stowcloud serve` retains the one ordering constraint that makes the
sandbox meaningful:

1. parse command/data directory;
2. require the race-free VFS resolver;
3. pre-open only enough store state to derive hardening policy and share host
   parents, then close it;
4. apply/re-exec the sandbox before opening long-lived state or minting tokens;
5. lock the data directory;
6. open store, auth/master key, ACL/core and every service in dependency order;
7. build setup and presentation;
8. start background tasks after their dependencies and before accepting
   product traffic where required;
9. serve and drain on signal.

Landlock grants the data directory, enabled SMB paths/socket directory and
each share's parent so a later sibling share can become reachable without
restart. A share directly under `/` grants itself, never root. The exact
component-boundary `inJail` check reports whether a newly configured host path
is live now or after restart.

An engine construction failure after store/auth opens degrades into the
standalone emergency listener and stays up. A hardening refusal before that
cannot degrade by opening the resources it just refused to confine. The
best-effort `.engine-failures` one-minute counter remains a deliberately
non-durable diagnostic exception: corruption only causes one extra retry and
never loses user/security data.

## CLI and cutover

Argv[1] dispatch remains before flag parsing, so Docker exec-form healthchecks
work without a shell. Commands retain `serve`, `healthcheck`, `preview-worker`,
`caps`, `setup`, `settings get/set`, `gc`, `routes`, `smb-sync`, `index` and
`masterkey rotate`. Master-key rotation remains CLI-only.

`routes --json` dumps the validated Fiber/compat/DAV table, not old server
routes. `settings set` bounds stdin and uses the typed settings schema; as an
emergency CLI it may bypass save-time probes deliberately, because boot-time
clamping is the recovery mechanism, but it still validates JSON shape and
known section names.

The cutover change:

- adds Fiber/contrib websocket dependencies and updates `deps.allow`;
- rewires `cmd/stowcloud` and `cmd/sc-smb-agent` to engine packages;
- rewires the frontend and route/contract tools to v1;
- removes `gorilla/websocket` if no old package remains;
- deletes the replaced `go/internal` tree in the same change set;
- preserves all database/file formats and build tags;
- leaves no command importing both trees after integration tests finish.

### Frontend cutover and accessibility

The shipped Svelte client changes in the same commit. All API modules,
event/TUS transports, mocks and route fixtures use `/api/v1`; user-owned
share-link code is renamed from its ambiguous share API module to links, while
the short `/s/*` visitor module stays. No compatibility fallback retries an
old path.

The network settings form preserves and labels `app_hosts`, `content_hosts`,
trusted proxies and compatibility origin roles. Save warnings and
stored/applied/restart outcomes remain announced through `role=status` or
`role=alert`, dialogs trap/restore focus, and keyboard flows remain covered.
The route rewrite may change data calls, never accessible names, focus order
or status semantics. Browser integration tests run axe checks on login, setup,
file browser, link management and every admin settings section after rewiring.

## Deliberate changes

1. **The main direct listener advertises HTTP/1.1 only**, an explicit Fiber
   transport constraint. HTTP/2 may terminate at a trusted reverse proxy.
2. **TLS, setup token and probe writes use fsatomic**, with TLS recovery for
   the non-atomic pair rename (server audit findings 1 through 3).
3. **Setup uses canonical username/password policy and core grant wrappers**,
   removing the command-layer SQL bypass.
4. **Each listener generation owns a separate Fiber app**, preserving bind-new
   and bounded drain semantics under Fiber.
5. **Periodic maintenance is one checked task table**, so DAV/login-flow sweeps
   cannot be forgotten.
6. **Settings application policy leaves handlers/assembly**; server supplies
   lifecycle callbacks but does not switch on sections.

## Tests

- Assembly dependency/route/task tables contain every required component once
  and start in the documented order.
- Listener swap fault matrix: app build failure, cert failure, bind failure,
  concurrent swaps, long request, request-context cancellation, drain timeout
  and process shutdown. The old listener stays live until replacement accepts.
- Fiber generation isolation: draining old does not stop new.
- TLS golden and fault injection at every stage/rename. The visible pair is
  old-valid, new-valid, or recognized torn and regenerated, never silently
  trusted mismatched material.
- Healthcheck verifies the real certificate, rejects a substituted cert,
  bounds a malicious body and exits 0 for degraded.
- Setup race, expiry, constant-time comparison, durable token fault injection,
  canonical username vectors, first-admin grants and partial failure audit.
- Probe concurrent-read and crash durability tests; default fallback.
- Periodic task integration advances an injected clock and observes share,
  DAV-lock and login-flow cleanup.
- Sandbox parent-grant/component-boundary fixtures, root special case and
  apply-before-open ordering assertion.
- Degraded boot reaches the emergency door on invalid stored and bindable-but-
  unavailable listener addresses; public peers still see 404.
- CLI dispatch before flags, bounded settings stdin, master key never exposed
  over routes, route dump matches the live Fiber table.
- End-to-end cutover builds all tag combinations and grep/layer gates find no
  old/new cross-import or remaining runtime import of `go/internal`.
- Frontend route literals and mocks contain no unversioned native API path;
  browser flows pass keyboard/focus and automated accessibility checks.
