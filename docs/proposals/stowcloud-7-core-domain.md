# Core domain - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The protocol-agnostic domain API: the virtual root, entry listing, the
operations (create, move, copy, delete, restore), directory ETags and the
recursive rollup, trash, conflict detection, share registration, restart-visible
long operations and share links. This is the layer every protocol sits on and
the layer that must not know any of them exist.

## 2. Background & Motivation

`sc-core` is 7,283 lines and it is where principle 4 is actually kept: nothing
in it knows that WebDAV, Nextcloud or SMB exist. The port preserves that, and
the mechanism changes from a `grep` to an import graph
([`stowcloud-13-compat-nc.md`](stowcloud-13-compat-nc.md) §4.2).

Two things in this layer need more than translation.

**The file ETag is too weak.** F11: `mtime_ns` plus size. A rewrite in the same
nanosecond at the same length is invisible to `If-Match`, so the conflict screen
the product advertises passes and an edit is lost. Unlikely, and likeliest in
exactly the case principle 3 says to expect: another program rewriting a file in
place.

**Path vocabulary is where this layer has already been wrong.** Three
vocabularies were carried under one name, and D10 turns them into three types
precisely because this is the layer that converts between them.

Three stances from
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 decide the close
calls here, and each one is a place where the obvious implementation is wrong:

- **S2, existence is never revealed.** The obvious implementation returns 403
  for a path the caller may not touch. That tells a stranger the path exists.
  §4.3.1 makes it one function so the rule cannot be half-applied.
- **S6, the neighbours' access survives us.** The obvious implementation of a
  replace is truncate-and-write, which is neither atomic nor mode-preserving.
  Every content replacement goes through `WriteDurable`; other namespace
  mutations use their named VFS operation.
- **A permission decision carries its own reason.** An evaluation that returns
  a bare boolean cannot tell the UI why an action is unavailable and cannot
  tell the audit log what was actually decided, so `Perms` travels with the
  grant that produced it rather than collapsing to yes or no.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] A domain API with no protocol vocabulary in any signature.
- [ ] The virtual root assembled from grants, with an ungranted path
      indistinguishable from a missing one.
- [ ] A file ETag that includes every change field Linux exposes, is marked
      weak, and is never accepted as a strong mutation precondition (F11).
- [ ] Directory ETags and the recursive rollup, invalidated by generation.
- [ ] Trash per share, off by default, with the setting visible rather than
      guessable.
- [ ] Strong mutation preconditions and no-replace creates produce a conflict
      rather than an overwrite. A weak precondition is refused rather than
      presented as proof.
- [ ] Share links: the hash authenticates public requests and encrypted token
      storage lets the owner list an existing URL again.
- [ ] Admin-created shares and edits to config-defined shares survive restart.
- [ ] Long operations that survive a client disconnect and retain an honest
      terminal record across restart or cutover.

### 3.2 Non-Goals

- [ ] Versioning. A standing non-goal: keeping old copies of a file means
      owning storage the folder's other writers know nothing about, which is
      principle 3 inverted.
- [ ] Server-side move or copy across shares as a single atomic operation. It is
      a copy plus a delete today and the semantics are documented.
- [ ] A **new** quota system. The existing one is ported as-is and §4.3.6b says
      what it is; what is out of scope is adding group quotas, per-share quotas,
      or soft limits with grace periods.
- [ ] Content-addressed storage of any kind. The folder on disk is the storage.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/core
  root.go       the virtual root: grants to a browsable tree
  entry.go      Entry, the one listing shape every protocol renders
  resolve.go    Vpath to (ShareRoot, SafePath), and the ACL check
  ops.go        create, move, copy, delete, mkdir
  stream.go     ranged reads and writes
  trash.go      per-share trash, restore, purge
  aggregate.go  directory ETag and the recursive rollup
  links.go      share links
  archive.go    server-side zip of a subtree
  quota.go      two different things, §4.3.6b: the free-space floor, and the
                per-user byte quota with its reserve-then-commit ledger
  homes.go      per-user home directories, off by default
