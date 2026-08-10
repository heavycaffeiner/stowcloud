//! The trashbin collection.
//!
//! The core's trash is per share, flat inside each share, and **off by
//! default**. The compat protocol has exactly one flat trash collection per
//! user. This module is that mapping and nothing else: every operation
//! re-resolves the share id out of the entry name and re-runs the ACL check,
//! so an entry naming a share the caller cannot reach is `404`, never `403`,
//! matching the existence rule the rest of the server follows.

use std::sync::Arc;

use axum::extract::Request;
use axum::response::{IntoResponse, Response};
use http::StatusCode;
use sc_vfs::{ShareId, UserId};

use crate::nc::{path_escape, prop_tag, propfind_row};

/// Reserved-range marker for a trash entry's synthetic `oc:fileid`.
///
/// A trashed file has no `node` row: it was moved into a control directory the
/// metadata store never indexes. The client still needs a stable key per row,
/// so one is derived from the entry's own name, which is itself derived from
/// the share id and the core's trash id and therefore stable for as long as the
/// entry exists. Bit 60, distinct from the files-root (bit 61) and share-root
/// (bit 62) markers, and positive for the same reason those are.
const TRASH_PSEUDO_MARKER: i64 = 1 << 60;

fn trash_pseudo_id(entry_id: &str) -> i64 {
    let h = blake3::hash(entry_id.as_bytes());
    let mut b = [0u8; 8];
    b.copy_from_slice(&h.as_bytes()[..8]);
    TRASH_PSEUDO_MARKER | (i64::from_le_bytes(b) & 0x0FFF_FFFF_FFFF_FFFF)
}

/// `{share_id}.{core_id}` — the URL segment naming one trash entry.
///
/// The core id is a `Uuid::simple` (32 lowercase hex) so a `.` separator is
/// unambiguous and needs no escaping. The encoding is derived, so there is no
/// alias table to grow or sweep, and it is not a capability: every operation
/// re-resolves the share and re-checks the ACL.
fn encode_entry(share: ShareId, core_id: &str) -> String {
    format!("{}.{}", share.get(), core_id)
}

fn decode_entry(seg: &str) -> Option<(ShareId, &str)> {
    let (share, id) = seg.split_once('.')?;
    let share: u32 = share.parse().ok()?;
    if id.is_empty() {
        return None;
    }
    Some((ShareId::new(share), id))
}

/// Every share the caller can reach that has trash turned on.
///
/// An empty result is a normal state, not a fault: per-share trash is off by
/// default, so a default install has no trash anywhere and the collection is
/// simply empty.
fn trash_shares(core: &sc_core::Core, user: UserId) -> Vec<ShareId> {
    core.roots(user)
        .into_iter()
        .filter(|r| r.trash_enabled)
        .map(|r| r.share)
        .fold(Vec::new(), |mut acc, s| {
            if !acc.contains(&s) {
                acc.push(s);
            }
            acc
        })
}

pub struct TrashApi {
    pub core: Arc<sc_core::Core>,
    pub instance_id: Arc<str>,
}

