//! Who the server thinks a request came from, end to end.
//!
//! Every IP-based control in this binary — the login brute-force gate
//! (`sc_auth::AuthService::verify_basic` → `ip_gate`), the API rate limiter,
//! the share-link and search limits, and the audit log's IP column — reads one
//! value: the `ClientIp` the trusted-proxy layer publishes. These tests are
//! about that value being *real*, on every mount.
//!
//! Before the fix these all failed, for three independent reasons:
//!
//! 1. `cmd_serve` called `axum::serve(listener, router)`, which never installs
//!    `ConnectInfo`, so `trusted_proxy` saw no peer and fell back to `0.0.0.0`
//!    for every request — and, having no trusted peer, discarded
//!    `CF-Connecting-IP` too. The stated deployment is Docker behind
//!    Cloudflare, where that header is the only place the client address ever
//!    appears.
//! 2. The WebDAV and compatibility mounts were merged in *beside*
//!    `sc_http::build_router`, so `trusted_proxy` never ran for them at all.
//! 3. `app::dav_authenticate` and `nc::NcAuth::verify_basic` didn't read any
//!    resolved value; both passed a literal `127.0.0.1` into `sc-auth`.
//!
//! The result was one bucket and one meaningless address for the whole
//! internet.
//!
//! Which test pins which defect, since a `oneshot` request can *always* be
//! handed a `ConnectInfo` by hand and would therefore not notice (1) on its
//! own:
//!
//! * (1) — `a_real_bound_socket_supplies_the_peer_address`, the only test here
//!   that goes through `axum::serve` on a real listener. Reverted, it reports
//!   the session's address as `0.0.0.0`.
//! * (2) and (3) — `the_dav_mount_charges_the_login_gate_to_the_real_client`
//!   and `the_compat_mount_does_not_authenticate_against_a_hardcoded_loopback`,
//!   plus `forwarded_for_is_honoured_...`.
//! * The `CF-Connecting-IP` bucket tests are regression guards on the resolver
//!   itself. They pass against the old code *given* a `ConnectInfo`, which is
//!   precisely the thing production never had.

use std::net::{IpAddr, SocketAddr};

use axum::body::Body;
use axum::extract::ConnectInfo;
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

