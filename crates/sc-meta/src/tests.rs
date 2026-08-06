use sc_vfs::{FileId, Kind, ShareId, Stat};

use crate::{Aggregate, MetaStore};

fn stat(dev: u64, ino: u64, size: u64, mtime_ns: i64, btime_ns: Option<i64>, kind: Kind) -> Stat {
    Stat {
        dev,
        ino,
        btime_ns: btime_ns.map(|b| b as i128),
        mtime_ns: mtime_ns as i128,
        ctime_ns: Some(mtime_ns as i128),
        size,
        mode: 0o644,
        uid: 0,
        gid: 0,
        nlink: 1,
        kind,
    }
}

fn node_count(store: &MetaStore) -> i64 {
    store
        .conn()
        .unwrap()
        .query_row("SELECT COUNT(*) FROM node", [], |r| r.get(0))
        .unwrap()
}

#[test]
fn pragma_auto_vacuum_is_incremental() {
    let store = MetaStore::open_in_memory().unwrap();
    let av: i64 = store
        .conn()
        .unwrap()
        .query_row("PRAGMA auto_vacuum", [], |r| r.get(0))
        .unwrap();
    assert_eq!(av, 2, "auto_vacuum must be INCREMENTAL (2)");
}

#[test]
fn pragma_auto_vacuum_is_incremental_file_backed() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("meta.db");
    let store = MetaStore::open(&path).unwrap();
    let av: i64 = store
        .conn()
        .unwrap()
        .query_row("PRAGMA auto_vacuum", [], |r| r.get(0))
        .unwrap();
    assert_eq!(av, 2);

    // Re-opening an existing store must not choke on re-applying pragmas.
    drop(store);
    let store2 = MetaStore::open(&path).unwrap();
    let av2: i64 = store2
        .conn()
        .unwrap()
        .query_row("PRAGMA auto_vacuum", [], |r| r.get(0))
        .unwrap();
    assert_eq!(av2, 2);
}

#[test]
fn no_rows_until_fileid_is_actually_requested() {
    let store = MetaStore::open_in_memory().unwrap();
    assert_eq!(node_count(&store), 0, "web-UI-only usage must allocate zero rows");

    let share = ShareId::new(1);
    let st = stat(1, 100, 4096, 1_000, Some(1), Kind::File);
    assert!(store.lookup_fileid(share, &st).unwrap().is_none());
    assert_eq!(node_count(&store), 0, "a pure lookup must never allocate");
}

#[test]
fn lazy_fileid_allocation_is_idempotent() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(1);
    let st = stat(1, 100, 4096, 1_000, Some(1), Kind::File);

    let id1 = store.fileid(share, FileId::new(0), "a.txt", &st, false).unwrap();
    assert_eq!(node_count(&store), 1);

    let id2 = store.fileid(share, FileId::new(0), "a.txt", &st, false).unwrap();
    assert_eq!(id1, id2, "same physical file must return the same fileid");
    assert_eq!(node_count(&store), 1, "second call must not insert a new row");

    let found = store.lookup_fileid(share, &st).unwrap();
    assert_eq!(found, Some(id1));
}

#[test]
fn fileid_lazy_revalidation_follows_external_rename() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(1);
    let st = stat(1, 200, 10, 1_000, Some(1), Kind::File);

    let root = FileId::new(0);
    let id = store.fileid(share, root, "old.txt", &st, false).unwrap();
    let (_, path) = store.resolve_path(id).unwrap().unwrap();
    assert_eq!(path, "old.txt");

    // Some other process renamed the file on disk; the caller re-observes
    // the same (dev, ino) under a new name and calls fileid() again.
    let id_again = store.fileid(share, root, "new.txt", &st, false).unwrap();
    assert_eq!(id, id_again);
    let (_, path2) = store.resolve_path(id).unwrap().unwrap();
    assert_eq!(path2, "new.txt");
}

#[test]
fn resolve_path_walks_parent_chain() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(7);
    let root = FileId::new(0);

    let dir_stat = stat(1, 10, 0, 1, Some(1), Kind::Dir);
    let a = store.fileid(share, root, "a", &dir_stat, true).unwrap();

    let dir_stat2 = stat(1, 11, 0, 1, Some(1), Kind::Dir);
    let b = store.fileid(share, a, "b", &dir_stat2, true).unwrap();

    let file_stat = stat(1, 12, 42, 1, Some(1), Kind::File);
    let c = store.fileid(share, b, "c.txt", &file_stat, false).unwrap();

    let (resolved_share, path) = store.resolve_path(c).unwrap().unwrap();
    assert_eq!(resolved_share, share);
    assert_eq!(path, "a/b/c.txt");

    let (_, path_a) = store.resolve_path(a).unwrap().unwrap();
    assert_eq!(path_a, "a");

    assert!(store.resolve_path(FileId::new(999_999)).unwrap().is_none());
}

