//! Wire-agnostic result types: `Entry`/`Listing` (read side), `OnConflict`/
//! `OpResult` (write side). These are what `sc-http`/`sc-dav` translate into
//! their own JSON/XML shapes — `sc-core` itself is protocol-agnostic
//! Directory-entry projection for the domain API.

use sc_acl::Perms;
use sc_vfs::{FileId, Kind, ShareId, Stat};

use crate::error::CoreError;

#[derive(Clone, Debug)]
pub struct Entry {
    pub name: String,
    pub kind: Kind,
    pub size: u64,
    pub mtime_ns: i128,
    pub etag: String,
    pub perms: Perms,
    /// Populated only if a stable id has already been allocated for this
    /// physical file (allocated lazily) — plain
    /// listing never forces one into existence.
    pub id: Option<FileId>,
    pub is_symlink_denied: bool,
    pub confusable: bool,
    /// `statx` btime, when the filesystem records one. Protocol layers that
    /// have a creation-time concept (WebDAV `creationdate`) need it; nothing
    /// in the core reads it.
    pub btime_ns: Option<i128>,
}

/// Free/used accounting for the filesystem backing a resolved path.
///
/// `available` is `None` when the backend genuinely cannot answer. Reporting
/// a literal `0` instead is not a harmless approximation: clients that ask
/// (macOS Finder above all) refuse every copy when free space reads zero.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Quota {
    pub used: u64,
    pub available: Option<u64>,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Sort {
    Name,
    Size,
    Mtime,
    Kind,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Order {
    Asc,
    Desc,
}

impl Order {
    pub(crate) fn apply(self, ord: std::cmp::Ordering) -> std::cmp::Ordering {
        match self {
            Order::Asc => ord,
            Order::Desc => ord.reverse(),
        }
    }
}

#[derive(Clone, Debug)]
pub struct Listing {
    pub entries: Vec<Entry>,
    pub total: usize,
    /// How many of `total` are directories. They sort first (`ops::list`), so
    /// this is also the index where files begin.
    ///
    /// The grid view needs it: folders and files are drawn as different-sized
    /// cards, so it has to know where one run ends without having loaded the
    /// rows either side of the boundary. Counted while the listing is sorted,
    /// which already knows every entry kind, so it costs nothing.
    pub dirs: usize,
    pub dir_etag: String,
    pub listing_id: String,
    pub cursor: Option<String>,
}

/// Server-side cache of a sorted name vector for one directory listing,
/// keyed by `(dir, dir_etag, sort, order)`. Kept for
/// `LISTING_TTL`; a `dir_etag` change makes the key itself miss, so there is
/// nothing to actively invalidate.
pub(crate) struct ListingSession {
    /// Kept for diagnostics/future cursor-based re-fetch, not read yet.
    #[allow(dead_code)]
    pub share: ShareId,
    #[allow(dead_code)]
    pub dir_path: String,
    pub names: Vec<String>,
    /// Directory count for the whole listing, see `Listing::dirs`. Cached with
    /// the sorted names so every page of the same listing agrees.
    pub dirs: usize,
    /// Populated only when the sort required a full stat pass (Size/Mtime/
    /// Kind —), so building the returned page can
    /// reuse these instead of stat-ing the same entries twice.
    pub stats: Option<std::collections::HashMap<String, Stat>>,
    pub created: std::time::Instant,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum OnConflict {
    Fail,
    Rename,
    Overwrite,
    Skip,
}

#[derive(Clone, Debug)]
pub struct OpResult {
    pub path: String,
    pub ok: bool,
    pub error: Option<CoreError>,
    pub will_copy: bool,
}

#[derive(Clone, Debug)]
pub struct TrashEntry {
    pub id: String,
    pub name: String,
    pub size: u64,
    /// When the entry was moved into the trash, from the inode change time
    /// the move itself set. This used to carry the file's mtime, which a
    /// move does not touch: a file last edited a year ago and deleted a
    /// minute ago was listed as "Deleted" a year ago, and nothing anywhere
    /// recorded the real answer.
    pub deleted_at_ns: i128,
}
