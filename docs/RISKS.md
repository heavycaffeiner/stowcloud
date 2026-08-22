# What is likely to break, and what to fix

Written after the work that finished the port, then revised after the repairs
that work identified. It is what a person maintaining this should know before
something goes wrong, in the order of how much it will cost them.

Every entry is a fact checked against the tree rather than a recollection.
Where something is unverified it says so, because the whole value of a document
like this is that its uncertainties are marked.

## The pattern that produced almost every defect

Nine separate failures in the work that finished the port were the same shape,
and it is worth naming once rather than nine times.

**A package that compiles and has no caller is indistinguishable, from inside
the package, from a package that works.** Its tests pass. Its lint is clean.
Nothing about reading it suggests anything is wrong. What is missing is a wire
somewhere else.

It produced:

- `smb.Render`, complete and tested, called by nothing. SMB could not have
  worked in any deployment.
- The change-channel hub, built at startup and never put where the handler
  looks, so a build with a working hub told every client it had none.
- The OIDC client, complete, with no flow around it.
- The search service, wired, with no route querying it.
- The streaming search, whose route existed nowhere while the browse screen
  called it.
- Six routes mounted under a verb the client does not send.
- `AttachLinkCrypto`, which nothing called, so every password-protected link
  refused its own password.
- The archive walker, with no writer to hand its entries to.
- The write journal, recording faithfully, with nothing reading it.

The lesson is not "write more tests". Each of these had tests. It is that **a
test that calls a function proves the function works, and proves nothing about
whether anything calls it.** The checks that find this class are the ones that
compare two independent descriptions of the same thing: the route table against
the client, the server against another implementation, the browser against the
running binary.

The repairs below were mostly a second instance of the same pattern. `Append`
and `Tombstone` existed and were tested and nothing called them; `SetIndex` was
exported for a caller that was never written; the publisher had exactly one
caller and it was a button.

## Repaired

Each of these was on this list and is not any more. They are kept with what
made them wrong, because the shape recurs.

### SMB publishing is a sink

**Was**: `POST /api/admin/smb/apply` was the only caller of the publisher. A
grant created or revoked, an account disabled, a share added or removed: none
of them re-rendered the configuration or told the sidecar. An administrator
revoked somebody's access and it stayed live over SMB until somebody happened
to press apply. It failed in the permissive direction and silently, which is
the worst pair of properties a revocation path can have.

**Now**: three call sites, and they are sinks rather than steps each path
remembers.

`internal/auth` gained an optional publisher, set by the command. Every path
that already rewrote the credential file now also tells the sidecar to import
it, which matters because smbd does not authenticate against that file: the
sidecar imports it into the credential database, so a file written with nobody
told was a revocation that landed whenever something else happened to publish.

The grant and share write paths call it directly, through `grantsChanged` and
`sharesChanged`. A whole-share grant is an account list in that configuration
and a share is a section of it, so a write that stopped in this process left
the daemon serving the previous one.

The server publishes once at startup, because the state can have moved while it
was not running.

Three properties are load-bearing and each has a test:

- **The caller is never failed.** The database write already committed and this
  server is already enforcing it, so a refusal would report a change that did
  happen as one that did not. A sidecar that did not answer becomes
  `smb_stale` on the health endpoint.
- **The publish is synchronous.** It is the administrator waiting on a
  revocation reaching the other surface, which is the right person to wait.
- **The context is detached from the request.** A browser that navigated away
  mid-publish must not cancel a revocation halfway to the sidecar.

`PublishPassdb` renders and does not call the sink, because the publisher calls
it: without the split, publishing recurses.

### The search index is kept current

**Was**: `POST /api/admin/index/build` filled the index and nothing updated it
afterwards. A file created after the build was not in the index and the search
never learned that. This was worse than it sounds because it was silent in both
directions a person can check: a query the index can answer is answered from
the index alone, so the result was short with a success status. A person
searched for a file they created that morning, found nothing, and concluded it
was not there.

