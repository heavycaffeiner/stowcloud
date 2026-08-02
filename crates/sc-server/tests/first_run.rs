//! End-to-end proof that an assembled `sc-server` can be *bootstrapped* —
//! that a fresh deployment reaches a state where a human can log in.
//!
//! This is deliberately separate from `wiring.rs`. Every other test in this
//! workspace that needs an account calls `sc_auth::AuthService::create_user`
//! directly, which is exactly why the gap this file covers went unnoticed:
//! the suite was green while the assembled product had no route, no CLI path
//! and no endpoint that could create the first account. So nothing here is
//! allowed to touch `create_user`. Everything goes over HTTP, through the
//! real router, against a real auth database, exactly as an operator would.

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_server::app::App;
use sc_server::config::Config;
use sc_server::masterkey::MasterKeyResult;
use tower::ServiceExt;

struct Fixture {
    app: App,
    data: tempfile::TempDir,
}

/// Build an app over a fresh data dir and arm the first-run gate, which is
/// what `cmd_serve` does at startup.
fn armed_fixture() -> (Fixture, String) {
    let f = unarmed_fixture();
    assert!(
        f.app.setup.arm_for_first_run().expect("arm"),
        "a fresh data dir has no account"
    );
    let token = sc_server::setup::read_existing(f.data.path())
        .expect("token file")
        .token;
    (f, token)
}

fn unarmed_fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![],
        // `Config::default()`'s three-entry `app_hosts` is deliberately
        // ambiguous for `resolve_compat_canonical_url` (`app.rs`).
        compat_canonical_url: Some("https://localhost".into()),
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [7u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg, &key).expect("app builds");
    Fixture { app, data }
}

async fn get_setup(f: &Fixture) -> (StatusCode, serde_json::Value) {
    let req = Request::builder()
        .uri("/api/setup")
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    (status, serde_json::from_slice(&bytes).unwrap())
}

async fn post_setup(
    f: &Fixture,
    token: &str,
    username: &str,
    password: &str,
) -> (StatusCode, serde_json::Value) {
    let body = serde_json::json!({ "token": token, "username": username, "password": password });
    let req = Request::builder()
        .method("POST")
        .uri("/api/setup")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    let status = resp.status();
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    (status, serde_json::from_slice(&bytes).unwrap())
}

async fn login(f: &Fixture, username: &str, password: &str) -> StatusCode {
    let body = serde_json::json!({ "username": username, "password": password });
    let req = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap();
    f.app.router().oneshot(req).await.unwrap().status()
}

/// The gap, stated as a test: a freshly assembled server can be turned into
/// one a human can sign in to, using nothing but HTTP and the token the
/// process printed at startup.
#[tokio::test(flavor = "multi_thread")]
async fn a_fresh_deployment_can_be_bootstrapped_and_then_logged_into() {
    let (f, token) = armed_fixture();

    let (status, body) = get_setup(&f).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(body["required"], serde_json::json!(true));

    let (status, body) = post_setup(&f, &token, "admin", "correct horse battery").await;
    assert_eq!(status, StatusCode::CREATED, "{body}");
    assert_eq!(body["user"]["name"], serde_json::json!("admin"));

    assert_eq!(
        login(&f, "admin", "correct horse battery").await,
        StatusCode::OK
    );

    let (_, body) = get_setup(&f).await;
    assert_eq!(body["required"], serde_json::json!(false));
}

