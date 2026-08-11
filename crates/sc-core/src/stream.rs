//! Bounded-memory streaming reads — the primitive `GET /c/{token}`
//! and the streaming zip archive (§8) are both built
//! on. Whole-file buffered reads (`Core::read_bytes`) are fine for the text
//! editor's size-capped path; they are wrong for a multi-gigabyte download,
//! which is why nothing above this crate could actually serve one before
//! this module existed.
//!
//! [`CoreFileStream`] wraps a single [`sc_vfs::FileHandle`] and reads it in
//! bounded chunks via `read_at` — never more than [`CHUNK`] bytes per
//! syscall, so memory use does not scale with file size. The handle is
//! opened once and kept for the stream's entire lifetime: wants a download to keep serving a single consistent version even if
//! another service replaces the file mid-transfer, and an already-open fd on
//! a POSIX filesystem keeps referencing the inode it was opened against
//! regardless of what a rename-based atomic replace does to the directory
//! entry.

use std::sync::Arc;

use sc_acl::Perms;
use sc_vfs::ids::FileId;
use sc_vfs::{FileHandle, Kind, SafePath, ShareRoot, Stat, UserId};

use crate::error::CoreError;
use crate::path::Vpath;

/// Bytes read per `read_at` call, regardless of the caller's buffer size
/// (/: "memory must not scale with file
/// size").
pub const CHUNK: usize = 256 * 1024;

/// Metadata about the file a stream was opened against — enough for a
/// protocol layer to build `Content-Length`/`ETag`/`Content-Disposition`
/// headers without a second round trip through `sc-vfs`.
#[derive(Clone, Debug)]
pub struct FidEntry {
    /// The file's own name (last path component).
    pub name: String,
    /// Full file size, independent of any requested range.
    pub size: u64,
    pub mtime_ns: i128,
    pub etag: String,
}

/// A bounded-memory `Read` over one already-open file, restricted to
/// `[pos, end)`. Constructed only by `Core` — never directly — so the fd
/// lifetime and range clamping stay in one place.
pub struct CoreFileStream {
    fh: FileHandle,
    pos: u64,
    end: u64,
}

impl CoreFileStream {
    /// Bytes remaining to be read (i.e. `Content-Length` for this stream).
    pub fn remaining(&self) -> u64 {
        self.end.saturating_sub(self.pos)
    }
}

impl std::io::Read for CoreFileStream {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        if self.pos >= self.end {
            return Ok(0);
        }
        let remaining = (self.end - self.pos) as usize;
        let cap = remaining.min(buf.len()).min(CHUNK);
        let n = self
            .fh
            .read_at(&mut buf[..cap], self.pos)
            .map_err(std::io::Error::other)?;
        if n == 0 {
            // The file shrank out from under us (race with a concurrent
            // write) before we reached `end`. Reporting a short read here is
            // still an honest stream: `Ok(0)` just means "no more bytes",
            // which is exactly true.
            self.end = self.pos;
            return Ok(0);
        }
        self.pos += n as u64;
        Ok(n)
    }
}

impl crate::Core {
    /// Streaming read by virtual path, ACL-checked (`Perms::READ`) exactly
    /// like every other `Core` entry point. `range` is an inclusive
    /// `(start, end)` byte range — HTTP `Range` semantics — clamped to the
    /// file's actual size; `None` reads the whole file.
    pub fn open_stream(
        &self,
        user: UserId,
        vpath: &str,
        range: Option<(u64, u64)>,
    ) -> Result<(FidEntry, CoreFileStream), CoreError> {
        let r = self.resolve_want(user, &Vpath::new(vpath), Perms::READ)?;
        self.open_stream_in(&r.root, &r.path, range)
    }

    /// Streaming read addressed by stable `FileId`, with **no ACL
    /// re-check** — used by signed content-URL serving,
    /// where the capability *is* the access
    /// control: a URL is only ever issued after the issuing request already
    /// passed an ACL check, and verifying a signed URL is deliberately
    /// stateless (no DB lookup beyond the one needed to find the bytes).
    pub fn open_stream_by_fid(
        &self,
        fid: FileId,
        range: Option<(u64, u64)>,
    ) -> Result<(FidEntry, CoreFileStream), CoreError> {
        let (root, path) = self.resolve_fid(fid)?;
        self.open_stream_in(&root, &path, range)
    }

    /// Metadata only, by `FileId` — no fd opened. Used to decide response
    /// headers (and the etag-mismatch `410 Gone` check) before committing to
    /// a stream.
    pub fn stat_by_fid(&self, fid: FileId) -> Result<FidEntry, CoreError> {
        let (root, path) = self.resolve_fid(fid)?;
        let st = root.stat(&path)?;
        Ok(Self::fid_entry(&path, &st))
    }

