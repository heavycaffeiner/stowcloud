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

/// The creation table, which is stricter than the traversal one. `join` is
/// what every new name goes through; `parse` only ever names something that
/// already exists.
fn assert_uncreatable(name: &str) {
    match SafePath::root().join(name, DEPTH) {
        Err(VfsError::InvalidName(_)) => {}
        other => panic!("expected join to reject {name:?}, got {other:?}"),
    }
}

/// ...and the same name, once it exists on disk, has to be reachable.
fn assert_traversable(path: &str) {
    assert!(
        SafePath::parse(path, DEPTH).is_ok(),
        "an existing path must stay reachable: {path:?}"
    );
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
fn rejects_nul_everywhere_but_only_refuses_to_create_other_control_bytes() {
    // NUL truncates the C string the kernel is eventually handed, so it is
    // rejected on both paths.
    assert_invalid_name("a\0b");
    assert_uncreatable("a\0b");

    // The rest are legal on Linux. Refusing to *create* them is portability;
    // refusing to *open* them would strand whatever another tool already wrote.
    for n in ["a\u{01}b", "a\u{1f}b", "a\u{7f}b", "\nb", "\tb"] {
        assert_uncreatable(n);
        assert_traversable(n);
    }
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
fn refuses_to_create_a_colon_but_still_opens_one() {
    assert_uncreatable("file.txt:stream");
    assert_traversable("file.txt:stream");
    assert_traversable("a/b:c");
}

#[test]
fn refuses_to_create_a_trailing_dot_or_space_but_still_opens_one() {
    // The reported bug: a folder shown as `Mods` is `Mods ` on disk, it lists
    // like anything else, and opening it answered `invalid name`.
    for n in ["name.", "name ", "Mods "] {
        assert_uncreatable(n);
        assert_traversable(n);
    }
    assert_traversable("a/b.");
    assert_traversable("a/b ");
    // Leading/interior dots and spaces were always fine, on both paths.
    for n in [".hidden", "a b", "a.b"] {
        assert!(SafePath::root().join(n, DEPTH).is_ok(), "{n}");
        assert_traversable(n);
    }
}

#[test]
fn refuses_to_create_windows_device_names_but_still_opens_them() {
    for base in [
        "CON", "PRN", "AUX", "NUL", "COM1", "COM9", "LPT1", "LPT9",
    ] {
        for n in [
            base.to_string(),
            base.to_lowercase(),
            format!("{base}.txt"),
            format!("{}.txt", base.to_lowercase()),
        ] {
            assert_uncreatable(&n);
            assert_traversable(&n);
        }
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
    // A cheap stand-in for a cargo-fuzz target: throw a grab-bag of nasty
    // inputs at `parse` and
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
