//! The screens the mobile clients ship that this server could not answer:
//! search, the photo timeline, favourites, deleted files, and the account
//! lifecycle a phone needs in order to log out without leaving a live
//! credential behind.
//!
//! Everything here goes through the real merged router and authenticates the
//! way a real client does, with an app password over Basic, so the compat
//! mount's own auth wiring and the DAV method gate are under test too.

#![cfg(feature = "compat-nc")]

use axum::body::Body;
use axum::http::{Request, StatusCode};
use sc_server::app::App;
use sc_server::config::{Config, ShareBootstrap};
use sc_server::masterkey::MasterKeyResult;
use tower::ServiceExt;

struct Fixture {
    app: App,
    uid: sc_vfs::UserId,
    token: String,
    label: String,
    _data: tempfile::TempDir,
    _share: tempfile::TempDir,
}

/// `trash_enabled` is off by default for every share, which is the correct
/// default and also means a trashbin test has to turn it on explicitly.
fn fixture(trash: bool) -> Fixture {
    let data = tempfile::tempdir().expect("data dir");
    let share = tempfile::tempdir().expect("share dir");
    std::fs::write(share.path().join("report.txt"), b"quarterly").unwrap();
    std::fs::write(share.path().join("holiday.jpg"), vec![0u8; 32]).unwrap();
    std::fs::create_dir_all(share.path().join("album")).unwrap();
    std::fs::write(share.path().join("album/beach.png"), vec![0u8; 64]).unwrap();

    let cfg = Config {
        data_dir: data.path().to_path_buf(),
        shares: vec![ShareBootstrap {
            name: "files".into(),
            host_path: share.path().to_path_buf(),
            shared_externally: false,
        }],
        compat_canonical_url: Some("https://localhost".into()),
        ..Config::default()
    };
    let key = MasterKeyResult {
        key: [11u8; 32],
        inside_data_dir: false,
        generated: true,
    };
    let app = App::build(cfg, &key).expect("app builds");

    let uid = app
        .auth
        .create_user(
            "alice",
            &secrecy::SecretString::from("correct-horse-battery".to_string()),
        )
        .expect("create user");
    app.core.seed_full_access(uid).expect("grant");
    let label = app
        .core
        .roots(uid)
        .first()
        .map(|r| r.label.clone())
        .expect("a projected root");
    if trash {
        let id = app.core.roots(uid).first().map(|r| r.share).unwrap();
        app.core
            .update_share(id, None, None, Some(true))
            .expect("turn trash on");
    }
    let token = app
        .auth
        .issue_app_password(uid, "phone", sc_auth::Scope::default())
        .expect("app password")
        .1;

    Fixture {
        app,
        uid,
        token,
        label,
        _data: data,
        _share: share,
    }
}

fn basic(token: &str) -> String {
    use data_encoding::BASE64;
    format!("Basic {}", BASE64.encode(format!("alice:{token}").as_bytes()))
}

struct Resp {
    status: StatusCode,
    headers: axum::http::HeaderMap,
    body: String,
}

async fn send(f: &Fixture, method: &str, uri: &str, hdrs: &[(&str, &str)], body: &str) -> Resp {
    let mut b = Request::builder()
        .method(method)
        .uri(uri)
        .header("Host", "localhost")
        .header("Authorization", basic(&f.token));
    for (k, v) in hdrs {
        b = b.header(*k, *v);
    }
    let resp = f
        .app
        .router()
        .oneshot(b.body(Body::from(body.to_string())).unwrap())
        .await
        .unwrap();
    let status = resp.status();
    let headers = resp.headers().clone();
    let bytes = http_body_util::BodyExt::collect(resp.into_body())
        .await
        .unwrap()
        .to_bytes();
    Resp {
        status,
        headers,
        body: String::from_utf8_lossy(&bytes).into_owned(),
    }
}

// ------------------------------------------------------------------ search --

/// `SearchRemoteOperation.run()` sends `OPTIONS` first and returns failure
/// without issuing a search at all unless the `Allow` header names `SEARCH`.
/// The search box then comes back empty and reports no error.
#[tokio::test(flavor = "multi_thread")]
async fn options_on_the_dav_mount_advertises_search_and_report() {
    let f = fixture(false);
    let r = send(&f, "OPTIONS", "/remote.php/dav", &[], "").await;
    assert_eq!(r.status, StatusCode::OK);
    let allow = r.headers.get("allow").unwrap().to_str().unwrap();
    assert!(allow.contains("SEARCH"), "{allow}");
    assert!(allow.contains("REPORT"), "{allow}");
}

