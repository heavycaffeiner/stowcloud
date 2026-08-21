# Open questions

Decisions the port surfaced that only the maintainer can make. Each one records
the options, what would decide between them, and the answer taken in the
meantime so that no phase stalls on it. Taking an answer is not settling the
question: the entry stays until it is settled, and the phase named under
**Reopen by** is where the evidence to settle it arrives.

A question leaves this file by being answered in the document that owns it, not
by being deleted.

---

## Q1. The thumbnail output format, now that Go has no WebP encoder

**Raised by** Phase 0c, settling assumption A3
([`2`](stowcloud-2-gate-and-toolchain.md) §4.4).

**What was found.** A3 asked whether Go's decoders cover what the current build
accepts, and they do. What the question did not ask, and the compile answered
anyway, is the other direction: the preview pipeline's *output* is lossless
WebP (`crates/sc-preview/src/exif_strip.rs:31`, encoded with
`WebPEncoder::new_lossless`, cached as `<fid>-<w>x<h>-<etag8>.webp` in
`cache.rs:113`). `golang.org/x/image/webp` is a decoder. There is no WebP
encoder in the standard library or anywhere in `x/image`.

**Options.**

| # | Output format | Cost |
|---|---|---|
| 1 | PNG, standard library | lossless and keeps alpha, like today. Larger files than lossless WebP, against a 2 GB default thumbnail cache |
| 2 | JPEG, standard library | much smaller, and the wrong answer for the icons and screenshots a file browser is full of: lossy, and no alpha at all |
| 3 | A third-party pure-Go WebP encoder | keeps the format and the cache footprint. Adds a direct dependency the parent proposal's table does not carry, in a codec, which is the class of dependency this port spent §6-2 minimising |
| 4 | Write one | a still-image WebP encoder is VP8L plus a RIFF container. This is the category the house rules say not to hand-roll |

**What decides between them.** The measured size ratio at the preset
dimensions actually generated, against the 2 GB cache budget and the eviction
rate that budget implies. If PNG at those sizes is within a small factor of
lossless WebP, option 1 costs nothing anyone notices. If it is several times
larger, the cache holds proportionally fewer thumbnails and option 3 becomes
worth a dependency.

**Taken for now: option 1, PNG.** It is what the surrounding documents imply
rather than a preference: [`0`](stowcloud-0-motivation-and-findings.md) §6-2
commits to a direct-module list that has no encoder in it and says everything
else is standard library, [`12`](stowcloud-12-preview.md) §4.3.4 needs an
encoder that writes no EXIF and Go's do not, and the current output is
lossless, which PNG is and JPEG is not. The cache filename extension follows
the format.

**Reopen by** Phase 9, which is the first phase with a decoder and an encoder to
measure, and which owes the numbers either way.

**What is not in question.** The decode side. Still WebP reads in all three
shapes it ships in; animated WebP does not, and that reduction is recorded in
[`12`](stowcloud-12-preview.md) §3.1 rather than left to be discovered.

---

## Q2. What "byte-identical `base.idx`" can mean across two zstd encoders

**Raised by** milestone 8a, building the golden fixtures
([`11`](stowcloud-11-search.md) §4.3.1).

**What was found.** §4.3.1 asks that the Go implementation, writing `base.idx`
from the same corpus, produce the same bytes, and says that where a
byte-identical write is impossible the fix is to make the writer's order
explicit rather than to weaken the check. That instruction is the right one for
every difference it anticipated, and there is one it does not reach: the block
payloads are zstd frames. The Rust side produces them with the `zstd` crate at
level 6; the Go side will produce them with `klauspost/compress`.

zstd's format specifies what a decoder must accept, not what an encoder must
emit. Block splitting, match finding and entropy table selection are all the
encoder's to choose, and libzstd's numeric levels have no defined counterpart
in another implementation. Two independent encoders agreeing byte for byte
would be a coincidence, and one that any version bump on either side would end.
No ordering fix reaches this, because it is not an ordering difference.

**Options.**

| # | The check becomes | Cost |
|---|---|---|
| 1 | byte-identical for the header, the block directory, the dictionary and the postings; identical after decompression for the block payloads | the compressed bytes go unchecked, so a difference in compression ratio is invisible to the fixture |
| 2 | byte-identical everywhere, with the block payloads stored uncompressed | gives up the reason the index is small, which is three quarters of the design |
| 3 | byte-identical everywhere, with a cgo zstd on the Go side | contradicts `CGO_ENABLED=0`, which is the whole static-binary story |
| 4 | behavioural only: same corpus in, same query answers out | this is what the golden-file strategy exists to be stronger than |

