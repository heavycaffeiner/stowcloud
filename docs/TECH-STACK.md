# Tech stack & algorithm reference

Single reference table for the technology and algorithm choices across the
design. Background for each choice lives in the design doc it links to.

---

## 1. Language & runtime

| | Choice | Why |
|---|---|---|
| Backend | Rust, edition 2021 (`rust-version` 1.88) | Memory safety + zero-cost abstraction. Direct syscall control |
| Async | `tokio` (multi-thread scheduler) | Ecosystem. Blocking FS work goes to a dedicated pool |
| HTTP | `hyper 1.x` + `axum 0.8` + `tower-http` | Composable middleware. HTTP/1.1 + HTTP/2 |
| Allocator | **`mimalloc`**, musl target only | musl's default allocator is several times slower under multi-threaded allocation. Wired via `cfg(target_env = "musl")`, so native Windows dev builds and the glibc CI job never link it |
| Target | `x86_64/aarch64-unknown-linux-musl`, static | distroless/scratch images |
| Build profile | `release`: `lto="thin"`, `codegen-units=4`. `release-dist` (inherits `release`): `lto="fat"`, `codegen-units=1`. Both: `panic="abort"`, `strip=true` | `release` is the fast iteration profile; `release-dist` is the one actually shipped |
| Frontend | Svelte 5 (runes) + SvelteKit 2, `adapter-static` | No virtual DOM. Favors 100k-row lists |

---

## 2. Backend crates

| Area | Crate | Purpose |
|---|---|---|
| Syscalls | `rustix` (0.38) | Direct `openat2`, `statx`, `renameat2`, `copy_file_range`, `getdents64` |
| Syscall fallback | `cap-std` | Component-wise `O_NOFOLLOW` walk when `openat2` is unavailable |
| Sandbox | `landlock`, hand-rolled seccomp-BPF (see §4) | Process and worker isolation |
| Storage | `rusqlite` (bundled) + `r2d2_sqlite` | WAL mode. No ORM |
| Hashing | `blake3`, `crc32c`, `sha2`, `hmac` | ETag / chunk checksum / tokens / signatures |
| KDF / cipher | `argon2`, `chacha20poly1305`, `zeroize`, `secrecy` | Passwords, seed encryption, memory hygiene |
| XML | `quick-xml` (`NsReader`) | WebDAV. DTD events rejected on sight |
| Serialization | `serde`, `serde_json`, `postcard` | JSON API / signed-URL claims (compact binary) |
| Data structures | `smallvec`, `compact_str`, `dashmap`, `lru` | Allocation avoidance, concurrent map, LRU |
| Watching | `inotify` (raw), `notify` (fallback) | Change detection — see §14 M3 for wiring status |
| Images | `image` (0.25, default-features off; jpeg/png/gif/webp/bmp/tiff), `infer` | Pure-Rust codecs only, no C library linked |
| EXIF | `kamadak-exif` | Orientation tag only — the rest of the EXIF blob (including any GPS IFD) is never forwarded to the encoder |
| Archives | `zip` (2.x, read-only, listing) + a hand-rolled streaming ZIP64 writer (`sc-http::archive_zip`) | The `zip` crate's writer needs `Write + Seek` to back-patch sizes; an HTTP response body is `Write`-only and chunked, so `POST /api/fs/archive` uses the data-descriptor ZIP form instead (STORE only, always ZIP64) |
| Sniffing | `infer` | Magic-byte detection |
| Unicode | `unicode-normalization` | NFC/NFD interop |
| Static assets | `rust-embed` | Frontend embedded in the binary (`embed-ui` feature, off by default — see §14 M2) |
| Observability | `tracing`, `tracing-subscriber` | Structured logs |
| CLI / config | `clap`, `toml` + `serde` | |
| Testing | `proptest`, `criterion` | Property tests exist (`sc-vfs`, `sc-dav`, `sc-upload`); no `cargo-fuzz` harness is checked into the tree yet despite being a stated goal — see §10 |

Content indexing and OCR (text extraction, FTS5/BM25 content index, OCR) are
not a dependency of anything in the workspace — no `lopdf`, `tract`, or `ort`.
This is not "off by default"; `sc-search`'s own module doc states it plainly:
*"content indexing / OCR is deliberately not implemented here."* See §11.

