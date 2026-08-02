//! Reserved-name rejection: `.sctrash`, `.scpart-*`, `.scmeta`, `.scindex`
//! are our own control files. Nothing above `sc-vfs` should ever be able to
//! address them through a user-supplied path — see and
//! the single shared `RESERVED_PREFIXES` list it calls out (tree walker,
//! listing, and SMB `veto files` must all agree).

use sc_vfs::{is_reserved_name, SafePath, ShareId, SharePolicy, ShareRoot, RESERVED_PREFIXES};

const DEPTH: u16 = 64;

#[test]
fn reserved_prefixes_are_exactly_the_documented_set() {
    assert_eq!(
        RESERVED_PREFIXES,
        &[".sctrash", ".scpart-", ".scmeta", ".scindex"]
    );
}

#[test]
fn is_reserved_name_matches_prefixes_not_just_exact_names() {
    assert!(is_reserved_name(".sctrash"));
    assert!(is_reserved_name(".sctrash-anything"));
    assert!(is_reserved_name(".scpart-abc123"));
    assert!(is_reserved_name(".scmeta"));
    assert!(is_reserved_name(".scmeta.json"));
    assert!(is_reserved_name(".scindex"));
    assert!(!is_reserved_name("normal.txt"));
    assert!(!is_reserved_name("sctrash")); // no leading dot: not reserved
}

#[test]
fn safe_path_parse_rejects_every_reserved_prefix() {
    for &prefix in RESERVED_PREFIXES {
        assert!(
            SafePath::parse(prefix, DEPTH).is_err(),
            "expected {prefix:?} to be rejected"
        );
        assert!(
            SafePath::parse(&format!("a/b/{prefix}"), DEPTH).is_err(),
            "expected nested {prefix:?} to be rejected"
        );
    }
}

#[test]
fn share_root_cannot_be_made_to_create_a_reserved_name() {
    let dir = tempfile::tempdir().unwrap();
    let root = ShareRoot::open(ShareId::new(1), dir.path(), SharePolicy::default()).unwrap();

    // There's no way to *get* a SafePath naming a reserved prefix in the
    // first place, so the only thing left to check is that `join` (the
    // other constructor) enforces the same rule.
    let root_path = SafePath::root();
    assert!(root_path.join(".scmeta", DEPTH).is_err());
    assert!(root_path.join(".scpart-xyz", DEPTH).is_err());

    // And that a name merely *containing* the reserved substring elsewhere
    // (not as a prefix) is fine — the rule is prefix-based, not "contains".
    let ok = SafePath::parse("my.scmeta.notes.txt", DEPTH).unwrap();
    root.create_excl(&ok, 0o644).unwrap();
    assert!(root.stat(&ok).is_ok());
}
