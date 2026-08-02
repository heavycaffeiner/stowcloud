//! `sc-http` — native REST API + WebSocket + middleware stack for the web
//! UI. See `docs/DESIGN-API.md` (authoritative). This crate is intentionally
//! isolated from the *other* cloud-storage compatibility protocol this
//! workspace also speaks — those routes live entirely in the sibling
//! `sc-compat-nc` crate, and a CI check enforces that none of that
//! protocol's naming ever leaks in here. If you're about to add a
//! capability/config field that mirrors a concept from that protocol, name
//! it after *our* API's own vocabulary, not theirs.

pub mod archive_zip;
pub mod config;
pub mod content;
pub mod content_api;
pub mod core_api;
pub mod error;
pub mod listing;
pub mod middleware;
pub mod oidc_api;
pub mod rate_limit;
pub mod routes;
pub mod search_api;
pub mod search_limits;
pub mod settings_api;
pub mod setup_api;
pub mod state;
pub mod upload_api;
pub mod ws;

#[cfg(any(test, feature = "test-support"))]
pub mod testutil;

use axum::Router;
use tower_http::limit::RequestBodyLimitLayer;

/// The browser session cookie's name, in one place.
///
/// `__Host-` is a prefix with teeth: a browser only accepts such a cookie over
/// a secure origin, with `Path=/` and no `Domain`, which is why the app cannot
/// be used over plaintext HTTP anywhere but localhost.
///
/// It lives here as a constant because it did not, and the cost showed up
/// immediately: the compat layer looked for `sc_session`, a
/// name nothing has ever set, so it never found a logged-in browser. Every
/// visit to its login-flow consent screen was judged unauthenticated and
/// redirected to `/login`, which redirected back — a loop that reads, from a
/// phone, as the login form quietly resetting after correct credentials.
///
/// Anything that reads or writes this cookie must use this constant.
pub const SESSION_COOKIE: &str = "__Host-sc_sid";

/// The OIDC flow-binding cookie
/// (`docs/proposals/stowcloud-0-oidc-login.md` §4.3.1).
///
/// Set by `/api/auth/oidc/start` and `POST /api/auth/oidc/link/start`, read
/// once by the callback, and expired immediately afterwards whether the
/// callback succeeded or not.
///
/// It exists because `state` alone does not stop login-CSRF. `state` is
/// server-issued, server-stored and single-use, and none of that prevents an
/// attacker who started their *own* flow from delivering the resulting
/// callback URL to a victim's browser as a top-level navigation: the flow
/// record is genuine, the nonce matches, and the victim ends up holding a
/// session for the attacker's account. Binding the flow to a cookie the
/// victim's browser never received is what closes that, and it is what RFC
/// 9700 means by binding `state` to the user agent.
///
/// `SameSite=Lax` is deliberate and sufficient: the callback arrives as a
/// top-level GET navigation from the IdP, which Lax permits. `__Host-` costs
/// nothing extra here and rules out a subdomain writing it.
pub const OIDC_FLOW_COOKIE: &str = "__Host-sc_oidc";

pub use state::AppState;

/// Assembles the full router with the middleware stack in `DESIGN-API.md`
/// §9's exact order.
///
/// Axum layers wrap in last-added-is-outermost order, so to get the
/// documented request-phase execution order (`RequestId` first ...
/// `AclScope` last before the handler), layers are added here in the reverse
/// sequence — see the module doc comment in `middleware.rs` for the full
/// derivation. `BodyLimit` (§9 step 6) is the one exception implemented as a
/// router split rather than a `from_fn` layer: `/api/uploads/**` lives in a
/// sibling router that never receives `RequestBodyLimitLayer`, so TUS
/// chunks of arbitrary size are never rejected by it
/// (`DESIGN-UPLOAD.md` §1.3/§8).
pub fn build_router(state: AppState) -> Router {
    let protected = routes::protected_routes(state.clone()).layer(RequestBodyLimitLayer::new(state.cfg.body_limit_bytes));
    let uploads = routes::upload_routes(state.clone());
    let merged = protected.merge(uploads);

    merged
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::error_mapper))
        .layer(axum::middleware::from_fn(middleware::audit_sink))
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::scope_gate))
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::csrf))
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::auth))
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::rate_limit))
        .layer(axum::middleware::from_fn(middleware::security_headers))
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::host_guard))
        .layer(axum::middleware::from_fn_with_state(state.clone(), middleware::trusted_proxy))
        .layer(axum::middleware::from_fn(middleware::request_id))
}

