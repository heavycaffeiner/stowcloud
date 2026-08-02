//! `ShareRoot` — the share-root anchor. Everything the rest of the codebase
//! does to touch a share's filesystem goes through this type plus
//! `SafePath`; nothing above `sc-vfs` ever sees a host path or raw fd.

use std::path::Path;
use std::sync::Arc;

use crate::backend;
use crate::error::VfsError;
use crate::handle::{DirHandle, FileHandle};
use crate::ids::ShareId;
use crate::safe_path::SafePath;
use crate::types::{DirEntry, FsType, SharePolicy, Stat};

pub struct ShareRoot {
    id: ShareId,
    anchor: backend::imp::AnchorHandle,
    root_dev: u64,
    fstype: FsType,
    policy: Arc<SharePolicy>,
}

impl ShareRoot {
    /// Open a share root. `host_path` must be a directory; it is resolved
    /// once here and kept alive as an anchor for the lifetime of the
    /// process (or until this `ShareRoot` is dropped) — see
    /// `DESIGN-CORE.md` §1.1.
    pub fn open(id: ShareId, host_path: &Path, policy: SharePolicy) -> Result<Self, VfsError> {
        let (anchor, root_dev, fstype) = backend::imp::open_anchor(host_path, &policy)?;
        if fstype.is_rejected() {
            return Err(VfsError::UnsupportedFs(fstype));
        }
        Ok(Self {
            id,
            anchor,
            root_dev,
            fstype,
            policy: Arc::new(policy),
        })
    }

    pub fn id(&self) -> ShareId {
        self.id
    }

    pub fn fstype(&self) -> FsType {
        self.fstype
    }

    pub fn root_dev(&self) -> u64 {
        self.root_dev
    }

    pub fn policy(&self) -> &SharePolicy {
        &self.policy
    }

    pub fn stat(&self, p: &SafePath) -> Result<Stat, VfsError> {
        backend::imp::stat_path(&self.anchor, p, &self.policy)
    }

    /// `O_RDONLY|O_DIRECTORY` — can list. See `DESIGN-CORE.md` §1.1a.
    pub fn open_dir(&self, p: &SafePath) -> Result<DirHandle, VfsError> {
        let inner = backend::imp::open_dir(&self.anchor, p, &self.policy)?;
        Ok(DirHandle { inner })
    }

    pub fn open_read(&self, p: &SafePath) -> Result<FileHandle, VfsError> {
        let inner = backend::imp::open_read(&self.anchor, p, &self.policy)?;
        Ok(FileHandle { inner })
    }

    /// `O_CREAT|O_EXCL|O_WRONLY|O_NOFOLLOW` — atomic create, refuses to
    /// clobber an existing entry or follow a preemptively-planted symlink.
    pub fn create_excl(&self, p: &SafePath, mode: u32) -> Result<FileHandle, VfsError> {
        let inner = backend::imp::create_excl(&self.anchor, p, &self.policy, mode)?;
        Ok(FileHandle { inner })
    }

    pub fn mkdir(&self, p: &SafePath) -> Result<(), VfsError> {
        backend::imp::mkdir(&self.anchor, p, &self.policy)
    }

    pub fn unlink(&self, p: &SafePath) -> Result<(), VfsError> {
        backend::imp::unlink(&self.anchor, p, &self.policy)
    }

    pub fn rmdir(&self, p: &SafePath) -> Result<(), VfsError> {
        backend::imp::rmdir(&self.anchor, p, &self.policy)
    }

    /// `no_replace` maps to `RENAME_NOREPLACE` on Linux (atomic). The
    /// portable backend approximates it with a check-then-rename — see
    /// `DESIGN-CORE.md` §5.5 for why that window can't always be closed.
    pub fn rename(&self, from: &SafePath, to: &SafePath, no_replace: bool) -> Result<(), VfsError> {
        backend::imp::rename(&self.anchor, from, to, &self.policy, no_replace)
    }

    /// `fsync` the directory at `p`, making the *entries* in it durable.
    ///
    /// The counterpart to [`FileHandle::sync_data`] for a write-then-rename:
    /// syncing the staging file guarantees its bytes, and this guarantees that
    /// the rename which published them under the final name survives a power
    /// loss. Callers that publish through a rename need both — see
    /// `sc_core::ops`.
    ///
    /// A no-op on Windows, which has no directory-fsync; see
    /// `backend::portable::sync_dir`.
    pub fn sync_dir(&self, p: &SafePath) -> Result<(), VfsError> {
        backend::imp::sync_dir(&self.anchor, p, &self.policy)
    }

    pub fn set_times(&self, p: &SafePath, mtime_ns: i128) -> Result<(), VfsError> {
        backend::imp::set_times(&self.anchor, p, &self.policy, mtime_ns)
    }

    pub fn read_dir(&self, p: &SafePath) -> Result<Vec<DirEntry>, VfsError> {
        backend::imp::read_dir(&self.anchor, p, &self.policy)
    }

    /// `read_dir`, but including our own control files (`.scpart-*`,
    /// `.sctrash`, ...) which `read_dir` deliberately hides.
    ///
    /// Only for trusted internal maintenance that must *find* those files —
    /// the upload orphan sweep and trash GC. Never reachable from a request
    /// path, and never used to build anything a user sees: if a control file
    /// reached a listing, the reserved prefix would have failed at its one
    /// job.
    pub fn read_dir_control(&self, p: &SafePath) -> Result<Vec<DirEntry>, VfsError> {
        backend::imp::INCLUDE_RESERVED.with(|c| c.set(true));
        let r = backend::imp::read_dir(&self.anchor, p, &self.policy);
        backend::imp::INCLUDE_RESERVED.with(|c| c.set(false));
        r
    }

    /// `(free_bytes, total_bytes)`.
    pub fn statfs_free(&self) -> Result<(u64, u64), VfsError> {
        backend::imp::statfs_free(&self.anchor)
    }
}
