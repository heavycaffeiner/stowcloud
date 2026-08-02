//! `IntervalSet` — the set of byte ranges received so far for one upload
//! session. See docs/DESIGN-UPLOAD.md §4.
//!
//! Invariant maintained at all times: `runs` is sorted by start, and no two
//! runs overlap or touch (adjacent runs are always merged). `decode` must
//! never trust a blob read back from the database — it re-derives the
//! invariant from scratch and rejects anything that doesn't hold, because a
//! corrupted `received` set silently turns into wrong offset math, which
//! turns into silent data corruption on the next `PATCH`.

use smallvec::SmallVec;

#[derive(Default, Clone, PartialEq, Eq, Debug)]
pub struct IntervalSet {
    runs: SmallVec<[(u64, u64); 2]>,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Fragmented;

impl std::fmt::Display for Fragmented {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "session has too many disjoint received runs (pathological client)")
    }
}
impl std::error::Error for Fragmented {}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Corrupt;

impl std::fmt::Display for Corrupt {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "corrupt IntervalSet encoding")
    }
}
impl std::error::Error for Corrupt {}

impl IntervalSet {
    /// Cap on the number of disjoint runs. Normal use (sequential: 1 run,
    /// `parallel` random-access uploaders: a handful) never gets close to
    /// this; only a pathological/malicious client fragments a session this
    /// badly, and at that point we cut it off rather than let `runs` grow
    /// unboundedly.
    pub const MAX_RUNS: usize = 4096;

    pub fn new() -> Self {
        Self::default()
    }

    /// Insert `[start, end)`, merging with any overlapping or touching
    /// existing runs. Rejects the insert (leaving `self` unchanged) if it
    /// would push the run count over `MAX_RUNS`.
    pub fn insert(&mut self, start: u64, end: u64) -> Result<(), Fragmented> {
        debug_assert!(start < end);
        if start >= end {
            return Ok(()); // empty range, nothing to do
        }
        let i = self.runs.partition_point(|r| r.1 < start);
        let mut j = i;
        let (mut s, mut e) = (start, end);
        while j < self.runs.len() && self.runs[j].0 <= e {
            s = s.min(self.runs[j].0);
            e = e.max(self.runs[j].1);
            j += 1;
        }
        let new_len = self.runs.len() - (j - i) + 1;
        if new_len > Self::MAX_RUNS {
            return Err(Fragmented);
        }
        self.runs.drain(i..j);
        self.runs.insert(i, (s, e));
        Ok(())
    }

    /// End of the contiguous run starting at 0 (0 if there is none). This is
    /// the TUS `Upload-Offset` semantics and is meaningful for both
    /// sequential and random-access sessions (§2.3): a standard TUS client
    /// resuming a random-access session just resends whatever is beyond
    /// this point, which is always safe (already-received bytes in later
    /// runs get overwritten with identical content).
    pub fn contiguous_prefix(&self) -> u64 {
        match self.runs.first() {
            Some(&(0, e)) => e,
            _ => 0,
        }
    }

    pub fn is_complete(&self, len: u64) -> bool {
        if len == 0 {
            return self.runs.is_empty();
        }
        matches!(self.runs.as_slice(), [(0, e)] if *e == len)
    }

    pub fn run_count(&self) -> usize {
        self.runs.len()
    }

    pub fn runs(&self) -> &[(u64, u64)] {
        &self.runs
    }

    pub fn full(len: u64) -> Self {
        if len == 0 {
            Self::default()
        } else {
            let mut runs = SmallVec::new();
            runs.push((0, len));
            Self { runs }
        }
    }

    /// Varint delta encoding: `count`, then `(gap_from_prev_end, length)*`.
    /// A sequential upload (one run starting at 0) encodes to 3 bytes for
    /// any length that fits in one varint byte's worth of leading bits.
    pub fn encode(&self, out: &mut Vec<u8>) {
        write_varint(out, self.runs.len() as u64);
        let mut prev_end = 0u64;
        for &(start, end) in &self.runs {
            write_varint(out, start - prev_end);
            write_varint(out, end - start);
            prev_end = end;
        }
    }