    /// Whether `user` currently has `READ` on the file identified by `fid`.
    /// Unlike `open_stream_by_fid`/`stat_by_fid`, this one *does* consult the
    /// ACL — it exists so that **issuing** a capability (`POST /api/fs/link`)
    /// can refuse to mint one for a file the caller cannot read, rather than
    /// relying solely on the client already knowing a legitimate `fid`
    /// (`fid`s are small sequential integers, not secrets).
    pub fn check_read_by_fid(&self, user: UserId, fid: FileId) -> Result<(), CoreError> {
        let (share, rel) = self
            .meta
            .resolve_path(fid)
            .map_err(CoreError::from)?
            .ok_or(CoreError::NotFound)?;
        let root = self.share(share).ok_or(CoreError::NotFound)?;
        let max_depth = root.policy().max_depth;
        let path = Self::parse_rel(&rel, max_depth)?;
        let decision = self.acl.evaluate(user, share, &path, Perms::READ);
        if decision.is_allowed() {
            Ok(())
        } else {
            let by = match decision {
                sc_acl::Decision::Denied { by } => by,
                sc_acl::Decision::Allowed { .. } => None,
            };
            Err(CoreError::Denied { by })
        }
    }

    fn resolve_fid(&self, fid: FileId) -> Result<(Arc<ShareRoot>, SafePath), CoreError> {
        let (share, rel) = self
            .meta
            .resolve_path(fid)
            .map_err(CoreError::from)?
            .ok_or(CoreError::NotFound)?;
        let root = self.share(share).ok_or(CoreError::NotFound)?;
        let max_depth = root.policy().max_depth;
        let path = Self::parse_rel(&rel, max_depth)?;
        Ok((root, path))
    }

    fn parse_rel(rel: &str, max_depth: u16) -> Result<SafePath, CoreError> {
        if rel.is_empty() {
            Ok(SafePath::root())
        } else {
            Ok(SafePath::parse(rel, max_depth)?)
        }
    }

    fn fid_entry(path: &SafePath, st: &Stat) -> FidEntry {
        FidEntry {
            name: path.name().unwrap_or("").to_string(),
            size: st.size,
            mtime_ns: st.mtime_ns,
            etag: sc_meta::MetaStore::file_etag(st),
        }
    }

    pub(crate) fn open_stream_in(
        &self,
        root: &Arc<ShareRoot>,
        path: &SafePath,
        range: Option<(u64, u64)>,
    ) -> Result<(FidEntry, CoreFileStream), CoreError> {
        let fh = root.open_read(path)?;
        // Stat through the *same* fd we're about to stream from, so the
        // reported size/etag and the bytes actually delivered are always
        // consistent with one another even if the directory entry changes
        // underneath us a moment later.
        let st = fh.stat()?;
        if st.kind == Kind::Dir {
            return Err(CoreError::InvalidPath("is a directory".into()));
        }
        let (start, end) = match range {
            Some((s, e)) => {
                let start = s.min(st.size);
                let end = e.saturating_add(1).min(st.size).max(start);
                (start, end)
            }
            None => (0, st.size),
        };
        let entry = Self::fid_entry(path, &st);
        Ok((entry, CoreFileStream { fh, pos: start, end }))
    }

    /// A whole-file `Read + Seek`, ACL-checked (`Perms::READ`) like every
    /// other `Core` entry point. The zip central directory lives at the end of
    /// the file, so a zip reader has to seek; nothing else above this crate
    /// does.
    pub fn open_seekable(
        &self,
        user: UserId,
        vpath: &str,
    ) -> Result<(FidEntry, SeekableFile), CoreError> {
        let r = self.resolve_want(user, &Vpath::new(vpath), Perms::READ)?;
        let fh = r.root.open_read(&r.path)?;
        let st = fh.stat()?;
        if st.kind == Kind::Dir {
            return Err(CoreError::InvalidPath("is a directory".into()));
        }
        let entry = Self::fid_entry(&r.path, &st);
        Ok((entry, SeekableFile { fh, pos: 0, len: st.size }))
    }
}

/// `std::io::Read + Seek` over one whole file.
///
/// [`sc_vfs::FileHandle`] offers `read_at(buf, off)` and nothing else, on
/// purpose: nothing above it holds a file cursor. A reader that needs one
/// keeps it here, beside the handle, because the cursor belongs to the reader
/// and not to the file.
pub struct SeekableFile {
    fh: FileHandle,
    pos: u64,
    len: u64,
}

impl std::io::Read for SeekableFile {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        if self.pos >= self.len {
            return Ok(0);
        }
        let remaining = (self.len - self.pos) as usize;
        let cap = remaining.min(buf.len()).min(CHUNK);
        let n = self
            .fh
            .read_at(&mut buf[..cap], self.pos)
            .map_err(std::io::Error::other)?;
        self.pos += n as u64;
        Ok(n)
    }
}

impl std::io::Seek for SeekableFile {
    fn seek(&mut self, from: std::io::SeekFrom) -> std::io::Result<u64> {
        use std::io::SeekFrom;
        let target: i128 = match from {
            SeekFrom::Start(n) => n as i128,
            SeekFrom::End(n) => self.len as i128 + n as i128,
            SeekFrom::Current(n) => self.pos as i128 + n as i128,
        };
        if target < 0 {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "seek before the start of the file",
            ));
        }
        // Seeking past the end is legal and reads nothing, which is what
        // `File` does too.
        self.pos = target as u64;
        Ok(self.pos)
    }
}
