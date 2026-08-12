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

### 2.0 The question this subsystem answers

Not "how strong a KDF", but **"how few times must it run"**. Argon2id at 48 MiB
takes about 80 ms, which is right for a login form and impossible for WebDAV,
where a sync client sends hundreds of requests a minute and every one carries
the same Basic credential. Running the KDF per request would make the server
slower than the disk it fronts and turn an ordinary sync into a self-inflicted
denial of service. The three-tier verification path is the answer to that
question, and it is the reason this subsystem has the shape it has.

The parameter choice follows the same logic and is
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S10: 48 MiB rather
than 64, because the memory cost is multiplied by the concurrency cap and 48 by
4 is 192 MiB of the container's budget. A stronger per-hash setting that lets
four concurrent logins exhaust the container is not stronger in practice. Any
future proposal to raise it has to raise the product, not the number.

### 2.1 What the current tree already learned

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
- [ ] **Revocation that actually revokes, including over SMB.** Disabling an
      account, revoking an app password or rotating a credential takes effect
      on every surface, not only the one it was issued on. The SMB path is the
      one that gets forgotten, because the credential lives in a passdb the
      sidecar reads, so revocation there is a file that has to be rewritten
      rather than a row that stops matching.
- [ ] **Second-factor enforcement no protocol path can bypass.** A factor
      required for the web UI and not for WebDAV is not a factor.

### 3.2 Non-Goals

- [ ] New authentication factors. WebAuthn is not in scope and is not a
      non-goal on the merits, only on scope.
- [ ] Changing the Argon2 parameters. This port does not relitigate them, for
      the reason in §2.0: the number is not the knob, the product is.
- [ ] **Just-in-time provisioning from an identity provider.** The provider
      authenticates; it never creates an account. Authority stays in the local
      database, which is what makes revocation here total rather than advisory.
- [ ] **A password-strength meter as a gate, and a breached-password
      denylist.** The gate is a ten-character minimum with no composition
      rules, and the meter is advisory and lives only in the browser. The
      denylist was considered and dropped rather than merely unscheduled: the
      smallest useful one is six figures of entries, which is a megabyte inside
      a binary the deployment proposal keeps deliberately small, and the dual
      rate gate already blocks the online guessing it would defend against.
- [ ] Storing the SMB password separately from the account password. SMB needs
      the NT hash, `MD4(UTF-16LE(password))`, which cannot be derived from an
      Argon2 digest, so setting an SMB password additionally stores that hash
      encrypted under the master key. It is not the password, and it is
      password-equivalent for SMB authentication, which is the weakening: an
      attacker holding both the database and the master key can authenticate as
      that account over SMB without ever recovering the password. It is
      accepted because the alternative is a second password per account for a
      protocol most people reach with the same credential. The trade is not
      reopened here; the mitigations are that the ciphertext is useless without
      the master key, that it is bound to the user id and key version as
      additional authenticated data so it cannot be transplanted between
      accounts, and that revocation clears it.
- [ ] Session storage outside `state.db`.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/auth
  password.go   Argon2id hashing, the encoded form, rehash-on-verify
  gate.go       the counting semaphore (peak memory bound)
  credcache.go  the three tiers above Argon2, and auth_generation
  session.go    creation, lookup, expiry, rotation
  apppw.go      app passwords and their scopes
  totp.go       enrolment and verification, with the drift window
  recovery.go   single-use codes
  crockford.go  the Base32 alphabet both of the above are printed in
  groups.go     group and membership storage, no ACL knowledge
  nthash.go     the SMB NT hash, encrypted at rest
  masterkey.go  load, generate, rotate, and the startup decrypt check
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

#### 4.3.2a The three-tier verification path

§2.0 poses the question and this is the answer. Three tiers, each cheaper than
the one below it, all of them invalidated together by an `auth_generation`
counter that any credential change bumps:

| Tier | Key | Holds | Lifetime |
|---|---|---|---|
| 1, connection memo | `sha256(the raw Authorization header bytes)` | the resolved principal | until `auth_generation` moves; a bounded LRU rather than per-connection state, because the auth package has no connection object to hang it off |
| 2, credential cache | `HMAC(ephemeral key, "dav\x00" ‖ user ‖ "\x00" ‖ password)` | the Argon2 outcome, accepted or rejected | 15 minutes absolute, 5 minutes idle, positive; 30 seconds negative |
| 3, Argon2 | none | the answer itself | one invocation |

Four properties are load-bearing and each would be easy to drop:

- **The tier-2 key is an HMAC under a per-process ephemeral key**, not a hash of
  the password. A process dump then yields nothing offline-attackable, and the
  cache dies with the process.
- **Rejections are cached too**, for a much shorter window. Without that, a
  client looping with a wrong password pays Argon2 every time and so does the
  server, which is the denial of service §2.0 exists to avoid, arriving from
  the direction nobody looks at.
- **Invalidation is a counter, not a sweep.** A password change, an app-password
  revocation or an account disable bumps `auth_generation`, and every entry in
  every tier becomes stale by comparison rather than by being found and deleted.
  That is what makes revocation immediate on a surface that never re-reads the
  database.
- **App-password tokens get their own tier-3 bypass**, a 60-second cache keyed
  by `sha256(token)`, because an app password is high-entropy and does not need
  a memory-hard function to be safe. It is still invalidated by the same
  counter.

