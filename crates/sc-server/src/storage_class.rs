//! Storage-class detection for the search resource limiter
//!. `sc_vfs::ShareRoot`
//! already exposes what a share's filesystem is (`fstype()`) and which
//! block device backs it (`root_dev()`); this module turns those into an
//! `sc_http::search_limits::StorageClass` without needing anything new from
//! `sc-vfs` itself.

use std::collections::HashMap;

use sc_http::search_limits::StorageClass;
use sc_vfs::{FsType, ShareId, ShareRoot};

/// Detects `root`'s storage class.
///
/// Network/FUSE filesystems (`Nfs`/`Cifs`/`Fuse`) go straight to
/// `StorageClass::Network` — there's no local block device to ask about
/// rotational-ness, and unpredictable latency deserves the conservative
/// path regardless (the same call `sc-search`'s `decide_threads` already
/// makes for NFS/CIFS's thread count).
///
/// Everything else is a local filesystem, classified by the kernel's own
/// rotational flag (`/sys/block/*/queue/rotational`) — see `detect_local`.
pub fn detect(root: &ShareRoot) -> StorageClass {
    match root.fstype() {
        FsType::Nfs | FsType::Cifs | FsType::Fuse => StorageClass::Network,
        _ => detect_local(root.root_dev()),
    }
}

#[cfg(target_os = "linux")]
fn detect_local(root_dev: u64) -> StorageClass {
    let dev = root_dev as rustix::fs::Dev;
    let major = rustix::fs::major(dev);
    let minor = rustix::fs::minor(dev);
    match rotational_from_sysfs(major, minor) {
        Some((true, _)) => StorageClass::Rotational,
        Some((false, path)) => {
            // NVMe devices live under an `nvme` subsystem directory
            // (`/sys/devices/.../nvme/nvme0/nvme0n1/...`); everything else
            // non-rotational is a SATA/SAS SSD. `/sys/block/*/queue/
            // rotational` alone only distinguishes
            // spinning vs. flash, not which kind of flash — this is an
            // additional, cheap classification on top of it.
            if path.to_string_lossy().contains("nvme") {
                StorageClass::Nvme
            } else {
                StorageClass::SataSsd
            }
        }
        // Sysfs unreadable (container without /sys mounted, unusual device-
        // mapper setup, ...): assume the worst rather than the best. An
        // over-conservative cap costs latency; an over-permissive one costs
        // a thrashing disk and starved neighbours.
        None => StorageClass::Rotational,
    }
}

/// Resolves `/sys/dev/block/{major}:{minor}` and walks up looking for
/// `queue/rotational`. Partitions (`.../block/sda/sda1`) don't carry their
/// own `queue/` — only the whole-disk directory one level up does — so this
/// walks a few levels rather than reading the leaf directly. Returns the
/// resolved directory alongside the flag so the caller can look for `nvme`
/// in it without a second sysfs round trip.
#[cfg(target_os = "linux")]
fn rotational_from_sysfs(major: u32, minor: u32) -> Option<(bool, std::path::PathBuf)> {
    let link = std::path::PathBuf::from(format!("/sys/dev/block/{major}:{minor}"));
    let mut dir = std::fs::canonicalize(&link).ok()?;
    for _ in 0..4 {
        let rotational_path = dir.join("queue/rotational");
        if let Ok(s) = std::fs::read_to_string(&rotational_path) {
            return Some((s.trim() == "1", dir));
        }
        dir = dir.parent()?.to_path_buf();
    }
    None
}

/// Non-Linux (the dev host, Windows): there is no `/sys/block` to consult,
/// and the dev host is never the deployment target —'s detection is Linux-specific. Default to the permissive class rather
/// than pretending to detect something that isn't there, so local
/// iteration on the dev host stays fast.
#[cfg(not(target_os = "linux"))]
fn detect_local(_root_dev: u64) -> StorageClass {
    StorageClass::Nvme
}

/// Caches per-share detection so the hot search path doesn't re-read sysfs
/// on every request — detection is meant to happen (conceptually) once, at
/// share registration, not per query.
#[derive(Default)]
pub struct StorageClassCache {
    inner: parking_lot::Mutex<HashMap<ShareId, StorageClass>>,
}

impl StorageClassCache {
    pub fn get_or_detect(&self, root: &ShareRoot) -> StorageClass {
        if let Some(class) = self.inner.lock().get(&root.id()) {
            return *class;
        }
        let class = detect(root);
        self.inner.lock().insert(root.id(), class);
        class
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cache_returns_a_stable_value_per_share() {
        // We can't open a real `ShareRoot` without a filesystem fixture, so
        // this only exercises the cache's own bookkeeping directly.
        let cache = StorageClassCache::default();
        cache
            .inner
            .lock()
            .insert(ShareId::new(1), StorageClass::Rotational);
        // A second "detect" for the same id must come from the cache, not
        // re-run detection (which would need a live `ShareRoot`).
        assert_eq!(
            *cache.inner.lock().get(&ShareId::new(1)).unwrap(),
            StorageClass::Rotational
        );
    }
}
