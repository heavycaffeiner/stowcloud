//! The axum router.
//!
//! Everything compat-shaped hangs off here, so that `sc-server` mounts (or,
//! with `feature = "compat-nc"` off, does not mount) exactly one thing. The
//! isolation gate in checks that a build without the
//! feature exposes no `remote.php`, `/ocs/`, `status.php` or `/index.php/login`
//! route — that property holds because every one of those strings occurs only
//! in this crate.

use std::sync::Arc;

use axum::body::Body;
use axum::extract::{Path, State};
use axum::http::{HeaderMap, StatusCode, Uri};
use axum::response::{IntoResponse, Redirect, Response};
use axum::routing::{any, get, post};
use axum::Router;

use crate::capabilities::capabilities;
use crate::config::NcConfig;
use crate::login_flow::{consent_html, LoginFlowService};
use crate::ocs::{require_ocs_api_request, Ocs, OcsError, OcsFormat, OcsVersion, Val};
use crate::ports::{ClientAddr, Deps, Scope};
use crate::preview::{PreviewApi, PreviewQuery};
use crate::shares::{ShareesApi, ShareRequest, SharesApi};
use crate::status::status_response;
use crate::store::NcStore;
use crate::stubs;
use crate::user::current_user;

#[derive(Clone)]
pub struct NcState {
    pub cfg: Arc<NcConfig>,
    pub store: Arc<dyn NcStore>,
    pub deps: Deps,
    pub login: Arc<LoginFlowService>,
    pub shares: Arc<SharesApi>,
    pub sharees: Arc<ShareesApi>,
    pub preview: Arc<PreviewApi>,
}

/// Context an OCS handler needs before it can answer at all.
struct OcsCtx {
    version: OcsVersion,
    format: OcsFormat,
}

impl OcsCtx {
    /// Negotiate format and enforce `OCS-APIRequest`.
    ///
    /// The header is the compat protocol's CSRF defence for the whole OCS surface: a
    /// browser cannot set a custom header cross-origin without a preflight, so
    /// its presence proves the request is not a drive-by form post. Accepting
    /// requests without it would expose every state-changing OCS endpoint —
    /// share creation above all — to CSRF.
    ///
    /// Divergence from the reference, deliberately: upstream's
    /// `SecurityMiddleware` throws `CrossSiteRequestForgeryException`, which
    /// renders as a bare `412` with `{"message":"CSRF check failed"}` and no
    /// OCS envelope. specifies 401/403 inside a proper
    /// envelope, which is what we do — it is a strictly better-formed answer
    /// and no client depends on the 412.
    // `Response` (axum::http::Response<Body>) is >=128 bytes, so clippy wants
    // it boxed. Not worth it here: this is a private constructor with one
    // call site, the `Err` path is the rare CSRF-rejection branch (not a hot
    // loop), and the immediate `Err(r) => return r` at that call site would
    // gain nothing from an extra `Box::new`/deref — it would just add
    // ceremony around a single early return.
    #[allow(clippy::result_large_err)]
    fn new(version: OcsVersion, uri: &Uri, headers: &HeaderMap) -> Result<Self, Response> {
        let format = OcsFormat::negotiate(uri.query(), headers);
        if let Err(e) = require_ocs_api_request(headers) {
            return Err(Ocs::err(version, format, e).into_response());
        }
        Ok(Self { version, format })
    }

    fn ok(&self, data: Val) -> Response {
        Ocs::ok(self.version, self.format, data).into_response()
    }

    fn result(&self, r: Result<Val, OcsError>) -> Response {
        match r {
            Ok(d) => self.ok(d),
            Err(e) => Ocs::err(self.version, self.format, e).into_response(),
        }
    }
}

/// Both OCS entry points are the same handlers; only the envelope differs.
fn ocs_version_of(path: &str) -> OcsVersion {
    if path.starts_with("/ocs/v1.php") {
        OcsVersion::V1
    } else {
        OcsVersion::V2
    }
}

