//! Byte trigrams.
//!
//! Three **bytes**, not three characters (§4.2, "filenames are byte
//! strings"). This is the choice plocate makes and it is what makes the
//! index work for CJK: a UTF-8 Hangul syllable is exactly three bytes, so
//! one syllable is exactly one trigram and a two-syllable query yields four
//! overlapping trigrams.

/// Iterate every overlapping 3-byte window.
pub fn trigrams(bytes: &[u8]) -> impl Iterator<Item = [u8; 3]> + '_ {
    bytes.windows(3).map(|w| [w[0], w[1], w[2]])
}

/// Distinct trigrams of `bytes`, sorted. Allocates; use [`push_distinct`] in
/// hot loops.
pub fn distinct(bytes: &[u8]) -> Vec<[u8; 3]> {
    let mut v: Vec<[u8; 3]> = trigrams(bytes).collect();
    v.sort_unstable();
    v.dedup();
    v
}

/// Append the distinct trigrams of `bytes` to `out` without clearing it. The
/// caller is expected to sort/dedup once at the end.
pub fn push_all(out: &mut Vec<[u8; 3]>, bytes: &[u8]) {
    out.extend(trigrams(bytes));
}

/// Number of trigram *occurrences* (not distinct) a byte string contributes.
/// Used by the estimator's posting-list model (§6.3).
pub fn occurrences(len: usize) -> usize {
    len.saturating_sub(2)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ascii() {
        assert_eq!(
            distinct(b"abcd"),
            vec![*b"abc", *b"bcd"]
        );
    }

    #[test]
    fn too_short_yields_nothing() {
        assert!(distinct(b"ab").is_empty());
        assert!(distinct(b"").is_empty());
    }

    #[test]
    fn hangul_syllable_is_exactly_one_trigram() {
        let s = "휴".as_bytes();
        assert_eq!(s.len(), 3);
        assert_eq!(distinct(s).len(), 1);
        // Two syllables → 6 bytes → 4 overlapping trigrams, as §4.1 states.
        assert_eq!(distinct("휴가".as_bytes()).len(), 4);
    }

    #[test]
    fn query_trigrams_are_a_subset_of_name_trigrams() {
        let name = "여름휴가사진.jpg".as_bytes();
        let name_set = distinct(name);
        for q in ["휴가", "여름", "사진"] {
            for t in distinct(q.as_bytes()) {
                assert!(
                    name_set.binary_search(&t).is_ok(),
                    "{q}: trigram {t:?} missing from the name's trigram set"
                );
            }
        }
    }
}
