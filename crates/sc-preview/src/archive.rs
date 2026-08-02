//! Archive listing (DESIGN-PREVIEW.md section 5).
//!
//! Listing only -- extraction is a separate, explicit operation that lives
//! elsewhere. This module streams through the ZIP central directory,
//! validating every entry name and accumulating size counters, without ever
//! reading an entry's compressed *content* into memory.
//!
//! Zip-slip protection reuses `sc_vfs::SafePath::parse`, which already
//! rejects `..`, absolute paths, NUL/control bytes, Windows-reserved names,
//! trailing dot/space, and `:`. On top of that we reject: names containing
//! `\` (a raw Windows separator that `SafePath` doesn't need to know about
//! since it's a Unix-style path type), entries whose `enclosed_name()` is
//! `None` (the `zip` crate's own opinion that the entry escapes the
//! archive), and symlink/device-node entries.

use std::io::{Read, Seek};

use crate::error::PreviewError;

#[derive(Debug, Clone)]
pub struct ArchiveLimits {
    pub max_entries: u32,
    pub max_total_uncompressed: u64,
    /// Compression ratio cap (`uncompressed / compressed`), per entry.
    pub max_ratio: u32,
    /// Forwarded to `SafePath::parse` as `max_depth` -- also bounds entry
    /// path component count.
    pub max_depth: u16,
    pub max_name_len: u16,
}

impl Default for ArchiveLimits {
    fn default() -> Self {
        Self {
            max_entries: 10_000,
            max_total_uncompressed: 1024 * 1024 * 1024, // 1 GiB
            max_ratio: 100,
            max_depth: 32,
            max_name_len: 255,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ArchiveEntryKind {
    File,
    Dir,
}

#[derive(Debug, Clone)]
pub struct ArchiveEntry {
    pub name: String,
    pub size: u64,
    pub compressed_size: u64,
    pub kind: ArchiveEntryKind,
}

/// List every entry in a ZIP archive, enforcing `limits`. Rejects the
/// *whole* archive (no partial results) the moment any entry violates a
/// limit -- a half-validated listing is not a safe thing to hand back to a
/// caller.
pub fn list_archive<R: Read + Seek>(
    reader: R,
    limits: &ArchiveLimits,
) -> Result<Vec<ArchiveEntry>, PreviewError> {
    let mut zip = zip::ZipArchive::new(reader)
        .map_err(|e| PreviewError::ArchiveRejected(format!("not a valid zip: {e}")))?;

    let n = zip.len();
    if n as u64 > u64::from(limits.max_entries) {
        return Err(PreviewError::ArchiveRejected(format!(
            "archive has {n} entries, exceeding max_entries {}",
            limits.max_entries
        )));
    }

    let mut out = Vec::with_capacity(n);
    let mut cumulative_uncompressed: u64 = 0;

    for i in 0..n {
        // `by_index_raw` gives us metadata (name, sizes, unix mode) without
        // setting up a decompressing reader over the entry's content -- we
        // never call `.read()` on it, so no entry bytes are ever loaded.
        let entry = zip.by_index_raw(i).map_err(|e| {
            PreviewError::ArchiveRejected(format!("corrupt central directory entry {i}: {e}"))
        })?;

        let name = entry.name().to_string();

        if name.len() > limits.max_name_len as usize {
            return Err(PreviewError::ArchiveRejected(format!(
                "entry name exceeds max_name_len ({} bytes): {name:?}",
                limits.max_name_len
            )));
        }
        if name.contains('\\') {
            return Err(PreviewError::ArchiveRejected(format!(
                "entry name contains a backslash, rejected as a possible path escape: {name:?}"
            )));
        }
        if entry.enclosed_name().is_none() {
            return Err(PreviewError::ArchiveRejected(format!(
                "entry escapes the archive root: {name:?}"
            )));
        }

        // Zip-slip: reuse the same rejection table SafePath uses everywhere
        // else. Directory entries have a trailing '/' in their zip name;
        // strip it before validating (the root itself, "", is allowed).
        let trimmed = name.trim_end_matches('/');
        if !trimmed.is_empty() {
            sc_vfs::SafePath::parse(trimmed, limits.max_depth).map_err(|e| {
                PreviewError::ArchiveRejected(format!("invalid entry name {name:?}: {e}"))
            })?;
        }

        if entry.is_symlink() {
            return Err(PreviewError::ArchiveRejected(format!(
                "symlink entries are rejected: {name:?}"
            )));
        }
        if let Some(mode) = entry.unix_mode() {
            const S_IFMT: u32 = 0o170000;
            const S_IFCHR: u32 = 0o020000;
            const S_IFBLK: u32 = 0o060000;
            const S_IFIFO: u32 = 0o010000;
            const S_IFSOCK: u32 = 0o140000;
            let file_type = mode & S_IFMT;
            if matches!(file_type, S_IFCHR | S_IFBLK | S_IFIFO | S_IFSOCK) {
                return Err(PreviewError::ArchiveRejected(format!(
                    "device/fifo/socket entries are rejected: {name:?}"
                )));
            }
        }

        let size = entry.size();
        let compressed_size = entry.compressed_size();

        cumulative_uncompressed = cumulative_uncompressed.saturating_add(size);
        if cumulative_uncompressed > limits.max_total_uncompressed {
            return Err(PreviewError::ArchiveRejected(format!(
                "cumulative uncompressed size {cumulative_uncompressed} exceeds max_total_uncompressed {}",
                limits.max_total_uncompressed
            )));
        }

        match size.checked_div(compressed_size) {
            None if size > 0 => {
                return Err(PreviewError::ArchiveRejected(format!(
                    "entry {name:?} has zero compressed size but {size} declared uncompressed bytes (ratio bomb)"
                )));
            }
            None => {}
            Some(ratio) if ratio > u64::from(limits.max_ratio) => {
                return Err(PreviewError::ArchiveRejected(format!(
                    "entry {name:?} has compression ratio {ratio} exceeding max_ratio {}",
                    limits.max_ratio
                )));
            }
            Some(_) => {}
        }

        let kind = if entry.is_dir() {
            ArchiveEntryKind::Dir
        } else {
            ArchiveEntryKind::File
        };

        out.push(ArchiveEntry {
            name,
            size,
            compressed_size,
            kind,
        });
    }

    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write as _;

    fn writer() -> zip::ZipWriter<std::io::Cursor<Vec<u8>>> {
        zip::ZipWriter::new(std::io::Cursor::new(Vec::new()))
    }

    #[test]
    fn lists_a_benign_archive() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Deflated);
        zw.start_file("a.txt", opts).unwrap();
        zw.write_all(b"hello world").unwrap();
        zw.add_directory("dir/", opts).unwrap();
        zw.start_file("dir/b.txt", opts).unwrap();
        zw.write_all(b"nested").unwrap();
        let cursor = zw.finish().unwrap();

        let entries = list_archive(cursor, &ArchiveLimits::default()).unwrap();
        assert_eq!(entries.len(), 3);
        assert!(entries.iter().any(|e| e.name == "a.txt" && e.kind == ArchiveEntryKind::File));
        assert!(entries.iter().any(|e| e.name == "dir/" && e.kind == ArchiveEntryKind::Dir));
        assert!(entries.iter().any(|e| e.name == "dir/b.txt" && e.kind == ArchiveEntryKind::File));
    }

    #[test]
    fn rejects_zip_slip_entry() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("../../etc/cron.d/x", opts).unwrap();
        zw.write_all(b"evil").unwrap();
        let cursor = zw.finish().unwrap();

        let err = list_archive(cursor, &ArchiveLimits::default())
            .expect_err("zip-slip entry must be rejected before any listing is returned");
        match err {
            PreviewError::ArchiveRejected(msg) => {
                assert!(msg.contains("etc/cron.d/x") || msg.contains("escapes"));
            }
            other => panic!("expected ArchiveRejected, got {other:?}"),
        }
    }

