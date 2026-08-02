//! PROPPATCH — dead properties (`DESIGN-WEBDAV.md` §4.2).
//!
//! Values are stored as text and **re-serialised** on the way out. The client's
//! original markup is never echoed: doing so is a straight XML-injection hole.
//!
//! MS Office sends `Win32CreationTime`, `Win32LastAccessTime`,
//! `Win32LastModifiedTime` and `Win32FileAttributes` when saving and treats
//! anything other than a 200 for them as a failure, so they are accepted like
//! any other dead property.

use std::sync::Arc;

use axum::body::Bytes;
use axum::response::Response;
use sc_vfs::UserId;
use http::{HeaderMap, StatusCode};

use crate::error::{DavError, DavResult};
use crate::props::LIVE_PROPS;
use crate::xml::{escape_into, is_valid_xml_name, parse_proppatch, PropName, PropPatchOp, NS_DAV};
use crate::{ensure_unlocked, eval_if_header, DavService};

/// Live properties are server-maintained and cannot be set by a client.
fn is_protected(p: &PropName) -> bool {
    p.ns == NS_DAV && LIVE_PROPS.contains(&p.name.as_str())
}

pub(crate) async fn handle(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    let ops = parse_proppatch(&body, svc.cfg.max_request_body)?;

    let resolved = svc.core.resolve(user, vpath)?;
    let entry = svc.core.stat_entry(user, vpath)?;
    if !entry.can_read() {
        return Err(DavError::NotFound);
    }
    let submitted = eval_if_header(
        svc,
        headers,
        resolved.share,
        vpath,
        Some(&entry.etag),
        true,
    )?;
    ensure_unlocked(svc, resolved.share, vpath, &submitted, user)?;
    if !entry.perms.contains(crate::backend::Perms::WRITE) {
        return Err(DavError::Forbidden);
    }

    // Two passes: RFC 4918 requires the whole PROPPATCH to be atomic, so if any
    // property is refused the rest report 424 rather than being applied.
    let mut refused: Vec<(PropName, StatusCode)> = Vec::new();
    for op in &ops {
        let name = match op {
            PropPatchOp::Set(n, _) | PropPatchOp::Remove(n) => n,
        };
        if is_protected(name) {
            refused.push((name.clone(), StatusCode::FORBIDDEN));
        } else if !is_valid_xml_name(&name.name) {
            refused.push((name.clone(), StatusCode::BAD_REQUEST));
        }
    }

    let mut results: Vec<(PropName, StatusCode)> = Vec::new();
    if refused.is_empty() {
        for op in &ops {
            let st = match op {
                PropPatchOp::Set(n, v) => {
                    match entry.id {
                        Some(id) => match svc.meta.set_prop(id, &n.ns, &n.name, v) {
                            Ok(()) => StatusCode::OK,
                            Err(e) => {
                                tracing::warn!("dead property store failed: {e}");
                                StatusCode::INTERNAL_SERVER_ERROR
                            }
                        },
                        // The core has not allocated a node row for this path
                        // yet (lazy allocation). Accept rather than fail: Office
                        // aborts the save on anything but 200, and the value is
                        // re-sent on the next save.
                        None => {
                            tracing::warn!(
                                "PROPPATCH on a path with no file id; property not persisted"
                            );
                            StatusCode::OK
                        }
                    }
                }
                PropPatchOp::Remove(n) => match entry.id {
                    Some(id) => match svc.meta.del_prop(id, &n.ns, &n.name) {
                        Ok(()) => StatusCode::OK,
                        Err(_) => StatusCode::INTERNAL_SERVER_ERROR,
                    },
                    None => StatusCode::OK,
                },
            };
            let n = match op {
                PropPatchOp::Set(n, _) | PropPatchOp::Remove(n) => n.clone(),
            };
            results.push((n, st));
        }
    } else {
        for op in &ops {
            let n = match op {
                PropPatchOp::Set(n, _) | PropPatchOp::Remove(n) => n.clone(),
            };
            let st = refused
                .iter()
                .find(|(r, _)| *r == n)
                .map(|(_, s)| *s)
                .unwrap_or(StatusCode::FAILED_DEPENDENCY);
            results.push((n, st));
        }
    }

    let href = svc.href_of(vpath, entry.is_dir());
    let mut out = String::with_capacity(512);
    out.push_str("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:multistatus");
    out.push_str(&svc.ns_decls);
    out.push_str(">\n<d:response><d:href>");
    escape_into(&href, &mut out);
    out.push_str("</d:href>");

    let mut codes: Vec<StatusCode> = results.iter().map(|(_, s)| *s).collect();
    codes.sort_by_key(|c| c.as_u16());
    codes.dedup();
    for code in codes {
        out.push_str("<d:propstat><d:prop>");
        for (n, _) in results.iter().filter(|(_, s)| *s == code) {
            if n.ns == NS_DAV {
                out.push_str("<d:");
                out.push_str(&n.name);
                out.push_str("/>");
            } else if is_valid_xml_name(&n.name) {
                out.push_str("<x:");
                out.push_str(&n.name);
                out.push_str(" xmlns:x=\"");
                escape_into(&n.ns, &mut out);
                out.push_str("\"/>");
            }
        }
        out.push_str("</d:prop><d:status>HTTP/1.1 ");
        out.push_str(&code.as_u16().to_string());
        out.push(' ');
        out.push_str(code.canonical_reason().unwrap_or("Unknown"));
        out.push_str("</d:status></d:propstat>");
    }
    out.push_str("</d:response>\n</d:multistatus>\n");

    Ok(crate::xml_body(StatusCode::MULTI_STATUS, out))
}
