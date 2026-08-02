//! End-to-end tests through the real `axum::Router`.
//!
//! The unit tests cover the shapes; these cover the wiring — that the OCS
//! entry point actually selects the right envelope version, that the
//! `OCS-APIRequest` guard is actually installed on the OCS surface, and that
//! there is no GET route that can approve a login flow.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_compat_nc::config::NcConfig;
use sc_compat_nc::login_flow::{Clock, LoginFlowService};
use sc_compat_nc::ports::*;
use sc_compat_nc::preview::PreviewApi;
use sc_compat_nc::router::{router, NcState};
use sc_compat_nc::shares::{ShareesApi, SharesApi};
use sc_compat_nc::store::MemStore;
use tower::ServiceExt;

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

struct FakeCore;
impl CorePort for FakeCore {
    fn home_root(&self, _u: UserId) -> PortResult<ShareId> {
        Ok(ShareId(1))
    }
    fn resolve(&self, _r: ShareId, _u: UserId, _p: &str) -> PortResult<Entry> {
        Err(PortError::NotFound)
    }
    fn list(&self, _r: ShareId, _u: UserId, _p: &str) -> PortResult<Vec<Entry>> {
        Ok(vec![])
    }
    fn stat_entry(&self, _r: ShareId, _u: UserId, _p: &str) -> PortResult<Entry> {
        Err(PortError::NotFound)
    }
    fn aggregate(&self, _r: ShareId, _i: FileId) -> PortResult<Aggregate> {
        Ok(Aggregate { etag: String::new(), rsize: 0, rcount: 0 })
    }
    fn user_info(&self, _u: UserId) -> PortResult<UserInfo> {
        Ok(UserInfo {
            id: UserId(1),
            login_name: "alice".into(),
            display_name: "Alice".into(),
            email: None,
            enabled: true,
            groups: vec!["staff".into()],
            language: "ko".into(),
            locale: "ko_KR".into(),
        })
    }
    fn quota(&self, _u: UserId) -> PortResult<Quota> {
        Ok(Quota { used: 42, free: 0, total: None })
    }
    fn locate(&self, _u: UserId, _i: FileId) -> PortResult<(ShareId, String)> {
        Err(PortError::NotFound)
    }
}

struct FakeAuth;
impl AuthPort for FakeAuth {
    fn issue_app_password(&self, _u: UserId, _n: &str, _s: Scope) -> PortResult<(u32, String)> {
        Ok((1, "stow_test".into()))
    }
    fn verify_basic(
        &self,
        login: &str,
        secret: &str,
        _from: sc_compat_nc::ports::ClientAddr,
    ) -> PortResult<Option<Principal>> {
        if login == "alice" && secret == "hunter2" {
            Ok(Some(Principal {
                user: UserId(1),
                login_name: "alice".into(),
                display_name: "Alice".into(),
            }))
        } else {
            Ok(None)
        }
    }
    fn validate_session(&self, _t: &str) -> PortResult<Option<Principal>> {
        Ok(None)
    }
}

/// `FakeAuth` that also recognises one browser session, so a test can drive
/// Login Flow v2 all the way through consent instead of stopping at the
/// unauthenticated redirect.
struct SessionAuth;
impl SessionAuth {
    const SESSION: &'static str = "session-token-for-tests";
}
impl AuthPort for SessionAuth {
    fn issue_app_password(&self, u: UserId, n: &str, s: Scope) -> PortResult<(u32, String)> {
        FakeAuth.issue_app_password(u, n, s)
    }
    fn verify_basic(
        &self,
        login: &str,
        secret: &str,
        from: sc_compat_nc::ports::ClientAddr,
    ) -> PortResult<Option<Principal>> {
        FakeAuth.verify_basic(login, secret, from)
    }
    fn validate_session(&self, t: &str) -> PortResult<Option<Principal>> {
        if t == Self::SESSION {
            Ok(Some(Principal {
                user: UserId(1),
                login_name: "alice".into(),
                display_name: "Alice".into(),
            }))
        } else {
            Ok(None)
        }
    }
}

