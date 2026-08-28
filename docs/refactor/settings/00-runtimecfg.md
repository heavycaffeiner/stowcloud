# Settings 00: runtime configuration

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/runtimecfg` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch;
> nothing is copied.

## What runtimecfg is

The live shape of every stored setting: the `Values` struct, its
defaults and bounds, the boot-time loader, and the `Holder` the running
server reads. There is no configuration file; the database is the
configuration, and this package is how it becomes typed values.

Target `engine/service/settings/runtimecfg`. Imports: the state store's
settings aggregate, `infra/jail` (the hardening policy type),
`kit/netzone` (prefix parsing), the store's guard config. The `smb`
import goes away (below).

## The two validation moments

The design has two trust boundaries with different rules, and the split
is the requirement:

1. **Save time** (`Check`, `CheckListen`, `CheckHost`, `CheckCIDR`, the
   bounds): administrator input, validated strictly, refused with named
   findings. This is where a bad value is stopped.
2. **Boot time** (`Load`): a document that was already validated at
   save. Malformed stored values are **clamped or dropped with a logged
   warning, never refused**: a server that will not start over one
   stale field is a server the emergency door has to fix, and the
   emergency door edits this same document. Graceful degradation at
   boot is what keeps the repair tool reachable.

## Bounds

`Bound{Min, Max}` per numeric field, exported per field
(`BoundRatePerSec`, `BoundWatchHotSet`, ...) and as a table
(`Bounds()`) the settings screen renders. The client never compiles its
own copy: a client carrying its own bounds offers values the server
refuses.

## The Holder

`Holder` is the live value: RWMutex'd, `Set` releasing the lock
**before** the update callback runs (the callback-under-lock deadlock
is the named risk), readers taking snapshots.

## Section application service

Phase 3 adds the root `engine/service/settings` package over `runtimecfg` and
`check`. It owns section schemas, secret extraction and running-versus-stored
outcomes:

```go
type Lifecycle interface {
    SwapListener(ctx context.Context, addr string) error
    RequestRestart()
    ActiveWork(ctx context.Context) (uploads, jobs int, err error)
}

type ApplyOutcome struct {
    Stored, Applied, RestartRequired bool
    Findings []check.Finding
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error)
func (s *Service) ApplySection(ctx context.Context, actor int64, section string, patch SectionPatch, force bool) (ApplyOutcome, error)
```

Known section schemas identify secret fields (initially OIDC client secret),
seal them through auth, remove plaintext from the JSON settings document, run
the shared checker, merge one section and apply its live effects. Unknown
sections refuse. Listener swap/restart are neutral callbacks; no HTTP status or
Fiber type enters the service. Stored and applied are reported separately.

## Deliberate changes

1. **The SMB render probe moves to the smb phase as a dry-validate
   entry point** (`smb.Validate(cfg) error`), and this package calls
   it. Today `runtimecfg.CheckSMBRender` and `settingscheck.checkSMB`
   both import the renderer and probe with **different inline
   defaults** that can drift (`"WORKGROUP"`/`"scsvc"` literals versus
   the named constants). One entry point, owned by the package that
   owns the renderer, called by both checkers, with the defaults in
   exactly one place. (`../smb/00-config-rendering.md` owns the entry
   point.)
2. **The typed read helpers collapse.** The five near-identical
   `readInt`/`readUint`/`readString`/`readStrings`/`readBool` helpers
   over `map[string]any` disappear behind the store's typed settings
   schema; the aggregate hands back typed sections, and this package
   stops spelunking maps.
3. **Network settings include the full host-role schema** (Phase 3 amendment):
   `AppHosts []string`, `ContentHosts []string`, `AllowedOrigins []string` and
   `CompatCanonicalURL string`. App/content hosts use host-only syntax and
   must be disjoint. Allowed origins are absolute HTTPS origins with no path,
   query, fragment or userinfo and govern explicitly CORS-readable public
   compatibility responses only, never credential trust. The compat canonical
   URL must be one of the configured app origins and is the fallback used when
   a request origin is unavailable. Content-host-dependent capabilities remain
   unavailable when their list is empty. Boot-time malformed/duplicate entries
   are dropped with warnings; save time refuses overlap or a canonical URL
   outside the declared app hosts.
4. **Section application and secret extraction move into a settings service**
   (presentation audit handler findings 5 and 6). HTTP no longer switches on
   section names or knows which fields are credentials.

Everything else, including every bound value and the two-moment rule,
is behavior-preserving.

## Tests

- Save-time: each checker refuses its documented bad shapes (a
  malformed CIDR, a host with a scheme, a listen string without a
  port, an out-of-bounds numeric).
- Boot-time: a malformed stored value loads as the default with a
  warning, never an error (plant garbage in the document).
- Zero fields fall back to defaults.
- The Holder: concurrent readers under a writer (race detector); the
  update callback runs outside the lock (deadlock fixture: a callback
  that reads the holder).
- The bounds table covers every numeric field (reflection test: no
  field without a bound).
- Content hosts round-trip; app/content overlap refuses at save; boot drops a
  malformed or overlapping stored content host without refusing startup.
- Allowed-origin and canonical-URL shape/containment vectors, with no CORS
  value ever becoming an admitted Host, proxy or login-flow origin.
- ApplySection: blocking finding writes nothing; warning writes; secret
  plaintext is absent from settings JSON; swap failure reports stored but not
  applied; restart-required/force and active-work outcomes.
