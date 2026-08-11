//! End-to-end proof that `/recent` answers "what you did here" rather than
//! "what changed on disk", on both surfaces.
//!
//! Everything runs through the real assembled router. Writes go in over the
//! native API, a file appears on the share the way Samba would put it there,
//! and both the native tab and the compat `SEARCH` the phone apps send are
//! asked what they see.

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_server::app::App;
use sc_server::config::{Config, ShareBootstrap};
use sc_server::masterkey::MasterKeyResult;
use tower::ServiceExt;

struct Fixture {
    app: App,
    share: tempfile::TempDir,
    _data: tempfile::TempDir,
}

fn fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share = tempfile::tempdir().expect("share dir");
    std::fs::create_dir(share.path().join("docs")).unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "files".into(),
            host_path: share.path().to_path_buf(),
            shared_externally: false,
        }],
        public_origins: vec!["https://localhost".into()],
        ..Config::default()
    };
    let key = MasterKeyResult { key: [7u8; 32], inside_data_dir: false, generated: true };
    let app = App::build(cfg.clone(), cfg, &key).expect("app builds");
    Fixture { app, share, _data: data }
}

async fn json_of(resp: axum::response::Response) -> serde_json::Value {
    let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
    serde_json::from_slice(&bytes).unwrap_or(serde_json::Value::Null)
}

async fn text_of(resp: axum::response::Response) -> String {
    let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
    String::from_utf8_lossy(&bytes).into_owned()
}

struct Session {
    cookie: String,
    csrf: String,
}

fn bootstrap(f: &Fixture, name: &str) -> sc_vfs::UserId {
    let uid = f
        .app
        .auth
        .create_user(name, &secrecy::SecretString::from("correct horse battery".to_string()))
        .unwrap();
    f.app.auth.set_admin(uid, true).unwrap();
    f.app.core.seed_full_access(uid).unwrap();
    uid
}

async fn login(app: &App, username: &str) -> Session {
    let req = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(
            serde_json::json!({ "username": username, "password": "correct horse battery" })
                .to_string(),
        ))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "login as {username}");
    let cookie = resp
        .headers()
        .get("set-cookie")
        .and_then(|v| v.to_str().ok())
        .and_then(|c| c.split(';').next())
        .expect("a session cookie")
        .to_string();

    let req = Request::builder()
        .uri("/api/auth/session")
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .body(Body::empty())
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    let csrf = json_of(resp).await["csrf"].as_str().unwrap().to_string();
    Session { cookie, csrf }
}

async fn get(app: &App, s: &Session, uri: &str) -> (StatusCode, serde_json::Value) {
    let req = Request::builder()
        .uri(uri)
        .header("Host", "localhost")
        .header("Cookie", &s.cookie)
        .body(Body::empty())
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    (status, json_of(resp).await)
}

async fn send(
    app: &App,
    s: &Session,
    method: &str,
    uri: &str,
    body: serde_json::Value,
) -> (StatusCode, serde_json::Value) {
    let req = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .header("Cookie", &s.cookie)
        .header("Sc-Csrf", &s.csrf)
        .header("Origin", "https://localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    (status, json_of(resp).await)
}

/// `(vpath, op)` for every row `/api/recent` returns.
async fn recent(app: &App, s: &Session) -> Vec<(String, String)> {
    let (status, body) = get(app, s, "/api/recent").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.get("completeness").is_none(), "there is no walk left to truncate");
    body["hits"]
        .as_array()
        .expect("hits")
        .iter()
        .map(|h| {
            (
                h["vpath"].as_str().unwrap().to_string(),
                h["op"].as_str().unwrap().to_string(),
            )
        })
        .collect()
}

/// One `SEARCH` against the compat DAV root, as the phone apps send it.
async fn search(app: &App, username: &str, body: &str) -> (StatusCode, String) {
    let req = Request::builder()
        .method("SEARCH")
        .uri("/remote.php/dav")
        .header("Host", "localhost")
        .header("Content-Type", "application/xml")
        .header(
            "Authorization",
            format!(
                "Basic {}",
                data_encoding::BASE64.encode(
                    format!("{username}:correct horse battery").as_bytes()
                )
            ),
        )
        .body(Body::from(body.to_string()))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    (status, text_of(resp).await)
}

