//! `SafePath` — a share-relative path that has already passed the rejection
//! table in `DESIGN-CORE.md` §2. Once constructed it is trusted: backends
//! never re-validate it, they only walk its components.
//!
//! Parsing is reject-first: anything ambiguous is an error rather than being
//! silently normalized. In particular `.`/`..` are *rejected*, never
//! resolved — resolving them would just move the escape vector from
//! `SafePath::parse` into whatever code forgot to call it.

use compact_str::CompactString;
use smallvec::SmallVec;
use unicode_normalization::UnicodeNormalization;

use crate::error::VfsError;
use crate::reserved::is_reserved_name;

const MAX_TOTAL_BYTES: usize = 4096;
const MAX_COMPONENT_BYTES: usize = 255;

#[derive(Clone, PartialEq, Eq, Debug)]
pub struct SafePath(SmallVec<[CompactString; 8]>);

impl SafePath {
    /// Parse a share-relative path such as `"a/b/c"`. Never has a leading
    /// slash — that would make it look absolute, which is rejected.
    pub fn parse(s: &str, max_depth: u16) -> Result<Self, VfsError> {
        if s.len() > MAX_TOTAL_BYTES {
            return Err(VfsError::InvalidName("path exceeds 4096 bytes"));
        }
        if s.starts_with('/') {
            return Err(VfsError::InvalidName("absolute paths are not allowed"));
        }
        if s.is_empty() {
            return Ok(Self::root());
        }

        let mut comps: SmallVec<[CompactString; 8]> = SmallVec::new();
        for part in s.split('/') {
            validate_component(part)?;
            comps.push(CompactString::new(part));
        }
        if comps.len() > max_depth as usize {
            return Err(VfsError::TooDeep);
        }
        Ok(Self(comps))
    }

    pub fn root() -> Self {
        Self(SmallVec::new())
    }

    pub fn components(&self) -> &[CompactString] {
        &self.0
    }

