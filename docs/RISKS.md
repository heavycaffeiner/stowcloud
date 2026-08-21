# What is likely to break, and what to fix

Written after the work that finished the port: the SMB sidecar agent, the
surfaces that answered "not implemented", and the route mounting that finishing
them uncovered.

It is not a summary of that work. It is what a person maintaining this should
know before something goes wrong, in the order of how much it will cost them.

Every entry is a fact checked against the tree rather than a recollection.
Where something is unverified it says so, because the whole value of a document
like this is that its uncertainties are marked.

## The pattern that produced almost every defect

Nine separate failures in this work were the same shape, and it is worth
naming once rather than nine times.

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

## Ranked by what it will cost

### 1. Nothing republishes SMB except an administrator pressing a button

**What is true.** `POST /api/admin/smb/apply` is the only caller of the
publisher. A grant created or revoked, an account disabled, a share added or
removed: none of them re-render the SMB configuration or tell the sidecar.

**What goes wrong.** An administrator revokes somebody's access and it stays
live over SMB until somebody happens to apply. Nothing in the interface says
so. This is the most likely way this codebase produces a security incident,
because it fails in the permissive direction and silently.

The credential half is not affected: `republishPassdb` is a sink every
credential-changing path already calls, so a password change or a disabled
account does reach the rendered file. It is the **share and grant** half that
does not.

**The fix.** Call the publisher from the same places `republishPassdb` is
called from, plus the grant and share write paths. It has to be a sink, for
exactly the reason that one is: a change that stops at one surface is a change
that did not happen.

**Do not** fix this by publishing on a timer. A timer turns a revocation into
"revoked within N seconds", which is a different promise from the one the admin
screen implies.

### 2. The search index is built once and then goes stale

**What is true.** `POST /api/admin/index/build` walks every share and fills the
index. Nothing updates it afterwards. The watcher does not append, and nothing
tombstones a deleted file.

**What goes wrong.** A file created after the build is not in the index, and
the search never learns that.

The fallback does not help here, and it is worth being exact about why. The
index falls back to a walk when it *declines* to answer: a query under three
bytes, or one whose every trigram was pruned. A query it can answer is answered
from the index alone, and an index missing a file answers without it. There is
no signal, because from the index's point of view nothing went wrong.

So a stale index returns fewer results with a success status. A person searches
for a file they created this morning, finds nothing, and concludes it is not
there. That is a worse failure than an error, because nobody reports it.

A deleted file is the opposite and is handled: a hit is revalidated by a stat
before it is returned, so an entry for a file that is gone is dropped.

**The fix, and it is not just a wire.** `NameIndex.Append` and
`NameIndex.Tombstone` exist and are tested, but the watcher does not hand over
what they need. Its events name a *directory* that changed, not the files in
it, and one event says only that the whole share is stale because events were
lost. So keeping the index current means re-reading the changed directory and
diffing it against what the index holds, which is real work rather than a
connection between two existing pieces.

That makes this the one item on this list that is a subsystem rather than a
missing wire, and it should be scoped as one.

**Until then**, either leave the index off, which is the default and makes
every search a walk that is always current, or rebuild on a schedule and accept
that a search is complete only as of the last build.

Leaving it off is the better choice until the wire exists. The index is an
escalation taken when measurement says the walk is not enough, and a walk that
is slow beats an index that is quietly short.

### 3. Turning the index on requires a restart, and the screen does not say so

**What is true.** `serve.go` reads the stored switch once, at startup, and
attaches the index if it is on. `PATCH /api/admin/index/settings` writes the
switch and returns `{"name_enabled": true}` with no restart flag.

Every other settings section goes through `applyOutcome`, which reports
`restart_required` precisely so an administrator is not left wondering. The
index toggle does not use it.

**What goes wrong.** An administrator turns the index on, sees success, builds
it, and searches are still walks. Nothing is broken and nothing says anything.

**The fix.** Either call `Service.SetIndex` from the settings handler, which
the method exists for and is why it is exported, or return the
`restart_required` shape the other settings sections use. The first is better;
the second is honest.