**Now**: `internal/search/service/update.go`. The watcher's events feed an
updater that re-reads the named directory and compares it against what the
index holds, appending and tombstoning only the difference.

What it needed was not a wire. The watcher's events name a directory rather
than the files in it, so the comparison had to be built, and the index could
not answer "what do you hold for this directory" without reading every entry.
`BaseSegment.EachUnder` and `NameIndex.ChildrenOf` are that: the segment is in
tree order, so a directory's subtree is one contiguous run, which makes the
answer a block-level binary search and a forward scan.

Four properties, each tested:

- **Only the difference is written.** Re-appending an unchanged listing grows
  the overlay on every touch of one file, until the merge that collapses it is
  the only thing the index does.
- **The queue drops rather than blocks.** Its producer also feeds every
  connected client's change channel, and an index update must never be what
  makes a listing go stale in a browser.
- **A lost-events notification leaves the index alone** and logs. There is
  nothing to replay, and dropping the index would turn every query into a walk
  until somebody noticed.
- **The merge runs on a timer**, not per update. A merge rewrites the base
  segment.

The fan-out in `StartWatch` is one goroutine feeding two consumers rather than
two readers on one channel, which would give each event to exactly one of them.

**Still true**: the ceiling is unchanged. A build stops at `CorpusScanEntries`
and a query for what it did not reach falls back to a walk.

### The index switch takes effect without a restart

**Was**: `serve.go` read the stored switch once, at startup.
`PATCH /api/admin/index/settings` wrote it and returned success. An
administrator turned the index on, saw a success, built it, and every search
was still a walk with nothing saying why. `Service.SetIndex` existed and was
exported for this and was called by nothing.

**Now**: the settings handler applies it in the running process and the
response carries `restart_required`, which is the shape the other settings
sections already use. A build that cannot apply it says so rather than
reporting a plain success.

### The build estimate is measured

**Was**: `indexBuildRate` was a compiled-in 20,000 entries per second and
nothing had ever timed a build.

**Now**: a completed build records the rate it got, and the estimate uses it.
The constant remains as the fallback for a deployment where no build has
finished, and the response carries `build_rate_measured` so an operator knows
which of the two they were shown.

A build under a second is not recorded: dividing by a fraction of a second
produces a rate no later build matches.

### A merge no longer blocks every query

**Was**: `Merge` took the write lock for the whole rebuild. That was correct by
construction and nobody had timed it. Measured, on a million entries: **4.4
seconds**, against 87 microseconds for a query. Fifty thousand queries' worth
of pause, and the updater's timer had just become what decides when it happens.

**Now**: the rebuild runs against a snapshot with no lock held, and the lock is
taken only for the swap. What makes that safe is a property the format already
had: a base segment is immutable once written and the overlay is only appended
to. The seal opens a fresh delta segment, so a write landing mid-merge goes
somewhere the merge will not delete, and stays in the overlay for the next one.

The same corpus, with a query loop running against the merge:

| | queries completed | worst query |
|---|---|---|
| lock across the build | 3 | 3.58 s |
| lock for the swap only | 44,378 | 3.1 ms |

The faster shape has a way to be wrong that the old one did not, so each way is
a test: an append during a merge, a tombstone during a merge, an applied
tombstone being dropped, a second merge refused, and a concurrent run under the
race detector. Writing them found a real defect. The seal records the next
sequence to be issued, so the first write after it carries exactly that number,
and an exclusive comparison dropped that tombstone while the new base still
held the entry: a deleted file came back.

`BenchmarkMerge`, `BenchmarkQuery` and `BenchmarkChildrenOf` are checked in, so
the next person to change this has the numbers rather than the recollection.

### The gate's own tools were older than the toolchain

**Was**: `golangci-lint` and `govulncheck` are installed into `go/.tools/bin`
and reused if present. Both load and type-check source with the `go/*` packages
they were compiled against, so a binary built by an older release cannot parse
a newer standard library. Against go1.27 the linter panicked on `math/rand/v2`
and govulncheck refused to load any package at all.

