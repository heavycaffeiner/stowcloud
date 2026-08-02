//! Hot-set bookkeeping: which `(share, dir)` keys currently have a live OS
//! watch, split into "sticky" (WebSocket-subscribed dirs + their ancestor
//! chain, plus share roots — never auto-evicted) and "recent" (an LRU of
//! everything else that's been touched, capped at `hot_set_max` and evicted
//! oldest-first —).

use std::collections::HashMap;
use std::num::NonZeroUsize;

use sc_vfs::ShareId;
use lru::LruCache;

pub(crate) type Key = (ShareId, String);

pub(crate) struct HotSet {
    /// Key -> subscriber refcount. Present here ⇒ never evicted by `touch`.
    sticky: HashMap<Key, u32>,
    /// Recently-touched, evictable keys, oldest at the back.
    recent: LruCache<Key, ()>,
    /// Every key that currently has a live backend registration, sticky or
    /// recent — this is the thing capped at `hot_set_max`.
    registered: HashMap<Key, ()>,
    cap: usize,
}

impl HotSet {
    pub(crate) fn new(cap: usize) -> Self {
        Self {
            sticky: HashMap::new(),
            // `recent`'s own capacity is deliberately *not* `cap`: eviction
            // is enforced exclusively through `keys_to_evict_for_one_more`,
            // which also unregisters the backend watch and updates
            // `registered`. If `LruCache` itself were capped at `cap`, its
            // internal `put` would silently self-evict entries the moment
            // `recent.len() == cap` — even while `sticky` entries mean the
            // *real* registered count is still under `cap` — leaving a
            // "ghost" backend watch nothing ever unregisters. Giving it
            // a generous, effectively-unbounded capacity here makes it a
            // pure ordering structure; `keys_to_evict_for_one_more` is the
            // only thing that actually removes entries from it.
            recent: LruCache::new(NonZeroUsize::new(cap.max(1).saturating_add(1_000_000)).unwrap()),
            registered: HashMap::new(),
            cap,
        }
    }

    pub(crate) fn registered_count(&self) -> usize {
        self.registered.len()
    }

    /// Snapshot of every key with a live backend registration. Used by the
    /// periodic NFS/FUSE rescan, which only ever
    /// revisits directories already being tracked — never a fresh
    /// recursive walk.
    pub(crate) fn registered_keys(&self) -> Vec<Key> {
        self.registered.keys().cloned().collect()
    }


    pub(crate) fn mark_registered(&mut self, key: Key) {
        self.registered.insert(key, ());
    }

    pub(crate) fn mark_unregistered(&mut self, key: &Key) {
        self.registered.remove(key);
    }

    /// Add a subscriber to `key`, promoting it out of the recent LRU into
    /// the sticky set. Returns `true` if this is the key's first
    /// registration (caller must register it with the backend).
    pub(crate) fn add_sticky(&mut self, key: Key) -> bool {
        self.recent.pop(&key);
        *self.sticky.entry(key.clone()).or_insert(0) += 1;
        !self.registered.contains_key(&key)
    }

    /// Remove one subscriber from `key`. Once the refcount hits zero it
    /// drops back into the recent LRU (still registered, now evictable)
    /// rather than being torn down immediately.
    pub(crate) fn remove_sticky(&mut self, key: &Key) {
        if let Some(count) = self.sticky.get_mut(key) {
            *count -= 1;
            if *count == 0 {
                self.sticky.remove(key);
                self.recent.put(key.clone(), ());
            }
        }
    }

    /// LRU-bump `key`. Returns `true` if this is a brand new registration
    /// the caller must register with the backend.
    pub(crate) fn touch(&mut self, key: Key) -> bool {
        if self.sticky.contains_key(&key) {
            return false; // already registered and pinned
        }
        let is_new = !self.registered.contains_key(&key);
        self.recent.put(key, ());
        is_new
    }

    /// Keys to evict (oldest-first, non-sticky) so `registered_count() + 1
    /// <= cap` before adding one more registration. Does *not* mutate
    /// `registered`/`recent` itself — the caller evicts from the backend
    /// first (best-effort), then calls `mark_unregistered` for each.
    pub(crate) fn keys_to_evict_for_one_more(&mut self) -> Vec<Key> {
        let mut evicted = Vec::new();
        while self.registered.len() + 1 > self.cap {
            match self.recent.pop_lru() {
                Some((k, _)) => evicted.push(k),
                None => break, // nothing left to evict; let it exceed cap slightly
            }
        }
        evicted
    }
}