struct FakeUpload;
impl UploadEngine for FakeUpload {
    fn create(&self, _s: SessionSpec) -> PortResult<(ShareId, SessionId)> {
        Ok((ShareId(1), SessionId([1u8; 16])))
    }
    fn put_named(
        &self,
        _r: ShareId,
        _s: SessionId,
        _u: UserId,
        _n: u32,
        _d: &[u8],
    ) -> PortResult<()> {
        Ok(())
    }
    fn assemble_and_finalize(
        &self,
        _r: ShareId,
        _s: SessionId,
        _u: UserId,
        _t: u64,
        _m: Option<i128>,
    ) -> PortResult<()> {
        Ok(())
    }
    fn list_chunks(&self, _s: SessionId) -> PortResult<Vec<u32>> {
        Ok(vec![])
    }
    fn received_len(&self, _s: SessionId, _u: UserId) -> PortResult<u64> {
        Ok(0)
    }
    fn abort(&self, _s: SessionId, _u: UserId) -> PortResult<()> {
        Ok(())
    }
}

struct FakeShares;
impl SharePort for FakeShares {
    fn list(&self, _u: UserId, _f: &ShareFilter) -> PortResult<Vec<CoreShare>> {
        Ok(vec![])
    }
    fn get(&self, _u: UserId, _i: u64) -> PortResult<CoreShare> {
        Err(PortError::NotFound)
    }
    fn create(&self, _u: UserId, _s: &ShareSpec) -> PortResult<CoreShare> {
        Err(PortError::NotFound)
    }
    fn update(&self, _u: UserId, _i: u64, _s: &ShareSpec) -> PortResult<CoreShare> {
        Err(PortError::NotFound)
    }
    fn delete(&self, _u: UserId, _i: u64) -> PortResult<()> {
        Ok(())
    }
    fn kinds_for(&self, _r: ShareId, _i: FileId) -> PortResult<Vec<GranteeKind>> {
        Ok(vec![])
    }
    fn find_grantees(
        &self,
        _u: UserId,
        _q: &str,
        _s: GranteeScope,
    ) -> PortResult<Vec<GranteeCandidate>> {
        Ok(vec![])
    }
    fn link_url(&self, t: &str) -> String {
        format!("https://cloud.example.com/s/{t}")
    }
}

struct FakePreview;
impl PreviewPort for FakePreview {
    fn can_preview(&self, _e: &Entry) -> bool {
        false
    }
    fn signed_thumb_url(
        &self,
        _u: UserId,
        _r: ShareId,
        _p: &str,
        _w: u32,
        _h: u32,
        _f: FitMode,
    ) -> PortResult<Option<String>> {
        Ok(None)
    }
}

struct FixedClock;
impl Clock for FixedClock {
    fn now_ns(&self) -> i64 {
        1_000_000_000_000
    }
}

fn app() -> axum::Router {
    app_with_auth(Arc::new(FakeAuth))
}

fn app_with_auth(auth: Arc<dyn AuthPort>) -> axum::Router {
    app_with_cfg(
        NcConfig {
            canonical_url: "https://cloud.example.com".into(),
            ..NcConfig::default()
        },
        auth,
    )
}

fn app_with_cfg(cfg: NcConfig, auth: Arc<dyn AuthPort>) -> axum::Router {
    let cfg = Arc::new(cfg);
    let store: Arc<dyn NcStore> = Arc::new(MemStore::with_instance_id("ocTESTINST"));
    let deps = Deps {
        core: Arc::new(FakeCore),
        auth,
        upload: Arc::new(FakeUpload),
        shares: Arc::new(FakeShares),
        preview: Arc::new(FakePreview),
    };
    let login = Arc::new(LoginFlowService::new(
        store.clone(),
        deps.auth.clone(),
        cfg.clone(),
        Arc::new(FixedClock),
    ));
    router(NcState {
        cfg: cfg.clone(),
        store,
        deps: deps.clone(),
        login,
        shares: Arc::new(SharesApi::new(deps.shares.clone())),
        sharees: Arc::new(ShareesApi::new(deps.shares.clone(), cfg.clone())),
        preview: Arc::new(PreviewApi::new(deps.core.clone(), deps.preview.clone())),
    })
}

use sc_compat_nc::store::NcStore;

