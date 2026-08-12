# Auth and ACL - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Accounts, sessions, app passwords, TOTP, recovery codes and grants in Go, with
the Argon2 parameters and the concurrency gate that bounds their memory carried
over unchanged. The enumeration defence and the constant-time comparisons are
the parts that a reimplementation loses quietly, so both are specified as
behaviour with tests rather than as intentions.

## 2. Background & Motivation

`sc-auth` is 6,569 lines and `sc-acl` is 763. Neither is complicated; both are
easy to get subtly wrong, and the subtle wrongness is not visible in a passing
test.

Three properties in the current implementation are worth naming because they
were arrived at by fixing something:

- **Argon2 is behind a counting semaphore.** Peak memory is `m_cost` times
  concurrency, so 48 MiB times 4 is 192 MiB, and without the gate N concurrent
  password changes cost N times 48 MiB. The current code records the bug: the
  synchronous paths (`create_user`, `set_password`, `totp_enroll`) hashed
  straight through and bypassed the gate entirely.
- **A hash made under weaker parameters is upgraded on a successful login.**
  Without that, raising the cost protects new accounts only.
- **An unknown user costs the same as a known one.** Otherwise the response time
  is an account oracle.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Argon2id at m=48 MiB, t=3, p=1, behind a gate of 4 concurrent
      invocations, applied on **every** path that hashes or verifies.
- [ ] Rehash on successful verification when the stored parameters are weaker
      than the configured ones.
- [ ] Constant-time comparison for every secret: session token, app password,
      recovery code, TOTP, share-link password.
- [ ] Uniform cost and uniform response for an unknown user.
- [ ] App-password scopes enforced as a layer, not as a check each handler
      remembers.
- [ ] Grants evaluated against the virtual root with the default of "a new
      account sees nothing".

### 3.2 Non-Goals

- [ ] New authentication factors. WebAuthn is not in scope and is not a
      non-goal on the merits, only on scope.
- [ ] Changing the Argon2 parameters. They are `docs/proposals/stowcloud-10`'s
      and this port does not relitigate them.
- [ ] Password policy beyond a length floor. The current tree does not have one
      and adding one is a product decision.
- [ ] Session storage outside `state.db`.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/auth
  password.go   Argon2id hashing, the encoded form, rehash-on-verify
  gate.go       the counting semaphore (peak memory bound)
  session.go    creation, lookup, expiry, rotation
  apppw.go      app passwords and their scopes
  totp.go       enrolment and verification, with the drift window
  recovery.go   single-use codes
  nthash.go     the SMB NT hash, encrypted at rest
  login.go      the flow, and the enumeration defence
  ratelimit.go  the login bucket, keyed by client address
  audit.go      the append-only log
internal/acl
  perms.go      the permission bits
  grant.go      storage
  eval.go       evaluation against the virtual root
```

### 4.2 Data Model Changes

The tables named in
[`stowcloud-5-store-and-schema.md`](stowcloud-5-store-and-schema.md) §4.2.2, all
in `state.db`. One change of substance: `session` and `app_password` store a
hash of the token, never the token, so a `state.db` read does not yield live
credentials. Where the current tree already does this, it is carried over; where
it stores a comparable value, the port moves it.

### 4.3 Core Logic

#### 4.3.1 Argon2 and the gate

```go
// Hash derives an Argon2id hash under the configured parameters. It acquires
// the gate first: peak memory is m_cost times the number of concurrent
// invocations, so an ungated path is a memory-exhaustion vector reachable by
// anyone who can submit passwords.
func (s *Service) Hash(ctx context.Context, pw secret.Secret) (Encoded, error)
```

`golang.org/x/crypto/argon2`'s `IDKey` is the primitive. The gate is a buffered
channel of size `argon2_parallelism`, acquired with the request's context so a
client that gives up stops waiting. Every path that hashes or verifies goes
through it, including account creation, password change and TOTP enrolment,
which is the bug the current tree's test asserts against.

The encoded form is the standard PHC string, so a hash written by the Rust tree
is readable by the Go one. This matters for
[`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.4's migration: passwords
migrate as-is and nobody has to reset one.

#### 4.3.2 Verification and rehash

```go
// Verify checks pw against enc in constant time relative to the password, and
// reports whether the stored parameters are weaker than the configured ones.
// The caller rehashes on a true return, so that raising the cost protects
// existing accounts and not only new ones.
func (s *Service) Verify(ctx context.Context, enc Encoded, pw secret.Secret) (ok bool, stale bool, err error)
```

#### 4.3.3 The enumeration defence

An unknown username performs a verification against a fixed decoy hash carrying
the configured parameters, so the cost and the timing match a real account, and
returns the same error the wrong-password case returns. The decoy is computed
once at startup.

The tests are behavioural: an unknown user and a known user with a wrong
password must produce the same status, the same error key, and a duration
within a stated band. The band is wide enough not to be flaky and narrow enough
to catch the case where the lookup short-circuits.

#### 4.3.4 Sessions

A session token is 256 bits from `crypto/rand`, returned once, and stored as a
SHA-256 hash. Lookup hashes the presented token and compares in constant time.
Expiry is absolute and idle, both configured. Rotation on privilege change.

The cookie is `Secure`, `HttpOnly`, `SameSite=Lax`, and there is no non-TLS
listener for it to leak on, which is a property of the deployment rather than of
this package and is stated in `docs/proposals/stowcloud-13`.

#### 4.3.5 App passwords and scopes

