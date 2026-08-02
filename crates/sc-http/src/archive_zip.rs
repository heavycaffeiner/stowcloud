//! A minimal, from-scratch **streaming** ZIP writer — `STORE` only, always
//! ZIP64, always the data-descriptor form — used by `POST /api/fs/archive`
//! (`DESIGN-PREVIEW.md` §8).
//!
//! The `zip` crate (already a workspace dependency, used read-only by
//! `sc-preview`'s archive *listing*) cannot be reused here: its `ZipWriter`
//! requires `Write + Seek` because it back-patches the local header once an
//! entry's final size is known. An HTTP response body is `Write`-only and
//! chunked — we deliberately never learn (or promise) a `Content-Length`
//! (§8: "no need to know the size in advance... sent chunked, without
//! `Content-Length`"), so every entry uses the general-purpose bit-3 "data descriptor"
//! form: the local header carries zeroed size/CRC fields, and the real
//! values follow the entry's bytes. This is exactly the trick that makes
//! "cut an entry short because the file vanished mid-stream, and the archive
//! is still valid" (§8) possible — nothing before the entry's data commits to
//! a size.
//!
//! **Always ZIP64.** Rather than branch on "is this entry/archive actually
//! over 4 GiB or 65535 entries", every local header, every central directory
//! record, and the end-of-central-directory trailer are written in their
//! ZIP64 form unconditionally. One code path, always correct, and the
//! archives this crate produces (already-compressed media, potentially many
//! GiB) are exactly the case ZIP64 exists for.

use std::io::{self, Read, Write};

/// Bytes read per `Read::read` call while streaming an entry's content —
/// same bound as `sc_core::stream::CHUNK`, kept independent here so this
/// module has no `sc-core` dependency (`sc-http` never depends on it; see
/// `ARCHITECTURE.md` §1).
const COPY_CHUNK: usize = 256 * 1024;

const LOCAL_HEADER_SIG: u32 = 0x0403_4b50;
const DATA_DESCRIPTOR_SIG: u32 = 0x0807_4b50;
const CENTRAL_HEADER_SIG: u32 = 0x0201_4b50;
const ZIP64_EOCD_SIG: u32 = 0x0606_4b50;
const ZIP64_LOCATOR_SIG: u32 = 0x0706_4b50;
const EOCD_SIG: u32 = 0x0605_4b50;
const ZIP64_EXTRA_TAG: u16 = 0x0001;

const VERSION_NEEDED_ZIP64: u16 = 45;
const UNIX_FILE_ATTR: u32 = 0o100644 << 16;
const UNIX_DIR_ATTR: u32 = (0o040755 << 16) | 0x10; // FAT directory bit for cross-tool compat.

/// General-purpose bit flag, local *and* central headers: bit 3 (data
/// descriptor follows, see the module doc comment) **or** bit 11 (`0x0800`,
/// "Language encoding flag (EFS)" in the APPNOTE.TXT general-purpose flag
/// table). Without bit 11, an entry name is legacy-defined as either raw
/// ASCII or CP437-ish "whatever the writer's local codepage was" — nothing
/// in the format itself says the bytes are UTF-8. This deployment writes
/// Korean filenames into every entry name and this crate always encodes them
/// as UTF-8 bytes (`&str` -> `.as_bytes()` below), so leaving bit 11 unset
/// was a real bug, not a theoretical one: every mainstream extractor
/// (Windows Explorer, 7-Zip without an explicit codepage override, `unzip`
/// on a non-UTF-8 locale) falls back to guessing a legacy codepage and
/// mis-decodes the name, producing mojibake or, worse, a name that no
/// longer round-trips back to the original bytes at all. Setting the flag
/// tells every compliant reader "the name and comment fields are UTF-8,
/// don't guess" — which is simply true here, since that's the only encoding
/// this writer ever produces.
const GP_FLAG: u16 = 0x0008 | 0x0800;

struct CentralRecord {
    name: Vec<u8>,
    crc32: u32,
    size: u64,
    offset: u64,
    is_dir: bool,
    dos_time: u16,
    dos_date: u16,
}

