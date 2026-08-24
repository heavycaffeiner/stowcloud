# Phase 3: auth and ACL

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-6-auth-and-acl.md`.

## Scope

Accounts, the three-tier verification path, sessions, app passwords, TOTP,
recovery codes, groups, the master key, and grants.

Depends on Phase 2.5. Blocks Phases 4, 5 and 11.

## Milestones

3a to 3d exactly as the proposal's corrected table has them. The table includes
`credcache.go`, `groups.go`, `crockford.go` and `masterkey.go`.

## Traps

- **§4.3.2a is the answer to this subsystem's whole question.** §2.0 asks how
  few times the KDF must run; the three tiers are the answer, and a port that
  skips them makes the server slower than the disk it fronts.
- **Rejections are cached too**, for a shorter window. Without that, a client
  looping with a wrong password costs an Argon2 invocation every time, which is
  the denial of service arriving from the direction nobody watches.
- **Invalidation is a generation counter, not a sweep.** That is what makes
  revocation immediate on a surface that never re-reads the database.
- **The tier-2 key is an HMAC under a per-process ephemeral key.** A hash of
  the password would be offline-attackable from a process dump.
- **Argon2 goes through the gate on every path**, including account creation,
  password change and TOTP enrolment. Bypassing it there is the exact bug the
  Rust tree's own test asserts against.
- **An unknown user costs what a known one costs**, in time as well as in
  response body. The timing band is a test, not an aspiration.
- **Crockford folding happens before hashing**, at minting and at verification
  alike. The stored value is a hash of the string, so folding at a later decode
  step still fails. The current tree only encodes.
- **The master key**: a *path* may come from the environment, the key never
  may, and `SC_MASTER_KEY` being present at all is a hard error regardless of
  its value. Resolving inside the data directory is a **warning**, not a
  refusal, because that is the default location.
- **The startup decrypt check is asymmetric.** Fatal on a TOTP secret, a
  warning on an NT hash. Making both fatal took production down once over one
  stale row.
- **Adding `key_ver` to AAD requires re-sealing every existing TOTP and
  recoverable share-link ciphertext in one state transaction.** TOTP binds to
  the user and share links bind to the token hash. AAD is authenticated, so a
  silent addition makes old ciphertexts undecryptable. A version-0 link token
  that the current key cannot open was already broken by a Rust key rotation;
  clear only its owner-copy ciphertext, preserve the public hash, and report it.
- **Key rotation is a recovery protocol, not one transaction.** Persist a ring
  containing old and new keys, commit every re-sealed row plus the database
  version, then compact the ring. Startup completes or rolls back the file side
  according to the committed database version. SQLite and a file rename cannot
  be one atomic commit.
- **Revocation must reach the SMB passdb.** Six paths, §4.3.8a. The test is the
  file's contents, not the database row. Phase 11 writes the file; this phase
  builds the sink every path calls.
- **Eight permission bits.** `DOWNLOAD` is not `READ` and `MOVE` is not
  `RENAME`. Collapsing the first widens every view-only grant; collapsing the
  second lets an account move a file out of its only granted subtree.
- **Extend the Rust importer with this phase's durable auth state.** Preserve
  `key_version` and `user_smb_secret`, retain only live `totp_used` replay
  steps, and report expired login and OIDC challenges as transient. A missing
  `key_version` table in a database that predates it means version 1; an empty
  or malformed declared table is corruption. A missing user or recorded key
  version on an SMB ciphertext is a refusal, not a dropped row.

## Done when

- The gate is green, including `-race`.
- The enumeration test shows an unknown user and a wrong password producing the
  same status, the same key and a duration inside the stated band.
- The concurrency test proves no path reaches Argon2 outside the gate.
- Each of the six revocation paths has a test asserting the passdb sink fired.
- The importer preserves SMB ciphertexts and live TOTP replay rows with their
  key versions, and no longer refuses a source merely because Phase 3 state is
  present.
- Fault injection after each rotation step proves startup always finds the key
  named by the committed database. A successful rotation re-seals TOTP, SMB
  and recoverable share-link ciphertexts and removes the old key.
