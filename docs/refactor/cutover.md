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
| `preview` | 2,764 | `service/preview` | rebuilt, bound |
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
| `smbagent` | 1,851 | `service/smb/agent` | rebuilt |
| `smbpublish` | 288 | `service/smb/publish` | rebuilt |
| `httpapi/handler` | 7,886 | `http/handler` + `lifecycle` | partially bound |

## Three things block the cutover

### 1. Every route is bound

The engine binds 89 of the 89 routes its own table names. This section listed
the services `lifecycle.Open` never constructed; every one of them is
constructed now.

The sectioned settings resource is now bound, against `service/settings`
(1,626 lines, holding `check` and `runtimecfg`).

`admin.smb.apply` was on this list. It is bound, and the publication path
behind it is wired: `lifecycle.Open` reads the file-sharing section, hands
`auth.New` a credential renderer and a path, and attaches the publisher as the
access-change sink. Before that the three account-facing SMB routes wrote the
database and stopped there, so a withdrawn credential kept working against the
last file the sidecar had imported.

The two setup routes are bound as well. `SetupGate` was built and tested and
constructed nowhere, so nothing minted a token and no deployment could be set
up through the engine at all. `lifecycle.Open` now builds the gate, issues a
token when the account count is zero, and writes it under the data directory.
The first administrator's grant moved into `core.GrantEveryShare`, so no
command builds grant rows of its own.

The change channel is bound too. `engine/service/watch` had `Start` and no
caller, and the frame vocabulary in `http/handler` was referenced by nothing:
the engine could detect a change and had nowhere to send it. `lifecycle.Open`
now starts the watcher over the registered shares and joins it to a broker in
`http/server`, and `Close` releases both.

The old hub's leak is fixed rather than carried: it removed a socket without
unsubscribing, so a closed tab left a kernel watch pinned for the life of the
process. Disconnect now releases every subscription the connection held.

The name index is the last of them. `index.Open` was written and called by
nothing, so `Service.SetIndex` had no caller outside tests and `Build` returned
`ErrNoIndex` to anyone who asked. `lifecycle.Open` now opens the index
directory when the stored setting asks for one, and the build route runs the
traversal as a job the client polls rather than holding a connection open
across it.

The binding count is no longer what blocks a cutover. What follows is.

### 2. Two protocol surfaces have their vocabulary and not their handlers

| Surface | Lines | Reachable |
| --- | --- | --- |
| `http/dav` | 1,759 | no |
| `http/compat` | 1,178 | no |
| `http/archive` | 418 | **yes** |
| `http/emergency` | 519 | **yes** |

Archive and the repair door were both in this list and are now mounted:
archive behind `files.archive` and `files.archive.list`, the door on its own
`/emergency` prefix ahead of the middleware chain.

The other two are not one mount away, and an earlier version of this document
said they were. What `engine/http/dav` holds is the protocol's vocabulary:
the If-header grammar, the XML scanner, PROPFIND and PROPPATCH parsing, the
multistatus writer, the href encoder, the method-to-permission table. There
is no handler for a single method. The old tree's equivalent parsing lives
beside roughly 2,000 lines that do the work:

| `internal/dav` | Lines | Engine equivalent |
| --- | --- | --- |
| `lock.go` | 504 | store half only (`AdmitDavLock`, `SnapshotDavLocks`) |
| `content.go` | 488 | absent |
| `write.go` | 368 | absent |
| `props.go` | 326 | absent |
| `uploads.go` | 290 | absent |
| `lockmethod.go` | 228 | absent |
| `search.go` | 129 | absent |
| `propfind_root.go` | 79 | absent |

`http/compat` is the same shape: 1,178 lines of envelope, capabilities,
permission letters and vendor path layouts, against 4,356 lines of handler in
`internal/compat`.

So these are construction, not binding, and they are the largest remaining
piece of the rebuild rather than an afternoon's wiring.

### 3. The public link surface is built

This section said the surface was absent and that links could be minted and
never opened. That is no longer true, and the correction is the point: all five
routes under `/s/{token}` are implemented in `lifecycle/publiclink.go` and
mounted from `mount.go`. They sit outside the versioned table because these
addresses get pasted into messages and must not carry an API version.

