//! Portable dev-convenience backend (Windows / macOS / other Unix).
//!
//! This is **not** the hardened path — that's `linux.rs`, which uses
//! `openat2(RESOLVE_BENEATH)` so the kernel enforces escape prevention
//! atomically. Here we don't have that syscall, so we walk the path
//! component by component from the anchor, checking `symlink_metadata` at
//! every step and rejecting escapes ourselves. It exists so the crate
//! builds, and its security-invariant tests pass, on the primary dev
//! machine (Windows). See `ARCHITECTURE.md` "settled assumptions": Windows/macOS are
//! dev-convenience fallbacks, not a supported deployment target.

use std::path::{Path, PathBuf};

use compact_str::CompactString;

use crate::error::VfsError;
use crate::safe_path::{lookup_candidates, normalize_new_name};
use crate::types::{DirEntry, FsType, Kind, SharePolicy, Stat, SymlinkPolicy};
use crate::SafePath;

pub(crate) struct AnchorHandle(PathBuf);
pub(crate) struct FileInner(std::fs::File);
pub(crate) struct DirInner(PathBuf);

/// Look up `want` among the entries of `dir` on disk, trying the exact
/// bytes given, then NFC, then NFD (see `safe_path::lookup_candidates`).
/// Returns the *actual* on-disk spelling so callers never rewrite existing
/// names.
fn find_entry(dir: &Path, want: &str) -> Result<String, VfsError> {
    let candidates = lookup_candidates(want);
    let rd = std::fs::read_dir(dir).map_err(VfsError::from_io)?;
    let mut names = Vec::new();
    for entry in rd {
        let entry = entry.map_err(VfsError::from_io)?;
        names.push(entry.file_name());
    }
    for cand in &candidates {
        for name in &names {
            if name.to_string_lossy() == cand.as_str() {
                return Ok(name.to_string_lossy().into_owned());
            }
        }
    }
    Err(VfsError::NotFound)
}

/// Walk `comps` from `root`, requiring every component (including the last)
/// to already exist, and applying `policy.symlink` at every step —
/// component-by-component, never a naive joined-string-then-check (that's
/// the TOCTOU-prone pattern `ARCHITECTURE.md` §0.2 forbids).
fn resolve_existing(
    root: &Path,
    comps: &[CompactString],
    policy: &SharePolicy,
) -> Result<PathBuf, VfsError> {
    let mut cur = root.to_path_buf();
    for comp in comps {
        let actual = find_entry(&cur, comp.as_str())?;
        let candidate = cur.join(&actual);
        let md = std::fs::symlink_metadata(&candidate).map_err(VfsError::from_io)?;
        if md.file_type().is_symlink() {
            match policy.symlink {
                SymlinkPolicy::Deny => return Err(VfsError::SymlinkDenied),
                SymlinkPolicy::WithinShare => {
                    let real_target = std::fs::canonicalize(&candidate).map_err(VfsError::from_io)?;
                    let real_root = std::fs::canonicalize(root).map_err(VfsError::from_io)?;
                    if !real_target.starts_with(&real_root) {
                        return Err(VfsError::SymlinkDenied);
                    }
                }
                SymlinkPolicy::Follow => {}
            }
        }
        cur = candidate;
    }
    Ok(cur)
}

/// Resolve the parent of `p` (must exist) and NFC-normalize the new leaf
/// name (creation always forces NFC — see `DESIGN-CORE.md` §2).
fn resolve_parent_for_create(
    root: &Path,
    p: &SafePath,
    policy: &SharePolicy,
) -> Result<(PathBuf, String), VfsError> {
    let name = p.name().ok_or(VfsError::AlreadyExists)?; // creating "the root" makes no sense
    let parent = p.parent();
    let parent_path = resolve_existing(root, parent.components(), policy)?;
    Ok((parent_path, normalize_new_name(name)))
}

