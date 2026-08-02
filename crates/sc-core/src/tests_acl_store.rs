//! Persisted-grant tests — the properties that motivated this module in the
//! first place (`acl_store.rs`'s module doc): a user
//! with no grant sees no roots, a subpath grant exposes only that subtree,
//! `deny` beats `allow`, and the legacy-projection migration runs exactly
//! once. None of these can fail against `sc-acl`'s in-memory engine alone
//! (that crate's own `tests.rs` already covers the algorithm) — they only
//! fail if the *persistence* wiring in this file is wrong, which is exactly
//! what's new here.

use std::sync::Arc;

use sc_acl::{Perms, Principal};
use sc_meta::MetaStore;
use sc_vfs::{ShareId, SharePolicy, UserId};

use crate::acl_store::{perms_from_names, perms_to_names, AclStore, GrantFilter, GrantPatch, GrantSpec};
use crate::share::ShareDef;
use crate::{Core, CoreError};

const ALICE: UserId = UserId::new(1);
const BOB: UserId = UserId::new(2);
const SHARE: ShareId = ShareId::new(1);

/// A `Core` with a real (in-memory) grant store attached and one share
/// registered, but **no grants at all** — the new default.
fn setup() -> (Arc<Core>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(sc_acl::AclEngine::new());
    let core = Arc::new(Core::new(meta, acl));
    core.attach_acl_store(AclStore::open_in_memory().unwrap()).unwrap();
    core.register_share(ShareDef {
        id: SHARE,
        name: "photos".to_string(),
        host_path: dir.path().to_path_buf(),
        policy: SharePolicy::default(),
        shared_externally: false,
    })
    .unwrap();
    (core, dir)
}

fn spec(principal: Principal, subpath: &str, allow: Perms) -> GrantSpec {
    GrantSpec {
        principal,
        share: SHARE,
        subpath: subpath.to_string(),
        allow,
        deny: Perms::empty(),
        inherit: true,
        label: None,
    }
}

// ---------------------------------------------------------------------------
// 1. no grant -> no roots, nothing resolves
// ---------------------------------------------------------------------------

#[test]
fn a_user_with_no_grant_sees_no_roots() {
    let (core, _dir) = setup();
    assert!(core.roots(ALICE).is_empty(), "no grant was ever created for this user");
    assert!(matches!(core.resolve(ALICE, "/photos/anything"), Err(CoreError::NotFound)));
}

#[test]
fn creating_a_grant_makes_the_root_appear_immediately() {
    let (core, _dir) = setup();
    assert!(core.roots(ALICE).is_empty());

    core.create_grant(&spec(Principal::User(ALICE), "", Perms::READ | Perms::DOWNLOAD)).unwrap();

    // No restart, no explicit reload call from the test itself —
    // `create_grant` pushes into the live `AclEngine` on its own.
    let roots = core.roots(ALICE);
    assert_eq!(roots.len(), 1);
    assert_eq!(roots[0].label, "photos");

    // A second user granted nothing still sees nothing: grants don't leak
    // across principals.
    assert!(core.roots(BOB).is_empty());
}

// ---------------------------------------------------------------------------
// 2. a subpath grant exposes only that subtree
// ---------------------------------------------------------------------------

#[test]
fn a_subpath_grant_exposes_only_that_subtree() {
    let (core, dir) = setup();
    std::fs::create_dir(dir.path().join("vacation")).unwrap();
    std::fs::write(dir.path().join("vacation").join("beach.jpg"), b"jpeg-ish bytes").unwrap();
    std::fs::write(dir.path().join("secret.txt"), b"not granted").unwrap();

    core.create_grant(&spec(Principal::User(ALICE), "vacation", Perms::READ | Perms::DOWNLOAD)).unwrap();

    let roots = core.roots(ALICE);
    assert_eq!(roots.len(), 1, "exactly one root: the granted subtree, not the share root");
    assert_eq!(roots[0].label, "vacation");

    // The granted subtree resolves and lists its own contents.
    // `resolved.path` is share-root-relative (real on-disk path), not
    // virtual-root-relative — the grant's own subpath is folded in.
    let resolved = core.resolve(ALICE, "/vacation/beach.jpg").unwrap();
    assert_eq!(resolved.path.to_display_string(), "vacation/beach.jpg");
    let listing = core.list(ALICE, "/vacation", crate::Sort::Name, crate::Order::Asc).unwrap();
    assert_eq!(listing.entries.len(), 1);
    assert_eq!(listing.entries[0].name, "beach.jpg");

    // Nothing outside the granted subtree is reachable — not even under a
    // different label, because there is no other label at all: the share
    // root itself was never granted, so `secret.txt` (a sibling of
    // `vacation`, not inside it) has no virtual path that reaches it.
    assert!(matches!(core.resolve(ALICE, "/photos/secret.txt"), Err(CoreError::NotFound)));
    assert!(matches!(core.resolve(ALICE, "/secret.txt"), Err(CoreError::NotFound)));
}

