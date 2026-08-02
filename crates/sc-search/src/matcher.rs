//! Query predicate.
//!
//! Split deliberately into two halves (§3, §3.4):
//!
//! * [`Matcher::matches_name`] / [`Matcher::matches_kind`] — answerable from
//!   `getdents64` alone, **zero `statx`**.
//! * [`Matcher::post_matches`] — size/mtime, which force a stat and therefore
//!   run in a separate, inode-ordered phase over only the entries that already
//!   matched by name.

use crate::fold;

/// How the needle is compared against a name.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MatchMode {
    /// Anywhere in the name. The default, and what the trigram index answers.
    Substring,
    Prefix,
    Exact,
}

/// Which entry kinds are eligible. Decided from `d_type`, so it is free.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct KindFilter {
    pub files: bool,
    pub dirs: bool,
}

impl Default for KindFilter {
    fn default() -> Self {
        Self {
            files: true,
            dirs: true,
        }
    }
}

impl KindFilter {
    pub const ALL: Self = Self {
        files: true,
        dirs: true,
    };
    pub const FILES_ONLY: Self = Self {
        files: true,
        dirs: false,
    };
    pub const DIRS_ONLY: Self = Self {
        files: false,
        dirs: true,
    };
}

#[derive(Clone, Debug)]
pub struct Matcher {
    /// Folded needle. Empty means "match everything" (used by the estimator's
    /// corpus scan, which walks for statistics rather than for hits).
    needle: Vec<u8>,
    needle_is_ascii: bool,
    mode: MatchMode,
    kinds: KindFilter,
    /// Folded extensions without the leading dot.
    exts: Option<Vec<Vec<u8>>>,
    size: Option<(u64, u64)>,
    mtime: Option<(i128, i128)>,
    include_hidden: bool,
    /// Display-form scope prefix, for the `+0.3 in_scope` ranking term.
    scope: Option<String>,
}

impl Matcher {
    /// A matcher for `needle`, substring mode, all kinds, no stat-requiring
    /// filters.
    pub fn new(needle: &str) -> Self {
        let folded = fold::fold_str(needle);
        Self {
            needle_is_ascii: folded.is_ascii(),
            needle: folded,
            mode: MatchMode::Substring,
            kinds: KindFilter::default(),
            exts: None,
            size: None,
            mtime: None,
            include_hidden: true,
            scope: None,
        }
    }

    /// Matches every entry. Used to drive [`crate::CorpusScanner`].
    pub fn match_all() -> Self {
        Self::new("")
    }

    pub fn mode(mut self, mode: MatchMode) -> Self {
        self.mode = mode;
        self
    }

    pub fn kinds(mut self, kinds: KindFilter) -> Self {
        self.kinds = kinds;
        self
    }

    /// Extension group filter (`kind=image` in the API). Never opens the file
    /// — §7's `KindFilter` is explicitly extension-based.
    pub fn exts<I: IntoIterator<Item = S>, S: AsRef<str>>(mut self, exts: I) -> Self {
        let v: Vec<Vec<u8>> = exts
            .into_iter()
            .map(|e| fold::fold_str(e.as_ref().trim_start_matches('.')))
            .collect();
        self.exts = if v.is_empty() { None } else { Some(v) };
        self
    }

    /// Inclusive size range. **Forces the stat phase.**
    pub fn size_range(mut self, lo: u64, hi: u64) -> Self {
        self.size = Some((lo, hi));
        self
    }

    /// Inclusive mtime range in nanoseconds. **Forces the stat phase.**
    pub fn mtime_range(mut self, lo: i128, hi: i128) -> Self {
        self.mtime = Some((lo, hi));
        self
    }

    pub fn include_hidden(mut self, yes: bool) -> Self {
        self.include_hidden = yes;
        self
    }

    pub fn scope(mut self, scope: impl Into<String>) -> Self {
        self.scope = Some(scope.into());
        self
    }

    pub fn needle(&self) -> &[u8] {
        &self.needle
    }

    pub fn match_mode(&self) -> MatchMode {
        self.mode
    }

    pub fn scope_prefix(&self) -> Option<&str> {
        self.scope.as_deref()
    }

    /// Whether a size/mtime filter forces the (expensive) stat phase.
    /// Everything else in this matcher is answerable from `d_name` + `d_type`.
    pub fn needs_stat(&self) -> bool {
        self.size.is_some() || self.mtime.is_some()
    }

