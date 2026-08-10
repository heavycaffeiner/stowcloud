//! RFC 5323 `SEARCH` and RFC 3253 `REPORT`.
//!
//! Both are DAV methods, so their request bodies are parsed here. Neither
//! handler knows how to *answer* one: a [`SearchSource`] does the walking and a
//! [`ReportSource`] translates a report body whose vocabulary this crate has
//! never heard of into the same neutral [`SearchRequest`]. Registering either
//! is also what puts the method in the `Allow` header, so a build with no
//! source advertises nothing it cannot serve.
//!
//! The one thing this module deliberately does not do is interpret a
//! comparison against a property outside `DAV:`. Those are collected verbatim
//! into [`SearchRequest::vendor`] and handed on. A client's favourites query is
//! a comparison on a vendor property, and teaching this crate to recognise it
//! would put that vocabulary in the protocol layer.

use std::sync::Arc;

use axum::body::Bytes;
use axum::response::Response;
use http::{HeaderMap, StatusCode};
use sc_vfs::UserId;

use crate::backend::Entry;
use crate::error::{DavError, DavResult};
use crate::propfind::{prefix_map, write_response};
use crate::props::PropReq;
use crate::xml::{parse_report_root, parse_searchrequest};
use crate::DavService;

/// A comparison the client made against a property this crate does not own.
///
/// `ns` is a namespace URI, never a prefix. The literal is the client's text,
/// unmodified: interpreting `1` versus `yes` is the claiming source's business.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SearchTerm {
    pub ns: String,
    pub name: String,
    pub op: SearchOp,
    pub literal: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SearchOp {
    Eq,
    Like,
    Lt,
    Gt,
}

/// A parsed RFC 5323 basic search, stripped of protocol shape.
///
/// Every field is a filter; a `None` or empty field constrains nothing.
/// `sc-dav` never interprets these, it only fills them in and hands them to the
/// registered source.
#[derive(Clone, Debug)]
pub struct SearchRequest {
    /// `d:from/d:scope/d:href`, with this mount's own prefix stripped if it
    /// carried one, and otherwise exactly as the client wrote it.
    ///
    /// **This is a security boundary, not a hint.** It is a client-supplied
    /// string, and only the source knows how its URL space maps onto accounts,
    /// so the source resolves it and refuses anything that is not the caller's
    /// own tree. A scope naming another account is never narrowed, corrected or
    /// silently reinterpreted: a silent reinterpretation is how a search
    /// endpoint becomes a cross-account read.
    pub scope_href: String,
    /// `d:from/d:scope/d:depth`.
    ///
    /// Parsed and reported, not enforced: both clients send `infinity` and no
    /// source bounds the walk by it. A `0` or `1` here is answered as though it
    /// were `infinity`, which returns more than was asked for rather than less
    /// — recorded here because a caller reading this field could reasonably
    /// assume otherwise.
    pub depth_infinity: bool,
    /// Substring of the entry name, case-insensitive, from `d:like` on
    /// `d:displayname` with the surrounding `%` stripped.
    pub name_contains: Option<String>,
    /// Media-type prefixes from `d:like` on `d:getcontenttype`, e.g.
    /// `["image/", "video/"]`. Matched however the source chooses; this crate
    /// promises no content sniffing on anyone's behalf.
    pub content_type_prefixes: Vec<String>,
    /// Inclusive mtime bounds in nanoseconds since the epoch.
    pub mtime_from_ns: Option<i128>,
    pub mtime_to_ns: Option<i128>,
    /// `Some(true)` restricts to collections, `Some(false)` to non-collections.
    pub is_collection: Option<bool>,
    /// `d:limit/d:nresults`. Zero means the client asked for no cap; the source
    /// still applies its own.
    pub limit: u32,
    /// `d:orderby` on `d:getlastmodified` `d:descending`, the only ordering
    /// either client asks for. A request, not a guarantee.
    pub newest_first: bool,
    /// Comparisons on properties outside `DAV:`, verbatim.
    pub vendor: Vec<SearchTerm>,
    /// `d:select/d:prop`, which is what the 207 will carry per row.
    pub props: PropReq,
}

/// Answers an RFC 5323 SEARCH.
pub trait SearchSource: Send + Sync {
    /// Entries matching `req`, already ACL-filtered for `user`, each paired
    /// with its vpath so the caller can render an href. Ordering is the
    /// source's to decide; `req.newest_first` is a request, not a guarantee.
    fn search(&self, user: UserId, req: &SearchRequest) -> DavResult<Vec<(String, Entry)>>;
}

