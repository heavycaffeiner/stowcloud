use std::collections::HashMap;
use std::sync::Arc;

use sc_acl::{AclEngine, Grant, Perms, Principal};
use sc_meta::MetaStore;
use sc_vfs::{SafePath, ShareId, SharePolicy, TrashMode, UserId};

use crate::share::ShareDef;
use crate::{Core, OnConflict, Order, Sort};

const USER: UserId = UserId::new(1);
const SHARE: ShareId = ShareId::new(1);

fn setup() -> (Core, tempfile::TempDir) {
    setup_with_policy(SharePolicy::default())
}

/// Same as `setup()`, but with a caller-chosen policy — used by the trash
/// tests below, which need `trash: ShareLocal` explicitly now that
/// `SharePolicy::default()` is `Off`.
fn setup_with_policy(policy: SharePolicy) -> (Core, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(AclEngine::new());
    acl.replace_grants(vec![Grant {
        id: 1,
        principal: Principal::User(USER),
        share: SHARE,
        subpath: SafePath::root(),
        allow: Perms::all(),
        deny: Perms::empty(),
        inherit: true,
        label: Some("root".to_string()),
    }]);
    let core = Core::new(meta, acl);
    core.register_share(ShareDef {
        id: SHARE,
        name: "root".to_string(),
        host_path: dir.path().to_path_buf(),
        policy,
        shared_externally: false,
    })
    .unwrap();
    (core, dir)
}

#[test]
fn roots_and_resolve() {
    let (core, _dir) = setup();
    let roots = core.roots(USER);
    assert_eq!(roots.len(), 1);
    assert_eq!(roots[0].label, "root");

    let resolved = core.resolve(USER, "/root/a/b").unwrap();
    assert_eq!(resolved.share, SHARE);
    assert_eq!(resolved.path.to_display_string(), "a/b");
}

#[test]
fn resolve_rejects_escape() {
    let (core, _dir) = setup();
    assert!(core.resolve(USER, "/root/../../etc/passwd").is_err());
    assert!(core.resolve(USER, "/root/a/../../b").is_err());
    assert!(core.resolve(USER, "/nosuchlabel/a").is_err());
}

#[test]
fn mkdir_list_stat_roundtrip() {
    let (core, _dir) = setup();
    let dir_entry = core.mkdir(USER, "/root/photos").unwrap();
    assert_eq!(dir_entry.name, "photos");
    assert_eq!(dir_entry.kind, sc_vfs::Kind::Dir);

    // Duplicate mkdir is a conflict.
    assert!(matches!(core.mkdir(USER, "/root/photos"), Err(crate::CoreError::Conflict)));

    core.write_text(USER, "/root/photos/a.txt", b"hello", None).unwrap();
    let listing = core.list(USER, "/root/photos", Sort::Name, Order::Asc).unwrap();
    assert_eq!(listing.total, 1);
    assert_eq!(listing.entries[0].name, "a.txt");
    assert_eq!(listing.entries[0].size, 5);

    let st = core.stat_entry(USER, "/root/photos/a.txt").unwrap();
    assert_eq!(st.size, 5);
}

/// `stat_entry` allocates a stable id on demand when the entry does not
/// already have one — the fix for `POST /api/fs/link` having nothing to
/// send for a file that was never separately written/aggregated/shared:
/// before this, `GET /api/fs/stat` on such a file returned `id: None`
/// forever, since `list`/`stat_entry` both went through the same
/// never-allocates lookup (`build_entry`).
#[test]
fn stat_entry_allocates_a_stable_id_for_an_untouched_file() {
    let (core, _dir) = setup();
    let written = core.write_text(USER, "/root/a.txt", b"hello", None).unwrap();
    // `write_text` itself never allocates — this is the pre-fix baseline,
    // asserted so a future change to `write_text` that starts allocating
    // doesn't make this test pass for the wrong reason.
    assert!(written.id.is_none(), "write_text must not itself allocate an id");

    let stat = core.stat_entry(USER, "/root/a.txt").unwrap();
    assert!(stat.id.is_some(), "stat_entry must allocate one when the entry has none yet");
}

