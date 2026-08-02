//! The one place the buffered (userspace round-trip) copy loop lives.
//!
//! `FileHandle::copy_range_from` prefers the kernel-side primitive
//! (`copy_file_range` on Linux — reflink on btrfs/XFS, in-kernel copy
//! otherwise, see / `TECH-STACK.md` §3), but two
//! things fall back to a plain read/write loop: the portable backend (no
//! such syscall off Linux) and the Linux backend when the kernel primitive
//! itself reports `EXDEV`/`EOPNOTSUPP`/`ENOSYS`
//! (`DEPLOYMENT.md` §4 — a subdirectory can be a separate mount *inside* a
//! share, so `EXDEV` is a real, expected outcome here, not just a
//! theoretical one). Both call sites share this function so the loop is
//! written, and bounded-memory-audited, exactly once.

use crate::error::VfsError;

/// Never buffers more than this much at once, regardless of `len` — the
/// same bound `sc-upload`'s old placeholder loop used, kept here so nothing
/// downstream regresses on the "no chunk fully in memory" guarantee.
pub(crate) const BUF_LEN: usize = 256 * 1024;

/// Copy `len` bytes by reading from `read_at` at `src_off` and writing
/// through `write_at` at `dst_off`, through a fixed `BUF_LEN` buffer.
///
/// Returns the number of bytes actually copied, which is less than `len`
/// only if `read_at` hit EOF first (mirrors `copy_file_range`'s short-copy
/// contract, so callers that fell back mid-copy don't need to branch on
/// which path they took).
pub(crate) fn buffered_copy_range<R, W>(mut read_at: R, mut write_at: W, src_off: u64, dst_off: u64, len: u64) -> Result<u64, VfsError>
where
    R: FnMut(&mut [u8], u64) -> Result<usize, VfsError>,
    W: FnMut(&[u8], u64) -> Result<usize, VfsError>,
{
    if len == 0 {
        return Ok(0);
    }
    let mut buf = vec![0u8; BUF_LEN];
    let mut copied = 0u64;
    while copied < len {
        let want = BUF_LEN.min((len - copied) as usize);
        let n = read_at(&mut buf[..want], src_off + copied)?;
        if n == 0 {
            break; // source ran out of bytes before `len` was reached
        }
        write_all(&mut write_at, &buf[..n], dst_off + copied)?;
        copied += n as u64;
    }
    Ok(copied)
}

fn write_all<W>(write_at: &mut W, mut buf: &[u8], mut off: u64) -> Result<(), VfsError>
where
    W: FnMut(&[u8], u64) -> Result<usize, VfsError>,
{
    while !buf.is_empty() {
        let n = write_at(buf, off)?;
        if n == 0 {
            return Err(VfsError::Io(std::io::Error::new(std::io::ErrorKind::WriteZero, "write_at wrote 0 bytes")));
        }
        buf = &buf[n..];
        off += n as u64;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;

    /// A backing store simple enough to drive the closures directly,
    /// without needing a real `FileHandle`/`ShareRoot` — this module tests
    /// only the buffering arithmetic, not filesystem I/O.
    fn copy_vec(src: &[u8], src_off: u64, dst_len: usize, dst_off: u64, len: u64) -> (u64, Vec<u8>) {
        let dst = RefCell::new(vec![0u8; dst_len]);
        let copied = buffered_copy_range(
            |buf, off| {
                let off = off as usize;
                if off >= src.len() {
                    return Ok(0);
                }
                let n = buf.len().min(src.len() - off);
                buf[..n].copy_from_slice(&src[off..off + n]);
                Ok(n)
            },
            |buf, off| {
                let off = off as usize;
                let mut d = dst.borrow_mut();
                if off + buf.len() > d.len() {
                    d.resize(off + buf.len(), 0);
                }
                d[off..off + buf.len()].copy_from_slice(buf);
                Ok(buf.len())
            },
            src_off,
            dst_off,
            len,
        )
        .unwrap();
        (copied, dst.into_inner())
    }

    #[test]
    fn zero_length_copies_nothing() {
        let (n, dst) = copy_vec(b"hello", 0, 5, 0, 0);
        assert_eq!(n, 0);
        assert_eq!(dst, vec![0u8; 5]);
    }

    #[test]
    fn one_byte() {
        let (n, dst) = copy_vec(b"hello", 0, 1, 0, 1);
        assert_eq!(n, 1);
        assert_eq!(dst, b"h");
    }

    #[test]
    fn exactly_one_buffer() {
        let src = vec![7u8; BUF_LEN];
        let (n, dst) = copy_vec(&src, 0, BUF_LEN, 0, BUF_LEN as u64);
        assert_eq!(n, BUF_LEN as u64);
        assert_eq!(dst, src);
    }

    #[test]
    fn several_buffers_plus_remainder() {
        let total = BUF_LEN * 3 + 12345;
        let src: Vec<u8> = (0..total).map(|i| (i % 251) as u8).collect();
        let (n, dst) = copy_vec(&src, 0, total, 0, total as u64);
        assert_eq!(n, total as u64);
        assert_eq!(dst, src);
    }

    #[test]
    fn offsets_into_both_src_and_dst() {
        let src = b"0123456789".to_vec();
        let (n, dst) = copy_vec(&src, 3, 20, 10, 4); // copy "3456" to dst[10..14]
        assert_eq!(n, 4);
        assert_eq!(&dst[10..14], b"3456");
    }

    #[test]
    fn short_source_stops_at_eof() {
        let src = b"abc".to_vec();
        let (n, dst) = copy_vec(&src, 0, 10, 0, 100);
        assert_eq!(n, 3, "must report only the bytes actually available, not the requested len");
        assert_eq!(&dst[..3], b"abc");
    }
}