pub fn router(state: NcState) -> Router {
    Router::new()
        // --- unauthenticated discovery ---
        .route("/status.php", get(h_status))
        // Captive-portal probe. Upstream's `WalledGardenController` is a
        // `#[PublicPage]` returning a bare 204; the Android client treats
        // anything else as "no internet" and parks every upload as pending
        // without ever issuing a request. Ours had no route at all, so it
        // fell through to the authenticated handler and answered 401 —
        // which is exactly the "captive portal intercepted us" signal.
        .route("/index.php/204", get(h_walled_garden))
        // --- OCS: one wildcard per version, dispatched internally ---
        .route("/ocs/v1.php/{*rest}", any(h_ocs))
        .route("/ocs/v2.php/{*rest}", any(h_ocs))
        // --- Login Flow v2 ---
        .route("/index.php/login/v2", post(h_login_init))
        .route("/index.php/login/v2/poll", post(h_login_poll))
        .route("/index.php/login/v2/flow/{token}", get(h_login_flow))
        // Approval is POST-ONLY and CSRF-checked. There is deliberately no GET
        // route for /grant: a GET that approves is an app-password-issuance
        // CSRF hole (see login_flow.rs).
        .route("/index.php/login/v2/grant", post(h_login_grant))
        // --- previews ---
        .route("/index.php/core/preview", get(h_preview))
        .route("/index.php/core/preview.png", get(h_preview))
        // The Android app's second thumbnail path, used when it has a remote
        // path rather than a file id
        // (`ThumbnailsCacheManager.java:1209`):
        //   /index.php/apps/files/api/v1/thumbnail/{x}/{y}/{path}
        .route(
            "/index.php/apps/files/api/v1/thumbnail/{x}/{y}/{*path}",
            get(h_thumbnail_by_path),
        )
        // Avatars. Both clients fetch these constantly — Android at
        // `ThumbnailsCacheManager.java:958`, iOS at `the iOS SDK's +API.swift:649`
        // — and both use the same non-OCS path shape.
        .route("/index.php/avatar/{user}/{size}", get(h_avatar))
        // --- web UI hand-off ---
        .route("/index.php/apps/files/", get(h_files_redirect))
        .route("/index.php/apps/files", get(h_files_redirect))
        .with_state(state)
}

// ---------------------------------------------------------------------------
// status.php
// ---------------------------------------------------------------------------

async fn h_status(State(s): State<NcState>) -> Response {
    status_response(&s.cfg)
}

/// `GET /index.php/204` — the client's captive-portal check. Deliberately
/// unauthenticated and empty, matching upstream's `WalledGardenController`.
async fn h_walled_garden() -> Response {
    StatusCode::NO_CONTENT.into_response()
}

// ---------------------------------------------------------------------------
// OCS dispatch
// ---------------------------------------------------------------------------

