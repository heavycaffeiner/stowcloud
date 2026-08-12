//! The middleware stack,, in the exact documented order.
//!
//! ```text
//! 1. RequestId       2. TrustedProxy   3. HostGuard       4. SecurityHeaders
//! 5. RateLimit       6. BodyLimit      7. Auth            8. Csrf
//! 9. AclScope        10. Handler       11. ErrorMapper    12. AuditSink
//! ```
//!
//! Axum layers added later wrap layers added earlier (last-added = outermost
//! = runs first on the way in). To get request-phase execution in the order
//! above, `build_router` (in `lib.rs`) adds layers in the *reverse* order:
//! `error_mapper`/`audit_sink` innermost (closest to the handler, so they
//! see the handler's raw result first), then `scope_gate`, `csrf`, `auth`,
//! `body_limit`(via router split, see `lib.rs`), `rate_limit`,
//! `security_headers`, `host_guard`, `trusted_proxy`, and finally
//! `request_id` outermost.
//!
//! **`scope_gate` (step 9's app-password half)**: virtual-path ACL
//! resolution (the other half of step 9) happens inline inside
//! `sc-core`/`CoreApi`, not as a layer here (see `core_api::Resolved`'s doc
//! comment). What *is* a layer, added here, is the piece nothing downstream
//! ever checked: an app password's `sc_auth::Scope`. See `scope_gate`'s own
//! doc comment for the enforcement this closes.

use std::collections::HashMap;
use std::net::{IpAddr, SocketAddr};

use axum::extract::{ConnectInfo, Query, Request, State};
use axum::http::{HeaderMap, HeaderValue, Method, StatusCode};
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use secrecy::SecretString;
use sha2::{Digest, Sha256};

use crate::config::HttpConfig;
use crate::error::{envelope_response, AppError, ErrorCode};
use crate::state::{AppState, ClientIp, HostOrigin, RequestId};

// ---------------------------------------------------------------- 1. RequestId --

pub async fn request_id(mut req: Request, next: Next) -> Response {
    let id = uuid::Uuid::new_v4();
    req.extensions_mut().insert(RequestId(id));
    let mut resp = next.run(req).await;
    let header = HeaderValue::from_str(&id.to_string()).unwrap_or_else(|_| HeaderValue::from_static("invalid"));
    resp.headers_mut().insert("Sc-Trace", header);
    resp
}

// ---------------------------------------------------------------- 2. TrustedProxy --

/// The address attributed to a request whose source cannot be determined at
/// all — no `ConnectInfo`, which in a served binary cannot happen (see
/// `sc_server::cmd_serve`, which binds through
/// `into_make_service_with_connect_info`) and in practice only means a unit
/// test that built a bare `Request`.
///
/// `0.0.0.0` is chosen deliberately over a routable placeholder: it is
/// `Ipv4Addr::UNSPECIFIED`, so it can never collide with a real client's
/// source address — a packet from `0.0.0.0` has nowhere to be answered. It is
/// its own rate-limit bucket, shared with nothing.
///
/// A request with no determinable source is also **never** treated as
/// arriving from a trusted proxy, regardless of what `trusted_proxy_cidrs`
/// says — see [`resolve_client_ip`]. Without that rule an operator who
/// configured `0.0.0.0/0` (already a mistake) would additionally let an
/// unattributable request pick its own address out of a header.
pub const UNKNOWN_CLIENT: IpAddr = IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED);

fn is_trusted(cfg: &HttpConfig, ip: IpAddr) -> bool {
    cfg.trusted_proxy_cidrs.iter().any(|c| c.contains(ip))
}

/// One `X-Forwarded-For` hop. Proxies are inconsistent about ports here
/// (`1.2.3.4`, `1.2.3.4:51234`, `[2001:db8::1]:443`, bare `[2001:db8::1]`),
/// and a hop we cannot parse must not be silently skipped — see
/// [`forwarded_for`].
fn parse_hop(s: &str) -> Option<IpAddr> {
    if let Ok(ip) = s.parse::<IpAddr>() {
        return Some(ip);
    }
    if let Ok(sa) = s.parse::<SocketAddr>() {
        return Some(sa.ip());
    }
    s.strip_prefix('[')?.strip_suffix(']')?.parse::<IpAddr>().ok()
}

/// `X-Forwarded-For`, walked **right to left**.
///
/// The list is append-only and the *leftmost* entry is whatever the original
/// client sent — attacker-controlled, always. Each proxy in the chain appends
/// the address it actually saw, so the rightmost entry is the only one written
/// by a machine we trust. Walking right and stopping at the first hop that is
/// not itself a trusted proxy yields the closest address a trusted party
/// vouched for; everything to its left is that party's hearsay.
///
/// Two fail-closed rules:
/// * an unparseable hop aborts the walk (`None` → caller uses the peer). We
///   cannot tell where the trusted chain ends if we cannot read it, and
///   skipping the garbage would hand the choice to whoever inserted it.
/// * a list consisting entirely of trusted proxies also yields `None`: there
///   is no client address in it, only infrastructure.
fn forwarded_for(cfg: &HttpConfig, headers: &HeaderMap) -> Option<IpAddr> {
    let raw = headers.get("X-Forwarded-For")?.to_str().ok()?;
    for hop in raw.rsplit(',') {
        let ip = parse_hop(hop.trim())?;
        if !is_trusted(cfg, ip) {
            return Some(ip);
        }
    }
    None
}

/// The single implementation of "who is this request from?".
///
/// Every mount in the server resolves the client address through this
/// function and nothing else — `sc-server` applies [`trusted_proxy`] once, in
/// front of the native API, the WebDAV tree and every compatibility mount, so
/// there is exactly one copy of this rule to audit.
///
/// * No peer at all → [`UNKNOWN_CLIENT`], and untrusted no matter what.
/// * Peer outside every `trusted_proxy_cidrs` entry → the peer. Forwarding
///   headers from an untrusted source are attacker-supplied strings and are
///   discarded without being parsed.
/// * Peer inside a trusted CIDR → `CF-Connecting-IP` if it parses (the edge
///   *sets* that header, never appends, so there is no list to disambiguate),
///   else `X-Forwarded-For` per [`forwarded_for`], else the peer.
pub fn resolve_client_ip(cfg: &HttpConfig, peer: Option<IpAddr>, headers: &HeaderMap) -> IpAddr {
    let Some(peer) = peer else {
        return UNKNOWN_CLIENT;
    };
    if !is_trusted(cfg, peer) {
        return peer;
    }
    if let Some(ip) = headers
        .get("CF-Connecting-IP")
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.trim().parse::<IpAddr>().ok())
    {
        return ip;
    }
    forwarded_for(cfg, headers).unwrap_or(peer)
}

/// Resolves the client address and publishes it as the [`ClientIp`] request
/// extension, which is the *only* thing downstream rate limits, the login
/// gate, and the audit log are allowed to read.
///
/// Idempotent on purpose. `sc-server` applies this layer once outside the
/// whole composed router so that the WebDAV and compatibility mounts — which
/// are not built by [`crate::build_router`] — get the same resolved value,
/// while `build_router` keeps it as documented step 2 of its own stack for
/// anyone using this crate standalone. When both apply, the inner one sees a
/// `ClientIp` already present and does nothing rather than recomputing.
pub async fn trusted_proxy(State(state): State<AppState>, mut req: Request, next: Next) -> Response {
    if req.extensions().get::<ClientIp>().is_some() {
        return next.run(req).await;
    }
    let peer = req.extensions().get::<ConnectInfo<SocketAddr>>().map(|ci| ci.0.ip());
    let resolved = resolve_client_ip(&state.cfg, peer, req.headers());
    req.extensions_mut().insert(ClientIp(resolved));
    next.run(req).await
}

// ---------------------------------------------------------------- 3. HostGuard --

pub async fn host_guard(State(state): State<AppState>, mut req: Request, next: Next) -> Response {
    // Lowercased before comparison: DNS names are case-insensitive (RFC 4343),
    // so `OFFICE-NAS` and `office-nas` are the same host and answering
    // `421` to one of them is wrong. Browsers normally send what the user
    // typed, lowercased — but "normally" is not a guarantee worth resting a
    // reachability failure on, and a `421` gives the operator no hint that
    // capitalisation was the problem.
    let host = req
        .headers()
        .get(axum::http::header::HOST)
        .and_then(|v| v.to_str().ok())
        .map(|s| crate::config::split_host_port(s).0.to_ascii_lowercase());

    let Some(host) = host else {
        return envelope_response(StatusCode::MISDIRECTED_REQUEST, ErrorCode::Internal, "missing Host header");
    };

    // Content first: with a dedicated content origin the lists are disjoint
    // so order does not matter, but an operator running the single-origin
    // fallback may list the same host in both, and the content classification
    // is the more restrictive of the two.
    // Case-insensitive on the configured side too: an operator who writes
    // `Office-NAS` in `app_hosts` has not made a mistake, and a `421` is a
    // singularly unhelpful way to tell them they had.
    // A private-LAN IP literal is accepted without being listed. `sc-server`
    // used to inject its bind address into `app_hosts` for this, but only when
    // that address was a specific one, and the reference compose mandates
    // `SC_BIND=0.0.0.0:8080`, which is unspecified, so the injection never fired
    // on the one deployment shape everybody actually runs. Reaching a NAS at the
    // address it has on the LAN is the ordinary case, not a configuration the
    // operator should have to anticipate by hand.
    //
    // Safe to allow unlisted because this guard defends against DNS rebinding,
    // which arrives as a *name* in `Host` (`is_private_host_literal`). The
    // separate question of who may *write* is still decided by the CSRF origin
    // check below, which does not take this shortcut.
    let origin = if state.cfg.content_hosts.iter().any(|h| h.eq_ignore_ascii_case(&host)) {
        HostOrigin::Content
    } else if state.cfg.app_hosts.iter().any(|h| h.eq_ignore_ascii_case(&host))
        || crate::config::is_private_host_literal(&host)
    {
        HostOrigin::App
    } else {
        // Named, because the shortcut above covers IP literals and nothing
        // else: reaching this server by a name it was not told about is the one
        // way to get here, and a Tailscale MagicDNS name does it every time.
        // Without the host in the log the operator sees only an unreachable
        // site and has no way to learn that the fix is one `app_hosts` entry.
        tracing::warn!(%host, "host guard: not in app_hosts or content_hosts, answering 421");
        return envelope_response(StatusCode::MISDIRECTED_REQUEST, ErrorCode::Internal, "unrecognized Host");
    };

    req.extensions_mut().insert(origin);
    next.run(req).await
}

