# Phase 5: HTTP and API

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-8-http-and-api.md`.

## Scope

The largest phase: the twelve-step chain, the error envelope, the REST surface,
the static frontend, the WebSocket channel, and the server assembly.

Depends on Phases 3 and 4. Blocks Phases 7, 10, 11 and 12.

## Milestones

- **5a**: `chain.go` and `mw/`: the twelve steps, the order test, the proxy
  fuzz target.
- **5b**: `apierr/`: the envelope, the mapper, the existence-rule test.
- **5c**: `route/`: the table, startup validation, scope wiring.
- **5d**: `handler/`: browse, session, settings, trash, link, share, recent,
  setup.
- **5e**: `internal/server`: TLS, config, wiring, shutdown, health, the D13 test.
- **5f**: `static/` and `ws/`.

5e is independent of 5d.

## Traps

- **The chain's order is a cost argument.** RateLimit is step 5 and Auth is step
  7 so a flood is refused before it costs an Argon2 invocation. Moving Auth
  above RateLimit compiles, passes every test, and hands an attacker the
  container's memory budget.
- **`X-Forwarded-For` is walked right to left.** An unparseable hop aborts the
  walk. An all-trusted list yields no client address. A request with no
  determinable source is never treated as coming from a trusted proxy whatever
  the configuration says. Fuzz the hop parser; it accepts four shapes.
- **One mapper names a status for this surface only.** TUS, WebDAV and the
  compat mounts have their own status vocabularies and that is correct, not a
  violation of the rule.
- **`ReadHeaderTimeout`, `IdleTimeout` and `MaxHeaderBytes` are set;
  `ReadTimeout` and `WriteTimeout` are zero on purpose** because uploads and
  downloads stream. The test asserts both halves, so a later edit setting them
  has to change the test.
- **A route with no declared scope is refused at startup.** Default-deny is what
  makes step 9 a layer rather than something each handler remembers.
- **The WebSocket is bidirectional and stateful**: `sub` and `unsub` frames
  carrying path lists, a READ recheck at subscribe *and* at send, a 200 ms
  debounce coalescing per connection and path, refcounted registration into
  Phase 1's sticky hot set, and revocation at both user and session grain. The
  frame-layer dependency decision is **reopened**, because the argument for
  hand-rolling it rested on a premise that was wrong.
- **Dropping the watch refcount fails silently** and only for the directory the
  user is currently looking at.
- **Six D5 limits and the whole proxy trust boundary are admin-mutable.** A D5
  constant is the compiled-in default and the outer bound. An administrator
  moves a value within it; no request path moves any of them.
- **A malformed origin is refused at save and dropped with a warning at boot.**
  The two paths disagree on purpose: refusing at boot makes a server unbootable
  over a typo committed weeks earlier.
- **The setup token is single use *and* fifteen minutes**, and the gate closes
  permanently once an administrator exists. There is no environment variable
  for the first password.
- **`//go:embed` has a real dependency edge**, so the stale-frontend hazard has
  no counterpart. Prove it once anyway.
- **No file over 1,500 lines.** That is F8, and this is the phase where it would
  otherwise be violated.

## Done when

- The gate is green, including `-race`.
- The chain-order test passes against recording stubs.
- The existence-rule table test covers every domain error, and a path outside a
  grant is byte-identical to a missing one.
- The server-timeout test asserts the set fields and the two deliberate zeros.
- F8 and F9 are closed.