/// Uniqueness and stability, not just presence: two different files must
/// get two different ids, and re-stating the same file must not mint a
/// second one or change the value already handed out.
#[test]
fn stat_entry_ids_are_distinct_and_stable_across_repeated_calls() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/a.txt", b"a", None).unwrap();
    core.write_text(USER, "/root/b.txt", b"b", None).unwrap();

    let a1 = core.stat_entry(USER, "/root/a.txt").unwrap().id.unwrap();
    let b1 = core.stat_entry(USER, "/root/b.txt").unwrap().id.unwrap();
    assert_ne!(a1, b1);

    let a2 = core.stat_entry(USER, "/root/a.txt").unwrap().id.unwrap();
    assert_eq!(a1, a2, "the same file must report the same id on a later stat");
}

/// `list()` must keep behaving exactly as documented ("lazy allocation"): a plain directory listing is the one thing that must
/// never allocate, because it can address an unbounded number of entries
/// merely by being browsed. The fix for `stat_entry` above must not have
/// leaked into `build_entry`, which both functions share.
#[test]
fn list_does_not_allocate_ids_for_entries_nobody_stat_ed_individually() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/a.txt", b"a", None).unwrap();
    core.write_text(USER, "/root/b.txt", b"b", None).unwrap();

    let listing = core.list(USER, "/root", Sort::Name, Order::Asc).unwrap();
    assert_eq!(listing.entries.len(), 2);
    assert!(
        listing.entries.iter().all(|e| e.id.is_none()),
        "a plain listing must not allocate ids for entries nobody has individually stat'd: {:?}",
        listing.entries.iter().map(|e| (&e.name, e.id)).collect::<Vec<_>>()
    );

    // Confirming the two are still distinguishable *after* they do get
    // stat'd individually — list() staying lazy must not somehow prevent
    // the per-entry allocation from working afterward.
    let a = core.stat_entry(USER, "/root/a.txt").unwrap().id.unwrap();
    let b = core.stat_entry(USER, "/root/b.txt").unwrap().id.unwrap();
    assert_ne!(a, b);
}

/// A share's own root has no real `node` row to allocate at all — no
/// parent to be named under. `stat_entry` must report `None` rather than
/// the internal chain sentinel, which is not a real id and would collide
/// across every share root if it ever reached a caller.
#[test]
fn stat_entry_on_a_share_root_does_not_leak_the_internal_sentinel() {
    let (core, _dir) = setup();
    let root_stat = core.stat_entry(USER, "/root").unwrap();
    assert!(root_stat.id.is_none(), "a share root's stat must not expose the allocator's internal sentinel id");
}

#[test]
fn rename_roundtrip() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/old.txt", b"data", None).unwrap();
    let renamed = core.rename(USER, "/root/old.txt", "new.txt", None).unwrap();
    assert_eq!(renamed.name, "new.txt");
    assert!(core.stat_entry(USER, "/root/old.txt").is_err());
    assert!(core.stat_entry(USER, "/root/new.txt").is_ok());
}

#[test]
fn if_match_mismatch_is_precondition() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"v1", None).unwrap();
    let result = core.write_text(USER, "/root/f.txt", b"v2", Some("bogus-etag"));
    match result {
        Err(crate::CoreError::Precondition { current_etag }) => assert!(!current_etag.is_empty()),
        other => panic!("expected Precondition, got {other:?}"),
    }

    // Correct etag succeeds.
    let cur = core.stat_entry(USER, "/root/f.txt").unwrap();
    core.write_text(USER, "/root/f.txt", b"v2", Some(&cur.etag)).unwrap();
    let (text, _) = core.read_text(USER, "/root/f.txt", 1024).unwrap();
    assert_eq!(text, "v2");
}