---

## 3. Kernel interfaces

| Purpose | Call | Key flags | Doc |
|---|---|---|---|
| Path isolation | `openat2` | `RESOLVE_BENEATH`, `RESOLVE_NO_MAGICLINKS`, `RESOLVE_NO_SYMLINKS`, `RESOLVE_IN_ROOT`, `RESOLVE_NO_XDEV` | CORE §1.2 |
| Metadata | `statx` | `STATX_BTIME` (inode-reuse detection) | CORE §1.1 |
| Listing | `getdents64` | `d_type` avoids a stat per entry | CORE §1.3 |
| Atomic replace | `renameat2` | `RENAME_NOREPLACE` | CORE §5.3 |
| Server-side copy | `copy_file_range` | reflink on btrfs/XFS | UPLOAD §7.2 |
| Sparse reservation | `ftruncate` | Assembly-free upload | UPLOAD §5.3 |
| Offset write | `pwrite` | Parallel chunks | UPLOAD §5.2 |
| Timestamp restore | `utimensat` | Preserve source mtime | CORE §5.3 |
| Transfer | `sendfile` / `splice` | Plaintext HTTP path | WEBDAV §9 |
| Watching | `inotify` / `fanotify` | `FAN_MARK_FILESYSTEM` needs `CAP_SYS_ADMIN` → off by default | DEPLOYMENT §5 |
| Sandbox | `landlock_*`, seccomp | Empty ruleset + KillProcess allowlist | PREVIEW §4.2 |
| FD passing | `SCM_RIGHTS` | Worker gets an FD, never a path | PREVIEW §4.2 |
| FS detection | `statfs.f_type` | Reject overlayfs | DEPLOYMENT §3 |

---

## 4. Algorithms

| Algorithm | Location | Complexity / property |
|---|---|---|
| **Kernel path isolation** | `sc-vfs` | `RESOLVE_BENEATH` removes TOCTOU by construction. No userspace validation needed |
| **ACL evaluation** — depth-first, same-depth DENY wins | `sc-acl` | O(depth × matching grants). Decision carries the grant that caused it — explainable |
| **ACL cache invalidation** — global generation counter | `sc-acl` | O(1) full invalidation |
| **Directory aggregate ETag** — mark ancestors dirty, lazy recompute | `sc-meta` | O(depth) writes per change, O(depth × fanout) per read. Sibling subtrees keep their cache |
| **fileid mapping** — `(dev, ino, btime)` → stable u63 | `sc-meta` | `btime` blocks inode reuse. Unsupported filesystems fall back to the `path` strategy |
| **IntervalSet** — sorted, non-overlapping runs, binary-search insert + merge | `sc-upload` | O(log n + merges). Normal use holds 1–5 runs |
| **Assembly-free upload** — sparse `ftruncate` + offset `pwrite` | `sc-upload` | Zero copies. Memory use is independent of chunk size |
| **Crash-safe ordering** — `pwrite` then commit, never the reverse | `sc-upload` | Always under-reports on crash → idempotent resend |
| **NC chunk assembly** — in-order append (fast path) / out-of-order spool + `copy_file_range` merge | `sc-compat-nc` | Fast path is zero-copy. Merge is near-free too when reflink is available |
| **Listing session** — sort by name, cache, `statx` per page | `sc-http` | First page of 100k entries costs a few dozen syscalls |
| **Lazy revalidation** — cached stat vs. live `statx` | Every layer | Correct even if the watcher is dead; only performance degrades |
| **Search T2 (first-class path)** — work-stealing parallel `getdents64` walk | `sc-search` | 256 KiB buffer, zero `statx`, ACL subtree pruning. Reference implementation (`fd`) measured 855 ms warm over 4M files. BFS, so a budget cutoff still leaves shallow levels complete |
| **Parallelism auto-tuning** | `sc-search` | Detects `rotational`: HDD → 2 threads, NVMe → 16. Directories with < 64 entries run single-threaded (avoids small-case regression) |
| **Inode-order stat batching** | `sc-search` | Only matched entries get `(dev, ino)`-sorted stat calls. Cuts spinning-disk seeks by roughly an order of magnitude |
| **Search T3 (opt-in)** — **block-compressed trigram index** (plocate model) | `sc-search` | 32 filenames/block, zstd-compressed together; postings point at blocks, not documents. No position data. High-df trigrams pruned. ~17 B/file measured (plocate: 27M files → 466 MB). Flat file outside the main DB, no `node` row required |
| **T3 post-filter** — over-match loop | `sc-search` | Decompress false-positive blocks, linear scan + ACL post-filter. Capped at `MAX_SCAN = 10 × page` |
| **Estimator** — HyperLogLog cardinality of distinct trigrams | `sc-search` | Hand-rolled (~80 lines, `hll.rs`), not a crate — the estimate has to be auditable line by line. p=14 → 16 KB, ≈0.8% error |
| **Ranking** — weighted sum (exact match 3.0 / prefix 2.0 / BM25 1.0 / recency 0.5 / current scope 0.3 / hidden −1.0) | `sc-search` | No learning, no click logs. The BM25 term is part of the design for a future content index and has no live input today — see §11 |
| **Rate limiting** — dual token bucket (IP hard / account soft) + exponential delay | `sc-auth` | `delay(n) = min(250ms · 2^(n−3), 30s)` |
| **Auth cache** — process-ephemeral-key HMAC → LRU | `sc-auth` | ~250,000× cheaper than Argon2. No fast hash ever touches disk |
| **Signed URLs** — stateless HMAC-SHA256 (128-bit truncated) | `sc-http` | Zero DB lookups. Bound to the ETag, so it self-invalidates on change |
| **Single-flight** — per-key `Arc<Mutex>` | Thumbnails, directory ETag | Prevents duplicate concurrent work on the same key |
| **Debounce/coalesce** — 200 ms | Invalidation, events, audit | Caps write storms during bulk copies |
| **Virtual scroll** — fixed row-height windowing | `web` | O(viewport). `contain: content` + `translate3d` — not `strict`, which needs a definite size these elements no longer have (`proposals/stowcloud-3-frontend.md`) |
| **MD3 color generation** — HCT tonal palette | m3-svelte's `genCSS`, run once by hand | Zero runtime *and* zero build-time dependency: the output is a static table in `app.css` |