fn search_body(scope: &str, where_xml: &str) -> String {
    format!(
        r#"<?xml version="1.0"?>
        <d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
          <d:basicsearch>
            <d:select><d:prop><d:displayname/><d:getcontentlength/><oc:fileid/></d:prop></d:select>
            <d:from><d:scope><d:href>{scope}</d:href><d:depth>infinity</d:depth></d:scope></d:from>
            <d:where>{where_xml}</d:where>
            <d:orderby><d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order></d:orderby>
            <d:limit><d:nresults>30</d:nresults></d:limit>
          </d:basicsearch>
        </d:searchrequest>"#
    )
}

#[tokio::test(flavor = "multi_thread")]
async fn a_filename_search_finds_the_file_across_every_readable_root() {
    let f = fixture(false);
    let body = search_body(
        "/files/alice",
        "<d:like><d:prop><d:displayname/></d:prop><d:literal>%report%</d:literal></d:like>",
    );
    let r = send(&f, "SEARCH", "/remote.php/dav", &[], &body).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("report.txt"), "{}", r.body);
    assert!(!r.body.contains("beach.png"), "{}", r.body);
}

/// The photo timeline is a search on the media type, and the walker matches
/// names, so the prefix has to become an extension set.
#[tokio::test(flavor = "multi_thread")]
async fn the_gallery_query_returns_images_and_nothing_else() {
    let f = fixture(false);
    let body = search_body(
        "/files/alice",
        "<d:or>\
           <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>image/%</d:literal></d:like>\
           <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>video/%</d:literal></d:like>\
         </d:or>",
    );
    let r = send(&f, "SEARCH", "/remote.php/dav", &[], &body).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("holiday.jpg"), "{}", r.body);
    assert!(r.body.contains("beach.png"), "{}", r.body);
    assert!(!r.body.contains("report.txt"), "{}", r.body);
}

/// The scope is a security boundary, not a hint. A scope naming another
/// account is refused outright rather than narrowed or reinterpreted.
#[tokio::test(flavor = "multi_thread")]
async fn a_search_scoped_to_another_account_is_refused() {
    let f = fixture(false);
    let body = search_body(
        "/files/mallory",
        "<d:like><d:prop><d:displayname/></d:prop><d:literal>%report%</d:literal></d:like>",
    );
    let r = send(&f, "SEARCH", "/remote.php/dav", &[], &body).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST, "{}", r.body);
    assert!(!r.body.contains("report.txt"));
}

// -------------------------------------------------------------- favourites --

/// The star used to fill in and then revert: the write landed in the
/// dead-property store, answered 200, and the next PROPFIND still read 0 from
/// the favourites table.
#[tokio::test(flavor = "multi_thread")]
async fn a_favourite_survives_the_round_trip_and_the_report_lists_it() {
    let f = fixture(false);
    let path = format!("/remote.php/dav/files/alice/{}/report.txt", f.label);

    let set = r#"<?xml version="1.0"?>
        <d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
          <d:set><d:prop><oc:favorite>1</oc:favorite></d:prop></d:set>
        </d:propertyupdate>"#;
    let r = send(&f, "PROPPATCH", &path, &[], set).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("200 OK"), "{}", r.body);

    let read = r#"<?xml version="1.0"?>
        <d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
          <d:prop><oc:favorite/></d:prop></d:propfind>"#;
    let r = send(&f, "PROPFIND", &path, &[("Depth", "0")], read).await;
    assert!(
        r.body.contains("<oc:favorite>1</oc:favorite>"),
        "the flag must read back as set: {}",
        r.body
    );

    // The favourites screen, both ways round: the report iOS sends...
    let report = r#"<?xml version="1.0"?>
        <oc:filter-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
          <d:prop><d:getetag/><oc:fileid/></d:prop>
          <d:filter-rules><oc:favorite>1</oc:favorite></d:filter-rules>
        </oc:filter-files>"#;
    let r = send(&f, "REPORT", "/remote.php/dav/files/alice", &[], report).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("report.txt"), "{}", r.body);

    // ...and the search Android sends, which spells the literal differently.
    let body = search_body(
        "/files/alice",
        "<d:eq><d:prop><oc:favorite/></d:prop><d:literal>yes</d:literal></d:eq>",
    );
    let r = send(&f, "SEARCH", "/remote.php/dav", &[], &body).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("report.txt"), "{}", r.body);
    assert!(!r.body.contains("holiday.jpg"), "{}", r.body);

    // Unstarring is a `d:remove`, which is how Android clears it.
    let remove = r#"<?xml version="1.0"?>
        <d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
          <d:remove><d:prop><oc:favorite/></d:prop></d:remove>
        </d:propertyupdate>"#;
    let r = send(&f, "PROPPATCH", &path, &[], remove).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    let r = send(&f, "PROPFIND", &path, &[("Depth", "0")], read).await;
    assert!(r.body.contains("<oc:favorite>0</oc:favorite>"), "{}", r.body);
}

