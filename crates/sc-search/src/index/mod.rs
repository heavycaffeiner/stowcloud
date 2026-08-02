//! **T3 — the block-compressed trigram name index** (§4).
//!
//! Off by default. It is an *escalation*, taken only when measurement says the
//! T2 walk is not fast enough (§4), and it is a cache: delete the directory and
//! search keeps working via T2.
//!
//! ## Segments
//!
//! ```text
//! <store>/names/
//!   base.idx        immutable, block-compressed trigram index (base.rs)
//!   delta.NNN.idx   append-only, lightly compressed, linearly scanned
//!   tomb.idx        deletions
//!   meta            generation, config, segment list — swapped by atomic rename
//! ```
//!
//! Query = `base ∪ Σdelta − tomb`.
//!
//! The split is not an optimisation, it is a necessity: **an immutable block
//! index cannot be upserted.** You cannot insert a name into the middle of a
//! compressed 32-name block, and you cannot insert a block id into a
//! delta-encoded posting list. plocate sidesteps this by rebuilding nightly;
//! we cannot, because "filesystem changes are reflected immediately" is a
//! requirement. So writes go to a delta segment in O(1) and the expensive
//! rebuild happens under a gate, at idle, only when
//! `Σdelta + tomb > merge_ratio × base`.

mod base;
mod seg;

pub use base::{
    BaseSegment, BlockEntry, Lookup, BLOCKDIR_ENTRY, DICT_ENTRY, HEADER_LEN, MIN_BLOCKS_FOR_PRUNE,
};

use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};

use crate::codec;
use crate::fold;
use crate::rank::{self, RankInput};
use crate::trigram;
use crate::varint;
use crate::vfs::ShareId;

/// Queries shorter than this cannot produce a trigram.
pub const MIN_TRIGRAM_QUERY: usize = 3;

const CODEC_RAW: u8 = 0;
const CODEC_ZSTD: u8 = 1;

/// Why the index declined to answer and the caller must run a T2 walk instead.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FallbackReason {
    /// Under three bytes — no trigram exists. §4.1: "a query under two
    /// characters can't form a trigram, so it falls back to a T2 walk even
    /// when an index exists."
    QueryTooShort,
    /// Every trigram in the query was dropped by high-df pruning, so the
    /// intersection would be over nothing at all.
    AllTrigramsPruned,
}

#[derive(Clone, Debug)]
pub struct IndexHit {
    pub share: ShareId,
    /// Full virtual path, straight out of the index — no `node` lookup.
    pub path: String,
    pub name: String,
    pub score: f32,
}

impl IndexHit {
    /// Promote a bare index hit into the same [`crate::Hit`] shape the T2
    /// walker produces, once the caller has resolved what the index does not
    /// store: `is_dir`, `size` and `mtime_ns` (§4.1 — the index is
    /// deliberately name-only). Callers get those from a stat done *after*
    /// their own ACL check, which doubles as the index's staleness
    /// revalidation (§4.2: "the index is a cache").
    pub fn into_hit(self, is_dir: bool, size: Option<u64>, mtime_ns: Option<i128>) -> crate::walker::Hit {
        crate::walker::Hit {
            share: self.share,
            path: self.path,
            name: compact_str::CompactString::from(self.name),
            is_dir,
            size,
            mtime_ns,
            score: self.score,
        }
    }
}

/// The return of [`NameIndex::query`].
///
/// **Contract note:** the spec's signature returns `Vec<IndexHit>`, but it also
/// requires that "if ALL of a query's trigrams were pruned, the caller must
/// fall back to T2 — expose that via the return type". A bare `Vec` cannot
/// distinguish "no matches" from "I could not answer", and confusing the two
/// silently turns a fallback into a wrong empty result. Hence this struct.
#[derive(Clone, Debug, Default)]
pub struct QueryResult {
    pub hits: Vec<IndexHit>,
    /// `Some` ⇒ `hits` is empty and meaningless; run T2.
    pub fallback: Option<FallbackReason>,
    /// Blocks the posting intersection produced.
    pub candidate_blocks: usize,
    /// Names linearly scanned inside those blocks.
    pub scanned_entries: usize,
    /// Candidate blocks that turned out to contain no match — the documented
    /// cost of block-level postings. Worth surfacing: it is the number that
    /// says whether `block_size` is tuned right for this corpus.
    pub false_positive_blocks: usize,
}