// ---------------------------------------------------------------- 4. SecurityHeaders --

/// The `script-src` addition an HTML document needs beyond `'self'` — empty
/// without `embed-ui`, since nothing on the app origin serves an HTML
/// document at all without it.
///
/// SvelteKit's adapter emits an inline bootstrap `<script>` directly into
/// `index.html`; `script-src 'self'` (below) blocks inline script content
/// outright, and `'unsafe-inline'` would loosen every response this CSP
/// covers, not only this one document, so it is not how this gets fixed. See
/// `embed::inline_script_csp_sources`'s doc comment for the actual, narrower
/// fix (a CSP hash of the exact bytes that are actually embedded).
#[cfg(feature = "embed-ui")]
fn html_script_src_extra() -> &'static str {
    crate::embed::inline_script_csp_sources()
}

#[cfg(not(feature = "embed-ui"))]
fn html_script_src_extra() -> &'static str {
    ""
}

/// for the app origin; for the
/// content origin (stricter — `default-src 'none'; sandbox`).
pub async fn security_headers(req: Request, next: Next) -> Response {
    let origin = req.extensions().get::<HostOrigin>().copied();
    let mut resp = next.run(req).await;

    // Read before `headers_mut()` below takes the response mutably — an API
    // JSON error and the SPA's `index.html` are both answered by the same
    // handler (`routes::admin_catch_all`) and therefore the same middleware
    // pass, so this has to branch on what actually came back, not on the
    // request path.
    // A `304` for the document carries no body and therefore no
    // `Content-Type`, but its headers still replace the stored ones on the
    // cached response the browser renders — so sniffing alone dropped the
    // script hash on exactly the revalidation that reuses the HTML needing
    // it. The embed handler marks the document explicitly for that reason;
    // the `Content-Type` check stays as the fallback for any other producer
    // of HTML on this origin.
    #[cfg(feature = "embed-ui")]
    let marked = resp.extensions().get::<crate::embed::SpaDocument>().is_some();
    #[cfg(not(feature = "embed-ui"))]
    let marked = false;

    let is_html = marked
        || resp
            .headers()
            .get(axum::http::header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .map(|s| s.starts_with("text/html"))
            .unwrap_or(false);

    let h = resp.headers_mut();

    h.insert("X-Content-Type-Options", HeaderValue::from_static("nosniff"));
    h.insert("Referrer-Policy", HeaderValue::from_static("no-referrer"));
    h.insert("Cross-Origin-Resource-Policy", HeaderValue::from_static("same-site"));
    h.insert("X-Robots-Tag", HeaderValue::from_static("noindex, nofollow"));

    match origin {
        Some(HostOrigin::Content) => {
            h.insert("Content-Security-Policy", HeaderValue::from_static("default-src 'none'; sandbox"));
        }
        _ => {
            let csp = if is_html {
                format!(
                    "default-src 'self'; script-src 'self'{}; style-src 'self' 'unsafe-inline'; \
                     img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; \
                     base-uri 'none'; form-action 'self'; object-src 'none'",
                    html_script_src_extra(),
                )
            } else {
                "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; \
                 img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; \
                 base-uri 'none'; form-action 'self'; object-src 'none'"
                    .to_string()
            };
            h.insert(
                "Content-Security-Policy",
                HeaderValue::from_str(&csp).unwrap_or_else(|_| HeaderValue::from_static("default-src 'self'")),
            );
            h.insert("Cross-Origin-Opener-Policy", HeaderValue::from_static("same-origin"));
            h.insert(
                "Permissions-Policy",
                HeaderValue::from_static("geolocation=(), camera=(), microphone=(), interest-cohort=()"),
            );
        }
    }
    resp
}

// ---------------------------------------------------------------- 5. RateLimit --

/// Runs *before* Auth so a flood of unauthenticated requests gets `429`
/// without ever reaching Argon2/session lookups (step 5).
/// Is this a plain `GET`/`HEAD` for a static file out of the embedded SPA?
///
/// Loading the app fetches several dozen hashed modules and stylesheets in one
/// burst — that is what a code-split bundle *is* — and metering them against
/// the same per-IP budget as the API means the first page load throttles
/// itself. That is not hypothetical: the app went blank behind a proxy because
/// its own chunks came back `429`, and a failed module import renders nothing
/// at all.
///
/// These are immutable bytes with no side effects, no authentication and no
/// database access, so they are not what the limiter exists to protect. It
/// exists for the API, and the API is exactly what a reserved prefix names.
fn is_static_asset(state: &AppState, req: &Request) -> bool {
    if req.method() != Method::GET && req.method() != Method::HEAD {
        return false;
    }
    let path = req.uri().path();
    // The same reserved list the SPA fallback consults, so the two can never
    // disagree about which paths are the application's and which are ours.
    !path.starts_with("/api/")
        && !path.starts_with("/c/")
        && !path.starts_with("/s/")
        && !state.cfg.reserved_path_prefixes.iter().any(|p| path.starts_with(p.as_str()))
}

pub async fn rate_limit(State(state): State<AppState>, req: Request, next: Next) -> Response {
    if is_static_asset(&state, &req) {
        return next.run(req).await;
    }
    let ip = req.extensions().get::<ClientIp>().map(|c| c.0).unwrap_or(UNKNOWN_CLIENT);
    if let Some(retry_after) = state.rate_limiter.check(ip) {
        let mut resp = AppError::rate_limited(retry_after).into_response();
        if let Ok(v) = HeaderValue::from_str(&retry_after.to_string()) {
            resp.headers_mut().insert("Retry-After", v);
        }
        return resp;
    }
    next.run(req).await
}

// ---------------------------------------------------------------- 7. Auth --

/// One cookie out of a `Cookie:` header, by exact name.
///
/// `pub(crate)` for `routes::oidc_callback`, which reads the flow-binding
/// cookie (`crate::OIDC_FLOW_COOKIE`) in a handler rather than in a layer:
/// the binding check has to happen at a specific point in §5-1's ordering,
/// after the flow row is consumed, and a middleware cannot be inserted into
/// the middle of a handler.
pub(crate) fn cookie_value<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    let raw = headers.get(axum::http::header::COOKIE)?.to_str().ok()?;
    raw.split(';').map(|p| p.trim()).find_map(|p| {
        let (k, v) = p.split_once('=')?;
        (k == name).then_some(v)
    })
}

/// Paths that never require authentication.
///
/// `cfg`/`method` exist for exactly one extra case beyond the explicit list
/// below: a `GET`/`HEAD` on anything that is not this crate's own protected
/// `/api/**` surface, nor a prefix `sc-server` reserves for a mount beside
/// this router (`routes::is_reserved_path`). That is precisely the set of
/// paths `routes::admin_catch_all`'s `embed-ui` fallback answers — a browser
/// has no session before it has loaded anything at all, so the bundle that
/// would *show* the login/first-run screen has to be reachable
/// unauthenticated, same as the screen's own API calls already are via the
/// explicit entries below. This has to be true whether or not `embed-ui` is
/// actually compiled in: without it these paths are a plain `404`, and a
/// `404` must not itself demand a session either.
fn is_public_path(cfg: &HttpConfig, path: &str, method: &Method) -> bool {
    path == "/api/capabilities"
        || path == "/api/auth/login"
        || path == "/api/auth/login/totp"
        // OIDC login.
        // The login screen has to know whether to draw the SSO button before
        // anyone has a credential, and `/start` *is* the act of going to get
        // one. `/api/auth/oidc/callback` is deliberately absent: it needs an
        // optional session, which this predicate cannot express -- see
        // `is_optional_session_path` and `auth` below.
        || path == "/api/auth/oidc/config"
        || path == "/api/auth/oidc/start"
        // First-run bootstrap. Necessarily
        // unauthenticated — there is no account to authenticate as yet.
        // Authorization is the one-time setup token, and the route stops
        // existing the moment an account does; see `routes::setup_complete`.
        || path == "/api/setup"
        || path.starts_with("/c/")
        // Public share links. Authorization for
        // these is the token in the URL plus, when one is set, the link
        // password — never a user session.
        || path.starts_with("/s/")
        || (matches!(*method, Method::GET | Method::HEAD) && !crate::routes::is_reserved_path(cfg, path))
}

/// Paths that are reachable without a session but still want one when it is
/// there.
///
/// Exactly one path qualifies, and only because it has two callers wearing
/// the same URL (proposal §4.3.1's middleware wiring). The OIDC callback in login
/// mode arrives at a browser that has no session yet and must not be turned
/// away; in link mode it arrives at a browser that does, and §4.3.2 step 2
/// has to compare that session's account against the one the flow was started
/// for. Putting it in [`is_public_path`] would satisfy the first and make the
/// second impossible, because a public path skips session parsing entirely.
///
/// An invalid or expired cookie here is treated as no cookie at all rather
/// than as a `401`. A stale session must not stop somebody logging in through
/// their IdP, and link mode is refused anyway two steps later when the
/// `Principal` it needs turns out to be absent.
fn is_optional_session_path(path: &str) -> bool {
    path == "/api/auth/oidc/callback"
}

