# Core design

Code-level extension of `ARCHITECTURE.md`. The algorithms in this document
account for most of the project's real difficulty.

---

## 1. sc-vfs — types and syscall contract

### 1.1 Handle types

```rust
/// Share root **anchor**. An O_PATH directory FD alive for the process
/// lifetime. As long as this FD exists, we keep pointing at the original
/// inode even if the host renames or unmounts the share path underneath us
/// (no path-reinterpretation attack).
pub struct ShareRoot {
    id:      ShareId,
    dirfd:   OwnedFd,          // O_PATH | O_DIRECTORY | O_CLOEXEC
    root_dev: u64,             // recorded via statx at startup, for mount-boundary checks
    fstype:  FsType,           // statfs f_type — see the deployment filesystem matrix
    policy:  Arc<SharePolicy>,
}

pub struct SharePolicy {
    pub symlink:     SymlinkPolicy,      // Deny | WithinShare | Follow
    pub cross_mount: bool,               // cross nested mounts inside the share (default true)
    pub id_strategy: IdStrategy,         // Inode | Path
    pub trash:       TrashMode,
    pub mode_file:   u32,                // 0o664
    pub mode_dir:    u32,                // 0o775
    pub chown:       Option<(Uid, Gid)>, // None = process uid/gid
    pub max_depth:   u16,                // default 64
}

/// Open file, readable/writable. FD returned on Drop.
pub struct FileHandle { fd: OwnedFd, st: Stat }

/// Listable directory handle. **`O_RDONLY | O_DIRECTORY | O_CLOEXEC`.**
pub struct DirHandle  { fd: OwnedFd, st: Stat }

/// Only the parts of a statx result we need. Kept small so it stays `Copy`.
#[derive(Clone, Copy)]
pub struct Stat {
    pub dev: u64, pub ino: u64,
    pub btime_ns: Option<i128>,     // STATX_BTIME; None on filesystems that don't record one
    pub mtime_ns: i128,
    pub size: u64,
    pub mode: u32, pub uid: u32, pub gid: u32,
    pub nlink: u32,
    pub kind: Kind,                 // File | Dir | Symlink | Other
}
```

**Invariant**: no `RawFd` or `&Path` (host-absolute) ever leaves `sc-vfs`. Layers above only see `ShareRoot + SafePath` or a handle.

### 1.1a Anchor FD vs. listing FD — `O_PATH` cannot read

An `O_PATH` FD **does not actually open the file.** It's usable only for FD-level operations — `openat2`'s base, `fstatat`, `fchdir` — and **`getdents64`/`read` fail with `EBADF`.** The two roles are therefore split by type:

| Role | Flags | Use | Why |
|---|---|---|---|
| **Anchor** (`ShareRoot.dirfd`) | `O_PATH \| O_DIRECTORY \| O_CLOEXEC` | `openat2` base, `fstatat` | Least privilege. Keeps pointing at the original inode even if the host renames/unmounts the share path |
| **Listing handle** (`DirHandle`) | `O_RDONLY \| O_DIRECTORY \| O_CLOEXEC` | `getdents64` | Needs to actually read directory contents, so `O_PATH` won't do |

```rust
impl ShareRoot {
    /// For listing. Returns a handle `getdents64` can use.
    pub fn open_dir(&self, p: &SafePath) -> Result<DirHandle>;      // O_RDONLY|O_DIRECTORY
    /// For descending only. A directory you won't list needs nothing more than an anchor.
    pub fn open_dir_anchor(&self, p: &SafePath) -> Result<AnchorFd>; // O_PATH|O_DIRECTORY
}
```

A tree walk opens `O_RDONLY` **only for directories it actually lists.** Intermediate path components don't need an FD at all — `openat2` resolves them inside the kernel.

### 1.2 Syscall mapping

```rust
fn resolve_flags(p: &SharePolicy) -> u64 {
    let mut f = RESOLVE_NO_MAGICLINKS;            // always: blocks escape via /proc/self/fd/*
    f |= match p.symlink {
        SymlinkPolicy::Deny        => RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS,
        SymlinkPolicy::WithinShare => RESOLVE_IN_ROOT,   // clamps links to root
        SymlinkPolicy::Follow      => RESOLVE_BENEATH,   // only the final path is forced under root
    };
    if !p.cross_mount { f |= RESOLVE_NO_XDEV; }
    f
}
```

