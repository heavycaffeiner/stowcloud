//! COPY / MOVE.
//!
//! Cross-mount bulk moves have a hard limit rather than a progress bar: WebDAV
//! has no progress concept, `202 Accepted` is badly supported by real clients,
//! and anything behind a CDN has to finish inside its request timeout. A clear
//! 507 beats a silent timeout.

use std::sync::Arc;

use axum::response::Response;
use sc_vfs::{Kind, UserId};
use http::{HeaderMap, StatusCode};

use crate::backend::CoreError;
use crate::error::{DavError, DavResult};
use crate::locks::Depth;
use crate::methods::parent_of;
use crate::{ensure_unlocked, eval_if_header, parse_depth, DavService};

/// Parse `Destination` into a virtual path. Cross-host destinations are a 502:
/// we do not proxy to another server.
pub(crate) fn parse_destination(
    svc: &DavService,
    headers: &HeaderMap,
) -> DavResult<String> {
    let raw = headers
        .get("destination")
        .ok_or_else(|| DavError::BadRequest("missing Destination".into()))?
        .to_str()
        .map_err(|_| DavError::BadRequest("bad Destination".into()))?
        .trim();

    let path = if let Some(rest) = raw
        .strip_prefix("http://")
        .or_else(|| raw.strip_prefix("https://"))
    {
        let (authority, p) = match rest.split_once('/') {
            Some((a, p)) => (a, format!("/{p}")),
            None => (rest, "/".to_string()),
        };
        let want = headers
            .get(http::header::HOST)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        // Compare host *and* port. An empty Host header means we cannot prove
        // it is the same origin, so refuse.
        if want.is_empty() || !authority.eq_ignore_ascii_case(want) {
            return Err(DavError::BadGateway);
        }
        p
    } else if raw.starts_with('/') {
        raw.to_string()
    } else {
        return Err(DavError::BadRequest("relative Destination".into()));
    };

    let path = path.split(['?', '#']).next().unwrap_or(&path).to_string();
    svc.vpath_of(&path)
}

pub(crate) async fn handle(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    is_move: bool,
) -> DavResult<Response> {
    if vpath.is_empty() {
        return Err(DavError::Forbidden);
    }
    let dest = parse_destination(svc, headers)?;
    if dest.is_empty() {
        return Err(DavError::Forbidden);
    }
    if dest == vpath {
        return Err(DavError::Forbidden);
    }
    // Moving a collection into itself would build an infinite tree.
    if dest.starts_with(vpath) && dest.as_bytes().get(vpath.len()) == Some(&b'/') {
        return Err(DavError::Conflict);
    }

    let overwrite = match headers.get("overwrite").and_then(|v| v.to_str().ok()) {
        None => true,
        Some(v) if v.trim().eq_ignore_ascii_case("t") => true,
        Some(v) if v.trim().eq_ignore_ascii_case("f") => false,
        Some(_) => return Err(DavError::BadRequest("bad Overwrite".into())),
    };

    // COPY of a collection defaults to depth infinity; 0 is the only other
    // legal value and means "the collection itself, without members".
    let _depth = parse_depth(headers, Depth::Infinity)?;

    let src_resolved = svc.core.resolve(user, vpath)?;
    let src = svc.core.stat_entry(user, vpath)?;
    if !src.can_read() {
        return Err(DavError::NotFound);
    }

    let dst_resolved = svc.core.resolve(user, &dest)?;
    let dst_parent = parent_of(&dest);
    match svc.core.stat_entry(user, &dst_parent) {
        Ok(e) if e.kind == Kind::Dir => {}
        // 409, not 404: the destination's parent is missing.
        Ok(_) | Err(CoreError::NotFound) => return Err(DavError::Conflict),
        Err(CoreError::NotListable) => return Err(DavError::NotFound),
        Err(e) => return Err(e.into()),
    }

    let existing = match svc.core.stat_entry(user, &dest) {
        Ok(e) => Some(e),
        Err(CoreError::NotFound) => None,
        Err(CoreError::NotListable) => return Err(DavError::NotFound),
        Err(e) => return Err(e.into()),
    };
    if existing.is_some() && !overwrite {
        return Err(DavError::PreconditionFailed);
    }

    // Locks: source only matters for MOVE (it is removed), destination always.
    let submitted = eval_if_header(
        svc,
        headers,
        src_resolved.share,
        vpath,
        Some(&src.etag),
        true,
    )?;
    ensure_unlocked(svc, dst_resolved.share, &dest, &submitted, user)?;
    ensure_unlocked(svc, dst_resolved.share, &dst_parent, &submitted, user)?;
    if is_move {
        ensure_unlocked(svc, src_resolved.share, vpath, &submitted, user)?;
    }

    // Cross-mount: bounded synchronous work only.
    if src_resolved.share != dst_resolved.share {
        let bytes = if src.kind == Kind::Dir {
            svc.core
                .aggregate(src_resolved.share, &src_resolved.path)
                .map(|a| a.rsize)
                .unwrap_or(src.size)
        } else {
            src.size
        };
        if bytes > svc.cfg.sync_copy_limit {
            return Err(DavError::InsufficientStorage);
        }
    }

    if existing.is_some() {
        svc.core.delete(user, &dest)?;
    }

    if is_move {
        svc.core.rename(user, vpath, &dest)?;
    } else {
        svc.core.copy_to(user, vpath, &dest)?;
    }

    Ok(crate::empty(if existing.is_some() {
        StatusCode::NO_CONTENT
    } else {
        StatusCode::CREATED
    }))
}