#[cfg(feature = "embed-ui")]
pub mod embed {
    //! Serves the built frontend (`web/build`) when compiled with
    //! `--features embed-ui`. Off by default — both here and transitively in
    //! `sc-server` — so the crate (and the real binary) builds before the
    //! frontend exists: `web/build` is `.gitignore`d, so a fresh checkout has
    //! none, and `#[derive(RustEmbed)]` reads this folder at *compile* time.
    //! `ARCHITECTURE.md` §7 /
    //!
    //! The actual mount point is `routes::admin_catch_all` — the router's
    //! single global fallback — not a route of its own; see that function's
    //! doc comment for why it has to sit there.

    /// Marks a response as the SPA's HTML document, for
    /// [`crate::middleware::security_headers`].
    ///
    /// It cannot infer this from `Content-Type`: a `304` carries no body and
    /// therefore no `Content-Type`, yet its headers replace the stored ones on
    /// the cached response the browser is about to render. Sniffing got the
    /// document's own revalidation exactly backwards.
    #[derive(Clone, Copy)]
    pub struct SpaDocument;

    use axum::body::Body;
    use axum::http::{header, HeaderMap, HeaderValue, Method, StatusCode};
    use axum::response::Response;
    use base64::Engine as _;
    use rust_embed::RustEmbed;
    use sha2::Digest;

    #[derive(RustEmbed)]
    #[folder = "../../web/build/"]
    pub struct Assets;

    const INDEX: &str = "index.html";

    /// SvelteKit's adapter always emits an `immutable/` folder for its
    /// content-hashed bundle, under whatever `kit.appDir` is configured to
    /// (this build uses `app/`, not the SvelteKit default `_app/` — see
    /// `web/svelte.config.js`). Matching on the fixed inner segment name
    /// rather than a hardcoded app-dir spelling is what makes the cache
    /// policy below robust to that choice.
    fn is_immutable_asset(rel_path: &str) -> bool {
        rel_path.split('/').any(|seg| seg == "immutable")
    }

    fn etag_of(hash: [u8; 32]) -> HeaderValue {
        let hex = data_encoding::HEXLOWER.encode(&hash);
        HeaderValue::from_str(&format!("\"{hex}\"")).unwrap_or_else(|_| HeaderValue::from_static("\"0\""))
    }

    fn content_type_of(rel_path: &str) -> HeaderValue {
        let guess = mime_guess::from_path(rel_path).first_or_octet_stream();
        // SvelteKit's HTML is always UTF-8; a bare `text/html` leaves a
        // browser free to sniff the charset instead of trusting the byte
        // stream, which is exactly the ambiguity `X-Content-Type-Options:
        // nosniff` (already set by `middleware::security_headers`) is
        // supposed to remove.
        if guess.essence_str() == "text/html" {
            return HeaderValue::from_static("text/html; charset=utf-8");
        }
        HeaderValue::from_str(guess.as_ref()).unwrap_or_else(|_| HeaderValue::from_static("application/octet-stream"))
    }

