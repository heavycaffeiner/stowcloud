//! Ad-hoc timing comparison for the M-this-task copy work: the new
//! `FileHandle::copy_range_from` (kernel-side `copy_file_range`, reflink on
//! XFS/btrfs) against the old 256 KiB buffered read/write loop it replaced
//! in `sc-upload`/`sc-core`.
//!
//! Not a test, not wired into `scripts/verify.sh` — just prints timings.
//! Meant to be run once, by hand, against a real share directory on the
//! deployment filesystem:
//!
//!   cargo run -p sc-vfs --release --example copy_bench -- /srv/shares/photos 2048
//!
//! (share dir, file size in MiB — defaults to 2048 MiB if omitted).
use std::time::Instant;

use sc_vfs::{SafePath, ShareId, ShareRoot, SharePolicy};

fn depth() -> u16 {
    64
}

fn write_test_file(root: &ShareRoot, p: &SafePath, total: u64) {
    let fh = root.create_excl(p, 0o644).unwrap();
    let buf = vec![0x5Au8; 4 * 1024 * 1024];
    let mut off = 0u64;
    while off < total {
        let n = ((total - off).min(buf.len() as u64)) as usize;
        let mut w = 0usize;
        while w < n {
            let m = fh.write_at(&buf[w..n], off + w as u64).unwrap();
            w += m;
        }
        off += n as u64;
    }
    fh.sync_data().unwrap();
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let dir = args.get(1).cloned().unwrap_or_else(|| {
        eprintln!("usage: copy_bench <share_dir> [size_mib=2048]");
        std::process::exit(2);
    });
    let size_mib: u64 = args.get(2).map(|s| s.parse().unwrap()).unwrap_or(2048);
    let total = size_mib * 1024 * 1024;

    let root = ShareRoot::open(ShareId::new(1), std::path::Path::new(&dir), SharePolicy::default()).unwrap();
    println!("share: {dir} (fstype={:?}), size: {size_mib} MiB", root.fstype());

    let src_path = SafePath::parse("bench_src.bin", depth()).unwrap();
    let dst_new = SafePath::parse("bench_dst_new.bin", depth()).unwrap();
    let dst_old = SafePath::parse("bench_dst_old.bin", depth()).unwrap();
    // Clean up anything left over from a previous run.
    let _ = root.unlink(&src_path);
    let _ = root.unlink(&dst_new);
    let _ = root.unlink(&dst_old);

    print!("writing source file... ");
    use std::io::Write;
    std::io::stdout().flush().unwrap();
    write_test_file(&root, &src_path, total);
    println!("done");

    let src_fh = root.open_read(&src_path).unwrap();

    // ---- new path: FileHandle::copy_range_from ----
    let t0 = Instant::now();
    {
        let dfh = root.create_excl(&dst_new, 0o644).unwrap();
        let mut copied = 0u64;
        while copied < total {
            let n = dfh.copy_range_from(&src_fh, copied, copied, total - copied).unwrap();
            if n == 0 {
                break;
            }
            copied += n;
        }
        assert_eq!(copied, total);
        dfh.sync_data().unwrap();
    }
    let new_elapsed = t0.elapsed();

    // ---- old path: the 256 KiB buffered read/write loop it replaced ----
    let t1 = Instant::now();
    {
        let dfh = root.create_excl(&dst_old, 0o644).unwrap();
        let mut buf = vec![0u8; 256 * 1024];
        let mut off = 0u64;
        loop {
            let n = src_fh.read_at(&mut buf, off).unwrap();
            if n == 0 {
                break;
            }
            let mut w = 0usize;
            while w < n {
                let m = dfh.write_at(&buf[w..n], off + w as u64).unwrap();
                w += m;
            }
            off += n as u64;
        }
        assert_eq!(off, total);
        dfh.sync_data().unwrap();
    }
    let old_elapsed = t1.elapsed();

    println!("copy_range_from (new, kernel-side): {new_elapsed:?}");
    println!("buffered read/write loop (old):     {old_elapsed:?}");
    if new_elapsed.as_secs_f64() > 0.0 {
        println!("speedup: {:.1}x", old_elapsed.as_secs_f64() / new_elapsed.as_secs_f64());
    }

    root.unlink(&src_path).unwrap();
    root.unlink(&dst_new).unwrap();
    root.unlink(&dst_old).unwrap();
}
