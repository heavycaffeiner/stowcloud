//! Golden test for `oc_permissions` over the entire `Perms` space.
//!
//! # Why this test is worth its weight
//!
//! A wrong `oc:permissions` string does not produce an error. The desktop
//! client reads it, decides the item is read-only / unrenameable / not a valid
//! upload target, and **silently declines to sync it**. There is no log line on
//! either side. calls this the hardest failure mode
//! in the compatibility surface to debug, which is why the mapping is pinned
//! here rather than left to the implementation.
//!
//! The expectation is computed by an *independently written* reference
//! implementation below, transcribed from
//! `lib/public/Files/DavUtil.php::getDavPermissions()` rather than from
//! `props::oc_permissions`. If both drift the same way the test is worthless,
//! so the reference is written in the reference's own shape (integer bitmask,
//! PHP letter order) and the two are compared over all 256 x 2 x 2 = 1024
//! combinations.
//!
//! The concrete letters for a handful of load-bearing cases are also asserted
//! literally, so a coordinated change to both implementations still fails.

use sc_compat_nc::ports::Perms;
use sc_compat_nc::props::oc_permissions;

/// `lib/public/Constants.php`.
mod php {
    pub const READ: u32 = 1;
    pub const UPDATE: u32 = 2;
    pub const CREATE: u32 = 4;
    pub const DELETE: u32 = 8;
    pub const SHARE: u32 = 16;
}

/// Our `Perms` -> the reference's integer bitmask, plus the two bits the
/// reference folds together but we keep apart.
struct RefInput {
    bits: u32,
    /// Reference `canRename($info, $parent)`.
    can_rename: bool,
    /// Reference `$isWritable` for a file.
    writable: bool,
    is_dir: bool,
    shared: bool,
    /// Reference `isMounted()`. Always false for us: no external storage.
    mounted: bool,
}

/// Transcription of `DavUtil::getDavPermissions`.
///
/// ```php
/// $p = '';
/// if ($info->isShared())                       { $p .= 'S'; }
/// if ($permissions & Constants::PERMISSION_SHARE)  { $p .= 'R'; }
/// if ($info->isMounted())                      { $p .= 'M'; }
/// if ($permissions & Constants::PERMISSION_READ)   { $p .= 'G'; }
/// if ($permissions & Constants::PERMISSION_DELETE) { $p .= 'D'; }
/// if (self::canRename($info, $parent))         { $p .= 'N'; }
/// if ($permissions & Constants::PERMISSION_UPDATE) { $p .= 'V'; }
/// if ($info->getType() === FileInfo::TYPE_FILE) {
///     if ($isWritable) { $p .= 'W'; }
/// } else {
///     if ($permissions & Constants::PERMISSION_CREATE) { $p .= 'CK'; }
/// }
/// ```
fn php_dav_permissions(i: &RefInput) -> String {
    let mut p = String::new();
    if i.shared {
        p.push('S');
    }
    if i.bits & php::SHARE != 0 {
        p.push('R');
    }
    if i.mounted {
        p.push('M');
    }
    if i.bits & php::READ != 0 {
        p.push('G');
    }
    if i.bits & php::DELETE != 0 {
        p.push('D');
    }
    if i.can_rename {
        p.push('N');
    }
    if i.bits & php::UPDATE != 0 {
        p.push('V');
    }
    if !i.is_dir {
        if i.writable {
            p.push('W');
        }
    } else if i.bits & php::CREATE != 0 {
        p.push_str("CK");
    }
    p
}

/// Project our richer `Perms` onto the reference's inputs.
///
/// This is where our documented divergence lives, and stating it as data keeps
/// it honest:
///
/// * `M` is never emitted — we have no external storage concept, and claiming
///   a mount makes clients apply mount-specific move restrictions.
/// * The reference derives `N` from `canRename()` and `V` from
///   `PERMISSION_UPDATE`; we have distinct `RENAME` and `MOVE` bits and map
///   them directly. Letter *positions* are
///   unchanged, which is what is on the wire.
/// * The reference folds file writability into `PERMISSION_UPDATE`; we have a
///   separate `WRITE` bit.
fn project(p: Perms, is_dir: bool, shared: bool) -> RefInput {
    let mut bits = 0;
    if p.contains(Perms::READ) {
        bits |= php::READ;
    }
    if p.contains(Perms::MOVE) {
        bits |= php::UPDATE;
    }
    if p.contains(Perms::CREATE) {
        bits |= php::CREATE;
    }
    if p.contains(Perms::DELETE) {
        bits |= php::DELETE;
    }
    if p.contains(Perms::SHARE) {
        bits |= php::SHARE;
    }
    RefInput {
        bits,
        can_rename: p.contains(Perms::RENAME),
        writable: p.contains(Perms::WRITE),
        is_dir,
        shared,
        mounted: false,
    }
}

