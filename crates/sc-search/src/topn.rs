//! Bounded top-N collection for an ordered, limited walk.
//!
//! `WalkBudget::max_results` bounds a walk by *stopping* it, which is the
//! right answer for an unordered query and the wrong one for an ordered one:
//! the first `N` matches the stat phase happens to reach are inode order on
//! rotational storage and readdir order otherwise, and neither has anything
//! to do with mtime. Keeping the newest `N` of everything the walk reaches is
//! what makes "the newest N" mean what it says.

use std::cmp::Reverse;
use std::collections::BinaryHeap;

use crate::walker::Hit;

/// A hit in the heap, ordered so that the heap's *minimum* is the one to evict
/// first: oldest `mtime_ns`, and among equal timestamps the path that sorts
/// last.
///
/// The path tie-break is not cosmetic. Two files written in the same
/// nanosecond, or two files on a filesystem that only records whole seconds,
/// would otherwise order differently between two identical requests and the
/// list would visibly reshuffle on refresh. `(mtime_ns descending, path
/// ascending)` is a total order, so repeated requests over an unchanged tree
/// return the identical sequence.
struct Ranked {
    mtime_ns: i128,
    hit: Hit,
}

impl PartialEq for Ranked {
    fn eq(&self, other: &Self) -> bool {
        self.cmp(other) == std::cmp::Ordering::Equal
    }
}
impl Eq for Ranked {}

impl PartialOrd for Ranked {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for Ranked {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        // Newer is greater; among equal timestamps, the earlier path is
        // greater, so that the heap's minimum (what `Reverse` puts on top) is
        // the row a newer hit should displace.
        self.mtime_ns
            .cmp(&other.mtime_ns)
            .then_with(|| other.hit.path.cmp(&self.hit.path))
    }
}

/// The newest `cap` hits seen, by `(mtime_ns descending, path ascending)`.
///
/// Holds at most `cap` hits regardless of how many are offered, which is what
/// lets a recency walk run to its deadline instead of stopping at the first
/// `cap` matches it happens to find.
pub struct TopN {
    cap: usize,
    heap: BinaryHeap<Reverse<Ranked>>,
}

impl TopN {
    pub fn new(cap: usize) -> Self {
        Self {
            cap,
            heap: BinaryHeap::with_capacity(cap.min(4096)),
        }
    }

    /// Keep `hit` if it is newer than the oldest kept hit, or if there is
    /// room. A hit with no `mtime_ns` is dropped: it cannot be placed in the
    /// order, and every caller is responsible for making the walk stat
    /// (`Matcher::needs_stat`) so this never silently empties a result.
    pub fn offer(&mut self, hit: Hit) {
        if self.cap == 0 {
            return;
        }
        let Some(mtime_ns) = hit.mtime_ns else {
            return;
        };
        let candidate = Ranked { mtime_ns, hit };
        if self.heap.len() < self.cap {
            self.heap.push(Reverse(candidate));
            return;
        }
        // `peek` is the oldest kept hit. Strictly newer only: an equal
        // candidate would churn the heap without changing the answer.
        if let Some(Reverse(oldest)) = self.heap.peek() {
            if candidate > *oldest {
                self.heap.pop();
                self.heap.push(Reverse(candidate));
            }
        }
    }

    /// Newest first. Consumes the collector.
    pub fn into_sorted_vec(self) -> Vec<Hit> {
        // Ascending in `Reverse<Ranked>` is descending in `Ranked`, and
        // `Ranked` ranks newer as greater, so this is already newest first.
        self.heap
            .into_sorted_vec()
            .into_iter()
            .map(|r| r.0.hit)
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use compact_str::CompactString;

    fn hit(path: &str, mtime_ns: Option<i128>) -> Hit {
        Hit {
            share: crate::vfs::ShareId::new(1),
            path: path.to_string(),
            name: CompactString::new(path.rsplit('/').next().unwrap_or(path)),
            is_dir: false,
            size: Some(0),
            mtime_ns,
            score: 0.0,
        }
    }

    #[test]
    fn capacity_is_never_exceeded() {
        let mut top = TopN::new(3);
        for i in 0..100 {
            top.offer(hit(&format!("f{i}"), Some(i)));
        }
        assert_eq!(top.into_sorted_vec().len(), 3);
    }

    /// The whole point: the newest survive whatever order they arrive in.
    #[test]
    fn the_newest_survive_a_shuffled_input() {
        let mut top = TopN::new(3);
        for i in [5i128, 1, 9, 3, 7, 2, 8] {
            top.offer(hit(&format!("f{i}"), Some(i)));
        }
        let paths: Vec<String> = top.into_sorted_vec().into_iter().map(|h| h.path).collect();
        assert_eq!(paths, vec!["f9", "f8", "f7"]);
    }

    /// Without this a refresh visibly reshuffles a list nothing changed in.
    #[test]
    fn equal_timestamps_order_by_path() {
        let mut top = TopN::new(3);
        for p in ["c", "a", "b"] {
            top.offer(hit(p, Some(42)));
        }
        let paths: Vec<String> = top.into_sorted_vec().into_iter().map(|h| h.path).collect();
        assert_eq!(paths, vec!["a", "b", "c"]);
    }

    /// A tie at the eviction boundary must not depend on arrival order
    /// either: with room for two and three hits at the same instant, the two
    /// lowest paths win.
    #[test]
    fn a_tie_at_the_boundary_still_orders_by_path() {
        let mut top = TopN::new(2);
        for p in ["c", "a", "b"] {
            top.offer(hit(p, Some(42)));
        }
        let paths: Vec<String> = top.into_sorted_vec().into_iter().map(|h| h.path).collect();
        assert_eq!(paths, vec!["a", "b"]);
    }

    #[test]
    fn a_hit_with_no_mtime_is_dropped() {
        let mut top = TopN::new(3);
        top.offer(hit("unstat", None));
        top.offer(hit("stat", Some(1)));
        let paths: Vec<String> = top.into_sorted_vec().into_iter().map(|h| h.path).collect();
        assert_eq!(paths, vec!["stat"]);
    }
}