| Operation | Call | Note |
|---|---|---|
| Open | `openat2(dirfd, rel, {flags, mode, resolve})` | Falls back per §1.4 on failure |
| Metadata | `statx(fd, "", AT_EMPTY_PATH, BTIME\|INO\|MNT_ID)` | Directly off the handle |
| List | raw `getdents64` parsing | **needs a `DirHandle` (`O_RDONLY`)** — an anchor FD can't do it (§1.1a). `d_type` avoids a stat; `statx` only on `DT_UNKNOWN` |
| Create | `openat2(.., O_CREAT\|O_EXCL\|O_WRONLY\|O_NOFOLLOW)` | Blocks symlink-preemption attacks |
| Copy | `copy_file_range` loop | btrfs/XFS reflink, NFS server-side copy |
| Move | `renameat2(RENAME_NOREPLACE)` / `0` | `EXDEV` → copy+unlink fallback (see deployment doc) |
| Times | `utimensat(fd, NULL, ts, 0)` | Restores upload mtime |
| Transfer | `sendfile` (plaintext HTTP) / `ReaderStream` | |
| Durability | `fdatasync(file)` + `fsync(parent_dir)` | Rename durability needs the parent dir fsync too |

### 1.3 Directory-listing cost

`getdents64` is parsed directly into a 64 KiB buffer. For a 10k-entry directory, `readdir` + a per-entry `statx` is 10k syscalls; when `d_type` alone is enough (tree view, name sort) it's 3–4 syscalls total.

List views that need size/mtime can't avoid `statx`, so they **stat only what's on screen**, via cursor pagination. Name-sorted pages can be stat'd page-at-a-time; size/mtime sort needs the full set stat'd, so only that case goes through the `sc-meta` cache with a background refresh.

### 1.4 Runtime feature detection

```rust
pub struct KernelCaps {
    pub openat2: bool,
    pub statx_btime: bool,       // varies per share → stored on ShareRoot
    pub renameat2: bool,
    pub copy_file_range: bool,
    pub landlock: Option<u32>,   // ABI version
}
```

