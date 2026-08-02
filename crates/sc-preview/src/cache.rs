//! In-memory LRU + negative cache + single-flight (section 6).
//!
//! This module implements the *policy* described in the design doc's
//! `preview_cache` / `preview_negative` tables using an in-memory index
//! rather than SQLite, per the task's explicit priority ("prioritize
//! compiling + the required tests over the on-disk SQLite persistence").
//! A best-effort on-disk byte cache sits alongside the in-memory LRU (see
//! `DiskCache` below) so a real deployment gets *some* persistence across
//! restarts, but the source of truth for hit/miss/eviction decisions here
//! is the in-memory structures.
//!
//! Single-flight is implemented with the classic "shared slot" pattern：a
//! `DashMap<Key, Arc<tokio::sync::Mutex<Option<CachedResult>>>>`. Every
//! caller for the same key gets the same `Arc`; whichever caller is first
//! to observe `None` inside the mutex is the one that actually runs the
//! generator, and every other caller blocks on the same mutex and then
//! reads the `Some(..)` the winner left behind -- so the generator body
//! runs at most once per key, no matter how many callers arrive
//! concurrently.

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use bytes::Bytes;
use dashmap::DashMap;
use sc_vfs::ids::FileId;
use parking_lot::Mutex;

use crate::error::{NegativeReason, PreviewError};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Key {
    pub fileid: FileId,
    pub w: u32,
    pub h: u32,
    pub etag8: [u8; 8],
}

/// What a single-flight slot ends up holding once its generation completes.
/// Uses [`NegativeReason`] rather than [`PreviewError`] on the error side
/// specifically so it can be `Clone`d out to every waiter sharing the slot.
type SlotResult = Result<Bytes, NegativeReason>;

struct NegativeEntry {
    reason: NegativeReason,
    until: Instant,
}

/// LRU cache bounded by an approximate total-byte budget rather than entry
/// count -- preview blobs vary a lot in size, so counting entries wouldn't
/// actually bound memory use the way a byte cap does.
struct BoundedByteCache {
    lru: lru::LruCache<Key, Bytes>,
    used_bytes: u64,
    cap_bytes: u64,
}

impl BoundedByteCache {
    fn new(cap_bytes: u64) -> Self {
        Self {
            // Entry-count bound is nominal/unbounded-ish; the real bound is
            // enforced by `used_bytes`/`cap_bytes` in `put`.
            lru: lru::LruCache::unbounded(),
            used_bytes: 0,
            cap_bytes,
        }
    }

    fn get(&mut self, key: &Key) -> Option<Bytes> {
        self.lru.get(key).cloned()
    }

    fn put(&mut self, key: Key, bytes: Bytes) {
        let len = bytes.len() as u64;
        if let Some(old) = self.lru.put(key, bytes) {
            self.used_bytes = self.used_bytes.saturating_sub(old.len() as u64);
        }
        self.used_bytes = self.used_bytes.saturating_add(len);
        while self.used_bytes > self.cap_bytes {
            match self.lru.pop_lru() {
                Some((_, evicted)) => {
                    self.used_bytes = self.used_bytes.saturating_sub(evicted.len() as u64);
                }
                None => break,
            }
        }
    }

    fn len(&self) -> usize {
        self.lru.len()
    }
}

/// Best-effort on-disk mirror of generated previews, laid out per
/// section 6: `<dir>/<fid % 256>/<fid>-<w>x<h>-<etag8>.webp`.
/// Failures here (disk full, permission denied, ...) are logged and
/// swallowed -- the in-memory cache is authoritative, disk is purely an
/// optimization for surviving process restarts.
pub struct DiskCache {
    dir: PathBuf,
}

impl DiskCache {
    pub fn new(dir: PathBuf) -> Self {
        Self { dir }
    }

    fn path_for(&self, key: &Key) -> PathBuf {
        let shard = (key.fileid.get().rem_euclid(256)) as u16;
        let etag_hex = data_encoding_hex(&key.etag8);
        self.dir.join(shard.to_string()).join(format!(
            "{}-{}x{}-{}.webp",
            key.fileid.get(),
            key.w,
            key.h,
            etag_hex
        ))
    }

    pub fn get(&self, key: &Key) -> Option<Bytes> {
        std::fs::read(self.path_for(key)).ok().map(Bytes::from)
    }

    pub fn put(&self, key: &Key, bytes: &Bytes) {
        let path = self.path_for(key);
        if let Some(parent) = path.parent() {
            if let Err(e) = std::fs::create_dir_all(parent) {
                tracing::warn!(error = %e, "preview disk cache: failed to create shard dir");
                return;
            }
        }
        if let Err(e) = std::fs::write(&path, bytes) {
            tracing::warn!(error = %e, path = %path.display(), "preview disk cache: write failed");
        }
    }
}

fn data_encoding_hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{b:02x}"));
    }
    s
}

pub struct CacheConfig {
    pub mem_cap_bytes: u64,
    pub negative_ttl: Duration,
    pub disk_dir: Option<PathBuf>,
}

