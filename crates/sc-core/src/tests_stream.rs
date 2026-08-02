//! Tests for `stream.rs` (`Core::open_stream`/`open_stream_by_fid`) and
//! `archive.rs` (`Core::archive_walk`).

use std::io::Read;
use std::sync::Arc;

use sc_acl::{AclEngine, Grant, Perms, Principal};
use sc_meta::MetaStore;
use sc_vfs::{SafePath, ShareId, SharePolicy, UserId};

use crate::share::ShareDef;
use crate::Core;

const USER: UserId = UserId::new(1);
const OTHER_USER: UserId = UserId::new(2);
const SHARE: ShareId = ShareId::new(1);

fn setup() -> (Core, tempfile::TempDir) {
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
        policy: SharePolicy::default(),
        shared_externally: false,
    })
    .unwrap();
    (core, dir)
}

fn read_all(mut r: impl Read) -> Vec<u8> {
    let mut out = Vec::new();
    r.read_to_end(&mut out).unwrap();
    out
}

#[test]
fn open_stream_reads_whole_file() {
    let (core, _dir) = setup();
    let payload = vec![7u8; 3 * crate::CHUNK + 123]; // spans several 256 KiB chunks
    core.write_text(USER, "/root/big.bin", &payload, None).unwrap();

    let (meta, stream) = core.open_stream(USER, "/root/big.bin", None).unwrap();
    assert_eq!(meta.size, payload.len() as u64);
    assert_eq!(meta.name, "big.bin");
    let got = read_all(stream);
    assert_eq!(got, payload);
}

#[test]
fn open_stream_never_reads_more_than_chunk_at_once() {
    let (core, _dir) = setup();
    let payload = vec![9u8; crate::CHUNK * 2 + 500];
    core.write_text(USER, "/root/big.bin", &payload, None).unwrap();

    let (_meta, mut stream) = core.open_stream(USER, "/root/big.bin", None).unwrap();
    // A read into a huge buffer must still be satisfied in <= CHUNK-sized
    // pulls from the underlying file, which is what keeps memory use
    // independent of file size — assert it directly rather than trusting
    // the doc comment.
    let mut oversized_buf = vec![0u8; crate::CHUNK * 10];
    let n = std::io::Read::read(&mut stream, &mut oversized_buf).unwrap();
    assert!(n <= crate::CHUNK, "single read() returned {n} bytes, expected <= CHUNK");
}

#[test]
fn open_stream_honours_inclusive_range() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"0123456789", None).unwrap();

    // bytes=2-5 -> "2345"
    let (_meta, stream) = core.open_stream(USER, "/root/f.txt", Some((2, 5))).unwrap();
    assert_eq!(read_all(stream), b"2345");
}

#[test]
fn open_stream_range_is_clamped_to_file_size() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"01234", None).unwrap();

    let (_meta, stream) = core.open_stream(USER, "/root/f.txt", Some((3, 1_000))).unwrap();
    assert_eq!(read_all(stream), b"34");
}

#[test]
fn open_stream_denies_unauthorized_user() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"secret", None).unwrap();
    assert!(core.open_stream(OTHER_USER, "/root/f.txt", None).is_err());
}

/// Test-only helper: allocate a fileid for `vpath` the same way the real
/// callers that need a stable id do (`ensure_fileid_chain`), since these
/// tests exercise the `FileId`-keyed entry points directly.
fn fid_for(core: &Core, user: UserId, vpath: &str) -> sc_vfs::FileId {
    let r = core.resolve(user, vpath).unwrap();
    core.ensure_fileid_chain(&r.root, r.share, &r.path).unwrap()
}

#[test]
fn open_stream_by_fid_round_trips_through_meta() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"hello world", None).unwrap();
    let fid = fid_for(&core, USER, "/root/f.txt");

    let (meta, stream) = core.open_stream_by_fid(fid, None).unwrap();
    assert_eq!(meta.name, "f.txt");
    assert_eq!(read_all(stream), b"hello world");

    let stat = core.stat_by_fid(fid).unwrap();
    assert_eq!(stat.size, 11);
}

#[test]
fn check_read_by_fid_reflects_acl() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"hello", None).unwrap();
    let fid = fid_for(&core, USER, "/root/f.txt");

    assert!(core.check_read_by_fid(USER, fid).is_ok());
    assert!(core.check_read_by_fid(OTHER_USER, fid).is_err());
}

#[test]
fn stat_by_fid_reflects_current_size_not_a_stale_snapshot() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/f.txt", b"aaa", None).unwrap();
    let fid = fid_for(&core, USER, "/root/f.txt");
    let first = core.stat_by_fid(fid).unwrap();
    assert_eq!(first.size, 3);

    // Atomic replace (temp + rename) keeps the same fileid identity because
    // `write_text` renames the new content over the old path.
    core.write_text(USER, "/root/f.txt", b"aaaaaaaaaa", Some(&first.etag)).unwrap();
    let second = core.stat_by_fid(fid).unwrap();
    assert_eq!(second.size, 10);
}

#[test]
fn archive_walk_over_a_directory_visits_every_descendant() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/photos").unwrap();
    core.write_text(USER, "/root/photos/a.txt", b"aaa", None).unwrap();
    core.write_text(USER, "/root/photos/b.txt", b"bbbbb", None).unwrap();
    core.mkdir(USER, "/root/photos/sub").unwrap();
    core.write_text(USER, "/root/photos/sub/c.txt", b"cc", None).unwrap();

    let mut seen: Vec<(String, bool, bool)> = Vec::new();
    let mut total_bytes: Vec<u8> = Vec::new();
    core.archive_walk(USER, "/root/photos", &mut |entry, stream| {
        seen.push((entry.rel_path.clone(), entry.is_dir, entry.readable));
        if let Some(s) = stream {
            let mut buf = Vec::new();
            s.read_to_end(&mut buf).unwrap();
            total_bytes.extend(buf);
        }
    })
    .unwrap();

    seen.sort();
    assert_eq!(
        seen,
        vec![
            ("photos/a.txt".to_string(), false, true),
            ("photos/b.txt".to_string(), false, true),
            ("photos/sub".to_string(), true, true),
            ("photos/sub/c.txt".to_string(), false, true),
        ]
    );
    assert_eq!(total_bytes.len(), 3 + 5 + 2);
}

#[test]
fn archive_walk_over_a_single_file_yields_one_entry() {
    let (core, _dir) = setup();
    core.write_text(USER, "/root/solo.txt", b"just one file", None).unwrap();

    let mut seen = 0;
    core.archive_walk(USER, "/root/solo.txt", &mut |entry, stream| {
        seen += 1;
        assert_eq!(entry.rel_path, "solo.txt");
        assert!(!entry.is_dir);
        assert!(entry.readable);
        assert!(stream.is_some());
    })
    .unwrap();
    assert_eq!(seen, 1);
}

#[test]
fn archive_walk_root_denied_is_an_error_but_never_enters_subtree() {
    let (core, _dir) = setup();
    core.mkdir(USER, "/root/private").unwrap();
    core.write_text(USER, "/root/private/secret.txt", b"nope", None).unwrap();

    assert!(core.archive_walk(OTHER_USER, "/root/private", &mut |_, _| {
        panic!("must never visit anything under a root the caller cannot read");
    })
    .is_err());
}
