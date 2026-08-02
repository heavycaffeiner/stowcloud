//! The seam between the TUS *protocol* (which is this crate's job) and the
//! resumable-upload *engine* (which is not).
//!
//! `DESIGN-UPLOAD.md` §1.3 puts the engine in its own crate precisely so the
//! spool format, interval bookkeeping and finalisation are not entangled with
//! header parsing. This trait is the narrow waist between the two: everything
//! here is expressed in the vocabulary the wire already forces on us
//! (a session handle string, a byte offset, a length), and nothing about how
//! a part file is laid out crosses it.
//!
//! `sc-server` binds this to `sc_upload::UploadEngine`; the default bodies
//! exist so `AppState` is constructible in tests without an engine.

use sc_vfs::ids::UserId;

use crate::core_api::CoreError;

/// What a `HEAD /api/uploads/{id}` has to answer.
#[derive(Clone, Copy, Debug)]
pub struct UploadStatus {
    /// TUS `Upload-Offset`: the first byte we do **not** have yet.
    pub offset: u64,
    /// TUS `Upload-Length`, when the client declared one.
    pub length: Option<u64>,
    pub complete: bool,
    /// The session's chunk size, fixed at creation (`DESIGN-UPLOAD.md` §3):
    /// an admin changing the server default mid-upload must not change what
    /// an in-flight session already committed to. Surfaced as `Sc-Chunk-Size`
    /// on `HEAD` so a resuming client follows the session's actual value
    /// instead of guessing from a possibly-stale local record.
    pub chunk_size: u32,
}

/// TUS `checksum` extension algorithm — `Tus-Checksum-Algorithm` advertises
/// both; `Upload-Checksum: <algo> <base64(digest)>` names one per `PATCH`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TusChecksumAlgo {
    Crc32c,
    Blake3,
}

/// A parsed `Upload-Checksum` header: which algorithm, and the digest the
/// client asserts `data` hashes to. Kept independent of `sc_upload::Checksum`
/// on purpose — this crate does not depend on `sc-upload` (see this file's
/// module doc: the trait is the narrow waist *between* the wire and the
/// engine), so the wire-shaped value crosses here and the engine-shaped one
/// is only ever constructed on the other side of this trait.
#[derive(Clone, Debug)]
pub struct TusChecksum {
    pub algo: TusChecksumAlgo,
    pub digest: Vec<u8>,
}

/// What `POST /api/uploads` answers when the request also carried a body
/// (TUS `creation-with-upload`): the new session id, plus how much of that
/// body actually landed — a `Content-Type` violating client, or a body
/// longer than `Upload-Length`, can end up with `offset` short of the whole
/// body's length, which the caller must still report accurately rather than
/// assuming "all of it took".
#[derive(Clone, Debug)]
pub struct CreatedWithUpload {
    pub id: String,
    pub offset: u64,
}

pub trait UploadApi: Send + Sync {
    /// `POST /api/uploads`. Returns the opaque session id that goes in
    /// `Location`.
    ///
    /// `random_access` mirrors the wire's opt-in `Sc-Random-Access: 1`
    /// (`DESIGN-UPLOAD.md` §2.3): when set, `patch` accepts any offset in
    /// `[0, total_len)` instead of requiring strict sequential delivery. The
    /// web client's own Worker sends chunks for one file with up to
    /// `MAX_INFLIGHT` requests in flight at once, so without this a chunked
    /// upload's out-of-order arrivals would all `409 Conflict`.
    fn create(
        &self,
        _user: UserId,
        _dest_vpath: &str,
        _total_len: Option<u64>,
        _random_access: bool,
    ) -> Result<String, CoreError> {
        Err(not_wired())
    }

    /// `POST /api/uploads` with a body attached (TUS `creation-with-upload`,
    /// `Content-Type: application/offset+octet-stream`): create the session
    /// *and* spool `initial_body` at offset 0 in one round trip, exactly as
    /// if a `PATCH` had immediately followed. Callers that don't have a body
    /// to attach should call [`UploadApi::create`] instead — this method
    /// exists so the extension the server advertises actually does
    /// something with the bytes, rather than creating an empty session and
    /// silently discarding whatever arrived in the same request.
    ///
    /// Default: creates the session and reports the body as *not* written.
    /// This is intentionally the same outcome as the historical bug (a
    /// creation-with-upload body being silently dropped) rather than a
    /// panic, so existing test doubles that only override `create` keep
    /// building — but any real backend must override this to get the
    /// advertised extension's actual behavior; see `UploadBridge` in
    /// `sc-server` for the implementation that does.
    fn create_with_upload(
        &self,
        user: UserId,
        dest_vpath: &str,
        total_len: Option<u64>,
        random_access: bool,
        _initial_body: &[u8],
    ) -> Result<CreatedWithUpload, CoreError> {
        self.create(user, dest_vpath, total_len, random_access)
            .map(|id| CreatedWithUpload { id, offset: 0 })
    }

