//! Chunked upload on the **native** mount, end to end through the real
//! assembled router.
//!
//! `compat_chunked_upload.rs` proves the same sequence over
//! `/remote.php/dav/uploads/{user}/{tid}/**`, and every one of those tests
//! carries `#[cfg(feature = "compat-nc")]`. Nothing in this file does — that
//! is the point. `scripts/verify.sh` gates a `--no-default-features` build,
//! and until this surface existed, turning the compatibility layer off removed
//! WebDAV chunked upload from the product entirely: `/dav/**` has only
//! whole-body `PUT`, because RFC 4918 has no partial-write verb.
//!
//! What each test is actually defending:
//!
//!   1. the wire sequence round-trips the original bytes, in order, with
//!      unequal chunk lengths (an assembler assuming a fixed stride passes
//!      with equal ones);
//!   2. a `{tid}` — chosen entirely by the client — cannot address another
//!      account's session;
//!   3. an abort publishes nothing;
//!   4. the mount does not shadow a share that happens to be named `uploads`.

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
    _uploads_share: tempfile::TempDir,
}

/// Two shares, and the second is named `uploads` on purpose — see
/// `the_upload_mount_does_not_shadow_a_share_named_uploads`.
fn fixture() -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share = tempfile::tempdir().expect("share dir");
    let uploads_share = tempfile::tempdir().expect("uploads share dir");

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![
            ShareBootstrap {
                name: "sharea".into(),
                host_path: share.path().to_path_buf(),
                shared_externally: false,
            },
            ShareBootstrap {
                name: "uploads".into(),
                host_path: uploads_share.path().to_path_buf(),
                shared_externally: false,
            },
        ],
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
        _uploads_share: uploads_share,
    }
}

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

/// Sum the `d:getcontentlength` values in a 207 listing. This is the number a
/// resuming client adds up to find where it left off, so it has to be the real
/// byte count received — not a chunk count.
fn listed_bytes(listing: &str) -> u64 {
    listing
        .split("<d:getcontentlength>")
        .skip(1)
        .map(|s| {
            s.split("</d:getcontentlength>")
                .next()
                .unwrap()
                .parse::<u64>()
                .unwrap()
        })
        .sum()
}

