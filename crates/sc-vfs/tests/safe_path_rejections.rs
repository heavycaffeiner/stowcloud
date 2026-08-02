//! Every rejection rule from, exercised individually.
//! Parsing is reject-first: anything on this list must come back `Err`,
//! never be silently normalized.

use sc_vfs::{SafePath, VfsError};

const DEPTH: u16 = 64;

fn assert_invalid_name(input: &str) {
    match SafePath::parse(input, DEPTH) {
        Err(VfsError::InvalidName(_)) => {}
        other => panic!("expected InvalidName for {input:?}, got {other:?}"),
    }
}

#[test]
fn accepts_ordinary_relative_paths() {
    assert!(SafePath::parse("a", DEPTH).is_ok());
    assert!(SafePath::parse("a/b/c", DEPTH).is_ok());
    assert!(SafePath::parse("", DEPTH).is_ok(), "empty string is the root");
}

#[test]
fn rejects_absolute_paths() {
    assert_invalid_name("/etc/passwd");
    assert_invalid_name("/");
}

#[test]
fn rejects_dot_and_dotdot_components() {
    assert_invalid_name(".");
    assert_invalid_name("..");
    assert_invalid_name("a/..");
    assert_invalid_name("a/../b");
    assert_invalid_name("../../../etc/passwd");
    assert_invalid_name("a/.");
    assert_invalid_name("a/./b");
}

#[test]
fn rejects_empty_components() {
    assert_invalid_name("a//b");
    assert_invalid_name("//");
    assert_invalid_name("a/");
    // A leading empty component would also look like an absolute path, but
    // make sure the empty-component check catches it independent of that.
    assert_invalid_name("a//");
}

#[test]
fn rejects_nul_and_control_characters() {
    assert_invalid_name("a\0b");
    assert_invalid_name("a\u{01}b");
    assert_invalid_name("a\u{1f}b");
    assert_invalid_name("a\u{7f}b");
    assert_invalid_name("\nb");
    assert_invalid_name("\tb");
}

#[test]
fn join_rejects_a_component_containing_a_slash() {
    // `parse` can never produce a single component containing '/' (it's the
    // separator), so this rule is only reachable through `join`, which
    // takes one component directly.
    let root = SafePath::root();
    assert!(root.join("a/b", DEPTH).is_err());
}

#[test]
fn rejects_components_over_255_bytes() {
    let long = "a".repeat(256);
    assert_invalid_name(&long);
    let ok = "a".repeat(255);
    assert!(SafePath::parse(&ok, DEPTH).is_ok());
}

#[test]
fn rejects_paths_over_4096_bytes() {
    // Individual components must stay <= 255 bytes, so build the overlong
    // total out of many small, individually-valid components.
    let component = "a".repeat(200);
    let mut s = component.clone();
    while s.len() <= 4096 {
        s.push('/');
        s.push_str(&component);
    }
    assert!(s.len() > 4096);
    let r = SafePath::parse(&s, DEPTH);
    assert!(r.is_err(), "expected overlong path to be rejected");
}

#[test]
fn rejects_depth_over_max_depth() {
    let comps: Vec<String> = (0..5).map(|i| format!("d{i}")).collect();
    let path = comps.join("/");
    assert!(SafePath::parse(&path, 5).is_ok());
    assert!(matches!(
        SafePath::parse(&path, 4),
        Err(VfsError::TooDeep)
    ));
}

#[test]
fn rejects_colon_ntfs_ads_separator() {
    assert_invalid_name("file.txt:stream");
    assert_invalid_name("a/b:c");
}

#[test]
fn rejects_trailing_dot_or_space() {
    assert_invalid_name("name.");
    assert_invalid_name("name ");
    assert_invalid_name("a/b.");
    assert_invalid_name("a/b ");
    // Leading/interior dots and spaces are fine.
    assert!(SafePath::parse(".hidden", DEPTH).is_ok());
    assert!(SafePath::parse("a b", DEPTH).is_ok());
    assert!(SafePath::parse("a.b", DEPTH).is_ok());
}

#[test]
fn rejects_windows_reserved_device_names() {
    for base in [
        "CON", "PRN", "AUX", "NUL", "COM1", "COM9", "LPT1", "LPT9",
    ] {
        assert_invalid_name(base);
        assert_invalid_name(&base.to_lowercase());
        assert_invalid_name(&format!("{base}.txt"));
        assert_invalid_name(&format!("{}.txt", base.to_lowercase()));
    }
    // Not actually reserved: only exact device-name stems are blocked.
    assert!(SafePath::parse("CONTACT.txt", DEPTH).is_ok());
    assert!(SafePath::parse("CONSOLE", DEPTH).is_ok());
}

#[test]
fn rejects_reserved_control_prefixes() {
    assert_invalid_name(".sctrash");
    assert_invalid_name(".sctrash/whatever");
    assert_invalid_name(".scpart-abc123");
    assert_invalid_name(".scmeta");
    assert_invalid_name(".scindex");
    for p in sc_vfs::RESERVED_PREFIXES {
        assert!(sc_vfs::is_reserved_name(p));
    }
}

#[test]
fn parse_never_panics_on_arbitrary_bytes() {
    // A cheap stand-in for the cargo-fuzz target described in
    //: throw a grab-bag of nasty inputs at `parse` and
    // make sure none of them panic, and that anything which *is* accepted
    // contains no '..' or absolute-path component.
    let long = "a".repeat(10_000);
    let nasty = [
        "", "/", "..", "a/../../b", "a\0", "a\u{7f}", ":", "a:b", "CON",
        long.as_str(),
        "///", "a/./b/../c",
    ];
    for input in nasty {
        if let Ok(p) = SafePath::parse(input, DEPTH) {
            for c in p.components() {
                assert_ne!(c.as_str(), "..");
                assert_ne!(c.as_str(), ".");
                assert!(!c.as_str().starts_with('/'));
            }
        }
    }
}
