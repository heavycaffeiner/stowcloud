//! Append-only record framing for `delta.NNN.idx` and `tomb.idx` (§4.2).
//!
//! ```text
//! record := u32 payload_len | u32 fnv1a32(payload) | payload
//! ```
//!
//! Crash safety comes from the length prefix plus the checksum: a torn tail —
//! a record whose header or body did not make it to disk — fails to parse, and
//! [`read_records`] reports the offset of the last *complete* record so the
//! opener can truncate the file back to it. Everything before the tear is
//! still good, because the file is only ever appended to.

use std::fs::{File, OpenOptions};
use std::io::{Read, Write};
use std::path::Path;

use anyhow::{Context, Result};

pub const FRAME_HEADER: usize = 8;

pub fn fnv1a32(data: &[u8]) -> u32 {
    let mut h: u32 = 0x811c_9dc5;
    for b in data {
        h ^= *b as u32;
        h = h.wrapping_mul(0x0100_0193);
    }
    h
}

pub fn frame(payload: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(FRAME_HEADER + payload.len());
    out.extend_from_slice(&(payload.len() as u32).to_le_bytes());
    out.extend_from_slice(&fnv1a32(payload).to_le_bytes());
    out.extend_from_slice(payload);
    out
}

/// Append one framed record. `O_APPEND`-equivalent, one write, then flush —
/// this is the O(1) write path that the whole segment design exists to enable
/// (§4.2, "writes are O(1)").
pub fn append_record(path: &Path, payload: &[u8]) -> Result<u64> {
    let framed = frame(payload);
    let mut f = OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .with_context(|| format!("append to {path:?}"))?;
    f.write_all(&framed)?;
    f.flush()?;
    Ok(framed.len() as u64)
}

pub struct Recovered {
    pub records: Vec<Vec<u8>>,
    /// Byte length of the intact prefix.
    pub good_len: u64,
    /// A torn tail was found and `good_len < file length`.
    pub torn: bool,
}

/// Read every intact record. Never errors on a torn tail — that is an expected
/// state after a crash, not corruption.
pub fn read_records(path: &Path) -> Result<Recovered> {
    let mut buf = Vec::new();
    match File::open(path) {
        Ok(mut f) => {
            f.read_to_end(&mut buf)?;
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return Ok(Recovered {
                records: Vec::new(),
                good_len: 0,
                torn: false,
            })
        }
        Err(e) => return Err(e).with_context(|| format!("read {path:?}")),
    }

    let mut records = Vec::new();
    let mut pos = 0usize;
    loop {
        if pos + FRAME_HEADER > buf.len() {
            break;
        }
        let len = u32::from_le_bytes(buf[pos..pos + 4].try_into().unwrap()) as usize;
        let sum = u32::from_le_bytes(buf[pos + 4..pos + 8].try_into().unwrap());
        let body = pos + FRAME_HEADER;
        let end = match body.checked_add(len) {
            Some(e) if e <= buf.len() => e,
            _ => break,
        };
        if fnv1a32(&buf[body..end]) != sum {
            break;
        }
        records.push(buf[body..end].to_vec());
        pos = end;
    }

    Ok(Recovered {
        records,
        good_len: pos as u64,
        torn: pos < buf.len(),
    })
}

/// Cut a torn tail off. Called by the opener when [`read_records`] reports one.
pub fn truncate_to(path: &Path, good_len: u64) -> Result<()> {
    let f = OpenOptions::new()
        .write(true)
        .open(path)
        .with_context(|| format!("truncate {path:?}"))?;
    f.set_len(good_len)?;
    f.sync_all().ok();
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn append_and_read() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("delta.000.idx");
        append_record(&p, b"first").unwrap();
        append_record(&p, b"second").unwrap();
        let r = read_records(&p).unwrap();
        assert_eq!(r.records, vec![b"first".to_vec(), b"second".to_vec()]);
        assert!(!r.torn);
    }

    #[test]
    fn missing_file_is_empty_not_an_error() {
        let dir = tempdir().unwrap();
        let r = read_records(&dir.path().join("nope.idx")).unwrap();
        assert!(r.records.is_empty());
        assert!(!r.torn);
    }

    #[test]
    fn torn_tail_keeps_the_intact_prefix() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("delta.000.idx");
        append_record(&p, b"keep-this-one").unwrap();
        let good = std::fs::metadata(&p).unwrap().len();
        append_record(&p, b"this-one-was-being-written-when-the-power-went-out").unwrap();

        // Simulate a torn write: the tail record is half on disk.
        let total = std::fs::metadata(&p).unwrap().len();
        truncate_to(&p, total - 12).unwrap();

        let r = read_records(&p).unwrap();
        assert_eq!(r.records, vec![b"keep-this-one".to_vec()]);
        assert!(r.torn);
        assert_eq!(r.good_len, good);

        // And after truncation the file is clean and appendable again.
        truncate_to(&p, r.good_len).unwrap();
        append_record(&p, b"after-recovery").unwrap();
        let r2 = read_records(&p).unwrap();
        assert_eq!(
            r2.records,
            vec![b"keep-this-one".to_vec(), b"after-recovery".to_vec()]
        );
        assert!(!r2.torn);
    }

    #[test]
    fn bit_flip_in_the_body_stops_the_scan() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("t.idx");
        append_record(&p, b"aaaa").unwrap();
        append_record(&p, b"bbbb").unwrap();
        let mut bytes = std::fs::read(&p).unwrap();
        let n = bytes.len();
        bytes[n - 1] ^= 0xff;
        std::fs::write(&p, &bytes).unwrap();
        let r = read_records(&p).unwrap();
        assert_eq!(r.records, vec![b"aaaa".to_vec()]);
        assert!(r.torn);
    }

    #[test]
    fn truncated_header_is_a_torn_tail() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("t.idx");
        append_record(&p, b"x").unwrap();
        let mut bytes = std::fs::read(&p).unwrap();
        bytes.extend_from_slice(&[0u8, 0, 0]); // three bytes of a header
        std::fs::write(&p, &bytes).unwrap();
        let r = read_records(&p).unwrap();
        assert_eq!(r.records.len(), 1);
        assert!(r.torn);
    }
}
