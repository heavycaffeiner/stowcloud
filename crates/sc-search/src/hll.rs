//! A small HyperLogLog.
//!
//! §6.2 needs `distinct_trigrams` to size the posting dictionary, and this is
//! *the* number that separates a CJK corpus from a Latin one (§4.1's "17 B
//! per file is the measured figure for a Latin corpus"). Counting exactly means holding millions of
//! distinct trigrams in a hash set during the estimate scan; HLL answers to
//! ±2% in 16 KB.
//!
//! Hand-rolled rather than pulled from a crate: it is eighty lines, and the
//! estimator has to be auditable line by line — an admin is being asked to
//! trust its arithmetic.

/// Register count is `2^p`. p=14 → 16384 registers → 16 KB → ~0.8% standard
/// error (`1.04 / sqrt(m)`).
pub const DEFAULT_P: u8 = 14;

#[derive(Clone)]
pub struct HyperLogLog {
    p: u8,
    regs: Vec<u8>,
}

impl Default for HyperLogLog {
    fn default() -> Self {
        Self::new(DEFAULT_P)
    }
}

impl HyperLogLog {
    /// `p` is clamped to 4..=18.
    pub fn new(p: u8) -> Self {
        let p = p.clamp(4, 18);
        Self {
            p,
            regs: vec![0u8; 1usize << p],
        }
    }

    pub fn precision(&self) -> u8 {
        self.p
    }

    /// Bytes of state. Reported so the estimator can say what it cost.
    pub fn memory_bytes(&self) -> usize {
        self.regs.len()
    }

    pub fn add(&mut self, bytes: &[u8]) {
        self.add_hash(hash64(bytes));
    }

    pub fn add_hash(&mut self, h: u64) {
        let idx = (h >> (64 - self.p)) as usize;
        // Rank = position of the first 1 in the remaining bits, 1-based.
        let w = h << self.p;
        let rank = if w == 0 {
            64 - self.p + 1
        } else {
            w.leading_zeros() as u8 + 1
        };
        if rank > self.regs[idx] {
            self.regs[idx] = rank;
        }
    }

    pub fn merge(&mut self, other: &Self) {
        assert_eq!(self.p, other.p, "cannot merge HLLs of different precision");
        for (a, b) in self.regs.iter_mut().zip(&other.regs) {
            if *b > *a {
                *a = *b;
            }
        }
    }

    pub fn estimate(&self) -> f64 {
        let m = self.regs.len() as f64;
        let alpha = match self.regs.len() {
            16 => 0.673,
            32 => 0.697,
            64 => 0.709,
            _ => 0.7213 / (1.0 + 1.079 / m),
        };
        let mut sum = 0.0f64;
        let mut zeros = 0usize;
        for &r in &self.regs {
            sum += 2f64.powi(-(r as i32));
            if r == 0 {
                zeros += 1;
            }
        }
        let raw = alpha * m * m / sum;
        // Small-range correction: with many empty registers, linear counting
        // is far more accurate than the raw estimator. No large-range
        // correction is needed — the hash is 64-bit, so the 2^32 collision
        // regime is unreachable in practice.
        if raw <= 2.5 * m && zeros > 0 {
            m * (m / zeros as f64).ln()
        } else {
            raw
        }
    }

    pub fn estimate_u64(&self) -> u64 {
        self.estimate().round().max(0.0) as u64
    }
}

/// FNV-1a followed by a splitmix64 finaliser.
///
/// FNV alone has poor avalanche in the high bits, which is exactly where HLL
/// reads the register index from; the finaliser fixes the distribution without
/// pulling in a hashing dependency.
pub fn hash64(bytes: &[u8]) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in bytes {
        h ^= *b as u64;
        h = h.wrapping_mul(0x0000_0100_0000_01b3);
    }
    splitmix64(h)
}

fn splitmix64(mut z: u64) -> u64 {
    z = z.wrapping_add(0x9e37_79b9_7f4a_7c15);
    let mut x = z;
    x = (x ^ (x >> 30)).wrapping_mul(0xbf58_476d_1ce4_e5b9);
    x = (x ^ (x >> 27)).wrapping_mul(0x94d0_49bb_1331_11eb);
    x ^ (x >> 31)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn small_cardinalities_are_near_exact() {
        for n in [0u64, 1, 10, 100, 1000] {
            let mut h = HyperLogLog::default();
            for i in 0..n {
                h.add(format!("item-{i}").as_bytes());
            }
            let est = h.estimate();
            let err = (est - n as f64).abs() / (n as f64).max(1.0);
            assert!(err < 0.05, "n={n} est={est:.0} err={err:.3}");
        }
    }

    #[test]
    fn within_five_percent_of_exact_at_scale() {
        // §10's "HLL accuracy" row: distinct trigram estimate within ±5% of an
        // exact count.
        let mut h = HyperLogLog::default();
        let mut exact: HashSet<[u8; 3]> = HashSet::new();
        for i in 0..200_000u32 {
            let s = format!("photos/2026/trip{}/IMG_{:05}.jpg", i % 977, i);
            for w in s.as_bytes().windows(3) {
                let t = [w[0], w[1], w[2]];
                exact.insert(t);
                h.add(&t);
            }
        }
        let n = exact.len() as f64;
        let est = h.estimate();
        let err = (est - n).abs() / n;
        assert!(err < 0.05, "exact={n} est={est:.0} err={err:.4}");
    }

    #[test]
    fn duplicates_do_not_inflate() {
        let mut h = HyperLogLog::default();
        for _ in 0..10_000 {
            h.add(b"the same trigram source over and over");
        }
        assert_eq!(h.estimate_u64(), 1);
    }

    #[test]
    fn merge_is_a_union() {
        let mut a = HyperLogLog::default();
        let mut b = HyperLogLog::default();
        let mut both = HyperLogLog::default();
        for i in 0..5000u32 {
            a.add(format!("a{i}").as_bytes());
            both.add(format!("a{i}").as_bytes());
        }
        for i in 0..5000u32 {
            b.add(format!("b{i}").as_bytes());
            both.add(format!("b{i}").as_bytes());
        }
        a.merge(&b);
        let err = (a.estimate() - both.estimate()).abs() / both.estimate();
        assert!(err < 0.001, "{} vs {}", a.estimate(), both.estimate());
    }

    #[test]
    fn state_is_sixteen_kilobytes() {
        assert_eq!(HyperLogLog::default().memory_bytes(), 16384);
    }
}