    pub fn to_vec(&self) -> Vec<u8> {
        let mut v = Vec::new();
        self.encode(&mut v);
        v
    }

    /// Decode and **re-validate** the invariant (sorted, non-overlapping,
    /// non-touching, `start < end` for every run, run count within
    /// `MAX_RUNS`). Never trust bytes read back from storage.
    pub fn decode(b: &[u8]) -> Result<Self, Corrupt> {
        let mut pos = 0usize;
        let count = read_varint(b, &mut pos).ok_or(Corrupt)?;
        if count > Self::MAX_RUNS as u64 {
            return Err(Corrupt);
        }
        let mut runs: SmallVec<[(u64, u64); 2]> = SmallVec::new();
        let mut prev_end = 0u64;
        for i in 0..count {
            let gap = read_varint(b, &mut pos).ok_or(Corrupt)?;
            let len = read_varint(b, &mut pos).ok_or(Corrupt)?;
            if len == 0 {
                return Err(Corrupt); // empty/degenerate run
            }
            if i > 0 && gap == 0 {
                return Err(Corrupt); // touching runs must have been merged
            }
            let start = prev_end.checked_add(gap).ok_or(Corrupt)?;
            let end = start.checked_add(len).ok_or(Corrupt)?;
            runs.push((start, end));
            prev_end = end;
        }
        if pos != b.len() {
            return Err(Corrupt); // trailing garbage
        }
        Ok(Self { runs })
    }
}

fn write_varint(out: &mut Vec<u8>, mut v: u64) {
    loop {
        let byte = (v & 0x7f) as u8;
        v >>= 7;
        if v == 0 {
            out.push(byte);
            break;
        } else {
            out.push(byte | 0x80);
        }
    }
}

fn read_varint(b: &[u8], pos: &mut usize) -> Option<u64> {
    let mut result: u64 = 0;
    let mut shift: u32 = 0;
    loop {
        if *pos >= b.len() || shift >= 64 {
            return None;
        }
        let byte = b[*pos];
        *pos += 1;
        result |= ((byte & 0x7f) as u64) << shift;
        if byte & 0x80 == 0 {
            break;
        }
        shift += 7;
    }
    Some(result)
}

#[cfg(test)]
mod tests {
    use super::*;
    use proptest::prelude::*;

    #[test]
    fn sequential_insert() {
        let mut s = IntervalSet::new();
        s.insert(0, 10).unwrap();
        assert_eq!(s.contiguous_prefix(), 10);
        s.insert(10, 20).unwrap();
        assert_eq!(s.contiguous_prefix(), 20);
        assert!(s.is_complete(20));
    }

    #[test]
    fn random_access_prefix_semantics() {
        // A parallel/random-access client writes [10,20) first, then [0,10).
        // contiguous_prefix must stay 0 until the gap at the front closes,
        // exactly the offset a standard TUS client would resume from.
        let mut s = IntervalSet::new();
        s.insert(10, 20).unwrap();
        assert_eq!(s.contiguous_prefix(), 0);
        s.insert(0, 10).unwrap();
        assert_eq!(s.contiguous_prefix(), 20);
    }

    #[test]
    fn touching_runs_merge() {
        let mut s = IntervalSet::new();
        s.insert(0, 5).unwrap();
        s.insert(5, 10).unwrap();
        assert_eq!(s.runs(), &[(0, 10)]);
    }

    #[test]
    fn overlapping_runs_merge() {
        let mut s = IntervalSet::new();
        s.insert(0, 10).unwrap();
        s.insert(5, 15).unwrap();
        assert_eq!(s.runs(), &[(0, 15)]);
    }