#[tokio::test(flavor = "multi_thread")]
async fn the_token_works_exactly_once_over_http() {
    let (f, token) = armed_fixture();
    assert_eq!(
        post_setup(&f, &token, "admin", "correct horse battery")
            .await
            .0,
        StatusCode::CREATED
    );

    let (status, body) = post_setup(&f, &token, "admin2", "correct horse battery").await;
    assert_eq!(status, StatusCode::GONE);
    assert_eq!(body["error"]["code"], serde_json::json!("setup.completed"));
    assert_eq!(f.app.auth.list_users().unwrap().len(), 1);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_wrong_token_is_refused_over_http() {
    let (f, token) = armed_fixture();
    let mut wrong = token.clone();
    wrong.pop();
    wrong.push(if token.ends_with('a') { 'b' } else { 'a' });

    let (status, body) = post_setup(&f, &wrong, "admin", "correct horse battery").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert_eq!(
        body["error"]["code"],
        serde_json::json!("setup.invalid_token")
    );
    assert!(f.app.auth.list_users().unwrap().is_empty());

    // Still bootstrappable with the real token — a failed attempt is not a
    // spent one.
    assert_eq!(
        post_setup(&f, &token, "admin", "correct horse battery")
            .await
            .0,
        StatusCode::CREATED
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn an_expired_token_is_refused_over_http() {
    let f = unarmed_fixture();
    let expired = sc_server::setup::SetupToken {
        token: "expired-token".into(),
        expires_at_unix: 1, // 1970
    };
    f.app.setup.arm_with(expired);

    let (status, body) = post_setup(&f, "expired-token", "admin", "correct horse battery").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert_eq!(
        body["error"]["code"],
        serde_json::json!("setup.token_expired")
    );
    assert!(f.app.auth.list_users().unwrap().is_empty());

    // Setup is still required — a restart is what re-issues.
    assert_eq!(get_setup(&f).await.1["required"], serde_json::json!(true));
}

#[tokio::test(flavor = "multi_thread")]
async fn a_short_password_is_refused_over_http() {
    let (f, token) = armed_fixture();
    // Nine characters; the minimum is ten.
    let (status, body) = post_setup(&f, &token, "admin", "123456789").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY);
    assert_eq!(
        body["error"]["code"],
        serde_json::json!("setup.weak_password")
    );
    assert_eq!(body["error"]["detail"]["min_length"], serde_json::json!(10));
    assert!(f.app.auth.list_users().unwrap().is_empty());
}

/// "The route is gone once an admin exists" has to survive the process that
/// created the admin. A second `App` over the same data directory is what the
/// next boot builds.
#[tokio::test(flavor = "multi_thread")]
async fn the_route_stays_closed_across_a_restart() {
    let (f, token) = armed_fixture();
    assert_eq!(
        post_setup(&f, &token, "admin", "correct horse battery")
            .await
            .0,
        StatusCode::CREATED
    );
    drop(f.app);

    let cfg = Config {
        data_dir: f.data.path().to_path_buf(),
        shares: vec![],
        compat_canonical_url: Some("https://localhost".into()),
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [7u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let rebooted = Fixture {
        app: App::build(cfg, &key).expect("app rebuilds"),
        data: f.data,
    };

    // Arming on the second boot must issue nothing.
    assert!(!rebooted.app.setup.arm_for_first_run().expect("arm"));
    assert!(sc_server::setup::read_existing(rebooted.data.path()).is_none());

    assert_eq!(
        get_setup(&rebooted).await.1["required"],
        serde_json::json!(false)
    );
    let (status, body) = post_setup(&rebooted, &token, "admin2", "correct horse battery").await;
    assert_eq!(status, StatusCode::GONE);
    assert_eq!(body["error"]["code"], serde_json::json!("setup.completed"));
    assert_eq!(rebooted.app.auth.list_users().unwrap().len(), 1);
}

/// The token file is a `0600` artifact that exists only for the length of the
/// window. Once spent it must not stay readable on disk.
#[tokio::test(flavor = "multi_thread")]
async fn the_token_file_is_removed_when_spent() {
    let (f, token) = armed_fixture();
    assert!(f.data.path().join("setup-token").exists());
    assert_eq!(
        post_setup(&f, &token, "admin", "correct horse battery")
            .await
            .0,
        StatusCode::CREATED
    );
    assert!(!f.data.path().join("setup-token").exists());
}

/// the NT hash is derived at account creation,
/// unconditionally, so SMB can be switched on later without a password reset.
/// The bootstrapped administrator is the account most likely to predate any
/// decision about SMB, so it is the one this matters most for.
#[tokio::test(flavor = "multi_thread")]
async fn the_bootstrapped_admin_is_smb_ready_without_a_password_reset() {
    let (f, token) = armed_fixture();
    let (_, body) = post_setup(&f, &token, "admin", "correct horse battery").await;
    let uid = sc_vfs::UserId::new(body["user"]["id"].as_u64().unwrap() as u32);
    assert!(f.app.auth.nt_hash_present(uid).unwrap());

    // Field 2 is `smb.service_uid + row id`, and has to match the uid this
    // account's passwd entry carries: `pdbedit -i` resolves the line through
    // it and imports nothing, silently, when it names no passwd entry
    // (`export_smbpasswd`). Asserting only `contains("admin:")` passed happily
    // on a bare row id and let a deployment ship that could not authenticate
    // one SMB session. 1000 is `Config::default`'s `smb_service_uid`.
    let line = f.app.auth.export_smbpasswd(1000).unwrap();
    assert_eq!(uid.get(), 1, "the fixture's admin should be row id 1");
    assert!(
        line.starts_with("admin:1001:"),
        "smbpasswd field 2 must be service_uid + row id, got {line:?}"
    );
}

/// The bootstrap writes an audit row, and a refused
/// attempt writes one too.
#[tokio::test(flavor = "multi_thread")]
async fn the_bootstrap_is_audited() {
    let (f, token) = armed_fixture();
    let _ = post_setup(&f, "wrong-token", "admin", "correct horse battery").await;
    assert_eq!(
        post_setup(&f, &token, "admin", "correct horse battery")
            .await
            .0,
        StatusCode::CREATED
    );
    assert_eq!(
        f.app
            .auth
            .audit_count("admin.setup_failed", Some(false))
            .unwrap(),
        1
    );
    assert_eq!(
        f.app
            .auth
            .audit_count("admin.setup_completed", Some(true))
            .unwrap(),
        1
    );
}

/// Building an `App` — which `gc` and `smb-sync` do — must not mint an
/// admin-creation credential as a side effect.
#[tokio::test(flavor = "multi_thread")]
async fn building_an_app_does_not_issue_a_token() {
    let f = unarmed_fixture();
    assert!(!f.data.path().join("setup-token").exists());
    // And with no token in circulation, nothing can be created.
    let (status, _) = post_setup(&f, "guess", "admin", "correct horse battery").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert!(f.app.auth.list_users().unwrap().is_empty());
}
