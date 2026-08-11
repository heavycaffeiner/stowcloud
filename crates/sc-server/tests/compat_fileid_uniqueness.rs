//! `oc:id`/`oc:fileid` uniqueness and stability over the compat
//! mount.
//!
//! A sync client keys its whole local sync journal on `oc:id`. Two
//! resources reporting the same one is not a cosmetic wire defect — the
//! client concludes they are the *same* resource, which manifests as files
//! vanishing locally, a folder appearing to contain itself, or a permanent
//! resync loop.
//!
//! Two independent mechanisms used to produce exactly that:
//!
//! 1. `sc_core::Entry::id` is populated by a pure lookup that never
//!    allocates (`ops.rs`'s `build_entry`) — right for the native API/web
//!    UI, which have no protocol reason to need one just to display a
//!    listing, but the compat layer's prop emission fell back to a shared
//!    `FileId(0)` placeholder for every entry that had never separately
//!    been written/aggregated, so every untouched entry in one response
//!    reported the same id. `sc-server::nc::NcPropBridge::emit` now
//!    materializes a real, stable one on demand (`sc_core::Core::ensure_fileid`)
//!    when the request actually asks for identity.
//! 2. A share's own root directory has no real `sc-meta` row at all (it has
//!    no parent to be named under), so even *that* allocator hands back an
//!    ambiguous zero — `sc-server::nc::share_root_pseudo_id` gives it a
//!    synthetic, per-share, real-row-disjoint id instead.
//!
//! This file proves both, through the real assembled router, not a unit
//! test against either function in isolation.

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_server::app::App;
use sc_server::config::{Config, ShareBootstrap};
use sc_server::masterkey::MasterKeyResult;
use std::collections::HashSet;
use tower::ServiceExt;

struct Fixture {
    app: App,
    _data: tempfile::TempDir,
    _share_a: tempfile::TempDir,
    _share_b: tempfile::TempDir,
}

/// Two shares, one with a real subdirectory and a file inside it — the
/// exact shape of the reported bug: a share's root ("sharea") and a real
/// child directory inside it ("sharea/Reports") were observed reporting the
/// same `oc:fileid`.
fn fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share_a = tempfile::tempdir().expect("share a dir");
    let share_b = tempfile::tempdir().expect("share b dir");
    std::fs::create_dir(share_a.path().join("Reports")).unwrap();
    std::fs::write(share_a.path().join("Reports").join("q1.txt"), b"q1").unwrap();
    std::fs::write(share_a.path().join("top.txt"), b"top level").unwrap();
    std::fs::write(share_b.path().join("b.txt"), b"in share b").unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![
            ShareBootstrap {
                name: "sharea".into(),
                host_path: share_a.path().to_path_buf(),
                shared_externally: false,
            },
            ShareBootstrap {
                name: "shareb".into(),
                host_path: share_b.path().to_path_buf(),
                shared_externally: false,
            },
        ],
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
        _share_b: share_b,
    }
}

fn basic_app_password(token: &str) -> String {
    use data_encoding::BASE64;
    format!(
        "Basic {}",
        BASE64.encode(format!("alice:{token}").as_bytes())
    )
}

async fn propfind(app: &App, uri: &str, token: &str, depth: &str) -> String {
    let req = Request::builder()
        .method("PROPFIND")
        .uri(uri)
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(token))
        .header("Depth", depth)
        .body(Body::from(
            r#"<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
                 <d:prop><oc:fileid/><oc:id/></d:prop></d:propfind>"#,
        ))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS, "PROPFIND {uri}");
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    String::from_utf8_lossy(&bytes).into_owned()
}

/// Every `<oc:fileid>` value in a multistatus body, in document order —
/// intentionally naive (no XML parser), matching how every other DAV test
/// in this crate inspects a response body.
fn fileids(body: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut rest = body;
    while let Some(start) = rest.find("<oc:fileid>") {
        rest = &rest[start + "<oc:fileid>".len()..];
        let end = rest.find("</oc:fileid>").expect("a closing tag");
        out.push(rest[..end].to_string());
        rest = &rest[end..];
    }
    out
}

fn assert_all_distinct(ids: &[String], context: &str) {
    assert!(
        !ids.is_empty(),
        "{context}: no oc:fileid values found at all"
    );
    let set: HashSet<&String> = ids.iter().collect();
    assert_eq!(
        set.len(),
        ids.len(),
        "{context}: duplicate oc:fileid among {ids:?} — a client reads this as \"these are the same file\""
    );
}

