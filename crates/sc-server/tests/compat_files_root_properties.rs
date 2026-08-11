//! The synthetic files-root collection's own row (`nc::h_files_root`) must
//! answer requested properties at least as completely and honestly as an
//! ordinary child row does: a real, content-derived `getetag`; an explicit
//! `oc:permissions` rather than silence a client could misread as
//! "writable"; an honest "unknown" for quota rather than a fabricated
//! number; and RFC 4918 §9.1's second `404` propstat for anything
//! explicitly requested that this collection genuinely cannot answer.

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_server::app::App;
use sc_server::config::{Config, ShareBootstrap};
use sc_server::masterkey::MasterKeyResult;
use tower::ServiceExt;

struct Fixture {
    app: App,
    _data: tempfile::TempDir,
    _share_a: tempfile::TempDir,
}

fn fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share_a = tempfile::tempdir().expect("share a dir");
    std::fs::write(share_a.path().join("top.txt"), b"top level").unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "sharea".into(),
            host_path: share_a.path().to_path_buf(),
            shared_externally: false,
        }],
        public_origins: vec!["https://localhost".into()],
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [9u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg.clone(), cfg, &key).expect("app builds");
    Fixture {
        app,
        _data: data,
        _share_a: share_a,
    }
}

fn basic_app_password(token: &str) -> String {
    use data_encoding::BASE64;
    format!(
        "Basic {}",
        BASE64.encode(format!("alice:{token}").as_bytes())
    )
}

/// PROPFIND the bare files root with an explicit property list, returning
/// the raw multistatus body.
async fn propfind_root(app: &App, token: &str, props_xml: &str) -> String {
    let req = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/alice/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(token))
        .header("Depth", "0")
        .body(Body::from(format!(
            r#"<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
                 <d:prop>{props_xml}</d:prop></d:propfind>"#
        )))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    String::from_utf8_lossy(&bytes).into_owned()
}

/// The self row of the *only* response in a Depth: 0 multistatus.
fn self_response(body: &str) -> &str {
    let start = body.find("<d:response>").expect("a response element");
    &body[start..]
}

fn token(f: &Fixture) -> String {
    let uid = f
        .app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    f.app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1
}

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_root_reports_a_real_etag_permissions_and_honest_quota() {
    let f = fixture();
    let tok = token(&f);

    let body = propfind_root(
        &f.app,
        &tok,
        "<d:getetag/><oc:permissions/><d:quota-available-bytes/><d:quota-used-bytes/><d:resourcetype/>",
    )
    .await;
    let row = self_response(&body);

    // A real, non-empty etag, present in a 200 OK propstat.
    let etag_start = row.find("<d:getetag>").expect("getetag answered");
    let etag_end = row.find("</d:getetag>").unwrap();
    let etag_value = &row[etag_start + "<d:getetag>".len()..etag_end];
    assert!(
        !etag_value.trim_matches('"').is_empty(),
        "the root's etag must not be empty: {row}"
    );

    // Explicit, read-only permissions — never silence a client could read
    // as "go ahead and create something here".
    assert!(row.contains("<oc:permissions>G</oc:permissions>"), "{row}");

    // Honest "unknown", not a fabricated number: the reference server's own
    // FileInfo::SPACE_UNKNOWN sentinel.
    assert!(
        row.contains("<d:quota-available-bytes>-2</d:quota-available-bytes>"),
        "{row}"
    );
    assert!(
        row.contains("<d:quota-used-bytes>-2</d:quota-used-bytes>"),
        "{row}"
    );

    // None of the four properties landed in a 404 propstat.
    assert!(!row.contains("HTTP/1.1 404"), "{row}");
}

/// RFC 4918 §9.1: a property the client named explicitly that this
/// collection genuinely cannot answer belongs in its own `404` propstat,
/// not silently missing from the response.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn an_unanswerable_explicitly_requested_property_gets_its_own_404_propstat() {
    let f = fixture();
    let tok = token(&f);

    let body = propfind_root(&f.app, &tok, "<d:getetag/><d:getcontentlength/>").await;
    let row = self_response(&body);

    // The answerable one is still a normal 200.
    assert!(row.contains("<d:getetag>"), "{row}");

    // The unanswerable one appears inside a *second* propstat whose status
    // is 404 — not simply absent from the whole response.
    let propstats: Vec<&str> = row.split("<d:propstat>").skip(1).collect();
    assert_eq!(
        propstats.len(),
        2,
        "expected a 200 propstat and a 404 propstat: {row}"
    );
    let not_found = propstats
        .iter()
        .find(|p| p.contains("HTTP/1.1 404"))
        .expect("a 404 propstat");
    assert!(not_found.contains("<d:getcontentlength/>"), "{not_found}");
}

