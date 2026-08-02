# Feature inventory

174 numbered items, 1–174 contiguous (1–161 from the original design scope,
162–174 appended for surfaces that shipped after it was written). **Numbers
are never reassigned**: they are cited from code comments, so a new surface
is appended and never inserted. Milestones live in `ARCHITECTURE.md` §14.

**Status, honestly.** Earlier revisions of this document described *design
scope*, not build state, on the theory that intent and implementation would
converge quickly. They didn't, uniformly. Three states now apply:

- **Shipped and reachable** — a user can drive it through the UI or a
  documented API call today. Unmarked items are this.
- **⚠ Implemented, not reachable** — the code exists and may even be tested,
  but nothing calls it, or nothing exposes it. Marked inline.
- **✗ Non-goal** — decided against, not merely unscheduled. Marked inline
  where it was previously described as scope; the full list is at the bottom.

No item carries ⚠ today. The last two that did were **#129/#130**, where the
watcher was wired but two pieces of it were not — `full_threshold` was
forwarded into `sc-watch` and never read, and nothing forced a periodic rescan
on NFS/FUSE mounts, where inotify does not see another host's writes at all.
`d800b57` closed both (`sc-watch`'s `rescan_loop`/`rescan_one_share` and the
overflow generation bump, with tests). The marker stays defined above because
the next partly-wired feature should be marked, not quietly described as
shipped.

---

## 1. Filesystem access & isolation (10)

1. Native FS access — the filesystem is the only source of truth; the DB is a rebuildable cache
2. Kernel-level path confinement — `openat2(RESOLVE_BENEATH)` removes TOCTOU by construction
3. Three-tier symlink policy (`Deny` / `WithinShare` / `Follow`), per share
4. Mount-boundary crossing control (`cross_mount`, `RESOLVE_NO_XDEV`)
5. Filesystem auto-detection gate — refuses to register overlayfs, auto-downgrades FUSE/NFS/CIFS
6. Bidirectional Unicode NFC/NFD lookup — macOS SMB client interop
7. Confusable-character warning badge
8. Rejects dangerous filenames — Windows reserved names, NTFS ADS, control characters, trailing dot/space, control-file prefixes
9. Landlock + seccomp self-confinement
10. Runtime kernel-feature detection + fallback — `openat2` / `statx.btime` / `renameat2` / `copy_file_range` / `landlock`

## 2. File management (17)

11. Directory listing — cursor pagination + server-side listing-session cache
12. Sort (name / size / time / kind) — name sort needs no `stat`
13. File/directory creation
14. Rename
15. Move — batched, cross-device copy fallback with pre-flight notice (`dry_run`)
16. Copy — `copy_file_range` reflink
17. Delete — batched, trash or permanent. UI: `/trash` overflow-menu entry from the browser (item 162)
18. Trash, per-share toggle, off by default (`off` / `share_local`) + TTL GC + restore
19. Direct text file read/write (editor)
20. Atomic replace — same-directory temp file + `renameat2` + parent `fsync`
21. Ownership/permission/xattr carry-over (on replacing an existing file)
22. mtime preservation/restore
23. Optimistic concurrency — `If-Match` required, `412` + conflict-resolution UI. Frontend deliberately has no optimistic UI on top of this (`DESIGN-FRONTEND.md` §4) — a shared folder makes `412` common enough that undoing an optimistic change would be the normal case
24. Streaming ZIP64 download of a multi-selection (unpermitted entries silently excluded + `_skipped.txt`). UI wiring: item 163
25. Long-running job queue — progress, per-item cancel, WebSocket push. `JobTray.svelte` (mounted in `(app)/+layout.svelte` beside `UploadTray`) shows per-item progress and offers cancel; `web/src/lib/state/job-tray.svelte.ts` drives it through `pollJob`/`jobCancel`
26. Stable `fileid` — survives rename, derived from `(dev, ino, btime)`
27. Recursive size/entry-count (`rsize` / `rcount`)

