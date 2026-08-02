//! Compat chunked upload v2, mapped onto `sc-upload`.
//!
//! Reference:
//! - server: `apps/dav/lib/Upload/{ChunkingV2Plugin,ChunkingPlugin,AssemblyStream,
//!   FutureFile,UploadFolder,UploadHome}.php`
//! - client: the desktop client's `src/libsync/propagateuploadng.cpp`
//!
//! ```text
//!   MKCOL  /remote.php/dav/uploads/{user}/{tid}          -> create session
//!          headers: Destination, OC-Total-Length          -> 201 (exactly)
//!   PUT    /remote.php/dav/uploads/{user}/{tid}/{name}    -> put_named
//!          name is numeric 1..10000, zero-padded to 5     -> 201
//!   MOVE   /remote.php/dav/uploads/{user}/{tid}/.file     -> assemble+finalize
//!          headers: Destination, OC-Total-Length, X-OC-Mtime
//!                                          -> 201 new / 204 overwrite
//!   DELETE /remote.php/dav/uploads/{user}/{tid}           -> abort
//!   PROPFIND on the folder                                -> chunk list (resume)
//!   GET on anything under it                              -> 405
//! ```
//!
//! # Two things here are load-bearing and easy to get wrong
//!
//! 1. **`{tid}` is attacker-controlled and is never a session key.** The client
//!    picks it (`Utility::rand() ^ modtime ^ (size << 16) ^ qHash(path)`), so it
//!    is guessable and collidable. It is resolved through `nc_upload_alias`
//!    **scoped by user id**, so one account cannot name its way into another
//!    account's in-flight upload.
//! 2. **The MOVE response must carry `OC-FileId` and an ETag.** The desktop
//!    client hard-fails the item without them even on a 201:
//!    "Missing File ID from server" / "File is not accessible on the server."

use std::sync::Arc;

use axum::http::{HeaderMap, StatusCode};

use crate::ports::{
    Entry, PortError, SessionSpec, SessionId, ShareId, SpoolMode, UploadEngine, UserId,
};
use crate::store::NcStore;

/// The reference server's own bound: `ChunkingV2Plugin` rejects anything outside 1..=10000
/// with a 400. We keep it, because a client that sends 10001 has a bug we would
/// rather surface than paper over.
pub const MIN_CHUNK_NAME: u32 = 1;
pub const MAX_CHUNK_NAME: u32 = 10000;

#[derive(Debug, thiserror::Error)]
pub enum ChunkError {
    #[error("{0}")]
    BadRequest(String),
    #[error("not found")]
    NotFound,
    #[error("conflict: {0}")]
    Conflict(String),
    #[error("forbidden")]
    Forbidden,
    #[error("{0}")]
    Backend(String),
}

impl ChunkError {
    pub fn status(&self) -> StatusCode {
        match self {
            ChunkError::BadRequest(_) => StatusCode::BAD_REQUEST,
            ChunkError::NotFound => StatusCode::NOT_FOUND,
            ChunkError::Conflict(_) => StatusCode::CONFLICT,
            ChunkError::Forbidden => StatusCode::FORBIDDEN,
            ChunkError::Backend(_) => StatusCode::INTERNAL_SERVER_ERROR,
        }
    }
}

impl From<PortError> for ChunkError {
    fn from(e: PortError) -> Self {
        match e {
            PortError::NotFound => ChunkError::NotFound,
            PortError::Forbidden => ChunkError::Forbidden,
            PortError::Invalid(m) => ChunkError::BadRequest(m),
            PortError::Conflict(m) => ChunkError::Conflict(m),
            PortError::Backend(m) => ChunkError::Backend(m),
        }
    }
}

/// Parse the chunk file name.
///
/// **The value is a sort key and nothing else.** The desktop client zero-pads
/// to five digits (`"%1".arg(chunk, 5, 10, '0')` → `00001`) because the server
/// sorts with `strnatcmp`, but other clients and rclone do not pad. `is_numeric`
/// in PHP accepts `"00001"` and casts it to `1`, so the two forms are the same
/// chunk. We do the same: parse to an integer, discard the textual form, and
/// never derive an offset from it (chunk sizes are dynamic — the desktop client
/// resizes every chunk based on the previous one's upload duration, so chunk N
/// has no fixed position).
pub fn parse_chunk_name(name: &str) -> Result<u32, ChunkError> {
    if name.is_empty() || !name.bytes().all(|b| b.is_ascii_digit()) {
        return Err(ChunkError::BadRequest(format!(
            "Invalid chunk name {name:?}, must be numeric between {MIN_CHUNK_NAME} and {MAX_CHUNK_NAME}"
        )));
    }
    // Leading zeros are fine; an absurdly long run of them is not (it would
    // overflow the parse), so bound the length first.
    let trimmed = name.trim_start_matches('0');
    let n: u32 = if trimmed.is_empty() {
        0
    } else {
        trimmed
            .parse()
            .map_err(|_| ChunkError::BadRequest("Invalid chunk name, out of range".into()))?
    };
    if !(MIN_CHUNK_NAME..=MAX_CHUNK_NAME).contains(&n) {
        return Err(ChunkError::BadRequest(format!(
            "Invalid chunk name, must be numeric between {MIN_CHUNK_NAME} and {MAX_CHUNK_NAME}"
        )));
    }
    Ok(n)
}

fn header_str<'a>(h: &'a HeaderMap, name: &str) -> Option<&'a str> {
    h.get(name).and_then(|v| v.to_str().ok())
}

/// `OC-Total-Length`, when present.
pub fn total_length(h: &HeaderMap) -> Result<Option<u64>, ChunkError> {
    match header_str(h, "oc-total-length") {
        None => Ok(None),
        Some(v) => v
            .trim()
            .parse::<u64>()
            .map(Some)
            .map_err(|_| ChunkError::BadRequest("OC-Total-Length is not an integer".into())),
    }
}

