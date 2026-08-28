# Auth rebuild: overview

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/auth` is referenced as a behavioral specification only. The
> new implementation is written completely from scratch; nothing is copied.

## What auth is

The subsystem every protocol asks "who is this" and "may they try again":
accounts, sessions, app passwords, TOTP, recovery codes, groups, the audit
log, the SMB credential sidecar, and the master key that seals every secret
at rest. The old package is 4,354 lines across 27 files; its audit
(`../audit/service.md`, auth findings 1 through 10) found one real data
race, two layering violations, and a set of verified-sound security
mechanisms the rebuild must carry forward exactly.

The design question the old package answers, and the rebuild keeps
answering, is not "how strong a KDF" but **"how few times must the KDF
run"**. WebDAV clients send hundreds of requests a minute carrying the same
Basic credential; running Argon2id per request would make the server slower
than the disk it fronts. The three-tier verification path is the whole
shape of the package, and everything else hangs off it.

## Package layout

One service package, aggregates in the store:

```
engine/
  service/
    auth/               this phase
      auth.go           Service, Config, New, the generation counter
      errors.go         the sentinel set
      login.go          Login, the decoy defence, the rate limiter (fixed)
      password.go       Argon2id, PHC encode/parse, Stale, the Gate
      session.go        CreateSession, LookupSession, revocation
      apppw.go          app passwords, Scope, Crockford encoding
      totp.go           enrol, verify, replay guard, drift window
      recovery.go       single-use recovery codes
      cache.go          the three tiers and their LRU
      masterkey.go      KeyRing, load/persist, ResolveKeyFile
      keystate.go       startup alignment, checkMasterKey
      rotate.go         RotateMasterKey, the three-step protocol
      seal.go           XChaCha20-Poly1305, the AAD bindings, LinkCipher
      users.go          create, disable, delete, password change
      groups.go         groups and memberships
      admin.go          listing, quota, role surface
      audit.go          the append-only record and its pager
      oidcbridge.go     the durable halves the OIDC package calls through
      smbcred.go        NT hash derivation, SMB state, the passdb seam
      username.go       the one canonical account-name rule
  store/
    state/              new aggregates, beside the existing ones
      authuser.go       user rows: lookup, create, flags, role, quota
      session.go        session rows
      apppw.go          app-password rows
      totp.go           totp_secret and totp_used rows
      recovery.go       recovery_code rows
      oidclink.go       oidc_link and oidc_flow rows
      audit.go          audit rows and the keyset pager
      smbsecret.go      user_smb_secret rows