```

### 4.2 Data Model Changes

`share_link` is already in `state.db` and `diretag` stays in `cache.db`, which
is the split working as intended: a link is not reconstructible and a rollup
is. This phase adds five later-owned durable tables to `state.db`:

- `share_definition` for shares created through the admin API;
- `share_identity_override` and `share_trash_override` for editable properties
  of config-defined shares;
- `operation` and `operation_result` for the bounded, restart-visible history
  of long operations.

The first three replace `shares.db`. The last two replace `jobs.db`. A job that
was running when the Rust server stopped is imported as interrupted, because no
Go task exists to resume it. Completed history, progress and per-item results
remain readable. Phase 4 extends `migrate --from-rust` for both source files and
does not alter either source file. Imported dynamic shares preserve their
external ids, including the Rust dynamic-share base offset, because grants and
API payloads already refer to those values. Config-share overrides keep the
config-derived ids they were keyed by. A job with an unknown owner is a
reasoned import refusal, not an orphaned history row. Imported result paths are
parsed through the same path boundary as live operations. A Rust result's raw
error sentence is mapped to a known typed reason or replaced with the generic
item-failed reason; lower-layer prose is not carried into the native REST wire
shape.

### 4.3 Core Logic

#### 4.3.1 Resolution and the existence rule

```go
// Resolve turns a client path into a share root, a validated path under it, and
// the permissions the caller holds there. A path outside every grant returns
// ErrNotFound, identical to a path that does not exist. This is the single
// place that rule is applied, so that "unlistable is 404 everywhere, never
// 403" is a property of one function rather than of every handler.
func (c *Core) Resolve(user UserID, p Vpath, need Perms) (Resolved, error)
```

Every operation below takes a `Resolved`, never a `Vpath`. That is what makes
the ACL check unskippable: there is no way to reach `ops.go` without having gone
through `Resolve`, because `Resolved` cannot be constructed elsewhere (D10, and
the unexported-field pattern).

#### 4.3.2 The file ETag

F11's fix, and it has an honest limit.

```go
// FileETag derives a change token from the identity, size, mtime and ctime that
// statx actually exposes. Linux statx has no inode change-version field, so the
// token is always weak: it is useful for caching and conflict warnings but is
// not represented as a strong validator.
func FileETag(st vfs.Stat) (token string, weak bool)
```

The `weak` flag is the point. An earlier draft claimed `statx` exposed an inode
generation that changes on write. It does not. Reporting a metadata-derived
token as strong would therefore preserve F11's false guarantee. The HTTP layer
emits a weak ETag validator, and mutation preconditions never use weak
comparison as proof that the bytes are unchanged. The frontend treats it as a
conflict warning, not as a lost-update guarantee.

**What is deliberately not done:** hashing the content to make the token strong.
That reads every byte of every file on every listing, and the product's premise
is a 12 TB tree.

#### 4.3.3 Directory ETag and the rollup

Unchanged in design. A directory's ETag is a hash over its children's identities
and tokens, its recursive size and count are stored beside it, and both are
invalidated by a generation counter bumped when the watcher sees a change under
the share.

The hash stays BLAKE3. Changing it to SHA-256 was proposed in the parent
document and reverted (the index, C1): it changes a wire-visible ETag and breaks
the parity corpus for no benefit. Phase 4 introduces the module; Phase 6 reuses
it for the client-facing upload checksum.

`oc:size` needs `rsize` and nothing else, which is why the rollup is a size and
a count rather than a file and directory split. That is the compat layer's
requirement leaking into the core's shape, and it is acceptable only because the
shape is also the right one for the core: the question is "did anything under
here change, and how big is it".

#### 4.3.4 Operations and conflicts

Every mutation is: resolve, check permissions, check the precondition, act
through the named VFS operation, invalidate. Content replacement and upload
finalization use `WriteDurable`; explicit moves use `ShareRoot.Rename`.
[`stowcloud-3`](stowcloud-3-vfs-and-paths.md) keeps the raw rename primitives
inside `internal/vfs`.

A caller-supplied `If-Match` uses RFC 9110 strong comparison. A weak file ETag
therefore cannot satisfy it and returns `ErrPrecondition` carrying the current weak
token. The frontend may offer an explicit unconditional retry, but neither the
core nor HTTP silently converts a weak token into proof. A strong mismatch is
also `ErrPrecondition`. A create without a precondition uses `RENAME_NOREPLACE`, so
another writer cannot be overwritten between the check and publication.

Copy uses `copy_file_range` through the vfs helper: a reflink on btrfs and XFS
when aligned, an in-kernel copy otherwise, and a bounded buffered fallback for
the remainder on the three documented "cannot do this here" errnos only.

**Every successful mutation records one journal row**, upserting the last thing
this account did to this file ([`5`](stowcloud-5-store-and-schema.md) §4.2.3).
It happens after the write has already succeeded, so a failure to record is
logged and dropped and never fails the operation the user asked for. This is the
write side of the Recent Files destination; the read side is a query in
[`8`](stowcloud-8-http-and-api.md).

#### 4.3.5 Trash

Per share, off by default, and the setting is reported in the listing response
rather than left for the client to assume. Until it is on, a delete is a delete,
and the UI says so before the destructive action rather than after.

Trash entries carry the original path, the deletion time and the size. Restore
resolves the original path again at restore time and produces a conflict if
something is there, rather than overwriting.

#### 4.3.6 Share links

Created once, the full URL is returned immediately and remains recoverable to
its owner, matching the existing API. A hash authenticates public requests and
an XChaCha20-Poly1305 ciphertext under the master key supports owner listings.
Legacy rows may lack the ciphertext and are the only links that cannot be
recovered. Revocation is permanent and the same link is never recreated.
Every ciphertext carries `token_key_ver`, and its AAD binds that version and
the token hash. Phase 3 upgrades imported Rust ciphertexts from their legacy
AAD before this package reads them. An imported owner copy that an earlier Rust
key rotation had already made unreadable is represented like any older row
without ciphertext; its token hash still keeps the issued public URL valid.

Options at creation: expiry, password, download cap, upload-only. Upload-only
means the recipient can write and cannot list, which is a distinct permission
set rather than a UI mode, and it is enforced at `Resolve` like every other
grant.

The link secret is encrypted at rest with XChaCha20-Poly1305 under the master
key, as today.

The target contract also stays as today: the row stores a path and, when one
could be allocated at creation, an identity. Access resolves the stored path
and requires the current identity to match. A rename therefore makes the link
gone instead of moving it, and replacement at the same path also makes it gone.
A path-only legacy or root link keeps the weaker path-only check rather than
being assigned a fabricated zero identity. New non-root links require a birth
time in that identity. `(dev, ino)` without it cannot distinguish the original
from a later inode reuse, so an imported identity-bearing link lacking birth
time blocks cutover instead of being weakened.

#### 4.3.6a Per-user home directories

Off by default, which is principle 5, and the phrase "no user homes" in that
principle means "not unless an operator turns them on" rather than "never".

The important property is what a home is **not**: it is not a second resolution
mechanism. One share root is opened at startup, homes are subtrees under it, and
they reach a caller through the same grant-projected virtual root and the same
single `Resolve` that every other share uses. A home that resolved by a
different path would be a second place the existence rule could be got wrong,
and §4.3.1 exists precisely so there is only one.

What the feature adds is a grant that is implied by the account rather than
created by an administrator. Everything downstream of that is unchanged.

**One thing is not downstream of it: a new home is seeded from a template.**
When `{homes.root}/.template` exists, the home is created by copying that tree
recursively; when it does not, the home is a `mkdir`. That is an admin-facing
feature with no other trace in configuration, so a port that only creates empty
directories loses it silently and nobody notices until a new account is missing
files everyone else has.

The template directory is unreachable as anybody's own home, because homes are
named by numeric user id and `.template` is not a number.

#### 4.3.6b Quota, which is two things under one word

They are separate mechanisms and the word covers both, which is how one of them
nearly got dropped from this plan as a non-goal:

1. **The free-space floor.** What the filesystem has left, reported to clients
   as RFC 4331 disk space and used to refuse a write that would fill the disk.
   Not per account.
2. **The per-user byte quota.** A cap on the account and a running ledger of
   what it has used, both columns on the user row, enforced through a
   reserve-then-commit seam: a write reserves before it starts and commits or
   releases when it ends, so two concurrent uploads cannot both pass a check
   against the same headroom. The compat layer reports it to clients, so it is
   client-visible as well as enforced.

The second is easy to lose in a port because the free-space floor answers the
same protocol question and looks like the whole feature from the wire.

#### 4.3.7 Long operations

A recursive copy, delete or archive of a large subtree outlives the request that
started it. The current tree does this and the port keeps it: the operation gets
an id, progress is readable, and a client that refreshes the tab reattaches.

In Go this is a `task.Go` (D7) with a context that is **not** the request's,
because cancelling on client disconnect is exactly the bug. The operation's own
cancellation is explicit and is a separate API call.

## 5. API Design

### 5-1. New / Modified

```go
package core