#[test]
fn golden_over_every_perms_combination_times_file_and_dir() {
    let mut checked = 0usize;
    for raw in 0u16..256 {
        let perms = Perms::from_bits_truncate(raw);
        // `from_bits_truncate` must be lossless over 0..256 — all 8 bits are
        // defined. If a bit is ever removed from Perms this assert catches it
        // before the table silently shrinks.
        assert_eq!(perms.bits(), raw, "Perms lost bit pattern {raw:#010b}");

        for is_dir in [false, true] {
            for shared in [false, true] {
                let got = oc_permissions(perms, is_dir, shared);
                let want = php_dav_permissions(&project(perms, is_dir, shared));
                assert_eq!(
                    got, want,
                    "perms={raw:#010b} ({perms:?}) is_dir={is_dir} shared={shared}"
                );

                // Structural invariants that hold for every single cell.
                assert!(
                    !got.contains('M'),
                    "M (mounted) must never be emitted: {got}"
                );
                assert_eq!(
                    got.contains('C'),
                    got.contains('K'),
                    "C and K are emitted as a pair or not at all: {got}"
                );
                if is_dir {
                    assert!(!got.contains('W'), "W is file-only: {got}");
                } else {
                    assert!(!got.contains('C'), "C/K are directory-only: {got}");
                }
                assert!(
                    is_sorted_by_reference_order(&got),
                    "letters out of order: {got}"
                );
                checked += 1;
            }
        }
    }
    assert_eq!(checked, 256 * 2 * 2);
}

/// The order is fixed by the reference and is not alphabetical.
fn is_sorted_by_reference_order(s: &str) -> bool {
    const ORDER: &str = "SRMGDNVWCK";
    // 'W' and 'CK' are mutually exclusive, so a single ascending pass over this
    // sequence is a valid check.
    let mut last = 0usize;
    for c in s.chars() {
        let Some(i) = ORDER.find(c) else { return false };
        if i < last {
            return false;
        }
        last = i;
    }
    true
}

/// The specific strings that matter operationally, asserted literally so that
/// changing both implementations in lockstep still fails.
#[test]
fn load_bearing_cases_are_literal() {
    let all = Perms::all();
    // A fully-permitted file and directory: the maximal strings.
    assert_eq!(oc_permissions(all, false, false), "RGDNVW");
    assert_eq!(oc_permissions(all, true, false), "RGDNVCK");
    assert_eq!(oc_permissions(all, false, true), "SRGDNVW");
    assert_eq!(oc_permissions(all, true, true), "SRGDNVCK");

    // A read-only share root. It MUST still carry G — an empty string makes
    // the client ignore the entry entirely, which looks like the share does
    // not exist.
    let ro = Perms::READ | Perms::DOWNLOAD;
    assert_eq!(oc_permissions(ro, true, false), "G");
    assert_eq!(oc_permissions(ro, false, false), "G");
    assert_eq!(oc_permissions(ro, true, true), "SG");

    // No permissions at all -> empty string. This is the one case where the
    // client is *supposed* to ignore the entry.
    assert_eq!(oc_permissions(Perms::empty(), false, false), "");
    assert_eq!(oc_permissions(Perms::empty(), true, false), "");

    // Writable file without rename/move: the client can upload edits but will
    // implement a rename as delete + re-upload.
    assert_eq!(
        oc_permissions(Perms::READ | Perms::WRITE, false, false),
        "GW"
    );

    // Rename and move are independent letters.
    assert_eq!(oc_permissions(Perms::READ | Perms::RENAME, false, false), "GN");
    assert_eq!(oc_permissions(Perms::READ | Perms::MOVE, false, false), "GV");
    assert_eq!(
        oc_permissions(Perms::READ | Perms::RENAME | Perms::MOVE, false, false),
        "GNV"
    );

    // A directory you may create in.
    assert_eq!(
        oc_permissions(Perms::READ | Perms::CREATE, true, false),
        "GCK"
    );
    // CREATE on a file is meaningless and must not leak a letter.
    assert_eq!(oc_permissions(Perms::READ | Perms::CREATE, false, false), "G");
    // WRITE on a directory likewise.
    assert_eq!(oc_permissions(Perms::READ | Perms::WRITE, true, false), "G");

    // Shareable.
    assert_eq!(oc_permissions(Perms::READ | Perms::SHARE, false, false), "RG");

    // DOWNLOAD is a stowcloud-only bit with no compat letter; it must not
    // alter the string.
    assert_eq!(
        oc_permissions(Perms::READ, false, false),
        oc_permissions(Perms::READ | Perms::DOWNLOAD, false, false)
    );
}

/// `oc:id` is the other string clients key their whole journal on.
#[test]
fn oc_id_format_string() {
    use sc_compat_nc::nc_id;
    use sc_compat_nc::ports::FileId;

    assert_eq!(nc_id(FileId(123), "oc9k2m4x1p"), "00000123oc9k2m4x1p");
    // Zero-padding is to a *minimum* of 8, matching sprintf('%08d').
    assert_eq!(nc_id(FileId(0), "inst"), "00000000inst");
    assert_eq!(nc_id(FileId(99_999_999), "inst"), "99999999inst");
    assert_eq!(nc_id(FileId(100_000_000), "inst"), "100000000inst");
    // The id is a plain concatenation: no separator, ever. A separator would
    // change every file identity on the wire.
    let id = nc_id(FileId(7), "ocabc");
    assert!(!id.contains('-') && !id.contains('_') && !id.contains(':'));
    assert_eq!(&id[..8], "00000007");
    assert_eq!(&id[8..], "ocabc");
}
