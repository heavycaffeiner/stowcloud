# Content serving, preview, and share links

The content path of `sc-preview` + `sc-http` — the subsystem with the highest
remote-code-execution risk in the project, because it is the only place
arbitrary user-uploaded bytes reach a parser.

---

## 1. Two independent threats

| Threat | Vector | Defense |
|---|---|---|
| **Stored XSS** | User-uploaded HTML/SVG/PDF rendered on the app origin → session theft | Content-origin separation (§2) |
| **Decoder RCE** | A crafted JPEG/TIFF/MKV fed to a parser → memory corruption | Separate worker process + Landlock + seccomp (§4) |

Neither defense substitutes for the other. Both are required.

---

## 2. Content origin and signed URLs

### 2.1 Principle

The app origin (`app.example.com`) never returns user-content bytes. Every
download, preview, and stream comes from `content.example.com`, which **has
no session cookie.** An XSS there has nothing to steal.

Access control is a capability (a signed URL), not a cookie.

Both hosts are served by the **same binary**. `HostGuard` middleware
(`DESIGN-API.md` §9) routes on the `Host` header — a request that arrives on
the content host never has its session cookie parsed at all; it goes straight
to the signature-only router. The security property comes from the browser's
same-origin policy, not from process separation, so there is no need to run
two processes.

### 2.2 Token format

```
https://content.example.com/c/<payload>.<sig>

payload = base64url( postcard::to_bytes(&Claim) )
sig     = base64url( HMAC-SHA256(key[kid], payload)[..16] )
```

```rust
#[derive(Serialize, Deserialize)]
pub struct Claim {
    pub v:    u8,            // format version
    pub kid:  u8,            // signing key id (for rotation)
    pub fid:  u64,           // FileId
    pub etag: [u8; 8],       // first 8 bytes of the ETag -> auto-invalidates on change
    pub exp:  u64,           // unix seconds
    pub disp: Disposition,   // Attachment | InlineThumb | Stream
    pub dim:  Option<(u16, u16)>,  // thumbnail size
    pub sub:  u32,           // issuing UserId (0 = public link). Audit only
}
```

Verification is **fully stateless** — no DB lookup, constant-time HMAC
comparison, `exp` check, `etag` recheck (`410 Gone` if the file changed).

### 2.3 Expiry

| Disposition | Default TTL | Why |
|---|---|---|
| `InlineThumb` | 5 min | Only needed while the listing renders |
| `Attachment` | 15 min | Enough slack to start a download |
| `Stream` | 12 h | Must not expire mid-playback of a 2-hour video |

The long `Stream` TTL is an accepted trade-off: a leaked URL grants access to
that one file for that window. IP binding is skipped deliberately — it false-positives
constantly on mobile networks. Instead, `etag` binding invalidates on content
change, and every issuance is audit-logged. Admins can lower `content.stream_ttl`.

**Key rotation**: up to 4 `kid`s valid at once; new issuance always uses the
newest. Revoking a `kid` after a leak kills every URL signed with it.

### 2.4 Response headers

```
Content-Disposition: attachment; filename="fallback.jpg"; filename*=UTF-8''%EC%82%AC%EC%A7%84.jpg
X-Content-Type-Options: nosniff
Content-Security-Policy: default-src 'none'; sandbox
Cross-Origin-Resource-Policy: same-site
Referrer-Policy: no-referrer
X-Robots-Tag: noindex, nofollow
Cache-Control: private, max-age=<remaining exp>, immutable
Accept-Ranges: bytes
```

**`inline` is used only for our own re-encoded output.** Original bytes are
always `attachment`, regardless of type. Thumbnails are decode-then-re-encode
AVIF/WebP, so they're safe to inline. This one rule removes the entire
SVG/HTML-upload XSS path.

`filename` is RFC 5987-encoded with an ASCII fallback. CR/LF/quotes are
stripped to block header injection.

### 2.5 Single-domain deployment

For deployments that cannot run a separate content origin, a fallback exists
— **not recommended, and the server warns about it at startup.**

