//! `/remote.php/...` path mapping.
//!
//! `sc-dav` owns WebDAV. This module only translates the compat URL layout
//! onto the paths the core service understands, and answers the couple of
//! endpoints that have no core equivalent.

/// What a `/remote.php/...` request addresses.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DavTarget {
    /// `/remote.php/dav/` — the root collection. Clients PROPFIND it with
    /// Depth: 0 during discovery.
    Root,
    /// `/remote.php/dav/files/{user}/{path}` and the `/remote.php/webdav/`
    /// legacy alias, which map to the same place.
    Files { user: String, path: String },
    /// `/remote.php/dav/uploads/{user}` — the upload home. Only MKCOL of a
    /// child is meaningful here.
    UploadHome { user: String },
    /// `/remote.php/dav/uploads/{user}/{tid}`.
    UploadFolder { user: String, tid: String },
    /// `/remote.php/dav/uploads/{user}/{tid}/{name}`. `name == ".file"` is the
    /// virtual assembly target, which always "exists".
    UploadChunk {
        user: String,
        tid: String,
        name: String,
    },
    /// `/remote.php/dav/principals/users/{user}` — minimal stub.
    Principal { user: String },
    /// `/remote.php/dav/principals/` collection root.
    PrincipalRoot,
    /// `/remote.php/dav/trashbin/{user}/trash` — the one flat trash
    /// collection the protocol has, over however many per-share trashes the
    /// caller can reach.
    TrashRoot { user: String },
    /// `/remote.php/dav/trashbin/{user}/trash/{entry}`.
    TrashEntry { user: String, entry: String },
    /// `/remote.php/dav/trashbin/{user}/restore/{anything}` — the `MOVE`
    /// destination that means "put it back". The leaf is ignored: the core
    /// restores to the path recorded in the entry, which is what the user
    /// means.
    TrashRestore { user: String },
}

/// The virtual child of an upload folder that a MOVE targets to finish an
/// upload.
pub const FUTURE_FILE: &str = ".file";

/// Parse a `/remote.php/...` URI path.
///
/// Percent-decoding happens per path segment, after splitting, so an encoded
/// `%2F` can never introduce a segment boundary — that is the classic way a
/// path-mapping layer gets walked out of its root.
pub fn parse(uri_path: &str) -> Option<DavTarget> {
    let rest = uri_path
        .strip_prefix("/remote.php/")
        // Some deployments and clients keep the index.php prefix.
        .or_else(|| uri_path.strip_prefix("/index.php/remote.php/"))?;

    let mut segs = rest.split('/');
    let head = segs.next()?;

    match head {
        // Legacy alias for the caller's own files root.
        "webdav" => {
            let path = decode_join(segs)?;
            Some(DavTarget::Files { user: String::new(), path })
        }
        "dav" => {
            let Some(kind) = segs.next() else {
                return Some(DavTarget::Root);
            };
            match kind {
                "" => Some(DavTarget::Root),
                "files" => {
                    let user = decode_seg(segs.next()?)?;
                    if user.is_empty() {
                        return None;
                    }
                    let path = decode_join(segs)?;
                    Some(DavTarget::Files { user, path })
                }
                "uploads" => {
                    let user = decode_seg(segs.next()?)?;
                    if user.is_empty() {
                        return None;
                    }
                    match segs.next() {
                        None | Some("") => Some(DavTarget::UploadHome { user }),
                        Some(tid) => {
                            let tid = decode_seg(tid)?;
                            match segs.next() {
                                None | Some("") => {
                                    Some(DavTarget::UploadFolder { user, tid })
                                }
                                Some(name) => {
                                    // No nesting below the chunk name; the
                                    // reference forbids createDirectory here.
                                    if segs.next().is_some() {
                                        return None;
                                    }
                                    Some(DavTarget::UploadChunk {
                                        user,
                                        tid,
                                        name: decode_seg(name)?,
                                    })
                                }
                            }
                        }
                    }
                }
                "trashbin" => {
                    let user = decode_seg(segs.next()?)?;
                    if user.is_empty() {
                        return None;
                    }
                    match segs.next() {
                        // The collection above `trash` is not addressable on
                        // its own; no client asks for it.
                        None | Some("") => None,
                        Some("trash") => match segs.next() {
                            // iOS sends the same URL with a trailing slash;
                            // both forms are the same collection.
                            None | Some("") => Some(DavTarget::TrashRoot { user }),
                            Some(entry) => {
                                if segs.next().is_some_and(|s| !s.is_empty()) {
                                    return None;
                                }
                                Some(DavTarget::TrashEntry {
                                    user,
                                    entry: decode_seg(entry)?,
                                })
                            }
                        },
                        Some("restore") => Some(DavTarget::TrashRestore { user }),
                        _ => None,
                    }
                }
                "principals" => match segs.next() {
                    None | Some("") => Some(DavTarget::PrincipalRoot),
                    Some("users") => match segs.next() {
                        None | Some("") => Some(DavTarget::PrincipalRoot),
                        Some(u) => Some(DavTarget::Principal { user: decode_seg(u)? }),
                    },
                    _ => None,
                },
                _ => None,
            }
        }
        _ => None,
    }
}

