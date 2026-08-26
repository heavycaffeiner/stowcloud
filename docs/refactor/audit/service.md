# Service layer audit

Scope: `go/internal/auth`, `go/internal/oidc`, `go/internal/upload`,
`go/internal/preview` (and `preview/worker`, `preview/worker/audit`),
`go/internal/runtimecfg`, `go/internal/settingscheck`,
`go/internal/emergency`, `go/internal/smb`, `go/internal/smbpublish`,
`go/internal/smbagent`.

Baseline is `docs/refactor/01-package-survey.md`. Findings below either
confirm, refine, or add to what that document already states. `core` was
skimmed only for cross-references; no new finding on `core` came up beyond
what its own rebuild documents already cover. `auth`, `upload`, `preview`,
and `smbpublish` import `core` directly; this matches the target table
(service may import service downward) and is not flagged as a violation.

## auth

### Findings

1. `passdb.go:1-24, 232-262`, misplacement, confirmed survey claim. The
   package imports `go/internal/smb` and calls `smb.PasswdEntries(users,
   gid)` and uses `smb.User` in `PublishPasswdEntries`. The write itself
   goes through `vfs.ReplaceFileDurable`, which is sound; the coupling to
   `smb`'s type and rendering function is the violation. Fix with a seam
   auth calls through (an interface or callback wired from `Config`,
   mirroring the existing `SetSMBPublisher` pattern), not a direct import.

2. `sql.go`, misplacement, confirmed survey claim. 200+ lines of raw SQL
   constants; every write/read method in `admin.go`, `apppw.go`,
   `audit.go`, `groups.go`, `login.go`, `oidcflow.go`, `oidclink.go`,
   `recovery.go`, `rotate.go`, `session.go`, `smbstate.go`, `totp.go`,
   `users.go` calls `s.st.SQL().QueryContext`/`s.write(...)` directly
   against these statements. All statements use `?` placeholders, so
   there is no injection defect, only a layering one: persistence-layer
   work lives in the service package.

3. `ratelimit.go:9-63`, defect, race condition, not previously flagged.
   `limiter` guards `k map[string]*limitBucket` and `ord []string` with no
   mutex. `Service.Login` is called concurrently per HTTP request and
   calls `s.ratelimit.Allow(req.IP)` on every attempt. Two goroutines
   calling `Allow` concurrently race on the map read/write and on
   `l.ord = append(l.ord, key)` / `l.ord = l.ord[1:]`, which is a data
   race (concurrent map write can panic) under concurrent logins. Every
   other shared structure in the package (`caches` in `credcache.go`,
   `discoveryCache`/`jwksCache` in oidc) is protected by its own mutex;
   `limiter` is the one exception.

4. `password.go:104-113` and `passdb.go:207-221`, duplication.
   `phcU32`, `phcU8` (`password.go`) and `sealVersion`, `smbUid`
   (`passdb.go`) each hand-roll a bounded integer narrowing check
   (round-trip compare plus range check) that duplicates
   `internal/num.Narrow`, the survey's "one legal integer narrowing".
   None of the four import `num`. The checks are correct, so this is not
   a defect, only a second spelling of an existing primitive.

5. `admin.go:242-249`, naming, minor. `isUniqueViolation` matches on
   `strings.Contains(err.Error(), "UNIQUE constraint failed")`, a
   driver-message string match the code's own comment calls fragile. If
   the persistence layer is redrawn per aggregate, this belongs there as
   a typed error the driver wrapper returns, not a string match per
   caller package.

6. `service.go:1-14`, naming, package doc is accurate but scope is broad.
   The doc correctly states the subsystem's shape: accounts, sessions,
   app passwords, TOTP, recovery codes, groups, NT hash, master key.
   Seven aggregates in one flat package of 27 files, 4353 lines. Matches
   the survey's "own phase, after core" verdict; the phase document
   should decide whether the rebuild keeps one `auth` package or splits
   by aggregate, the way `store/state` is being redrawn per aggregate.

7. `masterkey.go:246-251`, none, design decision worth stating explicitly.
   `ResolveKeyFile` logs a warning, not a refusal, when the master key
   resolves inside the data directory; the code comment explains why
   (refusing would break the default deployment). No action needed, but
   a rebuild document should state this as a decision, not leave it as a
   comment only.

8. `masterkey.go`, `rotate.go`, none, durability verified. `KeyRing.persist()`
   and every call site (`OpenMasterKey`, `alignRing`, `RotateMasterKey`) go
   through `vfs.ReplaceFileDurable` with mode `0o600`. `RotateMasterKey`'s
   three-step protocol (persist ring with both keys, reseal all rows in one
   transaction, compact ring) is designed so a crash at any boundary is
   recoverable by `alignRing` at next startup. The survey's file-persistence
   inventory names only the passdb sidecar under `auth`; the master key ring
   is the same durable pattern and should be listed alongside it.

9. `login.go`, `reconfirm.go`, `session.go`, `oidcflow.go`/`oidclink.go`,
   none, verified sound. The decoy-hash timing defense (`decoyPHC`,
   `decoyOnce`) and constant-time comparisons (`subtle.ConstantTimeCompare`)
   for session tokens and OIDC state/binding digests are correctly
   implemented account-enumeration and timing-oracle defenses.

10. `credcache.go`, none. Three-tier cache is internally synchronized (one
    mutex per tier), generation-invalidated, and bounded (FIFO eviction,
    fixed capacity).

11. `crockford.go`, `seal.go`, `totp.go`, `gate.go`, `groups.go`,
    `recovery.go`, `smbstate.go`, `errors.go`, none. No defects,
    misplacements, or duplication found beyond what is listed above. SQL
    is parameterized throughout; secrets are wrapped in `secret.Secret`
    before logging paths; AEAD sealing binds record identity and key
    version into AAD correctly.

### Rebuild notes

- Move every statement in `sql.go` into the state-store aggregate(s) the
  persistence rebuild draws for auth (sessions, credentials, TOTP/recovery,
  groups, master-key state, OIDC flow/link, audit). The service layer
  keeps only domain logic and calls the store's typed read/write surface.
- Replace the `smb` import in `passdb.go` with a seam (interface wired
  from outside, matching `SetSMBPublisher`) so `auth` has zero import of
  `smb`.
