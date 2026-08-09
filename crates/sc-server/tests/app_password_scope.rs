//! `sc_auth::Scope` enforcement over WebDAV and the compat mount.
//!
//! Before `app::dav_authenticate` grew a scope check, an app password's
//! `Scope::perms_mask` was issued and persisted (`sc-auth`) but never read
//! again anywhere downstream: every handler resolved a virtual path with
//! only the account's `UserId`. A "read-only" app password had exactly the
//! same DAV/compat access as an unrestricted one — this file is the
//! regression test for that gap, end to end through the real assembled
//! router (`App::router`), not a unit test against a mocked backend.
//!
//! The native-API half of the same fix (`sc_http::middleware::scope_gate`)
//! is covered in `crates/sc-http/src/middleware.rs`'s own test module.

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_server::app::App;
use sc_server::config::{Config, ShareBootstrap};
use sc_server::masterkey::MasterKeyResult;
use tower::ServiceExt;

struct Fixture {
    app: App,
    _data: tempfile::TempDir,
    _share: tempfile::TempDir,
}

fn fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share = tempfile::tempdir().expect("share dir");
    std::fs::write(share.path().join("hello.txt"), b"hello dav").unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "files".into(),
            host_path: share.path().to_path_buf(),
            shared_externally: false,
        }],
        // `Config::default()`'s `app_hosts` has three entries (`sc-http`'s
        // own default, for a fresh install answering `localhost`/`127.0.0.1`/
        // `::1` without a 421), which is ambiguous for
        // `resolve_compat_canonical_url` on purpose (`app.rs`) — set it
        // explicitly here so these compat-mount tests keep exercising a
        // mounted layer, matching what every one of `app_hosts`' entries
        // would have derived to before that change.
        compat_canonical_url: Some("https://localhost".into()),
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [9u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg, &key).expect("app builds");
    Fixture {
        app,
        _data: data,
        _share: share,
    }
}

/// A user with a projected grant over the fixture share, plus its virtual
/// root label and a freshly issued app password scoped to `perms_mask`
/// (`None` = unrestricted, mirroring `sc_auth::Scope::default()`).
fn user_with_scoped_app_password(
    f: &Fixture,
    perms_mask: Option<u16>,
) -> (sc_vfs::UserId, String, String) {
    let uid = f
        .app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    // Grants are persisted and admin-managed now (`sc_core::acl_store`'s
    // module doc), not a startup-time projection over the whole account
    // list — `seed_full_access` is the same one-time full-access seed a
    // grant-management API would call for this user.
    f.app.core.seed_full_access(uid).expect("grant");
    let label = f
        .app
        .core
        .roots(uid)
        .first()
        .map(|r| r.label.clone())
        .expect("a projected root");

    let scope = sc_auth::Scope {
        perms_mask,
        shares: None,
    };
    let (_id, token) = f
        .app
        .auth
        .issue_app_password(uid, "test device", scope)
        .expect("issue app password");
    (uid, label, token)
}

/// App passwords authenticate over DAV Basic the same way the account
/// password does — the username is ignored for
/// this path (`sc_auth::AuthService::verify_basic`: an `stow_`-prefixed
/// password is routed straight to `verify_app_password` regardless of what
/// username accompanies it), so any placeholder username works.
fn basic_app_password(token: &str) -> String {
    use data_encoding::BASE64;
    format!(
        "Basic {}",
        BASE64.encode(format!("alice:{token}").as_bytes())
    )
}

async fn dav_status(app: &App, method: &str, uri: &str, token: &str) -> StatusCode {
    let req = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(token))
        .header("Depth", "0")
        .body(Body::from(if method == "PUT" { "new bytes" } else { "" }))
        .unwrap();
    app.router().oneshot(req).await.unwrap().status()
}

// ---------------------------------------------------------------- native /dav --

