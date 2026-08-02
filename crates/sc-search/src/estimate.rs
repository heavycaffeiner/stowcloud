//! **The index estimator** (§6) — name index only. §5's content index and OCR
//! are out of scope here.
//!
//! The estimator is not a tool for recommending the index. It is a tool for
//! giving an admin *numbers to decide with*, and for most deployments the
//! right conclusion is "leave it off" — which the estimator must also support
//! with numbers (§6, opening paragraph).
//!
//! ## The rule that shapes everything here
//!
//! §6.1: **do not hardcode guessed constants.** Compression ratio varies by
//! multiples between a photo library (`IMG_0001.jpg`, `IMG_0002.jpg`, …) and a
//! document tree with unrelated names. So:
//!
//! * `sample_compress_ratio` is obtained by **actually zstd-compressing ~100
//!   sampled 32-name blocks**, not assumed.
//! * `distinct_trigrams_est` comes from a [`HyperLogLog`](crate::HyperLogLog)
//!   over the real trigrams, not from a per-file constant.
//!
//! Everything that remains a coefficient is stated in the returned
//! [`NameIndexEstimate::formula`] so it can be checked by hand.

use std::time::{Duration, Instant};

use crate::codec;
use crate::fold;
use crate::hll::HyperLogLog;
use crate::index::{BLOCKDIR_ENTRY, DICT_ENTRY, HEADER_LEN};
use crate::trigram;
use crate::varint;

/// Per-entry framing inside an uncompressed block: `uvarint(share)` +
/// `uvarint(len)`. Two bytes for typical ids and lengths, three once paths
/// exceed 127 bytes.
pub const ENTRY_OVERHEAD_BYTES: u64 = 3;

/// Fallback fraction of posting entries surviving high-df pruning, used only
/// when [`CorpusStats::posting_bytes_per_block`] is zero — i.e. when a caller
/// hand-built a `CorpusStats` without running a sampling scan. The real number
/// comes from the sample; this exists so the function still returns something
/// defensible rather than panicking.
pub const FALLBACK_PRUNE_RETENTION: f64 = 0.30;

/// Name-index crawl rate under §8's `index.name.crawl_rate` (NVMe figure).
pub const CRAWL_ENTRIES_PER_SEC: f64 = 20_000.0;

/// Measured block-compression throughput, bytes/sec of *input*.
pub const COMPRESS_BYTES_PER_SEC: f64 = 40_000_000.0;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Confidence {
    /// The scan completed — these are counts, not projections.
    High,
    /// The scan was cut short but covered ≥10% of the corpus.
    Medium,
    /// Extrapolated from a small sample.
    Low,
}

/// §6.2. Collected by the T2 walker, almost entirely without `statx`.
#[derive(Clone, Debug)]
pub struct CorpusStats {
    pub files: u64,
    pub dirs: u64,
    /// Sum of the byte lengths of the paths as they would be *stored* (i.e.
    /// after folding). Not the on-disk name lengths.
    pub name_bytes_total: u64,
    /// HyperLogLog estimate. This is the term that makes a CJK corpus cost
    /// more than a Latin one, and measuring it is the point.
    pub distinct_trigrams_est: u64,
    /// Measured, by compressing sampled blocks.
    pub sample_compress_ratio: f32,
    /// Measured posting-list bytes contributed by one block, after high-df
    /// pruning.
    ///
    /// **Addition to the struct as specified.** §6.3 requires the posting term
    /// to be sample-based — "sample blocks for a trigram → block frequency
    /// distribution, scale it to the whole, then subtract what's over the
    /// pruning threshold" — and there is no way to honour that from the
    /// other fields alone. An analytic occupancy model
    /// over `distinct_trigrams_est` assumes every trigram is equally common,
    /// which is precisely the assumption pruning exists to exploit; on a real
    /// photo corpus it over-predicts the posting term by ~7×. Since sampled
    /// blocks are a uniform random sample of all blocks, the per-block figure
    /// measured on them is an unbiased estimate of the per-block figure over
    /// the whole corpus, and multiplying by the block count is exact.
    ///
    /// Zero means "not measured"; the estimator then falls back to the
    /// analytic model and says so in the formula.
    pub posting_bytes_per_block: f32,
    pub scanned_entries: u64,
    pub elapsed: Duration,
    pub truncated: bool,
}