impl QueryResult {
    pub fn must_fall_back(&self) -> bool {
        self.fallback.is_some()
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct IndexStats {
    pub entries: u64,
    pub base_entries: u64,
    pub delta_entries: u64,
    pub tombstones: u64,
    pub blocks: u32,
    pub trigrams: u32,
    pub pruned_trigrams: u32,
    pub base_bytes: u64,
    pub delta_bytes: u64,
    pub tomb_bytes: u64,
    pub delta_segments: usize,
    pub generation: u64,
}

#[derive(Clone, Copy, Debug)]
pub struct IndexConfig {
    pub block_size: u32,
    pub prune_df_ratio: f32,
    pub merge_ratio: f32,
    /// Roll to a new delta segment past this size.
    pub delta_roll_bytes: u64,
}

impl Default for IndexConfig {
    fn default() -> Self {
        // defaults. These are constants, not TOML keys —
        // the config surface is only `[index] name_enabled`.
        Self {
            block_size: 32,
            prune_df_ratio: 0.60,
            merge_ratio: 0.15,
            delta_roll_bytes: 4 << 20,
        }
    }
}

#[derive(Serialize, Deserialize, Clone, Debug)]
struct Meta {
    version: u32,
    block_size: u32,
    prune_df_ratio: f32,
    merge_ratio: f32,
    generation: u64,
    seq: u64,
    base_bytes: u64,
    delta_bytes: u64,
    tomb_bytes: u64,
    entries: u64,
    delta_files: Vec<String>,
}

impl Meta {
    fn from_cfg(cfg: &IndexConfig) -> Self {
        Self {
            version: 1,
            block_size: cfg.block_size,
            prune_df_ratio: cfg.prune_df_ratio,
            merge_ratio: cfg.merge_ratio,
            generation: 0,
            seq: 0,
            base_bytes: 0,
            delta_bytes: 0,
            tomb_bytes: 0,
            entries: 0,
            delta_files: Vec::new(),
        }
    }
}

/// One entry living in a delta segment.
#[derive(Clone, Debug)]
struct Live {
    seq: u64,
    share: ShareId,
    path: String,
}

struct Inner {
    base: Option<BaseSegment>,
    base_bytes: u64,
    delta: Vec<Live>,
    delta_files: Vec<PathBuf>,
    delta_bytes: u64,
    /// `share -> path -> the seq at which it was deleted`. An entry survives a
    /// tombstone only if it was written *after* it, which is what makes
    /// "delete then re-create the same path" behave. Nested by share so the
    /// hot lookup can borrow a `&str` instead of allocating a key per entry
    /// scanned.
    tomb: HashMap<u32, HashMap<String, u64>>,
    tomb_bytes: u64,
    seq: u64,
    generation: u64,
}

pub struct NameIndex {
    dir: PathBuf,
    cfg: IndexConfig,
    inner: RwLock<Inner>,
}

// ---------------------------------------------------------------------------
// tree order
// ---------------------------------------------------------------------------

/// Compare two paths in **tree order**: component by component, so that
/// everything under one directory is contiguous.
///
/// A plain byte comparison gets this subtly wrong (`'.'` = 0x2E sorts before
/// `'/'` = 0x2F, so `a.txt` lands between `a` and `a/b`), which scatters
/// siblings and costs real compression. Mapping the separator below every
/// other byte fixes it.
pub fn tree_cmp(a: &str, b: &str) -> std::cmp::Ordering {
    let key = |c: u8| if c == b'/' { 0u8 } else { c.saturating_add(1) };
    let (a, b) = (a.as_bytes(), b.as_bytes());
    for i in 0..a.len().min(b.len()) {
        match key(a[i]).cmp(&key(b[i])) {
            std::cmp::Ordering::Equal => {}
            o => return o,
        }
    }
    a.len().cmp(&b.len())
}

/// Sort entries into the tree order `base.idx` requires.
pub fn tree_order(entries: &mut [(ShareId, String)]) {
    entries.sort_by(|(sa, pa), (sb, pb)| sa.0.cmp(&sb.0).then_with(|| tree_cmp(pa, pb)));
}

// ---------------------------------------------------------------------------
// builder
// ---------------------------------------------------------------------------

/// Builds `base.idx` from a full crawl (§4.2, "initial activation").
#[derive(Clone, Copy, Debug, Default)]
pub struct IndexBuilder {
    cfg: IndexConfig,
}

impl IndexBuilder {
    pub fn new() -> Self {
        Self::default()
    }

    /// plocate's default, and ours: 32. Larger compresses better and shortens
    /// posting lists, but makes the postings less precise so more work goes
    /// into filtering false positives after the intersection.
    pub fn block_size(mut self, n: u32) -> Self {
        self.cfg.block_size = n.max(1);
        self
    }

    pub fn prune_df_ratio(mut self, r: f32) -> Self {
        self.cfg.prune_df_ratio = r;
        self
    }

    pub fn merge_ratio(mut self, r: f32) -> Self {
        self.cfg.merge_ratio = r;
        self
    }

    pub fn config(&self) -> IndexConfig {
        self.cfg
    }

    /// Write a fresh index into `dir`, discarding anything already there.
    pub fn build(&self, dir: &Path, mut entries: Vec<(ShareId, String)>) -> Result<NameIndex> {
        std::fs::create_dir_all(dir).with_context(|| format!("create {dir:?}"))?;
        for stale in list_segment_files(dir)? {
            let _ = std::fs::remove_file(stale);
        }
        tree_order(&mut entries);
        let n = entries.len() as u64;
        let bytes = base::write_base(
            &dir.join("base.idx"),
            &entries,
            self.cfg.block_size,
            self.cfg.prune_df_ratio,
        )?;
        let mut meta = Meta::from_cfg(&self.cfg);
        meta.base_bytes = bytes;
        meta.entries = n;
        write_meta(dir, &meta)?;
        NameIndex::open_with(dir, self.cfg)
    }
}

// ---------------------------------------------------------------------------
// NameIndex
// ---------------------------------------------------------------------------

impl NameIndex {
    pub fn open(dir: &Path) -> Result<Self> {
        let cfg = read_meta(dir)
            .map(|m| IndexConfig {
                block_size: m.block_size.max(1),
                prune_df_ratio: m.prune_df_ratio,
                merge_ratio: m.merge_ratio,
                delta_roll_bytes: IndexConfig::default().delta_roll_bytes,
            })
            .unwrap_or_default();
        Self::open_with(dir, cfg)
    }

    pub fn open_with(dir: &Path, cfg: IndexConfig) -> Result<Self> {
        std::fs::create_dir_all(dir).with_context(|| format!("create {dir:?}"))?;
        let base_path = dir.join("base.idx");
        let (base, base_bytes) = if base_path.exists() {
            let seg = BaseSegment::open(&base_path)?;
            let bytes = seg.file_bytes;
            (Some(seg), bytes)
        } else {
            (None, 0)
        };

        // Delta segments, in creation order.
        let mut delta_files: Vec<PathBuf> = std::fs::read_dir(dir)?
            .filter_map(|e| e.ok())
            .map(|e| e.path())
            .filter(|p| {
                p.file_name()
                    .and_then(|n| n.to_str())
                    .is_some_and(|n| n.starts_with("delta.") && n.ends_with(".idx"))
            })
            .collect();
        delta_files.sort();

        let mut delta: Vec<Live> = Vec::new();
        let mut delta_bytes = 0u64;
        let mut max_seq = 0u64;
        for f in &delta_files {
            let rec = seg::read_records(f)?;
            if rec.torn {
                // Expected after a crash: cut the partial tail and carry on.
                tracing::warn!(path = ?f, good_len = rec.good_len, "truncating torn delta tail");
                seg::truncate_to(f, rec.good_len)?;
            }
            delta_bytes += rec.good_len;
            for payload in &rec.records {
                let (seq, items) = decode_payload(payload)?;
                max_seq = max_seq.max(seq);
                for (share, path) in items {
                    delta.push(Live { seq, share, path });
                }
            }
        }

        let tomb_path = dir.join("tomb.idx");
        let mut tomb: HashMap<u32, HashMap<String, u64>> = HashMap::new();
        let rec = seg::read_records(&tomb_path)?;
        if rec.torn {
            tracing::warn!(path = ?tomb_path, "truncating torn tombstone tail");
            seg::truncate_to(&tomb_path, rec.good_len)?;
        }
        let tomb_bytes = rec.good_len;
        for payload in &rec.records {
            let (seq, items) = decode_payload(payload)?;
            max_seq = max_seq.max(seq);
            for (share, path) in items {
                let e = tomb.entry(share.0).or_default().entry(path).or_insert(seq);
                *e = (*e).max(seq);
            }
        }

        let generation = read_meta(dir).map(|m| m.generation).unwrap_or(0);

        Ok(Self {
            dir: dir.to_path_buf(),
            cfg,
            inner: RwLock::new(Inner {
                base,
                base_bytes,
                delta,
                delta_files,
                delta_bytes,
                tomb,
                tomb_bytes,
                seq: max_seq + 1,
                generation,
            }),
        })
    }

    pub fn config(&self) -> IndexConfig {
        self.cfg
    }

    pub fn dir(&self) -> &Path {
        &self.dir
    }

    // -- query ------------------------------------------------------------

    /// Intersect posting lists → candidate blocks → decompress → linear scan
    /// the ≤ `block_size` names → exact substring match.
    pub fn query(&self, needle: &[u8], limit: usize) -> Result<QueryResult> {
        let folded = fold::fold(needle);
        if folded.len() < MIN_TRIGRAM_QUERY {
            return Ok(QueryResult {
                fallback: Some(FallbackReason::QueryTooShort),
                ..Default::default()
            });
        }

        let tris = trigram::distinct(&folded);
        let inner = self.inner.read();

        let mut out = QueryResult::default();
        let mut seen: HashSet<(u32, String)> = HashSet::new();
        let mut hits: Vec<IndexHit> = Vec::new();

        // ---- base ---------------------------------------------------------
        if let Some(base) = &inner.base {
            let mut lists: Vec<Vec<u32>> = Vec::with_capacity(tris.len());
            let mut pruned = 0usize;
            let mut missing = false;
            for t in &tris {
                match base.lookup(*t) {
                    Lookup::Pruned => pruned += 1,
                    Lookup::Missing => {
                        missing = true;
                        break;
                    }
                    Lookup::Postings(bytes) => match varint::decode_ascending(bytes) {
                        Some(ids) => lists.push(ids),
                        None => {
                            missing = true;
                            break;
                        }
                    },
                }
            }

            if !missing && lists.is_empty() && pruned == tris.len() && base.block_count > 0 {
                // Every trigram was high-df. The index cannot narrow this
                // query at all — say so rather than returning a wrong empty.
                return Ok(QueryResult {
                    fallback: Some(FallbackReason::AllTrigramsPruned),
                    ..Default::default()
                });
            }

            let candidates = if missing {
                Vec::new()
            } else {
                intersect(lists)
            };
            out.candidate_blocks = candidates.len();

            for bid in candidates {
                let entries = base.block(bid)?;
                out.scanned_entries += entries.len();
                let before = hits.len();
                for e in entries {
                    if !matches_folded(&e.path, &folded) {
                        continue;
                    }
                    if is_tombstoned(&inner, e.share, &e.path, 0) {
                        continue;
                    }
                    if seen.insert((e.share.0, e.path.clone())) {
                        hits.push(make_hit(e.share, e.path, &folded));
                    }
                }
                if hits.len() == before {
                    out.false_positive_blocks += 1;
                }
            }
        }

        // ---- delta: small enough that a linear scan is the right answer ---
        for d in &inner.delta {
            out.scanned_entries += 1;
            if !matches_folded(&d.path, &folded) {
                continue;
            }
            if is_tombstoned(&inner, d.share, &d.path, d.seq) {
                continue;
            }
            if seen.insert((d.share.0, d.path.clone())) {
                hits.push(make_hit(d.share, d.path.clone(), &folded));
            }
        }

        hits.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
                .then_with(|| a.path.cmp(&b.path))
        });
        hits.truncate(limit);
        out.hits = hits;
        Ok(out)
    }

