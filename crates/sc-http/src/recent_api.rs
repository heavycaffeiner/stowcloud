//! Trait boundary for `GET /api/recent`.
//!
//! Shaped after [`crate::search_api`] and separate for the same reason: the
//! query is answered by `sc-search`'s walker, which this crate does not (and
//! should not) depend on. `sc-server` binds it to the real walker over live
//! `ShareRoot`s.

use sc_vfs::ids::UserId;

use crate::core_api::CoreError;
use crate::search_api::SearchCompleteness;
use crate::search_limits::SearchTier;

/// A recency query. `since_ns` and `limit` are already clamped by the caller.
#[derive(Clone, Debug)]
pub struct RecentQuery {
    /// Restrict to this virtual path and below. `None` means every readable
    /// root.
    pub scope: Option<String>,
    /// Inclusive lower bound on mtime, nanoseconds since the epoch.
    pub since_ns: i128,
    /// How many rows to keep. This is a top-N bound, not a walk stop
    /// condition: the newest file may be the last one the walk visits.
    pub limit: u32,
}

#[derive(Clone, Debug, PartialEq)]
pub struct RecentHit {
    /// Navigable virtual path, `{label}/{rest}`, no leading slash. This is the
    /// field search's `{share, path}` pair should have been.
    pub vpath: String,
    /// Label of the share `vpath` starts with, so a client can group by root
    /// without re-parsing the path.
    pub share: String,
    pub name: String,
    pub size: u64,
    /// Never `None`: the mtime filter forces the stat phase.
    pub mtime_ns: i128,
}

pub trait RecentApi: Send + Sync {
    /// Which concurrency budget this query runs under. Resolves roots, never
    /// walks; safe to call before acquiring a permit.
    fn recent_tier(&self, _user: UserId, _q: &RecentQuery) -> SearchTier {
        SearchTier::Fast
    }

    /// The newest `q.limit` files the walk reached, newest first, ties broken
    /// by path ascending. The completeness says whether "reached" was
    /// everything: a `Full` answer is the newest N under the caller's roots, a
    /// truncated one is the newest N of what the budget reached and says so.
    ///
    /// **Blocking**: callers on the async path must `spawn_blocking`.
    fn recent(
        &self,
        _user: UserId,
        _q: &RecentQuery,
    ) -> Result<(Vec<RecentHit>, SearchCompleteness), CoreError> {
        Ok((Vec::new(), SearchCompleteness::Full))
    }
}

/// Empty and complete for every query, for builds and tests with no walker.
pub struct UnimplementedRecent;

impl RecentApi for UnimplementedRecent {}