## 3. Upload (12)

28. TUS 1.0.0 resumable chunked upload
29. Fixed chunk size — 5 MiB floor, 10 MiB default, no upper bound
30. Parallel chunk transfer — `Sc-Random-Access` extension (superset of standard TUS)
31. Assembly-free upload — sparse `ftruncate` + offset `pwrite`, zero copies
32. Crash-safe resume — `IntervalSet` + under-reporting order rule
33. Browser-side resume — session kept in IndexedDB, continues after reload
34. Chunk checksum CRC32C + optional whole-file BLAKE3 verification
35. Directory upload — File System Access API / `webkitdirectory` / drag-and-drop
36. `creation-with-upload` — one round trip for a small file
37. Session GC + orphaned part-file cleanup
38. Resource caps — concurrent sessions, reserved-byte accounting, idle timeout
39. Automatic shrink on an upstream `413` + remembers the working chunk size

## 4. Permission model (11)

40. Share definition — host_path + `SharePolicy`
41. Grant — (principal × share × subpath × allow/deny × inherit × label). Admin editor UI: item 165
42. 8 permission bits — `READ WRITE CREATE DELETE RENAME MOVE SHARE DOWNLOAD`
43. Depth-first ACL evaluation — DENY wins at the same depth, default deny
44. Explainable denial — the deciding grant is attached to the response and audit log
45. Cross-check against real FS permissions — `faccessat2` probe on registration, warns on mismatch
46. Virtual roots — host paths fully hidden, label-based
47. Optional per-user home — off by default; creates from a template when enabled
48. Groups + membership — `sc_acl::Principal::Group` evaluation, `/api/admin/groups` CRUD plus membership editing, and `GroupManagementSection.svelte` on the admin page. A grant can name a group as its principal
49. Per-user quota
50. ACL decision cache + global generation counter for O(1) invalidation

## 5. Auth (14)

51. Argon2id account passwords, PHC storage for zero-downtime parameter migration
52. Minimum 10 characters + breached-password list check + strength meter (no composition rules)
53. Server-side sessions — `__Host-` cookie, sliding + absolute expiry, immediate revocation
54. Active session list / individual revocation / IP+UA logging. UI: item 168. Revoking one session closes only that session's WebSocket (`WsHub::revoke_session`, `routes.rs`'s `auth_revoke_session`), leaving every other live session — including the one performing the revoke — untouched.
55. Triple CSRF — `SameSite=Lax` + `Sc-Csrf` header + `Origin` check
56. App-scoped passwords — permission-mask/share-limited scope, expiry, usage log, individual revocation. UI: item 166
57. TOTP 2FA + recovery codes + reuse prevention. UI: item 167
58. TOTP enable/disable requires password reconfirmation (disabling also re-derives the NT hash immediately)
59. Dual rate limiting — IP hard limit + account soft exponential backoff, no account lockout
60. Enumeration resistance — uniform responses and timing
61. DAV account-password support — per-connection memo + in-process temporary verification-cache key (~250,000×)
62. Argon2 concurrency semaphore — defends against memory exhaustion from a login flood
63. Admin one-time install-token bootstrap
64. Audit log — stable event keys, no real paths recorded, configurable retention

## 6. Protocols (21)

