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
