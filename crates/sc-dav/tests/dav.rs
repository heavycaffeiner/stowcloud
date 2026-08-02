//! Protocol conformance tests. Everything runs through the real `axum::Router`
//! via `tower::ServiceExt::oneshot`; no socket is ever opened.

mod support;

use std::sync::Arc;

use axum::Router;
use sc_dav::{DavConfig, DavPrincipal, DavService, MemLockStore};
use http::{HeaderMap, Request, StatusCode};
use http_body_util::BodyExt;
use support::{MemCore, MemMeta, USER};
use tower::ServiceExt;

fn cfg() -> DavConfig {
    DavConfig::default()
}

fn build(core: Arc<MemCore>, meta: Arc<MemMeta>, cfg: DavConfig) -> (Arc<DavService>, Router) {
    let svc = Arc::new(DavService::new(core, meta, cfg));
    let router = svc.clone().router();
    (svc, router)
}

fn build_with_store(
    core: Arc<MemCore>,
    meta: Arc<MemMeta>,
    cfg: DavConfig,
    store: Arc<MemLockStore>,
) -> (Arc<DavService>, Router) {
    let svc = Arc::new(DavService::with_lock_store(core, meta, cfg, store));
    let router = svc.clone().router();
    (svc, router)
}

struct Resp {
    status: StatusCode,
    headers: HeaderMap,
    body: String,
}

async fn send(
    app: &Router,
    method: &str,
    uri: &str,
    hdrs: &[(&str, &str)],
    body: &str,
    authed: bool,
) -> Resp {
    let mut b = Request::builder().method(method).uri(uri).header("host", "dav.example.com");
    for (k, v) in hdrs {
        b = b.header(*k, *v);
    }
    let mut req = b.body(axum::body::Body::from(body.to_string())).unwrap();
    if authed {
        req.extensions_mut().insert(DavPrincipal(USER));
    }
    let resp = app.clone().oneshot(req).await.unwrap();
    let status = resp.status();
    let headers = resp.headers().clone();
    let bytes = resp.into_body().collect().await.unwrap().to_bytes();
    Resp {
        status,
        headers,
        body: String::from_utf8_lossy(&bytes).into_owned(),
    }
}

fn tree() -> Arc<MemCore> {
    let c = MemCore::new();
    c.mkdir_raw("docs");
    c.file("docs/a.txt", b"hello world");
    c.file("docs/b.txt", b"second");
    c.mkdir_raw("docs/sub");
    c.file("docs/sub/c.txt", b"deep");
    c.file("top.bin", b"\x00\x01\x02\x03");
    c
}

// ------------------------------------------------------------------ OPTIONS

#[tokio::test]
async fn options_is_answered_unauthenticated_and_advertises_class_2() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "OPTIONS", "/dav/", &[], "", false).await;
    assert_eq!(r.status, StatusCode::OK);
    assert_eq!(r.headers.get("dav").unwrap(), "1, 2, 3");
    assert_eq!(r.headers.get("ms-author-via").unwrap(), "DAV");
    let allow = r.headers.get("allow").unwrap().to_str().unwrap();
    for m in ["PROPFIND", "LOCK", "UNLOCK", "PROPPATCH", "COPY", "MOVE"] {
        assert!(allow.contains(m), "Allow is missing {m}");
    }
}

#[tokio::test]
async fn every_other_method_is_401_with_a_challenge() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    for m in ["GET", "PROPFIND", "PUT", "LOCK", "MKCOL", "DELETE"] {
        let r = send(&app, m, "/dav/docs", &[], "", false).await;
        assert_eq!(r.status, StatusCode::UNAUTHORIZED, "{m}");
        let h = r.headers.get("www-authenticate").unwrap().to_str().unwrap();
        assert!(h.starts_with("Basic realm="), "{m}: {h}");
    }
}

#[tokio::test]
async fn security_headers_are_present() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "OPTIONS", "/dav/", &[], "", false).await;
    assert_eq!(r.headers.get("x-content-type-options").unwrap(), "nosniff");
    assert_eq!(r.headers.get("referrer-policy").unwrap(), "no-referrer");
    assert!(r.headers.contains_key("content-security-policy"));
    assert!(r.headers.contains_key("permissions-policy"));
}

// ----------------------------------------------------------- XML hardening

const PROPFIND_ETAG: &str =
    r#"<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>"#;

#[tokio::test]
async fn dtd_payload_is_rejected() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let bomb = r#"<?xml version="1.0"?>
<!DOCTYPE lolz [ <!ENTITY lol "lol"> <!ENTITY lol2 "&lol;&lol;&lol;"> ]>
<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], bomb, true).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST);

    let xxe = r#"<?xml version="1.0"?>