/// The exact reported shape: a folder with real children, PROPFIND'd
/// Depth: 1, over the compat mount. Every row — the collection's own row
/// included — must carry a distinct `oc:fileid`.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_folder_with_children_reports_distinct_ids_including_its_own_row() {
    let f = fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let token = f
        .app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1;

    let body = propfind(&f.app, "/remote.php/dav/files/alice/sharea/", &token, "1").await;
    assert_all_distinct(&fileids(&body), "sharea/ (Depth 1)");

    // Depth: 1 on "Reports" itself must also disagree with "sharea"'s own id
    // — the exact pairing that was observed colliding.
    let root_body = propfind(&f.app, "/remote.php/dav/files/alice/sharea/", &token, "0").await;
    let root_ids = fileids(&root_body);
    let reports_body = propfind(
        &f.app,
        "/remote.php/dav/files/alice/sharea/Reports/",
        &token,
        "0",
    )
    .await;
    let reports_ids = fileids(&reports_body);
    assert_eq!(root_ids.len(), 1);
    assert_eq!(reports_ids.len(), 1);
    assert_ne!(
        root_ids[0], reports_ids[0],
        "a share root and a real subdirectory inside it reported the same oc:fileid"
    );
}

/// The synthetic files-root listing (`nc::h_files_root`) is a second place
/// the same class of bug showed up: every grant-projected root that had
/// never individually been touched reported the placeholder id, so *every*
/// root in the listing collided on it at once.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_synthetic_files_root_reports_distinct_ids_for_every_share() {
    let f = fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "bob",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let token = f
        .app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1;

    let body = propfind(&f.app, "/remote.php/dav/files/bob/", &token, "1").await;
    let ids = fileids(&body);
    // The root row itself plus one per share.
    assert_eq!(ids.len(), 3, "{body}");
    assert_all_distinct(&ids, "synthetic files root");
}

/// Stability: the same entry, PROPFIND'd twice, must report the same id
/// both times — uniqueness alone is not the contract (`NcConfig::instance_id`'s
/// doc comment makes the same point for the instance half of the same
/// identifier: a changed id forces a full client resync).
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_same_entry_reports_the_same_id_across_requests() {
    let f = fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "carol",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let token = f
        .app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1;

    let first = fileids(
        &propfind(
            &f.app,
            "/remote.php/dav/files/carol/sharea/Reports/",
            &token,
            "0",
        )
        .await,
    );
    let second = fileids(
        &propfind(
            &f.app,
            "/remote.php/dav/files/carol/sharea/Reports/",
            &token,
            "0",
        )
        .await,
    );
    assert_eq!(first, second);

    let root_first = fileids(&propfind(&f.app, "/remote.php/dav/files/carol/", &token, "0").await);
    let root_second = fileids(&propfind(&f.app, "/remote.php/dav/files/carol/", &token, "0").await);
    assert_eq!(
        root_first, root_second,
        "the synthetic files root's own id must be stable too"
    );
}

/// `true` iff every `<d:response>` in `body` carries an `<oc:id>` with a
/// non-empty value inside a `200 OK` propstat — never absent, never
/// declared `404`.
fn every_response_has_an_ok_oc_id(body: &str) -> bool {
    let responses: Vec<&str> = body.split("<d:response>").skip(1).collect();
    if responses.is_empty() {
        return false;
    }
    responses.iter().all(|r| {
        let r = r.split("</d:response>").next().unwrap_or(r);
        r.split("<d:propstat>").skip(1).any(|p| {
            let p = p.split("</d:propstat>").next().unwrap_or(p);
            p.contains("<oc:id>") && !p.contains("<oc:id></oc:id>") && p.contains("HTTP/1.1 200")
        })
    })
}

/// The `<d:response>...</d:response>` block whose `<d:href>` contains
/// `href_substr` — naive substring scanning, matching every other body
/// inspection in this file.
fn response_for_href<'a>(body: &'a str, href_substr: &str) -> &'a str {
    let mut idx = 0;
    loop {
        let start = body[idx..]
            .find("<d:response>")
            .map(|i| i + idx)
            .unwrap_or_else(|| panic!("no response containing {href_substr:?} in {body}"));
        let end = body[start..]
            .find("</d:response>")
            .map(|i| i + start)
            .unwrap()
            + "</d:response>".len();
        let chunk = &body[start..end];
        if chunk.contains(href_substr) {
            return chunk;
        }
        idx = end;
    }
}

