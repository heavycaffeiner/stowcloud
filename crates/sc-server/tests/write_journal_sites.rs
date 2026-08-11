//! One test per write site the journal records, and per site it deliberately
//! does not.
//!
//! Driven through the two `CoreApi` traits and the upload port rather than
//! over HTTP: this file is about which write records what, and the router adds
//! nothing to that question. The end-to-end view is in `recorded_activity.rs`.

use std::collections::HashMap;
use std::sync::Arc;

use sc_acl::{AclEngine, Grant, Perms as AclPerms, Principal};
use sc_http::core_api as hapi;
use sc_http::upload_api::UploadApi as _;
use sc_server::bridge::{CoreBridge, UploadBridge};
use sc_server::journal::{WriteJournal, WriteOp};
use sc_vfs::{SafePath, ShareId, SharePolicy, TrashMode, UserId};

const USER: UserId = UserId::new(1);
const SHARE: ShareId = ShareId::new(1);

struct Fixture {
    bridge: CoreBridge,
    uploads: UploadBridge,
    core: Arc<sc_core::Core>,
    journal: Arc<WriteJournal>,
    host: std::path::PathBuf,
    _dir: tempfile::TempDir,
}

impl Fixture {
    /// Every recorded row as `(path, op)`, newest first.
    fn rows(&self) -> Vec<(String, WriteOp)> {
        self.journal
            .newest(USER, i64::MIN)
            .into_iter()
            .map(|r| (r.path, r.op))
            .collect()
    }

    fn op_for(&self, path: &str) -> Option<WriteOp> {
        self.rows().into_iter().find(|(p, _)| p == path).map(|(_, o)| o)
    }
}

fn fixture() -> Fixture {
    fixture_with(TrashMode::Off, true)
}

