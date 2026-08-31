# Presentation layer audit

Scope: `go/internal/httpapi` (and `handler`, `mw`, `route`, `spa`, `ws`),
`go/internal/dav`, `go/internal/apierr`, `go/internal/archive`,
`go/internal/server`, `go/internal/compat` (`nc`, `ncport`, `ncwire`),
`go/cmd/stowcloud`, `go/cmd/sc-smb-agent`.

This audit reads every non-test file in the packages above. Test files were
skimmed for behavioral hints only. Findings are numbered per package, with a
file and approximate location, a one-line description, and a severity tag:
`defect`, `misplacement`, `duplication`, `naming`, or `none` (confirmatory,
no action implied). Line numbers are approximate and may drift with future
edits; they point at the right function.

## httpapi (chain.go, state.go)

### Findings

1. `httpapi/chain.go`, `steps()`. [none] The twelve-step order is data (a
   `[]Step` slice), not a comment asserting an order. `Chain` composes from
   the end of the table backward, so the source order is the execution
   order by construction. `chain_test.go` replays the table with recording
   stubs, so a reorder is caught by a test rather than by reading the
   comment. This pattern (order as data, not prose) should carry into the
   fiber rebuild as a hard rule: any list of middleware wrapped around a
   fiber app should have one source-of-truth ordering, checked by a test
   that fails on reorder.

2. `httpapi/chain.go`, comment on step order. [none] Two deliberate
   inversions against a numbered proposal are called out explicitly:
   `AuditSink` wraps `ErrorMapper` (so the audit line carries the final
   status), and `ErrorMapper` is innermost (so no handler error escapes
   unmapped). Both are structural properties worth re-stating as an
   explicit ordering constraint document for the fiber rebuild, since
   fiber's own middleware chaining direction (`app.Use` order equals
   outer-to-inner, same as here) needs the same two inversions preserved
   rather than assumed to not matter.

3. `httpapi/state.go`, `State` struct. [none] `State` is a plain
   configuration/live-holder struct (trusted proxy set, host set, rate
   limiter, CSRF key, route lookup). No business logic lives here; every
   field is either boot-time configuration or a live pointer the settings
   surface mutates. Correct shape to carry forward, modulo the `handler.Deps`
   overlap noted below.

### Rebuild notes

- The chain-as-data pattern (`[]Step` with `Wrap func(*State, http.Handler)
  http.Handler`) and its order-replay test are worth carrying forward
  verbatim as a testing discipline, not just as an implementation detail.
- `mw.ProtocolPaths` (declared in `mw/auth.go`, populated from `state.go`
  and consumed here) is the seam a second protocol (Nextcloud compat) hangs
  its public-path and file-prefix rules on without the auth step knowing
  the other product's vocabulary. This indirection (protocol paths as data,
  not as an import) should be preserved as an explicit contract for fiber:
  the auth middleware takes a data value describing public paths, never an
  import of the compat package.

## httpapi/mw

### Findings