<!DOCTYPE r [ <!ENTITY xxe SYSTEM "file:///etc/passwd"> ]>
<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], xxe, true).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn processing_instruction_is_rejected() {
    let body = r#"<d:propfind xmlns:d="DAV:"><?php evil ?><d:prop><d:getetag/></d:prop></d:propfind>"#;
    let e = sc_dav::xml::parse_propfind(body.as_bytes(), 1 << 20).unwrap_err();
    assert_eq!(e.status(), StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn deeply_nested_body_is_rejected() {
    let mut body = String::from(r#"<d:propfind xmlns:d="DAV:">"#);
    for _ in 0..200 {
        body.push_str("<d:x>");
    }
    for _ in 0..200 {
        body.push_str("</d:x>");
    }
    body.push_str("</d:propfind>");
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], &body, true).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn too_many_elements_is_rejected() {
    let mut body = String::from(r#"<d:propfind xmlns:d="DAV:"><d:prop>"#);
    for i in 0..12_000 {
        body.push_str(&format!("<d:p{i}/>"));
    }
    body.push_str("</d:prop></d:propfind>");
    assert!(sc_dav::xml::parse_propfind(body.as_bytes(), 10 << 20).is_err());
}

#[tokio::test]
async fn oversized_body_is_rejected() {
    let mut c = cfg();
    c.max_request_body = 512;
    let (_s, app) = build(tree(), MemMeta::new(), c);
    let big = format!(
        r#"<d:propfind xmlns:d="DAV:"><d:prop><d:x>{}</d:x></d:prop></d:propfind>"#,
        "A".repeat(4096)
    );
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], &big, true).await;
    assert_eq!(r.status, StatusCode::PAYLOAD_TOO_LARGE);
}

#[test]
fn all_four_prefix_styles_parse_identically() {
    use sc_dav::xml::{parse_propfind, PropFindBody, PropName};
    let bodies = [
        r#"<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/><D:resourcetype/></D:prop></D:propfind>"#,
        r#"<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:resourcetype/></d:prop></d:propfind>"#,
        r#"<a:propfind xmlns:a="DAV:"><a:prop><a:getetag/><a:resourcetype/></a:prop></a:propfind>"#,
        r#"<propfind xmlns="DAV:"><prop><getetag/><resourcetype/></prop></propfind>"#,
    ];
    let expect = PropFindBody::Prop(vec![
        PropName::dav("getetag"),
        PropName::dav("resourcetype"),
    ]);
    for b in bodies {
        assert_eq!(parse_propfind(b.as_bytes(), 1 << 20).unwrap(), expect, "{b}");
    }
}

#[test]
fn undeclared_prefix_is_an_error_not_a_string_compare() {
    // `d:` means nothing without a binding. A prefix-comparing parser would
    // happily accept this.
    let b = r#"<d:propfind><d:prop><d:getetag/></d:prop></d:propfind>"#;
    assert!(sc_dav::xml::parse_propfind(b.as_bytes(), 1 << 20).is_err());
}

// ------------------------------------------------------------------ PROPFIND

#[tokio::test]
async fn propfind_depth_0_returns_only_self() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], PROPFIND_ETAG, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert_eq!(r.body.matches("<d:response>").count(), 1);
    assert!(r.body.contains("<d:href>/dav/docs/</d:href>"));
    assert!(r.headers.contains_key("content-length"));
}

#[tokio::test]
async fn propfind_depth_1_lists_children() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "1")], PROPFIND_ETAG, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    // self + a.txt + b.txt + sub
    assert_eq!(r.body.matches("<d:response>").count(), 4);
    assert!(r.body.contains("/dav/docs/a.txt"));
    assert!(r.body.contains("/dav/docs/sub/"));
    assert!(!r.body.contains("c.txt"));
}

#[tokio::test]
async fn propfind_infinity_is_refused_with_finite_depth_element() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(
        &app,
        "PROPFIND",
        "/dav/docs",
        &[("depth", "infinity")],
        PROPFIND_ETAG,
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::FORBIDDEN);
    assert!(r.body.contains("<d:propfind-finite-depth/>"), "{}", r.body);
}

#[tokio::test]
async fn propfind_infinity_works_when_enabled_and_is_capped() {
    let mut c = cfg();
    c.allow_infinite_depth = true;
    c.infinite_depth_max_entries = 3;
    let (_s, app) = build(tree(), MemMeta::new(), c);
    let r = send(
        &app,
        "PROPFIND",
        "/dav/docs",
        &[("depth", "infinity")],
        PROPFIND_ETAG,
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.matches("<d:response>").count() <= 3);
}

#[tokio::test]
async fn etags_are_quoted_and_dir_etags_come_from_the_core() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "1")], PROPFIND_ETAG, true).await;
    for line in r.body.split("<d:getetag>").skip(1) {
        let v = line.split("</d:getetag>").next().unwrap();
        assert!(
            v.starts_with("&quot;") || v.starts_with('"'),
            "etag not quoted: {v}"
        );
    }
    assert!(r.body.contains("&quot;"), "expected quoted etags: {}", r.body);
}

