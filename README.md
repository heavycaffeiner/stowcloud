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

> Every screenshot on this page comes from the image this repository builds,
> brought up with `docker compose up` and browsing real files on the host's
> disk. Nothing is mocked and nothing was staged in a design tool.

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

**2. Start it.**

```sh
docker compose up -d
```

That is the whole setup. It pulls a roughly 30 MB image built and smoke-tested
by this repository's CI, runs read-only as a non-root user with every Linux
capability dropped, and serves HTTPS on port 8443.

There are no directories to create and nothing to chown. The files you upload
live in a named volume, and the image ships the directories it mounts them over
already owned by the uid it runs as, so a volume inherits an owner that can
write. To serve a folder you already have instead, replace the `sc-files` line
under `volumes:` with a bind mount:

```yaml
      - /srv/my-files:/shares/files:z
```

A bind mount anywhere works with no extra configuration. The sandbox reads
the process's own mount table and grants every mounted directory a folder
could live on, so it finds `/mnt/photos` and `/mnt/video` without being told
about them:

```yaml
    volumes:
      - /srv/photos:/mnt/photos:z
      - /srv/video:/mnt/video:z
```

Folders anywhere under a mounted directory can be added and removed from the
admin screen without restarting.

Running the binary directly, without a container, there is no mount table to
read, so the sandbox instead grants a fixed set of directories: `/srv`,
`/mnt`, `/media`, `/data`, `/home` and `/opt`, each only when it exists. A
folder anywhere under one of them can be added from the admin screen. A path
outside all of them is refused when it is added, not later.

The server has to be able to read and write it. Rather than changing who owns
your files, tell the server which uid to be. `PUID` and `PGID` in the compose
file are the uid and gid it runs as, and the default of 1000 is what a
single-admin machine's own files usually carry:

```sh
stat -c '%u:%g' /srv/my-files
```

Put that pair in the compose file and the share works with no chown at all:

```yaml
    environment:
      PUID: 1000
      PGID: 1000
```

Create the directory before the first start either way. A directory Docker
creates for a missing bind source is owned by root, which no `PUID` matches.

**3. Create the administrator account.**

```sh
docker compose logs sc | grep 'setup token'
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
> a real certificate in front, and set the trusted-proxy ranges on the admin
> settings screen to that proxy's addresses. Without that last part every visitor looks like the
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
- **Docker** 20.10.0 or newer. Its default seccomp profile allows `openat2`
  from that release on; older profiles refuse it, and this server resolves
  every path with it and will not start without it.
- **About 30 MB** for the image, plus a folder for the cache database.

Linux is the only supported runtime. The code compiles on other systems, but
nothing else is a deployment target: the guarantees this project is built on
are Linux kernel features, and there is no substitute for them elsewhere.

SMB is off by default. The sidecar comes up with the stack and sits idle; the
switch is in the admin screen, and turning it on starts the daemon without
touching a shell.

The sidecar shares the host's network stack, so port 445 must be free: stop a
Samba already running there first. Two fields in the SMB section of the admin
settings screen are worth setting before you do: the server name clients see
(`stowcloud`), and the interfaces the daemon binds (`192.168.1.10`).

The first is what makes shares open as `\\stowcloud\photos` instead of
`\\192.168.1.10\photos`. It needs the host's network to work at all, because the
name is announced by broadcast and a Docker bridge does not carry broadcast.

The second pins which address smbd listens on. Left empty it binds every private
range it finds, which on the host's stack includes the Docker bridges, so any
container on the machine can reach it.

## When it will not start

The server refuses to start rather than running in a state it cannot make
guarantees about. Two refusals account for most first runs, and both name a
cause that is one layer away from the real one.

**`opening the store: ... unable to open database file`.** The data directory
is not writable by the uid the server runs as. SQLite reports a directory it
cannot create a file in this way, so the message names the file rather than the
directory. It is usually a bind mount whose host directory is owned by root,
because the path did not exist when the container first started. Create it, and
set `PUID`/`PGID` to its owner.

**`PUID/PGID ask for ... but the image was built as ...`.** Changing the uid
means moving an account in `/etc/passwd`, which `read_only: true` forbids. Drop
`read_only`, or bake the uid in with
`--build-arg PUID= --build-arg PGID=` and leave the environment alone.

**`openat2 ... refused`.** Every path is resolved with `openat2` and there is
no fallback, because resolving a path one component at a time is the race this
design exists to close. The message distinguishes the three causes: `EPERM` is
a seccomp profile, so upgrade Docker to 20.10.0 or newer; `EACCES` is a
filesystem permission, usually a mounted directory the running uid cannot
reach; `ENOSYS` is a kernel below 5.6.

To see what the kernel actually offers inside the container that is deployed,
read the hardening line the server logs as it starts. It names the policy that
was applied and, where one was refused, which capability the kernel lacked:

```sh
docker compose logs sc | grep hardening
```

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

[`docs/README.md`](docs/README.md) is the index. For running it in earnest,
this page and the compose file carry what an operator needs;
[`docs/CUTOVER.md`](docs/CUTOVER.md) is what changed for a deployment, and
[`docs/RISKS.md`](docs/RISKS.md) is what is likely to break. Those documents
describe the code in this repository.

<details>
<summary><b>Building from source</b></summary>

```sh
cd web && pnpm install && pnpm build && cd ..        # frontend first
cd go && CGO_ENABLED=0 go build -tags embed_ui ./cmd/sc-engine
bash scripts/verify.sh                              # the gate CI runs
```

The frontend is compiled into the binary, so it has to be built first. The
`embed_ui` tag is off by default because a fresh checkout has nothing to embed
yet, and the frontend builds into the package that embeds it: the embed
directive cannot name a path outside its own package, and that is also what
gives it a real dependency edge, so a rebuilt frontend is picked up by the next
build.

Cgo is off, which is the whole static-binary story: with it off there is no
dynamic loader and no libc to match, so the runtime image needs neither.
`Dockerfile` does both stages for you.

The SMB sidecar is a second binary, `go/cmd/sc-smb-agent`. It runs as root
beside the Samba daemon and applies what the server renders, which the server
cannot do itself: it runs unprivileged, in a network namespace that cannot see
the host's devices. `Dockerfile.smb` builds it, and `deploy/smb/native/`
holds the material for installing it on a bare-metal host.

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

The binary statically links its dependencies and embeds the built frontend, so
both are redistributed with it.
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md) carries their licences and
copyright notices, and the runtime image carries a copy at
`/THIRD-PARTY-NOTICES.md`.

</details>

## Status

The backend is Go. [`docs/CUTOVER.md`](docs/CUTOVER.md) is what an operator
needs before switching, including the one-time reconciliation every attached
sync client performs.

Measured rather than asserted: [`docs/CONFORMANCE.md`](docs/CONFORMANCE.md) has
the WebDAV suite against both implementations, with each failure attributed;
[`docs/FOOTPRINT.md`](docs/FOOTPRINT.md) has the memory and timing numbers, and
says which planned measurements were not made.

Still missing: an automated sync-client regression suite, a second architecture
for the sandbox proof, and an external security review. Four surfaces answer
that they are not implemented and name themselves. Treat it accordingly. It has
not been audited by anyone outside this repository.

[^tm]: Nextcloud is a registered trademark of Nextcloud GmbH. Stowcloud is not
    affiliated with, endorsed by, or sponsored by Nextcloud GmbH; the name is
    used only to state, factually, which clients interoperate.
