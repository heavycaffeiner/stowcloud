//! Backend contract.
//!
//! `sc-dav` must not reach into any concrete storage implementation: it is the
//! protocol layer and nothing else. Everything it needs from the core is
//! expressed here as an object-safe trait whose shape mirrors the agreed
//! `sc-core` / `sc-meta` API one-for-one.
//!
//! `pub type Core = dyn CoreApi` / `pub type MetaStore = dyn MetaApi` exist so
//! that the public constructor really is
//! `DavService::new(Arc<Core>, Arc<MetaStore>, DavConfig)` as specified — the
//! alias is what makes that spelling legal for a trait object.
//!
//! The concrete backend is bound in `sc-server`, which is the only crate that
//! depends on both this one and `sc-core`/`sc-meta`. Rust's orphan rule means
//! it binds them through a local newtype (`impl CoreApi for CoreBridge`)
//! rather than `impl CoreApi for sc_core::Core` directly.

use sc_vfs::{FileId, Kind, SafePath, ShareId, UserId};

/// Permissions are `sc-acl`'s, not a protocol concept: the DAV layer only ever
/// *reads* them (to decide 403 vs 404, and to refuse a download). Mirroring the
/// bitflags here would mean two definitions that must be kept bit-compatible by
/// hand, and a silent authorization bug the day they drift.
pub use sc_acl::Perms;

/// One filesystem entry as the core sees it.
#[derive(Clone, Debug)]
pub struct Entry {
    pub name: String,
    pub kind: Kind,
    pub size: u64,
    pub mtime_ns: i128,
    /// Unquoted etag. The DAV layer adds the mandatory `"` quoting.
    pub etag: String,
    pub perms: Perms,
    pub id: Option<FileId>,
    pub is_symlink_denied: bool,
    pub confusable: bool,
    /// `statx` btime when the filesystem has one. Used for `creationdate`.
    pub btime_ns: Option<i128>,
}

impl Entry {
    pub fn is_dir(&self) -> bool {
        self.kind == Kind::Dir
    }
    pub fn can_read(&self) -> bool {
        self.perms.contains(Perms::READ)
    }
}

#[derive(Clone, Debug)]
pub struct Listing {
    pub entries: Vec<Entry>,
    pub total: u64,
    /// Aggregate etag of the directory, unquoted.
    pub dir_etag: String,
    pub listing_id: u64,
    pub cursor: Option<String>,
}

#[derive(Clone, Debug)]
pub struct Resolved {
    pub share: ShareId,
    pub path: SafePath,
    pub perms: Perms,
}

/// `Core::aggregate` result — directory etag plus recursive size/count.
#[derive(Clone, Debug)]
pub struct Aggregate {
    pub etag: String,
    pub rsize: u64,
    pub rcount: u64,
}

