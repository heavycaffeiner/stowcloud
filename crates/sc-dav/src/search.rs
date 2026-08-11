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
    /// The comparison sat inside a `d:or`.
    ///
    /// A fact about the document, not about the property: this crate does not
    /// know what a vendor property means, so it cannot decide whether ignoring
    /// one is safe. The claiming source can, and needs this to make the call:
    /// dropping a disjunct narrows the answer, dropping a conjunct widens it.
    pub in_disjunction: bool,
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

/// A `d:getlastmodified` literal, in any of the three forms the shipped clients
/// and the ecosystem's own documentation use.
///
/// In order: a bare decimal integer of Unix **seconds**, an ISO 8601 / RFC 3339
/// datetime, an HTTP-date. Anything else makes the whole request a 400, because
/// a bound the server silently drops turns "modified since Tuesday" into
/// "everything".
pub(crate) fn parse_search_date_ns(s: &str) -> DavResult<i128> {
    let s = s.trim();
    if let Some(secs) = parse_decimal(s) {
        return Ok(i128::from(secs) * 1_000_000_000);
    }
    if let Some(ns) = parse_iso8601_ns(s) {
        return Ok(ns);
    }
    httpdate::parse_http_date(s)
        .map_err(|_| {
            DavError::BadRequest(
                "a date literal is not a Unix time, an ISO 8601 datetime or an HTTP-date".into(),
            )
        })
        .and_then(|t| {
            t.duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos() as i128)
                .map_err(|_| DavError::BadRequest("a date literal predates the epoch".into()))
        })
}

/// A whole decimal integer, optionally signed. Not a prefix of one: `2026-08`
/// has to fall through to the datetime reading rather than come back as 2026.
fn parse_decimal(s: &str) -> Option<i64> {
    let (sign, digits) = match s.strip_prefix('-') {
        Some(rest) => (-1i64, rest),
        None => (1i64, s),
    };
    if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    digits.parse::<i64>().ok().map(|n| sign * n)
}

/// Fixed-width unsigned field, all digits.
fn field(s: &str, from: usize, to: usize) -> Option<i64> {
    let part = s.get(from..to)?;
    if !part.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    part.parse().ok()
}

/// `YYYY-MM-DDTHH:MM:SS`, optional fractional seconds, optional `Z`, `+HH:MM`,
/// `-HH:MM`, `+HHMM` or `-HHMM`.
///
/// A missing offset is read as UTC. That is what the clients omitting one
/// intend, and it is also what Android's local-time-plus-`Z` string literally
/// says: guessing the device's offset would be inventing data.
fn parse_iso8601_ns(s: &str) -> Option<i128> {
    if s.len() < 19 {
        return None;
    }
    let b = s.as_bytes();
    if b[4] != b'-' || b[7] != b'-' || b[13] != b':' || b[16] != b':' {
        return None;
    }
    if !matches!(b[10], b'T' | b't' | b' ') {
        return None;
    }
    let year = field(s, 0, 4)?;
    let month = field(s, 5, 7)?;
    let day = field(s, 8, 10)?;
    let hour = field(s, 11, 13)?;
    let minute = field(s, 14, 16)?;
    let second = field(s, 17, 19)?;
    if !(1..=12).contains(&month) || day < 1 || day > days_in_month(year, month) {
        return None;
    }
    // 60 is a leap second, which no clock here has and every client may still
    // write. It carries into the next minute rather than being refused.
    if hour > 23 || minute > 59 || second > 60 {
        return None;
    }

    let mut rest = &s[19..];
    let mut nanos: i128 = 0;
    if let Some(frac) = rest.strip_prefix('.').or_else(|| rest.strip_prefix(',')) {
        let n = frac.bytes().take_while(u8::is_ascii_digit).count();
        if n == 0 {
            return None;
        }
        for (i, c) in frac[..n].bytes().take(9).enumerate() {
            nanos += i128::from(c - b'0') * 10i128.pow(8 - i as u32);
        }
        rest = &frac[n..];
    }

    let offset_secs = match rest {
        "" | "Z" | "z" => 0,
        _ => {
            let sign = match rest.as_bytes()[0] {
                b'+' => 1,
                b'-' => -1,
                _ => return None,
            };
            let t = &rest[1..];
            let (oh, om) = match t.len() {
                5 if t.as_bytes()[2] == b':' => (field(t, 0, 2)?, field(t, 3, 5)?),
                4 => (field(t, 0, 2)?, field(t, 2, 4)?),
                2 => (field(t, 0, 2)?, 0),
                _ => return None,
            };
            if oh > 23 || om > 59 {
                return None;
            }
            sign * (oh * 3600 + om * 60)
        }
    };

    let secs = days_from_civil(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second
        - offset_secs;
    Some(i128::from(secs) * 1_000_000_000 + nanos)
}

fn days_in_month(year: i64, month: i64) -> i64 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if (year % 4 == 0 && year % 100 != 0) || year % 400 == 0 => 29,
        2 => 28,
        _ => 0,
    }
}

/// Days between the epoch and a proleptic-Gregorian date, by the era method.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

#[cfg(test)]
mod date_tests {
    use super::parse_search_date_ns;

    const NS: i128 = 1_000_000_000;

    #[test]
    fn a_bare_integer_is_unix_seconds() {
        assert_eq!(parse_search_date_ns("1785321600").unwrap(), 1_785_321_600 * NS);
        assert_eq!(parse_search_date_ns(" 0 ").unwrap(), 0);
        assert_eq!(parse_search_date_ns("-1").unwrap(), -NS);
    }

    #[test]
    fn the_three_forms_agree_on_one_instant() {
        let iso = parse_search_date_ns("2026-08-04T13:22:05Z").unwrap();
        let http = parse_search_date_ns("Tue, 04 Aug 2026 13:22:05 GMT").unwrap();
        let unix = parse_search_date_ns("1785849725").unwrap();
        assert_eq!(iso, http);
        assert_eq!(iso, unix);
    }

    #[test]
    fn an_offset_moves_the_instant_and_a_missing_one_is_utc() {
        let utc = parse_search_date_ns("2026-08-11T00:00:00Z").unwrap();
        assert_eq!(
            parse_search_date_ns("2026-08-11T00:00:00+09:00").unwrap(),
            utc - 9 * 3600 * NS
        );
        assert_eq!(
            parse_search_date_ns("2026-08-11T00:00:00+0900").unwrap(),
            utc - 9 * 3600 * NS
        );
        assert_eq!(parse_search_date_ns("2026-08-11T00:00:00").unwrap(), utc);
    }

    #[test]
    fn fractional_seconds_are_kept() {
        let base = parse_search_date_ns("2026-08-11T00:00:00Z").unwrap();
        assert_eq!(
            parse_search_date_ns("2026-08-11T00:00:00.5Z").unwrap(),
            base + NS / 2
        );
        assert_eq!(
            parse_search_date_ns("2026-08-11T00:00:00.123456789Z").unwrap(),
            base + 123_456_789
        );
    }

    #[test]
    fn nonsense_is_still_a_refusal() {
        for s in ["", "yesterday", "2026-13-01T00:00:00Z", "2026-02-30T00:00:00Z", "2026-08-11"] {
            assert!(parse_search_date_ns(s).is_err(), "{s:?} must not parse");
        }
    }
}

