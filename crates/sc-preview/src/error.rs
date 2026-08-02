//! Error types for `sc-preview`.
//!
//! Two error surfaces are deliberately kept distinct:
//!
//! - [`PreviewError`] — the "real" error, returned by the code paths that
//!   actually attempted generation. It can carry a formatted message (from an
//!   underlying decoder/encoder error, which isn't `Clone`).
//! - [`NegativeReason`] — a small `Copy` classification of *why* generation
//!   failed, cheap enough to stash in the negative cache (`DESIGN-PREVIEW.md`
//!   §6, `preview_negative` table) and to hand back to every caller that hits
//!   the negative cache within the TTL without re-running the generator.

use serde::{Deserialize, Serialize};
use thiserror::Error;

#[derive(Debug, Error)]
pub enum PreviewError {
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),

    #[error("image decode error: {0}")]
    Decode(String),

    #[error(
        "declared dimensions {width}x{height} ({pixels} pixels) exceed max_pixels {max_pixels}"
    )]
    DecodeBomb {
        width: u32,
        height: u32,
        pixels: u64,
        max_pixels: u64,
    },

    #[error("unrecognized or unsupported input format")]
    UnsupportedFormat,

    #[error("image encode error: {0}")]
    Encode(String),

    #[error("archive rejected: {0}")]
    ArchiveRejected(String),

    #[error("negative-cached failure ({0:?}), not retrying within TTL")]
    NegativeCached(NegativeReason),

    #[error("worker pool error: {0}")]
    Worker(String),

    #[error("not implemented: {0}")]
    Unimplemented(String),

    /// A job-level failure that crossed the worker boundary (`worker::JobResult::Err`).
    ///
    /// `kind` is carried over the wire as-is (`PreviewError::classify()` ran
    /// *inside* the worker, where the original, fully-typed error — e.g.
    /// `DecodeBomb`'s width/height — was still available) rather than
    /// re-derived from `reason` on this side. Reconstructing one of the
    /// other variants instead (say, always `Decode(reason)`) would silently
    /// re-collapse every job failure back to `NegativeReason::DecodeError`
    /// regardless of what actually happened — exactly the bug this variant
    /// exists to close: a video file used to come back from the worker as
    /// `JobResult::Err { reason: "video preview generation is not \
    /// implemented in this build" }` and still get classified as a decode
    /// error, because the only thing crossing the wire was a string.
    #[error("{reason}")]
    FromWorker { kind: NegativeReason, reason: String },
}

impl PreviewError {
    /// Best-effort classification into the small set of reasons we're
    /// willing to persist in the negative cache. Not a lossless mapping —
    /// the point is a compact `INTEGER reason` column
    /// (`DESIGN-PREVIEW.md` §6), not preserving the original message.
    pub fn classify(&self) -> NegativeReason {
        match self {
            PreviewError::Io(_) => NegativeReason::Io,
            PreviewError::Decode(_) => NegativeReason::DecodeError,
            PreviewError::DecodeBomb { .. } => NegativeReason::DecodeBomb,
            PreviewError::UnsupportedFormat => NegativeReason::UnsupportedFormat,
            PreviewError::Encode(_) => NegativeReason::EncodeError,
            PreviewError::ArchiveRejected(_) => NegativeReason::ArchiveRejected,
            PreviewError::NegativeCached(r) => *r,
            PreviewError::Worker(_) => NegativeReason::WorkerError,
            PreviewError::Unimplemented(_) => NegativeReason::Unimplemented,
            PreviewError::FromWorker { kind, .. } => *kind,
        }
    }
}

/// `Copy`/`Clone` failure classification, safe to store in the negative
/// cache and to hand out to every caller sharing the same in-flight slot.
/// Also crosses the worker/jail boundary as part of `worker::JobResult::Err`
/// (`Serialize`/`Deserialize`, postcard-encoded) — see [`PreviewError::FromWorker`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum NegativeReason {
    Io,
    DecodeError,
    DecodeBomb,
    UnsupportedFormat,
    EncodeError,
    ArchiveRejected,
    WorkerError,
    Unimplemented,
}