    // -- writes -----------------------------------------------------------

    /// Append to the current delta segment. **O(1)** in the size of the index:
    /// one framed record, one write, one meta swap. This is why the segment
    /// split exists.
    pub fn append(&self, entries: &[(ShareId, String)]) -> Result<()> {
        if entries.is_empty() {
            return Ok(());
        }
        let mut inner = self.inner.write();
        let seq = inner.seq;
        inner.seq += 1;

        let payload = encode_payload(seq, entries);
        let path = self.current_delta(&mut inner);
        let written = seg::append_record(&path, &payload)?;

        if !inner.delta_files.contains(&path) {
            inner.delta_files.push(path);
        }
        inner.delta_bytes += written;
        for (share, p) in entries {
            inner.delta.push(Live {
                seq,
                share: *share,
                path: p.clone(),
            });
        }
        self.persist_meta(&inner)?;
        Ok(())
    }

    /// Record deletions. `base.idx` is never touched (§4.2).
    pub fn tombstone(&self, paths: &[(ShareId, String)]) -> Result<()> {
        if paths.is_empty() {
            return Ok(());
        }
        let mut inner = self.inner.write();
        let seq = inner.seq;
        inner.seq += 1;

        let payload = encode_payload(seq, paths);
        let written = seg::append_record(&self.dir.join("tomb.idx"), &payload)?;
        inner.tomb_bytes += written;
        for (share, p) in paths {
            let e = inner
                .tomb
                .entry(share.0)
                .or_default()
                .entry(p.clone())
                .or_insert(seq);
            *e = (*e).max(seq);
        }
        self.persist_meta(&inner)?;
        Ok(())
    }

