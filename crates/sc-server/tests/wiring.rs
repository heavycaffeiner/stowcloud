//! End-to-end proof that the assembly in `app.rs` produces a *working*
//! router, not merely one that compiles.
//!
//! The thing this guards against is the state the server was in before the
//! integration pass: a route table that looked complete while every handler
//! answered `501`. So the assertions are deliberately about the negative —
//! **no route may answer `501 Not Implemented`** — as much as about the
//! positive.

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
        // `Config::default()`'s three-entry `app_hosts` is deliberately
        // ambiguous for `resolve_compat_canonical_url` (`app.rs`); set it
        // explicitly so this fixture's compat-mount tests keep exercising a
        // mounted layer.
        compat_canonical_url: Some("https://localhost".into()),
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [7u8; 32],
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

async fn status_of(app: &App, method: &str, uri: &str) -> StatusCode {
    let req = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    app.router().oneshot(req).await.unwrap().status()
}

#[tokio::test(flavor = "multi_thread")]
async fn health_is_served_by_the_server_itself() {
    let f = fixture();
    assert_eq!(
        status_of(&f.app, "GET", "/api/health").await,
        StatusCode::OK
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn capabilities_comes_from_sc_http() {
    let f = fixture();
    let req = Request::builder()
        .uri("/api/capabilities")
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);

    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();

    // a neutral list of mounted compatibility layers, not
    // a boolean named after a vendor.
    let ext = json["features"]["extensions"]
        .as_array()
        .expect("extensions is a list");
    assert!(
        json["features"].get("nc_compat").is_none(),
        "no vendor-named boolean in the core API"
    );
    #[cfg(feature = "compat-nc")]
    assert!(
        ext.iter().any(|v| v == "compat-nc"),
        "the compat layer registers itself: {ext:?}"
    );
    #[cfg(not(feature = "compat-nc"))]
    assert!(
        ext.is_empty(),
        "nothing should be advertised when no layer is compiled in: {ext:?}"
    );
}

/// `sc-dav` answers `OPTIONS` before authentication on purpose: Windows
/// Explorer probes with an unauthenticated `OPTIONS` and will not offer
/// credentials unless it gets a `DAV:` header back.
#[tokio::test(flavor = "multi_thread")]
async fn dav_options_is_answered_unauthenticated() {
    let f = fixture();
    let req = Request::builder()
        .method("OPTIONS")
        .uri("/dav/")
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert!(
        resp.headers().get("dav").is_some(),
        "the DAV compliance header is what makes clients mount"
    );
}

/// The real service is reached (not a stub): an unauthenticated PROPFIND gets
/// `401` with a challenge, which only the real dispatcher produces.
#[tokio::test(flavor = "multi_thread")]
async fn dav_propfind_reaches_the_real_service() {
    let f = fixture();
    let req = Request::builder()
        .method("PROPFIND")
        .uri("/dav/files")
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    assert!(resp.headers().get("www-authenticate").is_some());
}

/// The regression this whole pass exists to prevent.
#[tokio::test(flavor = "multi_thread")]
async fn no_declared_route_answers_not_implemented() {
    let f = fixture();
    for r in sc_server::routes::route_table() {
        // Wildcards cannot be requested literally; substitute something
        // concrete so the request actually routes.
        let uri = r
            .path
            .replace("{*path}", "x")
            .replace("{*rest}", "cloud/capabilities")
            .replace("{user}", "alice")
            .replace("{token}", "t")
            .replace("{tid}", "t")
            .replace("{id}", "1");
        let status = status_of(&f.app, r.method, &uri).await;
        assert_ne!(
            status,
            StatusCode::NOT_IMPLEMENTED,
            "{} {} answered 501 — it is declared in the route table but not wired",
            r.method,
            r.path
        );
    }
}

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn status_php_is_served_when_the_compat_layer_is_compiled_in() {
    let f = fixture();
    assert_eq!(
        status_of(&f.app, "GET", "/status.php").await,
        StatusCode::OK
    );
}

/// `--no-default-features` must not merely stop registering these paths: the
/// code that produces them is not in the binary at all (`ARCHITECTURE.md`
/// §10.1). Here we can only check the routing half.
#[cfg(not(feature = "compat-nc"))]
#[tokio::test(flavor = "multi_thread")]
async fn compat_paths_are_absent_without_the_feature() {
    let f = fixture();
    // Asserting "not 200" rather than "404": these paths fall through to the
    // native stack, which rejects them at whatever layer notices first (the
    // `HostGuard`/auth middleware answers before routing does). What matters
    // is that none of them is *answered* — the compat layer replies `200` to
    // an unauthenticated `GET /status.php`, and that is the observable this
    // build must not produce.
    for uri in [
        "/status.php",
        "/ocs/v2.php/cloud/capabilities",
        "/remote.php/webdav/x",
    ] {
        assert_ne!(
            status_of(&f.app, "GET", uri).await,
            StatusCode::OK,
            "{uri} must not be served in a feature-stripped build"
        );
    }
}

// ------------------------------------------------------- share links (§7) --
//
// These go through the real stack: `sc_core::LinkStore` → `CoreBridge` →
// `sc-http`. Nothing is mocked, so a break anywhere along that chain shows up
// here rather than in a unit test that only proves the mock works.

/// A user with a projected grant over the fixture share, plus its virtual
/// root label.
fn user_with_grant(f: &Fixture) -> (sc_vfs::UserId, String) {
    let uid = f
        .app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    // Grants are persisted and admin-managed now, not a startup-time
    // projection of the whole account list (`sc_core::acl_store`'s module
    // doc) — a user created mid-test needs an explicit grant, the same way
    // an admin would create one through the (forthcoming) grant-management
    // API. `seed_full_access` is the one-time full-access seed that API
    // will also expose, reused here for brevity.
    f.app.core.seed_full_access(uid).expect("grant");
    let label = f
        .app
        .core
        .roots(uid)
        .first()
        .map(|r| r.label.clone())
        .expect("a projected root");
    (uid, label)
}

#[tokio::test(flavor = "multi_thread")]
async fn a_share_link_survives_the_whole_stack() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);

    let (link, token) = f
        .app
        .core
        .create_link(
            uid,
            &format!("/{label}/hello.txt"),
            &sc_core::LinkSpec::default(),
        )
        .expect("mint a link");
    assert_eq!(token.len(), 22);

    // Anonymous fetch of the public page: no session, no cookie.
    let req = Request::builder()
        .uri(format!("/s/{token}"))
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(
        resp.headers().get("X-Robots-Tag").unwrap(),
        "noindex, nofollow"
    );
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(json["name"], "hello.txt");
    assert_eq!(json["size"], 9);

    // The owner sees it through the native API's own listing.
    let listed = f.app.core.list_links(uid, None).expect("list");
    assert_eq!(listed.len(), 1);
    assert_eq!(listed[0].id, link.id);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_public_download_is_signed_for_nobody_and_counts_once() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let spec = sc_core::LinkSpec {
        max_downloads: Some(1),
        ..sc_core::LinkSpec::default()
    };
    let (link, token) = f
        .app
        .core
        .create_link(uid, &format!("/{label}/hello.txt"), &spec)
        .expect("mint a link");

    let download = |token: String| {
        let router = f.app.router();
        async move {
            let req = Request::builder()
                .method("POST")
                .uri(format!("/s/{token}/download"))
                .header("Host", "localhost")
                .body(Body::empty())
                .unwrap();
            router.oneshot(req).await.unwrap()
        }
    };

    let resp = download(token.clone()).await;
    assert_eq!(resp.status(), StatusCode::OK);
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let url = json["url"].as_str().expect("a signed content URL");
    let signed = url.rsplit('/').next().unwrap();
    let claim = sc_http::content::verify(&f.app.http.signed_url_keys.lock(), signed, None)
        .expect("valid claim");
    assert_eq!(claim.sub, 0, "`sub = 0` marks a public-link download");

    // The cap is spent; a second attempt is `410`, not a second URL.
    assert_eq!(download(token).await.status(), StatusCode::GONE);
    assert_eq!(f.app.core.get_link(uid, link.id).unwrap().downloads, 1);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_wrong_link_password_is_indistinguishable_from_an_unknown_token() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let spec = sc_core::LinkSpec {
        password: Some("open-sesame".into()),
        ..sc_core::LinkSpec::default()
    };
    let (_link, token) = f
        .app
        .core
        .create_link(uid, &format!("/{label}/hello.txt"), &spec)
        .expect("mint a link");

    let attempt = |uri: String, pw: &str| {
        let router = f.app.router();
        let body = format!(r#"{{"password":"{pw}"}}"#);
        async move {
            let req = Request::builder()
                .method("POST")
                .uri(uri)
                .header("Host", "localhost")
                .header("content-type", "application/json")
                .body(Body::from(body))
                .unwrap();
            let resp = router.oneshot(req).await.unwrap();
            let status = resp.status();
            let bytes = http_body_util::BodyExt::collect(resp.into_body())
                .await
                .unwrap()
                .to_bytes();
            (status, bytes)
        }
    };

    let (wrong_status, wrong_body) = attempt(format!("/s/{token}/auth"), "nope").await;
    let (unknown_status, unknown_body) =
        attempt("/s/definitely-not-a-token/auth".into(), "nope").await;
    assert_eq!(wrong_status, StatusCode::NOT_FOUND);
    assert_eq!(wrong_status, unknown_status);
    assert_eq!(
        wrong_body, unknown_body,
        "the two failures must be the same response"
    );

    // The right password does open it.
    let (ok_status, _) = attempt(format!("/s/{token}/auth"), "open-sesame").await;
    assert_eq!(ok_status, StatusCode::OK);
}

