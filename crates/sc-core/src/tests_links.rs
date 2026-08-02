//! Share-link tests — one per non-negotiable property in
//! / `links.rs`'s module doc.

use std::sync::Arc;

use sc_acl::{AclEngine, Grant, Perms, Principal};
use sc_meta::MetaStore;
use sc_vfs::{FileId, SafePath, ShareId, SharePolicy, UserId};

use crate::links::{LinkPatch, LinkSpec, LinkStore};
use crate::share::ShareDef;
use crate::{Core, CoreError};

/// Full rights, including `SHARE`.
const OWNER: UserId = UserId::new(1);
/// Read-only: may look, may not mint links.
const READER: UserId = UserId::new(2);
const SHARE: ShareId = ShareId::new(1);

fn grants() -> Vec<Grant> {
    vec![
        Grant {
            id: 1,
            principal: Principal::User(OWNER),
            share: SHARE,
            subpath: SafePath::root(),
            allow: Perms::all(),
            deny: Perms::empty(),
            inherit: true,
            label: Some("root".to_string()),
        },
        Grant {
            id: 2,
            principal: Principal::User(READER),
            share: SHARE,
            subpath: SafePath::root(),
            allow: Perms::READ | Perms::DOWNLOAD,
            deny: Perms::empty(),
            inherit: true,
            label: Some("root".to_string()),
        },
    ]
}

/// A `Core` with an in-memory link store attached.
fn setup() -> (Arc<Core>, tempfile::TempDir) {
    let (core, dir) = setup_without_links();
    core.attach_links(LinkStore::open_in_memory().unwrap()).unwrap();
    (core, dir)
}

fn setup_without_links() -> (Arc<Core>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(AclEngine::new());
    acl.replace_grants(grants());
    let core = Arc::new(Core::new(meta, acl));
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

/// A `Core` whose link store is a real file, so a test can go behind the API
/// and inspect what actually landed on disk.
fn setup_on_disk() -> (Arc<Core>, tempfile::TempDir, std::path::PathBuf) {
    let (core, dir) = setup_without_links();
    let db = dir.path().join("links.db");
    core.attach_links(LinkStore::open(&db).unwrap()).unwrap();
    (core, dir, db)
}

fn seed_file(core: &Core, vpath: &str) {
    core.write_text(OWNER, vpath, b"contents", None).unwrap();
}

/// Every byte the link database wrote, main file plus WAL/journal sidecars.
fn db_bytes(db: &std::path::Path) -> Vec<u8> {
    let mut out = Vec::new();
    for suffix in ["", "-wal", "-journal", "-shm"] {
        let p = db.with_file_name(format!("{}{suffix}", db.file_name().unwrap().to_string_lossy()));
        if let Ok(b) = std::fs::read(&p) {
            out.extend_from_slice(&b);
        }
    }
    out
}

// ------------------------------------------------------------------ tokens --

#[test]
fn token_is_128_bits_of_base64url_and_only_its_hash_is_persisted() {
    let (core, _dir, db) = setup_on_disk();
    seed_file(&core, "/root/a.txt");
    let (link, token) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();

    // 128 bits, base64url-no-pad => 22 characters, alphabet only.
    assert_eq!(token.len(), 22, "expected 22 base64url chars for 16 random bytes");
    assert!(token.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));

    // The token round-trips through the hash lookup...
    let found = core.resolve_link(&token).unwrap().expect("token resolves");
    assert_eq!(found.id, link.id);
    // ...but the plaintext is nowhere in the database. This is the whole
    // point of storing `sha256(token)`: a dump of this file yields no usable
    // link. The digest, by contrast, must be present.
    let bytes = db_bytes(&db);
    assert!(
        !bytes.windows(token.len()).any(|w| w == token.as_bytes()),
        "plaintext token found in the share-link database"
    );
    let digest = crate::links::token_hash(&token);
    assert!(
        bytes.windows(32).any(|w| w == digest),
        "sha256(token) should be what was written"
    );
}

