//! Session-folder chunked upload for the **native** mount.
//!
//! ```text
//!   MKCOL    /dav-uploads/{tid}         Destination (required),
//!                                       Upload-Length (optional)      -> 201
//!   PUT      /dav-uploads/{tid}/{n}     n numeric 1..10000            -> 201
//!   MOVE     /dav-uploads/{tid}/.file   Upload-Length, X-Mtime (opt)  -> 201 new
//!                                                                        204 overwrite
//!   DELETE   /dav-uploads/{tid}                                       -> 204
//!   PROPFIND /dav-uploads/{tid}                                       -> 207 chunk list
//!   OPTIONS  /dav-uploads/{tid}                                       -> 200 + Allow
//!   anything else                                                     -> 405
//! ```
//!
//! # Why this is not `/dav/uploads/…`
//!
//! `/dav/{*path}` maps straight onto the share tree, and axum matches literal
//! segments before a wildcard — registering `/dav/uploads/**` would permanently
//! shadow a share actually named `uploads`. The reference server has no such problem
//! because its files tree lives under its own `/remote.php/dav/files/` prefix;
//! on the native mount the root *is* the tree, so the session surface gets a
//! prefix of its own.
//!
//! # What this shares with the compat surface, and what it does not
//!
//! Both sit on the same `sc_upload::SpoolMode::NameOrdered` engine path, so
//! the assembly, the atomic publish and the GC behaviour are literally the
//! same code. The wire format is not shared: this one carries no `OC-*`
//! headers, no `{user}` path segment (the principal is the authenticated
//! user — putting a name in the path only recreates the "bob addresses
//! alice's path" case), and it survives `--no-default-features`, where the
//! whole compatibility layer is compiled out.
//!
//! # `{tid}` is attacker-controlled and is never a session key
//!
//! The client picks it. It is resolved through `sc_upload`'s `upload_alias`
//! table **scoped by user id**, which is the single thing separating
//! transfer-id guessing from session hijacking. A tid owned by someone else
//! answers 404, identically to one that never existed.

use std::sync::Arc;

use axum::extract::{Request, State};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use http::{HeaderMap, StatusCode};

use sc_upload::{SessionId, SpoolMode, UploadError};
use sc_vfs::{ShareId, UserId};
use sc_core::Vpath;

/// The mount point. A fixed literal rather than something derived from
/// `DavConfig::prefix`: the route table (`routes.rs`) is static, and the two
/// prefixes are independent URL-space facts — moving the DAV tree does not
/// have to move the session surface with it.
pub const PREFIX: &str = "/dav-uploads";

/// The sentinel name whose `MOVE` assembles and publishes the file. Same
/// spelling the reference server chose (`.file`) so a client library that already knows
/// one surface needs no new constant for the other.
const FUTURE_FILE: &str = ".file";

/// Same bound the reference implementation enforces. A client that sends
/// 10001 has a bug worth surfacing rather than papering over, and an unbounded
/// name is an unbounded spool directory.
const MIN_CHUNK_NAME: u32 = 1;
const MAX_CHUNK_NAME: u32 = 10000;

const ALLOW: &str = "OPTIONS, MKCOL, PUT, MOVE, DELETE, PROPFIND";

// ------------------------------------------------------------------ errors --

#[derive(Debug)]
enum UpErr {
    BadRequest(String),
    NotFound,
    Conflict(String),
    Forbidden,
    Gone,
    Backend(String),
}

impl UpErr {
    fn status(&self) -> StatusCode {
        match self {
            UpErr::BadRequest(_) => StatusCode::BAD_REQUEST,
            UpErr::NotFound => StatusCode::NOT_FOUND,
            UpErr::Conflict(_) => StatusCode::CONFLICT,
            UpErr::Forbidden => StatusCode::FORBIDDEN,
            UpErr::Gone => StatusCode::GONE,
            UpErr::Backend(_) => StatusCode::INTERNAL_SERVER_ERROR,
        }
    }