/// `X-OC-Mtime`, a **unix timestamp in seconds**, converted to our nanosecond
/// representation.
///
/// The reference rejects a non-numeric value
/// (`sanitizeMtime` throws `InvalidArgumentException`). We answer 400 rather
/// than the reference's accidental 500.
///
/// # Why a fractional value has to be accepted
///
/// The iOS client formats this header as a Swift `Double`:
///
/// ```text
/// the iOS SDK's +Upload.swift:401-406
///     options.customHeader?["X-OC-CTime"] = "\(creationDate.timeIntervalSince1970)"
///     options.customHeader?["X-OC-MTime"] = "\(date.timeIntervalSince1970)"
/// ```
///
/// which renders as e.g. `1751234567.891234`, not `1751234567`. Rejecting it
/// as "not an integer" fails the final `MOVE` of **every** iOS chunked upload
/// with a 400. Android and the desktop client both send a bare integer.
///
/// We therefore truncate at the decimal point (sub-second precision is not
/// something a client can meaningfully assert about a file it just read from a
/// camera roll) and still reject anything that is not otherwise numeric.
pub fn oc_mtime_ns(h: &HeaderMap) -> Result<Option<i128>, ChunkError> {
    match header_str(h, "x-oc-mtime") {
        None => Ok(None),
        Some(v) => Ok(Some(parse_unix_seconds(v, "X-OC-Mtime")? as i128 * 1_000_000_000)),
    }
}

/// Shared parser for the `X-OC-*time` headers. Accepts `123`, `123.456` and
/// `-123.456`; rejects `""`, `abc`, `1.2.3` and `1e9`.
fn parse_unix_seconds(raw: &str, header: &str) -> Result<i64, ChunkError> {
    let v = raw.trim();
    // Truncate toward zero at the decimal point rather than rounding: the
    // integral part is the value every other client would have sent.
    let (int_part, frac) = match v.split_once('.') {
        Some((i, f)) => (i, Some(f)),
        None => (v, None),
    };
    // A present-but-non-numeric fraction means the value is not a number at
    // all (e.g. "1.2.3"), so it must not silently become "1".
    if let Some(f) = frac {
        if f.is_empty() || !f.bytes().all(|b| b.is_ascii_digit()) {
            return Err(ChunkError::BadRequest(format!(
                "{header} header must be a unix timestamp in seconds"
            )));
        }
    }
    int_part.parse::<i64>().map_err(|_| {
        ChunkError::BadRequest(format!(
            "{header} header must be a unix timestamp in seconds"
        ))
    })
}

/// Turn a `Destination` header into a share-relative path.
///
/// The client sends either an absolute URL (`MKCOL`, from
/// `PropagateUploadFileNG::destinationHeader`) or a bare path (`MOVE`, from
/// `QDir::cleanPath(davUrl().path() + remotePath)`), so both must be accepted.
pub fn destination_path(h: &HeaderMap, dav_user: &str) -> Result<String, ChunkError> {
    let raw = header_str(h, "destination")
        .ok_or_else(|| ChunkError::BadRequest("Destination header is required".into()))?;

    // Strip scheme://host if present.
    let path = match raw.find("://") {
        Some(i) => match raw[i + 3..].find('/') {
            Some(j) => &raw[i + 3 + j..],
            None => "/",
        },
        None => raw,
    };
    let decoded = percent_encoding::percent_decode_str(path)
        .decode_utf8()
        .map_err(|_| ChunkError::BadRequest("Destination is not valid UTF-8".into()))?
        .into_owned();

    // Accept both DAV roots.
    let files_prefix = format!("/remote.php/dav/files/{dav_user}/");
    let rel = if let Some(r) = decoded.strip_prefix(&files_prefix) {
        r
    } else if let Some(r) = decoded.strip_prefix("/remote.php/webdav/") {
        r
    } else if decoded == files_prefix.trim_end_matches('/')
        || decoded == "/remote.php/webdav"
    {
        ""
    } else {
        return Err(ChunkError::BadRequest(format!(
            "Destination {decoded:?} is outside this user's WebDAV root"
        )));
    };
    Ok(rel.trim_start_matches('/').to_string())
}

/// Outcome of the final MOVE, so the route layer can build the response the
/// desktop client insists on.
#[derive(Clone, Debug)]
pub struct AssembleResult {
    /// 201 when the destination did not exist, 204 when it was overwritten.
    pub created: bool,
    /// `OC-FileId` — the `{:08}{instance}` form, not the bare integer.
    pub oc_file_id: String,
    /// Sent as both `ETag` and `OC-ETag`, quoted.
    pub etag: String,
    /// Whether we honoured `X-OC-Mtime` (drives `X-OC-MTime: accepted`).
    pub mtime_accepted: bool,
}

pub struct ChunkedUploads {
    store: Arc<dyn NcStore>,
    engine: Arc<dyn UploadEngine>,
    instance_id: String,
}

impl ChunkedUploads {
    pub fn new(
        store: Arc<dyn NcStore>,
        engine: Arc<dyn UploadEngine>,
        instance_id: String,
    ) -> Self {
        Self { store, engine, instance_id }
    }

    /// `MKCOL /remote.php/dav/uploads/{user}/{tid}`.
    ///
    /// Must answer **exactly 201**; the desktop client compares
    /// `_httpErrorCode != 201` and aborts the whole transfer otherwise.
    ///
    /// Returns the `ShareId` the engine resolved `dest` to, alongside the
    /// session id, so the caller can bind both to `tid` — every later
    /// PUT/MOVE/DELETE/PROPFIND on this `tid` reuses this exact share instead
    /// of recomputing `home_root()`, which need not agree with it.
    pub fn mkcol(
        &self,
        user: UserId,
        tid: &str,
        dest: String,
        total_len: Option<u64>,
        now_ns: i64,
    ) -> Result<(ShareId, SessionId), ChunkError> {
        validate_tid(tid)?;

        // Reject a rebind outright rather than silently replacing: a second
        // MKCOL on a live tid would orphan the first session's spool.
        if self.store.lookup_upload(tid, user)?.is_some() {
            return Err(ChunkError::Conflict(
                "an upload session with this transfer id already exists".into(),
            ));
        }

        let (share, sid) = self.engine.create(SessionSpec {
            mode: SpoolMode::NameOrdered,
            dest: dest.clone(),
            owner: user,
            total_len,
        })?;
        // Bind AFTER creation so a failed create leaves no dangling alias.
        self.store.bind_upload(tid, user, share, sid, &dest, now_ns)?;
        Ok((share, sid))
    }

