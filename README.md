# Stowcloud

A file-management service that serves directories you already have.

Point it at a folder on your disk and it gives you a web UI, WebDAV, SMB, and
sync-client compatibility over exactly those files. No import step, no managed
storage layer, no separate copy. Rust backend, Svelte frontend, Linux/Docker
only.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/browse-dark.png">
  <img alt="The Stowcloud file browser listing a folder of images, with the folder rail on the left and the upload and new-folder actions in the toolbar" src="docs/screenshots/browse-light.png">
</picture>

Every screenshot on this page was taken from a `docker compose up` of the
published `ghcr.io/heavycaffeiner/stowcloud:core` image on a Rocky Linux 10
host, browsing real files on that host's disk. Nothing here is a mockup.

## Why it exists

Most self-hosted file servers want to own the storage: you upload into them,
and what is on disk afterwards is their business, not yours. That breaks the
moment something else needs the same files, whether that is Jellyfin scanning
a media library, `*arr` writing into it, rsync syncing it, or Samba serving
it.

Stowcloud treats the filesystem as the only source of truth. Its database is a
cache you can delete at any time and have rebuilt. Another program writing to
a shared folder is a supported, expected event, not corruption. A folder that
something else also writes to says so in the breadcrumb, so a destructive
action can warn before it runs.

## What it does

- **Browse, upload, move, copy, rename, delete**, with optimistic concurrency
  (`If-Match`, and a real conflict-resolution screen), a long-running job
  queue that survives a browser refresh, and a per-folder trash an
  administrator switches on.
- **Resumable uploads** (TUS) that survive a dropped connection, a closed tab,
  or a proxy that caps request bodies.
- **Search** by name across every folder the asking account can see.
- **Share links**: public, password-protected, expiring, download-capped, or
  upload-only (file drop).
- **Per-folder access grants**, down to a subpath, with permissions allowed or
  denied one at a time. The default is no access.
- **WebDAV** (RFC 4918 Class 2, including LOCK) and a **compatibility layer
  for Nextcloud apps**, so the existing Nextcloud desktop and mobile clients
  log in through Login Flow v2 and sync against this server unmodified.[^tm]
- **SMB** through a Samba sidecar, off by default, LAN-only by construction.
- **Single sign-on** via OIDC, alongside local accounts, TOTP, app passwords,
  and recovery codes.
- **Image previews** decoded in a `fork`ed, Landlock-confined worker that can
  neither `execve` nor reach the network. Sync clients render them as
  thumbnails; the web UI marks which files have one but does not yet show them
  inline.

### Search reaches across folders

Results come from every share the account has a grant on, so one query covers
what would otherwise be several places to look. Names only: content indexing
is a separate, opt-in thing.

![Search results for "2026" listing matches from two different shares above the current folder's own contents](docs/screenshots/search.png)

### A link is created once and shown once

The plaintext link exists in exactly one response and is never recoverable
afterwards, which is why the dialog says so out loud. Expiry, a password, and
a download cap are set when it is created; revoking it is permanent, and the
same link cannot be recreated.

![The share-link dialog immediately after creating a link, showing the one-time URL, a copy button, and the link's expiry](docs/screenshots/share-link.png)

Whoever opens that link gets this, and nothing else. No account, no other
folder, no way to tell that anything else exists on the server.

![The public share page a recipient sees: a title, the list of files in the shared folder, and a download button](docs/screenshots/share-public.png)

### An administrator decides who sees what

A grant names a share and, optionally, a subpath inside it. Name one and the
account sees only that subtree and cannot tell that its parent, or anything
else in the share, exists. With no grant at all an account sees nothing, which
is the default rather than a misconfiguration.

![The folder-permission dialog for an account, showing one grant scoped to a subpath with read and download allowed](docs/screenshots/folder-grants.png)

### Text and code are editable in place

Small text files open in a browser editor with syntax highlighting and save
back through the same optimistic-concurrency check as everything else, so two
people editing one file get a conflict screen rather than a silent overwrite.

![A Markdown file open in the built-in editor with line numbers and syntax highlighting](docs/screenshots/editor.png)

## Requirements

- Linux, kernel 5.6+ (`openat2`); 5.13+ for the Landlock sandbox
- Docker 20.10.10 or newer, because older seccomp profiles return `EPERM` for
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

`docker-compose.yml` is a commented reference deployment. Read it before
copying it. It pulls `ghcr.io/heavycaffeiner/stowcloud:core` (amd64 + arm64)
and, optionally, `:smb` (amd64 only), both published only after the same CI
run built and smoke-tested them. Pin to the `:core-<commit-sha>` tag once a
deployment matters. It runs read-only, as a non-root user, with all
capabilities dropped.

Which folders the server offers, and under what names, comes from a config
file (`--config <path>`, `[[shares]]` entries) or from the folder-share screen
in the admin UI. A server with neither has nothing to show.

On first boot the server prints a one-time setup token to its log and writes
it to `<data>/setup-token`. Open <http://127.0.0.1:8080/setup>, paste the
token, and create the administrator account.

![The first-run screen asking for the setup token, an administrator username, and a password](docs/screenshots/setup.png)

The token expires after 15 minutes and is destroyed permanently once an
administrator exists. There is no environment variable for the initial
password, deliberately: one passed that way is readable in `docker inspect`
and in the process list.

Put a reverse proxy in front of it. The compose file publishes to loopback
only for that reason.

## Building from source

```sh
cd web && npm ci && npm run build && cd ..          # frontend first
cargo build -p sc-server --release --features embed-ui
bash scripts/verify.sh                              # the gate CI runs
```

The frontend is embedded into the binary at compile time, so it has to be
built first. `embed-ui` is off by default precisely because a fresh checkout
has no `web/build` yet. The release image is a static musl build, and
`Dockerfile` does both stages.

## Documentation

[`docs/README.md`](docs/README.md) is the index, and it opens with the reading
order. Start with
[Architecture](docs/proposals/stowcloud-12-architecture.md) for the design
principles and crate layout, and
[Deployment](docs/proposals/stowcloud-13-deployment.md) for running it. Every
subsystem has a proposal written from what is built, not from what was
planned.

## Design principles

1. **The filesystem is the only source of truth.** The database is a cache you
   can delete and rebuild.
2. **A path is a kernel handle, not a string.** `openat2(RESOLVE_BENEATH)`
   removes TOCTOU by construction rather than by convention.
3. **A shared folder is not ours.** Other programs are assumed to be writing
   to the same directory at the same time.
4. **The compatibility layer does not invade the core.** Enforced by a CI
   gate, not by good intentions.
5. **The default is least privilege.** No user homes, no symlinks, SMB off, no
   inline content rendering.

## Status

All six milestones in the architecture proposal are reachable by a real
client. Still missing: Litmus conformance in CI, an automated sync-client
regression suite, and an external security review. Treat it accordingly. It
has not been audited by anyone outside this repository.

## License

Copyright (C) 2026 heavycaffeiner.

GNU Affero General Public License v3.0 or later, see [`LICENSE`](LICENSE).
Running a modified version as a network service obliges you to offer its
source to the people using it; this repository does not do that for you, and
the published images carry only a `org.opencontainers.image.source` label
pointing back here.

Contributions are accepted under that same licence and no other: inbound
equals outbound. There is no CLA and no copyright assignment, so contributors
keep their copyright and nobody, this repository's author included, ends up
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
