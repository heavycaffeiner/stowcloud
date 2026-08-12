# Parity and cutover - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The last phase, and the only one allowed to claim a number. A response differ
that runs both binaries against one share set, a WebDAV conformance run, a real
sync client against both, the footprint remeasurement, and then the commit that
deletes `crates/` and amends the architecture document.

## 2. Background & Motivation

A big-bang rewrite has one structural weakness: there is no intermediate point
where a real client proves the new implementation. Every other phase ends green
on its own tests, and its own tests were written by the same person who wrote
the code they test.

This phase exists to be the thing that was not written by that person. Three of
its four checks compare against an artefact that already exists and was not
written for this purpose: the Rust binary's actual responses, a conformance
suite from outside this repository, and a sync client from outside this
repository.

The fourth is the footprint measurement, and it is the one that can send work
back. `docs/proposals/stowcloud-11-footprint.md` sets a 32 GB and 12 TB floor
from two findings that changed the schema, and three decisions in this port
touch it: the pure-Go SQLite driver, the exec'd worker pool (which costs more
resident memory than copy-on-write forks), and Go's garbage collector.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] A differ that runs a request corpus against both binaries and reports
      every difference in status, headers and body.
- [ ] Every difference either eliminated or recorded as intended, with a reason.
- [ ] A WebDAV conformance run, which the current tree lists as missing.
- [ ] A real desktop client and a real mobile client syncing against the Go
      build.
- [ ] The footprint remeasured on the Go build, against the same tree.
- [ ] The jail proof passing on real kernels for both architectures.
- [ ] The cutover commit: `crates/` deleted, the architecture document amended,
      `verify.sh` reduced to its Go half.

### 3.2 Non-Goals

- [ ] A performance improvement claim. Parity or a recorded regression, and
      nothing else.
- [ ] Fixing anything found here in this phase. A finding goes back to the
      phase that owns it, because a fix made in the cutover phase has no test
      of its own.
