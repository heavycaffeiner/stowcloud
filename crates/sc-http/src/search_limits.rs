//! Storage-class-aware resource bounds for search:
//!
//! | | NVMe / SATA SSD | Rotational / Network |
//! |---|---|---|
//! | global concurrent-search cap | 4 | 2 |
//! | T2 walk deadline | 3 s | 8 s |
//!
//! The design tabulates exactly two storage tiers. `StorageClass` is finer
//! (four values, matching `sc-search`'s own `decide_threads`) because a
//! share's filesystem can tell us that much, but the search limiter only
//! ever needs to know which of the two *tiers* a class falls into — see
//! [`StorageClass::tier`]. A per-storage-class Tokio blocking-pool resize
//! was considered and is **not implemented** — this four-way
//! classification is the only thing today that consumes `StorageClass`, and
//! it feeds only the search concurrency limiter below; the blocking pool
//! itself still runs with Tokio's own default size regardless of what's
//! mounted.
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use parking_lot::RwLock;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

/// Coarse storage classification for a share, detected from its filesystem
/// (`sc_vfs::ShareRoot::fstype()`) and, for local block devices, the
/// kernel's own rotational flag (`/sys/block/*/queue/rotational`). `sc-server` owns the actual detection
/// (it has the `ShareRoot`); this crate only needs the resulting class to
/// pick limits.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum StorageClass {
    Rotational,
    SataSsd,
    Nvme,
    /// NFS/CIFS/FUSE — latency is unpredictable and often high, so this
    /// gets the conservative tier rather than an optimistic one.
    Network,
}

/// The two-way split the limits below are actually tabulated for.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum SearchTier {
    Fast,
    Slow,
}

impl StorageClass {
    /// `SataSsd` is bucketed with `Nvme` (both flash, no seek penalty);
    /// `Network` is bucketed with `Rotational` (unpredictable/high latency
    /// deserves the conservative path — the same choice `sc-search`'s
    /// `decide_threads` makes for NFS/CIFS's thread count).
    pub fn tier(self) -> SearchTier {
        match self {
            StorageClass::Nvme | StorageClass::SataSsd => SearchTier::Fast,
            StorageClass::Rotational | StorageClass::Network => SearchTier::Slow,
        }
    }
}

/// Folds every storage class touched by one search into the single tier
/// that governs it: a search spanning shares of different classes takes the
/// most conservative one — a single HDD in the set is what bounds the walk. `Slow` wins over `Fast`
/// whenever both are present; an empty iterator (no readable roots at all)
/// defaults to `Fast` since there is nothing to be conservative *about*.
pub fn fold_tier(classes: impl IntoIterator<Item = StorageClass>) -> SearchTier {
    let mut tier = SearchTier::Fast;
    for c in classes {
        if c.tier() == SearchTier::Slow {
            tier = SearchTier::Slow;
        }
    }
    tier
}

/// Config-reachable defaults for the two tiers.
#[derive(Clone, Copy, Debug)]
pub struct SearchLimitsConfig {
    pub max_concurrent_fast: u32,
    pub max_concurrent_slow: u32,
    pub walk_deadline_fast: Duration,
    pub walk_deadline_slow: Duration,
}

impl Default for SearchLimitsConfig {
    fn default() -> Self {
        Self {
            max_concurrent_fast: 4,
            max_concurrent_slow: 2,
            walk_deadline_fast: Duration::from_secs(3),
            walk_deadline_slow: Duration::from_secs(8),
        }
    }
}

/// Global concurrent-search cap, split into one semaphore per tier so an
/// HDD-bound search and an NVMe-bound search don't compete for the same
/// budget — they aren't contending for the same disk's I/O. Each tier's
/// *own* cap still bounds it.
pub struct SearchConcurrency {
    fast: RwLock<Arc<Semaphore>>,
    slow: RwLock<Arc<Semaphore>>,
    /// Millis, atomic so [`Self::reconfigure`] can change these live (admin
    /// settings screen) without a lock around every `walk_deadline` read.
    walk_deadline_fast_ms: AtomicU64,
    walk_deadline_slow_ms: AtomicU64,
}

impl SearchConcurrency {
    pub fn new(cfg: &SearchLimitsConfig) -> Self {
        Self {
            fast: RwLock::new(Arc::new(Semaphore::new(cfg.max_concurrent_fast.max(1) as usize))),
            slow: RwLock::new(Arc::new(Semaphore::new(cfg.max_concurrent_slow.max(1) as usize))),
            walk_deadline_fast_ms: AtomicU64::new(cfg.walk_deadline_fast.as_millis() as u64),
            walk_deadline_slow_ms: AtomicU64::new(cfg.walk_deadline_slow.as_millis() as u64),
        }
    }

