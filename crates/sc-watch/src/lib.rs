//! `sc-watch` — change detection: hot-set watching + debounced invalidation
//! events.,
//!
//! Only the directories that can actually be *observed* right now are
//! watched: WebSocket-subscribed dirs + their ancestor chain, plus an LRU of
//! recently-accessed dirs, capped at `hot_set_max`. Nothing here is the sole
//! source of truth — every read path in `sc-core` re-stats before trusting
//! anything, so a watcher that's degraded, capped, or entirely dead only
//! costs freshness of the *pushed* UI update, never correctness.

mod backend;
mod hotset;

#[cfg(test)]
mod tests;

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Arc;

use crossbeam_channel::Sender;
use dashmap::DashMap;
use sc_core::Core;
use sc_vfs::{SafePath, ShareId};
use parking_lot::Mutex;

use backend::{OverflowFlag, PendingMap, RawBackend, ReversePathMap, WatchAddError, DEBOUNCE};
use hotset::HotSet;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum WatchBackend {
    /// Only hot-set dirs, via the portable `notify` transport. The default,
    /// and the only backend exercised in this workspace's test suite.
    HotSet,
    /// Linux only: same hot-set registration strategy, but the raw
    /// `inotify` transport instead of `notify`. Falls back to `HotSet` on
    /// every other target.
    InotifyFull,
    /// Linux only, and — in this build — an alias for `InotifyFull`: real
    /// `FAN_MARK_FILESYSTEM` needs `CAP_SYS_ADMIN` plus fanotify syscalls
    /// this crate doesn't wrap. Accepted rather than rejected so config
    /// files written against the full design don't fail to parse; a
    /// warning is logged at `start()`.
    Fanotify,
    /// Explicit portable `notify` transport (same as `HotSet` today; kept
    /// as a distinct name because the two ideas — "which dirs" vs. "which
    /// transport" — are conflated in's config schema).
    Portable,
}

#[derive(Clone, Debug)]
pub struct WatchConfig {
    pub backend: WatchBackend,
    pub hot_set_max: usize,
    pub full_threshold: u64,
}

impl Default for WatchConfig {
    fn default() -> Self {
        Self {
            backend: WatchBackend::HotSet,
            hot_set_max: 4096,
            full_threshold: 50_000,
        }
    }
}

#[derive(Clone, Debug)]
pub struct InvalEvent {
    pub share: ShareId,
    pub path: String,
    pub etag: Option<String>,
}

#[derive(Clone, Debug)]
pub struct WatchStats {
    pub registered: usize,
    pub degraded_subtrees: usize,
    pub backend: WatchBackend,
}

struct Inner {
    cfg: WatchConfig,
    core: Arc<Core>,
    sink: Sender<InvalEvent>,
    backend: Mutex<Box<dyn RawBackend>>,
    state: Mutex<HotSet>,
    reverse: ReversePathMap,
    pending: PendingMap,
    degraded: AtomicUsize,
    stop: std::sync::atomic::AtomicBool,
    /// Set when the OS reports its own event queue overflowed;
    /// consumed and cleared by `debounce_loop`.
    overflow: OverflowFlag,
}

pub struct Watcher {
    inner: Arc<Inner>,
    _debounce_thread: std::thread::JoinHandle<()>,
    _rescan_thread: std::thread::JoinHandle<()>,
}

/// Reconstruct a real host path for `path`. `sc-vfs` deliberately never
/// exposes one from `ShareRoot` itself, but the watcher is the one
/// sanctioned exception — it must hand a real path to the OS watch API
/// (`inotify`/`ReadDirectoryChangesW`) — so it goes through `sc-core`'s
/// `share_host_path` escape hatch instead of reaching into `sc-vfs`.
fn host_path_for(core: &Core, share: ShareId, path: &SafePath) -> Option<PathBuf> {
    let mut pb = core.share_host_path(share)?;
    for c in path.components() {
        pb.push(c.as_str());
    }
    Some(pb)
}