    fn respond(rel_path: &str, file: rust_embed::EmbeddedFile, headers: &HeaderMap, method: &Method) -> Response {
        let etag = etag_of(file.metadata.sha256_hash());
        let is_document = content_type_of(rel_path).as_bytes().starts_with(b"text/html");
        if let Some(inm) = headers.get(header::IF_NONE_MATCH).and_then(|v| v.to_str().ok()) {
            if inm.as_bytes() == etag.as_bytes() || inm == "*" {
                let mut resp = Response::new(Body::empty());
                *resp.status_mut() = StatusCode::NOT_MODIFIED;
                resp.headers_mut().insert(header::ETAG, etag);
                // A `304` has no body and therefore no `Content-Type`, but it
                // still *replaces the stored headers* of the cached response
                // the browser is about to reuse (RFC 9110 §15.4.5). Without
                // this marker `security_headers` sniffed `Content-Type`, saw
                // none, and sent the CSP for a non-document — dropping the
                // hash that permits the SPA's inline bootstrap. The browser
                // then paired its cached HTML with a policy that forbids it
                // and rendered nothing.
                //
                // First visit worked and every visit after it was a blank
                // page, which is the worst shape a bug can have: absent from
                // the run you test with and present for everyone else.
                if is_document {
                    resp.extensions_mut().insert(SpaDocument);
                }
                return resp;
            }
        }
        // The build's own content-hashed bundle is safe to cache forever —
        // a change in content always shows up under a new hashed filename
        // (`is_immutable_asset`), so nothing ever needs to invalidate one of
        // these. Everything else — `index.html` above all — is rewritten in
        // place on every deploy and must be revalidated on every request;
        // the `ETag` still turns a revisit into a `304` with no body, which
        // is most of what a mobile client re-opening the app actually pays
        // for on a slow link.
        let cache = if is_immutable_asset(rel_path) {
            HeaderValue::from_static("public, max-age=31536000, immutable")
        } else {
            HeaderValue::from_static("no-cache")
        };
        let body = if *method == Method::HEAD { Body::empty() } else { Body::from(file.data.into_owned()) };
        let mut resp = Response::new(body);
        let h = resp.headers_mut();
        h.insert(header::CONTENT_TYPE, content_type_of(rel_path));
        h.insert(header::CACHE_CONTROL, cache);
        h.insert(header::ETAG, etag);
        if is_document {
            resp.extensions_mut().insert(SpaDocument);
        }
        resp
    }

    /// Answers one request against the embedded SPA build, or `None` if there
    /// is nothing to serve at all (an `index.html`-less build — the caller
    /// falls back to the ordinary JSON `404`).
    ///
    /// `path` is a normal absolute URL path (`req.uri().path()`) — the
    /// caller (`routes::admin_catch_all`) has already turned away every
    /// reserved-prefix path and the content origin before this is reached,
    /// so nothing that looks like `/api/**`, a WebDAV path, or a
    /// compatibility-layer path ever gets here.
    pub fn serve(path: &str, headers: &HeaderMap, method: &Method) -> Option<Response> {
        let rel = path.trim_start_matches('/');
        if let Some(file) = Assets::get(rel) {
            return Some(respond(rel, file, headers, method));
        }
        // SPA fallback: anything that isn't a real
        // built asset is a client-routed page — only the app itself, once
        // loaded, knows whether `/b/Documents/Reports` is a real folder, so
        // a deep link has to survive a refresh by getting the same document
        // `/` would.
        Assets::get(INDEX).map(|file| respond(INDEX, file, headers, method))
    }

