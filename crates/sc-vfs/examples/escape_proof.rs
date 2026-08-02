//! Linux-only proof that the kernel, not our string checks, is what confines
//! a share. Run inside the Rocky VM: `cargo run -p sc-vfs --example escape_proof`
//!
//! The body is `cfg(unix)`-gated rather than left to fail: this file is an
//! *example*, so `cargo test` compiles it on every platform, and a Windows dev
//! box would otherwise fail the whole test gate on a target this proof was
//! never meant to run on.
#[cfg(unix)]
use sc_vfs::{SafePath, ShareId, ShareRoot, SharePolicy, SymlinkPolicy, VfsError};

#[cfg(unix)]
fn p(s: &str) -> SafePath { SafePath::parse(s, 64).unwrap() }

#[cfg(not(unix))]
fn main() {
    eprintln!("escape_proof only means anything on Linux: it proves that openat2/O_NOFOLLOW, not our string checks, are what confine a share. Run it in the Rocky VM.");
}

#[cfg(unix)]
fn main() {
    let dir = tempfile::tempdir().unwrap();
    // A symlink inside the share pointing at a real file outside it.
    std::os::unix::fs::symlink("/etc/passwd", dir.path().join("escape")).unwrap();
    std::os::unix::fs::symlink("/etc", dir.path().join("etcdir")).unwrap();
    std::fs::write(dir.path().join("ok.txt"), b"inside").unwrap();

    let mut checks = 0usize;
    let mut passed = 0usize;

    for (label, policy) in [
        ("Deny", SharePolicy { symlink: SymlinkPolicy::Deny, ..Default::default() }),
        ("WithinShare", SharePolicy { symlink: SymlinkPolicy::WithinShare, ..Default::default() }),
    ] {
        let root = ShareRoot::open(ShareId::new(1), dir.path(), policy).unwrap();

        // 1. reading the file we legitimately own still works
        checks += 1;
        match root.open_read(&p("ok.txt")) {
            Ok(_) => { passed += 1; println!("[{label}] in-share read              OK"); }
            Err(e) => println!("[{label}] in-share read              BROKEN: {e:?}"),
        }

        // 2. following a symlink to /etc/passwd must NOT yield that file
        checks += 1;
        match root.open_read(&p("escape")) {
            Ok(h) => {
                let mut buf = [0u8; 16];
                let n = h.read_at(&mut buf, 0).unwrap_or(0);
                println!("[{label}] symlink -> /etc/passwd    *** ESCAPED *** read {n} bytes");
            }
            Err(e) => { passed += 1; println!("[{label}] symlink -> /etc/passwd    blocked ({e:?})"); }
        }

        // 3. traversing through a symlinked directory must not escape either
        checks += 1;
        match root.stat(&p("etcdir/passwd")) {
            Ok(_) => println!("[{label}] via symlinked dir         *** ESCAPED ***"),
            Err(e) => { passed += 1; println!("[{label}] via symlinked dir         blocked ({e:?})"); }
        }

        // 4. a literal .. must never be constructible in the first place
        checks += 1;
        match SafePath::parse("../etc/passwd", 64) {
            Err(VfsError::InvalidName(_)) => { passed += 1; println!("[{label}] '../etc/passwd'           rejected at parse"); }
            other => println!("[{label}] '../etc/passwd'           *** ACCEPTED *** {other:?}"),
        }
    }

    println!("\n{passed}/{checks} containment checks passed");
    std::process::exit(if passed == checks { 0 } else { 1 });
}
