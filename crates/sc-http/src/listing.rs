//! Directory listing sessions — `DESIGN-API.md` §4.2.
//!
//! A server-side short-lived cache of a **sorted name vector** for a
//! directory, so paging through a 100k-entry directory doesn't re-sort on
//! every page. Name-sort pages `statx` only the 200 names on the requested
//! page; size/mtime sort needs a full stat pass up front (handled by the
//! caller before constructing the `Listing`, via the `sync_stat_limit` /
//! job-queue fallback — this module only owns the cache/pagination
//! mechanics, not the stat strategy).

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use lru::LruCache;
use parking_lot::Mutex;

use crate::core_api::{Entry, Order, SortKey};

pub const PAGE_SIZE_DEFAULT: usize = 200;
pub const SESSION_TTL: Duration = Duration::from_secs(60);
/// Per-user cap on live listing sessions (`DESIGN-API.md` §4.2: "cap of 4 per user").
pub const PER_USER_CAP: usize = 4;
/// Global memory cap: keep at most this many listing sessions across all
/// users regardless of per-user caps, bounding worst-case memory.
pub const GLOBAL_CAP: usize = 4096;

#[derive(Clone)]
pub struct ListingSession {
    pub dir_etag: String,
    pub sort: SortKey,
    pub order: Order,
    /// Already-sorted full entry set for this directory at creation time.
    /// `DESIGN-API.md` puts only *names* in the session and stats each page
    /// lazily; we keep already-materialized `Entry`s here since sc-http
    /// doesn't yet have a live stat backend to call per-page (`sc-core` is a
    /// placeholder — see `core_api.rs`). Swapping to lazy per-page `statx`
    /// is a matter of storing `Vec<CompactString>` here instead and calling
    /// back into `CoreApi::stat_entry` per page.
    pub entries: Vec<Entry>,
    created: Instant,
}

impl ListingSession {
    pub fn is_expired(&self) -> bool {
        self.created.elapsed() > SESSION_TTL
    }
}

fn new_listing_id() -> String {
    let mut buf = [0u8; 12];
    getrandom::getrandom(&mut buf).expect("getrandom failed");
    format!("L-{}", URL_SAFE_NO_PAD.encode(buf))
}

#[derive(Clone, Copy, serde::Serialize, serde::Deserialize)]
struct Cursor {
    i: usize,
}

fn encode_cursor(i: usize) -> String {
    let c = Cursor { i };
    let bytes = postcard::to_allocvec(&c).expect("Cursor postcard-serializable");
    URL_SAFE_NO_PAD.encode(bytes)
}

fn decode_cursor(s: &str) -> Option<usize> {
    let bytes = URL_SAFE_NO_PAD.decode(s).ok()?;
    let c: Cursor = postcard::from_bytes(&bytes).ok()?;
    Some(c.i)
}

#[derive(Debug)]
pub struct ListingPage {
    pub listing_id: String,
    pub total: u64,
    pub entries: Vec<Entry>,
    pub cursor: Option<String>,
    pub dir_etag: String,
    /// Set when the directory's etag changed since the cached session was
    /// created — caller should send `Sc-Listing-Stale: 1`.
    pub stale: bool,
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum ListingError {
    #[error("listing session expired")]
    Expired,
}

struct PerUser {
    order: std::collections::VecDeque<String>,
}

/// `LISTINGS` from `DESIGN-API.md` §4.2, plus the per-user cap it specifies.
pub struct ListingCache {
    sessions: Mutex<LruCache<String, ListingSession>>,
    per_user: Mutex<std::collections::HashMap<u32, PerUser>>,
    counter: AtomicU64,
}

impl Default for ListingCache {
    fn default() -> Self {
        Self::new()
    }
}

impl ListingCache {
    pub fn new() -> Self {
        Self {
            sessions: Mutex::new(LruCache::new(std::num::NonZeroUsize::new(GLOBAL_CAP).unwrap())),
            per_user: Mutex::new(std::collections::HashMap::new()),
            counter: AtomicU64::new(0),
        }
    }

    /// Create a brand-new page 1 session for `user`, evicting the user's
    /// oldest session if they're already at `PER_USER_CAP`.
    pub fn create(&self, user: u32, dir_etag: String, sort: SortKey, order: Order, all_entries: Vec<Entry>, page_size: usize) -> ListingPage {
        self.counter.fetch_add(1, Ordering::Relaxed);
        let id = new_listing_id();
        let total = all_entries.len() as u64;
        let first_page: Vec<Entry> = all_entries.iter().take(page_size).cloned().collect();
        let cursor = if all_entries.len() > page_size { Some(encode_cursor(page_size)) } else { None };

        let session = ListingSession { dir_etag: dir_etag.clone(), sort, order, entries: all_entries, created: Instant::now() };

        {
            let mut sessions = self.sessions.lock();
            let mut per_user = self.per_user.lock();
            let entry = per_user.entry(user).or_insert_with(|| PerUser { order: Default::default() });
            entry.order.push_back(id.clone());
            while entry.order.len() > PER_USER_CAP {
                if let Some(old_id) = entry.order.pop_front() {
                    sessions.pop(&old_id);
                }
            }
            sessions.put(id.clone(), session);
        }

        ListingPage { listing_id: id, total, entries: first_page, cursor, dir_etag, stale: false }
    }

