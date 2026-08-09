# Deployment: Linux, Docker, Coexistence - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-03                       |
| Status     | Implemented                      |
| Reviewers  |                                  |

---

## 1. Summary

Containerisation is not a convenience here, it is a **constraint**, and this
specifies how that constraint feeds back into the design: which filesystems a
share may live on, why the container's seccomp profile can silently downgrade
security, how uid and gid work on a directory somebody else also writes, and
what actually runs in production.

SMB and single sign-on have their own proposals; everything else about running
this is here.

## 2. Background & Motivation

The container premise adds real constraints, none of them optional: Docker's
default seccomp profile can block `openat2`, overlayfs breaks inode stability,
bind-mount boundaries produce `EXDEV`, and the kernel's inotify watch limit
cannot be raised from inside a container.

Underneath all of them is the sharing premise — a share is a directory a media
server, a downloader and rsync are also writing to. Every decision below is
either about the container or about not breaking those neighbours.

## 3. Goals & Non-Goals

### 3.1 Goals

- [x] A share on a filesystem that cannot support the design is refused at
      registration, not discovered later.
- [x] A security downgrade forced by the runtime is *loud*, never silent.
- [x] Ownership and permissions on shared directories survive us.
- [x] An image small enough to be uninteresting, running as no one.

### 3.2 Non-Goals

- [ ] `chown -R` on a mounted volume. Many images do it at startup; on a
      directory shared with other services it cuts off their access and is
      hard to reverse. Our entrypoint never touches ownership of user data.
- [ ] Running as root and dropping privileges. The container starts
      unprivileged, which is also why it needs no setuid capabilities.
- [ ] Bundling `ffmpeg`. It would end the distroless base for a non-goal
      feature.

## 4. Technical Design

### 4.1 Image layout

| Image | Base | Contents |
|---|---|---|
| `sc:core` | distroless static | one statically-linked musl binary, frontend embedded |
| `sc:smb` | alpine | `smbd` plus the config-sync loop — a sidecar |

The core image has no shell, no package manager, and no `chown`. That is the
point, and it is also why the health check is the same binary re-invoked with
a different argv rather than a shell probe, and why the *host* must
pre-create bind-mount directories with the right owner.

Two images rather than one so the majority who do not need SMB do not carry
Samba's weight or its root process.

### 4.2 Syscall availability — the seccomp reality

Docker's default seccomp profile is an **allow-list returning `EPERM`**, not
`ENOSYS`. That defeats the standard "fall back on `ENOSYS`" pattern outright:
a missing `openat2` looks like a permission error, not an absent feature.

So the probe distinguishes them and says which happened, because the operator
action differs — an old kernel needs an upgrade, a blocked syscall needs a
profile. A downgrade to the per-component fallback is logged loudly rather
than absorbed silently, since it is a real weakening of the path-resolution
guarantee.

### 4.3 The filesystem gate

`statfs` classifies a share's filesystem at registration, and one is refused
outright:

| Filesystem | Verdict |
|---|---|
| **overlayfs** | **refused** — a container's writable layer has unstable inodes across restarts and inotify misses lower-layer changes; the whole file-identity design breaks |
| ext4, btrfs, XFS, ZFS, f2fs | full support; btrfs/XFS additionally get reflink copies |
| tmpfs | allowed with a warning — data is lost on restart |
| FUSE (mergerfs, rclone, s3fs) | identity and `btime` unknown → forces path-based ids and periodic rescan |
| NFS | inotify cannot see server-side changes → forces a shorter rescan interval |
| CIFS/SMB | as NFS, plus name restrictions; strong warning |
| squashfs | read-only registration |

Refusing at registration is the design decision. The alternative — accept
anything and degrade quietly — produces a deployment that looks healthy and
loses file identity months later.

### 4.4 Mount boundaries

A bind mount inside a share is a mount boundary, and `rename` across one is
`EXDEV`. That is not an error to surface raw: the operation the user asked for
is still possible, just as copy-then-unlink, and it is *slower*. So it becomes
advance notice before the user commits, rather than a failure after.

