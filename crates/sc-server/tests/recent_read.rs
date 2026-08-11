//! The read side of the record: which rows reach an answer, and which of them
//! the read is allowed to delete.
//!
//! A row is deleted in exactly one case, `stat` answering `NotFound`. Every
//! other reason for dropping a row from an answer leaves the table alone,
//! because this record cannot be rebuilt from anything and the alternatives
//! reverse: a grant revoked today may be granted again tomorrow, and a share
//! that fails to mount at boot would otherwise erase every account's history
//! on the first page load.

use std::collections::HashMap;
use std::sync::Arc;

use sc_acl::{AclEngine, Grant, Perms as AclPerms, Principal};
use sc_http::core_api as hapi;
use sc_http::recent_api::{RecentApi, RecentQuery};
use sc_server::bridge::CoreBridge;
use sc_server::journal::WriteJournal;
use sc_server::recent::RecentEngine;
use sc_vfs::{SafePath, ShareId, SharePolicy, UserId};

const USER: UserId = UserId::new(1);
const SHARE: ShareId = ShareId::new(1);

struct Fixture {
    bridge: CoreBridge,
    engine: RecentEngine,
    acl: Arc<AclEngine>,
    journal: Arc<WriteJournal>,
    _dir: tempfile::TempDir,
}

fn full_access() -> Vec<Grant> {
    vec![Grant {
        id: 1,
        principal: Principal::User(USER),
        share: SHARE,
        subpath: SafePath::root(),
        allow: AclPerms::all(),
        deny: AclPerms::empty(),
        inherit: true,
        label: Some("root".into()),
    }]
}

fn fixture() -> Fixture {
    let dir = tempfile::tempdir().unwrap();
    let host = dir.path().join("data");
    std::fs::create_dir_all(&host).unwrap();

    let meta = Arc::new(sc_meta::MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(AclEngine::new());
    acl.replace_grants(full_access());
    let core = Arc::new(sc_core::Core::new(meta, acl.clone()));
    core.register_share(sc_core::ShareDef {
        id: SHARE,
        name: "root".into(),
        host_path: host,
        policy: SharePolicy::default(),
        shared_externally: false,
    })
    .unwrap();

    let journal = Arc::new(WriteJournal::open(&dir.path().join("journal.db")).unwrap());
    let bridge = CoreBridge::new(
        core.clone(),
        false,
        None,
        Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        Some(journal.clone()),
    );
    let engine = RecentEngine { core, journal: Some(journal.clone()) };
    Fixture { bridge, engine, acl, journal, _dir: dir }
}

fn query(scope: Option<&str>) -> RecentQuery {
    RecentQuery {
        scope: scope.map(str::to_string),
        since_ns: i128::MIN,
        limit: 100,
    }
}

fn vpaths(f: &Fixture, scope: Option<&str>) -> Vec<String> {
    f.engine
        .recent(USER, &query(scope))
        .unwrap()
        .into_iter()
        .map(|h| h.vpath)
        .collect()
}

/// A revoked grant hides the rows without any revocation bookkeeping, and
/// re-granting shows them again. The table is untouched throughout.
#[test]
fn a_revoked_grant_hides_rows_it_does_not_delete_them() {
    let f = fixture();
    hapi::CoreApi::write_text(&f.bridge, USER, "root/a.txt", "x", None).unwrap();
    assert_eq!(vpaths(&f, None), vec!["root/a.txt".to_string()]);

    f.acl.replace_grants(Vec::new());
    assert!(vpaths(&f, None).is_empty(), "no grant, no answer");
    assert_eq!(
        f.journal.newest(USER, i64::MIN).len(),
        1,
        "the row survives a revocation, which can be reversed"
    );

    f.acl.replace_grants(full_access());
    assert_eq!(vpaths(&f, None), vec!["root/a.txt".to_string()], "and comes back with the grant");
}

/// A directory-level copy records the directory, not the thousands of files
/// under it. The files-only reader then shows nothing for it, rather than the
/// operation evicting every other row the account has.
#[test]
fn a_directory_row_never_renders() {
    let f = fixture();
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/src").unwrap();
    hapi::CoreApi::write_text(&f.bridge, USER, "root/src/inner.txt", "x", None).unwrap();
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/dst").unwrap();
    let results = hapi::CoreApi::copy_entries(
        &f.bridge,
        USER,
        &["root/src".into()],
        "root/dst",
        hapi::OnConflict::Fail,
        &HashMap::new(),
    )
    .unwrap();
    assert!(results[0].ok, "{:?}", results[0].error);

    let stored: Vec<String> = f
        .journal
        .newest(USER, i64::MIN)
        .into_iter()
        .map(|r| r.path)
        .collect();
    assert!(stored.contains(&"dst/src".to_string()), "the directory is recorded: {stored:?}");
    assert_eq!(
        vpaths(&f, None),
        vec!["root/src/inner.txt".to_string()],
        "and never rendered, while the file the account wrote still is"
    );
}

/// A scope narrows the answer, and one that does not resolve is refused rather
/// than widened to everything.
#[test]
fn a_scope_narrows_and_a_bad_one_is_refused() {
    let f = fixture();
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    hapi::CoreApi::write_text(&f.bridge, USER, "root/top.txt", "x", None).unwrap();
    hapi::CoreApi::write_text(&f.bridge, USER, "root/d/inner.txt", "x", None).unwrap();

    assert_eq!(vpaths(&f, None).len(), 2);
    assert_eq!(vpaths(&f, Some("root/d")), vec!["root/d/inner.txt".to_string()]);
    assert!(f.engine.recent(USER, &query(Some("nosuchshare"))).is_err());
}

/// The two-hundred-and-first row is still the two-hundred-and-first row: two
/// identical requests over an unchanged table return the identical sequence,
/// including for rows that share a timestamp.
#[test]
fn two_identical_requests_return_the_identical_sequence() {
    let f = fixture();
    for i in 0..20 {
        hapi::CoreApi::write_text(&f.bridge, USER, &format!("root/f{i:02}.txt"), "x", None)
            .unwrap();
    }
    let first = vpaths(&f, None);
    assert_eq!(first.len(), 20);
    assert_eq!(first, vpaths(&f, None));
}

/// `forget_user` is the account-deletion half, reachable through the same port
/// the read goes through because that is the only seam `sc-http` has into the
/// store.
#[test]
fn forget_user_empties_the_account() {
    let f = fixture();
    hapi::CoreApi::write_text(&f.bridge, USER, "root/a.txt", "x", None).unwrap();
    f.engine.forget_user(USER);
    assert!(vpaths(&f, None).is_empty());
    assert!(f.journal.newest(USER, i64::MIN).is_empty());
}