/// Every write surface the native API offers, and the label each one earns.
#[tokio::test(flavor = "multi_thread")]
async fn the_recent_tab_shows_what_this_account_did() {
    let f = fixture();
    bootstrap(&f, "alice");
    let alice = login(&f.app, "alice").await;

    let (status, saved) =
        send(&f.app, &alice, "PUT", "/api/fs/write", serde_json::json!({ "path": "files/a.txt", "content": "one" })).await;
    assert_eq!(status, StatusCode::OK, "{saved}");
    assert_eq!(recent(&f.app, &alice).await, vec![("files/a.txt".into(), "upload".into())]);

    let etag = saved["etag"].as_str().unwrap().to_string();
    let (status, _) = send(
        &f.app,
        &alice,
        "PUT",
        "/api/fs/write",
        serde_json::json!({ "path": "files/a.txt", "content": "two", "if_match": etag }),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(recent(&f.app, &alice).await, vec![("files/a.txt".into(), "edit".into())]);

    let (status, _) =
        send(&f.app, &alice, "POST", "/api/fs/rename", serde_json::json!({ "path": "files/a.txt", "new_name": "b.txt" })).await;
    assert_eq!(status, StatusCode::OK);
    let rows = recent(&f.app, &alice).await;
    assert!(
        rows.contains(&("files/b.txt".to_string(), "move".to_string())),
        "a moved file appears at its new path: {rows:?}"
    );
    assert!(
        !rows.iter().any(|(p, _)| p == "files/a.txt"),
        "the old path fails its stat and is dropped: {rows:?}"
    );
}

/// Principle 3, from the other side: these shares are also written by Samba,
/// and this list is not about them.
#[tokio::test(flavor = "multi_thread")]
async fn a_file_written_outside_this_server_is_not_in_the_list() {
    let f = fixture();
    bootstrap(&f, "alice");
    let alice = login(&f.app, "alice").await;

    std::fs::write(f.share.path().join("from-samba.txt"), b"not ours").unwrap();
    send(&f.app, &alice, "PUT", "/api/fs/write", serde_json::json!({ "path": "files/mine.txt", "content": "x" })).await;

    let rows = recent(&f.app, &alice).await;
    assert_eq!(rows, vec![("files/mine.txt".into(), "upload".into())]);

    // It is still fully visible everywhere else.
    let (status, listing) = get(&f.app, &alice, "/api/fs/list?path=files").await;
    assert_eq!(status, StatusCode::OK);
    assert!(
        listing["entries"].as_array().unwrap().iter().any(|e| e["name"] == "from-samba.txt"),
        "browsing is unchanged: {listing}"
    );
}

/// Two accounts writing the same share see their own work and not each
/// other's.
#[tokio::test(flavor = "multi_thread")]
async fn one_accounts_writes_never_appear_in_anothers_list() {
    let f = fixture();
    bootstrap(&f, "alice");
    let bob = bootstrap(&f, "bob");
    f.app.core.seed_full_access(bob).unwrap();
    let alice = login(&f.app, "alice").await;
    let bob = login(&f.app, "bob").await;

    send(&f.app, &alice, "PUT", "/api/fs/write", serde_json::json!({ "path": "files/alice.txt", "content": "a" })).await;
    send(&f.app, &bob, "PUT", "/api/fs/write", serde_json::json!({ "path": "files/bob.txt", "content": "b" })).await;

    assert_eq!(recent(&f.app, &alice).await, vec![("files/alice.txt".into(), "upload".into())]);
    assert_eq!(recent(&f.app, &bob).await, vec![("files/bob.txt".into(), "upload".into())]);
}

/// A row whose file is gone is absent, and gone from the table afterwards.
/// Two identical requests over an unchanged table return the identical
/// sequence.
#[tokio::test(flavor = "multi_thread")]
async fn a_dead_row_disappears_and_the_order_is_stable() {
    let f = fixture();
    bootstrap(&f, "alice");
    let alice = login(&f.app, "alice").await;

    for name in ["a.txt", "b.txt", "c.txt"] {
        send(
            &f.app,
            &alice,
            "PUT",
            "/api/fs/write",
            serde_json::json!({ "path": format!("files/{name}"), "content": "x" }),
        )
        .await;
    }
    assert_eq!(recent(&f.app, &alice).await.len(), 3);
    assert_eq!(recent(&f.app, &alice).await, recent(&f.app, &alice).await);

    // Removed behind this server's back, as a delete over SMB would be.
    std::fs::remove_file(f.share.path().join("b.txt")).unwrap();
    let rows = recent(&f.app, &alice).await;
    assert_eq!(rows.len(), 2, "{rows:?}");
    assert!(!rows.iter().any(|(p, _)| p == "files/b.txt"));
    // And the row is gone from the table, so recreating the file by hand does
    // not bring it back.
    std::fs::write(f.share.path().join("b.txt"), b"x").unwrap();
    let rows = recent(&f.app, &alice).await;
    assert_eq!(rows.len(), 2, "a deleted row is not resurrected by the file coming back: {rows:?}");
}

/// The Android and iOS Recent bodies from proposal 21 §2.2, verbatim in shape:
/// a Unix-second bound, an ISO datetime bound, a negated directory content
/// type, and an `oc:size` disjunct nothing reads. All of them used to be a
/// `400` before the date literal was fixed.
#[tokio::test(flavor = "multi_thread")]
async fn both_phone_recent_screens_get_the_same_list_as_the_web_tab() {
    let f = fixture();
    bootstrap(&f, "alice");
    let alice = login(&f.app, "alice").await;

    std::fs::write(f.share.path().join("from-samba.txt"), b"not ours").unwrap();
    send(&f.app, &alice, "PUT", "/api/fs/write", serde_json::json!({ "path": "files/mine.txt", "content": "x" })).await;

    let android = r#"<?xml version="1.0"?>
      <d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
        <d:basicsearch>
          <d:select><d:prop><d:getlastmodified/><d:getcontentlength/></d:prop></d:select>
          <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
          <d:where><d:and>
            <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>1</d:literal></d:gt>
            <d:lt><d:prop><d:getlastmodified/></d:prop><d:literal>9999999999</d:literal></d:lt>
            <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>1970-01-01T00:00:01Z</d:literal></d:gt>
          </d:and></d:where>
          <d:orderby><d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order></d:orderby>
          <d:limit><d:nresults>100</d:nresults></d:limit>
        </d:basicsearch>
      </d:searchrequest>"#;
    let (status, body) = search(&f.app, "alice", android).await;
    assert_eq!(status, StatusCode::MULTI_STATUS, "{body}");
    assert!(body.contains("mine.txt"), "{body}");
    assert!(!body.contains("from-samba.txt"), "{body}");

    let ios = r#"<?xml version="1.0"?>
      <d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
        <d:basicsearch>
          <d:select><d:prop><d:getlastmodified/></d:prop></d:select>
          <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
          <d:where><d:and>
            <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>1</d:literal></d:gt>
            <d:or>
              <d:not><d:eq><d:prop><d:getcontenttype/></d:prop><d:literal>httpd/unix-directory</d:literal></d:eq></d:not>
              <d:eq><d:prop><oc:size/></d:prop><d:literal>0</d:literal></d:eq>
            </d:or>
          </d:and></d:where>
          <d:orderby><d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order></d:orderby>
          <d:limit><d:nresults>100</d:nresults></d:limit>
        </d:basicsearch>
      </d:searchrequest>"#;
    let (status, body) = search(&f.app, "alice", ios).await;
    assert_eq!(status, StatusCode::MULTI_STATUS, "{body}");
    assert!(body.contains("mine.txt"), "{body}");
    assert!(!body.contains("from-samba.txt"), "{body}");
}

/// A gallery query names media types, so it is not a recency request: it
/// still walks, and still finds a photo this account did not write.
#[tokio::test(flavor = "multi_thread")]
async fn a_gallery_query_still_walks_the_filesystem() {
    let f = fixture();
    bootstrap(&f, "alice");

    std::fs::write(f.share.path().join("holiday.jpg"), b"jpeg-ish").unwrap();

    let gallery = r#"<?xml version="1.0"?>
      <d:searchrequest xmlns:d="DAV:">
        <d:basicsearch>
          <d:select><d:prop><d:getlastmodified/></d:prop></d:select>
          <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
          <d:where><d:and>
            <d:or>
              <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>image/%</d:literal></d:like>
              <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>video/%</d:literal></d:like>
            </d:or>
            <d:lt><d:prop><d:getlastmodified/></d:prop><d:literal>9999999999</d:literal></d:lt>
          </d:and></d:where>
          <d:orderby><d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order></d:orderby>
        </d:basicsearch>
      </d:searchrequest>"#;
    let (status, body) = search(&f.app, "alice", gallery).await;
    assert_eq!(status, StatusCode::MULTI_STATUS, "{body}");
    assert!(
        body.contains("holiday.jpg"),
        "a media query answers from the filesystem, whoever wrote the file: {body}"
    );
}

/// A name search is a content query and keeps walking.
#[tokio::test(flavor = "multi_thread")]
async fn a_name_search_still_walks_the_filesystem() {
    let f = fixture();
    bootstrap(&f, "alice");

    std::fs::write(f.share.path().join("report-2026.txt"), b"x").unwrap();

    let by_name = r#"<?xml version="1.0"?>
      <d:searchrequest xmlns:d="DAV:">
        <d:basicsearch>
          <d:select><d:prop><d:displayname/></d:prop></d:select>
          <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
          <d:where><d:like><d:prop><d:displayname/></d:prop><d:literal>%report%</d:literal></d:like></d:where>
        </d:basicsearch>
      </d:searchrequest>"#;
    let (status, body) = search(&f.app, "alice", by_name).await;
    assert_eq!(status, StatusCode::MULTI_STATUS, "{body}");
    assert!(body.contains("report-2026.txt"), "{body}");
}

/// A date literal nothing can read is still a refusal: a bound the server
/// silently drops turns "modified since Tuesday" into "everything".
#[tokio::test(flavor = "multi_thread")]
async fn an_unreadable_date_literal_is_still_a_400() {
    let f = fixture();
    bootstrap(&f, "alice");

    let bad = r#"<?xml version="1.0"?>
      <d:searchrequest xmlns:d="DAV:"><d:basicsearch>
        <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
        <d:where><d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>last tuesday</d:literal></d:gt></d:where>
      </d:basicsearch></d:searchrequest>"#;
    let (status, _) = search(&f.app, "alice", bad).await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
}

/// Deleting the account takes its rows with it, because ids are reused.
#[tokio::test(flavor = "multi_thread")]
async fn deleting_an_account_leaves_it_no_rows() {
    let f = fixture();
    let admin = bootstrap(&f, "root");
    let bob = f
        .app
        .auth
        .create_user("bob", &secrecy::SecretString::from("correct horse battery".to_string()))
        .unwrap();
    f.app.core.seed_full_access(bob).unwrap();
    let _ = admin;

    let bob_session = login(&f.app, "bob").await;
    send(&f.app, &bob_session, "PUT", "/api/fs/write", serde_json::json!({ "path": "files/bob.txt", "content": "x" })).await;
    assert_eq!(recent(&f.app, &bob_session).await.len(), 1);

    let root = login(&f.app, "root").await;
    let req = Request::builder()
        .method("DELETE")
        .uri(format!("/api/admin/users/{}", bob.get()))
        .header("Host", "localhost")
        .header("Cookie", &root.cookie)
        .header("Sc-Csrf", &root.csrf)
        .header("Origin", "https://localhost")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::NO_CONTENT);

    // The account is gone, so ask the store directly: the next holder of this
    // id must not inherit a history.
    let journal = f.app.journal.as_ref().expect("the journal opened");
    assert!(journal.newest(bob, i64::MIN).is_empty());
}
