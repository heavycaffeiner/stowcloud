# Stowcloud

A file-management service that serves directories you already have.

Point it at a folder on your disk and it gives you a web UI, WebDAV, SMB, and
sync-client compatibility over exactly those files — no import step, no
managed storage layer, no separate copy. Rust backend, Svelte frontend,
Linux/Docker only.

## Why it exists

Most self-hosted file servers want to own the storage: you upload into them,
and what is on disk afterwards is their business, not yours. That breaks the
moment something else needs the same files — Jellyfin scanning a media
library, `*arr` writing into it, rsync syncing it, Samba serving it.

Stowcloud treats the filesystem as the only source of truth. Its database is
a cache you can delete at any time and have rebuilt. Another program writing
to a shared folder is a supported, expected event, not corruption.

## What it does

- **Browse, upload, move, copy, rename, delete** — with a trash, optimistic
  concurrency (`If-Match`, with a real conflict-resolution screen), and a
  long-running job queue that survives a browser refresh.
- **Resumable uploads** (TUS) that survive a dropped connection, a closed
  tab, or a proxy that caps request bodies.
- **Search** by name across shares, with results filtered by what the asking
  account may actually see.
- **Image thumbnails**, decoded in a `fork`ed, Landlock-confined worker that
  can neither `execve` nor reach the network. Text, code and archive
  listings preview in the browser.
- **Share links** — public, password-protected, expiring, download-capped,
  or upload-only (file drop).
- **Per-folder access grants** — an administrator decides which subtree each
  account gets, and with which permissions. The default is no access.
- **WebDAV** (RFC 4918 Class 2, including LOCK) and a **compatibility layer
  for Nextcloud apps** — the existing Nextcloud desktop and mobile clients
  log in via Login Flow v2 and sync against this server unmodified.[^tm]
- **SMB** through a Samba sidecar, off by default, LAN-only by construction.
- **Single sign-on** via OIDC, alongside local accounts, TOTP, app
  passwords, and recovery codes.

## Requirements

- Linux, kernel 5.6+ (`openat2`); 5.13+ for the Landlock sandbox
- Docker 20.10.10 or newer — older seccomp profiles return `EPERM` for
  `openat2` and force a security downgrade
- Roughly 30 MB of disk for the image and a data directory for the cache

Only Linux is supported at runtime. The code cross-compiles on other hosts,
but nothing else is a deployment target.

## Quick start

```sh
mkdir -p ./data ./secrets ./shares/photos
chown -R 1000:1000 ./data ./secrets
docker compose up -d
```

`docker-compose.yml` is a commented reference deployment — read it before
copying it, and replace the placeholder `image:` lines with your own
registry. It runs read-only, as a non-root user, with all capabilities
dropped.

On first boot the server prints a one-time setup token to its log and writes
it to `<data>/setup-token`. Open <http://127.0.0.1:8080/setup>, paste the
token, and create the administrator account. The token expires after 15
minutes and is destroyed permanently once an administrator exists; there is
no environment variable for the initial password, deliberately.

Put a reverse proxy in front of it. The compose file publishes to loopback
only for that reason.

## Building from source

```sh
cd web && npm ci && npm run build && cd ..          # frontend first
cargo build -p sc-server --release --features embed-ui
bash scripts/verify.sh                              # the gate CI runs
```

The frontend is embedded into the binary at compile time, so it has to be
built first — `embed-ui` is off by default precisely because a fresh
checkout has no `web/build` yet. The release image is a static musl build;
`Dockerfile` does both stages.

## Documentation

[`docs/README.md`](docs/README.md) is the index. Start with
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the design principles and
crate layout, [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for running it, and
[`docs/FEATURES.md`](docs/FEATURES.md) for the full feature inventory
including what is explicitly out of scope.

## Design principles

1. **The filesystem is the only source of truth.** The database is a cache
   you can delete and rebuild.
2. **A path is a kernel handle, not a string.** `openat2(RESOLVE_BENEATH)`
   removes TOCTOU by construction rather than by convention.
3. **A shared folder is not ours.** Other programs are assumed to be writing
   to the same directory at the same time.
4. **The compatibility layer does not invade the core.** Enforced by a CI
   gate, not by good intentions.
5. **The default is least privilege.** No user homes, no symlinks, SMB off,
   no inline content rendering.

## Status

All six milestones in `ARCHITECTURE.md` §14 are reachable by a real client.
Still missing: Litmus conformance in CI, an automated sync-client
regression suite, and an external security review. Treat it accordingly —
it has not been audited by anyone outside this repository.

## License

Copyright (C) 2026 heavycaffeiner.

GNU Affero General Public License v3.0 or later — see [`LICENSE`](LICENSE).
Running a modified version as a network service obliges you to offer its
source to the people using it; `docs/DEPLOYMENT.md` §14 is what that means in
practice, and what this repository does not do for you.

Contributions are accepted under that same licence and no other — inbound
equals outbound. There is no CLA and no copyright assignment: contributors
keep their copyright, and nobody, this repository's author included, ends up
holding a private right to relicense the result. The cost is that the licence
is now effectively permanent, because changing it would need every
contributor's agreement. That is the intended trade.

The binary statically links its Rust dependencies and embeds the built
frontend, so both are redistributed with it.
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md) carries their licences and
copyright notices, and the runtime image carries a copy at
`/THIRD-PARTY-NOTICES.md`.

[^tm]: Nextcloud is a registered trademark of Nextcloud GmbH. Stowcloud is not
    affiliated with, endorsed by, or sponsored by Nextcloud GmbH; the name is
    used only to state, factually, which clients interoperate.