fn decode_seg(s: &str) -> Option<String> {
    let d = percent_encoding::percent_decode_str(s)
        .decode_utf8()
        .ok()?
        .into_owned();
    // A decoded separator or NUL would let a crafted URL escape its segment.
    if d.contains('/') || d.contains('\0') || d.contains('\\') {
        return None;
    }
    Some(d)
}

fn decode_join<'a, I: Iterator<Item = &'a str>>(segs: I) -> Option<String> {
    let mut out: Vec<String> = Vec::new();
    for s in segs {
        if s.is_empty() {
            continue;
        }
        let d = decode_seg(s)?;
        // `.` and `..` are rejected outright rather than resolved, matching
        // `SafePath::parse`. Resolving them here would just move the escape
        // vector one layer down.
        if d == "." || d == ".." {
            return None;
        }
        out.push(d);
    }
    Some(out.join("/"))
}

/// Minimal `{DAV:}principal` document for
/// `/remote.php/dav/principals/users/{user}`.
///
/// Clients PROPFIND this during setup and treat a 404 as a broken server, but
/// nothing downstream of it matters to us: we have no calendars or address
/// books (documented non-goals), so the principal exists purely to answer
/// `principal-URL` and `displayname`.
pub fn principal_propfind_xml(user: &str, display_name: &str, href_base: &str) -> String {
    let href = format!("{href_base}/remote.php/dav/principals/users/{user}/");
    let e = |s: &str| {
        let mut o = String::new();
        crate::ocs::xml_escape_text(s, &mut o);
        o
    };
    format!(
        r#"<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
 <d:response>
  <d:href>{href}</d:href>
  <d:propstat>
   <d:prop>
    <d:resourcetype><d:principal/></d:resourcetype>
    <d:displayname>{display}</d:displayname>
    <d:principal-URL><d:href>{href}</d:href></d:principal-URL>
   </d:prop>
   <d:status>HTTP/1.1 200 OK</d:status>
  </d:propstat>
 </d:response>
</d:multistatus>
"#,
        href = e(&href),
        display = e(display_name),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn files(user: &str, path: &str) -> DavTarget {
        DavTarget::Files { user: user.into(), path: path.into() }
    }

    #[test]
    fn files_root_and_legacy_alias() {
        assert_eq!(
            parse("/remote.php/dav/files/alice/photos/a.jpg"),
            Some(files("alice", "photos/a.jpg"))
        );
        assert_eq!(
            parse("/remote.php/dav/files/alice"),
            Some(files("alice", ""))
        );
        assert_eq!(
            parse("/remote.php/dav/files/alice/"),
            Some(files("alice", ""))
        );
        // Legacy alias carries no user segment — it always means "the caller".
        assert_eq!(
            parse("/remote.php/webdav/photos/a.jpg"),
            Some(files("", "photos/a.jpg"))
        );
        assert_eq!(parse("/remote.php/webdav/"), Some(files("", "")));
        assert_eq!(parse("/remote.php/dav"), Some(DavTarget::Root));
        assert_eq!(parse("/remote.php/dav/"), Some(DavTarget::Root));
        // index.php-prefixed variant.
        assert_eq!(
            parse("/index.php/remote.php/dav/files/bob/x"),
            Some(files("bob", "x"))
        );
    }

    #[test]
    fn percent_decoding_is_per_segment() {
        assert_eq!(
            parse("/remote.php/dav/files/alice/a%20b/c%2Bd.txt"),
            Some(files("alice", "a b/c+d.txt"))
        );
        // Non-ASCII.
        assert_eq!(
            parse("/remote.php/dav/files/alice/%EC%82%AC%EC%A7%84"),
            Some(files("alice", "사진"))
        );
        // An encoded slash must NOT create a new segment.
        assert_eq!(parse("/remote.php/dav/files/alice/a%2Fb"), None);
        assert_eq!(parse("/remote.php/dav/files/a%2Fb/x"), None);
    }

    #[test]
    fn traversal_is_rejected_not_normalised() {
        assert_eq!(parse("/remote.php/dav/files/alice/../bob/secret"), None);
        assert_eq!(parse("/remote.php/dav/files/alice/%2E%2E/bob"), None);
        assert_eq!(parse("/remote.php/dav/files/alice/./x"), None);
        assert_eq!(parse("/remote.php/dav/files/alice/a%00b"), None);
    }

    #[test]
    fn upload_paths() {
        assert_eq!(
            parse("/remote.php/dav/uploads/alice"),
            Some(DavTarget::UploadHome { user: "alice".into() })
        );
        assert_eq!(
            parse("/remote.php/dav/uploads/alice/tid123"),
            Some(DavTarget::UploadFolder {
                user: "alice".into(),
                tid: "tid123".into()
            })
        );
        assert_eq!(
            parse("/remote.php/dav/uploads/alice/tid123/00001"),
            Some(DavTarget::UploadChunk {
                user: "alice".into(),
                tid: "tid123".into(),
                name: "00001".into()
            })
        );
        assert_eq!(
            parse("/remote.php/dav/uploads/alice/tid123/.file"),
            Some(DavTarget::UploadChunk {
                user: "alice".into(),
                tid: "tid123".into(),
                name: FUTURE_FILE.into()
            })
        );
        // No nesting below the chunk.
        assert_eq!(parse("/remote.php/dav/uploads/alice/tid/1/2"), None);
    }

    #[test]
    fn principals() {
        assert_eq!(
            parse("/remote.php/dav/principals/users/alice"),
            Some(DavTarget::Principal { user: "alice".into() })
        );
        assert_eq!(
            parse("/remote.php/dav/principals"),
            Some(DavTarget::PrincipalRoot)
        );
        assert_eq!(
            parse("/remote.php/dav/principals/users"),
            Some(DavTarget::PrincipalRoot)
        );
        // Non-goals: no calendar/addressbook principal collections.
        assert_eq!(parse("/remote.php/dav/calendars/alice"), None);
        assert_eq!(parse("/remote.php/dav/addressbooks/users/alice"), None);
    }

    #[test]
    fn unrelated_paths_are_not_ours() {
        assert_eq!(parse("/status.php"), None);
        assert_eq!(parse("/ocs/v2.php/cloud/user"), None);
        assert_eq!(parse("/remote.php/"), None);
    }

    #[test]
    fn principal_document_escapes_the_display_name() {
        let x = principal_propfind_xml("alice", "<script>x</script>", "https://h");
        assert!(!x.contains("<script>"));
        assert!(x.contains("&lt;script&gt;"));
        assert!(x.contains("<d:principal/>"));
        assert!(x.contains("https://h/remote.php/dav/principals/users/alice/"));
    }
}
