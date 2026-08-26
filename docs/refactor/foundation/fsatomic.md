# Foundation rebuild: fsatomic

> This document describes a from-scratch rebuild. The existing code
> (`go/internal/vfs/replace_linux.go`, `replace_other.go`,
> `publish_linux.go`, `publish_other.go`) is referenced as a behavioral
> specification only. The new implementation is written completely new;
> nothing is copied.

Target package: `engine/store/fsatomic`. No build tag on the package
declaration; the Linux implementation and a portable development-host
implementation both exist, split by file the same way the current `vfs`
functions are (`_linux.go` / `_other.go`), because the durability guarantee
genuinely differs by platform and pretending otherwise on a development
host that is not Linux would be a lie the tests could not catch.

## Purpose

`fsatomic` is the one primitive every subsystem uses to persist a file the
server itself owns: a key ring, a credential sidecar, a rendered config, an
index snapshot, a cache entry, a TLS certificate and key. It is not
filesystem security. It never takes a share root, never takes a validated
`vfs` path type, and never touches share content; its whole job is making
one plain-path write on local disk either fully land or not happen at all,
even across a crash.

This is the extraction the package survey and the audit both name: `vfs`
currently hosts this code even though its two functions take a bare
`string` path and open it with `unix.Open`/`os.Stat` rather than through the
`openat2` resolver every other `vfs` function uses (audit:
foundation-persistence.md, vfs findings 1 and 3; 01-package-survey.md,
"Cross-layer violations found" item 6). The functions belong in the
persistence layer because what they write is the server's own state, not a
share's content, and the persistence layer's other members (`dbfile`,
`cache`, `journal`, `state`) already sit at the same layer for the same
reason.

## Spec

### ReplaceFileDurable

```go
func ReplaceFileDurable(path string, mode uint32, write func(*os.File) error) (err error)
```

Rewrites one file this server exclusively owns: stage beside the
destination, write, sync, rename over the name, sync the directory. Nothing
user-supplied reaches `path` or the staged file's mode in any current
caller, and nothing in this signature is meant to accept it; `path` is
always a value derived from server configuration or a fixed filename under
a server-owned directory.

The exact sequence, every step load-bearing:

1. Open the destination's directory (`filepath.Dir(path)`) once, for
   reading, `O_RDONLY|O_DIRECTORY|O_CLOEXEC`. This descriptor is used for
   both the staging create and the publishing rename, which is what makes
   the rename atomic (both names relative to one open directory), and it
   is `O_RDONLY` rather than `O_PATH` because the last step fsyncs it and
   `fsync` on an `O_PATH` descriptor is `EBADF`.
