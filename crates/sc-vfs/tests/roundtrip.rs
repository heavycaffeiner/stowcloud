//! Create / read / write / rename / unlink round-trip against a real
//! `tempfile::TempDir`, exercising the full `ShareRoot` surface.

use sc_vfs::{Kind, SafePath, ShareId, SharePolicy, VfsError};
use sc_vfs::ShareRoot;

const DEPTH: u16 = 64;

fn path(s: &str) -> SafePath {
    SafePath::parse(s, DEPTH).unwrap()
}

#[test]
fn create_write_read_rename_unlink() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();

    root.mkdir(&path("a")).unwrap();
    root.mkdir(&path("a/b")).unwrap();

    let file_path = path("a/b/hello.txt");
    let fh = root.create_excl(&file_path, 0o644).unwrap();
    let written = fh.write_at(b"hello, world", 0).unwrap();
    assert_eq!(written, 12);
    fh.sync_data().unwrap();
    drop(fh);

    let fh2 = root.open_read(&file_path).unwrap();
    let mut buf = [0u8; 12];
    let n = fh2.read_at(&mut buf, 0).unwrap();
    assert_eq!(n, 12);
    assert_eq!(&buf, b"hello, world");
    let st = fh2.stat().unwrap();
    assert_eq!(st.size, 12);
    assert_eq!(st.kind, Kind::File);
    drop(fh2);

    // Same thing via ShareRoot::stat.
    let st2 = root.stat(&file_path).unwrap();
    assert_eq!(st2.size, 12);

    // mtime round-trip.
    let target_ns: i128 = 1_700_000_000 * 1_000_000_000;
    root.set_times(&file_path, target_ns).unwrap();
    let st3 = root.stat(&file_path).unwrap();
    assert_eq!(st3.mtime_ns / 1_000_000_000, 1_700_000_000);

    // Listing sees exactly the one file.
    let entries = root.read_dir(&path("a/b")).unwrap();
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0].name.as_str(), "hello.txt");
    assert_eq!(entries[0].kind, Kind::File);

    // Rename.
    let renamed_path = path("a/b/renamed.txt");
    root.rename(&file_path, &renamed_path, true).unwrap();
    assert!(matches!(root.stat(&file_path), Err(VfsError::NotFound)));
    let st4 = root.stat(&renamed_path).unwrap();
    assert_eq!(st4.size, 12);

    // Extending the file via set_len, then re-reading.
    let fh3 = root.open_read(&renamed_path).unwrap();
    fh3.set_len(20).unwrap();
    assert_eq!(fh3.stat().unwrap().size, 20);
    drop(fh3);

    // Unlink then rmdir, innermost first.
    root.unlink(&renamed_path).unwrap();
    assert!(matches!(root.stat(&renamed_path), Err(VfsError::NotFound)));
    root.rmdir(&path("a/b")).unwrap();
    root.rmdir(&path("a")).unwrap();
    assert!(matches!(root.stat(&path("a")), Err(VfsError::NotFound)));
}

#[test]
fn create_excl_refuses_to_clobber_an_existing_file() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(2), dir.path(), SharePolicy::default()).unwrap();

    let p = path("only-once.txt");
    root.create_excl(&p, 0o644).unwrap();
    let err = match root.create_excl(&p, 0o644) {
        Ok(_) => panic!("second create_excl should have failed"),
        Err(e) => e,
    };
    assert!(matches!(err, VfsError::AlreadyExists));
}

#[test]
fn rename_no_replace_refuses_to_clobber_destination() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(3), dir.path(), SharePolicy::default()).unwrap();

    let a = path("a.txt");
    let b = path("b.txt");
    root.create_excl(&a, 0o644).unwrap();
    root.create_excl(&b, 0o644).unwrap();

    let err = root.rename(&a, &b, true).unwrap_err();
    assert!(matches!(err, VfsError::AlreadyExists));
    // Both files must still exist, untouched.
    assert!(root.stat(&a).is_ok());
    assert!(root.stat(&b).is_ok());
}