#[test]
fn directory_rename_is_single_row_update_and_descendants_still_resolve() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(1);
    let root = FileId::new(0);

    let dir_stat = stat(1, 20, 0, 1, Some(1), Kind::Dir);
    let photos = store.fileid(share, root, "photos", &dir_stat, true).unwrap();

    let sub_stat = stat(1, 21, 0, 1, Some(1), Kind::Dir);
    let vacation = store
        .fileid(share, photos, "vacation", &sub_stat, true)
        .unwrap();

    let mut leaves = Vec::new();
    for i in 0..25u64 {
        let fstat = stat(1, 100 + i, 10, 1, Some(1), Kind::File);
        let name = format!("img{i}.jpg");
        leaves.push(store.fileid(share, vacation, &name, &fstat, false).unwrap());
    }

    // Snapshot every descendant row (id -> (parent, name)) before rename.
    let before: Vec<(i64, i64, String)> = {
        let conn = store.conn().unwrap();
        let mut stmt = conn
            .prepare("SELECT id, parent, name FROM node WHERE id != ?1 ORDER BY id")
            .unwrap();
        stmt.query_map([photos.get()], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))
            .unwrap()
            .collect::<Result<_, _>>()
            .unwrap()
    };

    // Rename "photos" -> "pictures" (still under the same parent, the share root).
    store.rename_node(photos, root, "pictures").unwrap();

    let after: Vec<(i64, i64, String)> = {
        let conn = store.conn().unwrap();
        let mut stmt = conn
            .prepare("SELECT id, parent, name FROM node WHERE id != ?1 ORDER BY id")
            .unwrap();
        stmt.query_map([photos.get()], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))
            .unwrap()
            .collect::<Result<_, _>>()
            .unwrap()
    };

    assert_eq!(
        before, after,
        "renaming a directory must not touch any descendant row"
    );

    let (_, path_top) = store.resolve_path(photos).unwrap().unwrap();
    assert_eq!(path_top, "pictures");

    let (_, path_sub) = store.resolve_path(vacation).unwrap().unwrap();
    assert_eq!(path_sub, "pictures/vacation");

    for (i, leaf) in leaves.iter().enumerate() {
        let (_, path) = store.resolve_path(*leaf).unwrap().unwrap();
        assert_eq!(path, format!("pictures/vacation/img{i}.jpg"));
    }
}

#[test]
fn dirty_chain_marking_and_gen_bump_invalidate_dir_etag() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(3);
    let dir_id = FileId::new(42);

    assert!(store.dir_etag(share, dir_id).unwrap().is_none());

    let gen0 = store.share_gen(share).unwrap();
    assert_eq!(gen0, 0);

    let agg = Aggregate {
        etag: "deadbeef".into(),
        rsize: 123,
        rcount: 4,
    };
    store.put_dir_etag(share, dir_id, &agg, gen0).unwrap();

    let got = store.dir_etag(share, dir_id).unwrap().expect("should be cached");
    assert_eq!(got.etag, "deadbeef");
    assert_eq!(got.rsize, 123);
    assert_eq!(got.rcount, 4);

    // Dirty-mark: must read back as invalid until recomputed.
    store.mark_dirty_chain(share, &[dir_id]).unwrap();
    assert!(store.dir_etag(share, dir_id).unwrap().is_none());

    // Recompute and store again at the (still current) generation.
    store.put_dir_etag(share, dir_id, &agg, gen0).unwrap();
    assert!(store.dir_etag(share, dir_id).unwrap().is_some());

    // Bumping the share generation invalidates it again, in O(1), without
    // touching the row's `valid` bit.
    let gen1 = store.bump_share_gen(share).unwrap();
    assert_eq!(gen1, gen0 + 1);
    assert!(
        store.dir_etag(share, dir_id).unwrap().is_none(),
        "stale generation must read as invalid"
    );

    // Recomputing against the new generation makes it valid again.
    store.put_dir_etag(share, dir_id, &agg, gen1).unwrap();
    assert!(store.dir_etag(share, dir_id).unwrap().is_some());

    // A different share's generation is independent.
    let other_share = ShareId::new(4);
    assert_eq!(store.share_gen(other_share).unwrap(), 0);
}

#[test]
fn file_etag_is_deterministic_and_sensitive_to_identity() {
    let a = stat(1, 2, 3, 4, Some(5), Kind::File);
    let b = stat(1, 2, 3, 4, Some(5), Kind::File);
    assert_eq!(MetaStore::file_etag(&a), MetaStore::file_etag(&b));

    let c = stat(1, 2, 3, 5, Some(5), Kind::File); // different mtime
    assert_ne!(MetaStore::file_etag(&a), MetaStore::file_etag(&c));

    let d = stat(1, 9, 3, 4, Some(5), Kind::File); // different ino
    assert_ne!(MetaStore::file_etag(&a), MetaStore::file_etag(&d));
}

