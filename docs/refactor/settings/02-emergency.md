# Settings 02: the emergency door

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/emergency` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch;
> nothing is copied.

## Why it exists

There is no configuration file. Every setting lives in the database and
is edited from the web interface, which is served by the engine those
settings configure. A stored value that stops the engine coming up
therefore takes the repair tool down with it, and the only remaining
fix would be a SQL client on the volume. The emergency door is the
second way in: it reads and writes the same settings document through
the same checker, and **it depends on nothing the engine owns**. No
core, no shares, no upload engine, no watcher. The store, the auth
service and a listener: the smallest set that can authenticate an
administrator and commit a change.

## Where it lives (the re-homing)

The audit's finding is blunt: this is not a service package with a
stray import, it is a complete presentation surface (a mux, five
routes, cookies, origin checks, JSON transport) living in the service
tree. The rebuild re-homes it as **presentation wiring**:

```
engine/http/emergency/     the handler: routes, gate, transport
```

built in this phase (its invariants are settings invariants, and the
engine must be able to mount it long before phase 3 exists), consumed
by phase 3's assembly as one more mounted handler. What stays
service-side is what it calls: `settingscheck`, `runtimecfg`, auth,
the store. The fiber migration does not apply to it: the door stays
on `net/http`, because it must keep working when everything else is
too broken to configure, and a dependency shared with the broken half
is a shared failure.

## The three constraints (unchanged, load-bearing)

This page edits the host guard and the share list; it is the most
valuable target in the product. In every mode:

1. **Network scope.** A request is admitted only from a private peer
   address (`kit/netzone`, which replaced `smb.IsPrivate`). Everything
   else gets **404, never 403**: the page does not confirm its
   existence to the outside.
2. **Authentication.** The administrator's real credentials through the
   same auth service, the same rate limiter, the same second factor.
   No token shortcut, no recovery bypass.
3. **Audit.** Every login and every write records under its own event
   names, so the log answers "was the safe-mode door used".

## Routes

```
GET   /emergency/api/state
POST  /emergency/api/login
GET   /emergency/api/settings
PATCH /emergency/api/settings/{section}
POST  /emergency/api/restart
```

Plus the page itself. Write-then-restart is the flow: a write commits
through the checker, and the restart is a separate explicit action.

The content-host setting is editable here with the network section. The door
itself is never mounted on a content host; it remains on the listener address
under the app/first-boot host behavior, and public peers still receive 404.

## The deliberate duplications

Two things look like duplications of the main stack and are kept
separate **on purpose**, each with its reason stated where it lives:

- **The origin check** is a direct `Origin`-versus-`Host` comparison,
  not the main stack's HMAC CSRF token: the app-host list that CSRF
  derives from is one of the things this door exists to repair. A
  door that trusts the broken value cannot fix it.
- **The JSON transport** (decode with a 1 MiB limit, the error
  writer) is local: depending on the main stack's handler kit would
  couple the door to the half being repaired.

What was **not** duplicated and must stay shared: the login path.
`auth.Login` serves this door, with its limiter, its decoy and its
second factor; a hand-rolled password check here would be a second
authentication surface with none of the defences.

## Deliberate changes

1. **Re-homed to `engine/http/emergency`** (the audit's misplacement).
2. **`kit/netzone` replaces the `smb.IsPrivate` import** (the
   classifier moved in phase 0).
3. **The unused `Deps.Log` field is dropped** (the audit's one-line
   smell).
4. The apierr dependency disappears with settingscheck's finding
   change (01): the door renders findings, not wire errors.

The 404-not-403 gate, the same-auth rule, the per-event audit names,
the origin check's independence and the transport's independence are
behavior-preserving requirements.

## Tests

- The gate: a public peer gets 404 on every route including the page;
  a private peer gets the page.
- Login: wrong credentials refuse through the real limiter (attempt
  counting observable); an enrolled account requires its factor; a
  session cookie is set and honored.
- A cross-site write is refused (the origin check); same-origin
  passes.
- A write goes through the checker: a blocking finding refuses, a
  warning saves; both audit under the door's event names.
- Restart: recorded, and only after an authenticated session.
- A degraded server fronts the door (the redirect wrapper serves the
  emergency page when the engine is down).
- The body limit refuses an oversized write.

## What was built, and what measuring it changed

The door is `engine/http/emergency`, and it holds every constraint
above. Two things the plan did not say are worth recording.

**`Deps` takes interfaces, not the auth service and the state DB.**
What belongs to this package is the policy wrapped around the call:
administrator only, its own audit events, no app-password path, the
checker consulted before the store. Depending on the concrete service
would have meant re-testing password verification here, which the auth
package already tests, while leaving the policy asserted only
indirectly. The interfaces name exactly the five auth calls and the two
store calls the door makes, so adding a sixth is a visible change.

**The session cookie is `__Host-sc_sid`.** This is the product's own
cookie rather than a second one, so a repaired server finds the operator
already signed in. The prefix is a browser-enforced rule and not a
naming convention: a cookie carrying it is discarded unless it is
`Secure`, has `Path=/` and names no `Domain`. A change that drops one of
the three does not weaken the cookie, it stops the cookie existing, and
the failure is silent. The constant says so, and a test asserts all
three on the emitted header.

### The mutations, and the two holes they found

Fourteen mutations were applied to the finished package, each one a
plausible regression, and every one of them fails at least one test:
404 turned into 403, the network gate opened, the origin check removed,
the administrator check dropped at the login and again behind the
session, blocking findings ignored, the unknown-section refusal removed,
the body limit lifted, `Secure` and `SameSite` dropped from the cookie,
the audit event renamed to the ordinary `auth.login`, the cookie's hex
decode skipped, and the session lookup's error ignored.

Two of them passed on the first run, which means two invariants were
unasserted:

- **The lockout rule.** Switching `LockoutWarns` to `LockoutBlocks`
  changed nothing any test could see. That is the single behaviour this
  door exists for. Somebody arrives here because the app-host list no
  longer contains the address they can reach the server on, and the list
  they save still will not contain it, because that host is the broken
  one. The settings screen refuses that save; here it has to go through.
  There are now three tests: the repair is saved and warns while naming
  the omitted host, the same input blocks under `LockoutBlocks` so the
  two modes are demonstrably different, and a list that does contain the
  current host warns about nothing.
- **An unrecognised session.** Ignoring the lookup error let a
  well-formed cookie naming no live session through to every
  authenticated route.

Both are covered now, and both mutations fail.

### Prose

`freshscan` found nineteen comment lines carried over from
`internal/emergency`, which is the rule that gate exists to enforce and
the second time it has caught this. All nineteen are restated.