    /// `Σ|delta| + |tomb| > merge_ratio × |base|` (§4.2, default 0.15).
    ///
    /// Bounding this ratio is what bounds read cost: the linear delta scan on
    /// every query can never grow past a fixed fraction of the base.
    pub fn needs_merge(&self) -> bool {
        let inner = self.inner.read();
        let extra = inner.delta_bytes + inner.tomb_bytes;
        if inner.base_bytes == 0 {
            return extra > 0;
        }
        extra as f64 > self.cfg.merge_ratio as f64 * inner.base_bytes as f64
    }

    /// Rebuild `base.idx` from `base ∪ Σdelta − tomb` and drop the segments.
    ///
    /// The only heavy operation in the whole design, so it runs under the same
    /// opportunistic gate as the initial crawl (§4.2, "the crawler uses the
    /// gate too"). `gate` is polled as we go; a `false` aborts cleanly and leaves
    /// every existing segment untouched — a refused merge must never be able
    /// to damage the index.
    pub fn merge(&self, gate: &dyn Fn() -> bool) -> Result<()> {
        if !gate() {
            return Ok(());
        }
        // Held for the duration: a merge that raced an append would delete the
        // delta file the append just landed in.
        let mut inner = self.inner.write();

        let mut live: Vec<(ShareId, String)> = Vec::new();
        let mut seen: HashSet<(u32, String)> = HashSet::new();

        if let Some(b) = &inner.base {
            for (i, block) in b.iter_entries().enumerate() {
                if i % 64 == 0 && !gate() {
                    return Ok(());
                }
                for e in block? {
                    if is_tombstoned(&inner, e.share, &e.path, 0) {
                        continue;
                    }
                    if seen.insert((e.share.0, e.path.clone())) {
                        live.push((e.share, e.path));
                    }
                }
            }
        }
        for d in &inner.delta {
            if is_tombstoned(&inner, d.share, &d.path, d.seq) {
                continue;
            }
            if seen.insert((d.share.0, d.path.clone())) {
                live.push((d.share, d.path.clone()));
            }
        }
        if !gate() {
            return Ok(());
        }

        tree_order(&mut live);
        let tmp = self.dir.join("base.idx.new");
        let bytes = base::write_base(&tmp, &live, self.cfg.block_size, self.cfg.prune_df_ratio)?;

        // Drop the mapping before the rename. Windows refuses to replace a
        // mapped file, and even on Linux keeping a mapping of a replaced inode
        // is a bug waiting to happen.
        inner.base = None;
        let base_path = self.dir.join("base.idx");
        std::fs::rename(&tmp, &base_path).with_context(|| format!("rename into {base_path:?}"))?;

        for f in std::mem::take(&mut inner.delta_files) {
            let _ = std::fs::remove_file(f);
        }
        let _ = std::fs::remove_file(self.dir.join("tomb.idx"));

        inner.base = Some(BaseSegment::open(&base_path)?);
        inner.base_bytes = bytes;
        inner.delta.clear();
        inner.delta_bytes = 0;
        inner.tomb.clear();
        inner.tomb_bytes = 0;
        inner.generation += 1;

        self.persist_meta(&inner)?;
        Ok(())
    }

