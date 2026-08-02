# Architecture

A lightweight file-management service on the native filesystem. Rust backend,
Svelte frontend.

## 0. Design principles (the basis for every decision)

1. **The filesystem is the only source of truth.**
   The DB is a *cache* — deletable and rebuildable at any time. No logic
   anywhere may treat "not in the DB" as "the file doesn't exist." This is
   exactly where DB-of-record designs fail. (Scope note: this principle covers
   `sc-meta`'s filesystem-derived cache — `node`/`diretag`. It does not cover
   `auth.db`, `acl.db`, or `links.db`: accounts, grants, and share links are
   not reconstructible from the filesystem and must be backed up like any
   other real state. See §4.3.)
2. **A path is a kernel handle, not a string.**
   Every FS access goes through a share root's directory FD via
   `openat2(RESOLVE_BENEATH)` or equivalent. String path concatenation or
   normalization followed by a prefix check is TOCTOU-prone and is forbidden.
3. **A shared folder is not ours.**
   Jellyfin, `*arr`, Samba, and rsync are assumed to touch the same directory
   at the same time. No sidecar-file litter, ownership/permissions are
   preserved, and external changes are reflected immediately.
4. **The compat layer does not invade the core.**
   Not one `if compat` branch, `oc_`-prefixed field, or NC-only table may
   exist in the core. It must be removable in its entirety via a compile
   feature.
5. **The default is least privilege.**
   No user homes, no symlinks, SMB off, no inline content rendering — all by
   default.

### Fixed premises

- **The deployment target is Linux only, in a Docker container.**
  Windows/macOS are dev-convenience fallback paths only, not officially
  supported. Reason: `openat2(RESOLVE_BENEATH)`, `statx`, Landlock,
  `copy_file_range`, and `renameat2` are all Linux-only, and they are the
  spine of the security and performance design.
- **SMB is Samba orchestration** (§9).

The container premise adds real constraints — Docker's default seccomp
profile can block `openat2`/`landlock_*`, overlayfs breaks inode stability,
bind-mount boundaries produce `EXDEV`, and `fs.inotify.max_user_watches`
cannot be raised from inside a container. Detail in **`DEPLOYMENT.md`**.

Code-level detail (VFS API, ACL evaluation, directory ETag propagation,
upload state machine) is authoritative in **`proposals/stowcloud-2-core-vfs.md`**.

---

## 1. Crate structure

```
stowcloud/
├── crates/
│   ├── sc-vfs/            # Kernel-handle safe FS layer. Minimal deps. The security core.
│   ├── sc-meta/           # SQLite cache: fileid, etag, index, upload sessions
│   ├── sc-auth/           # Users/groups/sessions/app passwords/TOTP
│   ├── sc-acl/            # Share definitions + permission evaluation
│   ├── sc-watch/          # fanotify/inotify → invalidation events
│   ├── sc-core/           # Domain API assembled from the above (protocol-agnostic)
│   │
│   ├── sc-search/         # Tiered search (BFS walk + FTS5-adjacent trigram index)
│   │
│   ├── sc-http/           # axum: native REST API + static frontend serving
│   ├── sc-dav/             # RFC 4918 WebDAV (protocol-pure implementation)
│   ├── sc-upload/         # TUS resumable chunked upload
│   ├── sc-preview/        # Thumbnail orchestration (cache, single-flight, worker jail)
│   │
│   ├── sc-compat-nc/      # ★ legacy-client compat layer (feature = "compat-nc")
│   ├── sc-smb/            # Samba config generation/sync (feature = "smb")
│   │
│   └── sc-server/         # Main binary. Assembles the router, nothing else.
└── web/                   # SvelteKit (adapter-static, SPA)
```

**Dependencies point one way.** `sc-compat-nc` depends on `sc-core`/`sc-dav`,
never the reverse. Building `sc-server` with `--no-default-features` drops
every NC-related line from the binary entirely.

`sc-preview`'s worker jail is a **forked child of the running `sc-server`
process** (`fork(2)`, no `execve`) — not a separate crate or binary. Its own
module doc (`crates/sc-preview/src/worker/jailed/mod.rs`) states plainly that
a re-exec'd dedicated worker binary would be "a strictly better shape for
production" and is "deliberately left as follow-up." There is no
`sc-preview-worker` source, binary, or Cargo target anywhere in this
workspace today — an earlier draft of this document named one prematurely.

---

## 2. sc-vfs — the security core

### 2.1 Path resolution

```rust
/// O_PATH directory FD for a share root. Held for the process's lifetime.
/// root_dev/fstype feed the mount-boundary (EXDEV) check and the FS gate.
pub struct ShareRoot {
    id: ShareId, dirfd: OwnedFd,
    root_dev: u64, fstype: FsType,
    policy: Arc<SharePolicy>,
}

/// A validated share-relative path. Checked only at construction, immutable after.
pub struct SafePath(SmallVec<[Component; 8]>);
```

The full definition and syscall contract are authoritative in
**`proposals/stowcloud-2-core-vfs.md`**.

- What `SafePath::parse()` rejects: absolute paths, `..`, `.`, empty
  components, NUL/control characters, components containing a slash,
  Windows-reserved names (`CON`, `PRN`, `NUL`, `AUX`, `COM1..9`, `LPT1..9` —
  for cross-platform SMB clients), trailing `.`/space, the NTFS ADS separator
  `:`, paths over 4096 bytes, components over 255 bytes, depth beyond
  `policy.max_depth`, and our own control-file prefixes (`.sctrash`,
  `.scpart-`, `.scmeta`, `.scindex`).
- **Unicode normalization**: stored names are normalized to NFC, but **the
  filename on disk is never rewritten** — changing it ourselves would break
  other services reading the same tree. Lookups instead try both NFC and
  NFD; only new creations force NFC. This absorbs the mismatch between
  macOS (NFD) SMB clients and Linux (NFC).
- **Homoglyph attacks**: a confusable name mixing scripts with an existing
  sibling in the same directory gets a warning badge in the UI. Not blocked
  — the false-positive cost is too high.

### 2.2 Kernel calls