/// Streams a ZIP archive to `W` as entries are added. Every write goes
/// straight through to `out` — nothing is buffered beyond one
/// [`COPY_CHUNK`]-sized copy buffer, so archive size is unbounded by memory.
pub struct ZipStreamWriter<W: Write> {
    out: W,
    offset: u64,
    records: Vec<CentralRecord>,
}

impl<W: Write> ZipStreamWriter<W> {
    pub fn new(out: W) -> Self {
        Self { out, offset: 0, records: Vec::new() }
    }

    fn write_all_counted(&mut self, buf: &[u8]) -> io::Result<()> {
        self.out.write_all(buf)?;
        self.offset += buf.len() as u64;
        Ok(())
    }

    /// A directory placeholder entry (trailing `/`, zero bytes). Not
    /// strictly necessary for non-empty directories — extracting the files
    /// beneath them recreates the tree — but it is what makes an *empty*
    /// directory show up in the archive at all.
    pub fn add_dir(&mut self, name: &str, mtime_ns: i128) -> io::Result<()> {
        let mut full = name.trim_end_matches('/').to_string();
        full.push('/');
        let (dos_time, dos_date) = dos_datetime(mtime_ns);
        let header_offset = self.offset;
        self.write_local_header(full.as_bytes(), dos_time, dos_date)?;
        self.write_data_descriptor(0, 0)?;
        self.records.push(CentralRecord {
            name: full.into_bytes(),
            crc32: 0,
            size: 0,
            offset: header_offset,
            is_dir: true,
            dos_time,
            dos_date,
        });
        Ok(())
    }

    /// Streams `reader` into a new STORE entry named `name`. Returns the
    /// number of bytes actually copied (the caller may want it for logging;
    /// the archive itself never advertises a size ahead of time).
    pub fn add_file(&mut self, name: &str, mtime_ns: i128, reader: &mut dyn Read) -> io::Result<u64> {
        let (dos_time, dos_date) = dos_datetime(mtime_ns);
        let header_offset = self.offset;
        self.write_local_header(name.as_bytes(), dos_time, dos_date)?;

        let mut hasher = Crc32::new();
        let mut total: u64 = 0;
        let mut buf = vec![0u8; COPY_CHUNK];
        loop {
            let n = reader.read(&mut buf)?;
            if n == 0 {
                break;
            }
            hasher.update(&buf[..n]);
            self.write_all_counted(&buf[..n])?;
            total += n as u64;
        }
        let crc = hasher.finish();
        self.write_data_descriptor(crc, total)?;
        self.records.push(CentralRecord {
            name: name.as_bytes().to_vec(),
            crc32: crc,
            size: total,
            offset: header_offset,
            is_dir: false,
            dos_time,
            dos_date,
        });
        Ok(total)
    }

    /// A small, already-in-memory entry (used for `_skipped.txt`) — just
    /// `add_file` over a `Cursor`, so it goes through the exact same header
    /// logic as a real file.
    pub fn add_bytes(&mut self, name: &str, mtime_ns: i128, data: &[u8]) -> io::Result<()> {
        let mut cursor = io::Cursor::new(data);
        self.add_file(name, mtime_ns, &mut cursor)?;
        Ok(())
    }

