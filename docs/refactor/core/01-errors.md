# Core rebuild: errors

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (chiefly `errors.go` and the `mapVFSErr` function in
> `entry.go`) is referenced as a behavioral specification only. The new
> implementation is written completely new; nothing is copied.

## Purpose

One file, `core/errors.go`, holds every error the domain returns: the
sentinel set, the two typed errors that carry payload, and the crossing that
converts a VFS error into a domain sentinel. Nothing in it chooses an HTTP
status. The mapping to wire status happens exactly once, in the protocol
layer, where the caller's grants are known and the existence rule can be
applied to the response shape.

## Spec

### Sentinels

All sentinels are `errors.New` values compared with `errors.Is`. The set and
its meanings:

| Sentinel | Meaning |
| --- | --- |
| `ErrNotFound` | Missing, or outside every grant. The two are one answer by design: returning a denial tells a stranger the path exists. |
| `ErrDenied` | The caller may know the target exists but may not do this to it. To a caller with no grant over the target at all, denied and missing are the same error, `ErrNotFound`. |
| `ErrPrecondition` | A supplied validator failed strong comparison. Always wrapped by `PreconditionError`, which carries the current token. |
| `ErrConflict` | The operation conflicts with current state and no validator was supplied. Opens the conflict dialogue on the client. |
| `ErrExists` | Create against an existing name with no-clobber. |
| `ErrNotEmpty` | Directory delete without recursion. |
| `ErrCrossShare` | An operation that cannot span shares atomically; the message names which half completed. |
| `ErrNoSpace` | ENOSPC, or the configured free-space floor. |
| `ErrTrashDisabled` | Restore or purge against a share with trash off. A plain delete on such a share is not this error; it is simply a permanent delete. |
| `ErrLinkExpired` | Expired, revoked, or over the download cap. One error, because distinguishing them tells a stranger about the link. |
| `ErrQuotaExceeded` | A write the acting user's ledger cap refuses. |
| `ErrShareBroken` | The share the path names is registered but its backing directory is unavailable right now. Deliberately not `ErrNotFound`: the path the caller named is good and the disk under it is gone. |

### Typed errors

Two errors carry payload beyond a message. Both wrap their sentinel via
`Unwrap`, so `errors.Is` against the sentinel works everywhere.

```go
// ShareBrokenError names which share is broken and why, so the message a
// user gets says the folder rather than the request.
type ShareBrokenError struct {
    Share  string // the share's display name
    Reason string // the health surface's own token: "missing", "unreadable", "unavailable"
}

// PreconditionError carries the current weak token alongside
// ErrPrecondition, so a conflict-resolution UI can show it without a second
// round trip. Current is empty when the target does not exist.
type PreconditionError struct {
    Current string
}
```

The `Reason` vocabulary is shared with the health surface and with
`Share.BrokenReason` (03-share-registry.md). A screen and a probe asking the
same question get the same word back.

`IsPrecondition(err error) bool` is kept as a convenience for protocol
layers: `errors.Is(err, ErrPrecondition)`.

### The VFS crossing

`mapVFSErr` moves here from its current home in `entry.go`, because every
file in the package calls it and it is a property of the error taxonomy, not
of listings.

```go
func mapVFSErr(err error) error
```

The mapping table:

| VFS error | Core sentinel |
| --- | --- |
| `vfs.ErrNotFound` | `ErrNotFound` |
| `vfs.ErrDenied`, `vfs.ErrSymlinkDenied` | `ErrDenied` |
| `vfs.ErrExists` | `ErrExists` |
| `vfs.ErrNotEmpty` | `ErrNotEmpty` |
| `vfs.ErrNoSpace` | `ErrNoSpace` |
| `vfs.ErrCrossDevice` | `ErrCrossShare` |
| anything else | passed through unchanged |

The existence rule is applied in `Resolve`, so a VFS `ErrNotFound` reaching
this function is a real missing path, not a permission answer, and maps to
the same `ErrNotFound` the resolver returns. The symlink denial folds into
`ErrDenied` because which policy refused is not the caller's business.

The pass-through default is deliberate. An error the table does not name is
an infrastructure failure, and wrapping it in a domain sentinel would let a
protocol layer map it to a 4xx it did not earn.

### Wrapping helper

`errf(wrap error, format string, args ...any) error` produces
`fmt.Errorf(format+": %w", ...)`. It lives in `core.go` beside the other
small construction helpers, not here; this file holds the taxonomy, not the
formatting.

## Rationale

- **One taxonomy file.** The current tree keeps the sentinels in `errors.go`
  but the VFS crossing in `entry.go`; a reader auditing "what can this
  package return" reads two files. The rebuild puts both in one.
- **No status codes.** The rule that an unlistable path is 404 everywhere is
  a protocol rule. Keeping status out of the core is what lets WebDAV, the
  web API and the compat surface each map the same sentinel to their own
  wire shape.
- **Sentinels over error codes.** `errors.Is` composes with wrapping, costs
  nothing, and needs no registry. A numeric code scheme is a second taxonomy
  to keep in sync.

## Deliberate changes

- `mapVFSErr` moves from `entry.go` to `errors.go`. No behavioral change.
- No new sentinels. The set above is exactly the set the protocol layers
  already map.

## Tests

- Every sentinel is distinct: `errors.Is(a, b)` is false for every pair.
- `ShareBrokenError` and `PreconditionError` unwrap to their sentinels.
- `mapVFSErr` maps each named VFS error to its sentinel and passes an
  unnamed error through with identity preserved (`errors.Is` on the
  original).
- The error strings leak no host path and no share host directory. This is
  asserted for the typed errors, whose fields could otherwise carry one.
