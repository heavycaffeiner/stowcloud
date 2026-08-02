//! Escape attempts: `..`, absolute paths, and a symlink planted so it points
//! outside the share root. All three must be refused — see `ARCHITECTURE.md`
//! §0.2 ("a path is a kernel handle, not a string") and the VFS-escape row in
//! `DESIGN-CORE.md` §7's test-strategy table.

use sc_vfs::{SafePath, ShareId, SharePolicy, ShareRoot, VfsError};

const DEPTH: u16 = 64;

#[test]
fn dotdot_is_rejected_at_parse_time_not_resolved() {
    // There is no way to get a `..` or absolute path *into* a `SafePath` at
    // all — `parse`/`join` are the only constructors, and both validate.
    // That's the point: the escape is impossible to express, not merely
    // checked-for later.
    assert!(SafePath::parse("../../../etc/passwd", DEPTH).is_err());
    assert!(SafePath::parse("a/../../b", DEPTH).is_err());
    assert!(SafePath::parse("/etc/passwd", DEPTH).is_err());

    let root = SafePath::root();
    assert!(root.join("..", DEPTH).is_err());
    assert!(root.join(".", DEPTH).is_err());
}

#[test]
fn symlink_escape_is_denied_under_the_default_policy() {
    let outside = tempfile::tempdir().unwrap();
    std::fs::write(outside.path().join("secret.txt"), b"top secret").unwrap();

    let share_dir = tempfile::tempdir().unwrap();
    let link_path = share_dir.path().join("escape_link");

    #[cfg(windows)]
    let create_result = std::os::windows::fs::symlink_dir(outside.path(), &link_path);
    #[cfg(unix)]
    let create_result = std::os::unix::fs::symlink(outside.path(), &link_path);

    if let Err(e) = create_result {
        // Creating symlinks requires Developer Mode or admin/elevated
        // privileges on Windows, and can be restricted on some Unix setups
        // too. Skip rather than fail when the *test harness* itself lacks
        // the privilege — that's not what this test is checking.
        eprintln!("skipping symlink escape test: cannot create a symlink here ({e})");
        return;
    }

    // `SharePolicy::default()` is `SymlinkPolicy::Deny`.
    let root = ShareRoot::open(ShareId::new(1), share_dir.path(), SharePolicy::default()).unwrap();

    let via_symlink = SafePath::parse("escape_link/secret.txt", DEPTH).unwrap();
    assert!(
        matches!(
            root.stat(&via_symlink),
            Err(VfsError::SymlinkDenied) | Err(VfsError::NotFound)
        ),
        "traversing a symlink to outside the share root must be denied"
    );

    let fh = root.open_read(&via_symlink);
    assert!(fh.is_err(), "opening through a denied symlink must fail");

    // The symlink entry itself is still visible in a listing (design intent:
    // hiding it would just confuse users — "files disappearing").
    let entries = root.read_dir(&SafePath::root()).unwrap();
    assert!(entries.iter().any(|e| e.name.as_str() == "escape_link"));
}