65. Native REST/JSON API with stable machine-readable error codes
66. WebSocket — invalidation push, job progress, quota, session-revocation notice, 30s heartbeat. Invalidation fires from the watcher's forwarding thread (`app.rs`'s `start_watcher_and_ws_hub`); `WsHub::revoke_user` closes sockets on logout, account disable and account delete; `WsHub::revoke_session` closes just the one socket a specific session owns — item 54
67. SSE search-result streaming
68. WebDAV RFC 4918 **Class 2** (including LOCK)
69. WebDAV streaming PROPFIND — constant memory even at 100k entries
70. WebDAV dead properties — stored in the DB keyed by `fileid`, no xattr use
71. WebDAV LOCK — keyed by `fileid` so it survives rename, persisted, expiry sweep
72. RFC 4331 quota properties (Finder requires this)
73. Range / conditional GET (`304`)
74. Hardened XML parser — DTD/PI rejected outright, depth/element-count caps
75. Client-specific workarounds — Explorer / Finder / Office / rclone / Cyberduck / DAVx⁵
76. Compat `status.php` + OCS v1/v2 envelopes
77. NC capabilities — unsupported features explicitly reported `false`
78. NC Login Flow v2 — consent screen + **scope selection** (an extension; not in the reference server)
79. NC chunking v2 — fast-path append + spool `copy_file_range` merge
80. NC extended properties — `oc:id/fileid/permissions/size/favorite/share-types`, `nc:has-preview`, etc.
81. NC sharing API + `sharees` (enumeration resistance scopes the results)
82. NC preview endpoint → 302 to the content origin
83. NC stub endpoints (notifications / user_status / navigation)
84. NC favorites (state that only exists in the compat layer)
85. SMB 3.1.1 — Samba orchestration, signing+encryption mandatory, NTLMv2 only

## 7. SMB operations (6)

86. Account-password integration — NT hash derived in parallel, from account creation onward
87. Derivation and publishing are separate steps, so turning SMB on makes it immediately usable, and from then on a running server republishes `smbpasswd` itself whenever an NT hash changes, so a password change, a TOTP toggle or an SSO link reaches SMB without `sc-server smb-sync` (`DESIGN-AUTH.md` §13.6; `DEPLOYMENT.md` §13.7 for the three cases where it still does not)
88. Opportunistic backfill — automatic on any auth that carries a plaintext password, no re-login needed
89. Internal-network-only enforcement — refuses to generate `smb.conf` if a public address is detected, plus `hosts allow/deny` and a startup self-check
90. Per-subpath-grant SMB share auto-generation
91. Contamination guards on shared folders — `store dos attributes = no`, `map * = no`, `oplocks = no` on shared shares

## 8. Content serving & preview (12)

92. Content-origin separation — same binary, `Host`-based routing, cookies never parsed there
93. Stateless signed URLs — HMAC-SHA256, etag-bound, `kid` rotation. UI wiring (single-file download): item 163
94. Per-disposition expiry — thumbnail 5 min / download 15 min / stream 12 hours
95. Magic-byte MIME detection — extension and client-supplied header both untrusted
96. Image thumbnails — pure-Rust decoders, preset sizes, AVIF/WebP
97. EXIF stripped — orientation applied, GPS discarded
98. Isolated worker process — empty Landlock ruleset, seccomp, RLIMIT, FD passing, automatic crash recovery
99. Video thumbnails — ffmpeg jail + `-protocol_whitelist file` (blocks SSRF). **✗ Non-goal — decided against, not pending.** `worker::JobKind::Video` exists in the wire protocol and is refused with `NegativeReason::Unimplemented` before any bytes are touched (`crates/sc-preview/src/lib.rs`, `worker/jailed/mod.rs`). ffmpeg is a separate binary; the jail's workers are `fork`ed, never `execve`'d (Landlock + 22-syscall seccomp allow-list — `worker/jailed/seccomp.rs`), so running it would need either relaxing that allow-list (rejected outright) or standing up a second, structurally different `execve`-based jail with its own per-file Landlock rule — real, unverified new attack surface. `DESIGN-PREVIEW.md` §4.4 already reasons through exactly this and reaches the same conclusion: a second jail shipped without being proven against a live kernel is worse than shipping none
100. PDF client-side rendering — `pdf.js` inside a sandboxed iframe
101. Text/code preview
102. Archive listing preview + zip-slip / zip-bomb defenses
103. Thumbnail cache — LRU eviction, negative cache, single-flight, concurrent-generation cap

## 9. Share links (6)

