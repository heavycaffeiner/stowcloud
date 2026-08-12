# Defensive coding standard - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Twenty rules the Go tree is held to, each with the mechanism that enforces it.
Four of them (D3, D4, D5, D10) are load-bearing: something in the security model
is false if they are dropped. The other sixteen are hygiene with teeth. A rule
whose only enforcement is this document is not on the list.

## 2. Background & Motivation

The Rust tree's safety came from three places: the borrow checker, `Result`
being `#[must_use]`, and `Drop`. Go has none of them. Nothing in Go stops an
ignored error, an unclosed descriptor, a nil map write, or a goroutine that
outlives its request. The compensation has to be mechanical, because the
alternative is remembering, and
[`stowcloud-0-motivation-and-findings.md`](stowcloud-0-motivation-and-findings.md)
§4.3 is what remembering produced over 99,614 lines of a language that was
helping.

The rules below are chosen against that list rather than from a style guide.
Every finding in document 0 maps to one, and every rule that maps to no finding
is there because Go's defaults are actively unsafe in that spot (D13 is the
clearest: `http.Server`'s zero timeouts).

**Most of them are not new positions.** They are
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5's inherited
stances, turned into something a compiler or a gate can check, because a stance
that only a careful author enforces is a stance a tired author does not:

| Stance | Becomes |
|---|---|
| S1, by construction not by validation | D10's three unconvertible path types, and the `.`/`..` rejection in [`3`](stowcloud-3-vfs-and-paths.md) §4.3.6 |
| S2, existence is never revealed | D15's `MessageKey`, and the single mapper in [`8`](stowcloud-8-http-and-api.md) §4.3.2 that is the only function choosing a status |
| S3, a downgrade is loud | D3 |
| S5, measured not asserted | D5's named limits, each with a test that the limit is what refuses rather than that a large input happens to fail |
| S6, the neighbours' access survives us | D1, which is why F7's three discarded results are a rule and not a nit |
| S7, the server does not decide how a client renders | D15 |
| S10, a parameter is chosen against the whole budget | D5's concurrency and in-flight bounds, which exist for the same reason 48 MiB was chosen over 64 |
| S11, fail closed on security and open on data | D3 for the first half; the size guard in [`5`](stowcloud-5-store-and-schema.md) §4.3.4 for the second |
| S17, a claim that cannot be executed is a comment | D16's fuzz targets, and the jail proof in [`4`](stowcloud-4-jail-and-hardening.md) §4.3.6 |

The four rules §4.3 calls load-bearing are load-bearing because a stance
becomes false without them, not because they are harder to implement.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Every rule enforced by a compiler error, a lint in the gate, or a test.
- [ ] The enforcement configured in Phase 0, before the code it governs exists.
- [ ] Exceptions annotated at the line, with a reason, and countable.
- [ ] The four load-bearing rules identified as such, so a future trade-off
      discussion knows which three are cheap to relax and which are not.

### 3.2 Non-Goals

- [ ] Formatting and naming. `gofmt` and the standard review conventions cover
      it, and nothing here has an opinion `gofmt` does not already have.
- [ ] A coverage target. Coverage measures which lines ran, and every finding in
      document 0 is in a line that runs.
- [ ] Applying these to `web/`. The frontend has its own gates.
- [ ] A custom linter framework. Rules that need one are written as `go vet`
      analysers, which is the standard mechanism, or as a grep in the gate when
      that is honestly enough.

## 4. Technical Design

### 4.1 Architecture Overview

Three enforcement tiers, and which tier a rule lands in is a deliberate choice
rather than a matter of convenience:

```
tier 1  the type system      cannot be violated, no configuration to disable
        D5 D10 D12 D15 D20   a violation does not compile

tier 2  the gate             verify.sh fails; a bypass is a visible diff
        D1 D2 D6 D7 D8 D9 D11 D13 D14 D17 D18 D19

tier 3  a required test      the rule is about runtime behaviour
        D3 D4 D5 D16
```

Nothing is in tier 3 that could be in tier 2, and nothing in tier 2 that could
be in tier 1. D5 is the one rule in two tiers and is listed in both: its limits
are constants, which is tier 1 for the value, checked by tests that the limit
is what refuses, which is tier 3 for the behaviour. A constant nothing asserts
against is a number, not a bound.

