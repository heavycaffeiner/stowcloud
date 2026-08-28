# SMB 01: publishing and the agent protocol

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/smbpublish` and the protocol halves of
> `go/internal/smbagent` (`wire.go`, `client.go`, `control_linux.go`) is
> referenced as a behavioral specification only. The new implementation
> is written completely from scratch; nothing is copied.

## The two processes

The server renders configuration and credentials; the **agent**, a
separate binary (`cmd/sc-smb-agent`) beside `smbd` in its own
container, imports them into the daemon's world. They meet at two
points: a shared volume of rendered files, and a control socket the
server drives.

## Publishing (server side)

```go
func Publish(ctx context.Context, d Deps, cfg smb.Config) (smbagent.Report, error)
```

Target `engine/service/smb/publish`. `Deps` stays narrow and
function-typed (verified correct layering): `Core`, `Grants`, `Names`
as closures, `Accounts` as an interface auth satisfies. Nothing here
reaches into another service's persistence.

- Every file writes durably (`store/fsatomic`): `smb.conf`, the network
  policy, and the credential files through auth's publishers.
- `disable()` removes the set, tolerating already-absent files and
  joining partial-removal errors rather than swallowing them.
- On an agent `Apply` failure the files are already durably written,
  and the error says exactly that: the next agent poll or the next
  publish retries; nothing is rolled back.
- **The deny rule, stated as the requirement it is**: a share where the
  user holds any grant carrying a deny bit is dropped from that user's
  SMB view entirely, even where the deny does not overlap what they
  could otherwise reach. SMB grants are whole-share and additive only;
  fine-grained authority lives in the web evaluator, and an SMB render
  that approximated subtree denies would be an approximation someone
  relies on.

## The wire protocol

One request per connection over a unix socket, JSON, line-delimited:

- The client (`Do`, `Apply`, `Status`) bounds the report read at
  256 KiB and applies the context deadline to the connection. The
  agent is treated as an untrusted-ish peer.
- The agent bounds the request line with its reader's fixed buffer;
  an over-long line fails JSON parsing on the truncated read. **The
  rebuild deletes the dead length-check branch** (audit finding 3: the
  branch is unreachable because the buffered reader can never return a
  longer line) and keeps the enforcement where it really is, in the
  bounded reader, with a comment saying so.
- `Report` carries what the agent did and why; `FailedReport(reason)`
  is the refusal shape; `LogReport` renders it for the operator.

## The socket-ownership trade-off

`setSocketOwner` falls back to mode `0666` when it cannot chown the
socket to the server's uid (the capability drop forbids chown). The
audit accepts this as documented: the socket lives on a volume only
the two containers and host root reach. The rebuild keeps the
trade-off **and its documentation together**, and the threat-model
sentence ("a world-writable control socket that can trigger OpApply is
a privilege surface if the volume-isolation assumption is ever
wrong") moves into the code comment, so the assumption travels with
the code that relies on it.

## Deliberate changes

1. **The dead length-check branch is deleted** (audit finding 3), the
   real bound documented in place.
2. **The durable-write repoints** to `store/fsatomic`.
3. Nothing else: the deny rule, the narrow deps, the failure wording
   and the socket trade-off carry whole.

## Tests

- Publish round-trip (the old roundtrip test rebuilt): rendered files
  land durably, the agent is asked, the report comes back.
- Disable removes the set and reports partial failures.
- The deny rule: a user with one deny grant on a share is absent from
  that share's SMB lists.
- Client: an oversized report refuses at the bound; the deadline
  propagates to the connection.
- Agent: an oversized request line refuses (through the reader bound);
  a malformed JSON line refuses; one request per connection.
- The failure wording: an apply failure names the files as written.