- [ ] A migration path for someone running a Rust build in production. The
      `stowcloud migrate --from-rust` subcommand from
      [`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.4 is the whole of it.
- [ ] Keeping the Rust tree available on a branch after cutover. Git history is
      that.

## 4. Technical Design

### 4.1 Architecture Overview

```
go/tools/differ/
  corpus/       the request corpus, checked in
  run.go        drive both binaries, capture, compare
  normalise.go  the fields allowed to differ, each with a reason
  report.go     the diff
```

Both servers run against the same share set, from the same `data/` shape, on
different ports, in the Linux VM.

### 4.2 Data Model Changes

None. `state.db` is produced from the Rust databases by the migrate subcommand
and both servers then read their own store, which is why the differ compares
responses rather than files.

### 4.3 Core Logic

#### 4.3.1 The corpus

Checked in, not generated at run time, so a difference is reproducible and a
regression is a diff. It covers:

| Group | Contents |
|---|---|
| REST | every route in the table, with valid, invalid and boundary inputs |
| WebDAV | `PROPFIND` at every depth, `PROPPATCH`, `LOCK`, `UNLOCK`, `COPY`, `MOVE`, `MKCOL`, with hostile XML bodies |
| Compat | the mounts a client actually calls, captured from a real client session |
| Auth | login, second factor, app password, expired session, unknown user |
| Errors | one request per row of every error table in these documents |
| Limits | one request per D5 bound, at the bound and one past it |

The compat group is captured rather than written, by proxying a real desktop
client session against the Rust build and recording it. A guess about what a
client sends is exactly the thing this phase exists to stop trusting.

#### 4.3.2 What is allowed to differ

Everything else is a failure. Each entry needs a reason, and the list being
short is the point:

| Field | Why |
|---|---|
| `Date` | wall clock |
| `Sc-Trace` and the envelope's `trace` | a per-request id |
| `ETag` where the algorithm changed | it did not; the hash is unchanged (folder README C1), so an ETag difference here is a real failure |
| `Server` | if it is sent at all |
| Session cookie values | new tokens |
| JSON key order | Go's `encoding/json` orders struct fields by declaration; the comparison is structural, not textual |
| `oc:fileid` | only if [`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.5 chose the non-stable answer, in which case it is recorded here and in the operator documentation |

Response header order is compared as a set, not a sequence. Body comparison is
structural for JSON and canonicalised for XML: namespace prefixes may differ,
resolved names may not.

#### 4.3.3 Conformance

`litmus` is the standard RFC 4918 suite. The current tree lists WebDAV
conformance in CI as missing, so this is new coverage rather than a
regression check, and its first run establishes a baseline for both binaries.

A test that the Rust build also fails is recorded as a known gap rather than
treated as a port defect. A test that only the Go build fails is a port defect.

#### 4.3.4 Real clients

A desktop sync client and a mobile client, each against both builds:

1. Initial sync of a populated share.
2. A change made on the server, seen by the client.
3. A change made by the client, seen on the server and by another client.
4. A conflict, produced deliberately.
5. A large file, interrupted and resumed.
6. A share link, opened by a browser with no account.

The Android "synced" tick is expected to appear in both, for the reason
`docs/README.md` records, and its appearance is not a defect.

#### 4.3.5 The footprint remeasurement

Against `docs/proposals/stowcloud-11-footprint.md`'s workload, on the same
tree, in the VM:

| Measure | Compared against |
|---|---|
| resident memory, idle | the Rust build |
| resident memory, cold walk of the full tree | the Rust build |
| resident memory, N preview workers busy | the Rust build, where the exec'd pool is expected to cost more |
| cold `node` population time | the Rust build, against §4.3.1's threshold in [`stowcloud-5`](stowcloud-5-store-and-schema.md) |
| steady-state ETag invalidation rate | the Rust build |
| image size | 30 MB |

Two are expected to regress and the expectation is written down before the
measurement rather than after: the worker pool, because processes are not forks,
and possibly the SQLite write path, because the driver is a translation. A
regression inside the budget is recorded. A regression outside it sends work
back to the phase that owns it, and
[`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.3.1 already names its
fallback so that decision is not improvised under time pressure.

#### 4.3.6 The jail proof on real kernels

The probe test from [`stowcloud-4`](stowcloud-4-jail-and-hardening.md) §4.3.6
run on `amd64` and `arm64`, on a kernel with Landlock and on one without, under
`hardening = "required"` and `preferred`. Four combinations, and the one that
matters most is `required` on a kernel without Landlock, which must refuse to
start.

### 4.4 The cutover commit

One commit, and the order inside it matters only in that it should be readable:

1. Delete `crates/`, `Cargo.toml`, `Cargo.lock`, `deny.toml`,
   `rust-toolchain.toml`, `scripts/musl-env.sh`, `tools/zigcc-musl.ps1`.
2. Reduce `scripts/verify.sh` to its Go half.
3. Replace the builder stage in `Dockerfile`.
4. Amend `docs/proposals/stowcloud-12-architecture.md`: §4.1's crate layout
   becomes the package layout, and §4.2's backend row stops saying Rust was
   chosen over "a GC'd runtime, where the syscall contract is someone else's".
   The replacement row states what actually changed, which is the three things
   in `../stowcloud-22-go-backend.md` §2.3 and not the syscall contract.
5. Update `README.md` and `README.ko.md`'s build instructions.
6. Move this folder's documents from `Draft` to `Implemented`, and rewrite each
   one's assumptions into statements, because at that point they describe code
   that exists and the directory's own convention applies again.

Step 6 is the one that is easy to skip and is the reason the folder README says
these documents invert the house rule "by necessity". The necessity ends here.

## 5. API Design

### 5-1. New / Modified

```go
// Package main, go/tools/differ. Not shipped, not in the binary.

// Run drives every request in the corpus against both servers and reports
// differences. A field in the allow list is normalised before comparison; a
// difference anywhere else is a failure with the request, both responses and
// the diff.
func Run(corpus string, rustAddr, goAddr string) (Report, error)
```

### 5-2. Error Handling

This phase produces a report, not responses.

| Outcome | Meaning |
|---|---|
| difference | a request where the two builds disagree outside the allow list |
| Go-only failure | a conformance or client case only the Go build fails |
| shared failure | a case both fail; a known gap, recorded, not a port defect |
| regression | a measurement outside the recorded expectation |

Each is filed against the phase that owns the subsystem. None is fixed here.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 13a | The differ and the corpus, including the captured client session | M | Phases 5, 7, 10 | heavycaffeiner |
| Phase 13b | The differ run, and every difference resolved or recorded | L | 13a | heavycaffeiner |
| Phase 13c | The conformance run, and its baseline for both builds | M | Phase 7 | heavycaffeiner |
| Phase 13d | Real clients against both builds | M | Phase 10 | heavycaffeiner |
| Phase 13e | The footprint remeasurement | M | all | heavycaffeiner |
| Phase 13f | The jail proof across four kernel and policy combinations | S | Phases 1, 9 | heavycaffeiner |
| Phase 13g | The cutover commit, including step 6 | S | 13b, 13c, 13d, 13e, 13f | heavycaffeiner |

13c, 13d, 13e and 13f are independent of each other and of 13b.

### 6-2. Dependencies

**Tools**: `litmus` for conformance, a desktop sync client, a mobile client,
and the Rocky VM with enough disk for a representative tree.

**Non-code dependency**: the Rust build has to still work at this point. That is
the argument for keeping `crates/` in the tree until the cutover commit rather
than deleting it when the Go equivalent lands, and it is why the folder layout
in [`stowcloud-2`](stowcloud-2-gate-and-toolchain.md) §4.1.1 has both.

## 7. References

- `docs/proposals/stowcloud-11-footprint.md`: the workload and the budget
  §4.3.5 measures against.
- `docs/proposals/stowcloud-12-architecture.md` §4.2: the row §4.4 amends.
- `docs/README.md`, "Known client behaviour": the Android tick that is expected
  in both builds.
- `README.md`, "Status": WebDAV conformance in CI and an automated sync-client
  regression suite, both listed as missing, and both partly closed here.
- [`stowcloud-5-store-and-schema.md`](stowcloud-5-store-and-schema.md) §4.3.1
  and §4.5: the threshold that can send work back, and the fileid decision that
  changes what §4.3.2 allows to differ.