    /// `try_acquire` (not the blocking/`.await` form) is the right shape
    /// here: an exhausted budget should reject with `429 Retry-After`
    /// immediately, never make the caller wait on the server side. Excess
    /// requests do get queued — by the client, on its own `Retry-After`,
    /// not by us holding a connection open.
    pub fn try_acquire(&self, tier: SearchTier) -> Option<OwnedSemaphorePermit> {
        let sem = match tier {
            SearchTier::Fast => self.fast.read().clone(),
            SearchTier::Slow => self.slow.read().clone(),
        };
        sem.try_acquire_owned().ok()
    }

    pub fn walk_deadline(&self, tier: SearchTier) -> Duration {
        let ms = match tier {
            SearchTier::Fast => self.walk_deadline_fast_ms.load(Ordering::Relaxed),
            SearchTier::Slow => self.walk_deadline_slow_ms.load(Ordering::Relaxed),
        };
        Duration::from_millis(ms)
    }

    /// Live-reconfigure (admin settings screen, no restart). Each tier's
    /// semaphore is replaced outright rather than grown/shrunk in place — a
    /// search already holding a permit from the old semaphore keeps running
    /// against it unaffected (`try_acquire`d permits aren't invalidated), so
    /// the only transient effect is that the true concurrent count briefly
    /// may exceed the new cap by however many searches were already in
    /// flight, until they finish (bounded by the walk deadline, seconds).
    /// New `try_acquire` calls see the new cap immediately.
    pub fn reconfigure(&self, cfg: &SearchLimitsConfig) {
        *self.fast.write() = Arc::new(Semaphore::new(cfg.max_concurrent_fast.max(1) as usize));
        *self.slow.write() = Arc::new(Semaphore::new(cfg.max_concurrent_slow.max(1) as usize));
        self.walk_deadline_fast_ms
            .store(cfg.walk_deadline_fast.as_millis() as u64, Ordering::Relaxed);
        self.walk_deadline_slow_ms
            .store(cfg.walk_deadline_slow.as_millis() as u64, Ordering::Relaxed);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fold_tier_is_conservative() {
        assert_eq!(fold_tier([StorageClass::Nvme, StorageClass::SataSsd]), SearchTier::Fast);
        assert_eq!(fold_tier([StorageClass::Nvme, StorageClass::Rotational]), SearchTier::Slow);
        assert_eq!(fold_tier([StorageClass::Network]), SearchTier::Slow);
        assert_eq!(fold_tier(std::iter::empty()), SearchTier::Fast);
    }

    #[test]
    fn each_tier_has_its_own_budget() {
        let cfg = SearchLimitsConfig { max_concurrent_fast: 2, max_concurrent_slow: 1, ..Default::default() };
        let sc = SearchConcurrency::new(&cfg);

        let slow1 = sc.try_acquire(SearchTier::Slow).expect("first slow permit");
        assert!(sc.try_acquire(SearchTier::Slow).is_none(), "slow budget is exhausted at 1");
        // The fast budget is untouched by slow-tier contention.
        let _fast1 = sc.try_acquire(SearchTier::Fast).expect("fast budget independent of slow");
        let _fast2 = sc.try_acquire(SearchTier::Fast).expect("fast budget is 2");
        assert!(sc.try_acquire(SearchTier::Fast).is_none(), "fast budget is exhausted at 2");

        drop(slow1);
        let _slow2 = sc.try_acquire(SearchTier::Slow).expect("slow permit freed after drop");
    }

    #[test]
    fn deadlines_match_design_defaults() {
        let sc = SearchConcurrency::new(&SearchLimitsConfig::default());
        assert_eq!(sc.walk_deadline(SearchTier::Fast), Duration::from_secs(3));
        assert_eq!(sc.walk_deadline(SearchTier::Slow), Duration::from_secs(8));
    }

    #[test]
    fn reconfigure_takes_effect_without_restart() {
        let sc = SearchConcurrency::new(&SearchLimitsConfig::default());
        sc.reconfigure(&SearchLimitsConfig {
            max_concurrent_fast: 1,
            max_concurrent_slow: 1,
            walk_deadline_fast: Duration::from_secs(1),
            walk_deadline_slow: Duration::from_secs(2),
        });
        assert_eq!(sc.walk_deadline(SearchTier::Fast), Duration::from_secs(1));
        assert_eq!(sc.walk_deadline(SearchTier::Slow), Duration::from_secs(2));
        let _p = sc.try_acquire(SearchTier::Fast).expect("new cap grants a permit");
        assert!(
            sc.try_acquire(SearchTier::Fast).is_none(),
            "new cap of 1 is exhausted after one permit"
        );
    }
}