/// matrix: `/api/**` (incl. `/api/uploads/**`) accepts
/// cookie+CSRF or Bearer, never Basic. Content-origin requests never parse
/// cookies at all and are left to their own
/// signed-URL verification inside the handler — Auth is a no-op for them.
pub async fn auth(State(state): State<AppState>, mut req: Request, next: Next) -> Response {
    let origin = req.extensions().get::<HostOrigin>().copied().unwrap_or(HostOrigin::App);
    if origin == HostOrigin::Content {
        return next.run(req).await;
    }

    let path = req.uri().path().to_string();
    if is_public_path(&state.cfg, &path, req.method()) {
        return next.run(req).await;
    }
    // Optional session (see `is_optional_session_path`): parse a cookie if
    // one came, pass through either way. Cookie only -- an app password has no
    // business completing an interactive browser login, and Bearer never
    // arrives on a redirect from an IdP.
    if is_optional_session_path(&path) {
        if let Some(token) = cookie_value(req.headers(), crate::SESSION_COOKIE).map(|s| s.to_string()) {
            if let Ok(Some(principal)) = state.auth.validate_session(&token) {
                req.extensions_mut().insert(SessionToken(token));
                req.extensions_mut().insert(principal);
            }
        }
        return next.run(req).await;
    }
    // CORS/TUS preflight: OPTIONS carries no credentials by design (browsers
    // strip them from preflight requests), so it must never 401 — the
    // client needs the capability headers back to decide how to send the
    // *real* request. TUS in particular relies on an unauthenticated
    // `OPTIONS /api/uploads` to discover `Tus-Version`/`Tus-Max-Size`.
    if req.method() == Method::OPTIONS {
        return next.run(req).await;
    }

    let ip = req.extensions().get::<ClientIp>().map(|c| c.0).unwrap_or(UNKNOWN_CLIENT);

    // Bearer (app password) takes priority if present.
    if let Some(authz) = req.headers().get(axum::http::header::AUTHORIZATION).and_then(|v| v.to_str().ok()) {
        if let Some(token) = authz.strip_prefix("Bearer ") {
            match state.auth.verify_basic("", &SecretString::from(token.to_string()), ip).await {
                sc_auth::BasicResult::Ok(principal) => {
                    req.extensions_mut().insert(principal);
                    return next.run(req).await;
                }
                sc_auth::BasicResult::RateLimited { retry_after_s } => {
                    let mut resp = AppError::rate_limited(retry_after_s).into_response();
                    if let Ok(v) = HeaderValue::from_str(&retry_after_s.to_string()) {
                        resp.headers_mut().insert("Retry-After", v);
                    }
                    return resp;
                }
                _ => return AppError::auth_required().into_response(),
            }
        }
    }

    // Otherwise, cookie session.
    if let Some(token) = cookie_value(req.headers(), crate::SESSION_COOKIE).map(|s| s.to_string()) {
        match state.auth.validate_session(&token) {
            Ok(Some(principal)) => {
                req.extensions_mut().insert(SessionToken(token));
                req.extensions_mut().insert(principal);
                return next.run(req).await;
            }
            _ => return AppError::auth_required().into_response(),
        }
    }

    AppError::auth_required().into_response()
}

/// Wrapper so the raw session token (needed by `Csrf` to derive the expected
/// `Sc-Csrf` value) rides along in extensions without re-parsing the cookie.
#[derive(Clone, Debug)]
pub struct SessionToken(pub String);

// ---------------------------------------------------------------- 8. Csrf --

const STATE_CHANGING: [Method; 4] = [Method::POST, Method::PUT, Method::PATCH, Method::DELETE];

/// Derives the per-session CSRF token as `hex(HMAC-SHA256(csrf_key,
/// sha256(session_token)))` — stateless (no extra DB column), sourced
/// entirely from data the Auth layer already validated.
pub fn derive_csrf_token(csrf_key: &[u8; 32], session_token: &str) -> String {
    use hmac::{Hmac, Mac};
    let mut session_hash = Sha256::new();
    session_hash.update(session_token.as_bytes());
    let session_hash = session_hash.finalize();

    let mut mac = Hmac::<Sha256>::new_from_slice(csrf_key).expect("HMAC accepts any key length");
    mac.update(&session_hash);
    data_encoding::HEXLOWER.encode(&mac.finalize().into_bytes())
}

/// Only cookie-authenticated state-changing requests need CSRF — Bearer
/// requires a custom `Authorization` header a `<form>` can't forge, so it's
/// exempt.
pub async fn csrf(State(state): State<AppState>, req: Request, next: Next) -> Response {
    let is_state_changing = STATE_CHANGING.contains(req.method());
    let session_token = req.extensions().get::<SessionToken>().cloned();

    if is_state_changing {
        if let Some(SessionToken(token)) = session_token {
            let expected = derive_csrf_token(&state.csrf_key, &token);
            let given = req.headers().get("Sc-Csrf").and_then(|v| v.to_str().ok());
            let origin_ok = req
                .headers()
                .get(axum::http::header::ORIGIN)
                .and_then(|v| v.to_str().ok())
                .map(|o| {
                    state.cfg.allowed_origins.iter().any(|a| a == o)
                        // The LAN counterpart of `host_guard`'s literal rule,
                        // but port-checked: see `is_self_lan_origin`.
                        || crate::config::is_self_lan_origin(&state.cfg, o)
                })
                .unwrap_or(false);

            let header_ok = given.map(|g| {
                use subtle::ConstantTimeEq;
                g.as_bytes().ct_eq(expected.as_bytes()).unwrap_u8() == 1
            }).unwrap_or(false);

            if !header_ok || !origin_ok {
                return AppError::new(ErrorCode::AuthRequired, "CSRF check failed").with_status(StatusCode::FORBIDDEN).into_response();
            }
        }
    }
    next.run(req).await
}

// ---------------------------------------------------------------- 9. AclScope (app-password Scope) --

/// What a route requires from an app password's `sc_auth::Scope`.
///
/// `sc_auth::Scope::perms_mask` is a bitmask over `sc_acl::Perms` — the same
/// vocabulary the ACL layer already uses for "what can this account do to
/// this file" (effective perms are `ACL ∩
/// scope_perms ∩ scope_shares`). Nothing downstream of `Auth` ever read that
/// mask before this layer existed — every `sc-http` handler called
/// `state.core.<op>(principal.user, ...)`, discarding `principal.scope`
/// entirely, so a "read-only" app password had exactly the same file access
/// as an unrestricted one. This enum is the per-route half of the fix that
/// belongs in this crate: which `Perms` bit(s) a route needs, a fact about
/// the route alone (method + path), answerable from a static table. The
/// `scope_shares` half — restricting to specific shares — needs the
/// virtual-path-to-`ShareId` resolution `CoreApi::resolve_share` now exposes
/// for exactly this, and is handled separately by `share_check`/
/// `share_scope_gate` below, which `scope_gate` runs after this table.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RouteScope {
    /// Gated on `AuthVia` alone, never on `scope_perms`: self-service
    /// account/credential management (minting or listing app passwords,
    /// rotating the account password, 2FA, session management) and the
    /// entire admin surface. `Perms` is a filesystem-capability mask; no
    /// combination of its bits means "and also administer the account", so
    /// an app password — scoped or not — never reaches these. Without this,
    /// an *unrestricted* app password (`scope_perms: None`) could mint a
    /// sibling with even less restriction, or read `GET /api/auth/session`,
    /// which is the "a token that can mint an unscoped sibling has no scope
    /// at all" problem stated plainly.
    SelfServiceOrAdmin,
    /// Any authenticated caller may reach this regardless of `scope_perms` —
    /// bookkeeping (job status, the event socket, logout) that carries no
    /// filesystem capability of its own. Re-checked per-path where it
    /// matters (`ws::ReadPermCheck`) independently of
    /// this gate.
    NoPermsRequired,
    /// Must hold every one of these bits, unless the credential is
    /// unrestricted (`scope_perms: None`).
    Requires(sc_acl::Perms),
    /// Not in the table below at all. Deliberately distinct from
    /// `Requires(Perms::empty())`: an unrestricted app password still gets
    /// through (identical to today's behavior — its scope is "the whole
    /// account", so an endpoint this table hasn't been taught about yet is
    /// no different from one it has), but a *restricted* one is refused.
    /// 's "an unrecognised scope, or a route the mapping
    /// does not mention, must deny" — a route added later without updating
    /// this table fails closed for exactly the credentials this gate exists
    /// to narrow, rather than silently granting it full access.
    Unmapped,
}

/// `prefix` ending without a `/` matches itself and anything one path
/// segment or deeper below it (`"/api/admin"` matches `/api/admin/storage`
/// but not `/api/admin-panel`); this crate's routes never register a bare
/// directory-with-trailing-slash path, so unlike `routes::is_reserved_path`
/// (which also matches a lone `/dav/`) there is no second case to handle.
fn path_is_under(path: &str, prefix: &str) -> bool {
    path == prefix || path.starts_with(&format!("{prefix}/"))
}

