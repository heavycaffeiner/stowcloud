//! Route-table drift test.
//!
//! `crates/sc-server/src/routes.rs`'s own module doc calls its `route_table`
//! the actual registry, but nothing checked that claim against what the
//! assembled router answers. Three drifts were found by reading, not by any
//! test, before this file existed:
//!
//! * `sc_http::routes::protected_routes` mounted `DELETE /api/jobs/{id}` and
//!   `POST /api/admin/index/estimate` — the table listed neither.
//! * The table listed `GET`/`POST /api/auth/totp/recovery-codes` for a route
//!   that was never registered anywhere at all (caught by the first test
//!   below while writing this file — see the removal note in
//!   `routes.rs::native_routes`).
//!
//! axum gives no route-table introspection, so both directions here are
//! inferred from HTTP responses:
//!
//! * **table ⊆ router** (`every_table_path_is_a_real_route`): an
//!   unauthenticated `OPTIONS` on the literal path. `OPTIONS` bypasses every
//!   auth layer in this codebase (`sc_http::middleware::auth`,
//!   `app::dav_authenticate` — both special-case it so CORS/TUS preflight and
//!   Windows Explorer's unauthenticated DAV probe work), and nothing here
//!   implements application-level `OPTIONS` handling that could itself
//!   answer `404`. A `404` can then only mean axum matched no route pattern
//!   at all for that path.
//! * **router ⊆ table** (`no_undeclared_method_is_mounted_on_a_native_path`):
//!   for every method *not* listed for a path, an authenticated request must
//!   get back `405 Method Not Allowed` — the one status code that is
//!   exclusively axum's own "path matched, method didn't" answer; no handler
//!   in this workspace returns 405 itself (grep the workspace for
//!   `METHOD_NOT_ALLOWED` — it's `sc-dav`, `nc.rs`'s own hand-rolled method
//!   match, and test assertions, never a `sc-http` handler body). Seeing
//!   anything else here means the router mounted a method the table stayed
//!   silent about — exactly the shape of both bugs above.
//!
//!   This half needs real credentials, and specifically a **cookie session**,
//!   not an app password: an unauthenticated non-`GET`/`HEAD` request to a
//!   native `/api/**` route is intercepted by `sc_http::middleware::auth`
//!   before axum's routing ever runs (turning every drift into an
//!   indistinguishable `401`), and — the part that ruled out an app
//!   password — `sc_http::middleware::scope_gate` unconditionally denies an
//!   `AuthVia::AppPassword` principal on the entire `/api/admin` and
//!   self-service surface (`RouteScope::SelfServiceOrAdmin`) regardless of
//!   method, which would have turned the exact drift this file exists to
//!   catch (`POST /api/admin/index/estimate`) into an indistinguishable `403`
//!   too. A logged-in admin session has no such gate (`scope_gate` is a
//!   no-op for `AuthVia::Session`), so every probe here logs in, grants
//!   itself admin, and carries the session cookie plus a real CSRF token and
//!   `Origin` header for state-changing methods
//!   (`sc_http::middleware::csrf`).
//!
//!   It is scoped to `sc_server::routes::route_table`'s `group == "native"`
//!   entries minus `/dav/**`. `/dav/**` and the (feature-gated) compat
//!   compatibility mount are deliberately **not** probed here: they run
//!   under a different authenticator (`app::dav_authenticate`, Basic/session
//!   with its own per-method `Perms` mapping in `dav_required_perms`), and
//!   OCS's `remote.php`/`ocs` dispatch is a hand-rolled method `match` inside
//!   one handler (`nc.rs`) rather than one axum `MethodRouter` per path — so
//!   "declared set vs. probed set" is not the same question there, and
//!   proving it would mean re-implementing each crate's own auth fixture
//!   here rather than reusing one. That coupling belongs in `sc-dav`'s and
//!   `sc-compat-nc`'s own suites (both already assert `405` where it
//!   matters — see `sc-dav/tests/dav.rs` and
//!   `sc-compat-nc/tests/http_integration.rs::there_is_no_get_route_that_grants`),
//!   not bolted onto this one. The `OPTIONS`-existence check above still
//!   covers both surfaces' path existence.
//!
//! **What neither check can prove**: a path mounted in the router with
//! *zero* entries in the table at all. There is nothing to diff it against —
//! catching that still depends on a human reading `app.rs`'s mounts against
//! `routes.rs`'s table, same as before this file existed.

use std::collections::{BTreeMap, BTreeSet};

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
    std::fs::write(share.path().join("hello.txt"), b"hello").unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "files".into(),
            host_path: share.path().to_path_buf(),
            shared_externally: false,
        }],
        // Unambiguous, same as `wiring.rs`'s fixture, so this build keeps
        // exercising a mounted compat layer rather than silently skipping it.
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

/// Same substitutions `tests/wiring.rs`'s own route-table walk uses — a
/// concrete value in each wildcard slot lets the request actually route.
/// axum does not care that a substituted value (`{id_hash}` stays literal;
/// nothing here declares that placeholder) happens to look like path syntax.
fn concretize(path: &str) -> String {
    path.replace("{*path}", "x")
        .replace("{*rest}", "cloud/capabilities")
        .replace("{user}", "alice")
        .replace("{token}", "t")
        .replace("{tid}", "t")
        .replace("{id}", "1")
}

