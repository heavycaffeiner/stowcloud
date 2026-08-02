//! `CoreError` — the protocol-agnostic error type every `sc-core` operation
//! returns. `sc-http`/`sc-dav` map this to their own error envelopes;
//! it deliberately mirrors that envelope's `code`
//! space closely enough that the mapping is mechanical.

use sc_vfs::VfsError;

#[derive(Clone, Debug, thiserror::Error)]
pub enum CoreError {
    #[error("not found")]
    NotFound,
    #[error("invalid path: {0}")]
    InvalidPath(String),
    /// `by` is the grant id responsible for the denial, if any (vs. implicit
    /// default-deny).
    #[error("permission denied")]
    Denied { by: Option<u32> },
    #[error("conflict: destination already exists")]
    Conflict,
    /// `If-Match` didn't match the current on-disk ETag. Carries the current
    /// value so the caller can show a conflict-resolution UI without a
    /// second round-trip.
    #[error("precondition failed")]
    Precondition { current_etag: String },
    #[error("cross-device")]
    CrossDevice,
    /// The resource existed but is permanently unavailable — a share link
    /// whose target moved or was replaced, expired, or hit its download cap
    /// ("a mismatch invalidates the link with
    /// `410 Gone`"). Distinct from `NotFound` because the caller must *not*
    /// retry or fall back.
    #[error("gone")]
    Gone,
    /// The operation is well-formed but this deployment does not provide it
    /// (e.g. share links with no link store configured). Never used to paper
    /// over a failure — a caller told "not supported" knows nothing happened.
    #[error("not supported: {0}")]
    NotSupported(String),
    #[error("io error: {0}")]
    Io(String),
    #[error("internal error: {0}")]
    Internal(String),
    /// The write would push the acting user's usage ledger past their
    /// configured cap (`FEATURES.md` #49, `quota.rs`). Distinct from
    /// `Denied` — this is a resource limit, not an ACL decision.
    #[error("quota exceeded")]
    QuotaExceeded,
    /// A refusal a human has to read. `key` is a stable catalogue key and
    /// `params` its placeholders; `message` is the English rendering for logs
    /// and non-browser callers. Separate from `InvalidPath` because that
    /// variant's payload is prose, and prose cannot be translated at the far
    /// end — the browser would have to match on it by substring.
    #[error("{message}")]
    Invalid { key: &'static str, params: Vec<(&'static str, String)>, message: String },
}

impl CoreError {
    /// Shorthand for [`CoreError::Invalid`].
    pub fn invalid(key: &'static str, params: Vec<(&'static str, String)>, message: impl Into<String>) -> Self {
        CoreError::Invalid { key, params, message: message.into() }
    }

    /// The stable catalogue key, for the one variant that has one. The other
    /// variants are already distinguished by their own `ErrorCode` once
    /// they cross into `sc-http`, so they need no key of their own here.
    pub fn key(&self) -> Option<&'static str> {
        match self {
            CoreError::Invalid { key, .. } => Some(key),
            _ => None,
        }
    }
}

impl From<VfsError> for CoreError {
    fn from(e: VfsError) -> Self {
        match e {
            VfsError::NotFound => CoreError::NotFound,
            VfsError::AlreadyExists => CoreError::Conflict,
            VfsError::CrossDevice => CoreError::CrossDevice,
            VfsError::InvalidName(s) => CoreError::InvalidPath(s.to_string()),
            VfsError::TooDeep => CoreError::InvalidPath("path too deep".to_string()),
            VfsError::PermissionDenied => CoreError::Denied { by: None },
            VfsError::SymlinkDenied => CoreError::Denied { by: None },
            other => CoreError::Io(other.to_string()),
        }
    }
}

impl From<anyhow::Error> for CoreError {
    fn from(e: anyhow::Error) -> Self {
        CoreError::Internal(e.to_string())
    }
}

impl From<std::io::Error> for CoreError {
    fn from(e: std::io::Error) -> Self {
        CoreError::from(VfsError::from(e))
    }
}
