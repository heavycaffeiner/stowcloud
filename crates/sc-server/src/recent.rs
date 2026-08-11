//! The recency query, and the bounded collector two surfaces share.
//!
//! [`collect_newest`] is the shared half and knows nothing about recency: the
//! caller brings the matcher, so the compat `SEARCH` keeps its own (a name
//! substring, media-type extensions, an `is_collection` filter and both mtime
//! bounds) and [`RecentEngine`] builds one. Pushing the compat search through
//! a recency-shaped engine would silently impose files-only and a 30-day
//! cutoff on searches that asked for neither.
//!
//! What is being fixed is narrow and worth stating. `WalkBudget::max_results`
//! bounds a walk by stopping it, so an ordered, limited query used to end
//! after the first `cap` matches the stat phase happened to reach -- inode
//! order on rotational storage, readdir order otherwise -- and only then
//! sorted those. On any share with more than `cap` files inside the window the
//! answer was wrong, and wrong while reporting `Full`.

use std::sync::Arc;

use sc_core::{SharePath, Vpath};
use sc_http::recent_api::{RecentApi, RecentHit, RecentQuery};
use sc_http::search_api::SearchCompleteness;
use sc_http::search_limits::{SearchConcurrency, SearchTier};
use sc_vfs::{SafePath, ShareId, ShareRoot, UserId};

/// How many hits sit between the walk and the collector at once.
///
/// `Walker::emit` uses a blocking `send`, so a full channel applies
/// backpressure instead of allocating. An unbounded one would hold every
/// matching file at once now that `max_results` no longer stops the walk. This
/// bounds the channel, not the walk: the larger allocation is the walker's own
/// `pending` vector, which is unchanged.
const CHANNEL_DEPTH: usize = 4096;

/// Walk `roots` with `matcher` and return the newest `limit` hits it reached,
/// newest first, ties broken by path ascending, plus the walk's completeness.
///
/// The caller must have set a size or mtime filter on `matcher`, because
/// `TopN` orders on `Hit::mtime_ns` and only those make the walk stat.
/// `budget` should carry `max_results(u32::MAX)`: the point of collecting is
/// that the walk is bounded by time and entries rather than by result count.
pub fn collect_newest(
    walker: &sc_search::Walker,
    roots: &[(Arc<ShareRoot>, SafePath)],
    matcher: &sc_search::Matcher,
    acl: &(dyn Fn(ShareId, &SafePath) -> bool + Sync),
    budget: &sc_search::WalkBudget,
    limit: u32,
) -> (Vec<sc_search::Hit>, sc_search::Completeness) {
    let (tx, rx) = crossbeam_channel::bounded::<sc_search::Hit>(CHANNEL_DEPTH);
    // No deadlock risk: the consumer only receives and the walk only sends.
    let consumer = std::thread::spawn(move || {
        let mut top = sc_search::TopN::new(limit as usize);
        for hit in rx {
            top.offer(hit);
        }
        top.into_sorted_vec()
    });
    let completeness = walker.walk(roots, matcher, acl, budget, &tx);
    drop(tx);
    let hits = consumer.join().unwrap_or_default();
    (hits, completeness)
}

/// The recency-specific half: scope resolution and refusal, root and ACL
/// filtering, matcher and budget construction, and the conversion back to
/// navigable virtual paths.
pub struct RecentEngine {
    pub core: Arc<sc_core::Core>,
    /// Shared with the search bridge, so a `[search]` setting an operator
    /// changes governs this surface too.
    pub storage_cache: Arc<crate::storage_class::StorageClassCache>,
    pub limits: Arc<SearchConcurrency>,
}

impl RecentEngine {
    /// Every `(root, start path)` the walk may begin from.
    ///
    /// A supplied scope is resolved and its error propagated: an unresolvable
    /// or unreadable scope is refused, never widened to "everything", because
    /// silently widening a scope is how a scoped endpoint becomes an unscoped
    /// read.
    fn roots_for(
        &self,
        user: UserId,
        scope: Option<&str>,
    ) -> Result<Vec<(Arc<ShareRoot>, SafePath)>, sc_core::CoreError> {
        if let Some(scope) = scope {
            let r = self.core.resolve(user, &Vpath::new(scope))?;
            return Ok(vec![(r.root, r.path.into_safe())]);
        }
        Ok(self
            .core
            .roots(user)
            .into_iter()
            .filter(|r| r.perms.contains(sc_acl::Perms::READ))
            .filter_map(|r| self.core.share(r.share).map(|root| (root, r.subpath)))
            .collect())
    }

    fn tier(&self, roots: &[(Arc<ShareRoot>, SafePath)]) -> SearchTier {
        sc_http::search_limits::fold_tier(
            roots
                .iter()
                .map(|(r, _)| self.storage_cache.get_or_detect(r)),
        )
    }
}

impl RecentApi for RecentEngine {
    fn recent_tier(&self, user: UserId, q: &RecentQuery) -> SearchTier {
        let roots = self.roots_for(user, q.scope.as_deref()).unwrap_or_default();
        self.tier(&roots)
    }

