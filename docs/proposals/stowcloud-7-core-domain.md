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
recursive rollup, trash, conflict detection, and share links. This is the layer
every protocol sits on and the layer that must not know any of them exist.

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
  Every mutation goes through the durable helper instead.
- **A permission decision carries its own reason.** An evaluation that returns
  a bare boolean cannot tell the UI why an action is unavailable and cannot
  tell the audit log what was actually decided, so `Perms` travels with the
  grant that produced it rather than collapsing to yes or no.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] A domain API with no protocol vocabulary in any signature.
- [ ] The virtual root assembled from grants, with an ungranted path
      indistinguishable from a missing one.
- [ ] A file ETag that notices a same-nanosecond same-length rewrite where the
      filesystem makes that possible, and says so where it does not (F11).
- [ ] Directory ETags and the recursive rollup, invalidated by generation.
- [ ] Trash per share, off by default, with the setting visible rather than
      guessable.
- [ ] Conflict detection on every mutation, producing a conflict rather than an
      overwrite.
- [ ] Share links: created once, shown once, unrecoverable afterwards.
- [ ] Long operations that survive a client disconnect.

### 3.2 Non-Goals

- [ ] Versioning. A standing non-goal: keeping old copies of a file means
      owning storage the folder's other writers know nothing about, which is
      principle 3 inverted.
- [ ] Server-side move or copy across shares as a single atomic operation. It is
      a copy plus a delete today and the semantics are documented.
- [ ] A quota system beyond the existing free-space floor.
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
  quota.go      the free-space floor
```

### 4.2 Data Model Changes

None beyond [`stowcloud-5`](stowcloud-5-store-and-schema.md). `share_link` moves
to `state.db` and `diretag` stays in `cache.db`, which is the split working as
intended: a link is not reconstructible and a rollup is.

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
// FileETag derives a change token for e. It uses mtime, size, and the inode
// generation where statx reports one (STATX_ATTR / ext4, XFS and btrfs carry a
// generation; many filesystems do not). Where no generation is available the
// token is mtime and size, and Weak reports true so that a caller performing a
// lost-update check knows it is relying on nanosecond mtime alone.
func FileETag(st vfs.Stat) (token string, weak bool)
```

The `weak` flag is the point. Today the caller cannot tell, so an `If-Match`
against a filesystem with coarse timestamps looks exactly as strong as one
against a filesystem with fine ones. With the flag, the HTTP layer can emit a
weak ETag validator, which is what RFC 9110 has the syntax for, and a client
that cares learns the difference.

**What is deliberately not done:** hashing the content to make the token strong.
That reads every byte of every file on every listing, and the product's premise
is a 12 TB tree.

#### 4.3.3 Directory ETag and the rollup

Unchanged in design. A directory's ETag is a hash over its children's identities
and tokens, its recursive size and count are stored beside it, and both are
invalidated by a generation counter bumped when the watcher sees a change under
the share.

The hash stays BLAKE3. Changing it to SHA-256 was proposed in the parent
document and reverted (the index, C1): it buys nothing, because BLAKE3 has
to be in the tree anyway for the upload checksum, and changing it invalidates
every stored rollup at cutover for no reason.

`oc:size` needs `rsize` and nothing else, which is why the rollup is a size and
a count rather than a file and directory split. That is the compat layer's
requirement leaking into the core's shape, and it is acceptable only because the
shape is also the right one for the core: the question is "did anything under
here change, and how big is it".

#### 4.3.4 Operations and conflicts

Every mutation is: resolve, check permissions, check the precondition, act
through [`stowcloud-3`](stowcloud-3-vfs-and-paths.md)'s durable-write helper,
invalidate.

The precondition is an `If-Match` token where the caller supplied one, and a
`RENAME_NOREPLACE` where it did not but the operation is a create. A mismatch is
`ErrConflict` carrying the current token, never an overwrite. The product
advertises "a conflict screen, not a silent overwrite" and this is where that is
true or false.

Copy uses `copy_file_range` through the vfs helper: a reflink on btrfs and XFS
when aligned, an in-kernel copy otherwise, and a bounded buffered fallback for
the remainder on the three documented "cannot do this here" errnos only.

#### 4.3.5 Trash

Per share, off by default, and the setting is reported in the listing response
rather than left for the client to assume. Until it is on, a delete is a delete,
and the UI says so before the destructive action rather than after.

Trash entries carry the original path, the deletion time and the size. Restore
resolves the original path again at restore time and produces a conflict if
something is there, rather than overwriting.

#### 4.3.6 Share links

Created once, the full URL exists in exactly one response, and it is not
recoverable afterwards because only a hash of the secret is stored. Revocation
is permanent and the same link can never be recreated.

Options at creation: expiry, password, download cap, upload-only. Upload-only
means the recipient can write and cannot list, which is a distinct permission
set rather than a UI mode, and it is enforced at `Resolve` like every other
grant.

The link secret is encrypted at rest with XChaCha20-Poly1305 under the master
key, as today.

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

// CreateLink mints a share link and returns the only copy of its secret that
// will ever exist. Storing the return value is the caller's single chance.
func (c *Core) CreateLink(ctx context.Context, r Resolved, spec LinkSpec) (Link, secret.Secret, error)
```

### 5-2. Error Handling

| Error | Meaning |
|---|---|
| `ErrNotFound` | missing, or outside every grant. The two are one answer |
| `ErrDenied` | permitted to know it exists, not permitted to do this |
| `ErrConflict` | precondition failed, carrying the current token |
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
| Phase 4f | `archive.go`, `quota.go` | S | 4b | heavycaffeiner |

4c, 4d, 4e and 4f are independent of each other.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| `lukechampine.com/blake3` or `github.com/zeebo/blake3` | the directory ETag hash, shared with the upload checksum |
| `golang.org/x/crypto/chacha20poly1305` | link secrets at rest |

The BLAKE3 module choice is made at Phase 6, where the checksum requirement is
concrete, and both packages are evaluated on the same criterion: a pure-Go
fallback path, because `CGO_ENABLED=0` is not negotiable. `archive/zip` is
standard library.

## 7. References

- `crates/sc-core/src/resolve.rs`, `ops.rs`, `aggregate.rs`, `trash.rs`,
  `links.rs`, `share.rs`: the layer this translates.
- `crates/sc-core/src/path.rs`: the two path vocabularies as they exist today,
  and the conversion D10 makes unskippable.
- [`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §4.3: F11.
- `crates/sc-core/src/ops.rs`, `aggregate.rs`, `links.rs`.
- RFC 9110 §8.8 on weak validators, which §4.3.2's flag exists to use.