/// Drive Login Flow v2 consent for `flow` the way the browser does: fetch the
/// landing page (which mints the `stateToken`) with a session cookie, then POST
/// both tokens back. Needs a router built with [`SessionAuth`].
///
/// A helper rather than copy-paste because two tests need a *completed* flow to
/// prove what they are about: since a pending poll, an unknown token and a
/// throttled poll all answer `404` on purpose, the only unambiguous evidence
/// that a token was extracted and looked up is that polling it yields the
/// credentials.
async fn approve(a: &axum::Router, flow: &str) {
    let cookie = format!("{}={}", NcConfig::default().session_cookie, SessionAuth::SESSION);
    let landing = a
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/index.php/login/v2/flow/{flow}"))
                .header("Cookie", &cookie)
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(landing.status(), StatusCode::OK, "consent page for a logged-in browser");
    let html = String::from_utf8_lossy(
        &axum::body::to_bytes(landing.into_body(), 1 << 20).await.unwrap(),
    )
    .into_owned();
    let state = html
        .split(r#"name="stateToken" value=""#)
        .nth(1)
        .and_then(|s| s.split('"').next())
        .expect("consent page carries a stateToken")
        .to_string();
    let granted = a
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2/grant")
                .header("Cookie", &cookie)
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from(format!("flowToken={flow}&stateToken={state}&scope=full")))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(granted.status(), StatusCode::OK, "consent must succeed");
}

/// `POST /index.php/login/v2` as `ua`, returning `(poll_token, flow_token)`.
async fn begin_flow(a: &axum::Router, ua: &str) -> (String, String) {
    let resp = a
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2")
                .header("User-Agent", ua)
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    let j: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let poll = j["poll"]["token"].as_str().unwrap().to_string();
    let flow = j["login"].as_str().unwrap().rsplit('/').next().unwrap().to_string();
    (poll, flow)
}

async fn call(req: Request<Body>) -> (StatusCode, String, Option<String>) {
    let resp = app().oneshot(req).await.unwrap();
    let status = resp.status();
    let ct = resp
        .headers()
        .get(axum::http::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    (status, String::from_utf8_lossy(&bytes).into_owned(), ct)
}

fn ocs_get(uri: &str) -> Request<Body> {
    Request::builder()
        .uri(uri)
        .header("OCS-APIRequest", "true")
        .body(Body::empty())
        .unwrap()
}

fn ocs_get_authed(uri: &str) -> Request<Body> {
    // base64("alice:hunter2")
    Request::builder()
        .uri(uri)
        .header("OCS-APIRequest", "true")
        .header("Authorization", "Basic YWxpY2U6aHVudGVyMg==")
        .body(Body::empty())
        .unwrap()
}

// ---------------------------------------------------------------------------
// status.php
// ---------------------------------------------------------------------------

#[tokio::test]
async fn status_php() {
    let (st, body, ct) = call(
        Request::builder()
            .uri("/status.php")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::OK);
    assert_eq!(ct.as_deref(), Some("application/json"));
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["installed"], true);
    assert_eq!(j["maintenance"], false);
    assert_eq!(j["productname"], "Nextcloud");
    assert_eq!(j["versionstring"], "31.0.4");
}

// ---------------------------------------------------------------------------
// OCS envelope
// ---------------------------------------------------------------------------

#[tokio::test]
async fn ocs_api_request_header_is_mandatory() {
    // No header at all.
    let (st, body, _) = call(
        Request::builder()
            .uri("/ocs/v2.php/cloud/capabilities?format=json")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::UNAUTHORIZED);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["ocs"]["meta"]["status"], "failure");
    // 997 = RESPOND_UNAUTHORISED, the legacy sentinel both versions promote
    // to an HTTP 401.
    assert_eq!(j["ocs"]["meta"]["statuscode"], 997);

    // Present but not "true".
    let (st, _, _) = call(
        Request::builder()
            .uri("/ocs/v2.php/cloud/capabilities?format=json")
            .header("OCS-APIRequest", "false")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::UNAUTHORIZED);

    // v1 promotes 997 into HTTP 401 too — it is the one code v1 ever leaks
    // into the HTTP status line.
    let (st, body, _) = call(
        Request::builder()
            .uri("/ocs/v1.php/cloud/capabilities?format=json")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::UNAUTHORIZED);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["ocs"]["meta"]["statuscode"], 997);
}

#[tokio::test]
async fn ocs_v1_vs_v2_success_codes_json() {
    let (st, body, ct) = call(ocs_get("/ocs/v2.php/cloud/capabilities?format=json")).await;
    assert_eq!(st, StatusCode::OK);
    assert_eq!(ct.as_deref(), Some("application/json; charset=utf-8"));
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["ocs"]["meta"]["statuscode"], 200, "v2 success is 200");
    assert_eq!(j["ocs"]["meta"]["status"], "ok");
    assert_eq!(j["ocs"]["meta"]["message"], "OK");

    let (st, body, _) = call(ocs_get("/ocs/v1.php/cloud/capabilities?format=json")).await;
    assert_eq!(st, StatusCode::OK);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["ocs"]["meta"]["statuscode"], 100, "v1 success is 100");
    assert_eq!(j["ocs"]["meta"]["status"], "ok");
    // v1 always carries the pagination pair as empty strings.
    assert_eq!(j["ocs"]["meta"]["totalitems"], "");
    assert_eq!(j["ocs"]["meta"]["itemsperpage"], "");
}