impl Default for CorpusStats {
    fn default() -> Self {
        Self {
            files: 0,
            dirs: 0,
            name_bytes_total: 0,
            distinct_trigrams_est: 0,
            sample_compress_ratio: 1.0,
            posting_bytes_per_block: 0.0,
            scanned_entries: 0,
            elapsed: Duration::ZERO,
            truncated: false,
        }
    }
}

#[derive(Clone, Debug)]
pub struct NameIndexEstimate {
    pub index_bytes: u64,
    pub build_secs: u64,
    pub confidence: Confidence,
    /// Term-by-term derivation — "2.10M × 25 B" — so that when the estimate is
    /// wrong, *which term* is wrong is visible.
    ///
    /// The server logs this per request rather than sending it on the wire:
    /// an operator checking the arithmetic needs the terms, and every other
    /// reader needed a size and a duration.
    pub formula: String,
}

/// §6.3.
///
/// ```text
/// blocks        = ceil(files / block_size)
/// block_bytes   ≈ (name_bytes_total + files × 3 B) × sample_compress_ratio
/// blockdir      = blocks × 16 B
/// dict_bytes    = distinct_trigrams × 12 B
/// posting_bytes ≈ Σ_t df(t) × varint width      (high-df trigrams excluded)
/// index_bytes   ≈ header + blockdir + dict + postings + blocks
/// ```
pub fn estimate_name_index(stats: &CorpusStats, block_size: u32) -> NameIndexEstimate {
    let bs = block_size.max(1) as u64;
    let files = stats.files;
    let blocks = files.div_ceil(bs);
    let blocks_f = blocks.max(1) as f64;

    let raw_names = stats.name_bytes_total + files * ENTRY_OVERHEAD_BYTES;
    let ratio = stats.sample_compress_ratio.clamp(0.01, 1.0) as f64;
    let block_bytes = (raw_names as f64 * ratio) as u64;
    let blockdir_bytes = blocks * BLOCKDIR_ENTRY as u64;

    let d = stats.distinct_trigrams_est.max(1) as f64;
    let dict_bytes = stats.distinct_trigrams_est * DICT_ENTRY as u64;

    // Posting length. Preferred path: measured on the sampled blocks, then
    // scaled by the block count. Sampled blocks are a uniform random sample of
    // all blocks, so per-block bytes measured on them estimate per-block bytes
    // over the corpus without bias, and pruning is applied using the sample's
    // own document-frequency distribution rather than assumed away.
    let (posting_bytes, posting_note) = if stats.posting_bytes_per_block > 0.0 {
        (
            (blocks_f * stats.posting_bytes_per_block as f64) as u64,
            format!(
                "{:.1} B/block measured on sampled blocks (post-pruning)",
                stats.posting_bytes_per_block
            ),
        )
    } else {
        // Analytic fallback. With `occ` trigram occurrences spread over `d`
        // distinct values, a block holding `occ/blocks` of them covers
        // `d × (1 − e^(−occ_per_block / d))` distinct values — the standard
        // occupancy expression. It cannot model pruning (it treats every
        // trigram as equally common), hence the flat retention factor and the
        // warning in the formula.
        let occ = stats.name_bytes_total.saturating_sub(2 * files) as f64;
        let distinct_per_block = d * (1.0 - (-(occ / blocks_f) / d).exp());
        let retained = blocks_f * distinct_per_block * FALLBACK_PRUNE_RETENTION;
        let avg_df = (retained / d).max(1.0);
        let avg_gap = (blocks_f / avg_df).max(1.0);
        let first = d * varint::len(blocks / 2) as f64;
        let rest = (retained - d).max(0.0) * varint::len(avg_gap as u64) as f64;
        (
            (first + rest) as u64,
            "analytic occupancy model, NOT sampled — treat as a lower-confidence bound".into(),
        )
    };

    let index_bytes =
        HEADER_LEN as u64 + blockdir_bytes + dict_bytes + posting_bytes + block_bytes;

    // §6.5. CPU time only — the wall-clock projection has to be divided by the
    // opportunistic gate's duty cycle, which the caller observes and which is
    // deliberately not faked here.
    let crawl_secs = stats.scanned_entries.max(files) as f64 / CRAWL_ENTRIES_PER_SEC;
    let compress_secs = raw_names as f64 / COMPRESS_BYTES_PER_SEC;
    let build_secs = (crawl_secs + compress_secs).ceil().max(1.0) as u64;

    let confidence = if !stats.truncated {
        Confidence::High
    } else {
        let total = files + stats.dirs;
        if total > 0 && stats.scanned_entries * 10 >= total {
            Confidence::Medium
        } else {
            Confidence::Low
        }
    };

    let per_file = if files > 0 {
        index_bytes as f64 / files as f64
    } else {
        0.0
    };

    let formula = format!(
        "{files} files → {blocks} blocks of {bs}\n\
         names   {raw} × {ratio:.3} (measured zstd ratio) = {block_h}\n\
         blockdir {blocks} × {bd} B = {blockdir_h}\n\
         dict     {tri} trigrams × {de} B = {dict_h}\n\
         postings {blocks} × {posting_note} = {posting_h}\n\
         header   {hdr} B\n\
         total    {total_h}  ({per_file:.1} B/file)\n\
         build    {crawl:.0} s crawl @ {rate:.0}/s + {comp:.0} s compress @ {cbps} MB/s \
         = {build_secs} s CPU (divide by the gate duty cycle for wall clock)",
        files = files,
        blocks = blocks,
        bs = bs,
        raw = human(raw_names),
        ratio = ratio,
        block_h = human(block_bytes),
        bd = BLOCKDIR_ENTRY,
        blockdir_h = human(blockdir_bytes),
        tri = stats.distinct_trigrams_est,
        de = DICT_ENTRY,
        dict_h = human(dict_bytes),
        posting_note = posting_note,
        posting_h = human(posting_bytes),
        hdr = HEADER_LEN,
        total_h = human(index_bytes),
        per_file = per_file,
        crawl = crawl_secs,
        rate = CRAWL_ENTRIES_PER_SEC,
        comp = compress_secs,
        cbps = (COMPRESS_BYTES_PER_SEC / 1e6) as u64,
        build_secs = build_secs,
    );

    NameIndexEstimate {
        index_bytes,
        build_secs,
        confidence,
        formula,
    }
}

