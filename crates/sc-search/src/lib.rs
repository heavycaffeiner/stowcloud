//! `sc-search` — search for stowcloud. See `docs/.
//!
//! Three things live here, in the order the design ranks them:
//!
//! * **T2, [`Walker`]** (§3) — the parallel filesystem walk. This is the
//!   *first-class* search path, not a fallback: zero `statx` for name-only
//!   matching, work-stealing at directory granularity, BFS so that budget
//!   exhaustion still leaves "the shallow levels are complete" as a useful
//!   guarantee, and ACL pruning that never enters a subtree the caller
//!   rejects.
//! * **T3, [`NameIndex`]** (§4) — the opt-in block-compressed trigram name
//!   index, plocate model: postings point at *blocks of 32 names*, not at
//!   documents; no position information; high-df trigrams pruned. Segmented
//!   into `base.idx` + `delta.NNN.idx` + `tomb.idx` so that `append` is O(1)
//!   (an immutable block index cannot be upserted).
//! * **The estimator** (§6) — projects name-index size from a sampled corpus
//!   scan, with every coefficient *measured* rather than hardcoded, and a
//!   human-readable `formula` so an admin can check our arithmetic.
//!
//! §5 (content indexing / OCR) is deliberately not implemented here.
//!
//! ## Filenames are bytes
//!
//! Matching is done on bytes throughout ([`Matcher::matches_name`] takes
//! `&[u8]`, trigrams are byte trigrams). Lossy UTF-8 conversion happens only
//! at display time. This is what makes CJK substring search work: a UTF-8
//! Hangul syllable is exactly three bytes, i.e. exactly one trigram.

pub mod codec;
pub mod estimate;
pub mod fold;
pub mod hll;
pub mod index;
pub mod matcher;
pub mod rank;
pub mod settings;
pub mod topn;
pub mod trigram;
pub mod varint;
pub mod vfs;
pub mod walker;

pub use estimate::{
    estimate_name_index, Confidence, CorpusScanner, CorpusStats, NameIndexEstimate,
};
pub use hll::HyperLogLog;
pub use index::{FallbackReason, IndexBuilder, IndexHit, IndexStats, NameIndex, QueryResult};
pub use matcher::{KindFilter, MatchMode, Matcher};
pub use rank::{score, RankInput};
pub use topn::TopN;
pub use settings::IndexSettingsStore;
pub use vfs::{DirEntry, DirSource, Kind, SafePath, ShareId, ShareSource, Stat};
pub use walker::{Completeness, Hit, TruncReason, WalkBudget, Walker};
