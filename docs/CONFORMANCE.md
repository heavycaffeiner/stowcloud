# WebDAV conformance baseline

The standard RFC 4918 suite, run against the Go build. This is new coverage
rather than a regression check: the Rust tree lists WebDAV conformance in CI as
missing, so there was no prior baseline to compare against.

## How it was run

The suite is `litmus` 0.13, built from source on the test host. It has no TLS
support, and this server has no plaintext listener by design, so requests reach
it through `scripts/tlsproxy.go`. Proxying is the honest way round that: adding
a plaintext listener to the thing being tested would test something other than
what ships.

```sh
go build -o /tmp/tlsproxy scripts/tlsproxy.go
/tmp/tlsproxy -listen 127.0.0.1:18802 -target https://127.0.0.1:18702 -host localhost &
litmus -k http://127.0.0.1:18802/dav/<share>/ <account> <app-password>
```

The rate limit has to be raised for the run. The suite sends several thousand
requests from one address in a few seconds, and the default bound refuses most
of them, which reports as a suite failure rather than as what it is. A run
against the default bound is measuring the limiter.

## Result, 2026-08-21

| Suite | Passed | Total |
|---|---|---|
| basic | 16 | 16 |
| copymove | 8 | 13 |
| props | 29 | 30 |
| locks | 29 | 33 |
| http | 4 | 4 |
| **total** | **86** | **96** |

## What fails, and what each one is

None of these is a regression, because there is nothing to regress from. Each
is a gap in this implementation, recorded here rather than fixed, because a fix
made during a parity run has no test of its own: each belongs to the phase that
owns the subsystem.

### copymove, five failures

- **`copy_overwrite` and `move` answer 500** when the destination is an
  existing collection. A server error is the wrong answer whatever the right
  one is: the operation is refused or it succeeds, and an unhandled failure is
  neither.
- **`copy_coll` answers 404** where the destination exists and overwrite was
  asked for.
- **`move_coll` succeeds** where the suite expects a refusal.
- **`copy_shallow` answers 405** to a `MKCOL` on a path the suite has already
  created, which suggests the collection-creation path disagrees with itself
  about trailing separators.

These are one cluster: collection-to-collection copy and move with an existing
destination. Phase 7 owns it.

### props, one failure

- **`propfind_invalid2` answers 207** to a body with an invalid namespace
  declaration, where 400 is correct. The scanner accepts a document it should
  refuse. Phase 7 owns it.

### locks, four failures

- **`cond_put` and `complex_cond_put` answer 412** where the condition should
  have passed. The `If` header's evaluation is stricter than the specification
  in a case the suite exercises and this tree's own tests do not.
- **`lock_shared` answers 400** to a shared lock request, which reads as the
  scope not being accepted at all.
- **`unmapped_lock` answers 404** where a lock on an unmapped URL should create
  a locked empty resource.

Phase 7 owns all four.

## Two warnings, both the same shape

`MKCOL` with a missing intermediate collection answers 404 where the
specification says 409. The distinction is real to a client: 404 says the target
is absent and 409 says the parent is, and a client that creates parents on
demand branches on exactly that.

## The Rust baseline is not recorded yet

An earlier version of this document said the Rust build refused a password its
own database accepts. That was wrong, and the correction matters more than the
original claim.

Its login is `POST /api/auth/login`. The probe used `POST /api/auth/password`,
which is the change-password endpoint, and it refused correctly. There was no
defect to report.

Chasing it found a real one on the other side: the Go build mounts login on the
change-password path, and 39 of the 58 paths the frontend calls do not exist on
it at all. That is recorded as Q9 in `OPEN-QUESTIONS.md` and it blocks cutover.

So the per-suite numbers above are this implementation measured against the
specification, not against the implementation it replaces. A test the Rust build
also fails is a known gap rather than a port defect, and that distinction still
cannot be drawn for any of the ten failures above. The baseline is now
obtainable, since the credential path works; it has not been run.