#[tokio::test(flavor = "multi_thread")]
async fn a_read_only_app_password_is_refused_on_a_native_dav_write() {
    let f = fixture();
    let (_uid, label, token) = user_with_scoped_app_password(&f, Some(sc_acl::Perms::READ.bits()));

    let put = dav_status(&f.app, "PUT", &format!("/dav/{label}/new.txt"), &token).await;
    assert_eq!(
        put,
        StatusCode::FORBIDDEN,
        "PUT should be refused by a read-only scope"
    );

    let delete = dav_status(&f.app, "DELETE", &format!("/dav/{label}/hello.txt"), &token).await;
    assert_eq!(
        delete,
        StatusCode::FORBIDDEN,
        "DELETE should be refused by a read-only scope"
    );

    let mkcol = dav_status(&f.app, "MKCOL", &format!("/dav/{label}/newdir"), &token).await;
    assert_eq!(
        mkcol,
        StatusCode::FORBIDDEN,
        "MKCOL should be refused by a read-only scope"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn the_same_read_only_app_password_still_succeeds_on_a_native_dav_read() {
    let f = fixture();
    let (_uid, label, token) = user_with_scoped_app_password(&f, Some(sc_acl::Perms::READ.bits()));

    let propfind = dav_status(&f.app, "PROPFIND", &format!("/dav/{label}"), &token).await;
    assert_eq!(
        propfind,
        StatusCode::MULTI_STATUS,
        "a read-only scope must still permit PROPFIND"
    );

    let get = dav_status(&f.app, "GET", &format!("/dav/{label}/hello.txt"), &token).await;
    assert_eq!(
        get,
        StatusCode::OK,
        "a read-only scope must still permit GET"
    );
}

/// Unchanged behavior for the common case: an app password with no
/// restriction at all (`Scope::default()`, `perms_mask: None`) must keep
/// working exactly as it did before this gate existed.
#[tokio::test(flavor = "multi_thread")]
async fn an_unrestricted_app_password_can_still_write_over_native_dav() {
    let f = fixture();
    let (_uid, label, token) = user_with_scoped_app_password(&f, None);

    let put = dav_status(&f.app, "PUT", &format!("/dav/{label}/new.txt"), &token).await;
    assert!(
        put.is_success(),
        "an unrestricted app password must be unaffected: {put}"
    );
}

// ---------------------------------------------------------------- compat mount --

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_read_only_app_password_is_refused_on_the_compat_mounts_write() {
    let f = fixture();
    let (_uid, label, token) = user_with_scoped_app_password(&f, Some(sc_acl::Perms::READ.bits()));

    let put = dav_status(
        &f.app,
        "PUT",
        &format!("/remote.php/dav/files/alice/{label}/new.txt"),
        &token,
    )
    .await;
    assert_eq!(
        put,
        StatusCode::FORBIDDEN,
        "compat PUT should be refused by a read-only scope"
    );

    let delete = dav_status(
        &f.app,
        "DELETE",
        &format!("/remote.php/dav/files/alice/{label}/hello.txt"),
        &token,
    )
    .await;
    assert_eq!(
        delete,
        StatusCode::FORBIDDEN,
        "compat DELETE should be refused by a read-only scope"
    );
}

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_same_read_only_app_password_still_succeeds_on_the_compat_mounts_read() {
    let f = fixture();
    let (_uid, label, token) = user_with_scoped_app_password(&f, Some(sc_acl::Perms::READ.bits()));

    let propfind = dav_status(
        &f.app,
        "PROPFIND",
        &format!("/remote.php/dav/files/alice/{label}"),
        &token,
    )
    .await;
    assert_eq!(
        propfind,
        StatusCode::MULTI_STATUS,
        "a read-only scope must still permit compat PROPFIND"
    );

    let get = dav_status(
        &f.app,
        "GET",
        &format!("/remote.php/dav/files/alice/{label}/hello.txt"),
        &token,
    )
    .await;
    assert_eq!(
        get,
        StatusCode::OK,
        "a read-only scope must still permit compat GET"
    );
}

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn an_unrestricted_app_password_can_still_write_over_the_compat_mount() {
    let f = fixture();
    let (_uid, label, token) = user_with_scoped_app_password(&f, None);

    let put = dav_status(
        &f.app,
        "PUT",
        &format!("/remote.php/dav/files/alice/{label}/new.txt"),
        &token,
    )
    .await;
    assert!(
        put.is_success(),
        "an unrestricted app password must be unaffected: {put}"
    );
}

/// The chunked-upload v2 surface speaks the same WebDAV verbs (`MKCOL`,
/// `PUT`, `MOVE`, `DELETE`) under `/remote.php/dav/uploads/**` — it is not
/// dispatched through `sc-dav` at all (`nc::h_chunked`), but it is still
/// wrapped by the same `with_dav_auth` gate `App::router` applies to every
/// compat route, so a read-only scope must refuse it too.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_read_only_app_password_is_refused_on_chunked_upload_session_creation() {
    let f = fixture();
    let (_uid, _label, token) = user_with_scoped_app_password(&f, Some(sc_acl::Perms::READ.bits()));

    let mkcol = dav_status(
        &f.app,
        "MKCOL",
        "/remote.php/dav/uploads/alice/txn-1",
        &token,
    )
    .await;
    assert_eq!(
        mkcol,
        StatusCode::FORBIDDEN,
        "opening a chunked-upload session is a write"
    );
}

/// A scope-restricted app password has no mapping for OCS share management —
/// it is not a WebDAV-shaped path — so it fails closed there too, even
/// though the operation is conceptually distinct from a file write.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_restricted_app_password_is_refused_on_ocs_share_creation() {
    let f = fixture();
    let (_uid, _label, token) = user_with_scoped_app_password(&f, Some(sc_acl::Perms::READ.bits()));

    let req = Request::builder()
        .method("POST")
        .uri("/ocs/v2.php/apps/files_sharing/api/v1/shares")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&token))
        .header("Content-Type", "application/x-www-form-urlencoded")
        .body(Body::from("path=/hello.txt&shareType=3"))
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::FORBIDDEN);
}