async fn send(app: &App, method: &str, uri: &str) -> StatusCode {
    let req = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .body(Body::empty())
        .unwrap();
    app.router().oneshot(req).await.unwrap().status()
}

/// **table ⊆ router.** See the module doc comment for the full argument;
/// short version: `OPTIONS` reaches real routing unauthenticated everywhere
/// in this workspace, and nothing answers it with an application `404`, so a
/// `404` here is unambiguous.
///
/// Chunked and rebuilt per chunk for the same reason
/// `no_undeclared_method_is_mounted_on_a_native_path` is:
/// `state.rate_limiter` is a 60-request burst bucket
/// (`app.rs`: `IpTokenBucket::new(60, Duration::from_secs(1))`, tuned for
/// ordinary traffic, not 50+ probes fired back to back from one fixture) —
/// exhausting it mid-run would surface as `429`, not `404`, and silently stop
/// proving anything for the remaining paths rather than failing loudly.
#[tokio::test(flavor = "multi_thread")]
async fn every_table_path_is_a_real_route() {
    let mut seen = BTreeSet::new();
    let mut paths = Vec::new();
    for r in sc_server::routes::route_table() {
        if seen.insert(r.path) {
            paths.push(r.path);
        }
    }

    const CHUNK: usize = 40;
    for chunk in paths.chunks(CHUNK) {
        let f = fixture();
        for &path in chunk {
            let uri = concretize(path);
            let status = send(&f.app, "OPTIONS", &uri).await;
            assert_ne!(
                status,
                StatusCode::NOT_FOUND,
                "{path} is declared in the route table but OPTIONS {uri} got 404 — nothing in \
                 the assembled router answers this path at all"
            );
        }
    }
}

/// A logged-in, self-promoted admin session: cookie, CSRF token, and the
/// `Origin` header `Config::default()`'s `bind` port derives into
/// `allowed_origins` (`app.rs`'s `allowed_origins.is_empty()` branch —
/// `http://localhost:8080`, the same value `tests/wiring.rs`'s own CSRF test
/// uses against this same default config).
struct AdminSession {
    cookie: String,
    csrf: String,
}

const ORIGIN: &str = "http://localhost:8080";

async fn admin_session(f: &Fixture) -> AdminSession {
    let uid = f
        .app
        .auth
        .create_user(
            "probe-admin",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.auth.set_admin(uid, true).expect("grant admin");

    let login = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("Content-Type", "application/json")
        .body(Body::from(
            r#"{"username":"probe-admin","password":"correct-horse-battery"}"#,
        ))
        .unwrap();
    let resp = f.app.router().oneshot(login).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK, "fixture login must succeed");
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

    AdminSession { cookie, csrf }
}

async fn send_as(app: &App, method: &str, uri: &str, session: &AdminSession) -> StatusCode {
    const STATE_CHANGING: [&str; 4] = ["POST", "PUT", "PATCH", "DELETE"];
    let mut b = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .header("Cookie", &session.cookie);
    if STATE_CHANGING.contains(&method) {
        b = b.header("Sc-Csrf", &session.csrf).header("Origin", ORIGIN);
    }
    app.router()
        .oneshot(b.body(Body::empty()).unwrap())
        .await
        .unwrap()
        .status()
}

/// **router ⊆ table**, for the surface that shares one auth model. See the
/// module doc comment for exactly what is and isn't covered and why.
#[tokio::test(flavor = "multi_thread")]
async fn no_undeclared_method_is_mounted_on_a_native_path() {
    let mut declared: BTreeMap<&str, Vec<&str>> = BTreeMap::new();
    for r in sc_server::routes::route_table() {
        if r.group != "native" || r.path.starts_with("/dav/") {
            continue;
        }
        declared.entry(r.path).or_default().push(r.method);
    }
    let paths: Vec<&str> = declared.keys().copied().collect();

    const UNIVERSE: [&str; 5] = ["GET", "POST", "PUT", "PATCH", "DELETE"];
    // Small enough that even a path with every one of the 5 universe methods
    // undeclared, plus the login + session round trip `admin_session` itself
    // spends, can't come close to the 60-request burst bucket in one chunk
    // (worst case 2 + 8 * 5 = 42 probes).
    const CHUNK: usize = 8;

    for chunk in paths.chunks(CHUNK) {
        let f = fixture();
        let session = admin_session(&f).await;

        for &path in chunk {
            let methods = &declared[path];
            let uri = concretize(path);
            for probe in UNIVERSE {
                if methods.contains(&probe) {
                    continue;
                }
                let status = send_as(&f.app, probe, &uri, &session).await;
                assert_eq!(
                    status,
                    StatusCode::METHOD_NOT_ALLOWED,
                    "{probe} {uri} ({path} declares only {methods:?}) answered {status} instead \
                     of 405 — a method is mounted here that the route table never declared"
                );
            }
        }
    }
}
