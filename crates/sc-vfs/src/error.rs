use thiserror::Error;

use crate::types::FsType;

#[derive(Debug, Error)]
pub enum VfsError {
    #[error("not found")]
    NotFound,
    #[error("permission denied")]
    PermissionDenied,
    #[error("already exists")]
    AlreadyExists,
    #[error("directory not empty")]
    NotEmpty,
    #[error("no space left on device")]
    NoSpace,
    #[error("cross-device link")]
    CrossDevice,
    #[error("invalid name: {0}")]
    InvalidName(&'static str),
    #[error("path too deep")]
    TooDeep,
    #[error("symlink traversal denied")]
    SymlinkDenied,
    #[error("unsupported filesystem: {0:?}")]
    UnsupportedFs(FsType),
    #[error("io error: {0}")]
    Io(std::io::Error),
}

impl VfsError {
    /// Map a `std::io::Error` to the closest `VfsError` variant. Kept as an
    /// explicit function (rather than a blanket `From` impl) so call sites
    /// can see, at a glance, that the mapping is a best-effort classification
    /// and not a lossless conversion.
    pub(crate) fn from_io(e: std::io::Error) -> Self {
        use std::io::ErrorKind;
        match e.kind() {
            ErrorKind::NotFound => VfsError::NotFound,
            ErrorKind::PermissionDenied => VfsError::PermissionDenied,
            ErrorKind::AlreadyExists => VfsError::AlreadyExists,
            ErrorKind::DirectoryNotEmpty => VfsError::NotEmpty,
            ErrorKind::StorageFull => VfsError::NoSpace,
            ErrorKind::CrossesDevices => VfsError::CrossDevice,
            _ => {
                if let Some(errno) = e.raw_os_error() {
                    match errno {
                        // ENOSPC
                        #[cfg(unix)]
                        28 => return VfsError::NoSpace,
                        // EXDEV
                        #[cfg(unix)]
                        18 => return VfsError::CrossDevice,
                        // ENOTEMPTY
                        #[cfg(unix)]
                        39 => return VfsError::NotEmpty,
                        _ => {}
                    }
                }
                VfsError::Io(e)
            }
        }
    }
}

impl From<std::io::Error> for VfsError {
    fn from(e: std::io::Error) -> Self {
        VfsError::from_io(e)
    }
}
