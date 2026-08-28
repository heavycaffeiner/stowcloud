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