With `content_hosts` empty, `content::content_url` (`crates/sc-http/src/content.rs`)
builds a **host-relative** `/c/{token}` instead of an absolute URL. This is
the only function in the binary allowed to assemble a `/c/` URL; every caller
(`fs_link`, `public_link_download`, and any future one) must go through it.
That rule exists because of a real bug: building the URL inline as
`format!("https://{host}/c/{token}")` with an empty host does not fail — the
WHATWG URL parser collapses the empty authority for a special scheme like
`https` and resolves the host as **`c`**, a real, unrelated, unreachable
domain, not "no host." A same-tab `<a href>` to it stranded the browser on
`chrome-error://chromewebdata/`; `curl` reported "Could not resolve host: c".
Both `.dev/sc.toml` and production run with `content_hosts` empty, so this
was live, not theoretical, and it cost a debugging round before `content_url`
became the single choke point. The frontend also opens downloads in a new tab
(`target="_blank"`) specifically so a malformed URL stalls an unused tab
instead of destroying the one the user was on.

`content_get`'s own single-origin gate (`crates/sc-http/src/routes.rs`)
accepts a `/c/{token}` request on whatever `Host` the browser is already
pointed at when `content_hosts` is empty, so the relative URL always resolves
to the one origin that will actually answer it.

Startup diagnostics warn loudly whenever `content_hosts` is empty:

```
[sc]   content origin: NONE — serving user content from the app origin
[sc]     A stored-XSS in an uploaded file can reach the session cookie.
[sc]     Set `content_hosts` to a separate hostname to restore the
[sc]     isolation (DESIGN-PREVIEW.md §2). Acceptable for local use.
```

The fallback still forces `Content-Disposition: attachment` and a `sandbox`
CSP; it just cannot buy the origin-separation guarantee, and the warning says
so rather than pretending the isolation still holds.

---

## 3. MIME detection

```rust
/// Trusts neither the extension nor the client-supplied Content-Type.
fn sniff(fd: &FileHandle) -> Sniffed {
    let mut head = [0u8; 8192];
    let n = pread(fd, &mut head, 0)?;
    match infer::get(&head[..n]) {          // magic bytes only
        Some(t) => Sniffed::Known(t.mime_type()),
        None    => Sniffed::Unknown,
    }
}
```

- **Preview eligibility is decided by magic bytes only.** An HTML file named
  `.jpg` never reaches a decoder.
- The served `Content-Type` is fixed at `application/octet-stream` (except
  thumbnails). Combined with `nosniff`, the browser has no room to reinterpret
  it as an executable type.
- The web UI's file-type icon is chosen client-side from the extension — the
  server deliberately never states a MIME type (`DESIGN-API.md` §4.3).

---

## 4. Preview worker process

### 4.1 Why a separate process

Image/video decoders are historically the largest source of memory-corruption
vulnerabilities. Pure-Rust decoders and resource limits still leave logic
bugs — crashes, infinite loops — and a thread-based isolation shares the
address space, so memory corruption there is full process compromise.

**`sc-server` pre-forks worker children at startup and talks to them over a
Unix socket.**

### 4.2 The worker's jail

```rust
fn enter_jail() -> Result<()> {
    // ① Filesystem access denied entirely. I/O only through handed-off FDs.
    Ruleset::default()
        .handle_access(AccessFs::from_all(ABI::V4))?
        .create()?                      // empty ruleset: no path is granted
        .restrict_self()?;

    // ② Resource limits
    setrlimit(RLIMIT_AS,     512 * MiB)?;
    setrlimit(RLIMIT_CPU,    10)?;      // seconds
    setrlimit(RLIMIT_NOFILE, 16)?;
    setrlimit(RLIMIT_NPROC,  0)?;       // no fork
    setrlimit(RLIMIT_CORE,   0)?;       // core dumps could leak user data

    // ③ seccomp allow-list (22 syscalls). socket() and friends are absent -> no network.
    SeccompFilter::new(Action::KillProcess)
        .allow(&[read, pread64, write, pwrite64, mmap, munmap, mremap, brk,
                 close, fstat, lseek, futex, sched_yield, exit, exit_group,
                 rt_sigreturn, rt_sigprocmask, madvise, getrandom, clock_gettime,
                 recvmsg, sendmsg])
        .load()?;
    Ok(())
}
```

Jobs are handed over as file descriptors via `SCM_RIGHTS`:

```
parent -> worker: { job_id, kind: Image|Video, target_w, target_h } + [input fd, output fd]
worker -> parent: { job_id, result: Ok{bytes_written} | Err{reason} }
```

The worker is never given a path — only open descriptors — so path traversal
in a decoder has nothing to traverse.

A worker crash (seccomp kill, OOM, segfault) is a normal event: the parent
detects it, fails that one job, and re-forks a replacement worker. **A crash
that does not take down the service is the entire point of this shape.**

This matches `crates/sc-preview/src/worker/jailed/mod.rs` and
`worker/jailed/seccomp.rs` exactly, down to the syscall count: Landlock is
`ABI::V4` with an empty ruleset, the allow-list is the 22 syscalls above
(the pseudocode above omitted `recvmsg`/`sendmsg` for a while — they're how
the worker receives its job and its FDs in the first place, so they have to
be there), and no `execve` is anywhere in the allow-list. The real jail is
proven against a running kernel, not just asserted: `examples/jail_proof.rs`
attempts `open("/etc/passwd")`, `socket()`, and `fork()` from inside a live
worker and checks that each one is denied or the worker is killed — 9/9 in
the last verified run.

### 4.3 Images

- Pure-Rust decoders only: `image` + `zune-jpeg` / `zune-png` / `webp` /
  `avif-decode`. No C library (libjpeg-turbo, libpng, ImageMagick) is linked.
- Size is read from the header before decoding and capped:
  `max_pixels = 100_000_000` (decompression-bomb guard).
  `image::io::Limits` also caps `max_alloc`.
- Output: AVIF (default) or WebP. Size presets `64 / 128 / 256 / 512 / 1024`
  — arbitrary sizes would blow up the cache and turn into a CPU DoS. A
  request rounds up to the nearest preset.
- Only EXIF orientation is applied; the rest of EXIF is stripped, so GPS
  coordinates don't leak into a thumbnail shared over a public link.

### 4.4 Video

**Current behavior: an honest refusal.** The §4.2 jail's 22-syscall allow-list
and empty Landlock ruleset have no `execve`. ffmpeg needs subprocess
execution and a Landlock rule scoped to one input file, so it categorically
cannot run inside this jail — loosening the allow-list is not on the table
(this jail is kernel-verified 9/9 in `examples/jail_proof.rs`, and staying
that way matters more than adding a feature to it). So:

- Videos are identified by magic bytes (`sniff::Sniffed::is_video`), never by
  extension — same principle as §3.
- A request identified as video routes to `worker::JobKind::Video`, and the
  worker (jailed or `InProcessWorkerPool`) refuses immediately without
  touching a single byte.
- The refusal is classified as `error::NegativeReason::Unimplemented`, not
  `DecodeError` or `UnsupportedFormat` — without that distinction, "we don't
  support this" and "this file is corrupt" look identical to a client.
- Anywhere preview capability is advertised (the listing API's `preview`
  field, etc.) advertises nothing for video — no capability is claimed that
  doesn't exist.

**Why a second jail isn't built yet.** Running ffmpeg needs a
structurally different jail from §4.2 — `execve` plus a Landlock rule scoped
to one input file (sketch below, unimplemented). That's not a small addition:
it opens a whole new attack surface (ffmpeg's own SSRF history via crafted
HLS/concat playlists, arbitrary URL requests), and like the §4.2 jail it
would need to be proven against a real kernel before it can be trusted. A
second, unverified jail shipped in a hurry is worse than not shipping one —
it lets people believe in a guarantee that was never actually checked. So the
honest refusal stands for now, and the sketch below is future work.

#### Unimplemented sketch — a second jail plus ffmpeg

```rust
// A different process kind from the worker jail, applied after execve.
Ruleset::default()
    .handle_access(AccessFs::from_all(ABI::V4))?
    .create()?
    .add_rule(PathBeneath::new(PathFd::new(&input_path)?, AccessFs::ReadFile))?
    .add_rule(PathBeneath::new(PathFd::new(&out_dir)?,   AccessFs::WriteFile))?
    .restrict_self()?;
```