async fn h_ocs(
    State(s): State<NcState>,
    method: axum::http::Method,
    uri: Uri,
    headers: HeaderMap,
    from: ClientAddr,
    body: String,
) -> Response {
    let path = uri.path();
    let version = ocs_version_of(path);

    if stubs::NOT_FOUND_PATHS.contains(&path) {
        return StatusCode::NOT_FOUND.into_response();
    }

    let ctx = match OcsCtx::new(version, &uri, &headers) {
        Ok(c) => c,
        Err(r) => return r,
    };

    // Strip the entry-point prefix so both versions share one match arm.
    let rest = path
        .trim_start_matches("/ocs/v1.php")
        .trim_start_matches("/ocs/v2.php");

    match (method.as_str(), rest) {
        ("GET", "/cloud/capabilities") => {
            ctx.ok(capabilities(&s.cfg, host_header(&headers).as_deref()))
        }

        ("GET", "/cloud/user") => {
            let Some(p) = authenticate(&s, &headers, from) else {
                return ctx.result(Err(OcsError::unauthorized("Unauthorised")));
            };
            let info = s.deps.core.user_info(p.user);
            let quota = s.deps.core.quota(p.user);
            match (info, quota) {
                (Ok(i), Ok(q)) => ctx.ok(current_user(&i, &q)),
                _ => ctx.result(Err(OcsError::server_error("could not read account"))),
            }
        }

        // --- stubs ---
        ("GET", "/apps/notifications/api/v2/notifications") => ctx.ok(stubs::notifications()),
        // Singular `user_status` answers 404 (`stubs::NOT_FOUND_PATHS`, checked
        // above, before this match) rather than a stub 200 here.
        ("GET", "/apps/user_status/api/v1/statuses") => ctx.ok(stubs::user_statuses()),
        ("GET", "/core/navigation/apps") => ctx.ok(stubs::navigation_apps()),
        ("GET", "/core/autocomplete/get") => ctx.ok(stubs::autocomplete()),

        // --- shares ---
        ("GET", "/apps/files_sharing/api/v1/shares") => {
            let Some(p) = authenticate(&s, &headers, from) else {
                return ctx.result(Err(OcsError::unauthorized("Unauthorised")));
            };
            let q = parse_pairs(uri.query().unwrap_or(""));
            let filter = crate::ports::ShareFilter {
                path: find(&q, "path"),
                // The reference compares against the literal string "true";
                // "1" and "TRUE" are falsy there, so they are here too.
                reshares: find(&q, "reshares").as_deref() == Some("true"),
                subfiles: find(&q, "subfiles").as_deref() == Some("true"),
                shared_with_me: find(&q, "shared_with_me").as_deref() == Some("true"),
            };
            ctx.result(s.shares.index(p.user, &filter, share_origin(&s, &headers)))
        }
        ("POST", "/apps/files_sharing/api/v1/shares") => {
            let Some(p) = authenticate(&s, &headers, from) else {
                return ctx.result(Err(OcsError::unauthorized("Unauthorised")));
            };
            let mut pairs = body_pairs(&headers, &body);
            pairs.extend(parse_pairs(uri.query().unwrap_or("")));
            ctx.result(s.shares.create(
                p.user,
                &ShareRequest::from_form(&pairs),
                share_origin(&s, &headers),
            ))
        }
        ("GET", "/apps/files_sharing/api/v1/sharees") => {
            let Some(p) = authenticate(&s, &headers, from) else {
                return ctx.result(Err(OcsError::unauthorized("Unauthorised")));
            };
            let q = parse_pairs(uri.query().unwrap_or(""));
            ctx.result(s.sharees.search(
                p.user,
                &find(&q, "search").unwrap_or_default(),
                find(&q, "itemType").as_deref(),
                find(&q, "page")
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(1),
                find(&q, "perPage")
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(25),
                now_s(),
            ))
        }

        ("GET" | "PUT" | "DELETE", p) if p.starts_with("/apps/files_sharing/api/v1/shares/") => {
            let Some(pr) = authenticate(&s, &headers, from) else {
                return ctx.result(Err(OcsError::unauthorized("Unauthorised")));
            };
            let id_str = &p["/apps/files_sharing/api/v1/shares/".len()..];
            let Ok(id) = id_str.parse::<u64>() else {
                return ctx.result(Err(OcsError::not_found(
                    "Wrong share ID, share does not exist",
                )));
            };
            if method == axum::http::Method::DELETE {
                ctx.result(s.shares.delete(pr.user, id))
            } else if method == axum::http::Method::GET {
                ctx.result(s.shares.show(pr.user, id, share_origin(&s, &headers)))
            } else {
                let mut pairs = body_pairs(&headers, &body);
                pairs.extend(parse_pairs(uri.query().unwrap_or("")));
                ctx.result(s.shares.update(
                    pr.user,
                    id,
                    &ShareRequest::from_form(&pairs),
                    share_origin(&s, &headers),
                ))
            }
        }

        _ => ctx.result(Err(OcsError::new(998, "Invalid query, please check the syntax"))),
    }
}

// ---------------------------------------------------------------------------
// Login Flow v2
// ---------------------------------------------------------------------------

async fn h_login_init(State(s): State<NcState>, headers: HeaderMap) -> Response {
    let ua = headers
        .get(axum::http::header::USER_AGENT)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    let ip = client_ip(&headers);
    match s.login.init(ua, &ip, host_header(&headers).as_deref()) {
        Ok(r) => axum::Json(r.to_json()).into_response(),
        Err(_) => StatusCode::INTERNAL_SERVER_ERROR.into_response(),
    }
}