/// Live-photo pairing is metadata only: the property round-trips and nothing
/// on this server understands the relationship between the two files.
///
/// It has no source of its own, so it takes the dead-property path — which is
/// the whole claim, and the reason it is pinned here rather than assumed.
#[tokio::test(flavor = "multi_thread")]
async fn the_live_photo_property_round_trips_without_anyone_interpreting_it() {
    let f = fixture(false);
    let path = format!("/remote.php/dav/files/alice/{}/holiday.jpg", f.label);

    let set = r#"<?xml version="1.0"?>
        <d:propertyupdate xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">
          <d:set><d:prop><nc:metadata-files-live-photo>IMG_0042.MOV</nc:metadata-files-live-photo></d:prop></d:set>
        </d:propertyupdate>"#;
    let r = send(&f, "PROPPATCH", &path, &[], set).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("200 OK"), "{}", r.body);

    let read = r#"<?xml version="1.0"?>
        <d:propfind xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">
          <d:prop><nc:metadata-files-live-photo/></d:prop></d:propfind>"#;
    let r = send(&f, "PROPFIND", &path, &[("Depth", "0")], read).await;
    assert!(
        r.body.contains("IMG_0042.MOV"),
        "the value must come back verbatim: {}",
        r.body
    );
}

// ----------------------------------------------------- share expiry status --

/// A date the user already passed is a conflict, not a malformed request:
/// both share sheets branch on the status to re-open the date picker rather
/// than showing a generic failure.
#[tokio::test(flavor = "multi_thread")]
async fn a_share_expiry_in_the_past_is_409_inside_the_envelope() {
    let f = fixture(false);
    let form = format!(
        "path=/{}/report.txt&shareType=3&expireDate=2001-01-01",
        f.label
    );
    let r = send(
        &f,
        "POST",
        "/ocs/v2.php/apps/files_sharing/api/v1/shares",
        &[
            ("OCS-APIRequest", "true"),
            ("Accept", "application/json"),
            ("Content-Type", "application/x-www-form-urlencoded"),
        ],
        &form,
    )
    .await;
    assert!(r.body.contains("\"statuscode\":409"), "{}", r.body);
}

// ----------------------------------------------------------------- trashbin --

const TRASH: &str = "/remote.php/dav/trashbin/alice/trash";

/// Per-share trash is off by default, so a default install has none anywhere.
/// The collection still exists and answers an empty multistatus: a 404 there
/// makes the app render an error for a state that is normal.
#[tokio::test(flavor = "multi_thread")]
async fn an_empty_trashbin_is_a_207_not_a_404() {
    let f = fixture(false);
    let r = send(&f, "PROPFIND", TRASH, &[("Depth", "1")], "").await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("<d:collection/>"), "{}", r.body);
    // The trailing-slash spelling iOS sends is the same collection.
    let r = send(&f, "PROPFIND", &format!("{TRASH}/"), &[("Depth", "1")], "").await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_deleted_file_is_listed_restored_and_purged() {
    let f = fixture(true);
    let vpath = format!("{}/album/beach.png", f.label);
    f.app
        .core
        .delete(f.uid, &[vpath.clone()], false)
        .expect("delete into the trash");

    let r = send(&f, "PROPFIND", TRASH, &[("Depth", "1")], "").await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS, "{}", r.body);
    assert!(r.body.contains("beach.png"), "{}", r.body);
    // The original location is what the Deleted files screen shows under the
    // name, and it must be the whole path, not just the leaf.
    assert!(
        r.body
            .contains("<nc:trashbin-original-location>album/beach.png</nc:trashbin-original-location>"),
        "{}",
        r.body
    );
    // Without `D` both apps hide purge and restore.
    assert!(r.body.contains("<oc:permissions>GD</oc:permissions>"), "{}", r.body);

    let entry = entry_segment(&r.body);
    let r = send(
        &f,
        "MOVE",
        &format!("{TRASH}/{entry}"),
        &[(
            "Destination",
            "/remote.php/dav/trashbin/alice/restore/beach.png",
        )],
        "",
    )
    .await;
    assert_eq!(r.status, StatusCode::CREATED, "{}", r.body);
    assert!(f.app.core.stat_entry(f.uid, &vpath).is_ok(), "it came back");

    // Delete it again and purge it for real this time.
    f.app.core.delete(f.uid, &[vpath.clone()], false).unwrap();
    let r = send(&f, "PROPFIND", TRASH, &[("Depth", "1")], "").await;
    let entry = entry_segment(&r.body);
    let r = send(&f, "DELETE", &format!("{TRASH}/{entry}"), &[], "").await;
    assert_eq!(r.status, StatusCode::NO_CONTENT, "{}", r.body);
    let r = send(&f, "PROPFIND", TRASH, &[("Depth", "1")], "").await;
    assert!(!r.body.contains("beach.png"), "{}", r.body);
}