#[tokio::test]
async fn ocs_defaults_to_xml_without_a_format_parameter() {
    let (st, body, ct) = call(ocs_get("/ocs/v2.php/cloud/capabilities")).await;
    assert_eq!(st, StatusCode::OK);
    assert_eq!(ct.as_deref(), Some("application/xml; charset=utf-8"));
    assert!(body.starts_with("<?xml version=\"1.0\"?>"));
    assert!(body.contains("<ocs>"));
    assert!(body.contains("<status>ok</status>"));
    assert!(body.contains("<statuscode>200</statuscode>"));
    assert!(body.contains("<message>OK</message>"));
    // v1 XML additionally carries the pagination pair.
    let (_, body1, _) = call(ocs_get("/ocs/v1.php/cloud/capabilities")).await;
    assert!(body1.contains("<statuscode>100</statuscode>"));
    assert!(body1.contains("<totalitems/>"));
    assert!(body1.contains("<itemsperpage/>"));
}

#[tokio::test]
async fn v2_maps_the_ocs_code_into_the_http_status() {
    // An unrouted OCS path answers 998 -> HTTP 404 on v2.
    let (st, body, _) = call(ocs_get("/ocs/v2.php/nope/nope?format=json")).await;
    assert_eq!(st, StatusCode::NOT_FOUND);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["ocs"]["meta"]["statuscode"], 998);
    assert!(j["ocs"]["data"].as_array().unwrap().is_empty());

    // ...and HTTP 200 with the same OCS code on v1.
    let (st, body, _) = call(ocs_get("/ocs/v1.php/nope/nope?format=json")).await;
    assert_eq!(st, StatusCode::OK);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(j["ocs"]["meta"]["statuscode"], 998);
}

// ---------------------------------------------------------------------------
// capabilities
// ---------------------------------------------------------------------------

#[tokio::test]
async fn capabilities_marks_unsupported_features_false_over_http() {
    let (_, body, _) = call(ocs_get("/ocs/v2.php/cloud/capabilities?format=json")).await;
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    let d = &j["ocs"]["data"];

    assert_eq!(d["version"]["major"], 31);
    assert_eq!(d["version"]["string"], "31.0.4");

    let c = &d["capabilities"];
    assert_eq!(c["files"]["versioning"], false);
    assert_eq!(c["files"]["comments"], false);
    assert_eq!(c["files_sharing"]["federation"]["outgoing"], false);
    assert_eq!(c["files_sharing"]["federation"]["incoming"], false);
    assert_eq!(c["user_status"]["enabled"], false);
    assert_eq!(c["systemtags"]["enabled"], false);
    assert_eq!(c["end-to-end-encryption"]["enabled"], false);
    assert_eq!(c["core"]["reference-api"], false);
    // `activity` must NOT appear: both mobile clients gate the feature on the
    // key's presence rather than its contents, so any value — including an
    // empty object — switches the activity UI on and makes them poll an
    // endpoint we answer with 404. See `capabilities::activity_caps`.
    assert!(c.get("activity").is_none());

    // Chunking on, bulkupload absent (presence is the signal there).
    assert_eq!(c["dav"]["chunking"], "1.0");
    assert!(c["dav"].get("bulkupload").is_none());

    // The advisory chunk size.
    assert_eq!(c["files"]["chunked_upload"]["max_size"], 10 * 1024 * 1024);

    // The sharee floor is advertised so clients stop typing below it.
    assert_eq!(c["files_sharing"]["sharee"]["minSearchStringLength"], 3);
    assert_eq!(c["files_sharing"]["sharee"]["query_lookup_default"], false);
}

// ---------------------------------------------------------------------------
// cloud/user
// ---------------------------------------------------------------------------

