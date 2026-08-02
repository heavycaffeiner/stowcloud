//! `GET /index.php/core/preview` and `/index.php/core/preview.png`.
//!
//! # We never serve image bytes from the app origin
//!
//! user-supplied content is rendered only on the
//! separate content origin, behind a signed URL, so that a malicious upload
//! cannot execute in the origin that holds session cookies. That invariant does
//! not get an exception for compat clients.
//!
//! So this endpoint resolves the file, mints a signed URL on the content
//! origin, and answers **302**. Mobile clients follow redirects, so nothing is
//! lost. Proxying the bytes here would be a one-line change and would quietly
//! delete the entire origin-isolation design.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{header, StatusCode};
use axum::response::Response;

use crate::ports::{CorePort, FileId, FitMode, PreviewPort, UserId};

/// Parsed `?fileId=&file=&x=&y=&a=&mode=&forceIcon=`.
#[derive(Clone, Debug, Default)]
pub struct PreviewQuery {
    pub file_id: Option<i64>,
    /// The `preview.png` variant addresses by path instead.
    pub path: Option<String>,
    pub x: u32,
    pub y: u32,
    /// `a=1` preserves the aspect ratio; `mode=cover` crops.
    pub fit: FitMode,
    pub force_icon: bool,
}

/// Upper bound on requested dimensions. Without it, `x=100000&y=100000` is a
/// memory-exhaustion request against the thumbnailer.
const MAX_DIM: u32 = 4096;
const DEFAULT_DIM: u32 = 32;

impl PreviewQuery {
    pub fn parse(query: &str) -> Self {
        let mut q = PreviewQuery {
            x: DEFAULT_DIM,
            y: DEFAULT_DIM,
            fit: FitMode::Contain,
            ..Default::default()
        };
        let mut a_flag = true;
        let mut mode_cover = false;
        for pair in query.split('&') {
            let Some((k, v)) = pair.split_once('=') else {
                continue;
            };
            let v = percent_encoding::percent_decode_str(v)
                .decode_utf8_lossy()
                .into_owned();
            match k {
                "fileId" | "fileid" => q.file_id = v.parse().ok(),
                "file" => q.path = Some(v),
                "x" => q.x = v.parse().unwrap_or(DEFAULT_DIM).clamp(1, MAX_DIM),
                "y" => q.y = v.parse().unwrap_or(DEFAULT_DIM).clamp(1, MAX_DIM),
                "a" => a_flag = v != "0",
                "mode" => mode_cover = v == "cover",
                "forceIcon" => q.force_icon = v == "1",
                _ => {}
            }
        }
        // `mode=cover` crops to fill; otherwise `a=1` (the default) preserves
        // the aspect ratio inside the box.
        q.fit = if mode_cover || !a_flag {
            FitMode::Cover
        } else {
            FitMode::Contain
        };
        q
    }
}

pub struct PreviewApi {
    core: Arc<dyn CorePort>,
    preview: Arc<dyn PreviewPort>,
}

impl PreviewApi {
    pub fn new(core: Arc<dyn CorePort>, preview: Arc<dyn PreviewPort>) -> Self {
        Self { core, preview }
    }

    pub fn redirect(&self, user: UserId, q: &PreviewQuery) -> Response {
        let located = match (q.file_id, &q.path) {
            (Some(id), _) => self.core.locate(user, FileId(id)).ok(),
            (None, Some(p)) => self
                .core
                .home_root(user)
                .ok()
                .map(|root| (root, p.trim_start_matches('/').to_string())),
            _ => None,
        };

        let Some((root, path)) = located else {
            return not_found();
        };

        match self
            .preview
            .signed_thumb_url(user, root, &path, q.x, q.y, q.fit)
        {
            Ok(Some(url)) => Response::builder()
                .status(StatusCode::FOUND)
                .header(header::LOCATION, url)
                // The signed URL is short-lived and user-scoped; never let a
                // shared cache keep it.
                .header(header::CACHE_CONTROL, "private, no-store")
                .body(Body::empty())
                .expect("valid response"),
            // `forceIcon=1` asks us to substitute a generic icon. We answer 404
            // instead and let the client draw its own: serving a placeholder
            // would mean serving bytes from the app origin for a file that has
            // no preview, and clients all ship icons anyway
            // (DESIGN-COMPAT.md §11).
            _ => not_found(),
        }
    }
}

