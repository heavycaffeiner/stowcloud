# Auth 01: credentials and sessions

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/auth` (here `login.go`, `password.go`, `session.go`,
> `apppw.go`, `totp.go`, `recovery.go`, `credcache.go`, `ratelimit.go`,
> `users.go`, `groups.go`, `admin.go`, `reconfirm.go`, `passdb.go`,
> `smbstate.go`) is referenced as a behavioral specification only. The new
> implementation is written completely from scratch; nothing is copied.

## The three-tier verification path

The path exists to bound how often Argon2 runs, not to weaken what it
protects. All three tiers live and die with the process and are keyed or
HMAC'd with per-process ephemeral material from the CSPRNG, so a process
dump yields nothing offline-attackable. All three compare their stored
generation against the service's counter; any credential change bumps it,
which makes revocation immediate on a surface that never re-reads the
database.

| Tier | Keyed by | Capacity | Lifetime |
| --- | --- | --- | --- |
| 1: connection memo | SHA-256 digest the transport hands in | 4,096 LRU | generation only |
| 2: credential cache | HMAC(ephemeral, user \|\| pw), 16 bytes | LRU | positive 15 min / 5 min idle; negative 30 s |
| 3: app-password token | SHA-256 of the folded token | 1,024 LRU | 60 s |

- The negative TTL on tier 2 is load-bearing: a client looping with a
  wrong password would otherwise buy a full Argon2 invocation per loop,
  which is a denial of service arriving as ordinary traffic.
- App passwords are high-entropy (256 bits), so tier 3 is allowed to
  bypass the memory-hard function entirely; the short TTL bounds the
  revocation window the cache adds beyond the generation check.

```go
type Principal struct {
    UserID   int64
    Login    string // the account name; what a client stores
    Display  string // optional label; never a substitute for Login
    Disabled bool
}

type Outcome struct {
    Accepted  bool
    Principal Principal
}

func (s *Service) Generation() int64
```

## Argon2id, PHC, and the gate

```go
type Params struct {
    MemoryKiB, Iterations uint32
    Parallelism           uint8
    KeyLen                uint32
}

func CurrentParams() Params // 49152 KiB, t=3, p=1, 32-byte key
const MinPasswordLen = 10
const GateConcurrency = 4

func (s *Service) Hash(ctx context.Context, pw secret.Secret) (string, error)
func (s *Service) Verify(ctx context.Context, enc string, pw secret.Secret) (ok, stale bool, err error)
func Stale(enc string) bool
```

- Every hash is stored in the standard PHC string form, which makes it
  self-describing: verification always runs under the parameters the
  stored hash names, so raising `CurrentParams` still verifies every
  password already on file. `Verify` reports `stale` when the stored
  parameters differ, and `Login` rehashes on a successful stale
  verification, so a cost raise protects existing accounts, not only new
  ones.
- A malformed stored hash is a refusal (`ok == false`), never an error: a
  corrupt row must fail that login, not the process. It also reports
  `stale == true`, so a corrupt hash is replaced the moment its owner
  logs in with the right password through some other credential path.
- The parsed PHC numbers are bounded before narrowing (`kit/num.Narrow`
  replaces the hand-rolled `phcU32`/`phcU8`), so a hostile stored hash
  cannot truncate an out-of-range memory cost into something that
  allocates unpredictably. The key length is bounded at 64 bytes.
- **The gate.** `Hash` and `Verify` both acquire a counting semaphore of
  width `GateConcurrency` before touching Argon2. Peak KDF memory is
  memory-cost x width (48 MiB x 4 by default), and the bound is enforced
  where the memory is spent, not left to callers: account creation and
  TOTP enrolment reach Argon2 too, and an ungated path is a
  memory-exhaustion vector for anyone who can submit a password. The gate
  honors context cancellation and records a high-water mark
  (`PeakConcurrency`) the tests read.
- Derived keys are zeroed after comparison; the comparison itself is
  `subtle.ConstantTimeCompare`.

## Login

```go
type LoginRequest struct {
    Name     string
    Password secret.Secret
    Factor   string // second-factor code; empty until the client is asked
    IP, UA   string
    AMR      int
}