#[tokio::test]
async fn cloud_user_requires_auth_and_reports_unlimited_quota_as_minus_three() {
    let (st, _, _) = call(ocs_get("/ocs/v2.php/cloud/user?format=json")).await;
    assert_eq!(st, StatusCode::UNAUTHORIZED);

    let (st, body, _) = call(ocs_get_authed("/ocs/v2.php/cloud/user?format=json")).await;
    assert_eq!(st, StatusCode::OK);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    let d = &j["ocs"]["data"];
    assert_eq!(d["id"], "alice");
    assert_eq!(d["displayname"], "Alice");
    assert_eq!(d["display-name"], "Alice");
    assert_eq!(d["quota"]["quota"], -3);
    assert_eq!(d["quota"]["used"], 42);
    assert_eq!(d["groups"][0], "staff");
}

// ---------------------------------------------------------------------------
// stubs
// ---------------------------------------------------------------------------

#[tokio::test]
async fn stub_endpoints_answer_empty_success() {
    for (uri, is_array) in [
        ("/ocs/v2.php/apps/notifications/api/v2/notifications", true),
        ("/ocs/v2.php/core/navigation/apps", true),
    ] {
        let (st, body, _) = call(ocs_get(&format!("{uri}?format=json"))).await;
        assert_eq!(st, StatusCode::OK, "{uri}");
        let j: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(j["ocs"]["meta"]["statuscode"], 200);
        if is_array {
            assert!(j["ocs"]["data"].is_array(), "{uri} must be []");
        } else {
            assert!(j["ocs"]["data"].is_object(), "{uri} must be {{}}");
        }
    }
}

#[tokio::test]
async fn deliberate_404s_stay_404() {
    for uri in [
        "/ocs/v2.php/apps/activity/api/v2/activity",
        "/ocs/v2.php/apps/dav/api/v1/direct",
        "/ocs/v2.php/apps/user_status/api/v1/user_status",
    ] {
        let (st, _, _) = call(ocs_get(uri)).await;
        assert_eq!(st, StatusCode::NOT_FOUND, "{uri}");
    }
}

// ---------------------------------------------------------------------------
// Login Flow v2
// ---------------------------------------------------------------------------

#[tokio::test]
async fn login_flow_init_uses_the_canonical_url_not_the_host_header() {
    let (st, body, _) = call(
        Request::builder()
            .method("POST")
            .uri("/index.php/login/v2")
            // A malicious Host header must have no effect.
            .header("Host", "evil.example.net")
            .header("User-Agent", "Mozilla/5.0 (Linux) mirall/3.13.0")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::OK);
    let j: serde_json::Value = serde_json::from_str(&body).unwrap();
    let poll_ep = j["poll"]["endpoint"].as_str().unwrap();
    let login = j["login"].as_str().unwrap();
    assert_eq!(
        poll_ep,
        "https://cloud.example.com/index.php/login/v2/poll"
    );
    assert!(login.starts_with("https://cloud.example.com/index.php/login/v2/flow/"));
    assert!(!poll_ep.contains("evil.example.net"));
    assert!(!login.contains("evil.example.net"));
    // Two distinct 256-bit tokens.
    let poll_tok = j["poll"]["token"].as_str().unwrap();
    let flow_tok = login.rsplit('/').next().unwrap();
    assert_eq!(poll_tok.len(), 64);
    assert_eq!(flow_tok.len(), 64);
    assert_ne!(poll_tok, flow_tok);
}

#[tokio::test]
async fn poll_before_approval_is_404_with_a_genuinely_empty_body() {
    let a = app();
    let resp = a
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2")
                .header("User-Agent", "mirall/3.13.0")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    let j: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let token = j["poll"]["token"].as_str().unwrap().to_string();

    let resp = a
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2/poll")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from(format!("token={token}")))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    // Not `"[]"` — a 404 body of `[]` is JSON-empty but not *string*-empty,
    // and `AuthenticatorActivity.performLoginFlowV2()` gates on
    // `!response.isEmpty()` before treating the poll as terminal
    // (`AuthenticatorActivity.java:558-560`). A pending poll must be
    // string-empty or Android 34.1.0 kills its own poll loop on the first
    // "not yet" answer — see `not_found_json`'s doc comment in `router.rs`.
    assert_eq!(bytes.len(), 0, "poll body must be empty, got {:?}", String::from_utf8_lossy(&bytes));
}