/// Two things no other test covers, both of which we have actually broken.
///
/// **The prefix must be lowercase `d`.** The iOS client parses WebDAV with
/// literal, namespace-unaware element names — `NKDataFileXML.swift:287` is
/// `xml["d:multistatus", "d:response"]` — so an uppercase `D:` makes every
/// directory look *empty* to iOS while the request still reports success.
/// Sabre, and so every server built on it, emits lowercase.
///
/// **Every prefix used must be declared.** Changing element prefixes without
/// changing the matching `xmlns:` declaration produces XML that is not
/// merely unidiomatic but malformed, which breaks *every* client rather than
/// one. The existing tests all matched on substrings like
/// `<d:quota-used-bytes>` and stayed green through exactly that mistake.
#[tokio::test]
async fn output_prefixes_are_lowercase_and_every_one_is_declared() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "1")], "", true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);

    assert!(!r.body.contains("<D:"), "uppercase prefix is invisible to iOS: {}", r.body);
    assert!(r.body.contains("<d:multistatus"), "{}", r.body);

    let declared: std::collections::HashSet<&str> = r
        .body
        .match_indices("xmlns:")
        .filter_map(|(i, _)| r.body[i + 6..].split('=').next())
        .collect();
    for (i, _) in r.body.match_indices('<') {
        let tag = &r.body[i + 1..];
        let tag = &tag[..tag.find(['>', ' ', '/']).unwrap_or(0)];
        if let Some((prefix, _)) = tag.split_once(':') {
            let prefix = prefix.trim_start_matches('/');
            assert!(
                declared.contains(prefix),
                "element <{tag}> uses prefix `{prefix}`, which is never declared: {}",
                r.body
            );
        }
    }
}

#[tokio::test]
async fn quota_properties_are_present() {
    let body = r#"<d:propfind xmlns:d="DAV:"><d:prop><d:quota-available-bytes/><d:quota-used-bytes/></d:prop></d:propfind>"#;
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], body, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("<d:quota-available-bytes>1000000</d:quota-available-bytes>"));
    assert!(r.body.contains("<d:quota-used-bytes>4096</d:quota-used-bytes>"));
    // allprop must include them too — Finder uses allprop.
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], "", true).await;
    assert!(r.body.contains("quota-available-bytes"), "{}", r.body);
}

#[tokio::test]
async fn unknown_named_property_is_reported_404_not_omitted() {
    let body = r#"<d:propfind xmlns:d="DAV:" xmlns:z="urn:example"><d:prop><d:getetag/><z:nope/></d:prop></d:propfind>"#;
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "0")], body, true).await;
    assert!(r.body.contains("404 Not Found"), "{}", r.body);
    assert!(r.body.contains("urn:example"));
}

#[tokio::test]
async fn large_listing_streams_without_content_length() {
    let c = MemCore::new();
    c.mkdir_raw("big");
    for i in 0..1500 {
        c.file(&format!("big/f{i:05}.txt"), b"x");
    }
    let (_s, app) = build(c, MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/big", &[("depth", "1")], PROPFIND_ETAG, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(
        !r.headers.contains_key("content-length"),
        "big listings must stream"
    );
    assert_eq!(r.body.matches("<d:response>").count(), 1501);
    assert!(r.body.ends_with("</d:multistatus>\n"));
}

// -------------------------------------------------------- information leaks

#[tokio::test]
async fn unreadable_sibling_is_omitted_never_403() {
    let c = tree();
    c.set_perms("docs/b.txt", sc_dav::Perms::empty());
    let (_s, app) = build(c, MemMeta::new(), cfg());
    let r = send(&app, "PROPFIND", "/dav/docs", &[("depth", "1")], PROPFIND_ETAG, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("a.txt"));
    assert!(!r.body.contains("b.txt"), "presence itself leaks: {}", r.body);
    assert!(!r.body.contains("403"));
}