- Fix the `limiter` race in `ratelimit.go` first: add a mutex guarding `k`
  and `ord`, matching the pattern already used by `caches`,
  `discoveryCache`, `jwksCache`.
- Replace the hand-rolled narrowing helpers with `num.Narrow` once the
  kit package's grouped home exists.
- Decide explicitly whether the master key ring belongs in the same
  file-persistence inventory entry as the passdb sidecar, and whether
  `auth` should split into per-aggregate packages when persistence is
  redrawn per aggregate.
- Specify the `isUniqueViolation` replacement as a typed sentinel from
  the persistence wrapper once SQL moves to `store/state`.

## oidc

### Findings

No misplacements, defects, or duplication found. Confirms the survey's
"clean deps already" verdict.

1. `client.go`, `dial.go`, none, verified sound. The address guard
   (`Guard.Allow`, `IsBlocked`) runs at discovery-parse time
   (`discovery.go: checkEndpoint`) and again at actual dial time via
   `net.Dialer.Control` (`dial.go: Dialer`), closing the
   resolve-then-check TOCTOU gap. `IsBlocked` handles IPv4-in-IPv6
   mapped/embedded forms, unique-local, link-local, loopback, and
   unspecified addresses.

2. `client.go: do`, none, verified sound. Response bodies are read
   through `io.LimitReader(res.Body, limits.OIDCResponseBytes+1)`
   (off-by-one to distinguish exactly-at-bound from over-bound), and
   `res.Body.Close()` is joined via `errors.Join` in a deferred closure
   so a close error is never swallowed. Redirects are refused outright.

3. `jws.go`, none, verified sound. Token verification is dependency-free.
   The algorithm is derived from the key type, not the untrusted header
   (closes alg-confusion and none-alg holes). Embedded key material in
   the header (`jwk`, `x5c`, `jku`, `x5u`) is refused, `crit` extensions
   are refused, RSA keys under 2048 bits are refused, EC points are
   validated on-curve. Claims validation (issuer exact-match, audience,
   expiry with skew, nonce via constant-time compare) runs only after
   signature verification.

4. `authorize.go: FlowSecrets`, none. `String()`/`GoString()` redact,
   consistent with `secret.Secret`, though `FlowSecrets` is a plain
   struct of strings rather than `secret.Secret`, a reasonable choice
   since these values are not credentials in the same sense as a
   password.

5. `link.go`, none, but flag for rebuild decision. `LinkStore` is a
   one-method interface (`UserForIdentity`) that keeps `oidc` from
   importing `auth`, the correct direction. No non-test caller of
   `oidc.ResolveIdentity` or a type satisfying `LinkStore` was found;
   `auth/oidcflow.go`/`oidclink.go` implement equivalent logic directly
   against SQL instead. If `LinkStore`/`ResolveIdentity` are dead code,
   the rebuild should drop them rather than carry forward an unused seam.

6. `jwks.go`, `discovery.go` caches, none, verified sound. Both are
   single-slot, mutex-guarded, TTL-bounded caches; a fetch failure is
   never cached, avoiding an hour-long cached outage. The two caches are
   near-duplicates of each other (minor, acceptable under YAGNI given
   their small size).

### Rebuild notes

- No defect fixes needed. Preserve verification order (signature before
  claims), the dial-time-plus-discovery-time address guard, and the
  redirect refusal on the back channel as explicit requirements.
- Decide explicitly whether `LinkStore`/`ResolveIdentity` is the intended
  integration seam with `auth`, or superseded by `auth`'s own SQL-backed
  flow; carry forward only one design.
- Document the two small caches as one required behavior (bounded TTL,
  no negative caching) rather than two hand-copies.

## upload

### Findings

1. `settings.go:110` (`SetChunkSettings`) vs `settings.go:145`
   (`ApplySettings`), duplication. Two admin write paths do the same job
   (validate, persist through `state.WriteChunkSettings`, update live
   atomics). Only `ApplySettings` is called from production code
   (`httpapi/handler/admin_ops.go:266`); `SetChunkSettings` is reachable
   only from `settings_test.go`.

2. `model.go:73-76` (`StateFinalizing`), naming. `SessionState` declares
   `StateFinalizing` ("has begun publishing") and `store.go:160` treats
   it as non-expiring like `StateReceiving`, but nothing in the package
   ever sets `r.sess.State = int64(StateFinalizing)`. `Finalize` never
   assigns it before or during publish. Either finalize is missing a real
   state transition, or the enum member should be removed as aspirational.

3. `cache.go`, naming/cohesion, minor. 1027 lines holding spool-volume
   budget math, the merger goroutine and its wake/progress protocol,
   chunk file naming and parsing, session recovery, and the cache-enabled
   admin switch. One coherent subsystem, but the largest file in the
   package by a wide margin; a rebuild should split naming/layout, the
   merger, and the admin surface into separate files even if they stay
   one package.

4. `engine.go:26` (`Engine.handles`, `Engine.rows`), naming, minor.
   `Abort` calls `closeHandle` but not `forgetRow`, so an aborted
   session's row-lock mutex stays in `rows` until the sweep's
   `collectExpired` runs (bounded by `UploadSessionTTL`, not unbounded).
   Worth noting for the lifecycle table in a rebuild spec.

5. `spool.go:158` (`spoolChunk`), none, verified correct. Looks like a
   check-then-act race at first glance, but every mutating call in this
   path runs under the per-session row lock (`lockRow` in `PutNamed`), so
   no concurrent access to the same chunk name is possible.

6. `finalize.go:57` (`if !dest.Equal(r.Path())`), none, verified correct.
   The trust-boundary check that a finalize's resolved destination
   matches the session's own recorded destination runs before any bytes
   are touched, combined with `requireOwner` and the ACL check above it.

7. `alias.go:113` (`checkTransferID`), none, verified correct. The
   client-chosen transfer id is length-bounded against `limits.NameBytes`
   and scanned for control characters and `/` before reaching
   `state.BindUploadAlias`/`LookupUploadAlias`. SQL is parameterized
   throughout `store/state/upload.go`, no string-built SQL found.

8. `verify.go`, `finalize.go:82`, none, verified correct.
   `VerifyWholeFile` requires both algorithm and digest, reads exactly
   `length` bytes back, refuses on short read, does a constant-time
   compare. No silent truncation or silent accept path found.