**What decides between it and option 2.** Only the measured size difference, and
only if it turns out that the pure-Go encoder's ratio on filename corpora is far
enough off libzstd's to matter against the 4 GB metadata budget. That is a
Phase 8c measurement and it is a different question from this one.

**Taken for now: option 1.** The bytes a format defines are checked exactly; the
bytes an encoder chooses are checked through what they decode to. That keeps
every claim §4.3.1 makes about ordering, layout, dictionary construction and
posting encoding, which is where the seven hand-written algorithms live, and
gives up only the claim that two compressors agree. The block directory records
each block's uncompressed length, so a payload that decompresses to the wrong
size is still caught by the format's own check.

**Reopen by** Phase 8c, which writes `base.idx` in Go and is where the
comparison is actually run.

---

## Q3. Whether the jail takes Landlock's IPC scoping, now that the kernel has it

**Raised by** Phase 1g, building the ruleset
([`4`](stowcloud-4-jail-and-hardening.md) §4.3.2).

**What was found.** The design was written against a Landlock that handles
filesystem access rights and nothing else, and `Spec` reflects that. The test
guest reports **ABI 6**, and `unix.LandlockRulesetAttr` has three fields rather
than one: `Access_fs`, `Access_net`, and `Scoped`. ABI 6 added
`LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET` and `LANDLOCK_SCOPE_SIGNAL`, which confine
a domain's reach over two channels the filesystem rights say nothing about. The
implementation passes `Scoped` as zero, so neither is applied.

This matters most for the decoder, whose whole premise is that it holds two
descriptors and can reach nothing else. `socket` is not on its allow-list, so an
abstract unix socket is already out of reach; signals are not, and
`LANDLOCK_SCOPE_SIGNAL` would stop a compromised worker signalling the parent.

**Options.**

| # | Scoping | Cost |
|---|---|---|
| 1 | Leave `Scoped` at zero | the design as specified, and one channel a decoder can still reach |
| 2 | Scope signals in the worker's domain only | closes it where it matters, on a kernel 5.13 to 6.11 the field is simply not handled, so it degrades by itself |
| 3 | Scope signals and abstract sockets in both domains | the server signals nothing outside itself either, but the SMB sidecar and any future subprocess become a thing to check rather than assume |

**What decides between them.** Whether the preview worker is the only
subprocess this product ever holds. Option 2 is free where the kernel has it and
invisible where it does not; option 3 is a claim about the whole process
topology, which [`14`](stowcloud-14-smb-and-oidc.md) owns and Phase 11 settles.

**Taken for now: option 1.** [`4`](stowcloud-4-jail-and-hardening.md) §3.2 says
the layers are the six named in [`0`](stowcloud-0-motivation-and-findings.md)
§4.3 F2 and that adding a seventh is a separate proposal, and an IPC boundary is
a seventh. Phase 1 ships what the document specifies rather than the superset
the kernel turned out to offer.

**Reopen by** Phase 9, which is the first phase with a worker to scope and the
place `proof.go` would demonstrate it.

---

## Q4. Which hash derives `node.id`, given that it can only be chosen once

**Raised by** Phase 2a, building the derivation
([`5`](stowcloud-5-store-and-schema.md) §4.5.2).

**What was found.** The original §4.5.2 specified BLAKE3 and justified it with
"already in the tree for the upload checksum, so this adds no dependency".
That is a fact about the Rust tree. The original Go schedule admitted BLAKE3 in
Phase 6 for a client-selected TUS checksum. The corrected schedule admits it in
Phase 4 because the directory ETag is already wire-visible and Phase 6 reuses
the same module. Neither schedule makes BLAKE3 available to Phase 2 without
pulling a dependency forward. §6-2 of the parent document says this phase takes
one dependency, the driver, and nothing else.

Nothing outside this server recomputes a `node.id`. The derivation needs a hash
that distributes and nothing more.

**Options.**

| # | Hash | Cost |
|---|---|---|
| 1 | `crypto/sha256` | standard library, and the derivation is settled at Phase 2 with no dependency |
| 2 | BLAKE3, pulled forward from Phase 4 | one module two phases early, for a digest no client ever sees |
| 3 | BLAKE3 later, switching at Phase 4 | every id changes a second time, for every install already running the Go build |

**What decides between them.** Whether anything outside this process ever has
to reproduce the derivation. Nothing does today: the id reaches a client as
`oc:fileid` and is compared, never recomputed. If a future tool has to derive
ids without this binary, the hash becomes part of a wire contract and the
argument reopens.