    #[test]
    fn max_runs_rejects() {
        let mut s = IntervalSet::new();
        for i in 0..IntervalSet::MAX_RUNS {
            let start = (i as u64) * 10;
            s.insert(start, start + 1).unwrap();
        }
        // one more disjoint run should be rejected
        let over = (IntervalSet::MAX_RUNS as u64) * 10;
        assert_eq!(s.insert(over, over + 1), Err(Fragmented));
    }

    #[test]
    fn encode_decode_roundtrip() {
        let mut s = IntervalSet::new();
        s.insert(0, 100).unwrap();
        s.insert(200, 300).unwrap();
        let bytes = s.to_vec();
        let back = IntervalSet::decode(&bytes).unwrap();
        assert_eq!(s, back);
    }

    #[test]
    fn decode_rejects_corrupt_garbage() {
        assert!(IntervalSet::decode(&[0xff, 0xff, 0xff, 0xff, 0xff]).is_err());
        // trailing garbage after a valid single-run encoding
        let mut s = IntervalSet::new();
        s.insert(0, 10).unwrap();
        let mut bytes = s.to_vec();
        bytes.push(0x01);
        assert!(IntervalSet::decode(&bytes).is_err());
    }

    #[test]
    fn decode_rejects_touching_runs_that_should_have_merged() {
        // Hand-craft an encoding claiming two runs [0,5) and [5,10) without
        // merging them (gap = 0 for the second run) — this should never be
        // producible by `insert`, so `decode` must reject it as corrupt.
        let mut bytes = Vec::new();
        write_varint(&mut bytes, 2); // count
        write_varint(&mut bytes, 0); // gap for run 0 (start=0)
        write_varint(&mut bytes, 5); // len = 5 -> [0,5)
        write_varint(&mut bytes, 0); // gap = 0 (touching, invalid)
        write_varint(&mut bytes, 5); // len = 5 -> [5,10)
        assert_eq!(IntervalSet::decode(&bytes), Err(Corrupt));
    }

    #[test]
    fn decode_rejects_out_of_order_construction() {
        // Overlapping runs encoded directly (not produced via insert) must
        // also be rejected: run 1 starts before run 0 ends.
        let mut bytes = Vec::new();
        write_varint(&mut bytes, 2);
        write_varint(&mut bytes, 0);
        write_varint(&mut bytes, 10); // [0,10)
        write_varint(&mut bytes, 0); // gap 0 -> start = 10, overlapping semantics caught by gap==0 rule
        write_varint(&mut bytes, 5);
        assert!(IntervalSet::decode(&bytes).is_err());
    }

    proptest! {
        #[test]
        fn property_random_permutations_converge(
            mut ranges in proptest::collection::vec((0u64..2000, 1u64..50), 1..40)
        ) {
            // Build (start, start+len) pairs, then insert in every one of a
            // few random shuffles and check they all converge to the same
            // normal form regardless of insertion order.
            let pairs: Vec<(u64, u64)> = ranges.drain(..).map(|(s, l)| (s, s + l)).collect();

            let mut baseline = IntervalSet::new();
            for &(s, e) in &pairs {
                let _ = baseline.insert(s, e);
            }

            // Try a handful of deterministic shuffles (reverse, and a few
            // rotations) rather than depending on an RNG inside proptest.
            let mut shuffled = pairs.clone();
            shuffled.reverse();
            let mut alt = IntervalSet::new();
            for &(s, e) in &shuffled {
                let _ = alt.insert(s, e);
            }
            prop_assert_eq!(&baseline, &alt);

            for rot in 1..pairs.len().min(5) {
                let mut rotated = pairs.clone();
                rotated.rotate_left(rot);
                let mut alt2 = IntervalSet::new();
                for &(s, e) in &rotated {
                    let _ = alt2.insert(s, e);
                }
                prop_assert_eq!(&baseline, &alt2);
            }

            // encode/decode roundtrip on the converged form.
            let bytes = baseline.to_vec();
            let back = IntervalSet::decode(&bytes).unwrap();
            prop_assert_eq!(baseline, back);
        }
    }
}