#[test]
fn each_link_gets_a_distinct_token() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let mut seen = std::collections::HashSet::new();
    for _ in 0..16 {
        let (_, t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
        assert!(seen.insert(t), "CSPRNG produced a duplicate token");
    }
}

#[test]
fn a_wrong_token_resolves_to_nothing() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (_, token) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    let mut wrong = token.clone();
    wrong.replace_range(0..1, if token.starts_with('A') { "B" } else { "A" });
    assert!(core.resolve_link(&wrong).unwrap().is_none());
}

// --------------------------------------------------------------- passwords --

#[test]
fn password_is_stored_as_an_argon2_hash_never_as_plaintext() {
    let (core, _dir, db) = setup_on_disk();
    seed_file(&core, "/root/a.txt");
    let spec = LinkSpec {
        password: Some("correct horse battery".into()),
        ..LinkSpec::default()
    };
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &spec).unwrap();
    assert!(link.has_password);

    let bytes = db_bytes(&db);
    assert!(
        !bytes.windows(21).any(|w| w == b"correct horse battery"),
        "the plaintext password reached the database"
    );
    // `sc-auth`'s parameters, not a locally invented set — Argon2id is what
    // that crate configures, and reusing it is the point of the dependency.
    assert!(
        bytes.windows(10).any(|w| w == b"$argon2id$"),
        "expected an Argon2id PHC string in the database"
    );

    assert!(core.check_link_password(link.id, "correct horse battery").unwrap());
    assert!(!core.check_link_password(link.id, "correct horse batterz").unwrap());
}

#[test]
fn a_link_with_no_password_accepts_and_a_missing_link_refuses() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();

    assert!(core.check_link_password(link.id, "anything").unwrap());
    // A link id that does not exist answers `false` — and, inside, still runs
    // a real Argon2 verify against the dummy hash so the timing matches the
    // wrong-password path.
    assert!(!core.check_link_password(999_999, "anything").unwrap());
}

#[test]
fn a_password_can_be_set_and_cleared_through_a_patch() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    assert!(!link.has_password);

    let set = core
        .update_link(OWNER, link.id, &LinkPatch { password: Some(Some("s3cret-pass".into())), ..Default::default() })
        .unwrap();
    assert!(set.has_password);
    assert!(core.check_link_password(link.id, "s3cret-pass").unwrap());

    // `Some(None)` is "explicitly clear", distinct from the absent `None`.
    let cleared = core
        .update_link(OWNER, link.id, &LinkPatch { password: Some(None), ..Default::default() })
        .unwrap();
    assert!(!cleared.has_password);
}

// ------------------------------------------------- path + fileid agreement --

#[test]
fn a_live_link_resolves_to_its_target() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    let entry = core.link_target(&link).unwrap();
    assert_eq!(entry.name, "a.txt");
    assert_eq!(entry.size, 8);
}

#[test]
fn a_link_to_the_share_root_itself_survives_its_own_first_read() {
    // A link whose target is the share root (not any file/dir beneath it)
    // resolves to `SafePath::root()`, and `ensure_fileid_chain` answers that
    // with the `FileId(0)` sentinel -- a value `aggregate.rs` deliberately
    // never inserts a `node` row for, because the root has no parent to be
    // named under. `lookup_fileid` therefore can never find a match for it,
    // which used to make `link_target`'s cross-check treat every share-root
    // link as "swapped" on the very next read, no matter how fresh: `Some(0)`
    // (what creation stored) against `None` (what the lookup returns) always
    // disagreed.
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root", &LinkSpec::default()).unwrap();
    let entry = core.link_target(&link).expect("a share-root link must not read back as Gone");
    assert!(entry.kind == sc_vfs::Kind::Dir);
}