104. Public links — token stored hashed, plaintext never persisted
105. Link password (Argon2) + attempt rate limiting + existence concealment
106. Expiry / download-count limit / label
107. File drop — upload-only, no listing or retrieval, auto-rename on name collision
108. Path + fileid double verification — recreating a same-name target answers `410 Gone`
109. `noindex` + full access audit log

Management UI (create/list/revoke, all of the above): item 164.

## 10. Search (17)

110. T1 client-side filter of the current directory
111. **T2 parallel FS tree walk — first-class path.** Work-stealing + 256 KiB `getdents64` + zero `statx` calls. Zero indexing/DB cost
112. Automatic parallelism — detects `rotational` (HDD: 2 threads, NVMe: 16), single-threaded for small corpora
113. Inode-order stat batching — minimizes seeks on spinning disks
114. SSE streaming results — first result within tens of ms
115. Honest completeness reporting — `Truncated { seen, elapsed }` are measured, not estimated
116. T3 **block-compressed trigram name index**. Off by default (`[index] name_enabled` in `config.toml`), but the default can be overridden at runtime without a restart or a config edit: `IndexSettingsStore` (`index.db`, single-row table, same shape as `sc-upload`'s `upload_chunk_settings`) holds the admin's on/off override, checked by `ensure_name_index_enabled` ahead of the config value. `StorageIndexSection` exposes the toggle plus a "Start index build" button that starts the build through the existing job queue (`POST /api/admin/index/build` → `202 { job }`, tracked in `JobTray` like any other job) rather than a bespoke progress mechanism; the crawl itself runs through `bridge.rs`'s `CrawlThrottle`, the same pacing every other disk-wide scan uses, so a build doesn't starve Jellyfin/Samba on a co-accessed share. zstd-compressed filenames (32/block), postings point at blocks, no position info, high-frequency trigrams pruned, byte-level trigrams (works on non-UTF-8 filenames). ~20–30 B/file, outside the main DB, needs no `node` row
117. Segment structure (`base` + `delta` + `tombstone`) — gives the immutable block index O(1) incremental updates, one index file per mount. Merges on idle for real: `spawn_idle_merge` runs a `std::thread`-based periodic loop (mirroring `sc-upload`'s GC thread) that calls `run_idle_merge_pass` every 10 minutes and merges each share whose index is over `merge_ratio`, and is stopped on graceful shutdown alongside the other background handles. "Idle" is deliberately narrow — it means no admin-triggered build is running, not low CPU/disk: this deployment has no load signal to sense (`CrawlThrottle`'s doc gives the same reason)
118. Optional dentry warming. **✗ Non-goal — decided against, not pending.** The design considered was a prefetch pass (kernel cache only, nothing persisted) issued ahead of the T2 walker's own directory reads, to warm the kernel dentry/page cache before the real walk touched it. Measured on the actual deployment target (the Rocky Linux guest, at the walker's own concurrency levels over a synthetic 20,000-directory tree with `/proc/sys/vm/drop_caches` between runs): a genuinely cold single-threaded walk takes ~0.8–1.0 s and a warm repeat ~0.17 s, so the kernel-cache effect itself is real — but a separate warming pass pays that same cold-read cost once just to populate the cache, and the walker then pays a second, now-cheap pass on top of it: 3.3–5.1 s combined against ~0.9 s for simply walking the tree once, directly. Adding concurrency to the warming pass made it slower, not faster (3.2 s at 16 concurrent readers, 4.2 s at 64), because this VM's directory-read latency is too low for thread parallelism to have anything to overlap. The T2 walker already warms the cache as a free side effect of its own one real read per directory; a dedicated warming step ahead of it can only duplicate that read inside a single search request, never avoid it — there is nothing left for it to buy
119. T4 content indexing. **✗ Non-goal — decided against, not pending.** The design considered here (separate `content.db` on the data volume, allow-list + hard path-count cap, opportunistic scheduling gated on load/disk-latency/temperature, isolated extraction, pre-estimate, LRU eviction, FTS5 `automerge` incremental merge) was not adopted; kept only as a record of what was evaluated
120. OCR. **✗ Non-goal — decided against, not pending.** Same status as #119; the considered design (off by default, piggybacking on thumbnail decode to reuse work, hourly budget cap, run inside the worker jail, offline model) was likewise not adopted
121. Filters — kind / time range / size range / scope
122. Weighted ranking — exact match, prefix, BM25, recency, current scope
123. Permission pruning (T2 — no timing channel by construction) + post-filtering (T3) + total count never exposed
124. Self-throttling index crawler — avoids disk contention with other services
125. Index-deletion resilience — T2 fallback, then background rebuild
126. **Index estimator** — before turning indexing on, computes space/search-latency/build-time for the 2×2 combinations (off/name/content/both) from sample compression ratio, per-extension extraction rate, HLL distinct-trigram estimate, and duty cycle; coefficients self-correct against later measurements. Reachable via `GET/POST /api/admin/index/estimate` and `StorageIndexSection`