Both reported as a gate failure against this tree, and neither had anything to
do with this tree. That is the worst failure a gate can have: it is red for a
reason the diff cannot explain, so the way to get work done is to stop reading
it.

**Now**: a cached tool is reused only when `go version` on the binary matches
the toolchain on PATH, and rebuilt when it does not. Rebuilding them turned up
three real findings in this tree that the panicking linter had been hiding: two
unchecked type assertions and a shadowed error.

### Two smbagent tests named the wrong tool

**Was**: both fail on a machine with Samba's client package and not its server.
They guard on `testparm`, which the client package carries, and then start the
daemon, which needs `smbd` from the server package. The failure read as "a
rejected candidate replaced the configuration that was serving", which is a
report of a serious agent defect, from a box that simply had no `smbd`.

**Now**: `requireDaemon` names every tool those two drive, so they skip with a
reason. A test that fails everywhere it runs is one people stop reading, and a
failure that names the wrong cause is worse than no test.

### The archive reports what it could not pack

**Was**: `_skipped.txt` covered the permission case and nothing covered a read
that failed partway. A caller got a complete, openable archive missing files,
with a success status.

**Now**: both go in `_skipped.txt`. The distinction the handler needed is
`Writer.Err`: a failed read is one file and the archive goes on, while a failed
write is the response body gone and everything after it is wasted work. Without
telling them apart, the first unreadable file dropped every entry after it.

The list is bounded at a thousand names, with the rest counted, because it is
held in memory while the archive streams.

### The archive is bounded

**Was**: `ArchiveEntriesListed` bounded how many paths a request may name and
nothing bounded what those paths contain, so one path naming a large tree was
an unbounded response.

**Now**: `ArchivePackedEntries` and `ArchivePackedBytes` in `internal/limits`.
Neither refuses, because the status is committed on the first byte: the archive
ends and carries a `_truncated.txt` saying what the bound was and what it holds.

The listing surface had the same shape and was worse about it: it kept walking
after reaching its bound, opening a stream per entry it discarded. It now stops
at the bound it reports.

### routecheck compares both directions

**Was**: one-directional. A route the server mounts that no client calls was
not reported.

**Now**: reported, and never fatal. Most such routes are correct, because the
routes a sync client or an operator calls are not ones the web interface has a
screen for; `go/routes.server-only` records those with the caller each exists
for. A check that failed on them is one people learn to silence.

Adding the direction found two defects in the check itself, both of which had
been hiding real coverage gaps:

- **It read one directory.** The resumable upload transport lives in a sibling,
  so four calls were invisible and every upload route read as uncalled. It now
  walks the tree.
- **Its pattern did not match a call with a type argument.** The client writes
  `request<SessionInfo>('/auth/session')`, and eleven routes read as uncalled.

**Still missing**: the shape check. A route can be mounted, called, and answer
with a body the client's types do not describe. That probably belongs to the
end-to-end suite, which already asserts on response fields.

## What is left

### 1. The service group id is compiled in

**What is true.** `smbpublish.serviceGID` is 1000 and must match the group in
the sidecar image. Nothing checks the two agree at build time.

**What goes wrong.** If the image's group changes and this constant does not,
the agent refuses the sync with "no group exists for 1000". That is a good
failure, loud and specific, and it is the agent's own check rather than luck.

**The fix.** Not urgent. If the group ever becomes configurable it belongs in
the SMB config section beside `service_user`, not in a constant.

### 2. Nothing bounds how large the index may grow

**What is true.** A build stops at `CorpusScanEntries`, five million. The
incremental updater has no such bound: it appends what a directory holds.

**What goes wrong.** A corpus that grows past what a build covers keeps growing
the index, and the merge cost above grows with it. It is a slow drift rather
than a failure, and the numbers say a merge stays under a second per two
hundred thousand entries, so it has room before it matters.

