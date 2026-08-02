//! Engine-level errors. HTTP status mapping (docs/DESIGN-UPLOAD.md §2.2)
//! lives in the HTTP layer (sc-http / sc-compat-nc); this enum only carries
//! enough structure for that layer to pick the right status code.

#[derive(thiserror::Error, Debug)]
pub enum UploadError {
    #[error("bad request: {0}")]
    BadRequest(String),

    #[error("session not found")]
    NotFound,

    #[error("offset conflict: expected {expected}, got {got}")]
    Conflict { expected: u64, got: u64 },

    #[error("session expired, pending GC")]
    Gone,

    #[error("If-Match precondition failed")]
    PreconditionFailed,

    #[error("chunk below minimum size ({min} bytes) and is not the last chunk")]
    ChunkTooSmall { min: u64 },

    #[error("offset + length exceeds declared upload length")]
    Unprocessable,

    #[error("session creation rate limit exceeded")]
    RateLimited,

    #[error("checksum mismatch")]
    ChecksumMismatch,

    #[error("resource limit exceeded: {0}")]
    ResourceExhausted(String),

    #[error("upload incomplete: cannot finalize")]
    Incomplete,

    #[error("too many disjoint received runs")]
    Fragmented,

    #[error("corrupt session state")]
    Corrupt,

    #[error(transparent)]
    Vfs(#[from] sc_vfs::VfsError),

    #[error(transparent)]
    Io(#[from] std::io::Error),

    #[error(transparent)]
    Sqlite(rusqlite::Error),
}

impl From<crate::interval_set::Fragmented> for UploadError {
    fn from(_: crate::interval_set::Fragmented) -> Self {
        UploadError::Fragmented
    }
}

impl From<crate::interval_set::Corrupt> for UploadError {
    fn from(_: crate::interval_set::Corrupt) -> Self {
        UploadError::Corrupt
    }
}

/// Hand-written rather than `#[from]`: `db.rs::row_from_sqlite` reports a
/// `received` blob that fails `IntervalSet::decode`'s re-validation through
/// `rusqlite::Error::FromSqlConversionFailure` wrapping `interval_set::Corrupt`
/// (the only way to surface a non-SQLite error through a `rusqlite::Result`
/// callback). Unwrap that shape back into `Corrupt` here so callers get the
/// real reason instead of an opaque `Sqlite` — a corrupt received-ranges blob
/// is not safe to treat as "row not found" or "fresh session" (see `db.rs`).
impl From<rusqlite::Error> for UploadError {
    fn from(e: rusqlite::Error) -> Self {
        if let rusqlite::Error::FromSqlConversionFailure(_, _, ref src) = e {
            if src.downcast_ref::<crate::interval_set::Corrupt>().is_some() {
                return UploadError::Corrupt;
            }
        }
        UploadError::Sqlite(e)
    }
}
