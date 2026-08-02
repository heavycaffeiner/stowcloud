//! Generic per-IP token bucket for the `RateLimit` middleware
//! (`DESIGN-API.md` §9 step 5 — sits *before* `Auth`, so unauthenticated
//! floods get `429` without ever reaching Argon2/session lookups).
//!
//! This is deliberately separate from `sc_auth`'s login-specific
//! `IpGate`/`AccountGate` (those are private to `sc-auth` and tuned for the
//! login/DAV-Basic path specifically); this one guards the API surface as a
//! whole.

use std::net::IpAddr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use dashmap::DashMap;

fn refill_per_sec_for(period: Duration) -> f64 {
    if period.is_zero() {
        f64::MAX
    } else {
        1.0 / period.as_secs_f64()
    }
}

struct Bucket {
    tokens: f64,
    last_refill: Instant,
}

pub struct IpTokenBucket {
    capacity: f64,
    refill_per_sec: f64,
    buckets: DashMap<IpAddr, Bucket>,
}

impl IpTokenBucket {
    pub fn new(capacity: u32, refill_period: Duration) -> Self {
        let refill_per_sec = refill_per_sec_for(refill_period);
        Self { capacity: capacity as f64, refill_per_sec, buckets: DashMap::new() }
    }

    /// Returns `None` if the request is allowed, `Some(retry_after_secs)` if
    /// it should be rejected with `429`.
    pub fn check(&self, ip: IpAddr) -> Option<u32> {
        let now = Instant::now();
        let mut entry = self.buckets.entry(ip).or_insert_with(|| Bucket { tokens: self.capacity, last_refill: now });
        let elapsed = now.duration_since(entry.last_refill).as_secs_f64();
        entry.tokens = (entry.tokens + elapsed * self.refill_per_sec).min(self.capacity);
        entry.last_refill = now;
        if entry.tokens >= 1.0 {
            entry.tokens -= 1.0;
            None
        } else {
            let deficit = 1.0 - entry.tokens;
            let wait = (deficit / self.refill_per_sec).ceil().max(1.0) as u32;
            Some(wait)
        }
    }
}

/// The same bucket keyed by an opaque string instead of an IP.
///
/// Share-link password attempts are limited **per token**, not per IP
/// (`DESIGN-PREVIEW.md` §7.2: "10 per hour per token"). Per-IP would be the wrong
/// axis twice over: one NAT'd office shares a bucket, and an attacker with a
/// botnet gets one bucket per host against a single link.
pub struct KeyedTokenBucket {
    /// `f64` bit pattern, atomic so [`Self::reconfigure`] can change the cap
    /// and refill rate live (e.g. an admin-changed `search.rate_per_minute`)
    /// without a lock around every `check()` call.
    capacity: AtomicU64,
    refill_per_sec: AtomicU64,
    buckets: DashMap<String, Bucket>,
    /// Hard cap on distinct tracked keys, so a flood of made-up tokens cannot
    /// grow the map without bound. On overflow the whole map is dropped: a
    /// rare global reset is a far smaller problem than unbounded memory, and
    /// an attacker who forces it has to pay for `max_keys` requests first.
    max_keys: usize,
}

impl KeyedTokenBucket {
    pub fn new(capacity: u32, refill_period: Duration) -> Self {
        Self {
            capacity: AtomicU64::new((capacity as f64).to_bits()),
            refill_per_sec: AtomicU64::new(refill_per_sec_for(refill_period).to_bits()),
            buckets: DashMap::new(),
            max_keys: 100_000,
        }
    }

    fn capacity(&self) -> f64 {
        f64::from_bits(self.capacity.load(Ordering::Relaxed))
    }

    fn refill_per_sec(&self) -> f64 {
        f64::from_bits(self.refill_per_sec.load(Ordering::Relaxed))
    }

    /// Live-reconfigure the cap and refill rate (admin settings screen,
    /// `search.rate_per_minute`). Existing per-key buckets keep their
    /// accumulated token count — only the ceiling and refill speed change,
    /// from the next `check()` call onward.
    pub fn reconfigure(&self, capacity: u32, refill_period: Duration) {
        self.capacity.store((capacity as f64).to_bits(), Ordering::Relaxed);
        self.refill_per_sec
            .store(refill_per_sec_for(refill_period).to_bits(), Ordering::Relaxed);
    }

    /// `None` if allowed, `Some(retry_after_secs)` if it should be rejected.
    pub fn check(&self, key: &str) -> Option<u32> {
        if self.buckets.len() > self.max_keys {
            self.buckets.clear();
        }
        let capacity = self.capacity();
        let refill_per_sec = self.refill_per_sec();
        let now = Instant::now();
        let mut entry = self
            .buckets
            .entry(key.to_string())
            .or_insert_with(|| Bucket { tokens: capacity, last_refill: now });
        let elapsed = now.duration_since(entry.last_refill).as_secs_f64();
        entry.tokens = (entry.tokens + elapsed * refill_per_sec).min(capacity);
        entry.last_refill = now;
        if entry.tokens >= 1.0 {
            entry.tokens -= 1.0;
            None
        } else {
            let deficit = 1.0 - entry.tokens;
            Some((deficit / refill_per_sec).ceil().max(1.0) as u32)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keyed_bucket_limits_per_key_independently() {
        let b = KeyedTokenBucket::new(2, Duration::from_secs(3600));
        assert!(b.check("tok-a").is_none());
        assert!(b.check("tok-a").is_none());
        assert!(b.check("tok-a").is_some(), "third attempt on the same token is limited");
        assert!(b.check("tok-b").is_none(), "a different token has its own budget");
    }

    #[test]
    fn allows_burst_up_to_capacity_then_limits() {
        let b = IpTokenBucket::new(3, Duration::from_secs(3600));
        let ip: IpAddr = "1.2.3.4".parse().unwrap();
        assert!(b.check(ip).is_none());
        assert!(b.check(ip).is_none());
        assert!(b.check(ip).is_none());
        assert!(b.check(ip).is_some());
    }

    #[test]
    fn reconfigure_changes_cap_for_new_keys() {
        let b = KeyedTokenBucket::new(1, Duration::from_secs(3600));
        assert!(b.check("tok-old").is_none());
        assert!(b.check("tok-old").is_some(), "capacity 1 is exhausted");

        b.reconfigure(3, Duration::from_secs(3600));
        // A fresh key starts at the new capacity — proves reconfigure took
        // effect, without depending on real time passing for refill.
        assert!(b.check("tok-new").is_none());
        assert!(b.check("tok-new").is_none());
        assert!(b.check("tok-new").is_none());
        assert!(b.check("tok-new").is_some(), "new capacity 3 is exhausted");
    }

    #[test]
    fn different_ips_have_independent_buckets() {
        let b = IpTokenBucket::new(1, Duration::from_secs(3600));
        let a: IpAddr = "1.1.1.1".parse().unwrap();
        let c: IpAddr = "2.2.2.2".parse().unwrap();
        assert!(b.check(a).is_none());
        assert!(b.check(c).is_none());
        assert!(b.check(a).is_some());
    }
}
