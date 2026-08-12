# Phase 11: SMB, OIDC, operational surface

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-14-smb-and-oidc.md` and
`docs/proposals/stowcloud-15-deployment.md`.

## Scope

Two small subsystems that both enforce a network rule where it cannot be
skipped, plus the operational surface: the filesystem gate, the syscall probe,
health reasons, and the image.

Depends on Phases 1, 3 and 5. Blocks Phase 13.

## Milestones

- **11a**: `internal/smb`: render, the bind rule, the escaping, the mask test.
- **11b**: `passdb.go` and `uid.go`.
- **11c**: `internal/oidc`: `dial.go`, `discovery.go`, the anchor pool and its
  refusal.
- **11d**: `authorize.go`, `exchange.go`, `jws.go`, `link.go`.
- **11e**: the filesystem gate and the `statfs` classification.
- **11f**: the `openat2` availability probe that distinguishes `EPERM` from
  `ENOSYS`.
- **11g**: health reasons, including the hardening state.
- **11h**: the `Dockerfile` builder stage and the compose file.

11a and 11c are independent.

## Traps

- **The SMB bind list is already derived from the host's own devices.** Carry
  that over; do not reimplement it as a CIDR table. An earlier draft of the
  proposal described the current implementation wrongly in every clause.
- **The nine-entry private CIDR list goes into `hosts allow` only**, never into
  `interfaces`, and only when the operator pinned `smb.interfaces` or the
  container namespace shows nothing but veth devices.
- **The core renders loopback-only when unpinned.** That baseline is today's
  default, not a new idea.
- **`bind interfaces only = yes` accompanies every interface list**, or the list
  is advice to `smbd` rather than a restriction.
- **The generated file is an outbound trust boundary.** A share name or path
  that cannot be represented safely in `smb.conf` syntax is a refusal, not an
  escape attempt. A share name is not where you discover Samba's parser has
  opinions.
- **The passdb is a revocation path.** Phase 3 built the sink; this phase makes
  the file its output. The test is the file's contents after each of the six
  paths.
- **The OIDC address rule is enforced in `net.Dialer.Control`**, at dial time,
  on the address the socket will actually use. A resolve-then-check leaves a
  rebinding window, which is exactly what the Rust tree wraps the resolver to
  close.
- **Redirects are not followed on the back channel.** The token endpoint is
  where discovery said it is.
- **The certificate pool is explicit and an empty one is a refusal to start.**
  Confirm what the shipped image actually carries; if it carries nothing, embed
  a PEM set at build time, which restores the Rust behaviour rather than
  reinventing it.
- **The ID token's algorithm is chosen by the key, never taken from the
  header.** That closes `alg: none` and algorithm confusion in one check.
- **Link-only.** The provider authenticates and never creates an account.
- **A share on overlayfs is refused at registration.** Accepting it produces a
  deployment that looks healthy and loses file identity months later, after the
  sync clients have written the wrong thing into their journals.
- **A blocked `openat2` is a refusal to start under every policy, `off`
  included.** There is no per-component fallback and none is being built: it
  would be the normalise-then-open shape S1 exists to refuse.
- **`degraded` is not `unhealthy`.** The healthcheck exits zero for both,
  because a degraded server is a configuration state a restart does not fix.

## Done when

- The gate is green, including `-race`.
- The render refuses a global bind without `allow_public_bind`, and refuses a
  share name it cannot represent safely.
- A dial-guard test refuses a hostname that resolves to a private address at
  connect time.
- `GET /api/health` names every degradation it can report, including the
  hardening state.
- The image builds and its smoke test passes on both architectures.
