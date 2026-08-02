//! Crockford Base32 encoding for app passwords and recovery codes.
//! Alphabet excludes I, L, O, U to avoid visual confusion.

use data_encoding::{Encoding, Specification};
use std::sync::OnceLock;

fn encoding() -> &'static Encoding {
    static ENC: OnceLock<Encoding> = OnceLock::new();
    ENC.get_or_init(|| {
        let mut spec = Specification::new();
        spec.symbols.push_str("0123456789ABCDEFGHJKMNPQRSTVWXYZ");
        spec.encoding().expect("valid crockford base32 spec")
    })
}

pub(crate) fn encode(bytes: &[u8]) -> String {
    encoding().encode(bytes)
}

/// Groups a Crockford-Base32 string into `-`-separated chunks of `n`.
pub(crate) fn group(s: &str, n: usize) -> String {
    s.as_bytes()
        .chunks(n)
        .map(|c| std::str::from_utf8(c).unwrap())
        .collect::<Vec<_>>()
        .join("-")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encode_roundtrip_shape() {
        let bytes = [0u8; 20];
        let s = encode(&bytes);
        // 20 bytes = 160 bits -> ceil(160/5) = 32 chars
        assert_eq!(s.len(), 32);
        assert!(s.chars().all(|c| "0123456789ABCDEFGHJKMNPQRSTVWXYZ".contains(c)));
    }

    #[test]
    fn grouping() {
        let g = group("ABCDEFGHIJ", 5);
        assert_eq!(g, "ABCDE-FGHIJ");
    }
}
