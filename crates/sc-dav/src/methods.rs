//! OPTIONS / GET / HEAD / PUT / DELETE / MKCOL.

use std::sync::Arc;

use axum::body::Bytes;
use axum::response::Response;
use sc_vfs::{Kind, UserId};
use http::{header, HeaderMap, HeaderValue, StatusCode};

use crate::backend::{CoreError, Perms};
use crate::error::{security_headers, DavError, DavResult};
use crate::props::{guess_content_type, rfc1123};
use crate::{ensure_unlocked, eval_if_header, DavService};

/// OPTIONS. Must be answerable **unauthenticated**: Windows Explorer probes
/// before it will consider offering credentials, and a 401 here means the
/// mount never happens.
pub(crate) fn options(allow: &str) -> Response {
    let mut r = crate::empty(StatusCode::OK);
    let h = r.headers_mut();
    h.insert("dav", HeaderValue::from_static("1, 2, 3"));
    h.insert("ms-author-via", HeaderValue::from_static("DAV"));
    if let Ok(v) = HeaderValue::from_str(allow) {
        h.insert(header::ALLOW, v);
    }
    h.insert(header::ACCEPT_RANGES, HeaderValue::from_static("bytes"));
    h.insert(header::CONTENT_LENGTH, HeaderValue::from_static("0"));
    security_headers(&mut r);
    r
}

fn etag_matches(header_val: &str, etag: &str) -> bool {
    for part in header_val.split(',') {
        let p = part.trim();
        if p == "*" {
            return true;
        }
        let p = p.strip_prefix("W/").unwrap_or(p);
        let p = p.trim_matches('"');
        if p == etag {
            return true;
        }
    }
    false
}

pub(crate) async fn get(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    with_body: bool,
) -> DavResult<Response> {
    let entry = svc.core.stat_entry(user, vpath)?;
    if !entry.can_read() {
        return Err(DavError::NotFound);
    }
    if entry.kind == Kind::Dir {
        // A collection has no default representation. Directory browsing is the
        // web UI's job, not the DAV endpoint's.
        return Err(DavError::MethodNotAllowed);
    }
    if !entry.perms.contains(Perms::DOWNLOAD) {
        return Err(DavError::Forbidden);
    }

    let quoted = format!("\"{}\"", entry.etag);
    let last_mod = rfc1123(entry.mtime_ns);

    // Conditional GET. Most of a sync client's traffic disappears here.
    if let Some(inm) = headers.get(header::IF_NONE_MATCH).and_then(|v| v.to_str().ok()) {
        if etag_matches(inm, &entry.etag) {
            return Ok(not_modified(&quoted, &last_mod));
        }
    } else if let Some(ims) = headers
        .get(header::IF_MODIFIED_SINCE)
        .and_then(|v| v.to_str().ok())
    {
        if let Ok(since) = httpdate::parse_http_date(ims) {
            let mtime = std::time::UNIX_EPOCH
                + std::time::Duration::from_secs((entry.mtime_ns / 1_000_000_000).max(0) as u64);
            // Second granularity: only 304 when we are not strictly newer.
            if mtime <= since {
                return Ok(not_modified(&quoted, &last_mod));
            }
        }
    }
    if let Some(im) = headers.get(header::IF_MATCH).and_then(|v| v.to_str().ok()) {
        if !etag_matches(im, &entry.etag) {
            return Err(DavError::PreconditionFailed);
        }
    }

    let full = if with_body {
        svc.core.read_bytes(user, vpath)?
    } else {
        Vec::new()
    };
    let total = if with_body { full.len() as u64 } else { entry.size };

    let range = parse_range(headers, total);
    let (status, slice, content_range) = match range {
        // Multi-range requests get the whole entity with a 200. RFC 7233 allows
        // it and no real client depends on multipart/byteranges.
        RangeResult::None | RangeResult::Multiple => (StatusCode::OK, (0u64, total), None),
        RangeResult::Unsatisfiable => return Err(DavError::RangeNotSatisfiable),
        RangeResult::Single(a, b) => (
            StatusCode::PARTIAL_CONTENT,
            (a, b + 1),
            Some(format!("bytes {a}-{b}/{total}")),
        ),
    };

    let body = if with_body {
        let s = slice.0.min(full.len() as u64) as usize;
        let e = slice.1.min(full.len() as u64) as usize;
        full[s..e].to_vec()
    } else {
        Vec::new()
    };
    let len = slice.1.saturating_sub(slice.0);

    let mut r = Response::new(axum::body::Body::from(body));
    *r.status_mut() = status;
    let h = r.headers_mut();
    h.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_str(&guess_content_type(&entry.name))
            .unwrap_or(HeaderValue::from_static("application/octet-stream")),
    );
    h.insert(
        header::CONTENT_LENGTH,
        HeaderValue::from_str(&len.to_string()).expect("ascii"),
    );
    h.insert(header::ACCEPT_RANGES, HeaderValue::from_static("bytes"));
    if let Ok(v) = HeaderValue::from_str(&quoted) {
        h.insert(header::ETAG, v);
    }
    if let Ok(v) = HeaderValue::from_str(&last_mod) {
        h.insert(header::LAST_MODIFIED, v);
    }
    if let Some(cr) = content_range {
        if let Ok(v) = HeaderValue::from_str(&cr) {
            h.insert(header::CONTENT_RANGE, v);
        }
    }
    // Never let a stored file render in the browser's origin.
    h.insert(
        header::CONTENT_DISPOSITION,
        HeaderValue::from_str(&content_disposition(&entry.name))
            .unwrap_or(HeaderValue::from_static("attachment")),
    );
    Ok(r)
}

