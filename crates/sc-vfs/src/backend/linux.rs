//! Linux backend: `openat2(RESOLVE_BENEATH | ...)` for everything that
//! touches the filesystem. See-2 — this module is a
//! direct translation of that spec.
//!
//! Pattern used throughout: resolve the *parent* directory of the target
//! with a single `openat2` call over the (possibly multi-component) parent
//! path — the kernel enforces the resolve-flag restrictions across every
//! intermediate component atomically, which is exactly what defeats TOCTOU.
//! The final path component is then handled with a plain `*at()` syscall
//! (`mkdirat`, `unlinkat`, `renameat2`, ...) against that already-safe parent
//! fd, or with one more `openat2` hop when we need the leaf's own
//! symlink-ness gated by policy too (open/stat).

use std::path::Path;

use compact_str::CompactString;
use rustix::fd::OwnedFd;
use rustix::fs::{self, AtFlags, Mode, OFlags, ResolveFlags, StatxFlags};
use rustix::io::Errno;

use crate::error::VfsError;
use crate::reserved::is_reserved_name;
use crate::safe_path::{lookup_candidates, normalize_new_name, path_candidates};
use crate::types::{DirEntry, FsType, Kind, SharePolicy, Stat, SymlinkPolicy};
use crate::SafePath;

pub(crate) struct AnchorHandle(OwnedFd);
pub(crate) struct FileInner(OwnedFd);
pub(crate) struct DirInner(OwnedFd);

const STAT_MASK: StatxFlags = StatxFlags::BASIC_STATS.union(StatxFlags::BTIME);

fn map_errno(e: Errno) -> VfsError {
    match e {
        Errno::NOENT => VfsError::NotFound,
        Errno::ACCESS | Errno::PERM => VfsError::PermissionDenied,
        Errno::EXIST => VfsError::AlreadyExists,
        Errno::NOTEMPTY => VfsError::NotEmpty,
        Errno::NOSPC => VfsError::NoSpace,
        Errno::XDEV => VfsError::CrossDevice,
        Errno::LOOP => VfsError::SymlinkDenied,
        other => VfsError::Io(std::io::Error::from_raw_os_error(other.raw_os_error())),
    }
}

/// Resolve flags for one share's symlink policy. NO_MAGICLINKS is
/// unconditional: it blocks escape through /proc/self/fd/*.
fn resolve_flags(policy: &SharePolicy) -> ResolveFlags {
    let mut f = ResolveFlags::NO_MAGICLINKS;
    f |= match policy.symlink {
        SymlinkPolicy::Deny => ResolveFlags::BENEATH | ResolveFlags::NO_SYMLINKS,
        SymlinkPolicy::WithinShare => ResolveFlags::IN_ROOT,
        SymlinkPolicy::Follow => ResolveFlags::BENEATH,
    };
    if !policy.cross_mount {
        f |= ResolveFlags::NO_XDEV;
    }
    f
}

fn statx_to_stat(st: &fs::Statx) -> Stat {
    let has_btime = (st.stx_mask & StatxFlags::BTIME.bits()) != 0;
    let btime_ns = has_btime.then(|| {
        st.stx_btime.tv_sec as i128 * 1_000_000_000 + st.stx_btime.tv_nsec as i128
    });
    let mtime_ns = st.stx_mtime.tv_sec as i128 * 1_000_000_000 + st.stx_mtime.tv_nsec as i128;
    let dev = fs::makedev(st.stx_dev_major, st.stx_dev_minor);
    let kind = match fs::FileType::from_raw_mode(st.stx_mode as fs::RawMode) {
        fs::FileType::Directory => Kind::Dir,
        fs::FileType::Symlink => Kind::Symlink,
        fs::FileType::RegularFile => Kind::File,
        _ => Kind::Other,
    };
    Stat {
        dev,
        ino: st.stx_ino,
        btime_ns,
        mtime_ns,
        size: st.stx_size,
        mode: st.stx_mode as u32,
        uid: st.stx_uid,
        gid: st.stx_gid,
        nlink: st.stx_nlink,
        kind,
    }
}

