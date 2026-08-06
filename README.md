# Stowcloud

[![verify](https://github.com/heavycaffeiner/stowcloud/actions/workflows/verify.yml/badge.svg)](https://github.com/heavycaffeiner/stowcloud/actions/workflows/verify.yml)
[![docker](https://github.com/heavycaffeiner/stowcloud/actions/workflows/docker.yml/badge.svg)](https://github.com/heavycaffeiner/stowcloud/actions/workflows/docker.yml)

**A file-management service that serves directories you already have.**

Point it at a folder on your disk and it gives you a web UI, WebDAV, SMB, and
sync-client compatibility over exactly those files. No import step, no managed
storage layer, no separate copy. Rust backend, Svelte frontend, Linux and
Docker only.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/browse-dark.png">
  <img alt="The Stowcloud file browser listing a folder of images: a navigation rail, the folder list, a breadcrumb marking the folder as shared with other services, and the file table" src="docs/screenshots/browse-light.png">
</picture>

> Every screenshot here comes from `docker compose up` of
> `ghcr.io/heavycaffeiner/stowcloud:core` at revision `d82d880`, the image
> this repository's CI built and published, running on a Rocky Linux 10 host
> and browsing real files on its disk. Nothing is mocked and nothing was
> staged in a design tool.

## Why it exists

Most self-hosted file servers want to own the storage: you upload into them,
and what is on disk afterwards is their business, not yours. That breaks the
moment something else needs the same files, whether that is Jellyfin scanning
a media library, `*arr` writing into it, rsync syncing it, or Samba serving
it.

Stowcloud treats the filesystem as the only source of truth. Its database is a
cache you can delete at any time and have rebuilt. Another program writing to
a shared folder is a supported, expected event, not corruption, and a folder
that something else also writes to says so in the breadcrumb so a destructive
action can warn before it runs.

## What it does

| | |
|---|---|
| **Browse and organise** | Upload, move, copy, rename, delete, with optimistic concurrency (`If-Match`) and a real conflict screen instead of a silent overwrite. Long-running jobs survive a browser refresh. |
| **Resumable uploads** | TUS, so a dropped connection, a closed tab, or a proxy that caps request bodies does not restart the transfer. |
| **Search** | By name, across every folder the asking account can see. |
| **Share links** | Public, password-protected, expiring, download-capped, or upload-only. |
| **Per-folder grants** | Down to a subpath, with permissions allowed or denied one at a time. The default is no access. |
| **WebDAV and sync clients** | RFC 4918 Class 2 including LOCK, plus a compatibility layer that existing Nextcloud desktop and mobile clients log into unmodified.[^tm] |
| **SMB** | Through a Samba sidecar, off by default, LAN-only by construction. |
| **Accounts** | Local passwords, OIDC single sign-on, TOTP, app passwords, recovery codes. |

Image previews are decoded in a `fork`ed, Landlock-confined worker that can
neither `execve` nor reach the network. Sync clients render them as
thumbnails; the web UI marks which files have one but does not yet show them
inline.

### One tree, however deep it goes

The folder pane walks the same directories the shares point at. No import
ever happened, so what you see here is what `ls` sees.

![The file browser with the folder tree open, showing nested folders under two different shares](docs/screenshots/tree.png)

### Search reaches across folders

Results come from every share the account has a grant on, so one query covers
what would otherwise be several places to look. Names only: content indexing
is separate and opt-in.

![Search results for "2026" listing matches from two shares above the current folder's own contents](docs/screenshots/search.png)

### A link is created once, and shown once

The plaintext link exists in exactly one response and is never recoverable
afterwards, which is why the dialog says so out loud. Expiry, a password and a
download cap are chosen when it is created; revoking is permanent, and the
same link cannot be made again.

![The share-link dialog just after creating a link: the one-time URL, a copy button, and the link's expiry](docs/screenshots/share-link.png)

Whoever opens it gets this, and nothing else. No account, no other folder, no
way to tell that anything else exists on the server.

![The public share page a recipient sees: a title, the files in the shared folder, and a download button](docs/screenshots/share-public.png)

### An administrator decides who sees what

A grant names a share and, optionally, a subpath inside it. Name one and the
account sees only that subtree and cannot tell that its parent, or anything
else in the share, exists. With no grant at all an account sees nothing, which
is the default rather than a misconfiguration.

![The folder-permission dialog for an account, showing one grant scoped to a subpath with read and download allowed](docs/screenshots/folder-grants.png)

### Text and code are editable in place

Small text files open in a browser editor with syntax highlighting and save
back through the same optimistic-concurrency check as everything else, so two
people editing one file get a conflict screen rather than a lost edit.

![A Markdown file open in the built-in editor with line numbers and syntax highlighting](docs/screenshots/editor.png)

### Nothing is deleted in a hurry

An administrator turns the trash on per folder. Until then a delete is a
delete, which is stated rather than assumed.

![The trash listing three deleted items with their sizes and deletion times, and restore and purge actions](docs/screenshots/trash.png)

## Requirements

- Linux, kernel 5.6+ (`openat2`); 5.13+ for the Landlock sandbox
- Docker 20.10.10 or newer, because older seccomp profiles return `EPERM` for
  `openat2` and force a security downgrade
- Roughly 30 MB of disk for the image, and a data directory for the cache

Only Linux is supported at runtime. The code cross-compiles on other hosts,
but nothing else is a deployment target.

## Quick start

```sh
mkdir -p ./data ./secrets ./shares/photos
chown -R 1000:1000 ./data ./secrets
docker compose up -d
```

`docker-compose.yml` is a commented reference deployment. Read it before
copying it. It pulls `ghcr.io/heavycaffeiner/stowcloud:core` (amd64 and arm64)
and, optionally, `:smb` (amd64 only), both published only after the same CI
run built and smoke-tested them. Pin to the `:core-<commit-sha>` tag once a
deployment matters. It runs read-only, as a non-root user, with all
capabilities dropped.

Which folders the server offers, and under what names, comes from
`<data_dir>/sc.toml` (or `--config <path>`), or from the folder-share screen
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

<details>
<summary><b>Building from source</b></summary>

```sh
cd web && npm ci && npm run build && cd ..          # frontend first
cargo build -p sc-server --release --features embed-ui
bash scripts/verify.sh                              # the gate CI runs
```

The frontend is embedded into the binary at compile time, so it has to be
built first. `embed-ui` is off by default precisely because a fresh checkout
has no `web/build` yet. The release image is a static musl build, and
`Dockerfile` does both stages.

</details>

<details>
<summary><b>Licence, and what it obliges</b></summary>

Copyright (C) 2026 heavycaffeiner.

GNU Affero General Public License v3.0 or later, see [`LICENSE`](LICENSE).
Running a modified version as a network service obliges you to offer its
source to the people using it. This repository does not do that for you; the
published images carry an `org.opencontainers.image.source` label pointing
back here and nothing more.

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

</details>

## Status

All six milestones in the architecture proposal are reachable by a real
client. Still missing: Litmus conformance in CI, an automated sync-client
regression suite, and an external security review. Treat it accordingly. It
has not been audited by anyone outside this repository.

[^tm]: Nextcloud is a registered trademark of Nextcloud GmbH. Stowcloud is not
    affiliated with, endorsed by, or sponsored by Nextcloud GmbH; the name is
    used only to state, factually, which clients interoperate.