    /// CSP `script-src` additions covering the inline bootstrap `<script>`
    /// SvelteKit's adapter emits directly into `index.html`.
    ///
    /// The default CSP (`middleware::security_headers`) is `script-src
    /// 'self'`, which blocks inline script content outright — and
    /// `'unsafe-inline'` would matter for every response that CSP covers,
    /// not only this one document, so it is not the fix. Instead this hashes
    /// the *exact bytes* of whatever inline script(s) are actually embedded
    /// and allowlists only those hashes, only on an HTML response
    /// (`security_headers` only asks for this when `Content-Type` is
    /// `text/html`). If a future build changes the script, the hash changes
    /// with it automatically the next time this is computed — nothing here
    /// is pinned to one build's output.
    pub fn inline_script_csp_sources() -> &'static str {
        static CACHE: std::sync::OnceLock<String> = std::sync::OnceLock::new();
        CACHE.get_or_init(|| {
            let Some(file) = Assets::get(INDEX) else { return String::new() };
            let html = String::from_utf8_lossy(&file.data).into_owned();
            let mut out = String::new();
            for body in inline_script_bodies(&html) {
                let digest = sha2::Sha256::digest(body.as_bytes());
                let b64 = base64::engine::general_purpose::STANDARD.encode(digest);
                out.push_str(&format!(" 'sha256-{b64}'"));
            }
            out
        })
    }

    /// The text content of every inline (no `src=`) `<script>` element in
    /// `html`, as byte ranges into the original string.
    ///
    /// A hand-rolled scanner, not a full HTML parser: the input is this
    /// build's own `index.html` — never attacker-controlled — and
    /// SvelteKit's adapter emits well-formed, lower-case markup, so pulling
    /// in a general HTML-parsing dependency for a search this constrained
    /// would be a lot of weight for no real benefit. Matching is done
    /// against an ASCII-lowercased copy so the scan is case-insensitive
    /// without disturbing byte offsets — ASCII lowercasing never changes a
    /// UTF-8 string's byte length, so slicing the *original* `html` at the
    /// same indices is exact.
    fn inline_script_bodies(html: &str) -> Vec<&str> {
        let lower = html.to_ascii_lowercase();
        let mut out = Vec::new();
        let mut idx = 0usize;
        while let Some(rel) = lower[idx..].find("<script") {
            let tag_start = idx + rel;
            let Some(gt_rel) = lower[tag_start..].find('>') else { break };
            let tag_end = tag_start + gt_rel;
            let has_src = lower[tag_start..tag_end].contains("src=");
            let content_start = tag_end + 1;
            let Some(close_rel) = lower[content_start..].find("</script>") else { break };
            let content_end = content_start + close_rel;
            if !has_src {
                out.push(&html[content_start..content_end]);
            }
            idx = content_end + "</script>".len();
        }
        out
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn finds_the_one_inline_bootstrap_script() {
            let html = r#"<html><head><script src="/x.js"></script></head>
                <body><script>console.log("hi")</script></body></html>"#;
            let bodies = inline_script_bodies(html);
            assert_eq!(bodies, vec!["console.log(\"hi\")"]);
        }

        #[test]
        fn no_inline_script_yields_no_hashes() {
            let html = r#"<html><head><script src="/x.js"></script></head></html>"#;
            assert!(inline_script_bodies(html).is_empty());
        }
    }
}

#[cfg(test)]
mod integration_tests {
    use super::*;
    use axum::body::Body;
    use axum::http::{Request, StatusCode};
    use tower::ServiceExt;

    fn app() -> (Router, tempfile::TempDir) {
        let (state, dir) = testutil::test_state();
        (build_router(state), dir)
    }

    #[tokio::test]
    async fn capabilities_is_reachable_end_to_end() {
        let (app, _dir) = app();
        let req = Request::builder()
            .uri("/api/capabilities")
            .header("Host", "localhost")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn unknown_host_is_rejected() {
        let (app, _dir) = app();
        let req = Request::builder()
            .uri("/api/capabilities")
            .header("Host", "evil.example.com")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::MISDIRECTED_REQUEST);
    }

    #[tokio::test]
    async fn protected_route_without_session_is_401() {
        let (app, _dir) = app();
        let req = Request::builder()
            .uri("/api/fs/list")
            .header("Host", "localhost")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    }

    /// Through the whole assembled stack, not just the `auth` layer: the
    /// login screen asks this before it holds any credential, and a route
    /// that is registered but not public, or public but not registered,
    /// answers 401 or 404 in exactly the same way here and nowhere else.
    #[tokio::test]
    async fn oidc_config_is_reachable_unauthenticated_end_to_end() {
        let (app, _dir) = app();
        let req = Request::builder()
            .uri("/api/auth/oidc/config")
            .header("Host", "localhost")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    /// The same stack, on the two self-service routes: nothing about OIDC
    /// makes credential management public.
    #[tokio::test]
    async fn the_oidc_link_routes_still_demand_a_session_end_to_end() {
        let (app, _dir) = app();
        for (method, uri) in [("POST", "/api/auth/oidc/link/start"), ("DELETE", "/api/auth/oidc/link")] {
            let req = Request::builder()
                .method(method)
                .uri(uri)
                .header("Host", "localhost")
                .body(Body::empty())
                .unwrap();
            let resp = app.clone().oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::UNAUTHORIZED, "{method} {uri}");
        }
    }

    #[tokio::test]
    async fn security_headers_present_end_to_end() {
        let (app, _dir) = app();
        let req = Request::builder()
            .uri("/api/capabilities")
            .header("Host", "localhost")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert!(resp.headers().get("X-Content-Type-Options").is_some());
        assert!(resp.headers().get("Sc-Trace").is_some());
    }
}