impl Default for CacheConfig {
    fn default() -> Self {
        Self {
            mem_cap_bytes: 2 * 1024 * 1024 * 1024, // 2 GiB, section 6 default
            negative_ttl: Duration::from_secs(7 * 24 * 3600), // 7 days
            disk_dir: None,
        }
    }
}

pub struct PreviewCache {
    mem: Mutex<BoundedByteCache>,
    negative: DashMap<(FileId, [u8; 8]), NegativeEntry>,
    inflight: DashMap<Key, std::sync::Arc<tokio::sync::Mutex<Option<SlotResult>>>>,
    disk: Option<DiskCache>,
    negative_ttl: Duration,
    /// Test/observability hook: incremented once per actual call into the
    /// generator closure, across all keys. Not part of the cache's
    /// steady-state logic.
    generation_calls: AtomicU64,
}

impl PreviewCache {
    pub fn new(cfg: CacheConfig) -> Self {
        Self {
            mem: Mutex::new(BoundedByteCache::new(cfg.mem_cap_bytes)),
            negative: DashMap::new(),
            inflight: DashMap::new(),
            disk: cfg.disk_dir.map(DiskCache::new),
            negative_ttl: cfg.negative_ttl,
            generation_calls: AtomicU64::new(0),
        }
    }

    pub fn generation_call_count(&self) -> u64 {
        self.generation_calls.load(Ordering::SeqCst)
    }

    fn mem_get(&self, key: &Key) -> Option<Bytes> {
        self.mem.lock().get(key)
    }

    fn mem_put(&self, key: Key, bytes: Bytes) {
        self.mem.lock().put(key, bytes);
    }

    pub fn mem_len(&self) -> usize {
        self.mem.lock().len()
    }

    fn negative_get(&self, fileid: FileId, etag8: [u8; 8]) -> Option<NegativeReason> {
        let entry = self.negative.get(&(fileid, etag8))?;
        if Instant::now() < entry.until {
            Some(entry.reason)
        } else {
            None
        }
    }

    fn negative_put(&self, fileid: FileId, etag8: [u8; 8], reason: NegativeReason) {
        self.negative.insert(
            (fileid, etag8),
            NegativeEntry {
                reason,
                until: Instant::now() + self.negative_ttl,
            },
        );
    }

    /// Look up `key` in the memory cache, then the disk cache (promoting a
    /// disk hit back into memory), then the negative cache. Returns `None`
    /// if none of the three have an answer -- generation is required.
    fn lookup_positive_or_negative(&self, key: &Key) -> Option<Result<Bytes, PreviewError>> {
        if let Some(bytes) = self.mem_get(key) {
            return Some(Ok(bytes));
        }
        if let Some(disk) = &self.disk {
            if let Some(bytes) = disk.get(key) {
                self.mem_put(*key, bytes.clone());
                return Some(Ok(bytes));
            }
        }
        if let Some(reason) = self.negative_get(key.fileid, key.etag8) {
            return Some(Err(PreviewError::NegativeCached(reason)));
        }
        None
    }