/// `(volume serial number, NTFS file index)` for `path` — the Windows
/// analogue of `statx`'s `(dev, ino)`, gone through `GetFileInformationByHandle`
/// (via the raw Win32 call in [`win`], same pattern as [`win::free_bytes`])
/// rather than `MetadataExt::{volume_serial_number, file_index}`, which are
/// still gated behind the unstable `windows_by_handle` feature
/// (rust-lang/rust#63010) as of this toolchain — confirmed by hand, not
/// assumed, since the comment this replaced asserted the same thing without
/// a re-check date.
///
/// This *is* load-bearing now, unlike the `(0, 0)` placeholder it replaces:
/// `sc-meta`'s node identity is `(share, dev, ino, btime_ns)`
/// (`crates/sc-meta/src/lib.rs`'s `node_ident` unique index), and with
/// `dev`/`ino` pinned to a constant the only thing distinguishing two nodes
/// in the same share was `btime_ns` — which two different directories can
/// share (same creation tick, or both simply never having had a btime the
/// filesystem chose to report), and did in practice: a share's root and one
/// of its own subdirectories were observed reporting the *same* `oc:fileid`
/// over WebDAV. A sync client keys its whole local sync journal on that
/// id; two resources sharing one is not a cosmetic wire defect, it looks
/// like "these are the same file" to the client.
#[cfg(windows)]
fn dev_ino_of_path(path: &Path) -> (u64, u64) {
    win::file_identity(path).unwrap_or((0, 0))
}

/// Same identity lookup, but from a handle this backend already has open
/// (`file_stat`) — avoids a second `CreateFileW` round trip for a file we
/// already hold.
#[cfg(windows)]
fn dev_ino_of_handle(f: &std::fs::File) -> (u64, u64) {
    use std::os::windows::io::AsRawHandle;
    win::file_identity_of_handle(f.as_raw_handle()).unwrap_or((0, 0))
}

#[cfg(unix)]
fn dev_ino(md: &std::fs::Metadata) -> (u64, u64) {
    use std::os::unix::fs::MetadataExt;
    (md.dev(), md.ino())
}

#[cfg(unix)]
fn platform_bits(md: &std::fs::Metadata) -> (u32, u32, u32, u32) {
    use std::os::unix::fs::MetadataExt;
    (md.mode(), md.uid(), md.gid(), md.nlink() as u32)
}

#[cfg(windows)]
fn platform_bits(md: &std::fs::Metadata) -> (u32, u32, u32, u32) {
    // Windows has no POSIX mode bits. Approximate: directories 0755, files
    // 0644, minus write bits when the read-only attribute is set. Good
    // enough for the dev fallback; the real permission model lives on the
    // Linux backend.
    let mut mode = if md.is_dir() { 0o040755 } else { 0o100644 };
    if md.permissions().readonly() {
        mode &= !0o222;
    }
    (mode, 0, 0, 1)
}

fn system_time_to_ns(t: std::time::SystemTime) -> i128 {
    match t.duration_since(std::time::UNIX_EPOCH) {
        Ok(d) => d.as_nanos() as i128,
        Err(e) => -(e.duration().as_nanos() as i128),
    }
}

/// `(dev, ino)` is taken as a parameter rather than derived internally: on
/// Unix it comes straight off `md` (cheap, no extra syscall — see the
/// `dev_ino` call sites below), but on Windows it needs either a path to
/// open a fresh identity handle for (`dev_ino_of_path`) or an
/// already-open one (`dev_ino_of_handle`) — two different things a caller
/// might have that `Metadata` alone never carries, so each call site
/// computes its own and hands the result in rather than this function
/// guessing which is available.
fn metadata_to_stat(md: &std::fs::Metadata, dev: u64, ino: u64) -> Stat {
    let kind = if md.file_type().is_symlink() {
        Kind::Symlink
    } else if md.is_dir() {
        Kind::Dir
    } else if md.is_file() {
        Kind::File
    } else {
        Kind::Other
    };
    let mtime_ns = md
        .modified()
        .ok()
        .map(system_time_to_ns)
        .unwrap_or(0);
    let btime_ns = md.created().ok().map(system_time_to_ns);
    let (mode, uid, gid, nlink) = platform_bits(md);
    Stat {
        dev,
        ino,
        btime_ns,
        mtime_ns,
        size: md.len(),
        mode,
        uid,
        gid,
        nlink,
        kind,
    }
}