| Operation | Linux | Fallback |
|---|---|---|
| Open | `openat2(dirfd, rel, resolve_flags(policy))` — default policy (`Deny`) uses `RESOLVE_BENEATH \| RESOLVE_NO_MAGICLINKS \| RESOLVE_NO_SYMLINKS` | `cap-std`'s component-wise `O_NOFOLLOW` walk |
| Metadata | `statx(AT_SYMLINK_NOFOLLOW, STATX_BTIME \| STATX_INO \| STATX_MNT_ID)` | `fstatat` |
| Listing | `getdents64` + `d_type` (avoids a stat storm). **Needs an `O_RDONLY` handle** — an `O_PATH` anchor cannot read it (`proposals/stowcloud-2-core-vfs.md`a) | `readdir` |
| Copy | `copy_file_range` (reflink / server-side) | 64 KiB buffer loop |
| Move | `renameat2(RENAME_NOREPLACE)` — copy+unlink across devices | `renameat` |
| Transfer | `sendfile`/`splice` (plaintext HTTP) | `ReaderStream` |

`RESOLVE_BENEATH` makes the kernel **atomically** block a path resolution
that would escape root via a symlink, `..`, or a mount-point crossing. Unlike
a userspace check, TOCTOU is not merely mitigated — it is impossible in
principle. On a kernel without `openat2` (< 5.6) a startup warning fires and
the `cap-std` fallback takes over.

### 2.3 Symlink policy

Three levels per share, **default `Deny`**:

- `Deny` — a symlink is visible in listings but cannot be opened. (Hiding it
  would just make the user think the file vanished.)
- `WithinShare` — the target is resolved and checked to be inside the share
  root before it's allowed. Uses `RESOLVE_IN_ROOT` (treats root like a
  chroot, clamping any link that would escape it) instead of
  `RESOLVE_BENEATH`.
- `Follow` — trusted environments only. Explicit UI warning.

`RESOLVE_NO_MAGICLINKS` is always on (blocks escape via `/proc/self/fd/*`).

### 2.4 Process-level isolation

- **Landlock** (Linux 5.13+): immediately after startup, the process
  restricts itself to the configured share paths plus the data directory. If
  a bug slips past path validation, the kernel is a second wall.
- **seccomp** filter: blocks `ptrace`, `process_vm_*`, `mount`, `kexec`,
  `bpf`, `userfaultfd`, and similar.
- systemd hardening: `NoNewPrivileges`, `PrivateTmp`,
  `ProtectSystem=strict`, `ProtectHome`, `RestrictAddressFamilies`,
  `MemoryDenyWriteExecute`, `SystemCallArchitectures=native`.
- Runs as a dedicated unprivileged user. No setuid, no user impersonation
  (that would require staying root-resident, which explodes the attack
  surface).

Both Landlock and the seccomp filter are actually applied at `serve`
startup (`crates/sc-server/src/hardening.rs`), best-effort: a kernel that
lacks support logs a warning and continues without that layer rather than
refusing to start.

---

## 3. Permission model

### 3.1 Concepts

```
Share  : Administrator-defined. host_path + SharePolicy
         { symlink, cross_mount, id_strategy, trash, mode_file, mode_dir, chown, max_depth }
Grant  : (principal, share, subpath, allow, deny, inherit, label)
Perms  : READ WRITE CREATE DELETE RENAME MOVE SHARE DOWNLOAD  (bitflags)
```

File-creation permission is applied explicitly via **`mode_file`/`mode_dir`,
not `umask`**. `umask` can only turn bits off, so it cannot express "0664 so
Jellyfin can read it" (`DEPLOYMENT.md` §6.2).

- User homes are **opt-in**. `homes.enabled = false` by default. Enabling it
  creates a `{username}` directory under `homes.root` from a template and
  auto-grants the owner full access. With it off, a user sees nothing beyond
  folders explicitly Granted to them — the root listing is a "virtual folder
  list," not a real directory.
- A subpath Grant may be **narrower or wider** than its parent. Evaluation
  scans from the deepest match outward; **at equal depth, DENY wins.** A
  deeper ALLOW beats a shallower DENY (that is what "most specific" means).
  No match → deny by default. The exact algorithm is authoritative in
  `proposals/stowcloud-2-core-vfs.md`
- **Effective permission = ACL permission ∩ actual FS permission.** A WRITE
  Grant means nothing if the service user can't write to that directory, so
  the admin UI probes with `faccessat` at share registration and warns.

### 3.2 The virtual root

Paths exposed to users take the form `/{grant_label}/sub/path`; the host
path **never appears in an API response, error message, or log at the
default level.** Only the admin API shows real paths.

---

## 4. sc-meta — the cache over the FS source of truth

SQLite (WAL, `synchronous=NORMAL`, single file, app data directory). No ORM,
`rusqlite` directly.

### 4.1 fileid — a stable integer ID

Both WebDAV and compat clients need a "file identifier that survives a
rename." On a native FS the only way to provide that is an inode mapping.

```sql
CREATE TABLE node (
  id       INTEGER PRIMARY KEY,   -- rowid = stable fileid. Zero extra storage cost
  share    INTEGER NOT NULL,
  parent   INTEGER NOT NULL,      -- parent node.id. Share root is 0
  name     TEXT    NOT NULL,      -- one path component. ★ full paths are never stored
  dev      INTEGER NOT NULL,
  ino      INTEGER NOT NULL,
  btime_ns INTEGER,               -- statx STATX_BTIME. Used to detect inode reuse
  flags    INTEGER NOT NULL,      -- is_dir | pinned | seen
  size     INTEGER,               -- cached for sort/display (avoids an HDD seek)
  mtime_ns INTEGER
);
CREATE UNIQUE INDEX node_ident ON node(share, dev, ino, btime_ns);
```

**Never storing the full path is the load-bearing decision here.** Two
reasons, both fatal if ignored:

1. **Size**: paths average ~100 bytes each. Across 60M files that's 10 GB,
   which destroys a 32 GB SSD budget.
2. **Rename cost**: with a stored path, renaming a directory becomes an
   `UPDATE` across every descendant. With `(parent, name)` it's **one row.**

The single index (`node_ident`) is also deliberate — path → fileid already
has a `statx` result in hand, and fileid → path is resolved by rowid lookup
plus recursion through `parent`. Path resolution is **always done by the
filesystem**, never the DB, so no forward index is needed.

**The search index does not use this table.** The name index is a
self-contained block-compressed trigram file (`proposals/stowcloud-5-search.md`), and
content indexing would live in a separate `content.db`. So enabling search
does not disturb lazy allocation.

- Inode-reuse risk is blocked by `btime`. On filesystems without `btime`
  support (some ext4 mount options, NFS), a share can be set to
  `id_strategy = path` instead — IDs then change on rename, which makes the
  NC desktop client treat a rename as delete+upload. The UI states this
  trade-off explicitly.
