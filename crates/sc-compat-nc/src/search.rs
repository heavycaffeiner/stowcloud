//! The vendor half of a `SEARCH`, and the `oc:filter-files` report.
//!
//! `sc-dav` parses `d:basicsearch` and hands on every comparison against a
//! property outside `DAV:` verbatim. Two of them matter, and both are this
//! crate's vocabulary rather than the protocol's: the favourites flag and the
//! file id. The same two are what the favourites report filters on, so the
//! report and the equivalent search reduce to one structure here.

use crate::props::{NS_NC, NS_OC};

/// What the clients ask for that is not `DAV:`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct VendorFilters {
    /// Only entries this user has marked. Answered from the favourites table,
    /// never from a walk.
    pub favourites_only: bool,
    /// Exact file id. When set, the answer is a lookup and no walk happens.
    pub file_id: Option<i64>,
}

/// Interpret `(namespace, local-name, literal, in_disjunction)` tuples, and
/// report back the comparisons that were not read.
///
/// The literal spelling differs by client: Android sends `yes` for the
/// favourites flag and iOS sends `1`. Both mean the same thing and both are
/// accepted; anything else is read as "not filtering on it" rather than as an
/// error, because a client that sends a third spelling wants its favourites,
/// not a 400.
///
/// The second half of the return names every comparison this vocabulary does
/// not read and that sat outside a `d:or`. Dropping one of those would answer a
/// wider query than was asked, so the caller refuses the request; a disjunct is
/// left out of the list, because dropping it narrows. `sc-dav` cannot make this
/// call: it deliberately does not know what `oc:favorite` means, so it cannot
/// tell a claimed property from an unclaimed one.
pub fn vendor_filters<'a, I>(terms: I) -> (VendorFilters, Vec<String>)
where
    I: IntoIterator<Item = (&'a str, &'a str, &'a str, bool)>,
{
    let mut f = VendorFilters::default();
    let mut unread = Vec::new();
    for (ns, name, literal, in_disjunction) in terms {
        let read = (ns == NS_OC || ns == NS_NC)
            && match name {
                "favorite" => {
                    if matches!(literal.trim(), "1" | "yes" | "true") {
                        f.favourites_only = true;
                    }
                    true
                }
                "fileid" | "id" => {
                    if let Ok(n) = literal.trim().parse::<i64>() {
                        f.file_id = Some(n);
                    }
                    true
                }
                _ => false,
            };
        if !read && !in_disjunction {
            unread.push(format!("{{{ns}}}{name}"));
        }
    }
    (f, unread)
}

/// The `(namespace, local-name)` of the favourites report both clients send.
pub const FILTER_FILES_REPORT: (&str, &str) = (NS_OC, "filter-files");

/// The favourite flag, as a property name. Read on `PROPFIND`, written on
/// `PROPPATCH`, and filtered on by both the report and the search.
pub const FAVORITE_PROPERTY: (&str, &str) = (NS_OC, "favorite");

/// A search scope href, reduced to a vpath in the caller's own tree.
///
/// The href a client sends is `/files/{userId}` or something under it, because
/// that is the URL layout its files live at. `None` means the scope named
/// another account, which is refused outright: narrowing or reinterpreting it
/// is how a search endpoint becomes a cross-account read.
///
/// An empty remainder is the caller's whole tree, which is not itself a path:
/// the files root is a synthesised collection of grant labels, so a search
/// scoped to it spans every root rather than resolving to one.
pub fn scope_to_vpath(scope_href: &str, login: &str) -> Option<Option<String>> {
    let s = scope_href.trim_matches('/');
    if s.is_empty() {
        return Some(None);
    }
    // Both the `files/{user}` layout and the older `webdav` alias reach here;
    // anything else is the caller's own tree already.
    let rest = if let Some(r) = s.strip_prefix("files/") {
        let (user, tail) = match r.split_once('/') {
            Some((u, t)) => (u, t),
            None => (r, ""),
        };
        if user != login {
            return None;
        }
        tail
    } else if let Some(r) = s.strip_prefix("webdav") {
        r.trim_start_matches('/')
    } else {
        s
    };
    Some(if rest.is_empty() {
        None
    } else {
        Some(rest.to_string())
    })
}

/// Media-type prefixes to filename extensions.
///
/// The walker matches names and never opens a file, so `image/%` has to become
/// a set of extensions. Being wrong is cheap in one direction only: a missing
/// extension hides a photo from the timeline, a spurious one costs a thumbnail
/// 404. The list therefore errs generous.
pub fn mime_prefix_extensions(prefix: &str) -> &'static [&'static str] {
    let p = prefix.trim_end_matches('/').to_ascii_lowercase();
    match p.split('/').next().unwrap_or("") {
        "image" => &[
            "jpg", "jpeg", "png", "gif", "webp", "bmp", "tif", "tiff", "avif", "heic", "heif",
            "svg", "ico", "jfif", "dng", "raw", "cr2", "nef", "arw",
        ],
        "video" => &[
            "mp4", "mkv", "mov", "avi", "webm", "m4v", "wmv", "mpg", "mpeg", "3gp", "flv", "ts",
        ],
        "audio" => &[
            "mp3", "flac", "wav", "ogg", "m4a", "aac", "opus", "wma", "aiff",
        ],
        _ => &[],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn both_clients_spellings_of_the_favourites_flag_are_accepted() {
        // Android sends `yes`, iOS sends `1`.
        assert!(vendor_filters([(NS_OC, "favorite", "yes", false)]).0.favourites_only);
        assert!(vendor_filters([(NS_OC, "favorite", "1", false)]).0.favourites_only);
        assert!(!vendor_filters([(NS_OC, "favorite", "0", false)]).0.favourites_only);
    }

    #[test]
    fn a_file_id_comparison_is_read_as_a_lookup() {
        assert_eq!(
            vendor_filters([(NS_OC, "fileid", "4711", false)]).0.file_id,
            Some(4711)
        );
        assert_eq!(vendor_filters([(NS_OC, "fileid", "abc", false)]).0.file_id, None);
    }

    /// A conjunct nobody read would widen the answer, so it is reported. The
    /// same comparison inside a `d:or` narrows when dropped, so it is not.
    #[test]
    fn an_unread_conjunct_is_reported_and_an_unread_disjunct_is_not() {
        let (_, unread) = vendor_filters([(NS_OC, "size", "0", false)]);
        assert_eq!(unread, vec![format!("{{{NS_OC}}}size")]);
        let (_, unread) = vendor_filters([(NS_OC, "size", "0", true)]);
        assert!(unread.is_empty());
    }

    #[test]
    fn a_scope_naming_another_account_is_refused_not_narrowed() {
        assert_eq!(scope_to_vpath("files/alice", "alice"), Some(None));
        assert_eq!(
            scope_to_vpath("files/alice/photos", "alice"),
            Some(Some("photos".into()))
        );
        assert_eq!(scope_to_vpath("files/bob/photos", "alice"), None);
        assert_eq!(scope_to_vpath("", "alice"), Some(None));
    }
}
