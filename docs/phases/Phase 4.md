# Phase 4: core domain

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-7-core-domain.md`.

## Scope

The protocol-agnostic domain API. This is the layer every protocol sits on and
the layer that must not know any of them exist.

Depends on Phases 1, 2 and 3. Blocks Phases 5, 6, 7, 8 and 9.

## Milestones

- **4a**: `root.go`, `resolve.go`, `entry.go`: the virtual root and the one ACL
  gate.
- **4b**: `ops.go`, `stream.go`: mutations, preconditions, ranged IO.
- **4c**: `aggregate.go`: ETags, the rollup, generation invalidation, F11.
- **4d**: `trash.go`.
- **4e**: `links.go`.
- **4f**: `archive.go`, `quota.go`, `homes.go`.
- **4g**: persisted share registry, operation store and the `shares.db` and
  `jobs.db` importer extension.

4c, 4d, 4e, 4f and 4g are independent of each other after their listed
dependencies.

## Traps

- **`Resolve` is the only place the existence rule is applied**, and `Resolved`
  cannot be constructed outside the package. That is what makes the ACL check
  unskippable rather than merely conventional.
- **A path outside every grant returns `ErrNotFound`.** Returning a denial tells
  a stranger the path exists. 403 is only for a caller who may know it does.
- **The file ETag always reports `weak`.** Linux `statx` does not expose an
  inode change version, so this metadata token is never a strong lost-update
  guarantee.
- **A weak `If-Match` never succeeds.** RFC 9110 requires strong comparison.
  Return the current weak token with the conflict and require an explicit
  unconditional retry; do not reinterpret the token as strong.
- **Do not hash content to make the token strong.** That reads every byte of
  every file on every listing against a 12 TB premise.
- **The directory ETag hash stays BLAKE3.** Changing a wire-visible ETag breaks
  parity for no benefit. This phase introduces the module and Phase 6 reuses it.
- **Content replacement and upload finalization go through `WriteDurable`.** An
  explicit namespace move goes through `ShareRoot.Rename`; publication of an
  already-complete database goes through `PublishNew`. Raw rename syscalls stay
  inside `internal/vfs`. A truncate-and-write replace is neither atomic nor
  mode-preserving, which is principle 3 broken.
- **Quota is two mechanisms under one word.** The free-space floor is what RFC
  4331 reports; the per-user byte quota is a cap plus a running ledger with
  reserve-then-commit, and it is client-visible. The second is the one a port
  drops by accident because the first answers the same protocol question.
- **Homes are seeded from `{homes.root}/.template`** when it exists, by
  recursive copy, and by `mkdir` when it does not. A port that only does
  `mkdir` loses an admin feature with no other trace in configuration.
- **Homes are not a second resolution mechanism.** One share root, subtrees
  under it, the same `Resolve`.
- **A long operation's context is not the request's.** Cancelling on client
  disconnect is the bug; the operation's own cancellation is a separate call.
- **Every successful mutation writes one journal row**, after the write already
  succeeded, and a failure there is logged and dropped. Nothing may treat a
  missing row as evidence.
- **Admin-created shares and config-share edits are durable.** They move from
  `shares.db` into `state.db`; rebuilding the filesystem cache cannot recreate
  either one.
- **A Rust job cannot resume in a Go process.** Import running jobs as
  interrupted and preserve their progress and results so a refreshed client
  gets an honest terminal state.
- **Restore resolves the original path again at restore time** and produces a
  conflict rather than overwriting.
- **A share link stores both a hash and an encrypted token.** The hash verifies
  public access without decryption; the ciphertext lets the owner list the URL
  again. Legacy rows without ciphertext remain unrecoverable. Revocation is
  permanent.

## Done when

- The gate is green, including `-race`.
- A path outside a grant and a path that does not exist produce byte-identical
  responses in a table test.
- F11 is closed: a weak `If-Match` is refused, and an explicit unconditional
  retry is the only way past the warning.
- A per-user quota test proves two concurrent writes cannot both pass a check
  against the same headroom.
- Imported share definitions and overrides preserve their externally visible
  ids, and imported running jobs read back as interrupted with prior results.