The tiers exist for WebDAV, and they are correct for the browser too. Nothing
in them is protocol-specific, which is why they live in `internal/auth` and not
in the DAV package.

#### 4.3.3 The enumeration defence

An unknown username performs a verification against a fixed decoy hash carrying
the configured parameters, so the cost and the timing match a real account, and
returns the same error the wrong-password case returns. The decoy is computed
once at startup.

The tests are behavioural: an unknown user and a known user with a wrong
password must produce the same status, the same error key, and a duration
within a stated band. The band is wide enough not to be flaky and narrow enough
to catch the case where the lookup short-circuits.

Timing is part of the surface, not an afterthought to it
([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S2). The stance is
"no account-existence oracle on **any** surface", and a response that is
identical in content and 80 ms faster is an oracle. The same reasoning governs
search, where a query that returns nothing quickly for a path the caller cannot
see is the same leak wearing different clothes
([`11`](stowcloud-11-search.md) §4.3.2).

#### 4.3.4 Sessions

A session token is 256 bits from `crypto/rand`, returned once, and stored as a
SHA-256 hash. Lookup hashes the presented token and compares in constant time.
Expiry is absolute and idle, both configured. Rotation on privilege change.

The cookie is `Secure`, `HttpOnly`, `SameSite=Lax`, and there is no non-TLS
listener anywhere for it to leak on. That last part is a property of the
product rather than of this package: one socket, always TLS, with a plain
request to the same port answered by a redirect rather than served. A `Secure`
cookie on a plaintext origin fails silently, so a plaintext listener would make
login mysteriously not work rather than obviously insecure, which is why there
is not one.

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

**Recovery codes and app passwords are Crockford Base32.** The alphabet
excludes `I`, `L`, `O` and `U`, so a code read off a screen and typed into a
phone does not fail on a character the reader guessed wrong. That is a
usability property with a security consequence: a code people mistype is a code
people write down somewhere worse. Decoding accepts the confusable characters
and folds them, which is the half of Crockford that matters here.

#### 4.3.6a Groups

Groups exist and carry membership; grants may name a group instead of a user.
The auth package owns `group` and `membership` as thin storage with no ACL
knowledge, and there is exactly one function that pushes a membership change
into the live permission engine. That single crossing is the design: two places
writing membership means two places that can disagree with what the evaluator
currently believes, and a stale membership is a grant that is wrong in the
direction that matters.

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

#### 4.3.10 The master key

Everything encrypted at rest here (the NT hash, the TOTP secret) and in
[`7`](stowcloud-7-core-domain.md) (share-link secrets) is under one key. Its
lifecycle is specified here because everything it protects is auth state.

**Loading.** From the file `SC_MASTER_KEY_FILE` points at, defaulting to
`master.key` in the data directory. Absent, it is generated and written with
mode `0600`. The key must be **ready before the first request**, not derived
lazily, because SMB credential rendering needs it at startup.

**Four rules, and each has a reason worth keeping:**

1. **Never from an environment variable.** Only a *path* may come from the
   environment. An env var is visible through `docker inspect` and
   `/proc/*/environ` to anyone who can inspect the container, which defeats the
   point of a key file entirely. The presence of a `SC_MASTER_KEY` variable is a
   **hard error regardless of its value**, because someone who set it believes
   it is being used.
2. **A warning, not a refusal, when the key resolves inside the data
   directory.** Backing up the database and the key together defeats
   encryption at rest. It is the default location, so refusing would make the
   default configuration fail to start; the warning is what an operator acts on
   when they set up backups.
3. **A key version travels with every ciphertext**, as additional
   authenticated data alongside the user id, so a value cannot be transplanted
   between accounts or replayed across a rotation.
4. **A startup check refuses to serve on a key that cannot decrypt what is
   already on disk.** The failure it prevents is the loud one arriving late:
   a wrong key mounted, the server starting happily, and every SMB login and
   every TOTP verification failing one at a time with no common cause visible.

**Rotation** generates a new key, re-encrypts every NT hash and every TOTP
secret under it, and bumps the key version, **all inside one transaction**, then
swaps the key file. A crash mid-rotation leaves the old key and the old
ciphertexts, which is the only safe direction to fail in.

It is a CLI subcommand and not an HTTP route
([`2`](stowcloud-2-gate-and-toolchain.md) §5-1), because a master key has no
business ever reaching a browser tab. Rotation sits at the trust level of shell
access to the data directory, which is the level the key file already sits at.

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
| `ErrAccountDisabled` | the account is disabled. Not `ErrLocked`, which [`10`](stowcloud-10-webdav.md) §5-2 uses for a WebDAV resource lock; two sentinels with one name in two packages is a name a reader conflates |

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

- `crates/sc-auth/src/login.rs`, `password.rs`, `session.rs`, `app_password.rs`,
  `totp.rs`, `rate_limit.rs`: the flow and the primitives this translates.
- `crates/sc-acl/src/lib.rs`, `crates/sc-core/src/acl_store.rs`: grants and
  evaluation.
- `crates/sc-auth/src/argon_gate.rs`: the memory bound and why the gate exists.
- `crates/sc-auth/src/tests.rs:125`: the bug where synchronous paths bypassed
  the gate.
- `crates/sc-auth/src/nt_hash.rs`: the encrypted-at-rest NT hash.
- `crates/sc-auth/src/nt_ops.rs`, `crates/sc-server/src/passdb.rs`: the SMB
  credential path revocation has to reach.