    // -- reconciliation -----------------------------------------------------

    /// Direct children of `parent` (a share-relative display path, `""` for
    /// the share root) that this index currently believes exist, restricted
    /// to `share`.
    ///
    /// Built on top of [`Self::query`] rather than a dedicated structure:
    /// `DESIGN-FOOTPRINT.md` §2 deliberately has no `parent -> children`
    /// index anywhere in this design (forward lookups are not needed for
    /// search — see that doc's §2 table), so this does not add one either.
    /// It queries for `parent` as a substring, then keeps only hits whose own
    /// parent (everything before the last `/`) is exactly `parent`.
    ///
    /// This exists for watcher-driven reconciliation (`bridge.rs`'s
    /// `reconcile_watch_event`): a caller lists the real directory and diffs
    /// it against this to know exactly what changed, without walking the
    /// whole index or the whole tree.
    ///
    /// Best-effort in two ways that both fail toward *doing nothing* rather
    /// than a wrong answer: a `parent` under [`MIN_TRIGRAM_QUERY`] bytes
    /// (including the empty root) cannot be substring-matched at all and
    /// returns empty, and results are capped at `limit`. Either one just
    /// means the caller's diff has fewer "already known" entries than reality
    /// — it appends a few names that were in fact already indexed (harmless;
    /// query-time dedup absorbs it) rather than ever tombstoning something
    /// that still exists.
    pub fn children_of(&self, share: ShareId, parent: &str, limit: usize) -> Vec<String> {
        if parent.len() < MIN_TRIGRAM_QUERY {
            return Vec::new();
        }
        let Ok(result) = self.query(parent.as_bytes(), limit) else {
            return Vec::new();
        };
        if result.must_fall_back() {
            return Vec::new();
        }
        result
            .hits
            .into_iter()
            .filter(|h| h.share == share)
            .filter_map(|h| {
                let (p, _name) = h.path.rsplit_once('/')?;
                (p == parent).then_some(h.path)
            })
            .collect()
    }

