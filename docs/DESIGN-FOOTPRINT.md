# Resource budget and immediacy review

**Premise**: the service runs on a 32 GB system SSD, and the data path is a 12 TB RAID, usually rotational. This document checks that premise against the rest of the design and fixes the two places that didn't hold up.

---

## 1. Budget

| Item | 32 GB SSD allocation |
|---|---|
| OS + container runtime | ~8 GB |
| Image + binary | ~1 GB |
| Headroom (WAL, temp, logs, upgrades) | ~4 GB |
| **What we get to spend** | **~18 GB** |
| ├ SQLite DB target | ≤ 4 GB (hard guard, off by default — §4) |
| └ Thumbnail cache | ≤ 2 GB default, configurable |

RAM isn't specified by this floor, but a 32 GB SSD box is typically 2–8 GB of RAM, shared with the page cache and whatever else (Jellyfin, most likely) is running alongside this.

---

## 2. Finding 1 — `fileid.path` broke the budget

### The problem

The original schema stored the full path string per file:

```sql
CREATE TABLE fileid ( …, path TEXT NOT NULL );   -- ← the problem
```

Average path depth in a deep tree runs 80–150 bytes, and that gets indexed too.

| Files | With `path` | Without (`parent`+`name`) |
|---|---|---|
| 1 M | ~160 MB | ~105 MB |
| 10 M | ~1.6 GB | ~1.05 GB |
| 60 M | **~10 GB** | ~6.3 GB |

Size wasn't the only problem: **renaming a directory meant updating `path` on every descendant row.** A rename under a 100k-descendant directory became a 100k-row `UPDATE` — an operation that should be O(1) turning into O(subtree).

### Fix — `(parent, name)` normalization, one table

`sc-meta`'s old `fileid` and `sc-search`'s old `idx_node` both stored `(share, parent, name, is_dir)` — duplicated. Merged into one:

```sql
CREATE TABLE node (
  id       INTEGER PRIMARY KEY,   -- rowid = the stable fileid, no extra storage
  share    INTEGER NOT NULL,
  parent   INTEGER NOT NULL,      -- parent node.id; 0 for a share root
  name     TEXT    NOT NULL,      -- one path component, not the full path
  dev      INTEGER NOT NULL,
  ino      INTEGER NOT NULL,
  btime_ns INTEGER,               -- distinguishes a reused inode number
  flags    INTEGER NOT NULL,      -- is_dir | pinned bits
  size     INTEGER,               -- cached, for sort/display (§5's HDD mitigation)
  mtime_ns INTEGER
);
CREATE UNIQUE INDEX node_ident ON node(share, dev, ino, btime_ns);
```

This is the real schema (`crates/sc-meta/src/lib.rs`) — not illustrative.

**Only one index, deliberately:**

| Access pattern | What it needs |
|---|---|
| path → fileid | We already hold the `statx` result (`dev`/`ino`) → `node_ident` |
| fileid → path | `id` is a free rowid lookup, then walk `parent` up — **O(depth), no index needed** |
| name search | **Never touches `node`.** T2 walk, or the self-contained `names.idx` (`proposals/stowcloud-5-search.md`) |

There is deliberately no `(share, parent, name)` index — path resolution is always the filesystem's job, never the DB's, so there's no forward lookup to serve. A directory rename is now a **single-row `name` update.**

### Estimate after the fix

| Component | Per file | 10 M | 60 M |
|---|---|---|---|
| `node` row (~15% b-tree overhead) | ~74 B | 740 MB | 4.4 GB |
| `node_ident` index | ~31 B | 310 MB | 1.9 GB |
| `diretag` (directories only, ~3% of files) | — | 21 MB | 126 MB |
| **Main DB subtotal** | **~105 B** | **~1.1 GB** | **~6.4 GB** |

These are engineering estimates from the schema above, not a measured production corpus — the dev instance's own DB currently sits in the tens of kilobytes, which is consistent with an empty/near-empty tree, not evidence for or against the per-file estimate at scale. Treat the table as a projection.

The name index (T3) is a **separate flat file outside the main DB** and needs no `node` row at all (`proposals/stowcloud-5-search.md` — the plocate model):

| Name index | Per file | 10 M | 60 M |
|---|---|---|---|
| plocate measured (Latin corpus, 27 M files, 466 MB) | ~17 B | — | — |
| Our conservative estimate (CJK included) | **~20–30 B** | **~200–300 MB** | ~1.2–1.8 GB |
| (discarded FTS5-trigram draft) | ~90 B + `node` 105 B | 2.0 GB | 11.8 GB |

