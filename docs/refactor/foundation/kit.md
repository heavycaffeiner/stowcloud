# Foundation kit

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/{num,clock,task,secret,limits,unixprobe}` and
> `go/internal/smb/bind.go` is referenced as a behavioral specification only.
> The new implementation is written completely new; nothing is copied.

## Purpose

The foundation tier: six small packages any layer may import, grouped under
`engine/kit/`. Each is a leaf (imports stdlib only) and each survived its
audit clean (`audit/foundation-persistence.md`, sections num through
unixprobe), so this document is short: it fixes the layout, restates each
contract, and specifies the two changes the survey ordered (the `limits`
regrouping and the `netzone` extraction from `smb/bind.go`).

New tree:

```
engine/kit/
  num/       integer narrowing
  clock/     injectable time
  task/      the one goroutine spawn
  secret/    zeroizable credential container
  limits/    every bound, grouped by layer
  netzone/   private-network classification (extracted from smb)
```

`unixprobe` stays where the build gate wants it (a compile-only probe; the
rebuild keeps it as `engine/kit/unixprobe` for uniformity). The grouping is
a directory move; each package keeps its own name and import identity, and
nothing merges.

## Spec: num

One exported function and its error pair.

```go
type Integer interface {
    ~int | ~int8 | ~int16 | ~int32 | ~int64 |
        ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

var ErrNarrow = errors.New(...)

type RangeError struct{ Value, From, To string } // Is(ErrNarrow) == true

func Narrow[To, From Integer](v From) (To, error)
```

`Narrow` converts between integer widths and reports the value that did not
fit rather than truncating it. The check is the round trip plus a sign
comparison: the round trip catches a value too wide for the target, the
sign comparison catches a negative value and an unsigned target converting
back to the same bits. `RangeError` carries the value, because a truncation
reported without the number is a bug report nobody can act on.

This is the only legal integer narrowing in the tree; the gate rejects a
hand-written conversion between differently sized types elsewhere.

## Spec: clock

```go
type Clock interface {
    Now() time.Time                  // wall clock; for anything stored or sent
    Since(t time.Time) time.Duration // monotonic elapsed; wall subtraction moves under NTP
    Nanos() int64                    // Now as ns since epoch, clamped at zero
}

func System() Clock
func Fixed(t time.Time) Clock
```

The only place in the tree that calls `time.Now`. `Nanos` clamps a wall
clock behind the epoch to zero and warns once per clock instance rather
than aborting: an RTC that lost its battery is an ordinary state. `Fixed`
holds time still for tests and measures `Since` against its own instant.

## Spec: task

```go
func Go(ctx context.Context, name string, fn func())
func Recover(ctx context.Context, name string) // deferred, never called directly
```

The only goroutine spawn in the tree; a bare `go` statement anywhere else
fails the gate. `Go` installs a recover that logs the panic with its stack
and fails that unit of work; the process survives. `Recover` exists
separately for goroutines this package cannot start: the HTTP server's own
handler goroutines. The context feeds the logger (request id beside the
stack), never cancellation.

The fiber note for phase 3: fasthttp starts its own handler goroutines the
same way `net/http` does, so the presentation layer's panic middleware
defers `Recover` exactly as the current stack does. The contract does not
change.

## Spec: secret

```go
type Secret struct{ /* unexported []byte */ }

func New(b []byte) Secret          // takes ownership; caller must not reuse b
func (Secret) String() string      // "[redacted]"
func (Secret) GoString() string    // "[redacted]"; %#v ignores String
func (Secret) MarshalJSON() ([]byte, error) // redacts
func (s Secret) Equal(other Secret) bool    // constant time
func (s Secret) Len() int
func (s Secret) Reveal() []byte    // the single accessor; slice aliases the secret
func (s *Secret) Destroy()         // zeroes the owned buffer
```

The type every credential, key and token is carried in; never a string,
which is immutable and can never be zeroed. The contract buys exactly
three things and claims no fourth: it cannot be printed by accident,
cannot be serialized by accident, and its owner can zero the one buffer it
holds. It cannot zero a copy the garbage collector made; that gap is
accepted and documented, not closed. `Reveal` being the single accessor is
what makes the secret-tracking analyser's job finite.

## Spec: limits

Every bound in the tree, as named constants, plus the one refusal shape:

```go
var ErrTooLarge = errors.New(...)

type Exceeded struct{ Limit string; Bound, Got int64 } // Is(ErrTooLarge) == true

func Exceed(limit string, bound, got int64) error
```

A bound is a constant here or it is a magic number somewhere else, and a
magic number is one a caller can widen without anyone seeing the diff. The
premise: the directories this product serves are written by other
programs, so no input's size is ours to assume.

Every constant carries over with its value unchanged. The rebuild
reorganizes the file by consuming layer, which is the survey's one order
for this package; the sections become:

1. **Protocol bounds** (presentation): request bodies, XML scanner, WebDAV
   scanner and If-header bounds, batch paths, archive listing and packing,
   concurrent requests.
2. **Service bounds**: upload (interval runs, reserved bytes, chunk floor,
   session TTL, spooled names), search (results, concurrency and deadlines
   per storage class, walk depth, query bytes, corpus scan), OIDC (response
   bytes, timeouts, JWKS, token bytes, skew, TTLs), preview worker wire
   message.
3. **Domain and persistence bounds**: path components and bytes, name
   bytes, buffered directory entries, journal rows per account, DAV locks
   and dead properties per resource or user.

A section is a comment heading, not a package split: one registry of
bounds is the point, and a split would recreate the magic-number problem
across package boundaries.

Where a bound's refusal status code is named in a comment today ("refuses
with 413"), the rebuilt comment keeps the status: it is the one place the
presentation layer's mapping is discoverable from the bound itself.

## Spec: netzone (extracted)

`smb/bind.go` holds a general private-network classifier that
`httpapi/mw` and `emergency` also import; nothing about it is SMB
(`audit/service.md`, smb section). It becomes `kit/netzone`:

```go
func IsPrivate(ip netip.Addr) bool
func EnclosingPrivateRange(ip netip.Addr) string
func PrivateCIDRs() []string
func ParseAddrSpec(spec string) (netip.Addr, error)
```

Behavior carries over unchanged: the classifier answers whether an address
is one this project treats as a LAN, names the enclosing private range for
diagnostics, and parses the operator's address spellings. The SMB config
renderer becomes a consumer like the other two; the bind-rule enforcement
(what Samba is told to bind) stays in the smb package, which owns that
policy.

## Rationale

- **Grouping without merging.** Six one-concept packages under one
  directory reads as one tier and imports as six names. Merging them into
  one `kit` package would put `Secret` beside `Narrow` in one namespace
  for no caller's benefit.
- **No new surface.** Every audit section for these packages says clean.
  The rebuild resists the temptation to "improve" a clean leaf; the only
  changes are the two the survey ordered.

## Deliberate changes

1. Directory move to `engine/kit/*`. Import paths change; contracts do
   not.
2. `limits` regrouped by consuming layer (comment sections only, values
   untouched).
3. `netzone` extracted from `smb/bind.go`, ending the `httpapi/mw` and
   `emergency` imports of an SMB package (survey violation list; audit
   service.md smb section).
4. Nothing else. In particular `secret.Secret` does not grow key-derivation
   or encoding helpers, and `limits` does not become configurable;
   configurability stays with `runtimecfg` for exactly the bounds that are
   settings today.

## Tests

- num: the narrowing table (wide-to-narrow refusals, negative-to-unsigned
  refusals, identity conversions, every pair carrying the offending value
  in `RangeError`); `errors.Is(err, ErrNarrow)`.
- clock: `Fixed` holds `Now` and measures `Since` against it; `Nanos`
  clamps a pre-epoch instant to zero and warns once, not per call.
- task: a panicking fn is recovered, logged with stack and name, and the
  process survives; `Recover` behaves identically when deferred directly.
- secret: `%v`, `%s`, `%q`, `%x`, `%#v` and JSON all render redacted;
  `Equal` is constant-time equal/unequal; `Destroy` zeroes the buffer and
  a later `Reveal` returns nil; `New` ownership (mutating the caller's
  slice after `New` is the caller's bug, documented not tested).
- limits: `Exceed` wraps into `Exceeded`, `errors.Is(err, ErrTooLarge)`;
  the constants are compile-time facts and get no tests.
- netzone: the classifier table (RFC 1918 ranges, loopback, link-local,
  ULA, public addresses), `ParseAddrSpec` accepting the operator
  spellings and refusing garbage.