```

**One auth package, deliberately.** The audit (finding 6) asks whether the
rebuild should split by aggregate the way `store/state` was. It should
not: the package's aggregates are not independent. A login touches users,
sessions, TOTP and audit in one flow; a TOTP enrolment touches the SMB
credential; a password change touches the NT hash, the caches and the
passdb sink. Splitting the domain logic would turn those couplings into
exported cross-package surfaces. What does split out is the SQL: every
statement moves to a state aggregate, following the same file-pair
convention (`<aggregate>.go` + `<aggregate>_sql.go`) `foundation/state.md`
fixed for the rest of the database.

## The documents

| Document | Contents |
| --- | --- |
| [01-credentials-and-sessions.md](01-credentials-and-sessions.md) | The three-tier path, Argon2id and PHC, the gate, login, the decoy, the rate limiter fix, sessions, app passwords, TOTP, recovery codes |
| [02-master-key-and-crypto.md](02-master-key-and-crypto.md) | The key ring, resolve/load/generate, startup alignment, rotation, the seal layer and its AAD bindings, config secrets, LinkCipher |
| [03-oidc-integration.md](03-oidc-integration.md) | The durable halves of the OIDC flow: flow rows, identity links, the reconfirm surface |
| [04-audit-log.md](04-audit-log.md) | The append-only rule, what is recorded where, the never-fail-the-action contract, the keyset pager |
| [05-username-policy.md](05-username-policy.md) | The one canonical rule, who calls it, and the whole-batch failure it ends |

## Dependencies

The service depends on, and only on:

| Dependency | Role |
| --- | --- |
| `engine/store/state` | every durable row, through the new aggregates |
| `engine/store/fsatomic` | the key-ring file replace (the survey's repoint) |
| `engine/kit/secret` | every password and token in flight |
| `engine/kit/clock` | injectable time |
| `engine/kit/limits` | the bounds that are shared product limits |
| stdlib + x/crypto | argon2, chacha20poly1305, md4 (SMB-fixed), hmac/sha1 (RFC 6238-fixed) |

Two imports the old package has do not survive:

1. **`internal/smb` is severed** (audit finding 1). The passdb rendering
   needs `smb.User` and `smb.PasswdEntries`; the rebuild inverts it with a
   seam: auth exposes the credential facts (name, uid, NT hash, enabled)
   and the SMB phase's renderer is wired in through `Config`, mirroring
   the existing `SetSMBPublisher` pattern. Detail in 01 under "The SMB
   credential seam".
2. **`internal/store/dbfile` drops out.** The old package opened its own
   handle for one pragma check; the state DB's own surface covers it.

Nothing here imports fiber, `net/http`, `core`, or `acl`. The one crossing
toward the ACL world is the `OnMembership` callback `Config` carries,
wired by the assembly layer, exactly as today.

## What the rebuild fixes (the changes)

Each is a "Deliberate changes" entry in its document; the roll-up:

1. **The rate-limiter race** (audit finding 3). `limiter` gets a mutex.
   This is the one place in the old package where concurrent logins can
   panic the process. 01 specifies the locked shape and the test that
   would have caught it.
2. **All SQL moves to state aggregates** (audit finding 2). 63 statement
   constants and 16 calling files stop touching `st.SQL()` directly.
3. **The smb import becomes a seam** (audit finding 1), above.
4. **The four hand-rolled narrowings** (audit finding 4) become
   `kit/num.Narrow` calls.
5. **`isUniqueViolation` stops string-matching** (audit finding 5): the
   state aggregate returns a typed `ErrNameTaken` the service maps.
6. **One username rule** (survey finding 8): `username.go` here, called by
   setup, admin creation, and re-exported through the smb phase's
   contract. 05 owns the rule.

## What must not change (the verified mechanisms)

The audit verified these sound (findings 7 through 10); each is normative
and carries its own tests in the documents:

- The **decoy-hash timing defence** and the one-error rule for every
  credential failure.
- **Constant-time comparison** for session tokens and OIDC digests.
- The **three-step rotation protocol** and `alignRing`'s crash recovery.
- The **generation counter**: every credential change bumps it; every
  cache tier compares against it; revocation is immediate without a
  database re-read.
- The **key-in-env refusal** (`SC_MASTER_KEY` present at all is a hard
  error) and the warn-not-refuse decision for a key inside the data
  directory (audit finding 7 asks that this be stated as a decision: it
  is, in 02).
- The **passdb sink discipline**: every credential-changing path
  republishes; a committed transaction is not yet a completed security
  decision until the sidecar file agrees.

## Feature inventory

The old package's public surface, and where each lands. Anything absent
from this table is a defect in this document, not an allowed omission.

| Old surface | Document | Notes |
| --- | --- | --- |
| `Login`, `LoginRequest`, rate limit, decoy | 01 | limiter fixed |
| `CreateSession`/`LookupSession`/`RevokeSession`/`RevokeSessionByHash`/`RevokeSessionsOf`/`Sessions` | 01 | |
| `CreateAppPassword`/`VerifyAppPassword`/`RevokeAppPassword`/`AppPasswords`, `Scope`, `RequestWipe` | 01 | |
| `GenerateTOTPSecret`/`EnrollTOTP`/`VerifyTOTP`/`DisableTOTP` | 01 | replay guard included |
| `GenerateRecoveryCodes`/`UseRecoveryCode`/`RecoveryCodesRemaining` | 01 | |
| `Hash`/`Verify`/`Stale`, `Params`/`CurrentParams`, `Gate`, `MinPasswordLen` | 01 | |
| Tier caches, `Principal`, `Outcome`, `Generation` | 01 | |
| `CreateUser`/`CreateAdmin`/`DeleteUser`/`Disable`/`EnableAccount`/`SetPassword`/`VerifyAccountPassword` | 01 | NT hash derived in the create transaction |
| `UserByID`/`UserIDByName`/`NameOf`/`ListUsers`/`CountUsers`/`HasAdmin`/`IsAdmin`/`SetQuota` | 01 | |
| Groups: `CreateGroup`/`RenameGroup`/`DeleteGroup`/`ListGroups`/`AddToGroup`/`RemoveFromGroup`/`SetMembership`/`GroupIDsOf` | 01 | `OnMembership` crossing preserved |
| `OpenMasterKey`/`RotateMasterKey`/`RotationReport`, `KeyRing`, `LoadKeyRing`, `ResolveKeyFile` | 02 | |
| `SealConfigSecret`/`OpenConfigSecret`, `LinkCipher` | 02 | |
| OIDC: `StartOIDCFlow`/`TakeOIDCFlow`/`CreateOIDCLink`/`RemoveOIDCLink`/`OIDCLinkOf`/`UserForOIDCIdentity`/`TouchOIDCLink`/`LinkOIDC`/`UnlinkOIDC` | 03 | |
| `Audit`/`Record`/`AuditPage`, `AuditFilter`/`AuditRow` | 04 | |
| SMB: `SetSMBPassword`/`ClearSMBPassword`/`SetSMBAccess`/`SMBStateOf`, `TOTPPolicy`/`SetSMBTOTPPolicy`, `PublishPassdb`/`SetSMBPublisher`, `SMBBaseUid` | 01 | the seam |

## Build order

1. The state aggregates (store side), with their tests.
2. `password.go`, `seal.go`, `masterkey.go`: pure crypto over the kit.
3. `auth.go`, `cache.go`, `errors.go`, `username.go`.
4. `session.go`, `apppw.go`, `totp.go`, `recovery.go`.
5. `login.go` (needs everything above), `users.go`, `groups.go`,
   `admin.go`, `audit.go`.
6. `keystate.go`, `rotate.go`.
7. `smbcred.go`, `oidcbridge.go` last: both are seams other phases plug
   into.

## Platform

Pure Go throughout; no build tag. The one filesystem dependency is the key
ring file, which goes through `store/fsatomic` and is portable.