## 11. Coexistence with external services (8)

127. inotify hybrid + watch budget + `ENOSPC` lazy downgrade. `Watcher::subscribe` is called from `app.rs` when a client subscribes, and `touch` from `bridge.rs`'s read paths, so the hot set reflects real traffic
128. Every read path does lazy revalidation — correctness holds even if the watcher is dead, which today it always is
129. Periodic rescan — forced on NFS/FUSE. A dedicated thread re-marks every hot-set directory dirty every 60s (matching NFS's own default attribute-cache staleness, `acdirmax=60`) for shares where `FsType::watch_unreliable` (#5's detector) is true, since inotify can't see a change another host makes on those mounts. Paced like the index crawler (#124) so a sweep never bursts I/O onto a co-accessed mount
130. O(1) full invalidation on queue overflow (generation counter). The debounce loop calls `sc-meta`'s `bump_share_gen` (exposed as `Core::invalidate_share`) instead of flushing per-directory, whenever the OS reports a lost event batch (`IN_Q_OVERFLOW`/`notify::Flag::Rescan`) or the pending-dirty set exceeds `WatchConfig::full_threshold`
131. Directory-aggregate ETag propagation — cost paid only on DAV/NC paths
132. No sidecar files — all metadata lives in the app DB
133. External-share-folder badge + confirmation step before an invasive action
134. `chown -R` refused + SELinux `:z` guidance

## 12. Frontend (14)

135. MD3 tokens — light / dark, from m3-svelte's own palette generation
     (`DESIGN-FRONTEND.md` §2.1). The build-time generator this item used to
     describe (`web/tools/gen-tokens.mjs` → `tokens.generated.css`) is gone
     with the hand-rolled component kit it fed. **High contrast is a ✗
     non-goal**, waived by the operator: the two themes already meet the
     contrast targets and a third palette is a third thing to keep correct
136. 4px grid — custom stylelint rule enforced in the build (§2.4)
137. Three-tier density (comfortable / compact / spacious)
138. Responsive shell — `NavigationRail` ↔ `NavigationBar` + `NavigationDrawer` at 905px (§3)
139. Virtualized list — 0 frame drops at 100k rows (§5)
140. List / grid views
141. Multi-select — shift range, ctrl toggle, selection kept by name (§4)
142. Lazy-loaded file tree
143. Upload tray — survives route changes (Worker-based, §6)
144. Code editor — CodeMirror 6, dynamic import
145. Accessibility — roving tabindex, grid role, 48px targets, one live region (§9 — narrower than earlier drafts claimed; see that section)
146. i18n — Korean and English, switchable at `/settings#appearance`, plus
     `Intl` date/number formatting via `web/src/lib/i18n` (§10) and 1024-based
     units labelled KB/MB/GB (`web/src/lib/format/bytes.ts`). The IEC spelling
     (KiB/MiB) this item used to claim was never shipped and is not wanted:
     `44acd3b` settled on the familiar labels, and there is no SI/IEC mode to
     choose between. This item read "**the locale switch is not shipped**" for
     a long time, and that was honest at the time: the catalogues held 56 keys
     against roughly a thousand Korean strings written straight into the
     components, so the toggle changed almost nothing on screen and was pulled
     rather than left to misrepresent the app. Both catalogues now carry every
     key (604), a call site names an abstract key rather than its own Korean
     text, and `web/tools/i18n-check.mjs` fails the build if a key is missing
     from either language, is unused, has disagreeing `{placeholder}` sets, or
     is not a key at all — so the switch can only go back to lying if CI is
     ignored. The chosen language is per-browser (`localStorage`), and the
     root layout keeps `<html lang>` in step with it