    /// Fetch the next page for an existing session. `current_dir_etag` is
    /// checked against the cached one; a mismatch discards the session and
    /// returns a *fresh first page* with `stale = true` instead of erroring
    /// (`DESIGN-API.md` §4.2: "the client refreshes silently while keeping
    /// its scroll position" — the caller is responsible for re-listing to build
    /// `fresh_entries` when `dir_etag` mismatches; this method only flags it).
    pub fn page(
        &self,
        user: u32,
        listing_id: &str,
        cursor: &str,
        current_dir_etag: &str,
        page_size: usize,
    ) -> Result<ListingPage, ListingError> {
        let _ = user;
        // Clone the session out from behind the lock immediately — avoids
        // holding a borrow of `sessions` across the later `pop()` calls.
        let session: ListingSession = {
            let mut sessions = self.sessions.lock();
            match sessions.get(listing_id) {
                Some(s) if !s.is_expired() => s.clone(),
                Some(_) => {
                    sessions.pop(listing_id);
                    return Err(ListingError::Expired);
                }
                None => return Err(ListingError::Expired),
            }
        };
        let start = decode_cursor(cursor).ok_or(ListingError::Expired)?;

        if session.dir_etag != current_dir_etag {
            // Discard and let the caller re-list; signal staleness.
            self.sessions.lock().pop(listing_id);
            return Ok(ListingPage {
                listing_id: listing_id.to_string(),
                total: session.entries.len() as u64,
                entries: session.entries.iter().take(page_size).cloned().collect(),
                cursor: if session.entries.len() > page_size { Some(encode_cursor(page_size)) } else { None },
                dir_etag: session.dir_etag,
                stale: true,
            });
        }

        let total = session.entries.len() as u64;
        let page: Vec<Entry> = session.entries.iter().skip(start).take(page_size).cloned().collect();
        let next = start + page.len();
        let cursor = if next < session.entries.len() { Some(encode_cursor(next)) } else { None };
        Ok(ListingPage { listing_id: listing_id.to_string(), total, entries: page, cursor, dir_etag: session.dir_etag.clone(), stale: false })
    }

    pub fn session_count(&self) -> usize {
        self.sessions.lock().len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::core_api::Entry;
    use sc_acl::Perms;

    fn mk_entry(name: &str) -> Entry {
        Entry {
            name: name.to_string(),
            kind: crate::core_api::Kind::File,
            size: 0,
            mtime_ns: "0".to_string(),
            etag: "e".to_string(),
            perms: Perms::READ,
            id: None,
            preview: None,
            link: None,
            confusable: false,
        }
    }

    #[test]
    fn pagination_walks_all_entries() {
        let cache = ListingCache::new();
        let entries: Vec<Entry> = (0..450).map(|i| mk_entry(&format!("f{i}"))).collect();
        let page1 = cache.create(1, "etag-1".into(), SortKey::Name, Order::Asc, entries, 200);
        assert_eq!(page1.entries.len(), 200);
        assert_eq!(page1.total, 450);
        assert!(page1.cursor.is_some());

        let page2 = cache.page(1, &page1.listing_id, page1.cursor.as_deref().unwrap(), "etag-1", 200).unwrap();
        assert_eq!(page2.entries.len(), 200);
        assert!(!page2.stale);
        assert_eq!(page2.entries[0].name, "f200");

        let page3 = cache.page(1, &page1.listing_id, page2.cursor.as_deref().unwrap(), "etag-1", 200).unwrap();
        assert_eq!(page3.entries.len(), 50);
        assert!(page3.cursor.is_none());
    }

    #[test]
    fn stale_dir_etag_flags_and_resets() {
        let cache = ListingCache::new();
        let entries: Vec<Entry> = (0..10).map(|i| mk_entry(&format!("f{i}"))).collect();
        let page1 = cache.create(1, "etag-1".into(), SortKey::Name, Order::Asc, entries, 5);
        let page2 = cache.page(1, &page1.listing_id, page1.cursor.as_deref().unwrap(), "etag-2", 5).unwrap();
        assert!(page2.stale);
    }

    #[test]
    fn unknown_listing_id_is_expired() {
        let cache = ListingCache::new();
        let err = cache.page(1, "L-doesnotexist", &encode_cursor(5), "etag-1", 5).unwrap_err();
        assert_eq!(err, ListingError::Expired);
    }

    #[test]
    fn per_user_cap_evicts_oldest() {
        let cache = ListingCache::new();
        let mut ids = Vec::new();
        for i in 0..(PER_USER_CAP + 2) {
            let entries = vec![mk_entry(&format!("f{i}"))];
            let page = cache.create(1, "etag".into(), SortKey::Name, Order::Asc, entries, 10);
            ids.push(page.listing_id);
        }
        // The first two sessions should have been evicted.
        assert!(cache.page(1, &ids[0], &encode_cursor(0), "etag", 10).is_err());
        assert!(cache.page(1, ids.last().unwrap(), &encode_cursor(0), "etag", 10).is_ok());
    }
}
