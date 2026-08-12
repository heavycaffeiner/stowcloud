//! `sc-dav` — RFC 4918 **Class 2** WebDAV plus RFC 4331 quota.
//!
//! This crate is the *protocol-pure* implementation. It knows about `DAV:` and
//! nothing else. Vendor extensions are added from the outside by registering a
//! [`PropSource`] with [`DavService::add_prop_source`]; no vendor namespace,
//! endpoint, or property name appears anywhere in this crate, and CI enforces
//! that with a grep.
//!
//! Storage is reached only through the [`backend::CoreApi`] / [`backend::MetaApi`]
//! traits, so the protocol layer is testable without a filesystem.
//!
//! ## What the locks are, honestly
//!
//! A WebDAV LOCK here is a logical lock in our own database. Another process
//! writing the same directory — rsync, a media server, a shell — does not see
//! it and is not stopped by it. Real concurrency safety comes from `If-Match`
//! optimistic concurrency. Class 2 exists because macOS Finder mounts
//! read-only without it and MS Office refuses to save.

pub mod backend;
mod copymove;
mod error;
mod ifheader;
mod lockmethods;
pub mod locks;
mod methods;
mod propfind;
mod proppatch;
mod props;
mod search;
pub mod xml;

use std::sync::Arc;

use axum::body::Bytes;
use axum::extract::{Request, State};
use axum::response::Response;
use axum::routing::any;
use axum::Router;
use sc_vfs::UserId;
use http::{header, HeaderMap, Method, StatusCode};

pub use backend::{
    Aggregate, Core, CoreApi, CoreError, DavProp, Entry, Listing, MetaApi, MetaStore, Order, Perms,
    Quota, Resolved, Sort,
};
pub use error::{DavError, DavResult};
pub use ifheader::{CondKind, Condition, IfHeader, IfList, ResourceState};
pub use locks::{DavLock, Depth, LockManager, LockStore, MemLockStore};
pub use props::{PropCtx, PropPatchSource, PropReq, PropSource, PropSourceRef, PropWriter};
pub use search::{ReportSource, SearchOp, SearchRequest, SearchSource, SearchTerm};
pub use xml::{LockScope, PropName};

/// Identity of the caller, injected by whatever authentication middleware sits
/// in front of the DAV router. Absent means unauthenticated.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DavPrincipal(pub UserId);

#[derive(Clone, Debug)]
pub struct DavConfig {
    /// URL prefix the DAV tree is mounted at, e.g. `/dav`. No trailing slash.
    pub prefix: String,
    /// `Depth: infinity` PROPFIND. Off by default: on a million-file tree it is
    /// a single-request denial of service.
    pub allow_infinite_depth: bool,
    pub infinite_depth_max_entries: u32,
    /// Cap on a request body this server has to *parse*: PROPFIND, PROPPATCH,
    /// LOCK, SEARCH. Sized for XML, which is what all of those are.
    pub max_request_body: usize,
    /// Cap on a `PUT` body, which is a file and not something to parse.
    ///
    /// Separate from `max_request_body` because it was not, and one limit for
    /// both meant an XML-sized number decided how large a file could be
    /// uploaded: a plain `PUT` of 3.8 MB answered `413`. Nextcloud clients hid
    /// most of it — they switch to the chunked flow above ~10 MB, and each
    /// chunk lands just under a megabyte — so what failed was the middle band,
    /// which is where ordinary photos and documents live.
    ///
    /// This is also a memory bound: `PUT` is an atomic replace and the body is
    /// buffered before the write, so a caller that sends a file this large
    /// costs that much resident memory for the length of the request. That is
    /// the reason it is a number at all rather than "unlimited", and the
    /// reason a client with something bigger should be using the chunked
    /// upload path, which streams.
    pub max_put_body: usize,
    /// Below this many entries, buffer the multistatus and send a
    /// `Content-Length` — Windows clients dislike a chunked 207.
    pub buffer_propfind_under: usize,
    pub sync_copy_limit: u64,
    pub lock_default_timeout_s: u32,
    pub lock_max_timeout_s: u32,
    /// `WWW-Authenticate` realm.
    pub realm: String,
}