#[test]
fn a_fileid_that_no_longer_matches_the_path_is_gone_not_served() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (mut link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    assert!(link.fileid_at_creation.is_some(), "a fileid should have been allocated");

    // Simulate "a different file now occupies this name": the path still
    // resolves and still holds a readable file, but its identity is not the
    // one the link was minted against. Path-only matching would happily serve
    // this; §7.1 says it must be `410 Gone`.
    link.fileid_at_creation = Some(FileId::new(link.fileid_at_creation.unwrap().get() + 4242));
    assert!(matches!(core.link_target(&link), Err(CoreError::Gone)));
}

#[test]
fn a_target_that_moved_away_is_gone() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    core.delete(OWNER, &["/root/a.txt".to_string()], true).unwrap();
    assert!(matches!(core.link_target(&link), Err(CoreError::Gone)));
}

// ------------------------------------------------------ expiry / cap / acl --

#[test]
fn an_expired_link_is_gone_and_a_past_expiry_is_refused_up_front() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");

    let past = LinkSpec { expires_ns: Some(crate::links::now_ns() - 1), ..LinkSpec::default() };
    assert!(core.create_link(OWNER, "/root/a.txt", &past).is_err());

    let soon = LinkSpec {
        expires_ns: Some(crate::links::now_ns() + 60_000_000), // +60ms
        ..LinkSpec::default()
    };
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &soon).unwrap();
    assert!(core.link_target(&link).is_ok());
    std::thread::sleep(std::time::Duration::from_millis(150));
    let refetched = core.get_link(OWNER, link.id).unwrap();
    assert!(matches!(core.link_target(&refetched), Err(CoreError::Gone)));
}

#[test]
fn max_downloads_counts_up_and_then_refuses() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let spec = LinkSpec { max_downloads: Some(2), ..LinkSpec::default() };
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &spec).unwrap();

    core.note_link_download(link.id).unwrap();
    core.note_link_download(link.id).unwrap();
    assert!(matches!(core.note_link_download(link.id), Err(CoreError::Gone)));

    let after = core.get_link(OWNER, link.id).unwrap();
    assert_eq!(after.downloads, 2, "a refused attempt must not inflate the counter");
    assert!(after.is_exhausted());
    assert!(matches!(core.link_target(&after), Err(CoreError::Gone)));
}

#[test]
fn concurrent_downloads_cannot_exceed_the_cap() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let spec = LinkSpec { max_downloads: Some(3), ..LinkSpec::default() };
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &spec).unwrap();

    // Read-then-write would let all eight observe `downloads < max`. The
    // conditional UPDATE is what makes exactly three win.
    let ok = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let mut handles = Vec::new();
    for _ in 0..8 {
        let core = core.clone();
        let ok = ok.clone();
        let id = link.id;
        handles.push(std::thread::spawn(move || {
            if core.note_link_download(id).is_ok() {
                ok.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            }
        }));
    }
    for h in handles {
        h.join().unwrap();
    }
    assert_eq!(ok.load(std::sync::atomic::Ordering::SeqCst), 3);
    assert_eq!(core.get_link(OWNER, link.id).unwrap().downloads, 3);
}

#[test]
fn creating_a_link_requires_the_share_permission() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    // READER can read the file but holds no SHARE bit.
    assert!(matches!(
        core.create_link(READER, "/root/a.txt", &LinkSpec::default()),
        Err(CoreError::Denied { .. })
    ));
}