    fn message(&self) -> String {
        match self {
            UpErr::BadRequest(m) | UpErr::Conflict(m) | UpErr::Backend(m) => m.clone(),
            UpErr::NotFound => "not found".into(),
            UpErr::Forbidden => "forbidden".into(),
            UpErr::Gone => "upload session expired".into(),
        }
    }
}

/// An empty-bodied rejection tells a client nothing past the status line. The
/// body is for a human reading a log, not for client parsing — nothing here
/// branches on the text.
fn err_response(e: &UpErr) -> Response {
    (
        e.status(),
        [(http::header::CONTENT_TYPE, "text/plain; charset=utf-8")],
        e.message(),
    )
        .into_response()
}

impl From<UploadError> for UpErr {
    fn from(e: UploadError) -> Self {
        match e {
            UploadError::NotFound => UpErr::NotFound,
            UploadError::Gone => UpErr::Gone,
            UploadError::BadRequest(m) => UpErr::BadRequest(m),
            UploadError::Incomplete => {
                UpErr::BadRequest("upload incomplete: a chunk is missing".into())
            }
            UploadError::Unprocessable => {
                UpErr::BadRequest("chunk exceeds the declared upload length".into())
            }
            UploadError::ChunkTooSmall { min } => {
                UpErr::BadRequest(format!("chunk below the {min}-byte minimum and not the last"))
            }
            UploadError::ResourceExhausted(m) => UpErr::Conflict(m),
            UploadError::RateLimited => {
                UpErr::Conflict("session creation rate limit exceeded".into())
            }
            other => UpErr::Backend(other.to_string()),
        }
    }
}

fn core_err(e: sc_core::CoreError) -> UpErr {
    match e {
        sc_core::CoreError::NotFound => UpErr::NotFound,
        sc_core::CoreError::Denied { .. } => UpErr::Forbidden,
        sc_core::CoreError::Conflict => UpErr::Conflict("already exists".into()),
        sc_core::CoreError::InvalidPath(m) => UpErr::BadRequest(m),
        other => UpErr::Backend(other.to_string()),
    }
}

// ------------------------------------------------------------------ parsing --

/// What a request URL under [`PREFIX`] addresses.
#[derive(Debug, PartialEq, Eq)]
enum Target {
    /// `/dav-uploads/{tid}` — the session itself.
    Session(String),
    /// `/dav-uploads/{tid}/{name}` — a chunk, or the `.file` sentinel.
    Member(String, String),
}

fn parse_target(uri_path: &str) -> Option<Target> {
    let rest = uri_path
        .strip_prefix(PREFIX)?
        .trim_start_matches('/')
        .trim_end_matches('/');
    if rest.is_empty() {
        return None;
    }
    let mut parts = rest.splitn(2, '/');
    let tid = decode_segment(parts.next()?)?;
    match parts.next() {
        None => Some(Target::Session(tid)),
        // A third segment means the client addressed something that does not
        // exist in this URL space; treat it as no target at all rather than
        // silently ignoring the tail.
        Some(name) if !name.contains('/') => Some(Target::Member(tid, decode_segment(name)?)),
        Some(_) => None,
    }
}

fn decode_segment(seg: &str) -> Option<String> {
    let decoded = percent_encoding::percent_decode_str(seg).decode_utf8().ok()?;
    if decoded.contains('\0') || decoded.contains('/') {
        return None;
    }
    Some(decoded.into_owned())
}

/// `{tid}` sanity. It becomes a database key and shows up in log lines; it
/// never reaches the filesystem (that is what the alias table is for), but
/// bounding it stops a client writing megabytes into our table.
fn validate_tid(tid: &str) -> Result<(), UpErr> {
    if tid.is_empty() || tid.len() > 128 {
        return Err(UpErr::BadRequest("invalid transfer id length".into()));
    }
    if !tid
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_' || b == b'.')
    {
        return Err(UpErr::BadRequest(
            "transfer id contains invalid characters".into(),
        ));
    }
    if tid == "." || tid == ".." {
        return Err(UpErr::BadRequest("invalid transfer id".into()));
    }
    Ok(())
}