### 4.2 Data Model Changes

None.

### 4.3 Core Logic

#### The four that are load-bearing

**D3. Hardening is a policy, not an outcome.**

```toml
# sc.toml
hardening = "required"   # "required" | "preferred" | "off"
```

`required` is the default in the shipped image; `preferred` is the default for a
bare-metal install where the operator may be on an older kernel; `off` exists so
that "I know, and I accept it" is expressible without editing code.

Under `required`, every step of the sequence in
[`stowcloud-4-jail-and-hardening.md`](stowcloud-4-jail-and-hardening.md) that
cannot be applied is a refusal to start, printed to stderr naming the step and
the errno, and exit code 78 (`EX_CONFIG`). Under `preferred` it is a warning and
the status is reported by `GET /api/health` as a named degradation rather than
only in a startup log. Under `off` nothing is attempted and the health report
says so.

This closes F2. It is load-bearing because every other statement about the
process sandbox is conditional on it: without a required mode, the sandbox is a
thing that usually happens.

**D4. A syscall filter verifies its ABI before it trusts a number.**

Every assembled BPF program starts with, in this order: load `arch` (offset 4),
compare against the compiled-in `AUDIT_ARCH` for this build, kill on mismatch,
load `nr` (offset 0), and on `x86_64` reject the x32 range before any allow
comparison. Numbers come from `unix.SYS_*`. An architecture with no verified
mapping in the table is a refusal to install, which under D3's `required` is a
refusal to start.

This closes F1 and F3. Load-bearing for the reason the preview jail's own
documentation gives: a filter that waves through a number because it read it
under the wrong ABI is worse than no filter, because it is believed.

**D5. Every input is bounded, and the bound is a named constant.**

One file, `internal/limits`, holds every one of them. Nothing takes a magic
number, and nothing takes a limit as a parameter that a caller could widen.

| Limit | Value | Refuses with |
|---|---|---|
| request body, general | 1 MiB | 413 |
| request body, XML | 256 KiB | 413 |
| XML elements | 10,000 | 400 |
| XML depth | 64 | 400 |
| XML element name | 256 B | 400 |
| path components | 256 | 400 |
| path total length | 4 KiB | 400 |
| name component | 255 B | 400 |
| directory entries, buffered read | 100,000 | `ErrTooLarge` |
| concurrent requests | configurable, default 512 | 503 |
| in-flight uploads per user | 32 | 429 |
| upload sessions per user | 256 | 429 |
| WebDAV locks per user | 256 | 507 |
| search results | 1,000 | truncated, flagged in the response |
| archive entries listed | 10,000 | truncated, flagged in the response |
| worker wire message | 8 KiB | worker killed |
| preview source pixels | from `DecodeLimits` | `ErrTooLarge` |

Each has a test that the limit is what refuses, not that a large input happens
to fail. Load-bearing because the product's premise is that the directories are
not ours, so no input's size is ours to assume. Closes F4.

**D10. The three path vocabularies are three types.**

```go
// Vpath is what a client names a file by: "{share label}/{rest}". It is the
// only one of the three that ever appears in a request or a response.
type Vpath struct{ s string }

// SharePath is relative to a share root with the grant's subpath already on
// the front. It is what the core returns and what the core accepts.
type SharePath struct{ s string }

// SafePath is validated and component-wise, relative to a share root, with
// every component checked against the reserved set. It is the only one the
// vfs package accepts.
type SafePath struct{ comps []string }
```

Struct types with unexported fields, not `type Vpath = string` and not
`type Vpath string`. A named string type still converts with a cast that reviews
clean; a struct with an unexported field cannot be constructed outside its
package, so every crossing goes through a named function that says which
direction it is going.

What the absence of this cost the Rust tree, twice: three vocabularies carried
under one name, and a share API that prefixed a share label onto a path that
already had the grant's subpath on its front. Load-bearing because the mistake
it prevents is a path-confusion bug in the layer that decides what a caller may
see.

#### The rest

**D1. No discarded error.** `errcheck` with an empty exclude list. An ignored
return needs `//nolint:errcheck // reason` on the line, and the gate counts
them, so the number going up is visible in a diff. Closes F7.