/// The per-route table this gate enforces — see `RouteScope`'s doc comment
/// for the three-way split and why "not listed" isn't the same as
/// "no restriction". Mirrors's route list exactly; a
/// route added there needs an entry here too, or it falls to `Unmapped` and
/// a scope-restricted app password loses access to it (fails closed, not
/// open — see `RouteScope::Unmapped`).
fn route_scope(method: &Method, path: &str) -> RouteScope {
    use sc_acl::Perms;
    use RouteScope::{NoPermsRequired, Requires, SelfServiceOrAdmin, Unmapped};

    // Self-service auth/account management, and the whole admin surface.
    // `AuthVia`-gated only — see `RouteScope::SelfServiceOrAdmin`.
    const SELF_SERVICE_OR_ADMIN: &[&str] = &[
        "/api/auth/session",
        "/api/auth/app-passwords",
        "/api/auth/password",
        "/api/auth/totp",
        "/api/auth/sessions",
        "/api/auth/smb",
        // Linking and unlinking an IdP identity is credential management of
        // exactly the kind §5.2 names: linking *adds* a permanent way into
        // the account, unlinking takes one away. The prefix also covers
        // `/config`, `/start` and `/callback`, which never carry an app
        // password's `Principal` anyway (the first two are public, the third
        // parses a session cookie and nothing else) -- listing the prefix
        // rather than the two routes is what keeps a fourth route added later
        // from defaulting open.
        "/api/auth/oidc",
        "/api/admin",
    ];
    if SELF_SERVICE_OR_ADMIN.iter().any(|p| path_is_under(path, p)) {
        return SelfServiceOrAdmin;
    }

    if path == "/api/auth/logout" || path == "/api/events" {
        return NoPermsRequired;
    }
    if path_is_under(path, "/api/jobs") {
        // Status/cancel of a job the caller already started; carries no
        // filesystem capability beyond what created the job in the first
        // place (already perms-checked at creation time).
        return NoPermsRequired;
    }

    if path == "/api/fs/list" || path == "/api/fs/stat" || path == "/api/fs/read" {
        return Requires(Perms::READ);
    }
    if path == "/api/fs/mkdir" {
        return Requires(Perms::CREATE);
    }
    if path == "/api/fs/rename" {
        return Requires(Perms::RENAME);
    }
    if path == "/api/fs/move" {
        return Requires(Perms::MOVE);
    }
    if path == "/api/fs/copy" {
        // `sc-core`'s `copy_to`/`copy_entries` check READ on the source and
        // CREATE on the destination (`crates/sc-core/src/ops.rs`) — the
        // scope requirement mirrors that, not just one bit of it.
        return Requires(Perms::READ | Perms::CREATE);
    }
    if path == "/api/fs/delete" {
        return Requires(Perms::DELETE);
    }
    if path == "/api/fs/write" {
        return Requires(Perms::WRITE);
    }
    if path == "/api/fs/link" || path == "/api/fs/archive" {
        return Requires(Perms::READ);
    }
    if path == "/api/trash" {
        return Requires(Perms::READ);
    }
    if path == "/api/trash/restore" {
        return Requires(Perms::CREATE);
    }
    if path == "/api/trash/purge" {
        return Requires(Perms::DELETE);
    }
    if path_is_under(path, "/api/shares") {
        return Requires(Perms::SHARE);
    }
    if path == "/api/search" || path == "/api/search/stream" {
        return Requires(Perms::READ);
    }
    if path == "/api/uploads" {
        // TUS creation (`POST`); `OPTIONS` never reaches this gate — see
        // `scope_gate`'s doc comment.
        if *method == Method::POST {
            return Requires(Perms::CREATE);
        }
        return Unmapped;
    }
    if path_is_under(path, "/api/uploads") {
        return match method.as_str() {
            "HEAD" => Requires(Perms::READ),
            "PATCH" => Requires(Perms::WRITE),
            "DELETE" => Requires(Perms::CREATE),
            _ => Unmapped,
        };
    }

    Unmapped
}

/// The enforcement point for `sc_auth::Scope`, and the reason it exists:
/// documents "effective perms = ACL ∩ scope_perms
/// ∩ scope_shares" for app passwords, but before this layer nothing in
/// `sc-http`, `sc-dav`, or the compatibility layer ever read `scope_perms` —
/// every handler resolved a virtual path with only `principal.user`. A "read
/// only" app password was, in practice, exactly as capable as an
/// unrestricted one. This closes the `perms_mask` half for the native API
/// (`sc-server::app::dav_authenticate` closes the WebDAV/compat half — see
/// that function's doc comment) and, via `share_scope_gate` below, the
/// `scope_shares` half too: a static route table can answer "does this
/// *method* need this *bit*" on its own, but "is the path this request
/// actually names inside an allowed share" needs the request's own path/
/// body, which is why the shares half runs as a second pass over the same
/// request rather than folding into `route_scope`'s table.
///
/// Only `AuthVia::AppPassword` is ever narrowed: a cookie session or an
/// account-password Basic login is `Scope::default()` (`perms_mask: None`)
/// by construction in `sc-auth` (`session.rs`/`basic.rs`), so this is a
/// no-op for them by the time it would matter — checked explicitly anyway
/// rather than relied upon, so a future caller that *does* attach a
/// restricted `Scope` to a session gets the same enforcement for free.
///
/// Placed after `Csrf` (step 9, "AclScope"): it needs
/// `Auth`'s `Principal` extension and must run before the handler, but has
/// no opinion about CSRF.
pub async fn scope_gate(State(state): State<AppState>, req: Request, next: Next) -> Response {
    let Some(principal) = req.extensions().get::<sc_auth::Principal>().cloned() else {
        // No `Principal` at all: a public path (`is_public_path`), an
        // unauthenticated preflight `OPTIONS`, or `Auth` already answered
        // `401` and short-circuited before `next.run` ever reached here.
        return next.run(req).await;
    };
    if !matches!(principal.via, sc_auth::AuthVia::AppPassword(_)) {
        return next.run(req).await;
    }

    let path = req.uri().path().to_string();
    let method = req.method().clone();
    let scope = route_scope(&method, &path);

    let perms_deny = match scope {
        RouteScope::SelfServiceOrAdmin => true,
        RouteScope::NoPermsRequired => false,
        RouteScope::Requires(required) => match principal.scope.perms_mask {
            None => false, // unrestricted app password
            Some(mask) => {
                // `from_bits_truncate` drops any bit outside the known set
                // rather than treating it as significant — an "unrecognised"
                // bit narrows what the mask is understood to grant, never
                // widens it.
                !sc_acl::Perms::from_bits_truncate(mask).contains(required)
            }
        },
        RouteScope::Unmapped => principal.scope.perms_mask.is_some(),
    };

    if perms_deny {
        return AppError::acl_denied("scope").into_response();
    }

    let Some(allowed_shares) = principal.scope.shares.clone() else {
        // Unrestricted by share — nothing more to check.
        return next.run(req).await;
    };
    share_scope_gate(&state, principal.user, &allowed_shares, &method, &path, req, next).await
}

/// Which virtual path(s), if any, a request carries — the piece
/// `share_scope_gate` needs to check `sc_auth::Scope::shares` against, since
/// unlike `perms_mask` (a route-level fact: does this *method* need this
/// *bit*, answerable from the route table alone) a share restriction can
/// only be answered once the specific path the request names is known.
enum ShareCheck {
    /// No virtual path in this request, or one already fixed and validated
    /// earlier in the request's life (a TUS upload's destination is checked
    /// once at `POST /api/uploads`; nothing later on `/api/uploads/{id}` can
    /// widen it, since none of `HEAD`/`PATCH`/`DELETE` carry a path at all) —
    /// fine regardless of `Scope::shares`.
    Exempt,
    /// The `path` query parameter, parsed exactly the way the handler's own
    /// `Query<...>` extractor would (`GET` routes).
    QueryPath,
    /// JSON body field(s), read once the body is buffered. A field holding a
    /// JSON string contributes one path; a field holding an array
    /// contributes every string element (`paths`). Every named field that is
    /// present contributes to the check.
    BodyPaths(&'static [&'static str]),
    /// `POST /api/uploads`: the destination lives in the TUS
    /// `Upload-Metadata` header, not the body or query string.
    UploadCreateHeader,
    /// This gate has no way to verify the route yet: it addresses a file by
    /// an opaque id rather than a virtual path (trash, share links, `fs/link`
    /// by `fid`), or it would need per-result filtering from a crate this
    /// pass does not touch (`/api/search`). Denies outright whenever
    /// `Scope::shares` is restricted — the same fail-closed rule
    /// `RouteScope::Unmapped` already applies to `perms_mask`, extended to
    /// cover routes that *are* mapped for perms but not yet for shares.
    Unverifiable,
}

fn share_check(method: &Method, path: &str) -> ShareCheck {
    use ShareCheck::*;

    if path == "/api/auth/logout" || path == "/api/events" || path_is_under(path, "/api/jobs") {
        return Exempt;
    }
    if path == "/api/fs/list" || path == "/api/fs/stat" || path == "/api/fs/read" {
        return QueryPath;
    }
    if path == "/api/fs/mkdir" || path == "/api/fs/rename" || path == "/api/fs/write" {
        return BodyPaths(&["path"]);
    }
    if path == "/api/fs/move" || path == "/api/fs/copy" {
        return BodyPaths(&["paths", "dest"]);
    }
    if path == "/api/fs/delete" {
        return BodyPaths(&["paths"]);
    }
    if path == "/api/uploads" {
        return if *method == Method::POST { UploadCreateHeader } else { Unverifiable };
    }
    if path_is_under(path, "/api/uploads") {
        return Exempt;
    }
    // Everything under `SELF_SERVICE_OR_ADMIN` in `route_scope` is already
    // denied outright for any app password regardless of scope, so
    // classifying it `Unverifiable` here is redundant, not wrong. Everything
    // else that addresses a share-bearing resource by id instead of by path
    // — trash, share links, `fs/link`'s `fid` — and `/api/search` (which
    // would need per-result filtering `sc-search` does not do) fall here
    // too, and fail closed.
    Unverifiable
}