147. Performance targets recorded in code comments (§8). The two byte budgets (Initial JS < 150 KB gzip, share-link page JS < 60 KB gzip) are CI-enforced: `web/tools/check-bundle-size.mjs` reads Vite's own build manifests (no reimplemented bundling) and fails the build on either regression — see `.github/workflows/verify.yml`'s "Check frontend bundle-size budgets" step, run on both platform jobs right after the frontend build. The third §8 target, 0 frame drops at 100k rows, is **not** gated: it needs a real browser trace to measure at all, and a GitHub-hosted runner's CPU/IO variance would make a frame-timing threshold flaky enough that it would get ignored rather than trusted — so it stays a manual/DevTools-verified target, not a CI gate. Lighthouse CI was considered and rejected for the same flakiness reason
148. Separate lightweight bundle for the public share-link page — no admin/auth code shipped to an unauthenticated visitor

## 13. Operations (13)

149. Single static binary with the frontend embedded
150. 2 container images — `sc:core` (distroless) / `sc:smb` (sidecar, `Dockerfile.smb` + `deploy/smb/`). **`sc:media` (ffmpeg) and `sc:ocr` struck from this item** — `sc:ocr` was always a contradiction (item 120 is a non-goal; an image built around a feature that doesn't exist would be nothing but a placeholder), and `sc:media` has no reason to exist without item 99's video-thumbnail decode, which is also a non-goal — see item 99
151. Startup self-diagnostics — kernel features, filesystems, watch budget, SMB binding, master-key location
152. Health endpoint + `degraded` state
153. Graceful shutdown — drains uploads, flushes dirty state, WAL checkpoint
154. Trusted proxy — `CF-Connecting-IP` CIDR validation
155. Cloudflare detection + recommended-value auto-adjustment (not enforced)
156. Master key management + rotation (`key_ver`)
157. Admin API — users / groups / grants / shares / SMB / diagnostics
158. Audit log browsing
159. DB is rebuildable — deleting the cache does not break the app
160. Runs unprivileged + `cap_drop: ALL` + read-only rootfs support
161. Image signing (cosign) + SBOM + `cargo-audit`/`cargo-deny`

## 14. UI surfaces shipped after the numbered list (8)

Added after the numbered list above was written; each verified against the
route/component that implements it, not carried forward from a plan.

162. **Trash UI** (`/trash`) — list, restore, permanently purge. A nav
     destination of its own in `NavigationRail`/`NavigationBar`, alongside the
     file browser's overflow-menu shortcut: it is a place, not an action on
     the current folder, and a restore path nobody can find is the same as no
     restore path. Backend existed before this UI did (item 18); this is the
     first thing in the app that ever called it
163. **Download UI** — a single selected file mints a signed `/c/{token}`
     link (item 93); a multi-selection or a directory streams a ZIP (item
     24). The browser picks which automatically based on the selection
164. **Share-link management UI** (`ShareManageDialog`) — create, list, and
     revoke public links (password, expiry, download-count limit, label) for
     items 104–109
165. **Per-user grant admin editor** (`GrantManagementSection`) — opens
     automatically immediately after an account is created, since a
     grant-less account otherwise just sees an empty file browser with no
     indication anything is wrong
166. **App-password scope UI** (`AppPasswordsSection`) — create/list/revoke,
     scope chosen at creation time (item 56)
167. **TOTP UI** (`TotpSection`) — enroll (QR + manual secret), recovery
     codes with reissue, disable — password reconfirmation on both enable
     and disable (item 57/58)
168. **Active-session management UI** (`SessionsSection`) — list with IP/UA,
     individual revoke (item 54)
169. **SMB credential opt-out UI** (`SmbSection`) — two independent toggles:
     publish over SMB (`smb_enabled`) and opt out of NT-hash derivation
     entirely (`smb_opt_out`, which also erases any hash already stored)

## 15. Admin settings screens (3)

Appended, not renumbered — the header rule above applies. Both live on
`/admin` (`is_admin`-gated, one tab each), because a setting an operator
cannot find is the same as a setting that does not exist.

170. **Server settings UI** (`ServerSettingsSection`, `/admin#server`) —
     parity with `config.toml`: every operator-settable field this deployment
     has, editable from one screen. `GET /api/admin/settings` returns one
     flat, dotted-key snapshot mirroring `config.toml`'s own `[section] key`
     shape (`sc-http/src/settings_api.rs::SettingsSnapshot`), persisted by
     `sc-server/src/settings_store.rs` and applied through
     `settings_bridge.rs`. Most groups apply live; network, DB, symlink
     policy, homes and SMB's own on/off switch need a restart, which the
     screen states per field (`readonly_reason_key`) and services itself through
     the "Restart server" section rather than sending the operator to a shell. A
     field the screen has no dedicated control for renders generically at the
     bottom instead of being silently dropped
171. **Upload/chunk settings UI** (`UploadSettingsSection`, `/admin#storage`)
     — two deliberately separate knobs: the server-global chunk floor and
     default (`PATCH /api/admin/upload-settings`, persisted in `upload.db`,
     changes what every account's `GET /api/auth/session` reports and what
     each new upload session uses, server-wide, with no restart), and this
     one browser's own 413 shrink-adaptation seed (`localStorage`
     `sc.chunk_size`, the same key `worker.ts` reads), which can never go
     below the server floor. Every byte figure in both is entered and
     displayed in MB (`lib/format/bytes.ts`), the unit used throughout the UI
