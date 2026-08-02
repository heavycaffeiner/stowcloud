//! Property tests for the RFC 4918 §10.4 `If` header parser.
//!
//! The parser is reachable by any unauthenticated-adjacent client, so the
//! contract under test is blunt: **it never panics**, on any input at all, and
//! anything the generator builds from the grammar always parses.

use sc_dav::IfHeader;
use proptest::prelude::*;

fn coded_url() -> impl Strategy<Value = String> {
    prop_oneof![
        Just("urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2".to_string()),
        Just("urn:uuid:00000000-0000-0000-0000-000000000000".to_string()),
        Just("http://example.com/locks/1".to_string()),
        "[a-z]{1,8}:[a-z0-9]{1,12}".prop_map(|s| s),
    ]
}

fn etag() -> impl Strategy<Value = String> {
    ("[a-zA-Z0-9._-]{1,16}", any::<bool>())
        .prop_map(|(v, weak)| if weak { format!("W/\"{v}\"") } else { format!("\"{v}\"") })
}

fn condition() -> impl Strategy<Value = String> {
    (
        any::<bool>(),
        prop_oneof![coded_url().prop_map(|u| format!("<{u}>")), etag().prop_map(|e| format!("[{e}]"))],
    )
        .prop_map(|(not, c)| if not { format!("Not {c}") } else { c })
}

fn list() -> impl Strategy<Value = String> {
    prop::collection::vec(condition(), 1..4).prop_map(|cs| format!("({})", cs.join(" ")))
}

fn tagged() -> impl Strategy<Value = String> {
    (
        "/[a-z0-9/._-]{0,24}",
        prop::collection::vec(list(), 1..3),
    )
        .prop_map(|(tag, ls)| format!("<{tag}> {}", ls.join(" ")))
}

fn header() -> impl Strategy<Value = String> {
    prop::collection::vec(prop_oneof![list(), tagged()], 1..4).prop_map(|ps| ps.join(" "))
}

proptest! {
    #![proptest_config(ProptestConfig { cases: 512, ..ProptestConfig::default() })]

    /// Anything built from the grammar parses.
    #[test]
    fn generated_valid_headers_parse(h in header()) {
        prop_assert!(IfHeader::parse(&h).is_ok(), "failed to parse {h:?}");
    }

    /// Arbitrary bytes: may fail, must never panic and must never hang.
    #[test]
    fn arbitrary_input_never_panics(s in ".{0,200}") {
        let _ = IfHeader::parse(&s);
    }

    /// Mutating a valid header keeps it total.
    #[test]
    fn mutated_headers_never_panic(h in header(), idx in 0usize..200, c in prop::char::range(' ', '~')) {
        let mut b: Vec<char> = h.chars().collect();
        if !b.is_empty() {
            let i = idx % b.len();
            b[i] = c;
        }
        let s: String = b.into_iter().collect();
        let _ = IfHeader::parse(&s);
    }

    /// Truncation at any point must be an error or a clean parse, never a panic.
    #[test]
    fn truncation_never_panics(h in header(), cut in 0usize..200) {
        let n = h.len();
        let i = if n == 0 { 0 } else { cut % (n + 1) };
        let mut i = i;
        while i < n && !h.is_char_boundary(i) { i += 1; }
        let _ = IfHeader::parse(&h[..i]);
    }

    /// Evaluation is total too.
    #[test]
    fn evaluation_never_panics(h in header()) {
        if let Ok(p) = IfHeader::parse(&h) {
            let _ = p.evaluate(|_| sc_dav::ResourceState {
                tokens: vec!["urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2".into()],
                etag: Some("x".into()),
                exists: true,
            });
            let _ = p.tokens();
        }
    }
}
