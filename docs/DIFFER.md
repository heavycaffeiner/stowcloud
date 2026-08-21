# The response differ

Both binaries, one share set, one checked-in corpus, 96 requests. Run:

```sh
go build -o /tmp/differ ./go/tools/differ
/tmp/differ -old https://localhost:19001 -new https://localhost:19002 \
  -host localhost -corpus go/tools/differ/corpus \
  -old-cookie "__Host-sc_sid=..." -new-cookie "__Host-sc_sid=..." \
  -old-csrf "..." -new-csrf "..."
```

The credential is per side. A session minted against one build is not valid on
the other, and one value for both sends each side something it ignores, so
every request then compares two unauthenticated answers, which match. That is
the shape of a differ that reports success while comparing nothing.

The rate limit has to be raised on both. The corpus sends ninety-six requests
from one address in a few seconds and the defaults refuse most of them, which
reports as a difference in every row rather than as what it is.

## What the run found, and what was fixed

Five defects, each only visible by comparing two implementations.

- **The audit log answered a server error.** Three of its columns are optional
  and were read into plain strings, so any deployment with a row missing an
  address or an agent could not open the screen. The same class as the display
  name, in a second place.
- **A request body past the limit answered a server error.** The limiter
  refuses by wrapping the reader, so its refusal arrived as a read error that
  nothing mapped: the client was told the server broke when it was the client
  that sent too much. It answers the status that names the limit now.
- **An unsatisfiable range answered success with the whole file.** The header
  was advertised as supported and ignored, so a client resuming a download was
  handed the file from the start with a success status, which it reads as the
  server restarting the transfer. Ranges are parsed now, including the suffix
  form, with the refusal carrying the file's real size.
- **A duplicate group name answered a server error**, because the database's
  constraint reached the client raw.
- **Login was mounted on the change-password path**, which is what the first
  run of this differ found and is recorded with the rest of the route work.

## What still differs

Twenty status differences across ninety-six requests, all of them one refusal
against a different refusal. None is a success against a failure.

| Shape | Count | What it is |
|---|---|---|
| a validation refusal, numbered differently | 8 | one build answers 422 where the other answers 400 or 404, and both refuse |
| a conflict, numbered differently | 3 | 409 against 400, 404 and 413 |
| absent against a bounded refusal | 3 | a name at and past the length bound, and the recovery-code count |
| the rest | 6 | one-off pairs, each a refusal against a refusal |

These are not resolved here. A status vocabulary is the surface's own contract
and moving one to match the other is a change to what every client sees, which
belongs to the phase that owns the surface rather than to a comparison run.

## The header differences are not defects

Thirty-one responses carry security headers on the Go build that the Rust build
does not send at all, and the content policy differs on the WebDAV mount: the
Rust build sends its sandboxing policy there and the Go build sends the
application's.

The first is the Go build being stricter. The second is worth looking at: a
document served from the file protocol is user content, and the stricter policy
is the right one for it. That is recorded as a difference rather than fixed,
for the same reason as the statuses.

## The allow list

Short by design, and each entry carries its reason: the wall clock, the
per-request id, the server's own name, a fresh session token, the framing
headers whose bytes the body comparison already checks, and the connection
decisions. The change token may gain the marker that says it is advisory and
may not otherwise change.
