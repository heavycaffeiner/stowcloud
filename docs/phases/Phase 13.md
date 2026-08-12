# Phase 13: parity and cutover

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-17-parity-and-cutover.md`.

## Scope

The only phase permitted to state a measured number. A response differ against
the Rust build, a conformance run, real clients, the footprint remeasurement,
and the commit that deletes `crates/`.

Depends on everything.

## Milestones

- **13a**: the differ and the corpus, including the captured client session.
- **13b**: the differ run, and every difference resolved or recorded.
- **13c**: the conformance run and its baseline for both builds.
- **13d**: real clients against both builds.
- **13e**: the footprint remeasurement.
- **13f**: the jail proof across four kernel and policy combinations.
- **13g**: the cutover commit.

13c, 13d, 13e and 13f are independent of each other and of 13b.

## Traps

- **The Rust build must still work at the start of this phase.** That is the
  whole reason `crates/` survived until now. Do not delete anything before 13g.
- **The corpus is checked in, not generated at run time**, so a difference is
  reproducible and a regression is a diff.
- **The compat group is captured from a real client session**, not written.
- **The allow-list of fields permitted to differ is short and every entry needs
  a reason.** `oc:fileid` differs once, by design; the differ asserts the Go
  build's ids are **self-consistent** instead, the same identity yielding the
  same id everywhere and no two identities sharing one.
- **Body comparison is structural for JSON and canonicalised for XML.**
  Namespace prefixes may differ; resolved names may not.
- **A conformance test the Rust build also fails is a known gap**, recorded, not
  a port defect. Only a Go-only failure is a defect.
- **Two measurements are expected to regress**, and the expectation is written
  down before measuring rather than after: the worker pool, because exec'd
  processes are not copy-on-write forks, and possibly the SQLite write path.
- **A regression outside the budget sends work back to the phase that owns
  it.** Proposal 5 §4.3.1 already names its fallback so that decision is not
  improvised under time pressure.
- **Nothing found here is fixed here.** A fix made in the cutover phase has no
  test of its own. File it against the phase that owns the subsystem.
- **The cutover commit's step 4 is already done.** The proposals were promoted
  and the Rust-era ones retired before the port started. What remains is step
  6: moving every document from `Draft` to `Implemented` and rewriting its
  assumptions as statements. That is the step that is easy to skip, and it is
  the one that ends the inversion of the directory's house rule.
- **The release note records the one operator-visible consequence**: every
  `oc:fileid` changes once, so every attached sync client performs a full
  reconciliation on first contact with the new binary. It is a one-time cost
  that buys the opposite property afterwards.
- Remove the note in both readmes saying the proposals describe a rewrite
  rather than the shipping code. At this commit it stops being true.

## Done when

- The differ reports no difference outside the allow list, or each remaining
  one is recorded with a reason.
- The conformance baseline is recorded for both builds.
- A real desktop client and a real mobile client sync against the Go build.
- The footprint is remeasured against the recorded expectation, with the
  verdict stated either way.
- The jail proof passes across all four kernel and policy combinations.
- The cutover commit has landed: `crates/`, `Cargo.toml`, `Cargo.lock`,
  `deny.toml`, `rust-toolchain.toml`, `scripts/musl-env.sh` and
  `tools/zigcc-musl.ps1` deleted, `verify.sh` reduced to its Go half, the
  `Dockerfile` builder stage replaced, both readmes updated, every proposal at
  `Implemented`, and the full gate green on `HEAD` in the guest.
