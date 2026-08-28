# SMB 00: configuration rendering

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/smb` (`render.go`, `safe.go`, `errors.go`; `bind.go`
> already left for `kit/netzone` in phase 0) is referenced as a
> behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## What this package is

The `smb.conf` renderer and its refusal rules: given a config and a
share list, produce the bytes `smbd` reads, or refuse. Target
`engine/service/smb`. After the netzone split this package is what its
name says, and nothing but SMB imports it.

## Feature inventory

Every exported symbol of `internal/smb`, `internal/smbagent` and
`internal/smbpublish`, and the document that owns it. An omission from
this table is a documented defect, not a silent loss.

| Old surface | Document |
| --- | --- |
| `Render`, `PasswdEntries`, `Config`, `ShareDef`, `User` | 00 |
| `ErrUnsafeValue`/`UnsafeError`, `ErrBindRefused`/`BindError` (the pinned-interface refusal in `Render`) | 00 |
| `IsPrivate`, `EnclosingPrivateRange`, `PrivateCIDRs`, `ParseAddrSpec` | gone to `kit/netzone` (phase 0) |
| `smbpublish.Publish`, `Deps`, `Accounts` | 01 |
| `Do`/`Apply`/`Status`, `ErrNotListening`, `ErrProtocol` | 01 |
| `Request`, `Report`, `SmbdAction`, `FailedReport`, `DefaultTimeout`, `DefaultSocket` | 01 |
| `Serve`, `ServeInBackground` | 01 |
| `Entry`, `ParseRendered`, `ValidName`, `Collisions`, `MissingGroups` | 02 |
| `Rebuild`, `WritePasswd`, `Import`, `PassdbNames`, `Prune`, `MissingPassdb` | 02 |
| `Device`, `Scope`, `Compute`, `Devices`, `Detect` | 03 |
| `Policy`, `ReadPolicy`, `Candidate`, `Section`/`Sections`, `NetbiosWanted`, `BoundInterfaces` | 03 |
| `Mode`/`ModeKind`, `DetectMode`, `Smbd`, `NewSmbd`, `NeedsRestart` | 03 |
| `Paths`, `DefaultPaths`, `Agent`, `NewAgent`, `LogReport` | 03 |

## Refuse, never escape

The format has no reliable escape, so the strategy is refusal
(verified clean in the audit and normative here): any interpolated
field containing `\n` `\r` NUL `[` `]`, the comment markers `;` `#`, a
trailing backslash, `%`, or any control character refuses with a typed
`UnsafeError` naming the field. Nothing attempts to escape; nothing
bypasses the checks on the way to `Render`. `checkPasswdName`
additionally refuses `:` in account names, the passwd separator.

```go
func Render(cfg Config, shares []ShareDef) ([]byte, Result, error)
func Validate(cfg Config) error   // the new dry-validate entry point
```

**`Validate` is new** (the settings phase's requirement): the one
dry-run both `runtimecfg` and `settingscheck` call, with the default
workgroup and service-user values defined here and nowhere else. Today
two packages probe by rendering with their own inline defaults that
can drift; this ends that.

## The whole-batch rule, made survivable

Today one bad account name fails the whole render, and with it SMB for
everyone (the audit's cross-package finding 3). The root fix is auth's
creation-time rule (`../auth/05-username-policy.md`): names that would
fail here stop existing. This package's behavior then layers:

- **Refusal stays whole-batch for config fields** (workgroup, server
  name, share names, paths): these are operator input, few in number,
  and a config with one bad field is a config to fix, not to partially
  apply.
- **Account lists render per-entry**: an account name that fails the
  check is dropped from `ValidUsers`/`ReadList`/`WriteList` with a
  warning naming it, rather than failing the file. This is the
  deliberate change: pre-rule accounts may still carry legacy names,
  and one legacy name must degrade one account's SMB access, not
  everyone's. The dropped name is reported in the render result so
  the publisher can surface it.

## Deliberate changes

1. **`Validate` is exported** (the settings phase's shared entry
   point; defaults live here only).
2. **Account names degrade per-entry** (above). Config fields keep the
   whole-batch refusal.
3. `bind.go` is already gone (phase 0's netzone).

The refusal character set, the typed errors and the rendered format
are behavior-preserving; the renderer is fuzzed as before.

## Tests

- Fuzz: `Render` never emits a value that re-parses as a directive or
  section it was not given (the injection property), and never panics.
- Each refused character class refuses, named field by field.
- A commented directive is not a directive (parse-side fixture).
- `Validate` refuses what `Render` refuses (parity), and both checkers
  reach it (compile-time: neither imports the renderer's internals).
- Per-entry degradation: a share list with one bad account name
  renders, drops the name, reports it; a bad share name still refuses
  the batch.