impl Default for DavConfig {
    fn default() -> Self {
        DavConfig {
            prefix: "/dav".to_string(),
            allow_infinite_depth: false,
            infinite_depth_max_entries: 50_000,
            max_request_body: 1024 * 1024,
            max_put_body: 256 * 1024 * 1024,
            buffer_propfind_under: 1000,
            sync_copy_limit: 2 * 1024 * 1024 * 1024,
            lock_default_timeout_s: 300,
            lock_max_timeout_s: 3600,
            realm: "WebDAV".to_string(),
        }
    }
}

pub struct DavService {
    pub(crate) core: Arc<Core>,
    pub(crate) meta: Arc<MetaStore>,
    pub(crate) locks: Arc<LockManager>,
    pub(crate) cfg: DavConfig,
    pub(crate) sources: Vec<Arc<dyn PropSource>>,
    pub(crate) patch_sources: Vec<Arc<dyn PropPatchSource>>,
    pub(crate) search: Option<Arc<dyn SearchSource>>,
    pub(crate) reports: Vec<Arc<dyn ReportSource>>,
    /// `xmlns:` declarations for the multistatus root, core plus every source.
    pub(crate) ns_decls: String,
    /// The `Allow` header, composed at construction from the base method set
    /// plus one entry per registered source.
    ///
    /// Not a constant, because a client decides whether to *send* `SEARCH` by
    /// reading this: the Android search box issues an `OPTIONS` first and gives
    /// up silently if `Allow` does not name the method. Advertising a method
    /// whose handler was compiled out is the same defect class as advertising a
    /// capability we do not have, and a constant string here would be invisible
    /// to the stripped-build CI gate, which only greps the compat crate.
    pub(crate) allow: String,
}

impl DavService {
    pub fn new(core: Arc<Core>, meta: Arc<MetaStore>, cfg: DavConfig) -> Self {
        Self::with_lock_store(core, meta, cfg, Arc::new(MemLockStore::new()))
    }

    /// Same, but with an explicit durable lock store (SQLite in production).
    pub fn with_lock_store(
        core: Arc<Core>,
        meta: Arc<MetaStore>,
        cfg: DavConfig,
        store: Arc<dyn LockStore>,
    ) -> Self {
        let locks = Arc::new(LockManager::new(
            store,
            cfg.lock_default_timeout_s,
            cfg.lock_max_timeout_s,
        ));
        DavService {
            core,
            meta,
            locks,
            cfg,
            sources: Vec::new(),
            patch_sources: Vec::new(),
            search: None,
            reports: Vec::new(),
            ns_decls: " xmlns:d=\"DAV:\"".to_string(),
            allow: BASE_ALLOW.to_string(),
        }
    }

    /// Decorator hook. Registering a source declares its namespaces on every
    /// multistatus we emit and calls it once per `<d:response>`.
    pub fn add_prop_source(&mut self, src: Arc<dyn PropSource>) {
        for (prefix, uri) in src.namespaces() {
            if *prefix == "d" || *uri == xml::NS_DAV {
                tracing::warn!("prop source tried to redeclare the DAV: namespace; ignored");
                continue;
            }
            let decl = format!(" xmlns:{prefix}=\"{}\"", xml::escape(uri));
            if !self.ns_decls.contains(&decl) {
                self.ns_decls.push_str(&decl);
            }
        }
        self.sources.push(src);
    }

    /// The write half of the decorator hook. A source that claims a property
    /// owns both its persistence and its protection from the dead-property
    /// store.
    pub fn add_prop_patch_source(&mut self, src: Arc<dyn PropPatchSource>) {
        self.patch_sources.push(src);
    }

    /// Register the search backend. Doing so is also what puts `SEARCH` in the
    /// `Allow` header; a build without one advertises nothing and answers 405.
    pub fn set_search_source(&mut self, src: Arc<dyn SearchSource>) {
        self.search = Some(src);
        self.recompose_allow();
    }

    /// Claim one report root element. `REPORT` reaches `Allow` only once both a
    /// report source and a search source exist, because a report is answered
    /// by translating it into a search.
    pub fn add_report_source(&mut self, src: Arc<dyn ReportSource>) {
        self.reports.push(src);
        self.recompose_allow();
    }

    fn recompose_allow(&mut self) {
        let mut allow = String::from(BASE_ALLOW);
        if self.search.is_some() {
            allow.push_str(", SEARCH");
            if !self.reports.is_empty() {
                allow.push_str(", REPORT");
            }
        }
        self.allow = allow;
    }