An app password carries a `Scope`. The current tree found that nothing
downstream checked it, and closed that with a middleware layer rather than a
check in each handler. The port keeps the layer, at step 9 of the chain in
[`stowcloud-8-http-and-api.md`](stowcloud-8-http-and-api.md), and adds the
property that makes it hold: the scope travels in the request context as a type
that the route table consults, so a new route is refused by default until its
required scope is declared.

Default-deny for a new route is the difference between a layer and a habit.

#### 4.3.6 TOTP and recovery codes

`crypto/hmac` with `crypto/sha1`, thirty-second steps, a one-step drift window
either side, and a replay guard that records the last accepted step per user so
a captured code cannot be reused inside its window. Base32 secrets, encrypted at
rest under the master key.

Recovery codes are single-use, stored hashed, and consumed in the same
transaction that accepts them so a concurrent second use finds nothing.

#### 4.3.7 The NT hash

MD4 of the UTF-16LE password, encrypted at rest with XChaCha20-Poly1305 under
the master key, exactly as today. `golang.org/x/crypto/md4` and
`golang.org/x/crypto/chacha20poly1305`. MD4 is deprecated upstream and correct
here: the algorithm is fixed by the SMB protocol, and the mitigation is that the
value is encrypted at rest and only ever handed to the sidecar.

#### 4.3.8 Rate limiting

The login bucket is keyed by the client address that
[`stowcloud-8`](stowcloud-8-http-and-api.md) resolved, which is why the trusted
proxy rule matters: without it every visitor is the proxy, the bucket collapses
onto one key, and one attacker locks out everyone. That failure is named in the
README and it is the reason the address resolution is one function with one
implementation.

#### 4.3.9 Grants

A grant names a share and, optionally, one subpath, with read and write decided
separately. Evaluation happens against the virtual root, and the default for an
account with no grant is an empty view rather than an error.

The property that makes it a security boundary rather than a filter: a subpath
grant means the account cannot tell the parent exists. That is the existence
rule from [`stowcloud-8`](stowcloud-8-http-and-api.md) §4.3 applied at the ACL
layer, and its test is that a path outside the grant answers identically to a
path that does not exist, on every mount.

## 5. API Design

### 5-1. New / Modified

```go
package auth

// Login runs the whole flow: rate-limit check, user lookup, password
// verification (against a decoy for an unknown user, so cost and timing do not
// distinguish), second factor if enrolled, session creation, audit write. It
// returns one error for every credential failure, because distinguishing them
// is an oracle.
func (s *Service) Login(ctx context.Context, req LoginRequest) (Session, error)

package acl

// Evaluate resolves what user may do at vpath. It returns a zero Perms and no
// error for a path outside every grant: "you may do nothing here" and "there is
// nothing here" are the same answer by design, and the HTTP layer turns both
// into 404.
func (e *Evaluator) Evaluate(user UserID, vpath Vpath) (Perms, error)
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 401 | any credential failure: unknown user, wrong password, wrong second factor, expired session. One error key for all of them |
| 403 | authenticated and refused, only where the caller may know the target exists |
| 404 | outside every grant |
| 429 | login bucket exhausted, or too many second-factor attempts |

| Error | Meaning |
|---|---|
| `ErrCredentials` | the single credential-failure error |
| `ErrSecondFactor` | a factor is required and was not supplied; distinct because the client must be told to ask |
| `ErrScope` | the app password's scope does not cover this route |
| `ErrLocked` | the account is disabled |

`ErrSecondFactor` being distinct from `ErrCredentials` is a deliberate leak: the
client cannot prompt for a code without it. It is only ever returned after the
password verified, so it discloses nothing an attacker did not already have.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 3a | `password.go`, `gate.go`, the PHC form, the rehash path, the concurrency test | M | Phase 2 | heavycaffeiner |
| Phase 3b | `session.go`, `apppw.go`, `recovery.go`, `totp.go`, `nthash.go` | M | 3a | heavycaffeiner |
| Phase 3c | `login.go`, `ratelimit.go`, `audit.go`, and the enumeration tests | M | 3b | heavycaffeiner |
| Phase 3d | `internal/acl`: perms, storage, evaluation, the existence-rule test | M | Phase 2 | heavycaffeiner |

3d is independent of 3a to 3c.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `golang.org/x/crypto/argon2` | Argon2id |
| `golang.org/x/crypto/chacha20poly1305` | XChaCha20-Poly1305 at rest |
| `golang.org/x/crypto/md4` | the NT hash |

TOTP is `crypto/hmac` and `crypto/sha1`, both standard library, rather than a
TOTP module: the algorithm is forty lines and a module here would be a
dependency for an encoding.

## 7. References

- `docs/proposals/stowcloud-10-auth.md`: the parameters, the three-tier
  verification path, sessions, app passwords, TOTP, the enumeration defence.
- `docs/proposals/stowcloud-0-oidc-login.md`: link-only single sign-on, ported
  in [`stowcloud-14-smb-and-oidc.md`](stowcloud-14-smb-and-oidc.md).
- `crates/sc-auth/src/argon_gate.rs`: the memory bound and why the gate exists.
- `crates/sc-auth/src/tests.rs:125`: the bug where synchronous paths bypassed
  the gate.
- `crates/sc-auth/src/nt_hash.rs`: the encrypted-at-rest NT hash.
- `docs/proposals/stowcloud-17-audit-gaps.md`: the SMB credential work.