/// `(dev, ino)` for a path this backend has already `stat`ed, unifying the
/// two platforms' different inputs (Unix reads it straight off `Metadata`;
/// Windows needs the path itself to open a fresh identity handle —
/// `dev_ino_of_path`'s doc comment) behind one call-site-agnostic name, so
/// `open_anchor` and `stat_path` below don't each need their own `#[cfg]`.
#[cfg(unix)]
fn stat_identity(_path: &Path, md: &std::fs::Metadata) -> (u64, u64) {
    dev_ino(md)
}

#[cfg(windows)]
fn stat_identity(path: &Path, _md: &std::fs::Metadata) -> (u64, u64) {
    dev_ino_of_path(path)
}

pub(crate) fn open_anchor(
    host_path: &Path,
    _policy: &SharePolicy,
) -> Result<(AnchorHandle, u64, FsType), VfsError> {
    let canon = std::fs::canonicalize(host_path).map_err(VfsError::from_io)?;
    let md = std::fs::metadata(&canon).map_err(VfsError::from_io)?;
    if !md.is_dir() {
        return Err(VfsError::InvalidName("share host_path is not a directory"));
    }
    let (dev, _ino) = stat_identity(&canon, &md);
    // No portable `statfs`-equivalent in std; the filesystem-type gate is a
    // Linux-only concern (see `types::FsType`), so this is always `Other(0)`
    // off the deployment target.
    Ok((AnchorHandle(canon), dev, FsType::Other(0)))
}

pub(crate) fn stat_path(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<Stat, VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    // lstat-like: report the entry itself (symlink or not), matching the
    // Linux backend's `statx(..., AT_SYMLINK_NOFOLLOW-by-resolve-flags)`.
    let md = std::fs::symlink_metadata(&path).map_err(VfsError::from_io)?;
    let (dev, ino) = stat_identity(&path, &md);
    Ok(metadata_to_stat(&md, dev, ino))
}

pub(crate) fn open_dir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<DirInner, VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    let md = std::fs::metadata(&path).map_err(VfsError::from_io)?;
    if !md.is_dir() {
        return Err(VfsError::Io(std::io::Error::other("not a directory")));
    }
    Ok(DirInner(path))
}

pub(crate) fn open_read(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<FileInner, VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    // Try read-write first (so `write_at` also works through this handle),
    // fall back to read-only for files we can't write.
    let file = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open(&path)
        .or_else(|_| std::fs::OpenOptions::new().read(true).open(&path))
        .map_err(VfsError::from_io)?;
    Ok(FileInner(file))
}

pub(crate) fn create_excl(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
    mode: u32,
) -> Result<FileInner, VfsError> {
    let (parent, leaf_nfc) = resolve_parent_for_create(&anchor.0, p, policy)?;
    let target = parent.join(&leaf_nfc);
    let mut opts = std::fs::OpenOptions::new();
    opts.read(true).write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(mode);
    }
    #[cfg(not(unix))]
    {
        let _ = mode; // no POSIX mode bits on this platform
    }
    let file = opts.open(&target).map_err(VfsError::from_io)?;
    Ok(FileInner(file))
}

pub(crate) fn mkdir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    let (parent, leaf_nfc) = resolve_parent_for_create(&anchor.0, p, policy)?;
    let target = parent.join(&leaf_nfc);
    std::fs::create_dir(&target).map_err(VfsError::from_io)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&target, std::fs::Permissions::from_mode(policy.mode_dir));
    }
    Ok(())
}

pub(crate) fn unlink(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    std::fs::remove_file(&path).map_err(VfsError::from_io)
}

