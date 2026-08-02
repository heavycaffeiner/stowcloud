# Auth design

`sc-auth` crate. Protocol-agnostic. Login Flow v2 is an adapter on
top of this, documented in `proposals/stowcloud-8-compat.md`.

---

## 1. Threat model

| Threat | Defense |
|---|---|
| Credential stuffing | Per-IP + per-account rate limits with escalating delay. No account lockout (that's a DoS an attacker triggers on someone else) |
| DB leak | Passwords: Argon2id. Session tokens and app passwords: hashed at rest. TOTP seeds and NT hashes: encrypted with the master key |
| Session theft (XSS) | Uploaded content served only from a separate, cookie-less origin (`proposals/stowcloud-6-preview-sharing.md`). Session cookie is `HttpOnly` |
| CSRF | `__Host-` cookie prefix + `SameSite=Lax` + a required custom header (§3.3) |
| Account enumeration | Login, password reset, and Login Flow responses are timing- and shape-identical regardless of whether the account exists |
| Login flood exhausting memory | Argon2 concurrency semaphore (§2.2) |
| Argon2 cost DoS via DAV Basic | Process-ephemeral-key verification cache + IP rate gate (§4.1–4.2) |
| 2FA bypass via WebDAV | A TOTP-enabled account can't use its account password over Basic; an app password is required (§4.3) |
| SMB NT hash leak = account password leak | SMB forced to the internal network (primary defense) + master key stored apart from the DB (§2.4, `DEPLOYMENT.md` §7.2/§7.4) |
| 2FA bypass via SMB | A TOTP account can't reach SMB with its account password; a dedicated SMB password is required (§2.4) |
| Login CSRF (an attacker finishes an SSO flow as themselves, then delivers the resulting callback URL to your browser, silently signing you in as them) | `state` alone does not stop this, because the attacker holds a legitimate one. The flow is bound to the browser that started it by a `__Host-sc_oidc` cookie whose SHA-256 is stored beside the state, and a callback whose cookie is missing or does not match is refused before the code is ever exchanged (§13.2) |

---

## 2. Passwords

### 2.1 Parameters

```rust
// Stored as a PHC string, so parameters travel with the hash — changing the
// default doesn't require a migration, just a rehash on next login.
pub const ARGON2_DEFAULT: Params = Params {
    algorithm: Argon2id,
    version:   0x13,
    m_cost:    48 * 1024,   // 48 MiB
    t_cost:    3,
    p_cost:    1,
    output:    32,
};
```

48 MiB rather than 64: it's multiplied by the concurrency cap in §2.2, and the
product has to fit the container's memory budget (48 MiB × 4 = 192 MiB peak).

**Rehash on successful login**: if the stored PHC parameters differ from the
current defaults, the hash is rewritten with the new ones right after
verification succeeds. Raising the config is enough to migrate every account,
one login at a time, without a maintenance window.

**No pepper.** Key-rotation cost and operational complexity outweigh the
benefit — a master-key leak is, in practice, almost always a DB leak too.

### 2.2 Concurrency cap

```rust
static ARGON2_SLOTS: Semaphore = Semaphore::new(config.auth.argon2_parallelism); // default 4

async fn verify_password(hash: &PasswordHash, pw: &SecretString) -> bool {
    let _permit = ARGON2_SLOTS.acquire().await;         // caps peak memory
    spawn_blocking(move || argon2.verify_password(pw, hash).is_ok()).await
}
```

Without this semaphore, 100 concurrent logins would demand 4.8 GB and get
OOM-killed. The wait is invisible to a normal user because it's already
bounded by the rate limits below.

### 2.3 Policy

- **Minimum 10 characters.** No composition rules (forced uppercase/symbols)
  — in practice they make chosen passwords worse. The UI recommends
  passphrases and shows a strength meter instead.
- Changing a password lets the user **choose** whether to also revoke every
  other session and every app password — auto-revoking app passwords too
  would silently break sync clients and flood support. The SMB NT hash is
  always re-derived alongside the change, per §2.4.

> A breached-password blocklist (local top-100k list, HIBP off by default) is
> not implemented — `sc-auth` has no such check today, despite an earlier
> version of this document claiming otherwise. Treat any reference to it
> elsewhere as aspirational until it lands.

### 2.4 Deriving the SMB NT hash alongside the account password

The SMB password **is** the account password. NTLM requires
`MD4(UTF-16LE(password))`, which an Argon2 hash cannot substitute for, so both
derivations happen **at the same moment** — whenever the plaintext is in
hand.

```rust
/// The only points that ever hold the plaintext: account creation, password
/// change, and a successful login (migration only). Both derivations always
/// run, independent of whether SMB is currently enabled.
fn derive_credentials(pw: &SecretString, u: UserId, totp: bool) -> Result<()> {
    db.set_pw_hash(u, &argon2_hash(pw)?)?;                    // web / WebDAV verification

    if !totp {                                                 // carve-out below
        let nt = md4_utf16le(pw.expose_secret());              // 16 bytes
        let ct = aead_seal(&master_key, &nt, aad!("smb_nt", u, key_ver))?;
        db.set_smb_nt(u, &ct, NtSource::AccountPassword)?;     // separate table (§10)
        smb_sync.mark_dirty(u);                                // triggers a passdb rebuild
    }
    Ok(())   // caller zeroizes pw
}
```

**`smb_sync.mark_dirty` is a real call, at most of the sites these snippets
show it.** A serving process with SMB enabled installs the sink behind it
(§13.6), so a password change, either TOTP toggle, a change to the two
self-service SMB toggles, and an OIDC link or unlink each rewrite the
published `smbpasswd` on their own, with
no `sc-server smb-sync` and no restart. Account creation and the
opportunistic backfill further down deliberately do not: both only ever add
a hash, so a file that is one account behind refuses an access rather than
granting one, and the backfill runs on the login path, where rewriting three
files does not belong. Both converge at the next republish or the next
`smb-sync`.

**Deriving and publishing are separate steps** — that split is what makes "SMB
can be turned on at any time" true:

| Step | Condition | Result |
|---|---|---|
| **Derive** (ciphertext written to the DB) | Unconditional at account creation (except TOTP accounts) | Every account is always SMB-ready |
| **Publish** (entry written to `smbpasswd`) | `smb.enabled && user.smb_enabled` | Works the instant an admin turns SMB on, no re-login |

Turning SMB on regenerates `smbpasswd` for every account. No lazy backfill,
no "please log in once" notice — everyone works the moment it's flipped.

**The master key becomes mandatory configuration.** It used to matter only
when SMB was enabled; now account creation always encrypts with it, so it must
exist at startup. If absent, the first boot generates one and writes it to
`SC_MASTER_KEY_FILE` (mode `0600`). Losing that file means **every NT hash
becomes unrecoverable** (a password change or login re-derives it) — both the
startup log and the backup documentation say so plainly.

**One cost is accepted deliberately**: an account that never touches SMB still
has an MD4-derived credential sitting in the DB. The exposure is bounded to
the same "DB + master key leaked together" scenario as everything else in
this section, and SMB being forced to the internal network limits the blast
radius further, so it's accepted. A user who wants to opt out can set
`user.smb_opt_out = true` and suppress the derivation entirely.

**TOTP carve-out**: enabling TOTP **deletes** the NT hash derived from the
account password — there's no reason to keep a credential that can no longer
be used. Setting a dedicated SMB password stores a fresh one as
`NtSource::Dedicated`.

#### The two hashes' lifecycles

**The Argon2 hash is completely independent of TOTP state.** `user.pw_hash`
changes only on account creation and password change; toggling TOTP never
interrupts web/WebDAV login. Use this table as the code-review reference.

| Event | `user.pw_hash` (Argon2) | `user_smb_secret` (NT) |
|---|---|---|
| Account created | created | created, `AccountPassword` |
| Password changed | updated | updated, `AccountPassword` |
| **TOTP enabled** (password re-confirmed) | **unchanged** | deleted |
| Dedicated SMB password set | unchanged | created, `Dedicated` |
| **TOTP disabled** (password re-confirmed) | **unchanged** | **re-derived on the spot**, `AccountPassword` |
| Admin force-disables TOTP | unchanged | absent → re-derived opportunistically on the next **authentication** (any protocol) |
| Plaintext verification succeeds + NT hash absent | unchanged | opportunistic re-derivation |

#### Immediate re-derivation on TOTP disable

Disabling 2FA removes a security control, so it **requires re-confirming the
password** (as it should). The plaintext is in hand at exactly that moment, so
the NT hash is re-derived in the same transaction — there is never a window
where "TOTP is off but SMB still doesn't work."

```rust
fn disable_totp(u: UserId, pw: &SecretString) -> Result<()> {
    verify_password(&db.pw_hash(u)?, pw).await.then_some(()).ok_or(BadPassword)?;
    let tx = db.begin()?;
    tx.clear_totp(u)?;
    tx.clear_totp_used(u)?;
    if !user.smb_opt_out && db.nt_source(u)? != Some(NtSource::Dedicated) {
        let nt = md4_utf16le(pw.expose_secret());                  // plaintext is here
        tx.set_smb_nt(u, &aead_seal(&master_key, &nt, aad!(...))?, NtSource::AccountPassword)?;
        smb_sync.mark_dirty(u);
    }
    tx.commit()?;
    audit(u, "auth.totp_disabled");
    Ok(())   // caller zeroizes pw
}
```

A user already on a dedicated SMB password (`NtSource::Dedicated`) is left
alone — TOTP disable must not silently undo a credential they deliberately
separated.

#### Opportunistic backfill — never a forced re-login

MD4 needs the plaintext and Argon2 is one-way, so "the plaintext is needed
once" can't be removed as a constraint. What's controllable is whether the
user ever notices.

The derivation point is widened from "login" to **"any successful credential
check that had the plaintext in hand"**:

```rust
/// Called on every path where a plaintext password verification succeeds.
/// Protocol-agnostic.
fn backfill_nt_if_absent(u: UserId, pw: &SecretString, p: &mut Principal) {
    if p.nt_present || p.smb_opt_out || p.totp_enabled { return }
    if db.nt_source(u) == Some(NtSource::Dedicated) { return }   // respect a deliberately separate credential

    let nt = md4_utf16le(pw.expose_secret());
    if db.set_smb_nt(u, &aead_seal(&master_key, &nt, aad!("smb_nt", u, key_ver))?,
                     NtSource::AccountPassword).is_ok() {
        smb_sync.mark_dirty(u);
        p.nt_present = true;          // recorded on the cached Principal, no recheck later
    }
}
```

| Call site | Effective delay |
|---|---|
| Web login | Next sign-in |
| **WebDAV / NC Basic (account password)** | **Plaintext arrives on every request — seconds, if any client is connected** |
| Password change | Immediate |
| TOTP-disable reauth | Immediate |
| Sensitive-action password reconfirmation | Immediate |

The DAV path is what makes this design work: with even one sync client
connected, the gap closes in seconds without the user doing anything.

**Interaction with the auth cache**: even when §4.2's connection memo or
verification cache hits, **the plaintext is still sitting in the
`Authorization` header.** Argon2 is skipped but the MD4 derivation still runs
— gated on `nt_present`, so it costs exactly once per account.

The upshot: **"log out and back in" never appears as a required user action.**
What remains is "the next authentication of any kind fills it in
automatically," and the admin UI marks `smb_enabled && NT hash absent`
accounts as "pending" rather than leaving a silent non-working state.

> **Re-authentication and re-login are different things.** Confirming a
> password to disable TOTP is one field in a modal, session intact — not a
> logout. It happens once, for a rare action, and the nature of disabling 2FA
> makes it necessary. "Log out, log back in" as a requirement has been
> eliminated everywhere else.

#### No forced password reset

Forcing a new password on TOTP disable would reach the same goal, but forcing
rotation while the old password is still valid has a documented backfire —
users pick weaker passwords under that pressure. Reauthentication is enough.
An organization that wants rotation can turn on
`auth.rotate_password_on_totp_disable = true`; default is off.

**2FA carve-out**: a TOTP account cannot reach SMB with its account password
— same reasoning as WebDAV: SMB has no way to carry a second factor, so
allowing it would be a bypass.

```toml
[smb]
totp_policy = "require_separate"   # require_separate | block
```

- `require_separate` (default): a TOTP account must set a **dedicated SMB
  password**, distinct from the account password, so it isn't a bypass. Its
  NT hash derives from that dedicated password and is recorded as
  `NtSource::Dedicated`.
- `block`: TOTP accounts can't use SMB at all.

**Cost accepted**: if the DB and the master key leak **together**, the
account password becomes crackable at MD4 speed instead of Argon2 speed. This
is accepted because SMB is forced to the internal network, bounding exposure
to the trusted network. Keeping the master key off the data volume is the
remaining real defense.

---

## 3. Sessions

### 3.1 Cookie

```
Set-Cookie: __Host-sc_sid=<43-char base64url>; Path=/; Secure; HttpOnly; SameSite=Lax
```

The `__Host-` prefix forces the browser to require `Secure` + `Path=/` + no
`Domain`. This blocks a subdomain from planting the cookie — a real threat
here since the content origin is a separate subdomain. Off `localhost`, plain
HTTP silently discards this cookie because of the `Secure` requirement — that
is a deployment fact, not a bug, and worth remembering when a local HTTP test
mysteriously has no session.

### 3.2 Storage

```sql
CREATE TABLE session (
  id_hash     BLOB PRIMARY KEY,   -- sha256(token). Plaintext token is never stored
  user        INTEGER NOT NULL,
  created_ns  INTEGER NOT NULL,
  last_seen_ns INTEGER NOT NULL,
  absolute_expiry_ns INTEGER NOT NULL,
  ip_first    TEXT, ua_first TEXT,   -- display/notification only, never an auth condition
  amr         INTEGER NOT NULL       -- auth method bits: pw | totp | recovery
);
```

- Token: 256-bit CSPRNG. Only the SHA-256 is stored — high entropy means a
  slow hash isn't needed here.
- Idle expiry 7 days (sliding on `last_seen_ns`), absolute expiry 30 days.
- **IP/UA are never an auth condition.** Mobile networks change IP constantly
  and false positives would be severe. They're recorded and shown alongside
  the active-session list instead.
- Rotation: a fresh token is issued and the old one revoked immediately on
  login, 2FA pass, and password change (blocks session fixation).

### 3.3 CSRF

Three layers, and the last two are both required — not either/or — for every
state-changing, cookie-authenticated request:

1. `SameSite=Lax` — the cookie isn't attached to a cross-site POST/PUT/DELETE.
2. An `Sc-Csrf` header is **required**. **Its value is not a secret the client
   invents** — the server derives it from the session token and returns it in
   the `csrf` field of `GET /api/auth/session`'s response body (§3, wire
   shape in `SessionInfoWire`). The client reads it from there once per
   session and echoes it verbatim on every state-changing request. A custom
   header can't be attached by a plain `<form>` submission, which is the
   actual defense — the value being unguessable is secondary.