// ---------------------------------------------------------------------------
// 3. deny beats allow, once persisted
// ---------------------------------------------------------------------------

#[test]
fn a_persisted_deny_beats_a_persisted_allow_at_the_same_depth() {
    let (core, dir) = setup();
    std::fs::create_dir(dir.path().join("shared")).unwrap();
    std::fs::create_dir(dir.path().join("shared").join("private")).unwrap();
    std::fs::write(dir.path().join("shared").join("ok.txt"), b"fine").unwrap();
    std::fs::write(dir.path().join("shared").join("private").join("nope.txt"), b"denied").unwrap();

    // Root grant: full read access to the whole share.
    core.create_grant(&spec(Principal::User(ALICE), "", Perms::READ | Perms::DOWNLOAD)).unwrap();
    // Deeper grant: explicit deny on the `private` subtree.
    core.create_grant(&GrantSpec {
        principal: Principal::User(ALICE),
        share: SHARE,
        subpath: "shared/private".to_string(),
        allow: Perms::empty(),
        deny: Perms::READ,
        inherit: true,
        label: None,
    })
    .unwrap();

    // Outside the deny: still readable.
    assert!(core.resolve(ALICE, "/photos/shared/ok.txt").is_ok());
    // Inside the deny: refused, even though the shallower grant allows READ
    // on the whole share.
    assert!(matches!(
        core.resolve(ALICE, "/photos/shared/private/nope.txt"),
        Err(CoreError::Denied { .. })
    ));
}

// ---------------------------------------------------------------------------
// 4. CRUD round-trip
// ---------------------------------------------------------------------------

#[test]
fn create_list_update_delete_round_trip() {
    let (core, _dir) = setup();

    let created = core.create_grant(&spec(Principal::User(ALICE), "docs", Perms::READ)).unwrap();
    assert_eq!(created.grant.allow, Perms::READ);
    assert!(!created.grant.deny.intersects(Perms::all()));

    let all = core.list_grants(&GrantFilter::default()).unwrap();
    assert_eq!(all.len(), 1);
    assert_eq!(all[0].grant.id, created.grant.id);

    let by_user = core.list_grants(&GrantFilter { principal: Some(Principal::User(ALICE)), share: None }).unwrap();
    assert_eq!(by_user.len(), 1);
    let by_other = core.list_grants(&GrantFilter { principal: Some(Principal::User(BOB)), share: None }).unwrap();
    assert!(by_other.is_empty());

    let updated = core
        .update_grant(created.grant.id, &GrantPatch { allow: Some(Perms::READ | Perms::WRITE), ..Default::default() })
        .unwrap();
    assert!(updated.grant.allow.contains(Perms::WRITE));
    // Effective access changes immediately, without a reload call.
    assert!(core.roots(ALICE)[0].perms.contains(Perms::WRITE));

    core.delete_grant(created.grant.id).unwrap();
    assert!(core.list_grants(&GrantFilter::default()).unwrap().is_empty());
    assert!(core.roots(ALICE).is_empty());

    // Deleting again is a clean `NotFound`, not a silent success.
    assert!(matches!(core.delete_grant(created.grant.id), Err(CoreError::NotFound)));
}

