# Upload - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Resumable upload in Go: TUS, the interval set that tracks which byte ranges have
landed, the ordering rule, the two spool modes, checksum verification, and the
orphan sweep. BLAKE3 stays, because a client sends it.

## 2. Background & Motivation

`sc-upload` is 2,679 lines and the part that matters is 1,449 of them in
`engine.rs`. The design is durable-by-construction: a part file under the
reserved prefix, an interval set recording what has arrived, and a finalize that
verifies and publishes atomically.

Three details from the current implementation are carried over because each was
arrived at by fixing something.

**The part file's name.** `.scpart-{id}` under the reserved prefix, produced by
the one call permitted to use it. An earlier revision disguised it as
`.{basename}.scpart-{id}` to get past component validation, which defeated the
reserved-name filter: part files showed up in ordinary listings, in the web UI
and to WebDAV clients, for the duration of every upload.

**The verify shape.** `Option<VerifyAlgo>` carried an algorithm and nothing to
compare it against, so `verify_whole_file` computed a digest and only logged it.
Verification could never fail whatever arrived on disk. The fixed shape is
`(algorithm, expected digest)`.

**Two locks per session.** The part-file handle and the bookkeeping row are
guarded separately, so the rare and brief metadata write never blocks the common
and potentially large disk write.

**A 413 is normal operation here, not an error path.** The legacy desktop
client starts at a 100 MB chunk and adjusts dynamically, and a server can
advertise a smaller maximum and still not always be honoured. Returning a
spec-correct 413 is what drives that client's own auto-adjust, so the refusal is
part of the protocol working rather than a symptom of it failing, and it is not
logged as an error or counted against an error budget. That distinction is
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S12 and it matters
in a rewrite because the natural instinct is to treat every 4xx as a fault.

**Two invariants are absolute**, and both are about not producing a half-thing:
a partial upload can never appear at the destination, and no staging file is
ever read back and rewritten on the native path. The first is why publication
is a rename and not a stream-to-destination. The second is why assembly uses
`copy_file_range` rather than a copy loop.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] TUS core plus the checksum, creation and termination extensions.
- [ ] The interval set, persisted, so a resume after a restart is exact.
- [ ] Whole-file verification that can actually fail.
- [ ] The ordering rule for compat chunking, and both spool modes.
- [ ] Part files unlistable for their whole lifetime.
- [ ] An orphan sweep that can see control files without an ambient flag (F6).
- [ ] Chunk settings that survive a restart, with "an admin set this" and "it
      fell back to the config file" distinguishable.

### 3.2 Non-Goals

- [ ] Client-side deduplication or delta upload.
- [ ] Parallel range upload of one file from one client. TUS is sequential by
      design and the interval set exists for resume, not concurrency.
- [ ] Changing the reserved prefix.
- [ ] Changing the checksum algorithm set. It is client-facing.
- [ ] **Dynamic chunk sizing.** Fixed size with parallel transfer instead.
      Memory use here is independent of chunk size, because the body flows
      through a reused 256 KiB buffer into `pwrite` and nothing buffers a whole
      chunk anywhere, so a 1 GiB chunk leaves resident memory unchanged. There
      is therefore no memory argument for dynamic sizing, and what it buys
      elsewhere, parallelism buys here more simply.
- [ ] **A chunk-size ceiling.** Not enforcing one does not make middleboxes
      disappear; it means we are not the one rejecting
      ([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S12). The
      advertised value is a recommendation, never a requirement.
- [ ] Per-chunk `fdatasync`. It would collapse throughput, and the durability
      the design actually promises is delivered at finalize.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/upload
  engine.go     the state machine, session lifecycle
  intervals.go  the interval set
  spool.go      the two modes
  verify.go     per-chunk and whole-file checksums
  sweep.go      orphan part files and expired sessions
  settings.go   the live chunk floor and default
  store.go      session rows, in state.db
```

### 4.2 Data Model Changes

`upload_session` and `upload_interval` move into `state.db`
([`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.2.2). They were in their own
database and they belong with the durable half: an interrupted upload whose
session row vanished is a part file nobody will ever finish, and that is data
loss dressed as cleanup.

