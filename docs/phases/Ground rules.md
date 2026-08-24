# Ground rules

Read this in full before every phase. It does not change between them.

## The specification

`docs/proposals/` is the contract. Read `docs/README.md`, then proposal 0 and
proposal 1 in full, before writing a line.

`crates/` is the current Rust implementation. It is the reference for behaviour
the documents leave open, and it must keep building until Phase 13's cutover
commit deletes it.

**Where a document and the code disagree, the document wins**, with one
exception: proposal 0 §4.3 lists fourteen findings F1..F14 that are known Rust
defects the plan fixes on purpose. Do not port those.

The proposals have been audited three times against the Rust tree, including
once by a panel of agents that found nine things the documents had wrong. They
are not infallible. They are still the contract.

## Rules that are not negotiable

1. **`CGO_ENABLED=0`** on every shipping build and every ordinary test. It is
   the whole static-binary story and it is what constrains the SQLite driver
   choice. D17's `go test -race` is the sole exception: the detector requires
   cgo and produces a test binary that never ships.
2. **D1 through D20 in proposal 1 are enforced by lints and tests, never by
   care.** A rule with no mechanism is not a rule. Build the mechanism.
3. **Never claim product parity, footprint or end-to-end performance.** Phase
   13 owns those claims. A phase may record a predeclared engineering decision
   benchmark, such as Phase 2's SQLite driver threshold, without presenting it
   as product performance.
4. **A phase is not done while a finding it owns is open.** Proposal 0 §6-1
   maps findings to phases.
5. **Tests touching Linux syscalls do not run on the Windows host.** Use
   `go test -c` and run the binary in the Rocky guest. Never weaken a test so
   it passes on Windows, and never build a portable backend to make one pass;
   proposal 2 §4.3.3 records the two bugs that hid behind exactly that.
6. **Commit per milestone.** Conventional Commits, imperative subject, the body
   explains why rather than what. Never add a Claude or Anthropic attribution
   trailer. Verify `git config user.name` and `user.email` before the first
   commit of a session.
7. **English in every artefact**: code, comments, identifiers, log strings,
   commit messages, documentation. No em dash and no middle dot, anywhere.
8. **If you delegate**: at most three agents at once, never nested. Tell each
   one it may not spawn its own and that its final message must carry the
   result rather than a status report.

## When the plan is wrong

You will find places where it is incomplete, ambiguous, or disagrees with the
Rust tree. Three shapes, three responses:

- **A gap you can close from the Rust source.** Close it, and add the sentence
  the document was missing, in the same commit.
- **A contradiction.** Fix the document first, in its own commit, stating the
  evidence. Then implement against the fixed document.
- **A decision only the maintainer can make.** Append it to
  `docs/proposals/OPEN-QUESTIONS.md` with the options and what decides between
  them, take the one the surrounding documents imply, say in the commit body
  that you did, and keep going. Do not stall waiting for an answer.

Never silently implement something the documents do not describe. Never widen a
D5 limit, weaken a gate, or skip an executable proof because it is
inconvenient. If a proof is hard to write, that is usually the proof earning
its place.

## Reporting

At the end of the phase, report: what landed, what the gate says, what went
into `OPEN-QUESTIONS.md`, what you changed in the proposals and why, and which
phase is next.
