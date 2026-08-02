//! sc-upload — resumable upload engine: TUS 1.0.0 core plus the engine side
//! of the compat chunking v2 mapping. See `docs/.
//!
//! The HTTP surface (routing, header parsing, status codes) lives in
//! sc-http / sc-compat-nc; this crate only knows about `SessionSpec` in and
//! `Result<_, UploadError>` out.

mod db;
mod engine;
mod error;
mod interval_set;
mod model;

pub use engine::{file_etag, UploadEngine};
pub use error::UploadError;
pub use interval_set::{Corrupt, Fragmented, IntervalSet};
pub use model::{
    Checksum, ChecksumAlgo, SessionId, SessionSpec, SessionState, SessionStatus, SpoolMode, UploadConfig,
    UploadMeta, VerifyAlgo, CHUNK_MIN_BYTES_FLOOR,
};
