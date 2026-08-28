# Settings 01: the settings checker

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/settingscheck` is referenced as a behavioral specification
> only. The new implementation is written completely from scratch;
> nothing is copied.

## What settingscheck is

The save-time referee: a proposed settings change comes in as a section,
and the checker answers with findings. Some findings block the save,
some are warnings the administrator saves through. The checker runs the
same probes at the emergency door and the ordinary settings screen, so
the two doors cannot disagree about what is saveable.

Target `engine/service/settings/check`. It imports `runtimecfg` (the
bounds), the smb dry-validate entry point (00's change), and the host
probes it owns. **It does not import `apierr` or anything
presentation.**

## The finding vocabulary (the apierr fix)

The audit's concrete finding: `Refused` builds an
`apierr.RequestError` with `http.StatusUnprocessableEntity` baked in,
and `stringList` does the same inline. A service package choosing an
HTTP status is the layering violation the whole tree's error rule
exists to prevent.

The rebuild's shape:

```go
type Finding struct {
    Section, Field string
    ReasonKey      string   // the i18n key; the client renders it
    Args           []string
    Blocking       bool
}

func Section(in Input) []Finding
func Blocked(findings []Finding) bool
var ErrRefused = errors.New(...)   // wraps the blocking findings; no status anywhere
func Refused(findings []Finding) error // ErrRefused carrying the findings
```

The presentation layer maps `ErrRefused` to 422 exactly once, in its
own error table (phase 3's `http/02-error-mapping.md`), and renders the
findings from the error's payload. The reason keys and argument shapes
are unchanged: the client's rendering contract survives, only the
transport shape moves out of this package.

## The probes

Carried whole, with their verified properties:

- **Homes writability** (`checkHomes`/`probeWritable`): a real temp
  file, close error checked and preferred over the remove error,
  always cleaned up. A side-effecting probe that leaves nothing
  behind.
- **Watch capacity** (`checkWatch`): reads
  `/proc/sys/fs/inotify/max_user_watches` as host-boundary input; a
  read failure or garbage **skips the check** rather than blocking a
  save, because a missing proc file is not the administrator's
  mistake.
- **SMB render**: through the one dry-validate entry point (00's
  change), not a local copy of the renderer's defaults.
- The host-list self-lockout probe: a host list omitting the caller's
  own host saves **with a warning**, not a refusal; the administrator
  may be intentionally moving hosts.

## Deliberate changes

1. **Domain findings replace wire errors** (above; the audit's
   misplacement finding 1).
2. **The SMB probe goes through the shared entry point** (audit
   finding 2; the drift-prone duplicate defaults die).

The probe behaviors, the blocking/warning split per finding, and the
reason keys are behavior-preserving.

## Tests

- Every section's checkers produce their documented findings for the
  documented bad inputs; the blocking flags match.
- `Refused` carries findings and no HTTP anything (compile-time: the
  package does not import net/http or apierr; assert in the layer
  gate).
- The homes probe cleans up after itself, including on close failure.
- A missing or garbled proc file skips the watch check.
- The self-lockout warning saves; the finding says so.
- The SMB probe refuses what the renderer refuses (parity test through
  the entry point).
