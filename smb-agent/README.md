# The SMB sidecar agent

The privileged half of SMB publishing. Its source is `go/cmd/sc-smb-agent` and
`go/internal/smbagent`; this directory holds the deployment material.

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

## The two things both halves agree on

The control protocol and the classification of what counts as an internal
network. Both are one definition in one module, not a copy on each side:
`go/internal/smbagent/wire.go` and `go/internal/smb/bind.go`.

They were vendored while this was a separate program, kept in step by both
sides' tests rather than by the compiler. They are not vendored now, because a
shared definition that agrees only because both sides were remembered is the
kind that stops agreeing.

## Reload against restart

The daemon binds its listening sockets once, at startup. Telling it to reload
rereads shares, users and permissions in place and does not revisit those
sockets, so a changed bind line needs the process replaced.

Before this agent existed the sidecar reloaded either way, so a container that
came up before its network did stayed bound to loopback for as long as it ran,
with a promoted configuration on disk that said otherwise.

## Building

```sh
cd go && CGO_ENABLED=0 go build ./cmd/sc-smb-agent
```

`Dockerfile.smb` builds it into the sidecar image, statically linked, and that
is how it ships. `deploy/smb/native/install.sh` installs it on a bare-metal
host.

## Running it by hand

`--once` applies what is currently rendered, prints the report and exits. It is
the operator's "apply now", and how a test harness drives this on a host with
no service manager.