async fn h_login_poll(
    State(s): State<NcState>,
    uri: Uri,
    headers: HeaderMap,
    body: String,
) -> Response {
    let token = poll_token(uri.query().unwrap_or(""), &body);
    let Some(token) = token else {
        return not_found_json();
    };
    match s.login.poll(&token, host_header(&headers).as_deref()) {
        Ok(v) => axum::Json(v).into_response(),
        // Pending, expired, unknown, already-consumed **and throttled** all
        // render identically, exactly as the reference does ("not found or
        // completed"). Distinguishing them would turn the poll endpoint into an
        // oracle for live flow tokens.
        //
        // `RateLimited` used to answer `429`, and that broke enrolment on a
        // real handset. This protocol has exactly two answers a client
        // understands — `404` for "not yet" and `200` with the credentials —
        // so `429` is a status we invented for a wire that has no meaning for
        // it. The Android app polls at roughly the same 1s cadence as
        // `login_flow_poll_interval_ns`, so with clock jitter about half its
        // polls were throttled; it stopped polling on the unrecognised status
        // and never asked again. The human then completed consent, saw
        // "Access granted. You may close this window", and the app sat on its
        // spinner forever with nothing in any log to explain it — the third
        // time this flow has failed on a phone for a reason invisible from a
        // single `curl`.
        //
        // Returning `404` keeps the DoS bound this throttle exists for: the
        // rate check still short-circuits *before* the store lookup
        // (`login_flow.rs`), so an unbounded poll loop still costs no DB scan.
        // It only stops us telling the client something it cannot act on.
        //
        // The *body* of that 404 also has to be genuinely empty, not merely
        // JSON-empty — see `not_found_json`'s doc comment. This was the
        // fourth phone failure: app 34.1.0 opens the grant page and, on
        // `ProcessLifecycleOwner.ON_START` (`AuthenticatorActivity.java:591-594`),
        // fires a poll with zero initial delay
        // (`scheduleWithFixedDelay(_, 0, 30, SECONDS)` at `:409-414`) —
        // necessarily before a human can have clicked Approve. That first
        // poll always lands here.
        Err(_) => not_found_json(),
    }
}

/// Extract the Login Flow v2 poll token from wherever the client put it.
///
/// There is no single answer here, because the clients genuinely disagree:
///
/// | client  | where `token` lives                                      |
/// |---------|----------------------------------------------------------|
/// | desktop | `application/x-www-form-urlencoded` request body         |
/// | Android | request body, form-encoded — verified: `AuthenticatorActivity.performLoginFlowV2()` builds `new FormBody.Builder().add("token", ...)` (`AuthenticatorActivity.java:547-550`) |
/// | iOS     | **query string** — `the iOS SDK's +Login.swift:398` builds `endpoint + "?token=" + token` and POSTs an *empty* body |
///
/// Reading the body alone made iOS Login Flow v2 impossible to complete: the
/// poll always answered 404, so the app sat on the QR/consent screen forever
/// even after the user had approved the grant.
///
/// The query string is checked first only because iOS sends nothing else; a
/// body value still wins if both are present, since that is the form the
/// reference documents.
fn poll_token(query: &str, body: &str) -> Option<String> {
    let body_pairs = parse_pairs(body);
    find(&body_pairs, "token")
        // Some clients send JSON instead of a form body.
        .or_else(|| {
            serde_json::from_str::<serde_json::Value>(body)
                .ok()
                .and_then(|v| v.get("token")?.as_str().map(str::to_owned))
        })
        .or_else(|| find(&parse_pairs(query), "token"))
        .filter(|t| !t.is_empty())
}