/// Resolve `comps` to a `O_PATH|O_DIRECTORY` fd, honoring `policy`'s resolve
/// flags across the whole (possibly multi-component, possibly empty ==
/// share root) path in one atomic kernel resolution. Tries each Unicode
/// candidate spelling of the path in turn.
fn resolve_dir(
    anchor: &AnchorHandle,
    comps: &[CompactString],
    policy: &SharePolicy,
) -> Result<OwnedFd, VfsError> {
    let resolve = resolve_flags(policy);
    let mut last = VfsError::NotFound;
    for cand in path_candidates(comps) {
        match fs::openat2(
            &anchor.0,
            cand.as_str(),
            OFlags::PATH | OFlags::DIRECTORY | OFlags::CLOEXEC,
            Mode::empty(),
            resolve,
        ) {
            Ok(fd) => return Ok(fd),
            Err(Errno::NOENT) | Err(Errno::NOTDIR) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

/// Split a non-root path into `(parent components, leaf name)`. Panics if
/// `comps` is empty — callers must special-case the share root themselves.
fn split_leaf(comps: &[CompactString]) -> (&[CompactString], &str) {
    let n = comps.len();
    (&comps[..n - 1], comps[n - 1].as_str())
}

pub(crate) fn open_anchor(
    host_path: &Path,
    _policy: &SharePolicy,
) -> Result<(AnchorHandle, u64, FsType), VfsError> {
    let fd = fs::open(
        host_path,
        OFlags::PATH | OFlags::DIRECTORY | OFlags::CLOEXEC,
        Mode::empty(),
    )
    .map_err(map_errno)?;
    let st = fs::statx(&fd, "", AtFlags::EMPTY_PATH, STAT_MASK).map_err(map_errno)?;
    let dev = fs::makedev(st.stx_dev_major, st.stx_dev_minor);
    // fstatfs is documented to work on O_PATH descriptors for the purpose of
    // identifying the filesystem; if a given kernel/fs combination disagrees
    // we fall back to `Other(0)` rather than failing share registration.
    let fstype = fs::fstatfs(&fd)
        .map(|s| FsType::from_statfs_magic(s.f_type as u64))
        .unwrap_or(FsType::Other(0));
    Ok((AnchorHandle(fd), dev, fstype))
}

pub(crate) fn stat_path(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
) -> Result<Stat, VfsError> {
    if p.is_empty() {
        let st = fs::statx(&anchor.0, "", AtFlags::EMPTY_PATH, STAT_MASK).map_err(map_errno)?;
        return Ok(statx_to_stat(&st));
    }
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let resolve = resolve_flags(policy);
    let mut last = VfsError::NotFound;
    for cand in lookup_candidates(leaf) {
        match fs::openat2(
            &parent_fd,
            cand.as_str(),
            OFlags::PATH | OFlags::CLOEXEC,
            Mode::empty(),
            resolve,
        ) {
            Ok(fd) => {
                let st = fs::statx(&fd, "", AtFlags::EMPTY_PATH, STAT_MASK).map_err(map_errno)?;
                return Ok(statx_to_stat(&st));
            }
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn open_dir(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
) -> Result<DirInner, VfsError> {
    let resolve = resolve_flags(policy);
    if p.is_empty() {
        let fd = fs::openat2(
            &anchor.0,
            ".",
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC,
            Mode::empty(),
            resolve,
        )
        .map_err(map_errno)?;
        return Ok(DirInner(fd));
    }
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let mut last = VfsError::NotFound;
    for cand in lookup_candidates(leaf) {
        match fs::openat2(
            &parent_fd,
            cand.as_str(),
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC,
            Mode::empty(),
            resolve,
        ) {
            Ok(fd) => return Ok(DirInner(fd)),
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn open_read(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
) -> Result<FileInner, VfsError> {
    let resolve = resolve_flags(policy);
    let (parent_fd, leaf_candidates): (OwnedFd, Vec<String>) = if p.is_empty() {
        let fd = fs::openat2(
            &anchor.0,
            ".",
            OFlags::PATH | OFlags::DIRECTORY | OFlags::CLOEXEC,
            Mode::empty(),
            resolve,
        )
        .map_err(map_errno)?;
        (fd, vec![".".to_string()])
    } else {
        let (parent_comps, leaf) = split_leaf(p.components());
        let fd = resolve_dir(anchor, parent_comps, policy)?;
        (fd, lookup_candidates(leaf).into_vec())
    };
    let mut last = VfsError::NotFound;
    for cand in leaf_candidates {
        // Try read-write first so the same handle can serve `write_at`
        // (e.g. finishing an upload's sparse allocation) without a second
        // open; if the file's permissions only allow reading, fall back so
        // plain reads still work for the common read-only case.
        let fd = fs::openat2(
            &parent_fd,
            cand.as_str(),
            OFlags::RDWR | OFlags::CLOEXEC,
            Mode::empty(),
            resolve,
        )
        .or_else(|e| {
            if e == Errno::ACCESS || e == Errno::PERM {
                fs::openat2(
                    &parent_fd,
                    cand.as_str(),
                    OFlags::RDONLY | OFlags::CLOEXEC,
                    Mode::empty(),
                    resolve,
                )
            } else {
                Err(e)
            }
        });
        match fd {
            Ok(fd) => return Ok(FileInner(fd)),
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn create_excl(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
    mode: u32,
) -> Result<FileInner, VfsError> {
    if p.is_empty() {
        return Err(VfsError::AlreadyExists);
    }
    let resolve = resolve_flags(policy);
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let leaf_nfc = normalize_new_name(leaf);
    let m = Mode::from_raw_mode(mode as fs::RawMode);
    let fd = fs::openat2(
        &parent_fd,
        leaf_nfc.as_str(),
        // `RDWR`, not `WRONLY`: the returned handle is the same `FileHandle`
        // `open_read` hands back, and callers read through it — `pread` on a
        // write-only fd is EBADF. `sc-upload` creates the part file here, then
        // verifies its digest through the cached handle at finalize; on Linux
        // that read failed unconditionally. Windows compiles `portable`, which
        // has always opened `read(true).write(true)`.
        OFlags::CREATE | OFlags::EXCL | OFlags::RDWR | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        m,
        resolve,
    )
    .map_err(map_errno)?;
    // Apply the exact mode regardless of umask (mode_*
    // is applied verbatim, not filtered through umask).
    let _ = fs::fchmod(&fd, m);
    Ok(FileInner(fd))
}

pub(crate) fn mkdir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    if p.is_empty() {
        return Err(VfsError::AlreadyExists);
    }
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let leaf_nfc = normalize_new_name(leaf);
    let m = Mode::from_raw_mode(policy.mode_dir as fs::RawMode);
    fs::mkdirat(&parent_fd, leaf_nfc.as_str(), m).map_err(map_errno)?;
    let _ = fs::chmodat(&parent_fd, leaf_nfc.as_str(), m, AtFlags::empty());
    if let Some((uid, gid)) = policy.chown {
        // SAFETY: `uid`/`gid` are opaque numeric identifiers taken from the
        // admin-configured `SharePolicy`, not derived from untrusted pointer
        // arithmetic; `from_raw` just wraps them, it doesn't dereference.
        let owner = unsafe { fs::Uid::from_raw(uid) };
        let group = unsafe { fs::Gid::from_raw(gid) };
        let _ = fs::chownat(
            &parent_fd,
            leaf_nfc.as_str(),
            Some(owner),
            Some(group),
            AtFlags::SYMLINK_NOFOLLOW,
        );
    }
    Ok(())
}

pub(crate) fn unlink(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    if p.is_empty() {
        return Err(VfsError::PermissionDenied);
    }
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let mut last = VfsError::NotFound;
    for cand in lookup_candidates(leaf) {
        match fs::unlinkat(&parent_fd, cand.as_str(), AtFlags::empty()) {
            Ok(()) => return Ok(()),
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn rmdir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    if p.is_empty() {
        return Err(VfsError::PermissionDenied);
    }
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let mut last = VfsError::NotFound;
    for cand in lookup_candidates(leaf) {
        match fs::unlinkat(&parent_fd, cand.as_str(), AtFlags::REMOVEDIR) {
            Ok(()) => return Ok(()),
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn rename(
    anchor: &AnchorHandle,
    from: &SafePath,
    to: &SafePath,
    policy: &SharePolicy,
    no_replace: bool,
) -> Result<(), VfsError> {
    if from.is_empty() || to.is_empty() {
        return Err(VfsError::PermissionDenied);
    }
    let (from_parent_comps, from_leaf) = split_leaf(from.components());
    let (to_parent_comps, to_leaf) = split_leaf(to.components());
    let from_parent_fd = resolve_dir(anchor, from_parent_comps, policy)?;
    let to_parent_fd = resolve_dir(anchor, to_parent_comps, policy)?;
    let to_leaf_nfc = normalize_new_name(to_leaf);
    let flags = if no_replace {
        fs::RenameFlags::NOREPLACE
    } else {
        fs::RenameFlags::empty()
    };
    let mut last = VfsError::NotFound;
    for cand in lookup_candidates(from_leaf) {
        match fs::renameat_with(
            &from_parent_fd,
            cand.as_str(),
            &to_parent_fd,
            to_leaf_nfc.as_str(),
            flags,
        ) {
            Ok(()) => return Ok(()),
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(Errno::NOSYS) if flags.is_empty() => {
                // No renameat2 on this kernel/fs; plain renameat has the
                // same semantics when we didn't need RENAME_NOREPLACE.
                fs::renameat(&from_parent_fd, cand.as_str(), &to_parent_fd, to_leaf_nfc.as_str())
                    .map_err(map_errno)?;
                return Ok(());
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn set_times(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
    mtime_ns: i128,
) -> Result<(), VfsError> {
    if p.is_empty() {
        return Err(VfsError::PermissionDenied);
    }
    let (parent_comps, leaf) = split_leaf(p.components());
    let parent_fd = resolve_dir(anchor, parent_comps, policy)?;
    let secs = mtime_ns.div_euclid(1_000_000_000);
    let nsecs = mtime_ns.rem_euclid(1_000_000_000);
    let times = fs::Timestamps {
        last_access: fs::Timespec {
            tv_sec: 0,
            tv_nsec: fs::UTIME_OMIT,
        },
        last_modification: fs::Timespec {
            tv_sec: secs as _,
            tv_nsec: nsecs as _,
        },
    };
    let mut last = VfsError::NotFound;
    for cand in lookup_candidates(leaf) {
        match fs::utimensat(&parent_fd, cand.as_str(), &times, AtFlags::SYMLINK_NOFOLLOW) {
            Ok(()) => return Ok(()),
            Err(Errno::NOENT) => {
                last = VfsError::NotFound;
                continue;
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Err(last)
}

pub(crate) fn read_dir(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
) -> Result<Vec<DirEntry>, VfsError> {
    let dir = open_dir(anchor, p, policy)?;
    dir_read_entries(&dir)
}

pub(crate) fn statfs_free(anchor: &AnchorHandle) -> Result<(u64, u64), VfsError> {
    let st = fs::fstatfs(&anchor.0).map_err(map_errno)?;
    let bsize = st.f_bsize as u64;
    let free = (st.f_bfree as u64).saturating_mul(bsize);
    let total = (st.f_blocks as u64).saturating_mul(bsize);
    Ok((free, total))
}

pub(crate) fn file_stat(f: &FileInner) -> Result<Stat, VfsError> {
    let st = fs::statx(&f.0, "", AtFlags::EMPTY_PATH, STAT_MASK).map_err(map_errno)?;
    Ok(statx_to_stat(&st))
}

pub(crate) fn file_read_at(f: &FileInner, buf: &mut [u8], off: u64) -> Result<usize, VfsError> {
    rustix::io::pread(&f.0, buf, off).map_err(map_errno)
}

pub(crate) fn file_write_at(f: &FileInner, buf: &[u8], off: u64) -> Result<usize, VfsError> {
    rustix::io::pwrite(&f.0, buf, off).map_err(map_errno)
}

pub(crate) fn file_set_len(f: &FileInner, n: u64) -> Result<(), VfsError> {
    fs::ftruncate(&f.0, n).map_err(map_errno)
}

pub(crate) fn file_sync_data(f: &FileInner) -> Result<(), VfsError> {
    fs::fdatasync(&f.0).map_err(map_errno)
}

/// `fsync` on a *directory* fd — makes the entries in it durable, which is a
/// separate act from making a file's contents durable.
///
/// `fdatasync` on the staging file only guarantees the bytes. The `rename`
/// that publishes those bytes under the final name is a directory operation,
/// and on ext4/XFS it can still be sitting in the journal when the power
/// goes: the file survives with its full contents under a `.scpart-` name
/// nobody will ever look for. This is the second half of every
/// write-then-rename in `sc_core::ops`.
///
/// Full `fsync`, not `fdatasync`: a directory's "data" *is* its metadata.
pub(crate) fn sync_dir(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
) -> Result<(), VfsError> {
    // `open_dir`, not `resolve_dir`: the latter hands back an `O_PATH`
    // descriptor, and `fsync` on `O_PATH` is EBADF (open(2)) — never a
    // real-world durability failure, always an unconditional one. Every
    // write-then-rename in `sc_core::ops` ends here, so on Linux every write
    // returned `Io(EBADF)`; Windows compiles `portable` instead and could not
    // see it.
    let dir = open_dir(anchor, p, policy)?;
    fs::fsync(&dir.0).map_err(map_errno)
}

/// `copy_file_range`: reflink on btrfs/XFS when block-aligned, an in-kernel copy
/// otherwise — either way no userspace round trip. Always passes explicit
/// offsets rather than `None`: `FileInner`'s fd is shared/positional
/// (`pread`/`pwrite` throughout this backend), so letting the syscall
/// advance the fd's own kernel-side cursor would corrupt whichever other
/// caller reads that cursor next.
///
/// Loops because a single call may return a short copy even on success
/// (the man page documents this explicitly, independent of any error), and
/// falls back to the shared buffered loop (`crate::copy`) for the *rest* of
/// the request — not the whole thing — the moment the kernel primitive
/// reports one of the three documented "can't do this here" errors:
/// - `EXDEV`: source and destination aren't on the same filesystem. Per
///   `DEPLOYMENT.md` §4 this can happen *inside* a single share, when a
///   subdirectory is a separate bind mount, so it is an expected outcome to
///   handle, not a bug to propagate.
/// - `EOPNOTSUPP`: the underlying filesystem doesn't implement it.
/// - `ENOSYS`: kernel predates the syscall (< 4.5).
///
/// Any other error is real and is surfaced, not swallowed into a fallback.
pub(crate) fn file_copy_range(src: &FileInner, src_off: u64, dst: &FileInner, dst_off: u64, len: u64) -> Result<u64, VfsError> {
    if len == 0 {
        return Ok(0);
    }
    let mut copied = 0u64;
    while copied < len {
        let remaining = len - copied;
        let want = usize::try_from(remaining).unwrap_or(usize::MAX);
        let mut off_in = src_off + copied;
        let mut off_out = dst_off + copied;
        match fs::copy_file_range(&src.0, Some(&mut off_in), &dst.0, Some(&mut off_out), want) {
            Ok(0) => break, // source exhausted before `len` was reached
            Ok(n) => copied += n as u64,
            Err(Errno::XDEV) | Err(Errno::OPNOTSUPP) | Err(Errno::NOSYS) => {
                let rest = crate::copy::buffered_copy_range(
                    |buf, off| file_read_at(src, buf, off),
                    |buf, off| file_write_at(dst, buf, off),
                    src_off + copied,
                    dst_off + copied,
                    len - copied,
                )?;
                return Ok(copied + rest);
            }
            Err(e) => return Err(map_errno(e)),
        }
    }
    Ok(copied)
}

/// `fchmod` on an already-open handle.
///
/// Needed when *replacing* an existing file: the replacement must inherit the
/// original's mode before the rename, otherwise the other services sharing the
/// directory (Jellyfin, *arr, rsync) lose access to a file they could read a
/// moment earlier. `ARCHITECTURE.md` §5.2 makes this a hard requirement, not a
/// nicety — it is one of the things that separates this design from the ones
/// that treat the share as private storage.
pub(crate) fn file_set_mode(f: &FileInner, mode: u32) -> Result<(), VfsError> {
    fs::fchmod(&f.0, Mode::from_bits_truncate(mode)).map_err(map_errno)
}

/// `fchown` on an already-open handle. Same rationale as `file_set_mode`.
///
/// Fails with `EPERM` for an unprivileged process trying to change the owning
/// user; that is surfaced rather than swallowed so the caller can decide
/// whether it matters (transplanting gid alone is often enough).
pub(crate) fn file_set_owner(f: &FileInner, uid: u32, gid: u32) -> Result<(), VfsError> {
    // SAFETY: `Uid`/`Gid` are plain numeric newtypes; any u32 is a valid raw
    // id as far as the kernel interface is concerned. The constructors are
    // `unsafe` only because rustix cannot verify the id actually exists.
    let (u, g) = unsafe {
        (
            rustix::process::Uid::from_raw(uid),
            rustix::process::Gid::from_raw(gid),
        )
    };
    fs::fchown(&f.0, Some(u), Some(g)).map_err(map_errno)
}

thread_local! {
    /// Set only by `read_dir_control`. Lets trusted internal callers (the
    /// upload orphan sweep, trash GC) enumerate our own control files, which
    /// `read_dir` deliberately hides from everyone else.
    pub(crate) static INCLUDE_RESERVED: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

pub(crate) fn dir_read_entries(d: &DirInner) -> Result<Vec<DirEntry>, VfsError> {
    let iter = fs::Dir::read_from(&d.0).map_err(map_errno)?;
    let mut out = Vec::new();
    for entry in iter {
        let entry = entry.map_err(map_errno)?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name == "." || name == ".." {
            continue;
        }
        if !INCLUDE_RESERVED.with(|c| c.get()) && is_reserved_name(&name) {
            continue;
        }
        // `DT_UNKNOWN` (some filesystems/FUSE) is conservatively reported as
        // `Kind::Other` rather than paying for a `statx` per entry here;
        // callers that need certainty can `stat()` the specific entry.
        let kind = match entry.file_type() {
            fs::FileType::Directory => Kind::Dir,
            fs::FileType::Symlink => Kind::Symlink,
            fs::FileType::RegularFile => Kind::File,
            _ => Kind::Other,
        };
        out.push(DirEntry {
            name: CompactString::new(&name),
            kind,
            // getdents64 already returned d_ino, so this costs nothing and
            // lets callers order a later stat batch by (dev, ino).
            ino: Some(entry.ino()),
        });
    }
    Ok(out)
}