    /// Depth (number of components). The root has depth 0.
    pub fn len(&self) -> usize {
        self.0.len()
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// Last component, if any.
    pub fn name(&self) -> Option<&str> {
        self.0.last().map(|c| c.as_str())
    }

    /// Parent path. The parent of the root is the root itself.
    pub fn parent(&self) -> SafePath {
        if self.0.is_empty() {
            return Self::root();
        }
        let mut v = self.0.clone();
        v.pop();
        Self(v)
    }

    /// Append a single validated component.
    pub fn join(&self, name: &str, max_depth: u16) -> Result<Self, VfsError> {
        validate_component(name)?;
        let mut v = self.0.clone();
        v.push(CompactString::new(name));
        if v.len() > max_depth as usize {
            return Err(VfsError::TooDeep);
        }
        let total: usize = v.iter().map(|c| c.len() + 1).sum::<usize>().saturating_sub(1);
        if total > MAX_TOTAL_BYTES {
            return Err(VfsError::InvalidName("path exceeds 4096 bytes"));
        }
        Ok(Self(v))
    }

    /// `self` is a prefix of (or equal to) `other`, component-wise. Used by
    /// grant inheritance (`DESIGN-CORE.md` §3.2).
    pub fn is_prefix_of(&self, other: &SafePath) -> bool {
        self.0.len() <= other.0.len() && self.0.iter().zip(other.0.iter()).all(|(a, b)| a == b)
    }

    /// Construct a single-component `SafePath` for one of *our own* control
    /// files (`.sctrash`, `.scpart-{id}`, ...) — the one place that needs to
    /// bypass the reserved-prefix rejection in `validate_component`, since
    /// that rejection exists precisely to keep *user-supplied* names from
    /// colliding with these. Every other rule in the table (no slashes, no
    /// control bytes, length caps) still applies; this is not a general
    /// escape hatch. `max_depth` is checked exactly like `join`.
    pub fn control(name: &str, max_depth: u16) -> Result<Self, VfsError> {
        validate_component_allow_reserved(name)?;
        if max_depth == 0 {
            return Err(VfsError::TooDeep);
        }
        Ok(Self(smallvec::smallvec![CompactString::new(name)]))
    }

    /// `join`, but for one of *our own* control files — the same narrow
    /// exemption `control` grants, applied to a non-root parent.
    ///
    /// Without this, callers that need a control file *inside a share
    /// subdirectory* (upload part files live next to their destination, so
    /// the rename that publishes them is same-directory and therefore atomic)
    /// have to disguise the name to get past `validate_component`. That
    /// disguise then defeats `is_reserved_name`, and the control file shows
    /// up in ordinary directory listings — which is exactly what the reserved
    /// prefix existed to prevent.
    pub fn join_control(&self, name: &str, max_depth: u16) -> Result<Self, VfsError> {
        validate_component_allow_reserved(name)?;
        let mut v = self.0.clone();
        v.push(CompactString::new(name));
        if v.len() > max_depth as usize {
            return Err(VfsError::TooDeep);
        }
        let total: usize = v.iter().map(|c| c.len() + 1).sum::<usize>().saturating_sub(1);
        if total > MAX_TOTAL_BYTES {
            return Err(VfsError::InvalidName("path exceeds 4096 bytes"));
        }
        Ok(Self(v))
    }

    /// `"a/b/c"`. Never has a leading slash. The root displays as `""`.
    pub fn to_display_string(&self) -> String {
        // itertools-free join to avoid pulling in another dependency.
        let mut out = String::new();
        for (i, c) in self.0.iter().enumerate() {
            if i > 0 {
                out.push('/');
            }
            out.push_str(c.as_str());
        }
        out
    }
}

const WINDOWS_RESERVED: &[&str] = &[
    "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8",
    "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
];

fn is_windows_reserved(name: &str) -> bool {
    // Extension-insensitive: "con.txt" is just as reserved as "CON".
    let base = name.split('.').next().unwrap_or(name);
    WINDOWS_RESERVED.iter().any(|r| r.eq_ignore_ascii_case(base))
}

/// The full rejection table from `DESIGN-CORE.md` §2, applied to a single
/// path component (never a whole path — callers split on `/` first, or, in
/// the case of `join`, are appending exactly one component).
fn validate_component(name: &str) -> Result<(), VfsError> {
    validate_component_inner(name, true)
}

/// Same table, minus the reserved-control-prefix rejection — used only by
/// `SafePath::control` for our own `.sctrash`/`.scpart-*` names.
fn validate_component_allow_reserved(name: &str) -> Result<(), VfsError> {
    validate_component_inner(name, false)
}

fn validate_component_inner(name: &str, reject_reserved: bool) -> Result<(), VfsError> {
    if name.is_empty() {
        return Err(VfsError::InvalidName("empty path component"));
    }
    if name == "." || name == ".." {
        return Err(VfsError::InvalidName("'.' and '..' components are rejected, not resolved"));
    }
    if name.contains('/') {
        return Err(VfsError::InvalidName("component must not contain '/'"));
    }
    if name
        .bytes()
        .any(|b| b == 0 || (1..=0x1F).contains(&b) || b == 0x7F)
    {
        return Err(VfsError::InvalidName(
            "NUL and control characters are not allowed",
        ));
    }
    if name.len() > MAX_COMPONENT_BYTES {
        return Err(VfsError::InvalidName("component exceeds 255 bytes"));
    }
    if name.contains(':') {
        return Err(VfsError::InvalidName(
            "':' is not allowed (NTFS alternate data stream separator)",
        ));
    }
    if name.ends_with('.') || name.ends_with(' ') {
        return Err(VfsError::InvalidName(
            "trailing '.' or space is not allowed (reinterpreted by Windows)",
        ));
    }
    if is_windows_reserved(name) {
        return Err(VfsError::InvalidName(
            "Windows reserved device name (CON/PRN/AUX/NUL/COM1-9/LPT1-9)",
        ));
    }
    if reject_reserved && is_reserved_name(name) {
        return Err(VfsError::InvalidName(
            "reserved prefix used by our own control files",
        ));
    }
    Ok(())
}

/// NFC-normalize a name. Used **only** when creating a brand new on-disk
/// name — existing names are never rewritten (see module docs / §2 in
/// DESIGN-CORE.md: rewriting on-disk names breaks external index DBs such as
/// Jellyfin's).
pub(crate) fn normalize_new_name(name: &str) -> String {
    name.nfc().collect()
}

/// Candidate spellings to try, in priority order, when looking up an
/// existing on-disk entry: the exact bytes given, then NFC, then NFD
/// (macOS/HFS+ SMB clients create NFD names). Deduplicated and
/// order-preserving.
pub(crate) fn lookup_candidates(name: &str) -> SmallVec<[String; 3]> {
    let mut v: SmallVec<[String; 3]> = SmallVec::new();
    v.push(name.to_string());
    let nfc: String = name.nfc().collect();
    if !v.contains(&nfc) {
        v.push(nfc);
    }
    let nfd: String = name.nfd().collect();
    if !v.contains(&nfd) {
        v.push(nfd);
    }
    v
}

/// Whole-path candidate spellings for the Linux backend, which resolves a
/// full relative path in a single `openat2` call. We approximate per-name
/// NFC/NFD fallback by trying the same normalization form uniformly across
/// every component — cheap (3 candidates instead of 3^depth) and correct
/// for the overwhelmingly common case where one non-conforming client wrote
/// the whole path in one normalization form. A path with components mixing
/// forms from *different* writers falls outside this approximation; that's
/// judged an acceptable gap given how rare it is in practice.
///
/// The root (no components) always yields `["."]`, the relative path
/// `openat2` understands as "this directory".
///
/// Only the Linux backend uses this (the portable backend resolves one
/// component at a time instead — see `backend::portable::find_entry`), so
/// it's dead code on non-Linux builds.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
pub(crate) fn path_candidates(comps: &[CompactString]) -> SmallVec<[String; 3]> {
    let mut v: SmallVec<[String; 3]> = SmallVec::new();
    if comps.is_empty() {
        v.push(".".to_string());
        return v;
    }
    let joined = |f: fn(&str) -> String| -> String {
        let mut out = String::new();
        for (i, c) in comps.iter().enumerate() {
            if i > 0 {
                out.push('/');
            }
            out.push_str(&f(c.as_str()));
        }
        out
    };
    let as_is = joined(|s| s.to_string());
    v.push(as_is);
    let nfc = joined(|s| s.nfc().collect());
    if !v.contains(&nfc) {
        v.push(nfc);
    }
    let nfd = joined(|s| s.nfd().collect());
    if !v.contains(&nfd) {
        v.push(nfd);
    }
    v
}