/// The reference server answers a pending/expired/throttled poll with `404`
/// and a body of `[]` (`ClientFlowLoginV2Controller::poll`,
/// the reference server's `core/Controller/ClientFlowLoginV2Controller.php:84`
/// — `new JSONResponse([], Http::STATUS_NOT_FOUND)`, and an empty PHP array
/// serializes to a JSON array, not `{}`). We used to match those bytes
/// exactly. That is wrong for the Android client actually shipping today.
///
/// `AuthenticatorActivity.performLoginFlowV2()` gates on the raw string:
///
/// ```java
/// // AuthenticatorActivity.java:558-560
/// if (!response.isEmpty()) {
///     runOnUiThread(() -> completeLoginFlow(response, status));
/// }
/// ```
///
/// `"[]"` is not an empty *string*, only an empty JSON array, so every
/// pending poll used to call `completeLoginFlow`. That method has no
/// early-out for "still pending": it tries to `Gson.fromJson` the body into
/// `LoginUrlInfo` (an object), which throws on an array and lands in the
/// `catch` block — but the `catch` has no `return`, so control falls through
/// to the same unconditional tail as a real success:
///
/// ```java
/// // AuthenticatorActivity.java:586-588
/// checkOcServer();
/// loginFlowExecutorService.shutdown();
/// ProcessLifecycleOwner.get().getLifecycle().removeObserver(lifecycleEventObserver);
/// ```
///
/// One non-empty "not yet" answer permanently stops the app's own polling
/// loop and detaches the lifecycle observer that would have restarted it —
/// there is no scheduled poll left to ever see the credential once the human
/// actually approves. `checkOcServer()` then runs with `webViewUser`/
/// `webViewPassword` still null (the catch above never set them), landing in
/// `onGetServerInfoFinish`'s else branch (`:1093-1108`). Whether that branch
/// calls `anonymouslyPostLoginRequest` again — a second flow, a second
/// browser tab, the "grant again" the phone showed — turns on
/// `isRedirectedToTheDefaultBrowser` (`:1100-1103`), a plain instance field
/// with no entry in `onSaveInstanceState` (`:792-807`) at all — unlike
/// `mWaitingForOpId` (`:797`), which *is* saved there and restored in
/// `onCreate` (`:299`). If the Activity is recreated while the browser has
/// focus — routine on a phone low on memory — `isRedirectedToTheDefaultBrowser`
/// resets to `false` on the new instance, while the surviving
/// `mWaitingForOpId` lets the still-pending `GetServerInfoOperation` result
/// be redelivered to that new instance via `doOnResumeAndBound()`
/// (`:1693-1698`), driving it straight back through the reset gate and
/// re-firing `anonymouslyPostLoginRequest`, matching the
/// observed second grant page and the three `nc_login_flow` rows the DB
/// snapshot showed within one 33-second window. A genuinely empty body
/// (`response.isEmpty()` true) keeps `completeLoginFlow` from running at all
/// until the real `200` arrives, which is what this function now sends. The
/// status code is unchanged; only the two bytes of body are gone. No client
/// audited here (desktop, iOS, Android) branches on 404-body content, only
/// on status, so this loses nothing any of them read.
fn not_found_json() -> Response {
    (StatusCode::NOT_FOUND, ()).into_response()
}

async fn h_login_flow(
    State(s): State<NcState>,
    Path(token): Path<String>,
    headers: HeaderMap,
    from: ClientAddr,
) -> Response {
    // Unauthenticated: bounce through our own login, preserving returnTo, so
    // the human lands back on the consent screen.
    let Some((_p, _sess)) = authenticate_session(&s, &headers, from) else {
        let to = s.login.login_redirect(&token, host_header(&headers).as_deref());
        return Redirect::to(&to).into_response();
    };
    let (p, sess) = authenticate_session(&s, &headers, from).expect("checked above");
    match s.login.consent(&token, &p, &sess) {
        Ok(info) => Response::builder()
            .status(StatusCode::OK)
            .header(
                axum::http::header::CONTENT_TYPE,
                "text/html; charset=utf-8",
            )
            // The consent page must never be framed: clickjacking it turns
            // "Grant access" into a single stray click.
            .header("X-Frame-Options", "DENY")
            .header(
                "Content-Security-Policy",
                "default-src 'none'; form-action 'self'; frame-ancestors 'none'",
            )
            .header("Referrer-Policy", "no-referrer")
            .body(Body::from(consent_html(&info)))
            .expect("valid response"),
        Err(_) => StatusCode::FORBIDDEN.into_response(),
    }
}

async fn h_login_grant(
    State(s): State<NcState>,
    headers: HeaderMap,
    from: ClientAddr,
    body: String,
) -> Response {
    let Some((p, sess)) = authenticate_session(&s, &headers, from) else {
        return StatusCode::UNAUTHORIZED.into_response();
    };
    let pairs = parse_pairs(&body);
    let (Some(flow), Some(state_token)) = (find(&pairs, "flowToken"), find(&pairs, "stateToken"))
    else {
        return StatusCode::FORBIDDEN.into_response();
    };
    // The user-selectable scope is our extension over the reference server, which always
    // issues full access.
    let scope = match find(&pairs, "scope").as_deref() {
        Some("readonly") => Scope {
            perms: crate::ports::Perms::READ | crate::ports::Perms::DOWNLOAD,
            share: None,
        },
        _ => Scope::full(),
    };
    match s.login.grant(&flow, &state_token, &p, &sess, scope) {
        Ok(()) => (
            StatusCode::OK,
            [(axum::http::header::CONTENT_TYPE, "text/html; charset=utf-8")],
            "<!DOCTYPE html><html><body><p>Access granted. You may close this window.</p></body></html>",
        )
            .into_response(),
        Err(_) => StatusCode::FORBIDDEN.into_response(),
    }
}

