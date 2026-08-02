# Upload design

Code-level extension of `ARCHITECTURE.md` §6 and `DESIGN-CORE.md` §5.

---

## 1. Findings and decisions

### 1.1 The reference server, as measured (2026-07 master)

| Item | Value | Source |
|---|---|---|
| Server `max_chunk_size` (legacy) | 10 MiB = `10485760` | `occ config:app:set files max_chunk_size` |
| Server `files.chunked_upload.max_size` (NC 34+) | 100 MiB = `104857600`; `0` disables chunking | system config |
| Desktop `_initialChunkSize` | `100LL * 1024LL * 1024LL` (100 MiB) | `libsync/syncoptions.h` |
| Desktop `_targetChunkUploadDuration` | `std::chrono::minutes(1)` — **0 disables dynamic adjustment** | same |
| Desktop `_minChunkSize` | `chunkV2MinChunkSize = 5LL*1000*1000` (5 MB) | same |
| Desktop `_maxChunkSize` | `chunkV2MaxChunkSize = 5LL*1000*1000*1000` (5 GB) | same |
| Desktop `_parallelNetworkJobs` | `6` | same |
| Env overrides | `OWNCLOUD_CHUNK_SIZE`, `OWNCLOUD_MIN_CHUNK_SIZE`, `OWNCLOUD_MAX_CHUNK_SIZE`, `OWNCLOUD_TARGET_CHUNK_UPLOAD_DURATION`, `OWNCLOUD_MAX_PARALLEL` | `libsync/syncoptions.cpp` |
| Protocol: chunk name | "a number between 1 and 10000" | developer manual |
| Protocol: chunk size | "must be between 5MB and 5GB (except for the last chunk)" | same |
| Protocol: assembly order | "assembled in the order of their names" | same |
| Protocol: session expiry | 24h inactivity | same |

Source comment: *"In chunkingNG, when dynamic chunk size adjustments are done, this is the starting value and is then gradually adjusted within the minChunkSize / maxChunkSize bounds."*

### 1.2 Two practical problems this creates

