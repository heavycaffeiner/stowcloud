# Deployment and operations - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

What the container premise forces, and what the port keeps of it: the two
images, the syscall-availability probe that has to tell a blocked call from an
absent one, the filesystem gate that refuses a share rather than degrading
quietly, the uid contract, the proxy contract, and the single TLS socket.

Almost none of this changes with the language. It is written down here because
it is not derivable from any other document in this directory, and deleting the
Rust-era proposals without it would lose the operational half of the product.

## 2. Background & Motivation

The container premise adds constraints, none of them optional:

- Docker's default seccomp profile can block `openat2`.
- overlayfs breaks inode stability.
- bind-mount boundaries produce `EXDEV` inside what looks like one share.
- `fs.inotify.max_user_watches` cannot be raised from inside a container.

Underneath all four is the sharing premise: a share is a directory a media
server, a downloader and rsync are also writing to. Every decision here is
either about the container or about not breaking those neighbours, which is
principle 3 and stance S6 in
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.2 and §2.5.

What the Go port changes is the builder stage and nothing else. The runtime
image, the sidecar, the uid contract and the operational surface are the same,
which is worth stating because a rewrite is exactly when an unrelated
operational property gets dropped by accident.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] A share on a filesystem that cannot support the design refused at
      registration, not discovered months later.
- [ ] A security downgrade forced by the runtime reported loudly, never
      absorbed.
- [ ] Ownership and permissions on shared directories surviving every write.
- [ ] An image small enough to be uninteresting, running as no one.
- [ ] One socket, always TLS, with no plaintext port to publish by mistake.
- [ ] A client address that is trustworthy behind a proxy, resolved once for
      every mount.
- [ ] Change detection that degrades rather than fails when the kernel's watch
      limit is reached.

### 3.2 Non-Goals

- [ ] **`chown -R` on a mounted volume.** Many images do it at startup; on a
      directory shared with other services it cuts off their access and is hard
      to reverse. The entrypoint never touches ownership of user data, and the
      distroless image has no `chown` to do it with even if someone tried.
- [ ] **Running as root and dropping privileges.** The container starts
      unprivileged, which is also why it needs no setuid capability.
- [ ] **Bundling `ffmpeg`.** It would end the distroless base for a feature
      that is a non-goal in [`12`](stowcloud-12-preview.md) §3.2.
- [ ] A plaintext listener, anywhere, for anything. §4.7 gives the reason,
      which is not "TLS is good" but "which port is safe to expose stops being
      a question anyone has to answer correctly".
- [ ] Kubernetes manifests, Helm charts, or an operator. Compose is the
      supported shape.

## 4. Technical Design

### 4.1 Image layout

| Image | Base | Architectures | Contents |
|---|---|---|---|
| core | distroless static | amd64, arm64 | one statically linked Go binary, frontend embedded |
| smb | alpine | amd64, arm64 | `smbd` plus the config-sync loop, a sidecar |

The core image has no shell, no package manager and no `chown`. That is the
point, and it has three consequences that are easy to lose:

1. **The health check is the same binary re-invoked with a different argv**,
   because nothing else in the image can run an exec-form probe. This is
   already the shape the Rust build uses and it generalises to the four
   subcommands in [`2`](stowcloud-2-gate-and-toolchain.md) §5-1.
2. **The host must pre-create bind-mount directories with the right owner.**
   Docker creates a missing one owned by root, and nothing in the container can
   fix it afterwards. This is documented at the point an operator meets it, in
   the compose file and the readme, not only here.
3. **Two images rather than one**, so the majority who do not need SMB carry
   neither Samba's weight nor its root process.

Both tags are manifest lists, and neither architecture is published on the
strength of a cross-build alone. Each is built on its own runner, the resulting
container is started, and the same smoke test runs against it. The reason is
recorded: arm64 was previously a QEMU cross-build that cost roughly ten times
the amd64 build and could not be executed at all on an emulating host, so the
arm64 image shipped having only ever been compiled.

Base images stay pinned by **per-architecture** manifest digest rather than by
index, because passing the wrong digest produces an image of the wrong
architecture rather than an error. Every build therefore asserts the
architecture of what it just built before trusting it.

**What the Go port changes here.** The builder stage loses the Rust toolchain,
`musl-tools`, `pkg-config`, the target-selection dance and the four `CC_*` and
`AR_*` variables, and becomes a `go build` with `CGO_ENABLED=0`. The SBOM
changes mechanism rather than disappearing: Go embeds its module graph in the
binary by default, so the wrapper tool and its pinned version go away and the
supply-chain workflow reads the binary instead.