// ================================================================= scope_shares ==
//
// `Scope::shares` enforcement over WebDAV and the compat mount
// (`app::dav_authenticate`'s `shares_deny` half). The native-API half of the
// same fix (`sc_http::middleware::scope_gate`'s `share_scope_gate`) is
// covered in `crates/sc-http/src/middleware.rs`'s own test module; the
// wire-level creation endpoint (`POST /api/auth/app-passwords` accepting a
// `scope`) is covered in `crates/sc-http/src/routes.rs`'s test module. This
// section is the end-to-end proof that all three pieces actually agree.

struct TwoShareFixture {
    app: App,
    _data: tempfile::TempDir,
    _share_a: tempfile::TempDir,
    _share_b: tempfile::TempDir,
}

fn two_share_fixture() -> TwoShareFixture {
    let data = tempfile::tempdir().expect("data dir");
    let share_a = tempfile::tempdir().expect("share a dir");
    let share_b = tempfile::tempdir().expect("share b dir");
    std::fs::write(share_a.path().join("a.txt"), b"file in share a").unwrap();
    std::fs::write(share_b.path().join("b.txt"), b"file in share b").unwrap();

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
        // See the other fixture in this file for why this must be explicit.
        compat_canonical_url: Some("https://localhost".into()),
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [9u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg, &key).expect("app builds");
    TwoShareFixture {
        app,
        _data: data,
        _share_a: share_a,
        _share_b: share_b,
    }
}