**D2. No ambient mutable state.** `gochecknoglobals`. A package-level `var` must
be a compile-time table or a constant. Anything that changes a security-relevant
answer is a parameter. Closes F6.

**D6. No unchecked numeric conversion.** One helper:

```go
// Narrow converts between integer widths and reports the value that did not
// fit rather than truncating it. Every conversion between differently sized
// integer types outside this function is a gate failure.
func Narrow[To, From constraints.Integer](v From) (To, error)
```

`gosec` G115 plus a `go vet` analyser for the conversions G115 does not see.
Builds are 64-bit only and the check runs regardless, because "we only build
64-bit" is a fact about today.

**D7. Panic policy.** No bare `go` statement outside one helper:

```go
// Go runs fn in a new goroutine with a recover installed. A panic is logged
// with ctx's request id and the stack, and fails that unit of work. Nothing
// else in this tree may start a goroutine.
func Go(ctx context.Context, name string, fn func())
```

Enforced by a `go vet` analyser that rejects `go` statements outside
`internal/task`. HTTP handlers get the same recover from a middleware. The
preview worker deliberately has neither: a panic there is a decoder failing on
input it could not handle, and the worker dying is the designed outcome.
Closes F9.

**D8. One clock.** `time.Now` appears once, in `internal/clock`. Business logic
takes a `Clock`. Durations are measured with monotonic readings. Nothing panics
on a wall clock behind the epoch; a timestamp before 1970 is clamped and logged
once. Gate: a grep for `time.Now(` outside `internal/clock`. Closes F10.

**D9. `crypto/rand` only.** Gate: a grep for `math/rand` in any import block.

**D11. Durable writes through one helper.** `os.Rename`, `unix.Renameat2` and
`unix.Renameat` are callable only from `internal/vfs`. Gate: an import and
call-site check. The helper's sequence is specified in
[`stowcloud-3-vfs-and-paths.md`](stowcloud-3-vfs-and-paths.md) §4.3.

**D12. Secrets are a type.**

```go
// Secret holds bytes that must never be logged, formatted, serialised or
// compared with ==. String and MarshalJSON return a fixed redaction. Equal is
// constant time. Destroy zeroes the buffer; see the residual-risk note in the
// parent proposal, because a garbage collector may already have copied it.
type Secret struct{ b []byte }
func (Secret) String() string { return "[redacted]" }
```

Never a `string`, because a Go string is immutable and cannot be zeroed at all.
Gate: a `go vet` analyser that rejects `Secret` reaching a formatting verb other
than through its own `String`.

**The residual risk, stated rather than dressed up.** `zeroize` has no Go
equivalent. A garbage collector may copy a value before anything zeroes it, so
`Destroy` clears the buffer this code holds and cannot clear a copy the runtime
made. What is actually available is the three things above: never a `string`, a
redacting formatter so a secret cannot reach a log by accident, and an explicit
zero when the owner is done. The gap that remains is a copy surviving in a dead
heap object, and it is accepted and recorded here rather than claimed closed.
The master key is where this matters most, and it is held for the process
lifetime either way.

**D13. Every server timeout set.** Go's `http.Server` zero values mean no limit,
so an unset `ReadHeaderTimeout` is a slowloris. `ReadHeaderTimeout`,
`ReadTimeout`, `WriteTimeout`, `IdleTimeout` and `MaxHeaderBytes` are set
explicitly at the one construction site, and a test constructs the server the
way `serve` does and asserts each field is non-zero. This is the Go counterpart
of the existing `bind site installs ConnectInfo` gate, and it exists for the
same reason: the failure is invisible to every test that builds its own handler.

**D14. SQL is parameters only.** No `fmt.Sprintf`, no `+`, no `strings.Builder`
reaching a query string. Every statement is a package-level constant, prepared
once. Gate: a grep for formatting verbs in `internal/store`.

**D15. Client-facing errors carry a key, not a sentence.**

```go
// NewError builds a wire error. It takes a MessageKey, never a string, so a
// sentence in any language cannot reach a response body: the browser renders
// the key from its own catalogue.
func NewError(code Code, key MessageKey, args ...Arg) *Error
```

Closes F14 structurally. The Korean scan is kept for comments and log lines,
implemented over a `unicode.RangeTable`, which behaves identically on every host
unlike the current `grep -P`.