    fn recent(
        &self,
        user: UserId,
        q: &RecentQuery,
    ) -> Result<(Vec<RecentHit>, SearchCompleteness), sc_http::core_api::CoreError> {
        let roots = self
            .roots_for(user, q.scope.as_deref())
            .map_err(crate::bridge::http_err)?;
        if roots.is_empty() {
            return Ok((Vec::new(), SearchCompleteness::Full));
        }

        // The mtime bound makes `Matcher::needs_stat()` true, so the stat
        // phase runs and every hit carries a real `mtime_ns` and `size` --
        // which is the precondition `TopN` orders on.
        //
        // Hidden files are included, because `Matcher::new` defaults
        // `include_hidden` to true and neither existing search path overrides
        // it; excluding them here would make this the one surface in the
        // product that hides a dotfile the browse view shows.
        let matcher = sc_search::Matcher::match_all()
            .mtime_range(q.since_ns, i128::MAX)
            .kinds(sc_search::KindFilter { files: true, dirs: false });

        let tier = self.tier(&roots);
        let rotational = tier == SearchTier::Slow;
        // `max_results(u32::MAX)` is the point of the change: the walk must
        // see every candidate it can, because the newest file may be the last
        // one visited. It is still bounded, by the deadline and by
        // `max_entries`, which are the two limits that bound work rather than
        // results.
        let budget = sc_search::WalkBudget::new(self.limits.walk_deadline(tier))
            .max_results(u32::MAX)
            .max_depth(u16::MAX);
        let walker = sc_search::Walker::new(sc_search::Walker::decide_threads(rotational, None))
            .with_rotational(rotational);
        let acl = |share: ShareId, path: &SafePath| self.core.can_read(user, share, path);

        let (hits, completeness) =
            collect_newest(&walker, &roots, &matcher, &acl, &budget, q.limit);

        let rows = hits
            .into_iter()
            .filter_map(|h| {
                // A hit whose vpath cannot be built is dropped rather than
                // guessed at: a path outside every grant the caller holds
                // cannot produce one.
                let sp = SharePath::parse(&h.path, u16::MAX).ok()?;
                let vpath = self.core.vpath_for(user, h.share, &sp)?;
                let vpath = vpath.as_str().to_string();
                let share = vpath.split('/').next().unwrap_or_default().to_string();
                Some(RecentHit {
                    share,
                    name: h.name.to_string(),
                    size: h.size.unwrap_or(0),
                    mtime_ns: h.mtime_ns.unwrap_or(0),
                    vpath,
                })
            })
            .collect();

        Ok((rows, crate::bridge::to_search_completeness(completeness)))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    /// `n` files whose mtimes ascend with their index, so "the newest three"
    /// is a fact about the tree rather than about traversal order.
    fn tree(n: usize) -> (tempfile::TempDir, Vec<(Arc<ShareRoot>, SafePath)>) {
        let dir = tempfile::tempdir().unwrap();
        for i in 0..n {
            let p = dir.path().join(format!("f{i:03}.txt"));
            std::fs::write(&p, b"x").unwrap();
            let t = filetime::FileTime::from_unix_time(1_700_000_000 + i as i64, 0);
            filetime::set_file_mtime(&p, t).unwrap();
        }
        let root = Arc::new(
            sc_vfs::ShareRoot::open(ShareId::new(1), dir.path(), sc_vfs::SharePolicy::default())
                .unwrap(),
        );
        (dir, vec![(root, SafePath::root())])
    }

    /// The defect: `max_results` bounds a walk by stopping it, so an ordered,
    /// limited query used to end at the first `cap` matches the stat phase
    /// happened to reach, and only then sort those. On any tree with more
    /// matches than `cap`, the answer was wrong while reporting `Full`.
    #[test]
    fn collect_newest_returns_the_newest_not_the_first_found() {
        let (_dir, roots) = tree(40);
        let matcher = sc_search::Matcher::match_all()
            .mtime_range(i128::MIN, i128::MAX)
            .kinds(sc_search::KindFilter { files: true, dirs: false });
        let budget = sc_search::WalkBudget::new(Duration::from_secs(30))
            .max_results(u32::MAX)
            .max_depth(u16::MAX);
        let walker = sc_search::Walker::new(2);
        let acl = |_: ShareId, _: &SafePath| true;

        let (hits, completeness) = collect_newest(&walker, &roots, &matcher, &acl, &budget, 3);

        assert!(matches!(completeness, sc_search::Completeness::Full));
        let names: Vec<String> = hits.iter().map(|h| h.name.to_string()).collect();
        assert_eq!(names, vec!["f039.txt", "f038.txt", "f037.txt"]);
    }

    /// A limit larger than the tree is not an error, and the order still holds.
    #[test]
    fn collect_newest_handles_a_limit_above_the_match_count() {
        let (_dir, roots) = tree(3);
        let matcher = sc_search::Matcher::match_all()
            .mtime_range(i128::MIN, i128::MAX)
            .kinds(sc_search::KindFilter { files: true, dirs: false });
        let budget = sc_search::WalkBudget::new(Duration::from_secs(30))
            .max_results(u32::MAX)
            .max_depth(u16::MAX);
        let walker = sc_search::Walker::new(2);
        let acl = |_: ShareId, _: &SafePath| true;

        let (hits, _) = collect_newest(&walker, &roots, &matcher, &acl, &budget, 100);
        let names: Vec<String> = hits.iter().map(|h| h.name.to_string()).collect();
        assert_eq!(names, vec!["f002.txt", "f001.txt", "f000.txt"]);
    }
}