```
ffmpeg -hide_banner -loglevel error -nostdin
       -protocol_whitelist file        # blocks SSRF via HLS/concat playlists
       -analyzeduration 5M -probesize 5M
       -ss <10% mark> -i <input>
       -frames:v 1 -vf scale=… -f image2 -
       -threads 2 -timelimit 15
```

`-protocol_whitelist file` is the load-bearing flag. Stock ffmpeg can be made
to issue arbitrary URL requests through a `.mkv` disguised as an HLS
playlist — a classic SSRF/local-file-leak path. Pinning the protocol to
`file`, with Landlock further limiting that `file` access to one target,
closes it.

ffmpeg is an **optional dependency**, never bundled into the `sc:core` image
(to keep it distroless). Video thumbnails, if enabled, require the `sc:media`
image variant or a sidecar.

### 4.5 PDF and office documents

**Not rendered server-side.** A PDF renderer has a larger attack surface than
an image decoder (JavaScript, fonts, embedded images), and office-document
conversion needs a resident LibreOffice, which conflicts with the
"lightweight" goal.

Instead, `pdf.js` runs inside a `sandbox` iframe on the content origin —
rendering happens in the user's browser, off the app origin. Office documents
get download-only, no preview.

### 4.6 Text and code

UTF-8-validated and returned as-is, under a size cap (default 1 MiB).
Highlighting is CodeMirror, client-side. The server never parses it.

---

## 5. Archive preview

Listing only; extraction is a separate, explicit action.

```rust
pub struct ArchiveLimits {
    max_entries:        u32,   // 10_000
    max_total_uncompressed: u64,  // 1 GiB
    max_ratio:          u32,   // 100 — compression-ratio cap (zip bomb)
    max_depth:          u16,   // 32
    max_name_len:       u16,   // 255
}
```

- Every entry name is validated through `SafePath::parse`. `../`, absolute
  paths, or a NUL byte reject the whole archive (zip slip).
- Symlink entries, hardlink entries, and device nodes are rejected.
- Extraction stops the moment cumulative decompressed size exceeds the cap;
  it's a streaming decode, so nothing is held in memory at once.
- Nested archives are not decompressed (recursion bomb).
- Encrypted archives get listing only, no extraction.

---

## 6. Cache

```
<data>/preview/<fid % 256>/<fid>-<w>x<h>-<etag8>.avif
```

```sql
CREATE TABLE preview_cache (
  fileid INTEGER, w INTEGER, h INTEGER, etag8 BLOB,
  bytes INTEGER NOT NULL, created_ns INTEGER, atime_ns INTEGER,
  PRIMARY KEY (fileid, w, h)
) WITHOUT ROWID;

CREATE TABLE preview_negative (           -- failure cache
  fileid INTEGER, etag8 BLOB, reason INTEGER, until_ns INTEGER,
  PRIMARY KEY (fileid)
) WITHOUT ROWID;
```

- **Without a negative cache**, a file that can't be previewed would wake the
  worker on every scroll through the listing. Failures are recorded with
  their etag and not retried within the TTL (default 7 days).
- A cache entry misses whenever `etag8` no longer matches the current file —
  an external edit never serves a stale thumbnail.
- Eviction: LRU by `atime_ns` once total size exceeds the cap (default 2 GiB).
  `atime` updates are coalesced to once per 5 minutes.
- Generation concurrency is capped by a global semaphore (default = cores/2),
  so a scroll that requests hundreds of thumbnails queues rather than floods
  the worker pool.
- Same-key requests (fileid, w, h) are single-flighted — only one generation
  runs, everyone else waits on it.

---

## 7. Share links

### 7.1 Model

```sql
CREATE TABLE share_link (
  id            INTEGER PRIMARY KEY,
  token_hash    BLOB UNIQUE NOT NULL,   -- sha256(token). Plaintext never stored
  share         INTEGER NOT NULL,
  path          TEXT NOT NULL,          -- SafePath. Keyed by path, not fileid
  owner         INTEGER NOT NULL,
  perms         INTEGER NOT NULL,       -- READ | CREATE (file drop) | DOWNLOAD
  password_hash TEXT,                   -- Argon2. Low frequency, slow hash is fine
  expires_ns    INTEGER,
  max_downloads INTEGER,
  downloads     INTEGER NOT NULL DEFAULT 0,
  label         TEXT,
  created_ns    INTEGER NOT NULL
);
```