fn fixture(trusted_proxies: &[&str]) -> Fixture {
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
        trusted_proxies: trusted_proxies.iter().map(|s| s.to_string()).collect(),
        // `Config::default()`'s three-entry `app_hosts` is deliberately
        // ambiguous for `resolve_compat_canonical_url` (`app.rs`); set this
        // explicitly so the compat-mount tests below keep exercising a
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

fn peer(ip: &str) -> ConnectInfo<SocketAddr> {
    ConnectInfo(SocketAddr::new(
        ip.parse::<IpAddr>().expect("test address"),
        51234,
    ))
}

/// A request that carries a socket peer, exactly as `axum::serve` +
/// `into_make_service_with_connect_info` delivers one.
fn from_peer(method: &str, uri: &str, ip: &str, headers: &[(&str, &str)]) -> Request<Body> {
    let mut b = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost");
    for (k, v) in headers {
        b = b.header(*k, *v);
    }
    let mut req = b.body(Body::empty()).unwrap();
    req.extensions_mut().insert(peer(ip));
    req
}

async fn send(f: &Fixture, req: Request<Body>) -> StatusCode {
    f.app.router().oneshot(req).await.unwrap().status()
}

/// The general API rate limiter is 60 requests per client (`app.rs`); spending
/// them is how a test observes *which* bucket a request landed in.
const API_BURST: usize = 60;

/// Enough requests to outrun the refill on any machine that could plausibly
/// run this.
///
/// The bucket holds 60 and refills one token per second, so "60 requests then
/// a 429" is only true if all 60 land inside one second. That is a wall-clock
/// assumption, and it is the one this test used to make: on a loaded CI runner
/// 60 real round-trips through the router take longer than that, a token comes
/// back, and the 61st request answers `200`. It failed exactly that way.
///
/// Exhausting the bucket needs `n > 60 + n*t` for a per-request cost of `t`
/// seconds, so 150 covers anything up to ~0.6s per request. A fast machine
/// still stops at 61: this is a ceiling, not a count.
const BURST_CEILING: usize = 150;

/// Send until the limiter says no, and answer how many it took. Panics rather
/// than returning if the ceiling is reached, because a test that never
/// exhausts a bucket is not observing anything.
async fn spend_until_limited(
    f: &Fixture,
    ip: &str,
    headers_for: impl Fn(usize) -> Vec<(String, String)>,
) -> usize {
    for i in 0..BURST_CEILING {
        let owned = headers_for(i);
        let headers: Vec<(&str, &str)> =
            owned.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
        let status = send(f, from_peer("GET", "/api/capabilities", ip, &headers)).await;
        if status == StatusCode::TOO_MANY_REQUESTS {
            assert!(
                i >= API_BURST,
                "the bucket was exhausted after only {i} requests; it holds {API_BURST}"
            );
            return i;
        }
        assert_eq!(status, StatusCode::OK, "request {i} answered neither 200 nor 429");
    }
    panic!("{BURST_CEILING} requests never exhausted a bucket, so this test proves nothing");
}

async fn spend_the_burst(f: &Fixture, ip: &str, headers: &[(&str, &str)]) {
    let owned: Vec<(String, String)> = headers
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    spend_until_limited(f, ip, |_| owned.clone()).await;
}

// --------------------------------------------------------------------------
// The required proof: an untrusted peer's forwarding header is a string, not
// an identity; a trusted peer's is an identity.
// --------------------------------------------------------------------------

/// An attacker rotating `CF-Connecting-IP` must not get a fresh rate-limit
/// budget per value — and, the half that actually failed before, two genuinely
/// different clients must not share one.
#[tokio::test(flavor = "multi_thread")]
async fn an_untrusted_peer_is_rate_limited_by_its_socket_address_not_by_its_headers() {
    let f = fixture(&["203.0.113.0/24"]);
    let attacker = "198.51.100.7";

    // Requests from one untrusted peer, each claiming to be somebody else. If
    // the header bought a fresh budget the limiter would never fire at all,
    // and `spend_until_limited` panics on that rather than passing quietly.
    spend_until_limited(&f, attacker, |i| {
        vec![(
            "CF-Connecting-IP".to_string(),
            format!("192.0.2.{}", i % 250 + 1),
        )]
    })
    .await;

    // ... and a real second client is unaffected. This is the assertion that
    // fails without the fix: with no `ConnectInfo` every request in this test,
    // from either address, resolves to 0.0.0.0 and shares one bucket.
    assert_eq!(
        send(
            &f,
            from_peer("GET", "/api/capabilities", "198.51.100.8", &[])
        )
        .await,
        StatusCode::OK,
        "an unrelated client was throttled by another client's traffic — one bucket for everybody"
    );
}

/// Behind Cloudflare the socket peer is the edge and the same for everyone, so
/// the header *is* the client — but only because the peer is in a configured
/// CIDR.
#[tokio::test(flavor = "multi_thread")]
async fn a_trusted_peer_s_connecting_ip_header_selects_the_bucket() {
    let f = fixture(&["203.0.113.0/24"]);
    let edge = "203.0.113.10";

    spend_the_burst(&f, edge, &[("CF-Connecting-IP", "192.0.2.1")]).await;

    // Same edge, different client behind it: a separate budget. Without the
    // fix the header is discarded, both clients are 0.0.0.0, and this is a
    // 429 — one Cloudflare-fronted user throttling every other.
    assert_eq!(
        send(
            &f,
            from_peer(
                "GET",
                "/api/capabilities",
                edge,
                &[("CF-Connecting-IP", "192.0.2.2")]
            )
        )
        .await,
        StatusCode::OK,
        "a second client behind the same trusted edge shared the first one's bucket"
    );
}

/// `X-Forwarded-For` gets the same gate. The deployment doc's compose example
/// says "behind cloudflared or a reverse proxy" — a plain nginx/Traefik in front
/// sets this header and not `CF-Connecting-IP`, and would otherwise hit the
/// exact bug being fixed.
#[tokio::test(flavor = "multi_thread")]
async fn forwarded_for_is_honoured_from_a_trusted_peer_and_ignored_from_anyone_else() {
    let f = fixture(&["203.0.113.0/24"]);
    let proxy = "203.0.113.10";

    // The leftmost entry is client-supplied; the rightmost is what the proxy
    // actually saw. 192.0.2.50 is the client.
    spend_the_burst(&f, proxy, &[("X-Forwarded-For", "1.1.1.1, 192.0.2.50")]).await;

    assert_eq!(
        send(
            &f,
            from_peer(
                "GET",
                "/api/capabilities",
                proxy,
                &[("X-Forwarded-For", "1.1.1.1, 192.0.2.51")]
            )
        )
        .await,
        StatusCode::OK,
        "a second client behind the same trusted proxy shared the first one's bucket"
    );

    // The same header from a peer that is not a configured proxy must not let
    // an attacker spend a victim's budget — nor borrow a fresh one.
    assert_eq!(
        send(
            &f,
            from_peer(
                "GET",
                "/api/capabilities",
                "198.51.100.9",
                &[("X-Forwarded-For", "192.0.2.50")]
            )
        )
        .await,
        StatusCode::OK,
        "an untrusted peer was charged to the bucket its header named"
    );
}

// --------------------------------------------------------------------------
// The value that reaches sc-auth
// --------------------------------------------------------------------------

fn make_alice(f: &Fixture) -> sc_vfs::UserId {
    let uid = f
        .app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    f.app.core.seed_full_access(uid).expect("grant");
    uid
}

fn basic(user: &str, pw: &str) -> String {
    format!(
        "Basic {}",
        data_encoding::BASE64.encode(format!("{user}:{pw}").as_bytes())
    )
}

/// the audit trail and the session record are only worth
/// keeping if the address in them is the client's.
#[tokio::test(flavor = "multi_thread")]
async fn a_session_records_the_real_client_address() {
    let f = fixture(&[]);
    let uid = make_alice(&f);

    let mut req = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("content-type", "application/json")
        .body(Body::from(
            r#"{"username":"alice","password":"correct-horse-battery"}"#,
        ))
        .unwrap();
    req.extensions_mut().insert(peer("198.51.100.7"));
    assert_eq!(
        f.app.router().oneshot(req).await.unwrap().status(),
        StatusCode::OK
    );

    let sessions = f.app.auth.list_sessions(uid).expect("sessions");
    assert_eq!(sessions.len(), 1);
    assert_eq!(
        sessions[0].ip_first.as_deref(),
        Some("198.51.100.7"),
        "the session was stamped with a placeholder instead of the client"
    );
}

/// Everything above hands the router a `ConnectInfo` by hand, which is exactly
/// what a correctly-bound listener does — and exactly what the *old* bind site
/// never did. This is the one test that goes through a real socket, so it is
/// the one that proves [`sc_server::connect_info_service`] actually delivers a
/// peer address rather than merely compiling.
///
/// It does **not** pin `cmd_serve`'s own call site: it builds its own
/// `axum::serve` expression, so reverting `cmd_serve` to serve the bare router
/// leaves every test here green. Verified by doing exactly that. Reaching
/// `cmd_serve` from a test would mean driving the whole CLI — config file,
/// master key, a bound port, a shutdown signal — so the call site is pinned by
/// a source check in `scripts/verify.sh` instead, next to the other invariants
/// that are cheaper to grep for than to execute.
///
/// The request is written by hand because this crate has no HTTP client
/// dependency, and does not need one for a fixed 200-byte request.
#[tokio::test(flavor = "multi_thread")]
async fn a_real_bound_socket_supplies_the_peer_address() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    let f = fixture(&[]);
    let uid = make_alice(&f);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let addr = listener.local_addr().expect("local addr");
    let router = f.app.router();
    let server = tokio::spawn(async move {
        // The same expression `cmd_serve` uses. With a bare
        // `axum::serve(listener, router)` there is no `ConnectInfo`, and the
        // session below is stamped `0.0.0.0`.
        let _ = axum::serve(listener, sc_server::connect_info_service(router)).await;
    });

    let body = r#"{"username":"alice","password":"correct-horse-battery"}"#;
    let raw = format!(
        "POST /api/auth/login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\n\
         Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    let mut sock = tokio::net::TcpStream::connect(addr).await.expect("connect");
    sock.write_all(raw.as_bytes()).await.expect("write");
    let mut resp = Vec::new();
    sock.read_to_end(&mut resp).await.expect("read");
    server.abort();

    let text = String::from_utf8_lossy(&resp);
    assert!(
        text.starts_with("HTTP/1.1 200"),
        "login over a real socket failed:\n{text}"
    );

    let sessions = f.app.auth.list_sessions(uid).expect("sessions");
    assert_eq!(
        sessions[0].ip_first.as_deref(),
        Some("127.0.0.1"),
        "the served socket never delivered a peer address"
    );
}

/// `sc-auth`'s per-IP hard gate is capacity 20 with a 10 s refill
/// (`AuthConfig::rate_ip_capacity`).
///
/// Two details this depends on:
///
/// * **Distinct usernames.** An identical credential pair is answered from the
///   negative credential cache *before* the gate is consulted, so a repeated
///   guess would spend nothing and prove nothing.
/// * **Concurrent.** Each miss costs a real Argon2 (48 MiB, t=3), which in a
///   debug build is close to a second — long enough for the bucket to refill
///   underneath a sequential loop. Firing them together keeps the whole burn
///   inside a few refill-free seconds. Argon2 concurrency is capped at 4 by
///   `AuthConfig::argon2_parallelism` regardless.
///
/// One burst is still a race against the refill, and on a loaded 4-core VM the
/// refill won: the whole suite passed on the Windows host and this test failed
/// in the Rocky VM. So callers use [`shut_the_login_gate`], which re-burns
/// until the gate is *observably* closed rather than assuming one burst did it.
/// Re-burning is cheap by design: makes the IP bucket a
/// hard limit checked *before* Argon2, so every guess after the first
/// exhaustion is rejected without hashing.
const GATE_BURN: usize = 28;

async fn burn_the_login_gate_over_dav(f: &Fixture, ip: &str, uri: &str, round: usize) {
    let mut handles = Vec::new();
    for i in 0..GATE_BURN {
        let router = f.app.router();
        // `round` keeps the usernames distinct across re-burns; a repeat pair
        // would be answered from the negative credential cache without ever
        // reaching the gate.
        let user = format!("ghost{round}_{i}");
        let req = from_peer(
            "PROPFIND",
            uri,
            ip,
            &[
                ("Depth", "0"),
                ("Authorization", &basic(&user, "not-the-password")),
            ],
        );
        handles.push(tokio::spawn(async move {
            router.oneshot(req).await.unwrap().status()
        }));
    }
    for (i, h) in handles.into_iter().enumerate() {
        let status = h.await.expect("join");
        // `dav_authenticate` is non-fatal: a rate-limited verify leaves no
        // principal and `sc-dav` answers 401 exactly as a wrong password does.
        // So this asserts the flood is *served* normally — it deliberately
        // cannot tell us whether the gate shut, which is why the caller checks
        // that separately.
        assert_eq!(
            status,
            StatusCode::UNAUTHORIZED,
            "guess {i} answered {status}"
        );
    }
}

/// Burn `ip`'s login gate over `uri` until `/api/auth/login` from that address
/// is actually refused, and return whether it ever was.
///
/// A single burst is a race: 28 guesses must outrun a 1-token-per-10s refill,
/// which holds on an idle host and does not on a contended VM. Looping removes
/// the race without weakening anything — the caller still asserts on a
/// *different* address being served, and that is the property under test.
async fn shut_the_login_gate(f: &Fixture, ip: &str, uri: &str) -> bool {
    for round in 0..4 {
        burn_the_login_gate_over_dav(f, ip, uri, round).await;
        if login_status(f, ip).await == StatusCode::TOO_MANY_REQUESTS {
            return true;
        }
    }
    false
}

async fn login_status(f: &Fixture, ip: &str) -> StatusCode {
    let mut req = Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("Host", "localhost")
        .header("content-type", "application/json")
        .body(Body::from(
            r#"{"username":"alice","password":"correct-horse-battery"}"#,
        ))
        .unwrap();
    req.extensions_mut().insert(peer(ip));
    f.app.router().oneshot(req).await.unwrap().status()
}

/// `dav_authenticate` used to hand `sc-auth` a literal `127.0.0.1`, so a DAV
/// password-guessing flood spent a bucket belonging to nobody while the
/// attacker's own stayed full. Here the flood arrives over `/dav` and the
/// consequence is observed on `/api/auth/login`, which shares the same gate:
/// the attacker must have spent *their own* budget, and only their own.
#[tokio::test(flavor = "multi_thread")]
async fn the_dav_mount_charges_the_login_gate_to_the_real_client() {
    let f = fixture(&[]);
    make_alice(&f);
    let attacker = "198.51.100.7";

    assert!(
        shut_the_login_gate(&f, attacker, "/dav/").await,
        "a DAV password-guessing flood cost the guesser nothing — the gate was charged elsewhere"
    );
    assert_eq!(
        login_status(&f, "198.51.100.8").await,
        StatusCode::OK,
        "an unrelated client was locked out by someone else's guessing"
    );
}

/// The same for the compatibility mount, which is merged in separately and was
/// likewise outside `trusted_proxy`'s reach. Both authenticators on that mount
/// (`dav_authenticate` and `nc::NcAuth::verify_basic`) hardcoded `127.0.0.1`;
/// this exhausts precisely that address's gate and then requires a real client
/// to be served anyway.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn the_compat_mount_does_not_authenticate_against_a_hardcoded_loopback() {
    let f = fixture(&[]);
    make_alice(&f);

    // Fill the bucket belonging to the address the old code always used, and
    // confirm it really is full — otherwise the assertion below would hold
    // vacuously. `login` does not consult the credential cache, so this reads
    // the gate directly and leaves nothing warm behind it.
    assert!(
        shut_the_login_gate(&f, "127.0.0.1", "/remote.php/dav/files/ghost/").await,
        "the loopback gate was not exhausted, so this test would prove nothing"
    );

    // A different client's valid credentials must still be honoured on that
    // mount. Before the fix both authenticators asked the exhausted
    // `127.0.0.1` bucket and refused.
    let status = send(
        &f,
        from_peer(
            "GET",
            "/ocs/v2.php/apps/files_sharing/api/v1/shares",
            "198.51.100.8",
            &[
                ("OCS-APIRequest", "true"),
                ("Authorization", &basic("alice", "correct-horse-battery")),
            ],
        ),
    )
    .await;
    assert_eq!(
        status,
        StatusCode::OK,
        "the compat mount refused a valid credential because a hardcoded address was rate-limited"
    );
}