    // -- introspection ----------------------------------------------------

    pub fn size_bytes(&self) -> u64 {
        let inner = self.inner.read();
        let meta = std::fs::metadata(self.dir.join("meta")).map(|m| m.len()).unwrap_or(0);
        inner.base_bytes + inner.delta_bytes + inner.tomb_bytes + meta
    }

    pub fn stats(&self) -> IndexStats {
        let inner = self.inner.read();
        let (base_entries, blocks, trigrams, pruned) = match &inner.base {
            Some(b) => (b.entry_count, b.block_count, b.trigram_count, b.pruned_count),
            None => (0, 0, 0, 0),
        };
        IndexStats {
            entries: base_entries + inner.delta.len() as u64,
            base_entries,
            delta_entries: inner.delta.len() as u64,
            tombstones: tomb_count(&inner),
            blocks,
            trigrams,
            pruned_trigrams: pruned,
            base_bytes: inner.base_bytes,
            delta_bytes: inner.delta_bytes,
            tomb_bytes: inner.tomb_bytes,
            delta_segments: inner.delta_files.len(),
            generation: inner.generation,
        }
    }

    // -- internals --------------------------------------------------------

    fn current_delta(&self, inner: &mut Inner) -> PathBuf {
        if let Some(last) = inner.delta_files.last() {
            let big = std::fs::metadata(last)
                .map(|m| m.len() >= self.cfg.delta_roll_bytes)
                .unwrap_or(false);
            if !big {
                return last.clone();
            }
        }
        self.dir
            .join(format!("delta.{:03}.idx", inner.delta_files.len()))
    }

