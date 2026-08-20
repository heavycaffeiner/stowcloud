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

Both builds, same host, same share, same suite.

| Suite | Go | Rust |
|---|---|---|
| basic | 16 / 16 | 16 / 16 |
| copymove | 8 / 13 | 7 / 13 |
| props | 29 / 30 | 28 / 30 |
| locks | 29 / 33 | 40 / 41 |
| http | 4 / 4 | 4 / 4 |
| **total** | **86 / 96** | **95 / 103** |

The lock suite runs more tests against the Rust build because the suite stops
early on a failure in that group, so the two totals are not directly
comparable. What is comparable is which named tests fail, below.

## What fails, and which of those are port defects

With both baselines, the distinction the phase document asks for can be drawn.
A test both builds fail is a known gap carried over rather than something this
port broke. A test only the Go build fails is a port defect.

Nothing below is fixed here. A fix made during a parity run has no test of its
own, so each belongs to the phase that owns the subsystem.

### Both builds fail: five carried-over gaps

`copy_coll`, `copy_overwrite`, `copy_shallow`, `move`, `move_coll`.

All five are collection-to-collection copy and move with an existing
destination. The Rust build fails them too, so this is a gap in the product
rather than in the port. The Go build additionally answers 500 on two of them
where the Rust build answers a refusal, and a server error is worse than a
wrong status: that part is the port's.

### Only the Go build fails: five port defects

`propfind_invalid2`, `cond_put`, `complex_cond_put`, `lock_shared`,
`unmapped_lock`.

- **`propfind_invalid2`** answers 207 to a body with an invalid namespace
  declaration where 400 is correct. The Rust build refuses it. The scanner
  accepts a document it should reject.
- **`cond_put` and `complex_cond_put`** answer 412 where the condition should
  pass. The `If` header evaluation is stricter than the specification in a case
  this tree's own tests do not cover.
- **`lock_shared`** answers 400 to a shared lock request, which reads as the
  scope not being accepted at all.
- **`unmapped_lock`** answers 404 where a lock on an unmapped URL should create
  a locked empty resource.

Phase 7 owns all five.

### Only the Rust build fails: four the port fixed

`copy`, `copy_simple`, `propget`, `propmove`. The Rust build emits a document
the suite cannot parse (`invalid namespace declaration`) and answers 502 on a
property-carrying move. The Go build passes all four.

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

## A correction worth keeping

An earlier version of this document said the Rust build refused a password its
own database accepts, and that its baseline therefore could not be measured.
That was wrong.

Its login is `POST /api/auth/login`. The probe used `POST /api/auth/password`,
which is the change-password endpoint, and it refused correctly. There was no
defect to report, and the missing column above was the measurement's fault
rather than the subject's.

Chasing it found a real defect on the other side: the Go build mounted login on
the change-password path, so the shipped interface could not sign in, and 41 of
the paths its own client calls are not mounted at all. That is Q9 in
`OPEN-QUESTIONS.md`, and it is what blocks cutover.
