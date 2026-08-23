# WebDAV conformance

RFC 4918 conformance, asserted by tests in this repository rather than by an
external suite.

## Why not litmus

The first baseline was litmus, and it is not a thing this project can keep
depending on. It is an autotools program from the 2000s with no TLS support: it
needed a plaintext proxy standing in front of a server that deliberately has no
plaintext listener, and it cannot be built at all on a host without neon and
the autotools to configure it. Reproducing a run took a machine prepared for
that one purpose.

A conformance table nobody can reproduce is a table that stops being true
without anybody noticing, and this one had already done it: it recorded a
comparison against a Rust build that no longer exists in this tree.

## What replaced it

`go/internal/dav/conformance_test.go`. Each test is named after the RFC section
it enforces, with the litmus case it stands in for noted where there was one,
and asserts what the specification requires rather than what any implementation
happens to do. They run in the ordinary `go test` gate, on every push, with no
proxy, no external dependency and nothing to prepare.

They also assert on the filesystem rather than only on the status code, which
is what the old baseline could not do and what mattered most: a recursive COPY
answered 202 and copied nothing for the entire life of the port, and the suite
scored it a pass because the status was the one it expected.

## What the tests found

Everything below was fixed with the test that reproduces it. These are the
cases the old table listed as failing in both builds, so they were carried over
rather than introduced by the port.

- **COPY of a collection onto an existing collection merged into it.** RFC 4918
  9.8.4 deletes the destination first. A member the destination had and the
  source did not survived a copy that was supposed to have replaced it.

- **MOVE of a collection onto an existing collection answered 409.** The
  underlying rename cannot replace a directory that has anything in it: the
  kernel answers ENOTEMPTY. 9.9.4 deletes the destination first, the same as
  the copy above.

- **COPY and MOVE onto themselves were accepted.** A collection copied into its
  own subtree is a walk that does not terminate: each pass copies what the
  previous one wrote, and it runs until the disk is full. Both are 403 under
  9.8.4 and 9.9.4, and the copy answered 202 and the move 204.

- **A Destination naming another server was treated as local.** The header
  carries an absolute URL as often as a path, and only the path component was
  read, so a COPY to `https://elsewhere.example/dav/docs/x` copied to
  `/dav/docs/x` on this server. It is a 502 now, and the host is compared
  case-insensitively without the scheme, because a reverse proxy terminates TLS
  and forwards http.

## What was already fixed, with tests

These were the five the old table attributed to the port, each repaired at the
time with a test reproducing the request sequence.

- **`propfind_invalid2`** answered 207 to a body using an undeclared namespace
  prefix, where 400 is correct. `encoding/xml` does not report this: an
  undeclared prefix arrives with `Space` set to the prefix itself, so
  `{bar}foo` is indistinguishable from a properly declared namespace. The
  scanner tracks declarations per element and refuses a name whose prefix
  nothing bound.

- **`cond_put` and `complex_cond_put`** answered 412 where the condition should
  pass. Every file validator here is weak, because Linux exposes no inode
  change version, and the `If` evaluation refused any weak tag outright: a
  client sending back the exact validator the server had just given it was told
  its precondition failed. **Guarding a write with `If` was impossible on this
  build**, which is a good deal larger than two failing conformance tests.

- **`lock_shared`** answered 400. Shared locks are implemented rather than
  refused: two shared locks coexist, an exclusive over a shared or a shared
  over an exclusive is refused, and `lockdiscovery` reports the scope actually
  taken.

- **`unmapped_lock`** answered 404 where RFC 4918 §7.3 creates an empty locked
  resource and answers 201. This is how a client reserves a name before writing
  it, so the refusal meant a client that locks before every PUT could not
  create a file at all.

- **A recursive COPY answered 202 and copied nothing.** `StartCopy` began the
  operation with a zero source stat, which told the walker every directory was
  a file. The existing test asserted only the 202 and passed against it.

Also corrected: **MKCOL with a missing intermediate collection** answered 404
and now answers 409, which is what a client branches on to decide whether to
create the parent.

## Running them

```sh
cd go && go test ./internal/dav/ -run Conformance
```

They are part of `scripts/verify.sh`, so a push that breaks one fails the gate.