impl TrashApi {
    /// `PROPFIND` on the collection or on one entry.
    pub async fn propfind(
        &self,
        user: UserId,
        prefix: &str,
        entry: Option<&str>,
        max_body: usize,
        req: Request,
    ) -> Response {
        let depth_children = req
            .headers()
            .get("depth")
            .and_then(|v| v.to_str().ok())
            .map(|v| v.trim() != "0")
            .unwrap_or(true)
            && entry.is_none();

        let body = match axum::body::to_bytes(req.into_body(), max_body).await {
            Ok(b) => b,
            Err(_) => return StatusCode::PAYLOAD_TOO_LARGE.into_response(),
        };
        let want = match sc_dav::xml::parse_propfind(&body, max_body) {
            Ok(w) => w,
            Err(_) => return StatusCode::BAD_REQUEST.into_response(),
        };
        let propreq = match want {
            sc_dav::xml::PropFindBody::AllProp => sc_dav::PropReq {
                all: true,
                names_only: false,
                requested: Vec::new(),
            },
            sc_dav::xml::PropFindBody::PropName => sc_dav::PropReq {
                all: false,
                names_only: true,
                requested: Vec::new(),
            },
            sc_dav::xml::PropFindBody::Prop(names) => sc_dav::PropReq {
                all: false,
                names_only: false,
                requested: names,
            },
        };

        let mut rows: Vec<(String, sc_core::TrashEntry)> = Vec::new();
        for share in trash_shares(&self.core, user) {
            let Ok(entries) = self.core.trash_list(user, share) else {
                continue;
            };
            for e in entries {
                rows.push((encode_entry(share, &e.id), e));
            }
        }
        if let Some(wanted) = entry {
            rows.retain(|(seg, _)| seg == wanted);
            if rows.is_empty() {
                return StatusCode::NOT_FOUND.into_response();
            }
        }
        // Newest deletion first: it is the order every client's Deleted files
        // screen presents, and the protocol carries no sort field.
        rows.sort_by(|a, b| b.1.deleted_at_ns.cmp(&a.1.deleted_at_ns));

        let body = trash_propfind_xml(
            prefix,
            entry,
            &rows,
            depth_children,
            &propreq,
            &self.instance_id,
        );
        (
            StatusCode::MULTI_STATUS,
            [(
                http::header::CONTENT_TYPE,
                "application/xml; charset=utf-8",
            )],
            body,
        )
            .into_response()
    }

    /// `DELETE` on one entry: purge exactly it.
    pub fn purge_one(&self, user: UserId, entry: &str) -> Response {
        let Some((share, id)) = decode_entry(entry) else {
            return StatusCode::NOT_FOUND.into_response();
        };
        if !trash_shares(&self.core, user).contains(&share) {
            // An id naming a share the caller cannot reach is not-found, never
            // forbidden: 403 would confirm the entry exists.
            return StatusCode::NOT_FOUND.into_response();
        }
        match self.core.trash_purge(user, share, Some(id)) {
            Ok(()) => StatusCode::NO_CONTENT.into_response(),
            Err(sc_core::CoreError::Denied { .. }) | Err(sc_core::CoreError::NotFound) => {
                StatusCode::NOT_FOUND.into_response()
            }
            Err(e) => {
                tracing::warn!(error = %e, entry, "purging one trash entry failed");
                StatusCode::INTERNAL_SERVER_ERROR.into_response()
            }
        }
    }

    /// `DELETE` on the collection: empty every reachable trash.
    ///
    /// A partial failure answers `500` and names the shares that were emptied,
    /// rather than reporting success for a job half done.
    pub fn empty_all(&self, user: UserId) -> Response {
        let mut emptied: Vec<u32> = Vec::new();
        let mut failed: Vec<u32> = Vec::new();
        for share in trash_shares(&self.core, user) {
            match self.core.trash_purge(user, share, None) {
                Ok(()) => emptied.push(share.get()),
                Err(e) => {
                    tracing::warn!(error = %e, share = share.get(), "emptying a share's trash failed");
                    failed.push(share.get());
                }
            }
        }
        if failed.is_empty() {
            return StatusCode::NO_CONTENT.into_response();
        }
        (
            StatusCode::INTERNAL_SERVER_ERROR,
            [(
                http::header::CONTENT_TYPE,
                "text/plain; charset=utf-8",
            )],
            format!(
                "emptied shares {emptied:?}; shares {failed:?} could not be emptied and still hold deleted files"
            ),
        )
            .into_response()
    }

