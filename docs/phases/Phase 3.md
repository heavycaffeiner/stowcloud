# Phase 3: auth and ACL

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-6-auth-and-acl.md`.

## Scope

Accounts, the three-tier verification path, sessions, app passwords, TOTP,
recovery codes, groups, the master key, and grants.

Depends on Phase 2. Blocks Phases 4, 5 and 11.

## Milestones

3a to 3d as the proposal's table has them, **plus the four files added to §4.1
after the audit**: `credcache.go`, `groups.go`, `crockford.go` and
`masterkey.go`. Fold those into the milestone table as you go rather than
leaving the table stale.

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
- **Adding `key_ver` to the TOTP AAD requires re-sealing every existing TOTP
  ciphertext in the same transaction that bumps the version.** AAD is
  authenticated, so a silent addition makes them all undecryptable, and the
  check above then refuses to boot. This is the single most dangerous change in
  the phase.
- **Revocation must reach the SMB passdb.** Six paths, §4.3.8a. The test is the
  file's contents, not the database row. Phase 11 writes the file; this phase
  builds the sink every path calls.
- **Eight permission bits.** `DOWNLOAD` is not `READ` and `MOVE` is not
  `RENAME`. Collapsing the first widens every view-only grant; collapsing the
  second lets an account move a file out of its only granted subtree.

## Done when

- The gate is green, including `-race`.
- The enumeration test shows an unknown user and a wrong password producing the
  same status, the same key and a duration inside the stated band.
- The concurrency test proves no path reaches Argon2 outside the gate.
- Each of the six revocation paths has a test asserting the passdb sink fired.
- A rotation re-seals TOTP secrets and bumps the version atomically, and the
  server still starts afterwards.
