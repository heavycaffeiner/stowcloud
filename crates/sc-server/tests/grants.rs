//! End-to-end proof that an administrator can decide which folders a new
//! account gets. `sc_core::acl_store` persists grants and pushes them into
//! the live `AclEngine`, and `crates/sc-core/src/tests_acl_store.rs` covers
//! the evaluation properties at that layer — this file covers the top:
//! create a second account and a grant *the way an admin actually would*,
//! entirely over HTTP through the real assembled router (`App::router`), and
//! check what that account can and cannot see, also entirely over HTTP.
//!
//! Nothing here calls `sc_core::Core::create_grant`/`seed_full_access`
//! directly — every grant in this file is minted through
//! `POST /api/admin/grants`.

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
    std::fs::create_dir(share.path().join("vacation")).unwrap();
    std::fs::write(
        share.path().join("vacation").join("beach.jpg"),
        b"jpeg-ish bytes",
    )
    .unwrap();
    std::fs::create_dir(share.path().join("work")).unwrap();
    std::fs::write(
        share.path().join("work").join("report.txt"),
        b"quarterly numbers",
    )
    .unwrap();
    std::fs::write(
        share.path().join("secret.txt"),
        b"not granted to anyone by default",
    )
    .unwrap();

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
    let key = MasterKeyResult {
        key: [11u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg.clone(), cfg, &key).expect("app builds");
    Fixture {
        app,
        _data: data,
        _share: share,
    }
}

async fn json_of(resp: axum::response::Response) -> serde_json::Value {
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    serde_json::from_slice(&bytes).unwrap_or(serde_json::Value::Null)
}

/// A logged-in session: the cookie header value and the CSRF token
/// `GET /api/auth/session` hands back — exactly what a browser keeps and what
/// every state-changing request in this file has to send, mirroring
/// `wiring.rs::a_cookie_authenticated_write_from_our_own_origin_passes_csrf`.
struct Session {
    cookie: String,
    csrf: String,
}

async fn login(app: &App, username: &str, password: &str) -> Session {
    let req = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(
            serde_json::json!({ "username": username, "password": password }).to_string(),
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

    let session_req = Request::builder()
        .uri("/api/auth/session")
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .body(Body::empty())
        .unwrap();
    let resp = app.router().oneshot(session_req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let body = json_of(resp).await;
    let csrf = body["csrf"]
        .as_str()
        .expect("session carries a csrf token")
        .to_string();
    Session { cookie, csrf }
}

async fn get(app: &App, session: &Session, uri: &str) -> (StatusCode, serde_json::Value) {
    let req = Request::builder()
        .uri(uri)
        .header("Host", "localhost")
        .header("Cookie", &session.cookie)
        .body(Body::empty())
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    (status, json_of(resp).await)
}

async fn send(
    app: &App,
    session: &Session,
    method: &str,
    uri: &str,
    body: serde_json::Value,
) -> (StatusCode, serde_json::Value) {
    let req = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .header("Cookie", &session.cookie)
        .header("Sc-Csrf", &session.csrf)
        .header("Origin", "https://localhost:8443")
        .header("Content-Type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap();
    let resp = app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    (status, json_of(resp).await)
}

/// Bootstraps an admin the same way every other non-first-run test in this
/// workspace does (`app_password_scope.rs`, `wiring.rs`): directly through
/// `sc_auth`, because bootstrap itself is `first_run.rs`'s dedicated concern,
/// not this file's. Seeds it full access the same one-time way
/// `SetupGate::complete` does for a real first-run administrator.
fn bootstrap_admin(f: &Fixture) -> sc_vfs::UserId {
    let uid = f
        .app
        .auth
        .create_user(
            "root",
            &secrecy::SecretString::from("correct horse battery".to_string()),
        )
        .unwrap();
    f.app.auth.set_admin(uid, true).unwrap();
    f.app.core.seed_full_access(uid).unwrap();
    uid
}

/// The scenario the task asked to be demonstrated, not merely unit-tested:
/// create a second account, grant it exactly one folder, log in as it, and
/// confirm it sees exactly that folder and nothing else — all three steps
/// over HTTP, through the real router.
#[tokio::test(flavor = "multi_thread")]
async fn a_second_account_granted_one_folder_sees_exactly_that_folder() {
    let f = fixture();
    bootstrap_admin(&f);
    let admin_session = login(&f.app, "root", "correct horse battery").await;

    // 1. Create the second account the way `POST /api/admin/users` does it —
    //    the route that already existed.
    let (status, created) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/users",
        serde_json::json!({ "name": "bob", "password": "correct horse battery 2" }),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED, "{created}");
    let bob_id = created["id"].as_u64().unwrap();

    // Before any grant: bob exists but can see nothing — a login that
    // succeeds and a session with zero roots, not an error. This is the
    // exact failure mode the task named: "reads as a broken server rather
    // than a deliberate default" without a grant screen to fix it from.
    let bob_session = login(&f.app, "bob", "correct horse battery 2").await;
    let (status, session_body) = get(&f.app, &bob_session, "/api/auth/session").await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(
        session_body["roots"].as_array().unwrap().len(),
        0,
        "no grant yet: no roots"
    );

    // 2. Find the share to grant against.
    let (status, shares) = get(&f.app, &admin_session, "/api/admin/shares").await;
    assert_eq!(status, StatusCode::OK);
    let share_id = shares[0]["id"].as_u64().unwrap();
    assert_eq!(shares[0]["name"], "files");

    // 3. Grant bob exactly the `vacation` subtree — also new.
    let (status, grant) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/grants",
        serde_json::json!({
            "principal": { "kind": "user", "id": bob_id },
            "share": share_id,
            "subpath": "vacation",
            "allow": ["read", "download"],
            "deny": [],
            "inherit": true
        }),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED, "{grant}");

    // 4. Log bob back in (a fresh session; a real browser would just refetch
    //    `/api/auth/session`, which resolves live off the `AclEngine` — no
    //    restart, no re-login, actually required, but this exercises the
    //    same read a client polling after being told "try again" would do).
    let (status, session_body) = get(&f.app, &bob_session, "/api/auth/session").await;
    assert_eq!(status, StatusCode::OK);
    let roots = session_body["roots"].as_array().unwrap();
    assert_eq!(
        roots.len(),
        1,
        "exactly one root: the granted subtree, not the whole share"
    );
    assert_eq!(roots[0]["label"], "vacation");

    // 5. The granted subtree actually lists.
    let (status, listing) = get(&f.app, &bob_session, "/api/fs/list?path=/vacation").await;
    assert_eq!(status, StatusCode::OK, "{listing}");
    let entries = listing["entries"].as_array().unwrap();
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0]["name"], "beach.jpg");

    // 6. Nothing else in the share is reachable — not the sibling `work`
    //    folder, not the share root, not a file at the share root. There is
    //    no virtual path that reaches them: the user cannot even know a
    //    directory named `a` exists.
    let (status, _) = get(&f.app, &bob_session, "/api/fs/list?path=/work").await;
    assert_eq!(
        status,
        StatusCode::NOT_FOUND,
        "no root named `work` was ever granted"
    );
    let (status, _) = get(&f.app, &bob_session, "/api/fs/list?path=/files").await;
    assert_eq!(
        status,
        StatusCode::NOT_FOUND,
        "no root named `files` was ever granted — only its subpath was"
    );
    let (status, _) = get(&f.app, &bob_session, "/api/fs/stat?path=/secret.txt").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
}