fn extract_tag<'a>(chunk: &'a str, open: &str, close: &str) -> &'a str {
    let start = chunk
        .find(open)
        .unwrap_or_else(|| panic!("{open} not found in {chunk}"))
        + open.len();
    let end = chunk[start..]
        .find(close)
        .map(|i| i + start)
        .unwrap_or_else(|| panic!("{close} not found in {chunk}"));
    &chunk[start..end]
}

/// Every `oc:fileid` on the wire must parse as a positive integer. A client
/// that does arithmetic on it (a diff, a hash bucket, storing it in an
/// unsigned column, ...) gets nonsense from a negative one, and the
/// pre-fix scheme's `i64::MIN` specifically is actively dangerous:
/// negating it overflows under two's-complement, and `abs()` on it is
/// undefined or panics in most languages that have one.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn every_oc_fileid_in_the_synthetic_root_is_a_positive_integer() {
    let f = fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "dave",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let token = f
        .app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1;

    let body = propfind(&f.app, "/remote.php/dav/files/dave/", &token, "1").await;
    let ids = fileids(&body);
    assert_eq!(ids.len(), 3, "{body}");
    for raw in &ids {
        let parsed: i64 = raw.parse().unwrap_or_else(|_| {
            panic!("oc:fileid {raw:?} does not even parse as an integer: {body}")
        });
        assert!(
            parsed > 0,
            "oc:fileid must be a positive integer, got {parsed}: {body}"
        );
    }
}

/// `oc:id` must be present, with a real value, inside a `200 OK` propstat
/// for every row the synthetic root response claims to describe — never
/// `404 Not Found`. Before this fix it was 404 for every row: the account's
/// own root and each of its granted roots reported `oc:fileid` but declared
/// `oc:id` unavailable, even though `oc:id` — not `oc:fileid` alone — is
/// what a sync client's local sync journal is actually keyed on
/// (`props.rs`'s `NcPropSource::emit` doc comment on `oc:id`/`nc_id`), and
/// the files root is the very first response a client sees after
/// enrolment.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn oc_id_is_present_with_200_for_every_row_in_the_synthetic_root() {
    let f = fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "erin",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let token = f
        .app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1;

    let body = propfind(&f.app, "/remote.php/dav/files/erin/", &token, "1").await;
    assert!(
        every_response_has_an_ok_oc_id(&body),
        "every row must answer oc:id with a real value under a 200 propstat, not omit or 404 it: {body}"
    );
}

/// The same resource must report the same identity whichever path a client
/// reaches it by: what the synthetic root listing says about "sharea" must
/// match what a direct `PROPFIND` on "sharea" itself says, for both
/// `oc:fileid` and `oc:id`. A client that stats a child before descending
/// into it — routine behavior — would otherwise see two different
/// identities for the same folder, the same class of bug as the collision
/// this whole fix exists to close, just inverted (one resource, two ids,
/// instead of two resources, one id).
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_synthetic_root_and_a_direct_propfind_agree_on_the_same_resource() {
    let f = fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "frank",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let token = f
        .app
        .auth
        .issue_app_password(uid, "test", sc_auth::Scope::default())
        .expect("app password")
        .1;

    let root_body = propfind(&f.app, "/remote.php/dav/files/frank/", &token, "1").await;
    let sharea_via_root = response_for_href(&root_body, "/sharea/");
    let fileid_via_root = extract_tag(sharea_via_root, "<oc:fileid>", "</oc:fileid>").to_string();
    let id_via_root = extract_tag(sharea_via_root, "<oc:id>", "</oc:id>").to_string();

    let direct_body = propfind(&f.app, "/remote.php/dav/files/frank/sharea/", &token, "0").await;
    let sharea_direct = response_for_href(&direct_body, "/sharea/");
    let fileid_direct = extract_tag(sharea_direct, "<oc:fileid>", "</oc:fileid>").to_string();
    let id_direct = extract_tag(sharea_direct, "<oc:id>", "</oc:id>").to_string();

    assert_eq!(
        fileid_via_root, fileid_direct,
        "oc:fileid must agree between the synthetic root listing and a direct PROPFIND"
    );
    assert_eq!(
        id_via_root, id_direct,
        "oc:id must agree between the synthetic root listing and a direct PROPFIND"
    );
}
