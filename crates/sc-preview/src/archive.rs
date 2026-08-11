//! Archive listing.
//!
//! Listing only: extraction is a separate, explicit operation that lives
//! elsewhere. This module reads a ZIP archive's metadata region and parses it
//! in memory. It never reads an entry's content, and it never reads an entry's
//! *local* header either, which is the part that used to cost one seek per
//! entry scattered across the whole file.
//!
//! Zip-slip protection reuses `sc_vfs::SafePath::parse`, which already
//! rejects `..`, absolute paths, NUL/control bytes, Windows-reserved names,
//! trailing dot/space, and `:`. On top of that: names containing `\` (a raw
//! Windows separator that `SafePath` doesn't need to know about since it's a
//! Unix-style path type) and symlink/device-node entries.
//!
//! Those names are left out of the listing and counted, not turned into a
//! refusal of the whole archive. The rule protects whoever builds a path out
//! of a name, and one entry that breaks it says nothing about the five
//! thousand beside it. What still refuses an archive outright is what makes
//! it unreadable or unbounded: not a zip, too many entries, a central
//! directory over budget.

use std::io::{Read, Seek, SeekFrom};

use crate::error::PreviewError;

/// What bounds a listing.
///
/// Every limit here bounds work *this* module does: how many entries are
/// allocated, how long a name may be, how much of the file is read. There is
/// deliberately no cap on uncompressed size or compression ratio any more.
/// Those bound decompression, and this module never decompresses: the numbers
/// they tested come out of the central directory as metadata and are copied
/// into the listing untouched. Applying them here rejected ordinary archives
/// for being large — a 1.2 GB installer is over a 1 GiB uncompressed budget
/// before it contains anything unusual — and protected nothing, since no byte
/// of any entry is ever read. A future extractor enforces its own; it is the
/// thing that would write the bytes.
#[derive(Debug, Clone)]
pub struct ArchiveLimits {
    pub max_entries: u32,
    /// Forwarded to `SafePath::parse` as `max_depth` -- also bounds entry
    /// path component count.
    pub max_depth: u16,
    pub max_name_len: u16,
    /// Cap on the central directory, which is the only region a listing reads
    /// in full and therefore the only thing that predicts its cost. At the
    /// entry and name caps above a listable directory is roughly 3 MiB.
    pub max_central_directory_bytes: u64,
}

impl Default for ArchiveLimits {
    fn default() -> Self {
        Self {
            max_entries: 10_000,
            max_depth: 32,
            max_name_len: 255,
            max_central_directory_bytes: 16 * 1024 * 1024,
        }
    }
}