    fn status(&self, _user: UserId, _id: &str) -> Result<UploadStatus, CoreError> {
        Err(not_wired())
    }

    /// Write `data` at `offset`. Returns the new offset.
    fn patch(
        &self,
        _user: UserId,
        _id: &str,
        _offset: u64,
        _data: &[u8],
    ) -> Result<u64, CoreError> {
        Err(not_wired())
    }

    /// As [`UploadApi::patch`], but also enforces the TUS `checksum`
    /// extension's `Upload-Checksum` header when the client sent one: `data`
    /// must hash to `checksum`'s digest under its named algorithm, or the
    /// bytes are rejected rather than silently accepted despite failing the
    /// client's own integrity check.
    ///
    /// Default: delegates to [`UploadApi::patch`] and ignores `checksum`
    /// entirely — again, the historical (buggy) behavior, kept as the
    /// default for the same reason `create_with_upload`'s default is. A real
    /// backend must override this; `UploadBridge` does, forwarding the
    /// parsed checksum to `sc_upload::UploadEngine::patch`, which already
    /// verifies it against the data before committing the range.
    fn patch_checked(
        &self,
        user: UserId,
        id: &str,
        offset: u64,
        data: &[u8],
        _checksum: Option<TusChecksum>,
    ) -> Result<u64, CoreError> {
        self.patch(user, id, offset, data)
    }

    /// TUS `termination`: drop the session and its spool.
    fn terminate(&self, _user: UserId, _id: &str) -> Result<(), CoreError> {
        Err(not_wired())
    }

    /// Best-effort drain at shutdown: flush every in-flight session to a
    /// clean resume point. Returns how many sessions were touched.
    fn drain(&self) -> usize {
        0
    }

    /// How many sessions (any user) are still accepting bytes right now —
    /// non-destructive, unlike [`Self::drain`]. Backs the server-settings
    /// restart warning: a TUS client mid-transfer sees its connection drop on
    /// restart, so the admin should see this count before confirming.
    fn active_count(&self) -> usize {
        0
    }

    /// How long a `PATCH` body read may sit idle (no bytes arriving) before
    /// it is aborted — `sc_upload::UploadConfig::body_idle_timeout`
    /// (`DESIGN-UPLOAD.md` §6). Without this, a client that opens a `PATCH`
    /// and then stops sending holds the request, and the engine's open part-
    /// file handle, forever: trivial resource exhaustion, one connection at
    /// a time.
    ///
    /// This crate deliberately does not depend on `sc-upload` (module doc,
    /// above), so the default here restates `UploadConfig::default()`'s 60s
    /// rather than importing it. A real backend should override this to
    /// return the actually-configured value; see `UploadBridge` in
    /// `sc-server` — as of this writing it does not yet override this
    /// method, so it runs on the restated default until it does.
    fn body_idle_timeout(&self) -> std::time::Duration {
        std::time::Duration::from_secs(60)
    }

    /// Current live `(chunk_min, chunk_default)` — the admin-settable pair
    /// `GET /api/capabilities`/`GET /api/auth/session` advertise
    /// (`DESIGN-UPLOAD.md` §1.3). This crate does not depend on `sc-upload`
    /// (module doc, above), so the default here restates
    /// `sc_upload::UploadConfig::default()`'s 5 MiB / 10 MiB rather than
    /// importing it. `UploadBridge` overrides this to read the engine's live
    /// `ChunkSettings` instead.
    fn chunk_limits(&self) -> (u64, u64) {
        (5 * 1024 * 1024, 10 * 1024 * 1024)
    }

    /// Admin write path for the pair above. Default: not wired (mirrors every
    /// other stub in this trait) — a real backend must override this;
    /// `UploadBridge` forwards to `sc_upload::UploadEngine::set_chunk_settings`,
    /// which does the actual floor/ordering validation and persistence.
    fn set_chunk_limits(&self, _min: u64, _default: u64) -> Result<(), CoreError> {
        Err(not_wired())
    }
}

fn not_wired() -> CoreError {
    CoreError::Internal("upload engine not wired".into())
}

/// The default `AppState::uploads`, used by tests and by any build that has
/// no engine attached.
pub struct UnimplementedUploads;
impl UploadApi for UnimplementedUploads {}