    pub fn locks(&self) -> &Arc<LockManager> {
        &self.locks
    }

    pub fn config(&self) -> &DavConfig {
        &self.cfg
    }

    /// 60 s expiry sweep, bounding how long a stale lock can block writers.
    pub fn spawn_lock_sweeper(self: &Arc<Self>) -> tokio::task::JoinHandle<()> {
        let locks = self.locks.clone();
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(std::time::Duration::from_secs(60));
            loop {
                tick.tick().await;
                let n = locks.sweep();
                if n > 0 {
                    tracing::debug!("swept {n} expired DAV locks");
                }
            }
        })
    }

    /// The same service, answering under a different URL prefix.
    ///
    /// Everything expensive or stateful is shared: the backend, the metadata
    /// store, the *same* [`LockManager`] (so a lock taken through one mount
    /// is honoured through the other — two independent lock tables over one
    /// filesystem would be a correctness bug, not a nuisance), and the
    /// registered property sources. Only `cfg.prefix` differs, which is what
    /// `vpath_of` strips and `href_of` re-emits, so the `<d:href>` values a
    /// client gets back always match the URL space it asked through.
    ///
    /// A mount prefix is a deployment concern, not a vendor one; this crate
    /// stays protocol-pure whatever the caller mounts it as.
    pub fn with_prefix(self: &Arc<Self>, prefix: impl Into<String>) -> Arc<Self> {
        Arc::new(DavService {
            core: self.core.clone(),
            meta: self.meta.clone(),
            locks: self.locks.clone(),
            cfg: DavConfig {
                prefix: prefix.into(),
                ..self.cfg.clone()
            },
            sources: self.sources.clone(),
            patch_sources: self.patch_sources.clone(),
            search: self.search.clone(),
            reports: self.reports.clone(),
            ns_decls: self.ns_decls.clone(),
            allow: self.allow.clone(),
        })
    }

    /// Handle one request directly, bypassing this crate's own `Router`.
    ///
    /// Exposed for callers that route by something the axum matcher cannot
    /// express — an alternate URL layout whose prefix is only known once the
    /// path has been parsed, for instance — and therefore need to pick the
    /// service (see [`DavService::with_prefix`]) before dispatching.
    pub async fn handle(self: Arc<Self>, req: Request) -> Response {
        dispatch(State(self), req).await
    }

    pub fn router(self: Arc<Self>) -> Router {
        let p = self.cfg.prefix.trim_end_matches('/').to_string();
        let mut r = Router::new();
        if p.is_empty() {
            r = r.route("/", any(dispatch)).route("/{*path}", any(dispatch));
        } else {
            r = r
                .route(&p, any(dispatch))
                .route(&format!("{p}/"), any(dispatch))
                .route(&format!("{p}/{{*path}}"), any(dispatch));
        }
        r.with_state(self)
    }

    // ---------------------------------------------------------------- paths

    /// URL path -> virtual path. Percent-decoded, no leading or trailing slash.
    pub(crate) fn vpath_of(&self, uri_path: &str) -> DavResult<String> {
        let p = self.cfg.prefix.trim_end_matches('/');
        let rest = if p.is_empty() {
            uri_path
        } else if let Some(r) = uri_path.strip_prefix(p) {
            r
        } else {
            // Router was nested, so the prefix has already been stripped.
            uri_path
        };
        let rest = rest.trim_start_matches('/').trim_end_matches('/');
        let decoded = percent_encoding::percent_decode_str(rest)
            .decode_utf8()
            .map_err(|_| DavError::BadRequest("path is not valid UTF-8".into()))?;
        if decoded.contains('\0') {
            return Err(DavError::BadRequest("NUL in path".into()));
        }
        // `.`/`..` are rejected, never resolved — resolving them just moves the
        // escape vector into whatever forgot to call this.
        for seg in decoded.split('/') {
            if seg == "." || seg == ".." {
                return Err(DavError::BadRequest("'.'/'..' are not accepted".into()));
            }
        }
        Ok(decoded.into_owned())
    }

    /// A client-supplied href reduced the same way [`Self::vpath_of`] reduces
    /// a request URI, but without refusing anything: a search scope that turns
    /// out to be nonsense is the source's to reject, with a message about
    /// scope, not this function's to reject as a malformed request URI.
    pub(crate) fn strip_prefix(&self, href: &str) -> String {
        // An absolute URL: keep the path and drop scheme and authority.
        let path = match href.split_once("://") {
            Some((_, rest)) => match rest.split_once('/') {
                Some((_, p)) => format!("/{p}"),
                None => "/".to_string(),
            },
            None => href.to_string(),
        };
        let p = self.cfg.prefix.trim_end_matches('/');
        let rest = if p.is_empty() {
            path.as_str()
        } else {
            path.strip_prefix(p).unwrap_or(path.as_str())
        };
        percent_encoding::percent_decode_str(rest.trim_matches('/'))
            .decode_utf8_lossy()
            .into_owned()
    }

    /// Virtual path -> `<d:href>` value.
    pub(crate) fn href_of(&self, vpath: &str, is_dir: bool) -> String {
        const SET: &percent_encoding::AsciiSet = &percent_encoding::CONTROLS
            .add(b' ')
            .add(b'"')
            .add(b'#')
            .add(b'<')
            .add(b'>')
            .add(b'?')
            .add(b'`')
            .add(b'{')
            .add(b'}')
            .add(b'%')
            .add(b'[')
            .add(b']')
            .add(b'^')
            .add(b'|')
            .add(b'\\');
        let p = self.cfg.prefix.trim_end_matches('/');
        let mut s = String::from(p);
        for seg in vpath.split('/').filter(|s| !s.is_empty()) {
            s.push('/');
            s.extend(percent_encoding::utf8_percent_encode(seg, SET));
        }
        if s.is_empty() || (is_dir && !s.ends_with('/')) {
            s.push('/');
        }
        s
    }
}