- **Lazy allocation**: a row is created only when its fileid is actually
  requested. The web UI operates on paths, so **a web-UI-only deployment
  never creates a single row.**
- **Rebuildable**: deleting the DB doesn't break the service; IDs are just
  reissued.
- **GC**: only rows whose `(dev, ino)` is gone get deleted. A live file's
  fileid is never reissued — that would cause an NC client to delete and
  re-download it.

Size math and hard guards are in **`DESIGN-FOOTPRINT.md`**.

### 4.2 ETag

- **File ETag** = hex of `blake3(dev, ino, size, mtime_ns)[..16]`. Content is
  never hashed (too costly at scale). An external service doing an
  mtime-preserving copy could in principle produce a false match, but size
  and inode change together in practice, which is enough.
- **A directory's ETag must change whenever a descendant changes.** The
  desktop sync client's sync entirely depends on this property, which a
  pure FS cannot provide — hence a cache:

```sql
CREATE TABLE diretag (
  share INTEGER, fileid INTEGER,
  etag TEXT NOT NULL, rsize INTEGER NOT NULL, rcount INTEGER NOT NULL,
  gen INTEGER NOT NULL,          -- share generation at compute time. Mismatch = invalid
  valid INTEGER NOT NULL,        -- 0 = dirty
  PRIMARY KEY (share, fileid)
) WITHOUT ROWID;
```

  On a change event, the ancestor chain from that node up to the share root
  is marked `valid=0` (O(depth) updates per event, batched after a 200 ms
  debounce). A PROPFIND against an invalid entry recomputes it by hashing
  the children's `(name, etag)` pairs with blake3 and caches the result. If
  the watcher dies or the event queue overflows, `share.gen` is bumped for
  an O(1) full invalidation followed by a background rescan. Full algorithm
  in `proposals/stowcloud-2-core-vfs.md`

  **This aggregation cost is DAV/NC-only.** The native web UI uses per-file
  ETags plus WebSocket invalidation, so it works fine even with this table
  empty.

### 4.3 Everything else — one SQLite file per crate that needs one

`sc-meta`'s `meta.db` holds only `node`/`diretag`/`dav_prop`/`share_gen` —
and, per §0.1's scope note, that file really is disposable: deleting it just
means fileids and cached ETags get regenerated.

Every other piece of state lives in its **own** SQLite file, opened by the
crate that owns it, and none of them are disposable in the same sense —
deleting any of these loses real, non-reconstructible state:

| File | Owner | Holds |
|---|---|---|
| `auth.db` | `sc-auth` | users, groups, memberships, sessions, app passwords, audit log |
| `acl.db` | `sc-core` (`acl_store`) | shares, grants |
| `links.db` | `sc-core` (`links`) | share links |
| `upload.db` | `sc-upload` | upload sessions |
| `dav-locks.db` | `sc-dav` | WebDAV locks |
| `compat-nc.db` | `sc-compat-nc` (feature-gated) | `nc_*`-prefixed NC-only tables |

Splitting grants and share links out of `meta.db` was a deliberate
correction, not the original design: putting a grant in a file documented as
"delete it any time" would mean "fix a corrupt cache by deleting it" silently
locks every non-admin user out of everything. Both `sc-core/src/acl_store.rs`
and `sc-core/src/links.rs` carry this reasoning in their own module docs.

**Note**: user metadata on a file (tags, favorites, comments) belongs here,
keyed by fileid — never as a sidecar file in the target directory. (A share
can optionally turn on `.scmeta/` sidecar mode, off by default, as an escape
hatch for users who want portability.)

---

## 5. Coexisting with other services

This section is the core difference between this project and
servers that own their storage.

### 5.1 Change detection

| Method | Condition | Character |
|---|---|---|
| `fanotify` + `FAN_MARK_FILESYSTEM` | Kernel 5.1+, needs `CAP_SYS_ADMIN` | Watches the whole mount, independent of directory count. `CAP_SYS_ADMIN` is effectively root in a container, so **off by default** (`DEPLOYMENT.md` §5.1) |
| Recursive `inotify` | Unprivileged | Hits `max_user_watches` (default 8192–65536). Fails on a million-directory tree |
| Periodic rescan | Always | Fallback / correctness backstop. Default every 6 hours, batched `statx` |

Because granting `CAP_SYS_ADMIN` in a container is often unwelcome, the
default is a hybrid: an inotify watch-count ceiling, with the subtree
demoted to "lazy" (revalidate on access) once it's exceeded.

Every read path additionally performs **lazy revalidation**: compare the
cached `(size, mtime, ino)` against a live `statx` and invalidate on
mismatch. Correctness holds even if the watcher is completely dead — only
performance degrades.

**Wiring, end to end.** This paragraph used to say the producer side did not
exist — that `sc-watch` implemented the hybrid logic but nothing in the
running server ever called `subscribe`/`touch`, so a change made by Jellyfin,
rsync or another user's SMB session never showed up live. It does now:

- `sc-server`'s `watch_subscribe`/`watch_unsubscribe` (`app.rs`) register and
  release the OS-level watch, driven by the client's own WebSocket
  subscribe/unsubscribe — so watches exist for exactly the directories
  someone is looking at.
- `bridge.rs` LRU-touches a directory on `fs_list`/`fs_stat`.
- A watcher that fails to start leaves `watcher: None`, and both calls become
  no-ops — "no live push", never an error. Lazy revalidation still holds
  correctness in that state, which is why degrading is acceptable.
- Two pieces stayed half-wired until `d800b57`: `full_threshold` was
  forwarded into `sc-watch` and never read, and nothing forced the periodic
  rescan on NFS/FUSE mounts, where inotify does not see another host's writes
  at all. Both are closed, with tests.

`GET /api/events` (WebSocket), `WsHub`, and the frontend hub (backoff,
per-path refcounting) were always in place on the consumer side. See §14 M3.

### 5.2 Writing — rules that don't break other services

1. **Atomic replace**: create `.scpart-{rand}` in the same directory
   → write → `fsync` → `renameat2` to swap in. Same directory because rename
   atomicity only holds within one filesystem. The name is exactly
   `.scpart-…` and not `.{name}.scpart-…` so that `sc_vfs::is_reserved_name`
   — a `starts_with` test — actually matches it: a write killed mid-syscall
   must leave something hidden from listings, WebDAV, SMB's `veto files` and
   the search walker, not a half-written file presenting itself as real.