// Entry is the one listing shape. Every protocol renders from this and nothing
// adds a field for one protocol's benefit: a vendor-specific property is
// decorated at the protocol layer through the PropSource hook, never here.
type Entry struct {
    Name     string
    Path     SharePath
    IsDir    bool
    Size     uint64
    MTimeNs  int64
    BTimeNs  *int64
    Ident    cache.Ident
    ETag     string
    ETagWeak bool
    Perms    acl.Perms
}

// List streams the entries of a directory. It streams because a directory is
// not ours to assume the size of; a caller that wants a page passes a cursor
// and gets a bounded response.
func (c *Core) List(ctx context.Context, r Resolved, cur Cursor) (Page, error)

// Move renames within a share, or copies and deletes across shares. Across
// shares it is not atomic, and it says so in the return rather than pretending.
func (c *Core) Move(ctx context.Context, from, to Resolved, opt MoveOpts) (MoveResult, error)

// CreateShare registers a validated host directory and returns the externally
// visible id stored with it. UpdateShare persists edits to a dynamic share or
// the allowed overrides for a config-defined share. Only a dynamic share can
// be deleted.
func (c *Core) CreateShare(ctx context.Context, spec ShareSpec) (Share, error)
func (c *Core) UpdateShare(ctx context.Context, id ShareID, patch SharePatch) (Share, error)
func (c *Core) DeleteShare(ctx context.Context, id ShareID) error