2. Mint a staging name (the same `.scpart-`-prefixed random-suffix
   convention `vfs` uses for its own staging files; see "Deliberate
   changes" for why the name generator is not shared code).
3. Create the staging file `O_CREAT|O_EXCL|O_WRONLY|O_NOFOLLOW|O_CLOEXEC`
   relative to the held directory descriptor. `O_EXCL` turns a name
   collision into a refusal, never a clobber.
4. **Exact mode via fchmod, not the open's own mode argument.** The mode
   bit that matters most: `O_CREAT`'s mode argument is filtered through
   the process umask, so a key file created at the intended `0o600` under
   a umask of `0o022` becomes `0o600` only by coincidence and a umask of
   `0o077` would make it unreadable by its own creator; a permissive umask
   makes it *wider* than intended, which is the exact failure this
   function exists to prevent for a master key or a credential sidecar. So
   the mode is set with an explicit `Chmod`/`Fchmod` call on the already-
   open staging file, after creation, applied verbatim regardless of
   umask.
5. Call the caller's `write(f)` with the open staging file.
6. **Fsync the file.** The content is durable before anything makes it
   findable under the real name.
7. Close the staging file (checked; a close failure after a successful
   fsync is still reported, since some filesystems surface a delayed
   write error only on close).
8. **Rename** the staging name onto `filepath.Base(path)`, through the
   held directory descriptor (plain `renameat`, replacing whatever is
   there; this function always replaces, since every current caller is
   rewriting its own file wholesale, never refusing a collision).
9. **Fsync the directory.** This is the step that makes the *name*
   durable, separate from the file's own fsync in step 6: without it, on
   ext4 or XFS, a power cut can leave the fully-written content sitting
   under the staging name with nothing pointing at it under the real name.

On any failure before the rename lands, the staging file is unlinked; a
failure to unlink it is joined into the returned error (unlike `vfs`'s
`WriteDurable`, which only logs an unlink failure, `ReplaceFileDurable`
returns it, because a control file this server exclusively owns has no
external sweep to eventually collect an orphan the way a share's upload
sweep does for `vfs`'s staging files). `ENOENT` on the cleanup unlink (the
staging file was never created, or was already removed) is not an error.

The development-host implementation (no Linux build tag) preserves the
same staged-then-renamed shape and the same explicit `Chmod`, but does not
open the directory by descriptor and does not fsync it: it exists so
callers and their tests compile and run on a non-Linux development
machine, and it makes no durability claim. The file that ships is the
Linux one.

### PublishNew

```go
func PublishNew(final, staged string) error
```

Renames an already-complete file, built beside the name it will take, onto
that final name, refusing rather than replacing if `final` already exists,
and syncing the directory afterward. Unlike `ReplaceFileDurable`, this
function does not stage, write, or apply a mode; it takes two paths that
must already be siblings in the same directory and does the rename-and-sync
half alone.

**The caller decision.** A repo-wide grep for `PublishNew` at audit time
found no call site anywhere outside `vfs` itself (audit:
foundation-persistence.md, vfs finding 2), and this document repeats that
grep against the same tree before writing this spec:

```
$ grep -rn "PublishNew" go/internal --include="*.go" | grep -v _test.go
go/internal/vfs/publish_linux.go
go/internal/vfs/publish_other.go
```

Confirmed: only the definitions, no caller. The function's own doc comment
claims a caller, "the one-shot import of an older data directory," that
does not exist in the current tree either as a `cmd/` subcommand or as
store-layer migration code; `store/dbfile`'s migration system operates on
SQLite databases through `dbfile`'s own transaction machinery, not on
whole files renamed into place, and nothing under `cmd/stowcloud` performs
a data-directory import today.

**Decision: drop `PublishNew`.** It does not move to `store/fsatomic`.
Carrying forward an unused primitive because a future caller might want it
is exactly the kind of speculative surface the codebase's own style avoids
elsewhere (YAGNI), and a persistence primitive nobody calls is untested by
use, which is a worse place for a bug to hide than a primitive exercised by
eight real call sites. If a future phase needs "rename a complete file onto
a name atomically, refusing rather than replacing, with the directory
synced afterward," `ReplaceFileDurable`'s no-replace variant (below) covers
the same shape with one additional, already-necessary argument (the mode),
and that is the function to extend rather than reviving a second one with
an overlapping contract.

`NoClobber` is not currently a `ReplaceFileDurable` parameter; if a future
caller needs the no-replace behavior, it is added as a boolean option on
`ReplaceFileDurable` at that point, following the same shape
`vfs.DurableOpts.NoClobber` already uses for share writes, rather than
resurrecting a second top-level function. No such caller exists today, so
this document specifies the option's *shape* for future reference and
does not add it now.

### Multi-file durable unit

A new API, motivated by the presentation audit's TLS finding (audit:
presentation.md line 832): `server/tls.go` writes a certificate and its
private key as two independent `os.WriteFile` calls, neither durable, and
a crash between the two leaves a certificate and key that do not match,
which is worse than a single torn file because `tls.X509KeyPair` fails to
parse the pair with no indication of which half is stale. The audit's own
note: "the cert/key pair specifically needs to be treated as one durable
unit... a torn pair between them is as bad as a torn single file."

```go
// Unit is one file to write as part of a multi-file durable publish: its
// final name (a full path) and its content mode.
type Unit struct {
    Path string
    Mode uint32
}

// ReplaceFilesDurable writes every unit staged beside its own destination,
// fsyncs every staged file, renames every one onto its destination, then
// fsyncs every distinct directory involved exactly once. write is called
// once per unit, in the order given, before any rename happens.
func ReplaceFilesDurable(units []Unit, write func(i int, f *os.File) error) error
```

Sequence:

1. For every unit, in order: open its directory (deduplicated when several
   units share one directory, so a directory with two units in it is
   opened and later synced once, not twice), mint a staging name, create
   the staging file, apply its mode via `fchmod`, call `write(i, f)`, and
   `fsync` the file. If any unit's write or fsync fails, every staging
   file created so far (this unit included) is unlinked and the function
   returns before any rename happens: nothing is renamed unless every
   unit's content is already fully staged and synced.
2. Once every unit is staged and synced, rename every staged file onto its
   destination, in the order given. A rename failure partway through
   stops the sequence (see the crash-consistency statement below for what
   this means for a caller).
3. Fsync every distinct directory involved, once each, after every rename
   in step 2 has been attempted.

**Crash-consistency statement.** `ReplaceFilesDurable` makes step 1 atomic
per file and step 3 durable per directory, exactly like
`ReplaceFileDurable` does for one file. It does **not** make the *set* of
renames in step 2 atomic as a group: there is no multi-file rename
primitive in POSIX, and a crash between renaming unit 0 and renaming unit
1 leaves one file updated and the other still at its old content. This is
stated plainly rather than implied away, because a caller that assumes
group atomicity from the function's name would ship a startup path that
trusts a torn pair.

**The recovery rule the caller must implement.** For the TLS cert/key pair,
the concrete rule is: **order the renames key-then-certificate**, and at
startup, treat a certificate whose modification time is older than its
key's, or a certificate/key pair that fails to parse together via
`tls.X509KeyPair`, as **torn**, and regenerate both rather than trying to
use either half. The ordering means the two failure windows are:

- Crash before the key's rename: neither file changed, the old pair (which
  matched before this write started) is still there. No tear.
- Crash after the key's rename, before the certificate's: the key is new,
  the certificate is old. `tls.X509KeyPair` fails to parse this pair (the
  old certificate's public key does not match the new private key), which
  is already `loadOrCreateTLS`'s existing failure path (rebuilt in the TLS
  phase's own document, but its detection mechanism, `X509KeyPair`
  returning an error, needs no new code; it is the existing parse check,
  now guaranteed to be the *only* place a torn pair can surface, because a
  crash before the key's own rename cannot produce this state and a crash
  after both renames cannot either).
- Crash after both renames: both files are new and match. No tear.

