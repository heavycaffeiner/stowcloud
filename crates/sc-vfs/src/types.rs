//! Plain data types shared by every backend: stat results, share policy,
//! filesystem-type gating.

use compact_str::CompactString;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Kind {
    File,
    Dir,
    Symlink,
    Other,
}

impl Kind {
    /// Note this is `== Dir` and not "can be traversed": a symlink *to* a
    /// directory answers `false`, which is what every caller here wants —
    /// under the default `SymlinkPolicy::Deny` it cannot be entered at all.
    #[inline]
    pub fn is_dir(self) -> bool {
        matches!(self, Kind::Dir)
    }
}

/// Minimal, `Copy`-able projection of a `statx`/`fstat` result.
#[derive(Clone, Copy, Debug)]
pub struct Stat {
    pub dev: u64,
    pub ino: u64,
    /// `STATX_BTIME`. `None` on filesystems that don't support birth time
    /// (some ext4 mount options, NFS, ...).
    pub btime_ns: Option<i128>,
    pub mtime_ns: i128,
    pub size: u64,
    pub mode: u32,
    pub uid: u32,
    pub gid: u32,
    pub nlink: u32,
    pub kind: Kind,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum SymlinkPolicy {
    /// Symlinks are visible in listings but cannot be opened/traversed.
    Deny,
    /// Symlinks may be followed as long as the resolved target stays within
    /// the share root.
    WithinShare,
    /// Symlinks are followed unconditionally. Trusted environments only.
    Follow,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum IdStrategy {
    /// Stable id derived from `(dev, ino, btime)`. Preferred: survives rename.
    Inode,
    /// Stable id derived from path. Used when the filesystem can't provide a
    /// trustworthy inode/btime (NFS, some FUSE mounts).
    Path,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum TrashMode {
    /// `<share>/.sctrash/{fileid}-{name}`. Same filesystem, so the move is a
    /// plain rename.
    ShareLocal,
    /// Immediate unlink, no trash.
    Off,
    // A third `Central` (shared, cross-share trash location) variant was
    // deleted rather than kept as a documented no-op: it was never
    // implemented (`ARCHITECTURE.md` §5.3), and nothing ever accepted it as
    // an input, so there was no boundary to reject it at either.
}

#[derive(Clone, Debug)]
pub struct SharePolicy {
    pub symlink: SymlinkPolicy,
    /// Allow traversal across nested mounts inside the share (default true).
    pub cross_mount: bool,
    pub id_strategy: IdStrategy,
    pub trash: TrashMode,
    /// Applied verbatim to newly created files (not filtered through umask).
    pub mode_file: u32,
    /// Applied verbatim to newly created directories.
    pub mode_dir: u32,
    /// `None` means "leave at the process uid/gid".
    pub chown: Option<(u32, u32)>,
    pub max_depth: u16,
}

impl Default for SharePolicy {
    fn default() -> Self {
        Self {
            symlink: SymlinkPolicy::Deny,
            cross_mount: true,
            id_strategy: IdStrategy::Inode,
            // Off by default: trash is a data-loss trap for a share nobody
            // is watching for GC (`ARCHITECTURE.md` §5.3). An admin turns it
            // on explicitly, per share, from `ShareManagementSection.svelte`
            // (`sc_core::Core::update_share`'s `trash_enabled`).
            trash: TrashMode::Off,
            mode_file: 0o664,
            mode_dir: 0o775,
            chown: None,
            max_depth: 64,
        }
    }
}

/// Filesystem-type gate. See `DEPLOYMENT.md` for the full matrix.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum FsType {
    Ext4,
    Btrfs,
    Xfs,
    Zfs,
    F2fs,
    Tmpfs,
    Overlay,
    Fuse,
    Nfs,
    Cifs,
    Squashfs,
    Ntfs,
    /// Anything we don't have a specific gate for. Carries the raw
    /// `statfs.f_type` magic (0 when unknown/unavailable, e.g. non-Linux).
    Other(u64),
}

impl FsType {
    /// Filesystems that are refused outright at `ShareRoot::open` time
    /// (inode instability, etc. — see ARCHITECTURE.md "filesystem gate").
    pub fn is_rejected(&self) -> bool {
        matches!(self, FsType::Overlay)
    }

    /// Filesystems where inode numbers aren't a trustworthy stable identity,
    /// forcing `IdStrategy::Path` regardless of what the share was
    /// configured with.
    pub fn forces_path_ids(&self) -> bool {
        matches!(self, FsType::Nfs | FsType::Cifs | FsType::Fuse)
    }

    /// Filesystems where inotify/fanotify watches are known to misbehave or
    /// be unavailable, so the caller should fall back to periodic rescan /
    /// lazy revalidation.
    pub fn watch_unreliable(&self) -> bool {
        matches!(
            self,
            FsType::Nfs | FsType::Cifs | FsType::Fuse | FsType::Overlay
        )
    }

    /// Map a Linux `statfs64.f_type` magic number to our enum. Unknown
    /// magics are preserved in `Other` rather than lossily discarded.
    ///
    /// Public because the startup diagnostics have to classify a share's
    /// filesystem *before* a `ShareRoot` exists for it — the whole point of
    /// the gate is to refuse registration on e.g. overlayfs. Keeping this
    /// private forced a second copy of the magic-number table into
    /// `sc-server`, and two copies of a lookup table drift.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    pub fn from_statfs_magic(magic: u64) -> FsType {
        match magic {
            0xEF53 => FsType::Ext4,
            0x9123_683E => FsType::Btrfs,
            0x5846_5342 => FsType::Xfs,
            0x2FC1_2FC1 => FsType::Zfs,
            0xF2F5_2010 => FsType::F2fs,
            0x0102_1994 => FsType::Tmpfs,
            0x794C_7630 => FsType::Overlay,
            0x6573_7546 => FsType::Fuse,
            0x6969 => FsType::Nfs,
            0xFF53_4D42 | 0xFE53_4D42 => FsType::Cifs,
            0x7371_7368 => FsType::Squashfs,
            0x5346_544E => FsType::Ntfs,
            other => FsType::Other(other),
        }
    }
}

#[derive(Clone, Debug)]
pub struct DirEntry {
    pub name: CompactString,
    pub kind: Kind,
    /// Inode number, when the platform's directory read hands it over for
    /// free (`getdents64` always does; Windows does not).
    ///
    /// This exists purely so callers can order a later `stat` batch by
    /// `(dev, ino)`. Filesystems lay inodes out in increasing numeric order,
    /// so requesting them that way makes the disk seek forward only and
    /// raises the chance several inodes of interest share a block — worth
    /// close to an order of magnitude on the spinning 12 TB RAID this is
    /// aimed at. Without it, a search that has to
    /// filter on size or mtime degrades to readdir order.
    ///
    /// `None` means "unknown", never "zero" — do not use it for identity.
    pub ino: Option<u64>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn trash_is_off_by_default() {
        assert_eq!(SharePolicy::default().trash, TrashMode::Off);
    }
}
