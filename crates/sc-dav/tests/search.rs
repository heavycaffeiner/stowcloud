//! The three seams a compatibility layer registers on the DAV service:
//! `SEARCH`, `REPORT`, and the write side of a live property.
//!
//! Everything runs through the real `axum::Router`; no socket is opened and no
//! vendor vocabulary appears, because none of it is this crate's.

mod support;

use std::sync::Arc;

use axum::Router;
use http::{HeaderMap, Request, StatusCode};
use http_body_util::BodyExt;
use sc_dav::backend::CoreApi;
use sc_dav::{DavConfig, DavPrincipal, DavService};
use support::{MemCore, MemMeta, USER};
use tower::ServiceExt;

fn tree() -> Arc<MemCore> {
    let c = MemCore::new();
    c.mkdir_raw("docs");
    c.file("docs/a.txt", b"hello world");
    c
}

struct Resp {
    status: StatusCode,
    headers: HeaderMap,
    body: String,
}

async fn send(app: &Router, method: &str, uri: &str, body: &str) -> Resp {
    let mut req = Request::builder()
        .method(method)
        .uri(uri)
        .header("host", "dav.example.com")
        .body(axum::body::Body::from(body.to_string()))
        .unwrap();
    req.extensions_mut().insert(DavPrincipal(USER));
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

/// A search backend that answers with whatever the fixture put in it, and
/// records the request it was handed so a test can assert on the parse.
struct RecordingSearch {
    seen: parking_lot::Mutex<Vec<sc_dav::SearchRequest>>,
    rows: Vec<(String, sc_dav::Entry)>,
}

impl RecordingSearch {
    fn new(rows: Vec<(String, sc_dav::Entry)>) -> Arc<Self> {
        Arc::new(RecordingSearch {
            seen: parking_lot::Mutex::new(Vec::new()),
            rows,
        })
    }
}

impl sc_dav::SearchSource for RecordingSearch {
    fn search(
        &self,
        _user: sc_vfs::UserId,
        req: &sc_dav::SearchRequest,
    ) -> Result<Vec<(String, sc_dav::Entry)>, sc_dav::DavError> {
        self.seen.lock().push(req.clone());
        Ok(self.rows.clone())
    }
}

fn blank_search(scope: &str) -> sc_dav::SearchRequest {
    sc_dav::SearchRequest {
        scope_href: scope.to_string(),
        depth_infinity: true,
        name_contains: None,
        content_type_prefixes: Vec::new(),
        mtime_from_ns: None,
        mtime_to_ns: None,
        is_collection: None,
        limit: 0,
        newest_first: false,
        vendor: Vec::new(),
        props: sc_dav::PropReq {
            all: true,
            names_only: false,
            requested: Vec::new(),
        },
    }
}

struct MarkedReport;
impl sc_dav::ReportSource for MarkedReport {
    fn report_name(&self) -> (&'static str, &'static str) {
        ("urn:vendor:example", "filter-files")
    }
    fn to_search(
        &self,
        _user: sc_vfs::UserId,
        vpath: &str,
        _body: &[u8],
    ) -> Result<sc_dav::SearchRequest, sc_dav::DavError> {
        Ok(blank_search(vpath))
    }
}

const SEARCH_BODY: &str = r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch>
      <d:from><d:scope><d:href>/dav/docs</d:href><d:depth>infinity</d:depth></d:scope></d:from>
      <d:where><d:like><d:prop><d:displayname/></d:prop><d:literal>%a%</d:literal></d:like></d:where>
    </d:basicsearch></d:searchrequest>"#;

/// The Android search box issues an `OPTIONS` first and gives up silently
/// unless `Allow` names `SEARCH`. A build with no search source must therefore
/// not name it: advertising a method whose handler is absent is the same
/// defect class as advertising a capability the server does not have.
#[tokio::test]
async fn allow_names_search_only_once_a_source_is_registered() {
    let plain = Arc::new(DavService::new(
        tree(),
        MemMeta::new(),
        DavConfig::default(),
    ))
    .router();
    let r = send(&plain, "OPTIONS", "/dav/", "").await;
    let allow = r.headers.get("allow").unwrap().to_str().unwrap().to_string();
    assert!(!allow.contains("SEARCH"), "{allow}");
    assert!(!allow.contains("REPORT"), "{allow}");
    // ...and the method really is refused, not merely unadvertised.
    let r = send(&plain, "SEARCH", "/dav/", SEARCH_BODY).await;
    assert_eq!(r.status, StatusCode::METHOD_NOT_ALLOWED);

    let mut svc = DavService::new(tree(), MemMeta::new(), DavConfig::default());
    svc.set_search_source(RecordingSearch::new(Vec::new()));
    svc.add_report_source(Arc::new(MarkedReport));
    let app = Arc::new(svc).router();
    let r = send(&app, "OPTIONS", "/dav/", "").await;
    let allow = r.headers.get("allow").unwrap().to_str().unwrap().to_string();
    assert!(allow.contains("SEARCH"), "{allow}");
    assert!(allow.contains("REPORT"), "{allow}");
}

#[tokio::test]
async fn a_search_answers_a_multistatus_through_the_registered_source() {
    let core = tree();
    let entry = core.stat_entry(USER, "docs/a.txt").unwrap();
    let source = RecordingSearch::new(vec![("docs/a.txt".to_string(), entry)]);
    let mut svc = DavService::new(core, MemMeta::new(), DavConfig::default());
    svc.set_search_source(source.clone());
    let app = Arc::new(svc).router();

    let r = send(&app, "SEARCH", "/dav/", SEARCH_BODY).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("/dav/docs/a.txt"), "{}", r.body);

    // The mount's own prefix is stripped off the scope before the source sees
    // it, so a source never has to know where the tree is mounted.
    let seen = source.seen.lock();
    assert_eq!(seen.len(), 1);
    assert_eq!(seen[0].scope_href, "docs");
    assert_eq!(seen[0].name_contains.as_deref(), Some("a"));
}