    /// Resolve a client-chosen transfer id to the binding `MKCOL` recorded.
    ///
    /// Scoped by `user`: this is the single place that stops transfer-id
    /// guessing from becoming session hijacking. A tid that belongs to someone
    /// else is reported as `NotFound`, identically to one that never existed,
    /// so it is not an existence oracle either.
    fn resolve(&self, tid: &str, user: UserId) -> Result<crate::store::UploadBinding, ChunkError> {
        self.store
            .lookup_upload(tid, user)?
            .ok_or(ChunkError::NotFound)
    }

    /// `PUT /remote.php/dav/uploads/{user}/{tid}/{name}` -> 201.
    pub fn put_chunk(
        &self,
        user: UserId,
        tid: &str,
        name: &str,
        data: &[u8],
    ) -> Result<(), ChunkError> {
        let binding = self.resolve(tid, user)?;
        let n = parse_chunk_name(name)?;
        self.engine.put_named(binding.share, binding.session, user, n, data)?;
        Ok(())
    }

    /// `MOVE /remote.php/dav/uploads/{user}/{tid}/.file`.
    ///
    /// `finished` receives the share and share-relative rest-path the
    /// original `MKCOL`'s `Destination` resolved to, so the caller can stat
    /// the file that was actually just assembled instead of the share root.
    pub fn assemble(
        &self,
        user: UserId,
        tid: &str,
        headers: &HeaderMap,
        destination_exists: bool,
        finished: impl FnOnce(ShareId, &str) -> Result<Entry, PortError>,
    ) -> Result<AssembleResult, ChunkError> {
        let binding = self.resolve(tid, user)?;

        // `OC-Total-Length` is optional in the reference (null => skip the
        // check), and it is optional here too — but it did not used to be, and
        // that was a total-breakage bug for one of the two mobile clients.
        //
        // | client  | sends OC-Total-Length on the final MOVE? |
        // |---------|------------------------------------------|
        // | desktop | yes                                      |
        // | iOS     | yes (it rides along in `options.customHeader` for the whole flow, `the iOS SDK's +Upload.swift:235-237`) |
        // | Android | **no** — `ChunkedFileUploadRemoteOperation.java:216-225` sets only `X-OC-Mtime` and, optionally, `X-OC-Ctime`/`e2e-token` |
        //
        // Requiring it therefore failed *every* Android chunked upload (i.e.
        // every file over 10,240,000 bytes) with a 400 at the assembly step,
        // after all the bytes had already been transferred.
        //
        // The original reasoning for requiring it — "without it a truncated
        // upload finalises silently as a corrupt file" — is still sound, so we
        // do not simply drop the check. Instead we fall back to the length the
        // engine actually holds. That keeps the invariant that we never
        // finalise a length nobody asserted, while accepting the assertion
        // from whichever side is able to make it:
        //
        //   * header present -> the client's number wins, and the engine still
        //     rejects a mismatch against what it received (400).
        //   * header absent  -> the engine's own contiguous received length,
        //     which by construction cannot be a truncation of itself.
        //
        // A genuinely truncated Android upload is still caught, just one layer
        // up: the client aborts before sending the MOVE at all, so no MOVE
        // arrives and nothing is finalised.
        let mtime_ns = oc_mtime_ns(headers)?;
        let total = match total_length(headers)? {
            Some(t) => t,
            None => self.engine.received_len(binding.session, user)?,
        };

        self.engine
            .assemble_and_finalize(binding.share, binding.session, user, total, mtime_ns)?;
        // The alias dies with the session; leaving it would let the tid be
        // reused to address a freed session id.
        self.store.unbind_upload(tid, user)?;

        // `binding.dest` is the full vpath (`{label}/{rest}`) captured at
        // MKCOL time; strip the label to get the share-relative path
        // `stat_entry` needs (`Core::resolve_want` re-adds the grant's
        // `subpath` itself, so this must stay the *rest* half only).
        let rest = binding.dest.split_once('/').map(|x| x.1).unwrap_or("");
        let e = finished(binding.share, rest)?;
        Ok(AssembleResult {
            created: !destination_exists,
            oc_file_id: crate::props::nc_id(e.id.unwrap_or(sc_vfs::FileId(0)), &self.instance_id),
            etag: format!("\"{}\"", e.etag),
            mtime_accepted: mtime_ns.is_some(),
        })
    }

    /// `DELETE /remote.php/dav/uploads/{user}/{tid}` -> abort.
    pub fn abort(&self, user: UserId, tid: &str) -> Result<(), ChunkError> {
        let binding = self.resolve(tid, user)?;
        self.engine.abort(binding.session, user)?;
        self.store.unbind_upload(tid, user)?;
        Ok(())
    }

    /// `PROPFIND /remote.php/dav/uploads/{user}/{tid}` -> received chunks.
    ///
    /// The desktop client asks only for `{DAV:}resourcetype` and
    /// `{DAV:}getcontentlength`, parses the last path segment as an integer,
    /// then walks 1,2,3,… until it finds a gap and resumes there — deleting any
    /// chunks after the gap. So the list has to be complete and honest;
    /// reporting a chunk we do not have makes the client skip it and produce a
    /// corrupt assembly.
    pub fn list_chunks(&self, user: UserId, tid: &str) -> Result<Vec<u32>, ChunkError> {
        let binding = self.resolve(tid, user)?;
        let mut v = self.engine.list_chunks(binding.session)?;
        v.sort_unstable();
        Ok(v)
    }