    fn persist_meta(&self, inner: &Inner) -> Result<()> {
        let meta = Meta {
            version: 1,
            block_size: self.cfg.block_size,
            prune_df_ratio: self.cfg.prune_df_ratio,
            merge_ratio: self.cfg.merge_ratio,
            generation: inner.generation,
            seq: inner.seq,
            base_bytes: inner.base_bytes,
            delta_bytes: inner.delta_bytes,
            tomb_bytes: inner.tomb_bytes,
            entries: inner.base.as_ref().map(|b| b.entry_count).unwrap_or(0)
                + inner.delta.len() as u64,
            delta_files: inner
                .delta_files
                .iter()
                .filter_map(|p| p.file_name().and_then(|n| n.to_str()).map(String::from))
                .collect(),
        };
        write_meta(&self.dir, &meta)
    }
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

fn make_hit(share: ShareId, path: String, needle: &[u8]) -> IndexHit {
    let name = path.rsplit('/').next().unwrap_or(&path).to_string();
    let name_folded = fold::fold_str(&name);
    let score = rank::score(&RankInput {
        name_folded: &name_folded,
        needle,
        path: &path,
        mtime_ns: None,
        now_ns: 0,
        scope: None,
        // The name index deliberately stores no term frequencies and no
        // positions (§4.1 point 3), so there is nothing to compute BM25 from.
        // The term is reserved for T4 content search.
        bm25: 0.0,
    });
    IndexHit {
        share,
        path,
        name,
        score,
    }
}

/// Match against the **full stored path**, which subsumes matching the name.
/// Storing the path is what keeps the index self-contained.
fn matches_folded(path: &str, needle: &[u8]) -> bool {
    fold::contains(&fold::fold_str(path), needle)
}

fn is_tombstoned(inner: &Inner, share: ShareId, path: &str, entry_seq: u64) -> bool {
    inner
        .tomb
        .get(&share.0)
        .and_then(|m| m.get(path))
        .is_some_and(|&tomb_seq| entry_seq <= tomb_seq)
}

fn tomb_count(inner: &Inner) -> u64 {
    inner.tomb.values().map(|m| m.len() as u64).sum()
}

/// Intersect ascending posting lists, smallest first.
fn intersect(mut lists: Vec<Vec<u32>>) -> Vec<u32> {
    if lists.is_empty() {
        return Vec::new();
    }
    lists.sort_by_key(|l| l.len());
    let mut acc = lists.remove(0);
    for l in lists {
        if acc.is_empty() {
            return acc;
        }
        let mut out = Vec::with_capacity(acc.len().min(l.len()));
        let (mut i, mut j) = (0usize, 0usize);
        while i < acc.len() && j < l.len() {
            match acc[i].cmp(&l[j]) {
                std::cmp::Ordering::Less => i += 1,
                std::cmp::Ordering::Greater => j += 1,
                std::cmp::Ordering::Equal => {
                    out.push(acc[i]);
                    i += 1;
                    j += 1;
                }
            }
        }
        acc = out;
    }
    acc
}

fn encode_payload(seq: u64, entries: &[(ShareId, String)]) -> Vec<u8> {
    let mut body = Vec::new();
    varint::put(&mut body, seq);
    varint::put(&mut body, entries.len() as u64);
    for (share, path) in entries {
        varint::put(&mut body, share.0 as u64);
        varint::put(&mut body, path.len() as u64);
        body.extend_from_slice(path.as_bytes());
    }
    let comp = codec::compress_fast(&body);
    let mut out = Vec::with_capacity(comp.len().min(body.len()) + 1);
    if comp.len() < body.len() {
        out.push(CODEC_ZSTD);
        out.extend_from_slice(&comp);
    } else {
        out.push(CODEC_RAW);
        out.extend_from_slice(&body);
    }
    out
}

fn decode_payload(payload: &[u8]) -> Result<(u64, Vec<(ShareId, String)>)> {
    let (tag, rest) = payload.split_first().context("empty segment record")?;
    let body = match *tag {
        CODEC_RAW => rest.to_vec(),
        CODEC_ZSTD => codec::decompress(rest)?,
        other => anyhow::bail!("unknown segment record codec {other}"),
    };
    let mut pos = 0usize;
    let seq = varint::get(&body, &mut pos).context("record: truncated seq")?;
    let n = varint::get(&body, &mut pos).context("record: truncated count")? as usize;
    let mut out = Vec::with_capacity(n.min(1 << 16));
    for _ in 0..n {
        let share = varint::get(&body, &mut pos).context("record: truncated share")?;
        let len = varint::get(&body, &mut pos).context("record: truncated length")? as usize;
        let end = pos.checked_add(len).context("record: length overflow")?;
        if end > body.len() {
            anyhow::bail!("record: path runs past end");
        }
        out.push((
            ShareId(share as u32),
            String::from_utf8_lossy(&body[pos..end]).into_owned(),
        ));
        pos = end;
    }
    Ok((seq, out))
}

fn list_segment_files(dir: &Path) -> Result<Vec<PathBuf>> {
    let mut v = Vec::new();
    if !dir.exists() {
        return Ok(v);
    }
    for e in std::fs::read_dir(dir)? {
        let p = e?.path();
        let Some(name) = p.file_name().and_then(|n| n.to_str()) else {
            continue;
        };
        if name == "base.idx"
            || name == "base.idx.new"
            || name == "tomb.idx"
            || (name.starts_with("delta.") && name.ends_with(".idx"))
        {
            v.push(p);
        }
    }
    Ok(v)
}

/// `meta` is swapped by atomic rename (§4.2, crash safety).
fn write_meta(dir: &Path, meta: &Meta) -> Result<()> {
    let tmp = dir.join("meta.tmp");
    let json = serde_json::to_vec_pretty(meta)?;
    std::fs::write(&tmp, &json).with_context(|| format!("write {tmp:?}"))?;
    std::fs::rename(&tmp, dir.join("meta")).context("swap meta")?;
    Ok(())
}

fn read_meta(dir: &Path) -> Option<Meta> {
    let bytes = std::fs::read(dir.join("meta")).ok()?;
    serde_json::from_slice(&bytes).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn into_hit_carries_the_index_data_through_and_fills_in_what_the_caller_resolved() {
        let ih = IndexHit {
            share: ShareId(7),
            path: "a/b.txt".to_string(),
            name: "b.txt".to_string(),
            score: 3.5,
        };
        let hit = ih.into_hit(false, Some(42), Some(100));
        assert_eq!(hit.share, ShareId(7));
        assert_eq!(hit.path, "a/b.txt");
        assert_eq!(hit.name.as_str(), "b.txt");
        assert_eq!(hit.score, 3.5);
        assert!(!hit.is_dir);
        assert_eq!(hit.size, Some(42));
        assert_eq!(hit.mtime_ns, Some(100));
    }

    #[test]
    fn tree_order_groups_siblings() {
        let mut v = vec![
            (ShareId(1), "a.txt".to_string()),
            (ShareId(1), "a/z.txt".to_string()),
            (ShareId(1), "a/b.txt".to_string()),
            (ShareId(0), "z".to_string()),
        ];
        tree_order(&mut v);
        let paths: Vec<&str> = v.iter().map(|(_, p)| p.as_str()).collect();
        // share 0 first; then within share 1 the directory `a/` is contiguous
        // and sorts before the sibling file `a.txt`.
        assert_eq!(paths, vec!["z", "a/b.txt", "a/z.txt", "a.txt"]);
    }

    #[test]
    fn intersection() {
        assert_eq!(
            intersect(vec![vec![1, 2, 3, 8], vec![2, 3, 9], vec![2, 3]]),
            vec![2, 3]
        );
        assert!(intersect(vec![vec![1], vec![2]]).is_empty());
        assert!(intersect(vec![]).is_empty());
    }

    #[test]
    fn children_of_returns_only_direct_children_of_the_given_share() {
        let dir = tempfile::tempdir().unwrap();
        let entries = vec![
            (ShareId(1), "photos/a.jpg".to_string()),
            (ShareId(1), "photos/b.jpg".to_string()),
            // Grandchild — must not appear in `children_of("photos", ...)`.
            (ShareId(1), "photos/2026/c.jpg".to_string()),
            // Same name, different share — must not leak across shares.
            (ShareId(2), "photos/a.jpg".to_string()),
            // Unrelated top-level file.
            (ShareId(1), "readme.txt".to_string()),
        ];
        let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();

        let mut kids = idx.children_of(ShareId(1), "photos", 100);
        kids.sort();
        assert_eq!(kids, vec!["photos/a.jpg".to_string(), "photos/b.jpg".to_string()]);
    }

    #[test]
    fn children_of_root_is_empty_rather_than_wrong() {
        // The root path ("") is under `MIN_TRIGRAM_QUERY` bytes and cannot be
        // substring-matched — this must return nothing, never guess, so a
        // caller diffing against it only ever appends (safe) and never
        // tombstones something that is still there.
        let dir = tempfile::tempdir().unwrap();
        let entries = vec![(ShareId(1), "a.txt".to_string())];
        let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();
        assert!(idx.children_of(ShareId(1), "", 100).is_empty());
    }

    #[test]
    fn children_of_reflects_appends_and_tombstones() {
        let dir = tempfile::tempdir().unwrap();
        let entries = vec![(ShareId(1), "dir/one.txt".to_string())];
        let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();
        assert_eq!(idx.children_of(ShareId(1), "dir", 100), vec!["dir/one.txt".to_string()]);

        idx.append(&[(ShareId(1), "dir/two.txt".to_string())]).unwrap();
        let mut kids = idx.children_of(ShareId(1), "dir", 100);
        kids.sort();
        assert_eq!(kids, vec!["dir/one.txt".to_string(), "dir/two.txt".to_string()]);

        idx.tombstone(&[(ShareId(1), "dir/one.txt".to_string())]).unwrap();
        assert_eq!(idx.children_of(ShareId(1), "dir", 100), vec!["dir/two.txt".to_string()]);
    }

    #[test]
    fn payload_roundtrip() {
        let entries = vec![
            (ShareId(3), "여름/휴가/사진.jpg".to_string()),
            (ShareId(3), "docs/report.pdf".to_string()),
        ];
        let p = encode_payload(42, &entries);
        let (seq, back) = decode_payload(&p).unwrap();
        assert_eq!(seq, 42);
        assert_eq!(back, entries);
    }
}