/// Reproduces the exact client-observed failure end to end with the real
/// Android 34.1.0 user agent and its real wire shape (form-encoded `token`
/// in the POST body, per `AuthenticatorActivity.performLoginFlowV2()`):
/// a poll that lands before the human approves must not carry any bytes
/// that Android's `!response.isEmpty()` gate would treat as a terminal
/// answer, because `completeLoginFlow` has no "still pending" branch — any
/// non-empty body, valid JSON or not, drives it into the same unconditional
/// `loginFlowExecutorService.shutdown()` tail a real success takes
/// (`AuthenticatorActivity.java:563-586`). This is the DB-observed shape of
/// the bug: a granted `nc_login_flow` row nobody ever polls again.
#[tokio::test]
async fn android_34_1_0_pre_grant_poll_gets_a_body_its_own_poll_loop_can_survive() {
    let a = app();
    let resp = a
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2")
                .header("User-Agent", "Mozilla/5.0 (Android) Nextcloud-android/34.1.0")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    let j: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let token = j["poll"]["token"].as_str().unwrap().to_string();

    // The zero-delay first poll `poolLogin()` fires on `ON_START`
    // (`AuthenticatorActivity.java:409-412`) — necessarily before the human
    // has had a chance to approve the still-open consent screen.
    let resp = a
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2/poll")
                .header("User-Agent", "Mozilla/5.0 (Android) Nextcloud-android/34.1.0")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from(format!("token={token}")))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    assert!(
        bytes.is_empty(),
        "a non-empty pre-grant poll body ({:?}) trips AuthenticatorActivity's \
         `!response.isEmpty()` gate and permanently kills its poll loop \
         before the human can ever approve",
        String::from_utf8_lossy(&bytes)
    );
}

/// The iOS client puts the poll token in the **query string** and POSTs an
/// empty body:
///
/// ```text
/// the iOS SDK's +Login.swift:398
///     let serverUrl = endpoint + "?token=" + token
/// the iOS SDK's +Login.swift:407
///     .request(serverUrl, method: .post, parameters: nil, encoding: URLEncoding.default, ...)
/// ```
///
/// Reading only the request body made this indistinguishable from "no token
/// supplied", so the poll answered 404 forever and iOS could never finish
/// Login Flow v2 even after the user approved the grant.
///
/// A pending poll and an unrecognised token both render as 404 on purpose (so
/// the endpoint is not an oracle for live tokens), which means 404 cannot
/// prove the token was seen. This used to lean on the per-token rate limiter
/// for that proof — a second immediate poll answered `429`, which only a
/// recognised token could reach. That `429` is gone (see `h_login_poll`: it
/// broke enrolment on a real phone, because the protocol has no meaning for
/// that status and the client stopped polling), so the proof is now the
/// stronger one available: drive consent to completion and show the
/// query-string poll returns the **credentials**. A token that was never
/// extracted cannot produce an app password.
#[tokio::test]
async fn ios_sends_the_poll_token_in_the_query_string() {
    let a = app_with_auth(Arc::new(SessionAuth));
    let (token, flow) = begin_flow(&a, "Nextcloud-iOS/6.0").await;

    let ios_poll = |t: String| {
        Request::builder()
            .method("POST")
            .uri(format!("/index.php/login/v2/poll?token={t}"))
            .body(Body::empty())
            .unwrap()
    };

    // A token that was never issued is a plain 404, and so is a pending one —
    // that ambiguity is deliberate, and it is why this test proves recognition
    // by completing the flow instead.
    let bogus = a.clone().oneshot(ios_poll("f".repeat(64))).await.unwrap();
    assert_eq!(bogus.status(), StatusCode::NOT_FOUND);

    approve(&a, &flow).await;

    // The proof, and the regression test for the `429` that stranded a real
    // handset: iOS's query-string poll yields the credentials. A token that was
    // never extracted from the query string could not.
    let done = a.clone().oneshot(ios_poll(token)).await.unwrap();
    assert_eq!(
        done.status(),
        StatusCode::OK,
        "the query-string token must be extracted and looked up, not ignored"
    );
    let body: serde_json::Value =
        serde_json::from_slice(&axum::body::to_bytes(done.into_body(), 1 << 20).await.unwrap())
            .unwrap();
    assert_eq!(body["appPassword"], "stow_test");
    assert_eq!(body["loginName"], "alice");
}