    /// `MOVE` an entry into the `restore/` collection: put it back.
    pub fn restore(&self, user: UserId, entry: &str, destination: Option<&str>) -> Response {
        let Some((share, id)) = decode_entry(entry) else {
            return StatusCode::NOT_FOUND.into_response();
        };
        // The destination leaf is ignored, but the *collection* is not: a
        // `Destination` outside `restore/` is a request to move a trashed file
        // somewhere specific, which this server cannot do, and answering it
        // with a restore-to-original would put the file somewhere the client
        // did not ask for.
        let dest_ok = destination
            .and_then(|d| {
                let path = match d.split_once("://") {
                    Some((_, rest)) => rest.split_once('/').map(|(_, p)| format!("/{p}")),
                    None => Some(d.to_string()),
                }?;
                sc_compat_nc::dav_paths::parse(&path)
            })
            .map(|t| matches!(t, sc_compat_nc::dav_paths::DavTarget::TrashRestore { .. }))
            .unwrap_or(false);
        if !dest_ok {
            return (
                StatusCode::BAD_REQUEST,
                [(
                    http::header::CONTENT_TYPE,
                    "text/plain; charset=utf-8",
                )],
                "a trashed file can only be moved back to where it came from; \
                 Destination must name the restore collection",
            )
                .into_response();
        }
        if !trash_shares(&self.core, user).contains(&share) {
            return StatusCode::NOT_FOUND.into_response();
        }
        match self.core.trash_restore(user, share, id) {
            Ok(()) => StatusCode::CREATED.into_response(),
            Err(sc_core::CoreError::Conflict) => StatusCode::PRECONDITION_FAILED.into_response(),
            Err(sc_core::CoreError::Denied { .. }) | Err(sc_core::CoreError::NotFound) => {
                StatusCode::NOT_FOUND.into_response()
            }
            Err(e) => {
                tracing::warn!(error = %e, entry, "restoring a trash entry failed");
                StatusCode::INTERNAL_SERVER_ERROR.into_response()
            }
        }
    }
}

