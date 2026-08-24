# Cutover

The server is a Go program, and there is nothing else left in the tree. This
records what the port changed for a deployment and for the clients attached to
one.

## Migrating from the previous build is no longer possible

`stowcloud migrate --from-rust` and the importer behind it are deleted, along
with the schema adoption path, the legacy seal readers and the response differ.

That is a deliberate one-way door rather than an oversight. Every one of those
existed to read a format the deleted implementation wrote, and each was a
second definition of a durable shape: an importer that maps its tables, an
adoption check that recognises its schema, an AAD variant that opens
ciphertext it sealed. A second definition of a durable shape is a place the two
can disagree, and it has to be maintained by whoever changes either. Keeping
them for a migration nobody can still run buys nothing and costs that.

An operator holding a data directory from the previous build needs a binary
from before this commit to convert it. What this build opens is what this build
wrote.

## Shares the filesystem gate refuses

This build refuses filesystems the previous one admitted: network filesystems,
user-space filesystems, a container's writable layer, read-only mounts,
anything whose type it has no name for, and any instance reporting no birth
time. A refused share does not stop the server, but it does not serve either.

The refusal names the filesystem and the path, including for a mount inside a
share: a supported share root does not bless an unsupported mount below it.

## What changed for clients

**Every file id changed, once.** The previous build used row ids and this one
derives an id from the file's identity, so an attached sync client performed
one full reconciliation on first contact. It bought the opposite property
afterwards: a cache rebuild costs clients nothing, because the id no longer
depends on a row a rebuild re-mints.

**File change tokens carry the marker that says they are advisory.** Linux
exposes no inode change version this server can derive an exact token from, so
a file's token comes from metadata that can repeat. The previous build sent it
as though it were exact. This one marks it, and refuses a conditional write
against it rather than accepting one it cannot honour.

A native client has to make an overwrite after a refused conditional write an
explicit unconditional retry. Retrying with the same token, or with the one the
refusal returned, is refused again every time. The shipped interface already
does this: its conflict dialogue says the file may have changed rather than
asserting it did, and its overwrite action sends no condition.

## The SMB sidecar

`go/cmd/sc-smb-agent`, built from the same module as the server. It runs as
root beside the Samba daemon and applies what the server renders, which the
server cannot do itself: it runs unprivileged, in a network namespace that
cannot see the host's devices. `Dockerfile.smb` builds it and the compose file
runs it.

Porting it uncovered that the half on the server's side had never been wired.
`smb.Render` compiled, was tested and had no caller, so no configuration was
ever written and there was no client for the agent's control socket. SMB could
not have worked in any deployment. There is a config section, a publisher and
an apply surface now, and the surface returns the sidecar's own report, because
everything worth knowing about a change is true in the sidecar's namespace
rather than this server's.

## Wires that were never run

The four surfaces that answered "not implemented" do the work now: packing an
archive, updating a live share link, building the search index, and the
provider link, which needed a whole flow rather than one handler.

Every one turned out to be a wire that was never run rather than work that was
never written, which is the same shape as the forty-one unmounted routes and as
the SMB renderer above. The uploader was the same again: `Finalize` was
complete and had no caller, so bytes reached the part file and the destination
never appeared. A package that compiles and has no caller is indistinguishable
from a package that works, from inside the package.

Ten WebDAV conformance tests fail. `docs/CONFORMANCE.md` has each one.

## Measured, not claimed

`docs/FOOTPRINT.md` has the numbers. Idle memory is less than half the previous
build's; a two-hundred-directory walk costs roughly twice the memory and two
thirds of the time. Two predicted regressions are listed as unmeasured rather
than as absent, because the workload that would show them was not run. Those
figures were taken while both builds could still run on one host and cannot be
reproduced now.