/// The staging name every copy/move/write in `ops.rs` renames from must
/// actually satisfy `is_reserved_name`. The previous `.{name}.scpart-{uuid}`
/// spelling did not (that test is a `starts_with` over `RESERVED_PREFIXES`),
/// so a process killed mid-copy left a partial file that listings, WebDAV,
/// SMB and the search walker all treated as an ordinary one.
#[test]
fn a_staging_file_name_is_reserved_so_a_killed_copy_leaves_nothing_visible() {
    let name = crate::ops::part_name();
    assert!(sc_vfs::is_reserved_name(&name), "{name} would show up in listings");
    // And user input still cannot mint one: only `join_control` may.
    assert!(SafePath::root().join(&name, 64).is_err());
}

#[test]
fn atomic_replace_leaves_no_temp_file_on_failure() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/target").unwrap();
    let root = core.share(SHARE).unwrap();

    // `read_dir_control`, not `read_dir`: the staging name is reserved
    // (`ops::part_name`) and `read_dir` hides reserved names, so the plain
    // listing would report "no temp file left behind" even when one was.
    let mut before: Vec<String> = root.read_dir_control(&SafePath::root()).unwrap().into_iter().map(|e| e.name.to_string()).collect();
    before.sort();

    // Writing to a path that is actually a directory forces the final
    // rename-over-target step to fail (can't rename a file over a dir),
    // exercising the temp-file cleanup path.
    let dir_entry = core.stat_entry(USER, "/root/target").unwrap();
    let result = core.write_text(USER, "/root/target", b"oops", Some(&dir_entry.etag));
    assert!(result.is_err());

    let mut after: Vec<String> = root.read_dir_control(&SafePath::root()).unwrap().into_iter().map(|e| e.name.to_string()).collect();
    after.sort();
    assert_eq!(before, after, "a temp .scpart- file was left behind after a failed atomic replace");
}

#[test]
fn move_and_copy_roundtrip() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/src").unwrap();
    core.mkdir(USER, "/root/dst").unwrap();
    core.write_text(USER, "/root/src/a.txt", b"content", None).unwrap();

    let copy_results = core
        .copy_entries(USER, &["/root/src/a.txt".to_string()], "/root/dst", OnConflict::Fail)
        .unwrap();
    assert!(copy_results[0].ok, "{:?}", copy_results[0].error);
    assert!(!copy_results[0].will_copy); // will_copy flags cross-device fallback, not "a copy happened"
    assert!(core.stat_entry(USER, "/root/dst/a.txt").is_ok());
    assert!(core.stat_entry(USER, "/root/src/a.txt").is_ok(), "copy must not remove the source");

    let move_results = core
        .move_entries(USER, &["/root/src/a.txt".to_string()], "/root/dst", OnConflict::Rename, &HashMap::new())
        .unwrap();
    assert!(move_results[0].ok, "{:?}", move_results[0].error);
    assert!(core.stat_entry(USER, "/root/src/a.txt").is_err(), "move must remove the source");
    // Conflict with the earlier copy resolved via Rename -> "a (2).txt".
    assert!(core.stat_entry(USER, "/root/dst/a (2).txt").is_ok());
}

/// A directory copy is staged under `.scpart-{uuid}` and published with one
/// rename, so it is never visible half-populated. Proven from the outside:
/// the tree arrives complete and the staging name is gone afterwards
/// (`read_dir_control`, since `read_dir` hides reserved names).
#[test]
fn copying_a_directory_stages_it_and_leaves_no_residue() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/tree").unwrap();
    core.mkdir(USER, "/root/tree/inner").unwrap();
    core.write_text(USER, "/root/tree/a.txt", b"a", None).unwrap();
    core.write_text(USER, "/root/tree/inner/b.txt", b"b", None).unwrap();
    core.mkdir(USER, "/root/dst").unwrap();

    let results = core
        .copy_entries(USER, &["/root/tree".to_string()], "/root/dst", OnConflict::Fail)
        .unwrap();
    assert!(results[0].ok, "{:?}", results[0].error);

    assert!(core.stat_entry(USER, "/root/dst/tree/a.txt").is_ok());
    assert!(core.stat_entry(USER, "/root/dst/tree/inner/b.txt").is_ok());

    let root = core.share(SHARE).unwrap();
    let dst = SafePath::root().join("dst", 32).unwrap();
    let leftovers: Vec<String> = root
        .read_dir_control(&dst)
        .unwrap()
        .into_iter()
        .map(|e| e.name.to_string())
        .filter(|n| n.starts_with(".scpart-"))
        .collect();
    assert!(leftovers.is_empty(), "staging directory left behind: {leftovers:?}");
}