### 4. The index size and build estimates are guesses presented as numbers

**What is true.** `EstimateNameIndex` is a real model over a real measured
corpus: the file count, the name bytes and the distinct-trigram sketch are all
counted by walking. That part is sound.

`indexBuildRate`, however, is a compiled-in constant of 20,000 entries per
second. Nothing has ever timed a build. The comment now says so; it previously
claimed the rate came from the walk that had just run, which was false.

**What goes wrong.** An operator plans around a build time nobody measured. A
corpus on a slow disk, or one whose names are mostly outside ASCII, will not
match it.

**The fix.** Have the build report its own rate and store it, then estimate
from the last real build. Until then the number is a guess and the constant's
comment says which.

### 5. The archive stream cannot report a failure that happens partway

**What is true.** This is a deliberate design, not an oversight, and it is
worth understanding before someone "fixes" it.

An archive streams. The status and headers are committed on the first byte, so
a failure after that cannot become an error status. The writer is built for
this: sizes follow the data rather than preceding it, so a file that vanishes
mid-archive produces a short entry inside a valid archive rather than a corrupt
one.

**What goes wrong.** A caller gets a complete, openable archive that is missing
files, with a success status. The `_skipped.txt` entry covers the permission
case and nothing covers the vanished-file case.

**The fix, if one is wanted.** Add the read failures to `_skipped.txt` the same
way permission failures are added. The alternative, buffering the archive so a
failure can be reported, trades a bounded memory cost for an unbounded one and
should not be taken.

### 6. `POST /api/fs/archive` has no bound on total bytes

**What is true.** `ArchiveEntriesListed` bounds how many paths a request may
name. Nothing bounds what those paths contain, so one path naming a large tree
is an unbounded response.

**What goes wrong.** This is a resource question rather than a correctness one:
one request can occupy a connection for a long time. The account has to be able
to read what it is archiving, so it is not an amplification an anonymous
attacker has.

**The fix.** A byte or entry ceiling on the walk, ending the archive with a
marker entry saying it was truncated. D5 wants it as a named constant in
`internal/limits` with a test proving the limit is what refuses.

### 7. Two flow lifetimes and one service group id are compiled in

**What is true.**

- `limits.OIDCFlowLifetime` is ten minutes. The binding cookie now derives from
  it rather than repeating it, and a test asserts they match.
- `smbpublish.serviceGID` is 1000 and must match the group in the sidecar
  image. Nothing checks the two agree at build time.

**What goes wrong.** The group id is the live one: if the image's group is
changed and this constant is not, the agent refuses the sync with "no group
exists for 1000". That is a good failure, loud and specific, and it is the
agent's own check rather than luck.

**The fix.** Not urgent. If the group ever becomes configurable, it belongs in
the SMB config section beside `service_user`, not in a constant.

### 8. `routecheck` covers what the client calls, not what the server serves

**What is true.** The check now reads every module in the client directory and
compares the verb as well as the path. That closed seven defects in one pass.

It is still one-directional. A route the server mounts that no client calls is
not reported, and there is no check that a mounted route's response shape
matches what the client's types expect.

**What goes wrong.** Dead routes accumulate unnoticed, which is a maintenance
cost rather than a fault. The shape mismatch is the more interesting gap: a
route can be mounted, called and wrong.

**The fix.** The reverse direction is easy and worth adding. Shape checking is
harder and probably belongs to the end-to-end suite, which already asserts on
response fields for the surfaces it covers.

## Things that look like problems and are not

Worth recording, because each is a decision someone will otherwise revisit.

- **`OperationDownload` answers "not implemented".** A job does not keep its
  output; an archive is streamed as it is packed, so there is nothing to hand
  back later. The route is mounted and honest rather than absent, so a client
  gets a status it can act on.
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

## If you change one thing

Make SMB publishing a sink, the way the credential path already is. It is the
only entry here that fails in the permissive direction, and it fails silently.

Everything else on this list is a person being told something untrue about the
state of the system, or a resource cost. That one is somebody keeping access
after it was taken away.