/// The chunk name is a **sort key and nothing else** — never an offset.
/// Chunk sizes are chosen by the client and need not be uniform, so name `n`
/// has no fixed position in the file; assembly is by ascending name. Leading
/// zeros are accepted and discarded, so `00007` and `7` are the same chunk.
fn parse_chunk_name(name: &str) -> Result<u32, UpErr> {
    let bad = || {
        UpErr::BadRequest(format!(
            "invalid chunk name {name:?}: must be numeric between {MIN_CHUNK_NAME} and {MAX_CHUNK_NAME}"
        ))
    };
    if name.is_empty() || !name.bytes().all(|b| b.is_ascii_digit()) {
        return Err(bad());
    }
    let trimmed = name.trim_start_matches('0');
    // Bound the length before parsing: a long run of zeros trims to something
    // sane, but a long run of digits would overflow.
    let n: u32 = if trimmed.is_empty() {
        0
    } else {
        trimmed.parse().map_err(|_| bad())?
    };
    if !(MIN_CHUNK_NAME..=MAX_CHUNK_NAME).contains(&n) {
        return Err(bad());
    }
    Ok(n)
}

fn header_str<'a>(h: &'a HeaderMap, name: &str) -> Option<&'a str> {
    h.get(name).and_then(|v| v.to_str().ok())
}

/// `Upload-Length`: the total byte length the client asserts. Optional on both
/// MKCOL and MOVE — when absent on the MOVE, the engine's own received length
/// is used, which by construction cannot be a truncation of itself. Named
/// after TUS 1.0.0's header of the same meaning, not after
/// `OC-Total-Length`.
fn upload_length(h: &HeaderMap) -> Result<Option<u64>, UpErr> {
    match header_str(h, "upload-length") {
        None => Ok(None),
        Some(v) => v
            .trim()
            .parse::<u64>()
            .map(Some)
            .map_err(|_| UpErr::BadRequest("Upload-Length is not an integer".into())),
    }
}

/// `X-Mtime`: a unix timestamp in **seconds**, applied to the published file.
/// A fractional part is truncated rather than rejected — clients that format
/// the value from a floating-point clock are otherwise unable to set an mtime
/// at all — but anything else numeric-looking is refused.
fn x_mtime_ns(h: &HeaderMap) -> Result<Option<i128>, UpErr> {
    let Some(raw) = header_str(h, "x-mtime") else {
        return Ok(None);
    };
    let v = raw.trim();
    let (int_part, frac) = match v.split_once('.') {
        Some((i, f)) => (i, Some(f)),
        None => (v, None),
    };
    let bad = || UpErr::BadRequest("X-Mtime must be a unix timestamp in seconds".into());
    // A present-but-non-numeric fraction means the whole value is not a number
    // (e.g. "1.2.3"), so it must not silently become "1".
    if let Some(f) = frac {
        if f.is_empty() || !f.bytes().all(|b| b.is_ascii_digit()) {
            return Err(bad());
        }
    }
    let secs: i64 = int_part.parse().map_err(|_| bad())?;
    Ok(Some(secs as i128 * 1_000_000_000))
}

// -------------------------------------------------------------------- state --

pub struct DavUploads {
    core: Arc<sc_core::Core>,
    uploads: Arc<sc_upload::UploadEngine>,
    /// The native DAV mount's prefix, so a `Destination` naming
    /// `/dav/{label}/{rest}` can be reduced to the `{label}/{rest}` vpath
    /// `sc_core` speaks. Read once at wiring time.
    dav_prefix: Arc<str>,
    journal: Option<Arc<crate::journal::WriteJournal>>,
}

