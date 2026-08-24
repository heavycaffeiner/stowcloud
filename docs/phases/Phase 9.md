# Phase 9: preview and the jailed worker

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-12-preview.md`, and
`docs/proposals/stowcloud-4-jail-and-hardening.md` §4.3.2 and §4.3.6.

## Scope

Thumbnail generation, the parent-side pool, the wire protocol, decode limits,
EXIF stripping, the cache, archive listing. **And the jail's evidence**: 9g and
9h are specified in proposal 4 and scheduled here, because both need a decoder
to exercise.

Depends on Phase 1 for the jail and Phase 4 for the service. Blocks Phase 10.
Independent of Phases 6, 7 and 8.

## Milestones

- **9a**: `wire.go` and its fuzz target.
- **9b**: `decode.go`, `exif.go`, `preset.go`, and the re-derived limits.
- **9c**: `worker.go`: the jail sequence, then the loop.
- **9d**: `pool.go`: exec, dispatch, reap, replace.
- **9e**: `service.go`, `cache.go`, `sniff.go`.
- **9f**: `ListArchive` and its fuzz target. Independent of everything else here.
- **9g**: the `SECCOMP_RET_LOG` corpus run that produces the worker allow-list.
- **9h**: the jail proof, on a real kernel.

## Traps

- **The wire codec is a fixed layout over `encoding/binary`.** Not `gob`, not
  JSON. The peer is the least trusted process in the system and a reflective
  decoder allocates on what that peer sends. A message that does not parse
  exactly kills the job and the worker.
- **Exactly two descriptors arrive with each job.** A different count is the
  same fatal case.
- **The worker is never told a path.** `openat` is not on the allow-list and the
  Landlock domain grants nothing, so a path-traversal bug in a decoder has
  nothing to traverse. Do not add a path field "for logging".
- **Re-derive `DecodeLimits` for Go's decoders.** `image/png` and the Rust
  `image` crate do not allocate the same way, so a limit tuned for one is a
  guess for the other.
- **Use `gif.Decode`, never `gif.DecodeAll`.** DecodeConfig supplies the logical
  screen bounds for the pre-decode limit, and Decode stops after the first
  frame instead of materialising the animation.
- **The graceful limit fires before `RLIMIT_AS` for the common case.** A 40,000
  by 40,000 PNG is an ordinary thing to find in a photo library, and killing a
  worker for it is a bad trade.
- **Orientation is applied to the pixels and then discarded.** Dropping it
  without applying it turns every portrait photo sideways.
- **Worker replacement is lazy, not eager**, or a source that reliably kills
  workers becomes a fork bomb.
- **`ErrTooLarge` and `ErrWorkerDied` are cached negatively with different
  lifetimes.** A file too large now will be too large next time; a worker death
  might have been the machine.
- **Archive listing runs in the parent**, reads the central directory only,
  extracts nothing, and is fuzzed. Putting it in the worker would mean passing
  a whole archive across the socket.
- **Video answers an honest "not implemented"** over the wire, and the negative
  result is cached so it is asked once.
- **9g's allow-list comes from measurement**, never from guessing: run the
  worker under `SECCOMP_RET_LOG` against a real image corpus, read the audit
  log, and commit the list with the corpus that produced it. Roughly 27 calls
  is the expectation, since nine of the fourteen a Go runtime needs are already
  on the Rust list. **Much larger than 27 is a finding to stop on**, not to
  absorb.
- **9h is where the jail stops being a claim.** Every probe must report refused
  or killed; a `Succeeded` is a test failure.

## Done when

- The gate is green, including `-race`.
- The probe test passes on a real kernel with every probe refused or killed.
- The measured allow-list is committed together with the corpus that produced
  it, and its size is compared against the 27-call expectation in the commit
  body.
- A crafted input that kills a worker costs one thumbnail and leaves the pool
  serving.