fn human(n: u64) -> String {
    const U: [(&str, u64); 4] = [("GB", 1 << 30), ("MB", 1 << 20), ("KB", 1 << 10), ("B", 1)];
    for (s, div) in U {
        if n >= div {
            return if div == 1 {
                format!("{n} B")
            } else {
                format!("{:.2} {s}", n as f64 / div as f64)
            };
        }
    }
    "0 B".into()
}

// ---------------------------------------------------------------------------
// corpus scanning
// ---------------------------------------------------------------------------

/// Default number of blocks to actually compress when measuring the ratio.
/// §6.3: "sampling 100 of the 32-filename blocks in tree order and
/// zstd-compressing them lands within ±5%."
pub const DEFAULT_SAMPLE_BLOCKS: usize = 100;

/// Accumulates [`CorpusStats`] from a stream of paths.
///
/// Driven by the T2 walker — §6.2's point is that the estimator does not need
/// its own traversal, because the walker is already fast and already gets
/// almost everything without `statx`.
pub struct CorpusScanner {
    block_size: usize,
    max_samples: usize,
    prune_df_ratio: f32,
    hll: HyperLogLog,
    files: u64,
    dirs: u64,
    name_bytes_total: u64,
    scanned_entries: u64,
    truncated: bool,
    /// Names accumulating into the current block. Kept in arrival order, which
    /// is the walker's order, which is tree order — the same adjacency the real
    /// builder gets.
    cur: Vec<String>,
    samples: Vec<Vec<String>>,
    blocks_seen: u64,
    rng: u64,
    start: Instant,
}

impl Default for CorpusScanner {
    fn default() -> Self {
        Self::new(32, DEFAULT_SAMPLE_BLOCKS)
    }
}

impl CorpusScanner {
    pub fn new(block_size: u32, max_samples: usize) -> Self {
        Self {
            block_size: block_size.max(1) as usize,
            max_samples: max_samples.max(1),
            prune_df_ratio: crate::index::IndexConfig::default().prune_df_ratio,
            hll: HyperLogLog::default(),
            files: 0,
            dirs: 0,
            name_bytes_total: 0,
            scanned_entries: 0,
            truncated: false,
            cur: Vec::new(),
            samples: Vec::new(),
            blocks_seen: 0,
            rng: 0x2545_F491_4F6C_DD1D,
            start: Instant::now(),
        }
    }

