//! A grant rooted at a subpath inside its share.
//!
//! Every path-vocabulary defect in this workspace survived because no fixture
//! used this shape. With every grant sitting at its share's own root the
//! grant's subpath is empty, so a conversion that forgets to strip it and one
//! that strips it agree, and a whole family of wrong answers is invisible.
//!
//! These tests use a share whose only grant is rooted one directory down, so
//! the two stop agreeing.

#![cfg(feature = "compat-nc")]

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_core::{Perms, SharePath};
use sc_server::app::App;
use sc_server::config::{Config, ShareBootstrap};
use sc_server::masterkey::MasterKeyResult;
use tower::ServiceExt;

struct Fixture {
    app: App,
    uid: sc_vfs::UserId,
    token: String,
    label: String,
    _data: tempfile::TempDir,
    _share: tempfile::TempDir,
}

/// The share holds `albums/summer/beach.png` and `outside.txt`; the grant is
/// rooted at `albums`, so the caller's tree is `{label}/summer/beach.png` and
/// `outside.txt` is not reachable at all.
fn fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share = tempfile::tempdir().expect("share dir");
    std::fs::create_dir_all(share.path().join("albums/summer")).unwrap();
    std::fs::write(share.path().join("albums/summer/beach.png"), vec![0u8; 500]).unwrap();
    std::fs::write(share.path().join("albums/note.txt"), vec![0u8; 24]).unwrap();
    std::fs::write(share.path().join("outside.txt"), b"not granted").unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "media".into(),
            host_path: share.path().to_path_buf(),
            shared_externally: false,
        }],
        public_origins: vec!["https://localhost".into()],
        // A content origin, so a resolved thumbnail is a 302 to it and an
        // unresolved one is a 404. Without one every preview answers 404 and
        // the two are indistinguishable — which is exactly the failure these
        // tests exist to tell apart.
        content_hosts: vec!["content.localhost".into()],
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [13u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg.clone(), cfg, &key).expect("app builds");

    let uid = app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    let share_id = app.core.share_defs().first().map(|d| d.id).expect("a share");
    app.core
        .create_grant(&sc_core::GrantSpec {
            principal: sc_core::Principal::User(uid),
            share: share_id,
            subpath: "albums".into(),
            allow: Perms::all(),
            deny: Perms::empty(),
            inherit: true,
            label: Some("photos".into()),
        })
        .expect("a grant rooted at a subpath");

    let label = app
        .core
        .roots(uid)
        .first()
        .map(|r| r.label.clone())
        .expect("a projected root");
    assert_eq!(label, "photos");
    assert!(
        !app.core.roots(uid).first().unwrap().subpath.is_empty(),
        "the fixture is pointless unless the grant really is rooted below the share"
    );

    let token = app
        .auth
        .issue_app_password(uid, "phone", sc_auth::Scope::default())
        .expect("app password")
        .1;

    Fixture {
        app,
        uid,
        token,
        label,
        _data: data,
        _share: share,
    }
}

async fn get(f: &Fixture, uri: &str) -> (StatusCode, Option<String>) {
    use data_encoding::BASE64;
    let req = Request::builder()
        .method("GET")
        .uri(uri)
        .header("Host", "localhost")
        .header(
            "Authorization",
            format!(
                "Basic {}",
                BASE64.encode(format!("alice:{}", f.token).as_bytes())
            ),
        )
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    let loc = resp
        .headers()
        .get(axum::http::header::LOCATION)
        .and_then(|v| v.to_str().ok())
        .map(str::to_string);
    (status, loc)
}

/// A thumbnail addressed by path: the client's path is already a vpath, and
/// prefixing the first root's label onto it aimed at `{label}/{label}/…`,
/// which almost never exists, so every such thumbnail 404'd.
#[tokio::test(flavor = "multi_thread")]
async fn a_path_addressed_thumbnail_resolves_through_a_subpath_rooted_grant() {
    let f = fixture();
    let (status, loc) = get(
        &f,
        &format!(
            "/index.php/apps/files/api/v1/thumbnail/64/64/{}/summer/beach.png",
            f.label
        ),
    )
    .await;
    assert_eq!(status, StatusCode::FOUND, "the path must resolve to a node");
    assert!(
        loc.unwrap_or_default().starts_with("https://content.localhost/c/"),
        "and the thumbnail must be signed on the content origin"
    );

    // A path outside the grant still 404s: this fixed a missed resolution, not
    // the ACL.
    let (status, _) = get(
        &f,
        "/index.php/apps/files/api/v1/thumbnail/64/64/photos/../outside.txt",
    )
    .await;
    assert_ne!(status, StatusCode::FOUND);
}

