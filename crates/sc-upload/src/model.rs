//! Public types for the upload engine. See docs/

use std::sync::atomic::{AtomicU64, Ordering};

use sc_vfs::{ShareId, UserId};

/// Hard floor for `chunk_size_min` — never settable below this, whether from
/// `sc.toml` or an admin's runtime write (`UploadEngine::set_chunk_settings`).
/// `crates/sc-server/src/config.rs::CHUNK_MIN_BYTES_FLOOR` is defined in terms
/// of this constant so the two never drift apart.
pub const CHUNK_MIN_BYTES_FLOOR: u64 = 5 * 1024 * 1024;

/// Opaque, unguessable session handle. 16 CSPRNG bytes, base64url-encoded
/// (22 chars) for transport. Unguessability matters: it is the only thing
/// standing between a `HEAD`/`PATCH` request and hijacking someone else's
/// upload, on top of the owner check we also perform.
#[derive(Clone, Copy, PartialEq, Eq, Hash)]
pub struct SessionId(pub [u8; 16]);

impl SessionId {
    pub fn new_random() -> Self {
        let mut b = [0u8; 16];
        getrandom::getrandom(&mut b).expect("OS CSPRNG unavailable");
        SessionId(b)
    }

    pub fn as_bytes(&self) -> &[u8; 16] {
        &self.0
    }

    pub fn to_b64(&self) -> String {
        use base64::Engine;
        base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(self.0)
    }

    pub fn parse_b64(s: &str) -> Option<Self> {
        use base64::Engine;
        let v = base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(s).ok()?;
        let arr: [u8; 16] = v.try_into().ok()?;
        Some(SessionId(arr))
    }
}

impl std::fmt::Debug for SessionId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "SessionId({})", self.to_b64())
    }
}

impl std::fmt::Display for SessionId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.to_b64())
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum SessionState {
    Receiving,
    Finalizing,
    Done,
    Aborted,
    Expired,
}

/// How chunks map onto the part file. See docs/
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum SpoolMode {
    /// Native TUS: client tells us the offset, so no assembly is needed.
    OffsetAddressed,
    /// Compat chunking v2: chunks are assembled in ascending name order.
    NameOrdered,
}

/// Whole-file verification algorithm, opt-in via `UploadMeta::verify`.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum VerifyAlgo {
    Crc32c,
    Blake3,
}

/// Which hash a per-chunk [`Checksum`] carries — the same two algorithms
/// `Tus-Checksum-Algorithm` advertises. Separate from [`Checksum`] itself
/// because [`Checksum::compute`] needs to name an algorithm *before* there is
/// a digest to attach it to.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum ChecksumAlgo {
    Crc32c,
    Blake3,
}

/// Per-chunk checksum, TUS `Upload-Checksum` extension.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Checksum {
    Crc32c(u32),
    Blake3([u8; 32]),
}

impl Checksum {
    /// Compute this checksum kind over `data` and compare.
    pub fn matches(&self, data: &[u8]) -> bool {
        match self {
            Checksum::Crc32c(expect) => crc32c::crc32c(data) == *expect,
            Checksum::Blake3(expect) => blake3::hash(data).as_bytes() == expect,
        }
    }

    /// Build the checksum `data` actually hashes to under `algo` — the
    /// inverse of [`Checksum::matches`]: that asks "does this checksum,
    /// already in hand, match these bytes"; this builds one from bytes that
    /// exist but have no checksum attached yet. A wire-side header parser
    /// only ever needs `matches` (the digest arrives already-formed in
    /// `Upload-Checksum`); this exists for the other direction — a caller
    /// that holds the plaintext and needs to *assert* a checksum for it
    /// (e.g. a test, or client-side tooling).
    pub fn compute(algo: ChecksumAlgo, data: &[u8]) -> Self {
        match algo {
            ChecksumAlgo::Crc32c => Checksum::Crc32c(crc32c::crc32c(data)),
            ChecksumAlgo::Blake3 => Checksum::Blake3(*blake3::hash(data).as_bytes()),
        }
    }
}

#[derive(Clone, Debug, Default)]
pub struct UploadMeta {
    pub filename: String,
    pub mtime_ns: Option<i128>,
    pub mime: Option<String>,
    pub relative_path: Option<String>,
    /// Opt-in whole-file check at finalize: algorithm plus the digest the
    /// caller expects the finished file to hash to. The shape that shipped
    /// before this fix, `Option<VerifyAlgo>`, carried an algorithm and
    /// nothing to compare it to — `UploadEngine::verify_whole_file` computed
    /// a digest and only logged it, so `verify` could never fail no matter
    /// what arrived on disk. `docs/ records that gap and
    /// specifies this exact `(algo, expected digest)` shape as the fix.
    pub verify: Option<(VerifyAlgo, Vec<u8>)>,
}