/// The methods every build serves. [`DavService::allow`] is this plus whatever
/// a registered source adds.
pub(crate) const BASE_ALLOW: &str =
    "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, COPY, MOVE, LOCK, UNLOCK";

pub(crate) fn parse_depth(h: &HeaderMap, default: Depth) -> DavResult<Depth> {
    match h.get("depth") {
        None => Ok(default),
        Some(v) => {
            let s = v
                .to_str()
                .map_err(|_| DavError::BadRequest("bad Depth header".into()))?
                .trim();
            if s == "0" {
                Ok(Depth::Zero)
            } else if s == "1" {
                Ok(Depth::One)
            } else if s.eq_ignore_ascii_case("infinity") {
                Ok(Depth::Infinity)
            } else {
                Err(DavError::BadRequest("bad Depth header".into()))
            }
        }
    }
}

async fn dispatch(State(svc): State<Arc<DavService>>, req: Request) -> Response {
    let method = req.method().clone();
    let uri_path = req.uri().path().to_string();
    let headers = req.headers().clone();

    // Windows Explorer probes with an unauthenticated OPTIONS before it will
    // even offer credentials; answering it is the difference between mounting
    // and not mounting at all.
    if method == Method::OPTIONS {
        return methods::options(&svc.allow);
    }

    let principal = req.extensions().get::<DavPrincipal>().copied();
    let Some(DavPrincipal(user)) = principal else {
        return DavError::Unauthorized.into_response_with_realm(&svc.cfg.realm);
    };

    // A `PUT` body is a file; every other body here is XML this server parses.
    // One limit for both is what made an XML budget decide the largest file a
    // client could upload.
    let max = if method == Method::PUT {
        svc.cfg.max_put_body
    } else {
        svc.cfg.max_request_body
    };
    let body = match axum::body::to_bytes(req.into_body(), max).await {
        Ok(b) => b,
        Err(_) => return DavError::TooLarge.into_response_now(),
    };

    let vpath = match svc.vpath_of(&uri_path) {
        Ok(v) => v,
        Err(e) => return e.into_response_now(),
    };

    match route(&svc, &method, user, &vpath, &headers, body).await {
        Ok(mut resp) => {
            error::security_headers(&mut resp);
            resp
        }
        Err(e) => e.into_response_now(),
    }
}