/// One archive's listing, and what was left out of it.
#[derive(Debug, Clone, Default)]
pub struct ArchiveListing {
    pub entries: Vec<ArchiveEntry>,
    /// Entries whose name or type this module refuses to hand out: a path
    /// escape, a raw Windows separator, a symlink, a device node.
    ///
    /// Counted rather than fatal. The rule protects whoever turns a name into
    /// a path, and a listing is not that, so one odd entry in five thousand is
    /// a row that is not shown and not a whole archive that will not open.
    /// Reported so the omission is visible rather than silent.
    pub skipped: u32,
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

const EOCD_SIG: u32 = 0x0605_4b50;
const EOCD_LEN: usize = 22;
const ZIP64_LOCATOR_SIG: u32 = 0x0706_4b50;
const ZIP64_LOCATOR_LEN: usize = 20;
const ZIP64_EOCD_SIG: u32 = 0x0606_4b50;
const ZIP64_EOCD_LEN: usize = 56;
const CDFH_SIG: u32 = 0x0201_4b50;
const CDFH_LEN: usize = 46;
/// The end-of-central-directory record plus the largest archive comment that
/// may follow it.
const TAIL_WINDOW: u64 = EOCD_LEN as u64 + 65_535;
/// `version made by`, high byte, for a Unix host. Only those archives carry a
/// meaningful unix mode in their external attributes.
const HOST_UNIX: u16 = 3;
/// The Zip64 extended information extra field.
const ZIP64_EXTRA_ID: u16 = 0x0001;

/// Every entry in a ZIP archive's central directory.
///
/// At most three positional reads, all inside the archive's metadata region:
/// the tail window holding the end-of-central-directory record, the Zip64
/// record when it falls outside that window, and the central directory itself.
/// An entry's local header is never read, which is what the per-entry probe
/// used to cost.
///
/// `len` is the file's size, which the caller already has from the `stat` that
/// opened it.
///
/// The whole archive is refused only for what makes it unreadable or
/// unbounded: not a zip, more entries than the cap, a central directory over
/// the budget. An entry whose *name* cannot be handed out safely is left out
/// of the listing and counted in [`ArchiveListing::skipped`] instead of taking
/// the other entries with it.
pub fn list_archive<R: Read + Seek>(
    mut reader: R,
    len: u64,
    limits: &ArchiveLimits,
) -> Result<ArchiveListing, PreviewError> {
    // `UnsupportedFormat`, not `ArchiveRejected`: "this file is not a zip" and
    // "this zip broke a limit" are different answers to a caller. A route
    // reports the first as `404`, since telling somebody what a file they
    // cannot read contains is exactly what that code exists to avoid, and the
    // second as `422` with the reason.
    if len < EOCD_LEN as u64 {
        return Err(PreviewError::UnsupportedFormat);
    }
    let window_len = len.min(TAIL_WINDOW);
    let window_at = len - window_len;
    let window = read_at(&mut reader, window_at, window_len as usize)?;

    let eocd_at = find_eocd(&window).ok_or(PreviewError::UnsupportedFormat)?;
    let eocd = &window[eocd_at..];
    let mut entries = u64::from(u16le(&eocd[10..]));
    let mut cd_size = u64::from(u32le(&eocd[12..]));
    let cd_offset = u32le(&eocd[16..]);
    // The describing record is the one the central directory sits immediately
    // in front of.
    let mut described_at = window_at + eocd_at as u64;

    let zip64 = entries == u64::from(u16::MAX)
        || cd_size == u64::from(u32::MAX)
        || cd_offset == u32::MAX
        || u16le(&eocd[4..]) == u16::MAX
        || u16le(&eocd[6..]) == u16::MAX;
    if zip64 {
        // The locator sits immediately before the end-of-central-directory
        // record, so it is always inside the tail window.
        if eocd_at < ZIP64_LOCATOR_LEN {
            return Err(PreviewError::UnsupportedFormat);
        }
        let locator = &window[eocd_at - ZIP64_LOCATOR_LEN..eocd_at];
        if u32le(locator) != ZIP64_LOCATOR_SIG {
            return Err(PreviewError::UnsupportedFormat);
        }
        let record_at = u64le(&locator[8..]);
        // In every real archive the record sits immediately before the locator
        // and is therefore already in the window; the read below covers the
        // case where it is not.
        let fetched;
        let record: &[u8] = match window_slice(&window, window_at, record_at, ZIP64_EOCD_LEN) {
            Some(s) => s,
            None => {
                if record_at.saturating_add(ZIP64_EOCD_LEN as u64) > len {
                    return Err(PreviewError::UnsupportedFormat);
                }
                fetched = read_at(&mut reader, record_at, ZIP64_EOCD_LEN)?;
                &fetched
            }
        };
        if u32le(record) != ZIP64_EOCD_SIG {
            return Err(PreviewError::UnsupportedFormat);
        }
        entries = u64le(&record[32..]);
        cd_size = u64le(&record[40..]);
        described_at = record_at;
    }

    if entries > u64::from(limits.max_entries) {
        return Err(PreviewError::ArchiveRejected(format!(
            "archive has {entries} entries, exceeding max_entries {}",
            limits.max_entries
        )));
    }
    if cd_size > limits.max_central_directory_bytes {
        return Err(PreviewError::ArchiveRejected(format!(
            "central directory is {cd_size} bytes, exceeding max_central_directory_bytes {}",
            limits.max_central_directory_bytes
        )));
    }
    // The declared offset is what this should already be. Taking it by
    // subtraction instead recovers an archive with data prepended in front of
    // it, such as a self-extracting stub, and never points a read outside the
    // file.
    if cd_size > described_at {
        return Err(PreviewError::UnsupportedFormat);
    }
    let cd_at = described_at - cd_size;

    let fetched;
    let cd: &[u8] = match window_slice(&window, window_at, cd_at, cd_size as usize) {
        Some(s) => s,
        None => {
            fetched = read_at(&mut reader, cd_at, cd_size as usize)?;
            &fetched
        }
    };
    parse_central_directory(cd, entries, limits)
}

/// Pure. No I/O, no reader, no file. Given the central directory bytes and the
/// entry count the end-of-central-directory record declared, produce the
/// listing or reject the archive.
///
/// Because this takes a byte slice, no listing can read an entry's content or
/// its local header: there is nothing here to read it with.
fn parse_central_directory(
    cd: &[u8],
    entries: u64,
    limits: &ArchiveLimits,
) -> Result<ArchiveListing, PreviewError> {
    let mut out = Vec::with_capacity(entries as usize);
    let mut skipped: u32 = 0;
    let mut at = 0usize;

    for _ in 0..entries {
        if at + CDFH_LEN > cd.len() {
            return Err(PreviewError::UnsupportedFormat);
        }
        let header = &cd[at..at + CDFH_LEN];
        if u32le(header) != CDFH_SIG {
            return Err(PreviewError::UnsupportedFormat);
        }
        let made_by = u16le(&header[4..]);
        let flags = u16le(&header[8..]);
        let mut compressed_size = u64::from(u32le(&header[20..]));
        let mut size = u64::from(u32le(&header[24..]));
        let name_len = usize::from(u16le(&header[28..]));
        let extra_len = usize::from(u16le(&header[30..]));
        let comment_len = usize::from(u16le(&header[32..]));
        let external_attributes = u32le(&header[38..]);

        let names_at = at + CDFH_LEN;
        let end = names_at + name_len + extra_len + comment_len;
        if end > cd.len() {
            return Err(PreviewError::UnsupportedFormat);
        }
        let raw_name = &cd[names_at..names_at + name_len];
        let extra = &cd[names_at + name_len..names_at + name_len + extra_len];
        at = end;

        if size == u64::from(u32::MAX) || compressed_size == u64::from(u32::MAX) {
            if let Some(d) = zip64_extra(extra) {
                let mut p = 0;
                if size == u64::from(u32::MAX) && p + 8 <= d.len() {
                    size = u64le(&d[p..]);
                    p += 8;
                }
                if compressed_size == u64::from(u32::MAX) && p + 8 <= d.len() {
                    compressed_size = u64le(&d[p..]);
                }
            }
        }

        // General-purpose bit 11 is the client's claim that the name is UTF-8.
        // Without it the name is CP437, which is what the format says and what
        // the `zip` crate assumes. An archive carrying CP949 bytes with the
        // flag clear renders as mojibake here, exactly as it did before:
        // guessing encodings is a separate question from reading the format.
        let name = if flags & 0x0800 != 0 {
            String::from_utf8_lossy(raw_name).into_owned()
        } else {
            decode_cp437(raw_name)
        };

        // Everything from here to the push is a per-entry refusal: the entry
        // is left out and counted, and the rest of the archive still lists.
        // A name is refused because handing it to whoever builds a path from
        // it would be unsafe, which is a property of that one name.
        if name.len() > limits.max_name_len as usize {
            skipped += 1;
            continue;
        }
        // A raw Windows separator, which `SafePath` has no reason to know
        // about since it is a Unix-style path type.
        if name.contains('\\') {
            skipped += 1;
            continue;
        }

        // Zip-slip: reuse the same rejection table SafePath uses everywhere
        // else. Directory entries have a trailing '/' in their zip name;
        // strip it before validating (the root itself, "", is allowed).
        let trimmed = name.trim_end_matches('/');
        if !trimmed.is_empty() && sc_vfs::SafePath::parse(trimmed, limits.max_depth).is_err() {
            skipped += 1;
            continue;
        }

        if made_by >> 8 == HOST_UNIX && external_attributes != 0 {
            const S_IFMT: u32 = 0o170000;
            const S_IFLNK: u32 = 0o120000;
            const S_IFCHR: u32 = 0o020000;
            const S_IFBLK: u32 = 0o060000;
            const S_IFIFO: u32 = 0o010000;
            const S_IFSOCK: u32 = 0o140000;
            let file_type = (external_attributes >> 16) & S_IFMT;
            if matches!(file_type, S_IFLNK | S_IFCHR | S_IFBLK | S_IFIFO | S_IFSOCK) {
                skipped += 1;
                continue;
            }
        }

        let kind = if name.ends_with('/') {
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

    Ok(ArchiveListing {
        entries: out,
        skipped,
    })
}

/// The data of the Zip64 extended information field, if the entry has one.
fn zip64_extra(extra: &[u8]) -> Option<&[u8]> {
    let mut p = 0usize;
    while p + 4 <= extra.len() {
        let id = u16le(&extra[p..]);
        let len = usize::from(u16le(&extra[p + 2..]));
        if p + 4 + len > extra.len() {
            return None;
        }
        if id == ZIP64_EXTRA_ID {
            return Some(&extra[p + 4..p + 4 + len]);
        }
        p += 4 + len;
    }
    None
}

/// The end-of-central-directory record's offset inside the tail window.
///
/// Scanned backwards, preferring a candidate whose declared comment length
/// accounts for exactly the bytes after it: an archive comment is free to
/// contain the signature, and that copy sits *after* the real record.
fn find_eocd(window: &[u8]) -> Option<usize> {
    if window.len() < EOCD_LEN {
        return None;
    }
    let sig = EOCD_SIG.to_le_bytes();
    let mut fallback = None;
    for i in (0..=window.len() - EOCD_LEN).rev() {
        if window[i..i + 4] != sig {
            continue;
        }
        let comment_len = usize::from(u16le(&window[i + 20..]));
        if i + EOCD_LEN + comment_len == window.len() {
            return Some(i);
        }
        fallback.get_or_insert(i);
    }
    fallback
}

/// `[at, at + n)` as a slice of an already-fetched window, when it lies wholly
/// inside it.
fn window_slice(window: &[u8], window_at: u64, at: u64, n: usize) -> Option<&[u8]> {
    let from = at.checked_sub(window_at)?;
    let from = usize::try_from(from).ok()?;
    window.get(from..from.checked_add(n)?)
}

fn read_at<R: Read + Seek>(reader: &mut R, at: u64, n: usize) -> Result<Vec<u8>, PreviewError> {
    let mut buf = vec![0u8; n];
    reader
        .seek(SeekFrom::Start(at))
        .map_err(|_| PreviewError::UnsupportedFormat)?;
    reader
        .read_exact(&mut buf)
        .map_err(|_| PreviewError::UnsupportedFormat)?;
    Ok(buf)
}

fn u16le(b: &[u8]) -> u16 {
    u16::from_le_bytes([b[0], b[1]])
}

fn u32le(b: &[u8]) -> u32 {
    u32::from_le_bytes([b[0], b[1], b[2], b[3]])
}

fn u64le(b: &[u8]) -> u64 {
    u64::from_le_bytes([b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7]])
}

fn decode_cp437(bytes: &[u8]) -> String {
    bytes
        .iter()
        .map(|&b| {
            if b < 0x80 {
                b as char
            } else {
                CP437_HIGH[usize::from(b - 0x80)]
            }
        })
        .collect()
}

/// CP437, code points 0x80 to 0xFF. The low half is ASCII.
const CP437_HIGH: [char; 128] = [
    'Ç', 'ü', 'é', 'â', 'ä', 'à', 'å', 'ç', 'ê', 'ë', 'è', 'ï', 'î', 'ì', 'Ä', 'Å', 'É', 'æ', 'Æ',
    'ô', 'ö', 'ò', 'û', 'ù', 'ÿ', 'Ö', 'Ü', '¢', '£', '¥', '₧', 'ƒ', 'á', 'í', 'ó', 'ú', 'ñ', 'Ñ',
    'ª', 'º', '¿', '⌐', '¬', '½', '¼', '¡', '«', '»', '░', '▒', '▓', '│', '┤', '╡', '╢', '╖', '╕',
    '╣', '║', '╗', '╝', '╜', '╛', '┐', '└', '┴', '┬', '├', '─', '┼', '╞', '╟', '╚', '╔', '╩', '╦',
    '╠', '═', '╬', '╧', '╨', '╤', '╥', '╙', '╘', '╒', '╓', '╫', '╪', '┘', '┌', '█', '▄', '▌', '▐',
    '▀', 'α', 'ß', 'Γ', 'π', 'Σ', 'σ', 'µ', 'τ', 'Φ', 'Θ', 'Ω', 'δ', '∞', 'φ', 'ε', '∩', '≡', '±',
    '≥', '≤', '⌠', '⌡', '÷', '≈', '°', '∙', '·', '√', 'ⁿ', '²', '■', '\u{a0}',
];

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Cursor, Write as _};