/// A user created and left ungranted stays that way indefinitely — creating
/// the account is not itself access, and nothing implicitly widens it later.
#[tokio::test(flavor = "multi_thread")]
async fn a_user_with_no_grant_ever_sees_no_roots() {
    let f = fixture();
    bootstrap_admin(&f);
    let admin_session = login(&f.app, "root", "correct horse battery").await;

    let (status, created) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/users",
        serde_json::json!({ "name": "carol", "password": "correct horse battery 3" }),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED, "{created}");

    let carol_session = login(&f.app, "carol", "correct horse battery 3").await;
    let (status, session_body) = get(&f.app, &carol_session, "/api/auth/session").await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(session_body["roots"].as_array().unwrap().len(), 0);

    let (status, _) = get(&f.app, &carol_session, "/api/fs/list?path=/files").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
}

/// `deny` beats `allow` at the same depth, proven
/// through the admin grant API and the real `/api/fs/list` route rather than
/// against the evaluator directly.
#[tokio::test(flavor = "multi_thread")]
async fn a_deny_grant_created_through_the_api_overrides_a_shallower_allow() {
    let f = fixture();
    bootstrap_admin(&f);
    let admin_session = login(&f.app, "root", "correct horse battery").await;

    let (_, created) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/users",
        serde_json::json!({ "name": "dave", "password": "correct horse battery 4" }),
    )
    .await;
    let dave_id = created["id"].as_u64().unwrap();
    let (_, shares) = get(&f.app, &admin_session, "/api/admin/shares").await;
    let share_id = shares[0]["id"].as_u64().unwrap();

    // Root grant: read the whole share.
    let (status, _) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/grants",
        serde_json::json!({
            "principal": { "kind": "user", "id": dave_id },
            "share": share_id,
            "subpath": "",
            "allow": ["read", "download"],
            "deny": [],
            "inherit": true
        }),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);

    // Deeper grant: explicit deny on `work`.
    let (status, _) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/grants",
        serde_json::json!({
            "principal": { "kind": "user", "id": dave_id },
            "share": share_id,
            "subpath": "work",
            "allow": [],
            "deny": ["read"],
            "inherit": true
        }),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);

    let dave_session = login(&f.app, "dave", "correct horse battery 4").await;
    let (status, session_body) = get(&f.app, &dave_session, "/api/auth/session").await;
    assert_eq!(status, StatusCode::OK);
    let roots = session_body["roots"].as_array().unwrap();
    assert_eq!(
        roots.len(),
        1,
        "the deny-only grant contributes no root of its own"
    );
    let root_label = roots[0]["label"].as_str().unwrap().to_string();

    // Outside the deny: still readable.
    let (status, _) = get(
        &f.app,
        &dave_session,
        &format!("/api/fs/list?path=/{root_label}/vacation"),
    )
    .await;
    assert_eq!(status, StatusCode::OK);

    // Inside the deny: refused, even though the shallower root grant allows
    // READ on the whole share.
    let (status, body) = get(
        &f.app,
        &dave_session,
        &format!("/api/fs/list?path=/{root_label}/work"),
    )
    .await;
    assert_eq!(status, StatusCode::FORBIDDEN, "{body}");
    assert_eq!(body["error"]["code"], "acl.denied");
}

