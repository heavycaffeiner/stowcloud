# Phase 6: upload

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-9-upload.md`.

## Scope

TUS, the interval set, the ordering rule, the two spool modes, checksum
verification, the orphan sweep.

Depends on Phase 4. Blocks Phases 7, 10 and 12. Independent of Phases 8 and 9.

## Milestones

- **6a**: `intervals.go` and its property tests.
- **6b**: `store.go`, `engine.go`: lifecycle, the write path, the two locks.
- **6c**: `verify.go`, reusing the BLAKE3 module chosen in Phase 4.
- **6d**: `spool.go`: both modes, assembly by `copy_file_range`.
- **6e**: `sweep.go`, `settings.go`.
- **6f**: state migration and Rust-import extension for upload aliases and
  chunk settings.

6a needs nothing but the `limits` package and can start immediately.

## Traps

- **The interval set is the phase's foundation.** Property tests: inserting the
  same ranges in any order yields the same set, and a set reporting complete
  covers every byte.
- **It is stored as rows, not a blob**, so a partially written set is not a
  corrupt one. It is the answer to "is this finished", not the part file's
  size, because a sparse file's size says where the last write landed.
- **A per-chunk checksum is verified before the range is recorded.** Recording
  first means a failed chunk leaves a hole the client resumes past.
- **BLAKE3 is client-facing.** It is a TUS `Upload-Checksum` algorithm a client
  sends, so it cannot be swapped for a standard-library hash. Reuse the
  pure-Go module introduced for directory ETags in Phase 4.
- **Whole-file verification takes `(algorithm, expected)`.** An algorithm with
  nothing to compare against is the shape that shipped once and could never
  fail.
- **A failure before publish leaves the session resumable and the part file on
  disk.** The durable rename is the commit point. Failures after it are
  invalidation or cleanup debt and must never be reported as a resumable upload
  whose destination already exists.
- **Assembly uses `copy_file_range` with a stat'd length.** Never a userspace
  buffer, and never an oversized sentinel: some kernels reject an implausible
  length with `EINVAL`, which is not a fall-back errno and correctly so.
- **The spool mode is named for what it does**, not for the client that needs
  it. `SpoolNameOrdered` is the boundary holding under real tension.
- **A 413 is normal operation**, not an error path. It is what drives the
  desktop client's own auto-adjust, so do not log it as a fault or count it
  against an error budget.
- **The sweep reads both sides before acting**, so a session created between
  the two reads is not mistaken for an orphan.
- **The chunk floor distinguishes "an admin set this" from "it fell back to the
  config file".** Collapsing them loses what the settings screen has to report.
- **Extend the Rust importer with upload aliases and chunk settings.** An alias
  is what makes a named chunk collection resumable after cutover. The touched
  directories come across as well: that table is the orphan sweep's record of
  where a part file may be, not cache-invalidation debt, and an orphan is by
  definition one whose session row is already gone.
- The part file's handle is the one holder of `IntentReadWrite` in the tree.

## Done when

- The gate is green, including `-race`.
- The interval-set property tests pass.
- A finalize with a deliberately wrong whole-file digest fails and leaves the
  session resumable with the part file intact.
- A chunk failing its checksum leaves the interval set untouched.
- The Rust importer preserves active upload aliases, chunk-setting intent and
  the touched-directory set the orphan sweep reads.