About a 7× reduction versus the discarded draft, and the name index can live on the data volume rather than the system SSD.

**Both indexes default off** (`proposals/stowcloud-5-search.md`, top of document) is what keeps this whole budget theoretical for most deployments: the parallel walk covers 4 M files in under a second warm (`proposals/stowcloud-5-search.md`), so most installs never allocate any of this. When an admin does turn the name index on, it's a deliberate act — a toggle they flip and a build they start (`proposals/stowcloud-5-search.md`), not something that happens by crossing a file count. The toggle moved out of the config file so it no longer needs a restart, but nothing about *who decides* changed: no measurement flips it, and a share that was never opted in has no `.scindex/` at all, because nothing on the query or write path will create one.

So the realistic ceiling is one of these tiers:

| Deployment shape | At 10 M files |
|---|---|
| Web UI only (lazy allocation → 0 `node` rows) | **~0** |
| Web UI + name index | ~200–300 MB (all in the index file; main DB still ~0) |
| DAV/compat sync in use | ~1.1 GB (main DB) |
| DAV/NC + name index | ~1.3–1.4 GB |
| + content index | Not applicable — content indexing is not implemented (`proposals/stowcloud-5-search.md`) |

### Lazy allocation — the other half of the fix

**When** a row gets created matters as much as its size. What actually needs a durable fileid?

| Consumer | Needs a fileid? |
|---|---|
| Native web UI | **No** — works off paths |
| Jellyfin and other external services | No — they never go through us |
| WebDAV / compat sync | Yes — rename tracking requires it |
| Dead properties, locks, favorites, share links | Yes |

So **a row is created only when a fileid is actually requested.** A 12 TB media library accessed only through the web UI creates **zero** `node` rows; a DAV/NC client attaching to that tree starts creating rows only for the paths it touches.

This split lives at two different call sites, deliberately:

- **`Core::stat_entry`** (`crates/sc-core/src/ops.rs`) allocates a fileid on demand when the entry doesn't already have one. It is the caller for every one of the four fileid-needing consumers above — a share link, for instance, is minted against one specific, already-named file, exactly the shape `stat_entry` handles: one call, one path, at most one row.
- **`Core::list`** deliberately does **not**. A directory listing can address every entry in a multi-million-file tree; materializing a row per entry just because someone browsed past it is exactly the unbounded write volume this section exists to prevent — "a row is made only when a fileid is actually requested," not when it's merely displayed.

Before this split existed, `GET /api/fs/stat` on an untouched file omitted `id` entirely, so `POST /api/fs/link` (the endpoint the web UI needs to mint a single-file download URL) had nothing to send unless the file had already been share-linked once — the UI's workaround was to disable the download button. `stat_entry`'s on-demand allocation is what closes that gap, without opening `list`'s.

Allocation is best-effort: a failure to mint an id must not turn an otherwise-successful stat into an error. A share root has no real `node` row to allocate at all — it draws its id from a reserved range instead (`ARCHITECTURE.md` §4.1) — so `stat_entry` reports `None` there rather than a fabricated per-call value.

`flags`'s `pinned` bit marks "has a reason it must not be deleted" (dead property, lock, share link, favorite). GC only reaps rows whose `(dev, ino)` no longer exists — never reissues the fileid of a file that's still alive, because a client would read that as delete+redownload.

---

## 3. Finding 2 — "reflects immediately" collides with inotify's watch limit

### The problem

