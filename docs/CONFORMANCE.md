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

### Only the Go build failed: five port defects, all fixed

`propfind_invalid2`, `cond_put`, `complex_cond_put`, `lock_shared`,
`unmapped_lock`. Each is repaired with a test that reproduces the suite's own
request sequence, taken from litmus 0.18's source rather than from the failure
name.

- **`propfind_invalid2`** answered 207 to a body using an undeclared namespace
  prefix, where 400 is correct.

  `encoding/xml` does not report this at all: an undeclared prefix arrives with
  `Space` set to the prefix itself, so `{bar}foo` is indistinguishable from a
  properly declared namespace. The scanner now tracks declarations per element
  and refuses a name whose prefix nothing bound. A prefix declared on any
  ancestor is bound, and `xml:` and `xmlns:` need no declaration.

- **`cond_put` and `complex_cond_put`** answered 412 where the condition should
  pass, and the cause was worse than the tests suggested.

  Every file validator here is weak, because Linux exposes no inode change
  version, and the `If` evaluation refused any weak tag outright. The suite
  reads the `ETag` header with a HEAD and echoes it back, so a client sending
  the exact validator the server had just given it was told its precondition
  failed. **Guarding a write with `If` was impossible on this build**, which is
  a good deal larger than two failing conformance tests.

  The comparison now requires the tags to be equal and to agree on strength.

- **`lock_shared`** answered 400, which reads as the scope not being understood.

  Shared locks are implemented rather than refused. The `scope` column already
  existed and was always written as exclusive. Two shared locks coexist; an
  exclusive one over a shared lock, or a shared one over an exclusive lock, is
  refused; and `lockdiscovery` reports the scope that was actually taken rather
  than the word "exclusive".

- **`unmapped_lock`** answered 404 where RFC 4918 §7.3 creates an empty locked
  resource and answers 201.

  This is how a client reserves a name before writing it, so the refusal meant
  a client that locks before every PUT could not create a file at all.

One more defect was found while fixing these, by a test that checked the files
rather than the status code:

- **A recursive COPY answered 202 and copied nothing.** `StartCopy` began the
  operation with a zero source stat, which told the walker that every directory
  was a file, so the copy took the single-file path and failed after the caller
  had already been told it started. Every collection COPY over WebDAV produced
  nothing for the whole of the port. The existing test asserted only the 202
  and passed against it.

Also corrected: **MKCOL with a missing intermediate collection** answered 404
and now answers 409, which was recorded below as a warning rather than a
failure. The distinction is what a client branches on to decide whether to
create the parent.

### Only the Rust build fails: four the port fixed

`copy`, `copy_simple`, `propget`, `propmove`. The Rust build emits a document
the suite cannot parse (`invalid namespace declaration`) and answers 502 on a
property-carrying move. The Go build passes all four.

### The copymove cluster, as measured

The five both builds failed, in the run above:

- **`copy_overwrite` and `move` answered 500** when the destination is an
  existing collection. A server error is the wrong answer whatever the right
  one is: the operation is refused or it succeeds, and an unhandled failure is
  neither.
- **`copy_coll` answered 404** where the destination exists and overwrite was
  asked for.
- **`move_coll` succeeded** where the suite expects a refusal.
- **`copy_shallow` answered 405** to a `MKCOL` on a path the suite has already
  created.

The recursive-copy defect above sits underneath at least the first three:
`copy_coll` copies a collection and then deletes each member to check it
arrived, and no member ever arrived. The `MKCOL` status correction touches
`copy_shallow`. How much of the cluster that leaves is what the re-run measures;
what remains is Phase 7's.

## The re-run this needs

The fixes above are verified by tests that reproduce each suite case's own
request sequence, read out of litmus 0.18's source. They are not verified by
litmus itself: this machine has neither the suite nor the autotools and neon
headers to build it, and no way to install them.

So the table above is the last measured run, and the section on the defects is
what the tree now does. Re-run the suite on a host that has it, and replace the
table rather than adding to it. What to expect: five Go-only failures gone, and
the collection copy and move cluster changed by the recursive-copy fix, which
is the one repair here that alters behaviour both builds got wrong.

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
the paths its own client calls were not mounted at all. That is Q9 in
`proposals/OPEN-QUESTIONS.md`, which is now closed: the routes were built and
`go/tools/routecheck` is the gate that says so, comparing the client's calls
against the route table on every run.