The interval set is stored as rows rather than a blob, so a partially written
set is not a corrupt one.

### 4.3 Core Logic

#### 4.3.1 The interval set

A sorted, coalescing set of half-open byte ranges. Insert merges with the
neighbours; the set is complete when it holds exactly one range covering
`[0, length)`.

It is the answer to "where should I resume", and it is also the answer to
"is this file finished", which is why it is persisted rather than derived from
the part file's size: a sparse file's size says where the last write landed, not
what is in it.

Property tests (D16): inserting the same ranges in any order yields the same
set; a set that reports complete covers every byte.

#### 4.3.2 The write path

```go
// PatchAt writes buf at off in the session's part file and records the range.
// It is the only writer, and it holds the handle lock for the write and the
// row lock for the record, in that order and never nested the other way.
func (e *Engine) PatchAt(ctx context.Context, id SessionID, off uint64, r io.Reader, sum *Checksum) (uint64, error)
```

Short writes are looped over, and a zero-length write is an error rather than a
retry, because `pwrite` returning 0 with no error means the file cannot take the
bytes.

The per-chunk checksum, where the client supplied one, is verified **before** the
range is recorded. A chunk that fails its checksum leaves the interval set
untouched, so the client resends the same range rather than resuming past a hole
it thinks is filled.

#### 4.3.3 Checksums

Two algorithms, and both are client-facing: CRC32C and BLAKE3. This is the
correction the index records as C1: the parent proposal dropped BLAKE3
for SHA-256 on the grounds that every digest here is internal, and
`Checksum::Blake3` is a value a client puts in a TUS `Upload-Checksum` header.

- CRC32C is `hash/crc32` with the Castagnoli table, standard library.
- BLAKE3 is a module. The choice is made here, on one criterion: it must build
  and run with `CGO_ENABLED=0`, with any assembly path optional rather than
  required.

Whole-file verification takes `(algorithm, expected)` and fails the finalize on
a mismatch, leaving the part file in place so the client can retry rather than
discarding what it uploaded.

#### 4.3.4 Finalize

1. The interval set must be complete. If it is not, the request is refused with
   the missing ranges named.
2. Whole-file verification if requested, read through the same handle that wrote
   the part file. This is the one caller allowed `IntentReadWrite` from
   [`stowcloud-3`](stowcloud-3-vfs-and-paths.md) §4.3.4, and it is the reason
   that intent exists.
3. Publish through the durable-write helper: mode and ownership restored,
   `fdatasync`, `renameat2`, parent `fsync`.
4. Invalidate the directory's generation.
5. Delete the session row in the same transaction that records the publish.

A failure at any step leaves the session resumable. Nothing deletes a part file
except an explicit termination or the sweep.

#### 4.3.5 Spool modes

Two, and the difference is the ordering rule. Direct mode writes each chunk to
its offset in the part file. Name-ordered spool mode writes each chunk to its
own file and assembles them at finalize in name order, which is what the compat
layer's chunked upload v2 needs because it does not carry offsets.

Assembly uses `copy_file_range` through the vfs helper, never a userspace
buffer, so a 50 GiB file does not pass through this process's heap. The source
length comes from a `stat` rather than an oversized sentinel: some kernels
reject an implausible length with `EINVAL`, which is not one of the fall-back
errnos and correctly so, because an `EINVAL` here would be a real bug.

The spool mode is a property of the session, chosen by the protocol that created
it, and `internal/upload` does not know which protocol that was. The mode is
named for what it does (`SpoolNameOrdered`), not for the client that needs it,
which is principle 4 holding at a place where it would be easy to break.

#### 4.3.6 The sweep

Orphaned part files and expired sessions. It is one of the two callers that must
see reserved names, and it says so at the call site now (F6):

