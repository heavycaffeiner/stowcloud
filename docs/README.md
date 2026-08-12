# Proposals

These specify the **Go backend**: what it does, why it is shaped that way, and
what each phase of building it has to deliver. They replaced the proposals that
specified the Rust backend, which described code this plan deletes.

Every one of them is `Status: Draft`, and unlike the directory's previous
occupants they describe code that does not exist yet. The house rule there
("written from what is built, not from what was planned") is inverted by
necessity, so each document states its assumptions where it depends on
something unverified rather than asserting it. That inversion ends at cutover;
[17](proposals/stowcloud-17-parity-and-cutover.md) §4.4 is where.

## Reading order

| Proposal | Content | When to read it |
|---|---|---|
| [0 - Motivation and findings](proposals/stowcloud-0-motivation-and-findings.md) | the five principles, the seventeen stances underneath them, why Go, what a port keeps and what it cannot, the technology mapping, the phase table, and the fourteen defects a read of the Rust tree turned up | **First**, and before arguing about any of it |
| [1 - Defensive standard](proposals/stowcloud-1-defensive-standard.md) | D1 to D20, what enforces each, and the four rules that are load-bearing rather than hygiene | **Second**, and before writing a line |
| [2 - Gate and toolchain](proposals/stowcloud-2-gate-and-toolchain.md) | the module layout, the lint set, the Go half of `verify.sh`, the four assumptions Phase 0 settles by compiling | Phase 0 |
| [3 - VFS and paths](proposals/stowcloud-3-vfs-and-paths.md) | `openat2` in Go, descriptor ownership without `Drop`, the three path types, streaming directory reads, the durable-write helper | Phase 1, before anything else touches the filesystem |
| [4 - Jail and hardening](proposals/stowcloud-4-jail-and-hardening.md) | why Landlock cannot be applied from a goroutine, the restrict-then-re-exec sequence, the seccomp assembler, the executable proof | Phase 1, with 3 |
| [5 - Store and schema](proposals/stowcloud-5-store-and-schema.md) | the cache/state split, migrations, the pure-Go SQLite decision and its abort criteria, and the derived node id that survives a cache rebuild | Phase 2 |
| [6 - Auth and ACL](proposals/stowcloud-6-auth-and-acl.md) | why the question is "how few times must the KDF run", the parameters, sessions, app-password scopes, TOTP, the enumeration defence, grants | Phase 3 |
| [7 - Core domain](proposals/stowcloud-7-core-domain.md) | the protocol-agnostic API, the virtual root, directory ETags and the rollup, trash, conflict detection, share links | Phase 4 |
| [8 - HTTP and API](proposals/stowcloud-8-http-and-api.md) | the twelve-step chain and why its order is a cost argument, the error envelope, the five REST changes and why only five | Phase 5 |
| [9 - Upload](proposals/stowcloud-9-upload.md) | TUS, the interval set, the ordering rule, spool modes, checksum verification, the orphan sweep | Phase 6 |
| [10 - WebDAV](proposals/stowcloud-10-webdav.md) | the hardened XML scanner in `encoding/xml` terms, the hand-written multistatus writer, Class 2 locking | Phase 7 |
| [11 - Search](proposals/stowcloud-11-search.md) | the golden-file port of the on-disk trigram format, the walk, the estimator, the two leaks a search can have | Phase 8 |
| [12 - Preview](proposals/stowcloud-12-preview.md) | the two threats and the two defences, the pool parent, the wire codec, decode limits, EXIF stripping, archive listing | Phase 9, after 4 |
| [13 - Compat](proposals/stowcloud-13-compat-nc.md) | the three-package seam, the five gates that hold it, chunked upload v2, OCS, the mobile surfaces | Phase 10 |
| [14 - SMB and OIDC](proposals/stowcloud-14-smb-and-oidc.md) | `smb.conf` generation and the bind rule computed from the host, the passdb, discovery, and the address guard that closes DNS rebinding | Phase 11 |
| [15 - Deployment](proposals/stowcloud-15-deployment.md) | the two images, the seccomp reality, the filesystem gate, the uid contract, the proxy contract, one socket | deployment and operations |
| [16 - Frontend client](proposals/stowcloud-16-frontend-client.md) | what changes in `web/src/lib/api` and what does not | Phase 12 |
| [17 - Parity and cutover](proposals/stowcloud-17-parity-and-cutover.md) | the response differ, the conformance run, the footprint remeasurement, the commit that deletes `crates/` | Phase 13 |