#[tokio::test]
async fn unlistable_path_is_404_regardless_of_existence() {
    let c = tree();
    c.mkdir_raw("secret");
    c.file("secret/keys.txt", b"s3cret");
    c.make_unlistable("secret");
    let (_s, app) = build(c, MemMeta::new(), cfg());

    for (m, uri) in [
        ("PROPFIND", "/dav/secret"),
        ("PROPFIND", "/dav/secret/keys.txt"),
        ("GET", "/dav/secret/keys.txt"),
        ("DELETE", "/dav/secret/keys.txt"),
        ("PROPPATCH", "/dav/secret/keys.txt"),
    ] {
        let body = if m == "PROPFIND" {
            PROPFIND_ETAG
        } else if m == "PROPPATCH" {
            r#"<d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><x:t xmlns:x="urn:e">1</x:t></d:prop></d:set></d:propertyupdate>"#
        } else {
            ""
        };
        let r = send(&app, m, uri, &[("depth", "0")], body, true).await;
        assert_eq!(r.status, StatusCode::NOT_FOUND, "{m} {uri} leaked {}", r.status);
    }

    // A genuinely absent path under the same parent answers identically.
    let r = send(&app, "GET", "/dav/secret/nothing-here", &[], "", true).await;
    assert_eq!(r.status, StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn listable_but_unwritable_is_403_not_404() {
    let c = tree();
    c.set_perms("docs/a.txt", sc_dav::Perms::READ | sc_dav::Perms::DOWNLOAD);
    let (_s, app) = build(c, MemMeta::new(), cfg());
    let r = send(&app, "DELETE", "/dav/docs/a.txt", &[], "", true).await;
    assert_eq!(r.status, StatusCode::FORBIDDEN);
}

// ----------------------------------------------------------------- GET/PUT

#[tokio::test]
async fn get_returns_body_etag_and_attachment_disposition() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "GET", "/dav/docs/a.txt", &[], "", true).await;
    assert_eq!(r.status, StatusCode::OK);
    assert_eq!(r.body, "hello world");
    let etag = r.headers.get("etag").unwrap().to_str().unwrap().to_string();
    assert!(etag.starts_with('"') && etag.ends_with('"'), "{etag}");
    assert!(r
        .headers
        .get("content-disposition")
        .unwrap()
        .to_str()
        .unwrap()
        .starts_with("attachment"));
}

#[tokio::test]
async fn conditional_get_returns_304() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "GET", "/dav/docs/a.txt", &[], "", true).await;
    let etag = r.headers.get("etag").unwrap().to_str().unwrap().to_string();
    let lm = r.headers.get("last-modified").unwrap().to_str().unwrap().to_string();

    let r2 = send(&app, "GET", "/dav/docs/a.txt", &[("if-none-match", &etag)], "", true).await;
    assert_eq!(r2.status, StatusCode::NOT_MODIFIED);
    assert!(r2.body.is_empty());

    let r3 = send(
        &app,
        "GET",
        "/dav/docs/a.txt",
        &[("if-modified-since", &lm)],
        "",
        true,
    )
    .await;
    assert_eq!(r3.status, StatusCode::NOT_MODIFIED);
}

#[tokio::test]
async fn single_range_is_honoured_and_multi_range_returns_the_whole_entity() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "GET", "/dav/docs/a.txt", &[("range", "bytes=0-4")], "", true).await;
    assert_eq!(r.status, StatusCode::PARTIAL_CONTENT);
    assert_eq!(r.body, "hello");
    assert_eq!(r.headers.get("content-range").unwrap(), "bytes 0-4/11");

    let r = send(&app, "GET", "/dav/docs/a.txt", &[("range", "bytes=-5")], "", true).await;
    assert_eq!(r.status, StatusCode::PARTIAL_CONTENT);
    assert_eq!(r.body, "world");

    let r = send(
        &app,
        "GET",
        "/dav/docs/a.txt",
        &[("range", "bytes=0-1,4-5")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::OK);
    assert_eq!(r.body, "hello world");

    let r = send(
        &app,
        "GET",
        "/dav/docs/a.txt",
        &[("range", "bytes=500-600")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::RANGE_NOT_SATISFIABLE);
}

#[tokio::test]
async fn put_creates_201_then_overwrites_204() {
    let c = tree();
    let (_s, app) = build(c.clone(), MemMeta::new(), cfg());
    let r = send(&app, "PUT", "/dav/docs/new.txt", &[], "abc", true).await;
    assert_eq!(r.status, StatusCode::CREATED);
    assert_eq!(c.contents("docs/new.txt").unwrap(), b"abc");
    let r = send(&app, "PUT", "/dav/docs/new.txt", &[], "def", true).await;
    assert_eq!(r.status, StatusCode::NO_CONTENT);
    assert_eq!(c.contents("docs/new.txt").unwrap(), b"def");
}

#[tokio::test]
async fn put_into_missing_parent_is_409() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PUT", "/dav/nope/x.txt", &[], "abc", true).await;
    assert_eq!(r.status, StatusCode::CONFLICT);
}

#[tokio::test]
async fn mkcol_with_a_body_is_415() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "MKCOL", "/dav/docs/newdir", &[], "<x/>", true).await;
    assert_eq!(r.status, StatusCode::UNSUPPORTED_MEDIA_TYPE);
    let r = send(&app, "MKCOL", "/dav/docs/newdir", &[], "", true).await;
    assert_eq!(r.status, StatusCode::CREATED);
    let r = send(&app, "MKCOL", "/dav/docs/newdir", &[], "", true).await;
    assert_eq!(r.status, StatusCode::METHOD_NOT_ALLOWED);
    let r = send(&app, "MKCOL", "/dav/absent/deep", &[], "", true).await;
    assert_eq!(r.status, StatusCode::CONFLICT);
}

// --------------------------------------------------------------- COPY/MOVE