func (s *Service) Login(ctx context.Context, req LoginRequest, sessionTTL time.Duration) (Session, error)
```

Order, and the reasons the order is what it is:

1. **Rate limit** on the resolved client IP. Refusal is `ErrRateLimited`.
2. **Lookup.** An unknown name verifies the password against a decoy hash
   before answering `ErrCredentials`: the decoy is the once-per-process
   Argon2 hash of 32 random bytes, computed under the gate like any real
   hash, so the unknown-user response costs the same ~80 ms as a
   wrong-password response. A response identical in content but faster is
   still an account-enumeration oracle. The failed attempt is audited
   with no actor and the tried name in the target column, because a run
   of guesses against one name is exactly what the log is read to find.
3. **Verify** against the stored hash. Failure audits with the account as
   actor and answers the same `ErrCredentials`.
4. **Stale rehash.** A successful verify under old parameters rehashes
   under `CurrentParams` now.
5. **Disabled check**, after password verification, so a disabled account
   with a wrong password still answers `ErrCredentials` rather than
   leaking that the account exists and is disabled.
6. **Second factor.** An enrolled account with an empty `Factor` answers
   `ErrSecondFactor`, which is the one distinguishable refusal by design:
   the client has to know to ask for a code, and by this point the
   password has already been verified, so nothing is leaked to a caller
   who does not hold it. A wrong code is `ErrCredentials`.
7. **Session mint**, then the audit row. An audit write failure after the
   session exists is logged and not returned: the person is holding a
   session that works, and answering 401 over a bookkeeping row reports
   their sign-in as failed while it succeeded. That defect shipped once;
   the regression test is named below.

### The rate limiter, fixed

The old `limiter` guards a map and an eviction slice with no mutex and is
called concurrently per request; concurrent map writes panic the process
(audit finding 3, the one real data race in the package). The rebuild's
limiter is the same sliding window (5 minutes, 10 attempts, 65,536 keys
with FIFO eviction) with one `sync.Mutex` around `Allow`. The lock is
per-call and the critical section is a map access; no I/O happens under
it. An empty key buckets as `"unknown"` rather than passing free.

## Sessions

```go
type Session struct {
    Token  secret.Secret // 256-bit, shown once, stored only as SHA-256
    UserID int64
}

func (s *Service) CreateSession(ctx context.Context, userID int64, ip, ua string, amr int, lifetime time.Duration) (Session, error)
func (s *Service) LookupSession(ctx context.Context, token secret.Secret) (Principal, error)
func (s *Service) RevokeSession(ctx context.Context, token secret.Secret) error
func (s *Service) RevokeSessionByHash(ctx context.Context, userID int64, hash []byte) error
func (s *Service) RevokeSessionsOf(ctx context.Context, userID int64) (int64, error)
func (s *Service) Sessions(ctx context.Context, userID int64) ([]SessionRow, error)
```

- Defaults: 30-day absolute lifetime (when the caller passes zero), 30
  minute idle window. The idle window is applied at lookup because the
  schema keeps no per-session idle column.
- `LookupSession` re-compares the presented hash against the stored row in
  constant time, checks absolute then idle expiry, sweeps an expired row
  best-effort (a sweep failure is logged, not returned: the session is
  dead either way), refuses a since-disabled account with
  `ErrAccountDisabled`, and touches `last_seen` best-effort (a cold stamp
  is refreshed by a later request).
- Every revocation bumps the generation, so the connection memo cannot
  serve a revoked session.
- `RevokeSessionByHash` is owner-scoped (both the hash and the user id in
  the predicate), for the session-list screen where the client holds row
  hashes, not tokens.

## App passwords

```go
type Scope struct {
    Perms  uint16
    Shares []string // empty means every share the account can see
}

