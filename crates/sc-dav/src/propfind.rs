//! PROPFIND — streaming 207 multistatus (`DESIGN-WEBDAV.md` §4).
//!
//! A hundred thousand entries must not become a hundred thousand-node DOM, so
//! the response is generated straight into a bounded channel with a 64 KiB
//! buffer and constant memory. Below `buffer_propfind_under` entries we take
//! the other path and buffer, because several Windows clients dislike a chunked
//! 207 and a `Content-Length` costs nothing at that size.

use std::collections::HashMap;
use std::sync::Arc;

use axum::body::Bytes;
use axum::response::Response;
use sc_vfs::{Kind, ShareId, UserId};
use http::{header, HeaderMap, StatusCode};

use crate::backend::{Entry, Order, Quota, Sort};
use crate::error::{DavError, DavResult};
use crate::locks::{lockdiscovery_xml, Depth};
use crate::props::{emit_live, PropCtx, PropReq, PropWriter};
use crate::xml::{escape_into, parse_propfind, PropFindBody, PropName, NS_DAV};
use crate::{parse_depth, DavService};

const FLUSH_AT: usize = 64 * 1024;

pub(crate) async fn handle(
    svc: &Arc<DavService>,
    user: UserId,
    vpath: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> DavResult<Response> {
    let depth = parse_depth(headers, Depth::Infinity)?;
    if depth == Depth::Infinity && !svc.cfg.allow_infinite_depth {
        // RFC 4918 explicitly sanctions refusing this, and the body tells the
        // client exactly why.
        return Err(DavError::FiniteDepthRequired);
    }

    let want = parse_propfind(&body, svc.cfg.max_request_body)?;
    let req = match want {
        PropFindBody::AllProp => PropReq {
            all: true,
            names_only: false,
            requested: Vec::new(),
        },
        PropFindBody::PropName => PropReq {
            all: false,
            names_only: true,
            requested: Vec::new(),
        },
        PropFindBody::Prop(names) => PropReq {
            all: false,
            names_only: false,
            requested: names,
        },
    };

    // A path the caller may not list must be indistinguishable from a missing
    // one — hence `CoreError::NotListable => 404` in the error mapping.
    let resolved = svc.core.resolve(user, vpath)?;
    let self_entry = svc.core.stat_entry(user, vpath)?;
    if !self_entry.can_read() {
        return Err(DavError::NotFound);
    }

    let quota = svc.core.quota(user, vpath).ok();

    let mut rows: Vec<(String, Entry)> = Vec::new();
    rows.push((vpath.to_string(), self_entry.clone()));

    match depth {
        Depth::Zero => {}
        Depth::One => {
            if self_entry.kind == Kind::Dir {
                let listing = svc.core.list(user, vpath, Sort::Name, Order::Asc)?;
                for e in listing.entries {
                    // Entries the caller cannot read are omitted outright.
                    // Returning 403 for them would leak their existence.
                    if !e.can_read() {
                        continue;
                    }
                    rows.push((join(vpath, &e.name), e));
                }
            }
        }
        Depth::Infinity => {
            let mut stack = vec![vpath.to_string()];
            let cap = svc.cfg.infinite_depth_max_entries as usize;
            while let Some(dir) = stack.pop() {
                if rows.len() >= cap {
                    break;
                }
                let Ok(listing) = svc.core.list(user, &dir, Sort::Name, Order::Asc) else {
                    continue;
                };
                for e in listing.entries {
                    if !e.can_read() {
                        continue;
                    }
                    if rows.len() >= cap {
                        break;
                    }
                    let p = join(&dir, &e.name);
                    if e.kind == Kind::Dir {
                        stack.push(p.clone());
                    }
                    rows.push((p, e));
                }
            }
        }
    }

    let prefix_ns = prefix_map(svc);
    let buffered = rows.len() < svc.cfg.buffer_propfind_under;

    if buffered {
        let mut out = String::with_capacity(1024 + rows.len() * 512);
        out.push_str(&open_tag(svc));
        for (p, e) in &rows {
            write_response(svc, user, resolved.share, p, e, &req, quota.as_ref(), &prefix_ns, &mut out);
        }
        out.push_str("</d:multistatus>\n");
        return Ok(crate::xml_body(StatusCode::MULTI_STATUS, out));
    }

    // Streaming path. The generator owns everything it needs; nothing but the
    // 64 KiB buffer is ever live at once.
    let (tx, rx) = tokio::sync::mpsc::channel::<Bytes>(4);
    let svc2 = svc.clone();
    let req2 = req.clone();
    let share = resolved.share;
    tokio::spawn(async move {
        let mut buf = String::with_capacity(FLUSH_AT + 4096);
        buf.push_str(&open_tag(&svc2));
        for (p, e) in &rows {
            write_response(
                &svc2,
                user,
                share,
                p,
                e,
                &req2,
                quota.as_ref(),
                &prefix_ns,
                &mut buf,
            );
            if buf.len() >= FLUSH_AT {
                let chunk = Bytes::from(std::mem::take(&mut buf).into_bytes());
                if tx.send(chunk).await.is_err() {
                    return;
                }
                buf.reserve(FLUSH_AT + 4096);
            }
        }
        buf.push_str("</d:multistatus>\n");
        let _ = tx.send(Bytes::from(buf.into_bytes())).await;
    });

    let stream = futures::stream::unfold(rx, |mut rx| async move {
        rx.recv()
            .await
            .map(|b| (Ok::<Bytes, std::io::Error>(b), rx))
    });
    let mut resp = Response::new(axum::body::Body::from_stream(stream));
    *resp.status_mut() = StatusCode::MULTI_STATUS;
    resp.headers_mut().insert(
        header::CONTENT_TYPE,
        http::HeaderValue::from_static("application/xml; charset=utf-8"),
    );
    Ok(resp)
}

pub(crate) fn prefix_map(svc: &DavService) -> HashMap<String, String> {
    let mut m = HashMap::new();
    m.insert("d".to_string(), NS_DAV.to_string());
    for s in &svc.sources {
        for (p, u) in s.namespaces() {
            m.insert((*p).to_string(), (*u).to_string());
        }
    }
    m
}

fn open_tag(svc: &DavService) -> String {
    format!(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:multistatus{}>\n",
        svc.ns_decls
    )
}

fn join(dir: &str, name: &str) -> String {
    if dir.is_empty() {
        name.to_string()
    } else {
        format!("{dir}/{name}")
    }
}

#[allow(clippy::too_many_arguments)]
fn write_response(
    svc: &DavService,
    user: UserId,
    share: ShareId,
    vpath: &str,
    e: &Entry,
    req: &PropReq,
    quota: Option<&Quota>,
    prefix_ns: &HashMap<String, String>,
    out: &mut String,
) {
    let mut w = PropWriter::new(req.names_only, prefix_ns.clone());

    let dead = match e.id {
        Some(id) => svc.meta.get_props(id).unwrap_or_default(),
        None => Vec::new(),
    };
    let locks = svc.locks.at(share, vpath);
    let href = svc.href_of(vpath, e.kind == Kind::Dir);
    let ld = lockdiscovery_xml(&locks, &href);

    emit_live(e, req, quota, &ld, &dead, &mut w);

    // Decorators get their turn with the same entry and request.
    if !svc.sources.is_empty() {
        let path = sc_vfs::SafePath::parse(vpath, 4096).unwrap_or_else(|_| sc_vfs::SafePath::root());
        let ctx = PropCtx {
            user,
            share,
            path,
            is_root: vpath.is_empty(),
        };
        for s in &svc.sources {
            s.emit(e, &ctx, req, &mut w);
        }
    }

    let (ok, mut missing, emitted) = w.finish();
    if req.is_explicit() {
        for want in &req.requested {
            if !emitted.iter().any(|p| p == want) && !missing.iter().any(|p| p == want) {
                missing.push(want.clone());
            }
        }
    }

    out.push_str("<d:response><d:href>");
    escape_into(&href, out);
    out.push_str("</d:href>");
    if !ok.is_empty() {
        out.push_str("<d:propstat><d:prop>");
        out.push_str(&ok);
        out.push_str("</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>");
    }
    if !missing.is_empty() {
        out.push_str("<d:propstat><d:prop>");
        for p in &missing {
            write_bare_name(p, out);
        }
        out.push_str("</d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>");
    }
    out.push_str("</d:response>\n");
}

fn write_bare_name(p: &PropName, out: &mut String) {
    if !crate::xml::is_valid_xml_name(&p.name) {
        return;
    }
    if p.ns == NS_DAV {
        out.push_str("<d:");
        out.push_str(&p.name);
        out.push_str("/>");
    } else {
        out.push_str("<x:");
        out.push_str(&p.name);
        out.push_str(" xmlns:x=\"");
        escape_into(&p.ns, out);
        out.push_str("\"/>");
    }
}