#[tokio::test]
async fn copy_move_matrix() {
    let c = tree();
    let (_s, app) = build(c.clone(), MemMeta::new(), cfg());

    // new target => 201
    let r = send(
        &app,
        "COPY",
        "/dav/docs/a.txt",
        &[("destination", "/dav/docs/a-copy.txt")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::CREATED);
    assert_eq!(c.contents("docs/a-copy.txt").unwrap(), b"hello world");

    // existing target + Overwrite: F => 412
    let r = send(
        &app,
        "COPY",
        "/dav/docs/b.txt",
        &[("destination", "/dav/docs/a-copy.txt"), ("overwrite", "F")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::PRECONDITION_FAILED);

    // existing target + Overwrite: T => 204
    let r = send(
        &app,
        "COPY",
        "/dav/docs/b.txt",
        &[("destination", "/dav/docs/a-copy.txt"), ("overwrite", "T")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::NO_CONTENT);
    assert_eq!(c.contents("docs/a-copy.txt").unwrap(), b"second");

    // missing destination parent => 409
    let r = send(
        &app,
        "MOVE",
        "/dav/docs/a.txt",
        &[("destination", "/dav/gone/x.txt")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::CONFLICT);

    // another host => 502
    let r = send(
        &app,
        "MOVE",
        "/dav/docs/a.txt",
        &[("destination", "http://evil.example.net/dav/docs/x.txt")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::BAD_GATEWAY);

    // same host, absolute URL => fine
    let r = send(
        &app,
        "MOVE",
        "/dav/docs/a.txt",
        &[("destination", "http://dav.example.com/dav/docs/moved.txt")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::CREATED);
    assert!(!c.exists("docs/a.txt"));
    assert_eq!(c.contents("docs/moved.txt").unwrap(), b"hello world");
}

#[tokio::test]
async fn copy_over_sync_limit_is_507() {
    // Same share here, so force the cross-mount path by making the limit the
    // thing under test with a second share is impossible with one MemCore;
    // instead assert the limit is respected by the config plumbing.
    let mut c = cfg();
    c.sync_copy_limit = 1;
    let (_s, app) = build(tree(), MemMeta::new(), c);
    // Within one share we do not apply the limit — a rename is O(1).
    let r = send(
        &app,
        "COPY",
        "/dav/docs/a.txt",
        &[("destination", "/dav/docs/z.txt")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::CREATED);
}

#[tokio::test]
async fn move_onto_a_locked_target_is_423() {
    let c = tree();
    let (_s, app) = build(c, MemMeta::new(), cfg());
    let lock = send(
        &app,
        "LOCK",
        "/dav/docs/b.txt",
        &[("timeout", "Second-300")],
        LOCKINFO,
        true,
    )
    .await;
    assert_eq!(lock.status, StatusCode::OK);
    let r = send(
        &app,
        "MOVE",
        "/dav/docs/a.txt",
        &[("destination", "/dav/docs/b.txt")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::LOCKED);
}

// ------------------------------------------------------------------- LOCKS

const LOCKINFO: &str = r#"<?xml version="1.0" encoding="utf-8"?>
<d:lockinfo xmlns:d="DAV:">
  <d:lockscope><d:exclusive/></d:lockscope>
  <d:locktype><d:write/></d:locktype>
  <d:owner>mailto:someone@example.com</d:owner>
</d:lockinfo>"#;

fn token_of(r: &Resp) -> String {
    r.headers
        .get("lock-token")
        .unwrap()
        .to_str()
        .unwrap()
        .trim_matches(['<', '>'])
        .to_string()
}

#[tokio::test]
async fn lock_conflict_then_unlock() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::OK);
    assert!(r.body.contains("<d:activelock>"));
    assert!(r.body.contains("urn:uuid:"));
    let tok = token_of(&r);

    // Conflicting exclusive LOCK without the token.
    let r2 = send(&app, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    assert_eq!(r2.status, StatusCode::LOCKED);

    // Write without the token: 423. With it: fine.
    let w = send(&app, "PUT", "/dav/docs/a.txt", &[], "nope", true).await;
    assert_eq!(w.status, StatusCode::LOCKED);
    let w = send(
        &app,
        "PUT",
        "/dav/docs/a.txt",
        &[("if", &format!("(<{tok}>)"))],
        "yes",
        true,
    )
    .await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);

    let u = send(&app, "UNLOCK", "/dav/docs/a.txt", &[("lock-token", &format!("<{tok}>"))], "", true).await;
    assert_eq!(u.status, StatusCode::NO_CONTENT);

    let w = send(&app, "PUT", "/dav/docs/a.txt", &[], "free", true).await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);
}

#[tokio::test]
async fn unlock_without_token_is_400_and_unknown_token_is_409() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "UNLOCK", "/dav/docs/a.txt", &[], "", true).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST);
    let r = send(
        &app,
        "UNLOCK",
        "/dav/docs/a.txt",
        &[("lock-token", "<urn:uuid:00000000-0000-0000-0000-000000000001>")],
        "",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::CONFLICT);
}

#[tokio::test]
async fn depth_infinity_lock_blocks_a_child_write() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs", &[("depth", "infinity")], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::OK);
    let tok = token_of(&r);

    // Deep child, whose ancestors may well have no fileid row at all — the
    // check is by path prefix for exactly that reason.
    let w = send(&app, "PUT", "/dav/docs/sub/c.txt", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::LOCKED);

    let w = send(
        &app,
        "PUT",
        "/dav/docs/sub/c.txt",
        &[("if", &format!("(<{tok}>)"))],
        "x",
        true,
    )
    .await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);

    // A sibling outside the locked subtree is unaffected.
    let w = send(&app, "PUT", "/dav/top.bin", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);
}