/// `propname` mode (RFC 4918 §14.20, `<d:propfind><d:propname/></d:propfind>`)
/// asks which properties exist, never their values — self-closing tags with
/// no content, the same degradation `sc_dav::PropWriter::text` already
/// applies for a normal entry.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn propname_mode_answers_with_empty_tags_not_values() {
    let f = fixture();
    let tok = token(&f);

    let req = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/alice/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&tok))
        .header("Depth", "0")
        .body(Body::from(
            r#"<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:propname/></d:propfind>"#,
        ))
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let body = String::from_utf8_lossy(&bytes).into_owned();

    assert!(body.contains("<d:getetag/>"), "{body}");
    assert!(
        !body.contains("<d:getetag>\""),
        "propname must not leak a value: {body}"
    );
}

/// `HEAD` on the bare files root must answer `200`, not fall through to
/// `sc_dav::DavService` with an empty vpath (which `resolve.rs` always
/// turns into `404`). This is exactly the request Android's
/// `ExistenceCheckRemoteOperation` sends right after Login Flow v2 hands
/// back credentials; a `404` here takes the "authorization fail due to
/// client side" branch in `AuthenticatorActivity` and reopens the web
/// login, producing a second Grant Access window the poller can no longer
/// collect.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn head_on_the_files_root_answers_200_not_404() {
    let f = fixture();
    let tok = token(&f);

    let req = Request::builder()
        .method("HEAD")
        .uri("/remote.php/dav/files/alice/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&tok))
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
}

/// The root's etag changes when the set of grant-projected roots changes —
/// otherwise a client that trusts it to decide "anything to re-list?" would
/// never notice a newly granted share.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_root_etag_changes_when_the_set_of_roots_changes() {
    let f = fixture();
    let tok = token(&f);

    let before = propfind_root(&f.app, &tok, "<d:getetag/>").await;
    let before_etag = self_response(&before).to_string();

    // A second share, granted to the same user.
    let share_b = tempfile::tempdir().unwrap();
    std::fs::write(share_b.path().join("b.txt"), b"in share b").unwrap();
    let share_def = sc_core::ShareDef {
        id: sc_vfs::ShareId::new(2),
        name: "shareb".into(),
        host_path: share_b.path().to_path_buf(),
        policy: sc_vfs::SharePolicy::default(),
        shared_externally: false,
    };
    f.app
        .core
        .register_share(share_def)
        .expect("register second share");
    let uid = f.app.auth.find_user("alice").unwrap().unwrap().id;
    f.app
        .core
        .seed_full_access(uid)
        .expect("re-grant after adding a share");

    let after = propfind_root(&f.app, &tok, "<d:getetag/>").await;
    let after_etag = self_response(&after).to_string();

    assert_ne!(
        before_etag, after_etag,
        "adding a root must change the files root's own etag"
    );
}

/// Every top-level folder used to read 0 B.
///
/// This response is hand-assembled rather than served through the property
/// source, and it carried no `oc:size` at all — so the value landed in the
/// `404` propstat and both clients left `size` at its initialised `0`. It is
/// also the *first* listing a client performs after enrolment, and the screen
/// that lists every folder the user has.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_root_and_its_children_report_a_recursive_size() {
    let f = fixture();
    let tok = token(&f);

    let body = propfind_root_deep(&f.app, &tok, "<d:displayname/><oc:size/>").await;
    // `top.txt` is the fixture's only file, at 9 bytes.
    assert!(
        body.contains("<oc:size>9</oc:size>"),
        "the grant root must report the rollup of its own subtree: {body}"
    );
    // Twice: once on the root collection's own row (the sum over distinct
    // grants) and once on the single child row.
    assert_eq!(
        body.matches("<oc:size>9</oc:size>").count(),
        2,
        "the root's own total and its one child's must both be present: {body}"
    );

    // And never as an empty element. Android casts this value unguarded, so
    // `<oc:size/>` fails the entire folder listing rather than one property —
    // the same rule `props.rs` applies to every other numeric property, now
    // applied to the response that test cannot see.
    assert!(!body.contains("<oc:size/>"), "{body}");
    assert!(!body.contains("<oc:fileid/>"), "{body}");
    assert!(!body.contains("<d:getetag/>"), "{body}");
}

/// As `propfind_root`, at `Depth: 1`, so the child rows are in the body too.
#[cfg(feature = "compat-nc")]
async fn propfind_root_deep(app: &App, token: &str, props_xml: &str) -> String {
    let req = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/alice/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(token))
        .header("Depth", "1")
        .body(Body::from(format!(
            r#"<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
                 <d:prop>{props_xml}</d:prop></d:propfind>"#
        )))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    String::from_utf8_lossy(&bytes).into_owned()
}
