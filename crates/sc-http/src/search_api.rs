//! Trait boundary for `GET /api/search` / `GET /api/search/stream`.
//! Kept out of [`crate::core_api::CoreApi`] because
//! it is backed by `sc-search`'s `Walker`, which this crate does not (and
//! should not) depend on — `sc-server` binds it to the real walker over live
//! `ShareRoot`s.

use sc_vfs::ids::UserId;

use crate::core_api::CoreError;
use crate::search_limits::SearchTier;

#[derive(Clone, Debug, Default)]
pub struct SearchQuery {
    pub text: String,
    /// Restrict the walk to this virtual path (and below). `None` means
    /// every root the caller can read.
    pub scope: Option<String>,
    /// Extension-group filter (`kind=image` etc.) — never
    /// opens a file to decide.
    pub kind: Option<String>,
    pub mtime_after_ns: Option<i128>,
    pub size_min: Option<u64>,
    pub size_max: Option<u64>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct SearchHit {
    pub path: String,
    pub name: String,
    pub is_dir: bool,
    pub size: Option<u64>,
    pub mtime_ns: Option<i128>,
    pub score: f32,
}

/// Mirrors `sc_search::Completeness`, minus the `sc-search` dependency
/// ("report truncation honestly").
#[derive(Clone, Debug, PartialEq)]
pub enum SearchCompleteness {
    Full,
    Truncated { reason: String, seen: u64, elapsed_ms: u64 },
}

#[derive(Clone, Debug)]
pub struct SearchOutcome {
    pub hits: Vec<SearchHit>,
    pub completeness: SearchCompleteness,
}

pub trait SearchApi: Send + Sync {
    /// Which concurrency budget this query would
    /// run under: folds the storage class of every share the query's
    /// `scope` resolves to (or every readable share, if unscoped) to the
    /// more conservative tier. Deliberately **cheap** — it resolves which
    /// roots are in play (an ACL/registry lookup) but never walks a
    /// directory tree, so the HTTP layer can call it *before* acquiring a
    /// concurrency permit, to know which of the two budgets to acquire
    /// from.
    fn search_tier(&self, user: UserId, q: &SearchQuery) -> SearchTier;

    /// Batch search: runs to completion (or budget exhaustion) and returns
    /// every hit at once, already ranked.
    /// **Blocking** — callers on the async path must `spawn_blocking`.
    fn search(&self, user: UserId, q: &SearchQuery) -> Result<SearchOutcome, CoreError>;

    /// Streaming search: calls `on_hit` once per result *as it is found*
    /// (unranked — says ranking happens client-side
    /// on completion), in the order the walker's `emit` produces them.
    /// `on_hit` returning `false` requests an early stop (the SSE client
    /// disconnected); the real stop reason reported back is still whatever
    /// the walk's own budget produced if that fired first.
    ///
    /// **Blocking** — this runs the walk synchronously on the calling
    /// thread; callers on the async path must `spawn_blocking` it and feed
    /// `on_hit` from inside that same blocking closure.
    fn search_stream(&self, user: UserId, q: &SearchQuery, on_hit: &mut dyn FnMut(SearchHit) -> bool) -> SearchCompleteness;
}

/// Reports an empty, complete result for every query — the default until a
/// real walker is wired in, and for HTTP-layer tests that don't exercise
/// search.
pub struct UnimplementedSearch;

impl SearchApi for UnimplementedSearch {
    fn search_tier(&self, _user: UserId, _q: &SearchQuery) -> SearchTier {
        SearchTier::Fast
    }
    fn search(&self, _user: UserId, _q: &SearchQuery) -> Result<SearchOutcome, CoreError> {
        Ok(SearchOutcome { hits: Vec::new(), completeness: SearchCompleteness::Full })
    }
    fn search_stream(&self, _user: UserId, _q: &SearchQuery, _on_hit: &mut dyn FnMut(SearchHit) -> bool) -> SearchCompleteness {
        SearchCompleteness::Full
    }
}