/// The compat layer's share API is backed by the same store, and refuses the
/// grant kinds nothing in this workspace can persist rather than dropping
/// them.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_compat_share_port_is_backed_by_the_same_store() {
    use sc_compat_nc::ports::{GranteeKind, ShareFilter, ShareSpec};

    let f = fixture();
    let (uid, _label) = user_with_grant(&f);
    let port = f.app.compat.as_ref().expect("compat built").share_port();

    let spec = ShareSpec {
        path: "/hello.txt".into(),
        kind: GranteeKind::Link,
        grantee: None,
        perms: sc_acl::Perms::READ | sc_acl::Perms::DOWNLOAD,
        password: None,
        expires_s: None,
        label: Some("holiday photos".into()),
        note: None,
    };
    let created = port.create(uid, &spec).expect("a public link is creatable");
    assert_eq!(created.kind, GranteeKind::Link);
    assert!(
        created.token.is_some(),
        "the plaintext token is returned once, at creation"
    );

    // Same row, seen through `sc-core`.
    assert_eq!(f.app.core.list_links(uid, None).unwrap().len(), 1);

    let listed = port.list(uid, &ShareFilter::default()).expect("list");
    assert_eq!(listed.len(), 1);
    assert_eq!(listed[0].id, created.id);
    assert_eq!(listed[0].label, "holiday photos");
    assert!(
        listed[0].token.is_none(),
        "only sha256(token) is stored, so a later read cannot reproduce the plaintext"
    );

    // User and group grants have no store anywhere. Refused, not dropped.
    for kind in [GranteeKind::User, GranteeKind::Group] {
        let s = ShareSpec {
            kind,
            grantee: Some("bob".into()),
            ..spec.clone()
        };
        assert!(
            port.create(uid, &s).is_err(),
            "{kind:?} must be refused, not silently accepted"
        );
    }
    assert_eq!(
        f.app.core.list_links(uid, None).unwrap().len(),
        1,
        "a refused create wrote nothing"
    );

    port.delete(uid, created.id).expect("delete");
    assert!(f.app.core.list_links(uid, None).unwrap().is_empty());
}

