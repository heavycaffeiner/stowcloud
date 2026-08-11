//! Trait boundary for `GET /api/recent`.
//!
//! Shaped after [`crate::search_api`] and separate for the same reason: the
//! query is answered against stores this crate does not (and should not)
//! depend on. `sc-server` binds it to the per-account record of the writes it
//! performed.

use sc_vfs::ids::UserId;

use crate::core_api::CoreError;

/// A recency query. `since_ns` and `limit` are already clamped by the caller.
#[derive(Clone, Debug)]
pub struct RecentQuery {
    /// Restrict to this virtual path and below. `None` means every readable
    /// root.
    pub scope: Option<String>,
    /// Inclusive lower bound on the recorded time, nanoseconds since the
    /// epoch.
    pub since_ns: i128,
    /// How many rows to keep, after dead and out-of-scope rows are dropped.
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
    /// The file's modification time, from the `stat` that answered this
    /// request.
    pub mtime_ns: i128,
    /// When this account performed the write. Differs from `mtime_ns` for a
    /// restore or a copy that preserved timestamps, which is why both are
    /// here.
    pub at_ns: i128,
    /// `upload`, `edit`, `copy`, `move` or `restore`.
    pub op: &'static str,
}

pub trait RecentApi: Send + Sync {
    /// Every file this account wrote here inside the window, newest first, up
    /// to `q.limit`.
    ///
    /// Exact, not best-effort: there is no walk to truncate. A row whose file
    /// is gone, whose share the caller no longer holds a grant on, or which
    /// names a directory is not in the answer.
    ///
    /// **Blocking**: callers on the async path must `spawn_blocking`.
    fn recent(&self, _user: UserId, _q: &RecentQuery) -> Result<Vec<RecentHit>, CoreError> {
        Ok(Vec::new())
    }

    /// Forget everything recorded for this account. Called when the account is
    /// deleted, because ids are reused and the next holder of this one must
    /// not inherit a history. Best-effort, like every other write to this
    /// store; the default body does nothing.
    fn forget_user(&self, _user: UserId) {}
}

/// Empty for every query, for builds and tests with no store behind it.
pub struct UnimplementedRecent;

impl RecentApi for UnimplementedRecent {}
