//! The OS-level watch transport. Two implementations live behind this
//! trait: `PortableBackend` (the `notify` crate — used on every non-Linux
//! target, and selectable on Linux too) and, on Linux only, a raw-`inotify`
//! backend (`linux.rs`). Both push raw change notifications into a shared
//! debounce buffer keyed by the *watched directory's* `(ShareId, SafePath
//! display string)` — never a host path — so the rest of `sc-watch` never
//! has to know which transport is live.

use std::path::{Path, PathBuf};
use std::sync::atomic::AtomicBool;
use std::sync::Arc;

use dashmap::DashMap;
use sc_vfs::ShareId;

use crate::hotset::Key;

/// Set by a backend the moment the OS reports *its own* event queue
/// overflowed (`inotify`'s `IN_Q_OVERFLOW`, or `notify`'s equivalent
/// `Flag::Rescan`) — individually changed paths were lost, not just
/// coalesced. The debounce loop reacts by invalidating every share in O(1)
/// instead of trusting the (now incomplete) per-directory dirty set
/// (`FEATURES.md` #130).
pub(crate) type OverflowFlag = Arc<AtomicBool>;

/// A watch registration failed. `NoSpace` is the expected, normal-path
/// failure (`ENOSPC`/`inotify` limit/OS watch-count cap) that callers
/// degrade on rather than propagate as a hard error.
#[derive(Debug)]
pub(crate) enum WatchAddError {
    NoSpace,
    Other(String),
}

pub(crate) trait RawBackend: Send {
    fn add_watch(&mut self, key: Key, path: &Path) -> Result<(), WatchAddError>;
    fn remove_watch(&mut self, key: &Key, path: &Path);
}

/// Shared by every backend: host path -> the `(share, dir)` key it
/// represents, so an incoming filesystem event (which only carries host
/// paths) can be translated back into share-relative terms.
pub(crate) type ReversePathMap = Arc<DashMap<PathBuf, Key>>;

/// Pending-dirty buffer: first-seen `Instant` per key. The debounce thread
/// flushes (and removes) entries once they've been sitting for
/// `DEBOUNCE_MS`, which means a burst that entirely finishes within the
/// window coalesces into exactly one flush.
pub(crate) type PendingMap = Arc<DashMap<Key, std::time::Instant>>;

pub(crate) const DEBOUNCE: std::time::Duration = std::time::Duration::from_millis(200);

pub(crate) fn record_event(pending: &PendingMap, share: ShareId, dir_vpath: String) {
    pending.entry((share, dir_vpath)).or_insert_with(std::time::Instant::now);
}

pub(crate) mod portable {
    use super::*;
    use notify::{Event, RecommendedWatcher, RecursiveMode, Watcher as NotifyWatcherTrait};

    pub(crate) struct PortableBackend {
        watcher: RecommendedWatcher,
    }

    impl PortableBackend {
        pub(crate) fn new(pending: PendingMap, reverse: ReversePathMap, overflow: OverflowFlag) -> anyhow::Result<Self> {
            let watcher = notify::recommended_watcher(move |res: notify::Result<Event>| {
                let Ok(event) = res else { return };
                if event.need_rescan() {
                    // The transport's own signal that the underlying OS
                    // queue overflowed and some changes were lost.
                    overflow.store(true, std::sync::atomic::Ordering::Relaxed);
                    return;
                }
                for p in &event.paths {
                    // The watch is registered non-recursively on the
                    // directory itself, but events report the *child*
                    // path that changed — look up its parent.
                    let Some(parent) = p.parent() else { continue };
                    if let Some(entry) = reverse.get(parent) {
                        let (share, vpath) = entry.value().clone();
                        record_event(&pending, share, vpath);
                    } else if let Some(entry) = reverse.get(p.as_path()) {
                        // Some backends report the watched dir itself on
                        // rename/delete-of-watched-dir.
                        let (share, vpath) = entry.value().clone();
                        record_event(&pending, share, vpath);
                    }
                }
            })?;
            Ok(Self { watcher })
        }
    }

    impl RawBackend for PortableBackend {
        fn add_watch(&mut self, _key: Key, path: &Path) -> Result<(), WatchAddError> {
            self.watcher
                .watch(path, RecursiveMode::NonRecursive)
                .map_err(|e| classify(&e))
        }