#[test]
fn space_reports_plausible_numbers() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(4), dir.path(), SharePolicy::default()).unwrap();
    let s = root.space(&SafePath::root()).unwrap();
    assert!(s.total >= s.free, "{s:?}");
    // The root reserve is the whole reason these are two numbers.
    assert!(s.free >= s.available, "{s:?}");
    assert_eq!(s.used(), s.total - s.free);
}

/// The path argument is what makes a nested mount (a RAID array under one of
/// a share's subdirectories) report its own filesystem instead of the share
/// root's. A test cannot mount anything, so it pins the other half of the
/// contract: every path that *is* on the anchor's filesystem — a
/// subdirectory, and a file, whose parent is what gets measured — still
/// answers with the anchor's numbers rather than failing or returning zeros.
#[test]
fn space_answers_for_a_subpath_and_for_a_file() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(12), dir.path(), SharePolicy::default()).unwrap();
    root.mkdir(&path("sub")).unwrap();
    root.create_excl(&path("sub/f.txt"), 0o644).unwrap();

    let at_root = root.space(&SafePath::root()).unwrap();
    assert_eq!(root.space(&path("sub")).unwrap().total, at_root.total);
    assert_eq!(root.space(&path("sub/f.txt")).unwrap().total, at_root.total);
}

#[test]
fn space_rejects_a_path_that_does_not_exist() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(13), dir.path(), SharePolicy::default()).unwrap();
    // Not `NotFound`-tolerant by accident: the file fallback retries at the
    // parent, and here the parent is missing too.
    assert!(matches!(
        root.space(&path("nope/deeper")),
        Err(VfsError::NotFound)
    ));
}

#[test]
fn rmdir_refuses_a_nonempty_directory() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(5), dir.path(), SharePolicy::default()).unwrap();

    root.mkdir(&path("d")).unwrap();
    root.create_excl(&path("d/f.txt"), 0o644).unwrap();

    let err = root.rmdir(&path("d")).unwrap_err();
    assert!(matches!(err, VfsError::NotEmpty));
}

/// Directory rename must work, and must move the whole subtree with it.
///
/// This is deliberately explicit because an earlier draft of the portable
/// backend approximated `RENAME_NOREPLACE` with `hard_link` + `unlink`, which
/// is not legal for directories — so directory rename silently went untested
/// while the rest of the suite stayed green. It also underpins the headline
/// O(1) directory rename in `sc-meta` (`rename_node` updates one row, which
/// is only correct if the filesystem side is a real rename rather than a
/// copy).
#[test]
fn rename_moves_a_directory_and_its_subtree() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(6), dir.path(), SharePolicy::default()).unwrap();

    root.mkdir(&path("old")).unwrap();
    root.mkdir(&path("old/inner")).unwrap();
    root.create_excl(&path("old/inner/deep.txt"), 0o644).unwrap();

    root.rename(&path("old"), &path("new"), true).unwrap();

    assert!(matches!(root.stat(&path("old")), Err(VfsError::NotFound)));
    assert!(root.stat(&path("new")).is_ok());
    // The descendant came along; nothing was copied and re-rooted.
    assert!(root.stat(&path("new/inner/deep.txt")).is_ok());
}

/// Renaming a directory onto an existing name must be refused when
/// `no_replace` is set, and must leave both trees intact.
#[test]
fn rename_directory_no_replace_refuses_and_preserves_both() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(7), dir.path(), SharePolicy::default()).unwrap();

    root.mkdir(&path("a")).unwrap();
    root.create_excl(&path("a/keep.txt"), 0o644).unwrap();
    root.mkdir(&path("b")).unwrap();
    root.create_excl(&path("b/also.txt"), 0o644).unwrap();

    let err = root.rename(&path("a"), &path("b"), true).unwrap_err();
    assert!(matches!(err, VfsError::AlreadyExists));

    assert!(root.stat(&path("a/keep.txt")).is_ok());
    assert!(root.stat(&path("b/also.txt")).is_ok());
}
