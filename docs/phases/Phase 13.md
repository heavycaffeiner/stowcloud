# Phase 13: parity and cutover

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-17-parity-and-cutover.md`.

## Scope

The only phase permitted to make product parity, footprint and end-to-end
performance claims. A response differ against the Rust build, a conformance
run, real clients, the footprint remeasurement, and the commit that deletes
`crates/`.

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
- **The Rust source inventory is closed before the response differ.** Compare
  every source database's non-`sqlite_%` `sqlite_schema` entries and every
  Stowcloud-owned SQLite filename with the import disposition manifest.
  Unknown databases, unknown tables and unclassified entries block cutover.
- **The offline lock is executable, not a runbook wish.** Migration refuses
  while either server holds `.stowcloud-instance.lock`, then succeeds after the
  server exits while holding the lock through publication.
- **The allow-list of fields permitted to differ is short and every entry needs
  a reason.** `oc:fileid` differs once, by design; the differ asserts the Go
  build's ids are **self-consistent** instead, the same identity yielding the
  same id everywhere and no two identities sharing one.
- **File ETags are an expected standards correction.** The Rust build emits a
  metadata token as strong; the Go build marks it weak because Linux exposes no
  inode change version. The differ accepts only the `W/` representation change
  for the same token and real-client tests cover the resulting `If-Match`
  behaviour.
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
  6: moving every remaining Go backend proposal from `Draft` to `Implemented`
  and rewriting its assumptions as statements. Already implemented and
  superseded documents keep their statuses. That is the step that is easy to
  skip, and it is the one that ends the inversion of the directory's house
  rule.
- **The release note records the cutover compatibility changes.** Every
  `oc:fileid` changes once, so every attached sync client performs a full
  reconciliation on first contact with the new binary. The Go filesystem gate
  also refuses network, FUSE, overlay, read-only, unknown and birth-time-less
  filesystems the Rust build may have admitted. Run that gate against every
  configured root and nested mount before stopping Rust; a refusal blocks
  cutover instead of leaving a share missing after startup. File ETags gain the
  standards-correct `W/` marker, so external native REST clients must treat an
  overwrite after 412 as an explicit unconditional retry.
- Remove the note in both readmes saying the proposals describe a rewrite
  rather than the shipping code. At this commit it stops being true.
- Update the implemented frontend proposal's embedding references from
  `rust-embed` and a Rust binary to `go:embed` and the Go binary. Its UI design
  remains implemented, but those build statements stop being true at cutover.

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
  `Dockerfile` builder stage replaced, both readmes updated, every Go backend
  proposal at `Implemented`, superseded historical documents still marked as
  such, and the full gate green on `HEAD` in the guest.
- The preflight filesystem report covers every configured root and currently
  mounted descendant, with no cutover-blocking refusal.