#[test]
fn a_link_cannot_grant_more_than_its_creator_holds() {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(AclEngine::new());
    // May share, may read — but has no WRITE to delegate.
    acl.replace_grants(vec![Grant {
        id: 1,
        principal: Principal::User(OWNER),
        share: SHARE,
        subpath: SafePath::root(),
        allow: Perms::READ | Perms::DOWNLOAD | Perms::SHARE | Perms::CREATE,
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
    core.attach_links(LinkStore::open_in_memory().unwrap()).unwrap();
    core.mkdir(OWNER, "/root/d").unwrap();

    let overreach = LinkSpec { perms: Perms::READ | Perms::WRITE, ..LinkSpec::default() };
    assert!(matches!(core.create_link(OWNER, "/root/d", &overreach), Err(CoreError::Denied { .. })));

    let ok = LinkSpec { perms: Perms::READ | Perms::DOWNLOAD, ..LinkSpec::default() };
    assert!(core.create_link(OWNER, "/root/d", &ok).is_ok());
}

#[test]
fn an_empty_permission_set_is_refused() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let spec = LinkSpec { perms: Perms::empty(), ..LinkSpec::default() };
    assert!(core.create_link(OWNER, "/root/a.txt", &spec).is_err());
}

// ------------------------------------------------------------- file drops --

fn drop_spec() -> LinkSpec {
    LinkSpec { perms: Perms::CREATE, ..LinkSpec::default() }
}

#[test]
fn a_file_drop_link_must_target_a_directory() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    assert!(core.create_link(OWNER, "/root/a.txt", &drop_spec()).is_err());
}

#[test]
fn a_file_drop_link_lists_nothing_and_never_overwrites() {
    let (core, _dir) = setup();
    core.mkdir(OWNER, "/root/inbox").unwrap();
    seed_file(&core, "/root/inbox/existing.txt");

    let (link, _t) = core.create_link(OWNER, "/root/inbox", &drop_spec()).unwrap();
    assert!(link.is_drop());

    // No listing: the uploader must not learn what is already in the box.
    assert!(matches!(core.link_list(&link), Err(CoreError::Denied { .. })));

    // A colliding name is renamed, not replaced.
    let e = core.link_drop(&link, "existing.txt", b"from a stranger").unwrap();
    assert_eq!(e.name, "existing (2).txt", "same rename scheme as OnConflict::Rename");
    assert_eq!(core.read_bytes(OWNER, "/root/inbox/existing.txt", 4096).unwrap().0, b"contents");
    assert_eq!(
        core.read_bytes(OWNER, "/root/inbox/existing (2).txt", 4096).unwrap().0,
        b"from a stranger"
    );

    // A fresh name lands as itself.
    let e = core.link_drop(&link, "new.txt", b"hi").unwrap();
    assert_eq!(e.name, "new.txt");
}

#[test]
fn a_read_only_link_cannot_be_used_to_upload() {
    let (core, _dir) = setup();
    core.mkdir(OWNER, "/root/pub").unwrap();
    let (link, _t) = core.create_link(OWNER, "/root/pub", &LinkSpec::default()).unwrap();
    assert!(matches!(core.link_drop(&link, "x.txt", b"no"), Err(CoreError::Denied { .. })));
}

#[test]
fn a_read_link_on_a_directory_lists_it() {
    let (core, _dir) = setup();
    core.mkdir(OWNER, "/root/pub").unwrap();
    seed_file(&core, "/root/pub/one.txt");
    seed_file(&core, "/root/pub/two.txt");
    let (link, _t) = core.create_link(OWNER, "/root/pub", &LinkSpec::default()).unwrap();
    let names: Vec<String> = core.link_list(&link).unwrap().into_iter().map(|e| e.name).collect();
    assert_eq!(names, vec!["one.txt", "two.txt"]);
}

// ---------------------------------------------------------------- CRUD/ACL --

#[test]
fn list_update_delete_roundtrip_and_path_filter() {
    let (core, _dir) = setup();
    core.mkdir(OWNER, "/root/d").unwrap();
    seed_file(&core, "/root/a.txt");
    let (l1, _) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    let (l2, _) = core.create_link(OWNER, "/root/d", &LinkSpec::default()).unwrap();

    assert_eq!(core.list_links(OWNER, None).unwrap().len(), 2);
    let only_file = core.list_links(OWNER, Some("/root/a.txt")).unwrap();
    assert_eq!(only_file.len(), 1);
    assert_eq!(only_file[0].id, l1.id);

    let patched = core
        .update_link(
            OWNER,
            l2.id,
            &LinkPatch { label: Some(Some("shared folder".into())), max_downloads: Some(Some(5)), ..Default::default() },
        )
        .unwrap();
    assert_eq!(patched.label.as_deref(), Some("shared folder"));
    assert_eq!(patched.max_downloads, Some(5));

    core.delete_link(OWNER, l1.id).unwrap();
    assert_eq!(core.list_links(OWNER, None).unwrap().len(), 1);
    assert!(matches!(core.get_link(OWNER, l1.id), Err(CoreError::NotFound)));
}

#[test]
fn one_user_cannot_see_or_touch_another_users_link() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();

    // `NotFound`, not `Denied`: probing ids must not confirm existence.
    assert!(matches!(core.get_link(READER, link.id), Err(CoreError::NotFound)));
    assert!(matches!(core.delete_link(READER, link.id), Err(CoreError::NotFound)));
    assert!(core.list_links(READER, None).unwrap().is_empty());
    assert!(core.get_link(OWNER, link.id).is_ok(), "the owner's link survived the failed delete");
}

