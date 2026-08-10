//! PROPPATCH — dead properties.
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

/// Live properties are server-maintained and cannot be set by a client. A
/// property a [`crate::PropPatchSource`] claims is live too, but it *is*
/// settable: the source, not the dead-property store, decides what happens.
fn is_protected(svc: &DavService, p: &PropName) -> bool {
    if claim_for(svc, p).is_some() {
        return false;
    }
    p.ns == NS_DAV && LIVE_PROPS.contains(&p.name.as_str())
}

fn claim_for<'a>(
    svc: &'a DavService,
    p: &PropName,
) -> Option<&'a Arc<dyn crate::PropPatchSource>> {
    svc.patch_sources
        .iter()
        .find(|s| s.claims().iter().any(|(ns, n)| *ns == p.ns && *n == p.name))
}

/// A file id that definitely exists, allocating one if the core has not yet.
///
/// The core allocates lazily, so a path that has never been written to,
/// aggregated or otherwise touched has no `node` row. A dead property can live
/// with that; a live one cannot, because nothing will resend it.
fn materialise_id(
    svc: &DavService,
    entry: &crate::backend::Entry,
    user: UserId,
    vpath: &str,
) -> Option<sc_vfs::FileId> {
    if let Some(id) = entry.id {
        return Some(id);
    }
    match svc.core.ensure_fileid(user, vpath) {
        Ok(id) => Some(id),
        Err(e) => {
            tracing::warn!(error = %e, vpath, "could not materialise a file id for a live PROPPATCH");
            None
        }
    }
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
        if is_protected(svc, name) {
            refused.push((name.clone(), StatusCode::FORBIDDEN));
        } else if !is_valid_xml_name(&name.name) {
            refused.push((name.clone(), StatusCode::BAD_REQUEST));
        }
    }

    let mut results: Vec<(PropName, StatusCode)> = Vec::new();
    if refused.is_empty() {
        for op in &ops {
            let name = match op {
                PropPatchOp::Set(n, _) | PropPatchOp::Remove(n) => n,
            };
            // A claimed property is a live one: it never reaches the dead
            // store, and the source is given a materialised file id because a
            // live property that silently does not persist is worse than a
            // visible failure. This is where the two differ from a dead
            // property, which is legitimately accepted with a warning when no
            // id exists (Office aborts the save on anything but 200 and
            // re-sends the value next time; nothing re-sends a live one).
            if let Some(src) = claim_for(svc, name) {
                let id = match materialise_id(svc, &entry, user, vpath) {
                    Some(id) => id,
                    None => {
                        results.push((name.clone(), StatusCode::INTERNAL_SERVER_ERROR));
                        continue;
                    }
                };
                let value = match op {
                    PropPatchOp::Set(_, v) => Some(v.as_str()),
                    PropPatchOp::Remove(_) => None,
                };
                let st = match src.set(user, resolved.share, id, &name.ns, &name.name, value) {
                    Ok(()) => StatusCode::OK,
                    Err(e) => {
                        tracing::warn!(error = %e, ns = %name.ns, name = %name.name,
                                       "a live property source refused a PROPPATCH");
                        e.status()
                    }
                };
                results.push((name.clone(), st));
                continue;
            }
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
