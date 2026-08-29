# Cutover: what it would take, measured

> This is a decision document, not a plan to execute. The cutover rewires
> `cmd/stowcloud` to `engine/` and removes `internal/`, which is the tree the
> product currently runs on. The numbers below were measured on
> `refactor/core` at `ea660bb` so the decision can be made against evidence
> rather than an estimate.

## The headline number is misleading

`internal/` is 59,493 lines and `engine/` is 56,303, which reads as though the
rebuild is nearly a replacement. Package by package it is not that simple.

| Package | `internal/` | `engine/` | Status |
| --- | --- | --- | --- |
| `acl` | 782 | `service/acl` | rebuilt |
| `auth` | 4,354 | `service/auth` | rebuilt |
| `core` | 5,190 | `service/core` | rebuilt |
| `vfs` | 3,226 | `infra/vfs` | rebuilt |
| `upload` | 3,928 | `service/upload` | rebuilt |
| `preview` | 2,764 | `service/preview` | rebuilt |
| `oidc` | 1,372 | `service/oidc` | rebuilt |
| `smb` | 721 | `service/smb` | rebuilt |
| `watch` | 789 | `service/watch` | rebuilt |
| `jail` | 1,034 | `infra/jail` | rebuilt |
| `server` | 2,391 | `http/server` | rebuilt |
| `store/state` | 3,075 | 6,406 | rebuilt, larger |
| `store/dbfile` | 445 | 438 | rebuilt |
| `search/index` | 2,002 | `service/search` 4,392 | rebuilt |
| `clock`, `secret`, `task` | 167 | `kit/*` | rebuilt |
| `runtimecfg` | 629 | `service/settings/runtimecfg` | rebuilt |
| `smbagent` | 1,851 | none | **absent** |
| `smbpublish` | 288 | none | **absent** |
| `httpapi/handler` | 7,886 | `http/handler` + `lifecycle` | partially bound |

## Three things block the cutover

### 1. Twenty-three routes have no binding

The engine binds 64 of the 87 routes its own table names. The rest need
services that `lifecycle.Open` never constructs: preview for thumbnails,
search for the stream and the index admin, OIDC for the three sign-in routes
and the two account-link ones, settings for the sectioned resource, SMB for
the credential routes. The service packages exist in every one of those
cases, including `service/settings` (1,626 lines, holding `check` and
`runtimecfg`). What is missing is construction and binding, not the services
themselves.

The unbound set, exactly, grouped by the service each one waits on:

- **OIDC** (7): `auth.oidc.start`, `auth.oidc.callback`, `auth.oidc.config`,
  `account.oidc-link.start`, `account.oidc-link.delete`,
  `admin.users.oidc.get`, `admin.users.oidc.delete`
- **SMB** (4): `account.smb.create`, `account.smb.password.set`,
  `account.smb.password.delete`, `admin.smb.apply`
- **Search** (3): `search.stream`, `admin.index.build`, `admin.index.estimate`
- **Settings** (3): `admin.settings.get`, `admin.settings.patch`,
  `admin.storage`
- **Preview** (1): `files.thumbnail`
- **Archive** (2): `files.archive`, `files.archive.list`
- **Setup** (2): `system.setup.get`, `system.setup.post`
- **Events** (1): `events`

### 2. Four protocol surfaces are built and unmounted

| Surface | Lines | Mounted |
| --- | --- | --- |
| `http/dav` | 1,759 | no |
| `http/compat` | 1,178 | no |
| `http/archive` | 418 | no |
| `http/emergency` | 519 | no |

That is 3,874 lines of finished, tested work that no request can reach. The
old tree serves WebDAV, the vendor compatibility layer, archive downloads and
the emergency console; a cutover that dropped them would remove working
features from the product.

### 3. The public link surface is absent

The old tree serves five routes under `/s/{token}`: the landing page, the
password gate, the download, the zip and the drop upload. The engine's route
table has none of them. Link creation, listing, update and deletion are bound,
so links can be minted and never opened.

The old tree serves 101 routes in total against the engine's 87-entry table,
and the difference is mostly this.

## What a cutover would actually require

In dependency order, with the measured size of each piece:

1. **Construct the missing services** in `lifecycle.Open`: preview, search,
   OIDC, settings, SMB. Every package exists; this is wiring plus whatever
   each needs from configuration.
2. **Bind the remaining 23 routes** against those services.
3. **Mount dav, compat, archive and emergency.** The code is written; the
   mounting, the route entries and the middleware ordering are not.
4. **Build the public link surface**, five routes with their own content
   negotiation, unlock cookie and archive path.
5. **Port `smbagent` and `smbpublish`** (2,139 lines) or decide the sidecar
   keeps using the old tree.
6. **Rewire `cmd/stowcloud`** and delete `internal/`.

Steps 1 through 5 are the work. Step 6 is an afternoon.

## What has been verified about the engine so far

Booted as a real process and driven with curl at `ea660bb`:

- 4,096 bytes written and read back byte-identical; `Content-Length` correct,
  ETag marked weak; range `100-199` exact with
  `Content-Range: bytes 100-199/4096`; a range past the end answering 416 with
  `bytes */4096`.
- Delete, trash listing, a 404 on an unmatched purge id, restore returning
  identical bytes.
- Administrator reaches the admin surface; an ordinary session and an app
  password both get 403. No host path in the share listing. A mutation
  refused without the CSRF header and accepted with it. Self-delete refused.
- A 12,000-byte resumable upload across a simulated interruption, delivered
  byte-identical, with a misaimed chunk refused 409.
- Five traversal attempts all 404. A wrong password and an absent account
  return byte-identical 401 bodies. 400 concurrent requests split 206 served,
  194 throttled.

Both delivered files were compared against the share directory on disk.

## The recommendation

**Do not cut over yet.** The engine is further along than the line count
suggests for the parts it covers, and it is genuinely missing three surfaces
the product serves today. Deleting `internal/` now would remove WebDAV, the
vendor compatibility layer and every public link from a running deployment.

The cheapest next step is item 3: mounting dav, compat, archive and emergency
is binding rather than construction, the same work as the last ten commits,
and it converts 3,874 written lines into reachable behaviour.

## A correction, recorded

An earlier version of this document said 67 routes were bound and that no
settings package existed. Both were wrong, and both came from reading a grep
instead of reconciling the two lists. The bound count is 64, verified by
comparing the route table's names against the binding table's cases; the
settings package exists and is 1,626 lines.

The same wrong figure is in commit `413d2d4`, whose message says "Sixty-seven
of eighty-seven routes now answer". It is 64. The commit message cannot be
corrected without rewriting history, so the correction lives here.

The counts in this document come from `comm` over two sorted lists: every
`Name` in `server.Table()` against every `case` in the binding switch. That
is reproducible and does not depend on how the switch happens to be written.