Measured against a running engine, as a stranger with no cookie jar:

| Route | Behaviour observed |
| --- | --- |
| `GET /s/{token}` | 200 with the name, size and what the holder may do |
| `POST /s/{token}/auth` | 204 on the password, 422 on a wrong one |
| `GET /s/{token}/download` | 200 with the bytes |
| `GET /s/{token}/zip` | 200, a real archive of the folder's files |
| `POST /s/{token}/drop` | 201, and the file lands in the share |

A locked link answers `{"protected":true}` and nothing else: no name, no size,
no listing. Its download is 403 until the password is answered. A drop link
reports `drop:true` with no entries, so a collection box does not reveal what
is already in it.

## What a cutover would actually require

In dependency order, with the measured size of each piece:

1. **Write the dav and compat handlers.** The vocabulary is done; the method
   handlers are not, and they are 4,176 and 4,356 lines in the tree they
   replace. Neither is mounted, and neither has an entry point to mount.
2. **Rewire `cmd/stowcloud`**, then delete `internal/`.

Step 1 is the work. Step 2 is an afternoon.

The list carried two more steps that are now done. The public link surface is
built and mounted, as the section above records. `cmd/sc-smb-agent` is rewired:
the supervisor it needed, `Agent` and `Smbd`, is in `service/smb/agent`, and the
command builds against the engine with no `internal/` import left.

This list carried a sixth step, porting `smbagent` and `smbpublish`, and it
was already done. Both are in the engine as `service/smb/agent` and
`service/smb/publish`, 1,973 non-test lines between them, and both are now
reachable through the publisher the apply route drives. The inventory table
above recorded them as absent, which was true of nothing but the old paths;
it is corrected.

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

- Archives: the system `unzip` lists a subtree archive's three members, the
  extracted files compare equal to the originals on disk, an empty directory
  comes back as a directory, and a filename carrying a quote is refused 422
  with no header injected.
- The repair door: `state` answers 200 with no credential and outside the
  chain, an `X-Forwarded-For` claiming a public address does not change the
  answer, three paths beyond its own four answer 404, reading and writing the
  settings and requesting a restart all answer 401, and the ordinary API still
  carries the chain's CSP header behind it.

Both delivered files were compared against the share directory on disk.

## The recommendation

**Do not cut over yet.** Deleting `internal/` now would remove WebDAV, the
vendor compatibility layer and every public link from a running deployment.

The cheapest next step is item 1, followed by item 2. Those 5 routes are binding
work against packages that already exist, which is what the last dozen commits
have been, and each one converts a 501 into an answer.

Item 3 is the largest single piece left and should be sized before it is
started. An earlier version of this document called it a mount and was wrong:
the engine holds the protocol vocabulary and none of the method handlers.

## A correction, recorded

An earlier version of this document said 67 routes were bound and that no
settings package existed. Both were wrong, and both came from reading a grep
instead of reconciling the two lists. The bound count is verified by comparing
the route table's names against the binding table's cases; the settings
package exists and is 1,626 lines.

Two claims made in commit messages while working through this were also
wrong, and are corrected here because a commit message cannot be edited:

- `413d2d4` says "Sixty-seven of eighty-seven routes now answer". It was 64.
- `823b6aa` says the OIDC client secret "has no home in either tree" and that
  the old tree's `StoreSecret` hook has no non-test caller. Both are false.
  `cmd/stowcloud/serve.go` supplies it, and `internal/server.StoreOIDCSecret`
  seals the value under the master key and writes it to a config-secret row.
  The engine has the sealing primitives (`SealConfigSecret`) and the row
  (`WriteConfigSecret`); what it lacks is the small function joining them and
  a settings path that strips the secret out of the document before storing
  the rest. That is an hour, not a blocker.
- The same commit says neither the upload engine nor the core exposes a count
  of work in flight. `state.CountActiveWork` exists and is now used, so a
  restart-required save reports what it would interrupt.

The counts in this document come from `comm` over two sorted lists: every
`Name` in `server.Table()` against every `case` in the binding switch. That
is reproducible and does not depend on how the switch happens to be written.