2. **Ownership/permission preservation**: replacing an existing file carries
   the original's mode/uid/gid/xattr onto the new file before the rename.
   New files get the share's configured `umask`/`uid`/`gid` (commonly
   `0664`/`0775` + a shared group, so Jellyfin can read them).
3. **mtime preservation**: if the client sends the original mtime via
   `X-OC-Mtime` or TUS metadata, `utimensat` restores it on upload — keeps
   media-library sort order intact.
4. **Optimistic concurrency**: an overwrite requires `If-Match: <etag>` (the
   web UI sends it automatically). A mismatch returns `412` and the UI
   offers conflict resolution (keep both / overwrite / cancel) — an external
   service's silent change is never clobbered without a chance to notice.
5. **Read consistency**: a download keeps its FD open for the entire
   response. If another service does a rename-replace mid-download, we keep
   serving the consistent old version to completion (POSIX guarantee).
   In-place overwrite can't be defended the same way, so an ETag mismatch
   detected mid-stream cuts the connection instead.
6. **Locking**: `flock` (advisory) is taken, but nothing ever assumes another
   service also respects it. A WebDAV `LOCK` is a logical lock valid only
   within our own DB, and is documented as such.

### 5.3 Delete / trash

A central trash inside a shared folder would force cross-device rename into
a copy, so trash is a per-share setting with two modes:

- `off` (**default**): immediate unlink. No `.sctrash/` is ever created.
- `share_local`: rename to `<share>/.sctrash/{fileid}-{name}`. Same
  filesystem, so instant. GC after 30 days.

The toggle is exposed on every row of the admin "Folder shares" screen
(`web/src/lib/ui/admin/ShareManagementSection.svelte`) via
`PATCH /api/admin/shares/{id}` (`trash_enabled`), and is persisted in
`shares.db` (`ShareStore::set_trash_override`, `crates/sc-core/src/share.rs`)
— independently of whether the share itself is admin-created or defined in
`config.toml`, and without ever rewriting `config.toml`. Turning trash off
never deletes an existing `.sctrash/`; it only stops new deletes from using
it.

A `config.toml` share's **name and host path** are editable on that same
screen, by the same mechanism: it owns no `share_` row, so the new values go
to `share_identity_override` keyed by its `ShareId`, and
`app::register_shares` runs every config entry through
`Core::apply_identity_override` (and `apply_trash_override`) as it builds the
`ShareDef` at startup. That is what makes the edit survive a restart instead
of being reverted by the file it overrides; the server logs the divergence,
since `config.toml` no longer describes what is running. **Deleting** such a
share is still refused (`share.config_defined_not_deletable`) — an edit can
be overridden because the config entry keeps declaring the share, but nothing
in `shares.db` can stop it from declaring it, so the next restart would bring
it back with nothing left to override.

There used to be a third mode, `central` (move to the app data directory),
but nothing ever accepted it as input at any config or API boundary, and the
trash backend silently treated it as `share_local` rather than implementing
it or rejecting it — a no-op variant with no way to reach it. It has been
deleted from `TrashMode` (YAGNI) rather than kept around unimplemented.

---

## 6. Chunked upload (behind Cloudflare)

### 6.1 Cloudflare constraints

| Constraint | Value | Response |
|---|---|---|
| Request body size | Free/Pro/Biz **100 MB**, Enterprise 500 MB+ | Default chunk 10 MiB (advisory, no server-enforced ceiling) |
| Origin response timeout | 100 s (524 error) | A 10 MiB chunk has margin even on a 512 kbps link |
| Request body buffering | Happens on non-Enterprise plans | Keep chunks small; don't rely on streaming |
| Real client IP | `CF-Connecting-IP` | Only trusted after checking a trusted-proxy CIDR list |

### 6.2 Protocol: TUS as first-class

`tus.io` v1 (aligned with the IETF `draft-ietf-httpbis-resumable-upload`) is
the native protocol. Why a standard instead of inventing one: mature client
libraries, battle-tested resume semantics, proxy-friendly (plain
POST/PATCH/HEAD).

```
POST   /api/uploads              Upload-Length, Upload-Metadata  → 201 + Location
HEAD   /api/uploads/{id}                                          → Upload-Offset
PATCH  /api/uploads/{id}         Upload-Offset, Content-Type: application/offset+octet-stream
DELETE /api/uploads/{id}
```

Extensions: `creation-with-upload` (small files in one round trip),
`checksum` (crc32c/blake3), `expiration`, `termination`.

### 6.3 Assembly without assembly

Storing chunks as individual files and concatenating at the end doubles disk
I/O. Instead:

- On upload start, one `.scpart-{id}` is created at the destination
  and reserved to final size via sparse `ftruncate` (the reserved-prefix
  spelling of §5.2's rule 1, matching `sc_upload::engine`).
- Each PATCH writes directly to its own offset via `pwrite`. Parallel chunks
  are safe.
- The received byte map (offset ranges) is stored in
  `upload_sessions.received`.
- On completion, the map is checked to cover `[0, len)` → `fsync` → metadata
  transplant → `renameat2`. **Zero copies.**
- Incomplete sessions are GC'd after a TTL (default 24h). State survives a
  server restart (it's in DB + disk).

### 6.4 Fixed chunk size

**No dynamic/adaptive chunk sizing.** Floor **5 MiB** (enforced), default
**10 MiB**, **no ceiling**. Admins can change the server default; users can
change their own.

No ceiling is needed because the server streams a chunk straight to disk via
`pwrite` through a 256 KiB buffer — **memory use is independent of chunk
size.** Defense instead comes from an idle timeout (60 s) and a concurrent
request limit. Cloudflare-class intermediary limits are advertised as
**recommendations** via `/api/capabilities`, never enforced.

Rationale: the desktop sync client actually does dynamic sizing
(starts at 100 MiB, targets 60 s, ranges 5 MB–5 GB), and the result is a 413
on the very first chunk behind Cloudflare — and clients don't reliably
respect an advertised ceiling anyway. Paying the complexity buys nothing.
Fixed size + parallel transfer (default 4) gets the same throughput far more
simply.

Detail — TUS spec, interval sets, state machine, 413 negotiation, NC
chunking v2 mapping — in **`proposals/stowcloud-7-upload.md`**.

### 6.5 Integrity

Every chunk carries `Upload-Checksum: crc32c <base64>` (hardware-accelerated,
essentially free). On completion a full blake3 is computed and compared
against a client-supplied value — optional, since it costs more at scale.
Default verification is size + per-chunk checksum.

---

## 7. HTTP layer

- `axum 0.8` / `hyper 1.x` / `tokio`. Blocking FS work is delegated to a
  dedicated blocking pool (sized to core count, tunable per storage
  characteristics). `sc-vfs` sits behind an async trait so a future
  `io_uring` (compio) transition stays possible.
- The frontend is embedded in the binary via `rust-embed` → a single static
  binary deployment.
- Response compression applies only to text MIME types (gzipping
  already-compressed media wastes CPU for nothing).

### 7.1 Content-origin separation (important)

Serving uploaded HTML/SVG/PDF from the same origin as the app is **stored
XSS against the whole session.** So:

- Downloads/previews are served from a separate host
  (`content.example.com`). That origin never carries the session cookie.
- Access uses a **short-lived signed URL**:
  `HMAC-SHA256(secret, fileid|etag|user|exp|disposition)`, 5-minute
  validity.
- Every download response: `Content-Disposition: attachment` (inline only
  for a preview MIME allowlist), `X-Content-Type-Options: nosniff`,
  `Content-Security-Policy: sandbox; default-src 'none'`,
  `Cache-Control: private, no-store`.
- For single-domain deployments, a `blob:` + Service Worker rendering
  fallback exists, but a separate origin remains the default recommendation.

### 7.2 Authentication

- Session: `__Host-`-prefixed cookie, `Secure; HttpOnly; SameSite=Lax`,
  server-side session record (revocable immediately), sliding expiry.
  ("Revocable immediately" means the session record stops authenticating
  new requests at once — it does not mean an already-open WebSocket gets
  force-closed. `WsHub::revoke()` exists and is tested but has no caller
  today, so a revoked session's push channel stays open until it drops on
  its own. See §14 M6.)
- CSRF: `SameSite=Lax` + a required `Sc-Csrf` header on state-changing
  requests + `Origin` validation. **`/dav/**` and NC routes reject cookie
  auth** and accept only Bearer/Basic, removing the CSRF surface for those
  entirely. Native TUS (`/api/uploads`) accepts cookies but requires a
  custom header, which a plain `<form>` cannot send. Exact matrix in
  `DESIGN-AUTH.md` §4.
- Password: Argon2id (m=48 MiB, t=3, p=1) + a concurrency semaphore of 4
  (192 MiB peak memory cap). No peppering (key-rotation cost outweighs the
  benefit). 10-character minimum.
- TOTP 2FA + recovery codes.
- **The account password works, unmodified, over WebDAV, NC, and SMB.** DAV's
  per-request Argon2 cost is solved by a connection-memoized,
  process-ephemeral-key verification cache (~250,000× cheaper). SMB derives
  an NT hash from the account password in parallel. **A TOTP user must use
  an app/dedicated password on both paths** — Basic and NTLM have no slot
  for a second factor, so allowing the account password through would be a
  silent 2FA bypass. Detail in `DESIGN-AUTH.md` §2.4/§4.
- **App-only passwords**: still the recommended path. Scoped (read-only /
  specific share), last-used time and IP recorded, revocable per device —
  none of which is possible for a connection made with the account
  password, so the UI marks those "unrestricted."
- Login rate limiting (IP + account, both), timing uniformity, account
  enumeration resistance.

---

## 8. WebDAV (sc-dav)

A native RFC 4918 **Class 2** implementation (LOCK included). Class 2 is
required because macOS Finder and MS Office refuse to mount/edit without
LOCK.

- Methods: `OPTIONS PROPFIND PROPPATCH MKCOL GET HEAD PUT DELETE COPY MOVE
  LOCK UNLOCK`
- Advertises `DAV: 1, 2, 3`. `Depth: 0/1/infinity` (infinity is off by
  default for PROPFIND — a DoS vector; configurable, with an entry-count
  cap).
- **XML-parser hardening**: `quick-xml` rejects DTDs, external entities, and
  parameter entities outright (XXE, billion laughs). Request-body size cap,
  nesting-depth cap.
- Live properties: `getetag` (§4.2), `getcontentlength`, `getlastmodified`,
  `resourcetype`, `getcontenttype`, `quota-available-bytes`/
  `quota-used-bytes` (RFC 4331).
- Dead properties: arbitrary properties set via `PROPPATCH` are stored in
  `sc-meta`, keyed by fileid — never as an on-disk xattr (would conflict
  with other services/backup tools).
- Client-quirk handling lives explicitly in code: Windows Explorer (WebClient
  service, 50 MB default cap, unauthenticated pre-flight OPTIONS), macOS
  Finder (`._` AppleDouble creation, handled separately), Cyberduck, rclone,
  Joplin.

Every Class 2 method above is registered and reachable today at
`/dav/{*path}` (`crates/sc-server/src/routes.rs`) — this is not aspirational.
What is not wired up: a Litmus conformance run in CI (see §14 M4).

---

## 9. SMB

### 9.1 A cold assessment

Writing an SMB2/3 server in Rust from scratch is **years of work**, and an
immature SMB server runs head-on into this project's "security first"
requirement (SMB has historically been a remote-RCE breeding ground, and its
protocol surface is dozens of times WebDAV's). No production-grade SMB
server implementation exists in the Rust ecosystem.

### 9.2 Approach — Samba orchestration

The path every NAS distribution that ships SMB has taken: drive Samba, don't
reimplement it.

- `sc-smb` renders `smb.conf` from Share/Grant definitions and writes it
  (`crates/sc-server/src/smb_cmd.rs`, the `sc-server smb-sync` command),
  gated by the LAN-only bind check. It **never** invokes Samba itself —
  reloading (`testparm`, `smbcontrol reload-config`) is explicitly out of
  scope for `sc-smb` (see its own module doc); that is a separate small
  sidecar's job, consuming the files this crate writes.
- User sync targets `tdbsam` (`pdbedit`/`smbpasswd`).
- **Credentials share the account password.** NTLM requires the NT hash
  (`MD4(UTF-16LE(pw))`), so the moment we hold the plaintext, both the
  Argon2 hash and the NT hash are derived together and stored
  master-key-encrypted.
- **Derivation is unconditional at account creation; publishing happens only
  when SMB is enabled.** This split means an admin who flips SMB on gets it
  working immediately for every existing account. The one exception — no
  plaintext was ever seen (TOTP-forced reset, an upgrade) — is filled in
  **opportunistically at the next authentication**, WebDAV Basic auth
  included, so no user action is required. The master key becomes a
  mandatory piece of config as a result.
- **Trade-off, stated plainly**: if the DB and the master key leak together,
  the account password becomes crackable at MD4 speed, not Argon2 speed.
  The mitigation is that **SMB is forced internal-network-only**:
  - SMB is **off by default**; enabling it **forces internal-network-only**
    — a public-address bind is detected and `smb.conf` generation is
    refused outright. A control, not a convention.
  - The master key lives only in a secret file **outside** the data volume.
    A startup warning fires if it's found inside.
  - `smb.conf` forces `server min protocol = SMB3_11`,
    `server signing = required`, `smb encrypt = required`,
    `ntlm auth = ntlmv2-only`, `lanman auth = no`, `restrict anonymous = 2`,
    `unix extensions = no`.
  - **A TOTP user cannot reach SMB with their account password** — no way to
    carry a second factor over SMB, so allowing it would be a bypass. They
    need a dedicated SMB password, or no SMB access.
  - A Kerberos/AD-joined environment (`security = ADS` + winbind) avoids
    storing an NT hash entirely — the only configuration where this
    trade-off disappears.
- Detail in `DEPLOYMENT.md` §7.2, `DESIGN-AUTH.md` §2.4.
- The interface is abstracted behind `trait ProtocolAdapter` → swappable for
  a native implementation later, though that is not on the current roadmap.

**Current reachability**: reachable by a real SMB client. `sc-smb`'s config
generation, the bind gate, and the NT-hash dual-derivation in `sc-auth` are
implemented and covered by tests; a startup bind check surfaces in
`sc-server`'s diagnostics output whenever `smb.enabled` is set; and the
Samba sidecar that reads the generated `smb.conf` and serves SMB ships as
`Dockerfile.smb` + `deploy/smb/` + the `sc-smb` service in
`docker-compose.yml`. See §14 M6.

### 9.3 Alternative (on request)

Could be replaced by `ksmbd` (in-kernel SMB3 server) configuration
management. Better performance than Samba, but has a CVE history and, being
a kernel module, a large blast radius. Unsuited to container deployment.

---

## 10. Compat layer

### 10.1 Isolation contract (enforced)

`sc-compat-nc` may only:

- Consume `sc-core`'s **public traits**: `Vfs`, `AuthProvider`,
  `AclEvaluator`, `MetaStore`, `UploadSessions`.
- Store NC-only concepts in **its own** SQLite tables (`nc_*` prefix):
  favorites, share-type mapping, client registration, login-flow poll
  tokens.
- **Translate** core concepts into NC vocabulary: `Perms` →
  `oc:permissions` string, `FileId` → `"{id}oc{instanceid}"`, `Share` → OCS
  response JSON.

Forbidden (checked in CI):

- The strings `oc`/`nc` appearing anywhere in
  `crates/sc-{vfs,meta,core,acl,auth,dav}` (grep gate).
- A core crate depending on `sc-compat-nc` (`cargo-deny` / dependency-graph
  check).
- A `--no-default-features` build failing to compile, or that binary
  containing any NC route (route-dump test).

**The gate is real, not aspirational**: `scripts/verify.sh` runs a grep over
every core crate's `src/` for `oc[:_-]`/`ocs`/`remote\.php`
(excluding comments), a check that the bind site installs `ConnectInfo`, and a
`cargo build -p sc-server --no-default-features` gate — 12 checks total,
currently 12/12. The route-table dump is a `cargo test` (`route_drift`), so
the gate's `cargo test` step carries it.

**The grey-area test**: fileid and directory-ETag propagation look like NC
requirements, but both are **protocol-neutral features in their own right**
(WebDAV rename tracking, efficient sync), so they live in the core. Encoding
a fileid as `12345oc9abc`, by contrast, is pure NC vocabulary, so it lives in
compat. The test — *"would this feature need to exist if NC didn't?"* —
applies to every new feature.

### 10.2 Surface implemented (what NC clients actually require)

| Endpoint | Purpose |
|---|---|
| `GET /status.php` | Version advertisement. `{"installed":true,"maintenance":false,"version":"29.0.0.19","versionstring":"29.0.0","productname":"..."}`. Clients start feature-gating here |
| `POST /index.php/login/v2` → poll | Login Flow v2. Browser approval → app-only password issued. The standard login path for current mobile/desktop clients |
| `GET /ocs/v2.php/cloud/capabilities` | Large capability JSON: `files.bigfilechunking`, `dav.chunking: "1.0"`, `files_sharing.*`, `core.*` |
| `GET /ocs/{v1,v2}.php/cloud/user`, `/users/{id}` | User info, quota |
| `PROPFIND /remote.php/dav/files/{user}/...` | Primary file access. Reuses `sc-dav`, wrapped in a **decorator** that injects NC extension properties |
| `/remote.php/webdav/` | Legacy alias |
| `/remote.php/dav/uploads/{user}/{tid}/` | NC chunked upload v2 (MKCOL → PUT `{offset}` → MOVE `.file`). **Mapped** onto the §6 session engine, not reimplemented |
| `/ocs/v2.php/apps/files_sharing/api/v1/shares` | Share-link CRUD |
| `/index.php/core/preview`, `/remote.php/dav/.../?preview` | Thumbnails |
| `/ocs/v2.php/apps/notifications/...` | Stub (empty list) — some clients retry noisily without it |

All of the above are registered and reachable today at their real paths
(`crates/sc-server/src/routes.rs`), gated behind the default-on `compat-nc`
feature. Login Flow v2 has been verified end to end against a real client.

NC-only WebDAV properties: `oc:id`, `oc:fileid`, `oc:permissions`,
`oc:size` (recursive size), `oc:checksums`, `oc:favorite`, `oc:owner-id`,
`oc:owner-display-name`, `oc:share-types`, `nc:has-preview`,
`nc:mount-type`, `nc:is-encrypted`.

`oc:permissions` string mapping: `S`=shared `R`=re-shareable `M`=mounted
`G`=readable `D`=deletable `N`=renameable `V`=movable `C`=can create file
(directory) `K`=can create directory (directory) `W`=writable (file). Get
this wrong and the desktop client silently refuses to sync, so it's pinned
by an integration test.

`oc:size` (recursive directory size) is expensive on a plain FS → cached in
`sc-meta` with the same dirty-propagation mechanism as the ETag.

The account root — there is no `sc-core` "root of everything" vpath — is
answered by a PROPFIND response synthesized in `sc-server` listing the
caller's grant-projected roots as children. That root reports
`oc:permissions: G` (read-only — nothing to create beside the roots) and
quota `-2`/`-2` (the reference server's own `SPACE_UNKNOWN` — the roots can live on
different filesystems, so any single number would be fabricated). The DAV
namespace prefix is lowercase `d:` deliberately — iOS matches the literal
name.

`POST /index.php/login/v2/poll` answers only **404** (pending, unknown,
consumed, or throttled — all indistinguishable on purpose) or **200** with
credentials. It must never answer 429: the app doesn't understand that
status, stops polling, and leaves the user on a spinner after a successful
consent.

### 10.3 Verification strategy

An integration suite that drives real NC desktop/Android clients and
`rclone`. Litmus (WebDAV conformance) is meant to run in CI alongside it.

**Current status**: neither is wired into `scripts/` or
`.github/workflows/` today — no `litmus` or desktop-client CLI reference exists
anywhere in the repo's CI configuration. What has actually been verified is
Login Flow v2 working end to end against a real client, done manually this
session, not as a regression test that runs on every change.

**Deliberate non-goals**: app store, server-side encryption, Talk,
Groupware, Federation, Collabora integration. Full compatibility is scoped
to file sync, browsing, sharing, and previews only.

---

## 11. Frontend

- **SvelteKit 2 / Svelte 5 (runes)**, `adapter-static` SPA, embedded in the
  backend binary.
- The design system is **m3-svelte**. `@material/web` is still not used, for
  the original reason — web-component interop friction (form participation,
  SSR, style leakage) conflicts with the "lightweight" goal. m3-svelte has
  none of it: it compiles to the same Svelte components everything else here
  is.
- What it replaced was a hand-built MD3 layer: a build-time token generator, a
  static token sheet, a state-layer class and ~30 primitives. Each was a
  re-implementation of something the library already ships, and each drifted
  from the spec at its own pace. `web/src/lib/ui/` is now thin adapters and a
  few app compositions (`ListItem`, `Breadcrumb`, the file views) built out of
  the framework's state layer, colour roles and type mixins.

### 11.1 Design tokens

`web/src/app.css` is the entire stylesheet. It imports m3-svelte's, pins a
static `--m3c-*` palette generated once from seed `#3F6C4F` with the library's
own `genCSS` (`SchemeTonalSpot`, contrast 0), and sets the font stack. There is
no token pipeline in this repo: `tools/gen-tokens.mjs`, `theme.config.json`,
`styles/tokens.css` and `styles/tokens.generated.css` are all deleted, and with
them the `--sc-space-*` scale and the high-contrast pair (now an explicit
non-goal, `FEATURES.md` #135).

Four kinds of variable are still ours, because the framework has no token for
them: `--sc-row-height` (file-list density), the three nav-reservation widths
and heights (the nav components are `position: fixed`), `--sc-page-pad` (MD3
window-class margins, 16/24/32px), and the `.sc-*` utility classes.

Every spacing/size/alignment value still snaps to the 4px grid — enforced on
literal px by a stylelint plugin (`proposals/stowcloud-3-frontend.md`), not by a
convention around named variables. Icons 24px, touch targets minimum 48px,
40px in dense mode.

### 11.2 Performance

- Directory listings are **virtual-scrolled** — no frame drops even at
  100,000 entries.
- The list API uses cursor pagination + server-side sort (never ships the
  full set to the client).
- The file tree lazy-loads; only open nodes stay resident.
- Upload runs in a **Web Worker** (hashing/chunking off the main thread).
  Directory upload uses the File System Access API, falling back to
  `webkitdirectory`.
- The code editor (CodeMirror 6) is dynamically imported — not part of the
  initial bundle. Initial JS budget **< 150 KB gzip**.
- Preview images are server-generated thumbnails with `loading="lazy"` +
  AVIF/WebP negotiation.

---

## 12. Performance targets

| Item | Target |
|---|---|
| Idle RSS | < 40 MB |
| Peak login RSS | < 40 MB + 192 MB (Argon2 48 MiB × semaphore of 4). Zero on a cache hit |
| `mmap` + page-cache contribution | < 128 MB (avoid RAM contention with Jellyfin etc.) |
| **DB size** | Opt-in guard (`db.size_guard`, **off by default**), default `max_bytes` **4 GiB** once enabled. Diagnostics recommends `size_guard=true, max_bytes=2 GiB` for data volumes under 64 GiB, 8 GiB up to 256 GiB. ~105 B/file unindexed, ~195 B/file with the name index. A web-UI-only deployment stays near 0 |
| Kernel watch memory | ~4 MB (hot set) / ~0 (fanotify) — moot until §14 M3's wiring gap closes |
| Thumbnail cache | 2 GB cap by default, storage location separable |
| Binary size | < 25 MB (frontend embedded) |
| Cold start | < 200 ms |
| List 10k entries, name sort | < 50 ms (zero `statx` — `getdents64`'s `d_type` only) |
| List 10k entries, size/time sort | < 150 ms (RAID untouched on a `node` cache hit) |
| PROPFIND Depth:1, 10k-entry directory | < 150 ms (warm cache) |
| Download throughput | Disk/NIC limit (`sendfile` path) |
| Upload overhead | < 5% over raw disk write |

Full budget analysis and mitigations on a 12 TB HDD RAID are in
**`DESIGN-FOOTPRINT.md`**.

---

## 13. Security checklist

- [ ] Every FS access goes through `openat2(RESOLVE_BENEATH)` or a cap-std
      handle. Zero string path concatenation (clippy lint + review gate)
- [ ] Landlock + seccomp active, systemd hardening unit shipped
- [ ] Real host paths never appear in a non-admin API response, error, or log
- [ ] Upload content served only from a cookie-less separate origin via
      signed URL
- [ ] Every download is `nosniff` + `attachment` (except the preview
      allowlist)
- [ ] XML parser has DTD/entity handling fully disabled
- [ ] Zip/archive preview defends against zip-slip and zip bombs
      (compression-ratio, entry-count, total-size caps)
- [ ] Thumbnail generators prefer pure-Rust decoders; ffmpeg runs as a
      network-less jailed subprocess with resource limits
- [ ] Argon2id (no per-path parameter relaxation), app-only passwords, TOTP,
      immediate session revocation
- [ ] A TOTP-enabled user's Basic (DAV)/NTLM (SMB) account-password path is
      blocked — regression-tested
- [ ] Auth cache key derives only from a process-ephemeral key; no fast hash
      ever touches disk
- [ ] Master key lives outside the data directory (startup warning
      otherwise), never taken from an environment variable
- [ ] A public-address SMB bind refuses `smb.conf` generation
- [ ] Rate limiting (login/API/upload), account-enumeration resistance
- [ ] Audit log: login, permission change, share creation, deletion, admin
      actions
- [ ] Share links: expiry as a required option, Argon2 password, download
      count limit, view-only
- [ ] Dependencies: `cargo-audit` + `cargo-deny` in CI; unsafe code confined
      to `sc-vfs`'s syscall wrappers with mandatory `# Safety` comments
- [ ] Fuzzing: `cargo-fuzz` on `SafePath::parse`, the WebDAV XML parser, the
      TUS header parser — **not yet in the tree** (no `fuzz/` crate exists);
      property tests via `proptest` exist for `sc-vfs`/`sc-dav`/`sc-upload`
      but are not a substitute

---

## 14. Roadmap

Section numbers below are fixed reference points (§4.1 alone has a dozen
inbound code comments, and this section is what `docs/README.md` points at)
— do not renumber even as scope shifts. "Done" below means *reachable by a
real client today*, not "the crate compiles."

**M1 — Core (foundation). Done.**
`sc-vfs` + `SafePath` (property-tested via `proptest`; no `cargo-fuzz`
harness exists yet despite being a stated goal — see §13), `sc-meta`
(fileid/etag), `sc-auth`, `sc-acl`, share management. Verified by unit tests,
no protocol involved.
`sc-auth` has carried **parallel NT-hash derivation since M1** — SMB itself
is M6, but derivation happens at account-creation time, so deferring it
would leave every account created in between needing a backfill (§9.2,
`DESIGN-AUTH.md` §2.4). Master-key management was also settled here.
Landlock/seccomp process hardening (§2.4), originally scoped to M6, is
already implemented and active at `serve` startup — ahead of the milestone
that was supposed to introduce it.

**M2 — Web. Done.**
Native REST API, TUS upload, `sc-preview` (in-process forked worker jail —
not the standalone `sc-preview-worker` binary once planned for this
milestone; see §1), content origin + signed URLs, the Svelte UI (browse,
upload, rename, move, delete, preview), and the MD3 design system — the
hand-built token layer this line used to name, since replaced wholesale by
m3-svelte (§11). All of it is live: the full route table is registered, and
the production deployment ships the frontend embedded in the binary
(`--features embed-ui`).

**M3 — Coexistence. Done.**
`sc-watch` (inotify hybrid/lazy-demotion/rescan), directory ETag
propagation, optimistic concurrency, trash, ownership preservation, the
filesystem gate (overlayfs rejection) all land here. ETag propagation
(§4.2), `If-Match`/`412` concurrency, the off-by-default/`share_local` trash
toggle, ownership/mtime preservation, and the overlayfs gate were never the
gap; the watcher was. It is connected now: `sc-server`'s
`watch_subscribe`/`watch_unsubscribe` register the OS-level watch off a
client's own WebSocket subscribe, `bridge.rs` touches the LRU on list/stat,
and a watcher that fails to start degrades to lazy revalidation rather than
erroring. `d800b57` closed the last two pieces — `full_threshold` was
forwarded into `sc-watch` and never read, and nothing forced a periodic
rescan on NFS/FUSE mounts, where inotify cannot see another host's writes at
all.
`sc-search` T1/T2 (the **parallel FS tree walk**) also belongs here, and is
in fact done and load-bearing — it works with no index and no dependency
beyond `sc-vfs`, and is the terminal search implementation for most
deployments. T3 (the block-compressed trigram name index) has *already*
shipped as an opt-in — ahead of where this roadmap originally placed it —
and is off until someone turns it on. That is now a runtime admin toggle
(`PATCH /api/admin/index/settings`, persisted in `index.db`, no restart and no
config edit) plus an explicit "build" — nothing crawls on its own.
Content indexing (T4) and OCR are not a future escalation from
here — they are an explicit non-goal (`sc-search`'s own module doc says so
directly); see TECH-STACK.md §11.

**M4 — WebDAV. Done as a protocol; conformance testing isn't wired up.**
Every RFC 4918 Class 2 method is implemented and reachable at
`/dav/{*path}`. Litmus conformance in CI, and real-world validation against
rclone/Finder/Explorer as a repeatable check, are not — no `litmus`
reference exists anywhere in `scripts/` or `.github/workflows/`.

**M5 — compat layer. Done and live; automated real-client regression
testing isn't.**
`sc-compat-nc` in full: status.php, Login Flow v2, capabilities, the
`remote.php/dav` PROPFIND decorator, NC chunking v2 mapping, share links,
the notifications stub. The isolation CI gate is real (§10.1,
`scripts/verify.sh`). Login Flow v2 has been verified end to end against a
real client — but manually, not as an automated desktop/Android client
regression suite; none exists in CI.

**M6 — SMB + hardening. Done, except an external review.**
Landlock/seccomp process self-restriction is live (moved up from here into
M1, above). The audit log exists and is called from the auth and admin
paths.

SMB is reachable now, which this section denied for a long time. The building
blocks were always real — LAN-only bind enforcement, `smb.conf`/`smbpasswd`/
passwd generation via `sc-server smb-sync`, the startup bind check surfaced in
diagnostics — and the piece that was missing, the Samba sidecar, exists:
`Dockerfile.smb`, `deploy/smb/entrypoint.sh`, the `sc-smb` service in
`docker-compose.yml`, and fail2ban wired to Samba's own auth failures. The
container's capability set was cut to `NET_BIND_SERVICE`/`SETUID`/`SETGID`/
`NET_ADMIN`/`DAC_READ_SEARCH` from `cap_drop: ALL`, verified 2026-07-31 by an
actual authenticated `smbclient` round-trip against that exact image plus a
real fail2ban ban insertion — see `docker-compose.yml`'s own comment for what
that run ruled out and why share roots must be 0775.

Still missing: an external security review. No evidence of one exists in the
repo, and nothing in this repo can substitute for it.