9. `cache.go:800` (`patchCached`/`writeCached`), none, verified correct.
   Cache chunks are written under an unlisted staging name, fsync'd, then
   renamed into place. A real, correctly ordered atomic-publish pattern,
   distinct from `vfs.ReplaceFileDurable` because the file lives inside a
   `vfs.ShareRoot` rather than as a plain path; the durability argument is
   explicit in the comment.

10. Trust-boundary checks across the package (`validateOffset`,
    `checkWithinDeclared`, `checkChunkFloor`, `IntervalSet.Insert`'s run
    count bound, `LoadIntervalSet` re-deriving the invariant from stored
    rows rather than trusting them), none, verified correct.

11. File scan of every `os.` call in non-test files: only
    `os.MkdirAll` (cache.go:102, startup-time operator directory, not
    request-driven) and `os.ErrClosed` (sentinel comparison). No
    `os.WriteFile`/`os.Create`/`os.Rename` anywhere; every write goes
    through a `vfs.ShareRoot` method. This confirms the survey's claim in
    the narrow sense of "no raw filesystem write bypassing vfs." See
    finding 12 for a caveat on the stronger implicit claim.

12. `cache.go:96-124` (`openCache`), misplacement, minor, cross-cutting
    with the vfs/store boundary. The cache spool is opened as a
    `vfs.ShareRoot` over `cacheShareID = vfs.ShareID(0)`, with the code's
    own comment admitting "It is not a share... The value exists because
    a rooted handle carries one." This reuses vfs's safe-open machinery
    for a directory that is not part of the share domain (server-owned
    scratch space). When vfs is rebuilt, this should become an explicit
    vfs capability ("open a safe root over a non-share directory") rather
    than upload minting a synthetic share id.

13. `store/state/upload.go` versus `upload`, none, verifies the survey's
    "right shape" claim with one caveat. The split is clean: SQL and
    parameterized values stay in `store/state/upload.go`; interval
    merging, spool-mode logic, and vfs calls stay in `upload`; one
    crossing file (`store.go`) converts between the two. Caveat:
    `state.UploadSession` is a 27-field struct mixing several
    sub-concerns (destination/identity, name-ordered assembly cursor,
    cache-mode cursor, whole-file verify pair, admin floor snapshot) in
    one flat row shape. Worth internal grouping when `store/state` is
    redrawn per aggregate.

14. `finalize.go:154` (`checkIfMatch`), none, verified correct. The
    precondition is checked against the destination as it stands at
    publish time, not a value cached at session open, closing the TOCTOU
    window the code comment calls out.

15. Duplication check against the foundation kit, none. `IntervalSet` is
    genuinely upload-specific (byte-range coverage tracking), not a
    reimplementation of an existing primitive. Every narrowing conversion
    goes through `num.Narrow`; the one goroutine spawn (the cache merger)
    goes through `task.Go`; every byte-size constant is defined in or
    read from `limits`.

### Rebuild notes

- The survey's claim that the durable half in `store/state/upload.go` is
  "the right shape" holds. Keep engine-side logic and persistence-side
  SQL split as they are, with one crossing file doing type conversion.
- The claim that upload's file persistence is "VFS control names... not
  file persistence" holds for part files and the destination-share spool.
  It does not cleanly cover the cache spool, which is a server-owned
  directory outside any share wearing a fake share id (finding 12). The
  vfs and file-persistence-inventory documents should decide explicitly
  whether this belongs to vfs (a first-class non-share safe-root
  capability) or to persistence.
- Collapse the two admin settings-write entry points (finding 1) to one;
  keep `ApplySettings`'s nil-means-unchanged merge semantics.
- Resolve the `StateFinalizing` enum member (finding 2) before rebuild:
  either give finalize a real state transition, or remove the state and
  document finalize as a stateless, idempotent retry.
- Capture the three durability ordering invariants (write-then-record,
  sync-before-rename at publish, copy-sync-record-delete for the merger)
  as explicit acceptance criteria, not prose comments.
- Flag the 27-field `UploadSession` row (finding 13) for internal
  grouping to whoever specs the upload aggregate in `store/state`.

## preview (and worker, worker/audit)

### Findings

1. `cache.go: Cache.Put` (around line 178), none, verified sound. Writes
   thumbnails through `vfs.ReplaceFileDurable`, staging and syncing
   before rename. No `os.WriteFile` or bypass found anywhere in the
   package. Confirms the survey's claim.

2. `wire.go` (whole file), none, but flagged for a naming collision this
   is not itself a defect. This file defines the parent-worker wire
   protocol (`Request`, `Response`, `Encode`/`Decode`), an internal RPC
   codec between two halves of the same process tree, not the "wire
   shapes" the survey's target table assigns to presentation (fiber
   surface, WebDAV, wire shapes, status mapping). `DecodeRequest`'s
   comment states explicitly this is the trust boundary. The rebuild
   should keep this file under the preview service package and not fold
   it into a presentation wire-shape module, since the name is easy to
   misread.

3. `pool.go: Pool.Generate`/`exchange` (around lines 140-230), none,
   verified sound. Job dispatch uses a buffered `free` channel as a
   semaphore and a per-slot mutex; dead workers are reaped and lazily
   replaced on next use. `exchange` treats an empty read, a read error,
   and a version mismatch identically as `ErrWorkerDied`. Every failure
   path in `Pool.start` closes what it opened before returning. No
   goroutine or descriptor leak found.

4. `decode.go: DecodeBounded` (around line 118), none, verified sound.
   Format is sniffed from magic bytes only, never a declared name.
   Header-only dimensions are checked before the pixel buffer is
   allocated (`checkBounds`), then checked again after decode in case a
   decoder ignored its own header. Real defence against
   decompression-bomb and format-confusion input.

5. `exif.go: tiffOrientation`/`jpegOrientation` (around lines 71-140),
   none, verified sound. IFD entry count is bounded (`exifMaxEntries`),
   the scanned prefix is bounded (`exifMaxScan`), recursion depth is
   capped at 2. All offsets are checked against slice length before
   indexing.

6. `archive.go: safeArchiveName`/`ListArchive` (around lines 60-138),
   none, verified sound. Listing reads only the zip central directory via
   `io.ReaderAt`, entries are capped (`limits.ArchiveEntriesListed`) with
   truncation reported back to the caller, and skipped unsafe names are
   counted rather than dropped invisibly. `safeArchiveName` is correctly
   documented as a display filter, not a path-traversal guard, since
   archive names are never opened.