impl DavUploads {
    /// Turn a `Destination` header into a vpath under the native DAV mount.
    ///
    /// Both forms are accepted — an absolute URL and a bare path — because
    /// which one a client sends is not something any spec pins down, and the
    /// compat surface already has to accept both for the same reason.
    fn destination_vpath(&self, h: &HeaderMap) -> Result<String, UpErr> {
        let raw = header_str(h, "destination")
            .ok_or_else(|| UpErr::BadRequest("Destination header is required".into()))?;
        let path = match raw.find("://") {
            Some(i) => match raw[i + 3..].find('/') {
                Some(j) => &raw[i + 3 + j..],
                None => "/",
            },
            None => raw,
        };
        let prefix = self.dav_prefix.as_ref();
        // An empty prefix means the DAV tree is mounted at bare `/`.
        let rest = if prefix.is_empty() {
            path
        } else if path == prefix {
            ""
        } else {
            path.strip_prefix(&format!("{prefix}/")).ok_or_else(|| {
                UpErr::BadRequest(format!(
                    "Destination {path:?} is outside the WebDAV mount at {prefix:?}"
                ))
            })?
        };
        let vpath = crate::app::decode_dav_path(rest)
            .ok_or_else(|| UpErr::BadRequest("Destination is not a usable path".into()))?;
        if vpath.is_empty() {
            return Err(UpErr::BadRequest(
                "Destination names the WebDAV root, not a file".into(),
            ));
        }
        Ok(vpath)
    }

    fn root(&self, share: ShareId) -> Result<Arc<sc_vfs::ShareRoot>, UpErr> {
        self.core.share(share).ok_or(UpErr::NotFound)
    }

    /// Resolve a transfer id to its session. Scoped by `user` — see the module
    /// header for why that is the whole point of the alias table.
    fn resolve(&self, tid: &str, user: UserId) -> Result<(SessionId, ShareId, String), UpErr> {
        validate_tid(tid)?;
        self.uploads.lookup_alias(tid, user)?.ok_or(UpErr::NotFound)
    }

    fn mkcol(&self, user: UserId, tid: &str, headers: &HeaderMap) -> Result<(), UpErr> {
        validate_tid(tid)?;
        let dest = self.destination_vpath(headers)?;
        let total = upload_length(headers)?;

        // Refuse a rebind rather than replacing: a second MKCOL on a live tid
        // would orphan the first session's spool with nothing left to address
        // it by. Checked again inside `bind_alias` (an INSERT that can lose a
        // race), so this early check is a courtesy, not the guarantee.
        if self.uploads.lookup_alias(tid, user)?.is_some() {
            return Err(UpErr::Conflict(
                "an upload session with this transfer id already exists".into(),
            ));
        }

        // WRITE-checked resolution, not `resolve`: a read-only grant must not
        // be able to open a session that then writes bytes no `fs` call of
        // its own would have been allowed to make.
        let resolved = self
            .core
            .resolve_for_upload(user, &Vpath::new(&dest))
            .map_err(core_err)?;
        let spec = sc_upload::SessionSpec {
            user,
            share: resolved.share,
            dest: resolved.path.into_safe(),
            total_len: total,
            random_access: false,
            if_match: None,
            mode: SpoolMode::NameOrdered,
            meta: sc_upload::UploadMeta {
                filename: dest.rsplit('/').next().unwrap_or("").to_string(),
                ..Default::default()
            },
        };
        let session = self.uploads.create(&resolved.root, spec)?;
        // Bind AFTER creation, so a failed create leaves no dangling alias.
        if !self
            .uploads
            .bind_alias(tid, user, session, resolved.share, &dest)?
        {
            // Lost the race against a concurrent MKCOL on the same tid. The
            // session we just made is now unaddressable, so abort it here
            // instead of leaving it for the TTL sweep.
            let _ = self.uploads.abort(session, user);
            return Err(UpErr::Conflict(
                "an upload session with this transfer id already exists".into(),
            ));
        }
        Ok(())
    }

