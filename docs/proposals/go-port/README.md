# Go port

Replacing the Rust backend with a Go one, in one cutover.
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) is the decision, the
shape of the plan and the findings that motivate it; everything after it is one
phase.

There is no separate overview document. There was one, and folding it back into
this folder removed the class of problem the ledger below records: a
program-level claim living far enough from the code it describes that nothing
forced it to be checked.

Every one of them is `Status: Draft`, and unlike the rest of `docs/proposals/`
they describe code that does not exist. The house rule there ("written from what
is built, not from what was planned") is inverted here by necessity, so each
document states its assumptions where it depends on something unverified rather
than asserting it.

## Reading order

| Proposal | Content | When to read it |
|---|---|---|
| [0 - Motivation and findings](stowcloud-0-motivation-and-findings.md) | why at all, what a Go port keeps and what it cannot, the technology mapping, the phase table, and the fourteen defects a read of the Rust tree turned up | **First**, and before arguing about any of it |
| [1 - Defensive standard](stowcloud-1-defensive-standard.md) | D1 to D20, what enforces each, and the four rules that are load-bearing rather than hygiene | **Second**, and before writing a line |
| [2 - Gate and toolchain](stowcloud-2-gate-and-toolchain.md) | the module layout, the lint set, the Go half of `verify.sh`, the four assumptions Phase 0 settles by compiling | Phase 0 |
| [3 - VFS and paths](stowcloud-3-vfs-and-paths.md) | `openat2` in Go, descriptor ownership, the three path types, streaming directory reads, the durable-write helper | Phase 1, before anything else touches the filesystem |
| [4 - Jail and hardening](stowcloud-4-jail-and-hardening.md) | why Landlock cannot be applied from a goroutine, the restrict-then-re-exec sequence, the seccomp assembler, the executable proof | Phase 1, with 3 |
| [5 - Store and schema](stowcloud-5-store-and-schema.md) | the cache/state split, migrations, the pure-Go SQLite decision and its abort criteria, and the derived node id that survives a cache rebuild | Phase 2 |
| [6 - Auth and ACL](stowcloud-6-auth-and-acl.md) | Argon2 parameters, the three-tier verification path, sessions, app-password scopes, TOTP, enumeration defence, grants | Phase 3 |
| [7 - Core domain](stowcloud-7-core-domain.md) | the protocol-agnostic API, the virtual root, directory ETags and the rollup, trash, conflict detection, share links | Phase 4 |
| [8 - HTTP and API](stowcloud-8-http-and-api.md) | the twelve-step chain, the error envelope and catalogue keys, the REST changes and the five that are justified | Phase 5 |
| [9 - Upload](stowcloud-9-upload.md) | TUS, the interval set, the ordering rule, spool modes, checksum verification, the orphan sweep | Phase 6 |
| [10 - WebDAV](stowcloud-10-webdav.md) | the hardened XML scanner in `encoding/xml` terms, the hand-written multistatus writer, Class 2 locking | Phase 7 |
| [11 - Search](stowcloud-11-search.md) | the golden-file port of the on-disk trigram format, the walk, the estimator | Phase 8 |
| [12 - Preview](stowcloud-12-preview.md) | the pool parent, the wire codec, decode limits, EXIF stripping, archive listing | Phase 9, after 4 |
| [13 - Compat](stowcloud-13-compat-nc.md) | the port interfaces, the import-graph gate that replaces the grep, chunked upload v2, OCS | Phase 10 |
| [14 - SMB and OIDC](stowcloud-14-smb-and-oidc.md) | `smb.conf` generation and the private-range rule, the passdb, discovery and the back-channel exchange | Phase 11 |
| [15 - Frontend client](stowcloud-15-frontend-client.md) | what changes in `web/src/lib/api` and what does not | Phase 12 |
| [16 - Parity and cutover](stowcloud-16-parity-and-cutover.md) | the response differ, the conformance run, the footprint remeasurement, the commit that deletes `crates/` | Phase 13 |

## The contradiction ledger

Five places where the overview draft and the code disagreed, found by splitting
the plan per phase and checking each claim against the tree it described. The
overview is gone and its content is in
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md), corrected; the ledger
stays because the reasoning is worth keeping. Each entry records a premise that
looked right and was not, and the code the correction was checked against.