// -------------------------------------------------- plain-PUT X-OC-Mtime --
//
// `nc::h_put_files` reads `X-OC-Mtime` on the non-chunked `PUT` — the path
// nearly every mobile upload takes, since Android only switches to the
// chunked flow above 10,240,000 bytes
// (`ChunkedFileUploadRemoteOperation.java`) — and applies it via
// `sc_vfs::ShareRoot::set_times` once `sc-dav` has actually written the
// file. These run through the real merged router and authenticate the way a
// real client does — Basic credentials, no `DavPrincipal` injected — so the
// compat mount's own auth wiring is under test here too.

#[cfg(feature = "compat-nc")]
fn basic(user: &str, pw: &str) -> String {
    use data_encoding::BASE64;
    format!("Basic {}", BASE64.encode(format!("{user}:{pw}").as_bytes()))
}

#[cfg(feature = "compat-nc")]
async fn put_files(
    f: &Fixture,
    _uid: sc_vfs::UserId,
    vpath: &str,
    header: Option<(&str, &str)>,
) -> axum::response::Response {
    let mut b = Request::builder()
        .method("PUT")
        .uri(format!("/remote.php/dav/files/alice{vpath}"))
        .header("Host", "localhost")
        .header("Authorization", basic("alice", "correct-horse-battery"));
    if let Some((k, v)) = header {
        b = b.header(k, v);
    }
    let req = b.body(Body::from("camera roll bytes")).unwrap();
    f.app.router().oneshot(req).await.unwrap()
}

