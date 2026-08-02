//! DAV Basic-auth verification cache — tiers ① and ② of the verification
//! path.
//!
//! Two caches live here:
//! - `CredCache`: HMAC(ephemeral_key, "dav\0"‖user‖"\0"‖pw) -> Argon2 outcome,
//!   so a keep-alive-less client re-authenticating with the *same* password
//!   doesn't re-run Argon2. Positive TTL 15m absolute / 5m idle, negative 30s.
//! - `ConnMemo`: sha256(raw Authorization header bytes) -> Principal, meant
//!   to be held by the HTTP layer per-connection. Invalidated purely by the
//!   `auth_generation` counter (no separate TTL — a real per-connection memo
//!   dies with the connection; here it is a bounded LRU keyed by header hash
//!   instead, since this crate has no connection object to hang state off).
//! - `TokenCache`: sha256(app-password token) -> Principal, TTL 60s,
//!   also invalidated by `auth_generation`.

use crate::Principal;
use hmac::{Hmac, Mac};
use lru::LruCache;
use sha2::Sha256;
use std::num::NonZeroUsize;
use std::time::{Duration, Instant};

type HmacSha256 = Hmac<Sha256>;

#[derive(Clone)]
pub(crate) enum Outcome {
    Accepted(Principal),
    Rejected,
}

struct CredEntry {
    outcome: Outcome,
    gen: u64,
    inserted: Instant,
    last_hit: Instant,
}

pub(crate) struct CredCache {
    ephemeral_key: [u8; 32],
    map: parking_lot::Mutex<LruCache<[u8; 16], CredEntry>>,
    pos_ttl: Duration,
    pos_idle: Duration,
    neg_ttl: Duration,
}

impl CredCache {
    pub(crate) fn new(cap: usize, pos_ttl: Duration, pos_idle: Duration, neg_ttl: Duration) -> Self {
        let mut ephemeral_key = [0u8; 32];
        getrandom::getrandom(&mut ephemeral_key).expect("getrandom for ephemeral cred-cache key");
        Self {
            ephemeral_key,
            map: parking_lot::Mutex::new(LruCache::new(
                NonZeroUsize::new(cap.max(1)).unwrap(),
            )),
            pos_ttl,
            pos_idle,
            neg_ttl,
        }
    }

    pub(crate) fn key(&self, user: &str, pw: &str) -> [u8; 16] {
        let mut mac = HmacSha256::new_from_slice(&self.ephemeral_key).expect("hmac key any size");
        mac.update(b"dav\0");
        mac.update(user.as_bytes());
        mac.update(b"\0");
        mac.update(pw.as_bytes());
        let full = mac.finalize().into_bytes();
        let mut out = [0u8; 16];
        out.copy_from_slice(&full[..16]);
        out
    }

    /// Looks up a cache entry, validating generation + TTL. Returns `None`
    /// on miss or expiry (the caller must fall through to the rate gate +
    /// Argon2 path).
    pub(crate) fn get(&self, key: &[u8; 16], current_gen: u64) -> Option<Outcome> {
        let mut map = self.map.lock();
        let expired = {
            let entry = map.get(key)?;
            if entry.gen != current_gen {
                true
            } else {
                match &entry.outcome {
                    Outcome::Accepted(_) => {
                        entry.inserted.elapsed() > self.pos_ttl
                            || entry.last_hit.elapsed() > self.pos_idle
                    }
                    Outcome::Rejected => entry.inserted.elapsed() > self.neg_ttl,
                }
            }
        };
        if expired {
            map.pop(key);
            return None;
        }
        let entry = map.get_mut(key)?;
        entry.last_hit = Instant::now();
        Some(entry.outcome.clone())
    }

    pub(crate) fn put(&self, key: [u8; 16], outcome: Outcome, gen: u64) {
        let now = Instant::now();
        self.map.lock().put(
            key,
            CredEntry {
                outcome,
                gen,
                inserted: now,
                last_hit: now,
            },
        );
    }
}

struct ConnEntry {
    principal: Principal,
    gen: u64,
}

/// See module docs — this is a process-wide stand-in for the per-connection
/// memo described in ①. The HTTP layer is expected to key
/// it with `sha256(Authorization header bytes)`, exactly per the design.
pub(crate) struct ConnMemo {
    map: parking_lot::Mutex<LruCache<[u8; 32], ConnEntry>>,
}

impl ConnMemo {
    pub(crate) fn new(cap: usize) -> Self {
        Self {
            map: parking_lot::Mutex::new(LruCache::new(NonZeroUsize::new(cap.max(1)).unwrap())),
        }
    }

    pub(crate) fn check(&self, header_hash: &[u8; 32], current_gen: u64) -> Option<Principal> {
        let mut map = self.map.lock();
        let entry = map.get(header_hash)?;
        if entry.gen != current_gen {
            map.pop(header_hash);
            return None;
        }
        Some(entry.principal.clone())
    }

    pub(crate) fn store(&self, header_hash: [u8; 32], principal: Principal, gen: u64) {
        self.map.lock().put(header_hash, ConnEntry { principal, gen });
    }
}

struct TokenEntry {
    principal: Principal,
    gen: u64,
    inserted: Instant,
}

/// App-password verification cache: key = sha256(token),
/// TTL 60s, invalidated by `auth_generation` on revoke.
pub(crate) struct TokenCache {
    map: parking_lot::Mutex<LruCache<[u8; 32], TokenEntry>>,
    ttl: Duration,
}

impl TokenCache {
    pub(crate) fn new(cap: usize, ttl: Duration) -> Self {
        Self {
            map: parking_lot::Mutex::new(LruCache::new(NonZeroUsize::new(cap.max(1)).unwrap())),
            ttl,
        }
    }

    pub(crate) fn get(&self, token_hash: &[u8; 32], current_gen: u64) -> Option<Principal> {
        let mut map = self.map.lock();
        let expired = {
            let e = map.get(token_hash)?;
            e.gen != current_gen || e.inserted.elapsed() > self.ttl
        };
        if expired {
            map.pop(token_hash);
            return None;
        }
        map.get(token_hash).map(|e| e.principal.clone())
    }

    pub(crate) fn put(&self, token_hash: [u8; 32], principal: Principal, gen: u64) {
        self.map.lock().put(
            token_hash,
            TokenEntry {
                principal,
                gen,
                inserted: Instant::now(),
            },
        );
    }
}