Two directories sit beside them and are not part of the port:

| Directory | Content |
|---|---|
| [`proposals/frontend/`](proposals/frontend/stowcloud-0-frontend.md) | the SvelteKit SPA: virtual scroll, the upload worker, i18n, the byte budgets. `web/` is unchanged by the port, so this proposal still describes what is built |
| [`proposals/design/`](proposals/design/) | design-system work, written against the frontend rather than the backend |

## The five principles

They are stated in full in
[0 §2.2](proposals/stowcloud-0-motivation-and-findings.md), with the seventeen
positions underneath them in §2.5. In short: the filesystem is the only source
of truth, a path is a kernel handle rather than a string, a shared folder is not
ours, the compat layer does not invade the core, and the default is the
restrictive one.

## The contradiction ledger

Five places where an early draft of this plan and the codebase disagreed, found
by splitting the plan per phase and checking each claim against the tree it
described. All five are corrected in the documents; the ledger stays because
each entry records a premise that looked right and was not.

| # | Contradiction | Resolution |
|---|---|---|
| C1 | The draft dropped BLAKE3 for SHA-256 on the grounds that every digest is an internal cache value. It is not: `Checksum::Blake3` is a TUS `Upload-Checksum` value a client sends (`crates/sc-upload/src/model.rs:95`). | BLAKE3 stays, as a Go module. The directory ETag keeps the same hash, because changing it for no reason invalidates every cached rollup at cutover. [9](proposals/stowcloud-9-upload.md) §4.3.3. |
| C2 | The draft shipped two binaries while also targeting an image no larger than today's. Two Go binaries roughly double it, and the `healthcheck` subcommand already in `Dockerfile` shows the argv dispatch that makes a second one unnecessary. | One binary, five subcommands. [2](proposals/stowcloud-2-gate-and-toolchain.md) §4.1 for the layout, [4](proposals/stowcloud-4-jail-and-hardening.md) §4.3.2 for what the worker does at startup. |
| C3 | The draft kept the portable filesystem backend "so the tree builds on Windows", while also making Windows a non-target. That backend is what hid two bugs that broke every write and every upload finalize on the only platform that ships. | No portable backend. `GOOS=linux go build` and `go vet` run on the Windows box with no toolchain at all; the test suite runs in the Linux VM. [2](proposals/stowcloud-2-gate-and-toolchain.md) §4.3.3 states what that costs. |
| C4 | The draft deleted `node.flags`' `PINNED` bit without noting that `node.id` is `oc:fileid` on the wire (`crates/sc-compat-nc/src/props.rs:282`). Deleting the cache re-mints every id, so every sync client sees every file as new. | Resolved by design rather than disclaimer: [5](proposals/stowcloud-5-store-and-schema.md) §4.5 derives the id from the file's identity. The collision case is recorded durably rather than allowed to conflate two files, which this codebase has already had happen once. |
| C5 | The draft said the distroless base ships a CA bundle, while `Cargo.toml` records `webpki-roots` being chosen precisely because it does not guarantee one. | The OIDC client takes an explicit pool and refuses to start with an empty one. [14](proposals/stowcloud-14-smb-and-oidc.md) §4.4.2. |

## Conventions

**These documents cite code and each other, and nothing else.** Implementing a
phase needs this directory and the repository's source tree, not a second set of
proposals. A path like `crates/sc-vfs/src/backend/linux.rs` is a citation of the
codebase and is expected. That rule is why the Rust-era proposals could be
removed rather than left beside a Go tree as a second specification with nothing
marking which one was wrong.

**A code comment states its own reason.** No document citations, no ticket ids:
a reader with only the file in front of them has to be able to use it. The
proposal carries the long-form argument and the code stands alone.

**A non-goal carries the reasoning that made it one**, in the document for the
subsystem it belongs to, because a non-goal without a reason gets re-proposed
every six months.

**Where a document depends on an unverified fact** about Go, the standard
library or a module, it says so in the sentence that depends on it. A proposal
for code that does not exist yet cannot also pretend its premises are measured.

`scripts/verify.sh` is what decides whether a change is releasable.
