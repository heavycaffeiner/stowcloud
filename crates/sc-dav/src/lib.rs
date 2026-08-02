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
pub use props::{PropCtx, PropReq, PropSource, PropSourceRef, PropWriter};
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
    pub max_request_body: usize,
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
    /// `xmlns:` declarations for the multistatus root, core plus every source.
    pub(crate) ns_decls: String,
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
            ns_decls: " xmlns:d=\"DAV:\"".to_string(),
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

    pub fn locks(&self) -> &Arc<LockManager> {
        &self.locks
    }

    pub fn config(&self) -> &DavConfig {
        &self.cfg
    }

    /// 60 s expiry sweep (`DESIGN-WEBDAV.md` §5.1).
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
            ns_decls: self.ns_decls.clone(),
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

pub(crate) const ALLOW: &str =
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
        return methods::options();
    }

    let principal = req.extensions().get::<DavPrincipal>().copied();
    let Some(DavPrincipal(user)) = principal else {
        return DavError::Unauthorized.into_response_with_realm(&svc.cfg.realm);
    };

    let max = svc.cfg.max_request_body;
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
