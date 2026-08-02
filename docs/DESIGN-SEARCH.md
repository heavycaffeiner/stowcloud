# Search — detailed design

`sc-search`. **The parallel filesystem walk is the first-class path. Indexing is a measured, opt-in escalation, not a default.**

## Status today

- **T2, the parallel walk** — always on, no configuration. This is what every search runs on unless a name index already exists for the share.
- **T3, the name index** — implemented, **off by default**, switchable at runtime from the admin UI. `[index] name_enabled` is still the config default, but an admin override lives in its own `index.db` and is checked ahead of it, so turning the index on takes no config edit and no restart (§4.3). Turning it on still doesn't crawl anything by itself: an admin presses "build" (`POST /api/admin/index/build`) or an operator runs `sc-server index build`. No per-share allow-list exists; enabling the flag makes every share eligible, and `--share` on the CLI just filters which of them a given `build`/`merge`/`status` invocation touches.
- **T4, content indexing and OCR** — **out of scope by decision**, not pending work. `sc-search` has zero lines of content-extraction or OCR code; its own module doc says so explicitly. §5 records the reasoning for the record, compressed, since nothing here is being built.

The rest of this document explains why the walk is fast enough to be the default, what the name index costs when it's worth turning on, and where the two meet.

---

## 1. Research basis

### 1.1 Measurements this design is built on

| Benchmark | Result | Source |
|---|---|---|
| `fd`, 4 M files / 750 k dirs, warm cache | **854.8 ms** | sharkdp/fd README |
| `fd`, 1 M files / 150 k dirs, warm | 892.6 ms | same |
| GNU `find -iname`, same corpus | 2.866 s (3.2× `fd`) | same |
| GNU `find -iregex`, same corpus | 6.265 s (7× `fd`) | same |
| `jwalk` 8 threads, unsorted (Linux source tree) | 54.631 ms | Byron/jwalk benchmarks.md |
| `ignore` 8 threads, unsorted | 70.848 ms | same |
| `walkdir` 1 thread, unsorted | 134.28 ms | same |
| `jwalk` 8 threads, sorted **+ metadata** | 86.985 ms | same |
| `walkdir` 1 thread, sorted + metadata | 310.26 ms | same |
| `fd` vs GNU find, single dir (`/usr/bin`) | **find 8.95× faster** | sharkdp/fd#1614 |
| `readdir` default buffer (32 KiB) | ~400 entries/syscall | BurntSushi/walkdir#108 |
| inode-order stat (rotational disk) | seek latency down by a single-digit multiple | Kołaczkowski, IPCCC'09 |

Five things follow from this:

1. **A parallel walk is practical without an index.** 855 ms for 4 M files is well inside "briefly slow," which is the budget this feature has to work within.
2. **Metadata is about half the cost.** jwalk goes from 54.6 ms to 87.0 ms once it stats. Matching on name alone means never calling `statx` — the single largest optimization available.
3. **Parallelism pays off at directory boundaries, not inside one giant directory**, and it actively loses on small corpora (fd#1614's 8.95× reversal).
4. **All of the above is warm-cache.** Cold HDD RAID is a different world (§1.2).
5. **`jwalk` beats `ignore`** because `ignore` does gitignore parsing we don't want.

### 1.2 Cold cache and HDD RAID

A name search only reads directory data blocks — `getdents64` gives names and `d_type` without touching inodes — but it still costs one block read per directory.

| State | 4 M files / 750 k dirs |
|---|---|
| dentry cache warm | ~0.9 s |
| NVMe cold | ~10–30 s |
| **12 TB HDD RAID cold** | **tens of minutes** (one seek per directory; ext4 block-group locality helps some) |

Keeping the dentry cache warm costs roughly 192 bytes of kernel memory per file:

| Files | Dentry cache | On a 2–8 GB RAM box |
|---|---|---|
| 100 k | ~20 MB | always warm |
| 1 M | ~190 MB | stays warm |
| 4 M | ~770 MB | competes with Jellyfin's page cache |
| 10 M+ | ~2 GB | cannot stay warm |

So the split isn't file count, it's whether the tree can stay warm — which depends on RAM, storage, and whatever else is competing for both, none of which is known ahead of time. That's why §4/§6 make indexing a measured decision instead of a threshold.

### 1.3 Why not an off-the-shelf walker

`jwalk`, `ignore`, and `walkdir` all sit on `std::fs`'s path-based API — they take path strings and resolve them the ordinary way. Adopting one of them would revive symlink escape and TOCTOU: `ShareRoot`'s `openat2(RESOLVE_BENEATH)` invariant (`DESIGN-CORE.md` §1) gets bypassed, and because search sweeps the whole tree, a break here is the broadest possible leak. It would also break the rule that `&Path` never leaves `sc-vfs`.

So `sc-search` walks through `sc-vfs::ShareRoot` the same way every other crate does, via a small `DirSource` trait (`crates/sc-search/src/vfs.rs`) rather than a generic path-based walker. This isn't a compromise: because each step resolves one path component against an already-open parent directory handle, there's no whole-path re-resolution per entry the way a path-based walker pays for. The security boundary is also the performance win.

---

## 2. Tier model

| Tier | Scope | Cost | Default |
|---|---|---|---|
| **T1** current directory | Client-side filter over an already-loaded listing | 0 | always |
| **T2 parallel FS walk** | `sc-vfs`-backed walker | **0 DB bytes** | **always — first-class path** |
| **T3 name index** (block-compressed trigram) | Implemented, opt-in | ~20–30 B/file, outside the main DB | **off** |
| **T4 content index** | Not implemented — out of scope by decision | — | — |

An earlier draft treated T3 as "opt-in but recommended." The walk benchmarks changed that: T2 is the default and, for most deployments, the final answer. T3 stays off until an operator turns it on by hand — there is no measurement that flips it automatically (§4). Not building T3's index costs nothing extra on a share that doesn't need it: ~200–300 MB avoided for 10 M files, and the main DB is untouched either way (`DESIGN-FOOTPRINT.md` §2).

---

## 3. T2 — the parallel FS walker

### 3.1 Shape

One directory is one unit of work (`sc_search::walker::Job` internally), distributed over a work-stealing queue. Threads are decided once per walk (§3.3) from the storage class and, once running, can escalate if the queue backs up.

The real implementation is `crates/sc-search/src/walker.rs` (741 lines) over the `DirSource` trait in `vfs.rs`; the two operations a share exposes to the walker are "list a directory without touching inodes" and "stat one entry, on demand" — nothing else.

### 3.2 Per-directory work

The walker never calls `statx` for a plain name match — `DirEntry::kind` comes from `d_type`, straight out of the directory read, which is the single largest optimization from §1.1. Three things matter here:

- **`statx` is zero for name-only queries.** This alone recovers the 54.6 → 87.0 ms gap §1.1 point 2 measured.
- **ACL pruning happens at descent, not after.** Before entering a subdirectory the walker checks whether the caller may list it; a directory that fails the check is never opened, never read, never counted. This is what makes §7.3's "no timing channel" claim true by construction rather than by afterthought.
- **Reserved names are skipped** (`.sctrash`, `.scpart-*`, `.scmeta`, `.scindex`) — `sc_vfs::is_reserved_name`, shared with every other crate that walks a tree.

### 3.3 Thread count — small corpora and rotational disks need care

fd#1614 shows parallelism can be an 8.95× loss on a small corpus, and over-parallelizing a rotational disk causes seek thrashing. `Walker::decide_threads` (`walker.rs`) is a three-way decision:

| Condition | Threads |
|---|---|
| Known directory count < 64 (`SMALL_CORPUS_DIRS`) | **1** — never spins up threads at all |
| Rotational media | **2** — avoids seek thrashing |
| Everything else (flash) | `available_parallelism()`, capped at **16** |

`dir_hint` comes from a previous walk or a cached share summary; with none, the walker assumes the corpus might be large. A walk that starts on the small-corpus fast path can still escalate mid-walk if the queue backs up past a threshold; a genuinely small tree just finishes on one thread.

### 3.4 When metadata is unavoidable — inode-order batching

A size or mtime filter forces `statx`. When it does, only matched entries are stat'd, and — on rotational media — sorted by inode number first, so the disk seeks forward monotonically instead of at random. Filesystems place inodes on disk roughly in ascending order, so this is what §1.1's "single-digit multiple" seek reduction actually buys. Matched entries are a small fraction of the corpus (thousands out of millions), so this phase is cheap even when it runs.

### 3.5 Budget and streaming

- **Results stream over SSE** (§7). The user never waits for the deadline; the screen fills as matches are found. This is what makes "briefly slow" feel instant.
- The walk is **BFS**, not DFS: if the deadline runs out, "every shallow level was seen" is still a useful guarantee. DFS would spend the whole budget on one deep branch.
- A truncated walk reports the real numbers — `Truncated { seen, elapsed }` — never a silent partial answer.
- Ranking conflicts with streaming, so results render as found and are **re-sorted once the walk completes** (or hits its deadline). Re-sorting happens client-side.

Budget defaults (`WalkBudget`, `walker.rs`): `max_entries` 5,000,000, `max_results` 1,000, `max_depth` 64. Deadlines and concurrency caps are storage-aware and covered in §8.

### 3.6 No result cache

Caching search results is tempting but wrong here: the kernel's dentry cache already does that job better, so holding names in userspace too would just spend the same RAM twice. A cache also reintroduces an invalidation problem — which is indistinguishable from having an index, so if that's wanted, turning on T3 is the honest way to get it.

(An earlier draft of this document specified a background "dentry warming" feature to pre-touch the kernel cache. It was never implemented — no config key, no code — and is not being pursued.)

---

## 4. T3 — the name index

The trigger for turning this on is not a file count — the same million files costs 0.2 s on an 8 GB NVMe box and 3 minutes on a 2 GB HDD box. The honest trigger is a measurement, and today that measurement is a **manual tool an admin runs and reads** (§6's estimator), not an automatic in-product suggestion. An earlier draft described the server watching search latency (`p95`) and proactively suggesting the index in the admin UI; that telemetry (`search_stat`, `index_suggest_p95_ms`) was never built. What exists is: an admin opens the storage/search-index panel, clicks "estimate," gets numbers back (§6.9), and then decides by hand — the deciding is still a person's, only the config-file edit is gone (§4.3).

### 4.1 Index structure — block-compressed trigram (the plocate model)

An earlier draft used SQLite FTS5's `trigram` tokenizer. That's discarded, on measured grounds:

| Implementation | Corpus | Index size | Per file |
|---|---|---|---|
| **plocate** | 27 M files | **466 MB** | **~17 B** |
| mlocate | 27 M files | 1.1 GB | ~41 B |
| FTS5 trigram (our discarded draft, estimated) | — | — | ~90 B |

plocate does the same job (filename substring match) in roughly a fifth of the estimated FTS5 space, and answers a query over 27 M files in 0.008 s (mlocate: 20.1 s).

Why it's this small — three things compound:

1. **Postings point at blocks, not documents.** 32 names share one posting entry, so posting-list length drops 32×. plocate's default `--block-size` is exactly 32, and this codebase uses the same default (`sc_search::index::IndexBuilder`, `block_size: 32`).
2. **Block compression gets context.** Filenames in the same directory tend to share prefixes — `IMG_0001.jpg`, `IMG_0002.jpg`, … — and compressing them together makes most of that redundancy disappear. Blocks are built in tree order, so this locality is free. Especially strong for photo libraries.
3. **No position information is stored** — the same choice Google Code Search made: filename matching doesn't need positions, and ranking here is name-match-based rather than BM25, so no term frequency either.

The cost is false positives: a posting hit only narrows to a block, which is then unpacked and linearly scanned. plocate's own documentation states the tradeoff plainly — bigger blocks compress better and shrink the index, but produce more false positives to filter after intersection; returns diminish around 256 names/block, and too large a block hurts ordinary-query performance. `block_size = 32` is the shipped default; it isn't config-exposed today (see below), so retuning it means changing the constant and rebuilding.

The index is **self-contained** — filenames live inside the compressed blocks, so unlike the FTS5 draft (which needed `node` rows for external content, the majority of that draft's 195 B/file cost) it needs nothing from the main DB. Results are bare paths; ACL and metadata are handled downstream, identically to a T2 hit (§7.2).

Additional techniques, all implemented:

| Technique | Effect |
|---|---|
| **High-df trigram pruning** | A trigram appearing in more than `prune_df_ratio` (default 0.60) of blocks has poor selectivity and the longest posting list — dropped from the index, intersection falls back to the remaining trigrams. If every trigram in a query gets pruned away, that query falls back to T2. |
| **Case-folding + NFC normalization at index time** | No duplicate storage of case/normalization variants. |
| **Block order = tree order** | Maximizes prefix sharing versus a random order. |

CJK note: a trigram is a 3-byte window. UTF-8 Hangul is 3 bytes per syllable, so one Hangul character is exactly one trigram, and a 2-character query produces 4 overlapping trigrams — favorable for CJK substring search. The distinct-trigram count is higher than for Latin text, though, so the posting dictionary is bigger; the per-file figure above (~17 B) is a Latin measurement, and the estimator (§6) measures the real ratio per corpus rather than assuming one. Queries under 2 characters cannot be trigram-matched and fall back to T2 even with an index present; single-character queries are rejected outright, with the UI explaining why.

Filenames are treated as **byte strings**, not text: Linux allows any byte except NUL and `/`, so trigrams are 3 raw bytes and matching happens on encoded bytes, exactly as plocate does. A non-UTF-8 name gets a lossy conversion only for display; indexing and matching keep the original bytes, so such files remain findable.

Cost, revised:

| | Discarded FTS5 draft | This design |
|---|---|---|
| Per file | `node` 105 B + FTS 90 B = **195 B** | **~20–30 B** (no `node` row needed) |
| 10 M files | ~2.0 GB | **~200–300 MB** |
| Storage | main DB (system SSD) | its own file, can live on the data volume |

About a 7× reduction, and the whole thing sits outside the main-DB guard (`DESIGN-FOOTPRINT.md` §4).

### 4.2 Updates and retirement

An immutable block index cannot be upserted — there's no way to splice one name into the middle of a compressed 32-name block, or insert a block id into a delta-encoded posting list. plocate's answer is periodic full rebuild (`updatedb`); that's not acceptable here because a change should show up in search without waiting for a rebuild.

The answer is a segmented structure, the same base/delta/tombstone shape Spotlight uses for its transient/static posting split:

```
<share>/.scindex/names/
  base.idx        # immutable, full crawl, block-compressed trigram (§4.1)
  delta.NNN.idx   # append-only additions, lightly or un-compressed
  tomb.idx        # deleted/moved entries
  meta            # generation, segment list, base-vs-delta ratio
```

| Operation | Behavior |
|---|---|
| **Add** (create, move-in) | Append to the current delta segment. O(1). |
| **Delete** (delete, move-out) | Record in `tomb.idx`. `base` untouched. |
| **Rename** | Tombstone + delta append. |
| **Query** | `base ∪ Σdelta` postings, minus tombstones. Deltas are small enough to scan linearly. |
| **Merge** | When `Σ|delta| + |tomb| > merge_ratio × |base|` (default 0.15), `base` is rewritten. |

Writes are O(1); a query's extra cost is bounded because delta never grows past 15% of base before a merge folds it back in. Merge is the one heavy operation, and it only runs when triggered.

What actually keeps an index current, in ascending order of certainty (`sc-server/src/bridge.rs`, the module doc above `SearchBridge`):

| Source | What happens today |
|---|---|
| **Self-writes** | The moment this process creates, deletes, or moves a path (`mkdir`/`delete`/`rename`/`move_entries`/`copy_entries`/`copy_to`/`write_*`), the affected share's index gets an append/tombstone for exactly that path. **Two gaps**: `hapi`'s `move_entries`/`copy_entries` under a caller-chosen conflict policy (the destination name isn't knowable without re-deriving `sc-core`'s own conflict logic), and TUS upload finalize (no destination vpath available at that layer). Both fall through to the mechanisms below instead of updating nothing. |
| **Watcher-driven reconciliation** | `reconcile_watch_event` exists and is tested — it diffs a fresh single-level listing against `NameIndex::children_of` — but **it is not wired to the running watcher**. `app.rs::start_watcher` never calls it. Until that one-line gap is closed, a change made by anything other than this process (another process writing the same tree, a client mounting the share directly) is invisible to the index until a full rebuild. |
| **Full rebuild** | `POST /api/admin/index/build` (the admin UI's button) or `sc-server index build`. Either way a person asks for it — there is still no automatic crawl on first activation. The HTTP path runs as a `JobKind::IndexBuild` job so it reports progress and can be cancelled like any other job, and paces itself through `CrawlThrottle` so it doesn't starve Jellyfin/Samba on a co-accessed share; the CLI path runs the same crawl. It is refused with `501` while the toggle is off — planting `.scindex/` for a feature nobody enabled would break the off-by-default invariant. |
| **Idle merge** | `app.rs::spawn_idle_merge` checks every share's `needs_merge()` on a 10-minute timer and merges the ones over `merge_ratio`, stopping on graceful shutdown. "Idle" is deliberately narrow: **no admin build is currently running**, not CPU/disk load — this deployment has no load signal to sense, the same reason `CrawlThrottle` doesn't have one. So there is still no *load-aware gate* of the kind every earlier draft's opportunistic scheduler (§5.5) imagined; there is a contention gate against the one competing writer this binary can actually see. `sc-server index merge` skips that gate entirely — an operator running it by hand has made the "now is a fine time" call themselves. |

A corrupt or missing index is never load-bearing for correctness: every index hit is ACL-rechecked and re-stat'd before being trusted (§7.2), and a missing/unreadable index just means every query for that share falls back to T2, exactly as if it had never been built. The index is a cache; deleting it degrades performance, not correctness.

### 4.3 What's actually configurable

```toml
[index]
name_enabled    = false   # ← the only switch. No per-share list.
content_enabled = false   # T4 — unused; kept for forward compatibility, does nothing
```

`name_enabled` is the **default**, not the last word: an admin override lives in `<data_dir>/index.db` (`sc_search::IndexSettingsStore` — one row, `CHECK (id = 1)`, absence meaning "no override yet") and is checked ahead of the config value by everything that asks: `GET`/`PATCH /api/admin/index/settings`, the build gate, and `sc-server index build`'s own `ensure_name_index_enabled`. Three consequences worth stating plainly:

- **Turning it on takes effect immediately.** `IndexSettingsStore` keeps the live value in an `AtomicBool` that the `PATCH` writes through, so the next request already sees it. No restart, and no rewrite of a `config.toml` that `scripts/deploy.sh` overwrites anyway.
- **Off by default is enforced by the filesystem, not only by the flag.** A deployment with no `[index]` section gets `IndexConfig::default()` (`name_enabled: false`) and no override row, so nothing builds. Nothing *consults* one either: `bridge.rs::open_name_index` checks for `names/meta` before calling `NameIndex::open`, which would otherwise `create_dir_all` a `.scindex/` under every share anyone merely searched. A share that never opted in has zero footprint from any of this, which is `DESIGN-FOOTPRINT.md` §2's actual claim.
- **Turning it back off refuses new builds and nothing else.** An index that already exists stays on disk, keeps being consulted, and keeps being maintained — self-write appends/tombstones (`note_index_change`) and the idle merge both key off "does this share have an index", not off the toggle. That is deliberate: an index that stopped being updated but kept answering queries would return a *wrong* answer (a file created after the toggle flipped would be invisible, because a root the index answers for never falls through to T2), which is worse than the disk it occupies. Reclaiming the space means deleting `<share>/.scindex/` — safe at any time by §4.2, and the admin panel says so.

`block_size` (32), `prune_df_ratio` (0.60), and `merge_ratio` (0.15) are `IndexBuilder` defaults in `sc-search`, not TOML keys — changing them today means changing the constant. An index lives at `<share host path>/.scindex/names/`, fixed relative to the share it belongs to; there is no separate `store = "auto" | <path>` config and no splitting one logical index across multiple mounts. Building or inspecting one is through the admin UI (§6.9) or `sc-server index build|merge|status [--share NAME]`.

---

## 5. T4 — content indexing and OCR: out of scope by decision

Not implemented, and not planned. The reasoning, briefly, for the record:

A walk cannot substitute for content search — 12 TB can't be grepped per query — so this is the one tier where an index would be unavoidable if it were built. That in turn means it would need the heaviest guardrails of anything in this document: a hard allow-list of paths (never "index everything"), a hard cap on how many paths, extraction size limits, and scheduling that yields to real traffic and to Jellyfin's disk use rather than competing with it — the same shape every desktop and NAS content indexer that survived contact with a large corpus arrived at independently, and the same shape the JVM/Lucene approaches get punished for skipping: they hit documented CPU blowups in the low millions of files, and the fix on offer is "index less," i.e. turn features off. That's the outcome this design avoids by never building the unbounded version in the first place (`TECH-STACK.md`'s rejection of Elasticsearch).

Two placeholders are kept only because other documents cite them by number:

**§5.2 — where an index would live, if built.** Not the main DB. `DESIGN-FOOTPRINT.md` §4 already assumes this separation exists (a `content.db` distinct from the metadata DB, on the data volume, deletable independently) — that assumption is what let the main-DB guard stay off by default. If content indexing is ever built, preserving that separation is a hard requirement, not a nice-to-have.

**§5.5 — opportunistic scheduling, if built.** Whatever runs extraction would need to back off under real load — active transfers, high load average, elevated disk-wait, thermal limits. §4.2's idle merge is the closest thing that exists, and it is deliberately not that: it backs off from one known competitor (an admin-triggered build) on a fixed 10-minute timer, because this deployment has no load signal to sense. A real load-aware scheduler would still have to be built, and there is still nothing to schedule.

Everything else this section used to contain — engine benchmarks, feature comparisons against commercial desktop and NAS search, extraction pipelines, FTS5 tuning — described work that was never started and isn't going to be from here. It has been removed rather than carried forward as unbuilt planning.

---

## 6. The index estimator

Both indexes default off, and for most deployments that's the right answer. The estimator's job is not to talk anyone into turning the index on — it's to put a real number next to that decision, including when the number says "don't."

### 6.1 Principle — measure, don't hardcode

Compression ratio and trigram density vary by multiples between corpora — a photo library (`IMG_0001.jpg`, `IMG_0002.jpg`, …) compresses nothing like a document tree of unrelated names. So every coefficient the estimator uses is **measured from a real sample**, not assumed:

- `sample_compress_ratio` — sampled 32-name blocks are actually zstd-compressed.
- `distinct_trigrams_est` — a [`HyperLogLog`] over real trigrams (12–16 KB of state for ~2% error), not a per-file constant. This is the term that makes a CJK corpus cost more than a Latin one, and it can only be measured, not guessed.

### 6.2 Corpus stats — reusing the T2 walker

The estimator walks with the same `Walker` search itself uses (`sc_search::CorpusScanner`), so it's already fast and gets almost everything without a single `statx`:

```rust
pub struct CorpusStats {
    pub files: u64, pub dirs: u64,
    pub name_bytes_total: u64,
    pub distinct_trigrams_est: u64,   // HyperLogLog
    pub sample_compress_ratio: f32,   // measured on sampled blocks
    pub posting_bytes_per_block: f32, // measured, not modeled — see below
    pub scanned_entries: u64, pub elapsed: Duration, pub truncated: bool,
}
```

`posting_bytes_per_block` is measured directly from sampled blocks rather than derived analytically from the trigram count: an analytic model assumes every trigram is equally common, which is exactly the assumption high-df pruning exists to violate, and over-predicts the posting term roughly 7× on a real photo corpus. Sampled blocks are a uniform random sample of all blocks, so the measured per-block figure scales exactly.

### 6.3 Name index size model

```
blocks        = ceil(files / block_size)
block_bytes  ≈ (name_bytes_total + files × 3 B) × sample_compress_ratio
blockdir      = blocks × 16 B
dict_bytes    = distinct_trigrams × 12 B
posting_bytes ≈ Σ_t df(t) × varint width      (high-df trigrams excluded)
index_bytes  ≈ header + blockdir + dict + postings + block_bytes
```

This is the real formula in `sc_search::estimate::estimate_name_index`, not illustrative pseudocode.

### 6.4–6.8 — not implemented

An earlier draft specified a matching content-index size model, a duty-cycle-aware time model, a "what you get for it" effect model, a 2×2 space/time table for both indexes together, and a persisted `estimate_log` table that calibrated future estimates against past ones. None of that exists: there is no content index to estimate (§5), no scheduler whose duty cycle could be measured (§4.2), and no calibration log. What remains is exactly §6.1–§6.3 plus §6.9 below — a single-shot, name-index-only estimate with no memory between runs.

### 6.9 API

```
GET /api/admin/index/estimate
POST /api/admin/index/estimate      (same handler, either verb)
  → { "files": 2104882,
      "index_bytes": 55937840,
      "build_secs": 1842,
      "confidence": "high" }
```

Synchronous — no job id, no polling, no per-share or per-path scoping. It samples up to 2,000,000 entries (256 sampled blocks) across **every share this deployment has**, capped and bounded the same way a search itself is (`sc_search::CorpusScanner`), and returns one estimate for turning the name index on everywhere. `confidence` is `"high"` when the sample scan completed, `"medium"`/`"low"` as it gets extrapolated from less — a code rather than a sentence, so the browser picks the wording and the reader's language.

`sc_search::NameIndexEstimate` also carries a term-by-term derivation (`2,104,882 files × ~26.6 B ≈ 53 MB (measured compress_ratio 0.31, …)`), per §6.1: when the estimate is wrong, *which* term is wrong should be visible. That derivation is **not on the wire**. The handler writes it to the server log on every estimate request, where an operator checking the arithmetic can find it; on the admin screen it read as noise to everyone who had not written the estimator.

The admin UI (`StorageIndexSection.svelte`) shows the three figures next to raw `GET /api/admin/storage` numbers, behind one explicit button. The same panel carries the `name_enabled` toggle (§4.3) and a "build" button that stays disabled while the toggle is off — but there is still **no auto-suggestion** (§4): nothing measures on its own, nothing recommends, and nothing flips the toggle for you. The estimate exists so the person deciding has a number, not so the product can decide for them.

### 6.10 — not implemented

An earlier draft split index storage across multiple mounts when a scope spanned more than one filesystem (`store = "auto"`, one index file per device). Since an index today is always per-share, fixed at `<share host path>/.scindex/names/`, this doesn't apply — a share is, by construction, one filesystem.

---

## 7. Query, ranking, and permissions

```
GET /api/search?q=…&scope=/photos&kind=image
GET /api/search/stream?…      # SSE, the default path for T2
```

```rust
pub struct SearchQuery {
    pub text: String,
    pub scope: Option<String>,       // VPath, restricts the walk/query
    pub kind: Option<String>,        // extension group, never opens a file to decide
    pub mtime_after_ns: Option<i128>,
    pub size_min: Option<u64>,
    pub size_max: Option<u64>,
}
```

A query carrying `kind`/`size`/`mtime` bypasses the index for every root, not only the ones lacking one: the index stores bare paths only (§4.1 — no kind, no size, no mtime), so it cannot evaluate such a filter, and answering from a source that can't apply the filter would be a wrong answer dressed as a fast one.

### 7.1 Ranking

```
score = 3.0 × exact name match
      + 2.0 × name prefix match
      + 1.0 × normalized bm25       (always 0 on the T2 walk path — no content)
      + 0.5 × recency (linear decay over 30 days)
      + 0.3 × below the current scope
      − 1.0 × hidden
```

This is `sc_search::rank::score`, byte-for-byte. Every term except `bm25` needs only the name and an optional stat, so it works identically whether the hit came from T2 or T3. No learned ranking, no click logs — none are collected.

### 7.2 Permissions

- **T2**: enforced during the walk itself. A subtree the caller cannot read is never entered, so there is nothing to leak and nothing to filter afterward. **No post-filter is needed.**
- **T3**: the index has no idea what an ACL is, so every hit is ACL-rechecked and re-stat'd after the index returns it — the same re-stat also catches a hit that's gone stale (deleted since the index was built) and supplies `is_dir`/`size`/`mtime_ns`, which the index doesn't carry. Filtering removes rows, so this runs an overscan loop: `sc-server/src/bridge.rs`'s `consult_name_indexes` asks the index for `want × 8` (floor 64) candidates per round, to avoid under-filling a page when a caller turns out to be allowed to see everything they asked for.

This split is the reason permission filtering happens **downstream of the index lookup rather than folded into it**: an index that tried to bake ACL awareness into its own postings would need per-user (or per-grant) structure inside an otherwise self-contained, immutable format, defeating the whole point of §4.1's design. Filtering after the fact keeps the index format simple and correct by construction — a wrong or stale ACL check can only under-return, never leak.

### 7.3 Preventing an existence leak

- **No total count is reported before filtering** — and today there is no total-count field in the response at all (see the wire shape below); only the filtered hit list and a completeness marker go out.
- T2 never enters a subtree the caller can't read, so response time doesn't depend on how much is hidden from that caller — **there is no timing channel by construction.** This is a real advantage T2 has over T3, whose overscan loop (§7.2) does cost more when more results are filtered out, even though it never returns them.

### 7.4 The wire shape (SSE)

The frontend was, for a time, written against a nested shape (`{path, entry: Entry}`) that matched the *mock* API rather than the real one — search silently rendered nothing against the live server while the mock's own tests kept passing, because a mocked `searchStream` reconstructed hits from full `Entry` objects instead of the flat event the real handler sends. Fixed in `web/src/lib/api/http.ts` (`ee17d3f`); the real shape, direct from `sc-http::routes::hit_json`, is:

```
event: hit
data: {"path": "...", "name": "...", "is_dir": false,
       "size": 12345, "mtime_ns": "1700000000000000000", "score": 3.5}

event: done
data: {"state": "full"}
  — or —
data: {"state": "truncated", "reason": "deadline", "seen": 3120442, "elapsed_ms": 8000}
```

`mtime_ns` travels as a string (JS numbers lose precision above 2^53, the same rule every other nanosecond timestamp in this API follows). There is no `id`/`etag`/`perms` in a hit — search doesn't carry them, and nothing downstream of a click needs them beyond `.path`.

---

## 8. Resource bounds

| | Config key | Fast tier (NVMe/SATA SSD) | Slow tier (rotational/network) |
|---|---|---|---|
| T2 walk deadline | `search.walk_deadline_fast_ms` / `_slow_ms` | 3 s | 8 s |
| Concurrent searches (global) | `search.max_concurrent_fast` / `_slow` | 4 | 2 |
| Per-user rate | `search.rate_per_minute` | 30/min | 30/min |
| T2 max entries walked | `WalkBudget::max_entries` (constant) | 5,000,000 | 5,000,000 |

These are the only search-related values actually exposed under `[search]` in the config file today (`sc_server::config::SearchConfig`). A search spanning shares of different storage classes takes the more conservative (slower) tier — a single HDD in the set is what bounds the walk (`sc_http::search_limits::fold_tier`). Storage class is detected once per share and cached, from `/sys/block/*/queue/rotational` plus an `nvme` subsystem check (Linux only; `DESIGN-FOOTPRINT.md` §5), or from filesystem type for NFS/CIFS/FUSE shares.

An exhausted concurrency budget answers `429` with `Retry-After` immediately (`try_acquire`, never a blocking wait) rather than queuing server-side — the queuing described for this budget is the client's own retry.

Numbers that appear in earlier drafts as if they were config keys but are not — the `getdents64` buffer size, `index.name.crawl_rate`, `index.content.parallel`, dentry-warming rate — are either fixed internal constants, or (for anything under `index.content`) not applicable, since content indexing doesn't exist to have a parallelism knob.

---

## 9. Protocol exposure

**`sc-dav` does not implement WebDAV `SEARCH`/`REPORT`** (`DESIGN-WEBDAV.md` §2) — this is a deliberate non-goal for the core DAV layer, and it is accurate. An earlier draft additionally described a compat translation layer that would accept `nc:filter-files`/`d:basicsearch` REPORT bodies, translate what it could into a `SearchQuery`, and reject the rest with `422` rather than silently returning "no results." **That translation layer does not exist** — there is no REPORT handling anywhere in the compat crate. A compat or DAV client has no search today; only the web UI's `GET /api/search[/stream]` works. If this gets built, the `422`-over-silent-empty-results argument still holds: a client that can't tell "no results" from "we didn't understand the query" will draw the wrong conclusion.

---

## 10. Tests

| Area | Method |
|---|---|
| Walker throughput regression | 100 k / 1 M file fixtures, warm-cache entries/sec recorded in CI, fails on regression |
| Small-corpus reversal | 10-directory corpus takes the 1-thread path (fd#1614 reproduction guard) |
| HDD simulation | `dm-delay` 10 ms injection: thread count drops to 2, inode-order sort measurably reduces seeks |
| Symlink escape | A link farm pointing at `../../etc` never leaves the share |
| ACL pruning | `getdents64` call count is exactly 0 for a subtree the caller can't read (strace count) |
| Timing channel | Response time is unaffected by scaling the caller's inaccessible file count 10× |
| CJK substring match | `여름휴가사진.jpg` matches `휴가`/`여름`/`사진` on both T2 and T3 |
| Short queries | 2-character queries fall back to T2 even with an index present; 1-character is rejected |
| Deadline honesty | Always responds inside its deadline at 1 M files; `Truncated.seen` matches the real count |
| Index resilience | Deleting `names/` mid-flight: every query for that share falls back to T2 with no error |
| Estimate accuracy | 1 M-file fixtures (photo-shaped, doc-shaped): predicted bytes within ±20% of measured |
| Estimate cost | Running an estimate does not push ordinary request latency past §8's bounds |
| HLL accuracy | Distinct-trigram estimate within ±5% of an exact count |

---

## References

- [sharkdp/fd](https://github.com/sharkdp/fd) — 4 M files in 855 ms, built on `ignore`
- [Byron/jwalk benchmarks](https://github.com/Byron/jwalk/blob/main/benches/benchmarks.md) — jwalk / ignore / walkdir comparison
- [sharkdp/fd#1614](https://github.com/sharkdp/fd/issues/1614) — parallelism reversal on small corpora
- [BurntSushi/walkdir#108](https://github.com/BurntSushi/walkdir/issues/108) — `readdir` buffer size vs. syscall count
- [Ordering Requests to Accelerate Disk I/O](https://pkolaczk.github.io/disk-access-ordering/)
- [Why processing things in inode order is a good idea](https://utcc.utoronto.ca/~cks/space/blog/unix/InodeOrderReason)
- [Improving File Tree Traversal Performance by Scheduling I/O Operations in User Space](https://home.simula.no/~paalh/publications/files/ipccc09.pdf) — IPCCC'09

### Index compression

- [plocate](https://plocate.sesse.net/) — 27 M files, 466 MB (~17 B/file), 0.008 s query. Basis for §4.1.
- [plocate-build(8)](https://plocate.sesse.net/plocate-build.8.html) — `--block-size` default 32, block-size/false-positive tradeoff
- [Regular Expression Matching with a Trigram Index (Russ Cox)](https://swtch.com/~rsc/regexp/regexp4.html) — document-level postings, no positions, index ≈ 20% of corpus
- [google/codesearch](https://github.com/google/codesearch/) — reference implementation