7. `worker/worker.go: Run` (around lines 45-95), none, verified sound
   isolation. `runtime.GOMAXPROCS(1)` is set and the jail
   (`jail.Apply`, `jail.InstallSeccomp`) is installed before the control
   socket is opened, so the first message read happens after the sandbox
   is live. The worker never receives a path, only descriptors passed
   over `SCM_RIGHTS`.

8. `worker/worker.go: handle` (around line 175), none, verified sound. A
   parent-supplied `req.MaxPixels` can only lower the compiled-in
   `DefaultDecodeLimits().MaxPixels`, never raise it. Correct hardening
   against a compromised or buggy parent trying to widen the worker's
   decode ceiling.

9. `worker/worker.go: readAll` (around line 232), none, verified sound.
   Input is read under `io.LimitReader(f, MaxInputBytes+1)` with an
   explicit post-check; the worker enforces its own 256 MiB ceiling
   independent of what the parent already checked, documented as
   deliberate defence in depth.

10. `wire.go: Response.Encode` (around line 208), none, deliberate,
    scoped truncation, not a defect. A worker's error string past
    `MaxErrorLen` (1024 bytes) is silently truncated. This field carries
    diagnostic text only, never user data or decoded image content; the
    doc comment states the intent explicitly.

11. `service.go: Service.generate` (around lines 118-145), none. The
    input file's close error is ignored with a comment explaining why
    (read-only descriptor on an ACL/VFS-validated path, matching the
    pattern used elsewhere in the codebase).

12. `worker/audit/main.go` (whole file), none. A standalone diagnostic
    command (`package main`), correctly kept out of the `preview`/
    `worker` packages proper. Shells out only to itself
    (`exec.Command(self, args...)`), never to an external binary or a
    caller-controlled path.

13. `pool.go: NewPool`/`PoolOptions.Exe` (around line 105), none.
    `exec.Command(p.opt.Exe, p.opt.Args...)` uses argv slices, `Exe`
    defaults to `os.Executable()`, this process's own binary. No
    `ffmpeg`/`imagemagick` or any external tool invocation found anywhere
    in `preview` or `preview/worker`; decoding is entirely in-process.

14. `worker/probe.go` and `worker.go` (`JobProbe` path, around line 165),
    none. Reachable only via a `Request` whose `Kind == JobProbe`, and
    `DecodeRequest` validates `Kind` against a small enum before any
    handling. Test/proof infrastructure correctly gated behind the same
    wire validation as ordinary jobs.

### Rebuild notes

- Keep the parent/worker wire codec service-internal and explicitly
  excluded from whatever document defines "wire shapes" for the
  presentation layer.
- Preserve the two-limit design as a named invariant: graceful
  pixel/dimension limits checked before allocation, plus a hard
  `RLIMIT_AS` backstop enforced by the jail, with the graceful check
  required to fire first for the common case.
- Preserve "worker never receives a path, only descriptors" and "parent
  limit can only be lowered, never raised, by the worker" as explicit
  spec requirements.
- Preserve the exec-based (not fork-based) worker model and
  lazy-replacement-on-crash pool design; document the `SOCK_SEQPACKET`
  framing choice as intentional.
- State that `store/fsatomic`, once extracted from `vfs`, is the intended
  home of `ReplaceFileDurable`, and that `preview/cache.go` repoints to
  it with no behavior change.
- No defects found requiring a fix-first document; this package can
  proceed straight to spec-writing in its own phase as scheduled.

## runtimecfg

### Findings