### 4.2 Syscall availability, and the seccomp reality

Docker's default seccomp profile is an **allow-list returning `EPERM`**, not
`ENOSYS`. That defeats the standard "fall back on `ENOSYS`" pattern outright: a
blocked `openat2` looks like a permission error rather than an absent feature.

So the probe distinguishes them and reports which happened, because the
operator action differs: an old kernel needs an upgrade and a blocked syscall
needs a profile change. A downgrade to a weaker path-resolution mechanism is
logged loudly, because it is a real weakening of the guarantee the whole
security model rests on.

This is stance S3, and it is the same stance D3 turns into a startup refusal
for Landlock and seccomp. The difference is deliberate: `openat2` being blocked
is a refusal to serve at all under `hardening = "required"`, because there is
no safe fallback for principle 2, while Landlock being unavailable is a
degradation the operator may accept.

### 4.3 The filesystem gate

`statfs` classifies a share's filesystem at registration, and one is refused
outright:

| Filesystem | Verdict |
|---|---|
| **overlayfs** | **refused.** A container's writable layer has unstable inodes across restarts and inotify misses lower-layer changes, so the whole file-identity design breaks, including the derived node id in [`5`](stowcloud-5-store-and-schema.md) §4.5 |
| ext4, btrfs, XFS, ZFS, f2fs | full support; btrfs and XFS additionally get reflink copies |
| tmpfs | allowed with a warning: data is lost on restart |
| FUSE (mergerfs, rclone, s3fs) | identity and btime unknown, so path-based ids and a periodic rescan |
| NFS | inotify cannot see server-side changes, so a shorter rescan interval |
| CIFS or SMB | as NFS, plus name restrictions, plus a strong warning |
| squashfs | classified but not special-cased: registration succeeds and the kernel refuses every write with `EROFS`, which surfaces as a 403 |

**Refusing at registration is the design decision** (S4). The alternative,
accepting anything and degrading quietly, produces a deployment that looks
healthy and loses file identity months later, at which point the sync clients
have already written the wrong thing into their journals.

The squashfs row is the one honest gap: the answer is right and the diagnosis
arrives per request instead of once at startup.

### 4.4 Mount boundaries and labels

A bind mount inside a share is a mount boundary, and a rename across one is
`EXDEV`. That is not an error to surface raw: the operation is still possible
as copy-then-unlink, it is merely slower. So it becomes advance notice before
the user commits rather than a failure after, which is why
[`7`](stowcloud-7-core-domain.md) §5-2 has `ErrCrossShare` carrying which half
completed rather than a bare failure.

**SELinux.** Shared directories take the shared label, never the exclusive one,
which would cut off the other services using the same tree. Startup warns when
a share is configured and SELinux is enforcing, because that is the likeliest
cause of an `EACCES` a neighbour hits and the least likely one an operator
looks at first.

### 4.5 Change detection in a container

`fs.inotify.max_user_watches` cannot be raised from inside a container and a
watch is per directory, so a large tree cannot be fully watched. The strategy
is layered rather than absolute: a watched hot set, lazy revalidation, and a
periodic rescan. A queue overflow bumps the share generation to invalidate
everything at once rather than trying to replay what was missed.

What survives every degradation is that **a stale answer is always detectable
and self-correcting**. That property, not the watch count, is what the design
promises.

### 4.6 uid, gid, and not breaking the neighbours

- The container runs as an unprivileged uid from the start, so no setuid
  capability is ever needed and `cap_drop: ALL` costs nothing.
- Groups shared with other services go in `group_add`.
- **Creation modes are set explicitly per share, never left to umask.** A umask
  can only mask bits, not set them, so it cannot express "group-readable so the
  media server can read it". This is why
  [`3`](stowcloud-3-vfs-and-paths.md) §4.3.5 applies the configured mode with
  an explicit `fchmod` after create rather than relying on the open mode.
- **Replacing an existing file transplants its mode and ownership onto the
  replacement before the rename.** Otherwise an atomic replace silently strips
  whatever access a neighbour had a moment earlier, which is F7.

### 4.7 Behind a proxy, and the single socket

**The trusted-proxy CIDR list is what makes a forwarded client address
trustworthy.** Without it every request folds onto one address and the login
rate limiter, the API limiter and the audit log become useless together, in a
way that looks like nothing is wrong. It is applied once, outside every mount,
so no surface can disagree about who the client is
([`8`](stowcloud-8-http-and-api.md) §4.3.1).