impl Watcher {
    pub fn start(cfg: WatchConfig, core: Arc<Core>, sink: Sender<InvalEvent>) -> anyhow::Result<Self> {
        if cfg.backend == WatchBackend::Fanotify {
            tracing::warn!("fanotify backend requested but not implemented; falling back to raw inotify (Linux) / notify (portable)");
        }

        let pending: PendingMap = Arc::new(DashMap::new());
        let reverse: ReversePathMap = Arc::new(DashMap::new());
        let overflow: OverflowFlag = Arc::new(AtomicBool::new(false));

        let backend: Box<dyn RawBackend> = new_backend(cfg.backend, pending.clone(), reverse.clone(), overflow.clone())?;

        let inner = Arc::new(Inner {
            cfg,
            core,
            sink,
            backend: Mutex::new(backend),
            state: Mutex::new(HotSet::new(4096)),
            reverse,
            pending,
            degraded: AtomicUsize::new(0),
            stop: std::sync::atomic::AtomicBool::new(false),
            overflow,
        });
        // hot_set_max is only known after `cfg` moved into `inner`; rebuild
        // the state with the real cap.
        *inner.state.lock() = HotSet::new(inner.cfg.hot_set_max.max(1));

        let debounce_inner = inner.clone();
        let handle = std::thread::spawn(move || debounce_loop(debounce_inner));

        let rescan_inner = inner.clone();
        let rescan_handle = std::thread::spawn(move || rescan_loop(rescan_inner));

        Ok(Self { inner, _debounce_thread: handle, _rescan_thread: rescan_handle })
    }

    /// Register a watch on `path` *before* returning, so the caller can
    /// safely read the directory afterward without missing a change that
    /// lands in between. Registration failure
    /// (`ENOSPC`-equivalent) degrades that subtree to lazy rather than
    /// erroring — the caller's read still proceeds correctly, just without
    /// a live push (`lazy revalidation` in every read path is the backstop).
    pub fn subscribe(&self, share: ShareId, path: &SafePath) -> anyhow::Result<()> {
        for key in self.ancestor_keys(share, path) {
            self.register_sticky(key);
        }
        Ok(())
    }

    pub fn unsubscribe(&self, share: ShareId, path: &SafePath) {
        for key in self.ancestor_keys(share, path) {
            self.inner.state.lock().remove_sticky(&key);
        }
    }

    pub fn touch(&self, share: ShareId, path: &SafePath) {
        let key = (share, path.to_display_string());
        let is_new = self.inner.state.lock().touch(key.clone());
        if is_new {
            self.try_register(key);
        }
    }

    pub fn stats(&self) -> WatchStats {
        WatchStats {
            registered: self.inner.state.lock().registered_count(),
            degraded_subtrees: self.inner.degraded.load(Ordering::Relaxed),
            backend: self.inner.cfg.backend,
        }
    }

    fn ancestor_keys(&self, share: ShareId, path: &SafePath) -> Vec<(ShareId, String)> {
        let mut out = vec![(share, SafePath::root().to_display_string())];
        let mut cur = SafePath::root();
        let max_depth = u16::MAX;
        for comp in path.components() {
            cur = match cur.join(comp.as_str(), max_depth) {
                Ok(p) => p,
                Err(_) => break,
            };
            out.push((share, cur.to_display_string()));
        }
        out
    }

    fn register_sticky(&self, key: (ShareId, String)) {
        let is_new = self.inner.state.lock().add_sticky(key.clone());
        if is_new {
            self.try_register(key);
        }
    }

    fn try_register(&self, key: (ShareId, String)) {
        let Ok(path) = SafePath::parse(&key.1, u16::MAX) else { return };
        let Some(host) = host_path_for(&self.inner.core, key.0, &path) else {
            // No host-path accessor available in this build — treat as an
            // immediate, permanent degradation for this key rather than
            // silently pretending to watch it.
            self.inner.degraded.fetch_add(1, Ordering::Relaxed);
            return;
        };

        let evicted = self.inner.state.lock().keys_to_evict_for_one_more();
        for ev_key in &evicted {
            if let Ok(ev_path) = SafePath::parse(&ev_key.1, u16::MAX) {
                if let Some(ev_host) = host_path_for(&self.inner.core, ev_key.0, &ev_path) {
                    self.inner.backend.lock().remove_watch(ev_key, &ev_host);
                    self.inner.reverse.remove(&ev_host);
                }
            }
            self.inner.state.lock().mark_unregistered(ev_key);
        }

        match self.inner.backend.lock().add_watch(key.clone(), &host) {
            Ok(()) => {
                self.inner.reverse.insert(host, key.clone());
                self.inner.state.lock().mark_registered(key);
            }
            Err(WatchAddError::NoSpace) => {
                self.inner.degraded.fetch_add(1, Ordering::Relaxed);
                tracing::warn!(?key, "watch registration hit a resource limit; subtree degraded to lazy revalidation");
            }
            Err(WatchAddError::Other(e)) => {
                self.inner.degraded.fetch_add(1, Ordering::Relaxed);
                tracing::warn!(?key, error = %e, "watch registration failed; subtree degraded to lazy revalidation");
            }
        }
    }
}