---

## 5. Crypto primitives

| Use | Algorithm | Parameters / rationale |
|---|---|---|
| Account password | **Argon2id** | m=48 MiB, t=3, p=1. PHC string storage → parameter migration without downtime. Concurrency semaphore of 4 |
| High-entropy tokens (session, app password, share link) | **SHA-256** | 160–256 bits of entropy already — a slow hash adds nothing |
| Signed URL / auth cache key | **HMAC-SHA256** (128-bit truncated) | Constant-time comparison. Cache key uses a process-ephemeral key |
| ETag / integrity | **BLAKE3** | 16-byte truncation. Never hashes content — metadata only |
| Chunk checksum | **CRC32C** | SSE4.2 / ARMv8 hardware acceleration, effectively free |
| TOTP seed, SMB NT hash storage | **XChaCha20-Poly1305** | AAD = user id. Master key lives in a secret file |
| TOTP | **HMAC-SHA1** (RFC 6238) | 6 digits / 30 s / ±1. SHA-1 is required for authenticator-app compatibility |
| SMB NT hash | **MD4(UTF-16LE)** | Protocol-mandated. Derived alongside the Argon2id hash at account-creation time, stored AEAD-encrypted. Trade-off discussed in DEPLOYMENT.md §7.2 |
| Randomness | `getrandom` | Sessions, tokens, signing keys, ephemeral keys — all of it |

---

## 6. Protocols