    fn write_local_header(&mut self, name: &[u8], dos_time: u16, dos_date: u16) -> io::Result<()> {
        let mut hdr = Vec::with_capacity(30 + name.len() + 20);
        hdr.extend_from_slice(&LOCAL_HEADER_SIG.to_le_bytes());
        hdr.extend_from_slice(&VERSION_NEEDED_ZIP64.to_le_bytes());
        hdr.extend_from_slice(&GP_FLAG.to_le_bytes()); // bit 3: data descriptor follows; bit 11: name is UTF-8
        hdr.extend_from_slice(&0u16.to_le_bytes()); // compression = store
        hdr.extend_from_slice(&dos_time.to_le_bytes());
        hdr.extend_from_slice(&dos_date.to_le_bytes());
        hdr.extend_from_slice(&0u32.to_le_bytes()); // crc-32, deferred
        hdr.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // compressed size -> see zip64 extra
        hdr.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // uncompressed size -> see zip64 extra
        hdr.extend_from_slice(&(name.len() as u16).to_le_bytes());
        hdr.extend_from_slice(&20u16.to_le_bytes()); // extra field length: tag(2)+size(2)+unc(8)+comp(8)
        hdr.extend_from_slice(name);
        hdr.extend_from_slice(&ZIP64_EXTRA_TAG.to_le_bytes());
        hdr.extend_from_slice(&16u16.to_le_bytes());
        hdr.extend_from_slice(&0u64.to_le_bytes()); // uncompressed size placeholder
        hdr.extend_from_slice(&0u64.to_le_bytes()); // compressed size placeholder
        self.write_all_counted(&hdr)
    }

    fn write_data_descriptor(&mut self, crc32: u32, size: u64) -> io::Result<()> {
        let mut dd = Vec::with_capacity(24);
        dd.extend_from_slice(&DATA_DESCRIPTOR_SIG.to_le_bytes());
        dd.extend_from_slice(&crc32.to_le_bytes());
        dd.extend_from_slice(&size.to_le_bytes()); // compressed size (== size, STORE)
        dd.extend_from_slice(&size.to_le_bytes()); // uncompressed size
        self.write_all_counted(&dd)
    }

    /// Writes the central directory, the ZIP64 end-of-central-directory
    /// record + locator, and the classic (sentinel-valued) EOCD, then
    /// returns the underlying writer.
    pub fn finish(mut self) -> io::Result<W> {
        let entry_count = self.records.len() as u64;
        let cd_start = self.offset;
        for rec in std::mem::take(&mut self.records) {
            let mut h = Vec::with_capacity(46 + rec.name.len() + 28);
            h.extend_from_slice(&CENTRAL_HEADER_SIG.to_le_bytes());
            h.extend_from_slice(&VERSION_NEEDED_ZIP64.to_le_bytes()); // version made by
            h.extend_from_slice(&VERSION_NEEDED_ZIP64.to_le_bytes()); // version needed
            h.extend_from_slice(&GP_FLAG.to_le_bytes()); // must match the local header's flag exactly
            h.extend_from_slice(&0u16.to_le_bytes()); // store
            h.extend_from_slice(&rec.dos_time.to_le_bytes());
            h.extend_from_slice(&rec.dos_date.to_le_bytes());
            h.extend_from_slice(&rec.crc32.to_le_bytes());
            h.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // compressed size -> extra
            h.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // uncompressed size -> extra
            h.extend_from_slice(&(rec.name.len() as u16).to_le_bytes());
            h.extend_from_slice(&28u16.to_le_bytes()); // extra len: tag2+size2+unc8+comp8+off8
            h.extend_from_slice(&0u16.to_le_bytes()); // comment length
            h.extend_from_slice(&0u16.to_le_bytes()); // disk number start
            h.extend_from_slice(&0u16.to_le_bytes()); // internal attrs
            let ext_attr = if rec.is_dir { UNIX_DIR_ATTR } else { UNIX_FILE_ATTR };
            h.extend_from_slice(&ext_attr.to_le_bytes());
            h.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // local header offset -> extra
            h.extend_from_slice(&rec.name);
            h.extend_from_slice(&ZIP64_EXTRA_TAG.to_le_bytes());
            h.extend_from_slice(&24u16.to_le_bytes());
            h.extend_from_slice(&rec.size.to_le_bytes()); // uncompressed size
            h.extend_from_slice(&rec.size.to_le_bytes()); // compressed size
            h.extend_from_slice(&rec.offset.to_le_bytes()); // local header offset
            self.write_all_counted(&h)?;
        }
        let cd_size = self.offset - cd_start;
        let zip64_eocd_offset = self.offset;

        // ZIP64 end-of-central-directory record.
        let mut z = Vec::with_capacity(56);
        z.extend_from_slice(&ZIP64_EOCD_SIG.to_le_bytes());
        z.extend_from_slice(&44u64.to_le_bytes()); // size of this record, excluding sig + this field
        z.extend_from_slice(&VERSION_NEEDED_ZIP64.to_le_bytes()); // version made by
        z.extend_from_slice(&VERSION_NEEDED_ZIP64.to_le_bytes()); // version needed
        z.extend_from_slice(&0u32.to_le_bytes()); // number of this disk
        z.extend_from_slice(&0u32.to_le_bytes()); // disk with start of CD
        z.extend_from_slice(&entry_count.to_le_bytes()); // entries on this disk
        z.extend_from_slice(&entry_count.to_le_bytes()); // total entries
        z.extend_from_slice(&cd_size.to_le_bytes());
        z.extend_from_slice(&cd_start.to_le_bytes());
        self.write_all_counted(&z)?;

        // ZIP64 end-of-central-directory locator.
        let mut loc = Vec::with_capacity(20);
        loc.extend_from_slice(&ZIP64_LOCATOR_SIG.to_le_bytes());
        loc.extend_from_slice(&0u32.to_le_bytes()); // disk with the zip64 EOCD record
        loc.extend_from_slice(&zip64_eocd_offset.to_le_bytes());
        loc.extend_from_slice(&1u32.to_le_bytes()); // total number of disks
        self.write_all_counted(&loc)?;

        // Classic EOCD, sentinel-valued so readers know to consult the
        // ZIP64 records above instead.
        let mut e = Vec::with_capacity(22);
        e.extend_from_slice(&EOCD_SIG.to_le_bytes());
        e.extend_from_slice(&0u16.to_le_bytes()); // disk number
        e.extend_from_slice(&0u16.to_le_bytes()); // disk with start of CD
        e.extend_from_slice(&0xFFFFu16.to_le_bytes()); // entries on this disk -> zip64
        e.extend_from_slice(&0xFFFFu16.to_le_bytes()); // total entries -> zip64
        e.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // size of CD -> zip64
        e.extend_from_slice(&0xFFFF_FFFFu32.to_le_bytes()); // offset of CD -> zip64
        e.extend_from_slice(&0u16.to_le_bytes()); // comment length
        self.write_all_counted(&e)?;

        Ok(self.out)
    }
}