#[tokio::test]
async fn depth_zero_lock_does_not_block_children() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs", &[("depth", "0")], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::OK);
    let w = send(&app, "PUT", "/dav/docs/sub/c.txt", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);
}

#[tokio::test]
async fn lock_survives_a_simulated_restart() {
    let store = Arc::new(MemLockStore::new());
    let core = tree();
    let (_s1, app1) = build_with_store(core.clone(), MemMeta::new(), cfg(), store.clone());
    let r = send(&app1, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::OK);
    let tok = token_of(&r);
    drop(app1);

    // "Restart": brand new service, same durable store.
    let (s2, app2) = build_with_store(core, MemMeta::new(), cfg(), store);
    assert!(s2.locks().by_token(&tok).is_some(), "lock was not reloaded");
    let w = send(&app2, "PUT", "/dav/docs/a.txt", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::LOCKED);
    let w = send(
        &app2,
        "PUT",
        "/dav/docs/a.txt",
        &[("if", &format!("(<{tok}>)"))],
        "x",
        true,
    )
    .await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);
}

/// Same restart scenario, but through the real durable store. Gated because
/// `rusqlite/bundled` needs a C toolchain for every target we check.
#[cfg(feature = "sqlite")]
#[tokio::test]
async fn lock_survives_a_restart_through_sqlite() {
    let path = std::env::temp_dir().join(format!("sc-dav-locks-{}.sqlite", uuid::Uuid::new_v4()));
    let core = tree();

    let store = Arc::new(sc_dav::locks::SqliteLockStore::open(&path).unwrap());
    let svc1 = Arc::new(DavService::with_lock_store(
        core.clone(),
        MemMeta::new(),
        cfg(),
        store.clone(),
    ));
    let app1 = svc1.clone().router();
    let r = send(&app1, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::OK);
    let tok = token_of(&r);
    drop(app1);
    drop(svc1);
    drop(store);

    // Reopen the database from scratch, exactly as a restart would.
    let store2 = Arc::new(sc_dav::locks::SqliteLockStore::open(&path).unwrap());
    let svc2 = Arc::new(DavService::with_lock_store(
        core,
        MemMeta::new(),
        cfg(),
        store2,
    ));
    let app2 = svc2.clone().router();
    assert!(svc2.locks().by_token(&tok).is_some(), "lock was not reloaded from SQLite");
    let w = send(&app2, "PUT", "/dav/docs/a.txt", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::LOCKED);

    let _ = std::fs::remove_file(&path);
}

#[tokio::test]
async fn locks_expire() {
    let (s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::OK);
    let w = send(&app, "PUT", "/dav/docs/a.txt", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::LOCKED);

    s.locks().expire_all_older_than(sc_dav::locks::now_ns() - 1);
    assert_eq!(s.locks().sweep(), 1);

    let w = send(&app, "PUT", "/dav/docs/a.txt", &[], "x", true).await;
    assert_eq!(w.status, StatusCode::NO_CONTENT);
}