// ---------------------------------------------------------------------------
// previews and redirects
// ---------------------------------------------------------------------------

async fn h_preview(
    State(s): State<NcState>,
    uri: Uri,
    headers: HeaderMap,
    from: ClientAddr,
) -> Response {
    let Some(p) = authenticate(&s, &headers, from) else {
        return StatusCode::UNAUTHORIZED.into_response();
    };
    s.preview
        .redirect(p.user, &PreviewQuery::parse(uri.query().unwrap_or("")))
}

/// `GET /index.php/apps/files/api/v1/thumbnail/{x}/{y}/{path}`.
///
/// The Android app falls back to this when it wants a thumbnail for a file it
/// knows only by remote path (`ThumbnailsCacheManager.java:1209-1211`). It is
/// the same preview pipeline with the parameters in the path instead of the
/// query, so it reuses `PreviewQuery` rather than growing a second code path.
async fn h_thumbnail_by_path(
    State(s): State<NcState>,
    Path((x, y, path)): Path<(u32, u32, String)>,
    headers: HeaderMap,
    from: ClientAddr,
) -> Response {
    let Some(p) = authenticate(&s, &headers, from) else {
        return StatusCode::UNAUTHORIZED.into_response();
    };
    let mut q = PreviewQuery::parse(&format!("x={x}&y={y}"));
    q.path = Some(path);
    s.preview.redirect(p.user, &q)
}

/// `GET /index.php/avatar/{user}/{size}`.
///
/// We have no avatar store, so this is a 404 — but a *routed* 404, which is
/// the point. Both clients tolerate a missing avatar and fall back to drawing
/// initials; what they do not tolerate well is the request landing somewhere
/// unexpected. On iOS in particular the response body is fed to an image
/// decoder and the macOS branch force-unwraps `image.cgImage!`
/// (`the iOS SDK's +API.swift:690`), so an HTML error page reaching that code is
/// worse than a clean 404.
///
/// It must also never answer 304: iOS sends `If-None-Match` here and validates
/// `200..<300`, so a Not Modified is an error to it (`:661`).
async fn h_avatar(State(s): State<NcState>, headers: HeaderMap, from: ClientAddr) -> Response {
    if authenticate(&s, &headers, from).is_none() {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    StatusCode::NOT_FOUND.into_response()
}

async fn h_files_redirect(State(s): State<NcState>, headers: HeaderMap) -> Response {
    Redirect::to(&s.cfg.url_for_host(host_header(&headers).as_deref(), "/")).into_response()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

fn now_s() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// `X-Forwarded-For` is only meaningful behind a proxy we control. It is shown
/// on the consent screen as advisory information and is never used for an
/// authorisation decision, so a spoofed value misleads a human at worst — but
/// that is exactly why it is labelled "request origin" and not "verified".
fn client_ip(headers: &HeaderMap) -> String {
    headers
        .get("x-forwarded-for")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.split(',').next())
        .map(|s| s.trim().to_string())
        .unwrap_or_else(|| "unknown".to_string())
}

/// The authority the client believes it asked for.
///
/// Unlike `client_ip` this is safe to read from a forwarded header, because it
/// is never used as a value: `NcConfig::canonical_for_host` only lets it *pick*
/// one of the origins an administrator registered, and an unrecognised
/// authority falls back to the canonical URL. `X-Forwarded-Host` comes first
/// because a reverse proxy that rewrites `Host` to its upstream would otherwise
/// hide which name the user typed.
fn host_header(headers: &HeaderMap) -> Option<String> {
    ["x-forwarded-host", "host"]
        .iter()
        .find_map(|h| headers.get(*h)?.to_str().ok())
        .and_then(|v| v.split(',').next())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

/// The origin a public link should name: the one this request arrived on, so a
/// client enrolled on an alternate origin is not handed a link on a name it may
/// have no route to.
fn share_origin<'a>(s: &'a NcState, headers: &HeaderMap) -> &'a str {
    s.cfg.canonical_for_host(host_header(headers).as_deref())
}

