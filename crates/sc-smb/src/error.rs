//! Error type for the whole crate.

use std::net::IpAddr;
use std::path::PathBuf;

#[derive(Debug, thiserror::Error)]
pub enum SmbError {
    /// The hard gate from /:
    /// SMB shares the account password (NTLM needs the NT hash), and the
    /// trade-off documented there is only accepted under the premise that
    /// SMB never leaves the LAN. This is refused, not warned, unless the
    /// operator explicitly opts in via `allow_public_bind`.
    #[error(
        "SMB is LAN-only: refusing to generate smb.conf for public bind address(es) {offending:?}. \
         Use WebDAV or a VPN for remote access, or set smb.allow_public_bind = true to override \
         (this posts a permanent admin-UI warning and an audit event)"
    )]
    PublicBindRefused { offending: Vec<IpAddr> },

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