/// Every staging site unlinks its temp entry on the error path, so residue
/// only exists when the process was killed outright. Nothing was reclaiming
/// it: `sc_upload`'s sweep only visits directories with an upload session
/// behind them. Simulated by planting a backdated `.scpart-` entry, since the
/// test cannot actually `SIGKILL` itself mid-copy.
#[test]
fn a_staging_entry_left_by_a_hard_kill_is_reclaimed() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/dst").unwrap();
    core.write_text(USER, "/root/a.txt", b"a", None).unwrap();

    let root = core.share(SHARE).unwrap();
    let dst = SafePath::root().join("dst", 32).unwrap();
    let orphan = dst.join_control(".scpart-deadbeef", 32).unwrap();
    let fresh = dst.join_control(".scpart-cafebabe", 32).unwrap();
    drop(root.create_excl(&orphan, 0o644).unwrap());
    drop(root.create_excl(&fresh, 0o644).unwrap());
    // A day and an hour back — past PART_ORPHAN_TTL. The second entry keeps
    // its real mtime and stands in for an operation running right now.
    let day_ago = crate::links::now_ns() - 25 * 3_600 * 1_000_000_000i128;
    root.set_times(&orphan, day_ago).unwrap();

    core.copy_entries(USER, &["/root/a.txt".to_string()], "/root/dst", OnConflict::Fail)
        .unwrap();

    assert!(root.stat(&orphan).is_err(), "an abandoned staging file was left on disk");
    assert!(root.stat(&fresh).is_ok(), "the sweep must not touch a staging file that could still be in use");
}

/// Staging must not turn an overwrite into a replace: copying onto a
/// directory that already exists merges into it, and a rename cannot replace
/// a non-empty directory. The pre-existing file has to survive.
#[test]
fn overwriting_an_existing_directory_still_merges() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/tree").unwrap();
    core.write_text(USER, "/root/tree/new.txt", b"new", None).unwrap();
    core.mkdir(USER, "/root/dst").unwrap();
    core.mkdir(USER, "/root/dst/tree").unwrap();
    core.write_text(USER, "/root/dst/tree/old.txt", b"old", None).unwrap();

    let results = core
        .copy_entries(USER, &["/root/tree".to_string()], "/root/dst", OnConflict::Overwrite)
        .unwrap();
    assert!(results[0].ok, "{:?}", results[0].error);
    assert!(core.stat_entry(USER, "/root/dst/tree/new.txt").is_ok());
    assert!(core.stat_entry(USER, "/root/dst/tree/old.txt").is_ok(), "a merge must not drop what was there");
}

#[test]
fn delete_permanent_removes_file() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/gone.txt", b"x", None).unwrap();
    let results = core.delete(USER, &["/root/gone.txt".to_string()], true).unwrap();
    assert!(results[0].ok);
    assert!(core.stat_entry(USER, "/root/gone.txt").is_err());
}

/// Trash is off by default (`SharePolicy::default()`), so an ordinary
/// (non-`permanent`) delete on a plain `setup()` share must unlink for real,
/// not move into `.sctrash` — the transition case this whole feature is
/// about: an admin who never touches the toggle gets no trash at all.
#[test]
fn delete_is_a_real_unlink_when_trash_is_off_by_default() {
    let (core, dir) = setup();
    core.write_text(USER, "/root/gone.txt", b"x", None).unwrap();
    let results = core.delete(USER, &["/root/gone.txt".to_string()], false).unwrap();
    assert!(results[0].ok);
    assert!(core.stat_entry(USER, "/root/gone.txt").is_err());
    assert!(core.trash_list(USER, SHARE).unwrap().is_empty(), "nothing was ever moved to trash");
    assert!(!dir.path().join(".sctrash").exists(), "off must not even create the trash directory");
}

