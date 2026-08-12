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

4c, 4d, 4e and 4f are independent of each other.

## Traps

- **`Resolve` is the only place the existence rule is applied**, and `Resolved`
  cannot be constructed outside the package. That is what makes the ACL check
  unskippable rather than merely conventional.
- **A path outside every grant returns `ErrNotFound`.** Returning a denial tells
  a stranger the path exists. 403 is only for a caller who may know it does.
- **The file ETag reports `weak`** when the filesystem carries no inode
  generation. Without the flag an `If-Match` on a coarse filesystem looks
  exactly as strong as one on a fine filesystem, which is F11 half-fixed.
- **Do not hash content to make the token strong.** That reads every byte of
  every file on every listing against a 12 TB premise.
- **The directory ETag hash stays BLAKE3.** Changing it invalidates every stored
  rollup for no benefit, and the module is in the tree anyway for the upload
  checksum.
- **Every mutation goes through the durable-write helper.** A truncate-and-write
  replace is neither atomic nor mode-preserving, which is principle 3 broken.
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
- **Restore resolves the original path again at restore time** and produces a
  conflict rather than overwriting.
- **A share link's secret exists in exactly one response.** Only its hash is
  stored, and revocation is permanent.

## Done when

- The gate is green, including `-race`.
- A path outside a grant and a path that does not exist produce byte-identical
  responses in a table test.
- F11 is closed, with the weak-validator case covered.
- A per-user quota test proves two concurrent writes cannot both pass a check
  against the same headroom.