/// Extract every string a JSON body's named top-level fields carry — a bare
/// string field contributes itself, an array field contributes each string
/// element, anything else (missing, wrong type) contributes nothing. Used
/// only to *check* `Scope::shares`, never to build the actual request the
/// real `Json<T>` extractor sees afterward, so a field this misses (an
/// unexpected shape) is caught by fail-closed denial when parsing the whole
/// body fails, not by silently skipping just that field.
fn json_body_paths(bytes: &[u8], fields: &[&str]) -> Option<Vec<String>> {
    let v: serde_json::Value = serde_json::from_slice(bytes).ok()?;
    let mut out = Vec::new();
    for f in fields {
        match v.get(*f) {
            Some(serde_json::Value::String(s)) => out.push(s.clone()),
            Some(serde_json::Value::Array(items)) => {
                for item in items {
                    if let Some(s) = item.as_str() {
                        out.push(s.to_string());
                    }
                }
            }
            _ => {}
        }
    }
    Some(out)
}

/// `Ok(())` iff `vpath` resolves (`CoreApi::resolve_share`) to a share in
/// `allowed`. Both "resolves to a share outside `allowed`" and "does not
/// resolve at all" deny — a `NotFound` here must not leak past the scope
/// check and reach the handler with a different, more informative status;
/// that is not what "fails closed" means. Malformed `Scope::shares` cannot
/// reach this function at all: `sc_auth::AuthService::verify_app_password`
/// already rejects a token whose persisted `scope_shares` fails to
/// deserialize (`BasicResult::Invalid`/`dav_authenticate` falls through
/// unauthenticated), so `allowed` is always a well-formed list by the time
/// any `Principal` carries it.
fn share_in_scope(state: &AppState, user: sc_vfs::UserId, allowed: &[sc_vfs::ShareId], vpath: &str) -> bool {
    matches!(state.core.resolve_share(user, vpath), Ok(share) if allowed.contains(&share))
}

/// The shares half of `scope_gate` (see that function's doc comment for the
/// perms half it runs after). Buffers the request body only for the routes
/// that need it (`ShareCheck::BodyPaths`) and hands an identical request —
/// same headers, same bytes — to `next` either way, so nothing downstream
/// (including the handler's own `Json<T>` extractor) can tell this layer
/// ever looked.
#[allow(clippy::too_many_arguments)]
async fn share_scope_gate(
    state: &AppState,
    user: sc_vfs::UserId,
    allowed: &[sc_vfs::ShareId],
    method: &Method,
    path: &str,
    req: Request,
    next: Next,
) -> Response {
    match share_check(method, path) {
        ShareCheck::Exempt => next.run(req).await,
        ShareCheck::Unverifiable => AppError::acl_denied("scope_share").into_response(),
        ShareCheck::QueryPath => {
            let vpath: Option<String> = Query::<HashMap<String, String>>::try_from_uri(req.uri())
                .ok()
                .and_then(|Query(m)| m.get("path").cloned());
            match vpath {
                Some(p) if !share_in_scope(state, user, allowed, &p) => AppError::acl_denied("scope_share").into_response(),
                _ => next.run(req).await,
            }
        }
        ShareCheck::UploadCreateHeader => {
            let vpath = crate::routes::upload_dest_vpath(req.headers());
            match vpath {
                // Malformed `Upload-Metadata`: the real handler rejects this
                // itself (`422`, unrelated to scope) — no share to check.
                None => next.run(req).await,
                Some(p) if !share_in_scope(state, user, allowed, &p) => AppError::acl_denied("scope_share").into_response(),
                Some(_) => next.run(req).await,
            }
        }
        ShareCheck::BodyPaths(fields) => {
            let (parts, body) = req.into_parts();
            let bytes = match axum::body::to_bytes(body, usize::MAX).await {
                Ok(b) => b,
                Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
            };
            let deny = match json_body_paths(&bytes, fields) {
                Some(paths) => paths.iter().any(|p| !share_in_scope(state, user, allowed, p)),
                // Body isn't valid JSON at all — can't verify, fail closed.
                // (The real `Json<T>` extractor would reject this anyway.)
                None => true,
            };
            if deny {
                return AppError::acl_denied("scope_share").into_response();
            }
            let rebuilt = Request::from_parts(parts, axum::body::Body::from(bytes));
            next.run(rebuilt).await
        }
    }
}

// ---------------------------------------------------------------- 11. ErrorMapper --