/// The same file, addressed by id. `MetaStore::resolve_path` answers a share
/// path that still carries the grant's `albums/`, and prefixing the label onto
/// it without stripping that aims one level too deep.
#[tokio::test(flavor = "multi_thread")]
async fn an_id_addressed_thumbnail_resolves_through_a_subpath_rooted_grant() {
    let f = fixture();
    let share = f.app.core.roots(f.uid).first().map(|r| r.share).unwrap();
    let sp = SharePath::parse("albums/summer/beach.png", 64).unwrap();
    let id = f
        .app
        .core
        .ensure_fileid(share, &sp)
        .expect("a stable id for the file");

    let (status, loc) = get(&f, &format!("/index.php/core/preview?fileId={}&x=64&y=64", id.0)).await;
    assert_eq!(status, StatusCode::FOUND, "the id must resolve to a node");
    assert!(loc
        .unwrap_or_default()
        .starts_with("https://content.localhost/c/"));
}

/// The conversion this fixture exists for. `MetaStore::resolve_path` answers a
/// share path that still carries `albums/`; the vpath is `photos/summer/...`,
/// with the subpath stripped and the label prefixed exactly once. Prefixing
/// without stripping aims at `photos/albums/summer/...` and every thumbnail in
/// this grant disappears.
#[tokio::test(flavor = "multi_thread")]
async fn a_share_path_converts_to_a_vpath_with_the_subpath_stripped() {
    let f = fixture();
    let share = f.app.core.roots(f.uid).first().map(|r| r.share).unwrap();
    let sp = SharePath::parse("albums/summer/beach.png", 64).unwrap();
    let vpath = f
        .app
        .core
        .vpath_for(f.uid, share, &sp)
        .expect("the grant projects it");
    assert_eq!(vpath.as_str(), "photos/summer/beach.png");
    // And it addresses the real file.
    let e = f
        .app
        .core
        .stat_entry(f.uid, vpath.as_str())
        .expect("the vpath resolves");
    assert_eq!(e.name, "beach.png");

    // A share path outside the grant has no vpath at all, which is what makes
    // `None` the not-found answer rather than a path that happens to miss.
    let outside = SharePath::parse("outside.txt", 64).unwrap();
    assert!(f.app.core.vpath_for(f.uid, share, &outside).is_none());
}

/// The grant root is the case an id-keyed aggregate could never serve: it has
/// no `node` row, so `resolve_path` missed, `recursive_size` returned `None`,
/// and `oc:size` fell back to the directory inode's own size — 4096 on ext4,
/// for a folder holding half a kilobyte here and a terabyte in production.
#[tokio::test(flavor = "multi_thread")]
async fn a_grant_root_reports_its_recursive_size_not_its_inode_size() {
    use data_encoding::BASE64;
    let f = fixture();
    let req = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/alice/")
        .header("Host", "localhost")
        .header(
            "Authorization",
            format!(
                "Basic {}",
                BASE64.encode(format!("alice:{}", f.token).as_bytes())
            ),
        )
        .header("Depth", "1")
        .body(Body::from(
            r#"<?xml version="1.0"?>
               <d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
                 <d:prop><d:displayname/><oc:size/></d:prop></d:propfind>"#,
        ))
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let body = String::from_utf8_lossy(&bytes).into_owned();

    // 500 + 24 for the two files under the grant root, and nothing for
    // `outside.txt`, which this grant does not project.
    assert!(
        body.contains("<oc:size>524</oc:size>"),
        "expected the recursive rollup of the grant's own subtree: {body}"
    );
    assert!(
        !body.contains("<oc:size/>"),
        "a numeric property is never emitted empty: {body}"
    );
}