async fn route(
    svc: &Arc<DavService>,
    method: &Method,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    match method.as_str() {
        "GET" => methods::get(svc, user, vpath, headers, true).await,
        "HEAD" => methods::get(svc, user, vpath, headers, false).await,
        "PUT" => methods::put(svc, user, vpath, headers, body).await,
        "DELETE" => methods::delete(svc, user, vpath, headers).await,
        "MKCOL" => methods::mkcol(svc, user, vpath, headers, body).await,
        "PROPFIND" => propfind::handle(svc, user, vpath, headers, body).await,
        "PROPPATCH" => proppatch::handle(svc, user, vpath, headers, body).await,
        "COPY" => copymove::handle(svc, user, vpath, headers, false).await,
        "MOVE" => copymove::handle(svc, user, vpath, headers, true).await,
        "LOCK" => lockmethods::lock(svc, user, vpath, headers, body).await,
        "UNLOCK" => lockmethods::unlock(svc, user, vpath, headers).await,
        "SEARCH" => search::handle_search(svc, user, vpath, headers, body).await,
        "REPORT" => search::handle_report(svc, user, vpath, headers, body).await,
        _ => Err(DavError::MethodNotAllowed),
    }
}

impl DavError {
    fn into_response_now(self) -> Response {
        axum::response::IntoResponse::into_response(self)
    }
    fn into_response_with_realm(self, realm: &str) -> Response {
        let mut r = self.into_response_now();
        if r.status() == StatusCode::UNAUTHORIZED {
            if let Ok(v) = http::HeaderValue::from_str(&format!(
                "Basic realm=\"{}\", charset=\"UTF-8\"",
                realm.replace('"', "")
            )) {
                r.headers_mut().insert(header::WWW_AUTHENTICATE, v);
            }
        }
        r
    }
}

// ------------------------------------------------------------- shared helpers

/// Enforce the `If` header and return the lock tokens it submitted.
///
/// * unparseable => 400
/// * parsed but unsatisfied => 412
/// * (callers then ask the lock manager separately for 423)
pub(crate) fn eval_if_header(
    svc: &DavService,
    headers: &HeaderMap,
    share: sc_vfs::ShareId,
    vpath: &str,
    etag: Option<&str>,
    exists: bool,
) -> DavResult<Vec<String>> {
    let Some(raw) = headers.get("if") else {
        return Ok(Vec::new());
    };
    let raw = raw
        .to_str()
        .map_err(|_| DavError::BadRequest("bad If header".into()))?;
    let parsed = IfHeader::parse(raw)?;

    let ok = parsed.evaluate(|tag| {
        // A tagged resource is a URL; map it back to a virtual path so every
        // lookup goes through exactly one place.
        let target = match tag {
            None => vpath.to_string(),
            Some(t) => {
                let path = t
                    .split_once("://")
                    .and_then(|(_, rest)| rest.split_once('/').map(|(_, p)| format!("/{p}")))
                    .unwrap_or_else(|| t.to_string());
                svc.vpath_of(&path).unwrap_or_else(|_| t.to_string())
            }
        };
        let tokens = svc
            .locks
            .covering(share, &target)
            .iter()
            .map(|l| l.token_urn())
            .collect();
        if target == vpath {
            ResourceState {
                tokens,
                etag: etag.map(|s| s.to_string()),
                exists,
            }
        } else {
            ResourceState {
                tokens,
                etag: None,
                exists: true,
            }
        }
    });
    if !ok {
        return Err(DavError::PreconditionFailed);
    }
    Ok(parsed.tokens().into_iter().map(|s| s.to_string()).collect())
}

/// 423 when a lock covers `vpath` and none of the submitted tokens matches.
pub(crate) fn ensure_unlocked(
    svc: &DavService,
    share: sc_vfs::ShareId,
    vpath: &str,
    submitted: &[String],
    user: UserId,
) -> DavResult<()> {
    let refs: Vec<&str> = submitted.iter().map(|s| s.as_str()).collect();
    match svc.locks.check_write(share, vpath, &refs, user) {
        Some(_) => Err(DavError::Locked),
        None => Ok(()),
    }
}

pub(crate) fn xml_body(status: StatusCode, body: String) -> Response {
    let bytes = body.into_bytes();
    let len = bytes.len();
    let mut r = Response::new(axum::body::Body::from(bytes));
    *r.status_mut() = status;
    r.headers_mut().insert(
        header::CONTENT_TYPE,
        http::HeaderValue::from_static("application/xml; charset=utf-8"),
    );
    r.headers_mut().insert(
        header::CONTENT_LENGTH,
        http::HeaderValue::from_str(&len.to_string()).expect("ascii"),
    );
    r
}

pub(crate) fn empty(status: StatusCode) -> Response {
    let mut r = Response::new(axum::body::Body::empty());
    *r.status_mut() = status;
    r
}