/// A user with a projected grant over both fixture shares, plus both shares'
/// real `ShareId`s (looked up by label, same as `user_with_scoped_app_password`
/// looks up its one label) — so a test can build whatever `Scope::shares` it
/// needs without hardcoding an id it never created.
fn user_with_two_shares(f: &TwoShareFixture) -> (sc_vfs::UserId, sc_vfs::ShareId, sc_vfs::ShareId) {
    let uid = f
        .app
        .auth
        .create_user(
            "bob",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    let roots = f.app.core.roots(uid);
    let share_a = roots
        .iter()
        .find(|r| r.label == "sharea")
        .expect("share a root")
        .share;
    let share_b = roots
        .iter()
        .find(|r| r.label == "shareb")
        .expect("share b root")
        .share;
    (uid, share_a, share_b)
}

fn issue_share_scoped(
    f: &TwoShareFixture,
    uid: sc_vfs::UserId,
    shares: Option<Vec<sc_vfs::ShareId>>,
) -> String {
    let scope = sc_auth::Scope {
        perms_mask: None,
        shares,
    };
    let (_id, token) = f
        .app
        .auth
        .issue_app_password(uid, "test device", scope)
        .expect("issue app password");
    token
}

// ---------------------------------------------------------------- native /dav --

/// The whole point: a token scoped to one share is refused on a path under
/// the other share, over native WebDAV, and keeps working on its own.
#[tokio::test(flavor = "multi_thread")]
async fn a_share_scoped_app_password_is_refused_on_the_other_shares_native_dav_path() {
    let f = two_share_fixture();
    let (uid, share_a, _share_b) = user_with_two_shares(&f);
    let token = issue_share_scoped(&f, uid, Some(vec![share_a]));

    let denied = dav_status(&f.app, "GET", "/dav/shareb/b.txt", &token).await;
    assert_eq!(
        denied,
        StatusCode::FORBIDDEN,
        "a different share's path must be refused over native DAV"
    );

    let allowed = dav_status(&f.app, "GET", "/dav/sharea/a.txt", &token).await;
    assert_eq!(
        allowed,
        StatusCode::OK,
        "the token's own share must still work over native DAV"
    );
}

/// A `MOVE`'s `Destination` header is its own virtual path, distinct from
/// the request URI's — a token scoped to `sharea` must not be able to use
/// `Destination` to move a file into `shareb`.
#[tokio::test(flavor = "multi_thread")]
async fn a_share_scoped_app_password_cannot_move_into_the_other_share_via_destination() {
    let f = two_share_fixture();
    let (uid, share_a, _share_b) = user_with_two_shares(&f);
    let token = issue_share_scoped(&f, uid, Some(vec![share_a]));

    let req = Request::builder()
        .method("MOVE")
        .uri("/dav/sharea/a.txt")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&token))
        .header("Destination", "http://localhost/dav/shareb/moved.txt")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::FORBIDDEN);
}

/// `Scope::shares` naming a share id nothing resolves to must deny every
/// real path over native DAV too — never fall back to "unrestricted".
#[tokio::test(flavor = "multi_thread")]
async fn a_nonexistent_share_id_in_scope_denies_over_native_dav() {
    let f = two_share_fixture();
    let (uid, _share_a, _share_b) = user_with_two_shares(&f);
    let token = issue_share_scoped(&f, uid, Some(vec![sc_vfs::ShareId::new(999_999)]));

    let resp = dav_status(&f.app, "GET", "/dav/sharea/a.txt", &token).await;
    assert_eq!(resp, StatusCode::FORBIDDEN);
}

// -------------------------------------------------------------- compat mount --

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_share_scoped_app_password_is_refused_on_the_other_shares_compat_path() {
    let f = two_share_fixture();
    let (uid, share_a, _share_b) = user_with_two_shares(&f);
    let token = issue_share_scoped(&f, uid, Some(vec![share_a]));

    let denied = dav_status(
        &f.app,
        "GET",
        "/remote.php/dav/files/bob/shareb/b.txt",
        &token,
    )
    .await;
    assert_eq!(
        denied,
        StatusCode::FORBIDDEN,
        "a different share's path must be refused over the compat mount"
    );

    let allowed = dav_status(
        &f.app,
        "GET",
        "/remote.php/dav/files/bob/sharea/a.txt",
        &token,
    )
    .await;
    assert_eq!(
        allowed,
        StatusCode::OK,
        "the token's own share must still work over the compat mount"
    );
}

// ------------------------------------------------------------- end to end --