impl Drop for Watcher {
    fn drop(&mut self) {
        self.inner.stop.store(true, Ordering::Relaxed);
    }
}

fn new_backend(
    backend: WatchBackend,
    pending: PendingMap,
    reverse: ReversePathMap,
    overflow: OverflowFlag,
) -> anyhow::Result<Box<dyn RawBackend>> {
    #[cfg(target_os = "linux")]
    {
        if matches!(backend, WatchBackend::InotifyFull | WatchBackend::Fanotify) {
            return Ok(Box::new(self::backend::linux::LinuxInotifyBackend::new(pending, reverse, overflow)?));
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = backend;
    }
    Ok(Box::new(self::backend::portable::PortableBackend::new(pending, reverse, overflow)?))
}

/// Every ~50ms, flush any pending key that has sat for `DEBOUNCE` (200ms)
/// since its *first* event — so a burst that finishes inside the window
/// coalesces into exactly one `InvalEvent`, and every flush marks the
/// ancestor chain dirty in `sc-meta`.
///
/// Before that: two independent signals that individual dirty paths can no
/// longer be trusted, or are too many to keep enumerating one at a time
/// Either fires the same O(1) fallback — bump every
/// share's generation instead of flushing per-directory:
/// - the OS reported its own event queue overflowed (`overflow`, set by a
///   backend on `IN_Q_OVERFLOW`/`notify::Flag::Rescan` — events were lost,
///   so the pending set itself is now incomplete and cannot be trusted); or
/// - the pending-dirty set has grown past `full_threshold` — this crate's
///   own queue, bounded in practice by `hot_set_max` (the only directories
///   that can ever produce an event), so it only fires for a deployment
///   that raised `hot_set_max` past the default `full_threshold` for a
///   large tree, exactly the case `full_threshold` exists for.
fn debounce_loop(inner: Arc<Inner>) {
    while !inner.stop.load(Ordering::Relaxed) {
        std::thread::sleep(std::time::Duration::from_millis(50));

        let kernel_overflow = inner.overflow.swap(false, Ordering::AcqRel);
        let pending_len = inner.pending.len() as u64;
        if kernel_overflow || pending_len > inner.cfg.full_threshold {
            full_invalidate(&inner, kernel_overflow, pending_len);
            continue;
        }

        let now = std::time::Instant::now();
        let ready: Vec<(ShareId, String)> = inner
            .pending
            .iter()
            .filter(|e| now.duration_since(*e.value()) >= DEBOUNCE)
            .map(|e| e.key().clone())
            .collect();

        for key in ready {
            inner.pending.remove(&key);
            let (share, vpath) = key;
            if let Ok(path) = SafePath::parse(&vpath, u16::MAX) {
                // Mark this directory's own ancestor chain (root..=this
                // dir) dirty by aiming `mark_dirty` at a synthetic child of
                // it — `mark_dirty(share, p)` dirties `p.parent()`'s chain,
                // so a dummy leaf under the changed directory makes that
                // directory itself (and everything above it) dirty without
                // needing to know the real filename that changed.
                if let Ok(synthetic) = path.join("_sc_watch_event_", u16::MAX) {
                    inner.core.mark_dirty(share, &synthetic);
                } else {
                    inner.core.mark_dirty(share, &path);
                }
            }
            let _ = inner.sink.send(InvalEvent { share, path: vpath, etag: None });
        }
    }
}

/// 's O(1) fallback: bump every registered share's
/// generation counter (`sc-meta`'s `bump_share_gen`, exposed as
/// `Core::invalidate_share`) instead of flushing the pending set one
/// directory at a time. A single `UPSERT` per share, regardless of how many
/// paths actually changed or were lost — every cached directory aggregate
/// for that share reads as stale on its very next lookup
/// (`sc-meta/src/etag.rs`'s `dir_etag`).
fn full_invalidate(inner: &Arc<Inner>, kernel_overflow: bool, pending_len: u64) {
    tracing::warn!(
        kernel_overflow,
        pending_len,
        full_threshold = inner.cfg.full_threshold,
        "watch queue overflowed; falling back to O(1) full invalidation instead of per-directory flush"
    );
    inner.pending.clear();
    for def in inner.core.share_defs() {
        match inner.core.invalidate_share(def.id) {
            Ok(_) => {
                let _ = inner.sink.send(InvalEvent { share: def.id, path: String::new(), etag: None });
            }
            Err(e) => {
                tracing::warn!(share = def.id.get(), error = %e, "full invalidation failed for a share");
            }
        }
    }
}

/// How often the periodic NFS/FUSE rescan sweeps hot-set directories
/// `inotify` cannot see a change another host makes
/// directly on a network mount, so this is the only thing that ever
/// notices one. 60s matches the staleness a real NFS client already
/// tolerates on its own attribute cache (Linux's default `acdirmax=60`), so
/// this adds no more lag than the mount itself already accepts.
const RESCAN_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60);