    /// Feed one entry. Only files are indexed, matching the model's `files`
    /// term; directories are counted for the confidence calculation.
    pub fn observe(&mut self, path: &str, is_dir: bool) {
        self.scanned_entries += 1;
        if is_dir {
            self.dirs += 1;
            return;
        }
        self.files += 1;
        let folded = fold::fold_str(path);
        self.name_bytes_total += folded.len() as u64;
        for t in trigram::trigrams(&folded) {
            self.hll.add(&t);
        }
        self.cur.push(String::from_utf8_lossy(&folded).into_owned());
        if self.cur.len() == self.block_size {
            let block = std::mem::take(&mut self.cur);
            self.offer_sample(block);
        }
    }

    pub fn set_truncated(&mut self, truncated: bool) {
        self.truncated = truncated;
    }

    /// Must match the ratio the index will actually be built with, or the
    /// posting measurement prunes a different set than the builder does.
    pub fn prune_df_ratio(mut self, r: f32) -> Self {
        self.prune_df_ratio = r;
        self
    }

    /// Reservoir sampling: every full block has the same chance of ending up
    /// in the sample, so the ratio is not biased towards the start of the tree.
    fn offer_sample(&mut self, block: Vec<String>) {
        self.blocks_seen += 1;
        if self.samples.len() < self.max_samples {
            self.samples.push(block);
            return;
        }
        let j = (self.next_rand() % self.blocks_seen) as usize;
        if j < self.max_samples {
            self.samples[j] = block;
        }
    }

    fn next_rand(&mut self) -> u64 {
        // xorshift64*
        self.rng ^= self.rng >> 12;
        self.rng ^= self.rng << 25;
        self.rng ^= self.rng >> 27;
        self.rng.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }

    pub fn files(&self) -> u64 {
        self.files
    }

    pub fn finish(mut self) -> CorpusStats {
        if !self.cur.is_empty() {
            let block = std::mem::take(&mut self.cur);
            self.offer_sample(block);
        }
        let ratio = measure_compress_ratio(&self.samples);
        let blocks = self.files.div_ceil(self.block_size as u64);
        let posting_bytes_per_block =
            measure_posting_bytes_per_block(&self.samples, blocks, self.prune_df_ratio);
        CorpusStats {
            files: self.files,
            dirs: self.dirs,
            name_bytes_total: self.name_bytes_total,
            distinct_trigrams_est: self.hll.estimate_u64(),
            sample_compress_ratio: ratio,
            posting_bytes_per_block,
            scanned_entries: self.scanned_entries,
            elapsed: self.start.elapsed(),
            truncated: self.truncated,
        }
    }
}

/// Compress the sampled blocks for real and report `compressed / raw`.
///
/// This one measurement is what absorbs the multiple-times difference between
/// a photo library and a document tree (§6.3). The framing matches
/// `base.rs`'s block payload so the ratio applies to the same bytes the
/// estimate multiplies.
pub fn measure_compress_ratio(samples: &[Vec<String>]) -> f32 {
    let mut raw_total = 0usize;
    let mut comp_total = 0usize;
    for block in samples {
        let mut raw = Vec::new();
        for name in block {
            varint::put(&mut raw, 0); // share id
            varint::put(&mut raw, name.len() as u64);
            raw.extend_from_slice(name.as_bytes());
        }
        if raw.is_empty() {
            continue;
        }
        comp_total += codec::compress(&raw).len();
        raw_total += raw.len();
    }
    if raw_total == 0 {
        return 1.0;
    }
    (comp_total as f64 / raw_total as f64) as f32
}