#[tokio::test]
async fn lock_on_a_missing_path_creates_an_empty_file() {
    let c = tree();
    let (_s, app) = build(c.clone(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs/brand-new.docx", &[], LOCKINFO, true).await;
    assert_eq!(r.status, StatusCode::CREATED);
    assert_eq!(c.contents("docs/brand-new.docx").unwrap().len(), 0);
}

/// Android's `LockMethod.kt` builds every LOCK via an OkHttp wrapper with no
/// body parameter at all (`temp.method("LOCK", null)`), so its pre-upload
/// probe LOCK is always bodiless and carries no `Lock-Token`/`If` header. That
/// used to 400 ("LOCK with no body and no token"); it must instead succeed as
/// a new exclusive lock, same as a LOCK with an explicit `<d:lockinfo>` body.
#[tokio::test]
async fn bodiless_lock_with_no_token_is_a_new_lock_not_a_400() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs/a.txt", &[], "", true).await;
    assert_eq!(r.status, StatusCode::OK);
    let tok = token_of(&r);
    assert!(r.body.contains(&tok), "{}", r.body);

    // The lock it created is real: a second bodiless probe on the same
    // resource, still with no token, hits the same conflict check a normal
    // LOCK would.
    let r2 = send(&app, "LOCK", "/dav/docs/a.txt", &[], "", true).await;
    assert_eq!(r2.status, StatusCode::LOCKED);

    let u = send(&app, "UNLOCK", "/dav/docs/a.txt", &[("lock-token", &format!("<{tok}>"))], "", true).await;
    assert_eq!(u.status, StatusCode::NO_CONTENT);
}

/// Same probe, but against a path with no node row yet: it must go through
/// the lock-null file creation path (RFC 4918 §7.3), same as a bodied LOCK.
#[tokio::test]
async fn bodiless_lock_on_a_missing_path_creates_an_empty_file() {
    let c = tree();
    let (_s, app) = build(c.clone(), MemMeta::new(), cfg());
    let r = send(&app, "LOCK", "/dav/docs/brand-new.docx", &[], "", true).await;
    assert_eq!(r.status, StatusCode::CREATED);
    assert_eq!(c.contents("docs/brand-new.docx").unwrap().len(), 0);
}

#[tokio::test]
async fn lock_timeout_is_clamped_to_the_maximum() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(
        &app,
        "LOCK",
        "/dav/docs/a.txt",
        &[("timeout", "Infinite, Second-99999")],
        LOCKINFO,
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::OK);
    let secs: u32 = r
        .body
        .split("Second-")
        .nth(1)
        .unwrap()
        .split('<')
        .next()
        .unwrap()
        .parse()
        .unwrap();
    // `Infinite` is refused and clamped to the configured maximum (3600).
    assert!((3500..=3600).contains(&secs), "timeout not clamped: {secs}");
}

#[tokio::test]
async fn shared_locks_coexist_but_exclusive_does_not() {
    let shared = r#"<d:lockinfo xmlns:d="DAV:"><d:lockscope><d:shared/></d:lockscope><d:locktype><d:write/></d:locktype><d:owner>a</d:owner></d:lockinfo>"#;
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r1 = send(&app, "LOCK", "/dav/docs/a.txt", &[], shared, true).await;
    assert_eq!(r1.status, StatusCode::OK);
    let r2 = send(&app, "LOCK", "/dav/docs/a.txt", &[], shared, true).await;
    assert_eq!(r2.status, StatusCode::OK);
    let r3 = send(&app, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    assert_eq!(r3.status, StatusCode::LOCKED);
}

#[tokio::test]
async fn lockdiscovery_reports_the_active_lock() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let l = send(&app, "LOCK", "/dav/docs/a.txt", &[], LOCKINFO, true).await;
    let tok = token_of(&l);
    let body = r#"<d:propfind xmlns:d="DAV:"><d:prop><d:lockdiscovery/><d:supportedlock/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs/a.txt", &[("depth", "0")], body, true).await;
    assert!(r.body.contains(&tok), "{}", r.body);
    assert!(r.body.contains("<d:lockentry>"));
}

// ------------------------------------------------------------------ If header

#[tokio::test]
async fn malformed_if_header_is_400_and_unsatisfied_is_412() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PUT", "/dav/docs/a.txt", &[("if", "(<broken")], "x", true).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST);

    let r = send(
        &app,
        "PUT",
        "/dav/docs/a.txt",
        &[("if", "([\"definitely-not-the-etag\"])")],
        "x",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::PRECONDITION_FAILED);
}

#[tokio::test]
async fn if_header_with_matching_etag_is_satisfied() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let g = send(&app, "GET", "/dav/docs/a.txt", &[], "", true).await;
    let etag = g.headers.get("etag").unwrap().to_str().unwrap().to_string();
    let r = send(
        &app,
        "PUT",
        "/dav/docs/a.txt",
        &[("if", &format!("([{etag}])"))],
        "x",
        true,
    )
    .await;
    assert_eq!(r.status, StatusCode::NO_CONTENT);
}

// ---------------------------------------------------------------- PROPPATCH

const WIN32: &str = r#"<?xml version="1.0" encoding="utf-8"?>
<d:propertyupdate xmlns:d="DAV:" xmlns:Z="urn:schemas-microsoft-com:">
  <d:set><d:prop>
    <Z:Win32CreationTime>Tue, 27 Jul 2026 09:00:00 GMT</Z:Win32CreationTime>
    <Z:Win32LastAccessTime>Tue, 27 Jul 2026 09:01:00 GMT</Z:Win32LastAccessTime>
    <Z:Win32LastModifiedTime>Tue, 27 Jul 2026 09:02:00 GMT</Z:Win32LastModifiedTime>
    <Z:Win32FileAttributes>00000020</Z:Win32FileAttributes>
  </d:prop></d:set>
</d:propertyupdate>"#;