    fn put_chunk(&self, user: UserId, tid: &str, name: &str, data: &[u8]) -> Result<(), UpErr> {
        let (session, share, _) = self.resolve(tid, user)?;
        let n = parse_chunk_name(name)?;
        let root = self.root(share)?;
        self.uploads.put_named(&root, session, user, n, data)?;
        Ok(())
    }

    /// `MOVE …/.file` — assemble in ascending chunk-name order and publish.
    ///
    /// Returns `true` when the destination did not previously exist (201) and
    /// `false` when it was overwritten (204), plus the published ETag.
    fn assemble(
        &self,
        user: UserId,
        tid: &str,
        headers: &HeaderMap,
    ) -> Result<(bool, String), UpErr> {
        let (session, share, dest) = self.resolve(tid, user)?;

        // The destination is fixed at MKCOL. A client that also sends it here
        // must agree with what it asked for then — silently honouring a
        // different one would publish the bytes somewhere the client's own
        // bookkeeping says they did not go.
        if headers.contains_key("destination") {
            let asked = self.destination_vpath(headers)?;
            if asked != dest {
                return Err(UpErr::Conflict(format!(
                    "Destination {asked:?} does not match the destination this session was opened for ({dest:?})"
                )));
            }
        }

        let mtime_ns = x_mtime_ns(headers)?;
        // Absent `Upload-Length`, fall back to what the engine actually holds:
        // that keeps the invariant "never finalise a length nobody asserted"
        // while still accepting the assertion from whichever side can make it.
        // A genuinely truncated upload is caught either way — the engine
        // rejects a mismatch against the header, and a client that gave up
        // early never sends the MOVE at all.
        let total = match upload_length(headers)? {
            Some(t) => t,
            None => self.uploads.head(session, user)?.received_bytes,
        };

        // Whether the file already exists decides 201 vs 204, and it has to be
        // read before the publish rather than after.
        let existed = self.core.stat_entry(user, &dest).is_ok();

        let root = self.root(share)?;
        // The engine's own answer, not the vpath in scope: one value for all
        // four finalize sites is one fewer way for two of them to disagree.
        let published = self
            .uploads
            .assemble_and_finalize(&root, session, user, total, mtime_ns)?;
        if let Some(j) = &self.journal {
            j.note(
                user,
                share,
                &published,
                crate::journal::WriteOp::Upload,
                crate::journal::now_ns(),
            );
        }
        // The alias dies with the session; leaving it would let the tid keep
        // addressing a freed session id.
        self.uploads.unbind_alias(tid, user)?;

        let entry = self.core.stat_entry(user, &dest).map_err(core_err)?;
        Ok((!existed, format!("\"{}\"", entry.etag)))
    }

    fn abort(&self, user: UserId, tid: &str) -> Result<(), UpErr> {
        let (session, _, _) = self.resolve(tid, user)?;
        self.uploads.abort(session, user)?;
        self.uploads.unbind_alias(tid, user)?;
        Ok(())
    }

    fn list_chunks(&self, user: UserId, tid: &str) -> Result<(Vec<u32>, u64), UpErr> {
        let (session, _, _) = self.resolve(tid, user)?;
        let mut names = self.uploads.list_chunks(session)?;
        names.sort_unstable();
        let received = self.uploads.head(session, user)?.received_bytes;
        Ok((names, received))
    }
}