| Protocol | Spec | Implementation | Reachable today? |
|---|---|---|---|
| Native API | REST/JSON + WebSocket | `sc-http` | Yes — full route table live |
| Upload | **TUS 1.0.0** core | `sc-upload`. 5 MiB floor / 10 MiB default / no ceiling | Yes |
| WebDAV | **RFC 4918 Class 2** + RFC 4331 (quota) | `sc-dav`. `REPORT`/`SEARCH` not implemented | Yes — every Class 2 method is registered and reachable at `/dav/{*path}` |
| NC compat | status.php, OCS v1/v2, Login Flow v2, chunking v2, `oc:*`/`nc:*` properties | `sc-compat-nc` | Yes, default-on feature. Login Flow v2 verified end to end against a real client |
| SMB | SMB3.1.1 (Samba orchestration) | `sc-smb` renders `smb.conf`/`smbpasswd`/passwd entries; `sc-server smb-sync` writes them, gated by the LAN-only bind check. The Samba sidecar that reads them is `Dockerfile.smb` + `deploy/smb/` + the `sc-smb` service in `docker-compose.yml` | Yes, off by default — verified by an authenticated `smbclient` round-trip against that image |
| Real-time | WebSocket + SSE (search stream) | 30 s heartbeat (Cloudflare idle-timeout workaround) | Yes — `sc-watch` feeds `WsHub`, and a client's own WebSocket subscribe is what registers the OS-level watch |

---

## 7. Storage

| Target | Technology | Note |
|---|---|---|
| **Source of truth** | Filesystem | The DB is a rebuildable cache, always |
| Meta cache | SQLite (WAL, `synchronous=NORMAL`) | Single writer task + read pool |
| Name index (T3, off by default) | Self-built block-compressed trigram flat file (`names.idx`) | 32 filenames/block, zstd. Outside SQLite, no `node` row needed |
| Content index (T4) | Not implemented — see §11 | |
| Thumbnails | Files (`<data>/preview/`) + SQLite accounting | LRU eviction + negative cache |
| Sessions, locks, uploads | SQLite + in-memory | Survives restart |
| Samba passdb | Would be `smbpasswd` file → `tdbsam` | Only meaningful once the Samba sidecar (§6) exists to consume it |

**No Redis, no PostgreSQL.** A single-node file server gains nothing from
either and only adds operational weight.

---

## 8. Isolation layers

| Layer | Mechanism | Target |
|---|---|---|
| Path | `openat2(RESOLVE_BENEATH)` | Every FS access |
| Process | Landlock, restricted to configured share paths + data directory | Main process — applied at `serve` startup, best-effort with graceful fallback if the kernel lacks support |
| Syscall | Hand-rolled seccomp-BPF deny-list (`ptrace`, `mount`, `bpf`, `userfaultfd`, etc.) | Main process |
| Decoder | Forked child process (`fork(2)`, no `execve`) — empty Landlock ruleset, seccomp KillProcess allowlist, `SCM_RIGHTS` FD passing, RLIMIT | Image/document parsing. A re-exec'd dedicated `sc-preview-worker` binary would be a strictly better shape and is a known, deliberate follow-up — not what ships |
| Transcoder | Subprocess + single-file Landlock + `-protocol_whitelist file` | ffmpeg |
| Origin | Cookie-less separate domain + signed URLs | All user-content bytes |
| Container | Unprivileged uid, `cap_drop: ALL`, `no-new-privileges`, `read_only` | Deployment |

---

## 9. Frontend

| Area | Technology |
|---|---|
| Framework | Svelte 5 runes (`$state`/`$derived`/`$effect`), SvelteKit 2 SPA |
| Design | **m3-svelte** — the full MD3 system, components included. `@material/web` still rejected (web-component interop friction); the hand-built token layer that preceded this is deleted |
| Color generation | m3-svelte's own `genCSS`, run once by hand from seed `#3F6C4F`; the output is a static table in `app.css`. No generator script, no build step, no dependency |
| Grid | 4px, enforced in CI by a custom `stylelint` plugin |
| Lists | Fixed row-height virtual scroll, `contain: content` |
| Upload | Dedicated Web Worker + IndexedDB-backed resume |
| Editor | CodeMirror 6 |
| Budget | Initial JS < 150 KB gzip |

---

## 10. Verification tooling