#[derive(Clone)]
pub struct SessionSpec {
    pub user: UserId,
    pub share: ShareId,
    pub dest: sc_vfs::SafePath,
    pub total_len: Option<u64>,
    pub random_access: bool,
    pub if_match: Option<String>,
    pub mode: SpoolMode,
    pub meta: UploadMeta,
}

#[derive(Clone, Debug)]
pub struct UploadConfig {
    /// Enforced minimum chunk size, except the last chunk and files smaller
    /// than this. 5 MiB.
    pub chunk_size_min: u64,
    /// Advertised default chunk size for clients that ask. 10 MiB.
    pub chunk_size_default: u64,
    /// Environment-detected recommendation (e.g. behind Cloudflare -> lower).
    /// Purely advisory — NEVER enforced server-side.
    pub chunk_size_advisory: u64,
    pub parallel: u32,
    pub max_sessions_per_user: u32,
    pub max_reserved_bytes_per_user: u64,
    pub session_ttl: std::time::Duration,
    pub body_idle_timeout: std::time::Duration,
    pub free_space_margin: u64,
}

impl Default for UploadConfig {
    fn default() -> Self {
        const MIB: u64 = 1024 * 1024;
        const GIB: u64 = 1024 * MIB;
        Self {
            chunk_size_min: 5 * MIB,
            chunk_size_default: 10 * MIB,
            chunk_size_advisory: 10 * MIB,
            parallel: 4,
            max_sessions_per_user: 32,
            max_reserved_bytes_per_user: 100 * GIB,
            session_ttl: std::time::Duration::from_secs(24 * 3600),
            body_idle_timeout: std::time::Duration::from_secs(60),
            free_space_margin: 2 * GIB,
        }
    }
}

/// Live, admin-settable chunk floor/default: the
/// only two `UploadConfig` fields that can change after `UploadEngine::new()`
/// without a restart. Plain atomics rather than a `Mutex` — every reader
/// (capabilities, `Sc-Chunk-Size`, `create()`) just needs the current numbers,
/// nothing coordinates the two fields against each other, and a reader
/// observing a torn read across one admin write is harmless (worst case,
/// `create()` snapshots a `chunk_min_at_creation` from a value one write away
/// from what `default_size()` used for the same session — no different from
/// two sessions created a moment apart).
pub struct ChunkSettings {
    min: AtomicU64,
    default: AtomicU64,
}

impl ChunkSettings {
    pub fn new(min: u64, default: u64) -> Self {
        Self { min: AtomicU64::new(min), default: AtomicU64::new(default) }
    }

    pub fn min(&self) -> u64 {
        self.min.load(Ordering::Relaxed)
    }

    pub fn default_size(&self) -> u64 {
        self.default.load(Ordering::Relaxed)
    }

    pub fn set(&self, min: u64, default: u64) {
        self.min.store(min, Ordering::Relaxed);
        self.default.store(default, Ordering::Relaxed);
    }
}

/// Snapshot returned by `head()`.
#[derive(Clone, Debug)]
pub struct SessionStatus {
    pub id: SessionId,
    /// Which share the finished file lands in. The HTTP layer holds only the
    /// session id (it is the whole of the TUS URL), so without this it cannot
    /// produce the `&ShareRoot` that `patch`/`finalize` require — it would
    /// have to keep its own id→share table and lose it on restart.
    pub share: ShareId,
    pub state: SessionState,
    /// Contiguous prefix from 0 — the TUS `Upload-Offset` semantics, valid
    /// for both sequential and random-access sessions (see §2.3).
    pub offset: u64,
    pub total_len: Option<u64>,
    pub chunk_size: u32,
    pub run_count: usize,
    pub expires_ns: i128,
    pub random_access: bool,
    /// Bytes actually spooled so far, whichever addressing mode the session
    /// uses. `offset` only counts `received`, which a `NameOrdered` session
    /// never populates until assembly finishes — so a caller asking "how much
    /// has arrived?" mid-upload got 0 and rejected a perfectly good assembly.
    pub received_bytes: u64,
}
