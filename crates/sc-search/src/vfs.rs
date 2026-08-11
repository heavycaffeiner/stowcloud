//! The **only** place `sc-search` touches `sc-vfs`.
//!
//! Two jobs:
//!
//! 1. Re-export the handful of `sc-vfs` types the search API mentions, so the
//!    rest of the crate never names `sc_vfs` directly and a signature change
//!    over there is a one-line fix here.
//! 2. Define [`DirSource`] — the two operations the walker actually needs from
//!    a share (list a directory without touching inodes; stat one entry, on
//!    demand). The walk engine is written against the trait, which makes it
//!    testable against a synthetic tree and lets the ACL-pruning test count
//!    directory reads directly rather than inferring them.
//!
//! `sc-search` never constructs a `&Path` and never resolves a path itself.
//! §1.4 is explicit about why: the walker sweeps the entire tree, so if share
//! isolation breaks here it is the broadest possible leak. Every descent goes
//! back through `ShareRoot`, which resolves under `RESOLVE_BENEATH`.

use std::sync::Arc;

pub use sc_vfs::ids::{FileId, ShareId, UserId};
pub use sc_vfs::{
    is_reserved_name, DirEntry, DirHandle, FsType, Kind, SafePath, ShareRoot, Stat, VfsError,
    RESERVED_PREFIXES,
};

/// Depth ceiling handed to `SafePath::join`. The walker enforces its own,
/// tighter, per-query limit via [`crate::WalkBudget::max_depth`]; this one only
/// has to be no smaller than the share policy allows, so that constructing the
/// path of an entry that legitimately exists never fails.
pub const JOIN_MAX_DEPTH: u16 = u16::MAX;

/// Parse a share-relative path.
pub fn parse_path(s: &str) -> Option<SafePath> {
    SafePath::parse(s, JOIN_MAX_DEPTH).ok()
}

/// Append one component read from a directory listing.
///
/// `None` when the name is not addressable as a `SafePath` — it contains a
/// separator or a NUL, is over-long, or names one of this server's own control
/// files. Such an entry is skipped rather than reported: we could not safely
/// re-open it, so promising the user a result there would be a lie.
///
/// `join_existing`, because that is exactly what this is: a name the kernel
/// just handed back. `join` applies the table for names being *created*, so
/// every `CON` and `a:b` on the share was skipped by the walk — silently
/// absent from search results and from the recency query, with no reason given
/// anywhere.
pub fn join_child(parent: &SafePath, name: &str) -> Option<SafePath> {
    parent.join_existing(name, JOIN_MAX_DEPTH).ok()
}

/// Display form (`"a/b/c"`; the root is `""`).
pub fn display_path(p: &SafePath) -> String {
    p.to_display_string()
}

// ---------------------------------------------------------------------------
// DirSource
// ---------------------------------------------------------------------------

/// What the walker needs from a share.
pub trait DirSource: Send + Sync {
    fn share(&self) -> ShareId;

    /// One `getdents64` pass. **Must not `statx`** — `DirEntry::kind` comes
    /// from `d_type`, and avoiding metadata is the single largest optimisation
    /// available to us (§1.2 point 2: 54.6 ms → 87.0 ms in jwalk's numbers).
    fn read_entries(&self, p: &SafePath) -> Result<Vec<DirEntry>, VfsError>;

    /// Called only from the stat phase, only for entries that already matched.
    fn stat(&self, p: &SafePath) -> Result<Stat, VfsError>;

    /// `st_dev` of the share root — the major key of the inode-order sort.
    fn root_dev(&self) -> u64;

    /// Spinning rust. Drives thread count (§3.3) and whether the stat phase
    /// bothers sorting into inode order (§3.4).
    fn rotational(&self) -> bool {
        false
    }
}

/// [`DirSource`] over a real share.
pub struct ShareSource {
    root: Arc<ShareRoot>,
    rotational: bool,
}

impl ShareSource {
    pub fn new(root: Arc<ShareRoot>, rotational: bool) -> Self {
        Self { root, rotational }
    }

    /// Infer the storage class from the filesystem type when the caller has no
    /// better information. Deliberately conservative: anything we cannot
    /// identify is treated as *not* rotational, because the cost of guessing
    /// "rotational" wrongly (2 threads on an NVMe array) is larger than the
    /// cost of guessing the other way for a name-only walk, which issues no
    /// inode reads at all.
    pub fn from_root(root: Arc<ShareRoot>) -> Self {
        let rotational = matches!(root.fstype(), FsType::Nfs | FsType::Cifs);
        Self::new(root, rotational)
    }

    pub fn share_root(&self) -> &Arc<ShareRoot> {
        &self.root
    }
}

impl DirSource for ShareSource {
    fn share(&self) -> ShareId {
        self.root.id()
    }

    fn read_entries(&self, p: &SafePath) -> Result<Vec<DirEntry>, VfsError> {
        self.root.read_dir(p)
    }

    fn stat(&self, p: &SafePath) -> Result<Stat, VfsError> {
        self.root.stat(p)
    }

    fn root_dev(&self) -> u64 {
        self.root.root_dev()
    }

    fn rotational(&self) -> bool {
        self.rotational
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reserved_names_come_from_sc_vfs() {
        assert!(is_reserved_name(".sctrash"));
        assert!(is_reserved_name(".scpart-abc"));
        assert!(is_reserved_name(".scindex"));
        assert!(!is_reserved_name("photo.jpg"));
    }

    #[test]
    fn path_helpers() {
        let root = SafePath::root();
        let a = join_child(&root, "a").unwrap();
        let b = join_child(&a, "b.txt").unwrap();
        assert_eq!(display_path(&b), "a/b.txt");
        assert!(join_child(&a, "..").is_none());
        assert!(join_child(&a, "x/y").is_none());
        assert!(parse_path("../etc").is_none());
    }
}