/// Same pacing idea as `sc-server`'s `CrawlThrottle` (#124) — not the same
/// type, since that one is private to `sc-server` and keyed by
/// `sc_http::search_limits::StorageClass`, a dependency `sc-watch` has no
/// business taking on. A short sleep every few directories keeps a sweep
/// over a large hot set from bursting disk-hitting reconciliation work
/// (`sc-server`'s `reconcile_watch_event`) onto a co-accessed NFS/FUSE mount
/// all at once.
struct RescanPace {
    batch: usize,
    sleep: std::time::Duration,
}
const RESCAN_THROTTLE: RescanPace = RescanPace { batch: 4, sleep: std::time::Duration::from_millis(50) };

/// One dedicated thread, strictly sleep-then-sweep: a slow sweep (a laggy
/// NFS mount) only ever delays the *next* sweep, since nothing here can
/// start a second one before this one returns.
fn rescan_loop(inner: Arc<Inner>) {
    while sleep_interruptible(&inner, RESCAN_INTERVAL) {
        rescan_unreliable_shares(&inner);
    }
}

/// Sleeps up to `total` in short steps, checking `stop` between each one so
/// shutdown is never blocked behind a full `RESCAN_INTERVAL`. Returns
/// `false` (without having slept the full duration) if `stop` fired.
fn sleep_interruptible(inner: &Inner, total: std::time::Duration) -> bool {
    let step = std::time::Duration::from_millis(500);
    let mut waited = std::time::Duration::ZERO;
    while waited < total {
        if inner.stop.load(Ordering::Relaxed) {
            return false;
        }
        std::thread::sleep(step.min(total - waited));
        waited += step;
    }
    !inner.stop.load(Ordering::Relaxed)
}

/// One sweep: for every share whose filesystem is known to lose inotify
/// events made by another host (`FsType::watch_unreliable` — the same
/// detector the startup share-registration gate uses),
/// re-mark every directory already in the hot set dirty. Never a fresh
/// recursive walk.
fn rescan_unreliable_shares(inner: &Arc<Inner>) {
    for def in inner.core.share_defs() {
        if inner.stop.load(Ordering::Relaxed) {
            return;
        }
        let Some(root) = inner.core.share(def.id) else { continue };
        if !root.fstype().watch_unreliable() {
            continue; // local disks already get real inotify/notify events
        }
        rescan_one_share(inner, def.id);
    }
}

/// Re-marks dirty every currently-registered directory of `share`, paced by
/// `RESCAN_THROTTLE`. Split out from `rescan_unreliable_shares` so it can be
/// exercised directly in tests without needing a real NFS/FUSE mount to
/// pass the `watch_unreliable` gate.
fn rescan_one_share(inner: &Arc<Inner>, share: ShareId) {
    let keys: Vec<_> = inner.state.lock().registered_keys().into_iter().filter(|(s, _)| *s == share).collect();
    for (i, (s, vpath)) in keys.into_iter().enumerate() {
        if inner.stop.load(Ordering::Relaxed) {
            return;
        }
        backend::record_event(&inner.pending, s, vpath);
        if (i + 1) % RESCAN_THROTTLE.batch == 0 {
            std::thread::sleep(RESCAN_THROTTLE.sleep);
        }
    }
}