| Target | Tool | Status |
|---|---|---|
| Property tests | `proptest` — `sc-vfs`, `sc-dav`, `sc-upload` | In the tree |
| Fuzzing | `cargo-fuzz` — `SafePath::parse`, WebDAV XML, TUS headers | **Not yet in the tree.** No `fuzz/` crate, no `libfuzzer-sys` dependency — a stated goal, not a shipped one |
| WebDAV conformance | Litmus | Not wired into CI — no reference to it anywhere in `scripts/` or `.github/workflows/` |
| NC compatibility | Login Flow v2 verified live against a real client | No automated desktop/Android client regression suite in CI |
| Frontend | Vitest + Testing Library (jsdom), `svelte-check`, `stylelint` | In `package.json`. No Playwright, no axe-core, no Lighthouse CI despite earlier claims to that effect |
| Supply chain | `cargo-deny` (advisories/licenses), `cargo-audit`, `cargo-auditable` (SBOM embedded in the binary) + CycloneDX SBOM artifact — `.github/workflows/supply-chain.yml` | Running in CI. No image signing (no cosign anywhere in the workflows) |
| Architecture | NC-vocabulary grep gate, `ConnectInfo` bind-site gate, `--no-default-features` build — in `scripts/verify.sh`; the route dump is the `route_drift` test its `cargo test` step runs | Running, 12/12 |

---

## 11. Rejected, or explicitly out of scope

| Rejected | Reason |
|---|---|
| `@material/web` | Web-component interop friction + bundle size. Tokens alone are enough |
| ORM (Diesel / SeaORM) | Few queries, and the generated SQL can't be controlled |
| PostgreSQL / Redis | Pure operational overhead for a single-node file server |
| **A from-scratch SMB3 server** | Years of work, and the protocol surface is tens of times WebDAV's. Head-on conflict with "security first" |
| ksmbd | Kernel module — large blast radius, unsuited to container deployment |
| JWT sessions | Can't be revoked instantly. A server-side session record is a requirement |
| HTTP Digest auth | Forces storage of password-equivalent material |
| ImageMagick / libvips / LibreOffice | C/C++ attack surface + image bombs. Replaced by pure-Rust decoders |
| Server-side PDF rendering | The renderer's attack surface is wider than a decoder's. Client-side `pdf.js` in a sandboxed iframe instead |
| **Elasticsearch / Tantivy** | Overkill for filename search. The JVM/Lucene NAS products that took this road hit CPU blowups past 4–5M files, mitigated only by capping the hit count — i.e. shipping less search. A JVM search engine has no place on a 32 GB SSD, low-RAM box |
| FTS5 trigram for the **name** index | ~90 B/file plus a mandatory `node` row. Block-compressed trigram does the same job at ~20 B/file with no `node` requirement |
| One index structure for names and content | Different cost drivers — entry count for names, document length for content |
| **Content indexing (T4, FTS5/BM25) and OCR** (`lopdf`, `tract`/`ort`) | Explicit non-goal, not a disabled feature. `sc-search`'s own module doc: *"content indexing / OCR is deliberately not implemented here."* No dependency for either exists anywhere in the workspace |
| Semantic search / embeddings / scene classification | Target hardware has no NPU/GPU. The NAS vendors that ship semantic search restrict it to GPU-equipped models for the same reason |
| **`jwalk` / `ignore` / `walkdir`** | All three sit on `std::fs`'s path-based API, which bypasses `ShareRoot`'s `openat2(RESOLVE_BENEATH)` invariant. A whole-tree walk is exactly the worst place for isolation to leak. The hand-rolled walker resolves one component at a time from the parent dirfd, and is faster for it |
| Userspace search-result cache | The kernel's dentry cache already does this job — no point paying for the same data twice |
| Search index enabled by default | The parallel walk covers most deployments. Indexing 10M files at ~900 MB on a 32 GB SSD isn't justified as a default |
| Adaptive chunk sizing | No payoff for the complexity. Fixed size + parallelism gets the same throughput |
| Optimistic UI updates | `412` conflicts aren't rare on a shared folder; rollbacks would be constant |
| `chown -R` on a volume | Destructive to every other service's access |
| xattr for metadata storage | Conflicts with backup tools, other services, filesystem migration |
| async-std | Ecosystem converged on tokio |
| musl's default allocator | Multi-threaded allocation performance — replaced by `mimalloc` on that target only |
| `async-zip` for archive writing | Needs `Write + Seek`; an HTTP response body is neither. Hand-rolled streaming ZIP64 writer instead (§2) |
| `moka` | Not actually a dependency of anything here — `lru` + `dashmap` cover the caching needs in the tree |