#[test]
fn trash_delete_then_restore() {
    let (core, _dir) = setup_with_policy(SharePolicy { trash: TrashMode::ShareLocal, ..Default::default() });
    core.write_text(USER, "/root/keepme.txt", b"content", None).unwrap();

    let results = core.delete(USER, &["/root/keepme.txt".to_string()], false).unwrap();
    assert!(results[0].ok);
    assert!(core.stat_entry(USER, "/root/keepme.txt").is_err());

    let trashed = core.trash_list(USER, SHARE).unwrap();
    assert_eq!(trashed.len(), 1);
    assert_eq!(trashed[0].name, "keepme.txt");

    core.trash_restore(USER, SHARE, &trashed[0].id).unwrap();
    let restored = core.stat_entry(USER, "/root/keepme.txt").unwrap();
    assert_eq!(restored.size, 7);
    assert!(core.trash_list(USER, SHARE).unwrap().is_empty());
}

/// The trash listed a file's own mtime under "Deleted", which a move does
/// not touch: an old file deleted a moment ago was reported as deleted
/// however long ago it was last edited, and the real answer was written down
/// nowhere. `ctime` is what the rename into `.sctrash` does change.
///
/// Unix only: this asserts a property of the backends that can report an
/// inode change time. The Windows dev fallback has none and says so
/// (`Stat::ctime_ns == None`), which is why `trash_list` falls back to mtime
/// there rather than inventing a number.
#[cfg(unix)]
#[test]
fn trash_reports_when_it_was_deleted_not_when_the_file_was_last_edited() {
    use std::time::{Duration, SystemTime};

    let (core, dir) = setup_with_policy(SharePolicy { trash: TrashMode::ShareLocal, ..Default::default() });
    core.write_text(USER, "/root/old.txt", b"content", None).unwrap();

    // A file last written a year ago, deleted now.
    let a_year_ago = SystemTime::now() - Duration::from_secs(365 * 24 * 60 * 60);
    let f = std::fs::File::options().write(true).open(dir.path().join("old.txt")).unwrap();
    f.set_times(std::fs::FileTimes::new().set_modified(a_year_ago)).unwrap();
    drop(f);

    let before = SystemTime::now().duration_since(SystemTime::UNIX_EPOCH).unwrap().as_nanos() as i128;
    core.delete(USER, &["/root/old.txt".to_string()], false).unwrap();

    let trashed = core.trash_list(USER, SHARE).unwrap();
    assert_eq!(trashed.len(), 1);
    let a_year_ago_ns = a_year_ago.duration_since(SystemTime::UNIX_EPOCH).unwrap().as_nanos() as i128;
    assert!(
        trashed[0].deleted_at_ns >= before,
        "deleted_at_ns {} predates the delete call ({}); mtime was {}",
        trashed[0].deleted_at_ns,
        before,
        a_year_ago_ns
    );
}

/// Regression for the bug found by checking actual on-disk state: trashing
/// a file from a subdirectory and restoring it used to always land the file
/// at the share root (`.sctrash` only ever remembered the basename), so a
/// deep file silently reappeared in the wrong place. This fails on the
/// pre-fix code (`stat_entry(".../a/b/nested.txt")` errors after restore
/// because the file actually landed at `/root/nested.txt`) and passes now
/// that the trash entry carries the whole relative path.
#[test]
fn trash_restore_puts_a_nested_file_back_in_its_own_directory() {
    let (core, _dir) = setup_with_policy(SharePolicy { trash: TrashMode::ShareLocal, ..Default::default() });
    core.mkdir(USER, "/root/a").unwrap();
    core.mkdir(USER, "/root/a/b").unwrap();
    core.write_text(USER, "/root/a/b/nested.txt", b"deep content", None).unwrap();

    let results = core.delete(USER, &["/root/a/b/nested.txt".to_string()], false).unwrap();
    assert!(results[0].ok);

    let trashed = core.trash_list(USER, SHARE).unwrap();
    assert_eq!(trashed.len(), 1);
    // Display name is still just the leaf, not the whole encoded path.
    assert_eq!(trashed[0].name, "nested.txt");

    core.trash_restore(USER, SHARE, &trashed[0].id).unwrap();

    // Restored to its original nested location, not the share root.
    let restored = core.stat_entry(USER, "/root/a/b/nested.txt").unwrap();
    assert_eq!(restored.size, 12);
    assert!(core.stat_entry(USER, "/root/nested.txt").is_err(), "must not land at the share root");
}