**The proxy's own timeout is a design input.** A hundred-second window is
typical, which is why long operations stream a first byte promptly and why a
large cross-mount move is refused with a clear error rather than left to time
out into a gateway error the operator has to guess at.

**There is no plaintext listener**, on any interface, for any caller. One
socket, always TLS, with the certificate generated into the data directory on
first run.

The reason is not that TLS is good. It is that "which port is safe to expose"
otherwise becomes a question an operator has to keep answering correctly, and
this repository's own compose file answered it wrong once already. A reverse
proxy pays one line for the alternative, skipping verification against a
certificate it has no reason to verify over loopback, and the class of accident
where an internal port ends up published disappears. The session cookie is
`Secure`, so a plaintext origin would make login fail silently rather than
loudly, which is the other half of the argument
([`6`](stowcloud-6-auth-and-acl.md) §4.3.4).

### 4.8 Health, and what `degraded` means

`GET /api/health` answers `ok` or `degraded`, and the healthcheck subcommand
exits zero for both. A degraded server is a configuration state, not something
a container restart fixes, so mapping it to unhealthy would make the runtime
restart-loop a problem forever without resolving it. Exit one means the server
did not answer at all, which is the one condition a restart can plausibly help.

What reports `degraded`, each naming itself: a rejected share, a failed SMB
bind, a tripped database-size guard, an unavailable preview pool, and, new in
this port, hardening applied only partially under `hardening = "preferred"`
(D3). That last one is F2 closed: today the same condition produces one line in
a startup log and nothing an operator can query.

## 5. API Design

### 5-1. New / Modified

No wire surface of its own beyond the health response, which is unchanged in
shape and gains the hardening field:

```json
{
  "status": "degraded",
  "reasons": [
    {"kind": "hardening", "detail": "landlock_unavailable"},
    {"kind": "share_rejected", "detail": "overlayfs"}
  ]
}
```

`reasons` carries kinds, not sentences, for the same reason every other refusal
does (D15).

### 5-2. Error Handling

| Condition | Effect |
|---|---|
| share on overlayfs | registration refused, named, startup continues without that share |
| `openat2` blocked by a seccomp profile | refusal to start under `required`; a loud downgrade otherwise |
| Landlock or seccomp unavailable | refusal to start under `required`; `degraded` under `preferred` |
| data directory not writable by the runtime uid | refusal to start, naming the path and the uid |
| certificate generation fails | refusal to start; there is no plaintext fallback to degrade into |
| inotify watch limit reached | `degraded`, and the rescan interval shortens |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 11e | the filesystem gate and the `statfs` classification | S | Phase 1 | heavycaffeiner |
| Phase 11f | the `openat2` availability probe that distinguishes `EPERM` from `ENOSYS` | S | Phase 1 | heavycaffeiner |
| Phase 11g | health reasons, including the hardening state | S | Phase 5, Phase 1 | heavycaffeiner |
| Phase 11h | the `Dockerfile` builder stage and the compose file | S | Phase 5 | heavycaffeiner |

These sit in Phase 11 because they are small and none of them blocks another
phase. The gate and the probe belong to Phase 1's subject matter and are listed
here because their consequence is operational rather than architectural.

### 6-2. Dependencies

No modules beyond `golang.org/x/sys/unix`, which Phase 1 already brings for
`Statfs` and the availability probe.

**Non-code**: the Rocky guest for anything involving a real kernel, and Docker
for anything involving the image. Neither is available on the Windows
development host, which is the same constraint
[`2`](stowcloud-2-gate-and-toolchain.md) §4.3.3 records for the test suite.

## 7. References

- `Dockerfile`, `Dockerfile.smb`, `docker-compose.yml`: the images and the
  compose shape this keeps, and the builder stage §4.1 replaces.
- `.github/workflows/docker.yml`: the per-architecture native build and the
  smoke test §4.1 describes.
- `crates/sc-server/src/storage_class.rs`, `diagnostics.rs`: the filesystem
  classification and the health reporting this translates.
- `crates/sc-vfs/src/caps.rs`: the syscall availability probing §4.2 is about.
- `crates/sc-server/src/main.rs`: the healthcheck subcommand and its exit-code
  contract.
- `README.md`, the reverse-proxy section: the operator-facing form of §4.7.