        fn remove_watch(&mut self, _key: &Key, path: &Path) {
            let _ = self.watcher.unwatch(path);
        }
    }

    fn classify(e: &notify::Error) -> WatchAddError {
        match &e.kind {
            notify::ErrorKind::MaxFilesWatch => WatchAddError::NoSpace,
            notify::ErrorKind::Io(io) if io.raw_os_error() == Some(28) /* ENOSPC */ => WatchAddError::NoSpace,
            other => WatchAddError::Other(format!("{other:?}")),
        }
    }
}

#[cfg(target_os = "linux")]
pub(crate) mod linux {
    use super::*;
    use inotify::{EventMask, Inotify, WatchDescriptor, Watches, WatchMask};
    use std::collections::HashMap;
    use std::sync::Mutex as StdMutex;

    /// Raw-`inotify` transport (`WatchBackend::InotifyFull`/`Fanotify` both
    /// map to this — real `FAN_MARK_FILESYSTEM` needs `CAP_SYS_ADMIN` and a
    /// dedicated fanotify syscall wrapper this crate doesn't pull in, so
    /// `Fanotify` is accepted and downgraded to this with a log warning
    /// rather than implemented separately; see `lib.rs`).
    ///
    /// `Inotify` owns the read side (moved into the reader thread);
    /// `Watches` is a cheaply-cloneable handle to the same underlying fd
    /// used for `add`/`remove` from `RawBackend` methods without contending
    /// with the blocking reader.
    pub(crate) struct LinuxInotifyBackend {
        watches: Watches,
        wd_to_key: Arc<StdMutex<HashMap<WatchDescriptor, Key>>>,
    }

    impl LinuxInotifyBackend {
        pub(crate) fn new(pending: PendingMap, _reverse: ReversePathMap, overflow: OverflowFlag) -> anyhow::Result<Self> {
            let inotify = Inotify::init()?;
            let watches = inotify.watches();
            let wd_to_key: Arc<StdMutex<HashMap<WatchDescriptor, Key>>> = Arc::new(StdMutex::new(HashMap::new()));

            let map_for_reader = wd_to_key.clone();
            std::thread::spawn(move || {
                let mut inotify = inotify;
                let mut buffer = [0u8; 4096];
                loop {
                    let events = match inotify.read_events_blocking(&mut buffer) {
                        Ok(ev) => ev,
                        Err(_) => break,
                    };
                    for ev in events {
                        if ev.mask.contains(EventMask::Q_OVERFLOW) {
                            // Kernel dropped events faster than we could
                            // read them; `ev.wd` is unspecified (`-1`) for
                            // this one, so there is no single path to mark
                            // dirty — the debounce loop's full-invalidation
                            // fallback handles it instead (`FEATURES.md` #130).
                            overflow.store(true, std::sync::atomic::Ordering::Relaxed);
                            continue;
                        }
                        let key = map_for_reader.lock().unwrap().get(&ev.wd).cloned();
                        if let Some((share, vpath)) = key {
                            record_event(&pending, share, vpath);
                        }
                    }
                }
            });

            Ok(Self { watches, wd_to_key })
        }
    }

    impl RawBackend for LinuxInotifyBackend {
        fn add_watch(&mut self, key: Key, path: &Path) -> Result<(), WatchAddError> {
            let mask = WatchMask::CREATE | WatchMask::DELETE | WatchMask::MODIFY | WatchMask::MOVE | WatchMask::ATTRIB;
            match self.watches.add(path, mask) {
                Ok(wd) => {
                    self.wd_to_key.lock().unwrap().insert(wd, key);
                    Ok(())
                }
                Err(e) => {
                    if e.raw_os_error() == Some(28) {
                        Err(WatchAddError::NoSpace)
                    } else {
                        Err(WatchAddError::Other(e.to_string()))
                    }
                }
            }
        }

        fn remove_watch(&mut self, key: &Key, _path: &Path) {
            let wd = {
                let mut m = self.wd_to_key.lock().unwrap();
                let found = m.iter().find(|(_, k)| *k == key).map(|(wd, _)| wd.clone());
                if let Some(w) = &found {
                    m.remove(w);
                }
                found
            };
            if let Some(wd) = wd {
                let _ = self.watches.remove(wd);
            }
        }
    }
}