/// The parent directory can itself be gone by the time of restore (deleted
/// independently, e.g. an empty dir removal, or never recreated after some
/// other cleanup) -- restore must recreate the ancestor chain rather than
/// erroring or dropping the file elsewhere.
#[test]
fn trash_restore_recreates_a_missing_parent_directory() {
    let (core, _dir) = setup_with_policy(SharePolicy { trash: TrashMode::ShareLocal, ..Default::default() });
    core.mkdir(USER, "/root/a").unwrap();
    core.write_text(USER, "/root/a/file.txt", b"x", None).unwrap();
    core.delete(USER, &["/root/a/file.txt".to_string()], false).unwrap();

    // The now-empty parent directory is itself removed before restore.
    core.delete(USER, &["/root/a".to_string()], true).unwrap();
    assert!(core.stat_entry(USER, "/root/a").is_err());

    let trashed = core.trash_list(USER, SHARE).unwrap();
    assert_eq!(trashed.len(), 1);
    core.trash_restore(USER, SHARE, &trashed[0].id).unwrap();

    let restored = core.stat_entry(USER, "/root/a/file.txt").unwrap();
    assert_eq!(restored.size, 1);
}

#[test]
fn aggregate_changes_on_deep_change_and_stable_otherwise() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/a").unwrap();
    core.mkdir(USER, "/root/a/b").unwrap();
    core.mkdir(USER, "/root/a/b/c").unwrap();
    core.write_text(USER, "/root/a/b/c/deep.txt", b"v1", None).unwrap();
    core.write_text(USER, "/root/sibling.txt", b"unrelated", None).unwrap();

    let root_path = SafePath::root();
    let agg1 = core.aggregate(SHARE, &root_path).unwrap();

    // Recomputing without any change must be perfectly stable.
    let agg1_again = core.aggregate(SHARE, &root_path).unwrap();
    assert_eq!(agg1.etag, agg1_again.etag);

    // Change a deeply nested descendant.
    let cur = core.stat_entry(USER, "/root/a/b/c/deep.txt").unwrap();
    core.write_text(USER, "/root/a/b/c/deep.txt", b"v2-longer-content", Some(&cur.etag))
        .unwrap();

    let agg2 = core.aggregate(SHARE, &root_path).unwrap();
    assert_ne!(agg1.etag, agg2.etag, "root aggregate ETag must change when a deep descendant changes");
    assert_ne!(agg1.rsize, agg2.rsize);

    // Re-marking dirty without any further real change must still converge
    // to the same, deterministic value.
    core.mark_dirty(SHARE, &SafePath::parse("a/b/c/deep.txt", 64).unwrap());
    let agg3 = core.aggregate(SHARE, &root_path).unwrap();
    assert_eq!(agg2.etag, agg3.etag, "aggregate ETag must be stable when nothing actually changed");
}

#[test]
fn listing_pagination_and_name_sort_avoids_full_stat() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/many").unwrap();
    for i in 0..250 {
        core.write_text(USER, &format!("/root/many/f{i:04}.txt"), b"x", None).unwrap();
    }

    let before = core.stat_call_count();
    let listing = core.list(USER, "/root/many", Sort::Name, Order::Asc).unwrap();
    let stats_used = core.stat_call_count() - before;

    assert_eq!(listing.total, 250);
    assert_eq!(listing.entries.len(), 200, "page should be capped, not the full 250");
    assert!(listing.cursor.is_some());
    assert_eq!(listing.entries[0].name, "f0000.txt");

    // One stat for the directory itself + one per returned page entry — the
    // 50 entries beyond the page must never be stat-ed for a name sort.
    assert_eq!(stats_used, 1 + 200, "name sort must only stat the returned page, not the whole directory");
}

