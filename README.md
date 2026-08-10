# Stowcloud

[![verify](https://github.com/heavycaffeiner/stowcloud/actions/workflows/verify.yml/badge.svg)](https://github.com/heavycaffeiner/stowcloud/actions/workflows/verify.yml)
[![docker](https://github.com/heavycaffeiner/stowcloud/actions/workflows/docker.yml/badge.svg)](https://github.com/heavycaffeiner/stowcloud/actions/workflows/docker.yml)

**English** | [한국어](README.ko.md)

**Stowcloud puts a web interface on folders you already have, without moving
or copying a single file.**

You point it at `/srv/photos`. It gives you a browser UI, a network drive you
can mount, and sync apps that keep a laptop folder up to date. The files stay
exactly where they were, with the same names, readable by every other program
on that machine.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/browse-dark.png">
  <img alt="The Stowcloud file browser listing a folder of images: a navigation rail, the folder list, a breadcrumb marking the folder as shared with other services, and the file table" src="docs/screenshots/browse-light.png">
</picture>

> Every screenshot on this page comes from `docker compose up` of
> `ghcr.io/heavycaffeiner/stowcloud:core` at revision `d82d880`, the image
> this repository's CI built and published, running on a Rocky Linux 10 host
> and browsing real files on its disk. Nothing is mocked and nothing was
> staged in a design tool.

## The problem it solves

Say you have a media server. Jellyfin scans `/srv/photos`, a backup script
rsyncs it every night, and your partner reaches it from a laptop over SMB.

Now you want to browse those photos from a phone, and send one folder to a
friend who has no account. So you install a self-hosted cloud drive. It asks
you to **upload** your photos into it. Afterwards the files live in that
program's own storage, under names it made up, and Jellyfin cannot see them
any more. You now have two copies of everything and one of them is a hostage.

Stowcloud does not do that. There is no import step, because there is nothing
to import into: the folder on disk **is** the storage. Its database is only a
cache, and deleting that cache costs you nothing but the time to rebuild it.
Other programs writing to the same folder is expected, not a fault, and a
folder that something else also writes to says so in the UI before you do
anything destructive to it.

## Is this for you?

**A good fit if you:**

- already have files on a Linux box and want a web UI over them
- share those files with other programs (media servers, backups, Samba)
- want per-person access to specific folders, not one shared password
- can run Docker and put a reverse proxy in front of it

**Look elsewhere if you:**

- want a hosted service with no server to run
- need Windows or macOS as the server (Linux only, by design)
- need collaborative document editing, calendars, contacts, or chat
- need this audited. It has not been reviewed by anyone outside this
  repository.

## Try it

You need a Linux machine with Docker, and about five minutes.

**1. Get the compose file.**

```sh
git clone https://github.com/heavycaffeiner/stowcloud
cd stowcloud
```

**2. Make the folders it will use.**

```sh
mkdir -p ./data ./secrets ./smbcfg ./shares/photos ./shares/video
chown -R 1000:1000 ./data ./secrets ./smbcfg
```

`data` holds the cache database, `secrets` holds the encryption key, and the
two under `shares/` stand in for folders of yours. The server runs as user
`1000` and cannot change ownership itself, so these have to be owned by
`1000` before it starts. Make all of them: Docker creates a missing one for
you, owned by root, and then nothing can write to it. Point them at real
folders later by editing the `volumes:` lines in `docker-compose.yml`.

**3. Start it.**

```sh
docker compose up -d
```

This pulls a roughly 30 MB image built and smoke-tested by this repository's
CI. It runs read-only, as a non-root user, with all Linux capabilities
dropped, and it serves HTTPS on port 8443, published to your network so you
can reach it from another machine.

**4. Tell it which folders to serve.**

Create `data/sc.toml`:

```toml
[[shares]]
name = "photos"
host_path = "/shares/photos"
```

Then `docker compose restart`. Paths here are the paths **inside** the
container, which is why this says `/shares/photos` and not the host path.
Without this file the server starts fine and simply has nothing to show, and
you can add folders from the admin screen later instead.

**5. Create the administrator account.**

```sh
docker compose logs sc | grep 'Setup token'
```

Open `https://<the machine's address>:8443/setup`, paste that token, and pick a
username and password.

Your browser will warn you about the certificate the first time. That is
expected: the server issued it to itself, because a private address has no
public name for a certificate authority to vouch for. Accept it once per
browser. There is no `http://` port to use instead.

![The first-run screen asking for the setup token, an administrator username, and a password](docs/screenshots/setup.png)

The token is single-use, expires after 15 minutes, and stops existing the
moment an administrator does. It is also written to `data/setup-token` if the
log has scrolled past. There is deliberately no environment variable for the
first password: anything passed that way is visible in `docker inspect` and
in the process list.

You are in. Everything below is what you can do from here.

**From inside your own network,** use `https://<the machine's address>:8443`.
That works from your router's LAN, and equally over Tailscale or WireGuard, with
nothing to configure. Your browser asks you to accept the certificate once,
because the server issued it to itself and no authority vouched for it. That
warning is the cost of a private address having no public name.

**There is no `http://` port.** Not on the LAN, not on loopback, not for
anything. One socket, always TLS. The session cookie only survives on a secure
origin, so a plaintext login would fail silently rather than fail loudly, and a
port that is safe only as long as nobody publishes it is a footgun worth not
shipping.

> **Before anyone outside your network can reach it,** put a reverse proxy with
> a real certificate in front, and set `trusted_proxies` in `data/sc.toml` to
> that proxy's addresses. Without that last part every visitor looks like the
> proxy, so the login rate limit and the audit log collapse onto one address and
> a single attacker can lock out everyone.
>
> The proxy talks HTTPS to this server and skips verifying the self-signed
> certificate, which is fine over loopback. In Caddy:
>
> ```
> cloud.example.com {
>   reverse_proxy https://127.0.0.1:8443 {
>     transport http { tls_insecure_skip_verify }
>   }
> }
> ```

## What you can do with it

| | |
|---|---|
| **Browse and organise** | Upload, move, copy, rename, delete. If two people change the same thing you get a conflict screen, not a silent overwrite. Long jobs keep running if you refresh the tab. |
| **Uploads that resume** | A dropped connection, a closed tab, or a proxy with a size limit does not restart the transfer. |
| **Search** | By name, across every folder the person searching is allowed to see. |
| **Share links** | Send a folder to someone with no account. Optionally password-protected, expiring, download-capped, or upload-only so people can drop files in without seeing what is there. |
| **Per-folder access** | Give an account one folder, or one subfolder inside it, with read and write decided separately. The default is that a new account sees nothing. |
| **Network drive** | WebDAV (mount it in Windows Explorer, Finder, or Linux) and SMB through an optional sidecar container, off unless you turn it on. |
| **Sync apps** | Existing Nextcloud desktop and mobile clients log in and sync against this server unmodified.[^tm] |
| **Accounts** | Local passwords, single sign-on through OIDC (Keycloak, Authentik, and the like), authenticator-app codes, app passwords, recovery codes. |

### One tree, however deep it goes

The folder pane walks the same directories your shares point at. Nothing was
imported, so what you see here is what `ls` sees.

![The file browser with the folder tree open, showing nested folders under two different shares](docs/screenshots/tree.png)

### Search reaches across folders

One query covers every folder that account can see, so it is one place to
look instead of several. Names only. Searching inside file contents is a
separate feature you turn on.

![Search results for "2026" listing matches from two shares above the current folder's own contents](docs/screenshots/search.png)

### A link is created once, and shown once

The full link exists in exactly one response and cannot be recovered
afterwards, which is why the dialog says so out loud. Copy it before you
close the box. Expiry, password and download cap are chosen at creation;
revoking is permanent, and the same link can never be recreated.

![The share-link dialog just after creating a link: the one-time URL, a copy button, and the link's expiry](docs/screenshots/share-link.png)

Whoever opens it sees this and nothing else. No account, no other folder, no
hint that anything else exists on the server.

![The public share page a recipient sees: a title, the files in the shared folder, and a download button](docs/screenshots/share-public.png)

### You decide who sees what

A grant names a folder and, if you want, one subfolder inside it. Name a
subfolder and that account sees only that subtree; it cannot tell that the
parent, or anything else in the share, exists. An account with no grant sees
an empty screen, and that is the intended default rather than something
broken.

![The folder-permission dialog for an account, showing one grant scoped to a subpath with read and download allowed](docs/screenshots/folder-grants.png)

### Text and code are editable in place

Small text files open in a browser editor with syntax highlighting and save
back through the same conflict check as everything else, so two people
editing one file get a conflict screen rather than a lost edit.

![A Markdown file open in the built-in editor with line numbers and syntax highlighting](docs/screenshots/editor.png)

### Nothing is deleted in a hurry

You switch the trash on per folder. Until you do, a delete is a delete, and
the setting says so rather than leaving you to guess.

![The trash listing deleted items with their sizes and deletion times, and restore and purge actions](docs/screenshots/trash.png)

## What you need

- **Linux**, kernel 5.6 or newer. 5.13 or newer adds the sandbox used for
  image previews.
- **Docker** 20.10.10 or newer. Older versions block a system call this
  depends on and force a weaker security mode.
- **About 30 MB** for the image, plus a folder for the cache database.

Linux is the only supported runtime. The code compiles on other systems, but
nothing else is a deployment target: the guarantees this project is built on
are Linux kernel features, and there is no substitute for them elsewhere.

To turn on SMB, edit the address in the `sc-smb` service to one your machine
actually has, then start it explicitly:

```sh
docker compose --profile smb up -d
```

That gets you `\\192.168.1.10\photos`. To mount by name instead, put
`server_name = "stowcloud"` under `[smb]` in `data/sc.toml` and give the sidecar
`network_mode: host`: the name is announced by broadcast, which a Docker bridge
does not carry.

## How it is built

Five rules decide most of the design.

1. **The folder on disk is the truth.** The database is a cache. Delete it
   and it rebuilds.
2. **A path is a kernel handle, not a string.** Paths are resolved by the
   kernel in one step, so a file cannot be swapped underneath a check that
   already passed.
3. **The folder is not ours.** Other programs are assumed to be writing to it
   at the same time, always.
4. **Compatibility code stays out of the core.** Enforced by a CI check, not
   by intentions.
5. **The default is the restrictive one.** No home folders, no symlinks
   followed, SMB off, uploaded content never rendered inline.

Image previews are decoded in a separate process that is sandboxed so it can
neither run another program nor reach the network. Sync clients show those as
thumbnails; the web UI currently marks which files have one but does not draw
them in the list yet.

## Documentation

[`docs/README.md`](docs/README.md) is the index and opens with a reading
order. Start with
[Architecture](docs/proposals/stowcloud-12-architecture.md) for the design
and layout, and [Deployment](docs/proposals/stowcloud-13-deployment.md) for
running it in earnest. Every subsystem has a document written from what was
built, not from what was planned.

<details>
<summary><b>Building from source</b></summary>

```sh
cd web && npm ci && npm run build && cd ..          # frontend first
cargo build -p sc-server --release --features embed-ui
bash scripts/verify.sh                              # the gate CI runs
```

The frontend is compiled into the binary, so it has to be built first.
`embed-ui` is off by default precisely because a fresh checkout has no
`web/build` yet. The release image is a statically linked musl build, and
`Dockerfile` does both stages for you.

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

Every milestone in the architecture document is reachable by a real client.
Still missing: WebDAV conformance testing in CI, an automated sync-client
regression suite, and an external security review. Treat it accordingly. It
has not been audited by anyone outside this repository.

[^tm]: Nextcloud is a registered trademark of Nextcloud GmbH. Stowcloud is not
    affiliated with, endorsed by, or sponsored by Nextcloud GmbH; the name is
    used only to state, factually, which clients interoperate.