/// Defense-in-depth: `AppError::into_response` already renders the exact
/// §1.1 envelope, so this only needs to catch responses that *didn't* come
/// from an `AppError` (e.g. tower-http's body-limit `413`, or any bare
/// non-JSON error response) and (a) wrap them in the envelope shape and (b)
/// guarantee `5xx` never carries `detail`, no matter what produced it.
pub async fn error_mapper(req: Request, next: Next) -> Response {
    let resp = next.run(req).await;
    let status = resp.status();
    if !status.is_client_error() && !status.is_server_error() {
        return resp;
    }
    let is_json = resp
        .headers()
        .get(axum::http::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .map(|s| s.starts_with("application/json"))
        .unwrap_or(false);

    if status.is_server_error() {
        // Always replace 5xx bodies — never trust an inner layer's body to
        // be detail-free.
        return envelope_response(status, ErrorCode::Internal, "internal error");
    }
    if is_json {
        return resp;
    }
    let code = match status {
        StatusCode::PAYLOAD_TOO_LARGE => "fs.body_too_large",
        StatusCode::NOT_FOUND => "fs.not_found",
        StatusCode::UNAUTHORIZED => "auth.required",
        StatusCode::FORBIDDEN => "acl.denied",
        _ => "internal",
    };
    let message = status.canonical_reason().unwrap_or("error");
    let body = serde_json::json!({ "error": { "code": code, "message": message } });
    (status, axum::Json(body)).into_response()
}

// ---------------------------------------------------------------- 12. AuditSink --

pub async fn audit_sink(req: Request, next: Next) -> Response {
    let method = req.method().clone();
    let path = req.uri().path().to_string();
    let resp = next.run(req).await;
    let status = resp.status();
    if status.is_client_error() || status.is_server_error() {
        tracing::info!(target: "sc_http::audit", %method, %path, %status, "request failed");
    } else if STATE_CHANGING.contains(&method) {
        tracing::info!(target: "sc_http::audit", %method, %path, %status, "state change");
    }
    resp
}

// ---------------------------------------------------------------- helpers for BodyLimit split, see lib.rs --

/// Used by `lib.rs` to build the `tower_http::limit::RequestBodyLimitLayer`
/// applied to everything *except* `/api/uploads/**`
/// (middleware step 6).
pub fn is_upload_path(path: &str) -> bool {
    path.starts_with("/api/uploads")
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::{Body, Bytes};
    use axum::http::Request as HttpRequest;
    use axum::response::IntoResponse;
    use axum::routing::{get, options, post, put};
    use axum::Router;
    use tower::ServiceExt;
    use tower_http::limit::RequestBodyLimitLayer;

    // ------------------------------------------------------ TrustedProxy --

    fn cfg_with(cidrs: &[&str]) -> HttpConfig {
        HttpConfig {
            trusted_proxy_cidrs: cidrs.iter().filter_map(|s| crate::config::Cidr::parse(s)).collect(),
            ..HttpConfig::default()
        }
    }

    fn headers_of(pairs: &[(&str, &str)]) -> HeaderMap {
        let mut h = HeaderMap::new();
        for (k, v) in pairs {
            h.insert(
                axum::http::HeaderName::from_bytes(k.as_bytes()).unwrap(),
                HeaderValue::from_str(v).unwrap(),
            );
        }
        h
    }

    fn ip(s: &str) -> IpAddr {
        s.parse().unwrap()
    }

    /// The whole point of the trusted-proxy gate: a header is not evidence.
    #[test]
    fn an_untrusted_peer_cannot_name_itself() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("CF-Connecting-IP", "9.9.9.9"), ("X-Forwarded-For", "8.8.8.8")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("198.51.100.7")), &h), ip("198.51.100.7"));
    }

    #[test]
    fn a_trusted_peer_supplies_the_connecting_ip() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("CF-Connecting-IP", "9.9.9.9")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("9.9.9.9"));
    }

    #[test]
    fn a_garbage_connecting_ip_from_a_trusted_peer_falls_back_to_the_peer() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("CF-Connecting-IP", "not-an-address")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("203.0.113.10"));
    }

    /// The leftmost entry is whatever the client sent; the rightmost is what
    /// the proxy actually saw.
    #[test]
    fn forwarded_for_is_read_right_to_left() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("X-Forwarded-For", "1.1.1.1, 198.51.100.7")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("198.51.100.7"));
    }

    #[test]
    fn forwarded_for_skips_hops_that_are_themselves_trusted_proxies() {
        let cfg = cfg_with(&["203.0.113.0/24", "192.0.2.0/24"]);
        let h = headers_of(&[("X-Forwarded-For", "1.1.1.1, 198.51.100.7, 192.0.2.5")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("198.51.100.7"));
    }

    #[test]
    fn an_unreadable_forwarded_for_hop_falls_back_to_the_peer() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("X-Forwarded-For", "1.1.1.1, junk")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("203.0.113.10"));
    }

    #[test]
    fn forwarded_for_hops_may_carry_ports() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("X-Forwarded-For", "198.51.100.7:51234")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("198.51.100.7"));
        let h6 = headers_of(&[("X-Forwarded-For", "[2001:db8::1]:443")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h6), ip("2001:db8::1"));
    }

    /// The edge sets `CF-Connecting-IP` itself and never appends to it, so it
    /// is the more trustworthy of the two when both are present.
    #[test]
    fn connecting_ip_outranks_forwarded_for() {
        let cfg = cfg_with(&["203.0.113.0/24"]);
        let h = headers_of(&[("CF-Connecting-IP", "9.9.9.9"), ("X-Forwarded-For", "8.8.8.8")]);
        assert_eq!(resolve_client_ip(&cfg, Some(ip("203.0.113.10")), &h), ip("9.9.9.9"));
    }

    /// An operator who writes `0.0.0.0/0` has trusted the whole internet, but
    /// they must still not have trusted a request that has no source at all.
    #[test]
    fn a_sourceless_request_is_never_trusted_even_with_a_wildcard_cidr() {
        let cfg = cfg_with(&["0.0.0.0/0"]);
        let h = headers_of(&[("CF-Connecting-IP", "9.9.9.9")]);
        assert_eq!(resolve_client_ip(&cfg, None, &h), UNKNOWN_CLIENT);
    }

    /// Two applications of the layer must not disagree, and the second must
    /// not undo the first — `sc-server` deliberately applies it outside a
    /// router that already contains it.
    #[tokio::test]
    async fn trusted_proxy_is_idempotent_and_publishes_client_ip() {
        let (mut state, _dir) = crate::testutil::test_state();
        state.cfg = std::sync::Arc::new(cfg_with(&["203.0.113.0/24"]));

        async fn handler(req: Request) -> String {
            req.extensions().get::<ClientIp>().map(|c| c.0.to_string()).unwrap_or_default()
        }
        let app = Router::new()
            .route("/x", get(handler))
            .layer(axum::middleware::from_fn_with_state(state.clone(), trusted_proxy))
            .layer(axum::middleware::from_fn_with_state(state, trusted_proxy));

        let mut req = HttpRequest::builder()
            .uri("/x")
            .header("CF-Connecting-IP", "9.9.9.9")
            .body(Body::empty())
            .unwrap();
        req.extensions_mut()
            .insert(ConnectInfo(SocketAddr::from(([203, 0, 113, 10], 1234))));
        let resp = app.oneshot(req).await.unwrap();
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        assert_eq!(String::from_utf8_lossy(&bytes), "9.9.9.9");
    }

    #[tokio::test]
    async fn security_headers_present_on_app_origin() {
        async fn handler() -> &'static str {
            "ok"
        }
        let app = Router::new().route("/x", get(handler)).layer(axum::middleware::from_fn(security_headers));
        let req = HttpRequest::builder().uri("/x").extension(HostOrigin::App).body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.headers().get("X-Content-Type-Options").unwrap(), "nosniff");
        assert_eq!(resp.headers().get("Referrer-Policy").unwrap(), "no-referrer");
        assert!(resp.headers().get("Content-Security-Policy").unwrap().to_str().unwrap().contains("frame-ancestors 'none'"));
    }

    #[tokio::test]
    async fn content_origin_gets_sandboxed_csp() {
        async fn handler() -> &'static str {
            "ok"
        }
        let app = Router::new().route("/x", get(handler)).layer(axum::middleware::from_fn(security_headers));
        let req = HttpRequest::builder().uri("/x").extension(HostOrigin::Content).body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.headers().get("Content-Security-Policy").unwrap(), "default-src 'none'; sandbox");
    }

    #[tokio::test]
    async fn json_response_on_app_origin_gets_the_plain_script_src() {
        async fn handler() -> Response {
            axum::Json(serde_json::json!({"ok": true})).into_response()
        }
        let app = Router::new().route("/x", get(handler)).layer(axum::middleware::from_fn(security_headers));
        let req = HttpRequest::builder().uri("/x").extension(HostOrigin::App).body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        let csp = resp.headers().get("Content-Security-Policy").unwrap().to_str().unwrap().to_string();
        // No inline-script hash noise on a response that was never HTML.
        assert!(csp.contains("script-src 'self';"), "{csp}");
        assert!(!csp.contains("sha256-"), "{csp}");
    }

    #[cfg(feature = "embed-ui")]
    #[tokio::test]
    async fn html_response_on_app_origin_gets_the_inline_script_hash() {
        async fn handler() -> Response {
            let mut resp = axum::response::Html("<!doctype html><script>1</script>").into_response();
            resp.headers_mut().insert(
                axum::http::header::CONTENT_TYPE,
                HeaderValue::from_static("text/html; charset=utf-8"),
            );
            resp
        }
        let app = Router::new().route("/x", get(handler)).layer(axum::middleware::from_fn(security_headers));
        let req = HttpRequest::builder().uri("/x").extension(HostOrigin::App).body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        let csp = resp.headers().get("Content-Security-Policy").unwrap().to_str().unwrap().to_string();
        // The embedded build's own inline bootstrap script hash must be
        // present, or `script-src 'self'` blocks it from ever running.
        assert!(csp.contains("script-src 'self' 'sha256-"), "{csp}");
    }

    #[tokio::test]
    async fn error_mapper_hides_detail_on_500() {
        async fn handler() -> Response {
            (StatusCode::INTERNAL_SERVER_ERROR, "leaked secret: /etc/shadow contents").into_response()
        }
        let app = Router::new().route("/x", get(handler)).layer(axum::middleware::from_fn(error_mapper));
        let resp = app.oneshot(HttpRequest::builder().uri("/x").body(Body::empty()).unwrap()).await.unwrap();
        assert_eq!(resp.status(), StatusCode::INTERNAL_SERVER_ERROR);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let text = String::from_utf8_lossy(&bytes);
        assert!(!text.contains("/etc/shadow"), "500 body must not leak internal detail: {text}");
        assert!(text.contains("\"code\":\"internal\""));
    }

    #[tokio::test]
    async fn csrf_rejects_missing_header_for_cookie_session() {
        let (state, _dir) = crate::testutil::test_state();
        async fn handler() -> &'static str {
            "ok"
        }
        let app = Router::new().route("/x", post(handler)).layer(axum::middleware::from_fn_with_state(state, csrf));
        let req = HttpRequest::builder().method("POST").uri("/x").extension(SessionToken("tok123".into())).body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// The LAN origin rule, and the reason it is port-checked rather than
    /// mirroring `host_guard`'s blanket acceptance of private literals.
    ///
    /// `SameSite=Lax` is computed from the host alone, so a neighbouring
    /// service on the same NAS (`:8096`) is same-site with us and the session
    /// cookie really is attached to its cross-origin writes. Accepting any
    /// private literal here would have handed that service the whole write
    /// surface.
    #[tokio::test]
    async fn csrf_accepts_our_own_lan_origin_but_not_a_neighbour_on_the_same_address() {
        for (origin, expected) in [
            ("https://192.168.0.50:8443", StatusCode::OK),
            // Tailscale hands out CGNAT addresses, and reaching the app over the
            // tailnet is meant to need no configuration.
            ("https://100.101.102.103:8443", StatusCode::OK),
            ("https://192.168.0.50:8096", StatusCode::FORBIDDEN),
            // No plaintext listener exists, so this origin is not us whatever
            // port it names.
            ("http://192.168.0.50:8443", StatusCode::FORBIDDEN),
        ] {
            let (mut state, _dir) = crate::testutil::test_state();
            let mut cfg = (*state.cfg).clone();
            cfg.https_port = Some(8443);
            state.cfg = std::sync::Arc::new(cfg);

            let token = derive_csrf_token(&state.csrf_key, "tok123");
            async fn handler() -> &'static str {
                "ok"
            }
            let app = Router::new().route("/x", post(handler)).layer(axum::middleware::from_fn_with_state(state, csrf));
            let req = HttpRequest::builder()
                .method("POST")
                .uri("/x")
                .extension(SessionToken("tok123".into()))
                .header("Sc-Csrf", token)
                .header("Origin", origin)
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), expected, "Origin: {origin}");
        }
    }

    #[tokio::test]
    async fn csrf_accepts_matching_header_and_origin() {
        let (state, _dir) = crate::testutil::test_state();
        let expected = derive_csrf_token(&state.csrf_key, "tok123");
        async fn handler() -> &'static str {
            "ok"
        }
        let app = Router::new().route("/x", post(handler)).layer(axum::middleware::from_fn_with_state(state, csrf));
        let req = HttpRequest::builder()
            .method("POST")
            .uri("/x")
            .extension(SessionToken("tok123".into()))
            .header("Sc-Csrf", expected)
            .header("Origin", "https://app.example.com")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn csrf_allows_bearer_authenticated_requests_without_header() {
        let (state, _dir) = crate::testutil::test_state();
        async fn handler() -> &'static str {
            "ok"
        }
        // No `SessionToken` extension: this is what a Bearer-authenticated
        // request looks like by the time it reaches `csrf` (only cookie auth
        // inserts `SessionToken`).
        let app = Router::new().route("/x", post(handler)).layer(axum::middleware::from_fn_with_state(state, csrf));
        let req = HttpRequest::builder().method("POST").uri("/x").body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn body_limit_is_not_applied_to_upload_routes() {
        async fn handler(_b: Bytes) -> StatusCode {
            StatusCode::OK
        }
        let big = vec![0u8; 20 * 1024 * 1024];
        let protected = Router::new()
            .route("/api/fs/write", put(handler))
            .layer(axum::extract::DefaultBodyLimit::disable())
            .layer(RequestBodyLimitLayer::new(16 * 1024 * 1024));
        let uploads = Router::new().route("/api/uploads", post(handler)).layer(axum::extract::DefaultBodyLimit::disable());
        let app = protected.merge(uploads);

        let resp1 = app
            .clone()
            .oneshot(HttpRequest::builder().method("PUT").uri("/api/fs/write").body(Body::from(big.clone())).unwrap())
            .await
            .unwrap();
        assert_eq!(resp1.status(), StatusCode::PAYLOAD_TOO_LARGE);

        let resp2 = app
            .oneshot(HttpRequest::builder().method("POST").uri("/api/uploads").body(Body::from(big)).unwrap())
            .await
            .unwrap();
        assert_eq!(resp2.status(), StatusCode::OK, "upload route must not be size-limited");
    }

    #[tokio::test]
    async fn unauthenticated_options_is_not_blocked_by_auth() {
        let (state, _dir) = crate::testutil::test_state();
        async fn handler() -> StatusCode {
            StatusCode::NO_CONTENT
        }
        let app = Router::new().route("/api/uploads", options(handler)).layer(axum::middleware::from_fn_with_state(state, auth));
        let req = HttpRequest::builder().method("OPTIONS").uri("/api/uploads").body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::NO_CONTENT);
    }

    #[tokio::test]
    async fn missing_auth_on_protected_path_is_401() {
        let (state, _dir) = crate::testutil::test_state();
        async fn handler() -> StatusCode {
            StatusCode::OK
        }
        let app = Router::new().route("/api/fs/list", get(handler)).layer(axum::middleware::from_fn_with_state(state, auth));
        let req = HttpRequest::builder().method("GET").uri("/api/fs/list").body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    }

    // --------------------------------------------- 7b. OIDC wiring (§4.3.1) --

    mod oidc_paths {
        use super::*;

        /// Reports whether a `Principal` reached the handler, which is the
        /// only thing these tests are actually asking about.
        async fn who(principal: Option<axum::Extension<sc_auth::Principal>>) -> String {
            match principal {
                Some(axum::Extension(p)) => format!("user {}", p.user.get()),
                None => "anonymous".to_string(),
            }
        }

        fn oidc_router(state: AppState) -> Router {
            Router::new()
                .route("/api/auth/oidc/config", get(who))
                .route("/api/auth/oidc/start", get(who))
                .route("/api/auth/oidc/callback", get(who))
                .route("/api/auth/oidc/link/start", post(who))
                .layer(axum::middleware::from_fn_with_state(state, auth))
        }

        async fn body_of(resp: axum::response::Response) -> String {
            let bytes = axum::body::to_bytes(resp.into_body(), 64 * 1024).await.unwrap();
            String::from_utf8(bytes.to_vec()).unwrap()
        }

        /// Without these two in `is_public_path`, the login screen 401s on
        /// the very call that decides whether to draw the SSO button, and the
        /// button itself leads to a 401 instead of to the IdP.
        #[tokio::test]
        async fn config_and_start_are_reachable_without_a_session() {
            let (state, _dir) = crate::testutil::test_state();
            let app = oidc_router(state);
            for path in ["/api/auth/oidc/config", "/api/auth/oidc/start"] {
                let req = HttpRequest::builder().method("GET").uri(path).body(Body::empty()).unwrap();
                let resp = app.clone().oneshot(req).await.unwrap();
                assert_eq!(resp.status(), StatusCode::OK, "{path}");
                assert_eq!(body_of(resp).await, "anonymous", "{path}");
            }
        }

        /// Login mode: the browser arriving back from the IdP has no session
        /// yet, and that is the entire point of the route.
        #[tokio::test]
        async fn the_callback_is_reachable_without_a_session() {
            let (state, _dir) = crate::testutil::test_state();
            let app = oidc_router(state);
            let req = HttpRequest::builder()
                .method("GET")
                .uri("/api/auth/oidc/callback?code=x&state=y")
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            assert_eq!(body_of(resp).await, "anonymous");
        }

        /// Link mode: the same route, with a session that §4.3.2 step 2 has
        /// to be able to compare against the flow's `link_user`. A public
        /// path would have skipped session parsing and left the handler
        /// unable to tell link mode from login mode.
        #[tokio::test]
        async fn the_callback_injects_a_principal_when_a_valid_cookie_is_present() {
            let (state, _dir) = crate::testutil::test_state();
            let uid = state
                .auth
                .create_user("linker", &secrecy::SecretString::from("correct horse battery"))
                .unwrap();
            let session = state
                .auth
                .create_session(uid, "127.0.0.1".parse().unwrap(), "browser", sc_auth::AMR_PASSWORD)
                .unwrap();

            let app = oidc_router(state);
            let req = HttpRequest::builder()
                .method("GET")
                .uri("/api/auth/oidc/callback?code=x&state=y")
                .header(axum::http::header::COOKIE, format!("{}={}", crate::SESSION_COOKIE, session.as_str()))
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            assert_eq!(body_of(resp).await, format!("user {}", uid.get()));
        }

        /// A cookie left over from a session that has since been revoked must
        /// not stop somebody logging in. §4.3.1 is explicit: treat it as
        /// absent, not as a `401`.
        #[tokio::test]
        async fn an_invalid_cookie_on_the_callback_is_absence_not_a_401() {
            let (state, _dir) = crate::testutil::test_state();
            let app = oidc_router(state);
            let req = HttpRequest::builder()
                .method("GET")
                .uri("/api/auth/oidc/callback?code=x&state=y")
                .header(axum::http::header::COOKIE, format!("{}=not-a-real-token", crate::SESSION_COOKIE))
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            assert_eq!(body_of(resp).await, "anonymous");
        }

        /// The self-service link routes are ordinary authenticated routes.
        /// Nothing about OIDC makes them public, and a prefix-shaped public
        /// rule would have made them so.
        #[tokio::test]
        async fn link_start_still_requires_a_session() {
            let (state, _dir) = crate::testutil::test_state();
            let app = oidc_router(state);
            let req = HttpRequest::builder()
                .method("POST")
                .uri("/api/auth/oidc/link/start")
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
        }

        /// `csrf` only inspects POST/PUT/PATCH/DELETE, so the GET callback
        /// passes it untouched and needs no exemption. This asserts the half
        /// that a prefix-shaped exemption would have broken: `POST
        /// /link/start` still fails CSRF without `Sc-Csrf` and `Origin`
        /// (proposal correction 3).
        #[tokio::test]
        async fn link_start_still_fails_csrf_while_the_callback_passes_it() {
            let (state, _dir) = crate::testutil::test_state();
            let uid = state
                .auth
                .create_user("csrftester", &secrecy::SecretString::from("correct horse battery"))
                .unwrap();
            let session = state
                .auth
                .create_session(uid, "127.0.0.1".parse().unwrap(), "browser", sc_auth::AMR_PASSWORD)
                .unwrap();
            let cookie = format!("{}={}", crate::SESSION_COOKIE, session.as_str());

            let app = Router::new()
                .route("/api/auth/oidc/callback", get(who))
                .route("/api/auth/oidc/link/start", post(who))
                .layer(axum::middleware::from_fn_with_state(state.clone(), csrf))
                .layer(axum::middleware::from_fn_with_state(state, auth));

            let posted = app
                .clone()
                .oneshot(
                    HttpRequest::builder()
                        .method("POST")
                        .uri("/api/auth/oidc/link/start")
                        .header(axum::http::header::COOKIE, cookie.clone())
                        .body(Body::empty())
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(posted.status(), StatusCode::FORBIDDEN, "link/start must keep its CSRF requirement");

            let got = app
                .oneshot(
                    HttpRequest::builder()
                        .method("GET")
                        .uri("/api/auth/oidc/callback?code=x&state=y")
                        .header(axum::http::header::COOKIE, cookie)
                        .body(Body::empty())
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(got.status(), StatusCode::OK, "a GET callback never reaches the CSRF check at all");
        }

        /// The "no auth-changing routes" rule of an app password's permission
        /// mask, extended to the two self-service OIDC routes: an app
        /// password may not attach a permanent new login method to the
        /// account it was minted from, nor remove one.
        #[tokio::test]
        async fn an_app_password_cannot_reach_the_self_service_link_routes() {
            let (state, _dir) = crate::testutil::test_state();
            let token = issue_scoped_app_password(&state, None);
            let app = Router::new()
                .route("/api/auth/oidc/link/start", post(who))
                .route("/api/auth/oidc/link", axum::routing::delete(who))
                .layer(axum::middleware::from_fn_with_state(state.clone(), scope_gate))
                .layer(axum::middleware::from_fn_with_state(state, auth));

            for (method, path) in [("POST", "/api/auth/oidc/link/start"), ("DELETE", "/api/auth/oidc/link")] {
                let resp = app.clone().oneshot(bearer_req(method, path, &token)).await.unwrap();
                assert_eq!(resp.status(), StatusCode::FORBIDDEN, "{method} {path}");
            }
        }
    }

    // ------------------------------------------------------ 9. AclScope (app-password Scope) --

    /// A fresh user plus an app password scoped to exactly `perms_mask`
    /// (`None` = unrestricted, mirroring `sc_auth::Scope::default()`).
    fn issue_scoped_app_password(state: &AppState, perms_mask: Option<u16>) -> String {
        let uid = state.auth.create_user("scopetester", &secrecy::SecretString::from("hunter22222")).unwrap();
        let scope = sc_auth::Scope { perms_mask, shares: None };
        let (_id, token) = state.auth.issue_app_password(uid, "test device", scope).unwrap();
        token
    }

    fn bearer_req(method: &str, uri: &str, token: &str) -> HttpRequest<Body> {
        HttpRequest::builder()
            .method(method)
            .uri(uri)
            .header("Host", "localhost")
            .header(axum::http::header::AUTHORIZATION, format!("Bearer {token}"))
            .body(Body::empty())
            .unwrap()
    }

    /// The whole point: before `scope_gate` existed, a "read-only" app
    /// password could still `PUT /api/fs/write` — nothing downstream of
    /// `Auth` ever consulted `Scope::perms_mask`. This is that gap, closed.
    #[tokio::test]
    async fn read_only_app_password_is_refused_on_fs_write() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, Some(sc_acl::Perms::READ.bits()));
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("PUT", "/api/fs/write", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn read_only_app_password_is_refused_on_fs_delete() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, Some(sc_acl::Perms::READ.bits()));
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("POST", "/api/fs/delete", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// Every TUS method on `/api/uploads[/:id]` — creation, chunk append, and
    /// abort — is a write in disguise and must be refused the same way.
    #[tokio::test]
    async fn read_only_app_password_is_refused_on_every_tus_method() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, Some(sc_acl::Perms::READ.bits()));
        let app = crate::build_router(state);

        let resp = app.clone().oneshot(bearer_req("POST", "/api/uploads", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN, "POST /api/uploads (create)");

        let resp = app.clone().oneshot(bearer_req("PATCH", "/api/uploads/upl-1", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN, "PATCH /api/uploads/{{id}} (append chunk)");

        let resp = app.oneshot(bearer_req("DELETE", "/api/uploads/upl-1", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN, "DELETE /api/uploads/{{id}} (abort)");
    }

    /// The same read-only credential must still succeed on the read its scope
    /// actually grants — a gate that is merely broad rather than correct would
    /// pass the tests above by blocking everything.
    #[tokio::test]
    async fn read_only_app_password_still_succeeds_on_the_matching_read() {
        let core = std::sync::Arc::new(crate::testutil::MockCore::new(Vec::new()));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        let token = issue_scoped_app_password(&state, Some(sc_acl::Perms::READ.bits()));
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("GET", "/api/fs/list", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    /// An app password issued with no scope restriction (`scope_perms: NULL`
    /// — `sc_auth::Scope::default()`) must keep working exactly as before:
    /// this gate only ever narrows, never adds a restriction nothing asked
    /// for. `UnimplementedCore` answers `fs/write` with `500`, never `403` —
    /// asserting `!= FORBIDDEN` distinguishes "the gate let it through" from
    /// "the gate happened not to fire for an unrelated reason".
    #[tokio::test]
    async fn unrestricted_app_password_is_not_narrowed() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, None);
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("PUT", "/api/fs/write", &token)).await.unwrap();
        assert_ne!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// 's design question, answered: a credential that
    /// can mint an unscoped sibling has no scope at all, so *no* app
    /// password — restricted or not — may create another one.
    #[tokio::test]
    async fn app_password_cannot_mint_a_sibling_app_password() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, None);
        let app = crate::build_router(state);
        let req = HttpRequest::builder()
            .method("POST")
            .uri("/api/auth/app-passwords")
            .header("Host", "localhost")
            .header(axum::http::header::AUTHORIZATION, format!("Bearer {token}"))
            .header(axum::http::header::CONTENT_TYPE, "application/json")
            .body(Body::from(serde_json::json!({ "name": "sibling" }).to_string()))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// Same question's other half: reading `GET /api/auth/session` is also
    /// self-service account introspection, denied to any app password.
    #[tokio::test]
    async fn app_password_cannot_read_auth_session() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, None);
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("GET", "/api/auth/session", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }
    /// ...and rotating the account password is denied the same way.
    #[tokio::test]
    async fn app_password_cannot_change_the_account_password() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, None);
        let app = crate::build_router(state);
        let req = HttpRequest::builder()
            .method("POST")
            .uri("/api/auth/password")
            .header("Host", "localhost")
            .header(axum::http::header::AUTHORIZATION, format!("Bearer {token}"))
            .header(axum::http::header::CONTENT_TYPE, "application/json")
            .body(Body::from(
                serde_json::json!({ "current_password": "hunter22222", "new_password": "hunter333333" }).to_string(),
            ))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// A route not in `route_scope`'s table must fail closed for a
    /// *restricted* app password — never silently treated as unrestricted.
    #[tokio::test]
    async fn a_restricted_app_password_is_refused_on_an_unmapped_route() {
        let (state, _dir) = crate::testutil::test_state();
        let token = issue_scoped_app_password(&state, Some(sc_acl::Perms::READ.bits()));
        let app = crate::build_router(state);
        let resp = app
            .oneshot(bearer_req("GET", "/api/this-route-does-not-exist", &token))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    // ------------------------------------------------ 9. AclScope (Scope::shares) --

    /// A fresh user plus an app password scoped to exactly `shares`
    /// (`None` = unrestricted, mirroring `sc_auth::Scope::default()`).
    /// Never restricted by `perms_mask` — these tests exist to isolate the
    /// shares half from the perms half already covered above.
    fn issue_share_scoped_app_password(state: &AppState, shares: Option<Vec<sc_vfs::ShareId>>) -> String {
        let uid = state.auth.create_user("sharetester", &secrecy::SecretString::from("hunter22222")).unwrap();
        let scope = sc_auth::Scope { perms_mask: None, shares };
        let (_id, token) = state.auth.issue_app_password(uid, "test device", scope).unwrap();
        token
    }

    /// The whole point: a token scoped to one share must be refused on a
    /// path under a different share, over the native API, and must keep
    /// working on its own.
    #[tokio::test]
    async fn a_share_scoped_app_password_is_refused_on_another_shares_path() {
        let core = std::sync::Arc::new(crate::testutil::ShareMockCore::new(&[("mine", 1), ("other", 2)]));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        let token = issue_share_scoped_app_password(&state, Some(vec![sc_vfs::ShareId::new(1)]));
        let app = crate::build_router(state);

        let denied = app
            .clone()
            .oneshot(bearer_req("GET", "/api/fs/stat?path=/other/x.txt", &token))
            .await
            .unwrap();
        assert_eq!(denied.status(), StatusCode::FORBIDDEN, "a different share's path must be refused");

        let allowed = app.oneshot(bearer_req("GET", "/api/fs/stat?path=/mine/x.txt", &token)).await.unwrap();
        assert_eq!(allowed.status(), StatusCode::OK, "the token's own share must still work");
    }

    /// Same enforcement, JSON-bodied route: `mkdir`'s `path` field is read
    /// from the buffered body, and the body is still intact for the real
    /// handler afterward.
    #[tokio::test]
    async fn a_share_scoped_app_password_is_refused_on_another_shares_path_in_a_json_body() {
        let core = std::sync::Arc::new(crate::testutil::ShareMockCore::new(&[("mine", 1), ("other", 2)]));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        let token = issue_share_scoped_app_password(&state, Some(vec![sc_vfs::ShareId::new(1)]));
        let app = crate::build_router(state);

        let req = |vpath: &str| {
            HttpRequest::builder()
                .method("POST")
                .uri("/api/fs/mkdir")
                .header("Host", "localhost")
                .header(axum::http::header::AUTHORIZATION, format!("Bearer {token}"))
                .header(axum::http::header::CONTENT_TYPE, "application/json")
                .body(Body::from(serde_json::json!({ "path": vpath }).to_string()))
                .unwrap()
        };

        let denied = app.clone().oneshot(req("/other/newdir")).await.unwrap();
        assert_eq!(denied.status(), StatusCode::FORBIDDEN);

        let allowed = app.oneshot(req("/mine/newdir")).await.unwrap();
        assert_eq!(allowed.status(), StatusCode::OK, "body must reach the real handler unchanged: {allowed:?}");
    }

    /// A `move`/`copy` request names two paths — the sources *and* the
    /// destination — and both must be checked: a token scoped to `mine`
    /// must not be able to use `dest` to smuggle a file into `other`, even
    /// though every `paths` entry is its own share.
    #[tokio::test]
    async fn a_share_scoped_app_password_cannot_move_into_another_shares_destination() {
        let core = std::sync::Arc::new(crate::testutil::ShareMockCore::new(&[("mine", 1), ("other", 2)]));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        let token = issue_share_scoped_app_password(&state, Some(vec![sc_vfs::ShareId::new(1)]));
        let app = crate::build_router(state);

        let req = HttpRequest::builder()
            .method("POST")
            .uri("/api/fs/move")
            .header("Host", "localhost")
            .header(axum::http::header::AUTHORIZATION, format!("Bearer {token}"))
            .header(axum::http::header::CONTENT_TYPE, "application/json")
            .body(Body::from(serde_json::json!({ "paths": ["/mine/a.txt"], "dest": "/other" }).to_string()))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// `Scope::shares` naming a share id nothing resolves to must deny every
    /// real path, never fall back to treating the token as unrestricted.
    #[tokio::test]
    async fn a_nonexistent_share_id_in_scope_denies() {
        let core = std::sync::Arc::new(crate::testutil::ShareMockCore::new(&[("mine", 1)]));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        // Scope references share 999, which nothing in `ShareMockCore` maps to.
        let token = issue_share_scoped_app_password(&state, Some(vec![sc_vfs::ShareId::new(999)]));
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("GET", "/api/fs/stat?path=/mine/x.txt", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// A route this gate cannot yet verify a path for (search has no
    /// per-result share filtering available to it) must fail closed for a
    /// share-restricted token rather than let it through unchecked.
    #[tokio::test]
    async fn a_share_scoped_app_password_is_refused_on_an_unverifiable_route() {
        let core = std::sync::Arc::new(crate::testutil::ShareMockCore::new(&[("mine", 1)]));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        let token = issue_share_scoped_app_password(&state, Some(vec![sc_vfs::ShareId::new(1)]));
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("GET", "/api/search?q=x", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// Unchanged behavior for the common case: `Scope::shares: None` must
    /// keep reaching every share exactly as before this gate existed.
    #[tokio::test]
    async fn an_unrestricted_by_share_app_password_is_not_narrowed() {
        let core = std::sync::Arc::new(crate::testutil::ShareMockCore::new(&[("mine", 1), ("other", 2)]));
        let (state, _dir) = crate::testutil::test_state_with_core(core);
        let token = issue_share_scoped_app_password(&state, None);
        let app = crate::build_router(state);
        let resp = app.oneshot(bearer_req("GET", "/api/fs/stat?path=/other/x.txt", &token)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn capabilities_is_public() {
        let (state, _dir) = crate::testutil::test_state();
        async fn handler() -> StatusCode {
            StatusCode::OK
        }
        let app = Router::new().route("/api/capabilities", get(handler)).layer(axum::middleware::from_fn_with_state(state, auth));
        let req = HttpRequest::builder().method("GET").uri("/api/capabilities").body(Body::empty()).unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }
}
