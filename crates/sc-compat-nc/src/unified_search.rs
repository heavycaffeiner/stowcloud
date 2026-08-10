//! The unified-search provider.
//!
//! `GET /ocs/v2.php/search/providers` advertises one provider and
//! `GET /ocs/v2.php/search/providers/files/search` answers it. This is an OCS
//! envelope over the same filename search the DAV `SEARCH` method uses, so the
//! two screens cannot disagree about what a term matches.

use std::sync::Arc;

use crate::ocs::{OcsError, Val};
use crate::ports::{FitMode, PreviewPort, SearchPort, UserId};

/// The provider list. One entry, because files are the only thing this server
/// has to search.
pub fn providers() -> Val {
    Val::List(vec![Val::map([
        ("id", Val::str("files")),
        ("name", Val::str("Files")),
        ("order", Val::Int(0)),
    ])])
}

/// Default page size when the client does not ask for one, and the ceiling it
/// is clamped to. Both apps page with `cursor`, so a small page is cheap.
const DEFAULT_LIMIT: u32 = 25;
const MAX_LIMIT: u32 = 100;

pub struct UnifiedSearchApi {
    search: Arc<dyn SearchPort>,
    preview: Arc<dyn PreviewPort>,
}

impl UnifiedSearchApi {
    pub fn new(search: Arc<dyn SearchPort>, preview: Arc<dyn PreviewPort>) -> Self {
        Self { search, preview }
    }

    pub fn search(
        &self,
        user: UserId,
        term: &str,
        limit: Option<u32>,
        cursor: Option<u32>,
        origin: &str,
    ) -> Result<Val, OcsError> {
        let limit = limit.unwrap_or(DEFAULT_LIMIT).clamp(1, MAX_LIMIT);
        let offset = cursor.unwrap_or(0);
        if term.trim().is_empty() {
            return Ok(page("Files", Vec::new(), None));
        }

        // Fetch one page past the offset, plus one extra row, so the cursor can
        // say whether there is more without a second query.
        let want = offset.saturating_add(limit).saturating_add(1);
        let hits = self
            .search
            .by_name(user, term, want)
            .map_err(|_| OcsError::server_error("search failed"))?;

        let total = hits.len() as u32;
        let entries: Vec<Val> = hits
            .into_iter()
            .skip(offset as usize)
            .take(limit as usize)
            .map(|h| {
                let thumb = self
                    .preview
                    .signed_thumb_url(user, &crate::ports::Vpath::new(&h.path), 64, 64, FitMode::Cover)
                    .ok()
                    .flatten();
                let absolute = format!("/{}", h.path);
                Val::map([
                    (
                        "thumbnailUrl",
                        match thumb {
                            Some(u) => Val::str(u),
                            None => Val::str(""),
                        },
                    ),
                    ("title", Val::str(h.entry.name.clone())),
                    // The parent path, which is what the reference puts here
                    // and what both apps show under the title.
                    ("subline", Val::str(parent_of(&absolute))),
                    ("resourceUrl", Val::str(format!("{}/apps/files/?dir={}", origin.trim_end_matches('/'), parent_of(&absolute)))),
                    ("icon", Val::str("")),
                    ("rounded", Val::Bool(false)),
                    (
                        "attributes",
                        // Both apps read `attributes.path` in preference to
                        // parsing `resourceUrl`, so it is always populated.
                        Val::map([
                            (
                                "fileId",
                                Val::str(
                                    h.entry
                                        .id
                                        .map(|i| i.0.to_string())
                                        .unwrap_or_default(),
                                ),
                            ),
                            ("path", Val::str(absolute)),
                        ]),
                    ),
                ])
            })
            .collect();

        let next = if total > offset.saturating_add(limit) {
            Some(offset + limit)
        } else {
            None
        };
        Ok(page("Files", entries, next))
    }
}

fn page(name: &str, entries: Vec<Val>, cursor: Option<u32>) -> Val {
    Val::map([
        ("name", Val::str(name)),
        ("isPaginated", Val::Bool(true)),
        ("entries", Val::List(entries)),
        (
            "cursor",
            match cursor {
                Some(c) => Val::Int(c as i64),
                None => Val::Null,
            },
        ),
    ])
}

fn parent_of(absolute: &str) -> String {
    match absolute.rsplit_once('/') {
        Some(("", _)) | None => "/".to_string(),
        Some((dir, _)) => dir.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_provider_list_names_exactly_one_provider() {
        let v = providers().to_json();
        let arr = v.as_array().unwrap();
        assert_eq!(arr.len(), 1);
        assert_eq!(arr[0]["id"], "files");
    }

    #[test]
    fn parent_of_a_top_level_entry_is_the_root() {
        assert_eq!(parent_of("/a.txt"), "/");
        assert_eq!(parent_of("/photos/a.txt"), "/photos");
    }
}