**Taken: option 1.** The documents' own rule is that a client-facing digest
buys a dependency and an internal one does not, and this one is internal.
Option 3 is ruled out on its own terms rather than on preference: §4.5's whole
purpose is that an id survives a rebuild, and changing the hash breaks that for
every install that already rebuilt once.

**Reopen by** Phase 4, which is where BLAKE3 now arrives for the wire-visible
directory ETag. Its arrival does not change the answer: switching an existing
file-id derivation would change every id, so Phase 4 must confirm the split and
leave the SHA-256 derivation untouched.

---

## Q5. Whether filesystems without durable inode identity remain supported

**Raised by** the full proposal audit before Phase 2.5
([`5`](stowcloud-5-store-and-schema.md) §4.5 and
[`15`](stowcloud-15-deployment.md) §4.3).

**What was found.** The deployment proposal allowed FUSE, NFS and CIFS or SMB
and assigned path-based ids where inode identity was not trustworthy. It also
admitted squashfs until the first failed write and had no decision for NTFS or
an unknown filesystem magic. The store proposal defines exactly one id and
durable-reference scheme, derived from
`(share, dev, ino, btime_present, btime_ns)`, and promises that a rename
preserves it. Phase 1
implemented `FsType.ForcesPathIDs`, but Phase 2 has no path identity to pass to
the derivation and `state.db` has no path-target variant for durable properties,
locks, favourites and share links.

A registration probe cannot prove identity stability across the event that
matters: a process restart, remote remount or server-side filesystem change.
Adding a path variant would also make a rename change `oc:fileid` and detach
durable metadata unless every external rename were observed and rewritten
atomically, which the watcher explicitly does not promise.

**Options.**

| # | Policy | Cost |
|---|---|---|
| 1 | Admit only birth-time-capable ext4, btrfs, XFS, ZFS, f2fs and warned tmpfs | drops storage backends and filesystem instances the Rust build admitted, but keeps one truthful identity and write contract and follows S4 |
| 2 | Restore path-based ids and path-target durable rows | retains admission, but ids change on rename and external renames can detach durable state |
| 3 | Use reported inode identity with a warning | retains admission and the schema, but can silently change ids after a remount, which is the delayed failure S4 forbids |

**What decides between them.** Whether compatibility with those storage classes
outweighs stable sync ids and durable metadata across external changes. A fourth
option needs a filesystem-specific persistent handle that can be reopened after
a restart; none is available through the current `statx` contract.

**Taken for now: option 1.** The surrounding documents choose refusal whenever
a filesystem cannot support the identity and write contracts, and the Go store
has intentionally removed the cache pinning that made a second identity scheme
survivable. Phase 2.5 removes the unused `ForcesPathIDs` promise and makes the
declaration fail closed for squashfs, NTFS and unknown values too, so no later
phase mistakes absence from a blacklist for support. Phase 11 additionally
checks `STATX_BTIME` on each admitted root or nested mount, because a familiar
filesystem magic alone does not prevent inode reuse from impersonating a
replacement.

**Reopen by** Phase 11, where the filesystem gate becomes an operator-visible
registration decision and the compatibility cost can be evaluated on actual
deployments.

---

## Q6. The Argon2 gate's permit count

**Raised by** Phase 3, crossing the auth design against itself
([`6`](stowcloud-6-auth-and-acl.md) §2.0, §4.3.1 and S10).

**What was found.** §2.0 and S10 state the concurrency cap as four, on the
reasoning that 48 MiB × 4 is 192 MiB of the container's budget, and the Rust
gate's own module comment says the same. §4.3.1 described the buffered channel
as "of size `argon2_parallelism`", which is `p = 1` in the chosen parameter set.
Those are different numbers, and they are not the same product: a gate of one
turndown would serialise logins while still claiming to bound memory at 192 MiB.

**Options.**

| # | Gate size | Cost |
|---|---|---|
| 1 | four, as §2.0 and S10 say | matches the documented memory budget and the Rust behaviour |
| 2 | `argon2_parallelism` (one) | serialises logins far below the memory budget the design set |

**What decides between them.** The whole subsystem's premise in §2.0 is "how
few times must the KDF run", and the memory bound is the product of the gate
size and the per-hash cost. A gate of one contradicts the stated 192 MiB
budget and every document that names four.

**Taken for now: option 1.** The implementation uses a gate of four, and the
contradictory §4.3.1 sentence is corrected in the proposal to say four. A
future proposal that raises the memory cost must still raise the product, not
the number.

**Reopen by** no phase; the decision is settled by the documents that already
name four.

---

## Q7. Which phase owns the resumable-upload HTTP surface

