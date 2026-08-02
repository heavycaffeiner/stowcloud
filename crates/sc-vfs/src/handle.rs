//! Open handles. `DirHandle` is `O_RDONLY|O_DIRECTORY` (can list);
//! `FileHandle` is a regular file open for positional read/write. Neither
//! ever exposes the underlying platform fd/handle or a host path — see
//! the invariant is that nothing outside `sc-vfs`
//! ever sees a `RawFd` or a host `&Path`.

use crate::backend;
use crate::error::VfsError;
use crate::types::{DirEntry, Stat};

/// A directory opened `O_RDONLY|O_DIRECTORY` (or the portable-backend
/// equivalent), suitable for listing. Distinct from the `O_PATH` anchor kept
/// inside `ShareRoot` — seea for why the two must not
/// be conflated (`O_PATH` fds can't `getdents64`).
pub struct DirHandle {
    pub(crate) inner: backend::imp::DirInner,
}

impl DirHandle {
    pub fn read_entries(&self) -> Result<Vec<DirEntry>, VfsError> {
        backend::imp::dir_read_entries(&self.inner)
    }
}

/// An open file, readable and (usually) writable at arbitrary offsets.
pub struct FileHandle {
    pub(crate) inner: backend::imp::FileInner,
}

impl FileHandle {
    pub fn stat(&self) -> Result<Stat, VfsError> {
        backend::imp::file_stat(&self.inner)
    }

    pub fn read_at(&self, buf: &mut [u8], off: u64) -> Result<usize, VfsError> {
        backend::imp::file_read_at(&self.inner, buf, off)
    }

    pub fn write_at(&self, buf: &[u8], off: u64) -> Result<usize, VfsError> {
        backend::imp::file_write_at(&self.inner, buf, off)
    }

    pub fn set_len(&self, n: u64) -> Result<(), VfsError> {
        backend::imp::file_set_len(&self.inner, n)
    }

    /// Set the file mode. No-op on hosts without POSIX mode bits.
    pub fn set_mode(&self, mode: u32) -> Result<(), VfsError> {
        backend::imp::file_set_mode(&self.inner, mode)
    }

    /// Set the owning uid/gid. May fail with `PermissionDenied` when
    /// unprivileged; that is reported, not swallowed.
    pub fn set_owner(&self, uid: u32, gid: u32) -> Result<(), VfsError> {
        backend::imp::file_set_owner(&self.inner, uid, gid)
    }

    /// Copy ownership and permissions from the file this one is about to
    /// replace, so that a rename-based atomic replace is invisible to the
    /// other services sharing the directory (`ARCHITECTURE.md` §5.2).
    ///
    /// Ownership is best-effort: an unprivileged process cannot give a file
    /// away, and failing the whole upload for that would be worse than
    /// completing it with our own uid. The mode is *not* best-effort — losing
    /// the group-read bit is exactly the failure this exists to prevent.
    pub fn transplant_metadata_from(&self, prev: &Stat) -> Result<(), VfsError> {
        self.set_mode(prev.mode)?;
        match self.set_owner(prev.uid, prev.gid) {
            Ok(()) | Err(VfsError::PermissionDenied) => Ok(()),
            Err(e) => Err(e),
        }
    }

    pub fn sync_data(&self) -> Result<(), VfsError> {
        backend::imp::file_sync_data(&self.inner)
    }

    /// Copy `len` bytes from `src` at `src_off` to `self` at `dst_off`
    /// without a userspace round trip when the platform allows it —
    /// `DESIGN-UPLOAD.md` §7.2 / `TECH-STACK.md` §3. On Linux this is
    /// `copy_file_range`: a reflink (near-instant, no bandwidth) on
    /// btrfs/XFS when block alignment allows it, an in-kernel copy
    /// otherwise. It falls back to a bounded-buffer read/write loop when the
    /// kernel primitive isn't usable (`EXDEV` — including *within* one
    /// share, per `DEPLOYMENT.md` §4 — `EOPNOTSUPP`, or `ENOSYS` on an old
    /// kernel) and unconditionally on the portable backend, which has no
    /// such syscall at all. Either way the fallback is the single
    /// implementation in `crate::copy`, never duplicated per call site.
    ///
    /// May copy fewer than `len` bytes (a short copy, or `src` ran out of
    /// bytes first) — same discipline as `read_at`/`write_at`: callers that
    /// need exactly `len` bytes copied must loop.
    pub fn copy_range_from(&self, src: &FileHandle, src_off: u64, dst_off: u64, len: u64) -> Result<u64, VfsError> {
        backend::imp::file_copy_range(&src.inner, src_off, &self.inner, dst_off, len)
    }
}