// -------------------------------------------------------------- copy_to --

/// The failure this operation exists to prevent.
///
/// Faking a named copy as `copy_entries(&[src], parent_of(dst))` followed by
/// `rename(landed, dst)` looks correct until the destination is in the
/// source's own directory: the copy lands *on top of* the source, and the
/// rename then moves it away — leaving the original gone. That is exactly
/// what macOS Finder's "Duplicate" asks for, so it is not a corner case.
#[test]
fn copy_to_in_the_same_directory_keeps_the_source() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/photo.jpg", b"original bytes", None).unwrap();

    let copy = core
        .copy_to(USER, "/root/photo.jpg", "/root/photo copy.jpg", false)
        .unwrap();
    assert_eq!(copy.name, "photo copy.jpg");

    let (src, _) = core.read_text(USER, "/root/photo.jpg", 1 << 20).unwrap();
    let (dst, _) = core.read_text(USER, "/root/photo copy.jpg", 1 << 20).unwrap();
    assert_eq!(src, "original bytes", "the source must survive a same-directory copy");
    assert_eq!(dst, "original bytes");
}

#[test]
fn copy_to_names_its_destination_across_directories() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/dst").unwrap();
    core.write_text(USER, "/root/a.txt", b"hello", None).unwrap();

    core.copy_to(USER, "/root/a.txt", "/root/dst/renamed.txt", false).unwrap();
    let (text, _) = core.read_text(USER, "/root/dst/renamed.txt", 1 << 20).unwrap();
    assert_eq!(text, "hello");
    // ...and the source is untouched.
    assert!(core.stat_entry(USER, "/root/a.txt").is_ok());
}

#[test]
fn copy_to_refuses_an_existing_destination_unless_overwrite() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/a.txt", b"aaa", None).unwrap();
    core.write_text(USER, "/root/b.txt", b"bbb", None).unwrap();

    assert!(matches!(
        core.copy_to(USER, "/root/a.txt", "/root/b.txt", false),
        Err(crate::CoreError::Conflict)
    ));

    core.copy_to(USER, "/root/a.txt", "/root/b.txt", true).unwrap();
    let (text, _) = core.read_text(USER, "/root/b.txt", 1 << 20).unwrap();
    assert_eq!(text, "aaa");
}

#[test]
fn copy_to_copies_a_directory_tree() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/src").unwrap();
    core.mkdir(USER, "/root/src/inner").unwrap();
    core.write_text(USER, "/root/src/inner/f.txt", b"deep", None).unwrap();

    core.copy_to(USER, "/root/src", "/root/src copy", false).unwrap();
    let (text, _) = core.read_text(USER, "/root/src copy/inner/f.txt", 1 << 20).unwrap();
    assert_eq!(text, "deep");
    // The original tree is still intact.
    assert!(core.stat_entry(USER, "/root/src/inner/f.txt").is_ok());
}

/// Copying a directory into itself would recurse until the depth limit or the
/// disk gave out; refuse it up front rather than part-way through.
#[test]
fn copy_to_refuses_a_destination_inside_the_source() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/d").unwrap();
    assert!(core.copy_to(USER, "/root/d", "/root/d/nested", false).is_err());
    assert!(core.copy_to(USER, "/root/d", "/root/d", false).is_err());
}

/// `move_to` is `copy_to`'s mirror and exists for the same reason: WebDAV
/// MOVE names its destination, which `move_entries` cannot express.
#[test]
fn move_to_names_its_destination() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/dst").unwrap();
    core.write_text(USER, "/root/a.txt", b"payload", None).unwrap();

    core.move_to(USER, "/root/a.txt", "/root/dst/b.txt", false).unwrap();
    let (text, _) = core.read_text(USER, "/root/dst/b.txt", 1 << 20).unwrap();
    assert_eq!(text, "payload");
    assert!(core.stat_entry(USER, "/root/a.txt").is_err());
}