/// The resume listing.
///
/// A resuming client needs two facts: which chunk names we hold, and how many
/// bytes that adds up to. The engine tracks the received total, not a
/// per-chunk table, so the total is distributed across the names — the sum is
/// exact, which is the number a client resumes from; the per-name split is
/// indicative, and the response says so rather than implying a per-chunk
/// ledger this does not keep.
fn chunk_listing_xml(href_prefix: &str, names: &[u32], received: u64) -> String {
    let mut body = String::from(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:multistatus xmlns:d=\"DAV:\">",
    );
    body.push_str(&format!(
        "<d:response><d:href>{href_prefix}/</d:href>\
         <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>\
         <d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>"
    ));
    let n = names.len() as u64;
    let each = received.checked_div(n).unwrap_or(0);
    for (i, name) in names.iter().enumerate() {
        // Remainder on the last entry, so the reported lengths sum to exactly
        // `received` rather than to `received` rounded down n times.
        let len = if i as u64 + 1 == n {
            each + (received - each * n)
        } else {
            each
        };
        body.push_str(&format!(
            "<d:response><d:href>{href_prefix}/{name}</d:href>\
             <d:propstat><d:prop>\
             <d:resourcetype/>\
             <d:getcontentlength>{len}</d:getcontentlength>\
             <d:getcontenttype>application/octet-stream</d:getcontenttype>\
             </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>"
        ));
    }
    body.push_str("</d:multistatus>");
    body
}

/// Percent-encode a path for use inside a `<d:href>`.
fn path_escape(p: &str) -> String {
    const SET: &percent_encoding::AsciiSet = &percent_encoding::CONTROLS
        .add(b' ')
        .add(b'"')
        .add(b'<')
        .add(b'>')
        .add(b'%');
    percent_encoding::utf8_percent_encode(p, SET).to_string()
}

// ------------------------------------------------------------------ routing --

pub fn router(
    core: Arc<sc_core::Core>,
    uploads: Arc<sc_upload::UploadEngine>,
    dav: &Arc<sc_dav::DavService>,
    journal: Option<Arc<crate::journal::WriteJournal>>,
) -> Router {
    let state = Arc::new(DavUploads {
        core,
        uploads,
        dav_prefix: Arc::from(dav.config().prefix.trim_end_matches('/')),
        journal,
    });
    Router::new()
        .route(&format!("{PREFIX}/{{*rest}}"), any(handle))
        .with_state(state)
}

