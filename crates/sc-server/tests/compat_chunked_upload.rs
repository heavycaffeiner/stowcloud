//! Chunked upload over WebDAV, end to end through the real assembled router.
//!
//! There are two WebDAV surfaces in this server and only one of them chunks:
//!
//!   * `/dav/**` (sc-dav, RFC 4918 Class 2 + RFC 4331) takes a whole-body
//!     `PUT`. `Range` is honoured on `GET`, never on `PUT` — RFC 4918 has no
//!     partial-write verb, so there is nothing to chunk with.
//!   * `/remote.php/dav/uploads/{user}/{tid}/**` (sc-compat-nc + sc-upload)
//!     carries compat chunking v2, which is the sequence every compat
//!     desktop/Android/iOS client uses for a file over 10,240,000 bytes.
//!
//! `crates/sc-compat-nc/src/chunking.rs` covers that sequence at the *store*
//! level, and `app_password_scope.rs` touches it over HTTP exactly once — an
//! auth-refusal case. Nothing proved the wire path end to end: that the router
//! dispatches all four verbs, that the status codes are the exact ones the
//! desktop client refuses to proceed without, that the assembled bytes come
//! back byte-for-byte over a subsequent `GET`, and that a transfer id — which
//! is entirely attacker-chosen — cannot address another account's session.

use axum::body::Body;
use axum::http::{Request, Response, StatusCode};
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

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "sharea".into(),
            host_path: share.path().to_path_buf(),
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
        _share: share,
    }
}