**The fix.** Bound the updater the way the build is bounded, and report the
index as partial when it is. Not urgent, and it should be measured against a
real corpus that actually drifts rather than guessed at.

## Things that look like problems and are not

Worth recording, because each is a decision someone will otherwise revisit.

- **`OperationDownload` answers "not implemented".** A job does not keep its
  output; an archive is streamed as it is packed, so there is nothing to hand
  back later. The route is mounted and honest rather than absent, so a client
  gets a status it can act on.
- **The archive stream cannot report a failure that happens partway.** This is
  the design, not an oversight. The status and headers are committed on the
  first byte, and the writer is built for it: sizes follow the data, so a file
  that vanishes mid-archive produces a short entry inside a valid archive. The
  alternative, buffering so a failure can be reported, trades a bounded memory
  cost for an unbounded one. What was missing was the report, and that is what
  `_skipped.txt` and `_truncated.txt` now carry.
- **The index's stat revalidation stays even though deletions are
  tombstoned.** The two cover different windows: the tombstone catches a
  deletion the watcher reported, and the stat catches one that happened between
  the query and the response, or on a filesystem the watcher cannot see.
- **The SMB agent refuses to run as anything but root.** It edits the system
  account file and the credential database. Refusing at startup beats a
  permission error three layers down.
- **The control socket is unauthenticated.** It lives on a volume shared by
  exactly the two containers that already exchange password hashes through the
  same directory. Filesystem permissions are the whole gate, deliberately.
- **The socket falls back to a permissive mode in a container.** Dropping every
  capability leaves the sidecar unable to hand a file to another user, and
  adding that capability back to a process that parses SMB off the wire costs
  more than it buys.
- **No write timeout on the server.** Downloads and streams need it absent. The
  idle timeout is what bounds a dead connection.
- **The Go build sends security headers the Rust build did not.** Recorded in
  `DIFFER.md` as a difference; it is the Go build being stricter.

## Still open from before this work

These are unchanged and each has its own document.

- **Ten WebDAV conformance failures**, five carried over from the previous
  implementation and five this port's. `CONFORMANCE.md` attributes each. Phase
  7 owns the Go-only ones.
- **Twenty status-code differences**, every one a refusal against a different
  refusal, none a success against a failure. `DIFFER.md` has the table. A
  status vocabulary belongs to the phase owning the surface.
- **Two unmeasured footprint items**: the preview worker pool and the SQLite
  write path, which are exactly the two the expectation named as likely
  regressions. `FOOTPRINT.md` lists them as not measured rather than as no
  regression.
- **The jail is unproven on aarch64.** `JAIL-PROOF.md`; it needs a second
  machine.

## Dependencies and the toolchain

Recorded because the reasoning is what a later update needs, not the versions,
which date immediately.

- **`go/go.mod`'s directive is the floor, not the compiler.** It is 1.25,
  which is the highest any dependency declares. CI and both Dockerfiles compile
  with a pinned current release, and those now agree with each other: an image
  building with a different compiler from the one the gate ran means the binary
  that ships is not the binary that was verified.
- **Vite stays on 7.** `vite-plugin-functions-mixins` declares `vite: ^7.2.4`
  and has no release above 0.4.1. It is not optional: m3-svelte's CSS is
  written against the `@function`/`@mixin` proposal, so without the plugin
  every elevation shadow and every type style silently drops out. Vite 8 needs
  that plugin to move first, or the styles to stop depending on it.
- **TypeScript stays on 5.** SvelteKit 2.70's peer range is `^5.3.3 || ^6.0.0`.
- **The one advisory was a lockfile pin, not a version range.** `postcss`
  already allowed the fixed `nanoid`; the lockfile held an older resolution.

## If you change one thing

Measure the preview worker pool. It is the last thing in this tree whose cost
is unknown rather than understood: `FOOTPRINT.md` names it as the regression
the expectation predicted most confidently, and then did not measure it. Every
other number that mattered has since been taken, and each time the measurement
changed what was done rather than confirming it.