172. **Move/copy-to-folder UI** (`DestinationPickerDialog`) — the client half
     of items 15 and 16, which the server has had all along and no screen
     could reach: `/api/fs/move` was routed, `move` was already a permission,
     a `JobKindWire` and a job-tray label, and the only way to relocate a file
     was SMB or WebDAV. One dialog serves both operations because they differ
     only in whether the source survives. It refuses an illegal destination
     before the user commits rather than letting them read the answer back as
     a failed job (`path-utils.ts::destinationProblem` — a folder into itself
     or its own descendant is fatal to both; the source's own folder blocks
     only the move, since a copy there is the ordinary duplicate case), and
     asks item 15's `dry_run` on every selection so a cross-device move says
     how many bytes it will rewrite *before* it starts. The browse page's
     "Duplicate" now runs through the same code path, so conflict retry, quota
     handling and job tracking cannot drift between the three actions
173. **Vendor-neutral chunked upload over WebDAV** (`/dav-uploads/{tid}`,
     `DESIGN-WEBDAV.md` §11) — RFC 4918 has no partial-write verb, so until
     now the only resumable WebDAV path in this server was item 79's
     compat chunking v2, which `--no-default-features` compiles out
     entirely: stripping the compatibility layer stripped resumable WebDAV
     upload with it, and `scripts/verify.sh` gates that build. Same session
     folder shape (`MKCOL` → `PUT {n}` → `MOVE .file`, `PROPFIND` to resume,
     `DELETE` to abort) with no `OC-*` header and no `{user}` path segment —
     the authenticated principal *is* the scope, so there is no "bob names
     alice's path" case to check. Reuses item 28's engine
     (`SpoolMode::NameOrdered`) unchanged, so assembly, the atomic publish and
     GC are literally the same code both surfaces already ran. The
     client-chosen `{tid}` resolves through a new `(user, tid)`-keyed alias
     table and answers `404` across accounts, per `DESIGN-WEBDAV.md` §8's
     no-existence-oracle rule. A top-level prefix rather than `/dav/uploads`
     because axum matches literal segments first and that would have
     permanently shadowed a share named `uploads`. Known limit: a `shares`-
     restricted app password is refused here (§11.5)