Take a 12 TB RAID with 300k directories. `fs.inotify.max_user_watches` defaults to 8192–65536 depending on distro, and **cannot be raised from inside a container** (it's a host-global, per-UID sysctl).

An earlier design fell back a subtree to "lazy mode" whenever watch registration failed. With only 8k of 300k directories watched, that means 97% of the tree runs lazy — which fails the "changes reflect immediately" requirement outright.

### First, what "reflects immediately" actually means

Three different guarantees were being conflated:

| Guarantee | How it's satisfied | Needs a watch? |
|---|---|---|
| **A user who looks sees the current state** | Every read path re-`stat`s (lazy revalidation) | **No** |
| **An open screen updates itself** | WebSocket `inval` push | Yes — **only for the directory being looked at** |
| **A sync client discovers a change** | The directory's aggregate ETag has to change | Yes — **across the whole tree** |

The first guarantee holds unconditionally, watcher or not. The second only matters for directories someone could be looking at, so narrowing what's watched is fine. Only the third is a real problem.

### Design: hot-set watching (default, no privilege needed)

Don't watch the whole tree — watch only what could plausibly be observed. The hot set (`crates/sc-watch/src/hotset.rs`) holds three kinds of directory: whatever currently has a live WebSocket subscriber (plus its ancestor chain), a small LRU of recently accessed directories (default 2048 entries), and each share's root plus two levels below it, pinned permanently. `watch.hot_set_max` (default 4096) bounds the total.

- Opening a directory registers the watch *before* reading it — reversed, a change in between would be missed.
- A directory evicted from the hot set gets its watch removed; `inotify_add_watch`/`rm_watch` cost microseconds, so churn is cheap.
- Kernel memory: 4096 watches × ~1 KB ≈ **4 MB**, comfortably inside default sysctls.

### Current implementation status

The backend selection (`sc_watch::WatchBackend::{HotSet, InotifyFull, Fanotify}`), the hot-set data structure, and the `Watcher::start`/`subscribe`/`touch`/`unsubscribe` API described above all exist and are exercised by `sc-watch`'s own tests. `app.rs::start_watcher` constructs and starts a `Watcher` at boot.

**But `subscribe`/`touch` — the calls that would put a directory into the hot set in the first place — are never invoked from any production code path.** They're called only from `sc-watch`'s own test suite. No route, no WebSocket-subscribe handler, no write path calls them. The practical result: the hot set is permanently empty, no directory is ever actually watched regardless of which `watch.backend` is configured, and the debounce loop that would emit `InvalEvent`s never fires. `GET /api/events`, `WsHub`, and the frontend's reconnect-with-backoff hub are all built and working on the *consumer* side — there is simply no producer feeding them.

Concretely: the first guarantee above (a user who looks gets the current state) still holds, always, because it never depended on watching. The second and third do not currently hold for anyone — an open screen does not update itself, and a sync client does not discover an external change, until it re-polls or reopens. This is a real gap, not a configuration choice; wiring `subscribe`/`touch` into the WebSocket-subscribe path (and `note_index_change`-style call sites) is a small, well-scoped fix, not a design problem.

### Design: backend choice for sync clients (once wired)

Aggregate-ETag freshness needs whole-tree watching — there's no shortcut; something has to see every change in the subtree, either by watching it directly or by the kernel doing so on our behalf.

**So the design's answer stands even though it isn't wired yet: for a large tree with a sync client attached, `fanotify` is the recommended backend.**

```toml
[watch]
backend = "auto"   # auto | hotset | inotify_full | fanotify
```

`auto`'s decision:

| Condition | Choice | Kernel memory | Sync immediacy |
|---|---|---|---|
| DAV/NC disabled | `hotset` | ~4 MB | n/a |
| ≤ `watch.full_threshold` dirs (default 50k) and watch headroom available | `inotify_full` | ~50 MB + sysctl raise needed | full, immediate |
| `CAP_SYS_ADMIN` available | **`fanotify`** (`FAN_MARK_FILESYSTEM`) | **~0** (one mark per mount) | full, immediate |
| Large tree, no privilege | `hotset` + periodic rescan | ~4 MB | **delayed by the rescan interval** |

That last row is the honest limit, surfaced rather than hidden:

> Watching 300,000 directories needs either `--cap-add SYS_ADMIN` (fanotify, ~0 kernel memory) or `sysctl fs.inotify.max_user_watches=524288` on the host (~500 MB kernel memory). Right now only open folders update live; the sync client may lag by up to N minutes.

An earlier version of this design excluded `fanotify` on the grounds of needing `CAP_SYS_ADMIN`. That judgment predates "reflects immediately" being a stated requirement; under this constraint, one capability flag is a better trade than 500 MB of kernel memory for 300k directories.

### The real cost of a rescan

Under `hotset` + periodic rescan, a rescan is one `statx` per file.

| Storage | 10 M-file rescan |
|---|---|
| dentry cache warm | ~10 s |
| NVMe cold | ~2 min |
| **12 TB HDD RAID cold** | **10+ min** (random seeks, ~10 ms each) |

So a full rescan is not a standing schedule on HDD RAID. Instead:

- **Structure-first rescan**: a directory's mtime changes on add/remove/rename. Stat-ing only directories (300k calls, not millions) detects structural change far more cheaply. In-place content edits are missed by this, but those are usually made by us or inside the hot set already.
- Full scan stays **admin-triggered, plus an optional low-load window** — never the default schedule.

---

## 4. DB size guard — off by default

```toml
[db]
size_guard     = false      # ★ off by default; an operator opts in for their hardware
max_bytes      = "4 GiB"    # applies only when size_guard = true
min_free_bytes = "1 GiB"    # always-on safety net (§4.4), independent of size_guard
```

`on_exceed`/`"degrade" | "warn"` does not exist as a config key — `size_guard` is a plain boolean, and what happens when it trips is described honestly in §4.5 below (less than an earlier draft implied).

### 4.1 Why off is the default

- On a 256 GB+ system disk, a 4 GB guard cuts capability for no reason. 32 GB is the **floor** this design supports, not the only shape.
- Content indexing, if it's ever built, is required to live in its own file outside the main DB (`proposals/stowcloud-5-search.md`) — so the main DB stays predictable at ~105 B/file even then. Today content indexing doesn't exist at all, so the main DB's only variable growth is `node` rows, and those only appear once DAV/NC is in use (§2).
- The name index is already off by default and, even on, never touches the main DB (`proposals/stowcloud-5-search.md`).

### 4.2 Off does not mean unobserved

The guard only controls **automatic intervention**. Size, growth, and breakdown are always measurable through the admin surface:

```
GET /api/admin/storage
{ "db_bytes": 412839424,
  "shares": [ { "label": "photos", "free_bytes": 19541229568, "total_bytes": 31580323840 }, … ] }
```

This is the real response shape (`sc_http::core_api::StorageReport`/`ShareStorage`) — not the richer per-table breakdown (`node`/`diretag`/`sessions`/`audit`, 7-day growth rate) an earlier draft specified. Only the whole-DB byte count and per-share free/total are exposed today; a table-level breakdown would need to be added if that granularity is wanted.

### 4.3 Startup spec-based recommendation

At boot, the volume holding the data directory is sized, and a recommendation is logged (never applied automatically):

```
[sc] data directory: /var/lib/sc  (ext4, total 29.4 GB, free 18.2 GB)
[sc]   DB size guard: disabled (current DB 394 MB)
[sc]   -> system volume is 64 GB or smaller. Recommend db.size_guard=true, db.max_bytes=2GiB
```

| Data directory volume | Recommendation |
|---|---|
| < 64 GB | `size_guard = true`, `max_bytes = 2 GiB` |
| 64–256 GB | `size_guard = true`, `max_bytes = 8 GiB` |
| > 256 GB | Guard unnecessary (stays `false`) |

This is `sc-server/src/diagnostics.rs::guard_recommendation`, pinned by a unit test against exactly these thresholds.

### 4.4 The always-on floor

**Independent of the guard**, if the volume's free space drops below a threshold, writes to that store stop and the server reports `degraded`. This isn't a policy ceiling — it's the baseline defense against SQLite corruption and outright service death, and it cannot be turned off.

Only one such key exists today: `db.min_free_bytes` (default 1 GiB), guarding the metadata DB's own volume. An earlier draft specified a second, `index.min_free`, for a separately configured index storage volume — that doesn't apply currently: the name index lives inside the share's own host path (`proposals/stowcloud-5-search.md`), on whatever volume the share itself is on, not a separately configured location, so there's nothing distinct for a second key to guard yet.

Guard (policy) and floor (unconditional) are different things — turning the guard off does not mean "keep writing as the disk fills."

**"Writes to that store stop" means growth stops, and nothing else.** `sc-server/src/diagnostics.rs::spawn_free_space_sampler` re-reads free space every 30 s (a single `statvfs`; the startup snapshot alone would never notice a volume filling after boot) and calls `MetaStore::set_writes_blocked`. What that gate refuses is exactly the two statements that add rows:

| Operation | Gated? | Why |
|---|---|---|
| `fileid` allocating a **new** id | **yes** | `INSERT INTO node` — the one growth path in the whole crate |
| `set_prop` | **yes** | `INSERT INTO dav_prop` |
| `fileid` refreshing an id that already exists | no | single-row `UPDATE`; a file that already has an id keeps working under DAV |
| `rename_node` | no | single-row `UPDATE`, and refusing it would leave the id → path mapping pointing at the old path |
| `put_dir_etag`, `mark_dirty_chain` | no | refusing them leaves a directory ETag that is stale *and* still flagged `valid`, so clients get told nothing changed when it did. A wrong answer is not an acceptable way to save a page |
| `del_prop`, `gc_dead_nodes`, `incremental_vacuum` | no | these reclaim space; blocking them would block recovery |
| every read | no | — |

So the service keeps browsing, downloading and uploading while the floor holds — the web UI's listing path never allocates anyway (`ARCHITECTURE.md` §4.1 lazy allocation; `ops.rs::build_entry` uses the read-only `lookup_fileid`). What degrades is DAV/compat operations that need a stable id for a file that has never had one, and `PROPPATCH`. Alongside the gate, `degraded_reasons()` gains `"db_free_space_low"` and a startup/transition log line names the figure and the floor.

### 4.5 What actually happens when the guard trips

An earlier draft specified a five-step automatic "degrade ladder" — reap dead `node` rows, halve audit-log retention, run an incremental vacuum, tighten lazy allocation, then block writes with a banner. **None of that runs automatically today.** Crossing `max_bytes` while `size_guard = true` does exactly one thing: it flips `degraded_reasons()` to include `"db_size_guard_tripped"`, which `GET /api/health` can report (as a bare degraded/not-degraded signal, never the reason itself — `proposals/stowcloud-9-api.md` forbids leaking configuration detail to an unauthenticated caller).

Reclaiming space is a **manual operation**: `sc-server gc` walks every share, reaps `node` rows whose `(dev, ino)` no longer exists, and runs `PRAGMA incremental_vacuum`. An operator (or a cron job calling the CLI) has to run it; nothing triggers it from the guard tripping.

Both indexes stay out of this discussion entirely — each is outside the main DB with its own cap:

| Index | Storage | Own cap | On exceeding it |
|---|---|---|---|
| Name (T3) | `<share>/.scindex/names/` flat files | none configurable yet — see `proposals/stowcloud-5-search.md` | n/a today |
| Content (T4) | not implemented | — | — |

Reads, writes, and sync are never blocked at any point in this section — the DB is a cache; it can shrink or degrade without the service going down.

### SQLite configuration

```
PRAGMA page_size          = 4096;      -- before any table exists
PRAGMA auto_vacuum        = INCREMENTAL; -- ★ must be set before the first table is created
PRAGMA journal_mode       = WAL;
PRAGMA synchronous        = NORMAL;
PRAGMA wal_autocheckpoint = 1000;      -- ~4 MB
PRAGMA journal_size_limit = 67108864;  -- 64 MB, caps unbounded WAL growth
PRAGMA cache_size         = -16000;    -- 16 MB
PRAGMA mmap_size          = 67108864;  -- 64 MB, avoids competing with the page cache (0 is fine too)
PRAGMA temp_store         = MEMORY;
PRAGMA busy_timeout       = 5000;
```

`auto_vacuum = INCREMENTAL` has to be set **before the first table is created** — miss that window and it can't be turned on later without a full `VACUUM` rewrite, and a large delete then never shrinks the file on disk, which is fatal on a 32 GB budget. Pinned by a migration test. `journal_size_limit` matters because without it, the WAL can swell to several GB during a large upload or a reindex.

---

## 5. Performance on 12 TB HDD RAID

### The DB as a performance strategy, not overhead

A random `statx` on rotational media is one seek (~10 ms). Stat-ing a 10k-entry directory cold is **100 seconds**.

That's why `node` caches `size`/`mtime_ns`: sorting and displaying a listing by size or time comes from a SQL lookup on the system SSD and **never touches the RAID.** The aggregate ETag saves the same seeks for a sync client by letting it skip subtrees that haven't changed.

So **DB-on-SSD, data-on-HDD is itself the asset** this design leans on — §4's guard just keeps its size in check.

Cached values are always reconciled against reality on the read path (lazy revalidation): fine for sorting and display, **never** used for permission decisions or actual I/O.

### Storage-class detection

A share's storage is classified by the kernel's own rotational flag, `/sys/block/*/queue/rotational` (Linux only — a non-Linux host, which is never the deployment target, defaults to the permissive flash class rather than pretending to detect something that isn't there). That flag alone only distinguishes spinning disks from flash; an additional, cheap check for an `nvme` subsystem directory further splits flash into `SataSsd` vs. `Nvme`. This detection runs once per share and is cached (`sc-server/src/storage_class.rs`) — it is the mechanism `proposals/stowcloud-5-search.md`'s per-tier search limits and §3.3's per-share thread counts both consume.

### Everything else

| Item | Treatment |
|---|---|
| Blocking-pool sizing per storage class | **Not implemented.** The four-way classification above exists and is real, but nothing today resizes Tokio's blocking thread pool by storage class — it runs with Tokio's own default regardless of what's mounted. Only the *search* concurrency budget (§8 of `proposals/stowcloud-5-search.md`) currently acts on this classification. |
| Sequential reads | `posix_fadvise(POSIX_FADV_SEQUENTIAL)` for ZIP streaming and downloads |
| Listing | `getdents64` via the OS directory iterator, `d_type` only — no `statx` for a name-sorted listing |
| Listing-session memory | Per-user cap of 4 sessions plus a **global cap** `list.total_memory` (default 64 MB) — a 100k-entry session runs ~3 MB, so ~20 sessions fit |
| Thumbnail cache location | `preview.cache_dir`, separate from the data directory by default; a 100k+ item library is better placed on the RAID (capacity vs. latency, left to the admin) |
| Thumbnail concurrency | Core count / 2; halved again on rotational media |
| Search T2 deadline | Storage-aware: 3 s fast tier, 8 s slow tier (`proposals/stowcloud-5-search.md`); a short deadline on cold HDD only reaches a few hundred entries, but results always stream regardless |
| Name-index crawl rate, content-index parallelism | `proposals/stowcloud-5-search.md`: fixed internal constants for the former (not config-exposed), not applicable for the latter (no content index exists) |
| `io_uring` | Future work. `sc-vfs` sits behind an async trait, so this is a swappable backend later — most valuable for stat-storm-heavy workloads |

---

## 6. Revised performance and resource targets

| Item | Target |
|---|---|
| Idle RSS | < 40 MB |
| Login-peak RSS | +192 MB (Argon2 48 MiB × 4 concurrent); a cache hit costs 0 |
| `mmap` + page-cache contribution | < 128 MB (avoids competing for RAM) |
| Main DB | ~105 B/file; a web-UI-only deployment stays **~0** (lazy allocation). Guard off by default, spec-based recommendation only |
| Name index (`names.idx`) | ~20–30 B/file, outside the main DB, no `node` row needed, can live on the data volume. Off by default, manual build |
| Content index (`content.db`) | **Not implemented** |
| Search, no index | 4 M files warm cache in < 1 s; SSE streams the first result in tens of ms |
| Per-file DB cost | ~105 B (no index) / ~135 B (name index on, main DB unaffected since the index is a separate file) |
| Watcher kernel memory | ~4 MB (hotset) / ~0 (fanotify) — **moot today; nothing populates the hot set in production (§3)** |
| Thumbnail cache | 2 GB default cap, relocatable |
| Binary | < 25 MB (frontend embedded) |
| Cold start | < 200 ms (lazy allocation means no initial crawl) |
| 10k-entry listing, name sort | < 50 ms (zero `statx`) |
| 10k-entry listing, size sort, DB warm | < 150 ms (RAID untouched) |
| Download | disk/NIC-bound (`sendfile`) |
| Upload overhead | < 5% over a raw disk write |

---

## 7. Verification

| Item | Method |
|---|---|
| DB size regression | 1 M / 10 M file fixtures; measured bytes recorded in CI; fails if the per-file budget is exceeded |
| Lazy allocation | Browse 1 M files through the web UI only → confirm 0 `node` rows |
| Directory rename is O(1) | Rename a directory with 100k descendants → exactly one row `UPDATE`, < 10 ms |
| `auto_vacuum` | File actually shrinks after a large delete |
| WAL cap | WAL never exceeds 64 MB during a large upload |
| Hot-set immediacy | **Currently fails** — would need `subscribe`/`touch` wired into a real request path first (§3); the test as specified (external change to an open directory reaches the WebSocket within 1 s) has nothing to exercise yet |
| Watch churn | Rapidly visiting 100 directories keeps the watch count at or under the cap |
| HDD simulation | `dm-delay` 10 ms injection; measure listing/search/sync latency |
| Degrade path (manual) | `sc-server gc` on a DB over `max_bytes` with `size_guard=true`: dead-row GC and vacuum run, in that order, service keeps serving throughout |
| Guard-off observability | `/api/admin/storage` still reports accurate `db_bytes` and per-share free/total with `size_guard=false` |
| Always-on floor | Filling a volume with `size_guard=false` still hits `min_free_bytes`, reports `degraded`, and refuses new fileid/dead-property allocation while browsing, download and upload keep working |
| Spec recommendation | Volume sizes 29 GB / 128 GB / 512 GB each produce the correct logged recommendation |