**Raised by** Phase 12c, confirming what the frontend's upload path talks to.

**What was found.** `web/src/lib/upload/transport.ts` speaks a resumable-upload
protocol to `/api/uploads` and `/api/uploads/{id}`: a create, a chunk append, a
head to learn the current offset, and a delete, exchanging `Upload-Offset`,
`Upload-Length`, `Tus-Resumable` and this server's own `Sc-Chunk-Size`. The
Rust tree mounts exactly those routes, deliberately outside the size-limited
router.

The Go tree has the engine and no routes. Phase 6 built the session store, the
interval set, the spool, verification and the sweep, and its milestone list
stops there. Phase 5 owns the HTTP surface and its milestone list does not name
uploads. Phase 7 owns the WebDAV upload collection, which is a different
protocol on a different path. So nothing between Phase 5 and Phase 12 owns
mounting the surface the frontend already speaks to, and no document says it
was dropped.

The frontend cannot be adapted around this. A client cannot upload against
routes that do not exist, and changing the client to speak something else would
be inventing a protocol rather than porting one.

**Options.**

| # | Owner | Cost |
|---|---|---|
| 1 | Phase 13 absorbs it as cutover work | the differ and the conformance run are the first things to exercise it, which is late for a surface with resumption and offset semantics |
| 2 | A Phase 6g milestone, retrospectively | Phase 6 is complete and reopening it makes "a phase is done" mean less |
| 3 | A Phase 5 milestone, retrospectively | same objection, and Phase 5 has no upload vocabulary to build on |

**What decides between them.** Whether the surface is HTTP work that belongs
with the rest of the HTTP surface, or cutover work. The engine's contracts, the
ordering rule and the crash windows are Phase 6's and are already proved; what
is missing is the mapping from protocol to engine, which is the same kind of
work Phase 5 and Phase 7 did for their own surfaces.

**Taken for now: option 1.** Phase 13 cannot pass its conformance run without
this surface, so it is blocked on it either way, and the alternatives reopen a
closed phase to record work that has not started. Phase 13 mounts it before the
differ runs, so the differ is what exercises it rather than a real client
discovering it.

**Reopen by** Phase 13, which either mounts it or reports it as a gap it could
not close.

---

## Q8. Which phase mounts the WebDAV and compat surfaces

**Raised by** Phase 13b, running the response differ against both builds.

**What was found.** The differ's WebDAV group produced an unauthenticated
refusal from both servers, but the Go build's came from the route table having
no `/dav` pattern at all rather than from the WebDAV handler. `internal/dav` is
complete: the scanner, the multistatus writer, locks, the `If` header,
`PROPFIND`, `PROPPATCH`, `SEARCH`, `REPORT`, the content methods and the upload
collection all exist and are tested. What does not exist is the mount: the
handler's entry points take an already-resolved path, and nothing resolves a
`/dav/...` URL and dispatches to them.

The compat layer is in the same state, and was already known to be: its
handlers and its route descriptions exist, and only the OCS router is
registered. Phase 10 recorded that as incomplete.

This is the same shape as Q7. A phase built a subsystem, its milestone list
ended at the subsystem, and the surface that exposes it belongs to no phase.
Three of them now: uploads, WebDAV, and the compat mounts.

**Options.**

| # | Owner | Cost |
|---|---|---|
| 1 | Phase 13 mounts all three as cutover work | the differ is the first thing to exercise them, which is late for protocols with locking and conditional requests |
| 2 | Reopen Phases 7 and 10 | both are closed, and reopening a closed phase to record work that never started makes "done" mean less |
| 3 | Ship without them and record the gap | the product is a file server whose file protocols are unreachable, which is not a gap but an absence |

**What decides between them.** Whether a phase owning a package also owns the
route that reaches it. Nothing in the phase documents says either way, which is
how three subsystems ended up in this state rather than one.

**Taken for now: option 1**, matching Q7. Phase 13 cannot compare what is not
mounted, so it is blocked either way, and the alternative reopens two closed
phases.

**The general lesson is worth stating separately**, because it is what produced
all three: a phase that ends when its package compiles and its tests pass has
not shipped anything. Every future subsystem phase should end at a mounted
route with a request reaching it, not at a package boundary.

**Reopen by** Phase 13, which mounts them or reports what it could not.

---

## Q9. The HTTP surface is a third of the one the clients call

**Raised by** Phase 13, after the conformance run, chasing what looked like a
defect in the Rust build's login.

**What was found.** There was no defect in the Rust build. Its login is
`POST /api/auth/login`; the probe used `POST /api/auth/password`, which is the
change-password endpoint, and it correctly refused. The conformance document's
claim that the Rust baseline could not be measured was wrong and is corrected.