3. `Origin` is checked against `allowed_origins`. Missing or unlisted →
   rejected.

Both (2) and (3) must pass (`sc-http`'s `csrf` middleware, `DESIGN-API.md`
§9 step 8) — a valid header from a disallowed origin, or a valid origin with
no header, both fail closed with `403`.

---

## 4. Auth matrix by protocol

| Path | Cookie | Basic | Bearer | Notes |
|---|:--:|:--:|:--:|---|
| `/api/**` (web UI) | ✅ + CSRF | ❌ | ✅ | |
| `/api/uploads/**` (TUS) | ✅ + CSRF | ❌ | ✅ | Custom header required, so no `<form>` attack surface |
| `/dav/**` | ❌ | ✅ **account password + app password** | ✅ | Cookies rejected → no CSRF surface at all |
| `/remote.php/**`, `/ocs/**` (NC) | ❌ | ✅ same | ✅ | |
| `content.<host>/**` | ❌ | ❌ | ❌ | Signed URLs only (`proposals/stowcloud-6-preview-sharing.md`) |
| SMB | — | — | — | **Account password** (NTLMv2). Dedicated password only for TOTP accounts (§2.4) |

Basic auth is only accepted **over TLS**. If `X-Forwarded-Proto` isn't
`https` and the socket itself is plaintext: `426 Upgrade Required`.

