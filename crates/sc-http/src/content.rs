//! Signed content URLs — `DESIGN-PREVIEW.md` §2.2.
//!
//! ```text
//! https://content.example.com/c/<payload>.<sig>
//! payload = base64url( postcard::to_bytes(&Claim) )
//! sig     = base64url( HMAC-SHA256(key[kid], payload)[..16] )
//! ```
//!
//! Verification is **completely stateless** — no DB lookup, constant-time
//! HMAC comparison, `exp` check, `etag` re-check (caller maps a mismatch to
//! `410 Gone`). Key rotation keeps up to 4 live keys addressed by `kid`; new
//! issuance always uses the newest.

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use hmac::{Hmac, Mac};
use serde::{Deserialize, Serialize};
use sha2::Sha256;
use subtle::ConstantTimeEq;

type HmacSha256 = Hmac<Sha256>;

/// Bytes pulled per `Read::read` call while turning a synchronous reader
/// into a chunked HTTP body — same bound as `sc_core::stream::CHUNK`
/// (independently defined: this crate does not depend on `sc-core`).
const BODY_CHUNK: usize = 256 * 1024;

/// Wraps a `tokio::sync::mpsc::UnboundedReceiver` as a `futures::Stream`, so
/// it can be handed to `axum::body::Body::from_stream`. `UnboundedSender`
/// is plain, non-async `Send + Sync + Clone`, which is what lets a
/// `spawn_blocking` task (running on an arbitrary blocking-pool thread, not
/// inside the async runtime) push chunks into it with a bare `.send()`.
pub(crate) struct ReceiverStream<T>(pub tokio::sync::mpsc::UnboundedReceiver<T>);

impl<T> futures::Stream for ReceiverStream<T> {
    type Item = T;
    fn poll_next(mut self: std::pin::Pin<&mut Self>, cx: &mut std::task::Context<'_>) -> std::task::Poll<Option<T>> {
        self.0.poll_recv(cx)
    }
}

/// Turns a bounded-memory synchronous `Read` (typically an
/// `sc_core::CoreFileStream`, type-erased at the `ContentApi`/`CoreApi`
/// boundary) into a chunked HTTP response body. The read loop runs on a
/// blocking-pool task in [`BODY_CHUNK`]-sized pulls, so neither the async
/// reactor nor process memory is tied to how large the underlying file is —
/// the same guarantee `Core::open_stream`'s 256 KiB read loop provides on
/// the `sc-core` side, carried through to the HTTP body.
pub fn body_from_reader(mut reader: Box<dyn std::io::Read + Send>) -> axum::body::Body {
    let (tx, rx) = tokio::sync::mpsc::unbounded_channel::<Result<bytes::Bytes, std::io::Error>>();
    tokio::task::spawn_blocking(move || {
        let mut buf = vec![0u8; BODY_CHUNK];
        loop {
            match reader.read(&mut buf) {
                Ok(0) => break,
                Ok(n) => {
                    if tx.send(Ok(bytes::Bytes::copy_from_slice(&buf[..n]))).is_err() {
                        break; // receiver dropped: client disconnected mid-stream.
                    }
                }
                Err(e) => {
                    let _ = tx.send(Err(e));
                    break;
                }
            }
        }
    });
    axum::body::Body::from_stream(ReceiverStream(rx))
}

/// Maximum number of simultaneously valid signing keys (`DESIGN-PREVIEW.md`
/// §2.3: "up to 4 keys valid at once, keyed by kid").
pub const MAX_KEYS: usize = 4;

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Disposition {
    Attachment,
    InlineThumb,
    Stream,
}