pub(crate) fn rmdir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    std::fs::remove_dir(&path).map_err(VfsError::from_io)
}

pub(crate) fn rename(
    anchor: &AnchorHandle,
    from: &SafePath,
    to: &SafePath,
    policy: &SharePolicy,
    no_replace: bool,
) -> Result<(), VfsError> {
    let from_path = resolve_existing(&anchor.0, from.components(), policy)?;
    let (to_parent, to_leaf_nfc) = resolve_parent_for_create(&anchor.0, to, policy)?;
    let to_path = to_parent.join(&to_leaf_nfc);
    if no_replace {
        // Best-effort only: unlike Linux's `RENAME_NOREPLACE`, there is no
        // atomic compare-and-rename in std, so a window remains between
        // this check and the rename below (documented limitation, mirrors
        // `DESIGN-CORE.md` §5.5's optimistic-concurrency caveat).
        if std::fs::symlink_metadata(&to_path).is_ok() {
            return Err(VfsError::AlreadyExists);
        }
    }
    std::fs::rename(&from_path, &to_path).map_err(VfsError::from_io)
}

pub(crate) fn set_times(
    anchor: &AnchorHandle,
    p: &SafePath,
    policy: &SharePolicy,
    mtime_ns: i128,
) -> Result<(), VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    let secs = mtime_ns.div_euclid(1_000_000_000) as i64;
    let nanos = mtime_ns.rem_euclid(1_000_000_000) as u32;
    let ft = filetime::FileTime::from_unix_time(secs, nanos);
    filetime::set_file_mtime(&path, ft).map_err(VfsError::from_io)
}

pub(crate) fn read_dir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<Vec<DirEntry>, VfsError> {
    let dir = open_dir(anchor, p, policy)?;
    dir_read_entries(&dir)
}

thread_local! {
    /// Set only by `read_dir_control`. Lets trusted internal callers (the
    /// upload orphan sweep, trash GC) enumerate our own control files, which
    /// `read_dir` deliberately hides from everyone else.
    pub(crate) static INCLUDE_RESERVED: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

pub(crate) fn dir_read_entries(d: &DirInner) -> Result<Vec<DirEntry>, VfsError> {
    let rd = std::fs::read_dir(&d.0).map_err(VfsError::from_io)?;
    let mut out = Vec::new();
    for entry in rd {
        let entry = entry.map_err(VfsError::from_io)?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if !INCLUDE_RESERVED.with(|c| c.get()) && crate::reserved::is_reserved_name(&name) {
            continue;
        }
        // `file_type()` reflects the entry itself (does not follow
        // symlinks), matching the Linux backend's `d_type` behavior.
        let kind = match entry.file_type() {
            Ok(ft) if ft.is_symlink() => Kind::Symlink,
            Ok(ft) if ft.is_dir() => Kind::Dir,
            Ok(ft) if ft.is_file() => Kind::File,
            _ => Kind::Other,
        };
        out.push(DirEntry {
            name: CompactString::new(&name),
            kind,
            // std::fs::read_dir exposes no inode portably. Callers must read
            // None as "unknown" and fall back to readdir order.
            ino: None,
        });
    }
    Ok(out)
}

#[cfg(unix)]
fn handle_identity(_f: &FileInner, md: &std::fs::Metadata) -> (u64, u64) {
    dev_ino(md)
}

#[cfg(windows)]
fn handle_identity(f: &FileInner, _md: &std::fs::Metadata) -> (u64, u64) {
    dev_ino_of_handle(&f.0)
}

pub(crate) fn file_stat(f: &FileInner) -> Result<Stat, VfsError> {
    let md = f.0.metadata().map_err(VfsError::from_io)?;
    let (dev, ino) = handle_identity(f, &md);
    Ok(metadata_to_stat(&md, dev, ino))
}

pub(crate) fn file_read_at(f: &FileInner, buf: &mut [u8], off: u64) -> Result<usize, VfsError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::FileExt;
        f.0.read_at(buf, off).map_err(VfsError::from_io)
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::FileExt;
        f.0.seek_read(buf, off).map_err(VfsError::from_io)
    }
}