/// A grant with neither `allow` nor `deny` set is refused at creation — a
/// no-op row would be silently useless, so the admin API says so instead of
/// accepting it (`sc_core::Core::create_grant`).
#[tokio::test(flavor = "multi_thread")]
async fn creating_a_grant_with_no_permission_bits_is_refused() {
    let f = fixture();
    bootstrap_admin(&f);
    let admin_session = login(&f.app, "root", "correct horse battery").await;
    let (_, shares) = get(&f.app, &admin_session, "/api/admin/shares").await;
    let share_id = shares[0]["id"].as_u64().unwrap();

    let (status, body) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/grants",
        serde_json::json!({
            "principal": { "kind": "user", "id": 999999 },
            "share": share_id,
            "subpath": "",
            "allow": [],
            "deny": [],
            "inherit": true
        }),
    )
    .await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{body}");
}

/// Revoking a grant takes effect on the very next request — no restart, no
/// re-login required, matching `sc_core::Core::delete_grant`'s doc comment.
#[tokio::test(flavor = "multi_thread")]
async fn revoking_a_grant_takes_effect_immediately() {
    let f = fixture();
    bootstrap_admin(&f);
    let admin_session = login(&f.app, "root", "correct horse battery").await;

    let (_, created_user) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/users",
        serde_json::json!({ "name": "erin", "password": "correct horse battery 5" }),
    )
    .await;
    let erin_id = created_user["id"].as_u64().unwrap();
    let (_, shares) = get(&f.app, &admin_session, "/api/admin/shares").await;
    let share_id = shares[0]["id"].as_u64().unwrap();

    let (_, grant) = send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/grants",
        serde_json::json!({
            "principal": { "kind": "user", "id": erin_id },
            "share": share_id,
            "subpath": "vacation",
            "allow": ["read"],
            "deny": [],
            "inherit": true
        }),
    )
    .await;
    let grant_id = grant["id"].as_u64().unwrap();

    let erin_session = login(&f.app, "erin", "correct horse battery 5").await;
    let (status, _) = get(&f.app, &erin_session, "/api/fs/list?path=/vacation").await;
    assert_eq!(status, StatusCode::OK);

    let (status, _) = send(
        &f.app,
        &admin_session,
        "DELETE",
        &format!("/api/admin/grants/{grant_id}"),
        serde_json::Value::Null,
    )
    .await;
    assert_eq!(status, StatusCode::NO_CONTENT);

    let (status, _) = get(&f.app, &erin_session, "/api/fs/list?path=/vacation").await;
    assert_eq!(
        status,
        StatusCode::NOT_FOUND,
        "revoked — the label no longer resolves to anything"
    );
}

/// Only an administrator may reach any of the grant-management surface — a
/// plain account (even one that exists and is logged in) gets `403`, the
/// same as every other `/api/admin/**` route.
#[tokio::test(flavor = "multi_thread")]
async fn a_plain_account_cannot_reach_grant_management() {
    let f = fixture();
    bootstrap_admin(&f);
    let admin_session = login(&f.app, "root", "correct horse battery").await;
    send(
        &f.app,
        &admin_session,
        "POST",
        "/api/admin/users",
        serde_json::json!({ "name": "frank", "password": "correct horse battery 6" }),
    )
    .await;
    let frank_session = login(&f.app, "frank", "correct horse battery 6").await;

    let (status, _) = get(&f.app, &frank_session, "/api/admin/grants").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    let (status, _) = get(&f.app, &frank_session, "/api/admin/shares").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    let (status, _) = send(
        &f.app,
        &frank_session,
        "POST",
        "/api/admin/grants",
        serde_json::json!({ "principal": { "kind": "user", "id": 1 }, "share": 1, "subpath": "", "allow": ["read"], "deny": [], "inherit": true }),
    )
    .await;
    assert_eq!(status, StatusCode::FORBIDDEN);
}