fn fixture_with(trash: TrashMode, with_journal: bool) -> Fixture {
    let dir = tempfile::tempdir().unwrap();
    let host = dir.path().join("data");
    std::fs::create_dir_all(&host).unwrap();

    let meta = Arc::new(sc_meta::MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(AclEngine::new());
    acl.replace_grants(vec![Grant {
        id: 1,
        principal: Principal::User(USER),
        share: SHARE,
        subpath: SafePath::root(),
        allow: AclPerms::all(),
        deny: AclPerms::empty(),
        inherit: true,
        label: Some("root".into()),
    }]);
    let core = Arc::new(sc_core::Core::new(meta, acl));
    core.register_share(sc_core::ShareDef {
        id: SHARE,
        name: "root".into(),
        host_path: host.clone(),
        policy: SharePolicy { trash, ..SharePolicy::default() },
        shared_externally: false,
    })
    .unwrap();

    let journal = Arc::new(WriteJournal::open(&dir.path().join("journal.db")).unwrap());
    let handle = with_journal.then(|| journal.clone());
    let bridge = CoreBridge::new(
        core.clone(),
        false,
        None,
        Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        handle.clone(),
    );
    let engine = Arc::new(
        sc_upload::UploadEngine::new(
            &dir.path().join("upload.db"),
            sc_upload::UploadConfig::default(),
        )
        .unwrap(),
    );
    let uploads = UploadBridge { engine, core: core.clone(), journal: handle };
    Fixture { bridge, uploads, core, journal, host, _dir: dir }
}

fn write(f: &Fixture, vpath: &str, content: &str) -> hapi::Entry {
    hapi::CoreApi::write_text(&f.bridge, USER, vpath, content, None).unwrap()
}

/// The label distinguishes what the caller did, so a first save is an upload
/// and a save over a file that was already there is an edit.
/// `Core::write_text` only accepts an etag when the file existed, which is what
/// makes this answerable without a second `stat`.
#[test]
fn a_native_save_records_upload_then_edit() {
    let f = fixture();
    let e = write(&f, "root/a.txt", "one");
    assert_eq!(f.op_for("a.txt"), Some(WriteOp::Upload));

    hapi::CoreApi::write_text(&f.bridge, USER, "root/a.txt", "two", Some(&e.etag)).unwrap();
    assert_eq!(f.op_for("a.txt"), Some(WriteOp::Edit));
    assert_eq!(f.rows().len(), 1, "one row per file, holding the newest event");
}

/// DAV `PUT` computes the etag itself, so it reaches the same answer.
#[test]
fn a_dav_put_records_upload_then_edit() {
    let f = fixture();
    sc_dav::backend::CoreApi::write_bytes(&f.bridge, USER, "root/a.txt", b"one").unwrap();
    assert_eq!(f.op_for("a.txt"), Some(WriteOp::Upload));
    sc_dav::backend::CoreApi::write_bytes(&f.bridge, USER, "root/a.txt", b"two").unwrap();
    assert_eq!(f.op_for("a.txt"), Some(WriteOp::Edit));
}

#[test]
fn dav_copy_and_move_record_their_destinations() {
    let f = fixture();
    write(&f, "root/a.txt", "x");
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();

    sc_dav::backend::CoreApi::copy_to(&f.bridge, USER, "root/a.txt", "root/b.txt").unwrap();
    assert_eq!(f.op_for("b.txt"), Some(WriteOp::Copy));

    sc_dav::backend::CoreApi::rename(&f.bridge, USER, "root/b.txt", "root/d/c.txt").unwrap();
    assert_eq!(f.op_for("d/c.txt"), Some(WriteOp::Move));

    sc_dav::backend::CoreApi::copy_entries(&f.bridge, USER, &["root/a.txt".into()], "root/d")
        .unwrap();
    assert_eq!(f.op_for("d/a.txt"), Some(WriteOp::Copy));

    sc_dav::backend::CoreApi::move_entries(&f.bridge, USER, &["root/d/c.txt".into()], "root")
        .unwrap();
    assert_eq!(f.op_for("c.txt"), Some(WriteOp::Move));
}

#[test]
fn a_native_rename_records_its_new_name() {
    let f = fixture();
    write(&f, "root/a.txt", "x");
    hapi::CoreApi::rename(&f.bridge, USER, "root/a.txt", "b.txt").unwrap();
    assert_eq!(f.op_for("b.txt"), Some(WriteOp::Move));
}

/// "Keep both" publishes at a name `unique_name` chose, which no caller can
/// reconstruct from the source. The row has to name the file that now exists,
/// not the one it collided with.
#[test]
fn a_native_copy_with_keep_both_records_the_renamed_destination() {
    let f = fixture();
    write(&f, "root/a.txt", "src");
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    write(&f, "root/d/a.txt", "already here");

    let results = hapi::CoreApi::copy_entries(
        &f.bridge,
        USER,
        &["root/a.txt".into()],
        "root/d",
        hapi::OnConflict::Rename,
        &HashMap::new(),
    )
    .unwrap();
    assert!(results[0].ok, "{:?}", results[0].error);

    let copied: Vec<String> = f
        .rows()
        .into_iter()
        .filter(|(_, o)| *o == WriteOp::Copy)
        .map(|(p, _)| p)
        .collect();
    assert_eq!(copied.len(), 1, "one copy happened, so one copy row: {copied:?}");
    assert_ne!(copied[0], "d/a.txt", "the collided-with file is not the one created");
    assert_eq!(
        std::fs::read_to_string(f.host.join(&copied[0])).unwrap(),
        "src",
        "the row names the file the copy actually made"
    );
}

/// `OnConflict::Skip` answers `ok: true` having copied nothing. Recording
/// every `ok` row would claim a copy that did not happen, at a path that
/// belongs to somebody else's file.
#[test]
fn a_skipped_batch_item_records_nothing_while_still_reporting_ok() {
    let f = fixture();
    write(&f, "root/a.txt", "src");
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    write(&f, "root/d/a.txt", "theirs");

    let results = hapi::CoreApi::copy_entries(
        &f.bridge,
        USER,
        &["root/a.txt".into()],
        "root/d",
        hapi::OnConflict::Skip,
        &HashMap::new(),
    )
    .unwrap();
    assert!(results[0].ok, "a skip is not a failure");
    assert!(
        !f.rows().iter().any(|(_, o)| *o == WriteOp::Copy),
        "nothing was copied, so nothing may be recorded: {:?}",
        f.rows()
    );
    assert_eq!(
        f.op_for("d/a.txt"),
        Some(WriteOp::Upload),
        "the row already there is untouched"
    );
}

#[test]
fn a_native_batch_move_records_its_destination() {
    let f = fixture();
    write(&f, "root/a.txt", "x");
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    hapi::CoreApi::move_entries(
        &f.bridge,
        USER,
        &["root/a.txt".into()],
        "root/d",
        hapi::OnConflict::Fail,
        &HashMap::new(),
    )
    .unwrap();
    assert_eq!(f.op_for("d/a.txt"), Some(WriteOp::Move));
}

/// A dry run creates nothing, so it records nothing.
#[test]
fn a_move_dry_run_records_nothing() {
    let f = fixture();
    write(&f, "root/a.txt", "x");
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    hapi::CoreApi::move_entries_dry_run(
        &f.bridge,
        USER,
        &["root/a.txt".into()],
        "root/d",
        hapi::OnConflict::Fail,
        &HashMap::new(),
    )
    .unwrap();
    assert!(!f.rows().iter().any(|(_, o)| *o == WriteOp::Move));
}

#[test]
fn a_trash_restore_records_where_it_landed() {
    let f = fixture_with(TrashMode::ShareLocal, true);
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    write(&f, "root/d/a.txt", "x");
    hapi::CoreApi::delete(&f.bridge, USER, &["root/d/a.txt".into()], false).unwrap();

    let listed = hapi::CoreApi::trash_list(&f.bridge, USER).unwrap();
    assert_eq!(listed.len(), 1);
    let results = hapi::CoreApi::trash_restore(&f.bridge, USER, &[listed[0].id.clone()]).unwrap();
    assert!(results[0].ok, "{:?}", results[0].error);
    assert_eq!(f.op_for("d/a.txt"), Some(WriteOp::Restore));
}

/// A trash entry written before the original path was encoded in its name
/// restores to the share root. Deriving the path from `TrashEntry::orig_path`
/// would agree for new entries and name a file that is not there for exactly
/// these.
#[test]
fn a_legacy_trash_entry_records_the_share_root_it_landed_in() {
    let f = fixture_with(TrashMode::ShareLocal, true);
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    write(&f, "root/d/a.txt", "x");
    hapi::CoreApi::delete(&f.bridge, USER, &["root/d/a.txt".into()], false).unwrap();

    // Rewrite the entry into the pre-fix spelling, `{id}-{basename}`, with no
    // encoded parent.
    let trash = f.host.join(".sctrash");
    let entry = std::fs::read_dir(&trash).unwrap().next().unwrap().unwrap();
    let name = entry.file_name().to_string_lossy().into_owned();
    let id = name.split('-').next().unwrap().to_string();
    std::fs::rename(entry.path(), trash.join(format!("{id}-a.txt"))).unwrap();

    let listed = hapi::CoreApi::trash_list(&f.bridge, USER).unwrap();
    let results = hapi::CoreApi::trash_restore(&f.bridge, USER, &[listed[0].id.clone()]).unwrap();
    assert!(results[0].ok, "{:?}", results[0].error);
    assert_eq!(
        f.op_for("a.txt"),
        Some(WriteOp::Restore),
        "it landed in the share root, so that is what the row names: {:?}",
        f.rows()
    );
}

#[test]
fn a_tus_upload_records_the_path_the_engine_published() {
    let f = fixture();
    let body = b"tus body";
    let id = f
        .uploads
        .create(USER, "root/up.txt", Some(body.len() as u64), false)
        .unwrap();
    f.uploads.patch(USER, &id, 0, body).unwrap();
    assert_eq!(f.op_for("up.txt"), Some(WriteOp::Upload));
    assert_eq!(std::fs::read_to_string(f.host.join("up.txt")).unwrap(), "tus body");
}

#[test]
fn a_creation_with_upload_records_the_path_the_engine_published() {
    let f = fixture();
    let body = b"all of it in the POST";
    f.uploads
        .create_with_upload(USER, "root/cwu.txt", Some(body.len() as u64), false, body)
        .unwrap();
    assert_eq!(f.op_for("cwu.txt"), Some(WriteOp::Upload));
}

/// The list shows files, and neither of these produces one anybody is looking
/// for: a deleted file has nothing to show, a directory row renders nothing,
/// and a `LOCK` placeholder is a zero-byte file nobody asked for.
#[test]
fn mkdir_delete_and_create_empty_record_nothing() {
    let f = fixture();
    hapi::CoreApi::mkdir(&f.bridge, USER, "root/d").unwrap();
    sc_dav::backend::CoreApi::create_empty(&f.bridge, USER, "root/locked.txt").unwrap();
    write(&f, "root/gone.txt", "x");
    hapi::CoreApi::delete(&f.bridge, USER, &["root/gone.txt".into()], true).unwrap();

    let paths: Vec<String> = f.rows().into_iter().map(|(p, _)| p).collect();
    assert_eq!(paths, vec!["gone.txt".to_string()], "only the write is recorded");
}

/// The record is best-effort: a server whose `journal.db` would not open still
/// writes files.
#[test]
fn a_write_succeeds_with_no_journal_behind_it() {
    let f = fixture_with(TrashMode::Off, false);
    write(&f, "root/a.txt", "x");
    assert_eq!(std::fs::read_to_string(f.host.join("a.txt")).unwrap(), "x");
    assert!(f.rows().is_empty());
}

/// Share ids are reused, so the rows have to go with the share.
#[test]
fn purging_a_share_leaves_it_no_rows() {
    let f = fixture();
    write(&f, "root/a.txt", "x");
    assert_eq!(f.rows().len(), 1);
    f.journal.forget_share(SHARE);
    assert!(f.rows().is_empty());
    let _ = &f.core;
}