    /// The same listing, plus the byte length to report for each chunk.
    ///
    /// # Why lengths are not optional
    ///
    /// This is the single most dangerous response in the mobile surface,
    /// because getting it wrong corrupts data instead of failing.
    ///
    /// Android drives *every* chunked upload through this endpoint — not just
    /// resumes. `ChunkedFileUploadRemoteOperation.run()` does MKCOL, then this
    /// PROPFIND, and derives its starting offset purely from the response:
    ///
    /// ```text
    /// ChunkedFileUploadRemoteOperation.java:178-194
    ///     long nextByte = 0;
    ///     int  lastId   = 0;
    ///     for (MultiStatusResponse response : dataInServer.getResponses()) {
    ///         …
    ///         if (id > lastId) { lastId = id; }
    ///         nextByte += we.getContentLength();
    ///     }
    /// ```
    ///
    /// `getContentLength()` is `0` unless `d:getcontentlength` was present. So
    /// a listing that carries names but no lengths makes a resuming Android
    /// client restart the *byte stream* at 0 while continuing the *chunk
    /// numbering* from `lastId + 1` — the assembled file is then the tail of
    /// the original prefixed by a duplicate of its own beginning. Silently.
    ///
    /// # What the lengths are derived from
    ///
    /// The engine exposes the contiguous received prefix, not a per-chunk
    /// table, so we distribute that total across the chunk names. Both clients
    /// and the desktop client use only the **sum** (Android above; the desktop
    /// client likewise accumulates into `_sent`) and the **maximum name**, so
    /// an exact total with a plausible split is behaviourally identical to a
    /// per-chunk table while remaining honest about the one number anybody
    /// reads. Reporting exact per-chunk sizes would need a small addition to
    /// `sc_upload::UploadEngine`; see
    pub fn list_chunks_sized(
        &self,
        user: UserId,
        tid: &str,
    ) -> Result<Vec<(u32, u64)>, ChunkError> {
        let binding = self.resolve(tid, user)?;
        let mut names = self.engine.list_chunks(binding.session)?;
        names.sort_unstable();
        let received = self.engine.received_len(binding.session, user)?;
        Ok(distribute(&names, received))
    }
}

/// Split `total` across `names`, remainder on the last entry, so the reported
/// lengths sum to exactly `total`.
fn distribute(names: &[u32], total: u64) -> Vec<(u32, u64)> {
    let n = names.len() as u64;
    if n == 0 {
        return Vec::new();
    }
    let each = total / n;
    let mut out: Vec<(u32, u64)> = names.iter().map(|&k| (k, each)).collect();
    if let Some(last) = out.last_mut() {
        last.1 += total - each * n;
    }
    out
}