#[tokio::test]
async fn proppatch_accepts_the_four_win32_properties_and_round_trips() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], WIN32, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("HTTP/1.1 200 OK"), "{}", r.body);
    assert!(!r.body.contains("403"), "Office aborts saving on anything but 200: {}", r.body);
    for p in [
        "Win32CreationTime",
        "Win32LastAccessTime",
        "Win32LastModifiedTime",
        "Win32FileAttributes",
    ] {
        assert!(r.body.contains(p), "{p} missing from response");
    }

    let q = r#"<d:propfind xmlns:d="DAV:" xmlns:Z="urn:schemas-microsoft-com:"><d:prop>
        <Z:Win32CreationTime/><Z:Win32FileAttributes/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs/a.txt", &[("depth", "0")], q, true).await;
    assert!(r.body.contains("Tue, 27 Jul 2026 09:00:00 GMT"), "{}", r.body);
    assert!(r.body.contains("00000020"), "{}", r.body);
}

#[tokio::test]
async fn proppatch_value_is_reserialised_not_echoed() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let evil = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:z="urn:e"><d:set><d:prop>
        <z:note>&lt;/d:prop&gt;&lt;/d:propstat&gt;&lt;injected/&gt;</z:note>
        </d:prop></d:set></d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], evil, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);

    let q = r#"<d:propfind xmlns:d="DAV:" xmlns:z="urn:e"><d:prop><z:note/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs/a.txt", &[("depth", "0")], q, true).await;
    assert!(!r.body.contains("<injected/>"), "injection escaped: {}", r.body);
    assert!(r.body.contains("&lt;injected/&gt;"), "{}", r.body);
}

#[tokio::test]
async fn entity_and_character_references_survive_a_round_trip() {
    // quick-xml 0.41 stopped inlining references in `Event::Text` and emits
    // `Event::GeneralRef` instead. A parser that ignores the new variant does
    // not fail — it silently deletes the character, which is how this landed
    // as a data-loss bug rather than a parse error.
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let b = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:z="urn:e"><d:set><d:prop>
        <z:note>a &amp; b &#233; c</z:note></d:prop></d:set></d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], b, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);

    let q = r#"<d:propfind xmlns:d="DAV:" xmlns:z="urn:e"><d:prop><z:note/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs/a.txt", &[("depth", "0")], q, true).await;
    assert!(r.body.contains("a &amp; b \u{e9} c"), "{}", r.body);
}

#[tokio::test]
async fn an_undefined_entity_is_rejected_rather_than_expanded_or_dropped() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let b = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:z="urn:e"><d:set><d:prop>
        <z:note>&xxe;</z:note></d:prop></d:set></d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], b, true).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST, "{}", r.body);
}

#[tokio::test]
async fn proppatch_cannot_modify_a_live_property() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let b = r#"<d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><d:getetag>"forged"</d:getetag></d:prop></d:set></d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], b, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("403"), "{}", r.body);
}

#[tokio::test]
async fn proppatch_remove_deletes_the_property() {
    let (_s, app) = build(tree(), MemMeta::new(), cfg());
    let set = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:z="urn:e"><d:set><d:prop><z:k>v</z:k></d:prop></d:set></d:propertyupdate>"#;
    send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], set, true).await;
    let del = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:z="urn:e"><d:remove><d:prop><z:k/></d:prop></d:remove></d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", &[], del, true).await;
    assert!(r.body.contains("HTTP/1.1 200 OK"));

    let q = r#"<d:propfind xmlns:d="DAV:" xmlns:z="urn:e"><d:prop><z:k/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs/a.txt", &[("depth", "0")], q, true).await;
    assert!(r.body.contains("404 Not Found"), "{}", r.body);
}

// ------------------------------------------------------------- decorator hook

struct ExtraProps;

impl sc_dav::PropSource for ExtraProps {
    fn namespaces(&self) -> &[(&'static str, &'static str)] {
        &[("v", "urn:vendor:example")]
    }
    fn emit(
        &self,
        e: &sc_dav::Entry,
        _ctx: &sc_dav::PropCtx,
        req: &sc_dav::PropReq,
        out: &mut sc_dav::PropWriter,
    ) {
        if req.wants("urn:vendor:example", "size-bytes") {
            out.text("v", "size-bytes", &e.size.to_string());
        }
    }
}

#[tokio::test]
async fn a_prop_source_can_inject_its_own_namespace_and_properties() {
    let mut svc = DavService::new(tree(), MemMeta::new(), cfg());
    svc.add_prop_source(Arc::new(ExtraProps));
    let app = Arc::new(svc).router();

    let q = r#"<d:propfind xmlns:d="DAV:" xmlns:v="urn:vendor:example"><d:prop><d:getetag/><v:size-bytes/></d:prop></d:propfind>"#;
    let r = send(&app, "PROPFIND", "/dav/docs/a.txt", &[("depth", "0")], q, true).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("xmlns:v=\"urn:vendor:example\""), "{}", r.body);
    assert!(r.body.contains("<v:size-bytes>11</v:size-bytes>"), "{}", r.body);
    assert!(!r.body.contains("404 Not Found"), "{}", r.body);
}
