# Phase 0: gate and toolchain

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-2-gate-and-toolchain.md`.

## Scope

Build the gate. **No product behaviour ships in this phase.** The gate and the
small rule-enforcement helpers are the deliverable, because every rule in
proposal 1 is worth what enforces it and nothing more, and a rule added after
the code it governs is a refactor.

Depends on nothing. Blocks Phases 1 and 2.

## Milestones

- **0a**: `go/go.mod`, `cmd/stowcloud` with argv dispatch and the subcommand set
  in §5-1, and the six D-rule packages (`limits`, `clock`, `task`, `secret`,
  `num`, `apierr`) carrying the helpers proposal 1 names.
- **0b**: the `golangci-lint` configuration, the two in-tree `go vet` analysers
  (`vetgo` for D7's go-statement ban, `vetsecret` for D12), the `koscan`
  source check, the Go half of `scripts/verify.sh`, and the CI job.
- **0c**: settle §4.4's four assumptions by compiling, and write each answer
  back into the document that depends on it.

0b and 0c are independent of each other.

## Traps

- **The gate lands before the code it governs.** That is the point of the
  phase. Resist writing a handler to "have something to lint".
- **A1 and A2 are about calls proposals 3 and 4 make.** If a wrapper is missing
  from `x/sys/unix`, correct those documents now rather than discovering it in
  Phase 1.
- **A3 decides whether WebP preview support is reduced.** If `x/image/webp`
  does not cover what the current build accepts, say so in proposal 12 §3.1
  rather than letting a format quietly stop working.
- **A4 is the whole test loop.** If cross-compiled test binaries will not run in
  the guest, proposal 2 §4.3.3's trade has to be reopened here, not in Phase 5.
- **Keep the Rust half of `verify.sh` intact.** It goes at Phase 13.
- All three in-tree checks need a deliberate violation in a test fixture to
  prove they reject it. A check nobody has seen fail is a check that may not
  work.
- `-trimpath` and `CGO_ENABLED=0` on every ordinary invocation, including
  tests. The D17 race step alone uses `CGO_ENABLED=1` because the detector
  refuses to run otherwise.

## Done when

- `bash scripts/verify.sh` runs its Go half and passes.
- `CGO_ENABLED=0 GOOS=linux go build ./...` succeeds for `amd64` and `arm64`.
- Both vet analysers and `koscan` reject a deliberate violation in a committed
  fixture.
- A1, A2, A3 and A4 are answered, and each answer is written into the document
  that depends on it.
- The CI job runs the Go half on every push.