/// Translates one REPORT, selected by the root element name of the request
/// body, into a search.
///
/// `sc-dav` reads that name and nothing else, so a report whose body is a
/// vocabulary this crate has never seen can be served without it learning one.
/// Returning a [`SearchRequest`] rather than a whole response is what makes a
/// report and the equivalent `SEARCH` one implementation with two entry points.
pub trait ReportSource: Send + Sync {
    /// `(namespace, local-name)` of the report root this source claims.
    fn report_name(&self) -> (&'static str, &'static str);

    fn to_search(&self, user: UserId, vpath: &str, body: &[u8]) -> DavResult<SearchRequest>;
}

pub(crate) async fn handle_search(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    _headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    let Some(source) = svc.search.as_ref() else {
        // Nothing registered, so this build genuinely does not answer SEARCH,
        // and `Allow` does not name it either.
        return Err(DavError::MethodNotAllowed);
    };
    let mut req = parse_searchrequest(&body, svc.cfg.max_request_body)?;
    // A search addressed at a collection with no `d:from` of its own is scoped
    // to that collection, which is what RFC 5323 means by the request-URI.
    if req.scope_href.is_empty() {
        req.scope_href = vpath.to_string();
    } else {
        req.scope_href = svc.strip_prefix(&req.scope_href);
    }
    let rows = source.search(user, &req)?;
    Ok(render_multistatus(svc, user, &req, rows))
}

pub(crate) async fn handle_report(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    _headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    if svc.reports.is_empty() || svc.search.is_none() {
        return Err(DavError::MethodNotAllowed);
    }
    let root = parse_report_root(&body, svc.cfg.max_request_body)?;
    let Some(source) = svc
        .reports
        .iter()
        .find(|s| { let (ns, n) = s.report_name(); ns == root.ns && n == root.name })
    else {
        // RFC 3253 §3.1.5: an unsupported report is 403 with a
        // `DAV:supported-report` precondition, not 404 and not 400.
        return Err(DavError::UnsupportedReport);
    };
    let req = source.to_search(user, vpath, &body)?;
    let rows = svc
        .search
        .as_ref()
        .expect("checked above")
        .search(user, &req)?;
    Ok(render_multistatus(svc, user, &req, rows))
}

/// The same per-row property machinery PROPFIND uses, so a search result and a
/// PROPFIND of the same node are byte-identical for the properties both carry.
fn render_multistatus(
    svc: &Arc<DavService>,
    user: UserId,
    req: &SearchRequest,
    rows: Vec<(String, Entry)>,
) -> Response {
    let prefix_ns = prefix_map(svc);
    let mut out = String::with_capacity(1024 + rows.len() * 512);
    out.push_str(&format!(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:multistatus{}>\n",
        svc.ns_decls
    ));
    for (vpath, e) in &rows {
        // The share is re-derived per row: a search spans every root the
        // caller holds, so unlike PROPFIND there is no single share for the
        // whole response.
        let share = svc
            .core
            .resolve(user, vpath)
            .map(|r| r.share)
            .unwrap_or(sc_vfs::ShareId::new(0));
        write_response(
            svc,
            user,
            share,
            vpath,
            e,
            &req.props,
            None,
            &prefix_ns,
            &mut out,
        );
    }
    out.push_str("</d:multistatus>\n");
    crate::xml_body(StatusCode::MULTI_STATUS, out)
}

/// Turn a `d:like` literal into the substring it is asking for.
///
/// Clients write `%term%` for "contains" and `image/%` for "starts with". The
/// `%` are stripped and the remainder is matched literally: there is no
/// wildcard engine behind this, and pretending otherwise would let `%a%b%`
/// silently match things it should not.
pub(crate) fn like_needle(literal: &str) -> (String, bool) {
    let leading = literal.starts_with('%');
    (literal.trim_matches('%').to_string(), leading)
}

/// `d:getlastmodified` literals are RFC 1123 dates; both clients send them that
/// way. Anything unparseable makes the whole request a 400, because a bound the
/// server silently drops turns "modified since Tuesday" into "everything".
pub(crate) fn parse_http_date_ns(s: &str) -> DavResult<i128> {
    httpdate::parse_http_date(s.trim())
        .map_err(|_| DavError::BadRequest("a date literal is not an HTTP-date".into()))
        .and_then(|t| {
            t.duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos() as i128)
                .map_err(|_| DavError::BadRequest("a date literal predates the epoch".into()))
        })
}