    fn writer() -> zip::ZipWriter<Cursor<Vec<u8>>> {
        zip::ZipWriter::new(Cursor::new(Vec::new()))
    }

    fn list(cursor: Cursor<Vec<u8>>, limits: &ArchiveLimits) -> Result<ArchiveListing, PreviewError> {
        let len = cursor.get_ref().len() as u64;
        list_archive(cursor, len, limits)
    }

    /// Every positional read a listing issued, as `(offset, length)`.
    struct CountingReader {
        inner: Cursor<Vec<u8>>,
        reads: std::rc::Rc<std::cell::RefCell<Vec<(u64, usize)>>>,
    }

    impl Read for CountingReader {
        fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
            let at = self.inner.position();
            let n = self.inner.read(buf)?;
            self.reads.borrow_mut().push((at, n));
            Ok(n)
        }
    }

    impl Seek for CountingReader {
        fn seek(&mut self, pos: SeekFrom) -> std::io::Result<u64> {
            self.inner.seek(pos)
        }
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

        let listing = list(cursor, &ArchiveLimits::default()).unwrap();
        let entries = &listing.entries;
        assert_eq!(entries.len(), 3);
        assert_eq!(listing.skipped, 0);
        assert!(entries.iter().any(|e| e.name == "a.txt" && e.kind == ArchiveEntryKind::File));
        assert!(entries.iter().any(|e| e.name == "dir/" && e.kind == ArchiveEntryKind::Dir));
        assert!(entries.iter().any(|e| e.name == "dir/b.txt" && e.kind == ArchiveEntryKind::File));
        let a = entries.iter().find(|e| e.name == "a.txt").unwrap();
        assert_eq!(a.size, 11);
    }

    /// The user-visible requirement, "read the metadata area only", as an
    /// executable check rather than a comment. Everything that describes the
    /// central directory sits after it, so a read below its offset is a read
    /// into somebody's file content.
    #[test]
    fn a_listing_reads_the_metadata_region_and_nothing_else() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        // Enough content that the tail window cannot reach into it.
        for i in 0..20 {
            zw.start_file(format!("f{i:02}.bin"), opts).unwrap();
            zw.write_all(&vec![b'x'; 64 * 1024]).unwrap();
        }
        let cursor = zw.finish().unwrap();
        let len = cursor.get_ref().len() as u64;
        let content_end = len - TAIL_WINDOW;

        let reads = std::rc::Rc::new(std::cell::RefCell::new(Vec::new()));
        let reader = CountingReader {
            inner: cursor,
            reads: std::rc::Rc::clone(&reads),
        };
        let entries = list_archive(reader, len, &ArchiveLimits::default()).unwrap().entries;
        assert_eq!(entries.len(), 20);

        let reads = reads.borrow();
        assert!(reads.len() <= 3, "a listing issued {} reads: {reads:?}", reads.len());
        for &(at, _) in reads.iter() {
            assert!(
                at >= content_end,
                "a listing read at {at}, below the metadata region at {content_end}"
            );
        }
    }

    /// Data prepended in front of an archive, as a self-extracting stub does,
    /// leaves every declared offset short by the stub's length. Taking the
    /// central directory's position by subtraction recovers it.
    #[test]
    fn an_archive_with_data_prepended_still_lists() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("a.txt", opts).unwrap();
        zw.write_all(b"hello").unwrap();
        let cursor = zw.finish().unwrap();

        let mut bytes = vec![0xAAu8; 4096];
        bytes.extend_from_slice(cursor.get_ref());
        let len = bytes.len() as u64;
        let entries = list_archive(Cursor::new(bytes), len, &ArchiveLimits::default()).unwrap().entries;
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].name, "a.txt");
    }

    #[test]
    fn a_zip64_archive_lists() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored)
            .large_file(true);
        zw.start_file("big.bin", opts).unwrap();
        zw.write_all(b"not actually big").unwrap();
        zw.start_file("other.bin", opts).unwrap();
        zw.write_all(b"also small").unwrap();
        let cursor = zw.finish().unwrap();

        let entries = list(cursor, &ArchiveLimits::default()).unwrap().entries;
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[0].name, "big.bin");
        assert_eq!(entries[0].size, 16);
    }

    /// A name written with the UTF-8 flag set comes back as it went in, at two
    /// and at three bytes per character.
    #[test]
    fn a_utf8_entry_name_round_trips() {
        let name = "café/日本語.txt";
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file(name, opts).unwrap();
        zw.write_all(b"x").unwrap();
        let cursor = zw.finish().unwrap();

        let entries = list(cursor, &ArchiveLimits::default()).unwrap().entries;
        assert_eq!(entries[0].name, name);
    }

    #[test]
    fn cp437_decodes_the_high_half() {
        assert_eq!(decode_cp437(b"caf\x82.txt"), "café.txt");
        assert_eq!(decode_cp437(b"plain.txt"), "plain.txt");
    }

    #[test]
    fn a_zip_slip_entry_is_left_out_and_counted() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("../../etc/cron.d/x", opts).unwrap();
        zw.write_all(b"evil").unwrap();
        zw.start_file("ordinary.txt", opts).unwrap();
        zw.write_all(b"fine").unwrap();
        let cursor = zw.finish().unwrap();

        let listing = list(cursor, &ArchiveLimits::default()).unwrap();
        // The name never reaches a caller, which is the whole of the rule.
        assert!(!listing.entries.iter().any(|e| e.name.contains("cron.d")));
        assert_eq!(listing.skipped, 1);
        // And the entry beside it is still listed, which is the point of
        // counting instead of refusing.
        assert_eq!(listing.entries.len(), 1);
        assert_eq!(listing.entries[0].name, "ordinary.txt");
    }

    #[test]
    fn an_absolute_path_entry_is_left_out_and_counted() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("/etc/passwd", opts).unwrap();
        zw.write_all(b"evil").unwrap();
        let cursor = zw.finish().unwrap();

        let listing = list(cursor, &ArchiveLimits::default()).unwrap();
        assert!(listing.entries.is_empty());
        assert_eq!(listing.skipped, 1);
    }

    #[test]
    fn a_backslash_in_an_entry_name_is_left_out_and_counted() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("dir\\..\\..\\x", opts).unwrap();
        zw.write_all(b"evil").unwrap();
        let cursor = zw.finish().unwrap();

        let listing = list(cursor, &ArchiveLimits::default()).unwrap();
        assert!(listing.entries.is_empty());
        assert_eq!(listing.skipped, 1);
    }

    /// The archive this whole change is about: an installer far larger than
    /// any decompression budget, containing nothing unusual. It lists.
    #[test]
    fn a_large_archive_lists_rather_than_being_refused_for_its_size() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("big.bin", opts).unwrap();
        zw.write_all(&[0u8; 64]).unwrap();
        let mut cursor = zw.finish().unwrap();

        // Rewrite the entry's declared uncompressed size in the central
        // directory to 8 GiB. Nothing reads the content, so the declaration is
        // all a listing ever sees — which is exactly why capping it protected
        // nothing.
        let bytes = cursor.get_mut();
        let cd_at = bytes
            .windows(4)
            .position(|w| u32::from_le_bytes([w[0], w[1], w[2], w[3]]) == CDFH_SIG)
            .expect("a central directory record");
        let huge: u64 = 8 * 1024 * 1024 * 1024;
        bytes[cd_at + 24..cd_at + 28].copy_from_slice(&(huge as u32).to_le_bytes());

        let listing = list(cursor, &ArchiveLimits::default()).unwrap();
        assert_eq!(listing.entries.len(), 1);
        assert_eq!(listing.skipped, 0);
    }

    /// One central directory record, built by hand. `ZipWriter` masks a mode
    /// down to its permission bits, so the entry kinds this rejects cannot be
    /// written with it at all.
    fn cdfh(name: &str, unix_mode: u32) -> Vec<u8> {
        let mut r = vec![0u8; CDFH_LEN];
        r[0..4].copy_from_slice(&CDFH_SIG.to_le_bytes());
        r[4..6].copy_from_slice(&(HOST_UNIX << 8).to_le_bytes());
        r[8..10].copy_from_slice(&0x0800u16.to_le_bytes());
        r[20..24].copy_from_slice(&6u32.to_le_bytes());
        r[24..28].copy_from_slice(&6u32.to_le_bytes());
        r[28..30].copy_from_slice(&(name.len() as u16).to_le_bytes());
        r[38..42].copy_from_slice(&(unix_mode << 16).to_le_bytes());
        r.extend_from_slice(name.as_bytes());
        r
    }

    #[test]
    fn symlink_and_device_entries_are_left_out_and_counted() {
        for mode in [0o120777u32, 0o020644, 0o060644, 0o010644, 0o140644] {
            let cd = cdfh("odd", mode);
            let listing = parse_central_directory(&cd, 1, &ArchiveLimits::default())
                .unwrap_or_else(|e| panic!("{mode:o} should list, got {e:?}"));
            assert!(listing.entries.is_empty(), "{mode:o} was listed");
            assert_eq!(listing.skipped, 1, "{mode:o} was not counted");
        }
        // An ordinary file with a mode is not left out for having one.
        let ok = parse_central_directory(&cdfh("f", 0o100644), 1, &ArchiveLimits::default())
            .unwrap();
        assert_eq!(ok.entries[0].name, "f");
        assert_eq!(ok.skipped, 0);
    }

    /// A zip bomb is dangerous to decompress, and this never decompresses: it
    /// reads the central directory and copies the declared numbers out. The
    /// ratio is reported, not refused, because refusing it here bought nothing
    /// and cost every archive of compressible data.
    #[test]
    fn a_highly_compressible_entry_lists() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Deflated);
        zw.start_file("zeros.bin", opts).unwrap();
        let zeros = vec![0u8; 4 * 1024 * 1024];
        zw.write_all(&zeros).unwrap();
        let cursor = zw.finish().unwrap();

        let listing = list(cursor, &ArchiveLimits::default()).unwrap();
        assert_eq!(listing.entries.len(), 1);
        assert_eq!(listing.entries[0].size, 4 * 1024 * 1024);
        assert!(listing.entries[0].compressed_size < listing.entries[0].size / 100);
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
        let err = list(cursor, &tight_limits).expect_err("must reject");
        assert!(matches!(err, PreviewError::ArchiveRejected(_)));
    }

    /// The budget that replaces the file-size ceiling, enforced where the cost
    /// actually is.
    #[test]
    fn rejects_when_the_central_directory_budget_is_exceeded() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        for i in 0..40 {
            zw.start_file(format!("{i:0200}.txt"), opts).unwrap();
            zw.write_all(b"x").unwrap();
        }
        let cursor = zw.finish().unwrap();

        let tight_limits = ArchiveLimits {
            max_central_directory_bytes: 1024,
            ..ArchiveLimits::default()
        };
        match list(cursor, &tight_limits).expect_err("must reject") {
            PreviewError::ArchiveRejected(msg) => assert!(msg.contains("central directory")),
            other => panic!("expected ArchiveRejected, got {other:?}"),
        }
    }

    /// A corrupt archive stays "not a zip", which is a 404, rather than
    /// becoming a 422 that tells a caller a file they cannot read is one.
    #[test]
    fn a_file_that_is_not_a_zip_is_unsupported_not_rejected() {
        for bytes in [
            vec![0u8; 8],
            vec![0u8; 4096],
            b"PK\x03\x04 and then nothing that ends an archive".to_vec(),
        ] {
            let len = bytes.len() as u64;
            assert!(matches!(
                list_archive(Cursor::new(bytes), len, &ArchiveLimits::default()),
                Err(PreviewError::UnsupportedFormat)
            ));
        }
    }

    /// A central directory whose records do not parse is the same answer.
    #[test]
    fn a_truncated_central_directory_is_unsupported() {
        let mut zw = writer();
        let opts = zip::write::SimpleFileOptions::default()
            .compression_method(zip::CompressionMethod::Stored);
        zw.start_file("a.txt", opts).unwrap();
        zw.write_all(b"hello").unwrap();
        let cursor = zw.finish().unwrap();
        let mut bytes = cursor.into_inner();
        // Claim two entries where the directory holds one.
        let n = bytes.len();
        bytes[n - 22 + 10] = 2;
        bytes[n - 22 + 8] = 2;

        let len = bytes.len() as u64;
        assert!(matches!(
            list_archive(Cursor::new(bytes), len, &ArchiveLimits::default()),
            Err(PreviewError::UnsupportedFormat)
        ));
    }
}