1. `check.go: CheckSMBRender` (around lines 91-99), misplacement, minor,
   not previously flagged. `runtimecfg` imports `smb` (`smb.Render`,
   `smb.Config`) to validate a proposed SMB configuration by rendering
   it. This is a sideways service-to-service import (settings phase
   reaching into the SMB phase). The intent is legitimate (do not
   duplicate the renderer's rules), but `settingscheck.checkSMB` performs
   the identical probe independently (see settingscheck finding 2), so
   the dependency exists twice for one purpose.

2. `runtimecfg.go:40` (import) and fields `Hardening jail.Policy`,
   `DBGuard store.GuardConfig`, none. `runtimecfg` imports `jail`
   (domain infrastructure, any layer may use it) and `store`
   (service importing persistence, the correct direction under the
   target table). Not a violation.

3. `runtimecfg.go: Load` (boot-time value handling, roughly lines
   260-340), none, design intent confirmed sound. Malformed stored
   values are clamped or dropped with a logged warning rather than
   refused, since the trust boundary (administrator input at save time)
   is already validated by `Check`/`CheckListen`/`CheckHost`/
   `CheckCIDR`/`CheckSMBRender`; the boot-time read of an
   already-validated document degrading gracefully is sound.

4. `runtimecfg.go: readInt`/`readUint`/`readString`/`readStrings`/
   `readBool` (roughly lines 350-430), duplication, minor. Five
   near-identical helpers pulling one typed value out of a
   `map[string]any` section. Reasonable given five different types; a
   rebuild that gives settings storage a typed schema removes the whole
   family at once.

5. `check.go`/`runtimecfg.go` split, none. Coherent: `check.go` holds
   pure validation used by both the boot-time loader and the save-time
   check; `runtimecfg.go` holds the `Values` struct, defaults, bounds,
   and the live `Holder`.

6. `Holder` type (`runtimecfg.go`, roughly lines 235-260), none. Correctly
   synchronized with a `sync.RWMutex`; `Set` releases the lock before
   calling the update callback, avoiding a callback-under-lock deadlock
   risk.

### Rebuild notes

- Decide once whether SMB preview validation lives in `runtimecfg`, in
  `settingscheck`, or as an exported "dry validate" entry point on `smb`
  itself that both call, instead of two packages both importing the
  renderer for the same purpose.
- The five `read*` helpers can collapse if settings storage gets a typed
  schema instead of `map[string]any`; that decision belongs to the
  `store/state` settings shape, not to `runtimecfg` alone.

## settingscheck

### Findings

1. `check.go:29` (import), used at lines 481, 483, 489, 491-495, 564,
   570, misplacement, confirmed and more concrete than the survey states.
   Every `apierr` use is in two functions, `Refused` and `stringList`.
   `Refused` returns:

   ```go
   return &apierr.RequestError{
       Status: http.StatusUnprocessableEntity, Code: apierr.CodeInvalidRequest,
       Message: "the settings change was refused",
       Key:     apierr.MessageKey(f.ReasonKey), Args: args,
   }
   ```

   This is a service package constructing a presentation-layer wire error
   (an HTTP status baked in) instead of a domain error. `settingscheck`
   has no business knowing HTTP status codes. `stringList` (lines 564,
   570) does the same thing inline with `apierr.Unprocessable`, a second
   call site for the same root cause, not a separate one.

2. `check.go: checkSMB` (roughly lines 415-450), duplication, confirmed.
   Renders an SMB config with `smb.Render(cfg, nil)` to validate it,
   duplicating `runtimecfg.CheckSMBRender`'s identical technique with
   different inline defaults (`"WORKGROUP"`, `"scsvc"` as string literals
   here versus `defaultSMBWorkgroup`/`DefaultSMBServiceUser` constants in
   `runtimecfg`). The two default values can silently drift.

3. `check.go: checkHomes`/`probeWritable` (roughly lines 351-410), none.
   Creates a temp file with `os.CreateTemp`, checks the close error,
   always attempts removal, and correctly surfaces the close error over
   the remove error. Legitimate side-effecting probe, cleans up after
   itself.

4. `check.go: checkWatch` (roughly lines 461-479), none. Reads
   `/proc/sys/fs/inotify/max_user_watches` as a host-boundary input and
   correctly treats a read failure or unparseable content as "skip the
   check" rather than crashing or blocking a save.

5. `check.go: hostAllowed` (roughly lines 300-310), none. The function's
   own comment documents a past bug (accepting wildcard forms) and its
   removal.

6. `check.go` package shape, none, coherent despite touching many
   unrelated settings sections. One dispatch function (`Section`) plus
   one `check*` function per section is a reasonable, low-duplication
   shape for a validation package with several independent concerns.

7. `check.go: Bounds()` (roughly lines 505-525), none. Re-wraps
   `runtimecfg`'s per-field `Bound*()` functions into a lookup map rather
   than reimplementing bounds; the coupling is real and intentional, one
   more sign that `settingscheck` and `runtimecfg` are one
   settings-validation concern split into two packages because
   `emergency` cannot reach the handler-side code.

### Rebuild notes

- The `apierr` dependency is confirmed exactly as the survey states, with
  two call sites (`Refused`, `stringList`) rather than one. Both need to
  move to a `settingscheck`-owned error type; the presentation layer
  (httpapi handler, emergency mux) maps it to `apierr.RequestError`.
- Resolve the `smb.Render` duplication between `checkSMB` and
  `CheckSMBRender` in the same document that resolves the equivalent
  duplication under `runtimecfg`: one canonical preview-render entry
  point, most naturally exported by `smb` itself.

## emergency

### Findings

1. `emergency.go:47` (import), used throughout (`mw.TrustedSet`,
   `mw.ResolvedClient`/`ResolveClient`, `mw.SessionCookie`), misplacement,
   confirmed and more severe than the survey states. The survey calls
   this "imports httpapi/mw, a service package mounting presentation
   middleware." The file is not a service package with a stray import,
   it is a complete presentation-layer HTTP surface: `Handler(d Deps)
   http.Handler` builds an `http.ServeMux`, registers five HTTP routes,
   sets cookies, parses `Origin` headers for a same-origin check
   (`sameOrigin`, roughly lines 150-172), reads/writes JSON with a size
   limit (`decode`, roughly lines 465-486), and wraps everything in its
   own network-scope gate (`gate`, roughly lines 120-140) that duplicates
   the shape of `httpapi`'s middleware chain, reimplemented locally
   rather than composed from `mw`. The package is presentation wiring
   with embedded service-shaped logic (calling `auth.Service`, `state.DB`,
   `settingscheck` directly), matching the survey's stated rebuild
   direction but understating how much of the file is HTTP surface code.

2. `emergency.go: gate`/`sameOrigin` (roughly lines 120-172),
   duplication, confirmed and concrete. `sameOrigin` is a narrower,
   hand-rolled cousin of `mw.CSRF` in `httpapi/mw/csrf.go`. `mw.CSRF`
   validates a derived HMAC token against a header using the live
   app-host list; `sameOrigin` compares `Origin` against `r.Host`
   directly, justified in the code comment by the fact that the app-host
   list is one of the things this door repairs. Deliberate, documented,
   but a second independently maintained CSRF-adjacent primitive.

3. `emergency.go: decode`/`writeJSON`/`refuse`/`writeErr` (roughly lines
   465-509), duplication, confirmed. Reimplements the shape of
   `httpapi/handler`'s `decodeJSON`/`writeJSON`/`Wrap`-plus-error-mapper
   pattern, with a smaller body limit (1 MiB here versus
   `limits.RequestBody`) and manual error-to-status mapping. Expected
   given the package is documented to depend on nothing the engine owns,
   but it means JSON transport boilerplate exists in two independently
   maintained places.

4. `emergency.go: login` (roughly lines 230-290), none, noteworthy.
   Correctly reuses `d.Auth.Login` (same credential path, rate limiter,
   second-factor handling as normal login) rather than reimplementing
   authentication.

5. `emergency.go: login` (line ~253) and every `log`/`slog` call in the
   file, none, secrets checked and not found. The password is wrapped in
   `secret.New([]byte(req.Password))` before being handed to
   `auth.Login`. No call in the file logs a password, token, or session
   value. `d.Log` is stored in `Deps` but never actually invoked in this
   file, an unused-dependency smell worth a one-line note, not a defect.

6. `emergency.go: gate` (roughly lines 120-140), none, correctly
   implemented. `smb.IsPrivate(clientAddr(d, r))` gates every route, and
   a failure returns `http.NotFound`, not `http.StatusForbidden`,
   matching the documented threat model of not confirming the path
   exists to an outside caller. `clientAddr` prefers `mw.ResolvedClient`
   when the main chain already resolved the peer, falling back to
   `mw.ResolveClient` with the package's own trusted set otherwise.

7. `emergency.go: admin` (roughly lines 296-320), none. Deliberately
   supports only the session cookie, not app passwords, with the
   justification that an app password is a device capability and
   anything that could edit settings could grant itself anything. Sound,
   minimal trust-boundary decision.

8. `emergency.go: writeSettings` (roughly lines 360-395), none. Uses
   `d.State.MergeSettings` (the SQLite path, durable and transactional).
   Audit events are recorded both on success and on the write's own
   failure, matching the package's stated audit guarantee. No swallowed
   error on this write path.

9. `emergency.go: restart` (roughly lines 415-432), none, correctly
   implemented. The JSON response is flushed to the client before
   `d.Restart()` is called, avoiding a race between the response write
   and process exit. No goroutine spawned.

10. `emergency.go` whole-file cohesion, naming, minor. At 509 lines, the
    file mixes HTTP transport plumbing, authentication, settings
    read/write, and top-level composition for the degraded-server case
    (`Redirecting`, `isAsset`). Not unreasonable for a package this size,
    and the doc comment justifies a single-file shape, but a rebuild that
    treats the package as presentation-layer wiring would naturally split
    it along those seams.

11. `emergency.go: Redirecting` (roughly lines 437-462) and `isAsset`
    (roughly lines 464-467), misplacement, minor. Server-composition-root
    logic (deciding what a degraded process serves for every path)
    sitting inside `emergency` rather than in `go/internal/server`, which
    already owns `ServeEmergency` and imports `emergency`. Under the
    3-layer target this belongs with the composition root.

### Rebuild notes

- The survey's claim is correct in direction but incomplete in severity:
  `emergency` is presentation-layer HTTP wiring today in full (mux,
  cookies, CSRF-adjacent origin check, JSON transport), not a service
  package with one stray import. Treat the whole package as presentation
  from the start rather than "fix the one import."
- Keep as invariants: the 404-not-403 network gate, session-cookie-only
  admin check (no app passwords), audit-on-every-write guarantee, and
  write-then-restart response ordering, all verified sound.
- Treat as open design questions: whether `sameOrigin` becomes a
  parameterized variant of `mw.CSRF` or stays a documented exception;
  whether the JSON transport helpers move into a foundation-kit-level
  dependency-free package shared with `httpapi/handler`; whether
  `Redirecting`/`isAsset` move into `go/internal/server`.
- The `apierr` and duplicated `smb.Render` issues are `settingscheck`'s;
  `emergency` only calls `settingscheck.Section`/`Sections`/`Known`, so
  those fixes propagate automatically once `settingscheck` stops
  returning `apierr` types.

## smb

### Findings

1. `bind.go` (whole file), naming/cohesion. Bundles two unrelated
   concerns into one package. `render.go`/`safe.go` render and validate
   a Samba config; `bind.go` is a general private-network classifier
   (`IsPrivate`, `EnclosingPrivateRange`, `PrivateCIDRs`,
   `ParseAddrSpec`). The classifier is imported by
   `httpapi/mw/hostguard.go` and `emergency/emergency.go`, neither of
   which touches SMB. The package name no longer describes what two of
   its three external consumers use it for.

2. `render.go`/`safe.go`, none, confirmed clean on injection. Every
   interpolated field (`Config.Workgroup`, `ServerName`, `ServiceUser`,
   `ShareDef.Name`/`Path`/`ValidUsers`/`ReadList`/`WriteList`) is checked
   through `checkSafeValue`/`checkSafeName`/`checkSafePath`/
   `checkServerName` before `Render` builds any string. `safe.go` refuses
   (does not attempt to escape) values containing `\n\r\x00[]`, comment
   markers `;#`, a trailing backslash, `%`, or any control character, a
   correct refuse-not-escape strategy for a format with no reliable
   escape. `PasswdEntries` additionally refuses `:\n\r\x00` in names. No
   caller bypasses the checks.

3. Cross-package inconsistency, defect (denial of service, not caught at
   the trust boundary). `smb.checkSafeName` accepts any non-whitespace,
   non-control Unicode string not starting with `+-&@`, no length cap.
   `smbagent.ValidName` is far stricter (lowercase ASCII, must start with
   `a-z`/`_`, max 32 chars). A username that passes `auth`'s
   `validateUsername` (uppercase and `.@+` allowed, up to 64 chars) and
   `smb.checkSafeName` can still fail `smbagent.ValidName`. Since
   `smb.Render` validates the whole share list as one batch and
   `smbagent.Collisions` refuses the whole account sync on the first
   invalid name, one badly-named account can fail SMB config rendering
   or account sync for every user, depending on which of the three
   inconsistent validators trips first. The fix belongs at account
   creation time in `auth`, not in three downstream checks.

4. `errors.go`, none. Small, appropriately typed refusal errors
   (`BindError`, `UnsafeError`) with `Is` glue. No internal imports
   besides stdlib.

### Rebuild notes

The survey's "effectively a leaf and clean" verdict is correct for
`render.go`/`safe.go`/`errors.go`, but undersells `bind.go`, which is a
general-purpose private-address classifier misplaced in an SMB-named
package. Split `bind.go` into a foundation-kit package in the rebuild,
keeping only config rendering under the SMB phase's own package. Fix the
username validator duplication across `auth`, `smb`, `smbagent` by
picking one canonical charset (the tightest, `smbagent.ValidName`'s
system-account rule) and enforcing it once at account creation in `auth`,
so the other two checks become defense in depth.

## smbpublish

### Findings

1. `publish.go`, none. Writes go through `vfs.ReplaceFileDurable`
   consistently: `writeFile` (`smb.conf`, `network.policy`) and the
   credential files delegated to `Accounts.PublishPasswdEntries`/
   `PublishPassdb` (in `auth/passdb.go`, also using
   `vfs.ReplaceFileDurable`).

2. `disable()`, none. Removes files with `os.Remove`, tolerating
   `os.IsNotExist`; a partial removal across the four files is joined
   with `errors.Join` and returned rather than swallowed.

3. `Deps`, none, correct layering. Takes `Core`, `Grants`, `Names` as
   narrow function-typed dependencies rather than importing `core`'s SQL
   directly; `Accounts` is an interface satisfied by `auth.Service`, so
   `smbpublish` does not reach into `auth`'s persistence.

4. `shareDefs`, none, documented behavior worth carrying into the rebuild
   spec explicitly. A share with any grant carrying `g.Deny != 0` is
   dropped entirely from the rendered share, per the comment ("reaches
   the share through the web interface, where the evaluator is the
   authority"). SMB grants are strictly whole-share and additive-only;
   any single deny anywhere on that share for that user removes them
   from the SMB list even if the deny does not overlap what they would
   otherwise read or write.

5. `push()`, none. On `smbagent.Apply` failure, the files are already
   durably written and the returned error correctly says so ("the
   configuration is written but the SMB agent did not answer") rather
   than claiming total failure.

### Rebuild notes

`smbpublish` is a thin, already-correct orchestration layer between
service packages (`core`, `acl`, `auth`) and the SMB rendering/agent
packages. No SQL, no direct persistence beyond the one durable helper, no
presentation reach. Document the whole-share, additive-only,
deny-drops-entirely semantics explicitly (finding 4) so a future reader
does not assume per-permission granularity that does not exist. The split
between `smbpublish` (compute what to render, call the agent) and `smb`
(pure rendering) is clean today and should carry forward.

## smbagent

### Findings

1. `sync_linux.go:178, 204`, defect, confirmed exactly, matches survey.
   Line 178: `os.WriteFile(a.paths.candidate(), []byte(candidate), 0o600)`
   writes the scratch candidate used only for `testparm` validation.
   Lower severity: regenerated every `apply()` pass, never read by the
   running daemon, so a torn write here self-heals on the next poll.
   Still bypasses the durable primitive `accounts.go`'s `WritePasswd`
   uses two functions away in the same call graph. Line 204:
   `os.WriteFile(a.paths.SmbConf, []byte(candidate), 0o644)` promotes the
   validated configuration to the path the daemon actually reads. This is
   the defect the survey flags: a crash or power loss mid-write can leave
   `smb.conf` truncated or interleaved, and this is the file `smbd` reads
   on next start or reload, so a torn write here can fail the daemon
   entirely rather than leave one share stale. `Import()`/`Prune()`
   afterwards can partially apply against a corrupted promoted file with
   no rollback. By contrast, `accounts.go:185`'s `WritePasswd` already
   uses `vfs.ReplaceFileDurable` correctly for `/etc/passwd`, in the same
   package.

2. Trust boundary / cascading failure (see smb finding 3), defect,
   cross-package. `accounts.go`'s `Collisions()` refuses the entire sync
   (all accounts, not just the offending one) when any single desired
   entry fails `ValidName` or collides on name/uid with a real system
   account. `sync_linux.go` (roughly line 193):
   `if c := Collisions(desired, string(currentPasswd)); len(c) > 0 {
   return FailedReport(...) }`. Combined with `smb`'s equally batch-wide
   `checkShares`, a single bad account name can block SMB entirely for
   every user, and the validation that should have stopped it never runs
   at account-creation time in `auth`.

3. `control_linux.go: handle()`, naming/cohesion, minor, misleading but
   not exploitable. `maxRequestLine = 4 << 10`, reader built with
   `bufio.NewReaderSize(conn, maxRequestLine)`. The branch
   `case len(line) > maxRequestLine:` is unreachable, since `ReadString`
   on a reader whose fixed buffer equals `maxRequestLine` can never
   return a longer line; when the buffer fills first, `ReadString`
   returns `bufio.ErrBufferFull` with a truncated line, and the request
   is then correctly rejected by `json.Unmarshal` failing on incomplete
   JSON. Observable behavior is correct; the size-check branch reads as
   the enforcement point but is dead code.

4. `client.go: Do`, none. Bounds the read with
   `io.LimitReader(conn, maxReportLine)` (256 KiB) before parsing JSON,
   and bounds the whole exchange with `conn.SetDeadline` when the context
   carries one. Correct trust-boundary handling of the agent as an
   untrusted-ish peer.

5. `control_linux.go: setSocketOwner`, none, documented trade-off, worth
   carrying into a threat-model document. On failure to hand the socket
   to the server's uid, falls back to `os.Chmod(socket, 0o666)`. The
   code comment explains and defends this (capability drop prevents
   `chown`, the socket lives on a volume only the two containers and host
   root can reach). A world-writable control socket that can trigger
   `OpApply` (promotes config, touches system accounts) is a meaningful
   privilege surface if the volume-isolation assumption is ever wrong,
   but this is a deliberate, documented trade-off, not a code defect.

6. Secrets in logs across `sync_linux.go`, `smbd_linux.go`,
   `control_linux.go`, `wire.go`'s `LogReport`, none. No call logs the
   NT hash, smbpasswd line content, or any password material. Failure
   logs around credentials name counts, share names, and interface
   strings, never hash bytes.

7. Command construction (`testparm`, `pdbedit -i/-e/-x/-L`, `smbcontrol`,
   `systemctl`/`rc-service`), none. All invoked through `exec.Command`
   with argument slices, never a shell. `Prune()`'s
   `exec.Command("pdbedit", "-x", "-u", name)` is the one place
   external-origin data reaches a command argument; `name` comes from
   `PassdbNames()` (the credential database's own listing) and is
   re-checked against `ValidName` immediately before use, so a database
   entry that could look like a flag is refused before reaching the
   argument list. Handled correctly and deliberately (the `ValidName`
   comment names "argument injection into the credential tool" as the
   reason for the leading-character rule).

8. Resource handling across `control_linux.go`, `smbd_linux.go`, none.
   Listeners and connections are closed via `defer` with `nolint`
   comments explaining why close errors are not actionable. Command
   invocations use one-shot, self-closing methods
   (`CombinedOutput`/`Output`/`Run`). Child processes inherit
   stdout/stderr (nothing to leak) and are reaped via `syscall.Wait4`.
   No leaked descriptors or unclosed pipes found.

9. Locking in `sync_linux.go`, none. `Agent` guards all mutable state
   (`smbd`, `bound`, `promoted`, `last`) behind one mutex, and every
   exported method touching them takes the lock. `Smbd` has no lock of
   its own, but every path to it goes through an already-locked `Agent`
   method. No race found.

10. Cohesion across the package, naming/cohesion. Mixes daemon/process
    control (`control_linux.go`, `smbd_linux.go`, `devices_linux.go`,
    `scope.go`) with account/credential reconciliation
    (`accounts.go`, most of `conf.go`) and the IPC wire format
    (`wire.go`, shared with the non-Linux-only `client.go`). `wire.go`
    and `client.go` have no dependency on Linux, system accounts, or the
    daemon and could be a separate protocol package callable from both
    sides without pulling in `exec.Command`/`syscall`. Account
    reconciliation, daemon supervision, and network detection are three
    distinct concerns sharing one flat package and one `Agent`
    god-object (`sync_linux.go`'s `apply()`) that calls into all three.

11. `sync_linux.go: Fingerprint()`, none/minor. Calls `Detect(...)` on
    every poll tick when interfaces are not pinned, discarding the error
    (`if s, err := Detect(...); err == nil`). A transient interface-read
    failure silently contributes nothing to the fingerprint rather than
    being surfaced; since it is a polling heuristic and `apply()` itself
    does check `Detect`'s error, the failure mode is "poll misses a
    change," not silent corruption.

### Rebuild notes

The survey's "mostly `ReplaceFileDurable`" statement is confirmed
accurate for `smbpublish` and `accounts.go`'s `WritePasswd`, and
confirmed inaccurate for the two `os.WriteFile` calls in `sync_linux.go`
at lines 178 and 204, exactly as the file-persistence inventory states.
Route both through the persistence primitive in the SMB phase, with the
promoted `smb.conf` write as the higher-priority fix since a torn write
there can take the daemon down entirely. Split `smbagent` along the seam
in finding 10: a protocol/wire piece shared by client and server, a
daemon-supervision piece, a network-scope piece, and an
account-reconciliation piece. Fix the username-validation duplication
(smb finding 3, smbagent finding 2) as one piece of work spanning `auth`,
`smb`, `smbagent`: define the charset once, enforce it at account
creation, let the other two checks become safety nets.

## Documents required

- `docs/refactor/auth/00-overview.md`: target shape of the auth phase
  (session, credential, TOTP, recovery, groups, master-key, audit
  aggregates), what moves to `store/state`, and the seam replacing the
  `smb` import in `passdb.go`.
- `docs/refactor/auth/01-credentials-and-sessions.md`: password hashing,
  session lifecycle, app passwords, recovery codes, the three-tier
  credential cache.
- `docs/refactor/auth/02-master-key-and-crypto.md`: key ring format,
  rotation protocol (the three-step crash-recoverable sequence), AEAD
  binding rules, the SMB NT-hash sidecar publication and its seam fix.
- `docs/refactor/auth/03-oidc-integration.md`: how auth's OIDC flow/link
  tables integrate with the `oidc` package's `LinkStore` seam or its
  replacement.
- `docs/refactor/auth/04-audit-log.md`: the append-only audit log shape,
  cursor pagination, actor-name resolution.
- `docs/refactor/auth/05-username-policy.md`: the single canonical
  username validation rule shared across `auth`, `smb`, `smbagent`,
  replacing the three inconsistent validators found in this audit.
- `docs/refactor/oidc/00-relying-party.md`: the `oidc` package as-is,
  since it is clean; mostly a behavioral transcription (discovery
  validation, JWKS caching, JWS verification, the SSRF address guard).
- `docs/refactor/upload/00-overview.md`: scope of the rebuilt upload
  service, its dependency on core/vfs/store, and the durability
  invariants (write-then-record, sync-before-rename, copy-sync-
  record-delete) as normative requirements.
- `docs/refactor/upload/01-session-lifecycle.md`: session states, the
  create/patch/finalize/abort state machine, resolution of the
  `StateFinalizing` question, and the sweep's orphan-collection contract.
- `docs/refactor/upload/02-spool-modes.md`: offset-addressed versus
  name-ordered spooling, the interval set as source of truth for
  completeness, assembly ordering for name-ordered sessions.
- `docs/refactor/upload/03-cache-spool.md`: budget math, merger
  concurrency protocol, staging-then-rename publish pattern, and the vfs
  boundary question (does the rebuilt vfs get a non-share safe-root
  capability, or does the cache spool move under persistence).
- `docs/refactor/upload/04-verification-and-limits.md`: checksum and
  whole-file verify semantics, per-account resource limits, the
  transfer-id alias trust boundary.
- `docs/refactor/store/upload-aggregate.md`: the upload schema and
  whether `UploadSession`'s row type should be internally grouped.
- `docs/refactor/preview/00-overview.md`: layer boundary, the
  parent/worker process split, the wire codec's scope (not a
  presentation wire shape).
- `docs/refactor/preview/01-decode-limits.md`: the two-tier limit design,
  the heap-per-pixel table, format sniffing rules.
- `docs/refactor/preview/02-jail-and-worker-pool.md`: seccomp/Landlock
  policy, exec-based worker lifecycle, lazy replacement on crash, the
  `worker/audit` tool's role.
- `docs/refactor/preview/03-cache.md`: thumbnail cache key derivation,
  negative-result caching, the repoint to `store/fsatomic`.
- `docs/refactor/preview/04-archive-listing.md`: the zip central-
  directory listing contract, truncation and skip reporting.
- `docs/refactor/settings/00-runtimecfg.md`: boot-time versus save-time
  validation split, the SMB preview render duplication fix (shared with
  settingscheck), the typed-schema decision that removes the `read*`
  helper family.
- `docs/refactor/settings/01-settingscheck.md`: the domain-error type
  replacing `apierr.RequestError`, and the presentation-side mapping it
  requires in the httpapi handler and the emergency mux.
- `docs/refactor/settings/02-emergency.md`: emergency as presentation-
  layer wiring in full (mux, cookies, CSRF-adjacent origin check, JSON
  transport), the invariants to preserve (404-not-403 gate, session-
  cookie-only admin, audit-on-every-write, write-then-restart ordering),
  and the `Redirecting`/`isAsset` move into `go/internal/server`.
- `docs/refactor/smb/00-config-rendering.md`: `smb`'s `Render`/`safe.go`
  contract as a security spec (refuse-not-escape rules, the unsafe-
  character table), and the `bind.go` classifier's move to a
  foundation-kit package.
- `docs/refactor/smb/01-publish-and-agent-protocol.md`: the `smbpublish`
  orchestration flow and the `smbagent` wire protocol, including the
  whole-share/additive-only/deny-drops-entirely semantics and the socket
  trust model.
- `docs/refactor/smb/02-agent-durable-writes.md`: the fix for
  `sync_linux.go`'s two `os.WriteFile` calls, with the promoted
  `smb.conf` write as the priority fix.
- `docs/refactor/smb/03-agent-package-split.md`: the split of `smbagent`
  into protocol, daemon-supervision, network-scope, and
  account-reconciliation seams.