An account linked to an identity provider is a further carve-out on this
table: it is refused Basic with its **account** password everywhere Basic is
accepted, and it cannot reach SMB with that password at all. App passwords are
unaffected on both. §13.5 has the reasoning and the exact placement of each
refusal, which matters more than it looks.

### 4.1 The problem — Argon2 can't run on every DAV request

A WebDAV client resends Basic credentials on every request over a keep-alive
connection — tens to hundreds of requests per second while syncing.

| | Cost |
|---|---|
| Argon2id (m=48 MiB, t=3) | ~**80 ms**, 48 MiB |
| One client at 100 req/s | 8 CPU-seconds/second + 4.8 GB — **impossible** |

App passwords carry 160 bits of entropy, so SHA-256 verification is enough
and this problem doesn't apply to them. Account passwords need a **cache** of
verification results. The question is what to key it on.

### 4.2 Three-tier verification path

```
request arrives
 ├─ ① connection memo    : same connection, identical Authorization bytes → pass immediately (~20 ns)
 ├─ ② verification cache : HMAC(ephemeral_key, user‖pw) lookup                                 (~300 ns)
 ├─ ③ rate gate          : IP/account token bucket. 429 here, before Argon2, if exceeded
 └─ ④ Argon2 + semaphore : only on a cache miss                                                (~80 ms)
```

**① Connection memo**

```rust
/// Attached to connection state. Shared across an HTTP/1.1 keep-alive
/// connection or an entire HTTP/2 stream set.
struct ConnAuth {
    header_hash: [u8; 32],   // sha256 of the raw Authorization header bytes
    principal:   Arc<Principal>,
    gen:         u64,        // auth_generation snapshot
    verified_at: Instant,
}
```

Every request compares the header hash and one atomic load of `gen` against
the current global generation. A mismatch falls through to ②. **The memo dies
with the connection** — short-lived, and safe because of it.

WebDAV clients hold connections open for a long time, so most real traffic
never leaves this tier.

**② Verification cache**

```rust
pub struct CredCache {
    /// Generated with getrandom at process start. Never written to disk.
    /// A restart makes the whole cache meaningless automatically.
    ephemeral_key: Zeroizing<[u8; 32]>,
    map: Mutex<LruCache<[u8; 16], Entry>>,   // capacity 4096
}

struct Entry {
    outcome:  Outcome,      // Accepted(UserId) | Rejected
    gen:      u64,
    inserted: Instant,
    last_hit: Instant,
}

fn cache_key(&self, user: &str, pw: &SecretString) -> [u8; 16] {
    let mut m = Hmac::<Sha256>::new_from_slice(&*self.ephemeral_key).unwrap();
    m.update(b"dav\x00"); m.update(user.as_bytes()); m.update(b"\x00");
    m.update(pw.expose_secret().as_bytes());
    m.finalize().into_bytes()[..16].try_into().unwrap()
}
```

TTL: **success 15 min absolute / 5 min idle, failure 30 s.**

Caching failures matters: a sync client left with a wrong password retries
the same bad credential dozens of times per second. Without a negative cache,
that alone would consume the entire Argon2 budget.

**Security properties**:
- The cache key is HMACed with a process-ephemeral key. **Nothing that
  survives to disk can be broken with a fast hash** — that property is the
  point of this design.
- An attacker who can dump process memory also gets the ephemeral key and any
  plaintext sitting in an in-flight request buffer, along with the master
  key. Process-memory compromise is already total; the cache adds no new
  risk.
- Plaintext is zeroized right after verification.
- **The cache never survives a restart.** Making it survive would mean
  writing something fast-hashable to disk, which defeats the entire reason to
  use Argon2.

**③ Why the rate gate sits before Argon2**

Sending a different password on every request makes every cache lookup miss,
forcing Argon2 to run each time — that's just brute force. The **same dual
token bucket** used by `/api/auth/login` (§7.1) applies to the DAV Basic path
too. Placing the gate after the cache and before Argon2 matters: a normal
client (cache hit) never touches it.

**Performance**

| Path | Cost | Speedup |
|---|---|---|
| Argon2 (cache miss) | ~80 ms | baseline |
| Verification cache hit | ~300 ns | ~250,000× |
| Connection memo hit | ~20 ns | ~4,000,000× |

Argon2 runs once every 5–15 minutes per client/connection. Even 60 clients
(20 people × 3 devices) restarting simultaneously and all missing at once
serializes through a semaphore of 4 — worst case 1.2 s.

**Argon2 parameters never change by path.** DAV never gets a weaker KDF. The
cache is the answer, not relaxed parameters.

### 4.3 Interaction with 2FA — non-negotiable

> **An account with TOTP enabled cannot use its account password for DAV
> Basic auth.**

Basic auth has no way to carry a second factor. Allowing it would turn
WebDAV into a full 2FA bypass — defeating the point of turning 2FA on at all.

```rust
if user.totp_enabled && matches!(cred, Credential::Basic { kind: AccountPassword, .. }) {
    return Err(AuthError::AppPasswordRequired);   // 401 + guidance
}
```

The response includes clear guidance:
`WWW-Authenticate: Basic realm="…", charset="UTF-8"` plus a body explaining
that two-factor is on and an app-specific password is needed. Without this
message the user has no way to know why they're locked out.

### 4.4 Policy switch and recommendation

```toml
[auth]
dav_account_password = "allow"   # allow | deny
```

- Default `allow`. An admin who wants to force app passwords org-wide sets
  `deny`.
- **The UI recommends app passwords regardless.** An account password can't
  be scope-restricted (§5.2's `scope_perms`/`scope_shares` don't apply), can't
  be revoked per device, and a password change disconnects every device at
  once. The connected-sessions UI badges an account-password session
  "unrestricted" to make the difference visible.

---

## 5. App passwords

### 5.1 Format and storage

```
stow_ + Crockford-Base32(20 bytes)  →  stow_a3k9r-7xm2q-p4v8n-6dh1t-w5zc0
```

- 160 bits of entropy. Grouped in 5s so a user can transcribe it.
- Stored as `sha256(token)` (indexed). High entropy, so Argon2 isn't needed.
- **The plaintext is shown exactly once, at issuance,** and never persisted
  anywhere.

```sql
CREATE TABLE app_password (
  id          INTEGER PRIMARY KEY,
  token_hash  BLOB UNIQUE NOT NULL,
  user        INTEGER NOT NULL,
  name        TEXT NOT NULL,          -- "iPhone sync app", "rclone backup"
  scope_perms INTEGER NOT NULL,       -- Perms bitmask, upper bound of what this token can do
  scope_shares BLOB,                  -- NULL = every share. Else a list of ShareId
  created_ns  INTEGER NOT NULL,
  last_used_ns INTEGER, last_ip TEXT, last_ua TEXT,
  expires_ns  INTEGER                 -- nullable
);
```