#[test]
fn gc_removes_only_dead_unpinned_nodes() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(1);
    let root = FileId::new(0);

    let alive_stat = stat(1, 1, 1, 1, Some(1), Kind::File);
    let alive_id = store.fileid(share, root, "alive.txt", &alive_stat, false).unwrap();

    let dead_stat = stat(1, 2, 1, 1, Some(1), Kind::File);
    let dead_id = store.fileid(share, root, "dead.txt", &dead_stat, false).unwrap();

    let pinned_but_dead_stat = stat(1, 3, 1, 1, Some(1), Kind::File);
    let pinned_id = store
        .fileid(share, root, "pinned.txt", &pinned_but_dead_stat, false)
        .unwrap();
    store.set_prop(pinned_id, "DAV:", "custom", "keep-me").unwrap();

    assert_eq!(node_count(&store), 3);

    // Only (dev=1, ino=1) — the "alive" file — reports as alive.
    let removed = store
        .gc_dead_nodes(share, &|dev, ino| dev == 1 && ino == 1)
        .unwrap();

    assert_eq!(removed, 1, "only the unpinned dead row should be removed");
    assert_eq!(node_count(&store), 2);

    assert!(store.resolve_path(alive_id).unwrap().is_some());
    assert!(store.resolve_path(dead_id).unwrap().is_none());
    assert!(
        store.resolve_path(pinned_id).unwrap().is_some(),
        "pinned rows survive GC even when the underlying file looks gone"
    );

    // Its dead property is untouched too.
    let props = store.get_props(pinned_id).unwrap();
    assert_eq!(props.len(), 1);
    assert_eq!(props[0].value, "keep-me");
}

#[test]
fn dav_props_roundtrip() {
    let store = MetaStore::open_in_memory().unwrap();
    // Insert a node row so the UPDATE in set_prop has a target (not
    // strictly required for props storage itself, but realistic usage).
    let share = ShareId::new(1);
    let st = stat(1, 1, 1, 1, Some(1), Kind::File);
    let id = store.fileid(share, FileId::new(0), "f.txt", &st, false).unwrap();

    store.set_prop(id, "DAV:", "displayname", "Hello").unwrap();
    store.set_prop(id, "custom:", "color", "red").unwrap();
    let props = store.get_props(id).unwrap();
    assert_eq!(props.len(), 2);

    store.set_prop(id, "DAV:", "displayname", "World").unwrap();
    let props = store.get_props(id).unwrap();
    assert_eq!(props.len(), 2, "same (ns, name) must overwrite, not duplicate");
    let dn = props.iter().find(|p| p.name == "displayname").unwrap();
    assert_eq!(dn.value, "World");

    store.del_prop(id, "custom:", "color").unwrap();
    let props = store.get_props(id).unwrap();
    assert_eq!(props.len(), 1);
}

#[test]
fn size_bytes_and_incremental_vacuum_do_not_error() {
    let store = MetaStore::open_in_memory().unwrap();
    let size = store.size_bytes().unwrap();
    assert!(size > 0);
    store.incremental_vacuum(16).unwrap();
}

#[test]
fn the_free_space_floor_stops_growth_and_nothing_else() {
    let store = MetaStore::open_in_memory().unwrap();
    let share = ShareId::new(1);
    let st = stat(1, 1, 10, 5, Some(1), Kind::File);
    let known = store.fileid(share, FileId::new(0), "a.txt", &st, false).unwrap();
    store.set_prop(known, "DAV:", "displayname", "A").unwrap();

    store.set_writes_blocked(true);

    // Growth stops: a file that has never been seen cannot take a new row.
    let fresh = stat(1, 2, 10, 5, Some(1), Kind::File);
    let err = store
        .fileid(share, FileId::new(0), "b.txt", &fresh, false)
        .unwrap_err()
        .to_string();
    assert!(err.contains("db.min_free_bytes"), "unhelpful error: {err}");
    assert_eq!(node_count(&store), 1);
    assert!(store.set_prop(known, "custom:", "c", "red").is_err());

    // Everything that does not grow the file keeps working — the point of
    // §4.4 is that the service stays up while the floor holds.
    assert_eq!(
        store.fileid(share, FileId::new(0), "a.txt", &st, false).unwrap(),
        known,
        "an id that already exists must still resolve"
    );
    assert_eq!(store.lookup_fileid(share, &st).unwrap(), Some(known));
    assert_eq!(store.get_props(known).unwrap().len(), 1);
    store.rename_node(known, FileId::new(0), "a2.txt").unwrap();
    let agg = Aggregate {
        etag: "etag".into(),
        rsize: 10,
        rcount: 1,
    };
    store.put_dir_etag(share, FileId::new(0), &agg, 1).unwrap();
    store.del_prop(known, "DAV:", "displayname").unwrap();
    store.gc_dead_nodes(share, &|_, _| true).unwrap();

    // And it is reversible: space freed up means writes resume.
    store.set_writes_blocked(false);
    store.fileid(share, FileId::new(0), "b.txt", &fresh, false).unwrap();
    assert_eq!(node_count(&store), 2);
}