/// A user with full access and an app password to authenticate as.
fn enrol(f: &Fixture, name: &str) -> String {
    let uid = f
        .app
        .auth
        .create_user(
            name,
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

fn basic(user: &str, token: &str) -> String {
    use data_encoding::BASE64;
    format!(
        "Basic {}",
        BASE64.encode(format!("{user}:{token}").as_bytes())
    )
}

/// One request through the whole router, with the headers every request on
/// this mount needs. `extra` carries the chunking-specific ones.
async fn send(
    app: &App,
    method: &str,
    uri: &str,
    user: &str,
    token: &str,
    extra: &[(&str, String)],
    body: Vec<u8>,
) -> Response<Body> {
    let mut req = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .header("Authorization", basic(user, token));
    for (k, v) in extra {
        req = req.header(*k, v.clone());
    }
    app.router()
        .oneshot(req.body(Body::from(body)).unwrap())
        .await
        .unwrap()
}

async fn body_bytes(resp: Response<Body>) -> Vec<u8> {
    http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes()
        .to_vec()
}

/// The full desktop-client sequence, over HTTP, asserting the assembled file
/// afterwards. Every status here is exact on purpose: the desktop client
/// aborts the whole transfer if `MKCOL` answers anything but 201
/// (`propagateuploadng.cpp:302`), and hard-fails the item — "Missing File ID
/// from server" — if the final `MOVE` omits `OC-FileId` or the ETag, even
/// though the bytes are already on disk by then.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn a_chunked_upload_over_webdav_assembles_the_original_bytes() {
    let f = fixture();
    let token = enrol(&f, "alice");
    let tid = "9e107d9d372bb6826bd81d3542a419d6";
    let folder = format!("/remote.php/dav/uploads/alice/{tid}");

    // Three chunks of deliberately unequal length: an assembler that assumed
    // a fixed stride would still pass with equal ones.
    let chunks: [&[u8]; 3] = [b"the first part, ", b"then the second, ", b"and the last."];
    let whole: Vec<u8> = chunks.concat();

    // 1. MKCOL opens the session. Destination is a full URL, as all three
    //    clients send it.
    let resp = send(
        &f.app,
        "MKCOL",
        &folder,
        "alice",
        &token,
        &[
            (
                "Destination",
                "https://localhost/remote.php/dav/files/alice/sharea/big.bin".into(),
            ),
            ("OC-Total-Length", whole.len().to_string()),
        ],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED, "MKCOL must be exactly 201");

    // 2. PROPFIND before any chunk lands: a resuming client reads this as
    //    "start at byte 0".
    let resp = send(&f.app, "PROPFIND", &folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let listing = String::from_utf8(body_bytes(resp).await).unwrap();
    assert!(
        !listing.contains("<d:getcontentlength>"),
        "no chunk has been uploaded yet: {listing}"
    );

    // 3. PUT each chunk under a zero-padded numeric name.
    for (i, data) in chunks.iter().enumerate() {
        let name = format!("{:05}", i + 1);
        let resp = send(
            &f.app,
            "PUT",
            &format!("{folder}/{name}"),
            "alice",
            &token,
            &[],
            data.to_vec(),
        )
        .await;
        assert_eq!(resp.status(), StatusCode::CREATED, "PUT chunk {name}");
    }

    // 4. PROPFIND again. Android derives its resume offset from the
    //    `d:getcontentlength` values here, so the sum has to be the real
    //    number of bytes received, not a chunk count.
    let resp = send(&f.app, "PROPFIND", &folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let listing = String::from_utf8(body_bytes(resp).await).unwrap();
    let received: u64 = listing
        .split("<d:getcontentlength>")
        .skip(1)
        .map(|s| {
            s.split("</d:getcontentlength>")
                .next()
                .unwrap()
                .parse::<u64>()
                .unwrap()
        })
        .sum();
    assert_eq!(received, whole.len() as u64, "resume offset: {listing}");

    // 5. MOVE .file assembles.
    let resp = send(
        &f.app,
        "MOVE",
        &format!("{folder}/.file"),
        "alice",
        &token,
        &[
            (
                "Destination",
                "/remote.php/dav/files/alice/sharea/big.bin".into(),
            ),
            ("OC-Total-Length", whole.len().to_string()),
            ("X-OC-Mtime", "1700000000".into()),
        ],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED, "MOVE creates the file");
    let file_id = resp
        .headers()
        .get("OC-FileId")
        .expect("MOVE must carry OC-FileId")
        .to_str()
        .unwrap()
        .to_string();
    let etag = resp
        .headers()
        .get("etag")
        .expect("MOVE must carry an ETag")
        .to_str()
        .unwrap()
        .to_string();
    assert!(!file_id.is_empty());
    assert!(
        etag.starts_with('"') && etag.ends_with('"'),
        "the ETag must be quoted, got {etag}"
    );

    // 6. The whole point: reading the file back yields the original bytes in
    //    the original order.
    let resp = send(
        &f.app,
        "GET",
        "/remote.php/dav/files/alice/sharea/big.bin",
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(body_bytes(resp).await, whole);

    // The session is gone with it — a replayed MOVE cannot address a freed
    // session id through the same tid.
    let resp = send(
        &f.app,
        "MOVE",
        &format!("{folder}/.file"),
        "alice",
        &token,
        &[("X-OC-Mtime", "1700000000".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
}

/// The transfer id is chosen entirely by the client — Android uses the file's
/// md5, so two accounts uploading the same file pick the *same* string. It is
/// resolved scoped by user id for that reason, and this proves the scoping
/// holds over the wire: bob cannot feed bytes into, list, or finalise alice's
/// transfer even though he names it correctly.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn one_users_transfer_id_is_unreachable_from_another_account() {
    let f = fixture();
    let alice = enrol(&f, "alice");
    let bob = enrol(&f, "bob");
    let tid = "9e107d9d372bb6826bd81d3542a419d6";

    let resp = send(
        &f.app,
        "MKCOL",
        &format!("/remote.php/dav/uploads/alice/{tid}"),
        "alice",
        &alice,
        &[(
            "Destination",
            "https://localhost/remote.php/dav/files/alice/sharea/secret.bin".into(),
        )],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    // Bob names alice's tid under his own upload root — the shape an attacker
    // controls without needing to guess anything.
    let folder = format!("/remote.php/dav/uploads/bob/{tid}");
    for (method, uri, extra) in [
        ("PUT", format!("{folder}/00001"), Vec::new()),
        ("PROPFIND", folder.clone(), Vec::new()),
        (
            "MOVE",
            format!("{folder}/.file"),
            vec![("X-OC-Mtime", "1700000000".to_string())],
        ),
        ("DELETE", folder.clone(), Vec::new()),
    ] {
        let resp = send(&f.app, method, &uri, "bob", &bob, &extra, b"evil".to_vec()).await;
        assert_eq!(
            resp.status(),
            StatusCode::NOT_FOUND,
            "{method} {uri} reached another account's session"
        );
    }

    // And alice's own session is untouched by all of that.
    let resp = send(
        &f.app,
        "PROPFIND",
        &format!("/remote.php/dav/uploads/alice/{tid}"),
        "alice",
        &alice,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let listing = String::from_utf8(body_bytes(resp).await).unwrap();
    assert!(
        !listing.contains("<d:getcontentlength>"),
        "bob's PUT landed in alice's session: {listing}"
    );
}

/// `DELETE` on the folder abandons the transfer, and nothing of it survives:
/// no partial file at the destination, and the tid stops resolving.
#[cfg(feature = "compat-nc")]
#[tokio::test(flavor = "multi_thread")]
async fn aborting_a_transfer_leaves_no_partial_file_behind() {
    let f = fixture();
    let token = enrol(&f, "alice");
    let tid = "abandoned-transfer";
    let folder = format!("/remote.php/dav/uploads/alice/{tid}");

    let resp = send(
        &f.app,
        "MKCOL",
        &folder,
        "alice",
        &token,
        &[(
            "Destination",
            "https://localhost/remote.php/dav/files/alice/sharea/half.bin".into(),
        )],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    let resp = send(
        &f.app,
        "PUT",
        &format!("{folder}/00001"),
        "alice",
        &token,
        &[],
        b"half a file".to_vec(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    let resp = send(&f.app, "DELETE", &folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::NO_CONTENT);

    // The destination was never created — an abort must not publish the
    // bytes that did arrive.
    let resp = send(
        &f.app,
        "GET",
        "/remote.php/dav/files/alice/sharea/half.bin",
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);

    // And the tid no longer resolves.
    let resp = send(&f.app, "PROPFIND", &folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
}
