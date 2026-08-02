//! `Core::create_share`/`update_share`/`delete_share` — the admin surface
//! was missing ("there is no setting to add
//! folders"). Covers the failure modes the admin UI needs to tell apart
//! (nonexistent path, not a directory, duplicate name, overlapping share),
//! that a `config.toml` share's edits are kept as overrides the next start
//! reapplies while its deletion stays refused, and that a delete cascades to
//! grants.

use std::sync::Arc;

use sc_acl::{Perms, Principal};
use sc_meta::MetaStore;
use sc_vfs::{ShareId, SharePolicy, TrashMode};

use crate::acl_store::AclStore;
use crate::share::{ShareDef, ShareStore, DYNAMIC_SHARE_ID_BASE};
use crate::Core;

const CONFIG_SHARE: ShareId = ShareId::new(1);

/// A `Core` with a real (in-memory) share store attached and one
/// config-file share already registered (`id: 1`, outside the dynamic
/// range) — mirrors what `sc-server::app::register_shares` +
/// `attach_share_store` produce at startup.
fn setup() -> (Arc<Core>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let docs = dir.path().join("docs");
    std::fs::create_dir(&docs).unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(sc_acl::AclEngine::new());
    let core = Arc::new(Core::new(meta, acl));
    core.attach_acl_store(AclStore::open_in_memory().unwrap()).unwrap();
    core.attach_share_store(ShareStore::open_in_memory().unwrap()).unwrap();
    core.register_share(ShareDef {
        id: CONFIG_SHARE,
        name: "docs".to_string(),
        host_path: docs,
        policy: SharePolicy::default(),
        shared_externally: false,
    })
    .unwrap();
    // `dir` itself is the tempdir root: new shares below live as its other
    // direct children (siblings of `docs`, not nested under it), so they
    // don't trip the overlap check against the config share.
    (core, dir)
}

#[test]
fn creating_a_share_registers_it_immediately_with_a_dynamic_id() {
    let (core, dir) = setup();
    let host = dir.path().join("photos");
    std::fs::create_dir(&host).unwrap();

    let def = core.create_share("photos".into(), host.clone()).unwrap();
    assert!(def.id.get() >= DYNAMIC_SHARE_ID_BASE, "admin-created shares live above the config range");
    assert_eq!(core.share_defs().len(), 2);
    assert!(core.share(def.id).is_some(), "registered against the live share map, no restart needed");
}

#[test]
fn an_empty_name_is_refused() {
    let (core, dir) = setup();
    let err = core.create_share("  ".into(), dir.path().to_path_buf()).unwrap_err();
    // The key is the contract — the admin screen renders it in whatever
    // language the reader picked, so it can never match on the message.
    assert_eq!(err.key(), Some("share.name_empty"));
    assert!(err.to_string().contains("empty"), "{err}");
}

#[test]
fn a_duplicate_name_is_refused() {
    let (core, dir) = setup();
    let err = core.create_share("docs".into(), dir.path().to_path_buf()).unwrap_err();
    assert_eq!(err.key(), Some("share.name_taken"));
    assert!(err.to_string().contains("already exists"), "{err}");
}

#[test]
fn a_nonexistent_host_path_is_refused() {
    let (core, dir) = setup();
    let missing = dir.path().join("does-not-exist");
    let err = core.create_share("gone".into(), missing).unwrap_err();
    assert_eq!(err.key(), Some("share.path_does_not_exist"));
    assert!(err.to_string().contains("does not exist"), "{err}");
}

#[test]
fn a_host_path_that_is_a_file_not_a_directory_is_refused() {
    let (core, dir) = setup();
    let file = dir.path().join("not-a-dir.txt");
    std::fs::write(&file, b"x").unwrap();
    let err = core.create_share("file-share".into(), file).unwrap_err();
    assert_eq!(err.key(), Some("share.path_not_a_directory"));
    assert!(err.to_string().contains("not a directory"), "{err}");
}

#[test]
fn a_host_path_overlapping_an_existing_share_is_refused() {
    let (core, dir) = setup();
    // Nested inside "docs"'s own host path (`dir/docs`), not just a sibling.
    let nested = dir.path().join("docs").join("nested");
    std::fs::create_dir(&nested).unwrap();
    let err = core.create_share("nested-share".into(), nested).unwrap_err();
    assert_eq!(err.key(), Some("share.path_overlaps"));
    assert!(err.to_string().contains("overlaps"), "{err}");
}

/// A `config.toml` share can be renamed and repointed here — the edit is
/// kept as an override keyed by `ShareId`, and `register_shares` reapplies
/// it at startup, which is the only thing the old blanket refusal was
/// guarding against.
#[test]
fn a_config_file_share_can_be_renamed_and_repointed() {
    let (core, dir) = setup();
    let new_host = dir.path().join("papers");
    std::fs::create_dir(&new_host).unwrap();

    let updated = core.update_share(CONFIG_SHARE, Some("papers".into()), Some(new_host.clone()), None).unwrap();
    assert_eq!(updated.name, "papers");
    assert_eq!(updated.host_path, new_host);
    assert_eq!(core.share_host_path(CONFIG_SHARE).unwrap(), new_host, "live share map repointed, no restart needed");
}