/// A restricted app password created through the *real* HTTP endpoint
/// (`POST /api/auth/app-passwords`, not `AuthService::issue_app_password`
/// called directly) is actually restricted end to end: mint it as a logged-in
/// session would, then use the returned token exactly as any WebDAV client
/// would, over the real assembled router.
#[tokio::test(flavor = "multi_thread")]
async fn a_restricted_token_from_the_real_endpoint_is_restricted_end_to_end() {
    let f = two_share_fixture();
    let uid = f
        .app
        .auth
        .create_user(
            "carol",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");

    let login = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(
            r#"{"username":"carol","password":"correct-horse-battery"}"#,
        ))
        .unwrap();
    let resp = f.app.router().oneshot(login).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "login");
    let cookie = resp
        .headers()
        .get("set-cookie")
        .and_then(|v| v.to_str().ok())
        .and_then(|c| c.split(';').next())
        .expect("a session cookie")
        .to_string();

    let session = Request::builder()
        .uri("/api/auth/session")
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(session).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let csrf = json["csrf"]
        .as_str()
        .expect("session carries a csrf token")
        .to_string();

    let create = Request::builder()
        .method("POST")
        .uri("/api/auth/app-passwords")
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .header("Sc-Csrf", &csrf)
        .header("Origin", "https://localhost:8443")
        .header("Content-Type", "application/json")
        .body(Body::from(
            serde_json::json!({ "name": "phone", "scope": { "shares": ["sharea"] } }).to_string(),
        ))
        .unwrap();
    let resp = f.app.router().oneshot(create).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::OK,
        "creating a scoped app password"
    );
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let token = json["token"].as_str().expect("a token").to_string();

    let denied = dav_status(&f.app, "GET", "/dav/shareb/b.txt", &token).await;
    assert_eq!(
        denied,
        StatusCode::FORBIDDEN,
        "a token scoped to sharea must not reach shareb"
    );

    let allowed = dav_status(&f.app, "GET", "/dav/sharea/a.txt", &token).await;
    assert_eq!(
        allowed,
        StatusCode::OK,
        "a token scoped to sharea must still reach sharea"
    );
}

// ============================================================== login_flow_v2 ==
//
// A token minted through the *real* Login Flow v2 HTTP endpoints
// (`POST /index.php/login/v2` -> a logged-in browser's consent screen ->
// `POST .../grant` -> `POST .../poll`), not `AuthService::issue_app_password`
// called directly. This is the gap that shipped broken: a real client walked
// the whole flow, chose "Full access" on the consent screen, and the
// resulting `appPassword` could read nothing at all afterward — not its own
// OCS capabilities, not its own account info (`sc_server::nc::NcAuth::
// issue_app_password` was translating "full access, no share restriction"
// into `sc_auth::Scope { perms_mask: Some(all_bits), .. }`, which is *not*
// what `perms_mask: None` — "unrestricted" — means to every enforcement
// point downstream, so it fell into the same fail-closed rule a genuinely
// restricted token does on the compat surfaces that have no per-method
// `Perms` bit to check a mask against), nor even browse
// `/remote.php/dav/files/{user}/` at all (`sc_core::Core::resolve_want`
// refuses an empty vpath unconditionally, and nothing translated the bare
// files-root request into one of the caller's grant-projected labels before
// this — see `nc::h_files_root`).

#[cfg(feature = "compat-nc")]
struct FlowCreds {
    app_password: String,
    login_name: String,
}