func (s *Service) CreateAppPassword(ctx context.Context, userID int64, name string, scope Scope, expires time.Duration) (string, error)
func (s *Service) VerifyAppPassword(ctx context.Context, token string) (Principal, Scope, error)
func (s *Service) RevokeAppPassword(ctx context.Context, userID, id int64) error
func (s *Service) AppPasswords(ctx context.Context, userID int64) ([]AppPasswordRow, error)
func (s *Service) RequestWipe(ctx context.Context, userID, id int64) error
```

- Tokens are 256 bits in Crockford Base32 (no I, L, O, U), so a token
  read off a screen survives being typed. Verification folds the
  presented string (case, the confusable letters) before hashing, and the
  fold is fuzzed: any two spellings that fold together must verify
  together.
- Stored as SHA-256 only. The verify path: fold, hash, tier-3 cache,
  then the row; refusals for a missing row, a requested wipe, an expired
  stamp, a missing or disabled owner are all `ErrCredentials`, one
  answer.
- `RequestWipe` marks the row so the next presentation of that token is
  refused, and the client that presented it is told to wipe its local
  state (the remote-wipe protocol the compat surface exposes). The mark
  survives until the row is deleted.
- The scope's share list is stored NUL-separated; the split helper is
  store-side now. `Scope` travels in the request context as a typed
  value; the route table consults it, and a new route is refused by
  default until its required scope is declared (that check is the
  presentation layer's, phase 3).

## TOTP

```go
func (s *Service) GenerateTOTPSecret() (string, error) // 160-bit, Base32
func (s *Service) EnrollTOTP(ctx context.Context, userID int64, secretB32 string) error
func (s *Service) VerifyTOTP(ctx context.Context, userID int64, code string, nowNs int64) (bool, error)
func (s *Service) DisableTOTP(ctx context.Context, userID int64) error
```

- RFC 6238: HMAC-SHA1, 30-second step, 6 digits, drift window of one step
  either side. SHA-1 is protocol-fixed and not used as a
  collision-resistant hash.
- **Replay guard.** Accepted steps are recorded (`totp_used`) in the same
  transaction that accepts them; a code captured in transit cannot be
  replayed inside its window. The guard row expires with the window.
- The secret is sealed at rest under the master key with the `totp` AAD
  binding (02).
- **Enrolment drops the NT hash in the same transaction.** The account
  password keeps working over SMB otherwise, which is exactly the factor
  the user just added being bypassed by the older protocol. Enrolment and
  disable both bump the generation and republish the passdb, because the
  SMB TOTP policy may change what is published.

## Recovery codes

```go
func (s *Service) GenerateRecoveryCodes(ctx context.Context, userID int64, n int) ([]string, error) // 1..64
func (s *Service) UseRecoveryCode(ctx context.Context, userID int64, code string) (bool, error)
func (s *Service) RecoveryCodesRemaining(ctx context.Context, userID int64) (int, error)
```

Single-use, stored hashed, shown once. Generation replaces the whole set.
`UseRecoveryCode` deletes the accepted hash in the same transaction that
accepts it, so a concurrent second use of one code finds nothing: the
delete's affected-row count is the acceptance.

## Accounts, groups, administration

```go
func (s *Service) CreateUser(ctx context.Context, name, display string, pw secret.Secret) (int64, error)
func (s *Service) CreateAdmin(...) (int64, error)
func (s *Service) SetPassword(ctx context.Context, userID int64, newPW secret.Secret) error
func (s *Service) VerifyAccountPassword(ctx context.Context, userID int64, pw secret.Secret) (bool, error)
func (s *Service) DisableAccount / EnableAccount / DeleteUser
func (s *Service) UserByID / UserIDByName / NameOf / ListUsers / CountUsers / HasAdmin / IsAdmin
func (s *Service) SetQuota(ctx context.Context, userID int64, bytes *int64) error
```

- **Creation validates the account name** through the one rule in 05 and
  refuses `ErrWeakPassword` under `MinPasswordLen`. The NT hash is
  derived and sealed **in the same transaction as the account row**: the
  hash comes from the plaintext, which exists only now. An account
  created without one had no way to reach SMB until it changed its
  password, and the interface's "set a separate password" framing made
  that defect read as a policy.
- A duplicate name is a typed `ErrNameTaken` from the store aggregate,
  not a driver-message string match (audit finding 5).
- `SetPassword` rehashes, re-derives and re-seals the NT hash, bumps the
  generation, republishes the passdb.
- `VerifyAccountPassword` is the reconfirm surface (an account proving
  itself before a sensitive change); it runs the same gated verify and
  the same decoy discipline is not needed because the account is already
  authenticated.
- `DisableAccount` bumps the generation (live sessions and cached
  credentials die now) and republishes the passdb (the SMB side dies
  too). `DeleteUser` does the same plus the row cascades.
- Groups: create, rename (refusing duplicates), delete, membership add,
  remove and whole-set replace. Every membership change calls the
  `OnMembership` crossing so the live ACL evaluator reloads; the callback
  is wiring, not an import.

## The SMB credential seam

The passdb rendering severs its `internal/smb` import (audit finding 1).
Auth owns the **facts**; the SMB phase owns the **format**.

```go
// SMBCredential is one publishable account, as facts.
type SMBCredential struct {
    Name    string
    UID     uint32 // row id + SMBBaseUid
    NTHash  [16]byte
}