/// Why a `PATCH` body read stopped short of a complete body.
#[derive(Debug, PartialEq, Eq)]
pub enum BodyReadError {
    /// No bytes arrived for a full `idle_timeout` window. The caller should
    /// map this to a client-actionable status (e.g. `408 Request Timeout`)
    /// rather than `500` — the client can simply retry the `PATCH` from
    /// `HEAD`'s reported offset, nothing on disk was corrupted.
    Idle,
    /// The connection/transport itself errored (reset, malformed framing).
    Body,
}

/// Drain `body` into memory, aborting with [`BodyReadError::Idle`] if no
/// chunk arrives within `idle_timeout` of the previous one (or of the
/// start). `DESIGN-UPLOAD.md` §6: `body_idle_timeout` has existed as a
/// config field since the upload engine shipped, but nothing in the HTTP
/// layer ever read it — the single `axum::body::to_bytes(req.into_body(),
/// usize::MAX).await` call this replaces waits forever no matter how long a
/// client goes silent mid-`PATCH`, holding the request (and, transitively,
/// the engine's open part-file handle) for as long as the client cares to
/// leave the connection open.
///
/// The timer resets on every chunk that actually arrives, so a slow-but-
/// steady transfer is never punished — only silence trips it, matching
/// `upload.request_timeout`'s documented absence of a *total* transfer-time
/// bound (`DESIGN-UPLOAD.md` §1.3's defense-in-depth table: idle timeout and
/// whole-request timeout are two different knobs on purpose).
pub async fn read_body_with_idle_timeout(
    body: axum::body::Body,
    idle_timeout: std::time::Duration,
) -> Result<bytes::Bytes, BodyReadError> {
    use futures::StreamExt;
    let mut stream = body.into_data_stream();
    let mut buf = bytes::BytesMut::new();
    loop {
        match tokio::time::timeout(idle_timeout, stream.next()).await {
            Ok(Some(Ok(chunk))) => buf.extend_from_slice(&chunk),
            Ok(Some(Err(_))) => return Err(BodyReadError::Body),
            Ok(None) => return Ok(buf.freeze()),
            Err(_) => return Err(BodyReadError::Idle),
        }
    }
}

#[cfg(test)]
mod idle_timeout_tests {
    //! Proves `read_body_with_idle_timeout` actually enforces silence,
    //! before any wiring into a real route: the defect this guards against
    //! (`DESIGN-UPLOAD.md` §6) is that `body_idle_timeout` was configured
    //! and read nowhere, so a body that just stops sending was never
    //! noticed by anything. These tests fail against the old
    //! `axum::body::to_bytes(.., usize::MAX)` call this function replaces --
    //! that call has no timeout parameter at all and would hang instead of
    //! returning `Err(Idle)`.
    use super::*;
    use std::time::Duration;

    /// A stream that yields a fixed set of chunks, sleeping before each one
    /// — the same shape a slow-drip client produces on the wire.
    fn drip_body(chunks: Vec<(Duration, &'static [u8])>) -> axum::body::Body {
        let s = futures::stream::unfold(chunks.into_iter(), |mut it| async move {
            let (delay, chunk) = it.next()?;
            tokio::time::sleep(delay).await;
            Some((Ok::<_, std::io::Error>(bytes::Bytes::from_static(chunk)), it))
        });
        axum::body::Body::from_stream(s)
    }

    #[tokio::test(start_paused = true)]
    async fn steady_drip_within_the_timeout_completes() {
        let body = drip_body(vec![
            (Duration::from_secs(1), b"hello "),
            (Duration::from_secs(1), b"world"),
        ]);
        let out = read_body_with_idle_timeout(body, Duration::from_secs(5)).await.unwrap();
        assert_eq!(&out[..], b"hello world");
    }

    #[tokio::test(start_paused = true)]
    async fn silence_past_the_deadline_aborts_with_idle() {
        // First chunk arrives promptly, then the client goes silent for
        // longer than the configured idle window — a client that opens a
        // PATCH and then stops sending.
        let body = drip_body(vec![
            (Duration::from_millis(100), b"partial"),
            (Duration::from_secs(60), b"never arrives in time"),
        ]);
        let err = read_body_with_idle_timeout(body, Duration::from_secs(2)).await.unwrap_err();
        assert_eq!(err, BodyReadError::Idle);
    }

    #[tokio::test(start_paused = true)]
    async fn an_empty_body_completes_without_waiting_for_the_deadline() {
        let body = axum::body::Body::empty();
        let out = read_body_with_idle_timeout(body, Duration::from_secs(60)).await.unwrap();
        assert!(out.is_empty());
    }
}