/// RFC 4331 quota. `available` is `None` when the backend genuinely cannot
/// answer; in that case the property is reported as 404 rather than as 0,
/// because a literal 0 makes Finder refuse every copy.
#[derive(Clone, Copy, Debug)]
pub struct Quota {
    pub used: u64,
    pub available: Option<u64>,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Sort {
    Name,
    Size,
    Mtime,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Order {
    Asc,
    Desc,
}

/// Errors the core can hand back. The split between `Denied` and `NotListable`
/// is load bearing: see `DESIGN-WEBDAV.md` §8 — a path the caller may not list
/// must answer 404 whether or not it exists, and 403 is only ever legal when
/// listing *is* permitted but this particular operation is not.
#[derive(Debug, thiserror::Error)]
pub enum CoreError {
    #[error("not found")]
    NotFound,
    /// Listing is permitted, this operation is not. => 403
    #[error("forbidden")]
    Denied,
    /// Caller may not list this path at all. => 404, existence not disclosed.
    #[error("not listable")]
    NotListable,
    #[error("already exists")]
    Exists,
    #[error("directory not empty")]
    NotEmpty,
    #[error("is a directory")]
    IsDir,
    #[error("not a directory")]
    NotDir,
    #[error("no space")]
    NoSpace,
    #[error("name too long")]
    NameTooLong,
    #[error("symlink policy violation")]
    SymlinkDenied,
    #[error("read-only")]
    ReadOnly,
    #[error("invalid: {0}")]
    Invalid(String),
    #[error("io: {0}")]
    Io(String),
}

impl CoreError {
    /// `DESIGN-WEBDAV.md` §8 errno table.
    pub fn from_errno(errno: i32, overwrite: bool) -> Self {
        match errno {
            13 | 1 => CoreError::Denied,          // EACCES / EPERM
            2 => CoreError::NotFound,             // ENOENT
            17 => {
                // EEXIST
                if overwrite {
                    CoreError::Exists
                } else {
                    CoreError::NotEmpty
                }
            }
            39 | 66 => CoreError::NotEmpty,       // ENOTEMPTY (linux / bsd)
            28 | 122 => CoreError::NoSpace,       // ENOSPC / EDQUOT
            40 | 62 => CoreError::SymlinkDenied,  // ELOOP
            36 | 63 => CoreError::NameTooLong,    // ENAMETOOLONG
            30 => CoreError::ReadOnly,            // EROFS
            21 => CoreError::IsDir,               // EISDIR
            20 => CoreError::NotDir,              // ENOTDIR
            other => CoreError::Io(format!("errno {other}")),
        }
    }
}

impl From<sc_vfs::VfsError> for CoreError {
    fn from(e: sc_vfs::VfsError) -> Self {
        use sc_vfs::VfsError as V;
        match e {
            V::NotFound => CoreError::NotFound,
            V::PermissionDenied => CoreError::Denied,
            V::AlreadyExists => CoreError::Exists,
            V::NotEmpty => CoreError::NotEmpty,
            V::NoSpace => CoreError::NoSpace,
            V::CrossDevice => CoreError::Io("cross-device".into()),
            V::InvalidName(m) => CoreError::Invalid(m.to_string()),
            V::TooDeep => CoreError::Invalid("path too deep".into()),
            V::SymlinkDenied => CoreError::SymlinkDenied,
            V::UnsupportedFs(_) => CoreError::Io("unsupported filesystem".into()),
            V::Io(e) => CoreError::Io(e.to_string()),
        }
    }
}

pub type CoreResult<T> = Result<T, CoreError>;

/// The storage-facing half of the contract.
///
/// Object safe on purpose — `DavService` holds `Arc<dyn CoreApi>`.
pub trait CoreApi: Send + Sync {
    fn resolve(&self, user: UserId, vpath: &str) -> CoreResult<Resolved>;
    fn list(&self, user: UserId, vpath: &str, sort: Sort, order: Order) -> CoreResult<Listing>;
    fn stat_entry(&self, user: UserId, vpath: &str) -> CoreResult<Entry>;

    fn mkdir(&self, user: UserId, vpath: &str) -> CoreResult<()>;
    fn rename(&self, user: UserId, from: &str, to: &str) -> CoreResult<()>;
    fn move_entries(&self, user: UserId, from: &[String], to_dir: &str) -> CoreResult<()>;
    fn copy_entries(&self, user: UserId, from: &[String], to_dir: &str) -> CoreResult<()>;
    fn delete(&self, user: UserId, vpath: &str) -> CoreResult<()>;

    fn read_text(&self, user: UserId, vpath: &str) -> CoreResult<String>;
    fn write_text(&self, user: UserId, vpath: &str, data: &str) -> CoreResult<()>;

    fn aggregate(&self, share: ShareId, path: &SafePath) -> anyhow::Result<Aggregate>;

    // ---- extensions the DAV layer needs beyond the text-oriented core API ----

    /// Binary read. Defaults to `read_text`, which is correct but lossy for
    /// non-UTF-8 payloads; every real backend overrides this.
    fn read_bytes(&self, user: UserId, vpath: &str) -> CoreResult<Vec<u8>> {
        self.read_text(user, vpath).map(String::into_bytes)
    }

    /// Binary write, replacing atomically. Defaults to `write_text`.
    fn write_bytes(&self, user: UserId, vpath: &str, data: &[u8]) -> CoreResult<()> {
        let s = std::str::from_utf8(data)
            .map_err(|_| CoreError::Invalid("non-UTF-8 body unsupported by backend".into()))?;
        self.write_text(user, vpath, s)
    }

    /// Copy `from` to the exact path `to`.
    ///
    /// Required, not defaulted. WebDAV COPY names its destination, which
    /// `copy_entries` — "copy these into that directory, keeping their names"
    /// — cannot express, and the obvious composition (`copy_entries` then
    /// `rename`) **destroys the source** when the destination lands in the
    /// source's own directory, which is exactly what Finder's "Duplicate"
    /// does. There is no safe default; every backend implements it.
    fn copy_to(&self, user: UserId, from: &str, to: &str) -> CoreResult<()>;

    /// RFC 4331 quota for the collection containing `vpath`.
    fn quota(&self, _user: UserId, _vpath: &str) -> CoreResult<Quota> {
        Ok(Quota {
            used: 0,
            available: None,
        })
    }

    /// Create an empty file. Used by LOCK on a non-existent path (RFC 4918
    /// deprecates lock-null resources in favour of creating an empty one).
    fn create_empty(&self, user: UserId, vpath: &str) -> CoreResult<()> {
        self.write_bytes(user, vpath, b"")
    }
}

/// See module docs — this alias is what makes `Arc<Core>` spell a trait object.
pub type Core = dyn CoreApi;

/// A dead property as stored by `sc-meta`, keyed by `FileId`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DavProp {
    pub ns: String,
    pub name: String,
    /// Already-normalised text. Never the client's raw XML.
    pub value: String,
}

pub trait MetaApi: Send + Sync {
    fn get_props(&self, id: FileId) -> anyhow::Result<Vec<DavProp>>;
    fn set_prop(&self, id: FileId, ns: &str, name: &str, value: &str) -> anyhow::Result<()>;
    fn del_prop(&self, id: FileId, ns: &str, name: &str) -> anyhow::Result<()>;
}

pub type MetaStore = dyn MetaApi;