/// The share body, whichever of the two encodings the client chose.
///
/// The reference reads its parameters through a body parser that switches on
/// `Content-Type`, so clients pick freely. The Android app takes both sides of
/// that: `POST` is form-encoded, `PUT` is a JSON object. Read as form pairs a
/// JSON body yields none at all, and an update with no recognised parameter is
/// a 400, so every edit to a share from that client failed.
fn body_pairs(headers: &HeaderMap, body: &str) -> Vec<(String, String)> {
    let json = headers
        .get(axum::http::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|v| v.split(';').next().unwrap_or("").trim() == "application/json");
    if !json {
        return parse_pairs(body);
    }
    // Values arrive as strings from that client, but a number or a bool is
    // just as valid JSON for the same field, so flatten the scalars rather
    // than requiring one spelling.
    match serde_json::from_str::<serde_json::Value>(body) {
        Ok(serde_json::Value::Object(map)) => map
            .into_iter()
            .filter_map(|(k, v)| {
                let s = match v {
                    serde_json::Value::String(s) => s,
                    serde_json::Value::Number(n) => n.to_string(),
                    serde_json::Value::Bool(b) => b.to_string(),
                    _ => return None,
                };
                Some((k, s))
            })
            .collect(),
        _ => Vec::new(),
    }
}

fn parse_pairs(s: &str) -> Vec<(String, String)> {
    s.split('&')
        .filter_map(|p| {
            let (k, v) = p.split_once('=')?;
            let dec = |x: &str| {
                percent_encoding::percent_decode_str(&x.replace('+', " "))
                    .decode_utf8_lossy()
                    .into_owned()
            };
            Some((dec(k), dec(v)))
        })
        .collect()
}

fn find(pairs: &[(String, String)], key: &str) -> Option<String> {
    pairs
        .iter()
        .find(|(k, _)| k == key)
        .map(|(_, v)| v.clone())
}

/// Resolve the caller from `Authorization: Basic` (app password) or a session
/// cookie. Both go through `sc-auth`; compat never verifies a credential
/// itself.
fn authenticate(
    s: &NcState,
    headers: &HeaderMap,
    from: ClientAddr,
) -> Option<crate::ports::Principal> {
    authenticate_session(s, headers, from).map(|(p, _)| p)
}

fn authenticate_session(
    s: &NcState,
    headers: &HeaderMap,
    from: ClientAddr,
) -> Option<(crate::ports::Principal, String)> {
    if let Some(v) = headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
    {
        if let Some(b64) = v.strip_prefix("Basic ") {
            if let Ok(raw) = data_encoding::BASE64.decode(b64.trim().as_bytes()) {
                if let Ok(txt) = String::from_utf8(raw) {
                    if let Some((u, pw)) = txt.split_once(':') {
                        if let Ok(Some(p)) = s.deps.auth.verify_basic(u, pw, from) {
                            // A Basic-authenticated request has no browser
                            // session; bind CSRF state to the credential
                            // instead so the value is still request-specific.
                            return Some((p, format!("basic:{u}")));
                        }
                    }
                }
            }
        }
    }
    let cookie = headers
        .get(axum::http::header::COOKIE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    for part in cookie.split(';') {
        if let Some((k, v)) = part.trim().split_once('=') {
            // The name the server actually sets. This read `sc_session`, which
            // nothing ever issues, so the cookie was never found and every
            // visit to the login-flow consent screen was treated as
            // unauthenticated — bouncing back to `/login`, which bounces back
            // to the flow, forever. From the phone that looks like the login
            // window silently resetting after correct credentials.
            //
            // `SESSION_COOKIE` is imported from the crate that sets it rather
            // than spelled again here: two string literals for one cookie is
            // exactly how the names drifted apart in the first place.
            if k == s.cfg.session_cookie {
                if let Ok(Some(p)) = s.deps.auth.validate_session(v) {
                    return Some((p, v.to_string()));
                }
            }
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ocs_version_is_taken_from_the_entry_point() {
        assert_eq!(ocs_version_of("/ocs/v1.php/cloud/user"), OcsVersion::V1);
        assert_eq!(ocs_version_of("/ocs/v2.php/cloud/user"), OcsVersion::V2);
        // Anything else defaults to v2, matching OCSMiddleware's default.
        assert_eq!(ocs_version_of("/ocs/whatever"), OcsVersion::V2);
    }

    #[test]
    fn form_parsing_decodes_plus_and_percent() {
        let p = parse_pairs("path=%2Fa+b%2Fc&shareType=3&permissions=1");
        assert_eq!(find(&p, "path").as_deref(), Some("/a b/c"));
        assert_eq!(find(&p, "shareType").as_deref(), Some("3"));
        assert_eq!(find(&p, "missing"), None);
    }
}