| # | Contradiction | Resolution |
|---|---|---|
| C1 | The overview dropped BLAKE3 for SHA-256 on the grounds that every digest is an internal cache value. It is not: `Checksum::Blake3` is a TUS `Upload-Checksum` value a client sends (`crates/sc-upload/src/model.rs:95`). | BLAKE3 stays, as a Go module. Only the directory ETag hash is free to change, and it does not, because changing it for no reason invalidates every cached rollup at cutover. See [9](stowcloud-9-upload.md) §4.3 and [7](stowcloud-7-core-domain.md) §4.3. |
| C2 | The overview shipped two binaries while also targeting an image no larger than today's. Two Go binaries roughly double the image, and the `healthcheck` subcommand already in `Dockerfile` shows the argv dispatch that makes a second binary unnecessary. | One binary. The worker is `stowcloud preview-worker`, exec'd from the same path. See [2](stowcloud-2-gate-and-toolchain.md) §4.1.1 for the layout and [4](stowcloud-4-jail-and-hardening.md) §4.3.2 for what it does at startup. |
| C3 | The overview kept the portable filesystem backend "so the tree builds on Windows", while also making Windows a non-target. The Rust tree's own comments record two bugs that existed only on Linux and were invisible because Windows compiled the portable backend instead (`sync_dir`'s `EBADF` on `O_PATH`, and `create_excl` opening write-only). | No portable backend. `GOOS=linux go build` and `go vet` run on the Windows box with no toolchain at all; the test suite runs in the Linux VM. See [2](stowcloud-2-gate-and-toolchain.md) §4.3.3, which also states what this costs. |
| C4 | The overview deleted `node.flags`' `PINNED` bit when dead properties move to `state.db`, without noting that `node.id` is `oc:fileid` on the wire (`crates/sc-compat-nc/src/props.rs:282`). Deleting the cache re-mints every id, and every sync client sees every file as new. | Resolved by design rather than by disclaimer: [5](stowcloud-5-store-and-schema.md) §4.5 derives the id from the file's identity, so a rebuilt cache produces the same ids. The collision case is recorded durably in `fileid_override` rather than allowed to conflate two files, which is a failure this codebase has already had once. The one-time change at cutover is in [16](stowcloud-16-parity-and-cutover.md) §4.4. |
| C5 | The overview said the distroless base ships a CA bundle, while `Cargo.toml` records `webpki-roots` being chosen precisely because it does not guarantee one. | The OIDC client takes an explicit pool and refuses to start with an empty one. See [14](stowcloud-14-smb-and-oidc.md) §4.4.2. |

## The inherited stances

[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 lists seventeen
positions the Rust proposals settled, with the document each came from. They
are not restated as slogans; each phase document applies the ones that decide
something in it, and names them so a reader can tell an inherited position from
a new opinion. Where the Go port makes one harder to keep, the document that
owns it says so rather than dropping it quietly.

The shortest form of why they are written down at all: a stance that only a
careful author enforces is a stance a tired author does not, which is why
[`1`](stowcloud-1-defensive-standard.md) §2 maps nine of them onto something a
compiler or a gate checks.

## Conventions

Two additions to the parent directory's.

**These documents cite code and each other, and nothing else.** Implementing a
phase needs this folder and the repository's source tree, not a second set of
proposals. Everything the Rust-era documents carried that is still true has
been restated here where it decides something: the five principles
([`0`](stowcloud-0-motivation-and-findings.md) §2.2), the seventeen stances
(§2.5), the resource budget, the measured incidents. What they carried that
describes the Rust implementation stops being true at cutover, which is why
[`16`](stowcloud-16-parity-and-cutover.md) §4.4 step 4 retires them rather than
leaving a reader two specifications with nothing marking which one is wrong.

A path like `crates/sc-vfs/src/backend/linux.rs` is a citation of the codebase
and is expected; a path like `docs/proposals/stowcloud-N-*.md` is not, and its
absence from these files is deliberate rather than an oversight.

**Where a document depends on an unverified fact** about Go, the standard
library or a module, it says so in the sentence that depends on it. A proposal
for code that does not exist yet cannot also pretend its premises are measured.
