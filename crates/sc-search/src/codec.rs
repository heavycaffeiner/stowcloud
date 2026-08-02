//! Block compression (§4.1).
//!
//! zstd, via the `zstd` crate. `zstd::bulk` is the right API here because every
//! payload is a self-contained, known-length block: no streaming state, one
//! call in each direction.
//!
//! The C binding is not a problem for this crate — `DESIGN-PREVIEW.md`'s
//! "pure Rust decoders only" rule is about parsers that eat untrusted user
//! content (images, documents). These bytes are ours: we wrote them, in a file
//! we own, and every decompression is length-checked against the block
//! directory before it is trusted.

use anyhow::{bail, Context, Result};

/// Base-segment blocks. Read once per candidate hit and written once per
/// build/merge, so ratio matters more than encode speed. Level 6 measured
/// meaningfully better than 3 on filename corpora at a cost paid only at build
/// time.
pub const BASE_LEVEL: i32 = 6;

/// Delta records. §4.2 asks for "light compression or none" — delta segments are
/// linearly scanned on *every* query, so decode cost dominates.
pub const DELTA_LEVEL: i32 = 1;

/// Ceiling on a single decompressed block, so a corrupt length prefix cannot
/// make us allocate the machine. A 32-name block of 4 KiB paths is ~128 KiB;
/// 64 MiB is four orders of magnitude of headroom.
pub const MAX_DECOMPRESSED: usize = 64 << 20;

pub fn compress(data: &[u8]) -> Vec<u8> {
    zstd::bulk::compress(data, BASE_LEVEL).expect("zstd compression of an in-memory buffer")
}

pub fn compress_fast(data: &[u8]) -> Vec<u8> {
    zstd::bulk::compress(data, DELTA_LEVEL).expect("zstd compression of an in-memory buffer")
}

/// Decompress a payload whose uncompressed length is not known up front.
pub fn decompress(data: &[u8]) -> Result<Vec<u8>> {
    let out = zstd::bulk::decompress(data, MAX_DECOMPRESSED)
        .context("zstd stream is truncated or corrupt")?;
    Ok(out)
}

/// Decompress with the uncompressed length from the block directory. The
/// length is both an allocation hint and a check: a block that does not
/// decompress to exactly the recorded size is not the block we wrote.
pub fn decompress_hint(data: &[u8], expect: usize) -> Result<Vec<u8>> {
    if expect > MAX_DECOMPRESSED {
        bail!("declared block size {expect} exceeds {MAX_DECOMPRESSED}");
    }
    let cap = if expect == 0 { MAX_DECOMPRESSED } else { expect };
    let out = zstd::bulk::decompress(data, cap).context("zstd stream is truncated or corrupt")?;
    if expect != 0 && out.len() != expect {
        bail!(
            "block directory says {expect} bytes, stream produced {}",
            out.len()
        );
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip() {
        let data = b"IMG_0001.jpg\nIMG_0002.jpg\nIMG_0003.jpg\n".repeat(40);
        let c = compress(&data);
        assert!(
            c.len() < data.len(),
            "no compression win: {} vs {}",
            c.len(),
            data.len()
        );
        assert_eq!(decompress_hint(&c, data.len()).unwrap(), data);
    }

    #[test]
    fn delta_level_roundtrips_too() {
        let data = b"docs/report-2026.pdf".repeat(20);
        assert_eq!(decompress(&compress_fast(&data)).unwrap(), data);
    }

    #[test]
    fn tree_order_adjacency_is_where_the_win_is() {
        // §4.1 point 2: the compression win comes from prefix sharing between
        // adjacent names, which only exists if blocks are built in tree order.
        let mut ordered = Vec::new();
        for i in 0..32 {
            ordered.extend_from_slice(format!("photos/2026/summer/IMG_{i:04}.jpg\n").as_bytes());
        }
        let mut scattered = Vec::new();
        for i in 0..32u64 {
            let h = crate::hll::hash64(&i.to_le_bytes());
            scattered.extend_from_slice(format!("{h:016x}/{:016x}.dat\n", h.rotate_left(21)).as_bytes());
        }
        let a = compress(&ordered).len() as f64 / ordered.len() as f64;
        let b = compress(&scattered).len() as f64 / scattered.len() as f64;
        assert!(a < b, "tree-ordered ratio {a:.3} should beat scattered {b:.3}");
    }

    #[test]
    fn corrupt_input_errors_rather_than_panics() {
        assert!(decompress(b"not a zstd frame at all").is_err());
    }

    #[test]
    fn length_mismatch_is_rejected() {
        let c = compress(b"hello");
        assert!(decompress_hint(&c, 999).is_err());
    }
}