#[tokio::test(flavor = "multi_thread")]
async fn a_native_chunked_upload_assembles_the_original_bytes() {
    let f = fixture();
    let token = enrol(&f, "alice");
    let tid = "9e107d9d372bb6826bd81d3542a419d6";
    let folder = format!("/dav-uploads/{tid}");

    // Unequal lengths: name `n` has no fixed offset, it is only a sort key.
    let chunks: [&[u8]; 3] = [b"the first part, ", b"then the second, ", b"and the last."];
    let whole: Vec<u8> = chunks.concat();

    // 1. MKCOL opens the session against a destination in the share tree.
    let resp = send(
        &f.app,
        "MKCOL",
        &folder,
        "alice",
        &token,
        &[
            ("Destination", "/dav/sharea/big.bin".into()),
            ("Upload-Length", whole.len().to_string()),
        ],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED, "MKCOL must be 201");

    // A second MKCOL on a live tid is refused rather than silently replacing:
    // a rebind would orphan the first session's spool with nothing left to
    // address it by.
    let resp = send(
        &f.app,
        "MKCOL",
        &folder,
        "alice",
        &token,
        &[("Destination", "/dav/sharea/other.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CONFLICT, "rebind must be refused");

    // 2. Nothing uploaded yet — a resuming client reads this as "start at 0".
    let resp = send(&f.app, "PROPFIND", &folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let listing = String::from_utf8(body_bytes(resp).await).unwrap();
    assert_eq!(listed_bytes(&listing), 0, "{listing}");

    // 3. PUT each chunk. Zero-padded here, unpadded in the offset arithmetic:
    //    both spellings must name the same chunk.
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

    // 4. The resume offset is now the real received total.
    let resp = send(&f.app, "PROPFIND", &folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let listing = String::from_utf8(body_bytes(resp).await).unwrap();
    assert_eq!(
        listed_bytes(&listing),
        whole.len() as u64,
        "resume offset: {listing}"
    );

    // 5. MOVE .file assembles and publishes.
    let resp = send(
        &f.app,
        "MOVE",
        &format!("{folder}/.file"),
        "alice",
        &token,
        &[
            ("Upload-Length", whole.len().to_string()),
            ("X-Mtime", "1700000000".into()),
        ],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED, "MOVE creates the file");
    let etag = resp
        .headers()
        .get("etag")
        .expect("MOVE must carry an ETag")
        .to_str()
        .unwrap()
        .to_string();
    assert!(
        etag.starts_with('"') && etag.ends_with('"'),
        "the ETag must be quoted, got {etag}"
    );

    // 6. The whole point: the bytes come back in the original order.
    let resp = send(
        &f.app,
        "GET",
        "/dav/sharea/big.bin",
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(body_bytes(resp).await, whole);

    // The session went with it — a replayed MOVE cannot address a freed
    // session id through the same tid.
    let resp = send(
        &f.app,
        "MOVE",
        &format!("{folder}/.file"),
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
}

/// The destination is fixed at MKCOL. A `Destination` on the MOVE that names
/// somewhere else is a client bug worth surfacing — honouring it would publish
/// the bytes somewhere the client's own bookkeeping says they did not go.
#[tokio::test(flavor = "multi_thread")]
async fn a_move_cannot_redirect_the_upload_to_a_different_destination() {
    let f = fixture();
    let token = enrol(&f, "alice");
    let folder = "/dav-uploads/redirect-me";

    let resp = send(
        &f.app,
        "MKCOL",
        folder,
        "alice",
        &token,
        &[("Destination", "/dav/sharea/agreed.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    let resp = send(
        &f.app,
        "PUT",
        &format!("{folder}/1"),
        "alice",
        &token,
        &[],
        b"payload".to_vec(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    let resp = send(
        &f.app,
        "MOVE",
        &format!("{folder}/.file"),
        "alice",
        &token,
        &[("Destination", "/dav/sharea/elsewhere.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CONFLICT);

    // Neither path was published by the refused MOVE.
    for path in ["/dav/sharea/agreed.bin", "/dav/sharea/elsewhere.bin"] {
        let resp = send(&f.app, "GET", path, "alice", &token, &[], Vec::new()).await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND, "{path}");
    }

    // Agreeing with the MKCOL destination still works, so the check is about
    // disagreement and not about the header being present at all.
    let resp = send(
        &f.app,
        "MOVE",
        &format!("{folder}/.file"),
        "alice",
        &token,
        &[("Destination", "/dav/sharea/agreed.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);
}

/// `{tid}` is chosen by the client, so two accounts uploading the same file
/// routinely pick the same string. It is resolved scoped by user id for that
/// reason. Unlike the compat surface there is no `{user}` segment to get
/// wrong here — the principal *is* the scope — and this proves that holds.
#[tokio::test(flavor = "multi_thread")]
async fn one_users_transfer_id_is_unreachable_from_another_account() {
    let f = fixture();
    let alice = enrol(&f, "alice");
    let bob = enrol(&f, "bob");
    let tid = "9e107d9d372bb6826bd81d3542a419d6";
    let folder = format!("/dav-uploads/{tid}");

    let resp = send(
        &f.app,
        "MKCOL",
        &folder,
        "alice",
        &alice,
        &[("Destination", "/dav/sharea/secret.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    // Bob names the exact same URL. Every verb must answer 404 — not 403:
    // a distinguishable status would confirm the session exists.
    for (method, uri) in [
        ("PUT", format!("{folder}/00001")),
        ("PROPFIND", folder.clone()),
        ("MOVE", format!("{folder}/.file")),
        ("DELETE", folder.clone()),
    ] {
        let resp = send(&f.app, method, &uri, "bob", &bob, &[], b"evil".to_vec()).await;
        assert_eq!(
            resp.status(),
            StatusCode::NOT_FOUND,
            "{method} {uri} reached another account's session"
        );
    }

    // Bob may open his own session under the same tid, and it is a different
    // session — not a collision with alice's.
    let resp = send(
        &f.app,
        "MKCOL",
        &folder,
        "bob",
        &bob,
        &[("Destination", "/dav/sharea/bobs.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);

    // And alice's session is untouched by all of it.
    let resp = send(&f.app, "PROPFIND", &folder, "alice", &alice, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
    let listing = String::from_utf8(body_bytes(resp).await).unwrap();
    assert_eq!(
        listed_bytes(&listing),
        0,
        "bob's bytes landed in alice's session: {listing}"
    );
}

/// An abort must not publish the bytes that did arrive. Nothing is ever
/// written to the destination before `MOVE .file`; the chunks live in
/// `sc-upload`'s spool until assembly, which is what makes a mid-transfer
/// disconnect, a browser refresh, or a process restart incapable of leaving a
/// partial file behind.
#[tokio::test(flavor = "multi_thread")]
async fn aborting_a_transfer_leaves_no_partial_file_behind() {
    let f = fixture();
    let token = enrol(&f, "alice");
    let folder = "/dav-uploads/abandoned-transfer";

    let resp = send(
        &f.app,
        "MKCOL",
        folder,
        "alice",
        &token,
        &[("Destination", "/dav/sharea/half.bin".into())],
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

    let resp = send(&f.app, "DELETE", folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::NO_CONTENT);

    let resp = send(
        &f.app,
        "GET",
        "/dav/sharea/half.bin",
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);

    // The tid no longer resolves.
    let resp = send(&f.app, "PROPFIND", folder, "alice", &token, &[], Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
}

/// Why this surface is `/dav-uploads` and not `/dav/uploads`: axum matches a
/// literal segment before a wildcard, so registering the session tree inside
/// the DAV mount would have made a share named `uploads` permanently
/// unreachable. The fixture creates exactly that share; this asserts it is
/// still addressable, and that the two URL spaces do not leak into each other.
#[tokio::test(flavor = "multi_thread")]
async fn the_upload_mount_does_not_shadow_a_share_named_uploads() {
    let f = fixture();
    let token = enrol(&f, "alice");

    // An ordinary whole-body PUT into the `uploads` share, then read back.
    let resp = send(
        &f.app,
        "PUT",
        "/dav/uploads/plain.txt",
        "alice",
        &token,
        &[],
        b"not a chunk".to_vec(),
    )
    .await;
    assert!(
        resp.status().is_success(),
        "PUT into the `uploads` share: {}",
        resp.status()
    );

    let resp = send(
        &f.app,
        "GET",
        "/dav/uploads/plain.txt",
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(body_bytes(resp).await, b"not a chunk");

    // And the session mount is not reachable through the share's URL space.
    // `Depth: 0` because the answer has to be sc-dav's verdict on the *path*:
    // without it sc-dav refuses infinite depth (403) before ever looking, and
    // a status that never depended on the path proves nothing about routing.
    let resp = send(
        &f.app,
        "PROPFIND",
        "/dav/uploads/some-tid",
        "alice",
        &token,
        &[("Depth", "0".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(
        resp.status(),
        StatusCode::NOT_FOUND,
        "`/dav/uploads/...` must address the share, not the session tree"
    );

    // The converse: a real session under the same-looking tid is only
    // addressable on its own mount, and the two do not share state.
    let resp = send(
        &f.app,
        "MKCOL",
        "/dav-uploads/some-tid",
        "alice",
        &token,
        &[("Destination", "/dav/uploads/chunked.bin".into())],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);
    let resp = send(
        &f.app,
        "PROPFIND",
        "/dav-uploads/some-tid",
        "alice",
        &token,
        &[],
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::MULTI_STATUS);
}