/// RFC 3253 §3.1.5: an unsupported report is 403 with a
/// `DAV:supported-report` precondition. Not 404, because the resource exists
/// and the report does not, and not 400.
#[tokio::test]
async fn an_unclaimed_report_is_403_with_the_supported_report_precondition() {
    let mut svc = DavService::new(tree(), MemMeta::new(), DavConfig::default());
    svc.set_search_source(RecordingSearch::new(Vec::new()));
    svc.add_report_source(Arc::new(MarkedReport));
    let app = Arc::new(svc).router();

    let body = r#"<d:sync-collection xmlns:d="DAV:"><d:sync-token/></d:sync-collection>"#;
    let r = send(&app, "REPORT", "/dav/docs", body).await;
    assert_eq!(r.status, StatusCode::FORBIDDEN);
    assert!(r.body.contains("supported-report"), "{}", r.body);
}

#[tokio::test]
async fn a_claimed_report_is_answered_through_the_same_search() {
    let core = tree();
    let entry = core.stat_entry(USER, "docs/a.txt").unwrap();
    let source = RecordingSearch::new(vec![("docs/a.txt".to_string(), entry)]);
    let mut svc = DavService::new(core, MemMeta::new(), DavConfig::default());
    svc.set_search_source(source.clone());
    svc.add_report_source(Arc::new(MarkedReport));
    let app = Arc::new(svc).router();

    let body = r#"<v:filter-files xmlns:d="DAV:" xmlns:v="urn:vendor:example">
          <d:prop><d:getetag/></d:prop>
          <d:filter-rules><v:marked>1</v:marked></d:filter-rules>
        </v:filter-files>"#;
    let r = send(&app, "REPORT", "/dav/docs", body).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("/dav/docs/a.txt"), "{}", r.body);
    assert_eq!(source.seen.lock().len(), 1, "the report ran one search");
}

const CLAIM: &[(&str, &str)] = &[("urn:vendor:example", "marked")];

struct Claiming {
    writes: parking_lot::Mutex<Vec<(sc_vfs::FileId, Option<String>)>>,
}

impl sc_dav::PropPatchSource for Claiming {
    fn claims(&self) -> &[(&'static str, &'static str)] {
        CLAIM
    }
    fn set(
        &self,
        _u: sc_vfs::UserId,
        _s: sc_vfs::ShareId,
        id: sc_vfs::FileId,
        _ns: &str,
        _name: &str,
        value: Option<&str>,
    ) -> Result<(), sc_dav::DavError> {
        self.writes.lock().push((id, value.map(str::to_string)));
        Ok(())
    }
}

/// A property a `PropPatchSource` claims never reaches the dead-property
/// store, and the source is handed a real file id.
///
/// Without this the write lands in the dead store, answers 200, and the next
/// PROPFIND reports the old value from wherever the read side gets it: the
/// client sees its change accepted and then reverted.
#[tokio::test]
async fn a_claimed_live_property_is_written_by_its_source_not_the_dead_store() {
    let source = Arc::new(Claiming {
        writes: parking_lot::Mutex::new(Vec::new()),
    });
    let core = tree();
    let meta = MemMeta::new();
    let mut svc = DavService::new(core.clone(), meta.clone(), DavConfig::default());
    svc.add_prop_patch_source(source.clone());
    let app = Arc::new(svc).router();

    let set = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:v="urn:vendor:example">
          <d:set><d:prop><v:marked>1</v:marked></d:prop></d:set>
        </d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", set).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert!(r.body.contains("200 OK"), "{}", r.body);
    {
        let w = source.writes.lock();
        assert_eq!(w.len(), 1);
        assert_eq!(w[0].1.as_deref(), Some("1"));
        assert!(w[0].0.0 > 0, "the source is handed a real file id");
    }

    // A `d:remove` reaches the same source as `None`, which is how one client
    // clears the flag.
    let remove = r#"<d:propertyupdate xmlns:d="DAV:" xmlns:v="urn:vendor:example">
          <d:remove><d:prop><v:marked/></d:prop></d:remove>
        </d:propertyupdate>"#;
    let r = send(&app, "PROPPATCH", "/dav/docs/a.txt", remove).await;
    assert_eq!(r.status, StatusCode::MULTI_STATUS);
    assert_eq!(source.writes.lock()[1].1, None);

    // And nothing was written to the dead-property store under that name.
    use sc_dav::MetaApi;
    let id = core.stat_entry(USER, "docs/a.txt").unwrap().id.unwrap();
    let props = meta.get_props(id).unwrap();
    assert!(
        !props.iter().any(|p| p.name == "marked"),
        "a claimed property must not also land in the dead store"
    );
}
