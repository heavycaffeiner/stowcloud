//! `FileHandle::copy_range_from` — byte-identical results across the sizes
//! that matter for the buffered-loop boundary (256 KiB, see
//! `crate::copy::BUF_LEN`), through a real `ShareRoot`/`FileHandle`, not the
//! bare closures `crate::copy`'s own unit tests exercise.
//!
//! On the deployment target (Linux, `/srv/shares` is XFS) this goes through
//! the real `copy_file_range` kernel primitive end to end; on the portable
//! dev backend it's always the buffered fallback. Either way the contract
//! under test — same bytes out as in, at arbitrary src/dst offsets — must
//! hold.

use sc_vfs::{SafePath, ShareId, ShareRoot, SharePolicy};
use proptest::prelude::*;

const DEPTH: u16 = 64;
const BUF_LEN: usize = 256 * 1024; // must track sc_vfs::copy::BUF_LEN (private)

fn path(s: &str) -> SafePath {
    SafePath::parse(s, DEPTH).unwrap()
}

fn root(id: u32) -> (tempfile::TempDir, ShareRoot) {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(id), dir.path(), SharePolicy::default()).unwrap();
    (dir, root)
}

/// Write `data` to a fresh `src.bin`, copy `len` bytes starting at
/// `src_off` into a fresh `dst.bin` at `dst_off` (whose tail is
/// pre-populated with a sentinel byte so a copy that's short, or that
/// starts partway through the file, doesn't accidentally read as "correct"
/// zeros), and assert the destination matches byte-for-byte.
fn assert_copy_round_trips(root: &ShareRoot, data: &[u8], src_off: u64, dst_off: u64, len: u64) {
    let src_path = path("src.bin");
    let dst_path = path("dst.bin");

    // `create_excl` is `O_WRONLY` on the Linux backend (`ShareRoot::create_excl`
    // doc / `linux.rs`) — deliberately write-only, matching every real caller
    // (which only ever writes through a freshly created handle). A real
    // `copy_file_range` source needs read access, so — exactly like
    // `sc-core`/`sc-upload` do — reopen through `open_read` (RDWR-or-RDONLY)
    // rather than reusing the creating handle as the copy source.
    {
        let fh = root.create_excl(&src_path, 0o644).unwrap();
        fh.write_at(data, 0).unwrap();
        fh.sync_data().unwrap();
    }
    let src_fh = root.open_read(&src_path).unwrap();

    let dst_prefill_len = (dst_off + len + 16) as usize;
    {
        let fh = root.create_excl(&dst_path, 0o644).unwrap();
        fh.write_at(&vec![0xAAu8; dst_prefill_len], 0).unwrap();
    }
    let dst_fh = root.open_read(&dst_path).unwrap();

    let copied = dst_fh.copy_range_from(&src_fh, src_off, dst_off, len).unwrap();

    let available = data.len() as u64 - src_off.min(data.len() as u64);
    let expected_copied = len.min(available);
    assert_eq!(copied, expected_copied, "short-copy accounting must match what was actually available");

    let mut got = vec![0u8; copied as usize];
    let n = dst_fh.read_at(&mut got, dst_off).unwrap();
    assert_eq!(n, copied as usize);
    let want = &data[src_off as usize..src_off as usize + copied as usize];
    assert_eq!(&got[..], want, "copied bytes must match the source exactly");

    // Bytes outside the copied range must be untouched (still the sentinel),
    // not zeroed or garbage from an over-wide write.
    if dst_off > 0 {
        let mut before = vec![0u8; dst_off as usize];
        dst_fh.read_at(&mut before, 0).unwrap();
        assert!(before.iter().all(|&b| b == 0xAA), "copy must not touch bytes before dst_off");
    }

    root.unlink(&src_path).unwrap();
    root.unlink(&dst_path).unwrap();
}

#[test]
fn zero_bytes() {
    let (_dir, root) = root(100);
    assert_copy_round_trips(&root, b"hello world", 0, 0, 0);
}

#[test]
fn one_byte() {
    let (_dir, root) = root(101);
    assert_copy_round_trips(&root, b"hello world", 0, 0, 1);
}

#[test]
fn one_byte_at_nonzero_offsets() {
    let (_dir, root) = root(102);
    assert_copy_round_trips(&root, b"hello world", 4, 7, 1);
}

#[test]
fn exactly_one_buffer() {
    let (_dir, root) = root(103);
    let data: Vec<u8> = (0..BUF_LEN).map(|i| (i % 251) as u8).collect();
    assert_copy_round_trips(&root, &data, 0, 0, BUF_LEN as u64);
}

#[test]
fn several_buffers_plus_a_remainder() {
    let (_dir, root) = root(104);
    let total = BUF_LEN * 3 + 12_345;
    let data: Vec<u8> = (0..total).map(|i| (i % 251) as u8).collect();
    assert_copy_round_trips(&root, &data, 0, 0, total as u64);
}

#[test]
fn several_buffers_plus_a_remainder_at_nonzero_offsets() {
    let (_dir, root) = root(105);
    let total = BUF_LEN * 3 + 54_321;
    let data: Vec<u8> = (0..total).map(|i| ((i * 7) % 251) as u8).collect();
    // Copy a sub-range that itself spans several buffers, starting and
    // landing at offsets that don't line up with BUF_LEN boundaries.
    let src_off = 777u64;
    let dst_off = 999u64;
    let len = (BUF_LEN * 2 + 4096) as u64;
    assert_copy_round_trips(&root, &data, src_off, dst_off, len);
}

#[test]
fn short_copy_when_source_has_fewer_bytes_than_requested() {
    let (_dir, root) = root(106);
    let data = b"only sixteen byt".to_vec(); // 17 bytes
    // Ask for far more than exists past src_off=2.
    assert_copy_round_trips(&root, &data, 2, 0, 10_000);
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(24))]

    /// Byte-identical results across a spread of sizes and offsets,
    /// including the BUF_LEN boundary itself.
    #[test]
    fn copy_range_is_byte_identical(
        data_len in 0usize..(BUF_LEN * 2 + 5000),
        src_off_frac in 0.0f64..1.0,
        dst_off in 0u64..4096,
        len_extra in 0u64..8192,
    ) {
        let (_dir, root) = root(200);
        let data: Vec<u8> = (0..data_len).map(|i| (i % 256) as u8).collect();
        let src_off = if data_len == 0 { 0 } else { ((data_len as f64) * src_off_frac) as u64 };
        let src_off = src_off.min(data_len as u64);
        let remaining = data_len as u64 - src_off;
        let len = remaining + len_extra; // deliberately sometimes overshoots EOF
        assert_copy_round_trips(&root, &data, src_off, dst_off, len);
    }
}