/// A `Destination` outside the restore collection is a request to move a
/// trashed file somewhere specific, which this server cannot do. Answering it
/// with a restore-to-original would put the file where nobody asked.
#[tokio::test(flavor = "multi_thread")]
async fn a_move_out_of_the_trash_to_anywhere_else_is_refused() {
    let f = fixture(true);
    let vpath = format!("{}/album/beach.png", f.label);
    f.app.core.delete(f.uid, &[vpath], false).unwrap();
    let r = send(&f, "PROPFIND", TRASH, &[("Depth", "1")], "").await;
    let entry = entry_segment(&r.body);

    let r = send(
        &f,
        "MOVE",
        &format!("{TRASH}/{entry}"),
        &[("Destination", "/remote.php/dav/files/alice/elsewhere.png")],
        "",
    )
    .await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST, "{}", r.body);
}

/// An entry naming a share the caller cannot reach is not-found, never
/// forbidden: 403 would confirm the entry exists.
#[tokio::test(flavor = "multi_thread")]
async fn an_entry_in_an_unreachable_share_is_404() {
    let f = fixture(true);
    let r = send(
        &f,
        "DELETE",
        &format!("{TRASH}/9999.0123456789abcdef0123456789abcdef"),
        &[],
        "",
    )
    .await;
    assert_eq!(r.status, StatusCode::NOT_FOUND);
}

/// Pull the first entry's URL segment out of a trash multistatus.
///
/// The collection's own row carries the same prefix with nothing after it, so
/// the first match is skipped rather than returned as an empty segment.
fn entry_segment(body: &str) -> String {
    let marker = "/remote.php/dav/trashbin/alice/trash/";
    for (idx, _) in body.match_indices(marker) {
        let rest = &body[idx + marker.len()..];
        let end = rest.find('<').expect("a closing href tag");
        if !rest[..end].is_empty() {
            return rest[..end].to_string();
        }
    }
    panic!("no trash entry href in {body}");
}

// ------------------------------------------------------- account lifecycle --

/// Both apps call this when the user removes the account. Without it the app
/// password stays valid for the life of the server: the phone forgets the
/// credential, the server does not.
#[tokio::test(flavor = "multi_thread")]
async fn logout_revokes_the_credential_the_request_arrived_on() {
    let f = fixture(false);
    let r = send(
        &f,
        "DELETE",
        "/ocs/v2.php/core/apppassword",
        &[("OCS-APIRequest", "true"), ("Accept", "application/json")],
        "",
    )
    .await;
    assert_eq!(r.status, StatusCode::OK, "{}", r.body);
    assert!(r.body.contains("\"statuscode\":200"), "{}", r.body);

    // The credential is gone: the very next request with it fails.
    let r = send(
        &f,
        "GET",
        "/ocs/v2.php/cloud/user",
        &[("OCS-APIRequest", "true"), ("Accept", "application/json")],
        "",
    )
    .await;
    assert_ne!(r.status, StatusCode::OK, "{}", r.body);
}

