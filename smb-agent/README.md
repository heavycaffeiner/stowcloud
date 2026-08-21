# The SMB sidecar agent

The last Rust program in this repository, and the only one. Everything else was
ported to Go; this was not, and the reason is worth stating rather than leaving
as an oddity somebody finds later.

## What it does

The server renders four files into a shared directory: the Samba
configuration, the password database, the account file and the network policy.
It writes them as an unprivileged user, in a container that has its own network
namespace.

This program runs as root beside the Samba daemon. It picks those files up,
decides the network scope from the interfaces it can actually see, validates
the result, promotes it, imports the password database and signals the daemon.

The split is the point. The server gains no privilege and this program parses
nothing off the network. Neither half can do the other's job: the server cannot
see the host's devices from its namespace, which is why it renders the closed
case, and this program is what expands it.

## Why it is still Rust

It was in no phase's milestone list. The port scoped `internal/smb` to the
render, the bind rule, the escaping and the password database, and the agent
belonged to nobody, which is the same pattern that left the upload surface and
the WebDAV mount unreachable.

The cutover kept it rather than take either of the alternatives. Deleting it
ships SMB with nothing to promote the rendered files, which is not a
degradation but a feature that stops working. Porting it means writing two
thousand lines of root-running code in the phase whose job is comparison, with
no phase's tests behind it.

So the honest shape is this: the port replaced the server, and one privileged
sidecar is still a Rust program. The gate builds it and runs its tests on every
run, so it cannot rot unnoticed, and a later phase can port it against that
gate. Recorded as Q10 in `docs/proposals/OPEN-QUESTIONS.md`.

## The vendored half

`src/shared/` holds the two things this program and the server both have to
agree on: the control protocol, and what counts as an internal network.

They were a shared crate until the cutover. There is no crate to share now, so
they are vendored here, and the agreement is kept by both sides' tests rather
than by the compiler. The Go side's copy of the classification is
`go/internal/smb/bind.go`. A change to either has to move both.

Some of what is vendored is the server's half rather than this program's: the
request builder, the client-side timeout, the classifier the server uses to
check a pinned interface. It is kept rather than trimmed, because deleting the
side this binary does not call is how one protocol becomes two.

## Building

```sh
cd smb-agent/agent && cargo build --release
```

`Dockerfile.smb` builds it into the sidecar image, statically linked, and that
is how it ships. `deploy/smb/native/install.sh` installs it on a bare-metal
host.