/// Walks the whole protocol exactly as a real client and a real human do:
/// `POST /index.php/login/v2` as the client, then a browser session logs in
/// and GETs the consent page, POSTs the grant with the requested `scope`,
/// and finally the client polls for the result — all through the real
/// assembled router, none of it constructed by hand.
#[cfg(feature = "compat-nc")]
async fn run_login_flow_v2(app: &App, username: &str, password: &str, scope: &str) -> FlowCreds {
    // Built *once* and reused (cloned — axum's `Router` is a cheap,
    // `Arc`-backed handle) for every step below. `App::router()` rebuilds
    // the whole compat mount on each call, including a fresh
    // `LoginFlowService` with its own randomly generated per-process CSRF
    // MAC key (`LoginFlowService::new`) — fine for a long-lived server that
    // builds its router once at startup, but calling `app.router()` again
    // per request here would mint a *different* key between the consent
    // screen's state token and the grant that has to verify it, making every
    // grant fail with a spurious `BadState`.
    let router = app.router();

    let init_req = Request::builder()
        .method("POST")
        .uri("/index.php/login/v2")
        .header("Host", "localhost")
        .header("User-Agent", "test-client/1.0")
        .body(Body::empty())
        .unwrap();
    let resp = router.clone().oneshot(init_req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "login/v2 init");
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let init: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let login_url = init["login"].as_str().expect("a login url").to_string();
    let poll_token = init["poll"]["token"]
        .as_str()
        .expect("a poll token")
        .to_string();
    let flow_token = login_url.rsplit('/').next().unwrap().to_string();

    // A human, in a browser, already logged in.
    let login_req = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(format!(
            r#"{{"username":"{username}","password":"{password}"}}"#
        )))
        .unwrap();
    let resp = router.clone().oneshot(login_req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "session login");
    let cookie = resp
        .headers()
        .get("set-cookie")
        .and_then(|v| v.to_str().ok())
        .and_then(|c| c.split(';').next())
        .expect("a session cookie")
        .to_string();

    // The consent screen, rendered for that logged-in browser.
    let consent_req = Request::builder()
        .uri(format!("/index.php/login/v2/flow/{flow_token}"))
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .body(Body::empty())
        .unwrap();
    let resp = router.clone().oneshot(consent_req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "consent screen");
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    let state_token = html
        .split("name=\"stateToken\" value=\"")
        .nth(1)
        .and_then(|s| s.split('"').next())
        .expect("a state token in the consent form")
        .to_string();

    // The human clicks "Grant access".
    let grant_req = Request::builder()
        .method("POST")
        .uri("/index.php/login/v2/grant")
        .header("Host", "localhost")
        .header("Origin", "https://localhost:8443")
        .header("Cookie", &cookie)
        .header("Content-Type", "application/x-www-form-urlencoded")
        .body(Body::from(format!(
            "flowToken={flow_token}&stateToken={state_token}&scope={scope}"
        )))
        .unwrap();
    let resp = router.clone().oneshot(grant_req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "grant");

    // The client, polling.
    let poll_req = Request::builder()
        .method("POST")
        .uri("/index.php/login/v2/poll")
        .header("Host", "localhost")
        .header("Content-Type", "application/x-www-form-urlencoded")
        .body(Body::from(format!("token={poll_token}")))
        .unwrap();
    let resp = router.clone().oneshot(poll_req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "poll");
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let creds: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    FlowCreds {
        app_password: creds["appPassword"]
            .as_str()
            .expect("an appPassword")
            .to_string(),
        login_name: creds["loginName"]
            .as_str()
            .expect("a loginName")
            .to_string(),
    }
}

/// The whole point: a real client, after walking the whole flow and picking
/// "Full access", can actually use what it was handed — over OCS (its own
/// capabilities and its own account info) and over WebDAV (its own files
/// root and a file inside it) alike. Fails on the pre-fix code with `403` on
/// both OCS calls and `404` on the PROPFIND.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_flow_issued_full_access_token_can_read_ocs_and_browse_dav() {
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

    let creds = run_login_flow_v2(&f.app, "erin", "correct-horse-battery", "full").await;
    assert_eq!(creds.login_name, "erin");
    assert!(creds.app_password.starts_with("stow_"));

    let auth_header = basic_app_password(&creds.app_password);

    let caps = Request::builder()
        .uri("/ocs/v2.php/cloud/capabilities")
        .header("Host", "localhost")
        .header("Authorization", &auth_header)
        .header("OCS-APIREQUEST", "true")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(caps).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::OK,
        "a flow-issued full-access token must read its own OCS capabilities"
    );

    let who = Request::builder()
        .uri("/ocs/v2.php/cloud/user")
        .header("Host", "localhost")
        .header("Authorization", &auth_header)
        .header("OCS-APIREQUEST", "true")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(who).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::OK,
        "a flow-issued full-access token must read its own OCS account info"
    );

    let root = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/erin/")
        .header("Host", "localhost")
        .header("Authorization", &auth_header)
        .header("Depth", "1")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(root).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::MULTI_STATUS,
        "a flow-issued full-access token must be able to browse its own files root"
    );
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let body = String::from_utf8_lossy(&bytes);
    assert!(
        body.contains("/remote.php/dav/files/erin/files/"),
        "the files root must list the caller's grant-projected root(s) as children: {body}"
    );
}