/// Reproduces the reported strand end to end: login OK, grant, "close this
/// window" — but the handset fails to finish consuming that one successful
/// poll response (dropped connection, app backgrounded mid-parse, or
/// `AuthenticatorActivity.poolLogin()`'s `scheduleWithFixedDelay` never
/// getting a second chance after an unrelated exception killed the loop).
///
/// The reference server (`LoginFlowV2Service::poll()`) deletes the row the
/// instant it is served, before it even attempts to decrypt the password — so
/// a client that never gets to finish processing that response loses a
/// perfectly valid app password forever, and every later poll of the same
/// token is a 404 indistinguishable from one that never existed. This test is
/// the fix: the same poll token must keep returning the same credential.
#[tokio::test]
async fn a_granted_credential_survives_being_polled_more_than_once() {
    // Rate limiting is not what this test is about; disable it so a second
    // poll immediately after the first isn't just throttled into a 404 for an
    // unrelated reason.
    let cfg = NcConfig {
        canonical_url: "https://cloud.example.com".into(),
        login_flow_poll_interval_ns: 0,
        ..NcConfig::default()
    };
    let a = app_with_cfg(cfg, Arc::new(SessionAuth));
    let (token, flow) =
        begin_flow(&a, "Mozilla/5.0 (Android) Nextcloud-android/34.1.0").await;
    approve(&a, &flow).await;

    let poll_req = |t: &str| {
        Request::builder()
            .method("POST")
            .uri("/index.php/login/v2/poll")
            .header("Content-Type", "application/x-www-form-urlencoded")
            .body(Body::from(format!("token={t}")))
            .unwrap()
    };

    // First poll: the response the real client failed to consume.
    let first = a.clone().oneshot(poll_req(&token)).await.unwrap();
    assert_eq!(first.status(), StatusCode::OK);
    let first_body: serde_json::Value =
        serde_json::from_slice(&axum::body::to_bytes(first.into_body(), 1 << 20).await.unwrap())
            .unwrap();

    // Simulated retry with the *same* poll token: must be the credentials
    // again, not a 404.
    let second = a.clone().oneshot(poll_req(&token)).await.unwrap();
    assert_eq!(
        second.status(),
        StatusCode::OK,
        "a dropped first response must not turn a valid grant into a permanent 404"
    );
    let second_body: serde_json::Value =
        serde_json::from_slice(&axum::body::to_bytes(second.into_body(), 1 << 20).await.unwrap())
            .unwrap();
    assert_eq!(first_body, second_body, "the retry must see the exact same credential");

    // A third poll still works — this is bounded by the flow's TTL and sweep,
    // not by a poll count, so it is not an unbounded replay window.
    let third = a.oneshot(poll_req(&token)).await.unwrap();
    assert_eq!(third.status(), StatusCode::OK);
}

/// A poll that arrives sooner than `login_flow_poll_interval_ns` must answer
/// `404`, the protocol's "not yet", and never `429`.
///
/// The reference clients know two answers here: `404` while the human has not
/// finished, and `200` with the credentials. A `429` is a status this wire has
/// no meaning for, and the Android app — which polls at about the same 1s
/// cadence as our own minimum spacing, so clock jitter throttled roughly half
/// its polls — stopped asking when it got one. The user completed consent, saw
/// "Access granted. You may close this window", and the app kept spinning with
/// nothing logged anywhere to say why.
#[tokio::test]
async fn a_throttled_poll_answers_not_found_never_too_many_requests() {
    let a = app();
    let resp = a
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/index.php/login/v2")
                .header("User-Agent", "Nextcloud-android/3.30")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let bytes = axum::body::to_bytes(resp.into_body(), 1 << 20).await.unwrap();
    let j: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let token = j["poll"]["token"].as_str().unwrap().to_string();

    // `FixedClock` never advances, so every poll after the first is inside the
    // minimum spacing — exactly the condition that used to produce `429`.
    for i in 0..5 {
        let r = a
            .clone()
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/index.php/login/v2/poll")
                    .header("Content-Type", "application/x-www-form-urlencoded")
                    .body(Body::from(format!("token={token}")))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(
            r.status(),
            StatusCode::NOT_FOUND,
            "poll #{i} must answer 404 while pending or throttled"
        );
    }
}

/// The desktop/form-encoded shape must keep working, and must win when both
/// are present.
#[tokio::test]
async fn a_form_body_poll_token_still_works_and_takes_precedence() {
    let a = app_with_auth(Arc::new(SessionAuth));
    let (token, flow) = begin_flow(&a, "mirall/3.13.0").await;
    approve(&a, &flow).await;

    // Real token in the body, a token that was never issued in the query
    // string. If the query string won, this poll could only 404.
    let bogus = "f".repeat(64);
    let r = a
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/index.php/login/v2/poll?token={bogus}"))
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from(format!("token={token}")))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(r.status(), StatusCode::OK, "the form body must win over the query string");
    let body: serde_json::Value =
        serde_json::from_slice(&axum::body::to_bytes(r.into_body(), 1 << 20).await.unwrap())
            .unwrap();
    assert_eq!(body["appPassword"], "stow_test");
}