/// A tiny table-based CRC-32 (IEEE 802.3 / `zlib`/PNG/ZIP polynomial,
/// `0xEDB88320` reflected) — hand-rolled instead of pulling in a new
/// dependency for one well-known 256-entry table.
struct Crc32 {
    state: u32,
    table: [u32; 256],
}

impl Crc32 {
    fn new() -> Self {
        let mut table = [0u32; 256];
        let mut i = 0u32;
        while i < 256 {
            let mut c = i;
            let mut k = 0;
            while k < 8 {
                c = if c & 1 != 0 { 0xEDB8_8320 ^ (c >> 1) } else { c >> 1 };
                k += 1;
            }
            table[i as usize] = c;
            i += 1;
        }
        Self { state: 0xFFFF_FFFF, table }
    }

    fn update(&mut self, data: &[u8]) {
        let mut c = self.state;
        for &b in data {
            c = self.table[((c ^ b as u32) & 0xFF) as usize] ^ (c >> 8);
        }
        self.state = c;
    }

    fn finish(&self) -> u32 {
        self.state ^ 0xFFFF_FFFF
    }
}

/// Unix nanoseconds -> MS-DOS `(time, date)` pair used by ZIP local/central
/// headers. Years before 1980 or after 2107 (the DOS date field's 7-bit
/// range) clamp to the nearest representable value rather than wrapping —
/// wrong-but-bounded beats corrupting the header.
fn dos_datetime(mtime_ns: i128) -> (u16, u16) {
    let secs = (mtime_ns / 1_000_000_000).max(0) as i64;
    let days = secs.div_euclid(86_400);
    let sec_of_day = secs.rem_euclid(86_400);
    let (year, month, day) = civil_from_days(days);

    let hour = (sec_of_day / 3600) as u16;
    let minute = ((sec_of_day % 3600) / 60) as u16;
    let second = (sec_of_day % 60) as u16;
    let dos_time = (hour << 11) | (minute << 5) | (second / 2);

    let dos_year = year.clamp(1980, 2107) - 1980;
    let dos_date = ((dos_year as u16) << 9) | ((month as u16) << 5) | (day as u16);
    (dos_time, dos_date)
}

