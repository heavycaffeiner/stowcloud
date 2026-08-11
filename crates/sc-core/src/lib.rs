//! `sc-core` — the protocol-agnostic domain API assembled from `sc-vfs`
//! (safe filesystem access), `sc-meta` (fileid/ETag cache), and `sc-acl`
//! (permission evaluation). `sc-http`/`sc-dav`/`sc-compat-nc` are thin
//! translations on top of this; none of them touch `sc-vfs` or `sc-acl`
//! directly.

mod acl_store;
mod aggregate;
mod archive;
mod argon_gate;
mod entry;
mod error;
mod homes;
mod links;
mod ops;
mod path;
mod quota;
mod resolve;
mod share;
mod stream;
mod trash;

#[cfg(test)]
mod tests;
#[cfg(test)]
mod tests_acl_store;
#[cfg(test)]
mod tests_homes;
#[cfg(test)]
mod tests_links;
#[cfg(test)]
mod tests_share;
#[cfg(test)]
mod tests_stream;

pub use acl_store::{
    perms_from_names, perms_to_names, AclStore, GrantFilter, GrantPatch, GrantRecord, GrantSpec, PERM_NAMES,
};
pub use archive::WalkEntry;
pub use entry::{Entry, Listing, OnConflict, OpResult, Order, Quota, Sort, TrashEntry};
pub use error::CoreError;
pub use sc_acl::{Decision, Perms, Principal, RootEntry};
pub use sc_meta::Aggregate;
pub use links::{
    token_hash, LinkArchiveVisit, LinkNode, LinkPatch, LinkSpec, LinkStore, ShareLink,
};
pub use path::{SharePath, Vpath};
pub use quota::QuotaSink;
pub use resolve::Resolved;
pub use share::{probe_access, AccessProbe, ShareDef, ShareStore, DYNAMIC_SHARE_ID_BASE};
pub use stream::{CoreFileStream, FidEntry, SeekableFile, CHUNK};

use std::sync::atomic::AtomicU64;
use std::sync::Arc;

use dashmap::DashMap;
use sc_acl::AclEngine;
use sc_meta::MetaStore;
use sc_vfs::{FileId, ShareId};
use parking_lot::Mutex;

use entry::ListingSession;
use share::ShareEntry;

pub struct Core {
    pub(crate) meta: Arc<MetaStore>,
    pub(crate) acl: Arc<AclEngine>,
    pub(crate) shares: DashMap<ShareId, Arc<ShareEntry>>,
    pub(crate) listings: Mutex<lru::LruCache<String, Arc<ListingSession>>>,
    /// Single-flight guards for directory aggregate computation,
    /// keyed by the directory's fileid.
    pub(crate) inflight: DashMap<FileId, Arc<Mutex<()>>>,
    pub(crate) stat_calls: AtomicU64,
    /// Share-link store (`links.rs`). Attached after construction rather than
    /// passed to `new` because it is optional: a deployment with links turned
    /// off answers `CoreError::NotSupported`, which the protocol layers render
    /// as "not implemented" — never as an accepted-then-dropped share.
    pub(crate) links: std::sync::OnceLock<Arc<links::LinkStore>>,
    /// Grant store (`acl_store.rs`). Same optional-attach shape as `links`
    /// above, and the same reason: a `Core` built by a test fixture that
    /// never calls `attach_acl_store` still works, it just has no grants —
    /// which is a legitimate (if useless) deployment state, not a bug.
    pub(crate) acl_store: std::sync::OnceLock<Arc<acl_store::AclStore>>,
    /// Admin-created share store (`share.rs`). Same optional-attach shape as
    /// `links`/`acl_store` above: a `Core` with none attached still works,
    /// it just has only the shares `sc-server::app::register_shares` loaded
    /// from `config.toml`.
    pub(crate) share_store: std::sync::OnceLock<Arc<share::ShareStore>>,
    /// Per-user quota sink (`quota.rs`). Same optional-attach shape as the
    /// three stores above: a `Core` with none attached enforces no quota.
    pub(crate) quota_sink: std::sync::OnceLock<Arc<dyn quota::QuotaSink>>,
    /// Serializes the rare (once-per-user) slow path of `homes::ensure_home`
    /// — see its doc comment. Unrelated to `acl`'s own locking.
    pub(crate) home_lock: Mutex<()>,
}

impl Core {
    pub fn new(meta: Arc<MetaStore>, acl: Arc<AclEngine>) -> Self {
        Self {
            meta,
            acl,
            shares: DashMap::new(),
            listings: Mutex::new(ops::new_listing_cache()),
            inflight: DashMap::new(),
            stat_calls: AtomicU64::new(0),
            links: std::sync::OnceLock::new(),
            acl_store: std::sync::OnceLock::new(),
            share_store: std::sync::OnceLock::new(),
            quota_sink: std::sync::OnceLock::new(),
            home_lock: Mutex::new(()),
        }
    }
}
