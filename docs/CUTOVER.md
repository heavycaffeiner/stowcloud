# Cutover

The server is a Go program. What follows is what an operator has to do about
that, and what changes for the clients already attached.

## Before you stop the old binary

**Run the filesystem gate over every share.** The Go build refuses filesystems
the Rust build admitted: network filesystems, user-space filesystems, a
container's writable layer, read-only mounts, anything whose type this build
has no name for, and any instance that reports no birth time. A refused share
does not stop the server, but it does not serve either, and finding that out
after the old binary is gone is finding it out at the worst moment.

The refusal names the filesystem and the path, including for a mount inside a
share: a supported share root does not bless an unsupported mount below it.

**Migrate while nothing is running.** The state spans several databases with no
snapshot across them, so reading them while something writes produces a
`state.db` holding one instant's accounts and another's grants. Both servers
hold the instance lock for their lifetime and the migration takes the same
lock, so a server still running is a refusal rather than a corrupt result.

```sh
stowcloud migrate --from-rust /var/lib/stowcloud
```

Copy the whole data directory if you are staging this elsewhere, not just the
`.db` files: the tables live in the write-ahead log until a checkpoint, and a
copy without the sidecars opens cleanly and is empty.

## What changes for clients

**Every file id changes, once.** The old build used row ids and this one
derives an id from the file's identity, so every attached sync client performs
one full reconciliation on first contact. It is a one-time cost that buys the
opposite property afterwards: a cache rebuild stops costing clients anything,
because the id no longer depends on a row that a rebuild re-mints.

**File change tokens gain the marker that says they are advisory.** Linux
exposes no inode change version this server can derive an exact token from, so
a file's token comes from metadata that can repeat. The old build sent it as
though it were exact. This one marks it, and refuses a conditional write
against it rather than accepting one it cannot honour.

A native client has to make an overwrite after a refused conditional write an
explicit unconditional retry. Retrying with the same token, or with the one the
refusal returned, is refused again every time. The shipped interface already
does this: its conflict dialogue says the file may have changed rather than
asserting it did, and its overwrite action sends no condition.

**Signing in moved.** It is `POST /api/auth/login`, which is where the client
always called it and where the old server always served it. The Go build had it
on the change-password path, which is why nothing could sign in, and that is
fixed rather than preserved.

## What is still Rust

The SMB sidecar agent, in `smb-agent/`. It runs as root beside the Samba daemon
and applies what the server renders, which the server cannot do itself: it runs
unprivileged, in a network namespace that cannot see the host's devices.

Nothing about deploying it changes. `Dockerfile.smb` builds it and the compose
file runs it, exactly as before.

## What the port does not do yet

Four surfaces answer that they are not implemented rather than pretending:
packing an archive, updating a live share link, building the search index, and
starting a provider link. The routes exist and return a status a client can act
on, which is why the screens that use them report a clear refusal instead of a
missing endpoint.

Ten WebDAV conformance tests fail, five of them shared with the old build.
`docs/CONFORMANCE.md` has each one, with which are carried over and which are
this port's.

## Measured, not claimed

`docs/FOOTPRINT.md` has the numbers, both builds on one host. Idle memory is
less than half the old build's; a two-hundred-directory walk costs roughly
twice the memory and two thirds of the time. Two predicted regressions are
listed as unmeasured rather than as absent, because the workload that would
show them was not run.