/// A scope-restricted token minted the same real way is still narrowed
/// exactly as before — the fix for "full" must not turn "readonly" into a
/// loophole.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_flow_issued_readonly_token_still_cannot_write() {
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
    let label = f
        .app
        .core
        .roots(uid)
        .first()
        .map(|r| r.label.clone())
        .expect("a projected root");

    let creds = run_login_flow_v2(&f.app, "frank", "correct-horse-battery", "readonly").await;

    let put = dav_status(
        &f.app,
        "PUT",
        &format!("/remote.php/dav/files/frank/{label}/new.txt"),
        &creds.app_password,
    )
    .await;
    assert_eq!(
        put,
        StatusCode::FORBIDDEN,
        "a flow-issued readonly token must still be refused a write"
    );

    let caps = Request::builder()
        .uri("/ocs/v2.php/cloud/capabilities")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&creds.app_password))
        .header("OCS-APIREQUEST", "true")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(caps).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "unchanged, documented behavior: a genuinely restricted scope has no mapping to prove \
         itself against on OCS and fails closed there, same as before this fix"
    );
}

// =========================================================== files_root_propfind ==
//
// `PROPFIND /remote.php/dav/files/{user}/` (and its `/remote.php/webdav/`
// alias) on the empty relative path — the caller's files root itself. Kept
// separate from the login-flow tests above because the bug it guards is
// independent of how the token was minted: even a hand-issued, fully
// unrestricted app password 404s here on the pre-fix code, because
// `sc_core::Core::resolve_want` refuses an empty vpath unconditionally and
// nothing translated the bare files-root request into one of the caller's
// grant-projected labels.

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_files_root_lists_every_grant_projected_root_as_a_child() {
    let f = two_share_fixture();
    let (uid, _share_a, _share_b) = user_with_two_shares(&f);
    let token = issue_share_scoped(&f, uid, None);

    let depth0 = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/bob/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&token))
        .header("Depth", "0")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(depth0).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::MULTI_STATUS,
        "Depth: 0 must answer, not 404"
    );
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let body = String::from_utf8_lossy(&bytes);
    assert!(
        !body.contains("sharea"),
        "Depth: 0 must not list children: {body}"
    );

    let depth1 = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/bob/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&token))
        .header("Depth", "1")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(depth1).await.unwrap();
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let body = String::from_utf8_lossy(&bytes);
    assert!(body.contains("/remote.php/dav/files/bob/sharea/"), "{body}");
    assert!(body.contains("/remote.php/dav/files/bob/shareb/"), "{body}");

    // A share-restricted token gets no filtered view of this synthetic
    // listing — it is refused outright, same as the existing "Unverifiable"
    // rule elsewhere in this same gate (`app::DavShapedPaths::vpath_of`'s doc
    // comment): the bare files root has no single vpath for
    // `Core::resolve_share` to check a `Scope::shares` restriction against
    // (`dav_shaped.vpath_of` yields `Some("")` here, and an empty vpath is
    // `NotFound` unconditionally — `sc-core/src/resolve.rs`), so
    // `dav_authenticate`'s `shares_deny` half fails closed before this
    // handler is ever reached. Documented here as current, deliberate
    // behavior rather than silently relied upon: a share-scoped credential
    // can still browse its own share directly by label
    // (`a_share_scoped_app_password_is_refused_on_the_other_shares_compat_path`
    // above), it just cannot use the synthetic multi-root discovery view.
    let restricted = issue_share_scoped(&f, uid, Some(vec![_share_a]));
    let depth1_restricted = Request::builder()
        .method("PROPFIND")
        .uri("/remote.php/dav/files/bob/")
        .header("Host", "localhost")
        .header("Authorization", basic_app_password(&restricted))
        .header("Depth", "1")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(depth1_restricted).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "a share-scoped token has no vpath to prove itself against at the bare files root, \
         so it fails closed there rather than leaking a filtered listing"
    );
}