/// Howard Hinnant's `civil_from_days`: proleptic-Gregorian days-since-epoch
/// -> `(year, month, day)`, exact and branch-free apart from the era split.
/// <https://howardhinnant.github.io/date_algorithms.html>
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = (z - era * 146_097) as u64; // [0, 146096]
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365; // [0, 399]
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100); // [0, 365]
    let mp = (5 * doy + 2) / 153; // [0, 11]
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32; // [1, 31]
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32; // [1, 12]
    let year = if m <= 2 { y + 1 } else { y };
    (year, m, d)
}

// The `finish()` method above needs the entry count for the ZIP64 EOCD
// record; tracked separately here because `finish` consumes `self.records`
// while building the central directory. Reopened as an inherent impl block
// so the field access above stays simple.
impl<W: Write> ZipStreamWriter<W> {
    /// Entries added so far. Exposed so `finish` (and its EOCD writer) can
    /// report an accurate count even though building the central directory
    /// drains `records`.
    pub fn entry_count(&self) -> usize {
        self.records.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crc32_matches_known_vector() {
        let mut c = Crc32::new();
        c.update(b"123456789");
        assert_eq!(c.finish(), 0xCBF4_3926);
    }

    #[test]
    fn civil_from_days_epoch_is_1970_01_01() {
        assert_eq!(civil_from_days(0), (1970, 1, 1));
    }

    #[test]
    fn civil_from_days_matches_a_known_date() {
        // 2024-03-15 is 19797 days after the epoch.
        assert_eq!(civil_from_days(19797), (2024, 3, 15));
    }

    /// Round-trip through the independent, `Seek`-based `zip` crate reader —
    /// the strongest test available short of shelling out to a real `unzip`.
    /// If this parses and the bytes match, real tools will open it too.
    #[test]
    fn round_trips_through_an_independent_reader() {
        let mut w = ZipStreamWriter::new(io::Cursor::new(Vec::new()));
        w.add_dir("photos", 0).unwrap();
        let mut a = io::Cursor::new(b"hello world".to_vec());
        w.add_file("photos/a.txt", 0, &mut a).unwrap();
        let big = vec![0x5Au8; COPY_CHUNK * 2 + 777]; // spans multiple copy chunks
        let mut b = io::Cursor::new(big.clone());
        w.add_file("photos/big.bin", 0, &mut b).unwrap();
        let out = w.finish().unwrap().into_inner();

        let mut zip = zip::ZipArchive::new(io::Cursor::new(out)).expect("valid zip");
        assert_eq!(zip.len(), 3);

        let mut names: Vec<String> = (0..zip.len()).map(|i| zip.by_index(i).unwrap().name().to_string()).collect();
        names.sort();
        assert_eq!(names, vec!["photos/", "photos/a.txt", "photos/big.bin"]);

        let mut a_entry = zip.by_name("photos/a.txt").unwrap();
        assert_eq!(a_entry.compression(), zip::CompressionMethod::Stored);
        let mut a_content = Vec::new();
        a_entry.read_to_end(&mut a_content).unwrap();
        assert_eq!(a_content, b"hello world");
        drop(a_entry);

        let mut big_entry = zip.by_name("photos/big.bin").unwrap();
        let mut big_content = Vec::new();
        big_entry.read_to_end(&mut big_content).unwrap();
        assert_eq!(big_content, big);
    }

    #[test]
    fn empty_archive_is_still_a_valid_zip() {
        let w = ZipStreamWriter::new(io::Cursor::new(Vec::new()));
        let out = w.finish().unwrap().into_inner();
        let zip = zip::ZipArchive::new(io::Cursor::new(out)).expect("valid empty zip");
        assert_eq!(zip.len(), 0);
    }

    /// Regression for the mangled-non-ASCII-name bug: without general-purpose
    /// bit 11 set, a reader has no signal that the entry name is UTF-8 and
    /// falls back to guessing a legacy codepage, corrupting any non-ASCII
    /// name. This deployment is Korean-language, so this is not an edge
    /// case. Fails on the pre-fix code (bit 11 unset -> the independent
    /// `zip` crate reader, which honors the flag exactly like real-world
    /// extractors, reports a name that has been re-decoded through the
    /// legacy fallback path and no longer matches the original UTF-8
    /// bytes) and passes now that the flag is set on both the local and
    /// central headers.
    #[test]
    fn korean_filename_round_trips_through_an_independent_reader() {
        let name = "사진/여름 휴가 사진.jpg"; // "photos/summer vacation photo.jpg"
        let mut w = ZipStreamWriter::new(io::Cursor::new(Vec::new()));
        let mut data = io::Cursor::new(b"jpeg bytes go here".to_vec());
        w.add_file(name, 0, &mut data).unwrap();
        let out = w.finish().unwrap().into_inner();

        let mut zip = zip::ZipArchive::new(io::Cursor::new(out)).expect("valid zip");
        assert_eq!(zip.len(), 1);
        let entry = zip.by_index(0).unwrap();
        // The independent `zip` crate only returns the UTF-8-decoded name
        // (rather than raw bytes reinterpreted through a legacy codepage)
        // when it sees bit 11 set on the entry it is reading -- so this
        // assertion is itself a check that the flag made it onto the wire,
        // not only that this crate's own writer and reader agree.
        assert_eq!(entry.name(), name, "extractor must recover the exact original name, not mojibake");
    }

    /// Directly inspects the general-purpose flag field's raw bytes in the
    /// local header, independent of whether any particular reader crate
    /// happens to interpret it correctly -- this is the actual bit on the
    /// wire that `PowerShell Expand-Archive`/`python -m zipfile`/every real
    /// extractor keys off.
    #[test]
    fn local_header_general_purpose_flag_has_utf8_bit_set() {
        let mut w = ZipStreamWriter::new(io::Cursor::new(Vec::new()));
        let mut data = io::Cursor::new(b"x".to_vec());
        w.add_file("a.txt", 0, &mut data).unwrap();
        let out = w.finish().unwrap().into_inner();
        // Local header layout: sig(4) version(2) flag(2) ...
        let flag = u16::from_le_bytes([out[6], out[7]]);
        assert_eq!(flag & 0x0800, 0x0800, "bit 11 (UTF-8 name) must be set");
        assert_eq!(flag & 0x0008, 0x0008, "bit 3 (data descriptor) must still be set");
    }

    #[test]
    fn a_short_read_still_produces_a_valid_entry() {
        // Simulates the "file vanished mid-stream" case (`DESIGN-PREVIEW.md`
        // §8): the reader yields fewer bytes than some hypothetical declared
        // size ever promised, because none was ever promised.
        struct Flaky(usize);
        impl Read for Flaky {
            fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
                if self.0 == 0 {
                    return Ok(0);
                }
                let n = buf.len().min(self.0).min(10);
                buf[..n].fill(b'x');
                self.0 -= n;
                Ok(n)
            }
        }
        let mut w = ZipStreamWriter::new(io::Cursor::new(Vec::new()));
        let mut r = Flaky(37);
        let written = w.add_file("f.txt", 0, &mut r).unwrap();
        assert_eq!(written, 37);
        let out = w.finish().unwrap().into_inner();
        let mut zip = zip::ZipArchive::new(io::Cursor::new(out)).unwrap();
        let mut f = zip.by_name("f.txt").unwrap();
        let mut content = Vec::new();
        f.read_to_end(&mut content).unwrap();
        assert_eq!(content.len(), 37);
    }
}