/// Render the chunk listing as a WebDAV `multistatus`.
///
/// # The `d:` prefix is load-bearing
///
/// It is lowercase on purpose. The iOS client parses WebDAV XML with
/// **literal, namespace-unaware element names** — `NKDataFileXML.swift:287`
/// is `xml["d:multistatus", "d:response"]`, with no `shouldProcessNamespaces`
/// anywhere in the library — so `<D:multistatus>` matches nothing and the
/// client sees an empty collection while still reporting success. Sabre (and
/// therefore every server built on it) emits `d:`, which is why the client
/// can get away with hardcoding it.
///
/// `href` must also carry the full `/remote.php/dav/uploads/...` path: the
/// Android parser splits the href on that literal prefix and indexes `[1]`
/// (`WebdavEntry.kt:118`), which throws — failing the whole upload — if the
/// prefix is absent.
pub fn chunk_listing_xml(href_prefix: &str, chunks: &[(u32, u64)]) -> String {
    let mut body = String::from(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:multistatus xmlns:d=\"DAV:\">",
    );
    // The collection itself, Depth:1 style. Android filters it out by name
    // (the tid is neither short enough nor digits-only) and the desktop client
    // by `resourcetype`, but omitting it entirely makes the response an odd
    // shape for anything that expects the requested resource to be present.
    body.push_str(&format!(
        "<d:response><d:href>{href_prefix}/</d:href>\
         <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>\
         <d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>"
    ));
    for (name, len) in chunks {
        // `d:getcontentlength` MUST be a non-empty integer element: Android
        // does `(prop.value as String).toLong()` with no null guard, so an
        // empty `<d:getcontentlength/>` is a Kotlin cast NPE that aborts the
        // entire folder parse rather than defaulting to zero.
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

/// `{tid}` sanity. It becomes a database key and appears in log lines; it never
/// touches the filesystem (that is what the alias table is for), but bounding
/// it keeps a client from writing megabytes into our table.
fn validate_tid(tid: &str) -> Result<(), ChunkError> {
    if tid.is_empty() || tid.len() > 128 {
        return Err(ChunkError::BadRequest("invalid transfer id length".into()));
    }
    if !tid
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_' || b == b'.')
    {
        return Err(ChunkError::BadRequest(
            "transfer id contains invalid characters".into(),
        ));
    }
    // `.` and `..` would be path-ish if anyone ever forgot the alias table.
    if tid == "." || tid == ".." {
        return Err(ChunkError::BadRequest("invalid transfer id".into()));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ports::{FileId, Kind, PortResult, Perms, ShareId};
    use crate::store::MemStore;
    use parking_lot::Mutex;
    use std::collections::BTreeMap;

    /// Minimal in-memory `UploadEngine` that actually assembles bytes, so the
    /// ordering test proves something.
    #[derive(Default)]
    struct FakeEngine {
        next: Mutex<u64>,
        sessions: Mutex<BTreeMap<[u8; 16], FakeSession>>,
    }

    #[derive(Default, Clone)]
    struct FakeSession {
        owner: u32,
        chunks: BTreeMap<u32, Vec<u8>>,
        assembled: Option<Vec<u8>>,
        mtime_ns: Option<i128>,
        aborted: bool,
    }

    impl FakeEngine {
        fn assembled(&self, sid: SessionId) -> Option<Vec<u8>> {
            self.sessions.lock().get(&sid.0).and_then(|s| s.assembled.clone())
        }
    }

    impl UploadEngine for FakeEngine {
        fn create(&self, spec: SessionSpec) -> PortResult<(ShareId, SessionId)> {
            let mut n = self.next.lock();
            *n += 1;
            let mut raw = [0u8; 16];
            raw[..8].copy_from_slice(&n.to_le_bytes());
            self.sessions.lock().insert(
                raw,
                FakeSession { owner: spec.owner.0, ..Default::default() },
            );
            // The real engine resolves `spec.dest`'s grant label; this fake
            // has no grants to resolve against, so it always lands on the
            // same fixed share — nothing here needs a second one.
            Ok((ShareId(1), SessionId(raw)))
        }
        fn put_named(
            &self,
            _share: ShareId,
            s: SessionId,
            user: UserId,
            name: u32,
            data: &[u8],
        ) -> PortResult<()> {
            let mut g = self.sessions.lock();
            let sess = g.get_mut(&s.0).ok_or(PortError::NotFound)?;
            if sess.owner != user.0 {
                return Err(PortError::Forbidden);
            }
            sess.chunks.insert(name, data.to_vec());
            Ok(())
        }
        fn assemble_and_finalize(
            &self,
            _share: ShareId,
            s: SessionId,
            user: UserId,
            total: u64,
            mtime_ns: Option<i128>,
        ) -> PortResult<()> {
            let mut g = self.sessions.lock();
            let sess = g.get_mut(&s.0).ok_or(PortError::NotFound)?;
            if sess.owner != user.0 {
                return Err(PortError::Forbidden);
            }
            // BTreeMap iterates in ascending key order — "assembled in the
            // order of their names".
            let out: Vec<u8> = sess.chunks.values().flatten().copied().collect();
            if out.len() as u64 != total {
                return Err(PortError::Invalid(format!(
                    "OC-Total-Length mismatch: expected {total}, have {}",
                    out.len()
                )));
            }
            sess.assembled = Some(out);
            sess.mtime_ns = mtime_ns;
            Ok(())
        }
        fn list_chunks(&self, s: SessionId) -> PortResult<Vec<u32>> {
            Ok(self
                .sessions
                .lock()
                .get(&s.0)
                .map(|s| s.chunks.keys().copied().collect())
                .unwrap_or_default())
        }
        fn received_len(&self, s: SessionId, _user: UserId) -> PortResult<u64> {
            Ok(self
                .sessions
                .lock()
                .get(&s.0)
                .map(|s| s.chunks.values().map(|c| c.len() as u64).sum())
                .unwrap_or(0))
        }
        fn abort(&self, s: SessionId, user: UserId) -> PortResult<()> {
            let mut g = self.sessions.lock();
            let sess = g.get_mut(&s.0).ok_or(PortError::NotFound)?;
            if sess.owner != user.0 {
                return Err(PortError::Forbidden);
            }
            sess.aborted = true;
            Ok(())
        }
    }

    fn setup() -> (ChunkedUploads, Arc<FakeEngine>) {
        let engine = Arc::new(FakeEngine::default());
        let cu = ChunkedUploads::new(
            Arc::new(MemStore::with_instance_id("ocTESTINST")),
            engine.clone(),
            "ocTESTINST".into(),
        );
        (cu, engine)
    }

    fn entry() -> Entry {
        Entry {
            name: "big.bin".into(),
            kind: Kind::File,
            size: 9,
            mtime_ns: 0,
            etag: "abc123".into(),
            perms: Perms::all(),
            id: Some(FileId(4242)),
            is_symlink_denied: false,
            confusable: false,
            btime_ns: None,
        }
    }

    fn move_headers(total: u64, mtime: Option<i64>) -> HeaderMap {
        let mut h = HeaderMap::new();
        h.insert("OC-Total-Length", total.to_string().parse().unwrap());
        if let Some(m) = mtime {
            h.insert("X-OC-Mtime", m.to_string().parse().unwrap());
        }
        h
    }

    #[test]
    fn chunk_names_parse_padded_and_unpadded_identically() {
        assert_eq!(parse_chunk_name("1").unwrap(), 1);
        assert_eq!(parse_chunk_name("00001").unwrap(), 1);
        assert_eq!(parse_chunk_name("00042").unwrap(), 42);
        assert_eq!(parse_chunk_name("10000").unwrap(), 10000);

        // Out of the reference's 1..=10000 range.
        assert!(parse_chunk_name("0").is_err());
        assert!(parse_chunk_name("00000").is_err());
        assert!(parse_chunk_name("10001").is_err());
        // Not a sort key at all.
        assert!(parse_chunk_name(".file").is_err());
        assert!(parse_chunk_name("").is_err());
        assert!(parse_chunk_name("-1").is_err());
        assert!(parse_chunk_name("1a").is_err());
        assert!(parse_chunk_name("99999999999999999999").is_err());
    }

    #[test]
    fn out_of_order_puts_assemble_in_name_order() {
        let (cu, engine) = setup();
        let u = UserId(1);
        let (_share, sid) = cu
            .mkcol(u, "tid-abc", "big.bin".into(), Some(9), 0)
            .unwrap();

        // Arrive scrambled, with mixed padding, exactly as parallel uploads do.
        cu.put_chunk(u, "tid-abc", "00003", b"GHI").unwrap();
        cu.put_chunk(u, "tid-abc", "1", b"ABC").unwrap();
        cu.put_chunk(u, "tid-abc", "00002", b"DEF").unwrap();

        let res = cu
            .assemble(
                u,
                "tid-abc",
                &move_headers(9, Some(1_700_000_000)),
                false,
                |_s, _p| Ok(entry()),
            )
            .unwrap();

        assert_eq!(engine.assembled(sid).unwrap(), b"ABCDEFGHI");
        assert!(res.created);
        assert!(res.mtime_accepted);
        assert_eq!(res.oc_file_id, "00004242ocTESTINST");
        assert_eq!(res.etag, "\"abc123\"", "ETag must be quoted");
    }

    #[test]
    fn overwriting_an_existing_destination_reports_204_not_201() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), Some(3), 0).unwrap();
        cu.put_chunk(u, "tid", "1", b"abc").unwrap();
        let r = cu
            .assemble(u, "tid", &move_headers(3, None), true, |_s, _p| Ok(entry()))
            .unwrap();
        assert!(!r.created);
        assert!(!r.mtime_accepted);
    }

    /// The isolation property the alias table exists for.
    #[test]
    fn tid_collision_cannot_hijack_another_users_session() {
        let (cu, engine) = setup();
        let victim = UserId(1);
        let attacker = UserId(2);

        let (_share, victim_sid) = cu
            .mkcol(victim, "shared-tid", "victim.bin".into(), Some(6), 0)
            .unwrap();
        cu.put_chunk(victim, "shared-tid", "1", b"SECRET").unwrap();

        // The attacker guesses the tid. It resolves to nothing in their
        // namespace — not to the victim's session.
        assert!(matches!(
            cu.put_chunk(attacker, "shared-tid", "1", b"EVIL!!"),
            Err(ChunkError::NotFound)
        ));
        assert!(matches!(
            cu.abort(attacker, "shared-tid"),
            Err(ChunkError::NotFound)
        ));
        assert!(matches!(
            cu.list_chunks(attacker, "shared-tid"),
            Err(ChunkError::NotFound)
        ));
        assert!(matches!(
            cu.assemble(attacker, "shared-tid", &move_headers(6, None), false, |_s, _p| {
                Ok(entry())
            }),
            Err(ChunkError::NotFound)
        ));

        // The attacker may legitimately use the same tid for their OWN upload,
        // and it gets a different session.
        let (_share, attacker_sid) = cu
            .mkcol(attacker, "shared-tid", "attacker.bin".into(), Some(4), 0)
            .unwrap();
        assert_ne!(victim_sid, attacker_sid);
        cu.put_chunk(attacker, "shared-tid", "1", b"MINE").unwrap();

        // The victim's data is untouched.
        cu.assemble(victim, "shared-tid", &move_headers(6, None), false, |_s, _p| {
            Ok(entry())
        })
        .unwrap();
        assert_eq!(engine.assembled(victim_sid).unwrap(), b"SECRET");
    }

    #[test]
    fn duplicate_mkcol_for_the_same_user_is_a_conflict_not_a_silent_rebind() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "a".into(), None, 0).unwrap();
        assert!(matches!(
            cu.mkcol(u, "tid", "b".into(), None, 0),
            Err(ChunkError::Conflict(_))
        ));
    }

    #[test]
    fn total_length_mismatch_is_a_400() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        cu.put_chunk(u, "tid", "1", b"abc").unwrap();
        let err = cu
            .assemble(u, "tid", &move_headers(999, None), false, |_s, _p| Ok(entry()))
            .unwrap_err();
        assert_eq!(err.status(), StatusCode::BAD_REQUEST);
    }

    /// The Android client never sends `OC-Total-Length` on the final MOVE
    /// (`ChunkedFileUploadRemoteOperation.java:216-225`). Requiring it failed
    /// every Android upload over 10,240,000 bytes with a 400 *after* all the
    /// bytes had been transferred. We fall back to the engine's own received
    /// length instead of refusing.
    #[test]
    fn android_move_without_total_length_assembles_from_the_received_length() {
        let (cu, e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        cu.put_chunk(u, "tid", "1", b"abc").unwrap();
        cu.put_chunk(u, "tid", "2", b"de").unwrap();

        // Exactly the header set Android sends: X-OC-Mtime and nothing else.
        let mut h = HeaderMap::new();
        h.insert("X-OC-Mtime", "1700000000".parse().unwrap());

        let sid = cu.resolve("tid", u).unwrap().session;
        let r = cu
            .assemble(u, "tid", &h, false, |_s, _p| Ok(entry()))
            .expect("Android's MOVE must not require OC-Total-Length");
        assert!(r.mtime_accepted);
        assert_eq!(e.assembled(sid).as_deref(), Some(&b"abcde"[..]));
    }

    /// The fallback must not become a way to finalise a *wrong* length: when
    /// the client does assert one, a mismatch is still a 400.
    #[test]
    fn an_asserted_total_length_still_has_to_match() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        cu.put_chunk(u, "tid", "1", b"abc").unwrap();
        let err = cu
            .assemble(u, "tid", &move_headers(999, None), false, |_s, _p| Ok(entry()))
            .unwrap_err();
        assert_eq!(err.status(), StatusCode::BAD_REQUEST);
    }

    /// iOS formats `X-OC-MTime` as a Swift `Double`
    /// (`the iOS SDK's +Upload.swift:404-406`), so it arrives with a fractional
    /// part. Rejecting it as "not an integer" failed every iOS chunked upload.
    #[test]
    fn ios_sends_a_fractional_mtime_and_it_is_truncated_not_rejected() {
        let mut h = HeaderMap::new();
        h.insert("X-OC-Mtime", "1751234567.891234".parse().unwrap());
        assert_eq!(
            oc_mtime_ns(&h).unwrap(),
            Some(1_751_234_567_i128 * 1_000_000_000)
        );

        // Android/desktop's integer form is unchanged.
        let mut h = HeaderMap::new();
        h.insert("X-OC-Mtime", "1751234567".parse().unwrap());
        assert_eq!(
            oc_mtime_ns(&h).unwrap(),
            Some(1_751_234_567_i128 * 1_000_000_000)
        );

        // Truncation toward zero, not rounding, and negatives survive.
        let mut h = HeaderMap::new();
        h.insert("X-OC-Mtime", "-5.9".parse().unwrap());
        assert_eq!(oc_mtime_ns(&h).unwrap(), Some(-5_i128 * 1_000_000_000));
    }

    /// Leniency has a limit: something that is not a number must not quietly
    /// become one.
    #[test]
    fn a_non_numeric_mtime_is_still_a_400() {
        for bad in ["abc", "1.2.3", "1.", "1e9", ""] {
            let mut h = HeaderMap::new();
            h.insert("X-OC-Mtime", bad.parse().unwrap());
            assert!(
                oc_mtime_ns(&h).is_err(),
                "X-OC-Mtime {bad:?} must be rejected"
            );
        }
    }

    #[test]
    fn resume_listing_is_sorted_and_complete() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        for n in ["00005", "1", "00002"] {
            cu.put_chunk(u, "tid", n, b"..").unwrap();
        }
        assert_eq!(cu.list_chunks(u, "tid").unwrap(), vec![1, 2, 5]);
    }

    #[test]
    fn abort_unbinds_the_alias() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        cu.abort(u, "tid").unwrap();
        assert!(matches!(cu.abort(u, "tid"), Err(ChunkError::NotFound)));
        // ...and the tid becomes reusable.
        cu.mkcol(u, "tid", "y".into(), None, 0).unwrap();
    }

    #[test]
    fn destination_header_accepts_absolute_url_and_bare_path() {
        let mut h = HeaderMap::new();
        h.insert(
            "Destination",
            "https://cloud.example.com/remote.php/dav/files/alice/a/b%20c.txt"
                .parse()
                .unwrap(),
        );
        assert_eq!(destination_path(&h, "alice").unwrap(), "a/b c.txt");

        h.insert(
            "Destination",
            "/remote.php/dav/files/alice/x.bin".parse().unwrap(),
        );
        assert_eq!(destination_path(&h, "alice").unwrap(), "x.bin");

        // Legacy alias.
        h.insert("Destination", "/remote.php/webdav/y.bin".parse().unwrap());
        assert_eq!(destination_path(&h, "alice").unwrap(), "y.bin");

        // Another user's root is not a valid destination.
        h.insert(
            "Destination",
            "/remote.php/dav/files/bob/secret".parse().unwrap(),
        );
        assert!(destination_path(&h, "alice").is_err());

        // Missing entirely.
        assert!(destination_path(&HeaderMap::new(), "alice").is_err());
    }

    /// The literal `Destination` values the mobile clients build. Both are
    /// absolute URLs, so the header cannot be used as a path unreduced.
    #[test]
    fn mobile_destination_headers_reduce_to_share_relative_paths() {
        // ChunkedFileUploadRemoteOperation.java:154
        //   client.getDavUri() + "/files/" + userId + encodePath(remotePath)
        let mut h = HeaderMap::new();
        h.insert(
            "Destination",
            "https://cloud.example.com/remote.php/dav/files/alice/Camera/IMG_0042.jpg"
                .parse()
                .unwrap(),
        );
        assert_eq!(
            destination_path(&h, "alice").unwrap(),
            "Camera/IMG_0042.jpg"
        );

        // the iOS SDK's +Upload.swift:230-236 — same shape, percent-encoded.
        h.insert(
            "Destination",
            "https://cloud.example.com/remote.php/dav/files/alice/My%20Photos/a%2Bb.heic"
                .parse()
                .unwrap(),
        );
        assert_eq!(
            destination_path(&h, "alice").unwrap(),
            "My Photos/a+b.heic"
        );
    }

    /// The complete Android chunked-upload conversation, in order, with only
    /// the headers Android actually sends. Each step corresponds to a line in
    /// `ChunkedFileUploadRemoteOperation.run()`.
    ///
    /// This is the regression test for the combination that used to fail: the
    /// MOVE carries no `OC-Total-Length`, so requiring it rejected the upload
    /// after every byte had already been transferred.
    #[test]
    fn the_full_android_chunked_upload_sequence_succeeds() {
        let (cu, e) = setup();
        let u = UserId(1);
        // Android's transfer id is md5(file) — stable across retries.
        let tid = "9e107d9d372bb6826bd81d3542a419d6";

        // 1. MKCOL with an absolute Destination and no OC-Total-Length.
        let mut mkcol_h = HeaderMap::new();
        mkcol_h.insert(
            "Destination",
            "https://cloud.example.com/remote.php/dav/files/alice/Camera/IMG_0042.jpg"
                .parse()
                .unwrap(),
        );
        let dest = destination_path(&mkcol_h, "alice").unwrap();
        assert_eq!(total_length(&mkcol_h).unwrap(), None);
        cu.mkcol(u, tid, dest, None, 0).unwrap();
        let sid = cu.resolve(tid, u).unwrap().session;

        // 2. PROPFIND: nothing uploaded yet, so Android starts at byte 0.
        assert!(cu.list_chunks_sized(u, tid).unwrap().is_empty());

        // 3. PUT chunks, zero-padded to six digits, starting at 1.
        cu.put_chunk(u, tid, "000001", b"hello ").unwrap();
        cu.put_chunk(u, tid, "000002", b"world").unwrap();

        // A resume at this point would report the exact byte count.
        let listed = cu.list_chunks_sized(u, tid).unwrap();
        assert_eq!(listed.iter().map(|(_, l)| *l).sum::<u64>(), 11);

        // 4. MOVE .file with X-OC-Mtime and X-OC-Ctime only.
        let mut move_h = HeaderMap::new();
        move_h.insert("X-OC-Mtime", "1700000000".parse().unwrap());
        move_h.insert("X-OC-Ctime", "1699999999".parse().unwrap());
        let r = cu
            .assemble(u, tid, &move_h, false, |_s, _p| Ok(entry()))
            .expect("Android's MOVE header set must be sufficient");

        assert!(r.created);
        assert!(r.mtime_accepted);
        // Both are mandatory on the MOVE response: without either one the
        // client hard-fails the item even on a 201.
        assert!(!r.oc_file_id.is_empty());
        assert!(r.etag.starts_with('"') && r.etag.ends_with('"'));
        assert_eq!(e.assembled(sid).as_deref(), Some(&b"hello world"[..]));

        // The alias is gone, so re-uploading the same file (same md5 tid)
        // opens a fresh session rather than colliding with this one.
        assert!(cu.resolve(tid, u).is_err());
    }

    /// The iOS sequence differs in three ways that all have to work: a UUID
    /// transfer id, unpadded chunk names, and a fractional `X-OC-MTime`.
    #[test]
    fn the_full_ios_chunked_upload_sequence_succeeds() {
        let (cu, e) = setup();
        let u = UserId(1);
        let tid = "E621E1F8-C36C-495A-93FC-0C247A3E6E5F";

        let mut mkcol_h = HeaderMap::new();
        mkcol_h.insert(
            "Destination",
            "https://cloud.example.com/remote.php/dav/files/alice/big.mov"
                .parse()
                .unwrap(),
        );
        // iOS carries OC-Total-Length on every request in the flow.
        mkcol_h.insert("OC-Total-Length", "11".parse().unwrap());
        let dest = destination_path(&mkcol_h, "alice").unwrap();
        cu.mkcol(u, tid, dest, total_length(&mkcol_h).unwrap(), 0)
            .unwrap();
        let sid = cu.resolve(tid, u).unwrap().session;

        // Unpadded, 1-based (NKCommon.swift:123, 204).
        cu.put_chunk(u, tid, "1", b"hello ").unwrap();
        cu.put_chunk(u, tid, "2", b"world").unwrap();

        let mut move_h = HeaderMap::new();
        move_h.insert("OC-Total-Length", "11".parse().unwrap());
        move_h.insert("X-OC-MTime", "1700000000.123456".parse().unwrap());
        move_h.insert("Overwrite", "T".parse().unwrap());
        let r = cu
            .assemble(u, tid, &move_h, false, |_s, _p| Ok(entry()))
            .expect("iOS's fractional X-OC-MTime must not fail the assembly");

        assert!(r.mtime_accepted);
        assert_eq!(e.assembled(sid).as_deref(), Some(&b"hello world"[..]));
    }

    #[test]
    fn mtime_header_parsing() {
        let mut h = HeaderMap::new();
        assert_eq!(oc_mtime_ns(&h).unwrap(), None);
        h.insert("X-OC-Mtime", "1700000000".parse().unwrap());
        assert_eq!(oc_mtime_ns(&h).unwrap(), Some(1_700_000_000_000_000_000i128));
        h.insert("X-OC-Mtime", "not-a-number".parse().unwrap());
        assert_eq!(
            oc_mtime_ns(&h).unwrap_err().status(),
            StatusCode::BAD_REQUEST
        );
    }

    #[test]
    fn transfer_ids_are_bounded() {
        assert!(validate_tid("").is_err());
        assert!(validate_tid(&"a".repeat(129)).is_err());
        assert!(validate_tid("..").is_err());
        assert!(validate_tid("../../etc/passwd").is_err());
        assert!(validate_tid("web-file-upload-1234").is_ok());
        assert!(validate_tid("3921084712").is_ok());
    }

    /// Android's transfer id is `md5(file)`
    /// (`ChunkedFileUploadRemoteOperation.java:152`) and iOS's is a UUID
    /// (`the iOS SDK's +Upload.swift:186`). Both must survive tid validation.
    #[test]
    fn mobile_transfer_ids_are_accepted() {
        assert!(validate_tid("d41d8cd98f00b204e9800998ecf8427e").is_ok());
        assert!(validate_tid("E621E1F8-C36C-495A-93FC-0C247A3E6E5F").is_ok());
    }

    /// The lengths must sum to exactly the received total, because that sum is
    /// literally the byte offset Android resumes from.
    #[test]
    fn reported_chunk_lengths_sum_to_the_received_total() {
        for (names, total) in [
            (vec![1u32], 10u64),
            (vec![1, 2, 3], 100),
            // A total that does not divide evenly must not lose the remainder.
            (vec![1, 2, 3], 100_001),
            (vec![1, 2, 3, 4, 5, 6, 7], 30_720_001),
        ] {
            let got = distribute(&names, total);
            assert_eq!(got.len(), names.len());
            assert_eq!(
                got.iter().map(|(_, l)| *l).sum::<u64>(),
                total,
                "reported lengths for {names:?} must sum to {total}"
            );
        }
        assert!(distribute(&[], 0).is_empty());
    }

    /// The response Android parses to decide where to resume. Every assertion
    /// here corresponds to a line in `ChunkedFileUploadRemoteOperation` or
    /// `WebdavEntry` that would otherwise corrupt or abort the upload.
    #[test]
    fn chunk_listing_carries_everything_both_mobile_parsers_need() {
        let xml = chunk_listing_xml("/remote.php/dav/uploads/alice/abc123", &[(1, 40), (2, 25)]);

        // iOS matches element names literally and namespace-unaware
        // (NKDataFileXML.swift:287), so the prefix must be lowercase `d:`.
        assert!(
            xml.contains("<d:multistatus"),
            "iOS looks up the literal name `d:multistatus`; `D:` matches nothing"
        );
        assert!(!xml.contains("<D:"), "no uppercase prefix may survive");

        // Android sums these to get its resume offset; without them it
        // restarts at byte 0 while continuing the chunk numbering.
        assert!(xml.contains("<d:getcontentlength>40</d:getcontentlength>"));
        assert!(xml.contains("<d:getcontentlength>25</d:getcontentlength>"));

        // Android splits the href on the uploads prefix and indexes [1]
        // (WebdavEntry.kt:118) — an href without it throws and fails the run.
        assert!(xml.contains("<d:href>/remote.php/dav/uploads/alice/abc123/1</d:href>"));

        // The collection itself is a collection, so it is filtered out of the
        // chunk scan rather than counted as a chunk.
        assert!(xml.contains("<d:resourcetype><d:collection/></d:resourcetype>"));
        // ...and the chunks are not.
        assert!(xml.contains("<d:resourcetype/>"));

        // A 200 propstat, because iOS reads only `d:propstat[0]`.
        assert!(xml.contains("<d:status>HTTP/1.1 200 OK</d:status>"));
        assert!(!xml.contains("404"));
    }

    /// A fresh session lists no chunks — Android then starts at byte 0, chunk
    /// 1, which is exactly right.
    #[test]
    fn a_fresh_session_lists_only_the_collection() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        assert!(cu.list_chunks_sized(u, "tid").unwrap().is_empty());
    }

    /// End-to-end: after two chunks the listing reports the exact number of
    /// bytes we hold, so a resuming client continues from the right offset.
    #[test]
    fn a_resumed_session_reports_the_bytes_we_actually_hold() {
        let (cu, _e) = setup();
        let u = UserId(1);
        cu.mkcol(u, "tid", "x".into(), None, 0).unwrap();
        cu.put_chunk(u, "tid", "000001", &[0u8; 40]).unwrap();
        cu.put_chunk(u, "tid", "000002", &[0u8; 25]).unwrap();

        let listed = cu.list_chunks_sized(u, "tid").unwrap();
        assert_eq!(listed.iter().map(|(n, _)| *n).collect::<Vec<_>>(), vec![1, 2]);
        assert_eq!(
            listed.iter().map(|(_, l)| *l).sum::<u64>(),
            65,
            "the sum is the byte offset Android resumes from"
        );
    }

    /// Android zero-pads chunk names to six digits
    /// (`ChunkedFileUploadRemoteOperation.java:280`, `%06d`) while iOS sends
    /// bare decimals (`NKCommon.swift:204`, `String(counter)`). Both must map
    /// to the same numeric ordering — and in particular iOS's names must not
    /// be ordered lexicographically, or chunk 10 would be assembled before
    /// chunk 2.
    #[test]
    fn android_padded_and_ios_unpadded_chunk_names_agree() {
        assert_eq!(parse_chunk_name("000001").unwrap(), 1);
        assert_eq!(parse_chunk_name("1").unwrap(), 1);
        assert_eq!(parse_chunk_name("000010").unwrap(), 10);
        assert_eq!(parse_chunk_name("10").unwrap(), 10);
        // The ordering the assembler uses is numeric, so 2 precedes 10.
        assert!(parse_chunk_name("2").unwrap() < parse_chunk_name("10").unwrap());
    }
}