pub(crate) fn file_write_at(f: &FileInner, buf: &[u8], off: u64) -> Result<usize, VfsError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::FileExt;
        f.0.write_at(buf, off).map_err(VfsError::from_io)
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::FileExt;
        f.0.seek_write(buf, off).map_err(VfsError::from_io)
    }
}

pub(crate) fn file_set_len(f: &FileInner, n: u64) -> Result<(), VfsError> {
    f.0.set_len(n).map_err(VfsError::from_io)
}

pub(crate) fn file_sync_data(f: &FileInner) -> Result<(), VfsError> {
    f.0.sync_data().map_err(VfsError::from_io)
}

/// Make a directory's entries durable — see `linux::sync_dir` for why a
/// write-then-rename needs this on top of syncing the file itself.
///
/// Unix opens the directory and `fsync`s it, same as the Linux backend does
/// through `openat2`. Windows has no equivalent: `File::open` refuses a
/// directory without `FILE_FLAG_BACKUP_SEMANTICS`, and NTFS's own metadata
/// journal is what orders a rename against a crash there. So it is a no-op
/// rather than an error — this backend is the dev fallback (see the module
/// doc), and refusing every copy on Windows to signal a durability property
/// Windows provides differently would be the worse trade.
pub(crate) fn sync_dir(anchor: &AnchorHandle, p: &SafePath, policy: &SharePolicy) -> Result<(), VfsError> {
    let path = resolve_existing(&anchor.0, p.components(), policy)?;
    #[cfg(unix)]
    {
        let d = std::fs::File::open(&path).map_err(VfsError::from_io)?;
        d.sync_all().map_err(VfsError::from_io)
    }
    #[cfg(windows)]
    {
        let _ = path;
        Ok(())
    }
}

/// No `copy_file_range`-equivalent off Linux (see the module doc): this
/// backend is always the buffered fallback, routed through the single
/// shared implementation in `crate::copy` rather than a copy of the loop
/// living here too.
pub(crate) fn file_copy_range(src: &FileInner, src_off: u64, dst: &FileInner, dst_off: u64, len: u64) -> Result<u64, VfsError> {
    crate::copy::buffered_copy_range(
        |buf, off| file_read_at(src, buf, off),
        |buf, off| file_write_at(dst, buf, off),
        src_off,
        dst_off,
        len,
    )
}

/// Set the file mode on an already-open handle.
///
/// Used when *replacing* an existing file: the replacement has to inherit the
/// original's permissions before the rename, or the other services sharing the
/// directory (Jellyfin, *arr, rsync) suddenly lose access to a file they could
/// read a moment ago. See `ARCHITECTURE.md` §5.2.
#[cfg(unix)]
pub(crate) fn file_set_mode(f: &FileInner, mode: u32) -> Result<(), VfsError> {
    use std::os::unix::fs::PermissionsExt;
    f.0.set_permissions(std::fs::Permissions::from_mode(mode))
        .map_err(VfsError::from_io)
}

/// Windows has no POSIX mode bits. This is a deliberate no-op rather than an
/// error: the dev host must not fail a code path that is correct on the
/// deployment target, and callers legitimately transplant metadata on both.
#[cfg(windows)]
pub(crate) fn file_set_mode(_f: &FileInner, _mode: u32) -> Result<(), VfsError> {
    Ok(())
}

/// Set the owning uid/gid on an already-open handle. Same rationale as
/// `file_set_mode`. Requires privilege on real Unix; a failure here is
/// reported rather than swallowed so the caller can decide.
#[cfg(all(unix, not(target_os = "linux")))]
pub(crate) fn file_set_owner(_f: &FileInner, _uid: u32, _gid: u32) -> Result<(), VfsError> {
    // Non-Linux Unix is a dev-convenience host only (see statfs_free above).
    Ok(())
}

