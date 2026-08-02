//! Ranking (§7.1).
//!
//! ```text
//! score = 3.0 × exact name match
//!       + 2.0 × name prefix match
//!       + 1.0 × normalised bm25      (0 on the T2 walk path — no content)
//!       + 0.5 × recency              (30-day linear decay)
//!       + 0.3 × below the current scope
//!       − 1.0 × hidden
//! ```
//!
//! No learned ranking, no click logs (§7.1, last line).

pub const W_EXACT: f32 = 3.0;
pub const W_PREFIX: f32 = 2.0;
pub const W_BM25: f32 = 1.0;
pub const W_RECENCY: f32 = 0.5;
pub const W_IN_SCOPE: f32 = 0.3;
pub const W_HIDDEN: f32 = 1.0;

/// Recency decays linearly to zero over this window.
pub const RECENCY_WINDOW_NS: i128 = 30 * 24 * 3600 * 1_000_000_000;

pub struct RankInput<'a> {
    /// Folded filename (not the path).
    pub name_folded: &'a [u8],
    /// Folded needle.
    pub needle: &'a [u8],
    /// Display path, used for the scope test.
    pub path: &'a str,
    /// `None` when no stat was performed — the recency term is then 0, which
    /// is the honest answer rather than a guess.
    pub mtime_ns: Option<i128>,
    pub now_ns: i128,
    pub scope: Option<&'a str>,
    /// Already normalised to 0..1. Always 0 for T2.
    pub bm25: f32,
}

pub fn score(i: &RankInput) -> f32 {
    let mut s = 0.0f32;

    if !i.needle.is_empty() {
        if i.name_folded == i.needle {
            s += W_EXACT;
        }
        if i.name_folded.len() >= i.needle.len()
            && &i.name_folded[..i.needle.len()] == i.needle
        {
            s += W_PREFIX;
        }
    }

    s += W_BM25 * i.bm25.clamp(0.0, 1.0);

    if let Some(m) = i.mtime_ns {
        let age = i.now_ns.saturating_sub(m);
        if age < RECENCY_WINDOW_NS {
            let frac = 1.0 - (age.max(0) as f64 / RECENCY_WINDOW_NS as f64);
            s += W_RECENCY * frac as f32;
        }
    }

    if let Some(scope) = i.scope {
        if in_scope(i.path, scope) {
            s += W_IN_SCOPE;
        }
    }

    if is_hidden(i.name_folded) {
        s -= W_HIDDEN;
    }

    s
}

/// `path` is at or below `scope`. Component-aware, so `/photo` does not match
/// `/photography`.
pub fn in_scope(path: &str, scope: &str) -> bool {
    if scope.is_empty() {
        return true;
    }
    let scope = scope.trim_end_matches('/');
    path == scope
        || (path.len() > scope.len()
            && path.starts_with(scope)
            && path.as_bytes()[scope.len()] == b'/')
}

pub fn is_hidden(name: &[u8]) -> bool {
    name.first() == Some(&b'.')
}

#[cfg(test)]
mod tests {
    use super::*;

    fn base<'a>(name: &'a [u8], needle: &'a [u8]) -> RankInput<'a> {
        RankInput {
            name_folded: name,
            needle,
            path: "",
            mtime_ns: None,
            now_ns: 0,
            scope: None,
            bm25: 0.0,
        }
    }

    #[test]
    fn exact_beats_prefix_beats_substring() {
        let exact = score(&base(b"report", b"report"));
        let prefix = score(&base(b"report_final", b"report"));
        let sub = score(&base(b"my_report_final", b"report"));
        // exact also satisfies prefix, hence 3.0 + 2.0.
        assert_eq!(exact, 5.0);
        assert_eq!(prefix, 2.0);
        assert_eq!(sub, 0.0);
        assert!(exact > prefix && prefix > sub);
    }

    #[test]
    fn hidden_penalty() {
        let s = score(&base(b".report", b"report"));
        assert_eq!(s, -1.0);
    }

    #[test]
    fn recency_decays_over_thirty_days() {
        let now = RECENCY_WINDOW_NS * 4;
        let mut i = base(b"a", b"");
        i.now_ns = now;
        i.mtime_ns = Some(now);
        let fresh = score(&i);
        i.mtime_ns = Some(now - RECENCY_WINDOW_NS / 2);
        let half = score(&i);
        i.mtime_ns = Some(now - RECENCY_WINDOW_NS * 2);
        let old = score(&i);
        assert!((fresh - 0.5).abs() < 1e-5);
        assert!((half - 0.25).abs() < 1e-3);
        assert_eq!(old, 0.0);
    }

    #[test]
    fn scope_is_component_aware() {
        assert!(in_scope("photos/a.jpg", "photos"));
        assert!(in_scope("photos", "photos"));
        assert!(!in_scope("photography/a.jpg", "photos"));
        assert!(in_scope("anything", ""));
    }

    #[test]
    fn bm25_is_zero_on_the_walk_path() {
        let mut i = base(b"a", b"");
        i.bm25 = 0.0;
        assert_eq!(score(&i), 0.0);
        i.bm25 = 1.0;
        assert_eq!(score(&i), 1.0);
    }
}