    /// Free — decided from `d_type`.
    pub fn matches_kind(&self, is_dir: bool) -> bool {
        if is_dir {
            self.kinds.dirs
        } else {
            self.kinds.files
        }
    }

    /// The hot predicate. `name` is the raw bytes from readdir; it is folded
    /// lazily and only when the ASCII fast path cannot answer.
    pub fn matches_name(&self, name: &[u8]) -> bool {
        if !self.include_hidden && name.first() == Some(&b'.') {
            return false;
        }
        if !self.ext_ok(name) {
            return false;
        }
        if self.needle.is_empty() {
            return true;
        }
        if self.needle_is_ascii && name.is_ascii() {
            return match self.mode {
                MatchMode::Substring => fold::contains_ascii_ci(name, &self.needle),
                MatchMode::Prefix => {
                    name.len() >= self.needle.len()
                        && name[..self.needle.len()]
                            .iter()
                            .zip(&self.needle)
                            .all(|(h, n)| h.to_ascii_lowercase() == *n)
                }
                MatchMode::Exact => {
                    name.len() == self.needle.len()
                        && name
                            .iter()
                            .zip(&self.needle)
                            .all(|(h, n)| h.to_ascii_lowercase() == *n)
                }
            };
        }
        let folded = fold::fold(name);
        self.matches_folded(&folded)
    }

    /// [`matches_name`](Self::matches_name) for bytes that are already folded
    /// — the index's block scan works in folded space.
    pub fn matches_folded(&self, folded: &[u8]) -> bool {
        match self.mode {
            MatchMode::Substring => fold::contains(folded, &self.needle),
            MatchMode::Prefix => fold::starts_with(folded, &self.needle),
            MatchMode::Exact => folded == self.needle.as_slice(),
        }
    }

    fn ext_ok(&self, name: &[u8]) -> bool {
        let Some(exts) = &self.exts else { return true };
        let Some(dot) = name.iter().rposition(|b| *b == b'.') else {
            return false;
        };
        let ext = fold::fold(&name[dot + 1..]);
        exts.contains(&ext)
    }

    /// The half that needs a stat. Only called for entries that already
    /// matched by name.
    pub fn post_matches(&self, size: u64, mtime_ns: i128) -> bool {
        if let Some((lo, hi)) = self.size {
            if size < lo || size > hi {
                return false;
            }
        }
        if let Some((lo, hi)) = self.mtime {
            if mtime_ns < lo || mtime_ns > hi {
                return false;
            }
        }
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn substring_is_case_insensitive() {
        let m = Matcher::new("photo");
        assert!(m.matches_name(b"Vacation_PHOTO_01.jpg"));
        assert!(!m.matches_name(b"Vacation_01.jpg"));
    }

    #[test]
    fn cjk_substring() {
        let m = Matcher::new("휴가");
        assert!(m.matches_name("여름휴가사진.jpg".as_bytes()));
        assert!(!m.matches_name("겨울사진.jpg".as_bytes()));
    }

    #[test]
    fn non_utf8_names_are_matchable() {
        let m = Matcher::new("bin");
        let name = b"weird\xff\xfe.bin";
        assert!(m.matches_name(name));
    }

    #[test]
    fn modes() {
        assert!(Matcher::new("img").mode(MatchMode::Prefix).matches_name(b"IMG_01.jpg"));
        assert!(!Matcher::new("01").mode(MatchMode::Prefix).matches_name(b"IMG_01.jpg"));
        assert!(Matcher::new("img_01.jpg").mode(MatchMode::Exact).matches_name(b"IMG_01.jpg"));
    }

    #[test]
    fn ext_filter_never_opens_the_file() {
        let m = Matcher::new("").exts(["jpg", "png"]);
        assert!(m.matches_name(b"a.JPG"));
        assert!(m.matches_name(b"b.png"));
        assert!(!m.matches_name(b"c.txt"));
        assert!(!m.matches_name(b"noext"));
    }

    #[test]
    fn only_size_and_time_force_a_stat() {
        assert!(!Matcher::new("x").needs_stat());
        assert!(!Matcher::new("x").exts(["jpg"]).needs_stat());
        assert!(Matcher::new("x").size_range(0, 10).needs_stat());
        assert!(Matcher::new("x").mtime_range(0, 10).needs_stat());
    }

    #[test]
    fn hidden_filter() {
        let m = Matcher::new("rc").include_hidden(false);
        assert!(!m.matches_name(b".bashrc"));
        assert!(m.matches_name(b"src.txt"));
    }
}