async fn handle(State(s): State<Arc<DavUploads>>, req: Request) -> Response {
    let method = req.method().clone();
    let headers = req.headers().clone();
    let path = req.uri().path().to_string();

    let Some(target) = parse_target(&path) else {
        return StatusCode::NOT_FOUND.into_response();
    };

    // OPTIONS is answered before authentication for the same reason `sc-dav`
    // does: it advertises what the surface supports and reveals nothing about
    // whether any particular session exists.
    if method == http::Method::OPTIONS {
        return (StatusCode::OK, [(http::header::ALLOW, ALLOW)]).into_response();
    }

    let Some(sc_dav::DavPrincipal(user)) = req.extensions().get::<sc_dav::DavPrincipal>().copied()
    else {
        return StatusCode::UNAUTHORIZED.into_response();
    };

    match (method.as_str(), target) {
        ("MKCOL", Target::Session(tid)) => match s.mkcol(user, &tid, &headers) {
            Ok(()) => StatusCode::CREATED.into_response(),
            Err(e) => err_response(&e),
        },
        ("PUT", Target::Member(tid, name)) => {
            let body = match axum::body::to_bytes(req.into_body(), usize::MAX).await {
                Ok(b) => b,
                Err(_) => return StatusCode::BAD_REQUEST.into_response(),
            };
            match s.put_chunk(user, &tid, &name, &body) {
                Ok(()) => StatusCode::CREATED.into_response(),
                Err(e) => err_response(&e),
            }
        }
        ("MOVE", Target::Member(tid, name)) if name == FUTURE_FILE => {
            match s.assemble(user, &tid, &headers) {
                Ok((created, etag)) => {
                    let code = if created {
                        StatusCode::CREATED
                    } else {
                        StatusCode::NO_CONTENT
                    };
                    let mut resp = code.into_response();
                    if let Ok(v) = http::HeaderValue::from_str(&etag) {
                        resp.headers_mut().insert(http::header::ETAG, v);
                    }
                    resp
                }
                Err(e) => err_response(&e),
            }
        }
        ("DELETE", Target::Session(tid)) => match s.abort(user, &tid) {
            Ok(()) => StatusCode::NO_CONTENT.into_response(),
            Err(e) => err_response(&e),
        },
        ("PROPFIND", Target::Session(tid)) => match s.list_chunks(user, &tid) {
            Ok((names, received)) => {
                let prefix = path_escape(&format!("{PREFIX}/{tid}"));
                let body = chunk_listing_xml(&prefix, &names, received);
                (
                    StatusCode::MULTI_STATUS,
                    [(http::header::CONTENT_TYPE, "application/xml; charset=utf-8")],
                    body,
                )
                    .into_response()
            }
            Err(e) => err_response(&e),
        },
        _ => (StatusCode::METHOD_NOT_ALLOWED, [(http::header::ALLOW, ALLOW)]).into_response(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn target_parsing() {
        assert_eq!(
            parse_target("/dav-uploads/abc"),
            Some(Target::Session("abc".into()))
        );
        assert_eq!(
            parse_target("/dav-uploads/abc/"),
            Some(Target::Session("abc".into()))
        );
        assert_eq!(
            parse_target("/dav-uploads/abc/00007"),
            Some(Target::Member("abc".into(), "00007".into()))
        );
        assert_eq!(
            parse_target("/dav-uploads/abc/.file"),
            Some(Target::Member("abc".into(), ".file".into()))
        );
        // Nothing addressed, and nothing deeper than a member.
        assert_eq!(parse_target("/dav-uploads"), None);
        assert_eq!(parse_target("/dav-uploads/"), None);
        assert_eq!(parse_target("/dav-uploads/a/b/c"), None);
        assert_eq!(parse_target("/dav/abc"), None);
    }

    /// Zero-padding is a client convention, not a distinct chunk.
    #[test]
    fn chunk_names_are_sort_keys_with_padding_ignored() {
        assert_eq!(parse_chunk_name("1").unwrap(), 1);
        assert_eq!(parse_chunk_name("00001").unwrap(), 1);
        assert_eq!(parse_chunk_name("10000").unwrap(), 10000);
        for bad in ["", "0", "00000", "10001", "-1", "1a", ".file", "99999999999999"] {
            assert!(parse_chunk_name(bad).is_err(), "{bad:?} should be rejected");
        }
    }

    #[test]
    fn tid_validation_rejects_path_shapes_and_overlong_ids() {
        assert!(validate_tid("a-b_c.1").is_ok());
        for bad in ["", ".", "..", "a/b", "a b", "a\0b", &"x".repeat(129)] {
            assert!(validate_tid(bad).is_err(), "{bad:?} should be rejected");
        }
    }

    #[test]
    fn x_mtime_accepts_a_fractional_value_and_rejects_nonsense() {
        let mut h = HeaderMap::new();
        assert_eq!(x_mtime_ns(&h).unwrap(), None);
        h.insert("x-mtime", "1751234567".parse().unwrap());
        assert_eq!(x_mtime_ns(&h).unwrap(), Some(1_751_234_567_000_000_000));
        h.insert("x-mtime", "1751234567.891".parse().unwrap());
        assert_eq!(x_mtime_ns(&h).unwrap(), Some(1_751_234_567_000_000_000));
        for bad in ["", "abc", "1.2.3", "1e9", "1."] {
            h.insert("x-mtime", bad.parse().unwrap());
            assert!(x_mtime_ns(&h).is_err(), "{bad:?} should be rejected");
        }
    }

    /// The reported per-chunk lengths must sum to exactly the received total —
    /// a resuming client adds them up to find its next byte.
    #[test]
    fn listing_lengths_sum_to_the_received_total() {
        let xml = chunk_listing_xml("/dav-uploads/t", &[1, 2, 3], 10);
        let sum: u64 = xml
            .split("<d:getcontentlength>")
            .skip(1)
            .filter_map(|s| s.split('<').next())
            .filter_map(|s| s.parse::<u64>().ok())
            .sum();
        assert_eq!(sum, 10);
    }
}
