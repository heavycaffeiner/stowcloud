//! `DESIGN-CORE.md` §2, Unicode section: lookup tries the given spelling
//! first, then NFC, then NFD — but never rewrites an existing on-disk name.
//! Creation always normalizes to NFC.

use sc_vfs::{SafePath, ShareId, SharePolicy, ShareRoot};

const DEPTH: u16 = 64;

// "café" — 4th letter as one precomposed codepoint (NFC form).
fn nfc_name() -> String {
    "caf\u{00e9}.txt".to_string()
}

// "café" — 4th letter decomposed into 'e' + COMBINING ACUTE ACCENT (the form
// macOS/HFS+ SMB clients tend to write).
fn nfd_name() -> String {
    "cafe\u{0301}.txt".to_string()
}

#[test]
fn nfd_on_disk_is_found_via_nfc_path() {
    let dir = tempfile::tempdir().unwrap();
    // Write the NFD spelling directly, bypassing sc-vfs, to simulate a file
    // an external (non-conforming) client already created on disk.
    std::fs::write(dir.path().join(nfd_name()), b"hello").unwrap();

    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();
    let p = SafePath::parse(&nfc_name(), DEPTH).unwrap();

    let st = root
        .stat(&p)
        .expect("NFD on-disk name should be found via an NFC-spelled path");
    assert_eq!(st.size, 5);

    // The listing must show the *original* on-disk bytes, untouched.
    let entries = root.read_dir(&SafePath::root()).unwrap();
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0].name.as_str(), nfd_name());
}

#[test]
fn nfc_on_disk_is_found_via_nfd_path() {
    let dir = tempfile::tempdir().unwrap();
    std::fs::write(dir.path().join(nfc_name()), b"world").unwrap();

    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();
    let p = SafePath::parse(&nfd_name(), DEPTH).unwrap();

    let st = root
        .stat(&p)
        .expect("NFC on-disk name should be found via an NFD-spelled path");
    assert_eq!(st.size, 5);
}

#[test]
fn creation_always_normalizes_the_new_name_to_nfc() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();

    // Create using the NFD spelling as input...
    let p = SafePath::parse(&nfd_name(), DEPTH).unwrap();
    root.create_excl(&p, 0o644).unwrap();

    // ...the on-disk name must be the NFC form, never the NFD bytes we gave.
    let entries = root.read_dir(&SafePath::root()).unwrap();
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0].name.as_str(), nfc_name());
}