fn content_disposition(name: &str) -> String {
    let ascii: String = name
        .chars()
        .map(|c| {
            if c.is_ascii_graphic() && c != '"' && c != '\\' {
                c
            } else {
                '_'
            }
        })
        .collect();
    let enc: String = percent_encoding::utf8_percent_encode(name, percent_encoding::NON_ALPHANUMERIC)
        .to_string();
    format!("attachment; filename=\"{ascii}\"; filename*=UTF-8''{enc}")
}

fn not_modified(etag: &str, last_mod: &str) -> Response {
    let mut r = crate::empty(StatusCode::NOT_MODIFIED);
    let h = r.headers_mut();
    if let Ok(v) = HeaderValue::from_str(etag) {
        h.insert(header::ETAG, v);
    }
    if let Ok(v) = HeaderValue::from_str(last_mod) {
        h.insert(header::LAST_MODIFIED, v);
    }
    r
}

enum RangeResult {
    None,
    Single(u64, u64),
    Multiple,
    Unsatisfiable,
}

fn parse_range(headers: &HeaderMap, total: u64) -> RangeResult {
    let Some(v) = headers.get(header::RANGE).and_then(|v| v.to_str().ok()) else {
        return RangeResult::None;
    };
    let Some(spec) = v.trim().strip_prefix("bytes=") else {
        return RangeResult::None;
    };
    let parts: Vec<&str> = spec.split(',').map(|s| s.trim()).collect();
    if parts.len() > 1 {
        return RangeResult::Multiple;
    }
    let Some(p) = parts.first() else {
        return RangeResult::None;
    };
    let (a, b) = match p.split_once('-') {
        None => return RangeResult::Unsatisfiable,
        Some((s, e)) => (s.trim(), e.trim()),
    };
    if total == 0 {
        return RangeResult::Unsatisfiable;
    }
    let (start, end) = if a.is_empty() {
        // suffix range
        let Ok(n) = b.parse::<u64>() else {
            return RangeResult::Unsatisfiable;
        };
        if n == 0 {
            return RangeResult::Unsatisfiable;
        }
        (total.saturating_sub(n), total - 1)
    } else {
        let Ok(s) = a.parse::<u64>() else {
            return RangeResult::Unsatisfiable;
        };
        let e = if b.is_empty() {
            total - 1
        } else {
            match b.parse::<u64>() {
                Ok(e) => e.min(total - 1),
                Err(_) => return RangeResult::Unsatisfiable,
            }
        };
        (s, e)
    };
    if start > end || start >= total {
        return RangeResult::Unsatisfiable;
    }
    RangeResult::Single(start, end)
}