// Operation returns the restart-visible state and bounded item results for a
// long mutation. Cancel changes a running operation through its own context;
// disconnecting the request that created it does not.
func (c *Core) Operation(ctx context.Context, id OperationID) (Operation, error)
func (c *Core) CancelOperation(ctx context.Context, id OperationID) error

// CreateLink mints a share link and returns its bearer secret. The store keeps
// a verification hash and an encrypted copy so the owner can list the URL
// again without making public access depend on decryption.
func (c *Core) CreateLink(ctx context.Context, r Resolved, spec LinkSpec) (Link, secret.Secret, error)
```

### 5-2. Error Handling

| Error | Meaning |
|---|---|
| `ErrNotFound` | missing, or outside every grant. The two are one answer |
| `ErrDenied` | permitted to know it exists, not permitted to do this |
| `ErrPrecondition` | a supplied validator failed strong comparison, carrying the current token |
| `ErrConflict` | an operation conflicts with current state without a supplied validator |
| `ErrExists` | create against an existing name with no-clobber |
| `ErrNotEmpty` | directory delete without recursion |
| `ErrCrossShare` | an operation that cannot span shares atomically, naming which half completed |
| `ErrNoSpace` | `ENOSPC`, or the configured free-space floor |
| `ErrTrashDisabled` | restore or purge against a share with trash off |
| `ErrLinkExpired` | expired, revoked, or over its download cap. One error, because distinguishing them tells a stranger about the link |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 4a | `root.go`, `resolve.go`, `entry.go`: the virtual root and the one ACL gate | M | Phases 1, 2, 3 | heavycaffeiner |
| Phase 4b | `ops.go`, `stream.go`: mutations, preconditions, ranged IO | L | 4a | heavycaffeiner |
| Phase 4c | `aggregate.go`: ETags, the rollup, generation invalidation, F11 | M | 4a | heavycaffeiner |
| Phase 4d | `trash.go` | M | 4b | heavycaffeiner |
| Phase 4e | `links.go` | M | 4a | heavycaffeiner |
| Phase 4f | `archive.go`, `quota.go`, `homes.go` | S | 4b | heavycaffeiner |
| Phase 4g | persisted share registry, operation store and the `shares.db`/`jobs.db` importer extension | M | 4a, Phase 2.5 | heavycaffeiner |

4c, 4d, 4e, 4f and 4g are independent of each other after their listed
dependencies.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `lukechampine.com/blake3` or `github.com/zeebo/blake3` | the directory ETag hash, shared with the upload checksum |
| `golang.org/x/crypto/chacha20poly1305` | link secrets at rest |

The BLAKE3 module choice is made here because the directory ETag is
wire-visible and parity requires the algorithm to stay unchanged. Phase 6
reuses the same implementation for the client-selected checksum. The module
must have a pure-Go fallback path because `CGO_ENABLED=0` is not negotiable.
`archive/zip` is standard library.

## 7. References

- `crates/sc-core/src/resolve.rs`, `ops.rs`, `aggregate.rs`, `trash.rs`,
  `links.rs`, `share.rs`: the layer this translates.
- `crates/sc-core/src/path.rs`: the two path vocabularies as they exist today,
  and the conversion D10 makes unskippable.
- [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3: F11.
- `crates/sc-core/src/ops.rs`, `aggregate.rs`, `links.rs`.
- RFC 9110 §8.8 on weak validators, which §4.3.2's flag exists to use.