#[test]
fn a_grant_with_no_allow_and_no_deny_is_rejected() {
    let (core, _dir) = setup();
    let result = core.create_grant(&spec(Principal::User(ALICE), "", Perms::empty()));
    assert!(matches!(result, Err(CoreError::InvalidPath(_))));
}

// ---------------------------------------------------------------------------
// 5. legacy-projection migration
// ---------------------------------------------------------------------------

#[test]
fn migration_seeds_full_access_for_every_pre_existing_user_exactly_once() {
    let (core, _dir) = setup();
    assert!(core.roots(ALICE).is_empty());
    assert!(core.roots(BOB).is_empty());

    // Simulates an upgrade: both accounts already existed before grants
    // were ever persisted.
    core.migrate_legacy_grants(&[ALICE, BOB]).unwrap();

    assert_eq!(core.roots(ALICE).len(), 1, "the pre-existing admin keeps its old blanket access");
    assert_eq!(core.roots(BOB).len(), 1);

    // An admin now revokes Bob's access entirely.
    let bobs_grant = core.list_grants(&GrantFilter { principal: Some(Principal::User(BOB)), share: None }).unwrap();
    for g in bobs_grant {
        core.delete_grant(g.grant.id).unwrap();
    }
    assert!(core.roots(BOB).is_empty());

    // Running the migration again (as a restart would) must not re-seed
    // what was just deliberately revoked — the marker makes this a no-op.
    core.migrate_legacy_grants(&[ALICE, BOB]).unwrap();
    assert!(core.roots(BOB).is_empty(), "a second migration run must not undo an explicit revoke");
    assert_eq!(core.roots(ALICE).len(), 1);
}

#[test]
fn migration_seeds_nothing_for_a_brand_new_install() {
    let (core, _dir) = setup();
    // Zero pre-existing users: nothing to preserve, so nothing is seeded —
    // the new default (no access) applies even to whoever is created next.
    core.migrate_legacy_grants(&[]).unwrap();
    assert!(core.roots(ALICE).is_empty());

    // A user created *after* the migration marker was written gets nothing
    // automatically either, even if the migration function is called again
    // (it won't be seeded, since the marker is already set).
    core.migrate_legacy_grants(&[ALICE]).unwrap();
    assert!(core.roots(ALICE).is_empty(), "the marker was already set on the first (empty) call");
}

#[test]
fn seed_full_access_is_idempotent() {
    let (core, _dir) = setup();
    core.seed_full_access(ALICE).unwrap();
    assert_eq!(core.list_grants(&GrantFilter::default()).unwrap().len(), 1);
    core.seed_full_access(ALICE).unwrap();
    assert_eq!(core.list_grants(&GrantFilter::default()).unwrap().len(), 1, "calling it twice must not duplicate the grant");
}

// ---------------------------------------------------------------------------
// 6. wire-name round trip (used by the admin grant API's request/response bodies)
// ---------------------------------------------------------------------------

#[test]
fn perm_names_round_trip_every_bit() {
    let all = Perms::all();
    let names = perms_to_names(all);
    assert_eq!(names.len(), 8, "every permission bit must have exactly one name");
    assert_eq!(perms_from_names(&names), all);

    let subset = Perms::READ | Perms::DOWNLOAD;
    let subset_names = perms_to_names(subset);
    assert_eq!(subset_names, vec!["read", "download"]);
    assert_eq!(perms_from_names(&subset_names), subset);

    assert_eq!(perms_from_names(&["read", "bogus"]), Perms::READ, "an unrecognized name is ignored, not an error");
    assert!(perms_to_names(Perms::empty()).is_empty());
}

#[test]
fn a_store_that_was_never_attached_reloads_to_no_access_instead_of_erroring() {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(sc_acl::AclEngine::new());
    let core = Arc::new(Core::new(meta, acl));
    core.register_share(ShareDef {
        id: SHARE,
        name: "photos".to_string(),
        host_path: dir.path().to_path_buf(),
        policy: SharePolicy::default(),
        shared_externally: false,
    })
    .unwrap();

    assert!(!core.acl_store_enabled());
    assert!(core.reload_acl().is_ok());
    assert!(core.roots(ALICE).is_empty());
}