This is a documented ordering rule, not a marker file, because the caller
already has a cheap, existing way to detect the one bad window (`X509KeyPair`
parse failure) without adding a second piece of state to keep consistent
with the pair itself. A marker file is the right tool when no such cheap
detector exists (for instance, two files whose mutual validity cannot be
checked by parsing either one); `ReplaceFilesDurable`'s contract is general
enough to support that shape too (a caller can include the marker as one
more `Unit`, ordered last, and treat its absence at startup as "the
previous write never completed, use the old set"), but the TLS caller does
not need it and the rebuild's TLS document should use the ordering rule,
not a marker, because it is simpler and the detector already exists.

Any future multi-file caller states its own recovery rule the same way,
explicitly, in its own phase document: `ReplaceFilesDurable` provides the
mechanism (stage all, fsync all, rename all, fsync directories once) and
is silent about group atomicity by design, so every caller must say, in
its own words, what "torn" looks like for its own pair and how startup
detects it.

## Rationale

- **Not a `vfs` function.** Everything this package writes is server
  configuration or server-owned state: a key, a credential sidecar, a
  rendered config, an index snapshot, a thumbnail, a certificate. None of
  it is share content, none of it passes through admission or a symlink
  policy, and none of it needs confinement inside an untrusted directory
  tree written by other software. The two packages solve genuinely
  different problems that happen to share a rename-and-fsync shape; moving
  this shape to its own package makes that shared mechanism visible
  without implying the two problems are the same one.
- **`fchmod`, not the open call's mode.** Every mode-related bug this
  primitive exists to prevent is a umask interaction, and the fix is one
  explicit syscall after the open rather than trusting the open's own mode
  argument, which the kernel filters through ambient process state the
  caller does not control at the call site.
- **Drop rather than keep `PublishNew`.** An unused primitive is not a
  neutral inclusion; it is untested surface with its own bug potential
  (its no-replace refusal, its cross-directory guard) that nothing
  exercises. If a real caller appears, the shape it needs is already
  covered by adding one option to `ReplaceFileDurable`.
- **Group non-atomicity stated, not hidden.** A function named
  `ReplaceFilesDurable` that silently could not deliver group atomicity
  would be worse than not offering the function at all, because a caller
  reading the name and not the fine print would build on a guarantee that
  does not exist. Spelling out the exact crash window and requiring each
  caller to state its own recovery rule keeps the guarantee honest.

## Deliberate changes

- `ReplaceFileDurable` moves from `vfs` (`replace_linux.go`/
  `replace_other.go`) to `store/fsatomic`, unchanged in behavior. Every
  current call site is updated to import `store/fsatomic` instead of
  `vfs` for this one function (list below).
- `PublishNew` is dropped, not moved. See "Decision" above.
- `ReplaceFilesDurable` (multi-file) is new: no current code has this
  shape, because no current caller writes more than one file as a unit
  through the durable primitive (the TLS cert/key pair is currently two
  independent, non-durable `os.WriteFile` calls, which this exists to
  replace).
- The staging-name generator (`stagingName`) is **not** shared code with
  `vfs`'s copy, despite drawing from the same `.scpart-`-prefixed
  convention (audit: vfs finding 8, which already documents this as an
  accepted, low-severity duplication: the prefix and the "unlistable
  reserved name" convention are shared by reference, but each package's
  generator is tied to that package's own id scheme). `fsatomic` has no
  `SafePath`/`JoinControl` machinery to route the name through, since it
  never resolves through a share root; it mints a bare filename with the
  same prefix and random suffix directly.

### Call sites, by phase

Every current `vfs.ReplaceFileDurable` caller, confirmed by grep against
the tree this document was written against:

```
$ grep -rn "ReplaceFileDurable" go/internal --include="*.go" | grep -v _test.go
go/internal/auth/masterkey.go:195
go/internal/auth/passdb.go:191
go/internal/auth/passdb.go:279
go/internal/preview/cache.go:173
go/internal/search/index/index.go:726
go/internal/search/index/index.go:827
go/internal/smbagent/accounts.go:185
go/internal/smbpublish/publish.go:271
```

| Call site | What it writes | Phase | Change needed |
| --- | --- | --- | --- |
| `auth/masterkey.go` (`KeyRing.persist`) | the master key ring, mode `0o600` | auth | repoint the import to `store/fsatomic`, no behavior change |
| `auth/passdb.go` (two sites) | the NT-hash passdb sidecar (`0o600`) and a second control file (`0o644`) | auth | same repoint, two call sites |
| `search/index/index.go` (two sites) | `base.idx` and `tomb.idx`, both `0o600` | search | same repoint; the delta segments' own `O_APPEND` write path is unrelated and unaffected |
| `smbagent/accounts.go` | the rendered `passwd`-format credential mode file | smb | same repoint |
| `smbpublish/publish.go` | a rendered SMB config file, mode from the caller | smb | same repoint |
| `preview/cache.go` (`Cache.Put`) | a thumbnail cache entry (`0o600`) | preview | same repoint |

None of these six sites change behavior; each is a one-line import and
call-target change from `vfs.ReplaceFileDurable` to
`fsatomic.ReplaceFileDurable`, made in that call site's own phase (the
package survey's per-phase document plan), not all at once in this phase.
This document exists so each phase's document can point at one settled
contract instead of re-deriving it.

### Rebuilt callers that bypass the primitive today

Four call sites write server-owned files without going through
`ReplaceFileDurable` at all today, three of them plain, non-durable
`os.WriteFile` (audit: foundation-persistence.md references these via the
survey's file-persistence inventory; presentation.md findings for the
`server` package cite the first two directly). Each is rebuilt onto
`fsatomic` in its own phase, not in this one:

| Call site | Current mechanism | Fix | Phase |
| --- | --- | --- | --- |
| `server/setup.go` (`SetupGate.issue`) | plain `os.WriteFile(..., 0o600)` for the setup token, no fsync, no atomic rename | `fsatomic.ReplaceFileDurable` | protocol/server assembly (`http/07-server-assembly.md`) |
| `server/tls.go` (`loadOrCreateTLS`) | two independent plain `os.WriteFile` calls for the certificate and the key, non-atomic as a pair | `fsatomic.ReplaceFilesDurable` with the key-then-certificate ordering rule specified above | protocol/server assembly (`http/07-server-assembly.md`) |
| `server/probefile.go` (`WriteProbe`) | hand-rolled `os.WriteFile` to a `.tmp` name plus `os.Rename`, no fsync of either the file or the directory | `fsatomic.ReplaceFileDurable` | protocol/server assembly (`http/07-server-assembly.md`); a second hand-rolled staged-rename is exactly the pattern this primitive exists to replace, per the survey |
| `smbagent/sync_linux.go` (`Apply`, promotion step) | plain `os.WriteFile` for `smb.conf` and its own candidate file under the agent's state directory | `fsatomic.ReplaceFileDurable` | smb phase (`smb/02-agent-durable-writes.md`), named explicitly in the document plan's highest-severity findings list (item 4) |

The probe file's own comment states its current rationale for tolerating a
plain write ("a failure here is the caller's to log and continue from: the
server is up either way"); that remains true after the fix, since a
missing probe snapshot already degrades to defaults in `ReadProbe`. The
fix is not about raising that file's importance, it is about removing a
second, independently-invented staged-rename implementation from the tree
in favor of the one this package now owns, so a bug fixed in the primitive
is fixed everywhere that pattern is used.

## Package placement

`engine/store/fsatomic`, under the persistence layer per the 3-layer target
architecture (01-package-survey.md). It imports only `engine/kit` packages
(`kit/limits` if a bound is ever needed here; none is used by the two
functions as specified) and the standard library. It does not import
`engine/vfs`: the two packages sit at the same layer and solve unrelated
problems, and an import in either direction would wire them together for
no shared behavior. It does not import `engine/store/dbfile` or any other
persistence sibling; every current and named future caller hands it a bare
path and a callback, nothing store-specific.

## Tests

1. **`ReplaceFileDurable` takes the exact mode despite a hostile umask.**
   With the process umask set to refuse every bit, a replace with
   `mode: 0o600` produces a file at exactly `0o600` (the same proof
   `vfs`'s current `replace_linux_test.go` runs, since the mechanism is
   unchanged).
2. **A failing writer leaves the prior file untouched**, and no staging
   file with the `.scpart-` prefix survives in the directory afterward.
3. **A successful replace is atomic**: a reader that opened the file
   before the replace continues to see the old content through its
   already-open descriptor (POSIX unlink-on-open-handle semantics), and a
   reader that opens by name after the replace sees only the new content,
   never a partial write.
4. **`PublishNew` is not part of the package.** A repo-wide grep for
   `fsatomic.PublishNew` after the rebuild finds nothing; this is asserted
   as a build-graph check (the symbol does not exist) rather than a
   runtime test.
5. **`ReplaceFilesDurable`, all units land.** Two units in the same
   directory and two units in different directories both complete with
   both files present, correct content, and correct per-unit mode; each
   distinct directory is fsynced exactly once (asserted via an instrumented
   directory-open counter in the test, not by inspecting real fsync
   counts, which the kernel does not expose countably).
6. **`ReplaceFilesDurable`, a write callback fails partway.** With two
   units and a callback that fails on the second, no unit's destination
   file is changed and no staging file survives under either directory.
7. **`ReplaceFilesDurable`, crash-window simulation for the TLS ordering
   rule.** A test harness that stops after renaming only the first unit
   (simulating the crash window between the two renames) leaves the first
   destination updated and the second still at its prior content; feeding
   that exact half-updated pair through `tls.X509KeyPair` fails to parse,
   confirming the detection the recovery rule depends on actually
   triggers on the one window that can produce a torn pair. This test
   lives beside the TLS phase's own tests once that phase is written, but
   the harness hook (a way to stop `ReplaceFilesDurable` after a given
   rename index) is part of this package's own test-only surface, so the
   TLS phase does not need to reimplement it.
8. **Every listed call site compiles against the new import** and its
   existing test coverage (from each site's own package test suite) still
   passes unchanged after the repoint; this is verified per phase, not in
   this document's own test list, since the call sites move in their own
   phases.