#[test]
fn links_for_node_finds_links_by_fileid() {
    let (core, _dir) = setup();
    seed_file(&core, "/root/a.txt");
    let (link, _t) = core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()).unwrap();
    let fid = link.fileid_at_creation.unwrap();
    let found = core.links_for_node(SHARE, fid).unwrap();
    assert_eq!(found.len(), 1);
    assert_eq!(found[0].id, link.id);
    assert!(core.links_for_node(SHARE, FileId::new(fid.get() + 1000)).unwrap().is_empty());
}

// ------------------------------------------------------------ argon2 gate --

/// `check_link_password` is reachable by anyone holding (or guessing) a
/// share token — no session, no login rate limit, nothing upstream of it.
/// Before `crate::argon_gate` existed, every concurrent call ran Argon2
/// with no bound at all: enough parallel requests against one public link
/// could stand up an unbounded number of 48 MiB (default) Argon2id buffers
/// at once. This proves the gate actually serializes concurrent verifies
/// down to `argon2_parallelism`, the same way `sc-auth`'s own login gate
/// bounds concurrent logins (`DESIGN-AUTH.md` §2.2).
#[test]
fn concurrent_share_link_password_checks_are_gated_not_unbounded() {
    let (core, _dir) = setup_without_links();
    // Small, fast-but-real Argon2 params so six concurrent verifies don't
    // make this test slow, while still exercising the actual hash path.
    let cfg = sc_auth::AuthConfig {
        argon2_parallelism: 2,
        argon2_m_cost_kib: 8 * 1024,
        argon2_t_cost: 1,
        ..sc_auth::AuthConfig::default()
    };
    core.attach_links(LinkStore::open_in_memory_with_config(cfg).unwrap()).unwrap();

    // A nonexistent link id still runs a full Argon2 verify against the
    // store's dummy hash (§7.2 existence-oracle resistance), so this alone
    // drives real Argon2 traffic without needing a password-protected link.
    let mut handles = Vec::new();
    for _ in 0..6 {
        let core = core.clone();
        handles.push(std::thread::spawn(move || {
            core.check_link_password(999_999, "whatever").unwrap()
        }));
    }
    for h in handles {
        h.join().unwrap();
    }

    let high_water = core.links.get().unwrap().argon2_high_water();
    assert!(
        high_water <= 2,
        "expected Argon2 concurrency bounded to 2, saw {high_water} instead — the share-link \
         password gate must serialize concurrent verifies exactly like sc-auth's own login gate"
    );
}

#[test]
fn without_a_store_every_operation_says_not_supported_rather_than_pretending() {
    let (core, _dir) = setup_without_links();
    seed_file(&core, "/root/a.txt");
    assert!(!core.links_enabled());
    assert!(matches!(
        core.create_link(OWNER, "/root/a.txt", &LinkSpec::default()),
        Err(CoreError::NotSupported(_))
    ));
    assert!(matches!(core.list_links(OWNER, None), Err(CoreError::NotSupported(_))));
    assert!(matches!(core.resolve_link("whatever"), Err(CoreError::NotSupported(_))));
    assert!(matches!(core.delete_link(OWNER, 1), Err(CoreError::NotSupported(_))));
}