### 5.2 Scope

An app password's effective permission is `ACL permission ∩ scope_perms ∩
scope_shares`. **Scope can only narrow, never widen**, an account's own ACL
permission. The UI steers toward the common case — read-only, one share — as
the default when minting a token for a backup tool.

`perms_mask: None` means **unrestricted** (identical to the account's own
access); `Some(bits)` means **restricted**. The wire shape mirrors
`ShareLinkCreate`'s existing `perms` field: omitting `scope` entirely, or
either half of it, keeps minting `Scope::default()` (unrestricted) exactly as
before scoping existed, so old clients that only ever send `{"name": "..."}`
are unaffected.

Enforcement (`sc-http::middleware::scope_gate`, `DESIGN-API.md` §9 step 9)
is a static per-route table, and it **fails closed**: a route this table
hasn't been taught about denies a *restricted* credential outright rather
than granting it by default. An unrestricted credential (`None`) is
unaffected either way — its scope is "the whole account," so an unmapped
route behaves the same as a mapped one.

**Every route under `/api/admin`, plus self-service credential management
(`/api/auth/session`, `/api/auth/app-passwords`, `/api/auth/password`,
`/api/auth/totp/**`, `/api/auth/sessions`, `/api/auth/smb`), is denied to any
app password regardless of scope** — gated on how the caller authenticated,
never on `scope_perms`. No combination of filesystem-capability bits means
"and also administer the account," so a scoped or unscoped app password never
reaches these; without this rule, an unrestricted app password could mint a
sibling with even less restriction, which is a scope that isn't a scope at
all.

**The reference server's "full access" consent is `perms_mask: None`, not
`Some(all_bits)`.** These behave identically on every route the scope table
explicitly maps, but not on the compatibility surfaces (OCS, `status.php`,
Login Flow v2 itself) that have no per-method `Perms` bit to check a mask
against — those refuse any `Some(_)` mask outright, since no bit combination
proves a restricted scope should be let through. Minting the literal
`Some(all_bits)` for "full access" broke a real phone enrollment: the client
walked the whole consent flow, picked "Full access," and then couldn't read
its own capabilities or account info. `None` is the correct encoding of "no
restriction at all," not a verbose way to write every bit down.

### 5.3 Verification cache

A WebDAV client sends tens to hundreds of requests per second; hitting the
DB on every one isn't acceptable:

```rust
/// key = sha256(token), TTL 60s, capacity 4096
static TOKEN_CACHE: Lru<[u8; 32], Arc<Principal>>;
```

Revocation must take effect immediately, so **cache entries also carry the
global `auth_generation`**; deleting an app password or disabling a user
bumps the counter and invalidates everything at once. `last_used_ns` updates
are coalesced to once per 60 s to avoid a write storm.

> **A separate cache from §4.2's `CredCache`.** This one is for app passwords
> (high-entropy → SHA-256 verification); `CredCache` is for account passwords
> (Argon2 verification). Both invalidate off the same `auth_generation`, and
> the connection memo ahead of both (§4.2 ①) hashes the raw `Authorization`
> header, so it absorbs both kinds without distinguishing them.

---

## 6. Two-factor authentication

### 6.1 TOTP

- RFC 6238, HMAC-SHA1, 6 digits, 30-second step, ±1 window (90 s total
  tolerance).
- SHA-1 is used for authenticator-app compatibility; its known weaknesses
  aren't a practical threat in the TOTP construction.
- The seed is AEAD-encrypted with the master key (XChaCha20-Poly1305, AAD =
  user id).
- **Replay defense**: a successful `(user, time_step)` is recorded for 30+
  seconds and a resubmission of the same code is rejected. Without this, a
  stolen code could be replayed immediately.

### 6.2 Recovery codes

10 × 10-character Crockford-Base32. Stored as `sha256`, **single-use**. Using
one notifies the user. At 3 or fewer remaining, the UI prompts for reissue.

### 6.3 2FA flow

```
POST /api/auth/login  {username, password}
  → 200 {status:"ok", ...}                        # no 2FA configured
  → 200 {status:"totp_required", challenge:"<one-shot token, 15 min>"}
POST /api/auth/login/totp  {challenge, code}
  → 200 {status:"ok"}
```

`challenge` is a server-side, one-shot record of partial-auth state. Not a
self-contained token like a JWT — that couldn't be revoked immediately.

### 6.4 Enable/disable require password reconfirmation

```
POST /api/auth/totp/enroll   {password, secret, code}   → verifies the seed, then activates
POST /api/auth/totp/disable  {password}                 → deactivates + re-derives the NT hash (§2.4)
```

If a live session alone were enough to turn 2FA off, anyone with a moment of
unattended access to an unlocked device could remove the control. Both
operations require the password again. As a side effect, disabling captures
the plaintext at exactly the moment needed to re-derive the NT hash.

Disabling also clears `totp_used` records and invalidates every remaining
recovery code. It emits the `auth.totp_disabled` audit event and a user
notification — so a disable the account owner didn't perform is noticeable.

---

## 7. Brute-force defense

### 7.1 Dual rate limit

```rust
struct LoginGate {
    per_ip:      TokenBucket,   // capacity 20, refill 1/10s  → sustained 6/min
    per_account: TokenBucket,   // capacity 10, refill 1/30s  → sustained 2/min
}
```

- **The IP bucket is a hard limit** — exceeding it is an immediate `429`
  without running Argon2 at all (blocks a compute-DoS).
- **The account bucket is a soft delay** — exceeding it doesn't reject, just
  slows the response.

```
delay(n) = min(250ms * 2^(n - 3), 30s)      for n > 3       # n = failure count
```

- **No account lockout.** It would let an attacker lock any account they
  choose, which is itself a DoS. Repeated failures instead trigger a
  notification to the account owner.
- Behind a trusted proxy, `CF-Connecting-IP` is the key. If proxy validation
  fails, the fallback is the socket peer IP — which means every request
  shares one bucket and legitimate users can get blocked, so a proxy
  misconfiguration logs loudly at startup.

### 7.2 Account enumeration defense

```rust
async fn login(req) -> Response {
    gate.check_ip(ip)?;                          // 429 regardless of whether the account exists
    let user = db.find_user(&req.username);
    let hash = user.as_ref().map(|u| &u.hash).unwrap_or(&DUMMY_HASH);
    let ok = verify_password(hash, &req.password).await && user.is_some();
    //         ^ runs real Argon2 even for a nonexistent user, to flatten timing.
    //           The IP bucket already bounds the cost.
    sleep(gate.delay_for(&req.username)).await;
    if !ok { return json(401, err("auth.invalid_credentials")) }   // identical message and code
    ...
}
```

`DUMMY_HASH` is generated once at startup with the current parameters —
hardcoding it would let a parameter change split the timing between real and
dummy accounts.

Password reset, Login Flow v2, and invite links follow the same principle:
response body and status code must never depend on whether the target
exists.

---

## 8. Administrator bootstrap

On first boot with no administrator account:

1. A one-time, 256-bit install token is generated and written to **stdout and
   `<data>/setup-token`** (mode `0600`).
2. `/setup` requires this token; once an administrator account is created,
   the token and route are disabled permanently.
3. Unused after 15 minutes → expires. A restart reissues a new one.

The initial password is never taken from an environment variable — that
would land in `docker inspect` and the process list.

### 8.1 Wire contract

```
GET  /api/setup            → 200 {"required": true|false}
POST /api/setup            {"token","username","password"}
  → 201 {"user":{"id":1,"name":"admin"}}
  → 403 setup.invalid_token / setup.token_expired
  → 410 setup.completed
  → 422 setup.invalid_username / setup.weak_password
  → 429 rate.limited  (+ Retry-After)
```

- **`GET` is a bare boolean and nothing more.** The SPA has to pick between a
  login screen and an admin-creation screen before it holds any credential,
  so something has to be readable unauthenticated — the design question is
  only how much. Token, token length, expiry timestamp (which would leak the
  last restart time), and account names are all withheld; an attacker
  learns nothing here they couldn't already learn by POSTing a junk token and
  reading `410` vs `403`. The moment the first account exists, this becomes
  `false` forever.
- **The token is read from the body only, never a header.** Headers routinely
  land in reverse-proxy/CDN/ingress access logs; bodies essentially never do
  — and this token is a bearer credential for creating an administrator. One
  transport also means one constant-time comparison path to reason about.
- **No session is issued.** The endpoint only creates the account; the client
  then calls `POST /api/auth/login` with the credentials it just chose. This
  avoids a second credential-issuance path, and incidentally lets the SPA
  confirm the just-set password actually works.
- **Rate limit**: burst 10 per IP, then 1 per 30 s. Charged **before** the
  body is parsed. The general API limiter (60/s) is far too loose for an
  unauthenticated admin-creation endpoint.
- Token comparison is `subtle::ConstantTimeEq`. Expiry is checked **before**
  the comparison, so a request arriving after the window closes learns
  nothing about the token's correctness.
- Username/password validation happens **after** token verification and
  **before** the token is spent — a weak password must not burn the only
  chance to create an administrator.

**What "permanently disabled" means**: the source of truth is **"does the
account exist"** (see `crates/sc-server/src/setup.rs`'s module docs). A
process-local flag doesn't survive a restart; a separate marker row/file
duplicates a fact the `user` table already states and can drift on backup
restore — and a drift reopens admin creation on a live system. The in-memory
token slot is a **narrower** second gate only: one-time use under concurrent
requests, and the expiry check.

### 8.2 A token that is in the git history

`24208e9` committed a real `setup-token` from a local run; `5fbecce` removed
it. It is absent from HEAD, but a `git log -p` still shows the value, so it is
written down here rather than left for someone to find and have to re-derive.

It grants nothing, on three independent counts, any one of which is sufficient:

- **It is not in circulation.** The only token `complete()` will accept is the
  one *this process* minted into its in-memory slot. Nothing in the server
  reads the file back — `read_existing` has test callers only — so a token
  from a process that has exited cannot be presented to any later one.
- **It expired.** The file's second line is the expiry, 15 minutes after it
  was issued.
- **Setup is closed.** Every deployment that exists has an account, and step
  (1) of `complete()` returns `410` before it looks at the token at all.

No history rewrite, therefore: it would rewrite every commit since for a value
that three separate gates already refuse. What was worth fixing is the reason
it could be committed — the ignore rule was root-anchored and the token is
written to `<data_dir>/setup-token`, which is wherever `sc.toml` points.

---

## 9. Audit log

```sql
CREATE TABLE audit (
  ts_ns    INTEGER NOT NULL,
  actor    INTEGER,              -- NULL = unauthenticated
  event    TEXT NOT NULL,        -- stable string key
  target   TEXT,                 -- virtual path or resource id, never a host path
  ip       TEXT, ua TEXT,
  result   INTEGER NOT NULL,     -- 0 success / 1 failure
  detail   TEXT                  -- JSON
);
CREATE INDEX audit_ts ON audit(ts_ns);
CREATE INDEX audit_actor ON audit(actor, ts_ns);
```

Recorded: `auth.login`, `auth.login_failed`, `auth.logout`,
`auth.totp_enrolled`, `auth.password_changed`, `apppw.created`,
`apppw.revoked`, `acl.grant_changed`, `share.link_created`,
`share.link_accessed`, `fs.delete`, `fs.permanent_delete`, `admin.*`,
`smb.enabled`.

**Never a host real path.** The audit log is visible to admins, but a leaked
log file must not also hand over internal filesystem layout. Retention
defaults to 180 days, configurable.

---

## 10. Schema summary

```sql
CREATE TABLE user (
  id INTEGER PRIMARY KEY,
  name TEXT UNIQUE NOT NULL COLLATE NOCASE,   -- case-insensitive unique; homoglyph check is separate
  display TEXT,
  pw_hash TEXT NOT NULL,                      -- PHC string
  totp_secret BLOB,                           -- AEAD ciphertext
  disabled INTEGER NOT NULL DEFAULT 0,
  role INTEGER NOT NULL DEFAULT 0,            -- 0 = ordinary account, 1 = administrator (§11)
  quota_bytes INTEGER,                        -- NULL = unlimited
  created_ns INTEGER NOT NULL
);
CREATE TABLE group_ (id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL);
CREATE TABLE membership (user INTEGER, group_ INTEGER, PRIMARY KEY(user, group_));
CREATE TABLE recovery_code (user INTEGER, code_hash BLOB, used_ns INTEGER);
CREATE TABLE totp_used (user INTEGER, time_step INTEGER, PRIMARY KEY(user, time_step));
CREATE TABLE login_challenge (
  token_hash BLOB PRIMARY KEY, user INTEGER, expires_ns INTEGER, amr INTEGER
);

-- The NT hash lives in its own table. No ordinary user-lookup path touches
-- it, so there's no structural way for it to leak into an admin API response
-- by accident.
CREATE TABLE user_smb_secret (
  user       INTEGER PRIMARY KEY,
  nt_hash_ct BLOB NOT NULL,     -- XChaCha20-Poly1305. AAD = ("smb_nt", user, key_ver)
  key_ver    INTEGER NOT NULL,
  source     INTEGER NOT NULL,  -- NtSource: 0 = derived from account password / 1 = dedicated SMB password
  updated_ns INTEGER NOT NULL
);
```

Row existence means "SMB-ready." Created unconditionally at account creation
(§2.4), so under normal conditions it's 1:1 with `user`; the only accounts
without one are TOTP-enabled accounts, `smb_opt_out` accounts, and accounts
where we never held the plaintext (admin force-disabled TOTP, or a pre-SMB
upgrade) — those fill in opportunistically on the next authentication.

```sql
ALTER TABLE user ADD COLUMN smb_opt_out INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user ADD COLUMN smb_enabled INTEGER NOT NULL DEFAULT 1;  -- passdb publish toggle
```

Single sign-on adds two tables and changes none of the above (§13):

```sql
-- One local account's link to one identity at the configured IdP. `subject`
-- is the ID token's `sub`; no other claim is read or stored.
CREATE TABLE oidc_identity (
  issuer     TEXT    NOT NULL,      -- must equal the configured issuer exactly
  subject    TEXT    NOT NULL,
  user       INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  linked_ns  INTEGER NOT NULL,
  last_login_ns INTEGER,            -- display only, never an authentication condition
  PRIMARY KEY (issuer, subject)
);
CREATE UNIQUE INDEX oidc_identity_user ON oidc_identity(user);   -- one identity per account

-- One in-flight authorization-code round trip. The same nature as
-- `login_challenge`: server-side, single-use, short TTL (10 minutes).
CREATE TABLE oidc_flow (
  state_hash    BLOB PRIMARY KEY,   -- sha256(state)
  binding_hash  BLOB NOT NULL,      -- sha256(the __Host-sc_oidc cookie). §13.2
  nonce_hash    BLOB NOT NULL,      -- sha256(nonce)
  code_verifier TEXT NOT NULL,      -- PKCE; plaintext by necessity, see below
  mode          INTEGER NOT NULL,   -- 0 = login, 1 = link
  link_user     INTEGER,            -- NOT NULL only when mode = 1
  return_to     TEXT,               -- validated same-origin path; NULL = the mode's default
  created_ns    INTEGER NOT NULL,
  expires_ns    INTEGER NOT NULL
);
CREATE INDEX oidc_flow_expiry ON oidc_flow(expires_ns);
```

`code_verifier` is the one column here that is not a digest, and it cannot be
one: PKCE requires presenting the original value to the token endpoint. On its
own it redeems nothing, since a code exchange also needs the `code` the IdP
hands the browser and the client secret this server holds. The row is
single-use and lives ten minutes. Expired rows are swept opportunistically on
each callback and by the same periodic job that cleans up sessions.

`user.role` was added by a migration (`sc-auth::db::migrate_user_role`) to
databases created before the admin-role model existed. Before that column,
"administrator" was implicitly `id == 1`; the migration promotes that account
so an existing deployment doesn't lose its only administrator. A fresh
install leaves `role` at its default and lets `/api/setup` promote the
account it creates explicitly — see §11.

`user_smb_secret` is reached only through a dedicated repository type that
doesn't implement `Serialize` — serialization not compiling at all closes the
leak path at the type-system level, not just by convention.

Usernames carry a `COLLATE NOCASE` unique constraint, plus a separate
**homoglyph check** (impersonating an admin with Cyrillic `а` in place of
Latin `a`). New signups/creations are checked against existing usernames for
mixed-script similarity and rejected — unlike filenames, blocking is the
right call here.

---

## 11. Roles

An account is either ordinary or an administrator — `user.role` (§10),
exposed at the API/domain layer as `is_admin: bool` (`GET /api/auth/session`'s
`user.is_admin`, `AdminUserWire::is_admin`). There is no third role today.

`sc-http::routes::require_admin` re-reads the account fresh from the DB on
every admin-gated request rather than trusting anything cached on the
session's `Principal` (which carries only a `UserId`) — a role change takes
effect on the very next request, not on the next login.

**The deployment can never end up with zero active administrators.**
`sc_auth::AuthService::set_admin`, `disable_user`, and `delete_user` all check
`is_last_active_admin` and refuse with `AdminGuardError::LastAdmin` (HTTP
`409 admin.last_admin`) if the target is the only administrator who is both
`role = 1` and not disabled. A disabled admin doesn't count as "the last
admin" for this guard — disabling a second admin while a first is already
disabled is refused too, correctly, since only the still-active one is
covering the deployment. The guard is unconditional, not a UI confirmation
step: `DELETE /api/admin/users/{id}` has no separate "are you sure" gate on
the server side precisely because this backstop exists.

Promoting or demoting an account (as opposed to disabling one) is not yet
exposed over HTTP — `PATCH /api/admin/users/{id}` accepts only `disabled`
today. The only way an account becomes an administrator right now is
`/api/setup`'s bootstrap promotion (§8) or the `role` migration (§10).

---

## 12. Grants and the access model

There are no user home directories, and a new account starts with **no
access to anything**. A share becomes visible to an account only once an
admin creates a grant for it — deny by default, not the traditional
"everyone gets their home plus whatever's shared" default. The
admin UI opens a per-user grant editor automatically when a new account is
created, so this isn't a silent trap.

Grants are persisted in their own store, `<data>/acl.db`, deliberately
separate from `sc-meta`'s disposable cache: `sc-meta` can be deleted and
rebuilt from the filesystem at any time (`ARCHITECTURE.md` §0.1), but nothing
about a directory's on-disk state says "user 7 may read this and not that
subdirectory" — a grant is not reconstructible, so it can't live somewhere
documented as disposable.

**Upgrade preservation, applied once per data directory.** The very first
time this store opens against a data directory that already had accounts, it
reproduces the old startup-time behavior it replaces (every enabled account
got full access to every share, recomputed from nothing on every boot) as
ordinary, revocable grant rows — so an upgrade doesn't lock existing users out
of everything they could already reach. A brand-new install has no prior
accounts to preserve and seeds nothing, except the bootstrap administrator
created by `/api/setup`, which gets full access on every registered share
once, mirroring the convenience of a built-in administrator without
reintroducing "everyone gets everything" for accounts created afterward. The
migration marker is written unconditionally on that first open, so an admin
who later revokes every grant does not get them silently reseeded on the next
restart — otherwise "no access" could never actually be configured.

**A share the caller has no grant on answers `404`, not `403`.** A `403`
would itself confirm the share exists; `404` is the same answer given to a
share that doesn't exist at all.

Routes: `GET /api/admin/shares` (the deployment's registered shares),
`GET /api/admin/grants[?user=|group=][&share=]`, `POST /api/admin/grants`,
`PATCH /api/admin/grants/{id}` (allow/deny/inherit/label only — principal,
share and subpath identify a grant and are not patchable; delete and
recreate to change what a grant *is* rather than what it allows),
`DELETE /api/admin/grants/{id}`. A delete takes effect immediately — it's
pushed into the live ACL engine before the response returns, not on the next
restart.

---

## 13. Single sign-on (OIDC)

`sc-oidc` holds the protocol (discovery, JWKS, JWS verification, PKCE, the
token exchange); `sc-auth` holds the two tables and every policy decision;
`sc-http` holds the eight routes. The split exists for dependency isolation:
the outbound TLS stack and HTTP client this needs did not exist in the
workspace before, and putting them in `sc-auth` would link rustls into
`sc-server smb-sync` and every other CLI path. It is a cargo feature
(`oidc`, on by default) with its whole footprint in `sc-server/src/oidc.rs`,
so `--no-default-features` drops rustls, `ring` and the compiled-in root
certificates out of the binary while the routes stay and answer
`oidc.disabled`.

### 13.1 Link-only, and why there is no JIT provisioning

**A session is issued only when `(issuer, subject)` already maps to a local
account.** An identity the IdP authenticated but nobody linked gets
`oidc.not_linked`, and no account is created.

§12 is the reason. A new account here sees nothing until an admin grants it
something, so "anyone with an account at the IdP can sign in" would create one
grant-less account per person in the IdP tenant: logins that succeed and show
an empty screen. Linking inverts that. Somebody who can already sign in
attaches their IdP identity, or an admin attaches it for them.

Authorization is untouched. `user.role` and the grant table remain the only
truth about what an account may do, and no claim is mapped to either. `sub` is
the only claim read at all: `email` is mutable at most providers and
`email_verified` is not uniformly trustworthy, so matching on either would
make "who is this" a thing the IdP could change under us.

### 13.2 The flow, and what actually stops login CSRF

`GET /api/auth/oidc/start` mints four independent 256-bit CSPRNG values, all
for one attempt: `state`, `nonce`, the PKCE `code_verifier`, and a browser
binding value. Three are stored as SHA-256 in `oidc_flow`; the verifier is
stored in the clear because PKCE requires presenting the original to the token
endpoint, and it cannot redeem anything on its own without the `code` and the
client secret. The binding value goes out as `__Host-sc_oidc`
(`Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=600`). TTL is ten minutes.

The callback's order is the security property, so it is fixed:

```
1. is OIDC on at all                       -> oidc.disabled
2. per-IP rate gate                        -> 429, the one status code this route answers with
3. take the flow by sha256(state)          -> single-use: read and delete in one transaction
4. constant-time compare the flow cookie   -> oidc.bad_state
5. expiry                                  -> oidc.expired
6. only now, look at what the IdP sent
7. exchange the code, verify the ID token  (§13.3)
8. resolve (iss, sub) to an account, check `disabled`
```

Steps 3 and 4 are the whole of the CSRF answer, and they run *before* step 6
on purpose. `state` stops an attacker choosing the value the callback carries.
It does not stop an attacker starting a flow themselves, authenticating at the
IdP as themselves, and then getting a victim's browser to make one top-level
GET to the resulting callback URL: the flow record is real, the nonce matches,
and the victim ends up holding a session for the attacker's account, uploading
files into it. RFC 9700 requires `state` to be bound to a user agent, and the
cookie is that binding. Every callback outcome expires the cookie on the way
out.

`error` and `code` are handled after the flow is consumed and the binding
checked, not before. An IdP echoes `state` on its error redirect too, so
handling `?error=` first would both leave "every callback is state-protected"
false and leave the flow record unconsumed and replayable.

The callback answers with a redirect for everything except the rate limit,
because a human arrives at this URL in a browser and a JSON error body renders
as a white page of JSON. The symbolic code rides on the redirect as
`?oidc_error=`, and the two landing screens (`/login`, `/settings/security`)
translate it. A rate limit is the exception because it is refused before a
flow exists, so there is no mode to decide a landing screen from.

The CSRF middleware is not touched. `STATE_CHANGING` in `middleware.rs` covers
POST/PUT/PATCH/DELETE, so a GET callback already passes; adding a prefix
exemption for `/api/auth/oidc/` would have silently exempted
`POST /api/auth/oidc/link/start`, which requires `Sc-Csrf` and `Origin`.

`/config` and `/start` are in `is_public_path`. The callback is **optionally**
authenticated: no cookie is fine (login mode), a valid one injects a
`Principal` (link mode), and an invalid one is treated as absent rather than
`401`, since link mode refuses it a step later anyway.

### 13.3 ID token verification

Eleven steps, in this order, and the order is deliberate: structural checks
cost nothing and run first, and no claim is trusted before the signature.

```
1.  alg is RS256 or ES256           `none` and HS* rejected by construction
2.  header has a kid               no "there is only one key" fallback
3.  kid found in the JWKS cache    miss triggers a refetch, per-kid cooldown
4.  the JWK's kty matches the alg  RSA/RS256, EC+P-256/ES256, use/key_ops sane
5.  signature verifies             ring, no JWT library
6.  iss equals the configured issuer, exactly
7.  aud contains client_id         string or array
8a. multiple aud implies azp exists and equals client_id
8b. azp present implies it equals client_id, at any aud count
9.  exp/iat/nbf within 60s leeway  fixed, not a setting
10. nonce present and sha256(nonce) equals the flow's, constant time
11. sub is a non-empty string
```

Step 1 is why `none` and the HMAC family are absent from the allow list rather
than checked for: with an HMAC alg the *public* key becomes the verification
key, so a token signed with the RSA modulus as an HMAC secret would verify.
Step 2 refuses the "only one key in the set, use it" fallback for the same
family of reasons. 8a and 8b are two obligations, not one: OIDC Core §3.1.3.7
item 5 requires `azp` when `aud` is plural, item 6 requires `azp == client_id`
whenever `azp` is present at all. Implementing only 8a would pass every token
with `aud = "us"` and `azp = "some other client"`, which is exactly a token
minted for a different client at the same IdP being replayed here.

`ring` does both signature families, so ID token verification adds no crate
beyond the TLS stack. `rsa` is under RUSTSEC-2023-0071 and `deny.toml`'s
ignore list is empty on purpose; `jsonwebtoken` wraps `ring` anyway. OIDC Core
permits skipping signature verification on the code flow and relying on the
token endpoint's TLS: not taken, because it would put the entire defence on
one connection and saves nothing when `ring` is already linked.

### 13.4 Discovery and JWKS, fetched lazily

Nothing is fetched at startup. An IdP that is down must not stop this server
from booting, so the first fetch happens on the first flow.

| item | policy |
|---|---|
| discovery (`{issuer}/.well-known/openid-configuration`) | fetched on demand, cached 1 hour. The document's own `issuer` must equal the configured one or the whole document is refused |
| JWKS | from the document's `jwks_uri`, cached 1 hour |
| unknown `kid` | refetch immediately, with a **per-`kid`** 5 minute cooldown (64-entry LRU) plus a 60 second global floor |
| known `kid`, signature fails | one refetch and one retry. Providers do rotate key material while keeping the `kid`, and without this every login fails for the rest of the cache hour |
| any fetch failure | `oidc.provider_unavailable` on the OIDC routes only. Nothing else on the server is affected |
| response body | capped at 256 KiB, so a hostile or broken IdP cannot exhaust memory |

A per-`kid` cooldown rather than a global one: with a global one, a single
forged `kid` burns the window, and a real key rotation arriving a second later
is locked out of the cache for five minutes while every login fails.

**SSRF.** The draft required every endpoint to share a host with the issuer.
That was wrong and would have made Google Workspace permanently unusable
(issuer `accounts.google.com`, token endpoint `oauth2.googleapis.com`, JWKS
`www.googleapis.com`), and Discovery does not require it. What is enforced
instead: HTTPS on every endpoint URL, and a refusal of loopback, RFC 1918,
link-local, unspecified, IPv6 unique-local and IPv4-mapped-private addresses,
unless `oidc.allow_private_endpoints` is set. The address check runs in two
places, and needs both: on the URL, which catches an IP literal with no
resolver at all, and inside hyper's resolver, which is where a hostname is
decided and therefore the only place that closes DNS rebinding.

### 13.5 What a link does to the other authentication paths

| situation | behaviour |
|---|---|
| linked account, web password login | allowed by default. `oidc.local_password_login = "deny"` refuses it |
| linked account, DAV Basic with the account password | refused, `AppPasswordRequired` |
| linked account, DAV Basic with an app password | allowed |
| linked account, SMB with the account password | impossible: the NT hash is deleted at link time, and the published `smbpasswd` is rewritten to match (§13.6) |
| linked account that also has TOTP | independent. A password login still demands TOTP; an OIDC login does not, because the IdP already ran its own second factor |
| `disabled` account, OIDC login | refused, and with the same code an unlinked subject gets |

**Both refusals happen after the password verifies, and that placement is the
point.** The existing `totp_enabled` check in `basic.rs` returns before the
rate gate and before Argon2, which makes a wrong password against a TOTP
account measurably cheaper than a wrong password against an ordinary one: an
oracle for which accounts have 2FA. Copying that placement for `oidc_linked`
would have added a second oracle, for which accounts use SSO, in direct
contradiction of §7.2. So the OIDC refusals sit after verification, and both
of them fail closed: a storage error reading the link resolves to refusing,
never to letting the password through.

The pre-existing `totp_enabled` placement is deliberately **not** moved.
Relocating it changes the observable behaviour of every TOTP account, which is
a different change from adding a provider. It is filed separately. The two
checks therefore sit at different points in the same function, which the code
comments there say out loud.

**`oidc.local_password_login` defaults to `allow`, and that is a real
trade-off, not an oversight.** `deny` is stricter: under `allow`, somebody who
knows the local password can sidestep the MFA the IdP enforces. The default is
still `allow` because `deny` has no recovery path. If the IdP goes down or the
client registration breaks, nobody gets in, including the administrator who
would have to fix it. This document has refused that class of state
consistently (§7.1 no account lockout, §11 never zero active administrators).
`deny` is the right setting for a deployment with a second way in, such as an
administrator who is deliberately left unlinked.

That key is also readable-but-not-writable from the settings screen, along
with `oidc.client_secret_file`. `Config::apply_settings_overrides` lets a
stored admin override beat `config.toml` on every boot, so an operator who set
`deny` from the screen and then lost their IdP could not undo it by editing
the file: the override would win again at the next start. Both keys are shown
with that reason attached. The rest of `[oidc]` is editable and
restart-required.

### 13.6 SMB, and the half of an unlink an admin cannot do

The DAV carve-out is an authentication-time check, so `|| oidc_linked` covers
it. **SMB is not**, and assuming it was is the single biggest thing the draft
of this design got wrong. smbd authenticates against the published
`smbpasswd` and never calls `sc-auth` at all. The TOTP carve-out works by
never deriving the NT hash in the first place, and both of the places that
decide that hold the plaintext (§2.4). At the moment an identity is linked
there is no plaintext, so neither place is reached, and an account that had a
working NT hash before linking keeps it: SMB stays open with the account
password, which is precisely the bypass linking exists to close.

So linking does the work explicitly, in the same transaction as the insert:

1. delete the `user_smb_secret` row when its `source` is `AccountPassword`.
   A `Dedicated` row is left alone, since the user deliberately separated that
   credential.
2. republish the passdb. Without this the DB is clean and the published
   `smbpasswd` still holds the entry, so SMB stays open anyway.
3. add `oidc_linked` to the derivation gates, so nothing puts it back. Three
   of them: password change, the opportunistic backfill, and the
   re-derivation that runs when TOTP is disabled. Account creation is not one
   and does not need to be, since a brand-new account cannot already be
   linked. The password-change gate matters most: without it a linked user
   changing their password would hand themselves a working SMB credential
   back, and neither they nor an administrator would have any reason to think
   so.

**Step 2 is a seam, and `sc-server` is what is plugged into it.**
`PassdbSink` is a callback because rewriting `smbpasswd` needs the share
projection, the bind check and the config directory, all of which live in
`sc-server`, several layers above `sc-auth`. The implementation is
`sc-server/src/passdb.rs`, and the shape it takes is the one §2.4's
pseudocode names: `republish` marks the passdb dirty and returns.

Nothing renders inside the caller's transaction, and that is deliberate.
The sink hangs off `AuthService` and the render reads the same
`AuthService` back (`export_smbpasswd`, `list_users`), so a synchronous
callback would be calling into the service from inside one of its own write
paths. A thread waits on the flag instead, waits 250 ms more so the writes
behind one admin action collapse into a single file, clears the flag before
it reads the database rather than after (clearing after would swallow a
change that commits mid-render), and renders through the settings bridge,
which is the only holder of the live config and already knows how: it is the
same `smb_cmd::render_live` that saving SMB settings calls.

Four conditions on "linking closes SMB", none of them hidden:

| condition | why |
|---|---|
| the sink exists only when `smb.enabled` was true **at startup** | a deployment with SMB off publishes no file for a change to be stale in. Turning SMB on from the settings screen already answers `restart_required`, and that restart is what installs the sink |
| only the serving process installs one | `gc`, `smb-sync` and `masterkey rotate` build their own short-lived `AuthService` and render explicitly. `smb-sync` writes the same three files, so a publisher underneath it would be racing it |
| a failed render is logged, not raised | the database change committed before the mark was ever set, and the caller is gone. The file stays stale, which is what every build before this one did, so the fallback is the old behaviour plus an `error!` naming `smb-sync` as the fix |
| the flush is at shutdown, not at startup | nothing republishes when the server comes up, so the shutdown sequence publishes anything still marked before it joins the thread. A `kill -9` inside that window leaves the file one change behind until the next change or an `smb-sync` |

**It is not only the OIDC paths.** The published file has to follow the
database, and it was not following it anywhere: `set_password`,
`set_smb_settings`, `totp_enroll` and `totp_disable` all left it stale the
same way. All four mark the passdb dirty now. The one that mattered most is
`set_password`, which re-derives the hash and so left the *previous*
password working over SMB, and which this section's own recovery procedure
depends on.

Two derivation sites stay silent on purpose: `create_user` and the
opportunistic backfill. Both only ever *add* a hash, so a published file
that is one account behind refuses an access that should have been allowed
instead of allowing one that should have been refused, and the backfill runs
on the login path, where rewriting three files does not belong. Both
converge at the next republish or the next `smb-sync`.

`oidc.smb_policy` has exactly one value, `block`. TOTP's `require_separate`
has no counterpart here because **nothing in this product can create a
dedicated SMB password today**: `POST /api/auth/smb` takes `{opt_out,
enabled}` and the UI is two toggles, so `NtSource::Dedicated` has no
user-facing way to come into existence. Offering `require_separate` would be
documenting a setting that cannot be satisfied.

**Unlinking is asymmetric, and the asymmetry is announced rather than hidden.**
`DELETE /api/auth/oidc/link` takes the account password, so it re-derives the
NT hash on the spot and republishes it.
`DELETE /api/admin/users/{id}/oidc`
has no plaintext and therefore cannot. It answers `200` with
`smb_nt_restored: false` and a note saying so, rather than the `204` the API
sketch first specified, because a `204` has no body to say it in. SMB stays
closed for that account until its owner changes their password (any password
change re-derives, and now republishes, so that is a whole recovery
procedure and not half of one) or a dedicated SMB password is set. The admin
UI's unlink confirmation says the same thing before the operator commits.

Both unlink paths also delete every session with `AMR_OIDC` set and bump the
account's generation. `validate_session` looks at expiry and `user.disabled`
and nothing else, so removing the identity row alone would leave every session
the IdP had already vouched for alive and working, and "cutting the link cuts
the access" would be false.

### 13.7 `session.amr`, honestly

§3.2 describes `amr` as a `pw | totp | recovery` bitmask. The code did not do
that: `create_session` wrote the literal `1` for every session, including
TOTP-gated logins, so the TOTP bit was never set anywhere.

This change makes `amr` a parameter and names the bits (`AMR_PASSWORD = 1`,
`AMR_TOTP = 2`, `AMR_RECOVERY = 4`, `AMR_OIDC = 8`). Every pre-existing call
site passes `AMR_PASSWORD`, preserving today's behaviour exactly, **including
the fact that a TOTP-gated login still does not set `AMR_TOTP`**. Fixing that
is a separate change with its own visible consequences, and this one only
needed the OIDC bit to be right so that unlinking can select the sessions it
must revoke.

`amr` also does not reach the screen. `list_sessions` reads it, the HTTP route
drops it, and `web/src/lib/api/types.ts` has no field for it. Recording it
correctly is therefore not the same as being able to tell an OIDC session
apart in the connected-sessions list, and this design does not claim the
latter.

### 13.8 Not built, and not promised

- **Multiple providers.** `issuer` is in the primary key so a row can move
  between providers without a schema change, but one identity per account
  (`oidc_identity_user`) would have to be dropped first.
- **Automatic matching on `email` or `preferred_username`.** Refused, §13.1.
- **RP-initiated, back-channel and front-channel logout.** Signing out of the
  IdP does not sign out of here.
- **Refresh tokens and IdP session synchronisation.** The tokens are used once
  at login and discarded. A session, once issued, lives by this server's own
  rules.
- **OIDC for WebDAV or compat clients.** File protocols use app passwords.
- **`oidc.ca_bundle_file`.** Planned, then dropped: `sc-oidc`'s TLS client
  compiles in the Mozilla root set and exposes no hook for extra roots, so the
  key would have been a setting the server ignores. An IdP with a private CA
  is not supported today.
- **A CI-proven TLS path.** Every `sc-oidc` test runs against an in-process
  fake behind the `HttpFetch` trait and opens no socket, which is what makes
  the flow testable on a runner with no IdP. The real rustls path is exercised
  by an `#[ignore]`d manual test only. This document does not claim CI proves
  it.