Probed once at startup. If `openat2` fails with `ENOSYS` the kernel is too old; if it fails with **`EPERM`, seccomp is blocking it** (typically Docker's default profile) — the two get distinct startup messages.

Fallback: a `cap-std` per-component `openat(O_NOFOLLOW | O_DIRECTORY)` walk. Each step still refuses symlinks, so safety holds, but syscalls scale with path length and there's a theoretical mid-walk rename race (each step holds its own dirfd, so escape is still impossible — the exposure is a spurious `ENOENT`, not a break-out). Startup logs a perf/security downgrade warning.

**The Windows dev backend (`sc-vfs::backend::portable`) runs entirely on this fallback path** — `KernelCaps` reports every flag false and `openat2` reports `NotApplicable` (§ diagnostics). It exists so the server runs on a Windows dev box, not as a deployment target: symlink handling, cross-mount detection, and reflink copy are all weaker or absent there. Production runs the Linux backend.

### 1.5 File identity

`sc_core::Entry::id` is `Option<FileId>`. A listing (`Core::list`) never populates it — browsing a directory of a million files must not write a million rows just because someone scrolled past them. Only `Core::stat_entry` allocates one on demand, and the compat layer's `ensure_fileid` (used for WebDAV prop emission, locks, favorites, share-link creation) does the same for its own single, already-named path. Both go through the same allocator; a plain listing goes through neither. This is the footprint trade-off `DESIGN-FOOTPRINT.md` §2 argues for: a row exists only once a fileid is actually requested. Anything that needs a stable id — a share link, a WebDAV client's sync journal — must go through the allocating path; code that only needs to display an entry must not assume `id` is populated.

Identity is keyed on `sc-meta`'s unique index over `(share, dev, ino, btime_ns)`. `dev`/`ino` alone aren't enough on a backend that can't tell two directories apart (see below), which is why `btime_ns` is part of the key at all — and not enough by itself either, since two directories can share a creation tick or simply have no reported btime.

This was load-bearing in practice, not just in theory: the Windows dev backend's `dev_ino()` used to return a hardcoded `(0, 0)`, leaving `btime_ns` as the *only* thing distinguishing two nodes in the same share. A share's root and one of its own subdirectories were observed reporting the same `oc:fileid` over WebDAV — a sync client keys its whole local sync journal on that id, so this wasn't cosmetic, it looked to the client like two resources were the same file. The fix reads real NTFS identity — `(volume serial number, file index)` — via `GetFileInformationByHandle`. The lesson: a shortcut noted as "not load-bearing in dev" can become load-bearing the moment identity actually needs to be unique, and the failure mode is silent until two nodes collide.

---

## 2. SafePath parsing rules

```rust
pub struct SafePath { comps: SmallVec<[Component; 8]> }   // Component = CompactString
```

Parsing is **reject-first.** Anything ambiguous is an error.

| Rule | Reason |
|---|---|
| Reject absolute paths (leading `/`) | Only share-relative paths exist |
| Reject `.` and `..` components | Not normalized — **rejected.** Normalizing creates a bypass vector |
| Reject empty components (`//`) | Parser-disagreement attacks |
| Reject NUL, `\x01-\x1F`, `\x7F` | C-string truncation, terminal escapes |
| Reject components containing `/` | Encoding bypass (`%2F`) |
| Reject components > 255 bytes | `NAME_MAX` |
| Reject paths > 4096 bytes total | `PATH_MAX` |
| Reject depth > `policy.max_depth` | Recursion DoS |
| Reject `:` | NTFS ADS; SMB client interop |
| Reject trailing `.` or space | Windows reinterprets these as a different file |
| Reject `CON PRN AUX NUL COM1-9 LPT1-9` (extension-insensitive, case-insensitive) | Windows reserved device names |
| Reject reserved prefixes `.sctrash`, `.scpart-`, `.scmeta`, `.scindex` | Protects our own control files. The tree walker, listing, and SMB `veto files` all share this one list (`sc_vfs::reserved`) |

**Unicode**: a lookup tries the given bytes first, then retries as NFC, then NFD (macOS SMB clients write NFD). **New names are NFC-normalized on creation only.** Existing on-disk names are never rewritten — rewriting them breaks external index DBs such as Jellyfin's.

**Confusables**: a name that visually collides with an existing sibling across scripts only gets a UI warning badge, never a block — mixed CJK filenames are normal in many environments and a hard block's false-positive cost is too high.

`SafePath::parse` is the primary `cargo-fuzz` target: arbitrary bytes in, checked for (a) no panic, (b) every path that parses contains no `..`, no absolute prefix, no NUL.

---

## 3. ACL evaluation

### 3.1 Data model

```rust
pub struct Grant {
    pub id:        GrantId,
    pub principal: Principal,      // User(UserId) | Group(GroupId)
    pub share:     ShareId,
    pub subpath:   SafePath,       // empty = share root
    pub allow:     Perms,
    pub deny:      Perms,
    pub inherit:   bool,           // false = applies to exactly this path, nothing beneath it
    pub label:     Option<String>, // virtual-root display name
}

bitflags! {
    pub struct Perms: u16 {
        const READ     = 1<<0;  // list + download
        const WRITE    = 1<<1;  // overwrite existing file content
        const CREATE   = 1<<2;  // create new file/directory
        const DELETE   = 1<<3;
        const RENAME   = 1<<4;  // rename within the same directory
        const MOVE     = 1<<5;  // move to a different directory (not source-DELETE + dest-CREATE; its own bit)
        const SHARE    = 1<<6;  // create a share link
        const DOWNLOAD = 1<<7;  // off = preview/stream only (UI must state this isn't watertight)
    }
}
```

### 3.2 Evaluation algorithm — depth-first, same-depth DENY wins

```
fn evaluate(user, share, path, want: Perms) -> Decision

  principals = {User(user)} ∪ {Group(g) | g ∈ groups(user)}
  candidates = grants where
        g.share == share
     && (g.inherit ? g.subpath.is_prefix_of(path) : g.subpath == path)
     && g.principal ∈ principals

  for depth in (path.len() .. 0).rev():          # deepest first
      level = candidates where g.subpath.len() == depth
      if level is empty: continue

      if ∃ g ∈ level : g.deny  ∩ want ≠ ∅ : return Denied(by = g)
      if ∃ g ∈ level : g.allow ⊇ want      : return Allowed(by = g)
      # partial allow doesn't pass through — the whole requested set must be satisfied to win
      if ∃ g ∈ level : g.allow ∩ want ≠ ∅ : want -= g.allow; continue

  return Denied(by = DefaultDeny)
```

Properties:
- **Default deny.** No match, no access.
- **The deepest rule wins.** A READ grant on `/photos` doesn't block a WRITE grant added at `/photos/upload`.
- **Same depth: DENY wins.** If a user's personal grant and their group's grant land on the same path, the denial wins — predictable, safe direction.
- **Every decision carries its reason.** `Decision` holds the grant that decided it, so the UI can say "denied by group `editors`' rule on `/photos/private`", and the audit log records the grant id.

Implemented exactly as above in `sc_acl::engine::evaluate_locked`, with one clarification beyond the pseudocode: if a partial match at some depth reduces `want` all the way to empty, that's a full grant by composition (e.g. READ at `/a/b` plus WRITE at `/a` together satisfying `want = READ|WRITE`), and evaluation returns `Allowed` immediately rather than continuing to search shallower depths for nothing. This never changes the outcome for a single-bit `want` — the case `effective()` (§3.3 below) actually exercises, one bit at a time.

### 3.3 Crossing with real FS permissions

```rust
let effective = acl_perms & fs_perms(handle);
```

`fs_perms` is computed from the open handle's `Stat` against the process's euid/egid/supplementary groups. POSIX ACLs (`system.posix_acl_access` xattr) would need to factor in for full accuracy, but reading that xattr on every request is expensive → it's measured **once, at share registration, via `faccessat2`**; at runtime we use a mode-bit approximation and pass any real `EACCES` straight through to the caller. Better than reporting "you have permission" and then failing anyway.

### 3.4 Caching

`(user, share, path)` → `Decision` in an LRU. Invalidation is a **global generation counter**: `acl_generation += 1` on any grant/group change; a cached entry whose generation doesn't match is discarded. O(1) full invalidation.

Implemented in `sc_acl::AclEngine`: `evaluate()` keys the cache on `(user, share, path, want)` and checks the entry's stored generation against `AclEngine::generation()` before trusting it; `replace_grants`/`set_memberships` both bump the counter.

### 3.5 Virtual root

**Implemented.** What a user sees as their root is not a real directory — it is a projection of their grant list, computed by `sc_acl::AclEngine::roots()`:

```
entries(user) = { (g.label ?? g.subpath.basename() ?? share.name, g)
                | g ∈ grants(user), g.allow ⊇ READ }
```

- Label collisions get a `" (2)"`, `" (3)"` suffix, in encounter order. Admins can set a label explicitly (recommended).
- If a user has grants on `/a/b` and `/a/c`, the root shows two entries, `b` and `c`. `/a` does not exist as far as the user can tell — its existence is never revealed.
- This mapping **is** the path-hiding mechanism. Every API path has the shape `/{label}/...`; the label → `(ShareRoot, SafePath)` translation happens in exactly one place, at the request entry point.

**A new account starts with no grants and sees nothing** — there are no user homes; access exists only where an admin has explicitly granted it (`GET /api/admin/shares`, `GET|POST /api/admin/grants`, `PATCH|DELETE /api/admin/grants/{id}`). Grants persist in their own database, `acl.db` (`sc_core::acl_store`), not in the disposable `sc-meta` cache — a grant isn't reconstructible from the filesystem the way a cache row is, so losing it silently locks users out rather than just costing a recompute. On first startup against a data directory that already had accounts (an upgrade), each existing account is granted full access to every share once, preserving the pre-grants behavior; a fresh install seeds nothing except the bootstrap administrator. An admin who later revokes every grant on a migrated deployment stays revoked — the migration never re-runs.

A share the caller holds no grant on answers **404, not 403** — 403 would confirm the share exists, which is exactly the information the projection exists to hide.

---

## 4. Directory aggregate ETag

The desktop sync client's sync algorithm depends entirely on "if a directory's ETag is unchanged, its subtree can be skipped." Without it every sync becomes a full tree crawl and the client is effectively unusable. A plain filesystem doesn't provide this property, so a cache is required.

### 4.1 Isolating the cost

**Key design decision: the aggregate ETag is a DAV/NC-path-only cost.**

The native web UI never uses it — per-file ETags, explicit refresh, and WebSocket invalidation pushes are enough. A web-UI-only user pays nothing for this, and the underlying table can be empty without breaking anything.

### 4.2 File ETag (no cache)

```rust
fn file_etag(st: &Stat) -> Etag {
    let h = blake3::hash(&[st.dev, st.ino, st.size, st.mtime_ns as u64].concat_le());
    Etag(hex(&h.as_bytes()[..16]))
}
```

Content is never hashed — reading 10 GB to compute a 10 GB file's ETag isn't viable. A mtime-preserving copy can produce a false collision in theory, but `ino` changes with it, so this doesn't happen in practice.

### 4.3 Aggregate table

```sql
CREATE TABLE diretag (
  share   INTEGER NOT NULL,
  fileid  INTEGER NOT NULL,
  etag    TEXT    NOT NULL,
  rsize   INTEGER NOT NULL,   -- recursive size (oc:size)
  rcount  INTEGER NOT NULL,   -- recursive entry count (UI display)
  gen     INTEGER NOT NULL,   -- share generation at computation time
  valid   INTEGER NOT NULL,   -- 0 = dirty
  PRIMARY KEY (share, fileid)
) WITHOUT ROWID;
```

`share.gen` invalidates a whole share in O(1). An inotify queue overflow, a mount reconnect, or a manual rescan bumps `gen`, which invalidates every existing row at once.

### 4.4 Invalidation (write path)

On a change event at path `P`:

```
mark_dirty(P):
    for ancestor in P.ancestors():          # from P's parent up to the share root
        pending.insert((share, fileid(ancestor)))
```

`pending` is an in-memory `HashSet`, flushed as one transaction after a **200 ms debounce**. A bulk copy of tens of thousands of files still touches a small ancestor set, so DB writes don't explode — batching is what makes this design work.

### 4.5 Recomputation (read path)

```
compute(dir) -> (etag, rsize, rcount):
    row = diretag[dir]
    if row.valid && row.gen == share.gen: return row

    # single-flight: don't let concurrent requests recompute the same directory
    guard = inflight.entry(dir).lock()

    children = getdents64(dir) + statx(each)
    acc = Hasher::new()
    (rsize, rcount) = (0, 0)
    for c in children.sorted_by(name):        # sort is required — readdir order isn't stable
        if c.is_dir:
            (ce, cs, cc) = compute(c)         # implemented as an explicit stack, not recursion
        else:
            (ce, cs, cc) = (file_etag(c), c.size, 1)
        acc.update(c.name); acc.update(ce)
        rsize += cs; rcount += cc
    store(dir, hex(acc.finalize())[..16], rsize, rcount, share.gen, valid=1)
```

**Cost**: a change to one deep file dirties only that path's ancestors. Sibling subtrees stay valid and their cached values are read straight back. Recompute cost is O(depth × fan-out), not O(subtree) — on a million-file tree, a root PROPFIND after a single-file change is a few milliseconds.

**Cold start**: an empty DB means the first computation is a full tree walk. This is not mitigated — an NC client's first sync PROPFINDs the whole tree anyway, so the cost amortizes into work that was happening regardless. A low-priority background warmer runs at startup; the web UI stays unaffected per §4.1 while it runs; progress is exposed in the admin UI.

**Recursion depth**: an explicit stack, bounded by `policy.max_depth`. Symlink loops are already blocked by `RESOLVE_NO_SYMLINKS`/`RESOLVE_IN_ROOT`, but a hard-linked directory on a nonstandard filesystem is defended against with a visited-`(dev, ino)` cycle check too.

### 4.6 Invalidation sources

| Source | Handling |
|---|---|
| Our own writes | `mark_dirty` called directly right after commit — doesn't wait on the watcher, so our own writes are immediately consistent |
| inotify events | `mark_dirty`. A subtree whose watch registration failed (`ENOSPC`) falls back to lazy mode |
| `IN_Q_OVERFLOW` | `share.gen += 1` + background rescan |
| Lazy revalidation | Cached stat disagrees with a fresh `statx` at read time → `mark_dirty` |
| Periodic rescan | Default every 6 hours. Batched `statx`, compares mtime |
| Manual (admin) | `share.gen += 1` |

---

## 5. Upload sessions

> Summary only. The full protocol spec, `IntervalSet`, 413 negotiation, and the
> compat chunking v2 mapping are in **`DESIGN-UPLOAD.md`**.
> Chunk size is a **fixed, configurable default of 10 MiB** — no adaptive resizing.

### 5.1 States

```
(none) ──POST──▶ Receiving ──PATCH*──▶ Receiving ──(runs == [0,len))──▶ Finalizing ──▶ Done
                     │                                                     │
                     │ DELETE / TTL elapsed                                │ success
                     ▼                                                     ▼
                Aborted / Expired                                        Done
                     │                                                     │
                     └──────────── GC: unlink part/spool ◀──────────────────┘
```

A session is created directly into `Receiving` — there is no separate "created but not receiving" state.

### 5.2 Why an interval set

TUS core assumes a single `Upload-Offset` (sequential only), but our own client sends parallel chunks (3–5x faster than one stream behind a proxy like Cloudflare that caps single-stream throughput). An interval set accepts any order; for TUS compatibility, `HEAD`'s `Upload-Offset` reports the **contiguous run ending from 0**. Both protocols run on the same engine underneath.

### 5.3 Assembly-free assembly

```
create:  fd = openat2(parent, part_name, O_CREAT|O_EXCL|O_WRONLY|O_NOFOLLOW, 0600)
         ftruncate(fd, total_len)          # sparse — no immediate disk usage
patch:   pwrite(fd, chunk, offset)         # safe under concurrency
         received.insert(offset..offset+n)
         commit                            # after pwrite returns, never before (§5.4)
finish:  assert received == [0, total_len)
         fdatasync(fd)
         utimensat(fd, meta.mtime)         # restore original mtime
         fchmod / fchown(policy)
         renameat2(parent, part_name, parent, dest, flags)
         fsync(parent)                     # rename durability
         mark_dirty(dest); delete session
```

Zero copies. The part file lives in the **same directory** as the final file, so the rename is atomic and never hits `EXDEV`.

One side effect: an in-progress upload is visible in the destination directory as a dot-file. Most scanners (Jellyfin included) ignore dot-files; a share-level dedicated part-file subdirectory for tools that don't is not currently configurable — a control name in the destination directory is the only supported behavior today.

### 5.4 Crash safety — always under-report

If the process dies after a successful `pwrite` but before the DB commit, the server remembers that chunk as never received → the client resends the same offset → the same bytes get overwritten. **Idempotent and safe.**

The reverse order (commit, then write) would make the server believe it has bytes it never actually received: **silent data corruption.** Never do this. The ordering rule is pinned by a code comment, a test, and code review.

### 5.5 Optimistic concurrency

If `If-Match` is given, the target is re-stat'd and its ETag compared right before the finalize rename. There's an unavoidable window (microseconds) between the comparison and the rename — the kernel has no compare-and-rename, so this can't be closed in principle. Creating a brand-new file closes the window completely via `RENAME_NOREPLACE`. This asymmetry is documented, not hidden.

### 5.6 GC

- Expired sessions: unlink the part file, delete the row.
- Orphaned part files: a `.scpart-*` with no session row, past a TTL (24h), gets cleaned up — crash residue.
- Per-user concurrent session count / total reserved bytes are capped, defending against disk exhaustion via `ftruncate` sparse-reservation abuse.

`sc_core::ops` stages under the same reserved `.scpart-{uuid}` name (see `ops::part_name`), so a copy/move/write killed mid-syscall leaves residue this sweep does not reach: it only looks inside directories an *upload* session touched, and it matches part files to session rows. Such residue is invisible everywhere (that is the point of the reserved name) and costs disk space, not data — the source of a killed copy or move is always still intact.

The sweep logic (`sc_upload::UploadEngine::gc`) exists and is exercised by tests, but nothing in `sc-server` calls it on a timer — its only caller, `UploadBridge::drain()`, itself has no caller anywhere in the workspace. Until something schedules it, expired sessions and orphaned part files are cleaned up only by whatever ad hoc mechanism an operator sets up externally, not automatically. See `DESIGN-UPLOAD.md` §9.

---

## 6. Concurrency

| Resource | Strategy |
|---|---|
| diretag recomputation | `DashMap<FileId, Arc<Mutex<()>>>` single-flight — prevents duplicate work on the same directory |
| SQLite writes | Single writer task + channel. WAL means readers run in parallel |
| SQLite reads | `r2d2` pool (sized to core count) |
| Blocking FS operations | Dedicated blocking pool, sized per storage class (HDD 4, NVMe 32) |
| Invalidation events | 200ms debounce buffer → batched flush |
| WebDAV LOCK | In-memory `DashMap` + SQLite persistence (locks survive a restart) |
| Downloads | Streamed with the FD held open. Per-user/global concurrent-connection caps |

---

## 7. Test strategy

| Target | Method |
|---|---|
| `SafePath::parse` | `cargo-fuzz` + property test (no `..`/absolute/NUL in anything that parses) |
| VFS escape | Symlink farms (`../../etc/passwd`, `/proc/self/root`, loops, a dir→symlink swap raced mid-resolve) — every API must refuse |
| ACL evaluation | Exhaustive decision-table test: grant combinations × path depth × permission bits |
| diretag | Random tree → random mutation → cached ETag matches full recomputation (model-based test) |
| Upload | Kill the process at an arbitrary point → resume → final file byte-identical to source |
| External concurrent change | A separate process rsyncs/renames/truncates the same directory mid-test |
| WebDAV | Litmus + rclone conformance suite |
| NC compatibility | Real desktop client + Android app driven in a CI container |

---

## 8. Trash

Per-share, flat: `<share>/.sctrash/{uuid}-{base64url(original share-relative path)}`. `.sctrash` has one level, not a mirror of the share tree — the original parent directory has nowhere else to live, so it's folded into the entry name.

Restore recovers the full original path from the encoded name and recreates any missing ancestor directories (`ShareRoot::mkdir` is one level at a time; there is no recursive mkdir in that API) before renaming the file back. Before this existed, only the basename was kept, so restoring a file trashed from `docs/2024/report.pdf` silently dropped it at the share root — no error, no indication anything had moved, confirmed by inspecting on-disk state after an actual delete+restore, not by reading the code. A trash entry written before this fix has no recorded parent (the old format never wrote one) and falls back to the old basename-only behavior on restore, which is the documented legacy shape, not a bug.

`TrashMode` has two variants: `Off` (**default** — deletes are handled directly, no `.sctrash/` is ever created) and `ShareLocal` (described above). A third variant, `Central` (a shared, cross-share trash location), was removed: nothing ever accepted it as input at any config or API boundary, and the trash backend only ever treated it identically to `ShareLocal` rather than implementing or rejecting it, so it was dead weight rather than a real option (YAGNI).

The mode is a per-share toggle, not a server-wide setting — `TrashMode` lives on `SharePolicy`, which is already per-share. It is exposed via `Core::update_share`'s `trash_enabled` argument (`PATCH /api/admin/shares/{id}`) and persisted in `shares.db` (`ShareStore::set_trash_override`/`trash_override`, `crates/sc-core/src/share.rs`), independent of whether the share is admin-created or defined in `config.toml`, and without ever rewriting `config.toml`. Turning trash off never deletes an existing `.sctrash/` directory — it only stops routing new deletes through it.

A `config.toml` share's name and host path are overridden the same way (`share_identity_override`, applied by `register_shares` via `Core::apply_identity_override`), so an admin-UI rename survives a restart rather than being reverted by the file. Deletion stays refused: the config entry would declare the share again on the next start, and there would be nothing to override.