1. `mw/auth.go`, `Auth` (whole function). [none] Verified in detail. A
   credential that fails to validate maps to the same 401 regardless of
   whether it was missing, expired, or forged (comment states this
   explicitly and the code follows it: `CodeAuthRequired` vs
   `CodeAuthInvalid` distinguishes "no credential" from "credential
   presented and rejected," never "expired" from "forged"). Session tokens
   travel as hex and are decoded before hashing, matching what
   `CreateSession` hashed; a raw-hex mismatch here would silently
   invalidate every session. Public GET/HEAD routes still attempt session
   resolution (so a signed-in browser sees personalized state on a public
   page) but never fail the request on a bad credential, which is
   correctly scoped to reads only. This is a carefully reasoned piece of
   code with real defect fixes recorded in its own comments (three
   documented past incidents: hex-vs-raw hashing, DAV mount losing its
   challenge, static-asset rule swallowing `/dav`). Worth preserving as an
   explicit written spec, not re-derived from a general understanding of
   "how auth middleware should work," because a fiber rewrite starting from
   a generic auth-middleware template would very likely reintroduce at
   least one of the three documented regressions.

2. `mw/auth.go`, `publicPaths` (near the end). [none] The exclusion of `/dav`
   and any `ProtocolPaths.FilePrefixes` from the "everything non-API GET is
   a public static asset" rule is the fix for a real historical defect
   (comment: "a read of any file on the server was treated as a static
   asset and reached the mount with no credential attached, so writing a
   file worked and reading the same file back was refused"). This exact
   rule, an allowlist of static-asset GETs that explicitly excludes every
   file-serving prefix, must be re-specified as its own boundary rule in
   the fiber rebuild rather than trusted to a static-file middleware's
   defaults, since fiber's own static/filesystem middleware has no concept
   of "this prefix is not actually static, it needs a credential."

3. `mw/csrf.go`, `CSRF` (whole function). [none] Correct construction:
   Bearer/app-password requests skip the check (cannot be attached
   cross-site), a request with no principal skips it (Auth already refused
   or the route is public), origin is checked against the live `HostSet`
   (not a value frozen at chain-build time, which the comment documents as
   a past defect: "an administrator who saved a new app host got a guard
   that admitted the request and a CSRF step that refused it"), and the
   token is `hmac.Equal` compared. `originAllowed` on an empty declared-host
   list (first boot) returns true regardless of origin, which is safe
   because `HostGuard` has already bounded the request to the local network
   at that point (see hostguard.go finding below) and nothing more
   sensitive than the setup form sits behind it.

4. `mw/csrf.go` and `mw/hostguard.go`, joint dependency. [misplacement,
   minor, spec-worthy] `originAllowed`'s "empty declared list is
   first-boot, admit anything" logic is only safe because `HostGuard`
   independently enforces the local-network-only rule for the same empty
   state (`hostguard.go`, `admit = smb.IsPrivate(...)` when `len(declared)
   == 0`). The two middleware steps share an invariant (an empty host list
   means first boot, which the network-boundary check bounds) but nothing
   in the code ties the two together beyond the fixed step order in
   `chain.go` and a comment in each file cross-referencing the other's
   reasoning. This is not a bug today because the order is fixed and
   tested, but it is exactly the kind of two-middleware coupled invariant
   that a fiber router's more flexible route-grouping could accidentally
   separate (e.g., if CSRF were ever applied to a route group that skips
   HostGuard). Worth a single stated invariant in the rebuild spec: "CSRF's
   origin bypass on an empty host list is only sound because HostGuard has
   already restricted the request to a private address"; the two rules
   should possibly be merged into one step in the rebuild rather than kept
   as two steps trusting each other's comment.

5. `mw/errormapper.go`, `ErrorMapper`. [none] Correct construction: recovers
   a panic and answers 500 with the trace-correlated log line rather than
   crashing the process; checks `rec.status == 0` before writing on a
   handler error, so a handler that already wrote a response is never
   double-written; `statusRecorder` forwards `Hijack` and `Flush`, which is
   the specific fix that lets a WebSocket upgrade (going through the same
   chain) or an SSE stream pass through the recorder transparently. This
   hijack/flush passthrough is a real, non-obvious requirement: a naive
   `http.ResponseWriter` wrapper in the fiber rebuild (or, if fiber's own
   `fiber.Ctx` model is used instead of `net/http` wrapping, its own
   different streaming/upgrade primitives) needs the equivalent property
   stated explicitly, since fiber does not have `http.Hijacker` at all
   (fasthttp's model differs), so this is not a line-by-line port but a
   "re-derive the same guarantee under fasthttp's model" task.

6. `mw/aclscope.go`, `ACLScope`. [none] Correctly scoped: an app password's
   scope is checked only against the route table's declared `Requirement`
   (`AccessAny` / `AccessSelfAdmin` / `AccessPerms`), and the comment
   correctly states that the per-path ACL check is deliberately not here
   ("it happens inside `core.Resolve`, because that check needs the
   resolved share and the two would otherwise disagree"). This is the
   correct division of labor: route-shape scope gating at the middleware
   layer, path-specific grant evaluation inside the service. Worth stating
   explicitly as a rule for the fiber rebuild's router: scope gating reads
   only the route table, never touches a resolved path, and the two must
   never be allowed to drift into re-implementing each other.

7. `mw/bodylimit.go`, `BodyLimit`/`exemptFromBodyLimit`. [duplication, minor,
   already flagged in handler.md] `handler.go`'s `readBody` re-checks
   `len(body) > limits.RequestBody` after `io.ReadAll`, duplicating what
   `mw.BodyLimit`'s `http.MaxBytesReader` already enforces ahead of every
   route but uploads. See httpapi/handler audit finding 10 for detail; the
   root cause is here (two enforcement points for one bound) and the fix
   belongs to whichever layer the fiber rebuild keeps.

8. `mw/hostguard.go`, `HostGuard`. [none] Correctly documented and
   implemented fail-open-to-local-network-only behavior on first boot
   (`smb.IsPrivate(ClientFrom(ctx))`), never fail-open to the internet. The
   misdirected-request response (421) with `Connection: close` is the
   RFC-correct signal for "this server does not answer for that name."
   Sound as a spec to carry forward; the empty-host-list special case
   should be documented alongside the CSRF interaction noted above.

9. `mw/ratelimit.go`, `RateLimiter.Allow`. [none] Standard token-bucket
   discipline with an injected clock (testable) and a bounded map (65536
   entries, arbitrary eviction on overflow) that prevents a flood of fresh
   addresses from growing memory without bound. The eviction is
   "unfair" (an arbitrary bucket is dropped, not the oldest by design) but
   the comment states this deliberately and it is a reasonable trade
   given the alternative is unbounded growth. Worth carrying forward as a
   named bound (65536) in the fiber rebuild's limits table rather than
   left as a bare literal in the rate limiter file.

10. `mw/requestid.go`, `newV4`. [none] Hand-rolled UUID v4 from
    `crypto/rand`, justified as "the dependency the plan would otherwise add
    exists to format sixteen bytes." A `panic` on `rand.Read` failure is
    deliberate (no secure session can be minted either at that point). This
    is a defensible YAGNI call; the fiber rebuild should make the same
    call explicitly rather than reach for a UUID library by default.

11. `mw/securityheaders.go`, `AppPolicy`/`SecurityHeaders`. [none] The CSP
    is deliberately harsh, has three documented past-incident fixes baked
    into specific directives (hash-based `script-src` for the hydration
    bootstrap, `font-src 'self' data:` for an inlined font, `worker-src
    'self' blob:` for the upload worker), and headers are written before
    the handler runs so every response, success or error, carries them.
    Every one of these three fixes is the kind of thing a fiber
    security-headers middleware's defaults would not know to include; they
    must be re-specified as explicit CSP directives in the rebuild, not
    inherited from a generic "secure headers" fiber middleware.

12. `mw/trustedproxy.go`, `resolveClient`/`forwardedFor`. [none] The
    fail-closed rules are explicit and correct: an untrusted peer's
    forwarding headers are discarded unparsed (never trust a client to
    name its own address), an unparseable hop in `X-Forwarded-For` aborts
    the walk rather than skipping the garbage entry (skipping would let an
    attacker-inserted malformed hop redirect trust past itself), and a
    list of only trusted proxies yields no client address at all rather
    than falling back to a proxy's own address. This proxy-trust logic is
    security-critical (it decides who is rate-limited and who the audit
    log names) and is exactly the kind of thing fiber's own
    `X-Forwarded-For`-aware helpers (or a generic `proxy` middleware) get
    wrong by default (most default implementations trust the rightmost
    non-trusted hop without the "abort on first unparseable hop" rule).
    Must be carried forward as a written spec with its three fail-closed
    rules named individually, not delegated to a generic library.

13. `mw/ctx.go`. [none] Plain context-key accessors, one type per key,
    correctly namespaced to avoid collision. No logic beyond storage and
    retrieval. Clean.

### Rebuild notes

- `mw.Auth`'s three documented past regressions (hex-vs-raw session hash
  mismatch, `/dav` losing its WWW-Authenticate challenge under a public-path
  rule, static-asset exclusion missing file-serving prefixes) are exactly
  the traps a fiber rewrite starting from a generic auth-middleware
  template would fall back into. Write these as three explicit negative
  test cases in the auth spec document, not just as prose.
- The `HostGuard`/`CSRF` shared invariant on an empty declared-host list
  (finding 4) should be resolved explicitly in the rebuild: either merge
  the two checks into one step, or write the dependency down as a named
  rule enforced by a test that fails if the step order changes.
- `ErrorMapper`'s panic recovery and `Hijack`/`Flush` passthrough
  (finding 5) needs a from-scratch redesign under fiber/fasthttp's
  different streaming model, not a line-by-line port; the property to
  preserve is "the WebSocket upgrade and the SSE stream still work when
  wrapped by the error-recovery layer."
- `TrustedProxy`'s three fail-closed rules (finding 12) are the single
  highest-value piece of middleware to carry forward as a written spec
  with explicit test cases, since they are the kind of subtlety a generic
  fiber proxy-trust middleware gets wrong by default.
- `SecurityHeaders`' three CSP fixes (finding 11) must be individually
  re-specified; a fiber security-headers middleware's defaults will not
  reproduce any of the three without being told.

## httpapi/route

### Findings

1. `route/route.go`, `Validate`. [none] Every route must declare a
   `Requirement`; a route with `AccessUnset` fails startup rather than
   silently defaulting to the loosest or tightest access. This
   fail-at-startup discipline (rather than fail-at-request-time or
   silently-default) is a real, working invariant worth preserving
   verbatim: the fiber rebuild's router should refuse to start if any
   registered route omits an explicit access requirement, exactly as here.

2. `route/route.go`, `From`/`Match`. [none] `route.Lookup` is the one
   function both the ACL-scope middleware and the mux consult, so the two
   cannot disagree about which route a request hit (the comment states
   this explicitly as the reason the same table backs both). This
   single-source-of-truth property is worth an explicit statement in the
   fiber rebuild spec: whatever fiber's router exposes as its own
   route-matching, the scope-gating middleware must consult that same
   matcher, not a hand-rolled parallel one, or the two can drift the way a
   second WebDAV-prefix matcher could.

### Rebuild notes

- Preserve "no route may omit an access requirement, checked at startup"
  as an explicit, tested property of the fiber rebuild's router
  construction.
- Preserve "the scope-gating middleware and the dispatcher read the exact
  same route table," which in a fiber app likely means deriving scope
  requirements from fiber's own route registration rather than
  maintaining route.go's parallel table.

## httpapi/spa

### Findings

1. `spa/embed_on.go`/`embed_off.go`. [none] The build-tag split changes
   whether the frontend bundle exists, never whether a function exists,
   which the comment explicitly calls out as the reason ("a build tag that
   changes a function's existence rather than its behaviour pushes the tag
   into every caller"). Clean pattern, worth reusing for any fiber-side
   optional-frontend build.

2. `spa/csp.go`, `hashesFrom`. [none] A minimal, string-scan-based inline
   script extractor (not a full HTML parser) that only needs to find
   `<script>...</script>` pairs without a `src` attribute, computed once at
   startup via `sync.OnceValue`. Correctly scoped to its one job (CSP hash
   generation for a known, self-produced bundle, not general HTML
   parsing of untrusted input), so the lack of a real parser is not a
   defect here.

3. `spa/spa.go`. [none] Uploaded content is explicitly never served from
   this mount (documented in the package doc: "a stored HTML or SVG file
   executing in a browser has no session to steal"), which is the
   structural reason a strict same-origin CSP is safe for this server:
   there is no untrusted-content origin under the same policy. This
   separation (app origin vs content origin, never merged) is a design
   decision worth carrying forward as an explicit architectural
   constraint, not just an implementation detail.

### Rebuild notes

- Carry forward the deliberate separation between the app origin (strict
  CSP, session cookie) and any content-serving path (uploaded bytes never
  served from a mount a session cookie reaches).

## httpapi/ws

### Findings

1. `ws/ws.go`, `conn.apply`/`conn.flush`. [none] Every subscribe is
   checked against `acl.Read` at subscribe time, and every event is
   re-checked against the same grant at flush time (comment: "a grant
   revoked between the two must not leak the next event, and checking only
   at subscribe time is exactly what makes a revoked grant keep
   delivering"). This re-check-on-every-delivery pattern is the correct
   fix for a real class of bug (stale authorization on a long-lived
   connection) and must be carried forward as an explicit requirement for
   any fiber-based WebSocket rebuild, since it is easy to check
   authorization once at upgrade time and never again.

2. `ws/ws.go`, `Frame`/`serverMsg`. [none] The wire message carries no
   read token or content, only "this path changed, re-fetch it," which
   sidesteps a whole class of staleness bugs (comment: "a read here would
   already be stale by the time the frame lands, and the client re-reads
   the path anyway"). Worth preserving as an explicit wire-shape rule: the
   invalidation channel never carries data, only invalidation signals.

3. `ws/ws.go`, `Hub.Upgrade`/`conn.readLoop`/`conn.close`. [none] Connection
   lifecycle is symmetric: `readLoop`'s `defer c.close()` guarantees the
   connection is removed from the hub's map and the socket is closed on
   any read error, which is what prevents the writer task leaking after
   the peer disconnects. `gorilla/websocket`'s `CheckOrigin: func(r
   *http.Request) bool { return true }` is notable: origin checking is
   disabled entirely at the WebSocket layer, relying on the fact that the
   upgrade path already runs behind the chain's own Auth/CSRF-equivalent
   checks (the socket is only reachable via `/api/events`, an
   authenticated route). This is not a defect given the surrounding chain,
   but it is worth flagging explicitly as a coupled invariant for the
   fiber rebuild: if a fiber-based WebSocket upgrade is ever exposed on a
   route that bypasses the auth chain, the disabled origin check becomes a
   real cross-site WebSocket hijacking hole.

### Rebuild notes

- The disabled `CheckOrigin` (finding 3) is safe only because the upgrade
  endpoint sits behind the full auth chain. Document this coupling
  explicitly so a future route change cannot silently strip the
  authentication gate while leaving origin checking permanently disabled.
- Preserve the debounce-then-recheck-on-delivery pattern (finding 1) as an
  explicit requirement, and the content-free invalidation message shape
  (finding 2) as an explicit wire-shape rule.

## httpapi/handler

(Audited in full by a subagent; findings reproduced and lightly edited for
consistency with the rest of this document. 35 non-test files, 8754 lines.)

### Findings

1. `handler/admin_grants.go` (`AdminGrants`, `AdminGrant`) and
   `handler/shares.go` (`Shares`). [misplacement] Direct SQL calls from the
   handler: `acl.ListGrants(r.Context(), d.State.SQL(), ...)`,
   `acl.CreateGrant`, `acl.DeleteGrant`, `acl.UpdateGrant`. The presentation
   layer reaches straight into `d.State.SQL()` and calls the ACL package's
   store functions, skipping any core/service method. This matches the
   survey's note that `acl` mixes evaluation and SQL, but from the
   handler's side it means the wire layer is a second caller of raw grant
   SQL alongside whatever the core does. In the fiber rebuild this must go
   through a service-level grant surface, not `d.State.SQL()` reached from
   a handler.

2. `handler/shares.go`, `grantToCreator`. [misplacement] The handler calls
   `acl.CreateGrant(r.Context(), d.State.SQL(), acl.Grant{...})` directly
   to grant a share's creator access. This is domain policy ("creating a
   share also grants its creator full access") implemented in the
   presentation layer by hand-building a SQL-backed grant row, not a
   service call.

3. `handler/admin_grants.go` (`grantsChanged`), `handler/shares.go`
   (`sharesChanged`), `handler/smb_apply.go` (`smbChanged`).
   [duplication/misplacement] Three names for two shapes of one idea
   ("something changed, tell SMB, reload ACL"), spread across three files.
   `sharesChanged` is a one-line pass-through to `smbChanged`. The rebuild
   should have one call with one name.

4. `handler/handler.go`, `Deps` struct. [misplacement] `Deps` is a
   190-line struct holding not just handler dependencies but live mutable
   state normally owned by a service or the composition root:
   `Runtime *runtimecfg.Holder`, `Trusted *mw.TrustedSet`, `Hosts
   *mw.HostSet`, `SwapListener`, `RequestRestart`, `ReloadACL`,
   `ApplyIndexEnabled`, `PublishSMB`. This is effectively the whole
   server's control surface injected into the handler package as one
   grab-bag struct. The fiber rebuild should split this into an explicit
   service interface (settings service, restart controller, ACL reload,
   SMB publish) rather than one struct of 30-odd fields the handlers close
   over.

5. `handler/admin_settings.go`, `applySaved`. [misplacement] This function
   decides, per settings section, whether a change is live, needs a
   listener swap, or needs a full engine restart, and directly calls
   `d.SwapListener` and `d.RequestRestart()`. This is server-lifecycle
   orchestration sitting in an HTTP handler file rather than in a
   settings/runtime service. The handler should ask a service "apply this
   section" and get back one outcome; today it computes the outcome
   itself.

6. `handler/admin_settings.go`, `extractSecrets`. [misplacement] The
   handler hardcodes which settings fields are secrets ("oidc" section,
   "client_secret" key), strips the key, and calls `d.StoreSecret`.
   Knowing which fields are secret-shaped per section is domain/service
   knowledge, not something the wire-shape layer should hardcode; a second
   settings section with a secret field needs the same logic copy-pasted
   here.

7. `handler/session.go`, `Session`. [misplacement, minor] The session
   response assembles account state, SMB state, OIDC link state, roots,
   limits and features by calling five to six different service methods
   and combining them inline, including business judgment ("an SMB state
   read failure is not a failed session") and folded second-factor policy.
   This projection logic belongs to a "current user view" service method
   returning one assembled value; today the handler stitches five domain
   reads together.

8. `handler/fs.go`, `moveOne`/`copyOne`. [duplication] Both re-derive the
   destination path the same way and both do the "resolve source, resolve
   computed destination, translate errors to a `batchItem`" dance almost
   identically. A second implementation of "resolve one leg of a batch
   transfer" to keep in sync by hand.

9. `handler/link.go` and `handler/admin_users.go`. [duplication] The
   "decode into a typed struct, then decode the same raw bytes again into
   `map[string]json.RawMessage`" pattern (to distinguish an omitted field
   from an explicit null on PATCH) is written out twice, independently. No
   shared `presentKeys` helper exists.

10. `handler/handler.go`, `readBody`. [defect, minor] `io.ReadAll(r.Body)`
    is fully buffered before the size check runs. `mw.BodyLimit` already
    installs `http.MaxBytesReader` at the same bound ahead of every
    handler except uploads, so in practice this is a second, redundant
    enforcement point rather than a live unbounded-read risk. Worth
    collapsing to one enforcement point in the rebuild.

11. `handler/handler.go`, `Wrap`/package doc. [none] Verified: every
    handler returns an `error` and never chooses a status on a failure
    path; the only direct `WriteHeader` calls are on success. A real,
    followable convention worth carrying forward as an explicit rule.

12. `handler/uploads.go`, whole file. [none] The TUS surface deliberately
    runs outside the shared body-size limit and outside the shared error
    mapper (`tusError`, including a non-standard status 460 for checksum
    mismatch). Correct, documented, intentional exception; must be an
    explicit named exception in the fiber router, not something a generic
    middleware silently swallows.

13. `handler/link.go`, `handler/link_public.go`. [none] The public
    share-link surface re-checks the password gate on every route through
    the shared `linkFor` helper. No route skips it.

14. `handler/oidc_flow.go`, `safeReturnTo`. [none] Sound open-redirect
    guard: leading single slash required, leading `//` (scheme-relative
    host) refused, every byte restricted to printable ASCII (blocks
    header/CRLF injection into the eventual `Location` header). Worth
    carrying forward as a named primitive.

15. `handler/archive_create.go`, `archiveName`/`contentDisposition`/
    `percentEncode`. [none] Filename-to-header encoding strips
    CR/LF/quote/backslash/control from the plain form and percent-encodes
    the RFC 5987 extended form; the one properly hardened piece of
    header-construction logic in the package, correctly centralized and
    reused by `fs.go`, `link.go`, `link_public.go`.

16. `handler/admin_users.go`, self-lock checks. [none] Both "cannot delete
    self" and "cannot disable self" compare the path id against the acting
    admin's own id before the mutating call, a real safeguard against an
    admin locking a deployment out of its own accounts.

17. `handler/session.go`, `Logout`. [defect, low severity] The
    session-revocation error is swallowed with the comment "a session that
    fails to revoke is expired anyway," but `RevokeSession` can fail for
    reasons other than expiry (a database error). In that case the cookie
    is cleared client-side while the session row survives server-side.
    Low-impact (the session still expires on schedule) but the
    justification is broader than what the code guarantees.

18. `handler/admin_ops.go`, `AdminIndexSettings`. [none] Correctly reports
    `restart_required` based on whether `ApplyIndexEnabled` actually
    succeeded, matching the "stored vs applied" honesty pattern used
    throughout the settings surface.

19. `handler/handler.go`, `limits.RequestBody` reuse. [misplacement,
    minor] A single JSON-body-shaped bound (1 MiB) is applied uniformly to
    every JSON route including large free-form documents
    (`AdminServerSettingsSection`), with no per-route override mechanism.
    Worth an explicit route-class-to-bound table in the fiber rebuild.

20. `handler/fs.go`, `handler/trash.go`, `handler/link.go`. [duplication,
    minor] The "iterate a batch, resolve each vpath, collect a `batchItem`
    per outcome" loop skeleton is copy-pasted across `Delete`,
    `Move`/`Copy`, `TrashRestore`/`TrashPurge`. A generic batch-runner
    helper would remove the repetition.

### Rebuild notes

- The `Wrap`/single-error-mapper discipline (finding 11) should become a
  compile-time property if practical in the fiber rebuild, since fiber's
  own idiom of writing directly to `fiber.Ctx` from anywhere in a handler
  would silently break this discipline if copied naively.
- `apierr.Map` is the single place that turns domain sentinels into
  status/code/key; the upload mount's `tusError` is the one named,
  deliberate exception. Carry forward exactly this split, not a third
  mapper invented ad hoc per route.
- The upload mount's exemption from the shared body limit and shared error
  mapper must be an explicit, named exception in the fiber router's
  middleware configuration, not something achieved by mount ordering a
  future refactor could silently reorder.
- CSRF, session cookie handling, and app-password bearer/basic auth are
  load-bearing for every state-changing handler here. Any fiber middleware
  substituted for these needs to preserve: same-origin check via `Origin`
  (not `Referer`), the `__Host-` cookie prefix, the stateless
  HMAC-derived CSRF token tied to the session token, and the deliberate
  refusal to distinguish "expired" from "forged" in the response.
- The route table's per-route `Requirement` (`AccessSelfAdmin`/`AccessAny`/
  `AccessPerms`), refused at startup if omitted, should be re-specified
  explicitly for the fiber router rather than left to route-grouping
  conventions: a misplaced route under the wrong fiber group is exactly
  the kind of silent scope-widening this table currently prevents.
- Grant writes and settings-secret handling bypassing a service layer
  (findings 1, 2, 6) need an explicit service surface before or during the
  fiber rebuild.
- Streaming responses (`Read`, `ArchiveCreate`, `SearchStream`, both link
  download routes) all share one assumption that should become one
  documented streaming-response contract rather than four independent
  restatements in comments: status and headers commit on the first byte,
  so all error paths must resolve before that point.
- `search.go`'s `http.Flusher` type assertions are safe under `net/http`
  but fasthttp does not support classic `http.Flusher` semantics; this
  needs a deliberate redesign under fiber, not a line-by-line port.
- The nanosecond-as-decimal-string wire convention (used pervasively) is a
  real, load-bearing client contract and needs to be captured as an
  explicit wire-shape rule for the fiber rebuild's JSON encoding.

## dav

(Audited in full by a subagent. 15 non-test files.)

### Findings

1. `dav/scan.go`, `newScanner`. [none] Refuses `xml.Directive` (DOCTYPE)
   and any `xml.ProcInst` beyond the leading `xml` declaration, disables
   `dec.Entity`, and never enables a charset reader: a correct, tested
   defense against XXE and entity expansion (billion laughs). Exercised by
   `scan_test.go` and by the PROPFIND/PROPPATCH/REPORT fuzz tests.

2. `dav/scan.go`, `Limits`/`scanner`. [none] Element count, nesting depth,
   per-element name length, and accumulated text are all bounded, on top
   of a 256 KiB `http.MaxBytesReader` cap on the raw body before the
   scanner ever runs. Layered bounding is sound.

3. `dav/scan.go`, `checkDeclared`. [none] Closes a real gap in
   `encoding/xml`: an undeclared namespace prefix is not an error to the
   stdlib decoder by itself, so `checkDeclared` is what makes an undeclared
   prefix and a properly bound one distinguishable. Not something a naive
   `encoding/xml`-based fiber rewrite would think to add.

4. `dav/lock.go`, `Locks.Create`. [defect, minor] Lock admission is not one
   atomic transaction: a read of existing locks, an in-memory conflict
   check, then a separate write transaction that only re-checks a
   per-user count cap, not path-level conflicts. Two concurrent LOCK
   requests on the same path can both pass the same snapshot check and
   both insert; no unique constraint on `(share, path)` exists in the
   `dav_lock` schema to catch this at the database level (the primary key
   is the per-call token, which never collides).

5. `dav/lockmethod.go`, `guardWrite`/`resourceState`. [defect, minor] The
   If-header evaluation and the lock guard each independently read the
   `dav_lock` table, unsynchronized; a lock taken or dropped between the
   two reads can leave a request acting on two different lock states.
   Same root cause as finding 4: no read composes several lock checks into
   one snapshot.

6. `dav/lock.go`, `newLockToken`. [none] Tokens are 16 bytes of
   `crypto/rand`, UUID-shaped for log readability. Correct choice.

7. `dav/lock.go`, `Locks.Guard`. [none] Checks token possession, then
   principal ownership of that token: a leaked token cannot be used by
   another user, stronger than RFC 4918 strictly requires and documented
   as deliberate.

8. `dav/ifheader.go`, whole file. [none] The If-header grammar (RFC 4918
   10.4) is hand-parsed with explicit bounds on list count, condition
   count, and token length. Weak ETags are recorded but never satisfy a
   condition, matching the documented rule that weak validators guard
   revalidation, not writes. Worth carrying into the fiber rebuild as a
   written spec, not reimplemented from a general RFC reading.

9. `dav/content.go`, `parseByteRange`. [none] Handles only a single byte
   range and explicitly refuses a multi-range request rather than
   silently answering a subset. Worth stating as an explicit spec
   requirement for the rebuild.

10. `dav/content.go`, `matchesAnyETag`. [none] If-None-Match uses weak
    comparison, the opposite rule from the If header's strong comparison
    in `ifheader.go`. Both correct for their use, but the asymmetry is
    easy to get wrong in a rewrite if the two are not read side by side.

11. `dav/content.go`, `put`/`copyFile`/`fileWriter`. [none] Writes go
    through `core.CreateFile` with `vfs.DurableOpts`; no plain
    `os.WriteFile` or hand-rolled temp-plus-rename anywhere in this
    package.

12. `dav/content.go`, `get`. [none] A streamed body's `io.Copy` error
    after `WriteHeader` is logged rather than surfaced as an HTTP error,
    correctly, since the status line has already gone out.

13. `dav/write.go`, `Multistatus`. [none] Hand-rolled XML serialization
    with a documented rationale (no stdlib model for a fixed-prefix
    namespace scoped at the root). Every text-carrying call site goes
    through `EscapeText`/`EscapeHref`, and `isValidXMLName` rejects any
    stored dead-property name that could not legally be an element name.
    No injection path found from client-controlled text into raw markup.

14. `dav/escape.go`, `encodePath`. [duplication, minor] A bespoke
    percent-encoder, deliberately not `url.URL.EscapedPath` (which leaves
    sub-delimiters like `&` unescaped, wrong for an XML href). The
    reasoning is sound but this is a second spelling of "percent-encode a
    path" alongside whatever REST URL building does elsewhere; the fiber
    rebuild should decide once whether WebDAV hrefs and REST URLs share
    one encoder.

15. `dav/method.go`'s `StatusOf` vs `apierr.Map`. [duplication] Two
    independent, hand-written status-mapping functions exist for the same
    `core` sentinel errors. Not a bug (WebDAV's status vocabulary
    genuinely differs from the REST surface's, and both get the individual
    decisions right), but a second place that must be kept in sync by hand
    whenever `core` grows a new sentinel. This is a concrete instance of
    the survey's general "status-mapping done in more than one place"
    concern.

16. `dav/method.go`, `Handler`/`New`. [none] The handler takes an
    already-resolved `core.Resolved` and documents plainly that it never
    re-checks a grant and never parses a virtual path; resolution and ACL
    checking happen in `server/davmount.go`. Correct layering, should stay
    this way in the fiber rebuild.

17. `dav/propfind.go`, `identOf`, vs `store/state.Ident`. [misplacement,
    very minor] `identOf` converts a `core.Entry` into a `state.Ident`
    inline inside `dav`, so `dav` knows the shape of a specific persistence
    aggregate's key. Under the target layering this conversion more
    naturally belongs behind a narrow interface the service layer exposes.
    Low severity, pure value conversion with no logic, but worth naming
    for the rebuild's import-direction gate.

18. `store/state/dav.go`, `SweepDavLocks`. [defect, load-bearing for dav]
    Never called from anywhere in the tree. Reads already ignore expired
    rows, so this is not a correctness bug for lock semantics, but the
    `dav_lock` table accumulates a permanent dead row for every lock ever
    taken and expired. Should be an explicit periodic-task requirement in
    the rebuild spec.

19. `dav/uploads.go`, whole file. [none] Chunked-upload collection modeled
    as engine-agnostic verbs behind an interface, with vendor header names
    injected rather than hardcoded. `ParseUploadPath` rejects zero-padded
    chunk names (`010` could alias `10`), a real correctness property
    worth carrying forward verbatim.

20. `dav/search.go`, `QuerySource`/`runQuery`. [none] Collects filter
    leaves verbatim and hands them to whichever registered source claims
    the namespace, refusing rather than silently answering an empty result
    set when nobody claims the vocabulary.

21. `dav/props.go`, `contentTypeOf`. [none] A small name-extension table
    rather than `http.DetectContentType`, deliberately: a PROPFIND must
    never open files just to sniff a MIME type.

22. `dav/props.go`, `PropCtx`/`PropSource`. [none] Vendor property
    rendering is a pull interface, keeping `dav` from importing any
    specific compat vocabulary. Correct shape for the layering boundary.

### Rebuild notes

- PROPFIND body shape rules (empty/whitespace body means allprop,
  `DAV:propname` wins over `DAV:allprop` if both appear, `DAV:include`
  never changes the mode away from allprop) must be re-specified exactly,
  since RFC 4918 leaves some of this to implementation choice.
- PROPPATCH atomicity (any live-property instruction in the request
  refuses the whole request with 403, the rest reported 424) is a
  concrete, testable RFC-4918 requirement worth its own spec document.
- Depth-infinity PROPFIND refusal above a configured collection size
  (before any walking starts, answering 507) and streaming-write flush
  cadence for allowed walks should be explicit named parameters, not
  something that falls out of fiber's own router or JSON-body defaults.
- Lock semantics to carry forward as spec: exclusive/shared conflict
  matrix, depth-infinity ancestor/descendant conflict checks, lock-null
  resource creation on LOCK of a nonexistent URL, and the 423-vs-412
  status split between "no token submitted" and "If header parsed but did
  not hold" (real clients depend on this distinction).
- If-header grammar and the weak/strong asymmetry (finding 10) should be
  carried over as a written grammar plus the two matching rules; fiber's
  own conditional-request middleware, if any, should not be substituted
  without checking it implements this asymmetry.
- Fix the two lock-table TOCTOU windows (findings 4, 5) in the fiber
  rebuild by making lock admission and the If/lock guard each a single
  read-modify-write against `dav_lock`, ideally inside one transaction,
  and add a real database-level constraint rather than relying purely on
  an in-memory conflict scan against a snapshot.
- If the rebuild still writes raw XML markup by hand, the
  `EscapeText`/`isValidXMLName` discipline (escape all text, validate
  every name pulled from storage before treating it as a tag) needs to be
  a named, tested requirement.
- The upload-collection's canonical-decimal member naming rule (no
  leading zero) is a correctness requirement for chunk ordering and must
  be re-specified exactly.
- Unify status-mapping direction (finding 15): define one canonical
  "error class" enumeration in the service layer that both fiber REST
  handlers and fiber WebDAV handlers consume, so a new `core` sentinel is
  wired into both status vocabularies from one place.
- Add `SweepDavLocks` (finding 18) to the fiber rebuild's periodic
  maintenance task list; it is presentation-layer wiring today that
  nothing calls, and should not be re-forgotten in the rewrite.

### Survey claim check

The survey states dav is "rebuilt on the fiber stack in the protocol
phase, parsing rules carry over as spec." This audit confirms the framing.
`dav` is close to a pure protocol/parsing package: it takes an
already-resolved, already-authorized `core.Resolved` and never itself
resolves a path or checks a grant. The XML parsing security surface
(PROPFIND/PROPPATCH/LOCK/REPORT) has real, fuzz-tested defenses. The
If-header parser, lock semantics, upload chunk-ordering rule, and
status-mapping table are all genuine protocol-specification material a
fiber rewrite should treat as a spec to satisfy rather than re-derive from
the RFCs cold. The two lock-table TOCTOU findings (4 and 5) are additional
information the survey did not have: the lock admission logic has a real,
narrow concurrency defect that should be fixed rather than carried over
verbatim.

## apierr

### Findings

1. `apierr/apierr.go`, `Error`/`Wire`/`Write`. [none] One envelope shape,
   constructed in one place (`Write` is the only function in the package
   that touches an `http.ResponseWriter`), so the JSON shape cannot drift
   between call sites. `internal` marks a 500 and suppresses detail on the
   way out, which prevents a caller that attaches a detail key to an error
   it later reclassifies as internal from leaking that detail.

2. `apierr/map.go`, `Map`. [none] The one function on the native REST
   surface that names an HTTP status. The existence rule (`ErrNotFound`
   and the denied-but-hidden subset produce the byte-identical 404 body)
   is centralized here, which is exactly the kind of thing that must stay
   centralized: a second copy of this switch anywhere else in presentation
   is the risk the survey's "status-mapping in more than one place" note
   is about. See dav finding 15 for the one place this concern is already
   real (`dav.StatusOf`).

3. `apierr/map.go`, `Map`, `MaxBytesError` handling. [none] The body
   limiter's `http.MaxBytesReader` refusal arrives as a `*http.MaxBytesError`
   rather than one of this package's own sentinels, and it is explicitly
   unwrapped and mapped to 413 with the correct client-facing code. The
   comment records that this was a real defect before the fix ("the client
   was told the server broke when it was the client that sent too much").
   Worth keeping as an explicit test case in the fiber rebuild, since
   fasthttp's own body-limit mechanism will surface its refusal through a
   different error type that needs the same explicit handling.

4. `apierr/map.go`, `BadRequest`/`Unprocessable`/`BadGateway`. [none] Field
   names are carried in `Args`, never the offending value itself,
   consistently across every constructor. Worth stating as an explicit
   rule for the fiber rebuild: a request-error constructor never echoes
   client-supplied data back into the response.

### Rebuild notes

- Keep `apierr.Map` as the single sentinel-to-status table for the native
  REST surface; do not let a second full copy of its `errors.Is` chain
  exist anywhere else in presentation (dav's `StatusOf` is the one
  existing exception and should be unified per the dav rebuild notes
  above, not used as precedent for a third one).
- The existence rule (not-found and denied-but-hidden are byte-identical)
  is a security property, not a style choice; it must survive as an
  explicit test in the fiber rebuild across every mount that resolves a
  path (REST, DAV, share links).
- `*http.MaxBytesError` unwrapping is stdlib-`net/http`-specific and will
  need a fasthttp-specific equivalent; write this down as a named mapping
  requirement rather than letting it silently disappear when the
  underlying HTTP server changes.

## archive

### Findings

1. `archive/zip.go`, whole file. [none] A hand-rolled streaming zip
   writer, justified by a real constraint (an HTTP response body cannot be
   seeked, and the standard library's zip writer needs to seek back to
   patch a local header once an entry's size is known). Always writes the
   64-bit (zip64) form and the UTF-8 name flag unconditionally, both
   correct simplifications for a server that only ever produces
   already-compressed media as stored entries. Zero internal imports,
   confirming the survey's "already clean" verdict.

2. `archive/zip.go`, `Writer.err`/`AddFile`. [none] Once a write fails,
   every subsequent call is a no-op returning the same error, so a caller
   streaming many entries does not have to check after each one. A read
   failure partway through one entry still produces a valid, if short,
   archive (the entry is closed out at the bytes actually read), which is
   the documented and correct behavior for "the file vanished mid-stream."

3. `archive/zip.go`, `dosDateTime`. [none] Clamps a time before the
   format's 1980 epoch rather than wrapping, which avoids producing a
   nonsensical date an extractor would display. Correct edge-case
   handling.

### Rebuild notes

- No changes implied. The package is a clean, dependency-free wire-format
  writer and can be carried into the fiber rebuild essentially unchanged;
  only its call site (`handler/archive_create.go`) needs to be
  re-implemented under fiber's streaming response model, with the
  "headers commit on the first byte, so validate everything before that
  point" contract preserved (see httpapi/handler rebuild notes).

## server

### Findings

1. `server/setup.go`, `SetupGate.issue`. [defect, confirmed] `os.WriteFile(filepath.Join(g.dir, "setup-token"), []byte(plain+"\n"), 0o600)` is a plain, non-durable write with no fsync and no atomic rename. A crash between the write and a subsequent read leaves the token file torn or absent while the in-memory gate still believes it issued a valid token, and a torn read of a valid-looking but truncated token is a hash mismatch a caller cannot distinguish from an expired one. Matches the survey's finding exactly; should move to the `store/fsatomic` primitive (survey: `ReplaceFileDurable`'s eventual home) in the rebuild.

2. `server/tls.go`, `loadOrCreateTLS`. [defect, confirmed] Both `os.WriteFile(certPath, certPEM, 0o600)` and `os.WriteFile(keyPath, keyPEM, 0o600)` are plain writes with no durability guarantee, and critically, they are two separate non-atomic writes: a crash between the two leaves a certificate and key pair that do not match, which `tls.X509KeyPair` further down would only fail to parse. Key material especially must never be a torn file, per the survey's own note. Should move to `store/fsatomic` with a shape that treats the cert/key pair as one durable unit.

3. `server/probefile.go`, `WriteProbe`. [defect, confirmed] Hand-rolled tmp-plus-rename with no fsync of either the temp file or the containing directory before renaming, and no fsync of the directory after: `os.Rename` alone does not guarantee durability against a crash immediately after (the rename itself can be lost if the directory entry is not synced, and the file's own data can be lost if it was never synced before the rename). The comment claims "a probe reading concurrently sees either the old snapshot or the new one," which is true for concurrent reads (rename is atomic with respect to a concurrent open) but says nothing about durability across a crash, which the survey correctly identifies as "exactly what the primitive exists to prevent." Consequence is low (`ReadProbe` degrades to a compiled-in default on any read failure) but this is precisely the kind of second hand-rolled replace implementation the persistence-layer primitive is meant to make unnecessary.

4. `server/wire.go`, `csrfKey`. [none] A fresh 32-byte CSRF key is minted per process start with `crypto/rand`, never persisted. This means every session's CSRF token becomes invalid on every restart, which is not a defect (a restarted process also drops in-memory sessions in this design, so nothing survives a restart that would need the old key) but is worth an explicit note: if the fiber rebuild ever moves to a persisted session store that survives a restart, the CSRF key must persist alongside it or every existing session becomes unable to make a state-changing request until it re-authenticates.

5. `server/wire.go`, `New` (whole function). [misplacement, minor] The composition root builds `httpapi.State`, the route table, the WebDAV mount, the compat mounts, the chain, and the emergency door, all in one ~150-line function. This is expected for a composition root, but a few pieces of actual behavior are embedded inline rather than delegated: `smbSink`'s three-paragraph comment justifying "never fail the caller, publish synchronously, detach the context" is real domain policy about how a revocation should propagate to SMB, written as a closure inside the server-assembly file rather than as a documented method on a service type. Low severity because the reasoning is sound and the code is correct; flagged because the fiber rebuild's composition root should be assembly only, with this kind of policy decision owned by whichever service exposes `PublishSMB`.

6. `server/wire.go`, `parsePrefixes`. [none] A stored trusted-proxy CIDR that fails to parse is dropped with a warning log rather than failing the load, with the stated rationale that the same value was already validated at save time and refusing it again at load time would make a server unbootable over a value saved weeks ago. Reasonable defensive design; the same reasoning should be applied consistently to every other stored, previously-validated value in the fiber rebuild's config loading rather than decided ad hoc per field.

7. `server/davmount.go`, `resolveDavPath`. [none] The path is percent-decoded per segment after splitting on `/`, and a decoded segment containing a literal `/` is refused (`apierr.BadRequest`). This is the correct order of operations for a path-mapping layer: decoding before splitting is the classic way such a layer is walked out of its root, and this function does the opposite (split first on the raw string, decode each segment, reject a decoded segment that would introduce a new separator). Worth carrying forward as an explicit rule, since it is easy to get backwards.

8. `server/davmount.go`, `davDestination`/`sameOrigin`. [none] A `Destination` header naming another origin is refused with 502 (RFC 4918 9.8.3) rather than having its host silently dropped and the path applied to this server, which the comment documents as a real historical defect ("COPY to `https://elsewhere.example/dav/docs/x` copied to `/dav/docs/x` here"). `sameOrigin` compares host only, not scheme, correctly accounting for a reverse proxy terminating TLS in front of this server.

9. `server/davmount.go`, `davAlias`. [none] Alternate mount points (used by the Nextcloud compat layer) rewrite onto the server's own DAV prefix by dropping a fixed number of leading path segments (the account-name segment a sync client addresses by), and the comment correctly notes that this segment is dropped rather than checked: resolution always happens against the caller's own grants, so a name in the URL cannot widen access. Sound design.

10. `server/consent.go`, `consentPage`/`writeConsent`. [none] The device-login consent page is server-rendered with `html/template` (auto-escaping) rather than string concatenation, the CSRF token and upload token reach the page via `data-*` attributes rather than being interpolated into the inline script body (the comment correctly notes `html/template` cannot safely escape into an arbitrary JavaScript context, only into an HTML attribute), and the page's own CSP uses a per-request nonce rather than widening the app-wide hash-based policy for one page. A `template.Must(template.New(...).Parse(...))` call inside a function (not a package-level var) is deliberate so the template cannot be reassigned by an importer. Sound, defensive construction throughout.

11. `server/secrets.go`, `OpenOIDCSecret`/`StoreOIDCSecret`. [none] A thin, correctly-scoped wrapper: sealing and opening go through `auth.Service`, which holds the master key; this file only names the row and handles the "absent means no secret" case distinctly from "failed to open," which is the right distinction (an unreadable secret is reported and the caller degrades one sign-in method, rather than being treated the same as "not configured").

12. `server/wire.go`, `smbSink` (see also finding 5). [none] The revocation-to-SMB propagation policy (never fail the caller since the database write already committed, publish synchronously since this is an administrator write rather than a request path, detach the context from the request since a browser navigating away must not cancel a revocation) is correct security reasoning, stated explicitly. This exact reasoning must be preserved verbatim in the fiber rebuild's equivalent surface; a naive async fire-and-forget "republish SMB" call would silently reintroduce the stale-access window this code was written to close.

13. `server/routes.go`, `mux`/`rootHandler`. [none] The frontend SPA mount and the WebDAV mount are dispatched from one handler rather than registered as two separate `ServeMux` patterns, with the comment correctly explaining why: Go's `net/http.ServeMux` (1.22+) refuses to register a bare-root single-method pattern beside an every-method-on-a-prefix pattern, since neither is more specific than the other. This is a `net/http`-specific routing limitation; fiber's own router does not have this restriction, so the fiber rebuild does not need to replicate this dispatch-inside-a-handler workaround, only the underlying rule it encodes ("a request under `/dav` never falls through to the SPA, even for a method the frontend has no handler for").

14. `server/routes.go`, `linkEntry`/`wantsJSON`. [none] Content negotiation between the rendered share-link page and its own API is done by inspecting `Accept` for `text/html` vs `application/json`, defaulting to the page when neither is named (reasoning: a client naming neither is more likely a person than a script, and a page rendered for a script is a more readable mistake than JSON rendered for a person). Reasonable, explicit policy; worth carrying forward as a named content-negotiation rule rather than left as router-default behavior in fiber.

15. `server/supervisor.go`, `Serve.Swap`. [none] A listener swap binds the new socket before touching the old one, so a bind failure (a typo in a saved address) leaves the old listener serving and the save request answers with a named-field refusal rather than the deployment going dark. The old listener is drained in the background with a bounded deadline (`drainDeadline`), detached from the request context that triggered the swap. This hot-swap-without-downtime design is a genuine piece of behavior worth carrying into the fiber rebuild as an explicit requirement, since fiber's own app lifecycle (a single `app.Listen()` call) does not have this pattern built in; achieving it under fiber will require the same "build new server, bind new listener, drain old" shape implemented independently of whatever fiber's own graceful-shutdown helpers provide, since those help with process-exit shutdown, not runtime listener replacement.

16. `server/emergency.go`, `ServeEmergency`. [none] The standalone emergency listener falls back from the stored bind address to a hardcoded default in two independent ways: if the stored address fails to parse at all (`runtimecfg.CheckListen`) and, separately, if a syntactically valid stored address fails to bind (interface not present, port taken). Both fallbacks are logged as warnings rather than causing the repair door itself to fail to start, which is the correct behavior for a door whose entire purpose is being reachable when something else is broken.

17. `server/routes.go`, whole route table. [none] Every route declares an explicit `route.Requirement` via the `req`/`selfAdmin`/`any` helpers, and the file's own comments document at least four distinct historical defects fixed by a specific route entry (a wrong HTTP method mounted so login answered the change-password path, an alternate DELETE spelling for cancelling a job that the shipped client actually sends, a PATCH/DELETE pair for editing a share link that existed as handler code but was never mounted, a thumbnail addressing scheme changed from fileid-based to path-based because fileid was not available where the request needed to be made). These are real client-compatibility facts, not just implementation history, and each one should become an explicit route-table test case in the fiber rebuild rather than trusted to be rediscovered by manual QA against the shipped frontend.

### Rebuild notes

- Route the three confirmed non-durable writes (setup token, TLS cert/key
  pair, probe file snapshot) through `store/fsatomic` (per the survey's
  planned extraction) in the persistence phase, before the presentation
  phase depends on them. The cert/key pair specifically needs to be
  written as one durable unit, not two independent atomic writes, since a
  torn pair between them is as bad as a torn single file.
- The listener hot-swap pattern (finding 15, bind new before touching old,
  drain old with a bounded deadline detached from the triggering request)
  is a named requirement to re-implement under fiber, not something
  fiber's own lifecycle helpers provide for free.
- The `net/http.ServeMux` SPA/DAV dispatch workaround (finding 13) is
  `net/http`-specific; carry forward only the underlying rule ("DAV never
  falls through to the SPA"), not the workaround's mechanism, since
  fiber's router does not share the limitation that produced it.
- The revocation-to-SMB propagation policy (findings 5, 12: never fail the
  caller, publish synchronously, detach the context) must be preserved
  verbatim as an explicit written rule, not re-derived, since the natural
  instinct in a rewrite is to make this async and that reintroduces the
  stale-access window it was written to close.
- The route table's documented historical defects (finding 17) should
  become explicit route-table regression tests in the fiber rebuild
  (wrong-method mount for login, missing DELETE alias for job
  cancellation, unmounted PATCH/DELETE for link editing, fileid-based vs
  path-based thumbnail addressing), since each one is a real
  client-compatibility fact discovered the hard way once already.
- The `Destination`-header foreign-origin refusal (finding 8) and the
  decode-after-split path handling (finding 7) are both easy to get
  backwards in a rewrite and should be named test cases, not left to be
  re-derived from an RFC reading.

## compat/nc, compat/ncport, compat/ncwire

(Audited in full by a subagent. 18 files in `nc`, 1 in `ncport`, 2 in
`ncwire`.)

The single biggest finding, stated up front: cross-referenced against
every call site in the tree (confirmed with `go build -tags compat_nc
./...` and a grep for `dav.New(`), only the OCS surface (capabilities,
user info, favorites, search, recent, direct URL, login flow v2, status
probe) is actually wired into a live request path under the `compat_nc`
build tag. The Nextcloud-flavored WebDAV path layout, chunked upload v2,
the trash collection, and the vendor DAV properties are fully implemented
and tested but never reached by a real request: `dav.New(dav.Options{...})`
in `server/wire.go` never sets `Sources`, `Uploads`, or `UploadHeaders`,
so `internal/dav`'s own compat hooks stay nil. `ncwire.go`'s own comment
enumerates a specific, closed list of intentionally-nil ports (Accounts,
Search, Preview, Direct) but does not mention `Revoke`, `Trash`,
`WriteTrash`, or `FileID`, which are also nil, so the comment claims a
smaller gap than actually exists.

### compat/nc

1. `nc/dav.go`, `nc/trash.go`, `nc/props.go`, `nc/chunking.go`,
   `nc/shares.go` (public surface of each). [defect] Dead code: parsed and
   rendered in tests only, never reached by a live request, for the
   reasons stated above. Either this is a missed wiring step or an
   intentional partial rollout with no comment saying so anywhere in
   `server/compat_on.go` or `ncwire.go`.

2. `nc/router.go`, `routeOCS`. [defect / duplication] Never routes any
   OCS share-listing/create/update/delete path; `shares.go`'s entire
   vocabulary (299 lines) is consequently unreachable, duplicating intent
   with `handler/shares.go` (the native, wired share admin surface)
   without ever being reachable itself.

3. `nc/router.go`, `revokeAppPassword`. [defect] Requires
   `l.deps.Revoke`, which `ncwire.Build` never sets and never mentions as
   nil. Live behavior: `DELETE /core/apppassword` always returns a 500,
   for every call, under the `compat_nc` build.

4. `nc/ocs.go`, the OCS envelope writer. [duplication] Hand-builds XML
   with its own escaper (`XMLEscapeText`) rather than `encoding/xml` or
   the escaper `dav/escape.go` already has, justified by a real
   byte-for-byte-fidelity requirement (booleans as `1`/empty, self-closing
   empty elements, no wrapper element for numeric keys) but still a second
   XML escaper with a different contract from the one in `dav`.

5. `nc/chunking.go`, `DestinationPath`. [duplication] Reimplements its own
   traversal check and percent-decoding, independently of
   `nc/dav.go`'s `decodeJoin` and `server/davmount.go`'s
   `resolveDavPath`. Three separate call sites reimplement "decode a path
   segment and refuse `..`," none sharing code.

6. `nc/shares.go`, `FormatShare`. [none] Reproduces a real, documented
   Nextcloud OCS quirk (`share_with` overloaded for a link's password
   state) deliberately, with a comment stating this is intentional
   fidelity to a known-odd reference behavior. Carry forward as an
   explicit spec note in the rebuild, not "fixed."

7. `nc/chunking.go`, `ChunkName`, vs `dav/uploads.go`'s
   `ParseUploadPath`. [duplication, landmine] Both parse chunk member
   names but disagree on leading-zero handling: `dav`'s parser explicitly
   refuses a leading-zero form, `nc`'s does not check for it at all. Since
   `nc`'s parser is unwired (finding 1), this is latent rather than a live
   behavioral divergence today, but if the compat DAV layout is ever
   wired up, the two parsers disagreeing on `"00001"` vs `"1"` would
   silently create two sessions from one intended chunk stream.

8. `nc/user.go`, `QuotaVal`/`bytesVal`, and `nc/shares.go`,
   `fileIDVal`. [duplication, minor] The same uint64-to-int64 clamp (with
   the same comment explaining the Android negative-free-space bug it
   avoids) is written independently at three call sites. A single
   exported clamp helper would remove the duplication.

9. `nc/ocs.go`, `OCS.Write`. [none] Sets `Access-Control-Allow-Origin: *`
   unconditionally on every OCS response, mirroring the reference
   implementation's own behavior. Not a live defect (OCS responses carry
   no cookie-based ambient authority a CORS-reading browser could exploit
   this way), but a wildcard CORS header on an authenticated API surface
   deserves an explicit line in the fiber rebuild's security spec rather
   than being carried over silently.

10. `nc/login_flow.go` and its handlers. [none] Verified end to end
    against `ncwire/loginflow.go` and `mw/csrf.go`: the security
    invariants (POST-only approval, CSRF via the shared step, digest-only
    storage, single-use delivery, rate-limited poll, host-bound origin)
    all hold. The strongest-audited part of this surface.

11. `nc/nc.go`/`ncport/ncport.go` package docs. [naming] Both assert a
    two-way import gate ("the layer may not import the core... may not
    import the store") enforced by a grep-based check, but no such
    gate-enforcing test or lint rule exists anywhere under
    `go/internal/compat`. If a gate script exists it lives outside this
    tree; worth confirming its location and porting the actual check, not
    just the convention, into the fiber rebuild's CI.

### compat/ncport

1. `ncport/ncport.go`, whole file. [none] Exactly what its own doc comment
   says: a seam of type aliases and narrow ported interfaces, no business
   logic, no wire-shape leakage.

2. `ncport.FS.Resolve`. [none] Correctly pushes path parsing into whichever
   `FS` implementation is supplied; `nc` itself never calls
   `vfs.ParseVpath` directly, only `ncwire.fsPort` does. The seam's own
   stated contract ("path parsing lives in the core... doing it here is
   the bug the two path types exist to prevent") is followed.

3. `AccountPort.UserInfoByLogin`. [none] The documented "outside scope and
   absent are the same answer" anti-enumeration property is correctly
   implemented in `nc/user.go`'s `otherUser`.

### compat/ncwire

1. `ncwire.go`, `Build`. [defect] Same issue as compat/nc finding 3, listed
   here because the fix belongs in this file: the comment enumerates a
   closed list of intentionally-nil ports and is silently missing four
   more (`Revoke`, `Trash`, `WriteTrash`, `FileID`).

2. `ncwire.go`, `statePort.InstanceID`. [misplacement] The one place in
   this package pair that writes SQL directly against `state.db`'s
   `compat_kv` table, justified as "the gate forbids the layer from
   importing the store." That reasoning explains why `nc` cannot own the
   SQL, but under the target layering this SQL belongs in a `store/state`
   aggregate that `ncwire` calls, the same shape `Favorites`/`SetFavorite`
   already use two functions below it. `InstanceID` is the one outlier
   with inline SQL instead of a delegated call.

3. `ncwire.go`, `authPort.MintAppPassword`. [misplacement] Hardcodes full
   scope and no expiry for every login-flow-issued app password. Not
   wrong today (one call site, one policy), but a business/policy decision
   sitting in the wiring package rather than being a parameter a future
   settings surface could narrow.

4. `ncwire.go`, imports of `httpapi/mw`. [naming] `ncwire` already imports
   across all three future layers at once (presentation `httpapi/mw`,
   service `auth`/`core`, persistence `store/state`) plus the compat seam,
   which is the sanctioned crossing point but is not mentioned anywhere in
   `01-package-survey.md`'s layer import table.

5. `loginflow.go`, `flowErr`. [duplication, of pattern] A third
   independent sentinel-to-error translation function (alongside
   `apierr.Map` and `dav.StatusOf`), correct for its own vocabulary but
   the same recurring pattern (switch on `errors.Is` against a fixed
   list) with no shared helper.

### Rebuild notes

- Resolve the wiring gap before treating any of this package's protocol
  logic as settled spec: either wire the Nextcloud WebDAV path layout,
  chunking, trash, and vendor DAV properties to a live `dav.Handler`, or
  explicitly drop them from the fiber rebuild's compat phase and keep only
  the OCS half. Carrying the parser forward without carrying the wiring
  gap forward again repeats today's silent half-implementation.
- The OCS envelope's exact byte-for-byte quirks (version-dependent status
  mapping, ordered `Val` tree, boolean-as-`1`-or-empty, self-closing empty
  elements) must be reproduced exactly in the fiber rebuild; write this as
  its own spec document with the reference behavior table already present
  in the comments, not re-derived.
- Login flow v2 (`nc/login_flow.go` and `ncwire/loginflow.go`) is the
  strongest candidate in the whole compat surface for a literal carry-over
  into the fiber rebuild's auth phase: the two-token design, digest-only
  storage, mint-at-delivery, single-use with idempotent redelivery, and
  POST-only approval behind session plus CSRF are a complete, well-reasoned
  state machine. Write it as its own document.
- The three-way public-path/credential-flow/file-prefix split
  (`mw.ProtocolPaths`, consumed by `mw.publicPaths`) is worth keeping as an
  explicit contract for any future protocol mount, but currently has no
  validation that a path in one list is not accidentally state-changing;
  add that validation in the fiber rebuild.
- Fiber-specific hazard: fiber's router and its wildcard/parametrized path
  matching do not behave identically to Go's `net/http.ServeMux` patterns
  used today, particularly for a catch-all path segment
  (`{path...}`-shaped routes in `nc/mounts.go` and `nc/preview.go`). A port
  to fiber's router needs an explicit test that the same URL grammar
  still resolves the same way on edge cases like an empty tail segment.
- Chunked upload v2's leading-zero disagreement with `dav`'s own upload
  collection (compat/nc finding 7) must be resolved to one rule before
  either is wired up in the fiber rebuild.
- Unify the third sentinel-to-error mapping pattern (`flowErr`) with the
  other two (`apierr.Map`, `dav.StatusOf`) per the earlier note in the dav
  and apierr sections: one canonical error-class enumeration, three thin
  per-boundary adapters, not three independent switches.

## cmd/stowcloud

### Findings

1. `cmd/stowcloud/serve.go`, `runServe` (whole function). [misplacement,
   expected] This is the composition root's composition root: it builds
   the sandbox spec, opens the store, opens the master key, builds the
   ACL evaluator, the core, the setup gate, the preview pool, the upload
   engine, the search index, the watcher, the SMB publisher, and finally
   calls `server.New`. At roughly 300 lines this is by far the largest
   function in the audited packages. This is expected and largely
   unavoidable for a single-binary composition root (the file's own
   comments justify the ordering constraints, particularly the sandbox
   having to apply before anything else opens, in detail), and splitting
   it would mostly move code around rather than reduce complexity. Flagged
   as `none` rather than a defect, but the fiber rebuild should consider
   whether this ordering-constrained assembly can be expressed as an
   explicit dependency graph (a list of named steps with their
   prerequisites) rather than one linear function, purely for
   readability, not correctness.

2. `cmd/stowcloud/serve.go`, `grantEveryShare`. [misplacement] This
   function contains real domain policy (the first administrator gets a
   grant with every permission bit over every registered share) expressed
   as a direct call to `acl.CreateGrant(ctx, st.State().SQL(), acl.Grant{...})`
   from the command layer, the same "handler reaches into the SQL-backed
   ACL store directly" pattern already flagged in `httpapi/handler`
   (findings 1, 2 in that section). Confirms that this bypass of a
   service-level grant surface is not confined to the HTTP handlers; it
   recurs at the process bootstrap layer too, which strengthens the case
   for a real grants service surface in the rebuild rather than a
   per-caller convention of calling `acl.CreateGrant` directly.

3. `cmd/stowcloud/serve.go`, `jailSpec`/`shareGrantPath`/`inJail`. [none]
   The sandbox domain is built from share parent directories rather than
   share directories themselves specifically so a share added later
   (inside an already-granted parent) does not need a process restart to
   become reachable, with the one exception of a share whose parent is
   `/` (which grants the share itself, not the root, to avoid putting the
   whole filesystem in the domain). `inJail` is a plain string-prefix
   comparison against the same grant list the domain was actually built
   from, which is the only way to answer "is this reachable now" since a
   Landlock domain cannot be introspected once applied. Sound, carefully
   reasoned.

4. `cmd/stowcloud/failtrack.go`, `recordEngineFailure`/`readFailures`.
   [none] A small file-backed counter (JSON array of timestamps) used
   purely to distinguish "a disk was slow to mount" from "a stored setting
   will never let this process start" across process restarts, bounded to
   only the values inside a one-minute window on every write so the file
   cannot grow unbounded. A parse failure on read is treated as "start the
   count over" rather than failing the boot, which is the correct
   direction for a diagnostic aid. This file uses plain `os.WriteFile` for
   the counter (`os.WriteFile(path, mustJSON(recent), 0o600)`), which is
   technically a non-durable write, but the data it protects is a
   best-effort diagnostic counter, not user data or a security credential;
   losing or corrupting it at worst causes one extra restart attempt
   before the loop is detected, which is proportionate. Not flagged as a
   defect for that reason, but worth naming as a case where a non-durable
   write is an acceptable, deliberate trade-off, in contrast to the setup
   token and TLS material in `server/setup.go`/`server/tls.go`, which are
   not.

5. `cmd/stowcloud/health.go`, `runHealthcheck`. [none] Verifies the
   server's presented certificate against the one stored in `data/tls`
   rather than skipping verification, which the comment states is
   deliberately the point ("a cert that no longer matches what the server
   holds is a server answering with material a healthcheck cannot account
   for"). The health document body read is bounded
   (`io.LimitReader(resp.Body, healthBodyLimit)`), a correct trust-boundary
   check on a response this process does not fully control (the server
   process could in principle be compromised or misbehaving).

6. `cmd/stowcloud/settings.go`, `runSettingsSet`. [none] The document read
   from stdin is bounded (`io.LimitReader(os.Stdin, settingsDocLimit)`,
   1 MiB), a correct trust-boundary check on operator-supplied input even
   though the operator is trusted to have shell access to the data
   directory already; bounding avoids an accidental unbounded read from a
   misused pipe rather than defending against a hostile operator.

7. `cmd/stowcloud/masterkey.go`, `runMasterkeyRotate`. [none] A CLI-only
   command, deliberately never an HTTP route, with the comment stating the
   security reasoning plainly ("a master key must not reach a browser
   tab"). The rotation is documented as crash-safe ("a crash at any point
   leaves the key the committed database names, which the next start
   picks up"), consistent with the durable-write discipline expected
   elsewhere.

8. `cmd/stowcloud/emergency.go`, `degrade`. [none] Correctly distinguishes
   "the engine failed to build, serve the repair door and stay up" from
   "exit and let a supervisor restart," and the process deliberately never
   exits on an engine-build failure precisely because exiting would hand
   the failure back to a supervisor's restart loop, which is the failure
   mode this whole mechanism exists to break. Sound.

9. `cmd/stowcloud/previewworker.go`, `runPreviewWorker`. [none] Takes no
   arguments and reads no configuration by design, specifically so this
   process (which runs inside the tightest sandbox in the system) has no
   way to name a file via argv. `jail.Required` (not a softer hardening
   mode) is used for this one process, correctly treating the jail as the
   feature rather than defense in depth for the one process where a
   sandbox escape would be maximally bad (a decoder fed attacker-supplied
   bytes).

10. `cmd/stowcloud/main.go`, dispatch table. [none] Argv[1]-based dispatch
    before any flag parsing, justified specifically so it works in a
    shell-less container image where Docker's exec-form `HEALTHCHECK` runs
    an argv directly rather than through a shell. Reasonable, working
    design for a single multi-purpose binary; nothing to change beyond
    re-deriving the equivalent entrypoint shape if the fiber rebuild keeps
    the single-binary model.

### Rebuild notes

- `grantEveryShare` (finding 2) confirms the direct-SQL-grant-creation
  pattern already flagged in `httpapi/handler` is not confined to HTTP
  request handling; it recurs at process bootstrap. This strengthens
  (does not merely repeat) the case for a real grants service surface
  callable from both the HTTP layer and the bootstrap CLI in the fiber
  rebuild.
- The jail-domain-from-share-parents construction (finding 3) and the
  crash-safe master-key rotation (finding 7) are both worth carrying
  forward as explicit written invariants, since both encode subtle
  reasoning (a Landlock domain cannot be introspected once applied; a key
  rotation must leave the database naming a key version the file on disk
  actually has) that a rewrite could easily get slightly wrong while
  looking correct.
- The engine-failure-loop-detection mechanism (finding 4) and its
  intentional acceptance of a non-durable write for a low-stakes counter
  is worth naming explicitly as a documented exception in the rebuild's
  durability rules, so a future reviewer does not flag it as an oversight
  equivalent to the setup-token or TLS-material findings in `server`.
- If the fiber rebuild keeps a single multi-purpose binary, preserve the
  argv[1]-before-flag-parsing dispatch shape (finding 10), since it is
  specifically what makes Docker's exec-form healthcheck usable without a
  shell in the image.

## cmd/sc-smb-agent

### Findings

1. `cmd/sc-smb-agent/main.go`, `run` (whole function). [none] Refuses to
   run as non-root with a clear, specific message rather than failing
   three layers down with a permission error from whichever syscall
   happens to hit the account file or credential database first. Correct,
   early trust-boundary check on the process's own privilege level, which
   is exactly what this process needs since it edits system account files
   and binds a privileged port through the daemon it manages.

2. `cmd/sc-smb-agent/main.go`, `run`, log file creation. [none] Creates
   `/var/log/samba/log.smbd` with `os.OpenFile(..., os.O_CREATE|
   os.O_APPEND|os.O_WRONLY, 0o640)` deliberately after the first apply
   (not before), because the comment explains the rendered configuration
   from that apply is what points the daemon at this exact log path, and
   the daemon treats a missing log file as a hard configuration error.
   This ordering dependency is subtle and correctly commented; worth
   preserving as an explicit sequencing rule if this component is ever
   touched during the fiber rebuild (it is not itself an HTTP surface, but
   its lifecycle is coupled to what the server publishes).

3. `cmd/sc-smb-agent/main.go`, `poll`. [none] Guards the "the supervised
   daemon died, restart it" branch on `agent.Last().Smbd !=
   smbagent.ActionStopped`, specifically to avoid treating a deliberately
   stopped daemon (SMB turned off, so no rendered configuration and thus
   no running daemon by design) as a crash to restart every polling
   interval. The comment documents this as the fix for a real defect
   ("tears down again every couple of seconds, an import and an account
   file rewrite included"). Correct guard, worth keeping as an explicit
   test case if this polling loop is rewritten.

4. `cmd/sc-smb-agent/main.go`, `flagEnv`. [none] Every flag has an
   environment-variable fallback, which is how a container configures
   this process without a command line; the flag itself still exists for
   interactive and test use. Standard, unremarkable pattern.

### Rebuild notes

- This binary is not itself an HTTP surface and mostly does not interact
  with the presentation layer directly, but its `Apply`/republish cycle is
  the receiving end of `server`'s `smbSink`/`PublishSMB` wiring (see the
  `server` section above). The ordering and "never fail the caller"
  contract on the `server` side and the "log file must exist before the
  daemon needs it, in this order" contract on this side are two halves of
  one cross-process protocol; if either side is rewritten independently
  in the fiber rebuild, the ordering dependency (finding 2) needs to be
  re-verified rather than assumed to still hold.

## Cross-package observations

- Status-mapping exists in at least three independent places across the
  audited packages: `apierr.Map` (native REST), `dav.StatusOf` (WebDAV),
  and `ncwire.flowErr` (compat login flow), each translating a different
  error vocabulary but following the identical pattern (a switch on
  `errors.Is` against a fixed sentinel list). The survey's general note
  about "status-mapping done in more than one place" is concretely true
  at all three sites; the fiber rebuild should define one canonical
  error-class enumeration in the service layer and let each protocol's
  thin adapter turn a class into its own status code, rather than
  maintaining three independent full switches that must each be updated
  by hand whenever a new service-layer sentinel error is added.
- The pattern of a handler or a bootstrap command reaching directly into
  `acl.CreateGrant(ctx, st.State().SQL(), ...)` (or the equivalent
  `ListGrants`/`DeleteGrant`/`UpdateGrant`) appears in three independent
  places: `httpapi/handler/admin_grants.go`, `httpapi/handler/shares.go`
  (twice), and `cmd/stowcloud/serve.go`'s `grantEveryShare`. This is the
  clearest, most repeated instance of the survey's "acl mixes evaluation
  and SQL" finding manifesting as a presentation-layer problem as well as
  a service-layer one: three unrelated callers each construct a
  SQL-backed grant row by hand because no service-level "create a grant"
  method exists to call instead. This is the single strongest argument in
  this audit for prioritizing the survey's planned ACL/store split (step
  0.4 in the pre-core work order) before or alongside the fiber
  presentation rebuild, since the presentation layer cannot be cleanly
  rebuilt against a grant surface that does not yet exist.
- Three of the four confirmed non-durable-write defects (`server/setup.go`,
  `server/tls.go`, `server/probefile.go`) sit in the `server` package and
  were already named by the survey; this audit confirms all three by
  direct code reading and adds the observation that the TLS pair is worse
  than a single torn file, since it is two independent non-atomic writes
  whose halves can disagree after a crash. `cmd/stowcloud/failtrack.go`'s
  fourth non-durable write is a deliberate, low-stakes exception (a
  diagnostic counter) and should not be conflated with the other three
  when the persistence-layer primitive is rolled out.
- Three packages (`httpapi/mw`, `server`, `compat/nc`) each carry a
  documented history of a specific, real regression fixed by a specific
  line of code, stated in the comment beside the fix (session hash
  mismatch, DAV challenge loss, static-asset rule swallowing file
  prefixes, cross-origin WebDAV destination, wrong-method login mount,
  missing job-cancel DELETE alias, unmounted link-edit routes,
  fileid-vs-path thumbnail addressing). These comments are, collectively,
  an informal regression-test suite in prose form. The single highest-value,
  lowest-cost action this audit can recommend before the fiber rebuild
  begins is transcribing every one of these documented incidents into an
  explicit named test case (or a single "known regressions" spec
  document), since they represent real, already-paid-for lessons that a
  rewrite is otherwise likely to relearn one at a time.

## Documents required

The following rebuild spec documents are implied by this audit's findings
and are not yet covered by an existing document in `docs/refactor/`:

1. **Auth middleware spec** (from `httpapi/mw/auth.go`, `csrf.go`,
   `hostguard.go`, `trustedproxy.go`): the credential-resolution order
   (session cookie, then app password via Basic, then Bearer), the
   deliberate non-distinction between expired and forged credentials, the
   public-path/file-prefix/credential-flow three-way split, the CSRF
   origin-check and HostGuard empty-host-list coupling, and the three
   fail-closed proxy-trust rules, each with the specific historical
   regression it fixes named as a test case.
2. **WebDAV protocol spec** (from `dav/*`): PROPFIND/PROPPATCH body
   parsing rules, the If-header grammar and its weak/strong asymmetry
   against If-None-Match, the lock conflict matrix and the 423-vs-412
   status split, the upload-collection chunk-naming rule, and the fix for
   the two lock-table TOCTOU windows found in this audit.
3. **Error-mapping and status-class spec** (from `apierr/*`, `dav/method.go`,
   `ncwire/loginflow.go`): one canonical service-layer error-class
   enumeration and the per-protocol adapters that render it, replacing
   the three independent hand-written mappers found in this audit.
4. **Nextcloud compat surface spec** (from `compat/*`): an explicit,
   feature-by-feature statement of what is in scope for the fiber
   rebuild's compat phase (OCS only, or OCS plus WebDAV path layout plus
   chunking plus trash plus vendor properties), resolving the wiring gap
   this audit found before any of the unwired code is treated as settled
   behavior to carry forward.
5. **Login flow v2 spec** (from `compat/nc/login_flow.go`,
   `compat/ncwire/loginflow.go`): the two-token state machine, digest-only
   storage, and POST-only-behind-session-and-CSRF approval, as a
   standalone document since it is reusable security-critical design
   independent of the rest of the compat surface.
6. **Listener hot-swap spec** (from `server/supervisor.go`): the
   bind-new-before-touching-old sequencing and bounded background drain,
   as an explicit requirement for whatever mechanism replaces it under
   fiber's own app lifecycle.
7. **Non-durable write remediation list** (from `server/setup.go`,
   `server/tls.go`, `server/probefile.go`): confirms the survey's three
   findings and specifies the TLS cert/key pair must be written as one
   durable unit, not two independent atomic writes.
8. **Grant write surface spec**: a service-level "create/list/update/delete
   grant" surface that `httpapi/handler`, `cmd/stowcloud`, and any future
   caller use instead of constructing `acl.CreateGrant(ctx,
   st.State().SQL(), ...)` calls directly; this audit found the same
   bypass in three independent locations and it blocks a clean
   presentation-layer rebuild on its own.
9. **Known regressions test list**: a transcription of every documented
   past-incident comment found across `httpapi/mw`, `server`, and
   `compat/nc` into explicit named test cases, so the fiber rebuild does
   not relearn any of them.