fn trash_propfind_xml(
    prefix: &str,
    entry: Option<&str>,
    rows: &[(String, sc_core::TrashEntry)],
    list_children: bool,
    req: &sc_dav::PropReq,
    instance_id: &str,
) -> String {
    let mut body = String::from(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n\
         <d:multistatus xmlns:d=\"DAV:\" xmlns:oc=\"http://owncloud.org/ns\" xmlns:nc=\"http://nextcloud.org/ns\">",
    );

    if entry.is_none() {
        // The collection's own row. It always exists, even with no trash
        // anywhere: a 404 here makes the app render an error for a state that
        // is normal on a default install.
        let self_known: Vec<(&'static str, &'static str, String)> = vec![
            (
                sc_dav::xml::NS_DAV,
                "resourcetype",
                "<d:resourcetype><d:collection/></d:resourcetype>".to_string(),
            ),
            (
                sc_compat_nc::NS_OC,
                "permissions",
                prop_tag("oc", "permissions", "GD", req.names_only),
            ),
        ];
        body.push_str(&propfind_row(
            &path_escape(&format!("{prefix}/")),
            &self_known,
            req,
        ));
    }

    if list_children || entry.is_some() {
        for (seg, e) in rows {
            let href = if entry.is_some() {
                path_escape(prefix)
            } else {
                path_escape(&format!("{prefix}/{seg}"))
            };
            let fid = sc_vfs::FileId::new(trash_pseudo_id(seg));
            let deleted_s = e.deleted_at_ns / 1_000_000_000;
            let mut known: Vec<(&'static str, &'static str, String)> = vec![
                (
                    sc_dav::xml::NS_DAV,
                    "resourcetype",
                    if e.is_dir {
                        "<d:resourcetype><d:collection/></d:resourcetype>".to_string()
                    } else {
                        "<d:resourcetype/>".to_string()
                    },
                ),
                (
                    sc_dav::xml::NS_DAV,
                    "displayname",
                    prop_tag("d", "displayname", &e.name, req.names_only),
                ),
                (
                    sc_compat_nc::NS_OC,
                    "id",
                    prop_tag(
                        "oc",
                        "id",
                        &sc_compat_nc::nc_id(fid, instance_id),
                        req.names_only,
                    ),
                ),
                (
                    sc_compat_nc::NS_OC,
                    "fileid",
                    prop_tag("oc", "fileid", &fid.0.to_string(), req.names_only),
                ),
                // Must contain `D`, or both apps hide purge and restore. `G`
                // as well, because a permission string with no read bit makes
                // a client skip the entry entirely.
                (
                    sc_compat_nc::NS_OC,
                    "permissions",
                    prop_tag("oc", "permissions", "GD", req.names_only),
                ),
                (
                    sc_compat_nc::NS_NC,
                    "trashbin-filename",
                    prop_tag("nc", "trashbin-filename", &e.name, req.names_only),
                ),
                (
                    sc_compat_nc::NS_NC,
                    "trashbin-original-location",
                    prop_tag(
                        "nc",
                        "trashbin-original-location",
                        // No leading separator: that is the shape both parsers
                        // expect, and it is already share-relative.
                        if e.orig_path.is_empty() {
                            &e.name
                        } else {
                            &e.orig_path
                        },
                        req.names_only,
                    ),
                ),
                (
                    sc_compat_nc::NS_NC,
                    "trashbin-deletion-time",
                    prop_tag(
                        "nc",
                        "trashbin-deletion-time",
                        &deleted_s.to_string(),
                        req.names_only,
                    ),
                ),
                (
                    sc_compat_nc::NS_NC,
                    "has-preview",
                    prop_tag("nc", "has-preview", "false", req.names_only),
                ),
            ];
            // A trashed *file* has a real size. A trashed directory's is the
            // inode's own, and reporting that as `oc:size` would be the same
            // plausible-but-wrong 4096 the ordinary listing stopped emitting:
            // the trash is flat and holds no aggregate, so the honest answer
            // is no property at all.
            if !e.is_dir {
                known.push((
                    sc_compat_nc::NS_OC,
                    "size",
                    prop_tag("oc", "size", &e.size.to_string(), req.names_only),
                ));
                known.push((
                    sc_dav::xml::NS_DAV,
                    "getcontentlength",
                    prop_tag(
                        "d",
                        "getcontentlength",
                        &e.size.to_string(),
                        req.names_only,
                    ),
                ));
            }
            body.push_str(&propfind_row(&href, &known, req));
        }
    }

    body.push_str("</d:multistatus>");
    body
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_entry_segment_round_trips_and_a_malformed_one_is_refused() {
        let seg = encode_entry(ShareId::new(7), "0123456789abcdef0123456789abcdef");
        assert_eq!(seg, "7.0123456789abcdef0123456789abcdef");
        let (share, id) = decode_entry(&seg).unwrap();
        assert_eq!(share.get(), 7);
        assert_eq!(id, "0123456789abcdef0123456789abcdef");

        assert!(decode_entry("nodot").is_none());
        assert!(decode_entry("notanumber.abc").is_none());
        assert!(decode_entry("7.").is_none());
    }

    /// A trash id has to be positive and disjoint from the other two synthetic
    /// ranges, for the same reason those are: clients zero-pad it, store it and
    /// diff it as an ordinary rowid.
    #[test]
    fn the_pseudo_id_stays_a_positive_integer_in_its_own_range() {
        for s in ["1.aaaa", "2.bbbb", "999.cccc"] {
            let id = trash_pseudo_id(s);
            assert!(id > 0, "{s} produced a non-positive id");
            assert_eq!(id & TRASH_PSEUDO_MARKER, TRASH_PSEUDO_MARKER);
            assert_eq!(id & (1 << 61), 0, "must not collide with the files root");
            assert_eq!(id & (1 << 62), 0, "must not collide with a share root");
        }
        assert_ne!(trash_pseudo_id("1.aaaa"), trash_pseudo_id("1.bbbb"));
        assert_eq!(trash_pseudo_id("1.aaaa"), trash_pseudo_id("1.aaaa"));
    }
}