    #[test]
    fn rejects_absolute_path_entry() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        // `zip`'s own `enclosed_name()` already refuses to resolve this, so
        // this exercises that path of the rejection.
        zw.start_file("/etc/passwd", opts).unwrap();
        zw.write_all(b"evil").unwrap();
        let cursor = zw.finish().unwrap();

        let err = list_archive(cursor, &ArchiveLimits::default()).expect_err("must reject");
        assert!(matches!(err, PreviewError::ArchiveRejected(_)));
    }

    #[test]
    fn rejects_backslash_in_entry_name() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("dir\\..\\..\\x", opts).unwrap();
        zw.write_all(b"evil").unwrap();
        let cursor = zw.finish().unwrap();

        let err = list_archive(cursor, &ArchiveLimits::default()).expect_err("must reject");
        assert!(matches!(err, PreviewError::ArchiveRejected(_)));
    }

    #[test]
    fn rejects_ratio_bomb() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Deflated);
        zw.start_file("bomb.bin", opts).unwrap();
        // 4 MiB of zeros deflates to a tiny fraction of its size, giving a
        // compression ratio comfortably over the default cap of 100.
        let zeros = vec![0u8; 4 * 1024 * 1024];
        zw.write_all(&zeros).unwrap();
        let cursor = zw.finish().unwrap();

        let err = list_archive(cursor, &ArchiveLimits::default())
            .expect_err("ratio bomb entry must be rejected");
        match err {
            PreviewError::ArchiveRejected(msg) => assert!(msg.contains("ratio")),
            other => panic!("expected ArchiveRejected, got {other:?}"),
        }
    }

    #[test]
    fn rejects_when_max_entries_exceeded() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        for i in 0..5 {
            zw.start_file(format!("f{i}.txt"), opts).unwrap();
            zw.write_all(b"x").unwrap();
        }
        let cursor = zw.finish().unwrap();

        let tight_limits = ArchiveLimits {
            max_entries: 3,
            ..ArchiveLimits::default()
        };
        let err = list_archive(cursor, &tight_limits).expect_err("must reject");
        assert!(matches!(err, PreviewError::ArchiveRejected(_)));
    }

    #[test]
    fn rejects_when_cumulative_uncompressed_exceeded() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("a.bin", opts).unwrap();
        zw.write_all(&vec![7u8; 1000]).unwrap();
        zw.start_file("b.bin", opts).unwrap();
        zw.write_all(&vec![7u8; 1000]).unwrap();
        let cursor = zw.finish().unwrap();

        let tight_limits = ArchiveLimits {
            max_total_uncompressed: 1500,
            ..ArchiveLimits::default()
        };
        let err = list_archive(cursor, &tight_limits).expect_err("must reject");
        assert!(matches!(err, PreviewError::ArchiveRejected(_)));
    }
}
