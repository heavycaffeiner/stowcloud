//! `(dev, ino)` identity of distinct filesystem objects must actually be
//! distinct.
//!
//! Before this, the portable (Windows dev-convenience) backend hardcoded
//! `dev_ino` to `(0, 0)` for every path. `sc-meta`'s node identity is
//! `(share, dev, ino, btime_ns)` — with `dev`/`ino` pinned to a constant,
//! `btime_ns` was the *only* thing distinguishing two nodes in the same
//! share, and two different directories can share one (same creation tick,
//! or a filesystem that just doesn't report one). In practice this meant a
//! share's root and one of its own subdirectories could resolve to the same
//! `oc:fileid` over WebDAV — which is not a cosmetic wire defect: a
//! sync client keys its whole local sync journal on that id, so two
//! resources sharing one reads to the client as "these are the same file".

use sc_vfs::{ShareId, ShareRoot, SharePolicy};

fn path(s: &str) -> sc_vfs::SafePath {
    sc_vfs::SafePath::parse(s, 64).unwrap()
}

/// The exact shape of the observed bug: a share's root directory and a real
/// subdirectory inside it must never report the same `(dev, ino)` — that
/// pair is exactly what a caller (`sc-meta`'s `node_ident`) uses to decide
/// "is this the same node I've seen before".
#[test]
fn a_share_root_and_its_own_subdirectory_have_distinct_identity() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();

    root.mkdir(&path("Reports")).unwrap();

    let root_stat = root.stat(&sc_vfs::SafePath::root()).unwrap();
    let child_stat = root.stat(&path("Reports")).unwrap();

    assert_ne!(
        (root_stat.dev, root_stat.ino),
        (child_stat.dev, child_stat.ino),
        "a share root and a real subdirectory inside it must not collide onto one identity"
    );
    // Neither the root nor a freshly created subdirectory should silently
    // degrade to the "identity unavailable" placeholder in an ordinary,
    // uncontended local-disk test run.
    assert_ne!((root_stat.dev, root_stat.ino), (0, 0), "root identity fell back to the placeholder");
    assert_ne!((child_stat.dev, child_stat.ino), (0, 0), "child identity fell back to the placeholder");
}

/// Two sibling directories, and a file beside them, must all be pairwise
/// distinct too — the root/child case above is the one that was actually
/// observed, but nothing about the fix is specific to a parent/child pair.
#[test]
fn sibling_directories_and_a_file_all_have_distinct_identity() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();

    root.mkdir(&path("a")).unwrap();
    root.mkdir(&path("b")).unwrap();
    let fh = root.create_excl(&path("c.txt"), 0o644).unwrap();
    drop(fh);

    let a = root.stat(&path("a")).unwrap();
    let b = root.stat(&path("b")).unwrap();
    let c = root.stat(&path("c.txt")).unwrap();

    let ids = [(a.dev, a.ino), (b.dev, b.ino), (c.dev, c.ino)];
    for i in 0..ids.len() {
        for j in (i + 1)..ids.len() {
            assert_ne!(ids[i], ids[j], "entries {i} and {j} share an identity: {ids:?}");
        }
    }
}

/// Stat'ing the same file twice, through two different calls, must report
/// the *same* identity both times — uniqueness alone isn't the contract,
/// stability is (`NcConfig::instance_id`'s doc comment makes the same point
/// for the instance half of a sync client's identifier: a changed id
/// forces a full resync).
#[test]
fn the_same_file_reports_the_same_identity_on_repeated_stats() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();
    root.mkdir(&path("x")).unwrap();

    let first = root.stat(&path("x")).unwrap();
    let second = root.stat(&path("x")).unwrap();
    assert_eq!((first.dev, first.ino), (second.dev, second.ino));
}

/// Identity read through an open file handle (`file_stat`, used by the
/// `create_excl`/`open_read` callers) must agree with identity read by path
/// (`stat_path`) for the same file — the two code paths use different Win32
/// calls under the hood (`GetFileInformationByHandle` on an already-open
/// handle vs. opening a fresh metadata-only one) and must not disagree.
#[test]
fn handle_identity_and_path_identity_agree() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();
    let fh = root.create_excl(&path("f.txt"), 0o644).unwrap();
    let via_handle = fh.stat().unwrap();
    drop(fh);
    let via_path = root.stat(&path("f.txt")).unwrap();
    assert_eq!((via_handle.dev, via_handle.ino), (via_path.dev, via_path.ino));
}