#[cfg(windows)]
pub(crate) fn file_set_owner(_f: &FileInner, _uid: u32, _gid: u32) -> Result<(), VfsError> {
    Ok(())
}

#[cfg(windows)]
pub(crate) fn statfs_free(anchor: &AnchorHandle) -> Result<(u64, u64), VfsError> {
    win::free_bytes(&anchor.0)
}

#[cfg(all(unix, not(target_os = "linux")))]
pub(crate) fn statfs_free(_anchor: &AnchorHandle) -> Result<(u64, u64), VfsError> {
    // Best-effort dev fallback only: real free-space accounting for
    // non-Linux Unix hosts is out of scope (the deployment target is Linux
    // — see `ARCHITECTURE.md` "settled assumptions").
    Ok((0, 0))
}

#[cfg(windows)]
mod win {
    use std::ffi::c_void;
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::io::RawHandle;
    use std::path::Path;

    use crate::error::VfsError;

    #[link(name = "kernel32")]
    extern "system" {
        fn GetDiskFreeSpaceExW(
            lp_directory_name: *const u16,
            lp_free_bytes_available: *mut u64,
            lp_total_number_of_bytes: *mut u64,
            lp_total_number_of_free_bytes: *mut u64,
        ) -> i32;
        fn CreateFileW(
            lp_file_name: *const u16,
            dw_desired_access: u32,
            dw_share_mode: u32,
            lp_security_attributes: *mut c_void,
            dw_creation_disposition: u32,
            dw_flags_and_attributes: u32,
            h_template_file: *mut c_void,
        ) -> *mut c_void;
        fn CloseHandle(h_object: *mut c_void) -> i32;
        fn GetFileInformationByHandle(h_file: *mut c_void, lp_file_information: *mut ByHandleFileInformation) -> i32;
    }

    pub(super) fn free_bytes(path: &Path) -> Result<(u64, u64), VfsError> {
        let wide: Vec<u16> = path
            .as_os_str()
            .encode_wide()
            .chain(std::iter::once(0))
            .collect();
        let mut free_available: u64 = 0;
        let mut total_bytes: u64 = 0;
        let mut total_free: u64 = 0;
        // SAFETY: `wide` is a NUL-terminated UTF-16 buffer kept alive for the
        // duration of the call. The three out-parameters point at valid,
        // aligned, writable `u64` locals owned by this stack frame. This
        // matches the documented `GetDiskFreeSpaceExW` FFI contract.
        let ok = unsafe {
            GetDiskFreeSpaceExW(
                wide.as_ptr(),
                &mut free_available,
                &mut total_bytes,
                &mut total_free,
            )
        };
        if ok == 0 {
            return Err(VfsError::Io(std::io::Error::last_os_error()));
        }
        Ok((free_available, total_bytes))
    }

    // Layout matches the documented `BY_HANDLE_FILE_INFORMATION` struct
    // field-for-field (win32 `fileapi.h`) — only the fields this module
    // actually reads are named individually; the rest are grouped into
    // padding so a mismatched field count can't silently shift the ones
    // that matter.
    #[repr(C)]
    struct ByHandleFileInformation {
        _file_attributes: u32,
        _creation_time: u64,
        _last_access_time: u64,
        _last_write_time: u64,
        volume_serial_number: u32,
        _file_size_high: u32,
        _file_size_low: u32,
        _number_of_links: u32,
        file_index_high: u32,
        file_index_low: u32,
    }

    const FILE_SHARE_READ: u32 = 0x0000_0001;
    const FILE_SHARE_WRITE: u32 = 0x0000_0002;
    const FILE_SHARE_DELETE: u32 = 0x0000_0004;
    const OPEN_EXISTING: u32 = 3;
    const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
    const FILE_FLAG_OPEN_REPARSE_POINT: u32 = 0x0020_0000;