## 16. Single sign-on (1)

Appended, not renumbered. `DESIGN-AUTH.md` §13 is the design; this is what a
user or an operator can actually reach.

174. **OpenID Connect login** (`sc-oidc`, `oidc` cargo feature, on by
     default). Authorization Code + PKCE against one configured provider,
     performed server-side as a confidential client, with the ID token's
     signature checked against the provider's JWKS. **Link-only: a session is
     issued only for an identity already attached to a local account**, and no
     account is ever created by a login (`DESIGN-AUTH.md` §13.1 for why JIT
     provisioning is the wrong shape for item 41's grant model). A user
     attaches or detaches their own identity from `/settings#security`, both
     directions costing a password re-confirmation the same way item 58's TOTP
     toggles do; an admin reads, attaches by `sub`, and detaches any account's
     from the user list. The login screen draws its button only when
     `GET /api/auth/oidc/config` says a provider is configured, so a
     deployment without one looks exactly as it did before. Linking closes the
     account password's SMB access by deleting the NT hash, and closes WebDAV
     Basic with the account password (an app
     password, item 56, still works); `oidc.local_password_login = "deny"`
     additionally closes the web password login for linked accounts, at the
     cost of having no way back in if the provider fails, which is why it is
     `allow` by default and `config.toml`-only. Detaching revokes every
     session the provider issued. The NT-hash deletion reaches the published
     `smbpasswd` too: a running server with SMB enabled rewrites the file
     within a second of the link, with no `sc-server smb-sync` and no
     restart, and `DEPLOYMENT.md` §13.7 lists the three cases where that does
     not happen (SMB switched on since the last restart, a render that
     failed, a process killed hard mid-publish). Configuration lives in
     `[oidc]` and on the
     server settings screen (item 170), except for `client_secret_file` and
     `local_password_login`, which are shown read-only with the reason.
     **Not** included, and listed in the non-goals below: multiple providers,
     JIT provisioning, group/role claim mapping, RP-initiated or back-channel
     logout, refresh-token storage, and OIDC for WebDAV or compat clients

## Explicit non-goals

App store · server-side encryption · file versioning · comments · tags ·
Talk · Calendar/Contacts (CalDAV/CardDAV) · Federation · Collabora/OnlyOffice
integration · notify_push · activity stream · external storage mounts ·
workflows · a homegrown SMB implementation · multi-node clustering ·
**content indexing (item 119)** · **OCR (item 120)** · **video thumbnails /
ffmpeg jail (item 99)** · **dentry warming (item 118)** · **a high-contrast
theme (item 135)**.

The first two were previously described only inside the search section as if
merely unscheduled; they are decisions, not a backlog — see items 119/120
above for what was evaluated and rejected. Item 99 is the same kind of call,
reached independently and for the same reason: `DESIGN-PREVIEW.md` §4.4 had
already reasoned it through before this note was added — running ffmpeg
needs a second, `execve`-based jail structurally unlike the fork-only one
the rest of preview generation uses, and shipping one unproven against a
live kernel is worse than shipping none. Item 118 was rejected for a
different reason than the other three: not unproven or structurally risky,
but measured on the real Rocky Linux deployment target and found to have
nothing left to buy — see item 118 above for the numbers.
