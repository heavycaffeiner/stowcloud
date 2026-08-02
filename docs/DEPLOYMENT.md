# Deployment design — Linux / Docker

Containerization is not a convenience here, it's a **constraint**. This
document is about how that constraint feeds back into the core design, and
— from §11 on — what has actually been built and run, as opposed to planned.

---

## 1. Image layout

Two images, so that the majority of users who don't need SMB don't pay for
Samba's weight and its root process.

| Image | Base | Contents | Size target |
|---|---|---|---|
| `sc:core` | `gcr.io/distroless/static` or `scratch` | one statically-linked binary¹ (`sc-server`, frontend embedded) | < 30 MB |
| `sc:smb` | `alpine:3.20` | `smbd` + a config-sync loop (`Dockerfile.smb`, `deploy/smb/`). **Sidecar** | 141 MB² |

`sc:core` doesn't link `ffmpeg`, to stay distroless (§4.4). Image thumbnails
are pure-Rust decoders and work with `sc:core` alone.

`sc:media` and `sc:ocr` were dropped from this table (`FEATURES.md` #150).
`sc:ocr` was always a contradiction — OCR is an explicit non-goal (#120), so
the image would have held nothing. `sc:media` existed only to carry `ffmpeg`
for video thumbnails (#99), which is also a non-goal: the preview jail's
workers are `fork`ed and never `execve`'d, so running a separate binary would
mean a second, unproven jail. §4.4 has the full reasoning.

¹ Originally spec'd as two binaries (`sc-server` + `sc-preview-worker`); see
§11.1 for why it's one, and what changes when the second one is built.

² Measured, not a target: `docker images` disk usage for `sc-smb-test:alpine`
built from this exact `Dockerfile.smb`, 2026-07-31, on the Rocky 9 test VM
(`node .vm/vm.mjs`). Was `debian:12-slim`/`~180 MB` (an unmeasured target)
before the Alpine rebase (§7.1's diagram carried the same base and has the
same fix).

Build target: `x86_64-unknown-linux-musl` + `aarch64-unknown-linux-musl`,
statically linked.

> musl's default allocator is significantly slower than glibc under
> multi-threaded allocation-heavy workloads, so `mimalloc` is set as the
> global allocator. Skipping this costs several times the concurrent
> throughput. See §11.2 for what's actually been measured about this claim.

`cargo build --profile release-dist`: `lto = "fat"`, `codegen-units = 1`,
`panic = "abort"`, `strip = true`.

---

## 2. Syscall availability — Docker seccomp reality

Docker's default seccomp profile is an **allow-list**, and it returns
**`EPERM`**, not `ENOSYS`, for anything not on it. That defeats the standard
"fall back on ENOSYS" pattern outright.

| syscall | Docker default profile | when missing |
|---|---|---|
| `openat2` | Docker >= 20.10.10 / libseccomp >= 2.5 | `EPERM` -> **security downgrade**, cap-std fallback |
| `faccessat2` | Docker >= 20.10.10 | `EPERM` -> `faccessat` fallback |
| `landlock_*` | fairly recent (~Docker 25) | `EPERM` -> boots without the Landlock sandbox (warning) |
| `statx` | allowed for a long time | fine |
| `copy_file_range` | allowed | buffer-copy fallback |
| `renameat2` | allowed | `renameat` fallback (loses `RENAME_NOREPLACE` -> a race window opens) |
| `fanotify_*` | needs `CAP_SYS_ADMIN` | not used by default (§5) |

### Self-diagnostic at startup

```
[sc] kernel diagnostics
[sc]   openat2         OK        (RESOLVE_BENEATH path isolation active)
[sc]   statx.btime     OK (inode-reuse detection active)
[sc]   renameat2       OK
[sc]   copy_file_range OK
[sc]   landlock        ABI 4 (path sandbox active)
[sc]   inotify watches 8192 available
```

An `openat2` `EPERM` is logged as an **error, not a warning**, with the fix
(upgrade Docker, or `--security-opt seccomp=<updated>.json`). The default
behavior is to continue on the fallback; `strict_syscalls = true` refuses to
start instead.

---

## 3. Filesystem detection — the share-registration gate

`statfs.f_type` classifies a share path's filesystem, and shares where
fileid stability, watching, or atomicity guarantees don't hold get **refused
at registration or auto-downgraded.**

| `f_type` | FS | verdict |
|---|---|---|
| `0x794c7630` | **overlayfs** | **Refused.** A container's writable layer has unstable inodes across restarts and inotify misses lower-layer changes — the whole fileid design breaks. Use a volume or bind mount instead |
| `0x01021994` | tmpfs | Warning (data lost on restart). Registration allowed |
| `0xEF53` | ext4 | Full support |
| `0x9123683E` | btrfs | Full support + `copy_file_range` reflink |
| `0x58465342` | XFS | Full support + reflink |
| `0x2FC12FC1` | ZFS | Full support. Needs `btime` support checked |
| `0xF2F52010` | f2fs | Full support |
| `0x65735546` | FUSE (mergerfs, rclone, s3fs, …) | `btime`/inode stability unknown -> **forces `id_strategy = path`**, inotify untrusted -> forces periodic rescan. Surfaced in the UI |
| `0x6969` | NFS | inotify **cannot see server-side changes** -> forces periodic rescan (shorter interval). May lack `renameat2` flag support |
| `0xFF534D42` | CIFS/SMB | Same as NFS, plus case/name restrictions. Strong warning |
| `0x73717368` | squashfs | Registered read-only only |

Without `btime` (filesystem or kernel), inode-reuse detection is impossible,
so that share auto-downgrades to `id_strategy = path`; the resulting
trade-off ("a rename looks like delete+reupload to an NC client") is surfaced
in the admin UI.

---

## 4. Mount boundaries and EXDEV

In Docker, shares are usually independent bind mounts. **`rename()` fails
with `EXDEV` across mount boundaries even on the same filesystem type**, and
it's common for users to mount `/data` and then mount `/data/media`
separately inside it — so **EXDEV can happen inside a single share.**

Handling:

1. `ShareRoot` carries `root_dev`, and every `Stat` includes `dev`. A `dev`
   mismatch means a mount boundary was crossed.
2. `renameat2` returning `EXDEV` triggers a **copy + fsync + unlink**
   fallback automatically. It isn't atomic and it's slow, so:
   - progress streams and the operation is cancelable.
   - the UI warns upfront ("this move will be a copy — different storage")
     using the `dev` comparison, before the user commits.
   - a failed copy cleans up its partial output; the original is deleted
     only after the copy completes and is fsynced.
3. **Trash placement**: `.sctrash` at the share root would turn deleting a
   file inside a nested mount into a copy. So `.sctrash` is created at **the
   mount point owning the deleted file** — found by walking up to the
   nearest ancestor where `dev` changes.
4. `copy_file_range` can itself return `EXDEV` across mounts/filesystems on
   some kernels -> falls back further to a buffered copy.

### SELinux labeling caveat

`docker run -v /media:/media:Z` — uppercase `Z` — applies an **exclusive
label that cuts off other containers'** (e.g. Jellyfin's) access. Shared
folders must use lowercase `z` (shared label) or no label. Documented in
compose examples; startup diagnostics read `/sys/fs/selinux/enforce`
directly (no `getenforce` subprocess) and, when a share is configured and
SELinux is enforcing, print a warning pointing at `:Z` labeling as the
likely cause of an `EACCES` another service hits on the same mount.

---

## 5. Change detection: container reality

### 5.1 Backend selection — by tree size and sync requirements

```toml
[watch]
backend        = "auto"     # auto | hotset | inotify_full | fanotify
hot_set_max    = 4096
full_threshold = 50000      # directory count
```

| condition | `auto` picks | kernel memory | sync immediacy |
|---|---|---|---|
| DAV/NC disabled | `hotset` | ~4 MB | n/a |
| directories <= `full_threshold` and watches available | `inotify_full` | ~50 MB | fully immediate |
| large tree, no privilege | `hotset` + structural rescan | ~4 MB | delayed by the rescan interval |

**`fanotify` is not implemented.** The design originally called for
`FAN_MARK_FILESYSTEM` under `CAP_SYS_ADMIN` (near-zero kernel memory: one
mark per mount, versus the ~500 MB a 300k-directory tree needs from
`fs.inotify.max_user_watches`). The config value is still accepted, so a
config file written against the full design doesn't fail to parse, but
`crates/sc-watch/src/lib.rs` treats `WatchBackend::Fanotify` as an **alias
for `InotifyFull`** and logs a warning at `start()`. Building the real
fanotify path is unstarted work, not a rounding error — `DESIGN-FOOTPRINT.md`
§3 has the kernel-memory math it would recover.

**None of this is switched on today.** `Watcher::start` is called from
`sc-server`'s `AppState::build`, but nothing in the workspace ever calls
`Watcher::subscribe` — grep confirms the only callers are the crate's own
tests. So no directory is ever registered with any backend, and the debounce
loop that would turn a raw event into a push never fires, for any change,
regardless of which backend `auto` would otherwise pick. `GET /api/events`,
`WsHub`, the frontend hub with backoff and per-path refcounting are all
built and working — only the producer that would feed them events is
missing. Every read path still returns correct data regardless (§5.4); the
gap is that an open screen never updates itself without a manual refresh.

### 5.2 Hot-set watching — the default, unprivileged path

The full tree is never watched — only what **can currently be observed**,
since real-time delivery to a directory nobody is looking at can't be
observed either.

```rust
pub struct HotSet {
    subscribed: HashSet<FileId>,   // WebSocket-subscribed directories + their ancestor chain
    recent:     Lru<FileId, ()>,   // recently accessed (default 2048)
    pinned:     HashSet<FileId>,   // share roots and two levels down (always)
}
```

- The watch is registered **before** the directory is read, never after —
  reversing the order can miss a change in between.
- Evicted watches are released. `inotify_add_watch`/`rm_watch` cost
  microseconds, so churn is negligible.
- 4096 watches x ~1 KB = **~4 MB**, well within default sysctl limits.

**Correctness for the caller is fully guaranteed regardless of tree size or
watch state**, because every read path re-verifies with `statx` (§5.4) — the
watcher's only job is refreshing screens that are already open.

### 5.3 What's surfaced when the watch ceiling is hit

An `ENOSPC`, or crossing `full_threshold`, presents options **with numbers
attached** in the admin UI and health endpoint:

> Watching all 312,480 directories needs `--cap-add SYS_ADMIN` (fanotify,
> ~0 kernel memory) or `sysctl fs.inotify.max_user_watches=524288` on the
> host (~500 MB kernel memory). Right now only open folders update live, and
> sync clients lag up to 12 minutes.

The choice is left to the admin, but the consequence is never hidden.

### 5.4 Why this stays correct anyway — layered defense

| layer | role |
|---|---|
| `mark_dirty` on our own writes | immediate consistency, independent of the watcher |
| watcher (hot-set / full / fanotify) | pushes to open screens + keeps aggregate ETags fresh |
| **lazy revalidation (every read)** | **the real backstop.** Cached `(size, mtime, ino)` is checked against a live `statx`; a mismatch invalidates. A dead watcher never serves stale data to a user |
| periodic rescan | catches missed deletes/moves. The only mechanism on NFS/FUSE |

`fs.inotify.max_user_watches` is a host-global, per-UID sysctl — it can't be
raised from inside a container, and it's shared with every other container
running under the same UID. `watch.budget` (default 50% of capacity) keeps
one instance from starving its neighbors; `ENOSPC` is treated as a normal
path, absorbed by falling back to the §5.2 hot set.

### 5.5 Rescan cost — the HDD RAID reality

Under `hotset`, a rescan costs one `statx` per file.

| storage | 10M files |
|---|---|
| dentry cache warm | ~10s |
| NVMe cold | ~2min |
| **12 TB HDD RAID cold** | **10+ min** (random seek ~10ms) |

So a full rescan is never the default schedule on rotating disks.

- **Structure-first rescan**: a directory's mtime changes on add/remove/rename.
  `statx`-ing only directories (300k calls) detects structural change far
  more cheaply than a full walk; it misses in-place content edits, but those
  are usually our own writes or already inside the hot set.
- A full scan is admin-triggered only, with an optional low-load-hours window.
- Rotational vs. SSD is detected via `/sys/block/*/queue/rotational` and
  adjusts the default automatically.

---

## 6. uid / gid — the core of shared folders

### 6.1 What we don't do

**No `chown -R` on a mounted volume.** Many container images (including the
linuxserver.io family) do this at startup; on a directory shared with
Jellyfin/*arr/rsync it cuts off those services' access and is hard to
reverse. Our container's entrypoint never touches ownership of user data.

### 6.2 What we do

- Run the container as an unprivileged UID from the start: compose's
  `user: "1000:1000"`. Skipping the root-then-setuid step means
  `CAP_SETUID`/`CAP_SETGID` aren't needed either.
- Groups shared with other services go in `group_add:` (e.g. `media` = gid
  1001 shared with Jellyfin).
- File creation mode is applied explicitly per share (`mode_file`/`mode_dir`),
  never left to umask — umask can only mask bits, not set them, so it can't
  express "0664 so Jellyfin can read it."
- Replacing an existing file transplants the original's mode/uid/gid/xattr
  onto the new file before renaming it into place (`ARCHITECTURE.md` §5.2).

Recommended defaults: `mode_file = 0664`, `mode_dir = 0775`, a shared gid via
`group_add`.

---

## 7. Samba sidecar

### 7.1 Topology

```
+- sc-core (distroless, uid 1000, cap_drop ALL, read_only) -+
|  :8080  HTTP                                              |
|  writes -> /config/smb/{smb.conf, smbpasswd, passwd.d}     |
+-------------------------------------------------------------+
                      | (shared config volume, sc-core rw / sidecar ro)
+- sc-smb (alpine, smbd) ---------------------------------------+
|  :445   SMB3                                               |
|  watches /config/smb via inotify -> validates -> smbcontrol reload |
+---------------------------------------------------------------+
        both mount the same share volumes
```

The point of the split is that `smbd`'s root privilege stays outside our
process's address space. An all-in-one image (`sc:aio`, s6-overlay) exists
too, but the sidecar is the default recommendation.

### 7.2 Credentials — sharing the account password

**The SMB password is the account password.** NTLM requires
`MD4(UTF-16LE(password))`, so both an Argon2 hash and an NT hash are derived
together at every moment the plaintext is held (account creation, password
change, successful login) — `DESIGN-AUTH.md` §2.4.

#### What that costs

Recorded for the record. **This cost is accepted on the premise that SMB is
forced to stay internal-network-only** (§7.4).

| scenario | with a separate SMB password | with the shared account password |
|---|---|---|
| DB leaked alone (master key safe) | safe | **safe** — AEAD ciphertext only |
| DB + master key leaked | only the SMB password is exposed; Argon2 still protects the account password | **the account password becomes crackable at MD4 speed** |
| pass-the-hash | SMB access only | SMB access, and web/WebDAV access too on a successful crack |

MD4 isn't memory-hard — a single high-end GPU does about 10^11/s, several
billion times weaker than Argon2 (m=48 MiB) for the same password. That
scenario, though, already assumes the attacker has both the DB and the
master key — at which point session tokens, share links, and TOTP seeds are
already gone too. The marginal loss is limited to "a password reused
elsewhere."

#### Mitigations

1. **Forced internal-network-only** (§7.4) — the primary defense, enforced
   at config-generation time, not just recommended in a doc.
2. **Keep the master key on separate storage from the DB.** `SC_MASTER_KEY_FILE`
   should point at a mount that is **not** the data volume (a Docker secret,
   a separate volume); startup **warns** if the key lives under the data
   directory. A backup that bundles the DB and the key together makes the
   encryption pointless — the single most common real-world mistake. Never
   pass the key as an environment variable — it shows up in `docker inspect`
   and `/proc/*/environ`.
3. The NT hash lives in a **separate table**, `user_smb_secret`, whose access
   type doesn't implement `Serialize`, so it can't leak into an API response.
4. Samba NTLM hardening: `ntlm auth = ntlmv2-only`, `lanman auth = no`,
   `raw NTLMv2 auth = no`, `server signing = required`, `smb encrypt =
   required`. NTLMv1/LM fully blocked, signing mandatory blocks NTLM relay.
5. **Key rotation**: `key_ver` rotates the master key; rotation decrypts and
   re-encrypts every NT hash (no plaintext needed).
6. The global password minimum stays **10 characters** — SMB doesn't push it
   higher. Exposure is limited to the internal network, so the usability
   cost of a longer minimum outweighs the benefit here.
7. **An AD-joined environment is a strictly better option, if available.**
   `security = ADS` + winbind delegates authentication to the domain
   controller, and we never store an NT hash at all — the only configuration
   where this whole trade-off disappears.

#### Always ready

The NT hash is **always derived at account creation**, independent of
whether SMB is enabled (`DESIGN-AUTH.md` §2.4).

```
derive : always, on account creation / password change  -> AEAD ciphertext in the DB
publish: when smb.enabled && user.smb_enabled            -> entered into the smbpasswd file
```

So when an admin turns SMB on, `smb_sync` regenerates every user's
`smbpasswd` immediately and **everyone can connect from that moment** — no
re-login required.

This depends on **the master key being mandatory configuration.** If absent
at first boot, one is generated and written to `SC_MASTER_KEY_FILE` at mode
`0600`. If the key file is lost, the stored NT hashes can't be decrypted, so
they're all discarded and each user's is **automatically re-derived on their
next authentication** — no user action needed, but SMB doesn't work in the
meantime, which the backup documentation calls out as a top-level item.

Exceptions (the list has outgrown the "three" it was written with):

| situation | behavior |
|---|---|
| TOTP-enabled user | the account-derived hash is **deleted**. A dedicated SMB password is required (§7.2 2FA). Turning TOTP back off re-derives it immediately on that request's password reconfirmation — no gap |
| user linked to an identity provider | the account-derived hash is **deleted** at link time, so the account password stops working for SMB (`DESIGN-AUTH.md` §13.6), and a running server with SMB enabled rewrites the published `smbpasswd` to match within a second. Unlinking re-derives the hash only when the *user* unlinks, since that path re-confirms the password; an admin unlink has no plaintext and cannot. §13.7 has the recovery procedure |
| `user.smb_opt_out = true` | derivation is skipped entirely, for accounts that will never use SMB |
| admin force-disables TOTP | the admin doesn't know the plaintext -> auto-generated on the user's **next** authentication |
| an account predating this feature | same: auto-generated on next authentication. An upgrade path, not normal operation |

The last two also **never require re-login.** Backfill happens on any
successful verification that carries a plaintext, including **WebDAV/NC
Basic auth** — a single connected sync client fills it in within seconds
without the user doing anything (`DESIGN-AUTH.md` §2.4).

The admin UI marks any account with `smb_enabled` but no NT hash yet as
"pending authentication." Empty in normal operation.

#### 2FA

TOTP users can't use their account password for SMB (SMB has no way to carry
a second factor, so it would be a bypass). `smb.totp_policy =
require_separate` (default) requires a dedicated SMB password; `block` shuts
SMB off entirely for that account.

#### passdb sync

1. `sc-core` writes the `smbpasswd`-format file directly (LM hash marked
   disabled, NT hash only):
   ```
   alice:1000:XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX:<32-hex NT hash>:[U          ]:LCT-00000000:
   ```
   No Samba binary invocation needed, and **plaintext never crosses the
   volume.** File mode `0600`, dedicated volume, sidecar mounts it read-only.
2. The sidecar detects the change, imports with `pdbedit -i
   smbpasswd:/config/smb/smbpasswd -e tdbsam:…`, then `smbcontrol all
   reload-config`.
3. Samba requires `getpwnam` to succeed, so the sidecar creates `/etc/passwd`
   entries. **Every SMB user shares one uid** (our service uid) with
   distinct names; real access control is `valid users` / `read list` /
   `write list`.
4. **Password-change propagation**: Argon2 re-hash -> NT hash recompute ->
   `smbpasswd` regenerate -> sidecar import. The regenerate step is the
   running server's own, not an operator's (`DESIGN-AUTH.md` §13.6); §13.7
   lists the three cases where it does not happen and `sc-server smb-sync` is
   still the answer. Existing SMB sessions are kept
   alive by Samba, not dropped immediately; forcing termination is the admin
   UI's "kill SMB session," which calls `smbcontrol smbd close-share` /
   kills the process.
5. Audit events: `smb.credential_synced`, `smb.enabled`, `smb.session_killed`.

#### Brute force

**Samba handles auth itself, so our rate limiter (`DESIGN-AUTH.md` §7)
doesn't apply.** A structural limitation. Mitigations:

- SMB is LAN-only, so the attack surface isn't internet-exposed.
- The sidecar image ships fail2ban configured to watch Samba's auth-failure log by default.
- An optional `smb.audit_ingest = true` has the sidecar tail the Samba log
  and fold auth success/failure into our own audit log, so web and SMB
  access show up in one place.

### 7.3 smb.conf generation

```ini
[global]
  workgroup = WORKGROUP
  server min protocol = SMB3_11
  server signing = required
  smb encrypt = required
  restrict anonymous = 2
  null passwords = no
  guest ok = no
  map to guest = never
  unix extensions = no

  # -- NTLM hardening (§7.2) --
  ntlm auth = ntlmv2-only
  lanman auth = no
  raw NTLMv2 auth = no
  client ipc signing = required
  disable netbios = yes
  smb ports = 445
  load printers = no
  printing = bsd
  printcap name = /dev/null
  disable spoolss = yes
  passdb backend = tdbsam
  force user = scsvc
  force group = scsvc

  # -- avoid polluting a folder shared with other services --
  store dos attributes = no
  map archive = no
  map hidden = no
  map system = no
  map readonly = no
  ea support = no
```

`store dos attributes` and the `map *` settings are off because, enabled,
Samba writes DOS attributes as xattrs or repurposes Unix permission bits to
store them — both break other services and backup tools sharing the same
directory.

Per share:

```ini
[photos]
  path = /shares/photos
  valid users = alice bob
  write list = alice
  read list  = bob
  create mask = 0664
  directory mask = 0775
  veto files = /.sctrash/.scpart-*/.scmeta/.scindex/
  delete veto files = no
  oplocks = no          # only on shares with shared_with_others = true
```

- **`oplocks = no`**: on a share other services also write to, allowing
  oplocks/leases means SMB clients can see stale cache. Shares flagged as
  shared give up some performance for correctness.
- **Sub-path grants**: SMB shares are path-scoped, so a sub-path restriction
  needs its own SMB share per distinct grant path.
- **macOS support** (`vfs objects = catia fruit streams_xattr`) uses xattrs
  and is **opt-in per share**, off by default.
- Generated config is validated with `testparm -s` before reload. A failed
  validation keeps the previous config and surfaces the error in the admin UI.

**Share directories must be group-writable (0775), not 0755.** `force user =
scsvc` makes the *process* uid the owner, so a plain POSIX check would allow
the write — but Samba runs its own NT ACL check with the **authenticated
user's** SID, which is not the owner's, so it falls through to the group ACE.
With 0755 there is no group write bit and every create fails
`NT_STATUS_ACCESS_DENIED` in `check_parent_access_fsp` (mask 0x2,
`FILE_ADD_FILE`) — while reads, directory listings and authentication all
still work, which makes it look like a permissions bug in sc-core rather than
a mode on the tree.

This is what §6.2's `mode_dir = 0775` / `mode_file = 0664` default is for, and
sc-core's VFS applies it to everything it creates (`sc-vfs`'s `SharePolicy`
default). It does **not** apply to a share root an operator created by hand:
production's roots were `mkdir`ed at 0755 during setup and stayed that way.
Verified 2026-07-31 by bisection — the same known-good `smb.conf` that
round-trips against a 0775 tree fails against a 0755 one, and `chmod 775` on
that one directory is the entire difference. When provisioning a share root
outside sc-core, match §6.2's modes.

### 7.4 Internal-network-only — an enforced control

This is the basis on which sharing the account password is accepted, so it's
implemented as an **enforced control, not a convention.** Port 445 also
can't be proxied through Cloudflare (short of Spectrum's enterprise tier).

**(1) Validation at config-generation time** — before `sc-smb` writes
`smb.conf`, it inspects the bind target and **refuses generation** if any
address isn't in a private range.

```rust
const PRIVATE: &[IpNet] = &[
    "10.0.0.0/8".parse(), "172.16.0.0/12".parse(), "192.168.0.0/16".parse(),
    "127.0.0.0/8".parse(), "169.254.0.0/16".parse(),          // IPv4
    "fc00::/7".parse(),   "fe80::/10".parse(), "::1/128".parse(),  // IPv6
];

fn validate_smb_bind(ifaces: &[IpAddr], cfg: &SmbCfg) -> Result<()> {
    let public: Vec<_> = ifaces.iter().filter(|a| !in_any(PRIVATE, a)).collect();
    if !public.is_empty() && !cfg.allow_public_bind {
        bail!(SmbConfigRefused {
            reason: "SMB is internal-network-only. Cannot bind a public address",
            offending: public,
            hint: "use WebDAV or a VPN for remote access",
        });
    }
    Ok(())
}
```

`smb.allow_public_bind = true` is an available escape hatch, but enabling it
puts a permanent warning banner at the top of the admin UI and logs
`smb.public_bind_enabled` to the audit log — the goal is that this can never
happen quietly.

**(2) Defense in depth inside the generated `smb.conf`**

```ini
[global]
  bind interfaces only = yes
  interfaces = lo 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16
  hosts allow = 127.0.0.0/8 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16
  hosts deny  = 0.0.0.0/0
```

`hosts allow`/`hosts deny` stop a connection at the connection stage even if
`interfaces` is misconfigured.

That "connection stage" is *before* SMB2 NEGOTIATE: a denied client gets an
NBSS negative session response (`83 00 00 01 81`) and nothing else — no
status code, no log line at the default level. `render_global` once built this
list with a `.skip(1)` over `PRIVATE_CIDRS_V4` meant to drop a duplicate
loopback entry, but loopback is *last* in that list, so what it actually
dropped was `10.0.0.0/8` (and left `127.0.0.0/8` in twice). Every client on a
10.x LAN was refused with that opaque 5-byte reply. Fixed and covered by
`hosts_allow_lists_every_private_cidr_once`; the pre-existing test only
asserted the substring `"hosts allow"` appeared at all, which is why a config
missing an entire RFC1918 range still passed.

**(3) Container port binding** — compose examples bind explicitly to a LAN
interface, e.g. `192.168.1.10:445:445`. `0.0.0.0:445` never appears anywhere
in the docs.

**(4) Self-diagnosis at startup** — the actually-listening sockets are
checked; a public-address listener triggers a loud warning and marks the
health endpoint `degraded`, to catch cases where config and reality diverge
(host network mode, a wrong port mapping).

---

### 7.5 Bare metal — the same contract without Docker

§7.1 assumes a sidecar container. Where Samba runs on the host instead, the
contract does not change: **sc-server renders the same three files into the
same `config_dir`, and something privileged picks them up.** In Docker that
something is `deploy/smb/entrypoint.sh`; on bare metal it is
`deploy/smb/native/sc-smb-agent.sh`, running as root under systemd or OpenRC.

sc-server stays unprivileged either way. It needs `useradd`, `pdbedit` and
service control to make SMB work, and the alternatives — running it as root,
or giving it sudo rules for those three — both widen the blast radius of a
bug in an internet-facing HTTP server. A separate root process reading a
directory is the smaller surface, and it keeps the settings-screen toggle
identical on both deployments.

```
./deploy/smb/native/install.sh [--config-dir DIR] [--service-user NAME]
```

It installs the agent and its init file and nothing else — not samba, not the
service account, not SMB itself. A missing `smbd` or a missing service account
is a hard failure with the distro's install line, rather than something
guessed at. The config dir is `chmod 700`: `smbpasswd` in it carries NT
hashes.

**Where it differs from the sidecar, and why**

- **Polls (5s) instead of `inotifywait`.** inotify-tools is a separate package
  on every distro here; the samba package alone is the premise of this path.
- **Reconciles `/etc/passwd` directly** rather than through `useradd`. busybox
  `adduser` has no `-o`, so on Alpine it cannot create the several accounts
  that must share one uid (§7.2 passdb sync point 3).
- **Rebuilds rather than diffs.** Every marked line is dropped and the
  rendered ones appended, which is what makes a user removed from sc-core's
  registry disappear here too; the sidecar gets the same property from
  `cp /etc/passwd.base /etc/passwd`. Written to a temp file and renamed, so no
  reader sees a partial `/etc/passwd`.

**What it refuses to do.** It only ever touches accounts carrying
`sc-managed-smb` in the GECOS field; anything else is somebody else's. It
refuses the whole sync — leaving the running config untouched — if a rendered
name collides with a non-managed system account (adopting it would hand that
name an NT hash, and removing it later would delete a system user), if a
referenced gid has no group, or if `testparm -s` rejects the candidate. The
distro's original `smb.conf` is backed up once, before the first overwrite.

**The off switch.** `SmbOrchestrator::remove_rendered` deletes all three files
when `smb.enabled` goes false, so their absence *after* a good sync is
unambiguously "off" rather than "not synced yet" — the agent stops smbd,
prunes the passdb and restores the backup. It also means NT hashes do not sit
on disk for a feature that is turned off.

**A silent failure worth knowing about.** `pdbedit -i` reports success and
imports *nothing* when the uid in field 2 of the `smbpasswd` file doesn't
match the account's real uid — no error, no output, an empty passdb (measured
on Samba 4.20; `pdbedit -e` itself emits uid 0, which is how this was found).
Every downstream symptom looks like a config problem, so the agent verifies
after importing that each desired name is actually in the passdb and logs a
`WARNING` naming the missing accounts. A warning rather than a failure: one
bad entry must not take SMB down for everyone else.

**Coverage.** `scripts/smb-native-test.sh` runs the agent against
`rockylinux:9`, `debian:12` and `alpine:3.20` — config install, passwd
reconciliation, the smbpasswd import including the uid-0 case above, an
authenticated SMB3 round-trip checked byte-for-byte, a wrong password, both
refusals, and teardown. It takes `[global]` verbatim from real
`sc-server smb-sync` output, so a renderer change that breaks the agent's
"first line is `[global]`" assumption fails the test instead of production.
**Not covered: the systemd and OpenRC glue** — containers have no init, so
the harness drives the agent with `--once`. The unit files are only exercised
by installing them on a real host.

---

## 8. Cloudflare in front

| item | handling |
|---|---|
| real client IP | `CF-Connecting-IP`, **only after matching against the trusted-proxy CIDR list.** The list ships pinned in the image and is admin-updatable; unverified requests fall back to the socket peer IP |
| real client IP (generic reverse proxy) | `X-Forwarded-For` if `CF-Connecting-IP` is absent. Used **only if the same CIDR gate passes**, scanning the list **right to left** and taking the first entry that isn't a trusted proxy (the leftmost entry is always client-supplied and can be forged). An unparseable entry stops the scan immediately and falls back to the peer IP. §9's compose supports "behind cloudflared or a reverse proxy," so nginx/Traefik deployments get the same protection |
| trusted proxies unset | startup diagnostics warn loudly (`DESIGN-AUTH.md` §7.1). Behind a proxy with an empty list, every request collapses onto the proxy's single address — the login brute-force gate, rate limiter, and audit log all fall into one bucket. Malformed CIDR entries are silently dropped, and diagnostics list them separately |
| protocol | `X-Forwarded-Proto` decides the secure-cookie flag. `Host` is validated against the allow-list |
| body size | Free/Pro/Biz cap at 100MB -> `chunk_size_advisory` is advertised as a **recommendation** (10 MiB when Cloudflare is detected). No server-enforced ceiling — see `DESIGN-UPLOAD.md` §1.3 for the full chunk-size policy this feeds |
| origin timeout | 100s (524) -> chunk target transfer time is 20s |
| caching | every auth response gets `Cache-Control: private, no-store`. Content-origin signed URLs get `max-age` equal to the signature's own TTL |
| Cloudflare Tunnel | the recommended no-exposed-port deployment path. **The same body-size limits apply**, documented explicitly |
| WebSocket | used for invalidation pushes. Cloudflare supports it, but has a 100s idle timeout, so a 30s heartbeat keeps it alive |

---

## 9. Compose example

```yaml
services:
  sc:
    image: ghcr.io/…/sc:core
    user: "1000:1000"
    group_add: ["1001"]              # media group shared with Jellyfin
    read_only: true
    tmpfs: ["/tmp:size=64m"]
    cap_drop: ["ALL"]
    security_opt:
      - no-new-privileges:true
      # only if openat2/landlock are EPERM on an older Docker:
      # - seccomp=./seccomp-openat2.json
    volumes:
      - sc-data:/var/lib/sc                    # DB, thumbnails, sessions. The only writable path
      - sc-smbcfg:/config/smb                  # shared with the sidecar (rw)
      - /srv/photos:/shares/photos:z           # lowercase z -- SELinux shared label
      - /srv/video:/shares/video:z             # shared with Jellyfin
    environment:
      SC_DATA_DIR: /var/lib/sc
      SC_TRUSTED_PROXIES: "173.245.48.0/20,…" # Cloudflare CIDRs
      SC_MASTER_KEY_FILE: /run/secrets/sc_master
    secrets: [sc_master]
    ports: ["127.0.0.1:8080:8080"]             # behind cloudflared or a reverse proxy
    stop_grace_period: 30s                     # upload draining
    healthcheck:
      test: ["CMD", "/sc", "healthcheck"]
      interval: 30s

  sc-smb:                                       # optional
    image: ghcr.io/…/sc:smb
    volumes:
      - sc-smbcfg:/config/smb:ro
      - /srv/photos:/shares/photos:z
      - /srv/video:/shares/video:z
    ports: ["192.168.1.10:445:445"]             # LAN only. Never expose to the internet
```

**Graceful shutdown**: SIGTERM -> refuse new connections -> commit in-flight
upload session state -> flush any pending dirty `diretag` -> DB WAL
checkpoint -> exit. Uploads are resumable, so a forced kill loses no data,
but a clean shutdown makes the resume point exact.

### Reachability — `bind` alone isn't enough

This actually happened once: a local dev test and the operator's own
Tailscale session were pointed at the same process, and an upload test wrote
a file straight into the share the operator was looking at. Half the cause
was `Config::default()`'s `bind = 127.0.0.1` still being live — even a
Tailscale connection never reaches loopback, so the connection couldn't even
form. From here on, **development and production always run as separate
instances, on separate ports**: production on `8080` (this document's
compose example), development on `8081`, each with its own config, data
directory, and `shares/`. Pointing both at the same data directory or the
same share paths reproduces the same incident — the point of separating
ports is to force two processes running on the same machine, for different
purposes (one real data, one a test), to actually stay apart.

Fixing `bind` alone doesn't finish the job, either. Reaching the server from
outside the container (another machine, a VPN, Tailscale) needs three things
aligned, and only one of them is commonly known:

1. **`bind`** — must actually listen on a non-loopback interface
   (`SC_BIND=0.0.0.0:8080`; §9/§11.4 above already cover this).
2. **`app_hosts`** (`HostGuard`) — the list of `Host` headers the server will
   answer. A request with a `Host` not on the list gets 421
   (`crates/sc-server/src/config.rs`'s doc comment: "Host header carries
   whatever literal the client dialled"). Fixing `bind` and leaving this
   empty means the connection succeeds from outside the container but every
   request bounces with 421 — the hardest failure mode for a first-time
   operator to diagnose.
3. **`allowed_origins`** — the CSRF origin check for cookie-authenticated
   write requests (`DESIGN-AUTH.md` §3.3, not the SMB material in §7.2).
   **Left empty, it's derived automatically from `app_hosts`**
   (`config.rs`: "Empty means derive from `app_hosts`"), so in practice only
   two things need to be known. Setting it explicitly only matters for the
   uncommon proxy setup where the browser's `Origin` doesn't match what the
   server sees as `Host`.

So the minimum configuration is **`bind` + `app_hosts`**; an empty
`allowed_origins` stands in for the third automatically. An operator who
fixes `bind` and then hits 421 should look at this table next.

---

## 10. Deployment security checklist

- [ ] `user:` set — unprivileged execution, no root entrypoint
- [ ] `cap_drop: ALL`, `no-new-privileges`
- [ ] `read_only: true` + only the data directory writable
- [ ] no uppercase `:Z` on volume mounts (breaks shared folders)
- [ ] no `chown -R` on user data
- [ ] master key mounted as a secret file, not an environment variable (`docker inspect` exposure)
- [ ] SMB 445 bound to LAN only. Sidecar separated out
- [ ] `openat2` / Landlock availability surfaced in startup logs and the health endpoint
- [ ] overlayfs-path shares refused
- [ ] no shell or package manager in the image (distroless); debugging uses a separate tag
- [ ] image signed (cosign) + SBOM published
- [ ] OIDC client secret in its own file at mode `0600`, never in `config.toml` and never an environment variable (§13.2)
- [ ] `oidc.local_password_login = "deny"` only where an unlinked administrator is kept as a way back in (§13.5)

---

## 11. Implementation status (2026-07-29 deployment audit)

This document described a deployment that, for a long time, didn't exist.
`Dockerfile`, `docker-compose.yml`, `.dockerignore`, `.github/workflows/*`,
and `deny.toml` closed that gap, and this section records what they actually
produce versus what's still missing, without softening it. Paths are
repo-root-relative.

### 11.1 One binary, not two

§1's table describes `sc:core` as containing **two** statically-linked
binaries (`sc-server` + `sc-preview-worker`). In reality `sc-preview-worker`
has no source, no binary, no Cargo target. The module doc in
`crates/sc-preview/src/worker/jailed/mod.rs` already says so accurately: the
preview worker only `fork(2)`s from the running `sc-server` process, never
`execve`s. That module's own doc notes that a hardened variant would
re-`exec` a dedicated `sc-preview-worker` binary, that this would be strictly
better for production, and that it's deliberately deferred.

`Dockerfile` reflects this — it builds the one binary that exists and
invents no second one. If `sc-preview-worker` is ever built for real, the
builder stage needs one added line (`cargo auditable build ... --bin
sc-preview-worker`) and the runtime stage one `COPY --from=builder` line;
the structure doesn't need to change.

### 11.2 mimalloc — wired up, and measured (2026-07-30 follow-up audit)

The `#[global_allocator]` §1 calls for turned out to live outside
`crates/sc-server/src/lib.rs`: `main.rs` is the `sc-server` **binary**
crate's root, and `#[global_allocator]` is a whole-program, link-time
setting, so declaring `#[cfg(target_env = "musl")] #[global_allocator]
static GLOBAL_ALLOCATOR: mimalloc::MiMalloc = ...` in `main.rs` was enough —
`lib.rs`/`routes.rs`/`app.rs`/`bridge.rs` (owned by other agents) are
untouched. `crates/sc-server/Cargo.toml` puts `mimalloc` under
`[target.'cfg(target_env = "musl")'.dependencies]`, so neither the Windows
dev build nor the `ubuntu-latest` glibc CI job pulls in the dependency or its
C toolchain requirement.

**Measurement**: this box has no Docker, and linking a real musl executable
with the local zig cross-linker also fails — rustc's self-contained musl CRT
objects and zig's bundled musl libc both define `_start`
(`cargo check`/`clippy` only compile and archive, never link, which is why
this doesn't touch the verification gate — `verify.sh` never runs `cargo
build` against the musl target; the one place a musl link happens outside
Docker is `verify.yml`'s Linux job, which links `release-dist` with a real
`musl-gcc`, and there mimalloc *is* compiled in). So the musl-vs-mimalloc
comparison
§1 makes couldn't be reproduced in this environment. Instead, an independent
microbenchmark (4-8 threads, many small `Vec`/`String`/`HashMap` allocations
per iteration — standing in for "lots of small allocations across a thread
pool") was built and run natively on this repo's Rocky 9 test VM (`node
.vm/vm.mjs`, real Linux, glibc, virtualized) comparing the system allocator
against mimalloc directly. Result: mimalloc was **consistently ~3-4x
slower** than the glibc system allocator on this workload (e.g., 4 threads /
2M iterations: glibc ~270-280ms, mimalloc ~1000-1050ms; the same ratio held
at 8 threads). This doesn't contradict §1's claim — glibc has per-thread
arenas, a fundamentally different design from musl's single-lock dlmalloc
with no arenas, and virtualization disadvantages mimalloc's heavier
`mmap`/`madvise` usage. But honestly: **musl itself has never been measured
in any environment.** The real answer only comes from inside the
image the `docker` CI workflow actually builds — don't take §1's throughput
claim at face value until it's re-measured there. Full reasoning and
benchmark code live in `crates/sc-server/src/main.rs`'s comments.

### 11.3 `sc:media` and `sc:ocr` are not built, and will not be

Both are non-goals now, not pending work — see `FEATURES.md` #150.

- `sc:ocr`: OCR is an explicit non-goal (#120). No model, no feature flag,
  nothing to package.
- `sc:media`: it existed only to carry `ffmpeg` for video thumbnails (#99),
  also a non-goal. `worker::JobKind::Video` keeps its protocol slot and is
  refused with `NegativeReason::Unimplemented` before any bytes are read;
  §4.4 explains why a second, `execve`-based jail is not worth standing up.

`sc:smb` **is** built — `Dockerfile.smb` plus `deploy/smb/` (entrypoint that
validates a candidate `smb.conf` with `testparm -s` before promoting it,
inotify re-sync, fail2ban, `tini` as PID 1), wired into `docker-compose.yml`
and covered by the `build-smb-and-smoke` job in `.github/workflows/docker.yml`.

### 11.4 `SC_BIND` — the hidden bug in §9's compose example

`Config::default()` (`crates/sc-server/src/config.rs`) binds to
`127.0.0.1:8080`. Correct on bare metal, wrong in a container: `docker run
-p` DNATs to the container's bridge interface, not to loopback, so following
§9's compose example literally produces **a server unreachable from outside
the container.** Of the four env vars `apply_env` reads, `SC_BIND` is the
documented way out. Fixed in both `Dockerfile` (image default) and
`docker-compose.yml` (set explicitly, redundantly) with
`SC_BIND=0.0.0.0:8080`. §9's compose snippet itself is still missing this —
noted here.

### 11.5 Master key mount — an rw bind mount, not compose `secrets:`

§9's compose example uses `secrets: [sc_master]` +
`SC_MASTER_KEY_FILE: /run/secrets/sc_master`. Docker Compose's (non-swarm)
file-based `secrets:` mounts its target **read-only**. But
`crates/sc-server/src/masterkey.rs`'s first-boot logic needs to **write** a
new key file at that path if none exists (§7.2/§8: "if absent, generate one
and write it at mode 0600") — which a read-only mount can't allow.
`docker-compose.yml` instead bind-mounts a dedicated directory
(`./secrets:/run/sc-secrets`, separate from the data volume — §7.2's
requirement #2) read-write, so first-boot auto-generation actually works. If
a key was pre-provisioned and should never be rewritten by the container
(Vault/SOPS or a real secrets manager), mount that source read-only instead
— the code path only writes when the file is absent.

### 11.6 Healthcheck — wired up (2026-07-30 follow-up audit)

`test: ["CMD", "/sc-server", "healthcheck"]` now actually works. Rather than
adding a sixth subcommand to the `Command` enum in
`crates/sc-server/src/lib.rs` (`Serve`/`Caps`/`Setup`/`Gc`/`Routes`/
`SmbSync`, which would mean touching a file owned by another agent),
`main.rs` checks `argv[1] == "healthcheck"` directly, before `Cli::parse()`
runs, and branches there — `sc_server::Cli` never sees this path. Once
branched, it opens a plain `std::net::TcpStream` (no new dependency) to the
port named by `SC_BIND` and issues a loopback `GET /api/health`; HTTP 200
with a `"status":"ok"` or `"status":"degraded"` body exits 0, anything else
(connection failure, timeout, malformed response) exits 1. **Treating
`degraded` as healthy (exit 0) is deliberate** — a rejected share or a failed
SMB bind is a config problem a container restart won't fix, so mapping
`degraded` to unhealthy would put Docker into a restart loop that never
ends. Only the one state a restart can actually help gets marked unhealthy.
`Dockerfile` adds `HEALTHCHECK --interval=30s --timeout=5s
--start-period=10s --retries=3 CMD ["/sc-server", "healthcheck"]` — exec form,
so it runs the listed argv directly with no shell or `ENTRYPOINT` in the
path, which works fine in a distroless image. `docker-compose.yml` states the
same values explicitly too, so `docker compose ps` shows them directly. This
box still has no Docker to verify locally, but
`.github/workflows/docker.yml`'s smoke test now checks both `docker exec
sc-smoke /sc-server healthcheck` directly and waits for `docker inspect
--format='{{.State.Health.Status}}'` to report `healthy` — the only place
that proves the Dockerfile's `HEALTHCHECK` instruction itself works inside a
real container.

### 11.7 What CI proves, and what it doesn't

`.github/workflows/verify.yml` runs `bash scripts/verify.sh` on both
`ubuntu-latest` and `windows-latest`, but the two jobs are not symmetric and
are not meant to be. Each proves what it can actually prove:

- **Linux is the deployment platform, and proves strictly more.** It is the
  only place in the whole pipeline where the `openat2`/`RESOLVE_BENEATH` VFS
  backend, the Landlock and seccomp hardening, and the forked preview jail
  *execute* rather than merely type-check — everything behind
  `#[cfg(target_os = "linux")]`. The job sets `VERIFY_REQUIRE_MUSL=1` and
  `VERIFY_REQUIRE_UI=1`, so a missing toolchain there is a failure, not a
  SKIP, and it also runs the musl `cargo check` + `clippy` and then links the
  shipping binary (`--profile release-dist`, the exact build the Dockerfile
  runs).
- **There is no committed `.cargo/config.toml` any more, and that was a bug
  fix, not tidying.** It carried the Windows dev box's `zig cc` shim — an
  `[env]` CC of `powershell -File …`, plus a `[target.x86_64-unknown-linux-musl]`
  linker — as *absolute paths under one developer's home directory*. A cargo
  config applies to every host that reads it, so `ubuntu-latest` dutifully
  resolved that linker relative to its own checkout and failed with ``linker
  `/home/runner/work/stowcloud/stowcloud/C:/Users/<name>/.../zigcc-musl-linker.cmd`
  not found``. The `[env]` half had already needed a workflow-level override
  for exactly the same reason, which should have been the tell. Host-specific
  toolchain wiring now lives in `scripts/musl-env.sh`, sourced by
  `verify.sh` and `deploy.sh`, which can ask `uname -s` what host it is on:
  the zig shim on Windows (repo-relative, so a clone works wherever it lands),
  plain `musl-gcc` + rustc's own self-contained linking everywhere else. Every
  assignment defers to an already-set value, so the Dockerfile's explicit
  `CC_*`/`AR_*` still win. The cost, stated because it is real: a bare `cargo
  build --target x86_64-unknown-linux-musl` on Windows now needs
  `. scripts/musl-env.sh` first.
- **Windows is the mandated development platform**, and its job proves the
  `portable` VFS backend, the frontend build, and that the gate a contributor
  runs on their own machine passes. It sets `VERIFY_REQUIRE_UI=1` only: the
  musl steps SKIP there **by design**. That job used to cross-compile too,
  through a `zig cc` shim installed by downloading a zip from ziglang.org and
  matching a hardcoded SHA-256 — the most fragile step in the pipeline, whose
  only coverage was a `cargo check` the Linux job now does natively with a
  real `musl-gcc`, and then runs and links. Dropping it also halves that job's
  disk: `cargo clippy --all-targets --target musl` is a second complete copy
  of every crate and every test binary, and `windows-latest` ships ~14 GB
  free. `CARGO_INCREMENTAL: 0` and `CARGO_PROFILE_{DEV,TEST}_DEBUG:
  line-tables-only` are there for the same budget — measured on this
  workspace, the test binaries alone go from 3506 MB to 624 MB without full
  debug info, and `line-tables-only` still keeps panic locations and
  symbolized backtraces.
- Both jobs end with a `Disk` step marked `if: always()`, because "no space
  left on device" and "a crate failed to compile" are indistinguishable once
  a step's log is collapsed.
- What this workflow **doesn't** prove: the confidence level that comes from
  testing inside the actual Rocky VM §12 deploys to. A GitHub-hosted runner's
  kernel version and enabled LSMs (Landlock in particular —
  `CONFIG_SECURITY_LANDLOCK` plus correct `lsm=` stacking) aren't controlled
  or verified here. And a hosted runner is a real VM, not a container, so
  §2's central claim — Docker's default seccomp allow-list returning EPERM —
  doesn't reproduce there at all; that's covered separately by
  `.github/workflows/docker.yml`'s smoke test hitting `/api/health` inside an
  actual container.
- `.github/workflows/docker.yml` builds the image, runs an amd64 container,
  and smoke-tests `/api/health`, `/`, and `/api/setup` — never run locally on
  this Docker-less Windows box. arm64 is build-verified only via QEMU
  (execution under emulation isn't meaningful, so `--load`/run is skipped);
  real aarch64 hardware execution is unverified.

### 11.8 SBOM / dependency audit — `cargo auditable` + `cargo-deny`

Chose `cargo auditable` over `cargo-sbom` or a standalone CycloneDX file: it
wraps `cargo build` and embeds the dependency list (crate name, version,
source) directly in the binary's own ELF section (`.rust-audit-info`). A
distroless image has no shell and no package manager, so there's normally no
way to ask "what's in this image" from inside the image itself; an embedded
SBOM answers that question from outside (`cargo audit bin` or
`rust-audit-info dump` on the extracted binary) with nothing that can go
missing as a separate file. `.github/workflows/supply-chain.yml` also runs
`cargo-cyclonedx` on top, producing a standard CycloneDX JSON artifact for
Dependency-Track-style consumers.

The real gate is `deny.toml` (repo root) via `cargo deny check`: RustSec
advisories, license conflicts with this workspace's AGPL-3.0-or-later, and a
ban on wildcard (`"*"`) dependencies, all enforced in CI. Version pinning
itself was already handled by the committed `Cargo.lock` + `--locked` on
every build — `deny.toml` is policy, not a new pin.

### 11.9 Image size — measured 2026-07-31: 26.1 MB, under target

Previously "still unmeasured", because this Windows box has no Docker. It
does not need to: the Rocky guest §12 describes has Docker 29.6.2, and the
image now builds and runs there.

`docker images sc:core` reports **26.1 MB** on disk against §1's < 30 MB
target — a real `docker build` of this repo's `Dockerfile`, not an estimate.
`docker image inspect --format '{{.Size}}'` gives 6,995,788 bytes of content;
the two numbers differ because one counts uncompressed layer bytes as stored
and the other the image's own content size, and the 26.1 MB figure is the one
§1's target should be read against.

That is with everything the old estimate worried about actually linked in:
`rusqlite`'s bundled SQLite, `argon2`/`chacha20poly1305`, `image`'s decoders,
and `mimalloc`. It leaves ~4 MB of headroom, which is thinner than it looks —
one more bundled C library could cross it, so this stays worth re-measuring
rather than assuming it holds.

### 11.10 CI action pins — now SHA-pinned (2026-07-30 follow-up audit)

GitHub Actions were originally pinned
(`actions/checkout@v4`, `docker/build-push-action@v6`, etc.) by tag only,
deferring SHA pinning as "one lookup per action." That lookup has since been
done: the GitHub REST API (`GET /repos/<owner>/<repo>/commits/<tag>`)
resolved the commit SHA for every tag in use, across all eight actions
(`actions/checkout`, `actions/setup-node`, `goto-bus-stop/setup-zig`,
`docker/setup-buildx-action`, `docker/build-push-action`,
`docker/setup-qemu-action`, `EmbarkStudios/cargo-deny-action`,
`actions/upload-artifact`), rewritten as `uses: <owner>/<repo>@<sha> #
<tag>`. **Why not defer again**: these workflows will almost certainly gain
registry credentials soon (`docker.yml` is `push: false`/`load: true` only
today, but shipping `sc:core` for real means `docker/login-action` + a GHCR
push eventually), and a mutable tag becomes a supply-chain attack surface the
moment credentials exist — whoever can retarget a tag can run code holding
our registry credentials on the next run. SHA pins remove that surface. The
cost was genuinely small (one API call per action), so there was no reason
to defer further. The maintenance cost of re-resolving SHAs on version
bumps remains, but the `# v4.4.0`-style comment on each line makes that
mechanical.

**Since then**: `goto-bus-stop/setup-zig` is gone. The Windows job no longer
cross-compiles to musl at all (§11.7), so the action, the zip download that
replaced it, and its pinned SHA-256 all left with it — seven actions remain.

### 11.11 Base image digests — one didn't exist

`gcr.io/distroless/static-debian12:nonroot`'s amd64/arm64 digests were
originally recorded as "obtained via registry API, not confirmed by a local
pull" — honestly, but unverified. Verifying them needs no Docker: the
registry's own HTTP API is reachable with curl directly — an anonymous
pull token from `gcr.io/v2/token`, the OCI image index via the manifest-list
Accept header, then each platform digest re-fetched from
`.../manifests/<digest>` to confirm a 200.

Result: **both previously recorded digests didn't exist on the registry**
(`sha256:23795be0…` amd64, `sha256:42ae677c…` arm64 — both 404). Not "unverified,"
just wrong — anyone who ran `docker build` against that state would have
failed at the `FROM` line. `Dockerfile` and `.github/workflows/docker.yml`
now carry the measured values:

| platform | digest (verified 2026-07-30) |
|---|---|
| amd64 | `sha256:7a2bd171a18bdd39a4729600d0dca5f16e779d41156a6908b4f8a9a289e76d92` |
| arm64 | `sha256:37d3baf4e657ce79c144b69b69f60b8542f366a64fb7a49bd99f5444ef008bb0` |
| multi-arch index (the `nonroot` tag itself) | `sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35` |

`node:22-alpine` and `rust:1.85-slim-bookworm`'s existing digests were
re-checked the same way and confirmed valid (Docker Hub's registry API
returned 200), so those weren't changed. **Both images have since been
replaced** — not because the digests were wrong, but because the versions
were: the first real `docker build` (2026-07-31, in the Rocky guest) failed
at `npm ci` under node 22's npm 10, then at `cargo build --locked` under
rustc 1.85, both because the committed lockfiles need newer. `Dockerfile`
now pins `node:24-alpine` and `rust:1.88-slim-bookworm`, digests confirmed
by actual `docker pull` + `docker inspect` — the stronger of the two claims
this section distinguishes. "Confirmed to exist via the
registry API" and "confirmed via `docker pull` + `docker inspect`" remain
different claims — only the former was done here. Still, this at least
surfaced that two of the three previously-recorded digests were invalid to
begin with. Running `docker buildx imagetools inspect
gcr.io/distroless/static-debian12:nonroot` once more on a machine with Docker
is recommended as a final check.

---

## 12. Production today — a QEMU guest, not bare Windows

Everything above (§1-§11) describes the Docker path. **As of 2026-07-31 that
path is what production actually runs** — `sc-prod-docker.service` in the
Rocky guest, `sc:core` against the same `/opt/sc/prod` data and shares the
bare-metal unit used. Previous revisions of this section said the Docker
path "has never served a real request"; that is no longer true.

An earlier revision described a *bare-Windows* deployment (two
`sc-server.exe` processes on the host, fronted by `tailscale serve`). That is
gone. **Windows now runs neither instance and keeps no residual files from
either** — no `.demo/`, no `.dev/`, no `sc-server.exe` anywhere on the host.
What's actually running, and reachable from a phone, is a Rocky Linux 9 guest
under QEMU, on the same Windows machine, with `tailscale serve` fronting it
exactly as before.

### Topology

```
tailnet --https--> tailscale serve --> 127.0.0.1:8080 (Windows host)
                                            |
                                   .vm/tunnel.mjs (Node, host)
                                            |  SSH channel, port 2222
                                            v
                                   QEMU guest (Rocky 9, sc-test.qcow2)
                                     sc-prod-docker.service :8080
                                       └─ docker run sc:core --user 1000:1000
                                          127.0.0.1:8080 -> container :8080
                                     sc-dev.service  :8081  (systemd, User=sc)
```

- **Production**: `https://<host>.<tailnet>.ts.net` -> host
  `127.0.0.1:8080` -> guest `sc-prod-docker.service`, config
  `/opt/sc/prod/sc.docker.toml` (rendered from `sc.toml`; see below).
- **Development**: `http://127.0.0.1:8081` -> guest `sc-dev.service`, config
  `/opt/sc/dev/sc.toml`. Own data directory and `shares/`, never pointed at
  production's, for the reason in §9's reachability subsection (an upload
  test once wrote into the share the operator was actually browsing).
- `tailscale serve`'s configuration didn't change — it still proxies to
  `127.0.0.1:8080` on the Windows host. Only what answers behind that socket
  changed, from a native process to `.vm/tunnel.mjs`'s listener. The MagicDNS
  certificate note from the previous revision of this section still applies
  unchanged: Tailscale issues a cert for the full MagicDNS name
  (`<host>.<tailnet>.ts.net`), not the bare short hostname, so hitting the
  server by the short name fails
  with a TLS alert before any application-level `Host` check runs.
- Inside the guest, both units bind `0.0.0.0`, not loopback: QEMU's user-mode
  networking (slirp) NATs a hostfwd connection to the guest's own address,
  never to the guest's loopback — the guest's private slirp network plays the
  same role Docker's bridge network does in §11.4's `SC_BIND` case.
  `trusted_proxies` on both includes `10.0.2.2/32`, the address slirp
  presents as the connection source for anything arriving via hostfwd
  (confirmed empirically against this guest).
- Production's `trusted_proxies` gained **`172.17.0.1/32`** when it moved into
  a container, and that entry is load-bearing: Docker's published-port DNAT
  rewrites the source address, so the server's peer is the bridge gateway
  rather than `127.0.0.1`. `resolve_client_ip` discards forwarding headers
  from an untrusted peer by design, so without it every request through the
  tunnel resolves to `172.17.0.1` — `tailscale serve`'s `X-Forwarded-For` is
  dropped, `auth.db`'s audit log attributes every login to one address, and
  the per-IP login rate limiter collapses into a single shared bucket.
  Measured both ways before the cutover, not reasoned about: a probe login
  through the container recorded `ip=172.17.0.1` without the entry and
  `ip=203.0.113.99` (the `X-Forwarded-For` value) with it, and the first real
  login attempt after cutover recorded a real `100.64.0.5` tailnet address.
  `/32` and not the `/16`: the port is published on `127.0.0.1` only, so
  only host-local traffic is DNAT'd through the gateway, while a sibling
  container would arrive as its own `172.17.0.x` and must stay untrusted.
- `auth.db`, `acl.db`, `links.db`, and the compat instance id
  (`compat-nc.db`) were carried into the guest **verbatim** when this move
  happened, never regenerated — none of them are reconstructible from the
  filesystem, and a changed instance id forces every compat sync client to
  resync from scratch.

### 12.1 Production on Docker (2026-07-31)

`sc-prod-docker.service` replaced `sc-prod.service`. Same data directory,
same shares, same master key file, same `127.0.0.1:8080` socket the host-side
tunnel and `tailscale serve` already point at — the container publishes to
that socket, so nothing above the guest changed.

```
ExecStart=/usr/bin/docker run --rm --name sc-prod-docker \
  --user 1000:1000 --read-only --tmpfs /tmp:size=64m \
  --cap-drop ALL --security-opt no-new-privileges:true \
  -p 127.0.0.1:8080:8080 \
  -v /opt/sc/prod/data:/var/lib/sc \
  -v /opt/sc/prod/shares:/shares \
  -v /opt/sc/prod/sc.docker.toml:/sc.toml:ro \
  -v /opt/sc/prod/keys/master:/keys/master:ro \
  -e SC_MASTER_KEY_FILE=/keys/master -e RUST_LOG=info \
  sc:core serve --config /sc.toml
```

`docker run` in the foreground under `Type=exec`, not `-d` with
`--restart`: systemd then supervises a process whose lifetime tracks the
container's, so `systemctl stop` and `Restart=on-failure` both behave.
`--user 1000:1000` matches the existing `sc` ownership of `/opt/sc/prod`, so
no `chown` was needed (§6.1 forbids it on mounted shares anyway).

`sc.docker.toml` is rendered from `sc.toml` by two path substitutions
(`data_dir` -> `/var/lib/sc`, `/opt/sc/prod/shares` -> `/shares`, both forced
by the container's mounts) plus the `172.17.0.1/32` `trusted_proxies` entry
§12's topology bullets explain. The bare-metal `sc.toml` is left untouched
next to it, since it is what the rollback reads.

**Rollback**: `sc-prod.service` is stopped and disabled but still on disk,
binary included. `systemctl disable --now sc-prod-docker && systemctl enable
--now sc-prod` returns to it. That is safe because the container binary was
verified not to migrate anything forward: a `.schema` hash of all ten SQLite
databases (`auth`, `acl`, `links`, `compat-nc`, `dav-locks`, `index`, `jobs`,
`meta`, `settings`, `shares`) was byte-identical before and after the image
opened a copy of the real data dir. Checked on a *copy* first, on a spare
port, precisely so this question could be answered before anything serving
was touched.

Verified after cutover, in the guest and through the full external path:
`/api/health` 200 via `tailscale serve`; the embedded UI served; `/status.php`
and `/ocs/v1.php/cloud/capabilities` 200; WebDAV and every `/api/*` route 401
unauthenticated; a real tailnet client IP in the audit log; 104 MB resident;
and `docker kill` of the container answered by systemd restarting it back to
a healthy 200. Job durability was proven separately on a scratch instance
rather than against production data — an archive job killed mid-flight at
3/13 came back as `interrupted` at 4/13, still in `GET /api/jobs`, with no
partial staging files left behind.

Not covered by this move: `sc-dev.service`, which stays a bare-metal systemd
unit on :8081. SMB was brought up separately the next day — see §12.2.

### 12.2 Production SMB, and the master key defect it exposed (2026-07-31)

Enabling `[smb]` in `sc.docker.toml` and running `sc-server smb-sync` produced
a **0-byte `smbpasswd`** while reporting success. `RUST_LOG=debug` gave the
reason: `master key cannot decrypt this account's stored SMB credential`, then
`skipping undecryptable NT hash in smbpasswd export`.

Two master key files had diverged. `/opt/sc/prod/keys/master` — the one the
container mounts — could not decrypt anything; `/opt/sc/prod/data/master.key`
could. Timestamps identified the cause: the NT hash was sealed 2026-07-29
13:20, and both key files were created 2026-07-30 04:25:32, 0.24 s apart. The
`keys/master` the unit mounts was **auto-generated by the container's first
boot** (the "generate one if absent" path in §3), not copied from the
bare-metal instance. Proven before acting by re-running `smb-sync` with
`SC_MASTER_KEY_FILE` pointed at the data-dir key: 101-byte `smbpasswd`.

Blast radius was checked against the DB *first*, not assumed: 1 SMB secret, 0
TOTP enrolments, 0 recovery codes, and 2 app passwords — which are plain
SHA-256, not master-key material, so a key swap could not touch them. Only the
one SMB credential was ever at stake. Fix: install the working key at
`keys/master`, keeping `keys/master.unusable-backup` and
`keys/master.copy-from-data-dir` (all mode 600) so it is reversible in both
directions. That also moved the live key *out of* the data directory, which is
what DESIGN-AUTH §7.2 wants — a data backup must not carry the key that
decrypts it.

**Why this stayed silent for a day**: `verify_master_key`
(`crates/sc-auth/src/rotate.rs`) deliberately only *warns* on an undecryptable
SMB secret and never refuses to start — its own comment records that treating
it as fatal once took production down over a single stale row. That is still
the right call, but it means the warning is the only signal, and with SMB off
nothing reads it. If you enable SMB on an instance whose key file history is
unknown, grep the journal for `master key cannot decrypt` before trusting an
empty `smbpasswd`.

Verified afterwards: `/api/health` 200, the warning gone from the journal, and
`smb-sync` rendering 2 shares / 1 user.

### Why a VM at all — and why the container runs inside it

§1-§11's container design needs Linux kernel features (`openat2`, Landlock,
`statx.btime`, real `inotify`) that a Windows host cannot provide. Docker
now runs *inside* this guest (§12.1), so the container path and the VM are
not alternatives — the VM is what supplies the kernel the container needs.
The guest stays deliberately minimal — **no Rust toolchain installed on the
guest itself**, still true after the Docker move: nothing on the guest's own
PATH is a compiler, and there is no `~/.cargo` or `~/.rustup` (checked, not
assumed). `docker build` gets its compiler from the
`rust:1.88-slim-bookworm` builder stage instead.

That is not free, though, and it is worth stating plainly rather than
implying the guest stayed as small as before: the builder image is 1.19 GB
and the build cache reached 3.5 GB on this disk. Both are reclaimable
(`docker image rm`, `docker builder prune`) and neither is needed to *run*
the 26 MB runtime image — but on a 23 GB disk with no swap, a build here is
a deliberate act with a cleanup step, not a free one.

An earlier cloud-init revision installed `rustup` plus a
C toolchain so `scripts/verify-linux.sh` could push the whole workspace in
and run `cargo build`/`cargo test` there; that script and the tooling it
needed are both gone now that prod/dev moved into this VM for real — running
a compiler in the same guest that serves production traffic was never the
plan (`.vm/cloud-init/user-data` has the removal note).

### Networking — slirp only, no TAP, no per-app hostfwd

`.vm/boot.sh` boots QEMU with `-netdev user` (slirp) and a single hostfwd
rule, `2222 -> 22`. **Production and development do not get their own
hostfwd rule.** Measured on this box: slirp drops a rising fraction of
truly-simultaneous new connections once a burst reaches ~8 (1/8, 3/16, 6/24,
all `ECONNREFUSED`), reproduced with N parallel Node `http.get` calls against
a hostfwd port. A browser's HTTP/2 fan-out for one page load (a dozen-odd
parallel stream opens) hits this every time and surfaces as scattered 502s
from `tailscale serve`. TAP would not have this problem, but was deliberately
not adopted — it needs an admin-privileged bridge/tap-adapter on the Windows
side, which this project chose not to take on.

The fix in use: `.vm/tunnel.mjs` listens on `127.0.0.1:8080`/`8081` on the
Windows host and forwards each accepted connection into the guest as an SSH
`direct-tcpip` channel multiplexed inside **one** SSH connection to port
2222 — slirp only ever sees a single new-connection event, no matter how
many application-level connections ride on top. No OpenSSH client, agent, or
`known_hosts` setup is needed; it's `ssh -L` reimplemented with `ssh2`'s
`forwardOut`.

9p is not available in this QEMU build, so host<->guest file transfer also
goes over SSH: `.vm/vm.mjs push`/`put` use SFTP (`ssh2`'s `fastPut`), not a
shared filesystem. A bind mount would be the sanctioned approach if ongoing
host<->guest file access were ever needed; nothing today needs more than the
one-shot binary push and source-tree push `scripts/deploy.sh` does.

#### SMB (§7): how the LAN reaches port 445 here

`sc:smb` binds `:445` inside the guest same as any other sidecar port, but
getting a LAN client to that port doesn't work the way `tunnel.mjs` solves it
for HTTP: an SMB client can't be pointed at a non-standard port the way a
browser can (Windows' own `net use`/UNC paths in particular have no syntax
for one), so the forward has to land on host port 445 itself.

Windows normally owns that port on every interface, which is what blocked
this for a long time. **Stopping the `LanmanServer` service is not enough.**
Reproduced on this box 2026-07-31: with `LanmanServer` `Stopped`, `netstat
-ano` still showed `0.0.0.0:445` `LISTENING` under PID 4 (`System`) and a
direct `TcpListener` bind still failed with access denied. The socket
belongs to the **`srv2`/`srvnet` kernel drivers**, not the user-mode
service. Both have to be stopped:

```powershell
# elevated
Stop-Service LanmanServer -Force
sc.exe stop srv2
sc.exe stop srvnet
```

`netstat` alone is a poor check here — it was misleading in both directions
during this work. The ground truth is an actual bind attempt:

```powershell
$l = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 445)
try { $l.Start(); 'BIND OK - port 445 is free'; $l.Stop() } catch { $_.Exception.Message }
```

With the port free, `.vm/boot.sh` forwards it (`hostfwd=tcp::445-:445`,
alongside the SSH rule). The slirp burst-drop that rules hostfwd out for
HTTP does not apply: an SMB client opens one TCP connection per server and
multiplexes everything inside it, so there is no fan-out of simultaneous new
connections to lose. The guest side is `sc-smb-prod.service` — a `docker run
sc:smb` publishing `0.0.0.0:445`, mounting `/opt/sc/prod/smbcfg` read-only
and `/opt/sc/prod/shares` read-write, with the cap set §7 documents.

Rollback for the host change (recorded before it was made, so it can be put
back exactly): `LanmanServer` → `Automatic`, Running; `srv2` → `Manual`,
Running; `srvnet` → `Manual`, Running.

Those three came back on their own after a host reboot while QEMU was down,
and the two now share the port rather than fight over it: Windows holds the
IPv6 wildcard `[::]:445` under PID 4 and QEMU holds `0.0.0.0:445`, because
Windows' listener is not dual-stack. So the hostfwd keeps working over IPv4
and a LAN client reaching the guest must not resolve the host to a AAAA
record — it would land on Windows' own file sharing instead. Whichever of
the two starts first wins its family, so restore the services only while the
VM is down, and re-check with the bind test above afterwards rather than
assuming the boot order held.

To tell which of the two answered, offer SMB 2.x only in a raw NEGOTIATE:
the sidecar renders `server min protocol = SMB3_11` and refuses with
`STATUS_NOT_SUPPORTED` (0xC00000BB), while Windows accepts and returns
dialect 0x0210. Do not probe with 3.1.1 — that dialect requires negotiate
contexts, and omitting them gets `STATUS_INVALID_PARAMETER` from both,
which distinguishes nothing. Measured 2026-07-31: IPv4 refused 2.x, IPv6
accepted it.

Binding host-side on `0.0.0.0` is deliberate — reaching it from the LAN is
the whole point — and is not a public exposure: the generated `smb.conf`
carries `hosts allow` for the private CIDRs plus `hosts deny = 0.0.0.0/0`,
and `validate_bind` (`crates/sc-smb/src/bind.rs`) refuses to render any
config at all if an interface is public. The standing no-TAP constraint
(above) still rules out a bridged adapter; nothing here needs one.

**Verified end to end 2026-07-31**, from the Windows host through the hostfwd
into the production sidecar against the real `/opt/sc/prod/shares` tree:
`net use` authenticates, a 256 KiB file written and read back is SHA-256
byte-identical, and `smbstatus -b` reports the session as SMB3_11 /
AES-128-GCM / AES-128-GMAC with NTLMv2. A wrong password is refused with
`NT_STATUS_LOGON_FAILURE`. The session shows up server-side as remote host
`10.0.2.2` — slirp's address for the Windows host — which is why the
`hosts allow` bug above had to be fixed before any of this could work.

Two things had to be right that were not, and neither announced itself: the
`hosts allow` CIDR list (§7.4 ②) and the share directory mode (§7.3). Both
present as "SMB is just broken" rather than as a specific error, so if a
fresh deployment cannot write over SMB, check those two before anything else.

### The way in — and a dead credential that cost an hour

**`node .vm/vm.mjs run "<cmd>"`** is the way to run a one-off command in the
guest; `push`/`put` move directories/files over SFTP; `ready` checks for
`/var/lib/cloud/sc-ready`. All of it authenticates as `sc`/`sctest` — a
password, not a key. Guest SSH is reachable only via QEMU's hostfwd on
`127.0.0.1:2222`, itself only reachable from the Windows host, so a plaintext
password over loopback-only SSH is an accepted trade for a disposable test
VM; it is not reachable from the tailnet or the LAN.

An earlier `.vm/sckey` keypair existed for the same purpose and looked like
the way in, but the guest's sshd rejected it — publickey denied, with no
matching entry ever written to `authorized_keys` in cloud-init. It has been
deleted rather than wired up: password auth over a loopback-only guest SSH
port already meets the bar this VM needs, and a second, unused auth path is
one more thing to go stale. If a key-based path is ever built, it needs a
`ssh_authorized_keys:` entry in `.vm/cloud-init/user-data` added and rebaked
in the same change that adds the key file — not before.

**A second trap, found while writing this section**: `node .vm/vm.mjs
put|push` takes the remote path as a bare CLI argument. Git Bash (MSYS)
rewrites any argument that looks like an absolute POSIX path (`/opt/...`)
into a Windows path (`C:/Program Files/Git/opt/...`) before `node.exe` —
a native, non-MSYS program — ever sees it, since MSYS only converts
arguments for programs it doesn't recognize as MSYS-aware. That silently
corrupted the remote path `scripts/deploy.sh` passed to `vm.mjs put`, and the
guest's "No such file" was completely accurate for the mangled path it
actually received. It then recurred verbatim when the source push was added
for the container build — `push` takes the same kind of bare `/opt/...`
argument, and all 380 files landed under `C:/Program Files/Git/opt/...`.
`scripts/deploy.sh` sets `MSYS_NO_PATHCONV=1` on each such call
individually, not for the whole script — exporting it globally was tried
first and broke a different thing: curl's own `mktemp`-generated `-D`/`-o`
paths further down the same script, which need MSYS's rewrite to become a
real Windows path since curl.exe is equally non-MSYS (reproduced: curl exit
23, "Failed writing received data to disk", with the export in place).
Anyone invoking `vm.mjs put`/`push` directly from Git Bash with an absolute
remote path needs the same env var, scoped to that one command.

### Deployment path: `scripts/deploy.sh`

Builds the frontend, bakes it into the server binary, cross-compiles for the
guest, and restarts both instances there. Run from the repo root: `bash
scripts/deploy.sh`.

Since the container cutover the two instances no longer deploy the same way.
Development still gets the cross-compiled binary this script builds;
production gets a source push and a `docker build` **in the guest**, because
the image builds its own frontend and its own binary in their own stages
(`.dockerignore` excludes `target/` and `web/build/` precisely so nothing
host-built can leak in). Steps 1–3 below therefore produce dev's binary, not
production's. Nothing here touches `sc-prod.service`: it is the disabled
rollback, and restarting it would start a *second* server against the same
data directory while the container holds `:8080` — the health check probes
that port, so the deploy would report success against the old container.

1. `cd web && npm run build`, then refuse to continue if the bundle carries
   the mock backend (`client.ts` throws at module load if it does, but by
   then it's already inside the binary — checked here so a mock build never
   reaches the guest).
2. `cargo clean -p sc-http` — required, not paranoia (next subsection).
3. `cargo build --release --target x86_64-unknown-linux-musl -p sc-server
   --features embed-ui`, into the repo's one shared `target/`.
4. Push the fresh binary to `/opt/sc/dev/bin/sc-server.new` over SFTP, then
   on the guest: `chmod 755` + `mv -f` into `sc-server`. Atomic rename (same
   filesystem), not a direct `cp`: `sc-dev.service` still has its current
   binary mapped at this point (restart happens after this step), and
   writing through a running executable's inode fails with `ETXTBSY` ("Text
   file busy") — reproduced the first time this was tried with a plain `cp
   -p`. The rename also means systemd never restarts into a half-written
   file.
5. Restart development by **systemd unit name** (`systemctl restart
   sc-dev`), never by matching the binary or the process. Polls
   `/api/health` on `:8081` for up to 30s.
6. Push the whole source tree to `/opt/sc/build/src` and `docker build -t
   sc:core .` there, then `systemctl restart sc-prod-docker` and poll
   `/api/health` on `:8080` the same way. The push is checked by grepping
   `vm.mjs push`'s output for its `FAILED` line rather than by exit code —
   `push` exits 0 even when individual files fail, so a partial tree would
   otherwise reach `docker build` unnoticed (it did, on the first run of
   this step: MSYS had rewritten the remote path and all 380 files failed).
7. Assert the served bundle is the one just built: compare the `start.<hash>.js`
   entry filename **and** the CSP inline-script hash actually served at `/`
   (via `scripts/inline-script-hashes.mjs`) against the local fresh build.
   Filename alone missed a real incident where the served inline bootstrap
   script and its advertised CSP hash came from two different builds — same
   filename, different embedded `index.html`, a blank page with nothing
   louder than a CSP violation in the console.

   Only **development** gets that full comparison. Its binary embedded this
   host's `web/build`, so anything but a byte match means a stale embed
   reached the guest. Production builds its frontend inside the image under
   `node:24-alpine` rather than whatever node this host has, so its asset
   hashes are not required to equal the host's; it gets the
   self-consistency half — the CSP hash the server advertises must match the
   document it actually served — which is the half that caught the
   blank-page incident.

### Why `cargo clean -p sc-http` is a required step, not paranoia

`#[derive(RustEmbed)]` reads `web/build/` while the macro expands, and cargo
has no dependency edge on those files — so building the frontend and then
running `cargo build` reuses whatever was embedded last time, silently. This
has shipped a stale UI more than once: a binary whose embedded SPA predated
the routes it was supposed to serve, indistinguishable from a frontend bug
until someone noticed the asset hashes hadn't moved. `scripts/deploy.sh` runs
`cargo clean -p sc-http` between the frontend build and the server build, and
then asserts the bundle the running binary actually serves matches the
bundle that was just built — the one check that would have caught every
stale-embed incident.

### Durability — surviving a host crash or reboot unattended

The host **has bluescreened** with the VM and tunnel up; recovery used to
mean noticing production was down and manually running `bash .vm/boot.sh`.
Both halves of the runtime now supervise themselves:

| piece | supervisor | detects | recovery action |
|---|---|---|---|
| `node .vm/tunnel.mjs` | `.vm/tunnel-supervisor.ps1` | `127.0.0.1:8080`/`8081` not both listening | relaunches the tunnel process |
| QEMU (`sc-test.qcow2`) | `.vm/vm-supervisor.ps1` | no `qemu-system-x86_64.exe` running against that disk | relaunches `bash .vm/boot.sh` |

Each supervisor is an infinite loop: check, act if needed, `Start-Process
... -PassThru` then `$p.WaitForExit()` so the loop blocks for exactly as long
as the child lives, then a short `Start-Sleep` (5s for the tunnel, 10s for
the VM) before checking again — so the worst-case detection delay after a
crash is one poll interval, and a supervisor that's already up never spawns
a duplicate of something still running.

**`.vm/ensure-runtime.ps1`** starts both supervisors, idempotently — it looks
for a `powershell.exe` process whose command line already names the
supervisor script before spawning a new one, so running it any number of
times, from any context, never produces a second copy of either. It spawns
each supervisor via `Invoke-CimMethod Win32_Process.Create` (WMI) rather than
`Start-Process`: a WMI-created process's parent is `WmiPrvSE.exe` (owned by
the `Winmgmt` service), not whatever ran `ensure-runtime.ps1` — so the
supervisors, the VM, and the tunnel all survive that script's own process
exiting or the terminal that launched it closing. That is not hypothetical:
a closing terminal is exactly what took production down before this existed.
`Register-ScheduledTask` was tried first and returns Access Denied
in this environment, tool-independent — not a fix for that problem, just
unavailable here.

**Host-level change**: `.vm/install-runtime-autostart.ps1` writes a single
value, `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\ScRuntime` = 
`powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File
"<repo>\.vm\ensure-runtime.ps1"`, so a Windows logon (after a reboot) reaches
the same idempotent entry point. This is the only persistent host-level
change the runtime installs — no scheduled task (Access Denied, above), no
service. Re-running the install script overwrites the same value; it never
adds a second one.

**Tested, not just designed.** With both supervisors running (confirmed
parented by `WmiPrvSE.exe`, not by the session that started them):

- `Stop-Process` on the live `node .vm/tunnel.mjs` process dropped both
  `127.0.0.1:8080` and `:8081` immediately; `tunnel-supervisor.ps1` relaunched
  the tunnel and both ports were listening again within its 5-second poll
  interval, no manual action taken.
- `Stop-Process -Force` on the live `qemu-system-x86_64.exe` process (running
  `sc-test.qcow2`) simulated a VM-level crash. `vm-supervisor.ps1` relaunched
  `bash .vm/boot.sh` within its 10-second poll interval; the disk overlay
  (`sc-test.qcow2`, never recreated — only baked once, per `boot.sh`) meant
  the guest came back with all prior state, and `sc-prod`/`sc-dev` (both
  `systemctl enable`d) started automatically. Both `/api/health` endpoints
  answered 200 again roughly 130 seconds after the kill — full unattended
  recovery, no keyboard input.

### The release gate: `scripts/verify.sh`, 12 checks

`scripts/verify.sh` is what "passing" means for a change in this repo:
a host build and test, strict clippy (`-D warnings`, `--all-targets`) on both
the host target and the `x86_64-unknown-linux-musl` cross target, a grep gate
that compat wire vocabulary never leaks into core crates, a grep gate that
the bind site installs `ConnectInfo` (silent client-IP loss is invisible to
the test suite otherwise), a `--no-default-features` build proving the NC
compat layer strips out cleanly, and — once a frontend build exists — the
`embed-ui` feature building and clippy-clean on both targets. Twelve checks in
total; currently 12/12.

The host is detected from `uname -s`, so the same file is the gate on a
Windows dev box, on `ubuntu-latest`, and in the Rocky VM. Two properties it
enforces on itself, both learned from a red CI:

- **A failing step prints everything it said.** An earlier revision piped
  failures through `tail -25`; when 57 tests failed at once the `failures:`
  name list alone overflowed that, and not one panic message survived into the
  log. Diagnosing it took a Linux VM and half a day.
- **Every cargo invocation passes `--locked`**, because CI and the Dockerfile
  do. Without it the script resolves a lockfile CI will refuse, and dependency
  drift is invisible here and fatal there.

The musl and `embed-ui` sections SKIP when the box has no cross toolchain or
no `web/build` — a fresh clone on a laptop should not have to install one to
run the gate. `VERIFY_REQUIRE_MUSL=1` and `VERIFY_REQUIRE_UI=1` turn those
SKIPs into failures; CI sets them where it has installed the thing, so a SKIP
there means the workflow broke rather than that the checkout is bare.

Two of these exist specifically because they catch failures the ordinary
test suite can't: the NC-isolation grep scans code, not comments, so a core
crate can explain in a comment why a protocol-neutral abstraction exists
without that check tripping, but literal `oc:`/`ocs`/`remote.php`
vocabulary in behavior fails it. The `ConnectInfo` grep exists because
`axum::serve(listener, router)` compiles and runs happily while silently
discarding all knowledge of where a request came from — a test can prove the
connect-info service works in isolation without proving the real bind site
calls it, so reverting that one call site would leave the test suite green.

---

## 13. Single sign-on (OIDC)

`DESIGN-AUTH.md` §13 is the design. This is the operator's side: what to
register at the identity provider, where the secret goes, and what changes for
your users the moment an account is linked.

### 13.1 What to register at the provider

One **confidential** client, in whatever the provider calls that (Keycloak:
"Client authentication: On"; Entra ID: a web app with a client secret; Google:
an OAuth client of type "Web application"). Public clients are not supported:
this server holds the secret and posts to the token endpoint itself.

| setting | value |
|---|---|
| grant / flow | Authorization Code |
| PKCE | S256. Sent on every request; a provider that rejects PKCE cannot be used |
| redirect URI | `https://<your app host>/api/auth/oidc/callback` |
| scopes | `openid`. Anything else is optional and, in practice, pointless here |
| token endpoint auth | `client_secret_basic`, or `client_secret_post` if that is all the provider advertises. Nothing else is supported |
| ID token signing | RS256 or ES256 |

**The redirect URI has to match byte for byte, in both directions.** It is
compared by the provider against what is registered, and the same string is
sent again on the token request as the spec requires. A trailing slash, `http`
instead of `https`, or a different host is a mismatch. This is why it is a
configuration key and not derived from `app_hosts` or `compat_canonical_url`:
those accept `http`, are not fully parsed as URLs, and are ambiguous when
`app_hosts` holds zero or several entries. Guessing here would produce a value
that is right on some deployments and silently wrong on others.

Only `openid` is needed because this server reads exactly one claim, `sub`.
`email`, `name` and `preferred_username` are never read or stored, so asking
for `profile` or `email` collects consent for data that is immediately
discarded.

### 13.2 The client secret goes in a file

```toml
[oidc]
enabled = true
issuer = "https://idp.example.com/realms/main"
client_id = "stowcloud"
client_secret_file = "/run/secrets/sc_oidc_client_secret"
redirect_uri = "https://cloud.example.com/api/auth/oidc/callback"
scopes = ["openid"]
display_name = "Company account"
```

```sh
install -m 0600 -o 1000 -g 1000 /dev/null /run/secrets/sc_oidc_client_secret
printf %s "$SECRET" > /run/secrets/sc_oidc_client_secret
```

Mode `0600`, owned by the uid the server runs as. **The server does not check
the mode**, unlike the master key, which it creates at `0600` itself. That is
worth knowing rather than assuming: nothing will warn you if this file is
world-readable.

`client_secret_file` is a path and not the secret itself for the same reason
`master_key_file` is (§7.2 mitigation 2). A secret written into `config.toml`
is a secret in whatever `scripts/deploy.sh` pushes and in every backup of that
file. It is not an environment variable for the reason §10's checklist gives
for the master key: environment variables show up in `docker inspect` and
`/proc/*/environ`. In Compose, mount it the same way `sc_master` is mounted.

There is no `SC_OIDC_*` environment variable. `[oidc]` is set in
`config.toml`, and eight of its ten keys are additionally editable from the
admin settings screen (item 170). `client_secret_file` and
`local_password_login` are **not** among those eight, deliberately: see §13.5.

### 13.3 HTTPS is not optional here

Three separate things fail without it, and two of them fail quietly:

1. The flow cookie is `__Host-sc_oidc`, and the session cookie the callback
   sets is `__Host-sc_sid`. The `__Host-` prefix requires `Secure`, so **a
   browser silently discards both over plain HTTP**. The visible symptom is a
   sign-in that always ends in `oidc.bad_state`, with nothing in the server
   log to suggest a cookie was involved.
2. Every provider endpoint URL must be `https://`, including the ones inside
   the discovery document. A plain-HTTP one is refused before a packet is
   sent.
3. The redirect URI must be `https://` or single sign-on does not activate at
   all, and the startup log says so (§13.4).

Behind Cloudflare or a reverse proxy this means `X-Forwarded-Proto: https`
must actually arrive, and the proxy's address must be in `trusted_proxies`
(§8). A deployment that terminates TLS at the proxy but does not forward the
header sees exactly the symptom in point 1.

### 13.4 Confirming it came up

Single sign-on is assembled at startup, and the provider is contacted lazily.
A provider that is down does not stop the server from starting; it makes the
sign-in button fail with `oidc.provider_unavailable` while everything else
keeps working.

| startup log line | meaning |
|---|---|
| `single sign-on is active; the provider is contacted lazily, not at startup` | good |
| `single sign-on is configured but inactive: <reason>` | `enabled = true` and something else is missing. The reason names the exact key |
| `[oidc] is configured but this binary was built with --no-default-features` | the OIDC client is not compiled into this binary |
| nothing at all (debug level says `single sign-on is off`) | `enabled = false`, the normal state for a deployment without a provider |

`<reason>` is one of: `oidc.issuer is empty`, `oidc.client_id is empty`,
`oidc.redirect_uri is empty`, `oidc.redirect_uri must start with https://`,
`oidc.client_secret_file is not set`, or `oidc.client_secret_file does not
exist: <path>`. A read failure or an empty secret file logs separately and
also leaves single sign-on off.

`GET /api/auth/oidc/config` is the other check, and needs no credential:

```sh
curl -s https://cloud.example.com/api/auth/oidc/config
# {"enabled":true,"display_name":"Company account"}
```

`enabled: false` there is exactly what the login screen sees, and the button
is not drawn.

### 13.5 `local_password_login`, and the state it can strand you in

```toml
[oidc]
local_password_login = "allow"   # allow | deny
```

`deny` refuses a **linked** account's web login by account password, so the
second factor the provider enforces cannot be sidestepped by knowing the local
password. Unlinked accounts are unaffected either way.

The default is `allow`, and the reason is recovery. Under `deny`, an outage at
the provider or a broken client registration locks out everybody, including
the administrator who would have to fix it. There is no account lockout in
this product (`DESIGN-AUTH.md` §7.1) and no way to reach zero active
administrators (§11) for the same reason: a state nobody can get out of is
worse than the risk it removes.

**If you set `deny`, keep a way in.** The practical one is an administrator
account that is never linked, since the refusal only applies to linked
accounts.

`local_password_login` and `client_secret_file` are the two `[oidc]` keys the
settings screen shows read-only, with the reason printed next to them. The
settings store overrides `config.toml` on every boot, so a `deny` set from the
screen could not be undone by editing the file: the override would win again
at the next start, which is the unrecoverable state the paragraph above exists
to avoid. Both are `config.toml` only, and both need a restart.

### 13.6 A provider on your own network

```toml
allow_private_endpoints = true
```

Off by default, because refusing loopback, RFC 1918, link-local and IPv6
unique-local addresses is what stops this server being pointed at internal
services, cloud instance metadata (`169.254.169.254`) included. Turn it on
only for the case it exists for: a Keycloak or Authentik on the same network
as this server. HTTPS is still required.

An identity provider using a **private CA** is not supported today. The TLS
client compiles in the Mozilla root set and has no hook for extra roots
(`DESIGN-AUTH.md` §13.8).

### 13.7 What linking does to SMB, and how to undo it

This is the part that generates support tickets, so read it before you enable
anything.

**Linking an account deletes the SMB NT hash derived from its account
password.** This is intentional and it is the whole point: SMB carries no
second factor, so leaving it working would be a way around the provider's own
policy (the same argument as §7.2's 2FA carve-out). A `Dedicated` SMB secret,
if one exists, is untouched. WebDAV with the account password is refused too;
an app password still works.

**The published `smbpasswd` follows that deletion on its own, and you should
know exactly when it does not.** A running server rewrites the file within a
second of the link, with no `smb-sync` and no restart, and says so once at
startup:

```
passdb publisher armed: an NT hash change now rewrites smbpasswd without `smb-sync`
```

Three things can leave the file behind anyway, and only the first one is
visible in the startup log (`DESIGN-AUTH.md` §13.6):

1. **`smb.enabled` was false when the server started**, which is what the
   missing log line means. Then there is no published file to be stale. If
   you turn SMB on from the settings screen, the screen already tells you a
   restart is required; that restart is also what starts publishing.
2. **The render failed.** It logs at `error` with the reason, and the usual
   reason is the LAN-only bind check refusing a public address. Fix the cause
   and run `sc-server smb-sync` once to catch up; the next NT-hash change
   tries again on its own.
3. **The process was killed hard** in the second between a change and its
   publication. A normal shutdown flushes first, `kill -9` does not.

In all three cases the fallback is what this build always did: `sc-server
smb-sync`, or saving SMB settings from the admin screen, either of which
rewrites the file from the current database.

This is not only the link path. A password change, both TOTP toggles and the
self-service SMB toggles publish the same way now, so the recovery procedures
below are complete rather than needing an `smb-sync` chaser.

Recovery depends on **who** unlinks:

| unlinked by | SMB afterwards |
|---|---|
| the account's owner, from `/settings#security` | **re-derived immediately.** That screen takes the account password, the NT hash is written back in the same transaction, and the published file is rewritten right after it |
| an administrator, from the user list | **not restored.** An administrator has no plaintext password, so there is nothing to derive from. The response says `smb_nt_restored: false` and the UI says so before you confirm |

After an admin unlink, SMB for that account comes back when **either** of
these happens:

1. the owner changes their password, which re-derives the NT hash into the
   database and publishes it, or
2. a dedicated SMB password is set for the account.

Option 1 needs no follow-up command. Only the three exceptions listed above
put you back on `sc-server smb-sync`.

Option 2 has no user-facing API in this build (`DESIGN-AUTH.md` §13.6), so in
practice the answer is option 1: tell the user to change their password. It
does not have to be a *different* password, only a password change.

Both unlink paths also sign out every session the provider issued for that
account. Password sessions on the same account are left alone.

`oidc.smb_policy` exists and accepts one value, `block`. There is no
`require_separate` counterpart to `smb.totp_policy`'s, because nothing in this
build can create a dedicated SMB password.

### 13.8 Attaching identities

Two ways, and both exist on purpose:

- **The user's own**, at `/settings#security`. They confirm their account
  password, get sent to the provider, sign in there, and come back linked. The
  password is charged for the same reason enabling TOTP charges one: adding a
  permanent way into an account must not be possible from a live session
  alone.
- **An administrator's**, from the user list. Paste the provider's `sub` for
  that person. This is the recovery path for an account whose owner does not
  know its password, and it is why an administrator creating an SSO-only
  account must still hand over the initial password: without it the owner
  cannot self-serve the unlink later.

There is no automatic matching on email or username, and no account is created
by a login (`DESIGN-AUTH.md` §13.1).

### 13.9 Sign-in failures, by what the user sees

The callback puts a code in the URL rather than showing a JSON page. What the
user reports is a sentence; this maps it back.

| `?oidc_error=` | likely cause |
|---|---|
| `oidc.bad_state` | the flow cookie was missing or did not match. Plain HTTP (§13.3), a cookie-stripping proxy, a bookmarked callback URL, or the back button after a completed sign-in |
| `oidc.expired` | more than ten minutes between pressing the button and finishing at the provider |
| `oidc.not_linked` | the identity is not attached to any account here, **or** the account is disabled. Deliberately the same code; the audit log distinguishes them |
| `oidc.provider_unavailable` | discovery, JWKS, the token endpoint, or ID token verification failed. Check the server log, which carries the detail the wire does not |
| `oidc.access_denied` | the user declined, or the provider's own policy refused them |
| `oidc.disabled` | single sign-on was switched off between starting and finishing |
| `oidc.subject_already_linked` | that provider identity is attached to a different account here |
| `oidc.already_linked` | this account already has a different identity attached |
| `oidc.link_session_changed` | the browser signed out or switched accounts mid-link |

Relevant audit events: `auth.login` with `detail = "oidc"`,
`auth.login_failed` with `oidc_not_linked` / `disabled` /
`oidc_local_password_denied` / `dav_oidc_linked`, `auth.oidc_linked`,
`auth.oidc_unlinked`, and `auth.oidc_link_denied` with `bad_password`.

---

## 14. Before you hand this to anyone else

Three obligations attach to *distribution* and to *operating the service for
other people*. Running it privately, for yourself, triggers none of them, which
is why nothing below is enforced by a gate today.

### 14.1 AGPL §13 — the source offer

> …if you modify the Program, your modified version must prominently offer all
> users interacting with it remotely through a computer network an opportunity
> to receive the Corresponding Source of your version…

"Users interacting with it remotely" means anyone who reaches the web UI, not
just people you gave the binary to. So the moment a second person can log in,
the running UI has to carry a visible route to the source of *the exact build
they are talking to* — not to this repository, unless this repository is what
you deployed.

That offer is the footer line on the login screen — the one page every user of
a network service reaches — reading `AGPL-3.0-or-later · Source`. It sits
outside both `{#if}` branches in `web/src/routes/login/+page.svelte`, so the
credentials step and the two-factor step both carry it.

Its target is `SOURCE_URL`, a constant at the top of that same file, and it
points at this repository — correct only for a build *of* this repository.
**A modified deployment has to repoint it at its own source.** Leaving it is
worse than having no link: the offer then names a tree the service is not
running.

It is a constant rather than a configuration key on purpose. §13 binds whoever
modified the Program, and modifying it already means editing and rebuilding
this tree — a config knob would not remove the need to notice this line, it
would only move where you fail to notice it.

### 14.2 Attribution — `THIRD-PARTY-NOTICES.md`

The release binary statically links 348 Rust crates and embeds the built
frontend, webfont included, via `rust_embed`. MIT, BSD-2/3-Clause, ISC and
Apache-2.0 all require their notice to travel with the copy, and OFL-1.1
requires the font's copyright and licence to be distributed alongside the font
data. `THIRD-PARTY-NOTICES.md` at the repo root is that notice; the Dockerfile
copies it and `LICENSE` into the runtime image, so `docker cp` retrieves both
from a running container.

Regenerate it whenever a dependency is added, removed or bumped — a stale
notices file is a false statement, not merely an out-of-date one:

```sh
cargo metadata --format-version 1 --locked > .notices-meta.json
node scripts/gen-notices.mjs
rm .notices-meta.json
```

The script pools byte-identical licence texts, so the diff after a routine
version bump is small enough to read.

### 14.3 Trademark

The compatibility statement in `README.md` names Nextcloud, which is a
registered trademark of Nextcloud GmbH. It is written as a factual,
non-promotional interoperability claim and carries a disclaimer of
affiliation — the shape their trademark policy acknowledges. Two rules keep it
that way: the mark must not appear in this product's name or logo, and the
claim must stay factual. "Works with Nextcloud clients" is fine;
"Nextcloud-compatible cloud" as a tagline is not.