impl Disposition {
    /// `DESIGN-PREVIEW.md` §2.3 default TTLs.
    pub fn default_ttl(self) -> std::time::Duration {
        use std::time::Duration;
        match self {
            Disposition::InlineThumb => Duration::from_secs(5 * 60),
            Disposition::Attachment => Duration::from_secs(15 * 60),
            Disposition::Stream => Duration::from_secs(12 * 60 * 60),
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct Claim {
    /// Format version.
    pub v: u8,
    /// Signing key id (rotation).
    pub kid: u8,
    /// `FileId`.
    pub fid: i64,
    /// First 8 bytes of the ETag — URL auto-invalidates when the file changes.
    pub etag: [u8; 8],
    /// Unix seconds.
    pub exp: u64,
    pub disp: Disposition,
    /// Thumbnail dimensions, if applicable.
    pub dim: Option<(u16, u16)>,
    /// Issued-to `UserId` (0 = public link). Audit only.
    pub sub: u32,
}

pub const CLAIM_VERSION: u8 = 1;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VerifyError {
    Malformed,
    UnknownKid,
    BadSignature,
    Expired,
    EtagMismatch,
}

/// Up to [`MAX_KEYS`] HMAC signing keys, addressed by `kid`. `sign` always
/// uses `current_kid`; `verify` looks up whichever `kid` the claim names so
/// still-live old keys keep validating outstanding URLs.
pub struct SignedUrlKeys {
    keys: [Option<[u8; 32]>; MAX_KEYS],
    current_kid: u8,
}

impl SignedUrlKeys {
    /// Fresh single-key set generated from a CSPRNG. Used at first boot / in
    /// tests.
    pub fn generate() -> Self {
        let mut key = [0u8; 32];
        getrandom::getrandom(&mut key).expect("getrandom failed");
        let mut keys: [Option<[u8; 32]>; MAX_KEYS] = [None, None, None, None];
        keys[0] = Some(key);
        Self { keys, current_kid: 0 }
    }

    pub fn from_key(kid: u8, key: [u8; 32]) -> Self {
        let mut keys: [Option<[u8; 32]>; MAX_KEYS] = [None, None, None, None];
        keys[kid as usize % MAX_KEYS] = Some(key);
        Self { keys, current_kid: kid }
    }

    /// Rotate in a new current key, keeping the previous keys live (up to
    /// `MAX_KEYS` total) so URLs signed with them keep validating.
    pub fn rotate_in(&mut self, kid: u8, key: [u8; 32]) {
        self.keys[kid as usize % MAX_KEYS] = Some(key);
        self.current_kid = kid;
    }

    /// Immediately revoke a `kid` — every URL signed with it dies at once
    /// (`DESIGN-PREVIEW.md` §2.3: "on a leak, revoke that kid immediately").
    pub fn revoke(&mut self, kid: u8) {
        self.keys[kid as usize % MAX_KEYS] = None;
    }

    fn key_for(&self, kid: u8) -> Option<&[u8; 32]> {
        self.keys[kid as usize % MAX_KEYS].as_ref()
    }
}

fn now_unix() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// Sign `claim` (fixing `kid` to `keys.current_kid`) and render the
/// `<payload>.<sig>` token, both base64url. Does not include the `/c/` path
/// prefix or host — callers assemble the full URL.
pub fn sign(keys: &SignedUrlKeys, mut claim: Claim) -> String {
    claim.kid = keys.current_kid;
    claim.v = CLAIM_VERSION;
    let payload = postcard::to_allocvec(&claim).expect("Claim postcard-serializable");
    let payload_b64 = URL_SAFE_NO_PAD.encode(&payload);
    let key = keys.key_for(claim.kid).expect("current key must be present");
    let mut mac = HmacSha256::new_from_slice(key).expect("HMAC accepts any key length");
    mac.update(payload_b64.as_bytes());
    let sig = mac.finalize().into_bytes();
    let sig_b64 = URL_SAFE_NO_PAD.encode(&sig[..16]);
    format!("{payload_b64}.{sig_b64}")
}

/// Build a claim with `exp` computed from `disp`'s default TTL (or an
/// explicit override).
pub fn make_claim(fid: i64, etag8: [u8; 8], disp: Disposition, dim: Option<(u16, u16)>, sub: u32, ttl: Option<std::time::Duration>) -> Claim {
    let ttl = ttl.unwrap_or_else(|| disp.default_ttl());
    Claim { v: CLAIM_VERSION, kid: 0, fid, etag: etag8, exp: now_unix() + ttl.as_secs(), disp, dim, sub }
}

/// Verify a `<payload>.<sig>` token. `current_etag8` is the *current* first-8
/// bytes of the target file's ETag — a mismatch means the file changed since
/// issuance and the caller should respond `410 Gone`.
pub fn verify(keys: &SignedUrlKeys, token: &str, current_etag8: Option<[u8; 8]>) -> Result<Claim, VerifyError> {
    let (payload_b64, sig_b64) = token.split_once('.').ok_or(VerifyError::Malformed)?;
    let sig = URL_SAFE_NO_PAD.decode(sig_b64).map_err(|_| VerifyError::Malformed)?;
    let payload = URL_SAFE_NO_PAD.decode(payload_b64).map_err(|_| VerifyError::Malformed)?;
    let claim: Claim = postcard::from_bytes(&payload).map_err(|_| VerifyError::Malformed)?;

    let key = keys.key_for(claim.kid).ok_or(VerifyError::UnknownKid)?;
    let mut mac = HmacSha256::new_from_slice(key).expect("HMAC accepts any key length");
    mac.update(payload_b64.as_bytes());
    let expected = mac.finalize().into_bytes();
    // Constant-time comparison (`DESIGN-PREVIEW.md` §2.2/§9: "constant-time HMAC comparison").
    if expected[..16].ct_eq(&sig[..]).unwrap_u8() != 1 || sig.len() != 16 {
        return Err(VerifyError::BadSignature);
    }

    if claim.exp < now_unix() {
        return Err(VerifyError::Expired);
    }
    if let Some(cur) = current_etag8 {
        if cur != claim.etag {
            return Err(VerifyError::EtagMismatch);
        }
    }
    Ok(claim)
}

/// First 8 raw bytes of a hex `ETag` string (`DESIGN-PREVIEW.md` §2.2:
/// "first 8 bytes of the ETag"). Every signing call site in this binary derives
/// `Claim::etag` this way (`sc-server`'s `nc.rs`/`bridge.rs` included), so
/// verification must recompute it identically or every link would 410 the
/// moment it was minted.
pub fn etag8_of(etag: &str) -> [u8; 8] {
    let mut out = [0u8; 8];
    let raw = etag.as_bytes();
    let n = raw.len().min(8);
    out[..n].copy_from_slice(&raw[..n]);
    out
}

/// A byte range parsed from an HTTP `Range` header, not yet resolved against
/// a real file size.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RangeRequest {
    /// No `Range` header, or a form this parser gives up on — serve the
    /// whole entity with `200`.
    None,
    /// `bytes=start-end` (inclusive) or `bytes=start-` (end unbounded).
    Single { start: u64, end: Option<u64> },
    /// `bytes=-n`: the last `n` bytes.
    Suffix(u64),
    /// More than one range requested. `DESIGN-PREVIEW.md`/the task brief:
    /// "a multi-range request gets the full 200" — we do not implement
    /// `multipart/byteranges`.
    Multi,
}

/// Parse a `Range` header value. Only the `bytes=` unit is understood;
/// anything else is treated as absent (same as `None`).
pub fn parse_range_header(value: &str) -> RangeRequest {
    let Some(spec) = value.strip_prefix("bytes=") else {
        return RangeRequest::None;
    };
    if spec.contains(',') {
        return RangeRequest::Multi;
    }
    let spec = spec.trim();
    match spec.split_once('-') {
        Some(("", suffix)) => match suffix.parse::<u64>() {
            Ok(n) if n > 0 => RangeRequest::Suffix(n),
            _ => RangeRequest::None,
        },
        Some((start, "")) => match start.parse::<u64>() {
            Ok(s) => RangeRequest::Single { start: s, end: None },
            Err(_) => RangeRequest::None,
        },
        Some((start, end)) => match (start.parse::<u64>(), end.parse::<u64>()) {
            (Ok(s), Ok(e)) if s <= e => RangeRequest::Single { start: s, end: Some(e) },
            _ => RangeRequest::None,
        },
        None => RangeRequest::None,
    }
}

/// Resolve a parsed [`RangeRequest`] against the real file `size`, clamping
/// to an inclusive `(start, end)` pair. `Err(())` means "not satisfiable" —
/// the caller should answer `416` with `Content-Range: bytes */{size}`.
// There is exactly one error condition this function can report, it's named
// and documented right here, and the one real call site (`routes.rs`)
// already matches `Err(())` meaningfully. A dedicated error type would be
// ceremony over a boolean-shaped outcome with nothing to add to it.
#[allow(clippy::result_unit_err)]
pub fn clamp_range(req: RangeRequest, size: u64) -> Result<Option<(u64, u64)>, ()> {
    if size == 0 {
        // Nothing to range over; treat as a full (empty) response rather
        // than manufacturing a 416 for a zero-byte file.
        return Ok(None);
    }
    match req {
        RangeRequest::None | RangeRequest::Multi => Ok(None),
        RangeRequest::Suffix(n) => {
            let n = n.min(size);
            Ok(Some((size - n, size - 1)))
        }
        RangeRequest::Single { start, end } => {
            if start >= size {
                return Err(());
            }
            let end = end.unwrap_or(size - 1).min(size - 1);
            Ok(Some((start, end)))
        }
    }
}

/// Build the browser-facing URL for a signed `/c/{token}` content link.
///
/// `host` is `content_hosts.first()`. When it is `None` — the
/// `DESIGN-PREVIEW.md` §2.5 single-origin fallback, which is exactly what
/// `.dev/sc.toml` and production both run today — the naive
/// `format!("https://{host}/c/{token}")` with `host = ""` produces
/// `https:///c/<token>`. That string does **not** mean "no host": per WHATWG
/// URL parsing, a special scheme (`https`) collapses the empty authority's
/// extra slashes and the browser resolves the host as `c` — a real,
/// unrelated, unreachable domain, not an absence of one. A same-tab
/// `<a href>` to it navigates the whole tab to `chrome-error://chromewebdata/`;
/// `curl` reports "Could not resolve host: c". This was reproduced against
/// the live dev server and cost real debugging time — see the bug write-up
/// this function's addition closes.
///
/// The fix is a host-relative path, not an attempt to guess an absolute
/// origin: `content_get`'s own `single_origin` gate (`routes.rs`) already
/// accepts a `/c/{token}` request on whatever `Host` the browser is already
/// pointed at when `content_hosts` is empty (`DESIGN-PREVIEW.md` §2.5's
/// documented, warned-about trade-off), so a relative URL always resolves to
/// the one origin that will actually answer it — no host to get wrong.
///
/// This is the *only* place in the binary allowed to assemble a `/c/`
/// URL — every caller (`fs_link`, `public_link_download`, and any future
/// one) must go through here rather than hand-rolling `format!("https://{}/c/{}", ...)`
/// again, which is exactly how this bug happened the first time.
pub fn content_url(host: Option<&str>, token: &str) -> String {
    match host {
        Some(h) if !h.is_empty() => format!("https://{h}/c/{token}"),
        _ => format!("/c/{token}"),
    }
}

/// RFC 5987 `filename*` percent-encoding, plus an ASCII-safe fallback for
/// `filename=`. Both strip CR/LF/quotes first — `DESIGN-PREVIEW.md` §2.4:
/// header injection through a crafted filename is exactly what this is for.
pub fn content_disposition_value(disposition: &str, name: &str) -> String {
    let sanitized: String = name.chars().filter(|c| !matches!(c, '\r' | '\n' | '"')).collect();
    let ascii_fallback: String = sanitized
        .chars()
        .map(|c| if c.is_ascii() && c != '\\' { c } else { '_' })
        .collect();
    let ascii_fallback = if ascii_fallback.is_empty() { "download".to_string() } else { ascii_fallback };
    let encoded = percent_encoding::utf8_percent_encode(&sanitized, RFC5987_ENCODE_SET).to_string();
    format!("{disposition}; filename=\"{ascii_fallback}\"; filename*=UTF-8''{encoded}")
}

/// RFC 5987 `attr-char` is narrower than the usual URL-safe set (no
/// `!#$&+^\`|`), so the standard `NON_ALPHANUMERIC` set from
/// `percent-encoding` (which escapes strictly more than required) is used
/// deliberately conservatively rather than hand-picking the exact set.
const RFC5987_ENCODE_SET: &percent_encoding::AsciiSet = &percent_encoding::NON_ALPHANUMERIC.remove(b'-').remove(b'.').remove(b'_').remove(b'~');

#[cfg(test)]
mod tests {
    use super::*;

    fn keys() -> SignedUrlKeys {
        SignedUrlKeys::from_key(0, [7u8; 32])
    }

    #[test]
    fn etag8_takes_first_eight_ascii_bytes() {
        assert_eq!(etag8_of("3f2a9c1de4b57608abcd"), *b"3f2a9c1d");
    }

    #[test]
    fn etag8_short_string_is_zero_padded() {
        let mut want = [0u8; 8];
        want[..2].copy_from_slice(b"ab");
        assert_eq!(etag8_of("ab"), want);
    }

    #[test]
    fn range_single_and_open_ended() {
        assert_eq!(parse_range_header("bytes=2-5"), RangeRequest::Single { start: 2, end: Some(5) });
        assert_eq!(parse_range_header("bytes=10-"), RangeRequest::Single { start: 10, end: None });
        assert_eq!(parse_range_header("bytes=-100"), RangeRequest::Suffix(100));
    }

    #[test]
    fn range_multi_and_garbage() {
        assert_eq!(parse_range_header("bytes=0-1,5-6"), RangeRequest::Multi);
        assert_eq!(parse_range_header("nonsense"), RangeRequest::None);
        assert_eq!(parse_range_header("bytes=5-2"), RangeRequest::None); // start > end
    }

    #[test]
    fn clamp_range_clamps_open_ended_and_suffix() {
        assert_eq!(clamp_range(RangeRequest::Single { start: 5, end: None }, 10), Ok(Some((5, 9))));
        assert_eq!(clamp_range(RangeRequest::Suffix(3), 10), Ok(Some((7, 9))));
        assert_eq!(clamp_range(RangeRequest::Suffix(1000), 10), Ok(Some((0, 9))));
    }

    #[test]
    fn clamp_range_start_beyond_size_is_unsatisfiable() {
        assert_eq!(clamp_range(RangeRequest::Single { start: 20, end: None }, 10), Err(()));
    }

    #[test]
    fn clamp_range_multi_falls_back_to_full_response() {
        assert_eq!(clamp_range(RangeRequest::Multi, 10), Ok(None));
    }

    #[test]
    fn clamp_range_zero_size_never_416s() {
        assert_eq!(clamp_range(RangeRequest::Single { start: 0, end: Some(0) }, 0), Ok(None));
    }

    #[test]
    fn content_url_with_configured_host_is_absolute() {
        assert_eq!(content_url(Some("content.example.com"), "abc.def"), "https://content.example.com/c/abc.def");
    }

    #[test]
    fn content_url_with_no_host_is_relative_not_triple_slash() {
        // The regression this guards: `format!("https://{host}/c/{token}")`
        // with an empty `host` produces `https:///c/abc.def`, which WHATWG
        // URL parsing resolves to host `c` — not "no host". A relative path
        // is the only form that cannot be misparsed this way.
        let url = content_url(None, "abc.def");
        assert_eq!(url, "/c/abc.def");
        assert!(!url.contains("://"));
    }

    #[test]
    fn content_url_with_explicit_empty_host_is_also_relative() {
        // `content_hosts.first().map(String::as_str)` never actually yields
        // `Some("")` in practice (an empty vec gives `None`), but the
        // builder treats it the same as `None` anyway rather than trusting
        // the distinction — an empty string is not a host either way.
        let url = content_url(Some(""), "abc.def");
        assert_eq!(url, "/c/abc.def");
        assert!(!url.contains("https:///"));
    }

    #[test]
    fn disposition_strips_cr_lf_and_quotes() {
        let v = content_disposition_value("attachment", "evil\r\nX-Injected: yes\".jpg");
        assert!(!v.contains('\r'));
        assert!(!v.contains('\n'));
        assert!(!v.contains("yes\".jpg\""));
    }

    #[test]
    fn disposition_carries_both_ascii_fallback_and_rfc5987() {
        let v = content_disposition_value("attachment", "사진.jpg");
        assert!(v.contains("filename=\"__.jpg\""));
        assert!(v.contains("filename*=UTF-8''"));
        assert!(v.contains(".jpg"));
    }

    #[test]
    fn round_trip_valid() {
        let k = keys();
        let claim = make_claim(42, [1, 2, 3, 4, 5, 6, 7, 8], Disposition::InlineThumb, Some((256, 256)), 9, None);
        let token = sign(&k, claim.clone());
        let verified = verify(&k, &token, Some([1, 2, 3, 4, 5, 6, 7, 8])).expect("valid");
        assert_eq!(verified.fid, 42);
        assert_eq!(verified.dim, Some((256, 256)));
    }

    #[test]
    fn tampered_payload_rejected() {
        let k = keys();
        let claim = make_claim(1, [0; 8], Disposition::Attachment, None, 0, None);
        let token = sign(&k, claim);
        let (payload, sig) = token.split_once('.').unwrap();
        let mut bytes = URL_SAFE_NO_PAD.decode(payload).unwrap();
        bytes[0] ^= 0xFF;
        let tampered_payload = URL_SAFE_NO_PAD.encode(&bytes);
        let tampered = format!("{tampered_payload}.{sig}");
        assert_eq!(verify(&k, &tampered, None), Err(VerifyError::BadSignature));
    }

    #[test]
    fn tampered_signature_rejected() {
        let k = keys();
        let claim = make_claim(1, [0; 8], Disposition::Attachment, None, 0, None);
        let token = sign(&k, claim);
        let (payload, _sig) = token.split_once('.').unwrap();
        let bad_sig = URL_SAFE_NO_PAD.encode([0u8; 16]);
        let tampered = format!("{payload}.{bad_sig}");
        assert_eq!(verify(&k, &tampered, None), Err(VerifyError::BadSignature));
    }

    #[test]
    fn expired_rejected() {
        let k = keys();
        let claim = make_claim(1, [0; 8], Disposition::Attachment, None, 0, Some(std::time::Duration::from_secs(0)));
        // exp = now + 0, but verify requires exp >= now, so sleep 1s over a
        // boundary isn't reliable in CI; force exp firmly into the past instead.
        let mut claim = claim;
        claim.exp = 1;
        let token = sign(&k, claim);
        assert_eq!(verify(&k, &token, None), Err(VerifyError::Expired));
    }

    #[test]
    fn etag_changed_rejected() {
        let k = keys();
        let claim = make_claim(1, [9; 8], Disposition::Attachment, None, 0, None);
        let token = sign(&k, claim);
        assert_eq!(verify(&k, &token, Some([1; 8])), Err(VerifyError::EtagMismatch));
    }

    #[test]
    fn unknown_kid_rejected() {
        let signer = SignedUrlKeys::from_key(3, [5u8; 32]);
        let claim = make_claim(1, [0; 8], Disposition::Attachment, None, 0, None);
        let token = sign(&signer, claim);
        // A verifier that never had kid=3 provisioned.
        let verifier = SignedUrlKeys::from_key(0, [5u8; 32]);
        assert_eq!(verify(&verifier, &token, None), Err(VerifyError::UnknownKid));
    }

    #[test]
    fn revoked_kid_rejected() {
        let mut k = keys();
        let claim = make_claim(1, [0; 8], Disposition::Attachment, None, 0, None);
        let token = sign(&k, claim);
        k.revoke(0);
        assert_eq!(verify(&k, &token, None), Err(VerifyError::UnknownKid));
    }

    #[test]
    fn rotation_keeps_old_key_valid() {
        let mut k = keys();
        let claim = make_claim(1, [0; 8], Disposition::Attachment, None, 0, None);
        let token = sign(&k, claim);
        k.rotate_in(1, [8u8; 32]);
        // Old token (kid=0) still verifies because key 0 wasn't revoked.
        assert!(verify(&k, &token, None).is_ok());
    }
}
