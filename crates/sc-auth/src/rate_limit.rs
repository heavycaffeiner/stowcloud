use std::collections::HashMap;
use std::net::IpAddr;
use std::time::{Duration, Instant};

/// Simple token bucket. `refill_per_sec` tokens are added per second,
/// capped at `capacity`.
#[derive(Debug)]
struct TokenBucket {
    capacity: f64,
    tokens: f64,
    refill_per_sec: f64,
    last: Instant,
}

impl TokenBucket {
    fn new(capacity: u32, refill_period: Duration) -> Self {
        let refill_per_sec = if refill_period.as_secs_f64() > 0.0 {
            1.0 / refill_period.as_secs_f64()
        } else {
            f64::INFINITY
        };
        Self {
            capacity: capacity as f64,
            tokens: capacity as f64,
            refill_per_sec,
            last: Instant::now(),
        }
    }

    fn refill(&mut self) {
        let now = Instant::now();
        let elapsed = now.duration_since(self.last).as_secs_f64();
        self.tokens = (self.tokens + elapsed * self.refill_per_sec).min(self.capacity);
        self.last = now;
    }

    /// Tries to consume one token. Returns `None` on success, or
    /// `Some(retry_after_seconds)` if the bucket is empty.
    fn try_consume(&mut self) -> Option<u32> {
        self.refill();
        if self.tokens >= 1.0 {
            self.tokens -= 1.0;
            None
        } else {
            let deficit = 1.0 - self.tokens;
            let secs = if self.refill_per_sec > 0.0 {
                (deficit / self.refill_per_sec).ceil().max(1.0)
            } else {
                1.0
            };
            Some(secs as u32)
        }
    }
}

/// Per-IP hard rate gate (DESIGN-AUTH §7.1): capacity 20, refill 1/10s.
/// Exceeding it returns 429 *before* Argon2 ever runs.
pub(crate) struct IpGate {
    capacity: u32,
    refill: Duration,
    buckets: parking_lot::Mutex<HashMap<IpAddr, TokenBucket>>,
}

impl IpGate {
    pub(crate) fn new(capacity: u32, refill: Duration) -> Self {
        Self {
            capacity,
            refill,
            buckets: parking_lot::Mutex::new(HashMap::new()),
        }
    }

    /// Returns `None` if allowed, `Some(retry_after_s)` if rate-limited.
    pub(crate) fn check(&self, ip: IpAddr) -> Option<u32> {
        let mut map = self.buckets.lock();
        let bucket = map
            .entry(ip)
            .or_insert_with(|| TokenBucket::new(self.capacity, self.refill));
        bucket.try_consume()
    }
}

struct FailState {
    count: u32,
    last: Instant,
}

/// Per-account soft delay (DESIGN-AUTH §7.1): `delay(n) = min(250ms *
/// 2^(n-3), 30s)` for `n > 3` failed attempts. Never rejects — no account
/// lockout, by design (an attacker must not be able to DoS a victim account).
pub(crate) struct AccountGate {
    refill: Duration,
    state: parking_lot::Mutex<HashMap<String, FailState>>,
}

impl AccountGate {
    pub(crate) fn new(refill: Duration) -> Self {
        Self {
            refill,
            state: parking_lot::Mutex::new(HashMap::new()),
        }
    }

    /// Records a failed attempt for `key` (normalized account identifier)
    /// and returns the delay that should be applied before responding.
    pub(crate) fn record_failure(&self, key: &str) -> Duration {
        let mut map = self.state.lock();
        let now = Instant::now();
        let entry = map.entry(key.to_string()).or_insert_with(|| FailState {
            count: 0,
            last: now,
        });
        let elapsed = now.duration_since(entry.last).as_secs_f64();
        let refill_secs = self.refill.as_secs_f64().max(0.001);
        let decay = (elapsed / refill_secs).floor() as u32;
        entry.count = entry.count.saturating_sub(decay);
        entry.last = now;
        entry.count += 1;
        let n = entry.count;
        if n > 3 {
            let ms = 250f64 * 2f64.powi((n - 3) as i32);
            Duration::from_millis(ms.min(30_000.0) as u64)
        } else {
            Duration::ZERO
        }
    }

    pub(crate) fn reset(&self, key: &str) {
        self.state.lock().remove(key);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ip_gate_hard_limits() {
        let gate = IpGate::new(3, Duration::from_secs(10));
        let ip: IpAddr = "127.0.0.1".parse().unwrap();
        assert!(gate.check(ip).is_none());
        assert!(gate.check(ip).is_none());
        assert!(gate.check(ip).is_none());
        assert!(gate.check(ip).is_some(), "4th request should be rate limited");
    }

    #[test]
    fn account_gate_soft_delay_formula() {
        let gate = AccountGate::new(Duration::from_secs(3600));
        let key = "alice";
        for _ in 0..3 {
            assert_eq!(gate.record_failure(key), Duration::ZERO);
        }
        // 4th failure: n=4 -> 250ms * 2^1 = 500ms
        assert_eq!(gate.record_failure(key), Duration::from_millis(500));
        // 5th failure: n=5 -> 250ms * 2^2 = 1000ms
        assert_eq!(gate.record_failure(key), Duration::from_millis(1000));
    }

    #[test]
    fn account_gate_reset() {
        let gate = AccountGate::new(Duration::from_secs(3600));
        gate.record_failure("bob");
        gate.record_failure("bob");
        gate.record_failure("bob");
        gate.record_failure("bob");
        gate.reset("bob");
        assert_eq!(gate.record_failure("bob"), Duration::ZERO);
    }
}