fn not_found() -> Response {
    Response::builder()
        .status(StatusCode::NOT_FOUND)
        .body(Body::empty())
        .expect("valid response")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ports::{
        Aggregate, Entry, PortError, PortResult, Quota, ShareId, UserInfo,
    };

    struct FakeCore;
    impl CorePort for FakeCore {
        fn home_root(&self, _u: UserId) -> PortResult<ShareId> {
            Ok(ShareId(1))
        }
        fn resolve(&self, _s: ShareId, _u: UserId, _p: &str) -> PortResult<Entry> {
            Err(PortError::NotFound)
        }
        fn list(&self, _s: ShareId, _u: UserId, _p: &str) -> PortResult<Vec<Entry>> {
            Ok(vec![])
        }
        fn stat_entry(&self, _s: ShareId, _u: UserId, _p: &str) -> PortResult<Entry> {
            Err(PortError::NotFound)
        }
        fn aggregate(&self, _s: ShareId, _i: FileId) -> PortResult<Aggregate> {
            Ok(Aggregate { etag: String::new(), rsize: 0, rcount: 0 })
        }
        fn user_info(&self, _u: UserId) -> PortResult<UserInfo> {
            Err(PortError::NotFound)
        }
        fn quota(&self, _u: UserId) -> PortResult<Quota> {
            Ok(Quota { used: 0, free: 0, total: None })
        }
        fn locate(&self, _u: UserId, id: FileId) -> PortResult<(ShareId, String)> {
            if id.0 == 123 {
                Ok((ShareId(1), "pic.jpg".into()))
            } else {
                Err(PortError::NotFound)
            }
        }
    }

    struct FakePreview(bool);
    impl PreviewPort for FakePreview {
        fn can_preview(&self, _e: &Entry) -> bool {
            self.0
        }
        fn signed_thumb_url(
            &self,
            _u: UserId,
            _s: ShareId,
            path: &str,
            w: u32,
            h: u32,
            _f: FitMode,
        ) -> PortResult<Option<String>> {
            if self.0 {
                Ok(Some(format!(
                    "https://content.example.com/t/{path}?w={w}&h={h}&sig=deadbeef"
                )))
            } else {
                Ok(None)
            }
        }
    }

    fn api(ok: bool) -> PreviewApi {
        PreviewApi::new(Arc::new(FakeCore), Arc::new(FakePreview(ok)))
    }

    #[test]
    fn query_parsing() {
        let q = PreviewQuery::parse("fileId=123&x=256&y=256&a=1&forceIcon=0&mode=cover");
        assert_eq!(q.file_id, Some(123));
        assert_eq!(q.x, 256);
        assert_eq!(q.fit, FitMode::Cover);
        assert!(!q.force_icon);

        let q = PreviewQuery::parse("fileId=1&a=1");
        assert_eq!(q.fit, FitMode::Contain);
        assert_eq!(q.x, 32, "default box size");

        // Dimensions are clamped, not trusted.
        let q = PreviewQuery::parse("fileId=1&x=999999&y=0");
        assert_eq!(q.x, 4096);
        assert_eq!(q.y, 1);

        let q = PreviewQuery::parse("file=%2Fphotos%2Fa.jpg&x=64&y=64");
        assert_eq!(q.path.as_deref(), Some("/photos/a.jpg"));
    }

    /// The invariant this endpoint exists to preserve.
    /// The literal query strings the two mobile clients build. Both apps lean
    /// on previews far harder than the desktop client does — the Android photo
    /// grid and the iOS media browser are *entirely* thumbnails — so a
    /// parameter we silently mis-parse shows up as a wall of blank tiles.
    #[test]
    fn the_exact_mobile_preview_queries_parse() {
        // ThumbnailsCacheManager.java:648-650, with pxW/pxH substituted.
        let q = PreviewQuery::parse("fileId=4711&x=512&y=512&a=1&mode=cover&forceIcon=0");
        assert_eq!(q.file_id, Some(4711));
        assert_eq!((q.x, q.y), (512, 512));
        assert_eq!(q.fit, FitMode::Cover);
        assert!(!q.force_icon);

        // the iOS SDK's +API.swift:443 — same shape plus two parameters the
        // Android client does not send. They must be ignored, not treated as
        // a parse failure that resets the dimensions to the 32px default.
        let q = PreviewQuery::parse(
            "fileId=4711&x=1024&y=1024&a=1&mode=cover&forceIcon=0&mimeFallback=0&etag=abc123",
        );
        assert_eq!(q.file_id, Some(4711));
        assert_eq!(
            (q.x, q.y),
            (1024, 1024),
            "iOS's default preview box is 1024x1024 (the iOS SDK's +API.swift:428-429)"
        );
        assert_eq!(q.fit, FitMode::Cover);

        // iOS trash previews default to 512 and omit `etag`
        // (the iOS SDK's +API.swift:544-549).
        let q = PreviewQuery::parse("fileId=1&x=512&y=512&a=1&mode=cover&forceIcon=0&mimeFallback=0");
        assert_eq!((q.x, q.y), (512, 512));
    }

    #[test]
    fn preview_redirects_to_the_content_origin_and_serves_no_bytes() {
        let r = api(true).redirect(UserId(1), &PreviewQuery::parse("fileId=123&x=256&y=256"));
        assert_eq!(r.status(), StatusCode::FOUND);
        let loc = r.headers().get(header::LOCATION).unwrap().to_str().unwrap();
        assert!(loc.starts_with("https://content.example.com/"));
        assert!(loc.contains("sig="));
        assert_eq!(
            r.headers().get(header::CACHE_CONTROL).unwrap(),
            "private, no-store"
        );
    }

    /// The redirect status is not free to choose.
    ///
    /// Android does not let its HTTP stack follow redirects
    /// (`OwnCloudClient.java:190` sets `setFollowRedirects(false)`) and instead
    /// runs its own loop, which handles **exactly** 301, 302 and 307:
    ///
    /// ```text
    /// OwnCloudClient.java:235-238
    ///     while (redirectionsCount < MAX_REDIRECTIONS_COUNT &&
    ///             (status == HttpStatus.SC_MOVED_PERMANENTLY ||
    ///              status == HttpStatus.SC_MOVED_TEMPORARILY ||
    ///              status == HttpStatus.SC_TEMPORARY_REDIRECT))
    /// ```
    ///
    /// A 303 or 308 falls out of that loop and is returned as-is; the caller
    /// checks `status == SC_OK` and drops the thumbnail. `MAX_REDIRECTIONS_COUNT`
    /// is 3, so the hop budget is fine, but the code must be one of the three.
    #[test]
    fn the_preview_redirect_is_a_302_because_android_follows_only_301_302_307() {
        let r = api(true).redirect(UserId(1), &PreviewQuery::parse("fileId=123&x=256&y=256"));
        assert_eq!(r.status(), StatusCode::FOUND, "302, not 303 and not 308");
    }

    /// iOS sends `If-None-Match` on preview and avatar requests and then
    /// validates `200..<300` (`the iOS SDK's +API.swift:450-455`), so a 304 is an
    /// **error** to it, not a cache hit — the thumbnail is dropped and retried
    /// on every scroll. We must never answer 304 here regardless of what the
    /// client offers as a validator.
    #[test]
    fn a_conditional_preview_request_never_yields_304() {
        // The etag parameter iOS appends is a cache-buster for its own URL
        // cache; it must not turn into a server-side revalidation.
        let r = api(true).redirect(
            UserId(1),
            &PreviewQuery::parse("fileId=123&x=256&y=256&etag=abc123"),
        );
        assert_ne!(r.status(), StatusCode::NOT_MODIFIED);
        assert_eq!(r.status(), StatusCode::FOUND);
    }

    #[test]
    fn missing_preview_is_404_not_a_placeholder_icon() {
        let r = api(false).redirect(UserId(1), &PreviewQuery::parse("fileId=123&forceIcon=1"));
        assert_eq!(r.status(), StatusCode::NOT_FOUND);
    }

    #[test]
    fn unknown_file_is_404() {
        let r = api(true).redirect(UserId(1), &PreviewQuery::parse("fileId=999"));
        assert_eq!(r.status(), StatusCode::NOT_FOUND);
        let r = api(true).redirect(UserId(1), &PreviewQuery::parse(""));
        assert_eq!(r.status(), StatusCode::NOT_FOUND);
    }
}