pub(crate) async fn put(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    if vpath.is_empty() {
        return Err(DavError::MethodNotAllowed);
    }
    let resolved = svc.core.resolve(user, vpath)?;
    let existing = match svc.core.stat_entry(user, vpath) {
        Ok(e) if e.can_read() => Some(e),
        Ok(_) => return Err(DavError::NotFound),
        Err(CoreError::NotFound) => None,
        Err(e) => return Err(e.into()),
    };
    if existing.as_ref().is_some_and(|e| e.kind == Kind::Dir) {
        return Err(DavError::MethodNotAllowed);
    }

    let etag = existing.as_ref().map(|e| e.etag.clone());
    let submitted = eval_if_header(
        svc,
        headers,
        resolved.share,
        vpath,
        etag.as_deref(),
        existing.is_some(),
    )?;
    ensure_unlocked(svc, resolved.share, vpath, &submitted, user)?;

    if let Some(im) = headers.get(header::IF_MATCH).and_then(|v| v.to_str().ok()) {
        match &etag {
            Some(e) if etag_matches(im, e) => {}
            _ => return Err(DavError::PreconditionFailed),
        }
    }
    if let Some(inm) = headers
        .get(header::IF_NONE_MATCH)
        .and_then(|v| v.to_str().ok())
    {
        let blocked = match &etag {
            Some(e) => etag_matches(inm, e),
            None => false,
        };
        if blocked || (inm.trim() == "*" && existing.is_some()) {
            return Err(DavError::PreconditionFailed);
        }
    }

    // The parent must exist; RFC 4918 says 409 when it does not.
    let parent = parent_of(vpath);
    match svc.core.stat_entry(user, &parent) {
        Ok(e) if e.kind == Kind::Dir => {}
        Ok(_) => return Err(DavError::Conflict),
        Err(CoreError::NotFound) => return Err(DavError::Conflict),
        Err(e) => return Err(e.into()),
    }

    svc.core.write_bytes(user, vpath, &body)?;

    let mut r = crate::empty(if existing.is_some() {
        StatusCode::NO_CONTENT
    } else {
        StatusCode::CREATED
    });
    if let Ok(e) = svc.core.stat_entry(user, vpath) {
        if let Ok(v) = HeaderValue::from_str(&format!("\"{}\"", e.etag)) {
            r.headers_mut().insert(header::ETAG, v);
        }
    }
    Ok(r)
}

pub(crate) async fn delete(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
) -> DavResult<Response> {
    if vpath.is_empty() {
        return Err(DavError::Forbidden);
    }
    let resolved = svc.core.resolve(user, vpath)?;
    let entry = svc.core.stat_entry(user, vpath)?;
    if !entry.can_read() {
        return Err(DavError::NotFound);
    }
    let submitted = eval_if_header(svc, headers, resolved.share, vpath, Some(&entry.etag), true)?;
    ensure_unlocked(svc, resolved.share, vpath, &submitted, user)?;
    if !entry.perms.contains(Perms::DELETE) {
        return Err(DavError::Forbidden);
    }
    svc.core.delete(user, vpath)?;
    Ok(crate::empty(StatusCode::NO_CONTENT))
}

pub(crate) async fn mkcol(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    // We support no MKCOL request bodies, so any body is a 415.
    if !body.is_empty() {
        return Err(DavError::UnsupportedMediaType);
    }
    if vpath.is_empty() {
        return Err(DavError::MethodNotAllowed);
    }
    let resolved = svc.core.resolve(user, vpath)?;
    match svc.core.stat_entry(user, vpath) {
        Ok(_) => return Err(DavError::MethodNotAllowed),
        Err(CoreError::NotFound) => {}
        Err(CoreError::NotListable) => return Err(DavError::NotFound),
        Err(e) => return Err(e.into()),
    }
    let parent = parent_of(vpath);
    match svc.core.stat_entry(user, &parent) {
        Ok(e) if e.kind == Kind::Dir => {}
        Ok(_) => return Err(DavError::Conflict),
        Err(CoreError::NotFound) => return Err(DavError::Conflict),
        Err(e) => return Err(e.into()),
    }
    let submitted = eval_if_header(svc, headers, resolved.share, vpath, None, false)?;
    ensure_unlocked(svc, resolved.share, &parent, &submitted, user)?;

    svc.core.mkdir(user, vpath)?;
    Ok(crate::empty(StatusCode::CREATED))
}

pub(crate) fn parent_of(vpath: &str) -> String {
    match vpath.rfind('/') {
        Some(i) => vpath[..i].to_string(),
        None => String::new(),
    }
}