**SELinux**: shared directories use the lowercase shared label, never the
uppercase exclusive one, which would cut off the other services using the same
tree. Startup warns when a share is configured and SELinux is enforcing,
because that is the likeliest cause of an `EACCES` a neighbour hits.

### 4.5 Change detection in a container

`fs.inotify.max_user_watches` cannot be raised from inside a container and a
watch is per directory, so a large tree cannot be fully watched. The strategy
is layered rather than absolute — a watched hot set, lazy revalidation, and a
periodic rescan — with a queue overflow bumping the share generation to
invalidate everything at once instead of trying to replay what was missed.

What survives is that a stale answer is always detectable and self-correcting.

### 4.6 uid, gid, and not breaking the neighbours

- The container runs as an unprivileged uid from the start, so no setuid
  capability is ever needed.
- Groups shared with other services go in `group_add`.
- Creation modes are set **explicitly per share**, never left to umask: umask
  can only mask bits, not set them, so it cannot express "group-readable so
  the media server can read it".
- Replacing an existing file transplants its mode, ownership and extended
  attributes onto the replacement before the rename — otherwise an atomic
  replace silently strips whatever access a neighbour had a moment ago.

### 4.7 Behind a CDN or reverse proxy

The trusted-proxy CIDR list is what makes a forwarded client address
trustworthy; without it every request folds onto one address and the login
rate limiter, the API limiter and the audit log all become useless together.
It is applied once, outside every mount, so no surface can disagree about who
the client is.

The 100-second proxy window is why long operations stream a first byte
promptly, and why a large cross-mount move is refused with a clear error
rather than left to time out.

### 4.8 Reaching the server on the LAN

A proxy answers for the public name. The LAN address is the other half, and it
used to fail twice over.

**The host allowlist.** `app_hosts` ships as loopback only, and the `Host`
header carries whatever literal the client dialled, so a request to
`192.168.0.50` was answered `421`. The bind address was injected into the list
to cover this, but only when it named a specific address, and §5-1's compose
reference mandates `SC_BIND=0.0.0.0:8080`, which does not. The injection
therefore never fired on the one shape everybody runs. A private IP literal
(RFC 1918, loopback, link-local, ULA) is now accepted without being listed:
DNS rebinding is carried out with a *name*, so an address literal is not the
thing the guard is defending against.

**The cookie.** The session cookie is `__Host-`-prefixed, which by definition
requires `Secure`, so a browser keeps it only over HTTPS or plain
`http://localhost`. Fixing the allowlist alone produces a page that loads and
then refuses to stay logged in, which is worse than a `421` because nothing
reports it. `tls_bind` puts up an HTTPS listener with a certificate the server
writes for itself into `<data_dir>/tls`, regenerating it whenever the names and
addresses it covers stop matching (a DHCP lease change otherwise produces a name
mismatch, which a browser refuses outright rather than offering the
click-through it gives an untrusted issuer).

Self-signed means one interstitial per browser, accepted deliberately: the
alternative on a private address is not a trusted certificate, it is no HTTPS
and therefore no login.

The covered addresses are `localhost`, the loopback pair, every `app_hosts`
entry, and the address the routing table picks for reaching the LAN. In a
bridged container that last one is the 172.x bridge address, not the one an
operator browses to, so a compose deployment names its host's LAN address in
`app_hosts`. The regeneration check is additive on purpose: it reissues when
something is missing, never because something went away, so a bridge address
that changes on recreation does not invalidate the exception every browser
stored.

The two listeners stay separate. The proxy talks plaintext to `bind` over
loopback or the bridge, and browsers on the LAN use `tls_bind`; folding them
into one would force the proxy onto an upstream certificate it has no reason to
verify. Both serve the same router through the same connect-info path, so the
peer address reaches the trusted-proxy layer identically and no IP-keyed control
behaves differently depending on which socket a request arrived on.