/// Outside the caller's scope and no such account are deliberately the same
/// answer: this is an account-name oracle otherwise.
#[tokio::test(flavor = "multi_thread")]
async fn another_account_is_404_under_the_default_lookup_scope() {
    let f = fixture(false);
    f.app
        .auth
        .create_user("bob", &secrecy::SecretString::from("hunter2hunter2".to_string()))
        .unwrap();
    let r = send(
        &f,
        "GET",
        "/ocs/v2.php/cloud/users/bob",
        &[("OCS-APIRequest", "true"), ("Accept", "application/json")],
        "",
    )
    .await;
    assert!(r.body.contains("\"statuscode\":404"), "{}", r.body);

    // Your own account is always in scope: asking about yourself reveals
    // nothing you do not already hold.
    let r = send(
        &f,
        "GET",
        "/ocs/v2.php/cloud/users/alice",
        &[("OCS-APIRequest", "true"), ("Accept", "application/json")],
        "",
    )
    .await;
    assert!(r.body.contains("\"statuscode\":200"), "{}", r.body);
    assert!(r.body.contains("\"displayname\""), "{}", r.body);
    assert!(
        !r.body.contains("\"quota\""),
        "another account's quota is nobody else's business: {}",
        r.body
    );
}

/// The client treats any non-200 as "no wipe", so the absence of a mark is a
/// 404 and the whole feature fails safe.
#[tokio::test(flavor = "multi_thread")]
async fn a_wipe_check_answers_only_once_the_device_is_marked() {
    let f = fixture(false);
    let form = format!("token={}", f.token);
    let hdrs = [("Content-Type", "application/x-www-form-urlencoded")];

    let r = send(&f, "POST", "/index.php/core/wipe/check", &hdrs, &form).await;
    assert_eq!(r.status, StatusCode::OK, "{}", r.body);
    assert!(r.body.contains("\"wipe\":false"), "{}", r.body);

    // A credential that does not verify at all is a different answer, so a
    // device can tell "nothing to do" from "this server does not know me".
    let r = send(
        &f,
        "POST",
        "/index.php/core/wipe/check",
        &hdrs,
        "token=stow_not_a_real_credential",
    )
    .await;
    assert_eq!(r.status, StatusCode::NOT_FOUND, "{}", r.body);

    let id = f
        .app
        .auth
        .list_app_passwords(f.uid)
        .unwrap()
        .first()
        .unwrap()
        .id;
    assert!(f.app.auth.request_wipe(f.uid, id).unwrap());

    let r = send(&f, "POST", "/index.php/core/wipe/check", &hdrs, &form).await;
    assert_eq!(r.status, StatusCode::OK, "{}", r.body);
    assert!(r.body.contains("\"wipe\":true"), "{}", r.body);

    // Marking does not revoke: a revoked credential could not ask, and the
    // files on the lost device would stay where they are.
    let r = send(&f, "POST", "/index.php/core/wipe/success", &hdrs, &form).await;
    assert_eq!(r.status, StatusCode::OK, "{}", r.body);
    assert!(
        f.app.auth.list_app_passwords(f.uid).unwrap().is_empty(),
        "reporting success retires the credential"
    );
}

// -------------------------------------------- direct download / unified search --

/// A file id the caller cannot read is not-found inside the OCS envelope,
/// never forbidden.
#[tokio::test(flavor = "multi_thread")]
async fn the_direct_endpoint_refuses_a_file_id_the_caller_cannot_read() {
    let f = fixture(false);
    let r = send(
        &f,
        "POST",
        "/ocs/v2.php/apps/dav/api/v1/direct",
        &[
            ("OCS-APIRequest", "true"),
            ("Accept", "application/json"),
            ("Content-Type", "application/x-www-form-urlencoded"),
        ],
        "fileId=987654321",
    )
    .await;
    assert!(r.body.contains("\"statuscode\":404"), "{}", r.body);
}

#[tokio::test(flavor = "multi_thread")]
async fn the_unified_search_provider_is_advertised_and_answers_the_same_term() {
    let f = fixture(false);
    let hdrs = [("OCS-APIRequest", "true"), ("Accept", "application/json")];

    let r = send(&f, "GET", "/ocs/v2.php/search/providers", &hdrs, "").await;
    assert!(r.body.contains("\"id\":\"files\""), "{}", r.body);

    let r = send(
        &f,
        "GET",
        "/ocs/v2.php/search/providers/files/search?term=report&limit=10",
        &hdrs,
        "",
    )
    .await;
    assert!(r.body.contains("report.txt"), "{}", r.body);
    // Both apps read `attributes.path` in preference to parsing `resourceUrl`.
    assert!(r.body.contains("\"path\""), "{}", r.body);
}