    /// Single-flight-coordinated get-or-generate. `generate` is called at
    /// most once per key among all concurrent callers; its result is
    /// cached (positively or negatively) for everyone.
    pub async fn get_or_generate<F, Fut>(&self, key: Key, generate: F) -> Result<Bytes, PreviewError>
    where
        F: FnOnce() -> Fut,
        Fut: std::future::Future<Output = Result<Bytes, PreviewError>>,
    {
        if let Some(hit) = self.lookup_positive_or_negative(&key) {
            return hit;
        }

        let slot = self
            .inflight
            .entry(key)
            .or_insert_with(|| std::sync::Arc::new(tokio::sync::Mutex::new(None)))
            .clone();

        let mut guard = slot.lock().await;
        if guard.is_none() {
            // We are the single caller that actually runs the generator for
            // this key -- everyone else is parked on `slot.lock().await`
            // above/below us.
            self.generation_calls.fetch_add(1, Ordering::SeqCst);
            let result = generate().await;

            let stored: SlotResult = match &result {
                Ok(bytes) => {
                    self.mem_put(key, bytes.clone());
                    if let Some(disk) = &self.disk {
                        disk.put(&key, bytes);
                    }
                    Ok(bytes.clone())
                }
                Err(e) => {
                    let reason = e.classify();
                    self.negative_put(key.fileid, key.etag8, reason);
                    Err(reason)
                }
            };
            *guard = Some(stored);
            drop(guard);
            self.inflight.remove(&key);
            return result;
        }

        // Someone else already ran (or is running -- but we hold the lock
        // now, so they're done) the generator for this key. `guard` is
        // guaranteed `Some` here.
        let cached = guard.clone().expect("slot must be populated once unlocked with Some");
        drop(guard);
        match cached {
            Ok(bytes) => Ok(bytes),
            Err(reason) => Err(PreviewError::NegativeCached(reason)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicUsize;
    use std::sync::Arc;

    fn test_key(w: u32) -> Key {
        Key {
            fileid: FileId::new(42),
            w,
            h: w,
            etag8: [1, 2, 3, 4, 5, 6, 7, 8],
        }
    }

    #[tokio::test]
    async fn single_flight_collapses_concurrent_identical_requests() {
        let cache = Arc::new(PreviewCache::new(CacheConfig::default()));
        let counter = Arc::new(AtomicUsize::new(0));
        let key = test_key(128);

        let mut handles = Vec::new();
        for _ in 0..16 {
            let cache = cache.clone();
            let counter = counter.clone();
            handles.push(tokio::spawn(async move {
                cache
                    .get_or_generate(key, || async move {
                        counter.fetch_add(1, Ordering::SeqCst);
                        tokio::time::sleep(Duration::from_millis(50)).await;
                        Ok(Bytes::from_static(b"generated"))
                    })
                    .await
            }));
        }

        for h in handles {
            let result = h.await.unwrap();
            assert_eq!(result.unwrap(), Bytes::from_static(b"generated"));
        }

        assert_eq!(counter.load(Ordering::SeqCst), 1, "generator must run exactly once");
        assert_eq!(cache.generation_call_count(), 1);
    }

    #[tokio::test]
    async fn negative_cache_prevents_repeat_work_within_ttl() {
        let cfg = CacheConfig {
            negative_ttl: Duration::from_secs(3600),
            ..CacheConfig::default()
        };
        let cache = PreviewCache::new(cfg);
        let counter = Arc::new(AtomicUsize::new(0));
        let key = test_key(256);

        let first = cache
            .get_or_generate(key, || {
                let counter = counter.clone();
                async move {
                    counter.fetch_add(1, Ordering::SeqCst);
                    Err(PreviewError::UnsupportedFormat)
                }
            })
            .await;
        assert!(first.is_err());
        assert_eq!(counter.load(Ordering::SeqCst), 1);

        // Second call, same key, well within the TTL: must not invoke the
        // generator again, and must come back as a NegativeCached error.
        let second = cache
            .get_or_generate(key, || {
                let counter = counter.clone();
                async move {
                    counter.fetch_add(1, Ordering::SeqCst);
                    Err(PreviewError::UnsupportedFormat)
                }
            })
            .await;
        assert!(matches!(second, Err(PreviewError::NegativeCached(_))));
        assert_eq!(counter.load(Ordering::SeqCst), 1, "must not retry within TTL");
    }

    #[tokio::test]
    async fn negative_cache_expires_after_ttl() {
        let cfg = CacheConfig {
            negative_ttl: Duration::from_millis(20),
            ..CacheConfig::default()
        };
        let cache = PreviewCache::new(cfg);
        let counter = Arc::new(AtomicUsize::new(0));
        let key = test_key(64);

        let _ = cache
            .get_or_generate(key, || {
                let counter = counter.clone();
                async move {
                    counter.fetch_add(1, Ordering::SeqCst);
                    Err(PreviewError::UnsupportedFormat)
                }
            })
            .await;
        assert_eq!(counter.load(Ordering::SeqCst), 1);

        tokio::time::sleep(Duration::from_millis(60)).await;

        let _ = cache
            .get_or_generate(key, || {
                let counter = counter.clone();
                async move {
                    counter.fetch_add(1, Ordering::SeqCst);
                    Err(PreviewError::UnsupportedFormat)
                }
            })
            .await;
        assert_eq!(counter.load(Ordering::SeqCst), 2, "must retry after TTL expiry");
    }

    #[tokio::test]
    async fn positive_result_is_served_from_memory_on_next_call_without_regenerating() {
        let cache = PreviewCache::new(CacheConfig::default());
        let counter = Arc::new(AtomicUsize::new(0));
        let key = test_key(512);

        for _ in 0..5 {
            let counter = counter.clone();
            let result = cache
                .get_or_generate(key, || async move {
                    counter.fetch_add(1, Ordering::SeqCst);
                    Ok(Bytes::from_static(b"data"))
                })
                .await;
            assert_eq!(result.unwrap(), Bytes::from_static(b"data"));
        }

        assert_eq!(counter.load(Ordering::SeqCst), 1);
        assert_eq!(cache.mem_len(), 1);
    }

    #[test]
    fn bounded_byte_cache_evicts_lru_when_over_capacity() {
        let mut c = BoundedByteCache::new(10);
        let k1 = test_key(1);
        let k2 = test_key(2);
        let k3 = test_key(3);
        c.put(k1, Bytes::from_static(b"aaaaa")); // 5 bytes
        c.put(k2, Bytes::from_static(b"bbbbb")); // 5 bytes, total 10, at cap
        assert!(c.get(&k1).is_some());
        c.put(k3, Bytes::from_static(b"ccccc")); // pushes over cap, evicts LRU
        // k1 was just touched by `get`, so k2 (least recently used) should
        // be the one evicted, not k1.
        assert!(c.get(&k2).is_none(), "least recently used entry should be evicted");
        assert!(c.get(&k1).is_some());
        assert!(c.get(&k3).is_some());
    }
}