// Config carries the two seams, both nil-able:
//   RenderPassdb  func(creds []SMBCredential) []byte     // the smb phase's renderer
//   PublishSMB    func(ctx context.Context)              // the whole-config publisher
```

- `SMBBaseUid = 30000` stays here and is part of the contract: the
  account file and the passdb resolve entries to accounts through this
  uid, and disagreement makes the import silently produce an empty
  credential database.
- Eligibility (`passdbEnabled`): not opted out, not disabled, and not
  blocked by the TOTP policy. `TOTPPolicy` (`RequireSeparate` default,
  `Block`) decides what is **published**, never what is stored, so
  changing it back restores access without anyone resetting a password.
- **The sink discipline.** Every credential-changing path (password set,
  SMB password set/clear, TOTP enrol/disable, account disable/delete,
  SMB access toggle) re-renders the whole file from state and then asks
  the publisher to push. A committed transaction is not a completed
  security decision until the sidecar file agrees; smbd authenticates
  against the last published file, and a revocation that stops at SQLite
  leaves the revoked credential serving.
- `SetSMBPassword` seals a fresh NT hash from the given password;
  `ClearSMBPassword` deletes it and reports `revertible`: whether the
  account password takes over (false when opted out, provider-linked, or
  TOTP-blocked, in which case clearing means losing SMB, and the caller
  says so instead of reporting a success that reads as "nothing
  changed").

## Errors

```go
var (
    ErrCredentials     = errors.New(...) // every credential failure; one answer by design
    ErrRateLimited     = errors.New(...)
    ErrSecondFactor    = errors.New(...) // the one distinguishable ask
    ErrAccountDisabled = errors.New(...)
    ErrWeakPassword    = errors.New(...)
    ErrNameTaken       = errors.New(...) // from the store aggregate, typed
    ErrNameInvalid     = errors.New(...) // from the username rule (05)
)
```

No error here chooses a wire status; the protocol layer maps them once.

## Deliberate changes

1. **The limiter gets a mutex** (audit finding 3). Behavior under a
   single caller is identical; under concurrent callers it stops being
   undefined.
2. **All SQL moves to state aggregates** (audit finding 2): `authuser`,
   `session`, `apppw`, `totp`, `recovery`, `smbsecret` (03 and 04 own
   `oidclink` and `audit`). The service keeps no statement text and never
   touches `SQL()` directly.
3. **The smb import becomes the seam above** (audit finding 1).
4. **`kit/num.Narrow` replaces the four hand-rolled narrowings** (audit
   finding 4): the PHC bounds and the passdb's seal-version and uid
   checks.
5. **`ErrNameTaken` replaces `isUniqueViolation`** (audit finding 5); the
   string match moves nowhere, it is deleted, and the aggregate maps the
   driver's constraint error once.

Everything else is behavior-preserving, including every constant named in
this document.

## Tests

Written fresh. At minimum, and beyond the old suite's coverage where the
old suite is thin:

Password and gate:
- PHC round-trip; verification under older stored parameters succeeds and
  reports stale; a malformed hash refuses without error and reports
  stale; hostile parameter values (negative, over-uint32, oversized key)
  refuse at parse.
- The gate bounds concurrent KDF invocations at `GateConcurrency`
  (observed via `PeakConcurrency` under a burst) and honors context
  cancellation while queued.
- Fuzz: `parsePHC` never panics; `crockfordFold` folds every confusable
  spelling of a minted token to the same canonical form.

Login:
- Unknown user and wrong password: identical error, and both cost a KDF
  run (assert via the gate's high-water mark or a counting hasher).
- Stale-parameter login rehashes; the stored hash's parameters change.
- Disabled account with the right password: `ErrAccountDisabled`; with a
  wrong password: `ErrCredentials`.
- Enrolled account without a factor: `ErrSecondFactor`; wrong factor:
  `ErrCredentials`; right factor: a session.
- **The audit-write regression**: an audit sink that fails does not fail
  a login that minted a session.
- The limiter refuses attempt 11 inside the window and admits after
  reset; **the race test**: N goroutines hammering `Allow` under `-race`.

Sessions:
- Token round-trip; absolute expiry; idle expiry; the idle stamp
  refreshes on use.
- A revoked session refuses immediately (generation, not TTL).
- A disabled account's live session refuses with `ErrAccountDisabled`.
- Owner-scoped revocation by hash refuses another owner's hash.

App passwords:
- Mint, verify, revoke; revocation is immediate despite the tier-3 TTL.
- A wipe-requested token refuses and stays refusing.
- Expiry honored; a disabled owner refuses; scope round-trips including
  multiple share labels.
- Case-folded and confusable-letter presentations verify.

TOTP and recovery:
- The RFC 6238 known-answer vector; the drift window accepts one step
  either side and refuses two.
- Replay: the same code twice inside a window refuses the second time.
- Enrolment drops the NT hash in the same transaction (read the row).
- A recovery code is single-use under two concurrent redeemers: exactly
  one wins (the transaction test).
- Generation replaces the set; remaining counts down.

Accounts and the seam:
- Creation derives and seals the NT hash in the creating transaction; a
  created account is SMB-eligible with no further step.
- A duplicate name is `ErrNameTaken`; an invalid name is `ErrNameInvalid`.
- Password change: old sessions still valid (sessions are not
  credentials), caches invalidated (a cached wrong-password outcome for
  the new password is not served), NT hash re-sealed, passdb republished
  (counting sink).
- Disable: cached credential refuses now; passdb drops the entry.
- TOTP policy `Block` publishes no enrolled account; moving the policy
  back republishes them with no password reset.
- `ClearSMBPassword` reports revertible correctly for the four cases
  (plain, opted out, linked, TOTP-blocked).
- The rendered credential facts agree with the account file on every uid
  (the old `TestTheAccountFileAgreesWithThePassdbOnEveryUid`, rebuilt
  against the seam).