**D16. Fuzz targets on everything that reads untrusted bytes.** `SafePath`
parsing, the WebDAV XML scanner, the worker wire decoder, the TUS metadata
header, the OIDC discovery document, the archive lister, the `X-Forwarded-For`
walker. Go's native fuzzing; the seed corpus is committed and the gate runs the
corpus, while the nightly job runs the fuzzer.

**D17. `-race` always.** `go test -race ./...` in the gate, not in a separate
job that can be skipped. Go's race detector finds a class the Rust tree could
not have and the Go tree can: two goroutines on one map.

**D18. `govulncheck` and a direct-dependency allowlist.** Replaces
`cargo-deny`. The allowlist is a checked-in file of module paths; a new direct
dependency is a diff to it. Go embeds its module graph in the binary by default,
so `go version -m` replaces what `cargo auditable` was added to provide, with
no wrapper tool to pin.

**D19. No file over 1,500 lines.** A gate, not a preference. It closes F8, and
the number is chosen as roughly the point at which the two current offenders
would have had to grow a seam.

**D20. Trust-boundary validation at the boundary.** Eight boundaries, each with
one validating constructor whose output type is the only thing the layer below
accepts: the config file, the environment, the HTTP request, an XML body, a
worker response, an IdP response, the generated `smb.conf`, and filesystem
contents. "Validated at the boundary" is a type in this tree, not a habit.

## 5. API Design

### 5-1. New / Modified

The helpers the rules introduce, in the packages that own them:

```go
package limits   // D5: every bound, nothing else
package clock    // D8: Clock, the only time.Now in the tree
package task     // D7: Go, the only goroutine spawn
package secret   // D12: Secret
package num      // D6: Narrow
package apierr   // D15: Code, MessageKey, Error, NewError
```

Each is small enough to read in one sitting, which is the point: a rule whose
implementation needs explaining is a rule that will be worked around.

### 5-2. Error Handling

| Error | Raised by | Meaning |
|---|---|---|
| `ErrHardeningRefused` | D3 | startup, `required` mode, a step could not be applied |
| `ErrArchUnsupported` | D4 | no verified syscall mapping for this architecture |
| `ErrTooLarge` | D5 | a named limit refused, and the error names it |
| `ErrNarrow` | D6 | a value did not fit the target width, and the error carries it |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Owner |
|---|---|---|---|
| Phase 0 | The six packages in §5-1, the lint configuration, the two `go vet` analysers (D7, D12), and the gate wiring for D1, D2, D6, D8, D9, D11, D13, D14, D17, D18, D19 | M | heavycaffeiner |
| Phase 1 | D3 and D4, with the tests that fault the kernel probe | with the phase | heavycaffeiner |
| every phase | D5's limits for that subsystem, D10 at every crossing, D16's targets for that subsystem's parsers, D20's constructor for its boundary | with the phase | heavycaffeiner |

The Phase 0 half is the deliverable that makes the rest cheap. A rule added
after the code it governs is a refactor; added before, it is just how the code
gets written.

### 6-2. Dependencies

**Tools**, all installed by the gate rather than assumed present:
`golangci-lint` (running `errcheck`, `gochecknoglobals`, `gosec`, `staticcheck`,
`ineffassign`, `bodyclose`), `govulncheck`. The two custom analysers are in-tree
under `go/tools/`, built by the gate, with no external dependency.

**Constraint**: `golang.org/x/exp/constraints` for D6's generic bound, or a
hand-written constraint interface to avoid the dependency. Decided at Phase 0;
the hand-written one is four lines and is preferred.

## 7. References

- [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3: every rule here that
  has a finding number attached.
- [`stowcloud-2-gate-and-toolchain.md`](stowcloud-2-gate-and-toolchain.md):
  where the enforcement is actually configured.
- `crates/sc-core/src/path.rs`, `crates/sc-vfs/src/safe_path.rs`: the three
  vocabularies D10 turns into three types, as they exist today.
- `scripts/verify.sh`: the two Rust gates whose reasoning D13 and D15 carry
  over, both of which exist because the failure was invisible to the test suite.
- Go: `net/http.Server` field documentation (the zero-value timeouts D13
  exists for), `testing` fuzzing, `go vet` analyser API.