```go
err := root.ReadDirFunc(p, vfs.IncludeReserved, func(e vfs.DirEntry) bool { ... })
```

A part file with no session row older than the grace period is deleted. A
session row with no part file is deleted. Neither is inferred from the other's
absence in a single pass: the sweep reads both first, then acts, so a session
created between the two reads is not mistaken for an orphan.

#### 4.3.7 Chunk settings

The live floor and default are seeded from the configuration file and overridden
by an admin value persisted in `state.db`, so a restart does not lose it. A
separate flag records whether the row exists, because collapsing the two makes
"an admin stored this" and "it fell back to the file" the same pair of numbers,
and the settings screen has to report which.

## 5. API Design

### 5-1. New / Modified

```go
package upload

// Create opens a session. length may be unknown (TUS deferred length), in
// which case the interval set cannot report completeness and finalize requires
// an explicit length first.
func (e *Engine) Create(ctx context.Context, r core.Resolved, spec SessionSpec) (Session, error)

// Offset reports the resumable offset: the end of the first range if the set
// starts at zero, and zero otherwise. A client that asks for this after a
// failed chunk gets the truth rather than the part file's size.
func (e *Engine) Offset(ctx context.Context, id SessionID) (uint64, error)

// Finalize verifies and publishes. On any failure the session stays resumable
// and the part file stays on disk: discarding an upload because its last step
// failed is data loss, not cleanup.
func (e *Engine) Finalize(ctx context.Context, id SessionID, verify *Verify) (core.Entry, error)
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 400 | malformed offset, unknown checksum algorithm, deferred length never supplied |
| 404 | unknown session, or one belonging to another user |
| 409 | offset does not match the resumable offset |
| 412 | the destination changed since the session was created |
| 413 | over the declared length, or over a D5 limit |
| 415 | `Upload-Checksum` names an algorithm this server does not offer |
| 422 | a chunk failed its checksum; the range is not recorded |
| 460 | whole-file verification failed at finalize (TUS checksum-mismatch convention) |

| Error | Meaning |
|---|---|
| `ErrIncomplete` | finalize with holes; the error names the missing ranges |
| `ErrChecksum` | per-chunk mismatch |
| `ErrVerify` | whole-file mismatch; the part file is kept |
| `ErrSessionExpired` | past its lifetime; the sweep may already have taken the part file |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 6a | `intervals.go` and its property tests | S | Phase 0 | heavycaffeiner |
| Phase 6b | `store.go`, `engine.go`: lifecycle, the write path, the two locks | M | 6a, Phase 4 | heavycaffeiner |
| Phase 6c | `verify.go`, and the BLAKE3 module decision | S | 6b | heavycaffeiner |
| Phase 6d | `spool.go`: both modes, assembly by `copy_file_range` | M | 6b | heavycaffeiner |
| Phase 6e | `sweep.go`, `settings.go` | S | 6b | heavycaffeiner |

6a can start immediately; it depends on nothing but the limits package.

### 6-2. Dependencies

| Module | Used for |
|---|---|
| a pure-Go BLAKE3 | the client-facing checksum, shared with the directory ETag |

CRC32C is standard library.

## 7. References

- `crates/sc-upload/src/engine.rs`, `interval_set.rs`, `model.rs`, `db.rs`:
  the engine this translates.
- `crates/sc-upload/src/engine.rs:1-18`: the part-file naming history quoted in
  §2.
- `crates/sc-upload/src/model.rs:128-136`: the verify shape that could never
  fail, and the fix.
- `crates/sc-upload/src/engine.rs:93`: why the copy length is stat'd rather
  than sent as a sentinel.
- [`stowcloud-3-vfs-and-paths.md`](stowcloud-3-vfs-and-paths.md) §4.3.5: the
  durable-write helper finalize publishes through.
- TUS 1.0.0 core, plus the creation, checksum, termination and
  concatenation-free extensions this server implements.