/// Measure posting-list bytes per block on the sampled blocks (§6.3).
///
/// For each sampled block we take its distinct trigram set — one posting entry
/// each, since postings are block-level. The sample's own document frequency
/// `c_t / S` estimates the global df ratio, which decides whether the trigram
/// is pruned; a surviving trigram appears in roughly every `1/r` blocks, so its
/// delta-encoded gap is `1/r` and costs `varint_len(1/r)` bytes.
///
/// This is where the model stops guessing. An analytic version has to assume a
/// trigram distribution, and the trigram distribution is exactly what differs
/// between a photo library and a document tree.
pub fn measure_posting_bytes_per_block(
    samples: &[Vec<String>],
    total_blocks: u64,
    prune_df_ratio: f32,
) -> f32 {
    use std::collections::HashMap;

    let s = samples.len();
    if s == 0 || total_blocks == 0 {
        return 0.0;
    }

    let sets: Vec<Vec<[u8; 3]>> = samples
        .iter()
        .map(|block| {
            let mut v = Vec::new();
            for name in block {
                trigram::push_all(&mut v, name.as_bytes());
            }
            v.sort_unstable();
            v.dedup();
            v
        })
        .collect();

    let mut df: HashMap<[u8; 3], u32> = HashMap::new();
    for set in &sets {
        for t in set {
            *df.entry(*t).or_insert(0) += 1;
        }
    }

    // Mirror the builder: pruning is disabled on tiny indexes, so the estimate
    // must not apply it there either.
    let can_prune = total_blocks >= crate::index::MIN_BLOCKS_FOR_PRUNE as u64;

    let mut total_bytes = 0f64;
    for set in &sets {
        for t in set {
            let r = df[t] as f64 / s as f64;
            if can_prune && r > prune_df_ratio as f64 {
                continue;
            }
            let gap = (1.0 / r).round().max(1.0) as u64;
            total_bytes += varint::len(gap.min(total_blocks)) as f64;
        }
    }
    (total_bytes / s as f64) as f32
}

#[cfg(test)]
mod tests {
    use super::*;

    fn photo_corpus(n: u32) -> CorpusScanner {
        let mut s = CorpusScanner::default();
        for i in 0..n {
            s.observe(
                &format!("photos/2026/summer/IMG_{i:05}.jpg"),
                false,
            );
        }
        s
    }

    #[test]
    fn compress_ratio_is_measured_not_assumed() {
        let stats = photo_corpus(2000).finish();
        assert!(
            stats.sample_compress_ratio > 0.0 && stats.sample_compress_ratio < 0.6,
            "photo-library names should compress hard, got {}",
            stats.sample_compress_ratio
        );

        let mut docs = CorpusScanner::default();
        for i in 0..2000u32 {
            let h = crate::hll::hash64(&i.to_le_bytes());
            docs.observe(&format!("docs/{h:016x}/{:x}.pdf", h.rotate_left(17)), false);
        }
        let d = docs.finish();
        assert!(
            d.sample_compress_ratio > stats.sample_compress_ratio,
            "unrelated names must compress worse: {} vs {}",
            d.sample_compress_ratio,
            stats.sample_compress_ratio
        );
    }

    #[test]
    fn directories_are_counted_but_not_indexed() {
        let mut s = CorpusScanner::default();
        s.observe("a", true);
        s.observe("a/b.txt", false);
        let st = s.finish();
        assert_eq!(st.dirs, 1);
        assert_eq!(st.files, 1);
        assert_eq!(st.scanned_entries, 2);
    }

    #[test]
    fn confidence_reflects_the_scan() {
        let mut st = photo_corpus(100).finish();
        assert_eq!(estimate_name_index(&st, 32).confidence, Confidence::High);
        st.truncated = true;
        st.scanned_entries = 100;
        st.files = 100;
        st.dirs = 0;
        assert_eq!(estimate_name_index(&st, 32).confidence, Confidence::Medium);
        st.scanned_entries = 5;
        st.files = 100_000;
        assert_eq!(estimate_name_index(&st, 32).confidence, Confidence::Low);
    }

    #[test]
    fn formula_shows_every_term() {
        let st = photo_corpus(1000).finish();
        let e = estimate_name_index(&st, 32);
        for term in ["blocks", "names", "dict", "postings", "total", "build"] {
            assert!(e.formula.contains(term), "formula missing `{term}`:\n{}", e.formula);
        }
        assert!(e.index_bytes > 0);
        assert!(e.build_secs >= 1);
    }

    #[test]
    fn empty_corpus_does_not_divide_by_zero() {
        let e = estimate_name_index(&CorpusStats::default(), 32);
        assert!(e.index_bytes >= HEADER_LEN as u64);
    }
}