/// The security property, asserted at the routing layer: there is no way to
/// reach the grant handler with a GET.
#[tokio::test]
async fn there_is_no_get_route_that_grants() {
    let (st, _, _) = call(
        Request::builder()
            .uri("/index.php/login/v2/grant?stateToken=x&flowToken=y")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(
        st,
        StatusCode::METHOD_NOT_ALLOWED,
        "a GET that can approve a login flow is an app-password CSRF hole"
    );
}

#[tokio::test]
async fn grant_without_a_session_is_unauthorised() {
    let (st, _, _) = call(
        Request::builder()
            .method("POST")
            .uri("/index.php/login/v2/grant")
            .header("Content-Type", "application/x-www-form-urlencoded")
            .body(Body::from("flowToken=x&stateToken=y"))
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::UNAUTHORIZED);
}

#[tokio::test]
async fn unauthenticated_flow_landing_redirects_to_login_with_return_to() {
    let (st, _, _) = call(
        Request::builder()
            .uri("/index.php/login/v2/flow/abc123")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::SEE_OTHER);
}

// ---------------------------------------------------------------------------
// preview
// ---------------------------------------------------------------------------

#[tokio::test]
async fn preview_requires_auth() {
    let (st, _, _) = call(
        Request::builder()
            .uri("/index.php/core/preview?fileId=1&x=32&y=32")
            .body(Body::empty())
            .unwrap(),
    )
    .await;
    assert_eq!(st, StatusCode::UNAUTHORIZED);
}

// ---------------------------------------------------------------------------
// the resolved client address reaches the auth port
// ---------------------------------------------------------------------------

/// `AuthPort::verify_basic` feeds `sc-auth`'s per-IP brute-force gate and its
/// audit rows, so the address it receives has to be the one the host resolved
/// — not a constant, and not something re-derived from headers in here.
///
/// This crate deliberately owns no trusted-proxy logic (see
/// `ports::ClientAddr`); what it owns is the promise to carry whatever the
/// host put in the extension all the way to the port. That promise is what is
/// tested here.
struct RecordingAuth {
    seen: Arc<std::sync::Mutex<Vec<ClientAddr>>>,
}

impl AuthPort for RecordingAuth {
    fn issue_app_password(&self, _u: UserId, _n: &str, _s: Scope) -> PortResult<(u32, String)> {
        Ok((1, "stow_test".into()))
    }
    fn verify_basic(&self, _l: &str, _s: &str, from: ClientAddr) -> PortResult<Option<Principal>> {
        self.seen.lock().unwrap().push(from);
        Ok(None)
    }
    fn validate_session(&self, _t: &str) -> PortResult<Option<Principal>> {
        Ok(None)
    }
}

async fn client_addr_seen_by_the_auth_port(ext: Option<ClientAddr>) -> Vec<ClientAddr> {
    let seen = Arc::new(std::sync::Mutex::new(Vec::new()));
    let app = app_with_auth(Arc::new(RecordingAuth { seen: seen.clone() }));
    let mut req = ocs_get_authed("/ocs/v2.php/cloud/user");
    if let Some(a) = ext {
        req.extensions_mut().insert(a);
    }
    let _ = app.oneshot(req).await.unwrap();
    let v = seen.lock().unwrap().clone();
    v
}

#[tokio::test]
async fn the_resolved_client_address_is_handed_to_the_auth_port() {
    let addr = ClientAddr("198.51.100.7".parse().unwrap());
    assert_eq!(client_addr_seen_by_the_auth_port(Some(addr)).await, vec![addr]);
}

/// A host that never set the extension must produce "unknown client", which
/// is its own rate-limit bucket — never a plausible-looking wrong address.
#[tokio::test]
async fn a_missing_client_address_degrades_to_unspecified_not_to_loopback() {
    let seen = client_addr_seen_by_the_auth_port(None).await;
    assert_eq!(seen, vec![ClientAddr::default()]);
    assert_eq!(seen[0].0, "0.0.0.0".parse::<std::net::IpAddr>().unwrap());
}
