//! LOCK / UNLOCK (`DESIGN-WEBDAV.md` §5).

use std::sync::Arc;

use axum::body::Bytes;
use axum::response::Response;
use sc_vfs::{FileId, UserId};
use http::{HeaderMap, HeaderValue, StatusCode};

use crate::backend::CoreError;
use crate::error::{DavError, DavResult};
use crate::locks::{lockdiscovery_xml, parse_timeout_header, Depth};
use crate::xml::{parse_lockinfo, LockBody, LockScope};
use crate::{eval_if_header, parse_depth, DavService};

pub(crate) async fn lock(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    let depth = parse_depth(headers, Depth::Infinity)?;
    if depth == Depth::One {
        return Err(DavError::BadRequest("Depth: 1 is not valid for LOCK".into()));
    }
    let timeout = svc
        .locks
        .clamp_timeout(headers.get("timeout").and_then(|v| v.to_str().ok()).and_then(parse_timeout_header));

    let resolved = svc.core.resolve(user, vpath)?;
    let share = resolved.share;

    // ---- refresh: no body, token supplied in If (or Lock-Token) ----
    if body.is_empty() {
        let token = headers
            .get("lock-token")
            .and_then(|v| v.to_str().ok())
            .map(|s| s.to_string())
            .or_else(|| {
                headers
                    .get("if")
                    .and_then(|v| v.to_str().ok())
                    .and_then(|raw| crate::IfHeader::parse(raw).ok())
                    .and_then(|h| h.tokens().first().map(|t| t.to_string()))
            });

        if let Some(token) = token {
            let Some(l) = svc.locks.refresh(&token, timeout) else {
                return Err(DavError::PreconditionFailed);
            };
            let href = svc.href_of(&l.path, false);
            return Ok(lock_response(
                svc,
                StatusCode::OK,
                &lockdiscovery_xml(std::slice::from_ref(&l), &href),
                Some(&l.token_urn()),
            ));
        }
        // else: no body and no token — fall through to the new-lock path
        // below with a synthesized default lockinfo. This is the Android
        // client's pre-upload probe LOCK (`LockMethod.kt` builds every LOCK
        // via `temp.method("LOCK", null)`; that OkHttp wrapper has no body
        // parameter at all, so a bodiless LOCK is the only shape it can send).
        // The reference server's `FakeLockerPlugin` answers any LOCK unconditionally
        // with success; we go through the normal conflict-checked `acquire()`
        // instead of faking one, since real locking is otherwise implemented
        // and tested here.
    }

    let info = if body.is_empty() {
        LockBody {
            scope: LockScope::Exclusive,
            owner: String::new(),
        }
    } else {
        parse_lockinfo(&body, svc.cfg.max_request_body)?
    };

    // ---- lock-null replacement: create a 0-byte file (RFC 4918 §7.3) ----
    let mut created = false;
    let entry = match svc.core.stat_entry(user, vpath) {
        Ok(e) if e.can_read() => e,
        Ok(_) => return Err(DavError::NotFound),
        Err(CoreError::NotListable) => return Err(DavError::NotFound),
        Err(CoreError::NotFound) => {
            svc.core.create_empty(user, vpath)?;
            created = true;
            svc.core.stat_entry(user, vpath)?
        }
        Err(e) => return Err(e.into()),
    };

    let submitted =
        eval_if_header(svc, headers, share, vpath, Some(&entry.etag), !created)?;
    let submitted_refs: Vec<&str> = submitted.iter().map(|s| s.as_str()).collect();

    // A path with no node row yet still needs an identity; fall back to a
    // deterministic placeholder so the lock is at least self-consistent until
    // the core allocates one. Ancestor checks use the path anyway (§5.1).
    let fileid = entry.id.unwrap_or(FileId(0));

    let l = match svc.locks.acquire(
        share,
        vpath,
        fileid,
        user,
        info.owner,
        depth,
        info.scope,
        timeout,
        &submitted_refs,
    ) {
        Ok(l) => l,
        Err(_conflict) => return Err(DavError::Locked),
    };

    let href = svc.href_of(vpath, entry.is_dir());
    Ok(lock_response(
        svc,
        if created {
            StatusCode::CREATED
        } else {
            StatusCode::OK
        },
        &lockdiscovery_xml(std::slice::from_ref(&l), &href),
        Some(&l.token_urn()),
    ))
}

fn lock_response(
    svc: &DavService,
    status: StatusCode,
    lockdiscovery: &str,
    token: Option<&str>,
) -> Response {
    let body = format!(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:prop{}>\n<d:lockdiscovery>{}</d:lockdiscovery>\n</d:prop>\n",
        svc.ns_decls, lockdiscovery
    );
    let mut r = crate::xml_body(status, body);
    if let Some(t) = token {
        if let Ok(v) = HeaderValue::from_str(&format!("<{t}>")) {
            r.headers_mut().insert("lock-token", v);
        }
    }
    r
}

pub(crate) async fn unlock(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
) -> DavResult<Response> {
    let raw = headers
        .get("lock-token")
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| DavError::BadRequest("UNLOCK without Lock-Token".into()))?;

    let resolved = svc.core.resolve(user, vpath)?;
    let Some(l) = svc.locks.by_token(raw) else {
        // RFC 4918 §9.11.1: no such lock on this resource.
        return Err(DavError::Conflict);
    };
    if l.share != resolved.share || !l.covers(resolved.share, vpath) {
        return Err(DavError::Conflict);
    }
    if l.principal != user {
        return Err(DavError::Forbidden);
    }
    svc.locks.release(raw);
    Ok(crate::empty(StatusCode::NO_CONTENT))
}