    fn info_to_identity(info: &ByHandleFileInformation) -> (u64, u64) {
        let ino = ((info.file_index_high as u64) << 32) | info.file_index_low as u64;
        (info.volume_serial_number as u64, ino)
    }

    /// Real, NTFS-stable `(volume serial number, file index)` identity for
    /// `path` — the Windows analogue of `statx`'s `(dev, ino)`, and what
    /// `sc-meta`'s node identity actually needs to be unique (see
    /// `dev_ino_of_path`'s doc comment in the parent module for why the
    /// `(0, 0)` placeholder this replaces was a real bug, not a cosmetic
    /// one). `MetadataExt::{volume_serial_number, file_index}` would give
    /// the same numbers for free, but both are still gated behind the
    /// unstable `windows_by_handle` feature (rust-lang/rust#63010) on this
    /// toolchain, so this goes straight to the Win32 call that feature
    /// would eventually wrap — the same one `GetFileInformationByHandle`'s
    /// own name promises, and the same shape as `free_bytes` above.
    ///
    /// `dwDesiredAccess: 0` ("query metadata only") plus every `FILE_SHARE_*`
    /// bit means this can never be the thing that makes a file "busy" to
    /// Explorer, Jellyfin, or *arr sharing the same directory — the same
    /// rule `file_set_mode`'s doc comment states for why those must keep
    /// working. `FILE_FLAG_BACKUP_SEMANTICS` is required to open a
    /// directory at all through `CreateFileW`; `FILE_FLAG_OPEN_REPARSE_POINT`
    /// keeps a symlink's *own* identity from being silently replaced by its
    /// target's, matching `stat_path`'s `symlink_metadata` (lstat-like, not
    /// stat-like) semantics elsewhere in this file.
    ///
    /// `None` on any failure (path vanished between the caller's own stat
    /// and this call, permission denied, ...) — callers fall back to
    /// `(0, 0)`, the same honest-when-possible, explicit-placeholder-when-not
    /// pattern `statfs_free` already uses for this backend.
    pub(super) fn file_identity(path: &Path) -> Option<(u64, u64)> {
        let wide: Vec<u16> = path.as_os_str().encode_wide().chain(std::iter::once(0)).collect();
        // SAFETY: `wide` is a NUL-terminated UTF-16 buffer kept alive for the
        // call. `lp_security_attributes` and `h_template_file` are
        // documented as legal to pass null; every other argument is a plain
        // integer flag from the Win32 API this call matches exactly.
        let handle = unsafe {
            CreateFileW(
                wide.as_ptr(),
                0,
                FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                std::ptr::null_mut(),
                OPEN_EXISTING,
                FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
                std::ptr::null_mut(),
            )
        };
        // `INVALID_HANDLE_VALUE` is `-1` reinterpreted as a pointer, not
        // null — `CreateFileW` never returns null on failure.
        if handle as isize == -1 {
            return None;
        }
        let identity = file_identity_of_handle(handle);
        // SAFETY: `handle` was just opened successfully above and is not
        // used again after this call in either branch.
        unsafe { CloseHandle(handle) };
        identity
    }

    /// Same identity lookup from a handle the caller already has open
    /// (`file_stat`'s `FileInner`) — no `CreateFileW` round trip needed.
    pub(super) fn file_identity_of_handle(handle: RawHandle) -> Option<(u64, u64)> {
        let mut info: ByHandleFileInformation = unsafe { std::mem::zeroed() };
        // SAFETY: `handle` is a valid, open, unowned-by-this-call handle
        // (the caller keeps ownership; nothing here closes it) — for a
        // `RawHandle` from `AsRawHandle` that is always true for as long as
        // the borrow it came from is alive, which outlives this call.
        // `info` is a valid, aligned, writable out-parameter matching the
        // documented `BY_HANDLE_FILE_INFORMATION` layout field-for-field.
        let ok = unsafe { GetFileInformationByHandle(handle, &mut info) };
        if ok == 0 {
            return None;
        }
        Some(info_to_identity(&info))
    }
}
