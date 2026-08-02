//! Per-user homes (`FEATURES.md` #47, `homes.rs`): off by default, one
//! idempotent directory+grant per user, template-seeded when a `.template`
//! directory exists, and never blanket-granted by `seed_full_access`.

use std::sync::Arc;

use sc_acl::Principal;
use sc_meta::MetaStore;
use sc_vfs::UserId;

use crate::acl_store::AclStore;
use crate::homes::HOME_SHARE_ID;
use crate::Core;

const USER: UserId = UserId::new(1);
const OTHER: UserId = UserId::new(2);

/// A `Core` with a real (in-memory) grant store attached, no homes attached
/// yet -- mirrors `sc-server::app::App::build` when `homes.enabled = false`.
fn setup() -> (Arc<Core>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(sc_acl::AclEngine::new());
    let core = Arc::new(Core::new(meta, acl));
    core.attach_acl_store(AclStore::open_in_memory().unwrap()).unwrap();
    (core, dir)
}

#[test]
fn homes_disabled_by_default_grants_no_root_and_ensure_home_is_a_silent_no_op() {
    let (core, _dir) = setup();
    assert!(!core.homes_enabled());
    // `roots()`/`resolve()` call `ensure_home` internally on every request;
    // with nothing attached this must stay a true no-op, not an error.
    assert!(core.roots(USER).is_empty());
    assert!(core.resolve(USER, "/Home/file.txt").is_err());
}

#[test]
fn enabling_homes_creates_the_root_directory_and_registers_the_share() {
    let (core, dir) = setup();
    let homes_root = dir.path().join("homes");
    assert!(!homes_root.exists(), "attach_homes creates it, not the test");
    core.attach_homes(&homes_root).unwrap();
    assert!(homes_root.is_dir());
    assert!(core.homes_enabled());
    assert!(core.share(HOME_SHARE_ID).is_some());
}

#[test]
fn a_users_first_request_gets_a_home_directory_contained_under_homes_root() {
    let (core, dir) = setup();
    let homes_root = dir.path().join("homes");
    core.attach_homes(&homes_root).unwrap();

    let roots = core.roots(USER);
    let home = roots.iter().find(|r| r.label == "Home").expect("home root present after first listing");
    assert_eq!(home.share, HOME_SHARE_ID);

    let host_dir = homes_root.join(USER.get().to_string());
    assert!(host_dir.is_dir(), "per-user directory created on the host under homes_root");

    // Resolves and is writable end to end, same as any other grant.
    let resolved = core.resolve_for_upload(USER, "/Home/note.txt").unwrap();
    assert_eq!(resolved.share, HOME_SHARE_ID);
    std::fs::write(host_dir.join("note.txt"), b"hi").unwrap();
    assert!(core.resolve(USER, "/Home/note.txt").is_ok());
}

#[test]
fn ensure_home_is_idempotent_and_does_not_clobber_an_existing_home() {
    let (core, dir) = setup();
    let homes_root = dir.path().join("homes");
    core.attach_homes(&homes_root).unwrap();

    core.roots(USER); // first-time creation
    let host_dir = homes_root.join(USER.get().to_string());
    std::fs::write(host_dir.join("keepme.txt"), b"important").unwrap();

    // Every subsequent request re-runs `ensure_home` -- must not touch an
    // already-provisioned home.
    for _ in 0..5 {
        core.roots(USER);
        core.resolve(USER, "/Home/keepme.txt").unwrap();
    }
    assert_eq!(std::fs::read(host_dir.join("keepme.txt")).unwrap(), b"important");

    // Exactly one "Home" grant, not "Home"/"Home (2)" from a duplicated grant.
    let roots = core.roots(USER);
    assert_eq!(roots.iter().filter(|r| r.share == HOME_SHARE_ID).count(), 1);
}

#[test]
fn a_users_home_is_contained_and_invisible_to_another_user() {
    let (core, dir) = setup();
    let homes_root = dir.path().join("homes");
    core.attach_homes(&homes_root).unwrap();

    core.roots(USER);
    let user_home = homes_root.join(USER.get().to_string());
    std::fs::write(user_home.join("secret.txt"), b"private").unwrap();

    core.roots(OTHER);
    // OTHER's own home resolves fine...
    assert!(core.resolve_for_upload(OTHER, "/Home/mine.txt").is_ok());
    // ...but OTHER has no grant reaching into USER's subpath: same label,
    // different (and non-overlapping) subpath per grant.
    let other_home = homes_root.join(OTHER.get().to_string());
    assert_ne!(user_home, other_home);

    // A symlink planted inside OTHER's home pointing at USER's home must not
    // be followed out of OTHER's own subpath (`SharePolicy::default()` is
    // `SymlinkPolicy::Deny`).
    //
    // Asserted against calls that open something, not against `resolve`:
    // `resolve` is label lookup plus an ACL decision over a lexical path and
    // touches no filesystem at all, so it answers `Ok` here by design and
    // always did. Containment is the kernel's `RESOLVE_NO_SYMLINKS`, which
    // only exists once a real syscall runs — this block never compiled on the
    // Windows dev box, so the mislaid assertion sat here unexecuted.
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(&user_home, other_home.join("escape")).unwrap();
        assert!(core.read_bytes(OTHER, "/Home/escape/secret.txt", 4096).is_err());
        assert!(core.stat_entry(OTHER, "/Home/escape/secret.txt").is_err());
        assert!(core
            .list(
                OTHER,
                "/Home/escape",
                crate::entry::Sort::Name,
                crate::entry::Order::Asc
            )
            .is_err());
    }
}

#[test]
fn seed_full_access_never_grants_the_homes_share() {
    let (core, dir) = setup();
    core.attach_homes(&dir.path().join("homes")).unwrap();

    core.seed_full_access(USER).unwrap();
    let grants = core
        .list_grants(&crate::GrantFilter { principal: Some(Principal::User(USER)), share: None })
        .unwrap();
    assert!(
        grants.iter().all(|r| r.grant.share != HOME_SHARE_ID),
        "seed_full_access must never blanket-grant the shared homes root"
    );
}

#[test]
fn a_home_is_seeded_from_the_template_directory_when_one_exists() {
    let (core, dir) = setup();
    let homes_root = dir.path().join("homes");
    std::fs::create_dir_all(&homes_root).unwrap();
    let template = homes_root.join(".template");
    std::fs::create_dir(&template).unwrap();
    std::fs::write(template.join("welcome.txt"), b"hello new user").unwrap();
    std::fs::create_dir(template.join("subdir")).unwrap();
    std::fs::write(template.join("subdir").join("nested.txt"), b"nested").unwrap();

    core.attach_homes(&homes_root).unwrap();
    core.roots(USER);

    let host_dir = homes_root.join(USER.get().to_string());
    assert_eq!(std::fs::read(host_dir.join("welcome.txt")).unwrap(), b"hello new user");
    assert_eq!(std::fs::read(host_dir.join("subdir").join("nested.txt")).unwrap(), b"nested");
}

#[test]
fn without_a_template_directory_a_home_is_created_empty() {
    let (core, dir) = setup();
    let homes_root = dir.path().join("homes");
    core.attach_homes(&homes_root).unwrap();
    core.roots(USER);

    let host_dir = homes_root.join(USER.get().to_string());
    assert!(host_dir.is_dir());
    assert_eq!(std::fs::read_dir(&host_dir).unwrap().count(), 0);
}
