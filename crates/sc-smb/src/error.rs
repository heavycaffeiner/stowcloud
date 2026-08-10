//! Error type for the whole crate.

use std::path::PathBuf;

#[derive(Debug, thiserror::Error)]
pub enum SmbError {
    #[error("invalid share definition {name:?}: {reason}")]
    InvalidShare { name: String, reason: String },

    #[error("invalid smb.server_name {name:?}: {reason}")]
    InvalidServerName { name: String, reason: String },

    #[error("invalid smb.interfaces entry {value:?}: {reason}")]
    InvalidInterface { value: String, reason: String },

    #[error("io error at {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: std::io::Error,
    },
}