CSRF does *not* take the same shortcut the host guard does. `SameSite=Lax` is
computed from the host alone, so a neighbouring service on the same NAS at
another port is same-site and its cross-origin writes do carry the session
cookie. A private-LAN `Origin` is therefore accepted only when its scheme and
port name a listener this server actually answers on.

## 5. API Design

### 5-1. Deployment surface

```
docker compose up -d          # the reference deployment
sc-server serve --config …    # what the container runs
sc-server smb-sync            # publish Samba's files
sc-server gc                  # housekeeping: incremental vacuum
sc-server index build|merge|status
sc-server masterkey rotate
sc-server caps                # kernel capability probe
sc-server healthcheck         # what HEALTHCHECK runs
```

One-time host setup, and the reason for each: `./data`, `./secrets` and
`./smbcfg` must exist and be owned by the runtime uid, because the image
cannot chown them; share roots must be `0775`, because Samba checks the group
ACE (`stowcloud-1-smb.md` §4.4).

The master key lives **outside** the data directory, so a backup of the data
cannot also carry the key that decrypts it.

### 5-2. Failure modes an operator will meet

| Symptom | Cause |
|---|---|
| share refused at startup | overlayfs, or a read-only filesystem |
| `openat2` reported `EPERM` | the runtime's seccomp profile, not the kernel |
| `EACCES` from a neighbouring service | exclusive SELinux label on a shared mount |
| first write fails `EACCES` | a named volume created root-owned; use a bind mount you own |
| SMB renders but nobody can log in | see `stowcloud-1-smb.md` §4.4 |
| a revoked grant still works over SMB | the publisher is not armed; SMB must be enabled at startup |
| `421` from a LAN address | pre-§4.8 build: `app_hosts` is loopback-only and the bind is `0.0.0.0` |
| login succeeds, next request is anonymous | plain HTTP on a non-loopback host; the `__Host-` cookie was dropped (§4.8) |
| certificate warning on every LAN visit | expected: `tls_bind`'s certificate is self-signed and nothing issued it |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Duration | Owner |
|---|---|---|---|
| Phase 1 | Two images, distroless core, static musl build | done | heavycaffeiner |
| Phase 2 | Capability probe, filesystem gate, downgrade warnings | done | heavycaffeiner |
| Phase 3 | uid/gid model, mode policy, ownership transplant | done | heavycaffeiner |
| Phase 4 | Watch tiering and rescan | done | heavycaffeiner |
| Phase 5 | Reference compose, health check, testbed stack | done | heavycaffeiner |

### 6-2. Dependencies

- Linux ≥ 5.6, Docker ≥ 20.10.10 for `openat2`/`faccessat2` in the default
  profile. Older runtimes need a supplied profile.
- `cargo auditable` so the shipped binary carries its own dependency manifest.

## 7. Operational notes

**Production runs the container inside a Linux guest**, because §2's kernel
features are what the container needs and the host does not provide them. The
guest stays deliberately minimal — no compiler on it; the build gets its
toolchain from the builder image instead.

**Verification before release** is `scripts/verify.sh`: build, test, clippy at
`-D warnings`, the compat-isolation grep, the no-Korean-in-server-code gate,
the bind-site check, and the feature-stripped build. It runs against the
committed tree, because an unstaged file can hide a broken HEAD.

**Licensing** matters at the moment this is handed to anyone else: the source
offer, the third-party notice file that must travel with the image rather than
only the repository, and trademark. Those obligations attach to distribution,
not to development, which is why they are recorded next to the deployment
procedure.

## 8. References

- `Dockerfile`, `Dockerfile.smb`, `docker-compose.yml`, `deploy/testbed/`
- `scripts/verify.sh`, `scripts/deploy.sh`, `scripts/smoke.sh`
- `stowcloud-1-smb.md`, `stowcloud-0-oidc-login.md`,
  `stowcloud-11-footprint.md` (the hardware floor this deploys onto),
  `stowcloud-12-architecture.md` (the principles this serves)
