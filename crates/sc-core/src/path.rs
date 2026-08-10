//! The two path vocabularies this workspace addresses files by, as types.
//!
//! A `String` and a `SafePath` say nothing about which root they are relative
//! to, and every host adapter that converted between them got it wrong the
//! same way: prefixing a grant label onto a path that already carried one, or
//! onto one whose grant subpath had not been stripped first. Naming the two
//! makes the wrong conversion stop compiling and leaves exactly one right one,
//! [`crate::Core::vpath_for`].

use sc_vfs::SafePath;

use crate::error::CoreError;

/// A path in the caller's own virtual tree: `{label}/{rest}`, where `label`
/// names a grant-projected root. This is what the web UI, the DAV mount and
/// the compat mount all send, and the only thing `Core::resolve_want` accepts.
///
/// Deliberately not constructible from a [`SharePath`] by prefixing: doing
/// that correctly needs a user and a share, which is what
/// [`crate::Core::vpath_for`] is for.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Default)]
pub struct Vpath(String);

impl Vpath {
    /// A vpath this server's own routing produced: the DAV mount's URL
    /// remainder, the web UI's `path` parameter, a label read back out of
    /// `Core::roots`. Leading and trailing separators are stripped; the empty
    /// result is kept rather than refused, because it is the caller's files
    /// root and `resolve_want` already answers `NotFound` for it.
    pub fn new(raw: &str) -> Self {
        Vpath(raw.trim_matches('/').to_string())
    }

    /// Normalise a client-supplied path into a vpath.
    ///
    /// Clients disagree about separators: the Android app appends one to a
    /// folder and does not to a file, and the OCS share API is called with and
    /// without a leading one. A trailing separator reaches `SafePath::parse`
    /// as an empty component and is rejected there, so it is stripped once,
    /// here, rather than at every call site.
    ///
    /// The empty result is the caller's files root, which is a synthesised
    /// collection of grant labels rather than a directory, so it is an error
    /// and not a path.
    pub fn from_client(raw: &str) -> Result<Self, CoreError> {
        let v = Vpath::new(raw);
        if v.is_empty() {
            return Err(CoreError::InvalidPath(
                "the files root is not a directory; name a file or folder inside it".into(),
            ));
        }
        Ok(v)
    }

    /// `{label}/{rest}`, assembled from parts the caller has already proved
    /// belong together. The only in-tree caller is [`crate::Core::vpath_for`].
    pub(crate) fn from_parts(label: &str, rest: &str) -> Self {
        Vpath(if rest.is_empty() {
            label.to_string()
        } else {
            format!("{label}/{rest}")
        })
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// The first segment: the grant label this vpath names a root of.
    pub fn label(&self) -> &str {
        self.0.split('/').next().unwrap_or("")
    }

    /// This vpath with a single leading separator, which is the spelling both
    /// the native and the compat wire formats use.
    pub fn to_absolute_string(&self) -> String {
        format!("/{}", self.0)
    }

    /// Whether `self` names `other` or something inside it, comparing whole
    /// path components so `photos/summerhouse` is not inside `photos/summer`.
    pub fn is_inside(&self, other: &Vpath) -> bool {
        if other.0.is_empty() {
            return true;
        }
        self.0 == other.0
            || (self.0.len() > other.0.len()
                && self.0.starts_with(&other.0)
                && self.0.as_bytes()[other.0.len()] == b'/')
    }
}

impl std::fmt::Display for Vpath {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

/// A path relative to a share's own root, which is what everything below
/// `ShareRoot` speaks and what [`crate::Resolved::path`],
/// [`crate::ShareLink::path`] and `MetaStore::resolve_path` return. It already
/// contains the grant's subpath, so prefixing a label onto it without
/// stripping that subpath doubles it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SharePath(SafePath);

impl Default for SharePath {
    fn default() -> Self {
        SharePath::root()
    }
}

impl SharePath {
    pub fn new(p: SafePath) -> Self {
        SharePath(p)
    }

    pub fn root() -> Self {
        SharePath(SafePath::root())
    }

    /// Parse a share-relative path string, which is what `MetaStore` and the
    /// share-link table store.
    pub fn parse(s: &str, max_depth: u16) -> Result<Self, CoreError> {
        Ok(SharePath(SafePath::parse(s, max_depth)?))
    }

    pub fn as_safe(&self) -> &SafePath {
        &self.0
    }

    pub fn into_safe(self) -> SafePath {
        self.0
    }
}

impl std::ops::Deref for SharePath {
    type Target = SafePath;

    fn deref(&self) -> &SafePath {
        &self.0
    }
}

impl AsRef<SafePath> for SharePath {
    fn as_ref(&self) -> &SafePath {
        &self.0
    }
}

impl From<SafePath> for SharePath {
    fn from(p: SafePath) -> Self {
        SharePath(p)
    }
}

impl crate::Core {
    /// The vpath a share path has in `user`'s own tree: the grant's subpath
    /// stripped off the front, its label prefixed on.
    ///
    /// `None` when no grant the user holds projects that share path, which is
    /// the honest answer for a link whose grant was revoked. Callers that must
    /// still render the row (a listing whose whole purpose is to let the owner
    /// delete it) fall back to the share path; callers that would use it to
    /// address a file must treat `None` as not-found.
    ///
    /// Two grants can project one share under different labels; the first
    /// whose subpath actually prefixes `path` is picked, so every surface
    /// names the same node the same way.
    pub fn vpath_for(
        &self,
        user: sc_vfs::UserId,
        share: sc_vfs::ShareId,
        path: &SharePath,
    ) -> Option<Vpath> {
        for r in self.roots(user) {
            if r.share != share || !r.subpath.is_prefix_of(path) {
                continue;
            }
            let skip = r.subpath.components().len();
            let rest: Vec<&str> = path.components()[skip..]
                .iter()
                .map(|c| c.as_str())
                .collect();
            return Some(Vpath::from_parts(&r.label, &rest.join("/")));
        }
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn from_client_strips_the_separator_android_appends_to_folders() {
        assert_eq!(
            Vpath::from_client("/photos/summer/").unwrap().as_str(),
            "photos/summer"
        );
        assert_eq!(
            Vpath::from_client("photos/a.jpg").unwrap().as_str(),
            "photos/a.jpg"
        );
    }

    #[test]
    fn the_bare_files_root_is_not_a_path_a_client_may_name() {
        assert!(Vpath::from_client("").is_err());
        assert!(Vpath::from_client("/").is_err());
        // The mount's own constructor keeps it, because `resolve_want`
        // already answers `NotFound` there and the synthetic files-root
        // handler runs before it.
        assert!(Vpath::new("/").is_empty());
    }

    #[test]
    fn is_inside_compares_whole_components() {
        let folder = Vpath::new("photos/summer");
        assert!(Vpath::new("photos/summer/a.jpg").is_inside(&folder));
        assert!(Vpath::new("photos/summer").is_inside(&folder));
        assert!(!Vpath::new("photos/summerhouse/a.jpg").is_inside(&folder));
        assert!(!Vpath::new("photos").is_inside(&folder));
    }
}