Token: 128-bit CSPRNG -> 22-char base64url. URL: `https://app.example.com/s/<token>`.

**Why path, not fileid**: if the shared target is deleted and a different
file lands at the same name, a fileid-keyed link dies (safe) while a
path-keyed link would expose the new file (unsafe). So both the path and the
fileid at creation time are stored, and access re-checks the fileid against
the current one at that path — a mismatch returns `410 Gone`. Storing both
gets the safe intersection of the two schemes.

### 7.2 Access flow

```
GET /s/<token>
  -> look up the link (token_hash), check expiry and download count
  -> if password_hash is set: password form (POST -> short-lived link session
     cookie, __Host-, scoped to path /s/<token>)
  -> target is a file: download button + preview if available
  -> target is a directory: listing. Real path, owner, and other shares are never revealed
  -> actual bytes always come from a content-origin signed URL (sub = 0)
```

- Password attempts are rate-limited (10/hour per token). A failure gets the
  same response and timing as "link doesn't exist" — a valid token's
  existence is never confirmed.
- **File drop (upload-only)**: `perms = CREATE` only. No listing, no reading
  existing files, no overwrite (name collisions get auto-renamed). Uploaded
  files are created with the owner's uid/gid and the share's mode policy.
  `GET /s/{token}` answers with `max_upload_bytes` (`body_limit_bytes`) on a
  drop link and only there — the public page has no other way to learn the
  ceiling, and without it the visitor discovers it by uploading past it.
- `max_downloads` decrements atomically; a dropped connection mid-stream is
  not rolled back — preventing abuse matters more than perfect accounting.
- Every access is audit-logged as `share.link_accessed` (IP, UA, success).
- The public page carries `X-Robots-Tag: noindex, nofollow` + `<meta
  name="robots">`.

### 7.3 Expiry enforcement

If an admin sets `sharing.max_link_ttl`, longer expirations are rejected.
Default is unlimited; the UI's default selection is 30 days.

---

## 8. Streaming archive download

Multi-select download streams a zip **without ever writing it to disk**.

```
POST /api/fs/archive { paths: [...] }  -> signed URL (Attachment, 15 min)
GET  content/…                          -> chunked zip stream
```

- ZIP64 + data descriptors, so no size needs to be known up front. Sent
  chunked, no `Content-Length`.
- Compression is **fixed to STORE** (no compression). Most selections are
  already-compressed media, and deflate would only burn CPU without shrinking
  anything. A text-heavy selection loses out, but predictability wins.
- Every entry gets its ACL re-checked; unauthorized entries are **silently
  skipped** (an error would fail the whole zip). Skipped entries are listed
  in `_skipped.txt` inside the zip.
- If a file is deleted mid-stream, that entry is truncated and streaming
  continues — the zip stays valid.
- Total size cap (default unlimited, configurable), and a cap on concurrent
  archive streams.
- The 100-second Cloudflare timeout only applies until the response starts.
  The zip header is flushed immediately, so long streaming afterward is fine.

---

## 9. Tests

| Item | Method |
|---|---|
| Signed URL | Rejection for forgery, expiry, changed etag, revoked `kid`. HMAC comparison is constant-time |
| XSS | Malicious SVG/HTML/PDF upload; every serving path returns `Content-Disposition: attachment` and never exposes the app origin |
| Worker isolation | `open("/etc/passwd")`, `socket()`, `fork()` from inside the worker — all blocked |
| Crash recovery | An input that kills the worker -> parent survives, re-forks automatically, only that job fails |
| Decode bombs | A 1-pixel-header PNG declaring 100000x100000, a high-ratio image -> rejected at the cap |
| ffmpeg SSRF | A file disguised as an HLS playlist -> zero outbound requests (verified in a network namespace) |
| Zip slip | An archive entry `../../etc/cron.d/x` -> rejected at listing time |
| Zip bomb | 42.zip -> stopped at the cap, no memory growth |
| Share links | Expiry, download-count exhaustion, password brute force, deleted-then-recreated same-name file -> `410` |
| EXIF | GPS-tagged photo's thumbnail carries no EXIF |
