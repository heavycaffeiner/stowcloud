//! Case folding + NFC normalisation, applied **at index time** so that
//! case/normalisation variants are never stored twice (§4.1, "an additional
//! compression technique").
//!
//! The same fold is applied to the query, so the two sides always meet in the
//! same space. Non-UTF-8 byte sequences are folded ASCII-only and otherwise
//! preserved verbatim — a filename that is not valid UTF-8 must still be
//! findable (§4.2, "filenames are byte strings").

use unicode_normalization::UnicodeNormalization;

/// Fold a byte string for matching/indexing.
///
/// * valid UTF-8, pure ASCII → `to_ascii_lowercase` (no allocation beyond the
///   copy, no Unicode tables touched — the common case)
/// * valid UTF-8, non-ASCII → NFC then Unicode lowercase
/// * invalid UTF-8 → ASCII lowercase on the raw bytes, everything else kept
pub fn fold(bytes: &[u8]) -> Vec<u8> {
    match std::str::from_utf8(bytes) {
        Ok(s) if s.is_ascii() => s.to_ascii_lowercase().into_bytes(),
        Ok(s) => {
            let nfc: String = s.nfc().collect();
            nfc.to_lowercase().into_bytes()
        }
        Err(_) => bytes.to_ascii_lowercase(),
    }
}

/// [`fold`] for a `&str`.
pub fn fold_str(s: &str) -> Vec<u8> {
    fold(s.as_bytes())
}

/// True when folding would be a no-op, i.e. the bytes are already ASCII and
/// already lowercase. Lets hot loops skip the allocation.
pub fn is_folded_ascii(bytes: &[u8]) -> bool {
    bytes
        .iter()
        .all(|b| b.is_ascii() && !b.is_ascii_uppercase())
}

/// Case-insensitive ASCII substring search that allocates nothing.
///
/// `needle` must already be folded and ASCII. Used for the overwhelmingly
/// common Latin query against a Latin filename.
pub fn contains_ascii_ci(haystack: &[u8], needle: &[u8]) -> bool {
    if needle.is_empty() {
        return true;
    }
    if haystack.len() < needle.len() {
        return false;
    }
    let first = needle[0];
    for i in 0..=(haystack.len() - needle.len()) {
        if haystack[i].to_ascii_lowercase() != first {
            continue;
        }
        if haystack[i..i + needle.len()]
            .iter()
            .zip(needle)
            .all(|(h, n)| h.to_ascii_lowercase() == *n)
        {
            return true;
        }
    }
    false
}

/// Byte-exact substring search. Both sides are expected to be pre-folded.
pub fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    if needle.is_empty() {
        return true;
    }
    if haystack.len() < needle.len() {
        return false;
    }
    haystack
        .windows(needle.len())
        .any(|w| w == needle)
}

/// Prefix test on pre-folded bytes.
pub fn starts_with(haystack: &[u8], needle: &[u8]) -> bool {
    haystack.len() >= needle.len() && &haystack[..needle.len()] == needle
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ascii_fold() {
        assert_eq!(fold(b"IMG_0001.JPG"), b"img_0001.jpg".to_vec());
    }

    #[test]
    fn cjk_survives_fold() {
        let s = "여름휴가사진.jpg";
        let f = fold_str(s);
        // Hangul has no case, so folding must not perturb the bytes.
        assert!(contains(&f, "휴가".as_bytes()));
        assert!(contains(&f, "여름".as_bytes()));
        assert!(contains(&f, "사진".as_bytes()));
    }

    #[test]
    fn nfd_and_nfc_fold_together() {
        // "é" as U+00E9 vs "e" + U+0301.
        let nfc = "caf\u{00e9}.txt";
        let nfd = "cafe\u{0301}.txt";
        assert_eq!(fold_str(nfc), fold_str(nfd));
    }

    #[test]
    fn invalid_utf8_is_preserved() {
        let raw = b"ABC\xff\xfe.bin";
        let f = fold(raw);
        assert_eq!(f, b"abc\xff\xfe.bin".to_vec());
        assert!(contains(&f, b"\xff\xfe"));
    }

    #[test]
    fn ci_search() {
        assert!(contains_ascii_ci(b"Vacation_Photo.JPG", b"photo"));
        assert!(!contains_ascii_ci(b"Vacation.JPG", b"photo"));
        assert!(contains_ascii_ci(b"abc", b""));
    }
}
