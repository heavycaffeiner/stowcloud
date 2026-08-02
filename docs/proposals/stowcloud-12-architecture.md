# Architecture, Stack, and Feature Scope - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-03                       |
| Status     | Implemented                      |
| Reviewers  |                                  |

---

## 1. Summary

The system-level proposal every other one in this directory sits under: the
five principles, the crate layout that enforces them, the technology choices
and what was rejected, and the boundary of what this product is.

## 2. Background & Motivation

A lightweight file-management service over a native filesystem, Rust backend
and Svelte frontend, Linux and Docker only. It shares its directories with
other services — a media server, a downloader, rsync, Samba — rather than
owning them.

That sharing premise is the one that makes this different from a storage
product with its own object layout, and most of the difficulty follows from it.

## 3. Goals & Non-Goals

### 3.1 Goals — the five principles

1. **The filesystem is the only source of truth.** The database is a cache,
   deletable and rebuildable. Nothing may treat "not in the DB" as "the file
   does not exist" — that is exactly where database-of-record designs fail.
   The exception is stated rather than implied: accounts, grants and share
   links are *not* reconstructible from the tree and must be backed up.
2. **A path is a kernel handle, not a string.** Every access resolves through
   a share root's directory fd. String concatenation followed by a prefix
   check is TOCTOU-prone and forbidden.
3. **A shared folder is not ours.** No sidecar litter, ownership and
   permissions preserved, external changes reflected.
4. **The compat layer does not invade the core.** Not one conditional, no
   vendor-prefixed field, no vendor-only table in any core crate. It must be
   removable in its entirety by a compile feature.
5. **The default is least privilege.** No user homes, no symlinks, SMB off,
   no inline content rendering — all by default.

### 3.2 Non-Goals

Full compatibility stops at **sync, browse, share, preview**. Beyond that
boundary lies rebuilding the server the compat layer only pretends to be:
versioning, comments, tags, groupware, federation, office-suite integration,
activity streams, external storage mounts, workflows.

Also decided against rather than merely unscheduled: content indexing and OCR
(`stowcloud-5-search.md` §3.2), video thumbnails
(`stowcloud-6-preview-sharing.md` §4.5), and a high-contrast theme
(`stowcloud-3-frontend.md` §3.2). Each has its reasoning recorded where the
subsystem is specified, because a non-goal without a reason gets re-proposed
every six months.

Windows and macOS are dev conveniences, never deployment targets: `openat2`,
`statx`, Landlock, `copy_file_range` and `renameat2` are Linux-only and they
are the spine of the security and performance design.

## 4. Technical Design

### 4.1 Crate structure

```
sc-vfs         kernel-handle FS layer — minimal deps, the security core
sc-meta        SQLite cache: file ids, directory ETags
sc-auth        accounts, sessions, app passwords, TOTP
sc-acl         grants and permission evaluation
sc-watch       fanotify/inotify → invalidation
sc-core        the domain API assembled from the above, protocol-agnostic
sc-search      tiered search
sc-http        REST + static frontend      sc-dav      RFC 4918
sc-upload      resumable upload            sc-preview  thumbnails + jail
sc-compat-nc   legacy clients (feature)    sc-smb      Samba orchestration
sc-server      the binary; assembles the router and nothing else
web/           SvelteKit SPA, embedded in the binary
```

**Dependencies point one way.** The compat crate depends on the core, never
the reverse, and a `--no-default-features` build drops every line of it.

The preview worker jail is a **forked child of the running server**, not a
separate binary. Its own module doc records that a re-exec'd dedicated worker
would be a better production shape and is deliberately left as follow-up —
worth keeping, because an earlier draft of the architecture named a worker
binary that never existed.

### 4.2 Technology choices, and what was rejected

| Area | Choice | Instead of |
|---|---|---|
| Backend | Rust — memory safety plus direct syscall control | a GC'd runtime, where the syscall contract is someone else's |
| Database | SQLite, WAL | a server database, for a single-node product |
| Search | own trigram index | Elasticsearch — the unbounded version is what gets punished at a few million files |
| Frontend | Svelte 5 runes, static SPA | SSR — the server is one binary, and a second runtime buys nothing |
| Design system | a maintained MD3 library | hand-built primitives, which each drifted from the spec at their own pace |
| Image decoding | pure Rust | linking C decoders into the process that serves HTTP |

### 4.3 The security posture, end to end

| Layer | Control |
|---|---|
| path resolution | `openat2(RESOLVE_BENEATH)`, symlink policy per share |
| process | non-root uid, `cap_drop: ALL`, read-only rootfs, Landlock, seccomp |
| decoders | forked worker: empty Landlock ruleset, 22 syscalls, no `execve`, no network |
| content | separate origin with no session cookie; capability URLs, not cookies |
| SMB | private-range bind enforced at config generation, not documented |
| existence | an unlistable path is 404 everywhere, never 403 |

Each is specified in its own proposal; the point of the table is that they are
layers, and no one of them is load-bearing alone.

### 4.4 What is reachable today

All six milestones are reachable by a real client: core, web UI, coexistence,
WebDAV, the compat layer, and SMB with the hardening work.

What remains open is verification breadth rather than function: protocol
conformance running in CI, an automated sync-client regression suite, and an
external security review. Those are listed as missing rather than quietly
omitted.

## 5. API Design

No surface of its own. The public surfaces are specified in
`stowcloud-9-api.md` (REST), `stowcloud-4-webdav.md`,
`stowcloud-8-compat.md`, and `stowcloud-1-smb.md`.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Duration | Owner |
|---|---|---|---|
| M1 | core: VFS, ACL, metadata cache | done | heavycaffeiner |
| M2 | web UI over the REST API | done | heavycaffeiner |
| M3 | coexistence: watching, external changes, shared modes | done | heavycaffeiner |
| M4 | WebDAV Class 2 | done | heavycaffeiner |
| M5 | compat layer | done | heavycaffeiner |
| M6 | SMB, process hardening | done | heavycaffeiner |

### 6-2. Dependencies

Linux ≥ 5.6 for `openat2`; Landlock, `statx` btime, `renameat2` and
`copy_file_range` are probed and degrade individually. Everything else is
listed in the proposal for the subsystem that needs it.

## 7. References

- Every other proposal in this directory; this one is the index they hang off.
- `scripts/verify.sh` — the gates that keep §3.1's principles enforced rather
  than aspirational.