/// The compat WebDAV mount must accept the same credentials as the native one.
///
/// It did not. `dav_authenticate` was layered onto `dav_router()` alone, while
/// `nc::router()` was merged in bare, so no `DavPrincipal` was ever
/// established and `sc-dav` refused every request. That failed *closed* — no
/// unauthorised access — but it made the entire compat surface unusable by
/// any real client, which no test noticed because the protocol tests all
/// inject the principal directly.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_compat_dav_mount_accepts_the_same_credentials_as_the_native_one() {
    let f = fixture();
    let (_uid, label) = user_with_grant(&f);

    let probe = |uri: String, auth: Option<String>| {
        let app = f.app.router();
        async move {
            let mut b = Request::builder()
                .method("PROPFIND")
                .uri(uri)
                .header("Host", "localhost")
                .header("Depth", "0");
            if let Some(a) = auth {
                b = b.header("Authorization", a);
            }
            app.oneshot(b.body(Body::empty()).unwrap())
                .await
                .unwrap()
                .status()
        }
    };

    let creds = basic("alice", "correct-horse-battery");
    let native = probe(format!("/dav/{label}"), Some(creds.clone())).await;
    let compat = probe(format!("/remote.php/dav/files/alice/{label}"), Some(creds)).await;
    assert_eq!(native, StatusCode::MULTI_STATUS, "native mount");
    assert_eq!(
        compat,
        StatusCode::MULTI_STATUS,
        "compat mount rejected valid credentials"
    );

    // And it must still fail closed without them.
    let anon = probe(format!("/remote.php/dav/files/alice/{label}"), None).await;
    assert_eq!(
        anon,
        StatusCode::UNAUTHORIZED,
        "compat mount served an anonymous PROPFIND"
    );
    let wrong = probe(
        format!("/remote.php/dav/files/alice/{label}"),
        Some(basic("alice", "not-the-password")),
    )
    .await;
    assert_eq!(
        wrong,
        StatusCode::UNAUTHORIZED,
        "compat mount accepted a bad password"
    );
}

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn plain_put_honours_an_integer_x_oc_mtime() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let vpath = format!("/{label}/photo.jpg");

    let resp = put_files(&f, uid, &vpath, Some(("X-OC-Mtime", "1700000000"))).await;
    assert!(resp.status().is_success(), "status: {}", resp.status());
    assert_eq!(
        resp.headers()
            .get("X-OC-MTime")
            .and_then(|v| v.to_str().ok()),
        Some("accepted"),
        "the reference server's own confirmation header, File.php:363"
    );

    let e = f.app.core.stat_entry(uid, &vpath).expect("file exists");
    assert_eq!(e.mtime_ns, 1_700_000_000_000_000_000);
}

/// iOS formats the header as a Swift `Double`
/// (the iOS client SDK's upload path), so it always carries a fractional part.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn plain_put_truncates_an_ios_style_fractional_x_oc_mtime() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let vpath = format!("/{label}/photo.jpg");

    let resp = put_files(&f, uid, &vpath, Some(("X-OC-Mtime", "1751234567.891234"))).await;
    assert!(resp.status().is_success());
    let e = f.app.core.stat_entry(uid, &vpath).expect("file exists");
    assert_eq!(
        e.mtime_ns, 1_751_234_567_000_000_000,
        "sub-second precision is truncated, not rejected"
    );
}

/// The reference server's own parser throws on a non-numeric value
/// (`MtimeSanitizer::sanitizeMtime`) — uncaught, it becomes a 500 out of
/// `File::put()` for a file whose bytes are already correctly on disk
/// (`File.php:322,339,361`). We instead ignore the header and let the
/// already-successful write stand.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn plain_put_with_a_garbage_x_oc_mtime_still_succeeds() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let vpath = format!("/{label}/photo.jpg");

    let resp = put_files(&f, uid, &vpath, Some(("X-OC-Mtime", "not-a-number"))).await;
    assert!(
        resp.status().is_success(),
        "a malformed header must not fail an upload whose bytes are already written: {}",
        resp.status()
    );
    assert!(
        resp.headers().get("X-OC-MTime").is_none(),
        "nothing was honoured, so nothing claims to have been"
    );
    // The file exists with *some* real mtime — just not derived from the
    // unparsable header, which contributed nothing.
    assert!(
        f.app
            .core
            .stat_entry(uid, &vpath)
            .expect("file exists")
            .mtime_ns
            > 0
    );
}