What the mistake uncovered is larger. The Go build mounts login on
`/api/auth/password`, so the two surfaces disagree on the one route every
session begins with, and the frontend calls `/api/auth/login`. Nobody can sign
in to the Go build from the shipped interface.

Counting properly: the Rust build mounts 66 routes under `/api`, the Go build
35. Of the 58 concrete paths the frontend's own client calls, **39 do not exist
on the Go server**. By area:

| Area | Missing |
|---|---|
| admin server-settings sections | 11 |
| TOTP enrolment and recovery | 4 |
| admin users, groups, grants | 8 |
| search index build and settings | 3 |
| SMB and OIDC self-service | 4 |
| login and the second factor | 2 |
| jobs, shares, archive, storage, app-password wipe | 7 |

The subsystems behind them are not missing. `internal/search`,
`internal/smb`, `internal/oidc` and `internal/acl` all build, and their tests
pass. What is missing is the route that reaches them, which is Q8 again and at a
scale that makes Q8's "three subsystems" an undercount.

**Why every phase was green.** Each phase tested its package. No phase tested
that a request arrives. The gate greps the route table for scope requirements
and validates what is in it; it never compares that table against the client
that has to use it, so a route the frontend needs and the server lacks is
invisible to every check in the tree.

**Options.**

| # | Approach | Cost |
|---|---|---|
| 1 | Build the 39 routes now, in Phase 13 | the largest body of untested work in the port lands in the phase whose job is to compare, not to write |
| 2 | Reopen the owning phases, one route group each | honest attribution, and it reopens five closed phases |
| 3 | Ship the subset and record the rest | a file server that cannot authenticate from its own interface is not a subset |

**What decides between them.** Whether Phase 13 is allowed to write product
code. Its own ground rule says nothing found there is fixed there, because a fix
made in the cutover phase has no test of its own.

**Taken for now: option 2**, with the login path as the exception. The route
table and the client are two halves of one contract and the tree has no check
that they agree, so the first thing to build is that check: a gate that reads
the frontend's client and the server's table and fails on a path the client
calls and the server does not mount. Then each phase gets its own routes back
with that gate telling it when it is done.

Cutover is blocked until that gate is green. Deleting `crates/` while the
replacement cannot serve a login is not a cutover.

**Reopen by** the gate landing, then Phases 3, 5, 8, 11 in turn.

---

## Q10. The SMB sidecar agent has no Go port

**Raised by** Phase 13g, checking what deleting `crates/` would delete.

**What was found.** Fifteen of the sixteen crates have a Go equivalent, three
of them under a different name. `sc-smb-agent` does not. It is 2,003 lines and
it is a shipping component: `Dockerfile.smb` builds it into the sidecar image,
and the compose file runs that image.

It is the privileged half of SMB publishing. The server renders the
configuration, the password database and the account file into a shared
directory as an unprivileged user; the agent picks them up as root, decides the
network scope from the interfaces it can actually see, validates the result,
promotes it, imports the password database and signals the daemon. The split
exists because the server runs in a different network namespace and cannot see
the host's devices, which is the reason the render is the closed case and the
agent is what expands it.

Phase 11 scoped `internal/smb` to the render, the bind rule, the escaping and
the password database. The agent was never in its milestone list, and nothing
else claimed it. This is Q8 and Q9 one more time: a phase ended at a package
and the component that uses it belonged to nobody.

**What this means for cutover.** Deleting `crates/` deletes a component that
ships. Three options, and none of them is "it is already ported".

| # | Approach | Cost |
|---|---|---|
| 1 | Keep the Rust agent, delete the rest | the cutover commit stops being "delete the Rust tree" and becomes "delete all but one crate", and the workspace has to keep building for that one |
| 2 | Port it before cutover | 2,003 lines of privileged, root-running code written in the phase whose job is comparison, with no phase's tests behind it |
| 3 | Cut over without it, and ship SMB with the agent absent | the sidecar has nothing to promote the rendered files, so SMB stops working entirely rather than degrading |

**Taken for now: option 1.** The Rust agent is kept, in its own directory,
with its own build. Everything else in `crates/` goes.

That is the honest shape: the port replaced the server, and one privileged
sidecar written in Rust is still a Rust program. Pretending otherwise means
either shipping a broken feature or writing root-running code in the phase
least equipped to test it. The gate keeps a step that builds it, so it cannot
rot unnoticed, and a later phase can port it against that gate.

**Reopen by** whichever phase takes the agent, with a milestone that ends at
the sidecar image running rather than at a package compiling.