**(A) NC desktop starts at 100 MiB → the first chunk 413s at Cloudflare.**
A server can advertise `files.chunked_upload.max_size` and the client still won't always honor it (desktop#7980, notably the bulk-upload path). So we advertise a value, but **returning a spec-correct 413 to drive the client's own auto-adjust (desktop#4826) is part of normal operation, not an error path.** A 100 MiB request never reaches us in the first place — Cloudflare answers its own 413 before we get a chance to attach a header — so capabilities advertisement and documentation are the first line of defense, not a server-side check.

**(B) "Assembly-free assembly" does not hold for the NC protocol.**
Chunk size is variable, so a chunk's name (index) alone doesn't give its offset. NC assumes assembly in name order. §7 handles this with a fast path / spill path.

### 1.3 Decision

- **The native protocol uses a fixed chunk size.** No dynamic adjustment.
- **Floor 5 MiB (enforced), default 10 MiB, no ceiling.**

```toml
[upload]
chunk_min_bytes    = 5242880    # enforced floor; exempts the last chunk and files smaller than this
chunk_default_bytes = 10485760  # advertised default; admin-configurable
```

There is no `chunk_max_bytes` key — see "Why no ceiling" below for why one was never wired in. The advisory value published to compat clients (`files.chunked_upload.max_size`) is **not** independently configurable today: `sc-server` sets it equal to `chunk_default_bytes` (`app.rs::upload_config`). There is no environment auto-detection (no "behind Cloudflare → lower the advisory" logic exists); it is a static mirror of the default.

Throughput comes from **fixed chunk size × parallel transfer** (4 concurrent by default, currently a hardcoded engine default, not exposed via `[upload]`). What dynamic chunk sizing buys elsewhere, parallelism buys here more simply.

#### Why no ceiling

The server **streams a chunk straight to disk.** The body flows through a reused 256 KiB buffer into `pwrite`; nothing buffers a whole chunk anywhere. **Memory use is independent of chunk size.** A 1 GiB chunk leaves RSS unchanged.

So instead of a size cap, defense is **time and concurrency**:

| Defense | Value |
|---|---|
| Body idle timeout | configured at 60s of silence; enforced by `uploads_patch` and `uploads_create`'s creation-with-upload branch via `read_body_with_idle_timeout` (`routes.rs`), aborting with 408 on a silent body |
| Whole-request timeout | `upload.request_timeout`, no default (a live stream just keeps going) |
| Concurrent upload requests per user | 8 (configured; enforcement not verified against a live code path) |
| Reserved-byte accounting | §6 — defends against `ftruncate` sparse-reservation overcommit |

#### Why a 5 MiB floor

- The compat protocol already forces 5 MB — a lower floor here would split the rules across the compat path.
- Every chunk costs a session load + DB commit + FD lookup. A 100 KiB chunk against a 10 GB file is 100,000 round trips; overhead swamps transfer.
- Exception: the **last chunk**, and a file **smaller than the floor** to begin with. The test is `offset + len == total_len`.

#### What "no ceiling" doesn't mean

Cloudflare (100 MB), nginx `client_max_body_size`, and other middleboxes still exist. Not enforcing a ceiling means **we don't do the rejecting** — it doesn't make the physical constraint disappear. So:

- We advertise `chunk_size_advisory` as a recommendation, never a requirement.
- A 413 from something upstream is handled entirely client-side (§8).
- The admin UI can note "this looks like it's behind Cloudflare, ≤10 MiB recommended" without stopping a user from setting 64 MiB.

---

## 2. Native protocol — TUS 1.0.0 + a minimal extension

Why a standard: resume semantics are already proven, plain `POST`/`HEAD`/`PATCH` gets no special treatment from any proxy, and third-party clients already exist.

### 2.1 Endpoints

```
OPTIONS /api/uploads
  204
  Tus-Resumable: 1.0.0
  Tus-Version: 1.0.0
  Tus-Extension: creation,creation-with-upload,expiration,termination,checksum
  Tus-Max-Size: <upload.max_file_size>      # header omitted entirely when unlimited (default)
  Tus-Checksum-Algorithm: crc32c,blake3

POST /api/uploads
  Tus-Resumable: 1.0.0
  Upload-Length: <total>                  # or Upload-Defer-Length: 1
  Upload-Metadata: filename <b64>,mtime <b64>,filetype <b64>,
                   dest <b64>,relativePath <b64>,ifMatch <b64>
  Sc-Random-Access: 1                     # optional: opt in to parallel transfer (§2.3)
  Content-Type: application/offset+octet-stream   # for creation-with-upload
  →
  201 Created
  Location: /api/uploads/<id>
  Upload-Expires: <HTTP-date>
  Upload-Offset: <n>                      # however much creation-with-upload delivered

HEAD /api/uploads/<id>
  200
  Upload-Offset: <end of contiguous run from 0>   # standard TUS clients resume from here
  Upload-Length: <total>
  Sc-Received-Runs: <run count>           # debugging
  Cache-Control: no-store

PATCH /api/uploads/<id>
  Content-Type: application/offset+octet-stream
  Upload-Offset: <offset>
  Upload-Checksum: crc32c <base64>        # optional
  →
  204 No Content
  Upload-Offset: <end of contiguous run from 0, after this request>
  Upload-Expires: <HTTP-date>

DELETE /api/uploads/<id>  → 204
```

`dest` is the destination directory's vpath; the leaf appended to it is `relativePath` (directory uploads) or, failing that, `filename` (single-file uploads) — neither key alone names a target.

### 2.2 Status codes

As actually mapped by `UploadBridge::upload_err` (`sc-server/src/bridge.rs`) through `sc-http`'s `ErrorCode` → `StatusCode` table, not the aspirational TUS status list:

| Code | Condition |
|---|---|
| `404` | No such session (GC'd after expiry), or a session `HEAD`/`PATCH` from a user who doesn't own it |
| `409` | Sequential-mode `Upload-Offset` doesn't match the current offset |
| `410` | Session has expired or been aborted (see §5, `HEAD` on a cancelled session) |
| `412` | `If-Match` mismatch at finalize, **and** checksum mismatch — checksum wants TUS's `460`, but `sc-http` has no dedicated code for it yet, so it gets the nearest available (412) instead. Still "don't blindly retry the same bytes," just not the exact TUS status |
| `422` | Malformed `Upload-Metadata`, a chunk under the floor that isn't the last chunk, or `offset + len` exceeding the declared length — all surfaced as `FsInvalidName`/Unprocessable Entity, not `400` |
| `429` | The general per-IP rate limiter can trigger this on the upload routes like any other route. A dedicated upload-session-creation rate limit (`upload.create_rate`) is defined in the engine's error type (`UploadError::RateLimited`) but nothing in `sc_upload::engine` constructs it — that specific limit is unwired |
| `507` | A resource limit from §6 (session count, reserved bytes, free space margin) |

There is no server-side validation of the `Tus-Resumable` request header's value — the server always answers with its own `Tus-Resumable: 1.0.0` but a missing or mismatched client header is not currently rejected.

### 2.3 `Sc-Random-Access` — a superset of the standard

TUS core requires `Upload-Offset` to exactly match the server's current offset (sequential only). One extension covers parallel transfer:

- `Sc-Random-Access: 1` at session creation lets `PATCH` accept any offset within `[0, total_len)`.
- Without opting in, a mismatched offset is `409`, as standard TUS specifies.
- **Either way, `HEAD`'s `Upload-Offset` is "the contiguous run ending from 0."** A standard TUS client resuming a random-access session behaves correctly without knowing anything changed — it just resends already-received bytes, which land identically.

Only our own web client sends this header. Third-party clients get the standard sequential path.

---

## 3. Types

```rust
// sc-upload/src/model.rs

#[derive(Clone, Copy, PartialEq, Eq, Hash)]
pub struct SessionId([u8; 16]);              // CSPRNG. 22-char base64url. Unguessable = hijack defense

#[derive(Clone, Copy, PartialEq, Eq)]
pub enum SessionState { Receiving, Finalizing, Done, Aborted, Expired }

#[derive(Clone, Copy)]
pub enum SpoolMode {
    /// Native: client tells us the offset, so no assembly is needed
    OffsetAddressed,
    /// NC chunking v2: assembled in name order — §7
    NameOrdered,
}

pub struct UploadSession {
    pub id:            SessionId,
    pub user:          UserId,
    pub share:         ShareId,
    pub dest:          SafePath,
    pub part_name:     CompactString,        // ".scpart-{id}"
    pub spool_dir:     Option<CompactString>,// NameOrdered only: ".scpart-{id}.d"
    pub mode:          SpoolMode,
    pub total_len:     Option<u64>,          // None = Upload-Defer-Length
    pub chunk_size:    u32,                  // fixed at session creation, never changes after
    pub random_access: bool,
    pub received:      IntervalSet,
    pub next_name:     u32,                  // NameOrdered fast-path cursor
    pub write_head:    u64,                  // NameOrdered fast-path write position
    pub if_match:      Option<Etag>,
    pub meta:          UploadMeta,
    pub created_ns:    i128,
    pub expires_ns:    i128,
    pub state:         SessionState,
}

pub struct UploadMeta {
    pub filename:      String,               // display only. `dest` is authoritative
    pub mtime_ns:      Option<i128>,         // restores the original mtime
    pub mime:          Option<String>,       // a hint only. Never trusted when serving
    pub relative_path: Option<String>,       // directory uploads
    /// Opt-in whole-file check at finalize: algorithm plus the digest the
    /// finished file is expected to hash to.
    pub verify:        Option<(VerifyAlgo, Vec<u8>)>,
}
```

`chunk_size` is fixed to the session so an admin changing the server setting mid-upload can't break a session in flight. On resume, the client follows the `Sc-Chunk-Size` header from `HEAD`.

**`UploadMeta.verify` carries an expected digest and is checked at finalize.** `UploadEngine::verify_whole_file` reads the finished file, computes the CRC32C or BLAKE3 digest, and compares it (constant-time, via `subtle`) against the expected value carried in `verify`. A mismatch unlinks the part file and deletes the session rather than leaving mismatching bytes at the destination or a resumable session against data that will never verify.

---

## 4. IntervalSet

The set of received byte ranges for one session. Maintains a sorted, non-overlapping, non-touching (adjacent runs always merged) invariant.

```rust
#[derive(Default, Clone, PartialEq, Eq)]
pub struct IntervalSet { runs: SmallVec<[(u64, u64); 2]> }   // [start, end)

impl IntervalSet {
    /// Cap on run count. Pathological/malicious clients only — normal use
    /// (1 run sequential, a handful for `parallel` random-access) never
    /// comes close.
    pub const MAX_RUNS: usize = 4096;

    pub fn insert(&mut self, start: u64, end: u64) -> Result<(), Fragmented> {
        debug_assert!(start < end);
        let i = self.runs.partition_point(|r| r.1 < start);   // merge-candidate start
        let mut j = i;
        let (mut s, mut e) = (start, end);
        while j < self.runs.len() && self.runs[j].0 <= e {
            s = s.min(self.runs[j].0);
            e = e.max(self.runs[j].1);
            j += 1;
        }
        if self.runs.len() - (j - i) + 1 > Self::MAX_RUNS { return Err(Fragmented); }
        self.runs.splice(i..j, [(s, e)]);
        Ok(())
    }

    /// End of the contiguous run from 0. TUS `Upload-Offset`.
    pub fn contiguous_prefix(&self) -> u64 {
        match self.runs.first() { Some(&(0, e)) => e, _ => 0 }
    }

    pub fn is_complete(&self, len: u64) -> bool {
        matches!(self.runs.as_slice(), [(0, e)] if *e == len)
    }

    /// Varint delta encoding. 3 bytes for a sequential upload.
    pub fn encode(&self, out: &mut Vec<u8>) { /* count, then (start-prev_end, end-start)* */ }
    pub fn decode(b: &[u8]) -> Result<Self, Corrupt> { /* must re-validate the invariant */ }
}
```

`decode` **never trusts** a value read back from the DB — it re-validates the sort/non-overlap invariant from scratch. This cuts off the path from "corrupted `received` blob" to "wrong offset math" to "silent data corruption."

Implemented exactly as above in `sc_upload::interval_set`, including `run_count()` (exposed to clients as `Sc-Received-Runs`) and a property test that random insertion-order permutations always converge to the same normal form.

---

## 5. State machine and ordering rules

```
                 POST                    PATCH*              completion
   (none) ──────────────▶ Receiving ─────────────▶ Receiving ──────────▶ Finalizing
                              │                                             │
                              │ DELETE / TTL elapsed                        │ success
                              ▼                                             ▼
                         Aborted / Expired                                Done
                              │                                             │
                              └──────────── GC: unlink part/spool ◀─────────┘
```

### 5.1 The absolute ordering rule

> **Commit the DB only after `pwrite` succeeds. Never the reverse.**

Under this order, a crash makes the server remember a received chunk as "not received" → the client resends the same offset → the same bytes land again. **Idempotent and safe.**

The reverse order (commit first) makes the server believe it has bytes it never durably received: **silent data corruption.** This is pinned by a code comment, a crash-injection unit test (`ordering_rule_crash_between_write_and_commit`), and code review.

For the same reason, `fdatasync` happens **once, at finalize** — not per chunk. Syncing every chunk would collapse throughput; skipping it entirely is still safe under the ordering rule above (a crash can only ever make the server under-report, never over-report, what it has). There is no per-chunk durability toggle in the current engine — sync-per-chunk would need to be added, not switched on, if a deployment needed it.

### 5.2 Write path (OffsetAddressed)

```rust
async fn patch(&self, sid: SessionId, offset: u64, mut body: BodyStream) -> Result<u64> {
    let s = self.load(sid)?;                                   // includes an ownership check
    ensure!(s.state == SessionState::Receiving, Gone);
    if !s.random_access && offset != s.received.contiguous_prefix() { bail!(Conflict) }

    let fd = self.part_fd(&s)?;                                // cached open FD
    let mut written = 0u64;
    let mut crc = Crc32c::new();
    let mut buf = self.pool.get();                             // reused 256 KiB buffer

    while let Some(chunk) = body.next().await {
        let c = chunk?;
        if offset + written + c.len() as u64 > s.total_len.unwrap_or(u64::MAX) { bail!(Unprocessable) }
        if written + c.len() as u64 > s.chunk_size as u64 { bail!(PayloadTooLarge) }
        crc.update(&c);
        pwrite_all(fd, &c, offset + written)?;                 // ① disk
        written += c.len() as u64;
    }
    if let Some(expect) = s.checksum_header { ensure!(crc.finish() == expect, ChecksumMismatch) }

    let mut s = s;
    s.received.insert(offset, offset + written)?;
    self.store.commit(&s)?;                                    // ② DB. always after ①
    Ok(s.received.contiguous_prefix())
}
```

A checksum mismatch leaves the bytes on disk but never records them in `received`, so that range stays "not received." A resend overwrites it. Consistency holds.

### 5.3 Finalize

```rust
fn finalize(&self, s: &UploadSession) -> Result<()> {
    ensure!(s.received.is_complete(s.total_len.unwrap()), Incomplete);
    let dir = self.vfs.open_dir(s.share, s.dest.parent())?;
    let fd  = self.part_fd(s)?;

    if let Some(algo) = s.meta.verify { verify_whole_file(fd, algo, &s.meta)?; }   // opt-in; one re-read pass, digest-only (§3)

    fdatasync(fd)?;
    if let Some(mt) = s.meta.mtime_ns { utimensat_fd(fd, mt)?; }                   // restore original mtime
    match self.vfs.stat_at(&dir, s.dest.name()) {
        Ok(prev) => {                                    // replacing an existing file
            if let Some(want) = &s.if_match {
                ensure!(file_etag(&prev) == *want, PreconditionFailed);            // §5.4 window applies
            }
            transplant_metadata_from(fd, &prev)?;                                  // inherit mode/ownership
            renameat2(&dir, &s.part_name, &dir, s.dest.name(), 0)?;
        }
        Err(ENOENT) => {                                 // new file
            fchmod(fd, policy.mode_file)?;
            if let Some((u, g)) = policy.chown { fchown(fd, u, g)?; }
            renameat2(&dir, &s.part_name, &dir, s.dest.name(), RENAME_NOREPLACE)?; // window closed
        }
        Err(e) => return Err(e.into()),
    }
    self.meta.mark_dirty(s.share, &s.dest);
    self.store.delete(s.id)
}
```

Replacing an existing file inherits its permissions and ownership before the rename — otherwise an atomic replace silently strips whatever access other services sharing the directory (Jellyfin, the *arr stack, rsync) had a moment ago.

### 5.4 Known limitation

There is no atomicity between the `If-Match` check and `renameat2` (a window of microseconds) — the kernel has no compare-and-rename, so this can't be closed in principle. **A new file closes the window completely via `RENAME_NOREPLACE`.** This asymmetry is documented rather than hidden.

---

## 6. Resource limits

Checked in full at session creation; any failure is `507` (or, where noted, unenforced).

| Item | Config key | Default | Enforced? |
|---|---|---|---|
| Concurrent sessions per user | `max_sessions_per_user` | 32 | yes |
| Reserved bytes per user | `max_reserved_bytes_per_user` | 100 GiB | yes |
| Max single file | `max_file_size` | unlimited (share quota is the practical cap) | not checked by the engine independently of quota |
| Chunk minimum | `chunk_size_min` | 5 MiB (last chunk exempt) | yes |
| Chunk maximum | — | **none** | by design — see §1.3 |
| Body idle timeout | `body_idle_timeout` | 60s | yes — `routes.rs`'s `uploads_patch`/`uploads_create` |
| Concurrent upload requests per user | — | 8 (documented default) | not found wired to a live check |
| Session TTL | `session_ttl` | 24h (matches NC) | yes |
| Session creation rate | `create_rate` | 60/min/user | **`UploadError::RateLimited` exists but nothing constructs it — unwired** |
| Free-space margin | `free_space_margin` | 2 GiB + file size | yes |

`ftruncate`-created sparse files don't consume space immediately, so without **logical accounting of reserved bytes**, overcommit would exhaust the disk. Available space is `statvfs`'s real free space minus the sum of every open session's undelivered bytes.

---

## 7. Compat chunking v2 mapping

**All of this lives in `sc-compat-nc`.** The core upload engine knows nothing about NC.

### 7.1 Protocol mapping

| NC request | Engine call |
|---|---|
| `MKCOL /remote.php/dav/uploads/{user}/{tid}` + `Destination` | `create(dest, total_len=None, mode=NameOrdered)`. `{tid}` is client-chosen and mapped to our own `SessionId` via `nc_upload_alias` — **never used as the session key directly** (it's client-controlled: enumeration/collision risk) |
| `PUT .../{tid}/{name}` + `OC-Total-Length` | `put_named(sid, name.parse::<u32>()?, body)` — §7.2 |
| `MOVE .../{tid}/.file` + `Destination`, `OC-Total-Length`, `X-OC-Mtime` | `assemble_and_finalize(sid, total_len, mtime)` |
| `DELETE .../{tid}` | `abort(sid)` |
| `PROPFIND .../{tid}` | returns the spooled chunk list (client resume) |

`{name}` is documented as an integer 1–10000, but some real clients send zero-padded offset strings instead. **Parsed as an integer and used only as a sort key** — its value carries no other meaning. A parse failure is `400`.

### 7.2 Fast path / spill path

Variable chunk size means the offset isn't knowable in advance. Two paths:

```rust
fn put_named(&self, s: &mut UploadSession, name: u32, body: BodyStream) -> Result<()> {
    if name == s.next_name {
        // ── fast path: arrives in order → write straight to the end of the part file. Zero copies
        let n = pwrite_stream(self.part_fd(s)?, body, s.write_head)?;
        s.write_head += n;
        s.next_name  += 1;
        // absorb any already-spooled chunks that are now next in line
        while let Some(sp) = self.spool_take(s, s.next_name)? {
            let n = copy_file_range_all(sp.fd, self.part_fd(s)?, s.write_head)?;
            s.write_head += n;
            s.next_name  += 1;
            sp.unlink();
        }
    } else {
        // ── spill path: out of order → its own file in the spool directory
        self.spool_write(s, name, body)?;
    }
    self.store.commit(s)                                   // always ① disk → ② DB
}
```

- The spool directory sits **in the same directory as the part file** (`.scpart-{id}.d/`) — same filesystem, so `copy_file_range` is efficient and `EXDEV` never happens.
- `copy_file_range` reflinks on btrfs/XFS when block-aligned (effectively free), otherwise falls back to an in-kernel copy. **Either way, no userspace round trip.**
- NC desktop runs 6-way parallel, but completion order tends to stay close to sorted, so the fast path hits often; the spool only absorbs the reordering.

### 7.3 Assembly and completion

```rust
fn assemble_and_finalize(&self, s: &mut UploadSession, total: u64, mtime: Option<i128>) -> Result<()> {
    // append remaining spooled chunks in ascending name order ("assembled in the order of their names")
    for name in self.spool_names_sorted(s)? {
        let sp = self.spool_take(s, name)?.unwrap();
        s.write_head += copy_file_range_all(sp.fd, self.part_fd(s)?, s.write_head)?;
        sp.unlink();
    }
    ensure!(s.write_head == total, BadRequest("OC-Total-Length mismatch"));
    s.received = IntervalSet::full(total);
    s.total_len = Some(total);
    s.meta.mtime_ns = mtime;
    self.core.finalize(s)?;                     // §5.3, shared path
    self.rmdir_spool(s)
}
```

Once assembly is done, finalize runs **exactly the native path**: atomic rename, ownership transplant, mtime restore, `mark_dirty`.

### 7.4 Side effect

`NameOrdered` mode creates a hidden directory `.scpart-{id}.d/` in the destination directory (skipped when only the fast path was ever used). It is removed on completion, abort, and GC either way. This is one reason the native protocol is preferable, and is called out in the admin-facing docs.

### 7.5 Capabilities advertisement

```json
"files": {
  "bigfilechunking": true,
  "chunked_upload": { "max_size": 10485760, "max_parallel_count": 4 }
},
"dav": { "chunking": "1.0" }
```

`chunked_upload.max_size` carries `upload.chunk_size_advisory`. The NC field name says "max," which reads as an enforced ceiling — **it isn't.** We never enforce it; an oversized chunk is accepted normally. The field exists purely to tell a client what is unlikely to trip an intermediary. The mismatch between the field's name and its actual meaning is called out in both the code comment and the admin docs.

NC desktop's `_minChunkSize` (5 MB, decimal) is slightly below our `chunk_size_min` (5 MiB, binary): 5,000,000 < 5,242,880, so **an NC client can legally send a chunk just under our floor.** The compat path doesn't apply `chunk_size_min` at all — the NC assembly scheme (§7) doesn't care about individual chunk sizes in the first place, so there's nothing to check.

---

## 8. Handling upstream 413s

We never generate a 413 because of chunk size (§1.3). Intermediate infrastructure still can. That 413 returns to the client before ever reaching us, so **handling it is entirely a client-side concern.**

```
client sends a large chunk
  ├─ Cloudflare / nginx / another proxy blocks it → the proxy's own 413. Never reaches the server
  │    ├─ our web client: halves the chunk size and retries, remembers what worked
  │    │                  (localStorage). Tells the user "chunk size adjusted to N MiB"
  │    └─ NC desktop: its own auto-adjust (desktop#4826) lowers maxChunkSize
  └─ reaches the server: 413 only when Upload-Length exceeds Tus-Max-Size
```

Our web client's backoff:

```
on 413:  next = max(chunk_size_min, floor(current / 2))
         if next == current: give up (rejected even at the floor → proxy misconfiguration)
         store: localStorage['sc.chunk_size'] = next   # starting point for the next session
```

A 413 at `chunk_size_min` (5 MiB) means the proxy is blocking even that — retrying won't help. The client surfaces a clear error pointing at the proxy configuration.

Server-side notes (only relevant to the whole-file-size 413):

- `Upload-Length` arrives at session creation (`POST`), before any body is read, so rejection can happen before the body starts.
- Responding mid-stream to a `Content-Length`-less chunked body requires `Connection: close` — otherwise the next request tries to parse the unread remainder of this one.
- **The upload route is deliberately excluded from `RequestBodyLimitLayer`.** Since there's no ceiling, the layer itself must not apply; §1.3's idle timeout and concurrency caps carry the defense instead.

---

## 9. GC

| Target | Interval | Action |
|---|---|---|
| Expired sessions | 15 min | unlink part file + spool directory, delete row |
| Orphaned part files | 6h | `.scpart-*` / `.scpart-*.d` with no session row, past a TTL on mtime |
| `nc_upload_alias` | alongside session GC | mapping rows cleaned up |

The sweep only checks the parent-directory set recorded in the session table — it never walks a whole share root.

**None of this currently runs.** `UploadEngine::gc` implements exactly the sweep above and is covered by tests, but its only caller in `sc-server`, `UploadBridge::drain()`, itself has no caller anywhere in the workspace — no timer, no admin route, no `sc gc --deep` command. Until something calls it, expired sessions and orphaned part files accumulate until an operator intervenes by hand. `DESIGN-CORE.md` §5.6 cross-references this same gap.

---

## 10. Testing

| Area | Method |
|---|---|
| Ordering rule | `SIGKILL` at an arbitrary point between `pwrite` and commit → resume → final bytes match the source |
| IntervalSet | property test: random insertion-order permutations converge to the same normal form; `encode ∘ decode == id` |
| Parallel | 4 threads send chunks in random order → completeness and hash verified |
| No ceiling | a single 1 GiB chunk succeeds and **process RSS doesn't move** (streaming proof). Same memory as a 5 MiB chunk |
| Floor | a 4 MiB non-last chunk → `422` + `Sc-Min-Chunk-Size`. A 4 MiB *last* chunk → succeeds |
| Idle timeout | `routes.rs`'s `patch_with_a_silent_body_aborts_instead_of_hanging`: a `PATCH` body that goes silent past `body_idle_timeout` aborts with `408`, session remains resumable |
| NC mapping | real NC desktop uploads a 1GB file. Fast-path hit rate logged. Zero spool residue afterward |
| NC out-of-order | chunks sent deliberately reversed → all spilled → `MOVE` assembles correctly |
| Resource limits | sparse-reservation abuse: 1000 sessions × 1TB declared each → `507` |
| External interference | another process renames/deletes the target file mid-upload → finalize fails safely or completes correctly |