/// The restart path: `sc-server::app::register_shares` builds each def from
/// `config.toml` and runs it through `apply_identity_override`, so the
/// config file's own name and path are what get replaced.
#[test]
fn a_config_file_shares_edit_is_reapplied_over_the_config_file_on_restart() {
    let (core, dir) = setup();
    let new_host = dir.path().join("papers");
    std::fs::create_dir(&new_host).unwrap();
    core.update_share(CONFIG_SHARE, Some("papers".into()), Some(new_host.clone()), None).unwrap();

    let (name, host_path) = core.apply_identity_override(CONFIG_SHARE, "docs".into(), dir.path().join("docs"));
    assert_eq!(name, "papers");
    assert_eq!(host_path, new_host);
}

/// No override was ever set, so the config file's values pass through
/// untouched — every share on a fresh deployment takes this path.
#[test]
fn an_unedited_config_file_share_keeps_what_the_config_file_says() {
    let (core, dir) = setup();
    let docs = dir.path().join("docs");
    let (name, host_path) = core.apply_identity_override(CONFIG_SHARE, "docs".into(), docs.clone());
    assert_eq!(name, "docs");
    assert_eq!(host_path, docs);
}

/// Deleting is still refused: unlike a rename, there is nothing to override —
/// the `config.toml` entry would declare the share again on the next restart.
#[test]
fn a_config_file_share_cannot_be_deleted_here() {
    let (core, _dir) = setup();
    let delete_err = core.delete_share(CONFIG_SHARE).unwrap_err();
    assert_eq!(delete_err.key(), Some("share.config_defined_not_deletable"));
}

#[test]
fn updating_a_share_renames_and_repoints_it_in_place() {
    let (core, dir) = setup();
    let host = dir.path().join("photos");
    std::fs::create_dir(&host).unwrap();
    let def = core.create_share("photos".into(), host).unwrap();

    let new_host = dir.path().join("photos2");
    std::fs::create_dir(&new_host).unwrap();
    let updated = core.update_share(def.id, Some("vacation".into()), Some(new_host.clone()), None).unwrap();

    assert_eq!(updated.id, def.id, "same id, edited in place");
    assert_eq!(updated.name, "vacation");
    assert_eq!(updated.host_path, new_host);
    assert_eq!(core.share_host_path(def.id).unwrap(), new_host);
}

#[test]
fn trash_is_off_by_default_and_toggleable_on_a_dynamic_share() {
    let (core, dir) = setup();
    let host = dir.path().join("photos");
    std::fs::create_dir(&host).unwrap();
    let def = core.create_share("photos".into(), host).unwrap();
    assert_eq!(def.policy.trash, TrashMode::Off, "off by default");

    let on = core.update_share(def.id, None, None, Some(true)).unwrap();
    assert_eq!(on.policy.trash, TrashMode::ShareLocal);
    assert_eq!(core.share(def.id).unwrap().policy().trash, TrashMode::ShareLocal, "live ShareRoot re-registered");

    let off = core.update_share(def.id, None, None, Some(false)).unwrap();
    assert_eq!(off.policy.trash, TrashMode::Off);
}

/// Same override mechanism as the rename above, and predating it: the toggle
/// lives in `shares.db` keyed by the real `ShareId`, so it survives a restart
/// the same way a dynamic share's does.
#[test]
fn trash_is_toggleable_on_a_config_file_share() {
    let (core, _dir) = setup();
    let updated = core.update_share(CONFIG_SHARE, None, None, Some(true)).unwrap();
    assert_eq!(updated.policy.trash, TrashMode::ShareLocal);
    assert_eq!(core.share(CONFIG_SHARE).unwrap().policy().trash, TrashMode::ShareLocal);
}

#[test]
fn deleting_a_share_cascades_to_its_grants() {
    let (core, dir) = setup();
    let host = dir.path().join("photos");
    std::fs::create_dir(&host).unwrap();
    let def = core.create_share("photos".into(), host).unwrap();

    core.create_grant(&crate::GrantSpec {
        principal: Principal::User(sc_vfs::UserId::new(1)),
        share: def.id,
        subpath: String::new(),
        allow: Perms::READ,
        deny: Perms::empty(),
        inherit: true,
        label: None,
    })
    .unwrap();
    assert_eq!(core.list_grants(&crate::GrantFilter { principal: None, share: Some(def.id) }).unwrap().len(), 1);

    core.delete_share(def.id).unwrap();

    assert!(core.share(def.id).is_none(), "unregistered from the live share map");
    assert!(
        core.list_grants(&crate::GrantFilter { principal: None, share: Some(def.id) }).unwrap().is_empty(),
        "grant naming the deleted share is gone too, not left orphaned"
    );
}

#[test]
fn deleting_an_unknown_share_is_not_found() {
    let (core, _dir) = setup();
    let unknown = ShareId::new(DYNAMIC_SHARE_ID_BASE + 999);
    assert!(matches!(core.delete_share(unknown), Err(crate::CoreError::NotFound)));
}
