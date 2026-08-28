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

### The access-change sink

Phase 3 wires one publisher value into core and auth through interfaces those
packages declare. Its method owns the cross-surface revocation policy:

```go
func (p *Publisher) AccessChanged(ctx context.Context) Outcome
```

It detaches from request cancellation, applies a bounded timeout, renders and
pushes synchronously, and never turns an already-committed account/grant/share
write into a reported rollback. The returned outcome says applied, warnings or
agent unreachable and updates health through an injected neutral sink. Core
and auth invoke it for every change affecting SMB access; the presentation
handler does not remember which writes need a republish.

## The wire protocol

One request per connection over a unix socket, JSON, line-delimited:

- The client (`Do`, `Apply`, `Status`) bounds the report read at
  256 KiB and applies the context deadline to the connection. The
  agent is treated as an untrusted-ish peer.
- The agent bounds the request line with a reader wrapped in an explicit
  `io.LimitReader`; an over-long line is refused by name rather than
  parsed. **The audit's finding 3 was wrong and is corrected here.** It
  called the length-check branch unreachable "because the buffered
  reader can never return a longer line". That is not how `bufio`
  behaves: the buffer size governs one fill, while `ReadString` keeps
  growing its own result until it finds the delimiter. Measured on this
  tree over a real unix socket, the old read path accumulated **64 MiB
  from a 4 KiB buffer**, and the discarded branch was the only bound
  that existed. Deleting it as the audit proposed was implemented and
  tested: the agent then read until the 5-second exchange deadline and
  answered nothing, which is an unbounded read on a socket whose whole
  vocabulary is two words. The bound is therefore made real rather than
  removed, and a truncated line is refused rather than parsed, since
  whichever prefix happens to be valid JSON is not the request the peer
  sent.
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

1. **The request bound is made real, not deleted** (correcting audit
   finding 3, which was measured wrong; see the protocol section). The
   client's report read gains the same treatment: the limit wraps the
   connection, so a peer that never sends a newline costs the bound
   rather than whatever it chooses to send.
2. **The durable-write repoints** to `store/fsatomic`.
3. **`absent` matches errno values rather than message substrings.** The
   old spelling compared libc's text, which a reworded or translated
   message breaks silently, turning an absent agent into a reported
   connection fault.
4. Nothing else: the deny rule, the narrow deps, the failure wording
   and the socket trade-off carry whole.
5. **`AccessChanged` becomes the one synchronous, detached revocation sink**
   (Phase 3 amendment), replacing three handler/server spellings and closing
   the stale-SMB-access window at the service boundary.

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
- Every core/auth access mutation invokes the sink once; browser cancellation
  does not cancel the push; agent failure returns an outcome without changing
  the committed mutation's error.