#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn plain_put_without_x_oc_mtime_is_unaffected() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let vpath = format!("/{label}/photo.jpg");

    let resp = put_files(&f, uid, &vpath, None).await;
    assert!(resp.status().is_success());
    assert!(resp.headers().get("X-OC-MTime").is_none());
}

/// The header must only be applied once the write has actually succeeded —
/// an `If-Match` precondition failure is a deterministic way to fail a PUT
/// that would otherwise carry a perfectly valid `X-OC-Mtime`.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_failed_put_does_not_apply_x_oc_mtime() {
    let f = fixture();
    let (uid, label) = user_with_grant(&f);
    let vpath = format!("/{label}/hello.txt");
    let before = f
        .app
        .core
        .stat_entry(uid, &vpath)
        .expect("fixture file")
        .mtime_ns;

    let mut req = Request::builder()
        .method("PUT")
        .uri(format!("/remote.php/dav/files/alice{vpath}"))
        .header("Host", "localhost")
        .header("If-Match", "\"not-the-real-etag\"")
        .header("X-OC-Mtime", "1700000000")
        .body(Body::from("replacement bytes"))
        .unwrap();
    req.extensions_mut().insert(sc_dav::DavPrincipal(uid));
    let resp = f.app.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::PRECONDITION_FAILED);

    let after = f
        .app
        .core
        .stat_entry(uid, &vpath)
        .expect("fixture file")
        .mtime_ns;
    assert_eq!(
        before, after,
        "a failed PUT must not touch the file's timestamp"
    );
}

// ---------------------------------------------------------------- CSRF origin --

/// The whole write surface of the web UI, end to end through the real router.
///
/// `HttpConfig::allowed_origins` defaults to the placeholder
/// `https://app.example.com` and nothing populated it from `Config`, so the
/// origin half of the CSRF check rejected every
/// cookie-authenticated state-changing request with `403`. New folder, rename,
/// delete, move, copy, upload, share — all of it, in any deployment. Reads
/// were unaffected, which is what made it look like a permissions problem.
///
/// No test caught it because the protocol tests either send no `Origin` at all
/// or drive `sc-http` with its own `AppState` rather than the one `sc-server`
/// assembles from a `Config`. This asserts on the assembled server.
#[tokio::test(flavor = "multi_thread")]
async fn a_cookie_authenticated_write_from_our_own_origin_passes_csrf() {
    let f = fixture();
    let (_uid, label) = user_with_grant(&f);

    // Log in the way a browser does, and keep what it keeps.
    let login = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(
            r#"{"username":"alice","password":"correct-horse-battery"}"#,
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
    // The same lookup the file browser's header depends on; it was hardcoded
    // to an empty string and only the mock backend hid it.
    assert_eq!(
        json["user"]["name"].as_str(),
        Some("alice"),
        "session must name the user"
    );

    let mkdir = Request::builder()
        .method("POST")
        .uri("/api/fs/mkdir")
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .header("Sc-Csrf", &csrf)
        // Exactly what a browser on the app origin sends.
        .header("Origin", "http://localhost:8080")
        .header("Content-Type", "application/json")
        .body(Body::from(format!(r#"{{"path":"/{label}/csrf-probe"}}"#)))
        .unwrap();
    let resp = f.app.router().oneshot(mkdir).await.unwrap();
    assert!(
        resp.status().is_success(),
        "a write from our own origin was refused: {} — allowed_origins is unset or wrong",
        resp.status()
    );

    // And the check still bites: same everything, a foreign origin.
    let evil = Request::builder()
        .method("POST")
        .uri("/api/fs/mkdir")
        .header("Host", "localhost")
        .header("Cookie", &cookie)
        .header("Sc-Csrf", &csrf)
        .header("Origin", "https://evil.example.com")
        .header("Content-Type", "application/json")
        .body(Body::from(format!(r#"{{"path":"/{label}/from-evil"}}"#)))
        .unwrap();
    let resp = f.app.router().oneshot(evil).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "a cross-origin write must still be refused"
    );
}
