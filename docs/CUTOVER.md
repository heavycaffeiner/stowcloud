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

Nothing. The SMB sidecar agent was the last of it and is now
`go/cmd/sc-smb-agent`, built from the same module as the server.

It runs as root beside the Samba daemon and applies what the server renders,
which the server cannot do itself: it runs unprivileged, in a network namespace
that cannot see the host's devices. Nothing about deploying it changes,
`Dockerfile.smb` builds it and the compose file runs it, and the gate's two
cargo steps are gone because the ordinary Go steps cover it.

Porting it uncovered that the half on the server's side had never been wired.
`smb.Render` compiled, was tested and had no caller, so no configuration was
ever written and there was no client for the agent's control socket. SMB could
not have worked in any deployment. There is a config section, a publisher and
an apply surface now, and the surface returns the sidecar's own report, because
everything worth knowing about a change is true in the sidecar's namespace
rather than this server's.

## What the port does not do yet

The four surfaces that answered "not implemented" now do the work: packing an
archive, updating a live share link, building the search index, and the
provider link, which needed a whole flow rather than one handler.

Every one of them turned out to be a wire that was never run rather than work
that was never written, which is the same shape as the forty-one unmounted
routes and as the SMB renderer above. A package that compiles and has no caller
is indistinguishable from a package that works, from inside the package.

Ten WebDAV conformance tests fail, five of them shared with the old build.
`docs/CONFORMANCE.md` has each one, with which are carried over and which are
this port's.

## Measured, not claimed

`docs/FOOTPRINT.md` has the numbers, both builds on one host. Idle memory is
less than half the old build's; a two-hundred-directory walk costs roughly
twice the memory and two thirds of the time. Two predicted regressions are
listed as unmeasured rather than as absent, because the workload that would
show them was not run.
