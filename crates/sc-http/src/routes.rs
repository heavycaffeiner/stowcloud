//! Route handlers —
//!
//! Handlers for `/api/fs/**`, `/api/trash*`, `/api/shares*` go through
//! [`crate::core_api::CoreApi`] (currently [`crate::core_api::UnimplementedCore`]
//! by default — see that module's doc comment for why). They return
//! `AppError::not_implemented()` until a real `CoreApi` is wired in, but the
//! route shapes, request/response DTOs, and middleware pipeline around them
//! are real and tested.

use std::collections::HashMap;

use axum::extract::ws::{Message, WebSocket, WebSocketUpgrade};
use axum::extract::{Path, Query, State};
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, options, patch, post, put};
use axum::{Extension, Json, Router};
use sc_auth::Principal;
use sc_vfs::ids::UserId;
use secrecy::{ExposeSecret, SecretString};
use serde::{Deserialize, Serialize};

use crate::content::{self, Disposition};
use crate::core_api::{CoreError, OnConflict, OpResult, Order, SortKey};
use crate::error::{AppError, ErrorCode};
use crate::middleware::SessionToken;
use crate::state::{AppState, ClientIp, HostOrigin, JobKind, JobState, JobStatus};

/// Every route except `/api/uploads/**` — this is the sub-router the
/// `RequestBodyLimitLayer` gets applied to (step 6).
pub fn protected_routes(state: AppState) -> Router {
    Router::new()
        .route("/api/capabilities", get(capabilities))
        .route("/api/setup", get(setup_status).post(setup_complete))
        .route("/api/auth/login", post(auth_login))
        .route("/api/auth/login/totp", post(auth_login_totp))
        .route("/api/auth/logout", post(auth_logout))
        .route("/api/auth/session", get(auth_session))
        .route("/api/auth/app-passwords", get(list_app_passwords).post(create_app_password))
        .route("/api/auth/app-passwords/{id}", delete(revoke_app_password))
        .route("/api/auth/password", post(auth_change_password))
        .route("/api/auth/totp/setup", post(auth_totp_setup))
        .route("/api/auth/totp/enroll", post(auth_totp_enroll))
        .route("/api/auth/totp/disable", post(auth_totp_disable))
        .route("/api/auth/sessions", get(auth_list_sessions))
        .route("/api/auth/sessions/{id_hash}", delete(auth_revoke_session))
        .route("/api/auth/smb", post(auth_smb_settings))
        // OIDC login (`docs/proposals/stowcloud-0-oidc-login.md` §5-1). The
        // first three are reached without a session -- `middleware::
        // is_public_path` for the first two, `is_optional_session_path` for
        // the callback, which needs one only in link mode.
        .route("/api/auth/oidc/config", get(oidc_config))
        .route("/api/auth/oidc/start", get(oidc_start))
        .route("/api/auth/oidc/callback", get(oidc_callback))
        .route("/api/auth/oidc/link/start", post(oidc_link_start))
        .route("/api/auth/oidc/link", delete(oidc_unlink))
        .route("/api/fs/list", get(fs_list))
        .route("/api/fs/stat", get(fs_stat))
        .route("/api/fs/mkdir", post(fs_mkdir))
        .route("/api/fs/rename", post(fs_rename))
        .route("/api/fs/move", post(fs_move))
        .route("/api/fs/copy", post(fs_copy))
        .route("/api/fs/delete", post(fs_delete))
        .route("/api/fs/read", get(fs_read))
        .route("/api/fs/write", put(fs_write))
        .route("/api/fs/link", post(fs_link))
        .route("/api/fs/archive", post(fs_archive))
        .route("/api/trash", get(trash_list))
        .route("/api/trash/restore", post(trash_restore))
        .route("/api/trash/purge", post(trash_purge))
        .route("/api/shares", get(shares_list).post(shares_create))
        .route("/api/shares/{id}", get(shares_get).patch(shares_patch).delete(shares_delete))
        .route("/api/search", get(search))
        .route("/api/search/stream", get(search_stream))
        .route("/api/jobs", get(job_list))
        .route("/api/jobs/{id}", get(job_status).delete(job_cancel))
        .route("/api/jobs/{id}/download", get(job_download))
        .route("/api/events", get(events_ws))
        .route("/api/admin/storage", get(admin_storage))
        .route("/api/admin/index/estimate", get(admin_index_estimate).post(admin_index_estimate))
        .route("/api/admin/index/settings", get(admin_index_settings).patch(admin_set_index_settings))
        .route("/api/admin/index/build", post(admin_build_index))
        .route("/api/admin/upload-settings", patch(admin_set_upload_settings))
        .route("/api/admin/server-settings", get(admin_get_server_settings))
        .route("/api/admin/server-settings/smb", patch(admin_set_smb_settings))
        .route("/api/admin/server-settings/search", patch(admin_set_search_settings))
        .route("/api/admin/server-settings/archive", patch(admin_set_archive_settings))
        .route("/api/admin/server-settings/network", patch(admin_set_network_settings))
        .route("/api/admin/server-settings/db", patch(admin_set_db_settings))
        .route("/api/admin/server-settings/symlink-policy", patch(admin_set_symlink_policy_settings))
        .route("/api/admin/server-settings/homes", patch(admin_set_homes_settings))
        .route("/api/admin/server-settings/watch", patch(admin_set_watch_settings))
        .route("/api/admin/server-settings/paths", patch(admin_set_paths_settings))
        .route("/api/admin/server-settings/oidc", patch(admin_set_oidc_settings))
        .route("/api/admin/server-settings/restart", post(admin_restart_server))
        .route("/api/admin/users", get(admin_list_users).post(admin_create_user))
        .route("/api/admin/users/{id}", patch(admin_patch_user).delete(admin_delete_user))
        .route(
            "/api/admin/users/{id}/oidc",
            get(admin_get_user_oidc).put(admin_put_user_oidc).delete(admin_delete_user_oidc),
        )
        .route("/api/admin/shares", get(admin_list_shares).post(admin_create_share))
        .route("/api/admin/shares/{id}", patch(admin_update_share).delete(admin_delete_share))
        .route("/api/admin/grants", get(admin_list_grants).post(admin_create_grant))
        .route("/api/admin/grants/{id}", patch(admin_update_grant).delete(admin_delete_grant))
        .route("/api/admin/groups", get(admin_list_groups).post(admin_create_group))
        .route("/api/admin/groups/{id}", patch(admin_rename_group).delete(admin_delete_group))
        .route("/api/admin/groups/{id}/members", post(admin_add_group_member))
        .route("/api/admin/groups/{id}/members/{user}", delete(admin_remove_group_member))
        .route("/api/admin/audit", get(admin_list_audit))
        .fallback(admin_catch_all)
        .route("/c/{token}", get(content_get))
        // Public share links. Unauthenticated by
        // design — see `middleware::is_public_path`.
        .route("/s/{token}", get(public_link_get))
        .route("/s/{token}/auth", post(public_link_auth))
        .route("/s/{token}/download", post(public_link_download))
        .route("/s/{token}/drop", post(public_link_drop))
        .with_state(state)
        // axum bakes a 2MB cap into `Bytes`/`Json`/`String` extractors
        // independent of any `tower_http` layer (see
        // `axum::extract::DefaultBodyLimit` docs) — disable it here so the
        // *only* enforcement point for non-upload routes is the explicit
        // `RequestBodyLimitLayer` applied in `lib.rs::build_router` (which
        // is configurable via `HttpConfig::body_limit_bytes`, unlike axum's
        // hardcoded default).
        .layer(axum::extract::DefaultBodyLimit::disable())
}

/// `/api/uploads[/:id]` (TUS) — deliberately kept out of `protected_routes`
/// so the body-size limit layer never wraps it (no chunk-size cap, streamed). Also disables axum's own built-in 2MB
/// `Bytes`/`Json` extractor default for the same reason.
pub fn upload_routes(state: AppState) -> Router {
    Router::new()
        .route("/api/uploads", options(uploads_options).post(uploads_create))
        .route(
            "/api/uploads/{id}",
            options(uploads_options).head(uploads_head).patch(uploads_patch).delete(uploads_delete),
        )
        .with_state(state)
        .layer(axum::extract::DefaultBodyLimit::disable())
}

fn principal_or_401(p: Option<Extension<Principal>>) -> Result<Principal, AppError> {
    p.map(|Extension(p)| p).ok_or_else(AppError::auth_required)
}

// ------------------------------------------------------------- capabilities --

#[derive(Serialize)]
struct UploadCaps {
    chunk_size_min: u64,
    chunk_size_default: u64,
    chunk_size_advisory: u64,
    chunk_size_max: Option<u64>,
    parallel: u32,
    max_file_size: Option<u64>,
}

#[derive(Serialize)]
struct FeatureCaps {
    webdav: bool,
    smb: bool,
    preview: bool,
    trash: bool,
    shares: bool,
    search: &'static str,
    /// the names of the compatibility layers that are
    /// actually mounted. See `HttpConfig::extensions` for why this is a list
    /// of neutral strings and not one boolean per vendor.
    extensions: Vec<String>,
}

fn feature_caps(state: &AppState) -> FeatureCaps {
    FeatureCaps {
        webdav: true,
        // Deployment-wide `[smb] enabled`, not the per-account toggle
        // — this was hardcoded `false`
        // unconditionally, which meant the SMB settings section (fully
        // implemented front to back) could never render no matter what the
        // server was configured with.
        smb: state.core.smb_enabled(),
        preview: true,
        trash: true,
        // Advertised from the backend, not hardcoded: a deployment with no
        // share-link store must not tell the UI to draw a button that every
        // click will `501` on ("only features actually enabled on the server").
        shares: state.core.shares_enabled(),
        search: "walk",
        extensions: state.cfg.extensions.clone(),
    }
}

#[derive(Serialize)]
struct AuthCaps {
    totp: bool,
    app_passwords: bool,
}

#[derive(Serialize)]
struct Capabilities {
    product: &'static str,
    api: u32,
    upload: UploadCaps,
    features: FeatureCaps,
    auth: AuthCaps,
    content_origin: String,
}

async fn capabilities(State(state): State<AppState>) -> Json<Capabilities> {
    let content_origin = state.cfg.content_hosts.first().map(|h| format!("https://{h}")).unwrap_or_default();
    let (chunk_min, chunk_default) = state.uploads.chunk_limits();
    Json(Capabilities {
        product: "sc",
        api: 1,
        upload: UploadCaps {
            chunk_size_min: chunk_min,
            chunk_size_default: chunk_default,
            chunk_size_advisory: chunk_default,
            chunk_size_max: None,
            parallel: 4,
            max_file_size: None,
        },
        features: feature_caps(&state),
        auth: AuthCaps { totp: true, app_passwords: true },
        content_origin,
    })
}

// ------------------------------------------------------------------ setup --

/// `GET /api/setup` — the whole first-run signal, and nothing else.
///
/// It is a bare boolean on purpose. The SPA has to decide between a login
/// screen and a create-administrator screen before it holds any credential,
/// so *something* here must be readable by an anonymous caller; the design
/// question is only how much. `{"required": true}` tells an attacker that
/// this server has no accounts yet — which they can already determine by
/// POSTing a junk token and reading `410` versus `403`, and which stops being
/// true forever the moment the first account exists. Everything that would
/// actually help them is withheld: no token, no token prefix or length, no
/// expiry timestamp (which would leak when the process last restarted), no
/// account names, no hint whether a token is currently live. There is no
/// value here that is worth more to an attacker than the correct first-run
/// screen is worth to the operator.
async fn setup_status(State(state): State<AppState>) -> Response {
    Json(serde_json::json!({ "required": state.setup.is_required() })).into_response()
}

#[derive(Deserialize)]
struct SetupReq {
    token: String,
    username: String,
    password: String,
}

/// `POST /api/setup` — spend the one-time token and create the administrator.
///
/// **The token is read from the JSON body only, never from a header.** A
/// header form (`Sc-Setup-Token:`) would be marginally nicer to `curl`, but
/// request headers are routinely written to reverse-proxy, CDN and ingress
/// access logs while request bodies essentially never are — and this token is
/// a bearer credential for creating an administrator. One accepted transport
/// is also one code path through the timing-safe comparison instead of two.
///
/// No CSRF token is required and none is meaningful: the request carries no
/// session, so `middleware::csrf` correctly stands aside, and a cross-site
/// `<form>` cannot forge this request in any case because it does not know
/// the setup token.
async fn setup_complete(State(state): State<AppState>, req: axum::extract::Request) -> Response {
    let ip = client_ip_of(req.extensions().get::<ClientIp>());

    // Before the body is even read: this endpoint creates an administrator
    // for whoever presents a secret, so the attempt budget is spent on the
    // attempt, not on a well-formed attempt.
    if let Some(retry_after) = state.setup_rate.check(ip) {
        let mut resp = AppError::rate_limited(retry_after).into_response();
        if let Ok(v) = axum::http::HeaderValue::from_str(&retry_after.to_string()) {
            resp.headers_mut().insert("Retry-After", v);
        }
        return resp;
    }

    let (_, body) = req.into_parts();
    let bytes = match axum::body::to_bytes(body, 64 * 1024).await {
        Ok(b) => b,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    let req: SetupReq = match serde_json::from_slice(&bytes) {
        Ok(r) => r,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };

    // `complete` runs Argon2 (~80 ms). Off the executor it goes.
    let setup = state.setup.clone();
    let password = SecretString::from(req.password);
    let outcome = tokio::task::spawn_blocking(move || {
        setup.complete(&req.token, &req.username, &password, ip)
    })
    .await;

    let outcome = match outcome {
        Ok(o) => o,
        Err(_) => return AppError::internal().into_response(),
    };

    match outcome {
        Ok(o) => (
            StatusCode::CREATED,
            Json(serde_json::json!({ "user": { "id": o.user_id, "name": o.username } })),
        )
            .into_response(),
        Err(e) => setup_error_response(e).into_response(),
    }
}

fn setup_error_response(e: crate::setup_api::SetupError) -> AppError {
    use crate::setup_api::SetupError as E;
    match e {
        E::Completed => AppError::new(
            ErrorCode::SetupCompleted,
            "first-run setup is already complete",
        ),
        E::Expired => AppError::new(
            ErrorCode::SetupTokenExpired,
            "the setup token has expired; restart the server to issue a new one",
        ),
        E::InvalidToken => AppError::new(ErrorCode::SetupInvalidToken, "invalid setup token"),
        E::InvalidUsername(reason) => {
            AppError::new(ErrorCode::SetupInvalidUsername, "invalid username")
                .with_detail(serde_json::json!({ "reason": reason }))
        }
        E::WeakPassword { min_len } => AppError::new(
            ErrorCode::SetupWeakPassword,
            "password is too short",
        )
        .with_detail(serde_json::json!({ "min_length": min_len })),
        E::Internal => AppError::internal(),
    }
}

// ------------------------------------------------------------------- auth --

#[derive(Deserialize)]
struct LoginReq {
    username: String,
    password: String,
}

#[derive(Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
enum LoginResp {
    Ok { user: UserInfo },
    TotpRequired { challenge: String },
}

#[derive(Serialize)]
struct UserInfo {
    id: u32,
    name: String,
}

fn client_ip_of(headers_ext: Option<&ClientIp>) -> std::net::IpAddr {
    headers_ext.map(|c| c.0).unwrap_or(crate::middleware::UNKNOWN_CLIENT)
}

/// `User-Agent`, for the "active sessions" list (`ua_first` is display-only, never an auth condition). Empty string — not
/// the header's absence — is what `create_session` stores for "none sent",
/// same as a missing header; a non-UTF-8 value (never sent by a real
/// browser) degrades the same way rather than failing the login.
fn user_agent_of(headers: &axum::http::HeaderMap) -> String {
    headers
        .get(axum::http::header::USER_AGENT)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string()
}

async fn auth_login(State(state): State<AppState>, req: axum::extract::Request) -> Response {
    let ip = client_ip_of(req.extensions().get::<ClientIp>());
    let (parts, body) = req.into_parts();
    let ua = user_agent_of(&parts.headers);
    let bytes = match axum::body::to_bytes(body, 1024 * 1024).await {
        Ok(b) => b,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    let req: LoginReq = match serde_json::from_slice(&bytes) {
        Ok(r) => r,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    let _ = parts;

    match state.auth.login(&req.username, &SecretString::from(req.password), ip).await {
        sc_auth::LoginResult::Ok(uid) => {
            let token = match state.auth.create_session(uid, ip, &ua, sc_auth::AMR_PASSWORD) {
                Ok(t) => t,
                Err(_) => return AppError::internal().into_response(),
            };
            let mut resp = Json(LoginResp::Ok { user: UserInfo { id: uid.get(), name: req.username } }).into_response();
            set_session_cookie(&mut resp, token.as_str());
            resp
        }
        sc_auth::LoginResult::TotpRequired { challenge } => Json(LoginResp::TotpRequired { challenge }).into_response(),
        sc_auth::LoginResult::RateLimited { retry_after_s } => {
            let mut resp = AppError::rate_limited(retry_after_s).into_response();
            if let Ok(v) = axum::http::HeaderValue::from_str(&retry_after_s.to_string()) {
                resp.headers_mut().insert("Retry-After", v);
            }
            resp
        }
        sc_auth::LoginResult::Invalid => AppError::invalid_credentials().into_response(),
    }
}

fn set_session_cookie(resp: &mut Response, token: &str) {
    // `__Host-` prefix: Secure + Path=/ + no Domain.
    let cookie = format!("{}={token}; Path=/; Secure; HttpOnly; SameSite=Lax", crate::SESSION_COOKIE);
    if let Ok(v) = axum::http::HeaderValue::from_str(&cookie) {
        resp.headers_mut().append(axum::http::header::SET_COOKIE, v);
    }
}

#[derive(Deserialize)]
struct TotpReq {
    challenge: String,
    code: String,
}

async fn auth_login_totp(State(state): State<AppState>, req: axum::extract::Request) -> Response {
    let ip = client_ip_of(req.extensions().get::<ClientIp>());
    let (parts, body) = req.into_parts();
    let ua = user_agent_of(&parts.headers);
    let bytes = match axum::body::to_bytes(body, 64 * 1024).await {
        Ok(b) => b,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    let req: TotpReq = match serde_json::from_slice(&bytes) {
        Ok(r) => r,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    match state.auth.verify_totp(&req.challenge, &req.code).await {
        Ok(Some(uid)) => {
            let token = match state.auth.create_session(uid, ip, &ua, sc_auth::AMR_PASSWORD) {
                Ok(t) => t,
                Err(_) => return AppError::internal().into_response(),
            };
            // Same lookup `auth_session` uses: the TOTP challenge only carries
            // a `UserId`, so the display name has to come from the account
            // row. This used to be `String::new()` — the same stub shape
            // `auth_session` was fixed to stop returning, left behind here.
            let name = state.auth.find_user_by_id(uid).ok().flatten().map(|u| u.name).unwrap_or_default();
            let mut resp = Json(LoginResp::Ok { user: UserInfo { id: uid.get(), name } }).into_response();
            set_session_cookie(&mut resp, token.as_str());
            resp
        }
        Ok(None) => AppError::invalid_credentials().into_response(),
        Err(_) => AppError::invalid_credentials().into_response(),
    }
}

async fn auth_logout(State(state): State<AppState>, req: axum::extract::Request) -> Response {
    if let Some(SessionToken(token)) = req.extensions().get::<SessionToken>() {
        let _ = state.auth.revoke_session(token);
    }
    // Closes every open socket of this user, not just the one behind this
    // cookie — `WsHub::revoke_user`'s own doc explains why that's the
    // coarsest-available granularity here, and why it's fine for a logout.
    if let Some(principal) = req.extensions().get::<Principal>() {
        state.ws.revoke_user(principal.user);
    }
    let mut resp = StatusCode::NO_CONTENT.into_response();
    let cookie = &format!("{}=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0", crate::SESSION_COOKIE);
    if let Ok(v) = axum::http::HeaderValue::from_str(cookie) {
        resp.headers_mut().append(axum::http::header::SET_COOKIE, v);
    }
    resp
}

#[derive(Serialize)]
struct ClientLimits {
    chunk_size: u64,
    /// `sc_upload::UploadConfig::chunk_size_min`:
    /// the hard floor a client's 413 shrink-adaptation must not go below
    /// (`shrinkChunkSize` in `chunk-planner.ts`).
    chunk_min: u64,
    max_file_size: Option<u64>,
    parallel: u32,
}

#[derive(Serialize)]
struct RootEntryWire {
    label: String,
    perms: serde_json::Value,
    share_kind: &'static str,
    shared_externally: bool,
    trash_enabled: bool,
}

/// The richer per-account view `GET /api/auth/session` returns — deliberately
/// a different (wider) shape than the bare `UserInfo` in `LoginResp::Ok`
/// (the login response is thin on purpose, the app
/// re-fetches this immediately after). Settings screens key their gating and
/// initial toggle state directly off these fields: `is_admin` decides whether
/// `/admin/*` even fetches anything, the rest seed
/// the password/TOTP/SMB sections without a second round trip.
#[derive(Serialize)]
struct SessionUserWire {
    id: u32,
    name: String,
    display_name: String,
    is_admin: bool,
    totp_enabled: bool,
    smb_opt_out: bool,
    smb_enabled: bool,
}

/// The caller's own OIDC link, for the settings screen's connect/disconnect
/// section (`docs/proposals/stowcloud-0-oidc-login.md` §5-1).
///
/// `subject_hint` rather than the subject: the screen only has to let a
/// person recognise *which* identity is attached, and the full `sub` is a
/// stable identifier at the IdP that nothing on this screen needs.
///
/// `linked_ns` is a decimal string, like every other nanosecond field this
/// API emits (`ActiveSession`, `list_app_passwords`). A nanosecond epoch is
/// around 1.8e18 and JavaScript's `number` loses precision past 2^53, so a
/// raw JSON number here would be a second, quietly wrong convention on the
/// same screen.
#[derive(Serialize)]
struct SessionOidcWire {
    linked: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    subject_hint: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    linked_ns: Option<String>,
}

#[derive(Serialize)]
struct SessionInfoWire {
    user: SessionUserWire,
    roots: Vec<RootEntryWire>,
    csrf: String,
    limits: ClientLimits,
    features: FeatureCaps,
    oidc: SessionOidcWire,
}

/// Enough of a `sub` to recognise, not enough to be a copy of it.
///
/// Subjects are opaque and vary wildly in shape between providers: a UUID
/// from Keycloak, a 21-digit number from Google, an email-looking string from
/// a badly configured one. Four characters from each end tells a person which
/// account they attached without reproducing the identifier. Anything short
/// enough that the two ends would overlap is shown as a length only, since
/// for those the "hint" would have been the whole value.
fn subject_hint(subject: &str) -> String {
    let chars: Vec<char> = subject.chars().collect();
    if chars.len() <= 8 {
        return format!("...({} characters)", chars.len());
    }
    let head: String = chars[..4].iter().collect();
    let tail: String = chars[chars.len() - 4..].iter().collect();
    format!("{head}...{tail}")
}

async fn auth_session(State(state): State<AppState>, principal: Option<Extension<Principal>>, req: axum::extract::Request) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let roots: Vec<RootEntryWire> = state
        .core
        .roots(principal.user)
        .into_iter()
        .map(|r| RootEntryWire {
            label: r.label,
            perms: crate::core_api::perms_to_json(r.perms),
            share_kind: "normal",
            shared_externally: r.shared_externally,
            trash_enabled: r.trash_enabled,
        })
        .collect();
    let csrf = match req.extensions().get::<SessionToken>() {
        Some(SessionToken(t)) => crate::middleware::derive_csrf_token(&state.csrf_key, t),
        None => String::new(),
    };
    let features = feature_caps(&state);
    let (chunk_min, chunk_default) = state.uploads.chunk_limits();
    // `Principal` carries only a `UserId`, so the rest of the account row has
    // to be looked up — which is exactly what `find_user_by_id` documents
    // itself as being for.
    let row = state.auth.find_user_by_id(principal.user).ok().flatten();
    let user = match row {
        Some(u) => SessionUserWire {
            id: u.id.get(),
            display_name: u.display.clone().unwrap_or_else(|| u.name.clone()),
            name: u.name,
            is_admin: u.is_admin,
            totp_enabled: u.totp_enabled,
            smb_opt_out: u.smb_opt_out,
            smb_enabled: u.smb_enabled,
        },
        // The session validated (a `Principal` exists) but the account row
        // is gone — shouldn't happen outside a racing account deletion, but
        // answer with *something* rather than panic on the `unwrap` this
        // would otherwise need.
        None => SessionUserWire {
            id: principal.user.get(),
            name: String::new(),
            display_name: String::new(),
            is_admin: false,
            totp_enabled: false,
            smb_opt_out: false,
            smb_enabled: false,
        },
    };
    // A read failure here degrades to "not linked" rather than failing the
    // whole session lookup: this field seeds one settings section, and the
    // response it belongs to is what gates the entire app's first paint.
    let oidc = match state.auth.oidc_identity_of(principal.user) {
        Ok(Some(row)) => SessionOidcWire {
            linked: true,
            subject_hint: Some(subject_hint(&row.subject)),
            linked_ns: Some(row.linked_ns.to_string()),
        },
        Ok(None) => SessionOidcWire { linked: false, subject_hint: None, linked_ns: None },
        Err(e) => {
            tracing::warn!(error = %e, user = principal.user.get(), "reading an oidc link for the session response failed");
            SessionOidcWire { linked: false, subject_hint: None, linked_ns: None }
        }
    };
    Json(SessionInfoWire {
        user,
        roots,
        csrf,
        limits: ClientLimits {
            chunk_size: chunk_default,
            chunk_min,
            max_file_size: None,
            parallel: 4,
        },
        features,
        oidc,
    })
    .into_response()
}

/// Shared by every admin-only handler (`/admin/*`
/// must not even load for a non-admin, and the API has to enforce that too —
/// a hidden button is not access control). Looks the account up fresh rather
/// than trusting anything cached on `Principal`, since `Principal` carries
/// only a `UserId` (see `auth_session` above for why every other handler that
/// needs account fields does the same lookup).
fn require_admin(state: &AppState, principal: &Principal) -> Result<(), AppError> {
    match state.auth.find_user_by_id(principal.user) {
        Ok(Some(u)) if u.is_admin => Ok(()),
        Ok(_) => Err(AppError::acl_denied("admin")),
        Err(_) => Err(AppError::internal()),
    }
}

async fn list_app_passwords(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.auth.list_app_passwords(principal.user) {
        // times are `i64` nanoseconds serialized as a
        // JSON *string* — a raw JSON number silently loses precision past
        // 2^53, which a nanosecond epoch timestamp (~1.8e18) is nowhere near
        // fitting inside. Every other ns-bearing field in this file already
        // gets `.to_string()`'d for exactly this reason (see `ActiveSession`
        // above); this endpoint predates that convention being applied here.
        Ok(list) => Json(list.into_iter().map(|a| serde_json::json!({
            "id": a.id,
            "name": a.name,
            "created_ns": a.created_ns.to_string(),
            "last_used_ns": a.last_used_ns.map(|n| n.to_string()),
            "expires_ns": a.expires_ns.map(|n| n.to_string()),
        })).collect::<Vec<_>>()).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

/// `scope` on the wire, mirroring `ShareLinkCreate`'s existing `perms: Option<PermsReq>`
/// shape rather than inventing a parallel one. Absent (the whole field, or
/// either half of it) means unrestricted — existing clients that only ever
/// send `{"name": "..."}` keep minting `Scope::default()` app passwords
/// exactly as before this endpoint could take a scope at all.
///
/// `shares`, when present, is a list of the **labels** `GET /api/auth/session`
/// already returns in `roots[].label` — the same vocabulary a user picks a
/// share by everywhere else in this API — never a raw `ShareId`, which is an
/// internal identifier this crate does not otherwise put on the wire. Each
/// label is resolved against the caller's *own* current roots
/// (`state.core.roots(principal.user)`) and must name one of them; a label
/// that doesn't (typo'd, or a share the caller cannot see at all) rejects the
/// whole request with `auth.unknown_share` rather than silently dropping it
/// or minting a token nothing can ever satisfy.
#[derive(Deserialize, Default)]
struct AppPasswordScopeReq {
    #[serde(default)]
    perms: Option<crate::core_api::PermsReq>,
    #[serde(default)]
    shares: Option<Vec<String>>,
}

#[derive(Deserialize)]
struct CreateAppPasswordReq {
    name: String,
    #[serde(default)]
    scope: Option<AppPasswordScopeReq>,
}

async fn create_app_password(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(req): Json<CreateAppPasswordReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let scope = match req.scope {
        None => sc_auth::Scope::default(),
        Some(s) => {
            let perms_mask = s.perms.map(|p| p.to_perms().bits());
            let shares = match s.shares {
                None => None,
                Some(labels) => {
                    let roots = state.core.roots(principal.user);
                    let mut ids = Vec::with_capacity(labels.len());
                    for label in &labels {
                        match roots.iter().find(|r| &r.label == label) {
                            Some(r) => ids.push(r.share),
                            None => return AppError::unknown_share(label).into_response(),
                        }
                    }
                    Some(ids)
                }
            };
            sc_auth::Scope { perms_mask, shares }
        }
    };
    match state.auth.issue_app_password(principal.user, &req.name, scope) {
        Ok((id, token)) => Json(serde_json::json!({ "id": id, "token": token })).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

async fn revoke_app_password(State(state): State<AppState>, principal: Option<Extension<Principal>>, Path(id): Path<u32>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    // Scoped delete (`revoke_app_password_owned`), not the bare
    // `revoke_app_password` — an id is a small guessable integer, and this is
    // a self-service route: nothing here re-authenticates as an admin, so
    // ownership has to be the query's own `WHERE`, not a check bolted on
    // after the fact.
    match state.auth.revoke_app_password_owned(principal.user, id) {
        Ok(true) => StatusCode::NO_CONTENT.into_response(),
        Ok(false) => AppError::not_found().into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

// ------------------------------------------------------------------ password --

#[derive(Deserialize)]
struct ChangePasswordReq {
    current_password: String,
    new_password: String,
    #[serde(default)]
    revoke_other_sessions: bool,
}

/// `POST /api/auth/password` — Requires the
/// current password (never just an active session — this is a security-
/// relevant action) and, on success, unconditionally re-derives the SMB NT
/// hash via `AuthService::set_password`. `revoke_other_sessions` is the
/// choice §2.3 says must be the *user's*, not automatic: app passwords are
/// left alone either way — auto-revoking those too is exactly what §2.3
/// warns silently breaks sync clients.
async fn auth_change_password(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    req: axum::extract::Request,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let current_token = req.extensions().get::<SessionToken>().map(|SessionToken(t)| t.clone());
    let (_, body) = req.into_parts();
    let bytes = match axum::body::to_bytes(body, 64 * 1024).await {
        Ok(b) => b,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    let req: ChangePasswordReq = match serde_json::from_slice(&bytes) {
        Ok(r) => r,
        Err(_) => return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response(),
    };
    let current = SecretString::from(req.current_password);
    let new = SecretString::from(req.new_password);
    let outcome = state.auth.change_password(principal.user, &current, &new).await;
    match outcome {
        Ok(()) => {
            if req.revoke_other_sessions {
                if let (Ok(sessions), Some(tok)) = (state.auth.list_sessions(principal.user), &current_token) {
                    let keep = sc_auth::token_hash_hex(tok);
                    for s in sessions {
                        if s.id_hash_hex != keep {
                            let _ = state.auth.revoke_session_by_hash(principal.user, &s.id_hash_hex);
                        }
                    }
                }
            }
            StatusCode::NO_CONTENT.into_response()
        }
        Err(sc_auth::ChangePasswordError::BadCurrentPassword) => AppError::invalid_credentials().into_response(),
        Err(sc_auth::ChangePasswordError::TooShort { min }) => {
            AppError::new(ErrorCode::AuthWeakPassword, "password is too short")
                .with_detail(serde_json::json!({ "min_length": min }))
                .into_response()
        }
    }
}

// ---------------------------------------------------------------------- totp --

/// `POST /api/auth/totp/setup` — step one of enrollment. Persists nothing; the client shows the returned secret/QR and
/// confirms with a live code via `POST /api/auth/totp/enroll`.
async fn auth_totp_setup(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let name = state
        .auth
        .find_user_by_id(principal.user)
        .ok()
        .flatten()
        .map(|u| u.name)
        .unwrap_or_default();
    match state.auth.totp_setup(&name) {
        Ok(setup) => Json(serde_json::json!({ "secret": setup.secret, "otpauth_url": setup.otpauth_url })).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

#[derive(Deserialize)]
struct TotpEnrollReq {
    password: String,
    secret: String,
    code: String,
}

async fn auth_totp_enroll(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<TotpEnrollReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let pw = SecretString::from(req.password);
    match state.auth.totp_enroll(principal.user, &pw, &req.secret, &req.code) {
        Ok(recovery_codes) => Json(serde_json::json!({ "recovery_codes": recovery_codes })).into_response(),
        // The two failure modes sc-auth reports here (wrong current password,
        // wrong/expired 6-digit code) are both "this proof of possession
        // failed" — the same family as a failed login, so the same code.
        Err(_) => AppError::invalid_credentials().into_response(),
    }
}

#[derive(Deserialize)]
struct TotpDisableReq {
    password: String,
}

async fn auth_totp_disable(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<TotpDisableReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let pw = SecretString::from(req.password);
    // Deliberately no session revocation and no forced re-login here:
    // disabling 2FA re-confirms the password
    // in-session, it does not log the user out. `totp_disable` itself
    // re-derives the SMB NT hash in the same transaction.
    match state.auth.totp_disable(principal.user, &pw).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(_) => AppError::invalid_credentials().into_response(),
    }
}

// ------------------------------------------------------------------ sessions --

async fn auth_list_sessions(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    req: axum::extract::Request,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let current_hash = req.extensions().get::<SessionToken>().map(|SessionToken(t)| sc_auth::token_hash_hex(t));
    match state.auth.list_sessions(principal.user) {
        Ok(list) => Json(
            list.into_iter()
                .map(|s| {
                    serde_json::json!({
                        "id_hash": s.id_hash_hex,
                        "created_ns": s.created_ns.to_string(),
                        "last_seen_ns": s.last_seen_ns.to_string(),
                        "absolute_expiry_ns": s.absolute_expiry_ns.to_string(),
                        "ip_first": s.ip_first,
                        "ua_first": s.ua_first,
                        "current": current_hash.as_deref() == Some(s.id_hash_hex.as_str()),
                    })
                })
                .collect::<Vec<_>>(),
        )
        .into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

async fn auth_revoke_session(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id_hash): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.auth.revoke_session_by_hash(principal.user, &id_hash) {
        Ok(true) => {
            // Close just this session's socket — `revoke_user`
            // would also kill the tab performing the revoke, since the
            // sessions UI never lets you target your own current session
            // (`WsHub::revoke_session`'s doc explains the choice).
            state.ws.revoke_session(principal.user, &id_hash);
            StatusCode::NO_CONTENT.into_response()
        }
        Ok(false) => AppError::not_found().into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

// -------------------------------------------------------------------- smb --

#[derive(Deserialize)]
struct SmbSettingsReq {
    opt_out: bool,
    enabled: bool,
}

/// `POST /api/auth/smb` — the two self-service toggles from `user.smb_opt_out`
/// / `user.smb_enabled`. Publishing still also
/// requires the deployment-wide `smb.enabled`, which this account can't see
/// or change — that half stays admin-only config, not exposed here.
async fn auth_smb_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<SmbSettingsReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.auth.set_smb_settings(principal.user, req.opt_out, req.enabled) {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

// ------------------------------------------------------------------- oidc --
// `docs/proposals/stowcloud-0-oidc-login.md` §5-1. Eight routes: three a
// browser walks through with no credential, two self-service, three admin.
//
// The two transports here are not interchangeable, and §5-2 splits its error
// tables for exactly that reason. Every JSON route answers with a status code
// and the envelope. The callback answers with a redirect
// for every outcome except rate limiting, because a person arrives there in a
// browser: a JSON error body would render as a white page of JSON, and the
// only useful thing to do with them is put them back on a screen that can say
// what went wrong. Same symbolic codes either way.

/// Which of the two screens a callback returns to.
///
/// Decided from the flow record, not from the request, and decided *before*
/// anything the IdP sent is examined -- by then the flow has been consumed and
/// the binding cookie checked, so the mode is known and trustworthy even for
/// the error paths.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum OidcLanding {
    Login,
    Link,
}

impl OidcLanding {
    fn of(mode: sc_auth::OidcFlowMode) -> Self {
        match mode {
            sc_auth::OidcFlowMode::Login => OidcLanding::Login,
            sc_auth::OidcFlowMode::Link => OidcLanding::Link,
        }
    }

    /// Where a successful flow lands when it carried no `returnTo`.
    fn default_path(self) -> &'static str {
        match self {
            OidcLanding::Login => "/b/",
            OidcLanding::Link => "/settings/security",
        }
    }

    /// Where a failed flow lands. Login failures go to the login screen even
    /// though the flow may have started elsewhere: there is no session, so
    /// every other screen would immediately bounce them here anyway.
    fn error_path(self) -> &'static str {
        match self {
            OidcLanding::Login => "/login",
            OidcLanding::Link => "/settings/security",
        }
    }
}

fn oidc_sha256(input: &str) -> [u8; 32] {
    use sha2::Digest;
    sha2::Sha256::digest(input.as_bytes()).into()
}

/// Constant-time digest comparison for the binding cookie. Lengths that
/// differ answer `false` without comparing, which leaks only the length of a
/// value whose length is fixed and public.
fn oidc_hash_eq(a: &[u8], b: &[u8]) -> bool {
    use subtle::ConstantTimeEq;
    a.len() == b.len() && a.ct_eq(b).unwrap_u8() == 1
}

fn now_ns() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i64)
        .unwrap_or(0)
}

/// §5-1's `returnTo` rule, enforced server side.
///
/// The same three checks `safeReturnTo` makes in the login screen
/// (`web/src/routes/login/+page.svelte`), plus one that helper does not need
/// and this must not go without: **every byte printable ASCII**. That
/// TypeScript never puts the value in an HTTP header; this does, and a CR or
/// LF in a `Location` is header injection.
///
/// It must not be left to `HeaderValue::from_str` to catch. The established
/// pattern in this file (`set_session_cookie`) drops a header that fails to
/// parse and carries on, which here would mean a `302` with no `Location` at
/// all -- a blank page instead of a redirect, for a reason nothing reports.
///
/// A rejected value falls back to the mode's default rather than failing the
/// request. Somebody logging in successfully should not be shown an error
/// because a link they followed had a bad query parameter.
fn safe_return_to(raw: &str) -> Option<&str> {
    if !raw.starts_with('/') {
        return None;
    }
    // Both are protocol-relative URLs to a browser, i.e. an open redirect off
    // this origin entirely.
    if raw.starts_with("//") || raw.starts_with("/\\") {
        return None;
    }
    if !raw.bytes().all(|b| (0x20..=0x7E).contains(&b)) {
        return None;
    }
    Some(raw)
}

/// A `302` that either carries a `Location` or is not a `302` at all.
///
/// Every caller passes a value that already survived [`safe_return_to`] or
/// came from `url::Url`'s own serialization, so the error arm is unreachable
/// in practice. It answers `500` rather than an empty redirect because a
/// browser given a `Location`-less `302` shows nothing and reports nothing.
fn redirect_to(location: &str) -> Response {
    match axum::http::HeaderValue::from_str(location) {
        Ok(v) => {
            let mut resp = StatusCode::FOUND.into_response();
            resp.headers_mut().insert(axum::http::header::LOCATION, v);
            resp
        }
        Err(_) => {
            tracing::error!(location, "refusing to emit a 302 with an unencodable Location");
            AppError::internal().into_response()
        }
    }
}

fn set_flow_cookie(resp: &mut Response, binding: &str) {
    let cookie = format!(
        "{}={binding}; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age={}",
        crate::OIDC_FLOW_COOKIE,
        sc_auth::OIDC_FLOW_TTL.as_secs()
    );
    if let Ok(v) = axum::http::HeaderValue::from_str(&cookie) {
        resp.headers_mut().append(axum::http::header::SET_COOKIE, v);
    }
}

/// Expires the flow cookie. §4.3.1: this happens whether the callback
/// succeeded or failed, so it is folded into every response the callback can
/// produce except the `429`, which is refused before any flow is consumed and
/// which a legitimate browser may retry.
fn clear_flow_cookie(resp: &mut Response) {
    let cookie = format!(
        "{}=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0",
        crate::OIDC_FLOW_COOKIE
    );
    if let Ok(v) = axum::http::HeaderValue::from_str(&cookie) {
        resp.headers_mut().append(axum::http::header::SET_COOKIE, v);
    }
}

/// §5-2 table B: the symbolic code rides on the redirect as `?oidc_error=`,
/// and the flow cookie is expired on the way out.
fn oidc_error_redirect(landing: OidcLanding, code: &str) -> Response {
    let mut resp = redirect_to(&format!("{}?oidc_error={code}", landing.error_path()));
    clear_flow_cookie(&mut resp);
    resp
}

fn rate_limited_response(retry_after_s: u32) -> Response {
    let mut resp = AppError::rate_limited(retry_after_s).into_response();
    if let Ok(v) = axum::http::HeaderValue::from_str(&retry_after_s.to_string()) {
        resp.headers_mut().insert("Retry-After", v);
    }
    resp
}

/// The IdP's own `?error=` parameter, mapped into §5-2 table B's vocabulary.
///
/// Only `access_denied` has a row of its own there, and it is the one that
/// matters: the person said no, or the IdP's policy said no on their behalf,
/// and nothing about this server is broken. Every other OAuth error code
/// (`invalid_request`, `unauthorized_client`, `server_error`, ...) describes
/// a client registration or a provider that this deployment cannot use right
/// now, which is what `oidc.provider_unavailable` says.
fn map_idp_error(raw: &str) -> &'static str {
    match raw {
        "access_denied" => "oidc.access_denied",
        _ => "oidc.provider_unavailable",
    }
}

/// `GET /api/auth/oidc/config` -- whether to draw the button, and what to
/// write on it. Unauthenticated by necessity: the login screen has to decide
/// this before anyone has a credential.
///
/// Not merged into `GET /api/setup`, whose response
/// pins to a bare boolean and nothing more. Not merged into
/// `GET /api/capabilities` either: that one answers "what can this server
/// do", and this answers "how do you get in", which is the question the
/// login screen is asking.
async fn oidc_config(State(state): State<AppState>) -> Response {
    let d = state.oidc.display();
    Json(serde_json::json!({ "enabled": d.enabled, "display_name": d.display_name })).into_response()
}

/// `GET /api/auth/oidc/start` -- mint a flow, set the binding cookie, redirect
/// to the IdP.
///
/// A `302` rather than a JSON body with a URL in it because the login screen
/// reaches this with `window.location.href`, not `fetch`. An XHR that
/// receives a `302` follows it in the background and the browser never
/// actually goes anywhere.
async fn oidc_start(State(state): State<AppState>, req: axum::extract::Request) -> Response {
    if !state.oidc.display().enabled {
        return AppError::new(ErrorCode::OidcDisabled, "single sign-on is not configured").into_response();
    }
    let ip = client_ip_of(req.extensions().get::<ClientIp>());
    if let Some(retry) = state.oidc_rate.check(ip) {
        return rate_limited_response(retry);
    }

    let raw_return_to = Query::<HashMap<String, String>>::try_from_uri(req.uri())
        .ok()
        .and_then(|Query(m)| m.get("returnTo").cloned());
    let return_to = raw_return_to.as_deref().and_then(safe_return_to);

    let started = match state.oidc.begin().await {
        Ok(s) => s,
        Err(e) => return oidc_begin_error(e),
    };
    if let Err(e) = state.auth.create_oidc_flow(sc_auth::NewOidcFlow {
        state_hash: started.state_hash,
        binding_hash: started.binding_hash,
        nonce_hash: started.nonce_hash,
        code_verifier: &started.code_verifier,
        mode: sc_auth::OidcFlowMode::Login,
        link_user: None,
        return_to,
    }) {
        tracing::error!(error = %e, "recording an oidc flow failed");
        return AppError::internal().into_response();
    }

    let mut resp = redirect_to(&started.authorize_url);
    set_flow_cookie(&mut resp, started.binding.expose_secret());
    resp
}

/// Table A's two answers for a failed `begin()`, shared by `/start` and
/// `POST /link/start`.
fn oidc_begin_error(e: crate::oidc_api::OidcError) -> Response {
    match e {
        crate::oidc_api::OidcError::ProviderUnavailable(m) => {
            tracing::warn!(detail = %m, "oidc provider is unreachable");
            AppError::new(ErrorCode::OidcProviderUnavailable, "the identity provider is unreachable").into_response()
        }
        crate::oidc_api::OidcError::Internal(m) => {
            tracing::error!(detail = %m, "starting an oidc flow failed");
            AppError::internal().into_response()
        }
    }
}

#[derive(Deserialize, Default)]
struct OidcCallbackQuery {
    code: Option<String>,
    state: Option<String>,
    error: Option<String>,
}

/// `GET /api/auth/oidc/callback` -- the ordering §5-1's pseudocode specifies,
/// in that order, and the order is the security property.
///
/// `state` is required on an error response exactly as much as on a success
/// one, and the flow is consumed and the binding cookie checked **before**
/// anything the IdP sent is looked at. Handling `?error=` first would make
/// "every callback is protected by state" false and would leave the flow
/// record unconsumed and replayable, which is what the draft did.
///
/// The `429` is the only status code this route ever answers with. The rate
/// gate fires before a flow exists, so there is no mode yet and therefore no
/// safe screen to land on; every later failure has one.
async fn oidc_callback(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    req: axum::extract::Request,
) -> Response {
    if !state.oidc.display().enabled {
        return oidc_error_redirect(OidcLanding::Login, "oidc.disabled");
    }
    let ip = client_ip_of(req.extensions().get::<ClientIp>());
    if let Some(retry) = state.oidc_rate.check(ip) {
        return rate_limited_response(retry);
    }
    let ua = user_agent_of(req.headers());
    let q = Query::<OidcCallbackQuery>::try_from_uri(req.uri())
        .map(|Query(q)| q)
        .unwrap_or_default();

    let Some(state_param) = q.state.as_deref().filter(|s| !s.is_empty()) else {
        return oidc_error_redirect(OidcLanding::Login, "oidc.bad_request");
    };
    // Lookup and delete in one transaction, so a replayed callback URL finds
    // nothing the second time.
    let flow = match state.auth.take_oidc_flow(&oidc_sha256(state_param)) {
        Ok(Some(f)) => f,
        Ok(None) => return oidc_error_redirect(OidcLanding::Login, "oidc.bad_state"),
        Err(e) => {
            tracing::error!(error = %e, "reading an oidc flow failed");
            return oidc_error_redirect(OidcLanding::Login, "internal");
        }
    };
    let landing = OidcLanding::of(flow.mode);

    // The browser binding (§4.3.1). This, not `state`, is what stops an
    // attacker delivering a flow they started to somebody else's browser.
    // Applied to the error paths as much as the success path.
    let binding_ok = crate::middleware::cookie_value(req.headers(), crate::OIDC_FLOW_COOKIE)
        .map(|v| oidc_hash_eq(&oidc_sha256(v), &flow.binding_hash))
        .unwrap_or(false);
    if !binding_ok {
        return oidc_error_redirect(landing, "oidc.bad_state");
    }
    if flow.expires_ns < now_ns() {
        return oidc_error_redirect(landing, "oidc.expired");
    }

    // Only now is the IdP's own answer examined. Exactly one of `code` and
    // `error` is acceptable; both or neither is a malformed callback.
    let code = match (
        q.code.as_deref().filter(|s| !s.is_empty()),
        q.error.as_deref().filter(|s| !s.is_empty()),
    ) {
        (Some(_), Some(_)) | (None, None) => return oidc_error_redirect(landing, "oidc.bad_request"),
        (None, Some(e)) => {
            tracing::info!(landing = ?landing, "the identity provider refused the authorization request");
            return oidc_error_redirect(landing, map_idp_error(e));
        }
        (Some(code), None) => code,
    };

    let Ok(nonce_hash) = <[u8; 32]>::try_from(flow.nonce_hash.as_slice()) else {
        tracing::error!("oidc flow row carries a nonce hash that is not 32 bytes");
        return oidc_error_redirect(landing, "internal");
    };
    let identity = match state.oidc.redeem(code, &flow.code_verifier, &nonce_hash).await {
        Ok(i) => i,
        Err(crate::oidc_api::OidcError::ProviderUnavailable(m)) => {
            tracing::warn!(detail = %m, "the oidc code exchange or id token verification failed");
            return oidc_error_redirect(landing, "oidc.provider_unavailable");
        }
        Err(crate::oidc_api::OidcError::Internal(m)) => {
            tracing::error!(detail = %m, "redeeming an oidc code failed");
            return oidc_error_redirect(landing, "internal");
        }
    };

    // Re-validated on the way out as well as on the way in: this string has
    // been through the database since it was checked, and re-checking it
    // costs three comparisons.
    let return_to = flow
        .return_to
        .as_deref()
        .and_then(safe_return_to)
        .unwrap_or(landing.default_path())
        .to_string();

    if landing == OidcLanding::Link {
        return oidc_callback_link(&state, principal, &flow, &identity, &return_to);
    }
    oidc_callback_login(&state, &identity, &return_to, ip, &ua)
}

/// §4.3.2's link half. No session is issued: this is an extra credential on a
/// session that is already authenticated, not a new one.
fn oidc_callback_link(
    state: &AppState,
    principal: Option<Extension<Principal>>,
    flow: &sc_auth::OidcFlow,
    identity: &crate::oidc_api::VerifiedIdentity,
    return_to: &str,
) -> Response {
    // Step 2: the session that started this flow has to still be the session
    // finishing it. A logout or an account switch during the IdP round trip
    // would otherwise attach the identity to whoever happens to be signed in
    // when the browser comes back.
    let session_user = principal.as_ref().map(|Extension(p)| p.user);
    if session_user != flow.link_user {
        return oidc_error_redirect(OidcLanding::Link, "oidc.link_session_changed");
    }
    let Some(uid) = flow.link_user else {
        // A link flow with no `link_user` cannot be written by `/link/start`;
        // the branch exists because the column is nullable for login flows.
        return oidc_error_redirect(OidcLanding::Link, "internal");
    };

    // `link_oidc_identity` writes the `auth.oidc_linked` audit event itself,
    // and in the same transaction deletes the account-password NT hash and
    // republishes the passdb (§4.3.6). Re-linking the identity this account
    // already has is idempotent success on purpose: a double-submitted
    // callback must not report a failure for a state that is exactly what was
    // asked for.
    match state.auth.link_oidc_identity(uid, &identity.issuer, &identity.subject) {
        Ok(()) => {}
        Err(sc_auth::OidcLinkError::SubjectTaken) => {
            return oidc_error_redirect(OidcLanding::Link, "oidc.subject_already_linked")
        }
        Err(sc_auth::OidcLinkError::AlreadyLinked) => {
            return oidc_error_redirect(OidcLanding::Link, "oidc.already_linked")
        }
        Err(sc_auth::OidcLinkError::InvalidSubject) => {
            // Unreachable through the real flow -- `sc-oidc` refuses an ID
            // token with an empty `sub` at verification step 11 -- but the
            // admin manual-link path can produce it, so the code exists and
            // is answered with rather than swallowed.
            return oidc_error_redirect(OidcLanding::Link, "oidc.invalid_subject");
        }
        Err(sc_auth::OidcLinkError::Internal(m)) => {
            tracing::error!(detail = %m, user = uid.get(), "linking an oidc identity failed");
            return oidc_error_redirect(OidcLanding::Link, "internal");
        }
    }
    let mut resp = redirect_to(return_to);
    clear_flow_cookie(&mut resp);
    resp
}

/// §5-1's login half. "Not linked" and "linked to a disabled account" answer
/// with the same code on the wire (§7.2's enumeration defense) and different
/// `detail` in the audit log.
fn oidc_callback_login(
    state: &AppState,
    identity: &crate::oidc_api::VerifiedIdentity,
    return_to: &str,
    ip: std::net::IpAddr,
    ua: &str,
) -> Response {
    let uid = match state.auth.find_oidc_identity(&identity.issuer, &identity.subject) {
        Ok(Some(u)) => u,
        Ok(None) => {
            // No JIT provisioning (§2.3): an identity the administrator has
            // not linked to an account is not an account.
            state.auth.audit(None, "auth.login_failed", None, Some(ip), false, Some("oidc_not_linked"));
            return oidc_error_redirect(OidcLanding::Login, "oidc.not_linked");
        }
        Err(e) => {
            tracing::error!(error = %e, "resolving an oidc identity failed");
            return oidc_error_redirect(OidcLanding::Login, "internal");
        }
    };
    match state.auth.find_user_by_id(uid) {
        Ok(Some(row)) if row.disabled => {
            state.auth.audit(Some(uid), "auth.login_failed", None, Some(ip), false, Some("disabled"));
            return oidc_error_redirect(OidcLanding::Login, "oidc.not_linked");
        }
        Ok(Some(_)) => {}
        // The identity row survives its account only if a delete raced this
        // request; `delete_user` removes both in one transaction.
        Ok(None) => {
            state.auth.audit(Some(uid), "auth.login_failed", None, Some(ip), false, Some("no_such_account"));
            return oidc_error_redirect(OidcLanding::Login, "oidc.not_linked");
        }
        Err(e) => {
            tracing::error!(error = %e, "reading an account for an oidc login failed");
            return oidc_error_redirect(OidcLanding::Login, "internal");
        }
    }

    let token = match state.auth.create_session(uid, ip, ua, sc_auth::AMR_OIDC) {
        Ok(t) => t,
        Err(e) => {
            tracing::error!(error = %e, "creating a session for an oidc login failed");
            return oidc_error_redirect(OidcLanding::Login, "internal");
        }
    };
    state.auth.touch_oidc_last_login(uid);
    state.auth.audit(Some(uid), "auth.login", None, Some(ip), true, Some("oidc"));

    let mut resp = redirect_to(return_to);
    set_session_cookie(&mut resp, token.as_str());
    clear_flow_cookie(&mut resp);
    resp
}

/// `returnTo` on the wire is camelCase here and in the `/start` query string,
/// matching the login screen's existing `?returnTo=` parameter rather than
/// this API's snake_case default. The snake_case spelling is accepted too, so
/// a client that assumed the house style is not silently ignored.
#[derive(Deserialize)]
struct OidcLinkStartReq {
    password: String,
    #[serde(default, rename = "returnTo", alias = "return_to")]
    return_to: Option<String>,
}

/// `POST /api/auth/oidc/link/start` -- re-confirm the password, then start a
/// link-mode flow.
///
/// The password is why this is a `POST` with a body and not the same `GET`
/// the login path uses. Correction 6: linking **adds a permanent credential**
/// to the account, so somebody with a few seconds at an unlocked screen must
/// not be able to attach their own IdP identity and keep coming back after
/// the victim changes their password and revokes every session.
/// charges a password for enabling *and* disabling
/// TOTP for precisely this reason.
///
/// It deliberately does **not** check whether the account is already linked.
/// A check here would be TOCTOU against the IdP round trip, and it would make
/// the callback's own already-linked branches unreachable dead code
/// (cross-review M4). `link_oidc_identity`'s return value is the only
/// verdict.
async fn oidc_link_start(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<OidcLinkStartReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if !state.oidc.display().enabled {
        return AppError::new(ErrorCode::OidcDisabled, "single sign-on is not configured").into_response();
    }
    if !state.auth.reconfirm_password(principal.user, &SecretString::from(req.password)).await {
        state.auth.audit(Some(principal.user), "auth.oidc_link_denied", None, None, false, Some("bad_password"));
        return AppError::invalid_credentials().into_response();
    }
    let return_to = req.return_to.as_deref().and_then(safe_return_to);

    let started = match state.oidc.begin().await {
        Ok(s) => s,
        Err(e) => return oidc_begin_error(e),
    };
    if let Err(e) = state.auth.create_oidc_flow(sc_auth::NewOidcFlow {
        state_hash: started.state_hash,
        binding_hash: started.binding_hash,
        nonce_hash: started.nonce_hash,
        code_verifier: &started.code_verifier,
        mode: sc_auth::OidcFlowMode::Link,
        link_user: Some(principal.user),
        return_to,
    }) {
        tracing::error!(error = %e, "recording an oidc link flow failed");
        return AppError::internal().into_response();
    }

    // A URL in a JSON body, unlike `/start`'s `302`: this one is called with
    // `fetch` (it carries a password and a CSRF header), and the caller does
    // the navigation itself.
    let mut resp = Json(serde_json::json!({ "authorize_url": started.authorize_url })).into_response();
    set_flow_cookie(&mut resp, started.binding.expose_secret());
    resp
}

#[derive(Deserialize)]
struct OidcUnlinkReq {
    password: String,
}

/// `DELETE /api/auth/oidc/link` -- remove the identity, re-derive the SMB NT
/// hash from the password this route just re-confirmed, and revoke every
/// session the IdP issued.
///
/// The password is verified inside `unlink_oidc_identity`, not here, because
/// it is needed there anyway: `validate_session` looks at expiry and
/// `user.disabled` and nothing else, so removing the identity row alone would
/// leave every OIDC-issued session alive, and re-deriving the NT hash needs
/// the plaintext (§4.3.6).
async fn oidc_unlink(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<OidcUnlinkReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    // Which sockets to close, decided before the rows are gone. `list_sessions`
    // is the only place `amr` is readable, and `unlink_oidc_identity` reports
    // how many sessions it deleted but not which. Closing exactly these is
    // what keeps a password-authenticated tab of the same account open --
    // `WsHub::revoke_user` would have logged that one out too, for a change
    // that did not touch its session at all.
    let oidc_session_hashes: Vec<String> = state
        .auth
        .list_sessions(principal.user)
        .unwrap_or_default()
        .into_iter()
        .filter(|s| s.amr & sc_auth::AMR_OIDC != 0)
        .map(|s| s.id_hash_hex)
        .collect();

    match state.auth.unlink_oidc_identity(principal.user, Some(&SecretString::from(req.password))) {
        Ok(_) => {
            for hash in &oidc_session_hashes {
                state.ws.revoke_session(principal.user, hash);
            }
            StatusCode::NO_CONTENT.into_response()
        }
        Err(sc_auth::OidcUnlinkError::BadPassword) => AppError::invalid_credentials().into_response(),
        Err(sc_auth::OidcUnlinkError::NotLinked) => {
            AppError::new(ErrorCode::OidcNotLinked, "this account has no linked identity").into_response()
        }
        Err(sc_auth::OidcUnlinkError::Internal(m)) => {
            tracing::error!(detail = %m, user = principal.user.get(), "unlinking an oidc identity failed");
            AppError::internal().into_response()
        }
    }
}

// ------------------------------------------------------------ admin: oidc --

/// `GET /api/admin/users/{id}/oidc` -- the whole `oidc_identity` row.
///
/// The full `subject`, not the `subject_hint` `GET /api/auth/session` shows.
/// An administrator resolving "why can this person not sign in" needs the
/// exact string to compare against what the IdP shows, and this route is
/// already behind `require_admin` and closed to app passwords.
async fn admin_get_user_oidc(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let uid = match parse_user_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.auth.oidc_identity_of(uid) {
        Ok(Some(row)) => Json(serde_json::json!({
            "linked": true,
            "issuer": row.issuer,
            "subject": row.subject,
            // Nanoseconds as a decimal string, like every other ns field in
            // this file: 1.8e18 does not survive a JSON number in JavaScript.
            "linked_ns": row.linked_ns.to_string(),
            "last_login_ns": row.last_login_ns.map(|n| n.to_string()),
        }))
        .into_response(),
        Ok(None) => Json(serde_json::json!({
            "linked": false,
            "issuer": serde_json::Value::Null,
            "subject": serde_json::Value::Null,
            "linked_ns": serde_json::Value::Null,
            "last_login_ns": serde_json::Value::Null,
        }))
        .into_response(),
        Err(e) => {
            tracing::error!(error = %e, user = uid.get(), "reading an oidc identity failed");
            AppError::internal().into_response()
        }
    }
}

#[derive(Deserialize)]
struct AdminOidcLinkReq {
    subject: String,
}

/// `PUT /api/admin/users/{id}/oidc` -- attach an identity by hand.
///
/// This does not contradict "no JIT provisioning" (§2.3), because it creates
/// no account: an administrator names an account that already exists and an
/// identity that already exists, and says the two are the same person. It is
/// also the recovery path for an account whose owner does not know its
/// password and therefore cannot drive `POST /api/auth/oidc/link/start`.
///
/// The `issuer` is this deployment's configured one, never a request field.
/// An `oidc_identity` row keyed by an issuer nothing authenticates against
/// would be a link that can never be used, and a request-supplied issuer
/// would let an administrator create exactly that by typo.
async fn admin_put_user_oidc(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(req): Json<AdminOidcLinkReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let uid = match parse_user_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    let Some(issuer) = state.oidc.issuer() else {
        return AppError::new(ErrorCode::OidcDisabled, "single sign-on is not configured").into_response();
    };
    let subject = req.subject.trim();
    if subject.is_empty() {
        return AppError::new(ErrorCode::OidcInvalidSubject, "subject must not be empty").into_response();
    }
    match state.auth.link_oidc_identity(uid, &issuer, subject) {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(sc_auth::OidcLinkError::SubjectTaken) => {
            AppError::new(ErrorCode::OidcSubjectAlreadyLinked, "that identity belongs to another account").into_response()
        }
        Err(sc_auth::OidcLinkError::AlreadyLinked) => {
            AppError::new(ErrorCode::OidcAlreadyLinked, "this account already has a linked identity").into_response()
        }
        Err(sc_auth::OidcLinkError::InvalidSubject) => {
            AppError::new(ErrorCode::OidcInvalidSubject, "subject must not be empty").into_response()
        }
        Err(sc_auth::OidcLinkError::Internal(m)) => {
            tracing::error!(detail = %m, user = uid.get(), "an admin oidc link failed");
            AppError::internal().into_response()
        }
    }
}

/// `DELETE /api/admin/users/{id}/oidc` -- unlink somebody else's identity.
///
/// **Answers `200` with a body, where §5-1 wrote `204`.** A `204` carries no
/// body by definition, and §4.3.6 requires this response to state that SMB
/// access is not restored -- an administrator has no plaintext password, so
/// the NT hash this account's link deleted cannot be re-derived here. Leaving
/// that unsaid is the "quietly broken state" that section refuses, so the
/// body wins and the status code gives way.
async fn admin_delete_user_oidc(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let uid = match parse_user_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    let oidc_session_hashes: Vec<String> = state
        .auth
        .list_sessions(uid)
        .unwrap_or_default()
        .into_iter()
        .filter(|s| s.amr & sc_auth::AMR_OIDC != 0)
        .map(|s| s.id_hash_hex)
        .collect();

    // `None`: no plaintext, therefore no re-derivation. That is the whole
    // reason this route reports what it did instead of just succeeding.
    match state.auth.unlink_oidc_identity(uid, None) {
        Ok(outcome) => {
            for hash in &oidc_session_hashes {
                state.ws.revoke_session(uid, hash);
            }
            Json(serde_json::json!({
                "smb_nt_restored": outcome.smb_nt_restored,
                "oidc_sessions_revoked": outcome.oidc_sessions_revoked,
            }))
            .into_response()
        }
        Err(sc_auth::OidcUnlinkError::NotLinked) => {
            AppError::new(ErrorCode::OidcNotLinked, "this account has no linked identity").into_response()
        }
        // Unreachable: only the self-service path passes a password.
        Err(sc_auth::OidcUnlinkError::BadPassword) => AppError::invalid_credentials().into_response(),
        Err(sc_auth::OidcUnlinkError::Internal(m)) => {
            tracing::error!(detail = %m, user = uid.get(), "an admin oidc unlink failed");
            AppError::internal().into_response()
        }
    }
}

// --------------------------------------------------------------------- fs --

#[derive(Deserialize)]
struct ListQuery {
    path: Option<String>,
    sort: Option<SortKey>,
    order: Option<Order>,
    limit: Option<usize>,
    listing: Option<String>,
    cursor: Option<String>,
}

async fn fs_list(State(state): State<AppState>, principal: Option<Extension<Principal>>, Query(q): Query<ListQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let page_size = q.limit.unwrap_or(crate::listing::PAGE_SIZE_DEFAULT).clamp(1, 5000);

    if let (Some(listing_id), Some(cursor)) = (&q.listing, &q.cursor) {
        // Continuing an existing session: current dir_etag is whatever
        // `CoreApi::list` reports right now for staleness comparison.
        let vpath = q.path.clone().unwrap_or_default();
        let current_etag = match state.core.list(principal.user, &vpath, q.sort.unwrap_or(SortKey::Name), q.order.unwrap_or(Order::Asc)) {
            Ok(l) => l.dir_etag,
            Err(_) => String::new(),
        };
        return match state.listings.page(principal.user.get(), listing_id, cursor, &current_etag, page_size) {
            Ok(page) => {
                let mut resp = Json(serde_json::json!({
                    "listing": page.listing_id, "total": page.total, "cursor": page.cursor,
                    "entries": page.entries, "dir_etag": page.dir_etag,
                }))
                .into_response();
                if page.stale {
                    resp.headers_mut().insert("Sc-Listing-Stale", axum::http::HeaderValue::from_static("1"));
                }
                resp
            }
            Err(_) => AppError::new(ErrorCode::FsListingExpired, "listing session expired").with_status(StatusCode::CONFLICT).into_response(),
        };
    }
    if q.cursor.is_some() && q.listing.is_none() {
        return AppError::new(ErrorCode::FsListingExpired, "listing session expired").with_status(StatusCode::CONFLICT).into_response();
    }

    let vpath = q.path.clone().unwrap_or_default();
    match state.core.list(principal.user, &vpath, q.sort.unwrap_or(SortKey::Name), q.order.unwrap_or(Order::Asc)) {
        Ok(listing) => {
            let page = state.listings.create(principal.user.get(), listing.dir_etag.clone(), q.sort.unwrap_or(SortKey::Name), q.order.unwrap_or(Order::Asc), listing.entries, page_size);
            Json(serde_json::json!({
                "listing": page.listing_id, "total": page.total, "cursor": page.cursor,
                "entries": page.entries, "dir_etag": page.dir_etag,
            }))
            .into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct PathQuery {
    path: String,
}

async fn fs_stat(State(state): State<AppState>, principal: Option<Extension<Principal>>, Query(q): Query<PathQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.stat_entry(principal.user, &q.path) {
        Ok(entry) => Json(entry).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn fs_mkdir(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<PathQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.mkdir(principal.user, &q.path) {
        Ok(entry) => Json(entry).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct RenameReq {
    path: String,
    new_name: String,
}

async fn fs_rename(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<RenameReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.rename(principal.user, &q.path, &q.new_name) {
        Ok(entry) => Json(entry).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct MoveReq {
    paths: Vec<String>,
    dest: String,
    #[serde(default)]
    on_conflict: OnConflictWire,
    #[serde(default)]
    if_match: HashMap<String, String>,
    #[serde(default)]
    dry_run: bool,
}

#[derive(Deserialize, Default, Clone, Copy)]
#[serde(rename_all = "snake_case")]
enum OnConflictWire {
    #[default]
    Fail,
    Rename,
    Overwrite,
    Skip,
}

impl From<OnConflictWire> for OnConflict {
    fn from(v: OnConflictWire) -> Self {
        match v {
            OnConflictWire::Fail => OnConflict::Fail,
            OnConflictWire::Rename => OnConflict::Rename,
            OnConflictWire::Overwrite => OnConflict::Overwrite,
            OnConflictWire::Skip => OnConflict::Skip,
        }
    }
}

async fn fs_move(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<MoveReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if q.dry_run {
        // actually inspect the move instead of always
        // answering "no copy needed" — a hardcoded `false` here means the
        // cross-device pre-notice this endpoint exists to give never fires.
        return match state.core.move_entries_dry_run(principal.user, &q.paths, &q.dest, q.on_conflict.into(), &q.if_match) {
            Ok(results) => {
                let will_copy = results.iter().any(|r| r.will_copy);
                let total_bytes: u64 = if will_copy {
                    results
                        .iter()
                        .filter(|r| r.will_copy)
                        .filter_map(|r| state.core.stat_entry(principal.user, &r.path).ok())
                        .map(|e| e.size)
                        .sum()
                } else {
                    0
                };
                let reason = if will_copy { "cross_device" } else { "" };
                Json(serde_json::json!({ "will_copy": will_copy, "total_bytes": total_bytes, "reason": reason })).into_response()
            }
            Err(e) => AppError::from(e).into_response(),
        };
    }
    // Every non-dry-run move is a durable job, regardless of batch size — a
    // request that dies mid-batch (client gone, tab closed, proxy timeout)
    // must never leave an unrecorded item; see `spawn_batch_job`'s doc.
    let user = principal.user;
    let dest = q.dest.clone();
    let on_conflict = q.on_conflict.into();
    let if_match = q.if_match.clone();
    let core = state.core.clone();
    let id = spawn_batch_job(&state, user, JobKind::Move, q.paths, move |p| {
        let one = [p.to_string()];
        match core.move_entries(user, &one, &dest, on_conflict, &if_match) {
            Ok(mut results) => results.pop().unwrap_or_else(|| empty_op_result(p)),
            Err(e) => core_err_op_result(p, e),
        }
    });
    match id {
        Some(id) => (StatusCode::ACCEPTED, Json(serde_json::json!({ "job": id }))).into_response(),
        None => AppError::internal().into_response(),
    }
}

async fn fs_copy(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<MoveReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let user = principal.user;
    let dest = q.dest.clone();
    let on_conflict = q.on_conflict.into();
    let if_match = q.if_match.clone();
    let core = state.core.clone();
    let id = spawn_batch_job(&state, user, JobKind::Copy, q.paths, move |p| {
        let one = [p.to_string()];
        match core.copy_entries(user, &one, &dest, on_conflict, &if_match) {
            Ok(mut results) => results.pop().unwrap_or_else(|| empty_op_result(p)),
            Err(e) => core_err_op_result(p, e),
        }
    });
    match id {
        Some(id) => (StatusCode::ACCEPTED, Json(serde_json::json!({ "job": id }))).into_response(),
        None => AppError::internal().into_response(),
    }
}

#[derive(Deserialize)]
struct DeleteReq {
    paths: Vec<String>,
    #[serde(default)]
    permanent: bool,
}

async fn fs_delete(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<DeleteReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let user = principal.user;
    let permanent = q.permanent;
    let core = state.core.clone();
    let id = spawn_batch_job(&state, user, JobKind::Delete, q.paths, move |p| {
        let one = [p.to_string()];
        match core.delete(user, &one, permanent) {
            Ok(mut results) => results.pop().unwrap_or_else(|| empty_op_result(p)),
            Err(e) => core_err_op_result(p, e),
        }
    });
    match id {
        Some(id) => (StatusCode::ACCEPTED, Json(serde_json::json!({ "job": id }))).into_response(),
        None => AppError::internal().into_response(),
    }
}

/// `OpResult` for a slice call that came back empty — shouldn't happen
/// (`copy_entries`/`move_entries`/`delete` always answer one result per
/// input path) but a job runner can't `unwrap()` its way through someone
/// else's invariant.
fn empty_op_result(path: &str) -> OpResult {
    OpResult { path: path.to_string(), ok: false, error: Some(serde_json::json!({ "message": "no result" })), will_copy: false }
}

/// A whole-call `CoreError` (e.g. the destination itself doesn't resolve)
/// turned into a per-item failure, same `{"message": ...}` shape
/// `sc-server::bridge::http_op_result` already uses for item-level errors —
/// a job poller sees one consistent error shape regardless of which layer
/// produced it.
fn core_err_op_result(path: &str, e: CoreError) -> OpResult {
    OpResult { path: path.to_string(), ok: false, error: Some(serde_json::json!({ "message": e.to_string() })), will_copy: false }
}

fn new_job_id() -> String {
    format!("J-{}", uuid::Uuid::new_v4().simple())
}

/// Runs a copy/move/delete job to completion in a blocking task, calling
/// `op` once per requested path — the same one-`OpResult`-per-top-level-path
/// granularity `copy_entries`/`move_entries`/`delete` already return, so no
/// `sc-core` change was needed to get per-item progress and cancellation.
/// Cancellation is only checked *before* an item starts: the item already
/// running always finishes, so a cancelled copy/move never leaves that one
/// item half-written — only items after it are skipped ("an in-progress item is finished... before the job actually stops").
///
/// Record-before-act (the zero-loss requirement):
/// `begin_result` commits an `attempting` row *before* `op(p)` runs, so a
/// crash mid-item leaves that row exactly where it was — never an absent
/// one for a path the operation may already have removed, moved, or
/// duplicated. `finish_result` overwrites it with the real outcome once
/// `op(p)` actually returns. Nothing here accumulates results in memory —
/// each item's outcome is durable the moment it's known, so `finish` at the
/// end has nothing left to lose if the process dies before it runs.
/// Returns `None` if the parent job row itself could not be persisted
/// (`JobStore::insert`'s doc) — the caller must answer with a `500` rather
/// than a `202` for a job id no restart could ever recover.
fn spawn_batch_job(state: &AppState, user: UserId, kind: JobKind, paths: Vec<String>, op: impl Fn(&str) -> OpResult + Send + 'static) -> Option<String> {
    let id = new_job_id();
    let total = paths.len() as u64;
    // `with_pending`: the whole path list goes down with the job row, so an
    // interrupted (or cancelled) job can name the items it never reached, not
    // just count them. See `JobStatus::pending`.
    if !state.jobs.insert(JobStatus::new_running(id.clone(), user, kind, total).with_pending(&paths)) {
        return None;
    }

    let jobs = state.jobs.clone();
    let ws = state.ws.clone();
    let job_id = id.clone();
    tokio::task::spawn_blocking(move || {
        let mut done = 0u64;
        let mut all_ok = true;
        for (seq, p) in paths.iter().enumerate() {
            if jobs.is_cancelled(&job_id) {
                break;
            }
            if !jobs.begin_result(&job_id, seq as u64, p) {
                // The durability record itself could not be written durably
                // (state.rs's `begin_result` doc) — refuse to run a
                // destructive op with no proof it ran. Stop the whole job
                // rather than skipping just this item: a jobs.db write
                // failure here (disk full, wedged WAL checkpoint) will fail
                // identically for every item still queued.
                all_ok = false;
                break;
            }
            let result = op(p);
            all_ok &= result.ok;
            jobs.finish_result(&job_id, seq as u64, &result);
            done += 1;
            jobs.set_progress(&job_id, done, Some(p.clone()));
            ws.send_job_to_user(user, &job_id, done, total);
        }
        let final_state = if jobs.is_cancelled(&job_id) { JobState::Cancelled } else if all_ok { JobState::Done } else { JobState::Error };
        jobs.finish(&job_id, final_state);
    });
    Some(id)
}

async fn fs_read(State(state): State<AppState>, principal: Option<Extension<Principal>>, Query(q): Query<PathQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.read_text(principal.user, &q.path) {
        Ok(text) => Json(serde_json::json!({ "content": text })).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct WriteReq {
    path: String,
    content: String,
    if_match: Option<String>,
}

async fn fs_write(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<WriteReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.write_text(principal.user, &q.path, &q.content, q.if_match.as_ref()) {
        Ok(entry) => Json(entry).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct LinkReq {
    fid: i64,
    #[serde(default)]
    disposition: LinkDisposition,
    dim: Option<(u16, u16)>,
}

#[derive(Deserialize, Default)]
#[serde(rename_all = "snake_case")]
enum LinkDisposition {
    #[default]
    Attachment,
    InlineThumb,
    Stream,
}

/// Mints a signed content URL for a `fid` the caller already knows about
/// (from a `list`/`stat` response). **Does not trust the client for the
/// etag** — a stale or forged value would either 410 the link immediately or
/// (worse) keep it alive past a content change — so it is always
/// re-derived here from the server's current view of the file
/// (`ContentApi::stat_by_fid`), the same way `content_get` re-derives it at
/// verification time.
async fn fs_link(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<LinkReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let fid = sc_vfs::ids::FileId::new(q.fid);

    // The capability *is* the access control once a URL is minted
    // — so minting one must itself be ACL-checked.
    // Without this, any authenticated user could request a download link for
    // any `fid` on the server: `fid`s are small sequential integers, not
    // secrets.
    if let Err(e) = state.content.check_read(principal.user, fid) {
        return AppError::from(e).into_response();
    }
    let stat = match state.content.stat_by_fid(fid) {
        Ok(s) => s,
        Err(e) => return AppError::from(e).into_response(),
    };
    let etag8 = content::etag8_of(&stat.etag);

    let disp = match q.disposition {
        LinkDisposition::Attachment => Disposition::Attachment,
        LinkDisposition::InlineThumb => Disposition::InlineThumb,
        LinkDisposition::Stream => Disposition::Stream,
    };
    let claim = content::make_claim(q.fid, etag8, disp, q.dim, principal.user.get(), None);
    let token = content::sign(&state.signed_url_keys.lock(), claim);
    // `content::content_url` — not a hand-rolled `format!("https://{host}/c/{token}")`
    // — is load-bearing here: with `content_hosts` empty (the single-origin
    // fallback `.dev/sc.toml` and production both use) that pattern collapses
    // to `https:///c/<token>`, which a browser resolves to host `c`, not "no
    // host" (see `content_url`'s doc comment for the WHATWG mechanics and the
    // live-server symptom this produced: a same-tab download landing on
    // `chrome-error://chromewebdata/`).
    let url = content::content_url(state.cfg.content_hosts.first().map(String::as_str), &token);
    Json(serde_json::json!({ "url": url })).into_response()
}

#[derive(Deserialize)]
struct ArchiveReq {
    paths: Vec<String>,
}

/// /: ZIP64/STORE archive of a
/// batch selection, always run as a durable job (`spawn_archive_job`) — the
/// per-selection nature of an archive request doesn't fit the single-`fid`
/// `Claim` shape the rest of `/c/{token}` uses, so the finished bytes are
/// fetched once via `GET /api/jobs/{id}/download` instead.
async fn fs_archive(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<ArchiveReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if q.paths.is_empty() {
        return AppError::invalid_name("paths must not be empty").into_response();
    }

    // Each archive walk holds an open fd and walks a tree for its entire
    // duration — unbounded concurrency here is a trivial resource-
    // exhaustion vector, so it gets the same global-cap-plus-429 treatment
    // as search ('s `rate.limited` shape). Held by the
    // job for its whole duration, released when `spawn_archive_job`'s
    // blocking task ends.
    let permit = match state.archive_concurrency.current().try_acquire_owned() {
        Ok(p) => p,
        Err(_) => {
            let mut resp = AppError::rate_limited(1).into_response();
            resp.headers_mut().insert("Retry-After", axum::http::HeaderValue::from_static("1"));
            return resp;
        }
    };

    // Every archive request is a durable job now, regardless of size — the
    // request that created it is free to disconnect (tab closed, network
    // drop) without losing the walk in progress. `total` is an upfront
    // estimate for progress display only, not a threshold gate.
    let total = estimate_entry_count(&state, principal.user, &q.paths).max(q.paths.len() as u64);
    match spawn_archive_job(&state, principal.user, q.paths, total, permit) {
        Some(id) => (StatusCode::ACCEPTED, Json(serde_json::json!({ "job": id }))).into_response(),
        None => AppError::internal().into_response(),
    }
}

/// Upfront estimate of how many entries an archive job will walk — used only
/// to seed `total` for progress display (`done`/`total` in a job's status),
/// never to decide whether the request becomes a job (every archive request
/// does). Same `+1`-per-root / `stat_entry`-leaf-file fallback `batch_scale`
/// used to use for its threshold check: `aggregate` only walks directories
/// and errors on a plain file, so a batch of leaf files alone still counts.
/// A path that fails to resolve contributes nothing here; the walk itself
/// still reports that failure normally.
fn estimate_entry_count(state: &AppState, user: UserId, paths: &[String]) -> u64 {
    let mut count = 0u64;
    for p in paths {
        let Ok(r) = state.core.resolve(user, p) else { continue };
        if let Ok(agg) = state.core.aggregate(r.share, &r.subpath) {
            count += agg.file_count + agg.dir_count + 1;
        } else if state.core.stat_entry(user, p).is_ok() {
            count += 1;
        }
    }
    count
}

/// Runs an archive job to completion in a blocking task: walks every
/// requested path and builds the zip into an in-memory buffer — the request
/// that created the job may already be gone by the time this finishes. Kept
/// in memory rather than spooled to a new on-disk location, to avoid
/// introducing a data directory and a GC lifecycle nothing else needs;
/// the memory cost is bounded by the same `archive_concurrency` permit,
/// held for the job's whole duration.
/// Returns `None` if the parent job row could not be persisted
/// (`JobStore::insert`'s doc); `permit` is simply dropped in that case,
/// releasing the archive-concurrency slot without ever starting a walk.
fn spawn_archive_job(state: &AppState, user: UserId, paths: Vec<String>, total: u64, permit: tokio::sync::OwnedSemaphorePermit) -> Option<String> {
    let id = new_job_id();
    if !state.jobs.insert(JobStatus::new_running(id.clone(), user, JobKind::Archive, total)) {
        return None;
    }

    let jobs = state.jobs.clone();
    let ws = state.ws.clone();
    let core = state.core.clone();
    let job_id = id.clone();
    tokio::task::spawn_blocking(move || {
        let _permit = permit;
        let mut zip = crate::archive_zip::ZipStreamWriter::new(std::io::Cursor::new(Vec::new()));
        let mut skipped: Vec<String> = Vec::new();
        let mut done = 0u64;

        for p in &paths {
            // Checked only between top-level paths: `archive_walk`'s visitor
            // closure has no way to signal "stop descending" back to the
            // walker, so a cancellation mid-walk of one path still finishes
            // that path's tree — same boundary `spawn_batch_job` uses, and
            // harmless here since archiving never mutates the filesystem.
            if jobs.is_cancelled(&job_id) {
                jobs.finish(&job_id, JobState::Cancelled);
                return;
            }
            let root_label = p.trim_start_matches('/').split('/').next().unwrap_or("").to_string();
            let mut had_error = false;
            let result = core.archive_walk(user, p, &mut |entry, stream| {
                if !entry.readable {
                    skipped.push(format!("{root_label}/{}", entry.rel_path));
                    return;
                }
                if sc_vfs::SafePath::parse(&entry.rel_path, u16::MAX).is_err() {
                    skipped.push(format!("{root_label}/{} (unsafe name)", entry.rel_path));
                    return;
                }
                let full_name = format!("{root_label}/{}", entry.rel_path);
                let mtime = entry.mtime_ns.unwrap_or(0);
                if entry.is_dir {
                    let _ = zip.add_dir(&full_name, mtime);
                } else if let Some(r) = stream {
                    if zip.add_file(&full_name, mtime, r).is_err() {
                        had_error = true;
                    }
                }
                done += 1;
                jobs.set_progress(&job_id, done, Some(full_name));
                // Per-entry WS push would flood the socket on a walk of
                // thousands of files; polling still sees every increment
                // through `set_progress` above.
                if done.is_multiple_of(32) {
                    ws.send_job_to_user(user, &job_id, done, total);
                }
            });
            if result.is_err() {
                skipped.push(p.clone());
            } else if had_error {
                skipped.push(format!("{p} (write error, archive may be truncated)"));
            }
        }

        if jobs.is_cancelled(&job_id) {
            jobs.finish(&job_id, JobState::Cancelled);
            return;
        }
        if !skipped.is_empty() {
            let body = skipped.join("\n");
            let _ = zip.add_bytes("_skipped.txt", 0, body.as_bytes());
        }
        ws.send_job_to_user(user, &job_id, done, total);
        match zip.finish() {
            Ok(cursor) => jobs.finish_archive(&job_id, cursor.into_inner()),
            Err(_) => jobs.finish(&job_id, JobState::Error),
        }
    });
    Some(id)
}

// ---------------------------------------------------------------- uploads --

async fn uploads_options() -> Response {
    let mut resp = StatusCode::NO_CONTENT.into_response();
    let h = resp.headers_mut();
    h.insert("Tus-Resumable", axum::http::HeaderValue::from_static("1.0.0"));
    h.insert("Tus-Version", axum::http::HeaderValue::from_static("1.0.0"));
    h.insert("Tus-Extension", axum::http::HeaderValue::from_static("creation,creation-with-upload,checksum,termination"));
    resp
}

/// TUS `Upload-Metadata`: comma-separated `key base64(value)` pairs.
fn tus_metadata(headers: &axum::http::HeaderMap, key: &str) -> Option<String> {
    let raw = headers.get("upload-metadata")?.to_str().ok()?;
    for pair in raw.split(',') {
        let mut it = pair.trim().splitn(2, ' ');
        if it.next()? != key {
            continue;
        }
        let b64 = it.next().unwrap_or("");
        if b64.is_empty() {
            return Some(String::new());
        }
        let bytes = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, b64).ok()?;
        return String::from_utf8(bytes).ok();
    }
    None
}

fn tus_headers(resp: &mut Response) {
    let h = resp.headers_mut();
    h.insert("Tus-Resumable", axum::http::HeaderValue::from_static("1.0.0"));
}

/// The destination vpath a TUS upload-creation request names, computed from
/// `Upload-Metadata` exactly the way `uploads_create` derives `dest` below
/// (`dest` directory + `relativePath`/`filename` leaf) — pulled out so
/// `middleware::scope_gate` can ask the identical
/// question when checking `Scope::shares` on `POST /api/uploads`, rather
/// than risk a second, subtly different parse of the same header.
pub(crate) fn upload_dest_vpath(headers: &axum::http::HeaderMap) -> Option<String> {
    let dest_dir = tus_metadata(headers, "dest").unwrap_or_default();
    let leaf = tus_metadata(headers, "relativePath")
        .filter(|s| !s.is_empty())
        .or_else(|| tus_metadata(headers, "filename"));
    match leaf {
        Some(l) if !l.is_empty() => Some(if dest_dir.is_empty() {
            l
        } else {
            format!("{}/{}", dest_dir.trim_end_matches('/'), l.trim_start_matches('/'))
        }),
        _ => None,
    }
}

async fn uploads_create(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    req: axum::extract::Request,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let headers = req.headers().clone();
    let total_len = headers
        .get("upload-length")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<u64>().ok());
    // The destination is carried in `Upload-Metadata`; TUS has no other place
    // to put it, and a query parameter would end up in access logs.
    // splits it in two: `dest` is the destination
    // *directory*'s vpath, and the leaf appended to it is `relativePath`
    // (directory uploads: sub-path + filename) or, failing that, `filename`
    // (single-file uploads: bare basename). Neither key alone names a
    // target — `dest` alone is a directory, and `filename`/`relativePath`
    // alone lack the share label `Core::resolve` requires as the first path
    // segment.
    let dest = match upload_dest_vpath(&headers) {
        Some(d) => d,
        None => return AppError::invalid_name("Upload-Metadata must carry a `filename` or `relativePath`").into_response(),
    };
    // Opt-in parallel-PATCH extension. Only our own
    // web client sends this; third-party TUS clients get strict sequential
    // delivery, unaffected.
    let random_access = headers.get("sc-random-access").and_then(|v| v.to_str().ok()).map(|v| v == "1").unwrap_or(false);

    // TUS `creation-with-upload`: a `POST` whose body is already the first
    // bytes, saving a round trip that matters most on the mobile links this
    // has to work over. We advertise it in `Tus-Extension`; until this read
    // the body, the request was answered `201` with the bytes discarded, so a
    // client using the extension believed it had uploaded a prefix it had not.
    let with_body = headers
        .get(axum::http::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .map(|v| v.starts_with("application/offset+octet-stream"))
        .unwrap_or(false);

    if with_body {
        // Same idle-timeout defense as `uploads_patch` — a `creation-with-upload`
        // body is read exactly the same way a `PATCH` body is, so it needs the
        // same protection against a client that opens the request and stops
        // sending (`upload_api.rs`).
        let body = match crate::upload_api::read_body_with_idle_timeout(req.into_body(), state.uploads.body_idle_timeout()).await {
            Ok(b) => b,
            Err(crate::upload_api::BodyReadError::Idle) => {
                return AppError::new(ErrorCode::FsInvalidName, "upload body idle timeout: no data received in time")
                    .with_status(StatusCode::REQUEST_TIMEOUT)
                    .into_response()
            }
            Err(crate::upload_api::BodyReadError::Body) => {
                return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response()
            }
        };
        return match state
            .uploads
            .create_with_upload(principal.user, &dest, total_len, random_access, &body)
        {
            Ok(created) => {
                let mut resp = StatusCode::CREATED.into_response();
                tus_headers(&mut resp);
                let h = resp.headers_mut();
                if let Ok(v) = axum::http::HeaderValue::from_str(&format!("/api/uploads/{}", created.id)) {
                    h.insert(axum::http::header::LOCATION, v);
                }
                // The offset the server actually reached, not the body length
                // we were handed — they differ if the body overran
                // `Upload-Length`, and the client resumes from this number.
                if let Ok(v) = axum::http::HeaderValue::from_str(&created.offset.to_string()) {
                    h.insert("Upload-Offset", v);
                }
                resp
            }
            Err(e) => AppError::from(e).into_response(),
        };
    }

    match state.uploads.create(principal.user, &dest, total_len, random_access) {
        Ok(id) => {
            let mut resp = StatusCode::CREATED.into_response();
            tus_headers(&mut resp);
            if let Ok(v) = axum::http::HeaderValue::from_str(&format!("/api/uploads/{id}")) {
                resp.headers_mut().insert(axum::http::header::LOCATION, v);
            }
            resp.headers_mut()
                .insert("Upload-Offset", axum::http::HeaderValue::from_static("0"));
            resp
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn uploads_head(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.uploads.status(principal.user, &id) {
        Ok(st) => {
            let mut resp = StatusCode::NO_CONTENT.into_response();
            tus_headers(&mut resp);
            let h = resp.headers_mut();
            if let Ok(v) = axum::http::HeaderValue::from_str(&st.offset.to_string()) {
                h.insert("Upload-Offset", v);
            }
            if let Some(len) = st.length {
                if let Ok(v) = axum::http::HeaderValue::from_str(&len.to_string()) {
                    h.insert("Upload-Length", v);
                }
            }
            // the session's chunk size is fixed at
            // creation from the server config then, so a config change made
            // after the fact can't break a session already in flight. A
            // resuming client follows this header rather than trusting a
            // possibly-stale locally-remembered value.
            if let Ok(v) = axum::http::HeaderValue::from_str(&st.chunk_size.to_string()) {
                h.insert("Sc-Chunk-Size", v);
            }
            // A resumable session must never be revalidated from a cache.
            h.insert("Cache-Control", axum::http::HeaderValue::from_static("no-store"));
            resp
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn uploads_patch(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    req: axum::extract::Request,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let headers = req.headers().clone();
    let offset = match headers
        .get("upload-offset")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<u64>().ok())
    {
        Some(o) => o,
        None => return AppError::invalid_name("Upload-Offset is required").into_response(),
    };
    // No cap: `/api/uploads/**` is deliberately outside the body-limit layer,
    // so the only bound is the engine's. The
    // idle timeout (not a size cap) is the actual defense — a client that
    // opens this `PATCH` and stops sending must not hold the request, and the
    // engine's open part-file handle, forever (`upload_api.rs`).
    let body = match crate::upload_api::read_body_with_idle_timeout(req.into_body(), state.uploads.body_idle_timeout()).await {
        Ok(b) => b,
        Err(crate::upload_api::BodyReadError::Idle) => {
            return AppError::new(ErrorCode::FsInvalidName, "PATCH body idle timeout: no data received in time")
                .with_status(StatusCode::REQUEST_TIMEOUT)
                .into_response()
        }
        Err(crate::upload_api::BodyReadError::Body) => {
            return AppError::new(ErrorCode::FsInvalidName, "bad body").into_response()
        }
    };

    // `Upload-Checksum: <algo> <base64(digest)>`. We advertise `checksum` in
    // `Tus-Extension`, and until this was read the header was accepted and
    // ignored — the client's integrity check "passed" without anything ever
    // having been compared. An extension we advertise and drop is worse than
    // one we never claimed: it converts a real guarantee into a believed one.
    let checksum = match headers.get("upload-checksum").and_then(|v| v.to_str().ok()) {
        Some(raw) => match parse_upload_checksum(raw) {
            Some(c) => Some(c),
            // Malformed or an algorithm we do not implement. Refusing beats
            // silently proceeding unverified, which is the bug being fixed.
            None => {
                return AppError::invalid_name("Upload-Checksum is malformed or names an unsupported algorithm")
                    .into_response()
            }
        },
        None => None,
    };

    match state.uploads.patch_checked(principal.user, &id, offset, &body, checksum) {
        Ok(new_offset) => {
            let mut resp = StatusCode::NO_CONTENT.into_response();
            tus_headers(&mut resp);
            if let Ok(v) = axum::http::HeaderValue::from_str(&new_offset.to_string()) {
                resp.headers_mut().insert("Upload-Offset", v);
            }
            resp
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `<algo> <base64(digest)>`, per the TUS checksum extension. `None` for
/// anything we cannot verify — a caller that gets `None` must refuse rather
/// than fall back to an unchecked write.
fn parse_upload_checksum(raw: &str) -> Option<crate::upload_api::TusChecksum> {
    use crate::upload_api::{TusChecksum, TusChecksumAlgo};
    let (algo, b64) = raw.trim().split_once(' ')?;
    let algo = match algo.to_ascii_lowercase().as_str() {
        "crc32c" => TusChecksumAlgo::Crc32c,
        "blake3" => TusChecksumAlgo::Blake3,
        _ => return None,
    };
    let digest = data_encoding::BASE64.decode(b64.trim().as_bytes()).ok()?;
    if digest.is_empty() {
        return None;
    }
    Some(TusChecksum { algo, digest })
}

async fn uploads_delete(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.uploads.terminate(principal.user, &id) {
        Ok(()) => {
            let mut resp = StatusCode::NO_CONTENT.into_response();
            tus_headers(&mut resp);
            resp
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

// ------------------------------------------------------------------ trash --

async fn trash_list(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.trash_list(principal.user) {
        Ok(entries) => Json(entries).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// Trash ids are opaque strings — see `core_api::TrashEntry`.
#[derive(Deserialize)]
struct IdsReq {
    ids: Vec<String>,
}

async fn trash_restore(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<IdsReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.trash_restore(principal.user, &q.ids) {
        Ok(results) => Json(serde_json::json!({ "results": results })).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn trash_purge(State(state): State<AppState>, principal: Option<Extension<Principal>>, Json(q): Json<IdsReq>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.trash_purge(principal.user, &q.ids) {
        Ok(results) => Json(serde_json::json!({ "results": results })).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

// ----------------------------------------------------------------- shares --
// share-link CRUD over `sc_core::LinkStore`, reached
// through `CoreApi`. A deployment with no link store attached answers `501`
// from the trait's own defaults (`CoreError::NotSupported`) rather than
// accepting a create and dropping it.

#[derive(Deserialize)]
struct SharesQuery {
    path: Option<String>,
}

async fn shares_list(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Query(q): Query<SharesQuery>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.share_link_list(principal.user, q.path.as_deref()) {
        Ok(links) => Json(links).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn shares_create(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::core_api::ShareLinkCreate>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.core.share_link_create(principal.user, &req) {
        Ok((mut info, token)) => {
            // The one and only time the plaintext token is available. It is
            // not stored, so no later `GET` can produce it — the UI has to
            // show it now or lose it.
            info.url = Some(public_link_url(&state, &token));
            info.token = Some(token);
            (StatusCode::CREATED, Json(info)).into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

fn parse_share_id(raw: &str) -> Result<i64, AppError> {
    raw.parse::<i64>().map_err(|_| AppError::not_found())
}

async fn shares_get(State(state): State<AppState>, principal: Option<Extension<Principal>>, Path(id): Path<String>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let id = match parse_share_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.share_link_get(principal.user, id) {
        Ok(info) => Json(info).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn shares_patch(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(patch): Json<crate::core_api::ShareLinkPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let id = match parse_share_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.share_link_update(principal.user, id, &patch) {
        Ok(info) => Json(info).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn shares_delete(State(state): State<AppState>, principal: Option<Extension<Principal>>, Path(id): Path<String>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let id = match parse_share_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.share_link_delete(principal.user, id) {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

// ---------------------------------------------------- public share links --
// Everything below is reachable **without a
// session** (`middleware::is_public_path`); the token in the URL, plus the
// link password when one is set, is the entire authorization story.
//
// `X-Robots-Tag: noindex, nofollow` is not applied here — the
// `security_headers` middleware already sets it on every response, so a
// per-handler copy would be a second place to forget.

/// Lifetime of the cookie handed out after a successful password check.
const LINK_SESSION_TTL_SECS: u64 = 30 * 60;

/// The link handed to whoever will open it, so it has to be the address they
/// can actually reach, not the one this process happens to be bound to.
///
/// `public_base_url` carries the deployment's declared origin when it has
/// one. Without it this falls back to `https://{app_hosts[0]}`, which is what
/// this function used to do unconditionally: an origin with the port dropped
/// and the scheme assumed. That is right for the reverse-proxied deployment
/// this is designed for and wrong for every other one, and nothing in the
/// dialog that shows the link says which it got.
fn public_link_url(state: &AppState, token: &str) -> String {
    match &state.cfg.public_base_url {
        Some(base) => format!("{base}/s/{token}"),
        None => {
            let host = state.cfg.app_hosts.first().cloned().unwrap_or_default();
            format!("https://{host}/s/{token}")
        }
    }
}

/// Stateless link-session value: `{expiry}.{HMAC(csrf_key, token|expiry)}`.
/// Nothing is stored server-side — the same trick `Csrf` uses — so a link
/// session costs no memory and cannot be enumerated.
fn link_cookie_value(csrf_key: &[u8; 32], token: &str, exp: u64) -> String {
    use hmac::{Hmac, Mac};
    let mut mac = Hmac::<sha2::Sha256>::new_from_slice(csrf_key).expect("HMAC accepts any key length");
    mac.update(token.as_bytes());
    mac.update(b".");
    mac.update(exp.to_string().as_bytes());
    format!("{exp}.{}", data_encoding::HEXLOWER.encode(&mac.finalize().into_bytes()))
}

fn link_cookie_valid(csrf_key: &[u8; 32], token: &str, raw: &str) -> bool {
    let Some((exp_str, _)) = raw.split_once('.') else { return false };
    let Ok(exp) = exp_str.parse::<u64>() else { return false };
    if exp < now_unix() {
        return false;
    }
    let expected = link_cookie_value(csrf_key, token, exp);
    use subtle::ConstantTimeEq;
    raw.as_bytes().ct_eq(expected.as_bytes()).unwrap_u8() == 1
}

fn now_unix() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn cookie_named<'a>(headers: &'a axum::http::HeaderMap, name: &str) -> Option<&'a str> {
    let raw = headers.get(axum::http::header::COOKIE)?.to_str().ok()?;
    raw.split(';').map(|p| p.trim()).find_map(|p| {
        let (k, v) = p.split_once('=')?;
        (k == name).then_some(v)
    })
}

const LINK_COOKIE: &str = "__Host-sc_link";

/// True when this request already cleared the link's password (or the link has
/// none).
fn link_authorized(state: &AppState, headers: &axum::http::HeaderMap, token: &str, has_password: bool) -> bool {
    if !has_password {
        return true;
    }
    cookie_named(headers, LINK_COOKIE)
        .map(|raw| link_cookie_valid(&state.csrf_key, token, raw))
        .unwrap_or(false)
}

/// `share.link_accessed` ("every access is
/// audit-logged as `share.link_accessed` (IP, UA, success)").
///
/// The **token is never logged** — an audit trail that records live
/// credentials turns log access into link access. The link id identifies the
/// row just as well and is useless on its own.
fn audit_link(req_headers: &axum::http::HeaderMap, ip: Option<std::net::IpAddr>, action: &str, id: Option<i64>, ok: bool) {
    let ua = req_headers
        .get(axum::http::header::USER_AGENT)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    tracing::info!(
        target: "sc_http::audit",
        event = "share.link_accessed",
        %action,
        link = id.unwrap_or(-1),
        ip = %ip.map(|i| i.to_string()).unwrap_or_default(),
        %ua,
        %ok,
        "public share link access"
    );
}

fn client_ip_ext(req: &axum::extract::Request) -> Option<std::net::IpAddr> {
    req.extensions().get::<ClientIp>().map(|c| c.0)
}

/// Does this request's `Accept` header prefer an HTML document over JSON?
///
/// This endpoint has exactly two audiences and no others: a browser
/// navigating here fresh (a bookmark, a pasted link, a reload) sends its
/// default document `Accept`, which always lists `text/html` — Chromium,
/// Firefox and Safari all put it first. The public share page's own JS
/// (`web/src/lib/api/share.ts`'s `fetch`) sends no override, which per the
/// Fetch spec defaults to `*/*`; `curl`, tooling and every existing test in
/// this file that doesn't set `Accept` at all get the same `None`. Either of
/// those — no explicit preference, or an explicit `application/json` — must
/// keep getting the JSON body this endpoint has always returned, so only an
/// `Accept` that names `text/html` *before* it names `application/json`
/// switches the answer. That is why this is a `find`-position comparison
/// rather than a full RFC 9110 q-value parse: the two-way choice this
/// endpoint ever has to make doesn't need one.
fn wants_html(headers: &axum::http::HeaderMap) -> bool {
    let Some(accept) = headers.get(axum::http::header::ACCEPT).and_then(|v| v.to_str().ok()) else {
        return false;
    };
    match (accept.find("text/html"), accept.find("application/json")) {
        (Some(html), Some(json)) => html < json,
        (Some(_), None) => true,
        (None, _) => false,
    }
}

/// Metadata for the public page.
///
/// Never carries the host path, the owner, the virtual path, or any hint that
/// other links exist on the same file. A password-protected link answers with
/// nothing but `{"protected": true}` until the password is cleared.
async fn public_link_get(
    State(state): State<AppState>,
    Path(token): Path<String>,
    req: axum::extract::Request,
) -> Response {
    // `/s/{token}` is carved out of `routes::admin_catch_all`'s SPA fallback
    // on purpose (`OWN_RESERVED_PREFIXES`) so a client parsing this token's
    // JSON never has a `404`/`410` silently turn into an HTML document out
    // from under it. But that means *this* handler is the only place left
    // that can hand a fresh browser navigation anything to render at all —
    // the fallback will never see this path. Serve the same embedded SPA
    // document `admin_catch_all` would have ('s
    // separate, lightweight `/s/[token]` bundle; the code-splitting that
    // keeps it small is a build-time property of that bundle, not something
    // this handler needs to know about) — it then calls this very endpoint
    // again from JS to get the JSON below.
    if wants_html(req.headers()) {
        #[cfg(feature = "embed-ui")]
        {
            let path = req.uri().path();
            if let Some(resp) = crate::embed::serve(path, req.headers(), req.method()) {
                return resp;
            }
        }
    }

    let ip = client_ip_ext(&req);
    let Some(id) = (match state.core.share_link_lookup(&token) {
        Ok(v) => v,
        Err(e) => return AppError::from(e).into_response(),
    }) else {
        audit_link(req.headers(), ip, "view", None, false);
        return AppError::not_found().into_response();
    };

    let link = match state.core.share_link_public(id) {
        Ok(l) => l,
        Err(e) => {
            audit_link(req.headers(), ip, "view", Some(id), false);
            return AppError::from(e).into_response();
        }
    };

    if !link_authorized(&state, req.headers(), &token, link.has_password) {
        audit_link(req.headers(), ip, "view.locked", Some(id), false);
        return Json(serde_json::json!({ "protected": true })).into_response();
    }
    audit_link(req.headers(), ip, "view", Some(id), true);

    let mut body = serde_json::json!({
        "protected": link.has_password,
        "name": link.name,
        "is_dir": link.is_dir,
        "size": link.size,
        "mtime_ns": link.mtime_ns.to_string(),
        "label": link.label,
        "drop": link.is_drop,
        "can_download": link.can_download,
    });

    // The uploader has to learn its own ceiling from somewhere, and this page
    // deliberately never imports the app bundle that could ask
    // `/api/capabilities`. Without this it discovers `body_limit_bytes` by
    // uploading a file too large and getting a 413 at the end of the transfer.
    // Only on drop links: nothing else on this page can upload.
    if link.is_drop {
        body["max_upload_bytes"] = serde_json::json!(state.cfg.body_limit_bytes);
    }

    // A file-drop link lists nothing — that is what makes it a drop box and
    // not a shared folder (§7.2).
    if link.is_dir && !link.is_drop {
        match state.core.share_link_entries(id) {
            Ok(entries) => body["entries"] = serde_json::to_value(entries).unwrap_or_default(),
            Err(e) => return AppError::from(e).into_response(),
        }
    }
    Json(body).into_response()
}

#[derive(Deserialize)]
struct LinkAuthReq {
    #[serde(default)]
    password: String,
}

/// Password check.
///
/// Three things here are the security contract, not implementation detail:
///
/// * the bucket is keyed by **token**, not by IP (§7.2, 10/hour);
/// * an unknown token still runs a full Argon2 verify inside
///   `share_link_check_password` (the `-1` sentinel), so the timing of "no
///   such link" matches "wrong password";
/// * both failures return the **same** `404` envelope. Answering "that link
///   exists, but wrong password" here would confirm a guessed token.
async fn public_link_auth(
    State(state): State<AppState>,
    Path(token): Path<String>,
    req: axum::extract::Request,
) -> Response {
    let ip = client_ip_ext(&req);
    let (parts, raw_body) = req.into_parts();
    let headers = parts.headers;
    let bytes = match axum::body::to_bytes(raw_body, 64 * 1024).await {
        Ok(b) => b,
        Err(_) => return AppError::not_found().into_response(),
    };
    // A malformed body is answered exactly like a wrong password, for the
    // same reason: nothing about this endpoint may vary with what the caller
    // guessed.
    let body: LinkAuthReq = serde_json::from_slice(&bytes).unwrap_or(LinkAuthReq { password: String::new() });

    if let Some(retry) = state.link_rate.check(&token) {
        let mut resp = AppError::rate_limited(retry).into_response();
        if let Ok(v) = axum::http::HeaderValue::from_str(&retry.to_string()) {
            resp.headers_mut().insert("Retry-After", v);
        }
        return resp;
    }

    let id = match state.core.share_link_lookup(&token) {
        Ok(v) => v.unwrap_or(-1),
        Err(e) => return AppError::from(e).into_response(),
    };

    let core = state.core.clone();
    let candidate = body.password.clone();
    let ok = tokio::task::spawn_blocking(move || core.share_link_check_password(id, &candidate))
        .await
        .unwrap_or(Ok(false))
        .unwrap_or(false);

    audit_link(&headers, ip, "auth", (id >= 0).then_some(id), ok);
    if !ok {
        return AppError::not_found().into_response();
    }

    let exp = now_unix() + LINK_SESSION_TTL_SECS;
    let value = link_cookie_value(&state.csrf_key, &token, exp);
    let cookie = format!(
        "{LINK_COOKIE}={value}; Path=/s/{token}; Max-Age={LINK_SESSION_TTL_SECS}; Secure; HttpOnly; SameSite=Lax"
    );
    let mut resp = Json(serde_json::json!({ "ok": true })).into_response();
    if let Ok(v) = axum::http::HeaderValue::from_str(&cookie) {
        resp.headers_mut().insert(axum::http::header::SET_COOKIE, v);
    }
    resp
}

/// Mint the signed content URL for a link's target.
///
/// `sub = 0` marks the claim as issued to nobody in particular
/// (`content::Claim::sub`, "0 = public link") — the bytes still leave through
/// the same signed-URL machinery every authenticated download uses, so the
/// content origin needs no share-link awareness at all.
async fn public_link_download(
    State(state): State<AppState>,
    Path(token): Path<String>,
    req: axum::extract::Request,
) -> Response {
    let ip = client_ip_ext(&req);
    let Some(id) = (match state.core.share_link_lookup(&token) {
        Ok(v) => v,
        Err(e) => return AppError::from(e).into_response(),
    }) else {
        audit_link(req.headers(), ip, "download", None, false);
        return AppError::not_found().into_response();
    };
    let link = match state.core.share_link_public(id) {
        Ok(l) => l,
        Err(e) => {
            audit_link(req.headers(), ip, "download", Some(id), false);
            return AppError::from(e).into_response();
        }
    };
    if !link_authorized(&state, req.headers(), &token, link.has_password) {
        audit_link(req.headers(), ip, "download", Some(id), false);
        return AppError::acl_denied("link.password").into_response();
    }
    if !link.can_download || link.is_drop {
        audit_link(req.headers(), ip, "download", Some(id), false);
        return AppError::acl_denied("link.perms").into_response();
    }
    let Some(fid) = link.fid else {
        audit_link(req.headers(), ip, "download", Some(id), false);
        return AppError::gone().into_response();
    };

    // Count first. A stream that dies later does not give the download back
    // (§7.2: abuse prevention beats exact accounting).
    if let Err(e) = state.core.share_link_note_download(id) {
        audit_link(req.headers(), ip, "download", Some(id), false);
        return AppError::from(e).into_response();
    }
    audit_link(req.headers(), ip, "download", Some(id), true);

    let claim = content::make_claim(fid, link.etag8, Disposition::Attachment, None, 0, None);
    let signed = content::sign(&state.signed_url_keys.lock(), claim);
    // See `content::content_url`'s doc comment: the naive
    // `format!("https://{host}/c/{token}")` with an empty `content_hosts`
    // produces `https:///c/<token>`, which browsers parse as host `c`.
    let url = content::content_url(state.cfg.content_hosts.first().map(String::as_str), &signed);
    Json(serde_json::json!({ "url": url })).into_response()
}

#[derive(Deserialize)]
struct DropQuery {
    name: String,
}

/// Did this body read fail because the limit layer cut it off? The cause is
/// wrapped several times over by the time it reaches a handler, so this walks
/// the chain rather than downcasting the outermost error.
fn is_length_limit(err: &axum::Error) -> bool {
    let mut cur: Option<&(dyn std::error::Error + 'static)> = Some(err);
    while let Some(e) = cur {
        if e.is::<http_body_util::LengthLimitError>() {
            return true;
        }
        cur = e.source();
    }
    false
}

/// Upload through a file-drop link. Never overwrites — a colliding name is
/// renamed by the core.
async fn public_link_drop(
    State(state): State<AppState>,
    Path(token): Path<String>,
    Query(q): Query<DropQuery>,
    req: axum::extract::Request,
) -> Response {
    // Taken before `req.into_body()` consumes the request. This is the one
    // public link action that *writes*, and it was the only one not audited
    // (`view`/`auth`/`download` all were), so an anonymous upload left no
    // trace at all — asks for the opposite.
    let ip = client_ip_ext(&req);
    let headers = req.headers().clone();

    let Some(id) = (match state.core.share_link_lookup(&token) {
        Ok(v) => v,
        Err(e) => return AppError::from(e).into_response(),
    }) else {
        audit_link(&headers, ip, "drop", None, false);
        return AppError::not_found().into_response();
    };
    let link = match state.core.share_link_public(id) {
        Ok(l) => l,
        Err(e) => {
            audit_link(&headers, ip, "drop", Some(id), false);
            return AppError::from(e).into_response();
        }
    };
    if !link_authorized(&state, &headers, &token, link.has_password) {
        audit_link(&headers, ip, "drop.locked", Some(id), false);
        return AppError::acl_denied("link.password").into_response();
    }
    if !link.is_drop {
        audit_link(&headers, ip, "drop", Some(id), false);
        return AppError::acl_denied("link.perms").into_response();
    }

    // `usize::MAX` here is not "unbounded": `/s/**` is part of
    // `protected_routes`, which `lib.rs::build_router` wraps in
    // `RequestBodyLimitLayer::new(cfg.body_limit_bytes)`. A body over that
    // limit never reaches this line.
    let body = match axum::body::to_bytes(req.into_body(), usize::MAX).await {
        Ok(b) => b,
        Err(e) => {
            audit_link(&headers, ip, "drop", Some(id), false);
            // The limit layer answers `413` itself when `Content-Length`
            // already says the body is too big. Without that header it can
            // only cut the stream mid-body, and that arrives here as a read
            // failure — same cause, so the same status rather than a
            // `422 fs.invalid_name` that blames the filename.
            // `error_mapper` puts the `fs.body_too_large` envelope on it.
            return if is_length_limit(&e) {
                StatusCode::PAYLOAD_TOO_LARGE.into_response()
            } else {
                AppError::invalid_name("unreadable body").into_response()
            };
        }
    };
    match state.core.share_link_drop(id, &q.name, &body) {
        // The uploader is told the stored name (which may have been changed
        // to avoid a collision) and nothing else about the directory.
        Ok(entry) => {
            audit_link(&headers, ip, "drop", Some(id), true);
            (StatusCode::CREATED, Json(serde_json::json!({ "name": entry.name }))).into_response()
        }
        Err(e) => {
            audit_link(&headers, ip, "drop", Some(id), false);
            AppError::from(e).into_response()
        }
    }
}

// ----------------------------------------------------------------- search --
// T2 (the parallel walker) is the first-class path;
// `state.search` is `sc-server`'s binding of it to live `ShareRoot`s.

#[derive(Deserialize)]
struct SearchQuery {
    #[serde(default)]
    q: String,
    scope: Option<String>,
    kind: Option<String>,
}

impl SearchQuery {
    fn into_api(self) -> crate::search_api::SearchQuery {
        crate::search_api::SearchQuery { text: self.q, scope: self.scope, kind: self.kind, ..Default::default() }
    }
}

fn hit_json(h: &crate::search_api::SearchHit) -> serde_json::Value {
    serde_json::json!({
        "path": h.path,
        "name": h.name,
        "is_dir": h.is_dir,
        "size": h.size,
        "mtime_ns": h.mtime_ns.map(|v| v.to_string()),
        "score": h.score,
    })
}

fn completeness_json(c: &crate::search_api::SearchCompleteness) -> serde_json::Value {
    match c {
        crate::search_api::SearchCompleteness::Full => serde_json::json!({ "state": "full" }),
        crate::search_api::SearchCompleteness::Truncated { reason, seen, elapsed_ms } => {
            serde_json::json!({ "state": "truncated", "reason": reason, "seen": seen, "elapsed_ms": elapsed_ms })
        }
    }
}

/// `None` if the caller may proceed, `Some(response)` if it was rejected by
/// the per-user rate limit (30/min).
fn check_search_rate(state: &AppState, user_key: &str) -> Option<Response> {
    let retry = state.search_rate.check(user_key)?;
    let mut resp = AppError::rate_limited(retry).into_response();
    if let Ok(v) = axum::http::HeaderValue::from_str(&retry.to_string()) {
        resp.headers_mut().insert("Retry-After", v);
    }
    Some(resp)
}

/// `429` for an exhausted concurrent-search budget. The hint is the tier's own
/// walk deadline — a slot in the slow (HDD) budget takes longer to free up
/// than one in the fast budget, so the retry hint should say so.
fn search_rate_limited_response(state: &AppState, tier: crate::search_limits::SearchTier) -> Response {
    let retry_after_s = state.search_concurrency.walk_deadline(tier).as_secs().max(1);
    let mut resp = AppError::rate_limited(retry_after_s as u32).into_response();
    if let Ok(v) = axum::http::HeaderValue::from_str(&retry_after_s.to_string()) {
        resp.headers_mut().insert("Retry-After", v);
    }
    resp
}

async fn search(State(state): State<AppState>, principal: Option<Extension<Principal>>, Query(q): Query<SearchQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if q.q.trim().is_empty() {
        return Json(serde_json::json!({ "hits": [], "completeness": { "state": "full" } })).into_response();
    }
    let user_key = principal.user.get().to_string();
    if let Some(resp) = check_search_rate(&state, &user_key) {
        return resp;
    }
    let query = q.into_api();
    let user = principal.user;
    // Global concurrency cap, split per storage-class
    // tier — T2 is I/O-heavy enough that unbounded concurrent walks would
    // starve other services sharing the disk, and an HDD-bound walk needs a
    // tighter cap than an NVMe-bound one. `search_tier` is cheap (no tree
    // walk) so it's safe to call before acquiring anything.
    let tier = state.search.search_tier(user, &query);
    let permit = match state.search_concurrency.try_acquire(tier) {
        Some(p) => p,
        None => return search_rate_limited_response(&state, tier),
    };

    let search = state.search.clone();
    let result = tokio::task::spawn_blocking(move || {
        let _permit = permit;
        search.search(user, &query)
    })
    .await;

    match result {
        Ok(Ok(outcome)) => Json(serde_json::json!({
            "hits": outcome.hits.iter().map(hit_json).collect::<Vec<_>>(),
            "completeness": completeness_json(&outcome.completeness),
        }))
        .into_response(),
        Ok(Err(e)) => AppError::from(e).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

async fn search_stream(State(state): State<AppState>, principal: Option<Extension<Principal>>, Query(q): Query<SearchQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let user_key = principal.user.get().to_string();
    if let Some(resp) = check_search_rate(&state, &user_key) {
        return resp;
    }
    let query = q.into_api();
    let user = principal.user;
    let tier = state.search.search_tier(user, &query);
    let permit = match state.search_concurrency.try_acquire(tier) {
        Some(p) => p,
        None => return search_rate_limited_response(&state, tier),
    };

    let (tx, rx) = tokio::sync::mpsc::unbounded_channel::<Result<axum::response::sse::Event, std::convert::Infallible>>();
    let search = state.search.clone();

    tokio::task::spawn_blocking(move || {
        let _permit = permit;
        let tx_hits = tx.clone();
        let completeness = search.search_stream(user, &query, &mut |hit| {
            let ev = axum::response::sse::Event::default()
                .event("hit")
                .json_data(hit_json(&hit))
                .unwrap_or_else(|_| axum::response::sse::Event::default().event("hit"));
            tx_hits.send(Ok(ev)).is_ok()
        });
        let done = axum::response::sse::Event::default()
            .event("done")
            .json_data(completeness_json(&completeness))
            .unwrap_or_else(|_| axum::response::sse::Event::default().event("done"));
        let _ = tx.send(Ok(done));
    });

    axum::response::sse::Sse::new(crate::content::ReceiverStream(rx)).into_response()
}

// ------------------------------------------------------------------- jobs --

/// `GET /api/jobs` — every non-terminal job the caller owns, owner-scoped
/// the same way `job_status`/`job_cancel` are (`JobStore::list_open`).
/// `JobTray` calls this once on mount so a browser refresh (or a job started
/// in a different tab) re-attaches to whatever `jobs.db` already has, rather
/// than losing track of it client-side.
async fn job_list(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    let jobs: Vec<serde_json::Value> = state.jobs.list_open(principal.user).iter().map(JobStatus::done_total_json).collect();
    Json(serde_json::json!({ "jobs": jobs })).into_response()
}

async fn job_status(State(state): State<AppState>, principal: Option<Extension<Principal>>, Path(id): Path<String>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    // `get_owned` answers `None` for both "no such job" and "not yours" — a
    // job id must not be readable by another account (original task's
    // authorization requirement), so the two cases must be indistinguishable.
    match state.jobs.get_owned(&id, principal.user) {
        Some(job) => Json(job.done_total_json()).into_response(),
        None => AppError::not_found().into_response(),
    }
}

async fn job_cancel(State(state): State<AppState>, principal: Option<Extension<Principal>>, Path(id): Path<String>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if state.jobs.cancel_owned(&id, principal.user) {
        StatusCode::NO_CONTENT.into_response()
    } else {
        AppError::not_found().into_response()
    }
}

/// `GET /api/jobs/{id}/download` — one-shot fetch of a finished archive
/// job's zip bytes (`JobStore::take_artifact`: owner-checked, and clears the
/// buffer so a repeat request 404s instead of serving stale bytes forever).
async fn job_download(State(state): State<AppState>, principal: Option<Extension<Principal>>, Path(id): Path<String>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    match state.jobs.take_artifact(&id, principal.user) {
        Some(bytes) => {
            let mut resp = axum::body::Body::from((*bytes).clone()).into_response();
            let h = resp.headers_mut();
            h.insert(axum::http::header::CONTENT_TYPE, axum::http::HeaderValue::from_static("application/zip"));
            if let Ok(v) = axum::http::HeaderValue::from_str(&content::content_disposition_value("attachment", "archive.zip")) {
                h.insert(axum::http::header::CONTENT_DISPOSITION, v);
            }
            resp
        }
        None => AppError::not_found().into_response(),
    }
}

// ----------------------------------------------------------------- events --

async fn events_ws(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    session: Option<Extension<SessionToken>>,
    ws: WebSocketUpgrade,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    // `None` for Bearer/app-password auth — `revoke_session` only ever
    // targets a cookie session, so those sockets simply never match one.
    let session_hash = session.map(|Extension(SessionToken(t))| sc_auth::token_hash_hex(&t));
    ws.on_upgrade(move |socket| handle_socket(socket, state, principal, session_hash))
}

async fn handle_socket(mut socket: WebSocket, state: AppState, principal: Principal, session_hash: Option<String>) {
    let (conn_id, mut rx) = state.ws.connect_with_session(principal.user, session_hash);
    loop {
        tokio::select! {
            incoming = socket.recv() => {
                match incoming {
                    Some(Ok(Message::Text(text))) => {
                        if let Ok(msg) = serde_json::from_str::<crate::ws::ClientMsg>(&text) {
                            state.ws.handle_client_msg(conn_id, msg);
                        }
                    }
                    Some(Ok(Message::Close(_))) | None => break,
                    Some(Err(_)) => break,
                    _ => {}
                }
            }
            outgoing = rx.recv() => {
                match outgoing {
                    Some(msg) => {
                        let text = serde_json::to_string(&msg).unwrap_or_default();
                        if socket.send(Message::Text(text.into())).await.is_err() {
                            break;
                        }
                    }
                    None => break,
                }
            }
        }
    }
    state.ws.disconnect(conn_id);
}

// ------------------------------------------------------------------ admin --

async fn admin_storage(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    // `/admin/*` not loading in the SPA for a non-admin (
    // §7) is a UI nicety, not access control — the route itself has to
    // refuse, or a non-admin curling the API directly would still get the
    // DB/share storage breakdown.
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.core.storage_report() {
        Ok(report) => Json(report).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn admin_index_estimate(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    // Sampling the corpus walks real directories, so it must not run on the
    // async reactor.
    let core = state.core.clone();
    match tokio::task::spawn_blocking(move || core.index_estimate()).await {
        Ok(Ok(est)) => Json(est).into_response(),
        Ok(Err(e)) => AppError::from(e).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

/// `GET /api/admin/index/settings` — whether the T3
/// name index is currently switched on. The other half of `admin_index_
/// estimate`'s "here's what it would cost": this is "here's whether it's
/// running", so `StorageIndexSection.svelte` can render a toggle instead of
/// only the estimate.
async fn admin_index_settings(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.core.index_settings() {
        Ok(s) => Json(s).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct IndexSettingsReq {
    name_enabled: bool,
}

/// `PATCH /api/admin/index/settings` — flips the persisted override
/// (`sc_search::IndexSettingsStore`, not a `config.toml` rewrite; see that
/// module's doc for why). Runs on a blocking thread: it's a SQLite write,
/// same reasoning as `admin_set_upload_settings`'s neighbor `sc-upload`
/// table.
async fn admin_set_index_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<IndexSettingsReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let core = state.core.clone();
    match tokio::task::spawn_blocking(move || core.set_index_name_enabled(req.name_enabled)).await {
        Ok(Ok(s)) => Json(s).into_response(),
        Ok(Err(e)) => AppError::from(e).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

/// `POST /api/admin/index/build` — crawls every share
/// and (re)builds its name index, through the same `JobKind::IndexBuild`
/// queue `/api/jobs/{id}` already polls/cancels, rather than a second
/// progress mechanism. `501`s if the index is off — starting a build for a
/// feature the admin has not enabled would plant `.scindex/` directories the
/// "off by default" invariant promises won't
/// exist.
async fn admin_build_index(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.core.index_settings() {
        Ok(s) if !s.name_enabled => return AppError::not_implemented().into_response(),
        Ok(_) => {}
        Err(e) => return AppError::from(e).into_response(),
    }
    match spawn_index_build_job(&state, principal.user) {
        Some(id) => (StatusCode::ACCEPTED, Json(serde_json::json!({ "job": id }))).into_response(),
        None => AppError::internal().into_response(),
    }
}

/// `total` starts at `0` (indeterminate): a build's item count isn't known
/// until each share's crawl finishes, and `JobTray`/`ProgressLinear` already
/// render `total == 0` as an indeterminate bar rather than "0/0 done" —
/// `spawn_archive_job`'s doc has the same shape for a different reason.
/// Returns `None` if the parent job row could not be persisted
/// (`JobStore::insert`'s doc).
fn spawn_index_build_job(state: &AppState, user: UserId) -> Option<String> {
    let id = new_job_id();
    if !state.jobs.insert(JobStatus::new_running(id.clone(), user, JobKind::IndexBuild, 0)) {
        return None;
    }

    let jobs = state.jobs.clone();
    let ws = state.ws.clone();
    let core = state.core.clone();
    let job_id = id.clone();
    tokio::task::spawn_blocking(move || {
        let progress_jobs = jobs.clone();
        let progress_ws = ws.clone();
        let progress_id = job_id.clone();
        let on_progress = move |visited: u64, current: Option<String>| {
            progress_jobs.set_progress(&progress_id, visited, current);
            // Same reasoning as `spawn_archive_job`'s `done % 32` push: a
            // per-batch WS message already matches `CrawlThrottle`'s own
            // pacing boundary, so this doesn't add extra chatter beyond it.
            progress_ws.send_job_to_user(user, &progress_id, visited, 0);
        };
        let cancel_jobs = jobs.clone();
        let cancel_id = job_id.clone();
        let should_cancel = move || cancel_jobs.is_cancelled(&cancel_id);

        match core.build_name_indexes(&on_progress, &should_cancel) {
            Ok(results) => {
                let final_state = if jobs.is_cancelled(&job_id) {
                    JobState::Cancelled
                } else if results.iter().all(|r| r.ok) {
                    JobState::Done
                } else {
                    JobState::Error
                };
                jobs.finish_index_build(&job_id, final_state, &results);
            }
            Err(e) => {
                tracing::warn!(error = %e, "admin-triggered index build failed to start");
                jobs.finish_index_build(&job_id, JobState::Error, &[]);
            }
        }
    });
    Some(id)
}

#[derive(Deserialize)]
struct UploadSettingsReq {
    chunk_min: u64,
    chunk_default: u64,
}

#[derive(Serialize)]
struct UploadSettingsResp {
    chunk_min: u64,
    chunk_default: u64,
}

/// `PATCH /api/admin/upload-settings` — the write half of the chunk floor/
/// default pair `capabilities`/`auth_session` read. Server-global and
/// persisted: every client sees the new value on
/// its next `GET /api/auth/session`, and it survives a restart. Does not
/// affect any upload already in progress — see
/// `sc_upload::UploadEngine::validate_patch`'s doc for why an in-flight
/// session must keep enforcing the floor it started under.
async fn admin_set_upload_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<UploadSettingsReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.uploads.set_chunk_limits(req.chunk_min, req.chunk_default) {
        Ok(()) => Json(UploadSettingsResp { chunk_min: req.chunk_min, chunk_default: req.chunk_default }).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

// ------------------------------------------------- admin: server settings --
// Every operator-settable `config.toml` field the settings screen covers —
// live-apply where possible, restart-required where not
// (`crate::settings_api::SettingsApi`). This file is the wire skin; real
// enforcement (persistence in `settings.db`, live reconfiguration,
// `smb.conf` regeneration) lives in `sc-server`'s `SettingsBridge`, same
// split as `admin_set_upload_settings` above and `sc_upload`.

/// `GET /api/admin/server-settings` — every field this screen covers,
/// tagged with its source (built-in default / `config.toml` / admin
/// override) and whether changing it needs a restart. A field this screen
/// cannot safely let an admin change is still listed, read-only, with why —
/// never silently hidden.
async fn admin_get_server_settings(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    Json(state.settings.snapshot()).into_response()
}

/// `PATCH /api/admin/server-settings/smb` — `smb.enabled` and enough of
/// `SmbConfig` to actually make SMB usable. Everything except `enabled`
/// itself takes effect immediately (`SettingsBridge::set_smb` calls
/// `sc_server::smb_cmd::render_live`); `enabled` needs a restart because
/// `CoreBridge` bakes it in at boot.
async fn admin_set_smb_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::SmbPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_smb(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/search` — fully live ('s concurrency caps, walk deadlines, per-user rate limit).
async fn admin_set_search_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::SearchPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_search(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/archive` — fully live, resizes the same
/// semaphore `POST /api/fs/archive` acquires from.
async fn admin_set_archive_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::ArchivePatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_archive(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/network` — `bind`, `app_hosts`,
/// `content_hosts`, `allowed_origins`, `trusted_proxies`,
/// `compat_canonical_url`. Always restart-required: the listener is already
/// bound and the CSRF/Host allowlists are baked into `HttpConfig` at boot.
async fn admin_set_network_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::NetworkPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_network(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/db` — the DB size-guard trio
/// Restart-required: `Diagnostics` reads these at
/// boot.
async fn admin_set_db_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::DbPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_db(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/symlink-policy` — restart-required:
/// `sc_vfs::SymlinkPolicy` is read once when the VFS layer is built.
async fn admin_set_symlink_policy_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::SymlinkPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_symlink_policy(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/homes` — restart-required: per-user
/// homes are attached once at `App::build` time (`Core::attach_homes`).
async fn admin_set_homes_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::HomesPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_homes(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/watch` — restart-required: the watcher
/// backend and its hot-set/full-invalidation limits are baked into
/// `sc_watch::Watcher` when `App::build` constructs it.
async fn admin_set_watch_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::WatchPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_watch(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/oidc` -- restart-required: the relying
/// party, its TLS client and its discovery/JWKS caches are assembled once in
/// `App::build`.
///
/// Only the seven keys `sc_http::settings_api::OidcPatch` carries.
/// `oidc.client_secret_file` and `oidc.local_password_login` are absent from
/// this route on purpose and cannot be set from any screen -- see that type's
/// doc comment for the lockout that omission prevents.
async fn admin_set_oidc_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::OidcPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_oidc(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `PATCH /api/admin/server-settings/paths` — restart-required, and the only
/// settings group whose *rejection* matters more than its application: a
/// `data_dir` that doesn't already hold the databases, or a `master_key_file`
/// that isn't the current key, would leave the server unable to authenticate
/// anyone after the restart. The bridge refuses those outright (422) rather
/// than persisting them; see `SettingsBridge::set_paths`.
async fn admin_set_paths_settings(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::PathsPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.settings.set_paths(req) {
        Ok(o) => Json(o).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `POST /api/admin/server-settings/restart` — the explicit, admin-triggered
/// counterpart to a restart-required patch above. Notifies `cmd_serve`'s
/// `tokio::select!` (`AppState::restart_signal`), which runs the same
/// graceful-shutdown sequence a signal-triggered stop does before exiting
/// with a code `systemd`'s `Restart=on-failure` picks up
/// (`sc-server/src/lib.rs::cmd_serve`). Accepted, not completed, by the time
/// this responds — the connection serving this very request is one of the
/// things about to be drained.
///
/// Refuses (`409 restart.busy`) when uploads or jobs are in flight and the
/// caller didn't set `force: true` — `run_shutdown_sequence` drains upload
/// sessions to a clean resume point, but a TUS client mid-transfer still
/// sees its connection drop, and a running copy/move/delete/archive job is
/// stopped where it stands. Not lost: `JobStore::open` reclassifies it as
/// `interrupted` on the next start and its per-item rows survive, so the
/// tray can say what did and didn't happen. It is still never resumed. The
/// settings screen must show these counts and get an explicit second
/// confirmation before setting `force`, never restart silently out from
/// under active work.
async fn admin_restart_server(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::settings_api::RestartPatch>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let active_uploads = state.uploads.active_count();
    let running_jobs = state.jobs.running_count();
    if !req.force && (active_uploads > 0 || running_jobs > 0) {
        return AppError::restart_busy(active_uploads, running_jobs).into_response();
    }
    match state.settings.request_restart() {
        Ok(()) => StatusCode::ACCEPTED.into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

// -------------------------------------------------------- admin: users --
// User management ('s "admin API — users"). Every handler
// here is admin-gated the same way `admin_storage`/`admin_index_estimate`
// are: `require_admin` re-checks the account fresh, because a hidden button
// in the SPA is not access control.

/// The account row shape every admin-users endpoint returns. Deliberately
/// never includes `pw_hash`, `totp_secret`, or anything from
/// `user_smb_secret` — those live behind `sc_auth::UserRow`'s own field
/// list, which this simply doesn't forward, rather than an explicit
/// exclusion that could rot.
#[derive(Serialize)]
struct AdminUserWire {
    id: u32,
    name: String,
    display_name: String,
    is_admin: bool,
    disabled: bool,
    totp_enabled: bool,
    smb_enabled: bool,
    // A JS number loses precision above 2^53; every other nanosecond
    // timestamp this crate serializes goes out as a string for the same
    // reason (`created_ns`/`last_used_ns` elsewhere in this file all call
    // `.to_string()`) — matched here rather than left as a native `i64`.
    created_ns: String,
    // As a string for the same 2^53 reason. `None` = unlimited.
    quota_bytes: Option<String>,
    /// Running usage ledger (`user.usage_bytes`) — as a
    /// string for the same 2^53 reason as every other byte count here.
    usage_bytes: String,
}

impl From<sc_auth::UserRow> for AdminUserWire {
    fn from(u: sc_auth::UserRow) -> Self {
        AdminUserWire {
            id: u.id.get(),
            display_name: u.display.clone().unwrap_or_else(|| u.name.clone()),
            name: u.name,
            is_admin: u.is_admin,
            disabled: u.disabled,
            totp_enabled: u.totp_enabled,
            smb_enabled: u.smb_enabled,
            created_ns: u.created_ns.to_string(),
            quota_bytes: u.quota_bytes.map(|v| v.to_string()),
            usage_bytes: u.usage_bytes.to_string(),
        }
    }
}

async fn admin_list_users(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.auth.list_users() {
        Ok(users) => Json(users.into_iter().map(AdminUserWire::from).collect::<Vec<_>>()).into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

#[derive(Deserialize)]
struct AdminCreateUserReq {
    name: String,
    password: String,
}

fn create_user_error_response(e: sc_auth::CreateUserError) -> AppError {
    use sc_auth::CreateUserError as E;
    match e {
        // Same code/shape as `setup.weak_password`/`POST /api/auth/password`
        // (`ErrorCode::AuthWeakPassword`) — one "need N characters" message
        // the frontend already knows how to render, reused rather than
        // inventing a fourth spelling of it.
        E::TooShort { min } => AppError::new(ErrorCode::AuthWeakPassword, "password is too short")
            .with_detail(serde_json::json!({ "min_length": min })),
        E::DuplicateName => AppError::new(ErrorCode::FsConflict, "a user with that name already exists"),
        E::Internal(_) => AppError::internal(),
    }
}

/// `POST /api/admin/users` — admin-only account creation. Reuses
/// `sc_auth::AuthService::create_user` verbatim: minimum-length enforcement
/// and the unconditional NT-hash derivation (so SMB
/// can be switched on for this account later without a password reset) both
/// happen there, not here. The created account is never an administrator —
/// only `sc-server::setup`'s first-run bootstrap grants that role.
async fn admin_create_user(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<AdminCreateUserReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let password = SecretString::from(req.password);
    match state.auth.create_user(&req.name, &password) {
        Ok(id) => match state.auth.find_user_by_id(id) {
            Ok(Some(row)) => (StatusCode::CREATED, Json(AdminUserWire::from(row))).into_response(),
            _ => AppError::internal().into_response(),
        },
        Err(e) => create_user_error_response(e).into_response(),
    }
}

fn admin_guard_error_response(e: sc_auth::AdminGuardError) -> AppError {
    use sc_auth::AdminGuardError as E;
    match e {
        E::NoSuchUser => AppError::not_found(),
        E::LastAdmin => AppError::admin_last_admin(),
        E::Internal(_) => AppError::internal(),
    }
}

fn parse_user_id(raw: &str) -> Result<sc_vfs::UserId, AppError> {
    raw.parse::<u32>().map(sc_vfs::UserId::new).map_err(|_| AppError::not_found())
}

/// `quota_bytes` is a *double* option, same reason `GrantPatchReq::label`
/// (`sc-http/src/core_api.rs`) is: "absent" (leave the quota alone) and
/// "explicitly `null`" (clear it back to unlimited) are different requests.
/// A bare byte count sets a real cap.
#[derive(Deserialize)]
struct AdminUserPatchReq {
    /// "disable/enable". Role changes aren't exposed
    /// over HTTP yet (see `sc_auth::AuthService::set_admin`'s doc comment);
    /// this stays an `Option` rather than a bare `bool` so a future field
    /// can be added beside it without every existing caller having to start
    /// sending it.
    disabled: Option<bool>,
    /// Per-user quota cap in bytes. `Some(None)` clears
    /// it to unlimited; `Some(Some(0))` is rejected the same way (0 reads as
    /// unlimited downstream per `quota_val`'s `$quota > 0` guard, so refusing
    /// it here rather than silently accepting a no-op is the honest answer).
    #[serde(default, deserialize_with = "double_option_u64")]
    quota_bytes: Option<Option<u64>>,
}

fn double_option_u64<'de, D>(d: D) -> Result<Option<Option<u64>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Option::deserialize(d).map(Some)
}

/// `PATCH /api/admin/users/{id}` — today, only the disable/enable toggle.
/// Refuses with `409 admin.last_admin` (via
/// [`sc_auth::AdminGuardError::LastAdmin`]) rather than disabling the
/// deployment's last active administrator out from under itself — see that
/// error variant's docs for why this is unconditional, not just a UI
/// confirmation.
async fn admin_patch_user(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(req): Json<AdminUserPatchReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let uid = match parse_user_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    if let Some(disabled) = req.disabled {
        if let Err(e) = state.auth.disable_user(uid, disabled) {
            return admin_guard_error_response(e).into_response();
        }
        // Disabling ends every session this account has open, not just new
        // logins — the live sockets need to hear about it too.
        if disabled {
            state.ws.revoke_user(uid);
        }
    }
    if let Some(quota) = req.quota_bytes {
        if quota == Some(0) {
            return AppError::invalid_quota().into_response();
        }
        if let Err(e) = state.auth.set_quota(uid, quota) {
            return admin_guard_error_response(e).into_response();
        }
    }
    match state.auth.find_user_by_id(uid) {
        Ok(Some(row)) => Json(AdminUserWire::from(row)).into_response(),
        Ok(None) => AppError::not_found().into_response(),
        Err(_) => AppError::internal().into_response(),
    }
}

/// `DELETE /api/admin/users/{id}` — permanent, irreversible (`sc_auth`'s
/// `delete_user` sweeps sessions, app passwords, recovery codes, TOTP replay
/// records, group membership and the SMB secret in one transaction). The UI
/// is expected to confirm before ever sending this request (the same
/// pattern `DeleteDialog` already uses for file deletes); the server's own
/// backstop against an *accidental* wipeout is the last-admin guard, not a
/// second confirmation step here.
async fn admin_delete_user(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let uid = match parse_user_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.auth.delete_user(uid) {
        Ok(()) => {
            state.ws.revoke_user(uid);
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => admin_guard_error_response(e).into_response(),
    }
}

// -------------------------------------------------------- admin: grants --
// No access until explicitly granted.
// `sc_core::acl_store` already persists `sc_acl::Grant`s and pushes every
// change straight into the live `AclEngine`; every handler here is a thin
// HTTP skin over `CoreApi`'s grant methods, gated by `require_admin` exactly
// like every other `/api/admin/**` route.

async fn admin_list_shares(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    Json(state.core.admin_shares()).into_response()
}

/// `POST /api/admin/shares` — register a new folder share (the complaint this route exists to fix is "there is no setting
/// to add folders"). `sc_core::Core::create_share` validates `host_path`
/// explicitly and reports which of nonexistent/not-a-directory/unreadable/
/// overlapping-share failed, surfaced here as `422 fs.invalid_name` with
/// `detail.reason` rather than a generic error.
async fn admin_create_share(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::core_api::ShareCreateReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.core.create_share(req) {
        Ok(s) => {
            republish_smb_registry(&state);
            (StatusCode::CREATED, Json(s)).into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

/// Distinct from `parse_share_id` above (that one is for public share
/// *links*, an `i64` id) — a folder share's `sc_vfs::ShareId` is a `u32`.
fn parse_folder_share_id(raw: &str) -> Result<u32, AppError> {
    raw.parse::<u32>().map_err(|_| AppError::not_found())
}

/// `PATCH /api/admin/shares/{id}` — rename, repoint, and/or toggle trash on
/// a share. `sc_core::Core::update_share` refuses a rename/repoint on a
/// `config.toml` share (`422`, since it would be silently undone at the next
/// restart) and re-validates a new `host_path` the same way `create_share`
/// does, but allows `trash_enabled` on any share, config-file or not — that
/// override is persisted in `shares.db`, so it survives a restart.
async fn admin_update_share(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(req): Json<crate::core_api::SharePatchReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let sid = match parse_folder_share_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.update_share(sid, req) {
        Ok(s) => {
            republish_smb_registry(&state);
            Json(s).into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `DELETE /api/admin/shares/{id}` — unregister a share created from this
/// UI, cascading to any grant that named it (`sc_core::Core::delete_share`).
/// Same `config.toml` refusal as `admin_update_share`.
async fn admin_delete_share(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let sid = match parse_folder_share_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.delete_share(sid) {
        Ok(()) => {
            republish_smb_registry(&state);
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

#[derive(Deserialize)]
struct AdminGrantsQuery {
    /// Filter to one user's grants — the id `AdminUser.id` names. Mutually
    /// exclusive with `group` in practice (a grant's principal is one or the
    /// other, never both); if a caller sends both, `user` wins, matching
    /// `sc_acl::Principal` only ever being one variant at a time.
    user: Option<u32>,
    group: Option<u32>,
    share: Option<u32>,
}

/// `GET /api/admin/grants[?user=|group=][&share=]`. Un-narrowed, this is the
/// deployment's whole grant table — the admin "who can see what" view; the
/// per-user grant editor in the UI narrows with `?user=`.
async fn admin_list_grants(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Query(q): Query<AdminGrantsQuery>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let grant_principal = match (q.user, q.group) {
        (Some(u), _) => Some(crate::core_api::GrantPrincipal::User(u)),
        (None, Some(g)) => Some(crate::core_api::GrantPrincipal::Group(g)),
        (None, None) => None,
    };
    let filter = crate::core_api::GrantFilter { principal: grant_principal, share: q.share };
    match state.core.list_grants(filter) {
        Ok(grants) => Json(grants).into_response(),
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `POST /api/admin/grants`. `sc_core::Core::create_grant` refuses a spec
/// with neither `allow` nor `deny` set — a grant that authorizes nothing
/// would be a silent no-op row (`CoreError::InvalidPath`, surfaced here as
/// `422 fs.invalid_name`) — and refuses an unknown `share` id
/// (`404 fs.not_found`).
async fn admin_create_grant(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<crate::core_api::GrantCreateReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.core.create_grant(req.into_spec()) {
        Ok(g) => {
            republish_smb_registry(&state);
            (StatusCode::CREATED, Json(g)).into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

fn parse_grant_id(raw: &str) -> Result<u32, AppError> {
    raw.parse::<u32>().map_err(|_| AppError::not_found())
}

/// `PATCH /api/admin/grants/{id}` — `allow`/`deny`/`inherit`/`label` only;
/// principal/share/subpath identify the grant and are not patchable (delete
/// and recreate instead — `sc_core::acl_store::GrantPatch`'s own doc explains
/// why: changing what a grant *is* rather than what it *allows* shouldn't be
/// reachable by omitting a field).
async fn admin_update_grant(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(req): Json<crate::core_api::GrantPatchReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let gid = match parse_grant_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.update_grant(gid, req.into_patch()) {
        Ok(g) => {
            republish_smb_registry(&state);
            Json(g).into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

/// `DELETE /api/admin/grants/{id}` — revoke, immediately: `Core::delete_grant`
/// pushes the change into the live `AclEngine` before this responds, so the
/// next request from the affected user (or the very next `GET
/// /api/auth/session` this admin's own UI issues to refresh the picture) sees
/// it gone, not on the next restart.
async fn admin_delete_grant(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let gid = match parse_grant_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.core.delete_grant(gid) {
        Ok(()) => {
            republish_smb_registry(&state);
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

// -------------------------------------------------------- admin: groups --
// Group CRUD + membership. `sc-auth` owns `group_`/
// `membership`; `Principal::Group`/`GrantPrincipal::Group` on the ACL side
// already existed before this, so the only wiring left is: push a freshly
// read membership map into the live `AclEngine`
// (`CoreApi::refresh_group_memberships`) after every mutation here, so a
// change is visible immediately rather than only after the next restart's
// `sc-server::app::project_grants`.

/// A group plus its current member ids — returned inline rather than
/// requiring a second request per group, since the admin UI's one screen
/// needs both to render anything.
#[derive(Serialize)]
struct AdminGroupWire {
    id: u32,
    name: String,
    members: Vec<u32>,
}

fn group_name_error_response(e: sc_auth::GroupNameError) -> AppError {
    use sc_auth::GroupNameError as E;
    match e {
        E::DuplicateName => AppError::new(ErrorCode::FsConflict, "a group with that name already exists"),
        E::NotFound => AppError::not_found(),
        E::Internal(_) => AppError::internal(),
    }
}

fn group_op_error_response(e: sc_auth::GroupOpError) -> AppError {
    use sc_auth::GroupOpError as E;
    match e {
        E::NotFound => AppError::not_found(),
        E::Internal(_) => AppError::internal(),
    }
}

fn parse_group_id(raw: &str) -> Result<sc_vfs::GroupId, AppError> {
    raw.parse::<u32>().map(sc_vfs::GroupId::new).map_err(|_| AppError::not_found())
}

/// Re-reads every membership row and pushes it into the live `AclEngine` —
/// called after any mutation that could have changed who belongs to which
/// group (create/delete a group, add/remove a member), never after a plain
/// rename.
fn refresh_group_memberships(state: &AppState) {
    let m = state.auth.list_memberships_all().unwrap_or_default();
    state.core.refresh_group_memberships(m);
}

/// Tell the passdb publisher that the Share/Grant registry moved, so
/// `smb.conf`/`smbpasswd` get rewritten from it.
///
/// Call after **every** successful admin mutation of a share or a grant.
/// `smbd` authenticates and authorises against the last file this server
/// published, and nothing republishes on a timer or at startup — so without
/// this a deleted grant stays live over SMB indefinitely, while the web UI
/// shows it gone. Group and account mutations do not need it: `sc-auth`
/// raises the same signal from inside those calls.
///
/// Cheap and idempotent — the publisher coalesces a burst into one render, so
/// calling it on a no-op mutation costs nothing worth avoiding.
fn republish_smb_registry(state: &AppState) {
    state.auth.republish_passdb();
}

/// `GET /api/admin/groups` — every group with its current members.
async fn admin_list_groups(State(state): State<AppState>, principal: Option<Extension<Principal>>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let groups = match state.auth.list_groups() {
        Ok(g) => g,
        Err(_) => return AppError::internal().into_response(),
    };
    let memberships = state.auth.list_memberships_all().unwrap_or_default();
    let out: Vec<AdminGroupWire> = groups
        .into_iter()
        .map(|g| {
            let members = memberships
                .iter()
                .filter(|(_, groups)| groups.contains(&g.id))
                .map(|(u, _)| u.get())
                .collect();
            AdminGroupWire { id: g.id.get(), name: g.name, members }
        })
        .collect();
    Json(out).into_response()
}

#[derive(Deserialize)]
struct AdminGroupCreateReq {
    name: String,
}

/// `POST /api/admin/groups`.
async fn admin_create_group(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Json(req): Json<AdminGroupCreateReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    match state.auth.create_group(&req.name) {
        Ok(id) => (StatusCode::CREATED, Json(AdminGroupWire { id: id.get(), name: req.name, members: Vec::new() }))
            .into_response(),
        Err(e) => group_name_error_response(e).into_response(),
    }
}

#[derive(Deserialize)]
struct AdminGroupPatchReq {
    name: String,
}

/// `PATCH /api/admin/groups/{id}` — rename only; membership is managed
/// through the `/members` sub-routes below, not this one.
async fn admin_rename_group(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(req): Json<AdminGroupPatchReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let gid = match parse_group_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = state.auth.rename_group(gid, &req.name) {
        return group_name_error_response(e).into_response();
    }
    let members = state.auth.list_group_members(gid).unwrap_or_default().into_iter().map(|u| u.get()).collect();
    Json(AdminGroupWire { id: gid.get(), name: req.name, members }).into_response()
}

/// `DELETE /api/admin/groups/{id}` — cascades to `membership`
/// (`sc_auth::AuthService::delete_group`); refreshes the live ACL map since
/// every grant naming this group as its principal is now dead weight (the
/// grant row itself is left in place, same as a share grant surviving a
/// user's own deletion — an orphaned grant is inert, not a security hole).
async fn admin_delete_group(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let gid = match parse_group_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.auth.delete_group(gid) {
        Ok(()) => {
            refresh_group_memberships(&state);
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => group_op_error_response(e).into_response(),
    }
}

#[derive(Deserialize)]
struct AdminGroupMemberReq {
    user: u32,
}

/// `POST /api/admin/groups/{id}/members` — add one account to this group.
async fn admin_add_group_member(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path(id): Path<String>,
    Json(req): Json<AdminGroupMemberReq>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let gid = match parse_group_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.auth.add_membership(sc_vfs::UserId::new(req.user), gid) {
        Ok(()) => {
            refresh_group_memberships(&state);
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => group_op_error_response(e).into_response(),
    }
}

/// `DELETE /api/admin/groups/{id}/members/{user}` — remove one account from
/// this group.
async fn admin_remove_group_member(
    State(state): State<AppState>,
    principal: Option<Extension<Principal>>,
    Path((id, user)): Path<(String, String)>,
) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let gid = match parse_group_id(&id) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    let uid = match parse_user_id(&user) {
        Ok(v) => v,
        Err(e) => return e.into_response(),
    };
    match state.auth.remove_membership(uid, gid) {
        Ok(()) => {
            refresh_group_memberships(&state);
            StatusCode::NO_CONTENT.into_response()
        }
        Err(e) => group_op_error_response(e).into_response(),
    }
}

// ── admin: audit log ──────────────────────────────
// Read-only over `sc_auth::AuthService::list_audit` — no wiring through
// `CoreApi` needed, unlike shares/grants/groups, since the audit table lives
// entirely in `sc-auth`'s own database.

/// Default page size when `?limit=` is omitted. Independent from, but no
/// larger than, `sc_auth::audit`'s own internal `AUDIT_PAGE_MAX` (200) —
/// mirrored here (not exported by that crate) purely to decide whether a
/// page came back short (nothing older left) or full (there may be more).
const ADMIN_AUDIT_DEFAULT_LIMIT: u32 = 50;
const ADMIN_AUDIT_MAX_LIMIT: u32 = 200;

#[derive(Deserialize)]
struct AdminAuditQuery {
    actor: Option<u32>,
    event: Option<String>,
    since_ns: Option<i64>,
    until_ns: Option<i64>,
    /// Previous page's last `rowid` (exclusive) — the pagination cursor.
    /// Omit for the first page.
    before: Option<i64>,
    limit: Option<u32>,
}

#[derive(Serialize)]
struct AdminAuditRowWire {
    rowid: i64,
    // Nanosecond timestamp — as a string, same reason every other one in
    // this file is (`AdminUserWire::created_ns`'s doc comment): it already
    // exceeds a JS number's 2^53 exact-integer range.
    ts_ns: String,
    actor: Option<u32>,
    /// Resolved display name, best-effort — `None` for a since-deleted
    /// account or a system-attributed row (`actor` itself is also `None`
    /// there).
    actor_name: Option<String>,
    event: String,
    target: Option<String>,
    ip: Option<String>,
    ok: bool,
    detail: Option<String>,
}

#[derive(Serialize)]
struct AdminAuditPage {
    rows: Vec<AdminAuditRowWire>,
    /// `rows.last().rowid`, present only when the page came back full — a
    /// short page means there is nothing older left to fetch.
    next: Option<i64>,
}

/// `GET /api/admin/audit[?actor=&event=&since_ns=&until_ns=&before=&limit=]`
/// Newest first; cursor-paginated on `rowid` rather
/// than offset so a page boundary stays correct even while new rows keep
/// landing ahead of it.
async fn admin_list_audit(State(state): State<AppState>, principal: Option<Extension<Principal>>, Query(q): Query<AdminAuditQuery>) -> Response {
    let principal = match principal_or_401(principal) {
        Ok(p) => p,
        Err(e) => return e.into_response(),
    };
    if let Err(e) = require_admin(&state, &principal) {
        return e.into_response();
    }
    let limit = q.limit.unwrap_or(ADMIN_AUDIT_DEFAULT_LIMIT).clamp(1, ADMIN_AUDIT_MAX_LIMIT);
    let filter = sc_auth::AuditFilter {
        actor: q.actor.map(UserId::new),
        event: q.event,
        since_ns: q.since_ns,
        until_ns: q.until_ns,
    };
    let rows = match state.auth.list_audit(&filter, q.before, limit) {
        Ok(r) => r,
        Err(_) => return AppError::internal().into_response(),
    };
    let names: std::collections::HashMap<u32, String> = state
        .auth
        .list_users()
        .unwrap_or_default()
        .into_iter()
        .map(|u| (u.id.get(), u.display.clone().unwrap_or(u.name)))
        .collect();
    let next = (rows.len() as u32 == limit).then(|| rows.last().map(|r| r.rowid)).flatten();
    let wire_rows = rows
        .into_iter()
        .map(|r| AdminAuditRowWire {
            rowid: r.rowid,
            ts_ns: r.ts_ns.to_string(),
            actor_name: r.actor.and_then(|a| names.get(&a).cloned()),
            actor: r.actor,
            event: r.event,
            target: r.target,
            ip: r.ip,
            ok: r.ok,
            detail: r.detail,
        })
        .collect();
    Json(AdminAuditPage { rows: wire_rows, next }).into_response()
}

/// `true` when `path` falls under `prefix`. A trailing `/` on `prefix` makes
/// it a directory match (`path == "/dav"` *or* `path.starts_with("/dav/")`,
/// for a `prefix` of `"/dav/"`); no trailing slash makes it an exact-file
/// match (`"/status.php"` matches only itself, never
/// `"/status.phpwhatever"`).
fn path_is_under(path: &str, prefix: &str) -> bool {
    match prefix.strip_suffix('/') {
        Some(dir) => path == dir || path.starts_with(prefix),
        None => path == prefix,
    }
}

/// This crate's own URL vocabulary — — never a candidate
/// for the `embed-ui` SPA fallback no matter what else is merged in beside
/// it. Unlike [`crate::config::HttpConfig::reserved_path_prefixes`], these
/// never need to be supplied by an assembler: this crate already knows they
/// are its own.
const OWN_RESERVED_PREFIXES: &[&str] = &["/api/", "/c/", "/s/"];

/// Also used by `middleware::auth` — the same question ("is this path
/// somebody else's protected surface?") decides both whether the SPA
/// fallback may answer here *and* whether a session is required to reach
/// this path at all. A browser has no session before it has even loaded the
/// login screen, so every path this predicate says "no" to has to be
/// reachable unauthenticated, or the bundle that would show that login
/// screen could never load in the first place.
pub(crate) fn is_reserved_path(cfg: &crate::config::HttpConfig, path: &str) -> bool {
    OWN_RESERVED_PREFIXES.iter().any(|p| path_is_under(path, p))
        || cfg.reserved_path_prefixes.iter().any(|p| path_is_under(path, p))
}

/// The router's single global fallback. It is the
/// *only* one in the whole merged application, not only this crate's own
/// `/api/**`: `sc-server`'s `App::router` merges the WebDAV tree and
/// (feature-gated) the compatibility layer in beside this router's output,
/// neither sets a fallback of its own, and axum panics if two merged
/// routers both tried (`lib.rs::build_router`'s doc comment traces the
/// ordering this all depends on). So this is where every request that
/// matched *no* route anywhere in that tree ends up.
///
/// In order:
/// 1. `/api/admin/**` — unrelated to the frontend — gets `501`, unchanged
///    from before this handler knew about the SPA.
/// 2. Anything under a reserved prefix (this crate's own `/api/`·`/c/`·`/s/`,
///    plus whatever `sc-server` supplies via `HttpConfig::reserved_path_prefixes`
///    for the WebDAV/compatibility mounts it assembles) gets the ordinary
///    JSON `fs.not_found` envelope a client parsing an API response expects.
///    A `404` that silently turns into an HTML document here would be a
///    debugging nightmare for exactly the callers most likely to hit it —
///    and it must be checked *before* the SPA attempt below, not merely
///    happen to fail there.
/// 3. The content origin never gets the SPA, full stop — that origin exists
///    so untrusted stored content can never run with the application's own
///    authority; serving the app's own HTML/JS
///    there would hand that authority right back.
/// 4. Everything else, on the app origin, `GET`/`HEAD` only, is a
///    client-routed SPA path — `#[cfg(feature = "embed-ui")]` serves the
///    matching built asset or `index.html`. Without that feature (or for
///    any other method) this falls straight through to the same plain `404`
///    this handler always returned.
async fn admin_catch_all(State(state): State<AppState>, req: axum::extract::Request) -> Response {
    let path = req.uri().path().to_string();
    if path.starts_with("/api/admin/") {
        return AppError::not_implemented().into_response();
    }
    if is_reserved_path(&state.cfg, &path) {
        return AppError::not_found().into_response();
    }

    let origin = req.extensions().get::<HostOrigin>().copied();
    if origin == Some(HostOrigin::Content) {
        return AppError::not_found().into_response();
    }

    #[cfg(feature = "embed-ui")]
    {
        let method = req.method().clone();
        if matches!(method, axum::http::Method::GET | axum::http::Method::HEAD) {
            if let Some(resp) = crate::embed::serve(&path, req.headers(), &method) {
                return resp;
            }
        }
    }

    AppError::not_found().into_response()
}

// ---------------------------------------------------------------- content --
// — signed content-origin serving.
//
// `security_headers` (middleware.rs) already sets `X-Content-Type-Options`,
// `Referrer-Policy`, `Cross-Origin-Resource-Policy` and the sandboxed CSP for
// every response on the content host; what's left for this handler is the
// per-token verification, the etag-mismatch `410`, and the actual bytes.

fn cache_control_for(claim: &content::Claim) -> String {
    let remaining = claim.exp.saturating_sub(now_unix());
    format!("private, max-age={remaining}, immutable")
}

/// `Attachment`/`Stream`: stream the original bytes. `range` is already
/// clamped against `stat.size` by the caller.
fn serve_original(state: &AppState, fid: sc_vfs::ids::FileId, stat: &crate::content_api::ContentStat, claim: &content::Claim, range: Option<(u64, u64)>) -> Response {
    let reader = match state.content.open_stream(fid, range) {
        Ok(r) => r,
        Err(crate::core_api::CoreError::Gone) => return StatusCode::GONE.into_response(),
        Err(_) => return AppError::not_found().into_response(),
    };

    let (status, content_len) = match range {
        Some((start, end)) => (StatusCode::PARTIAL_CONTENT, end - start + 1),
        None => (StatusCode::OK, stat.size),
    };

    let mut resp = content::body_from_reader(reader).into_response();
    *resp.status_mut() = status;
    let h = resp.headers_mut();
    h.insert(
        axum::http::header::CONTENT_TYPE,
        axum::http::HeaderValue::from_static("application/octet-stream"),
    );
    if let Ok(v) = axum::http::HeaderValue::from_str(&content::content_disposition_value("attachment", &stat.name)) {
        h.insert(axum::http::header::CONTENT_DISPOSITION, v);
    }
    if let Some((start, end)) = range {
        if let Ok(v) = axum::http::HeaderValue::from_str(&format!("bytes {start}-{end}/{}", stat.size)) {
            h.insert(axum::http::header::CONTENT_RANGE, v);
        }
    }
    if let Ok(v) = axum::http::HeaderValue::from_str(&content_len.to_string()) {
        h.insert(axum::http::header::CONTENT_LENGTH, v);
    }
    h.insert(axum::http::header::ACCEPT_RANGES, axum::http::HeaderValue::from_static("bytes"));
    if let Ok(v) = axum::http::HeaderValue::from_str(&cache_control_for(claim)) {
        h.insert(axum::http::header::CACHE_CONTROL, v);
    }
    resp
}

async fn content_get(State(state): State<AppState>, Path(token): Path<String>, req: axum::extract::Request) -> Response {
    // Content-origin requests never parse cookies;
    // `HostGuard` already ensured we only reach this handler when the Host
    // header matched a content host, but double check defensively.
    // Single-origin fallback: with no dedicated
    // content host configured there is nothing for the App origin to be
    // distinguished *from*, so refusing here would make downloads impossible
    // in that deployment. The isolation this normally buys is genuinely lost,
    // which is why startup warns about it rather than treating it as equal.
    let single_origin = state.cfg.content_hosts.is_empty();
    let origin = req.extensions().get::<HostOrigin>().copied();
    if origin != Some(HostOrigin::Content) && !(single_origin && origin == Some(HostOrigin::App)) {
        return AppError::not_found().into_response();
    }
    // Scoped so the (non-`Send`) `parking_lot` guard is provably dropped
    // before the `.await`s below — `content_get` must be `Send` to be an
    // axum handler.
    let claim = {
        let keys = state.signed_url_keys.lock();
        // The etag re-check happens below, against the file's *current*
        // state — verification here only settles the signature and expiry.
        match content::verify(&keys, &token, None) {
            Ok(c) => c,
            Err(content::VerifyError::Expired) | Err(content::VerifyError::EtagMismatch) => {
                return StatusCode::GONE.into_response();
            }
            Err(_) => return StatusCode::FORBIDDEN.into_response(),
        }
    };

    let fid = sc_vfs::ids::FileId::new(claim.fid);
    let stat = match state.content.stat_by_fid(fid) {
        Ok(s) => s,
        Err(_) => return AppError::not_found().into_response(),
    };
    // the etag prefix in the claim auto-invalidates
    // the URL the moment the file changes underneath it.
    if content::etag8_of(&stat.etag) != claim.etag {
        return StatusCode::GONE.into_response();
    }

    match claim.disp {
        content::Disposition::InlineThumb => {
            let (w, h) = claim.dim.unwrap_or((256, 256));
            match state.content.thumbnail(fid, w, h, claim.etag).await {
                Ok(thumb_bytes) => {
                    let mut resp = bytes::Bytes::from(thumb_bytes).into_response();
                    let hh = resp.headers_mut();
                    // Our own re-encode only — never the original bytes, and
                    // that is exactly what makes `inline` safe here.
                    // 
                    hh.insert(axum::http::header::CONTENT_TYPE, axum::http::HeaderValue::from_static("image/webp"));
                    if let Ok(v) = axum::http::HeaderValue::from_str(&content::content_disposition_value("inline", "thumbnail.webp")) {
                        hh.insert(axum::http::header::CONTENT_DISPOSITION, v);
                    }
                    if let Ok(v) = axum::http::HeaderValue::from_str(&cache_control_for(&claim)) {
                        hh.insert(axum::http::header::CACHE_CONTROL, v);
                    }
                    resp
                }
                Err(crate::core_api::CoreError::Gone) => StatusCode::GONE.into_response(),
                Err(_) => AppError::internal().into_response(),
            }
        }
        content::Disposition::Attachment => serve_original(&state, fid, &stat, &claim, None),
        content::Disposition::Stream => {
            let range_req = req
                .headers()
                .get(axum::http::header::RANGE)
                .and_then(|v| v.to_str().ok())
                .map(content::parse_range_header)
                .unwrap_or(content::RangeRequest::None);
            match content::clamp_range(range_req, stat.size) {
                Ok(range) => serve_original(&state, fid, &stat, &claim, range),
                Err(()) => {
                    let mut resp = StatusCode::RANGE_NOT_SATISFIABLE.into_response();
                    if let Ok(v) = axum::http::HeaderValue::from_str(&format!("bytes */{}", stat.size)) {
                        resp.headers_mut().insert(axum::http::header::CONTENT_RANGE, v);
                    }
                    resp
                }
            }
        }
    }
}

use axum::http::StatusCode;

impl crate::state::JobStatus {
    fn done_total_json(&self) -> serde_json::Value {
        serde_json::json!({
            "id": self.id,
            "kind": self.kind.as_str(),
            "state": self.state, "done": self.done, "total": self.total,
            "current": self.current, "errors": self.errors,
            // Populated once the job reaches a terminal state — a poller
            // sees exactly the `OpResult` list a synchronous caller would
            // have gotten back inline.
            "results": self.results,
            // Paths a `begin_result` recorded but the process never got to
            // `finish_result` for — only ever non-empty on an `interrupted`
            // job (`JobStore::begin_result`'s doc: the record-before-act half
            // of the zero-loss requirement). Never folded into `results`:
            // an "attempting" outcome is unknown, not `ok` or `failed`.
            "attempting": self.attempting,
            // Paths recorded at job creation that the runner never reached —
            // non-empty on an `interrupted` or `cancelled` job. Together with
            // `results` and `attempting` this accounts for every path the
            // request asked for, so the tray can say exactly what is left to
            // redo instead of only how many items are missing.
            "pending": self.pending,
            // Whether `GET /api/jobs/{id}/download` has bytes waiting.
            // Only ever true for a finished `JobKind::Archive`.
            "download": self.artifact.is_some(),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::core_api::{Entry, Kind};
    use crate::testutil::{test_state_with_core, MockCore};
    use axum::body::Body;
    use axum::http::Request as HttpRequest;
    use sc_acl::Perms;
    use std::sync::Arc;
    use tower::ServiceExt;

    fn mk_entry(name: &str) -> Entry {
        Entry {
            name: name.to_string(),
            kind: Kind::File,
            size: 0,
            mtime_ns: "0".to_string(),
            etag: "e".to_string(),
            perms: Perms::READ,
            id: None,
            preview: None,
            link: None,
            confusable: false,
        }
    }

    fn router_with_principal(state: AppState) -> Router {
        Router::new().route("/api/fs/list", get(fs_list)).with_state(state)
    }

    // ---------------------------------------------------- share link URLs --

    /// The link is handed to someone who is not on this machine, so the
    /// origin has to be the one the deployment says it is reachable at.
    /// This used to be `https://{app_hosts[0]}` unconditionally: scheme
    /// assumed, port dropped. Anything not terminated on 443 therefore
    /// printed a URL that resolves to nothing, in a dialog that says the
    /// link cannot be shown again.
    #[test]
    fn a_share_link_uses_the_declared_public_origin_verbatim() {
        let (mut state, _dir) = test_state_with_core(Arc::new(MockCore::new(vec![])));
        let mut cfg = (*state.cfg).clone();
        cfg.app_hosts = vec!["127.0.0.1".into(), "localhost".into()];
        cfg.public_base_url = Some("http://files.example.org:8080".into());
        state.cfg = Arc::new(cfg);

        assert_eq!(
            public_link_url(&state, "tok"),
            "http://files.example.org:8080/s/tok"
        );
    }

    /// No declared origin is still the old guess, not an error and not an
    /// empty link: a deployment that never set one keeps exactly the
    /// behaviour it had.
    #[test]
    fn a_share_link_falls_back_to_the_first_app_host() {
        let (mut state, _dir) = test_state_with_core(Arc::new(MockCore::new(vec![])));
        let mut cfg = (*state.cfg).clone();
        cfg.app_hosts = vec!["files.example.org".into(), "127.0.0.1".into()];
        cfg.public_base_url = None;
        state.cfg = Arc::new(cfg);

        assert_eq!(public_link_url(&state, "tok"), "https://files.example.org/s/tok");
    }

    // ------------------------------------------------------------ smb cap --

    /// A `CoreApi` whose only distinguishing feature is `smb_enabled() ==
    /// true` — everything else falls through to the trait's defaults, same
    /// as `UnimplementedCore`.
    struct SmbOnCore;
    impl crate::core_api::CoreApi for SmbOnCore {
        fn smb_enabled(&self) -> bool {
            true
        }
    }

    /// Pins the bug this task fixed: `feature_caps` used to hardcode
    /// `smb: false` regardless of what the backend reported, so the SMB
    /// settings section could never render even on a deployment with SMB
    /// switched on. It must now track `CoreApi::smb_enabled()` in both
    /// directions.
    #[tokio::test]
    async fn feature_caps_smb_tracks_the_core_backend() {
        let (state_off, _dir) = test_state_with_core(Arc::new(crate::core_api::UnimplementedCore));
        assert!(!feature_caps(&state_off).smb, "the default backend has no SMB config to report");

        let (state_on, _dir2) = test_state_with_core(Arc::new(SmbOnCore));
        assert!(feature_caps(&state_on).smb, "a backend that reports smb_enabled() == true must be surfaced");
    }

    // ------------------------------------------------------- acl_denied by --

    /// A `CoreApi` whose `list` always reports a denial attributed to a
    /// specific grant id — standing in for `sc_core::CoreError::Denied { by:
    /// Some(_) }` reaching this crate via `sc-server`'s `bridge.rs`.
    struct DeniedByCore;
    impl crate::core_api::CoreApi for DeniedByCore {
        fn list(
            &self,
            _user: sc_vfs::ids::UserId,
            _vpath: &str,
            _sort: SortKey,
            _order: Order,
        ) -> Result<crate::core_api::Listing, crate::core_api::CoreError> {
            Err(crate::core_api::CoreError::Denied { by: Some(42) })
        }
    }

    /// Pins the bug this task fixed: `AppError::acl_denied` used to always
    /// receive the hardcoded literal `"acl"`, so an explainable-deny never
    /// reached the client no matter which grant actually produced it. A
    /// `CoreError::Denied { by: Some(id) }` must now surface that `id`, via
    /// the same `AppError::from(CoreError)` conversion every `/api/fs/**`
    /// handler goes through.
    #[tokio::test]
    async fn acl_denied_reports_the_real_grant_id() {
        let (state, _dir) = test_state_with_core(Arc::new(DeniedByCore));
        let app = router_with_principal(state);
        let req = get_list_req("?path=");
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(json["error"]["detail"]["by"], "42", "the real grant id must reach the client, not the placeholder \"acl\"");
    }

    // ------------------------------------------------------ app passwords --

    /// A `CoreApi` whose only real behaviour is `roots`, reporting a fixed
    /// set of `(label, ShareId)` pairs — enough to exercise
    /// `create_app_password`'s label -> `ShareId` resolution for
    /// `scope.shares` without a real backend.
    struct RootsCore {
        roots: Vec<sc_acl::RootEntry>,
    }
    impl crate::core_api::CoreApi for RootsCore {
        fn roots(&self, _user: sc_vfs::ids::UserId) -> Vec<sc_acl::RootEntry> {
            self.roots.clone()
        }
    }
    fn roots_core(labels: &[(&str, u32)]) -> Arc<RootsCore> {
        Arc::new(RootsCore {
            roots: labels
                .iter()
                .map(|&(label, id)| sc_acl::RootEntry {
                    label: label.to_string(),
                    share: sc_vfs::ids::ShareId::new(id),
                    subpath: sc_vfs::SafePath::root(),
                    perms: Perms::all(),
                    shared_externally: false,
                    trash_enabled: false,
                })
                .collect(),
        })
    }

    fn app_password_router(state: AppState) -> Router {
        Router::new().route("/api/auth/app-passwords", post(create_app_password)).with_state(state)
    }

    fn app_password_req(body: serde_json::Value) -> HttpRequest<Body> {
        HttpRequest::builder()
            .method("POST")
            .uri("/api/auth/app-passwords")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .header("Content-Type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap()
    }

    /// No `scope` field at all must keep minting an unrestricted app
    /// password — existing clients that only ever send `{"name": "..."}"`
    /// must be unaffected by this endpoint learning to accept a scope.
    #[tokio::test]
    async fn create_app_password_with_no_scope_field_succeeds() {
        let (state, _dir) = test_state_with_core(roots_core(&[("mine", 1)]));
        let app = app_password_router(state);
        let resp = app.oneshot(app_password_req(serde_json::json!({ "name": "device" }))).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert!(json["token"].is_string());
    }

    /// A restricted scope naming shares the caller actually has succeeds.
    #[tokio::test]
    async fn create_app_password_with_a_valid_scope_succeeds() {
        let (state, _dir) = test_state_with_core(roots_core(&[("mine", 1), ("other", 2)]));
        let app = app_password_router(state);
        let body = serde_json::json!({
            "name": "device",
            "scope": { "perms": { "read": true }, "shares": ["mine"] },
        });
        let resp = app.oneshot(app_password_req(body)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    /// A share label that isn't one of the caller's own roots must reject
    /// the whole creation request — fail closed rather than mint a token
    /// scoped to a share it never had, or silently drop the restriction.
    #[tokio::test]
    async fn create_app_password_with_an_unknown_share_label_is_422() {
        let (state, _dir) = test_state_with_core(roots_core(&[("mine", 1)]));
        let app = app_password_router(state);
        let body = serde_json::json!({
            "name": "device",
            "scope": { "shares": ["does-not-exist"] },
        });
        let resp = app.oneshot(app_password_req(body)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNPROCESSABLE_ENTITY);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(json["error"]["code"], "auth.unknown_share");
    }

    fn get_list_req(query: &str) -> HttpRequest<Body> {
        HttpRequest::builder()
            .uri(format!("/api/fs/list{query}"))
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::empty())
            .unwrap()
    }

    // ------------------------------------------------------------ setup --

    mod setup {
        use super::*;
        use crate::setup_api::{SetupApi, SetupClosed, SetupError, SetupOutcome};
        use crate::testutil::{test_state_with_setup, ScriptedSetup};

        fn router(setup: Arc<dyn SetupApi>) -> (Router, tempfile::TempDir) {
            let (state, dir) = test_state_with_setup(setup);
            (crate::build_router(state), dir)
        }

        async fn post(app: &Router, body: &str) -> axum::response::Response {
            let req = HttpRequest::builder()
                .method("POST")
                .uri("/api/setup")
                .header("Host", "localhost")
                .header("Content-Type", "application/json")
                .body(Body::from(body.to_string()))
                .unwrap();
            app.clone().oneshot(req).await.unwrap()
        }

        async fn get(app: &Router) -> axum::response::Response {
            let req = HttpRequest::builder()
                .uri("/api/setup")
                .header("Host", "localhost")
                .body(Body::empty())
                .unwrap();
            app.clone().oneshot(req).await.unwrap()
        }

        async fn json_of(resp: axum::response::Response) -> serde_json::Value {
            let b = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&b).unwrap()
        }

        const OK_BODY: &str = r#"{"token":"t","username":"admin","password":"correct horse battery"}"#;

        /// The whole point: reachable with no credential at all. Before this
        /// existed there was no route in the binary that could create the
        /// first account, so a fresh deployment could not be logged into.
        #[tokio::test]
        async fn status_is_reachable_unauthenticated_and_is_a_bare_boolean() {
            let (app, _d) = router(Arc::new(ScriptedSetup::required_returning(Err(
                SetupError::Completed,
            ))));
            let resp = get(&app).await;
            assert_eq!(resp.status(), StatusCode::OK);
            let j = json_of(resp).await;
            assert_eq!(j["required"], serde_json::json!(true));
            // Nothing else may appear here: no token, no expiry, no names.
            assert_eq!(j.as_object().unwrap().len(), 1, "{j}");
        }

        #[tokio::test]
        async fn status_is_false_once_the_gate_is_closed() {
            let (app, _d) = router(Arc::new(SetupClosed));
            let j = json_of(get(&app).await).await;
            assert_eq!(j["required"], serde_json::json!(false));
        }

        #[tokio::test]
        async fn success_is_201_with_the_new_account_and_no_session_cookie() {
            let (app, _d) = router(Arc::new(ScriptedSetup::required_returning(Ok(
                SetupOutcome { user_id: 1, username: "admin".into() },
            ))));
            let resp = post(&app, OK_BODY).await;
            assert_eq!(resp.status(), StatusCode::CREATED);
            // The bootstrap creates an account; `POST /api/auth/login`
            // remains the only thing that issues a credential.
            assert!(resp.headers().get(axum::http::header::SET_COOKIE).is_none());
            let j = json_of(resp).await;
            assert_eq!(j["user"]["id"], serde_json::json!(1));
            assert_eq!(j["user"]["name"], serde_json::json!("admin"));
        }

        #[tokio::test]
        async fn a_closed_gate_answers_410_gone() {
            let (app, _d) = router(Arc::new(SetupClosed));
            let resp = post(&app, OK_BODY).await;
            assert_eq!(resp.status(), StatusCode::GONE);
            assert_eq!(json_of(resp).await["error"]["code"], serde_json::json!("setup.completed"));
        }

        #[tokio::test]
        async fn a_wrong_token_is_403_invalid_token() {
            let (app, _d) = router(Arc::new(ScriptedSetup::required_returning(Err(
                SetupError::InvalidToken,
            ))));
            let resp = post(&app, OK_BODY).await;
            assert_eq!(resp.status(), StatusCode::FORBIDDEN);
            assert_eq!(
                json_of(resp).await["error"]["code"],
                serde_json::json!("setup.invalid_token")
            );
        }

        #[tokio::test]
        async fn an_expired_token_is_403_token_expired() {
            let (app, _d) = router(Arc::new(ScriptedSetup::required_returning(Err(
                SetupError::Expired,
            ))));
            let resp = post(&app, OK_BODY).await;
            assert_eq!(resp.status(), StatusCode::FORBIDDEN);
            assert_eq!(
                json_of(resp).await["error"]["code"],
                serde_json::json!("setup.token_expired")
            );
        }

        #[tokio::test]
        async fn a_short_password_is_422_and_quotes_the_requirement() {
            let (app, _d) = router(Arc::new(ScriptedSetup::required_returning(Err(
                SetupError::WeakPassword { min_len: 10 },
            ))));
            let resp = post(&app, r#"{"token":"t","username":"admin","password":"short"}"#).await;
            assert_eq!(resp.status(), StatusCode::UNPROCESSABLE_ENTITY);
            let j = json_of(resp).await;
            assert_eq!(j["error"]["code"], serde_json::json!("setup.weak_password"));
            assert_eq!(j["error"]["detail"]["min_length"], serde_json::json!(10));
        }

        #[tokio::test]
        async fn a_bad_username_is_422_with_a_fixed_reason() {
            let (app, _d) = router(Arc::new(ScriptedSetup::required_returning(Err(
                SetupError::InvalidUsername("must not be empty"),
            ))));
            let resp = post(&app, r#"{"token":"t","username":"","password":"correct horse battery"}"#).await;
            assert_eq!(resp.status(), StatusCode::UNPROCESSABLE_ENTITY);
            let j = json_of(resp).await;
            assert_eq!(j["error"]["code"], serde_json::json!("setup.invalid_username"));
            assert_eq!(j["error"]["detail"]["reason"], serde_json::json!("must not be empty"));
        }

        /// An unauthenticated endpoint that creates an administrator gets its
        /// own budget, not the general API one.
        #[tokio::test]
        async fn attempts_are_rate_limited_per_ip() {
            let (mut state, _d) = crate::testutil::test_state_with_setup(Arc::new(
                ScriptedSetup::required_always(Err(SetupError::InvalidToken)),
            ));
            state.setup_rate =
                Arc::new(crate::rate_limit::IpTokenBucket::new(2, std::time::Duration::from_secs(3600)));
            let app = crate::build_router(state);

            assert_eq!(post(&app, OK_BODY).await.status(), StatusCode::FORBIDDEN);
            assert_eq!(post(&app, OK_BODY).await.status(), StatusCode::FORBIDDEN);
            let limited = post(&app, OK_BODY).await;
            assert_eq!(limited.status(), StatusCode::TOO_MANY_REQUESTS);
            assert!(limited.headers().get("Retry-After").is_some());
        }

        /// The rate limit is charged before the body is parsed, so a flood of
        /// junk cannot buy free attempts.
        #[tokio::test]
        async fn a_malformed_body_still_spends_an_attempt() {
            let (mut state, _d) = crate::testutil::test_state_with_setup(Arc::new(
                ScriptedSetup::required_always(Err(SetupError::InvalidToken)),
            ));
            state.setup_rate =
                Arc::new(crate::rate_limit::IpTokenBucket::new(1, std::time::Duration::from_secs(3600)));
            let app = crate::build_router(state);

            assert_eq!(post(&app, "not json").await.status(), StatusCode::UNPROCESSABLE_ENTITY);
            assert_eq!(post(&app, OK_BODY).await.status(), StatusCode::TOO_MANY_REQUESTS);
        }
    }

    // -------------------------------------------------------------- login --

    /// Pins the bug this task fixed: both `POST /api/auth/login` and
    /// `POST /api/auth/login/totp` called `create_session(uid, ip, "")` —
    /// the literal empty string, never the request's real `User-Agent` — so
    /// the active-sessions list always showed "unknown device" no matter what
    /// actually connected. `auth_login` must now forward the header through
    /// to the stored session row.
    #[tokio::test]
    async fn login_stores_the_real_user_agent_on_the_session() {
        let (state, _dir) = test_state_with_core(Arc::new(crate::core_api::UnimplementedCore));
        state.auth.create_user("carol", &secrecy::SecretString::from("correct horse battery")).unwrap();
        let app = crate::build_router(state.clone());

        let req = HttpRequest::builder()
            .method("POST")
            .uri("/api/auth/login")
            .header("Host", "localhost")
            .header("Content-Type", "application/json")
            .header(axum::http::header::USER_AGENT, "TestClient/1.0 (integration test)")
            .body(Body::from(r#"{"username":"carol","password":"correct horse battery"}"#))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let uid = state.auth.find_user("carol").unwrap().unwrap().id;
        let sessions = state.auth.list_sessions(uid).unwrap();
        assert_eq!(sessions.len(), 1);
        assert_eq!(sessions[0].ua_first.as_deref(), Some("TestClient/1.0 (integration test)"));
    }

    /// A request with no `User-Agent` header at all must not error — it
    /// degrades to the same empty string `create_session` already treats as
    /// "unknown device" (the frontend's own fallback, not this layer's job
    /// to render).
    #[tokio::test]
    async fn login_without_a_user_agent_header_still_succeeds() {
        let (state, _dir) = test_state_with_core(Arc::new(crate::core_api::UnimplementedCore));
        state.auth.create_user("dave", &secrecy::SecretString::from("correct horse battery")).unwrap();
        let app = crate::build_router(state.clone());

        let req = HttpRequest::builder()
            .method("POST")
            .uri("/api/auth/login")
            .header("Host", "localhost")
            .header("Content-Type", "application/json")
            .body(Body::from(r#"{"username":"dave","password":"correct horse battery"}"#))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let uid = state.auth.find_user("dave").unwrap().unwrap().id;
        let sessions = state.auth.list_sessions(uid).unwrap();
        assert_eq!(sessions[0].ua_first.as_deref(), Some(""));
    }

    async fn get_list(app: &Router, query: &str) -> axum::response::Response {
        let req = HttpRequest::builder()
            .uri(format!("/api/fs/list{query}"))
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::empty())
            .unwrap();
        app.clone().oneshot(req).await.unwrap()
    }

    #[tokio::test]
    async fn list_paginates_and_flags_stale_on_change() {
        let entries: Vec<Entry> = (0..5).map(|i| mk_entry(&format!("f{i}"))).collect();
        let core = Arc::new(MockCore::new(entries));
        let (state, _dir) = test_state_with_core(core.clone());
        let app = router_with_principal(state);

        let resp = get_list(&app, "?path=&limit=2").await;
        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(json["entries"].as_array().unwrap().len(), 2);
        assert_eq!(json["total"], 5);
        let listing_id = json["listing"].as_str().unwrap().to_string();
        let cursor = json["cursor"].as_str().unwrap().to_string();

        // Page 2, same etag: not stale.
        let resp2 = get_list(&app, &format!("?path=&limit=2&listing={listing_id}&cursor={cursor}")).await;
        let bytes2 = http_body_util::BodyExt::collect(resp2.into_body()).await.unwrap().to_bytes();
        assert!(!bytes2.is_empty());

        // Now the directory changes underneath the session.
        core.bump_etag();
        let resp3 = get_list(&app, &format!("?path=&limit=2&listing={listing_id}&cursor={cursor}")).await;
        assert_eq!(resp3.headers().get("Sc-Listing-Stale").unwrap(), "1");
    }

    #[tokio::test]
    async fn list_without_session_id_and_cursor_only_is_expired() {
        let (state, _dir) = test_state_with_core(Arc::new(MockCore::new(vec![mk_entry("a")])));
        let app = router_with_principal(state);
        let resp = get_list(&app, "?path=&cursor=eyJpIjoyMDB9").await;
        assert_eq!(resp.status(), StatusCode::CONFLICT);
    }

    // ------------------------------------------------------ share links --

    use crate::testutil::LinkMockCore;

    fn public_router(state: AppState) -> Router {
        Router::new()
            .route("/s/{token}", get(public_link_get))
            .route("/s/{token}/auth", post(public_link_auth))
            .route("/s/{token}/download", post(public_link_download))
            .route("/s/{token}/drop", post(public_link_drop))
            .with_state(state)
    }

    fn shares_router(state: AppState) -> Router {
        Router::new()
            .route("/api/shares", get(shares_list).post(shares_create))
            .route("/api/shares/{id}", get(shares_get).patch(shares_patch).delete(shares_delete))
            .with_state(state)
    }

    async fn body_json(resp: axum::response::Response) -> serde_json::Value {
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        serde_json::from_slice(&bytes).unwrap_or(serde_json::Value::Null)
    }

    async fn post_json(app: &Router, uri: &str, body: &str, cookie: Option<&str>) -> axum::response::Response {
        let mut b = HttpRequest::builder().method("POST").uri(uri).header("content-type", "application/json");
        if let Some(c) = cookie {
            b = b.header("cookie", c);
        }
        app.clone().oneshot(b.body(Body::from(body.to_string())).unwrap()).await.unwrap()
    }

    async fn get_uri(app: &Router, uri: &str, cookie: Option<&str>) -> axum::response::Response {
        let mut b = HttpRequest::builder().uri(uri);
        if let Some(c) = cookie {
            b = b.header("cookie", c);
        }
        app.clone().oneshot(b.body(Body::empty()).unwrap()).await.unwrap()
    }

    #[test]
    fn wants_html_prefers_whichever_media_type_is_listed_first() {
        let mk = |v: &str| {
            let mut h = axum::http::HeaderMap::new();
            h.insert(axum::http::header::ACCEPT, v.parse().unwrap());
            h
        };
        // A real browser navigation's default `Accept`.
        assert!(wants_html(&mk("text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")));
        // An explicit JSON request.
        assert!(!wants_html(&mk("application/json")));
        // `*/*` alone (a JS `fetch()` with no override) must not flip to HTML.
        assert!(!wants_html(&mk("*/*")));
        // Whichever comes first in the header wins, not merely whichever is
        // present — an API client that lists both, JSON first, still wants
        // JSON.
        assert!(!wants_html(&mk("application/json, text/html")));
        // No header at all: every existing caller of this endpoint that
        // never set one must keep getting JSON.
        assert!(!wants_html(&axum::http::HeaderMap::new()));
    }

    #[tokio::test]
    async fn a_public_link_reveals_only_the_basename() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1)));
        let app = public_router(state);
        let resp = get_uri(&app, "/s/tok", None).await;
        assert_eq!(resp.status(), StatusCode::OK);
        let j = body_json(resp).await;
        assert_eq!(j["name"], "shared.txt");
        // Never the host path, the owner, or the virtual path.
        for leaked in ["path", "owner", "share", "host_path"] {
            assert!(j.get(leaked).is_none(), "public link response leaked `{leaked}`");
        }
    }

    #[tokio::test]
    async fn an_unknown_token_is_404() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1)));
        let app = public_router(state);
        assert_eq!(get_uri(&app, "/s/nope", None).await.status(), StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn a_dead_link_is_410_not_404() {
        // 404 would invite a retry; `410` says the target is gone for good.
        // 
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1).kill(1)));
        let app = public_router(state);
        let resp = get_uri(&app, "/s/tok", None).await;
        assert_eq!(resp.status(), StatusCode::GONE);
        assert_eq!(body_json(resp).await["error"]["code"], "fs.gone");
    }

    #[tokio::test]
    async fn a_protected_link_says_nothing_until_the_password_is_cleared() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1).with_password(1, "hunter2")));
        let app = public_router(state);
        let j = body_json(get_uri(&app, "/s/tok", None).await).await;
        assert_eq!(j["protected"], true);
        assert!(j.get("name").is_none(), "a locked link disclosed its filename");
    }

    #[tokio::test]
    async fn a_wrong_password_is_byte_for_byte_the_same_as_an_unknown_token() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1).with_password(1, "hunter2")));
        let app = public_router(state);

        let wrong = post_json(&app, "/s/tok/auth", r#"{"password":"nope"}"#, None).await;
        let unknown = post_json(&app, "/s/no-such-token/auth", r#"{"password":"nope"}"#, None).await;

        assert_eq!(wrong.status(), unknown.status());
        assert_eq!(wrong.status(), StatusCode::NOT_FOUND);
        // Same body too: a differing `message` would be the oracle the equal
        // status code was meant to close.
        assert_eq!(body_json(wrong).await, body_json(unknown).await);
    }

    #[tokio::test]
    async fn the_right_password_issues_a_scoped_link_cookie() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1).with_password(1, "hunter2")));
        let app = public_router(state);
        let resp = post_json(&app, "/s/tok/auth", r#"{"password":"hunter2"}"#, None).await;
        assert_eq!(resp.status(), StatusCode::OK);
        let cookie = resp.headers().get("set-cookie").unwrap().to_str().unwrap().to_string();
        assert!(cookie.starts_with("__Host-sc_link="));
        // Scoped to this one link, so it cannot be replayed against another.
        assert!(cookie.contains("Path=/s/tok"));
        assert!(cookie.contains("HttpOnly"));
        assert!(cookie.contains("Secure"));

        let value = cookie.split(';').next().unwrap().to_string();
        let j = body_json(get_uri(&app, "/s/tok", Some(&value)).await).await;
        assert_eq!(j["name"], "shared.txt");
    }

    #[tokio::test]
    async fn password_attempts_are_rate_limited_per_token() {
        let (mut state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1).with_password(1, "hunter2")));
        state.link_rate = Arc::new(crate::rate_limit::KeyedTokenBucket::new(3, std::time::Duration::from_secs(3600)));
        let app = public_router(state);

        for _ in 0..3 {
            assert_eq!(post_json(&app, "/s/tok/auth", r#"{"password":"x"}"#, None).await.status(), StatusCode::NOT_FOUND);
        }
        let limited = post_json(&app, "/s/tok/auth", r#"{"password":"x"}"#, None).await;
        assert_eq!(limited.status(), StatusCode::TOO_MANY_REQUESTS);
        assert!(limited.headers().get("Retry-After").is_some());
    }

    #[tokio::test]
    async fn a_download_mints_a_signed_url_issued_to_nobody() {
        let core = Arc::new(LinkMockCore::with_link("tok", 1));
        let (state, _dir) = test_state_with_core(core.clone());
        let keys = state.signed_url_keys.clone();
        let app = public_router(state);

        let resp = post_json(&app, "/s/tok/download", "", None).await;
        assert_eq!(resp.status(), StatusCode::OK);
        let url = body_json(resp).await["url"].as_str().unwrap().to_string();
        let token = url.rsplit('/').next().unwrap().to_string();
        let claim = content::verify(&keys.lock(), &token, None).expect("signed by the same machinery");
        assert_eq!(claim.sub, 0, "a public-link download must be issued to `sub = 0`");
        assert_eq!(claim.fid, 77);
        assert_eq!(core.downloads.load(std::sync::atomic::Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_download_against_a_spent_link_is_410_and_does_not_count() {
        let core = Arc::new(LinkMockCore::with_link("tok", 1).kill(1));
        let (state, _dir) = test_state_with_core(core.clone());
        let app = public_router(state);
        assert_eq!(post_json(&app, "/s/tok/download", "", None).await.status(), StatusCode::GONE);
        assert_eq!(core.downloads.load(std::sync::atomic::Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn a_drop_link_accepts_an_upload_but_refuses_a_download() {
        let core = Arc::new(LinkMockCore::with_link("tok", 1).as_drop());
        let (state, _dir) = test_state_with_core(core.clone());
        let app = public_router(state);

        assert_eq!(post_json(&app, "/s/tok/download", "", None).await.status(), StatusCode::FORBIDDEN);

        let resp = post_json(&app, "/s/tok/drop?name=a.txt", "hello", None).await;
        assert_eq!(resp.status(), StatusCode::CREATED);
        assert_eq!(core.dropped.lock().len(), 1);
        assert_eq!(core.dropped.lock()[0].0, "a.txt");

        // And it lists nothing, even though the target is a directory.
        let j = body_json(get_uri(&app, "/s/tok", None).await).await;
        assert_eq!(j["drop"], true);
        assert!(j.get("entries").is_none(), "a file-drop link listed its directory");
    }

    /// The public page cannot reach `/api/capabilities` (it never imports the
    /// app bundle), so the only way it can refuse an oversized file *before*
    /// uploading it is if this response carries the limit.
    #[tokio::test]
    async fn a_drop_link_advertises_its_upload_limit() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1).as_drop()));
        let limit = state.cfg.body_limit_bytes;
        let app = public_router(state);
        let j = body_json(get_uri(&app, "/s/tok", None).await).await;
        assert_eq!(j["max_upload_bytes"], serde_json::json!(limit));

        // A read/download link has nothing to upload, so it is not told.
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1)));
        let app = public_router(state);
        let j = body_json(get_uri(&app, "/s/tok", None).await).await;
        assert!(j.get("max_upload_bytes").is_none(), "a download link was told an upload limit");
    }

    /// The limit above has to be the one actually enforced. This goes through
    /// `build_router` rather than `public_router` on purpose: what it proves
    /// is that `/s/{token}/drop` really does sit inside the router half that
    /// gets `RequestBodyLimitLayer` — `/api/uploads/**` deliberately does not,
    /// and a drop route on the wrong side would buffer anything anonymous
    /// callers sent.
    #[tokio::test]
    async fn a_drop_upload_over_the_body_limit_never_reaches_the_core() {
        let core = Arc::new(LinkMockCore::with_link("tok", 1).as_drop());
        let (mut state, _dir) = test_state_with_core(core.clone());
        state.cfg = Arc::new(crate::config::HttpConfig { body_limit_bytes: 8, ..Default::default() });
        let app = crate::build_router(state);

        let req = HttpRequest::builder()
            .method("POST")
            .uri("/s/tok/drop?name=big.bin")
            .header("Host", "localhost")
            .body(Body::from(vec![b'x'; 64]))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::PAYLOAD_TOO_LARGE);
        assert!(core.dropped.lock().is_empty(), "an over-limit body was written anyway");
    }

    #[tokio::test]
    async fn share_crud_requires_a_session() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1)));
        let app = shares_router(state);
        for (method, uri) in [("GET", "/api/shares"), ("POST", "/api/shares"), ("DELETE", "/api/shares/1")] {
            let req = HttpRequest::builder()
                .method(method)
                .uri(uri)
                .header("content-type", "application/json")
                .body(Body::from(r#"{"path":"/x"}"#))
                .unwrap();
            let resp = app.clone().oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::UNAUTHORIZED, "{method} {uri}");
        }
    }

    #[tokio::test]
    async fn share_crud_reports_not_implemented_rather_than_dropping_a_create() {
        // `UnimplementedCore` has no link store. The contract is that this
        // *refuses* — a client told "created" that then cannot find the share
        // is worse off than one told it cannot be created.
        let (state, _dir) = crate::testutil::test_state();
        let app = shares_router(state);
        let req = HttpRequest::builder()
            .method("POST")
            .uri("/api/shares")
            .header("content-type", "application/json")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::from(r#"{"path":"/x"}"#))
            .unwrap();
        let resp = app.clone().oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::NOT_IMPLEMENTED);
    }

    #[tokio::test]
    async fn every_public_link_response_is_noindex() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1)));
        let app = crate::build_router(state);
        for uri in ["/s/tok", "/s/nope"] {
            let req = HttpRequest::builder().uri(uri).header("Host", "localhost").body(Body::empty()).unwrap();
            let resp = app.clone().oneshot(req).await.unwrap();
            assert_eq!(
                resp.headers().get("X-Robots-Tag").map(|v| v.to_str().unwrap()),
                Some("noindex, nofollow"),
                "{uri}"
            );
        }
    }

    #[tokio::test]
    async fn public_link_routes_need_no_session() {
        let (state, _dir) = test_state_with_core(Arc::new(LinkMockCore::with_link("tok", 1)));
        let app = crate::build_router(state);
        let req = HttpRequest::builder().uri("/s/tok").header("Host", "localhost").body(Body::empty()).unwrap();
        assert_eq!(app.oneshot(req).await.unwrap().status(), StatusCode::OK);
    }

    // ---------------------------------------------------- content serving --

    use crate::testutil::{test_state_single_origin, test_state_with_content, test_state_with_search};

    struct MockContent {
        stat: crate::content_api::ContentStat,
        bytes: Vec<u8>,
        denied: bool,
    }

    impl crate::content_api::ContentApi for MockContent {
        fn stat_by_fid(&self, _fid: sc_vfs::ids::FileId) -> Result<crate::content_api::ContentStat, crate::core_api::CoreError> {
            Ok(self.stat.clone())
        }
        fn check_read(&self, _user: sc_vfs::ids::UserId, _fid: sc_vfs::ids::FileId) -> Result<(), crate::core_api::CoreError> {
            if self.denied {
                Err(crate::core_api::CoreError::Denied { by: None })
            } else {
                Ok(())
            }
        }
        fn open_stream(&self, _fid: sc_vfs::ids::FileId, range: Option<(u64, u64)>) -> Result<Box<dyn std::io::Read + Send>, crate::core_api::CoreError> {
            let (start, end) = range.unwrap_or((0, self.bytes.len() as u64 - 1));
            let slice = self.bytes[start as usize..=(end as usize)].to_vec();
            Ok(Box::new(std::io::Cursor::new(slice)))
        }
        fn thumbnail(
            &self,
            _fid: sc_vfs::ids::FileId,
            _w: u16,
            _h: u16,
            _etag8: [u8; 8],
        ) -> crate::content_api::BoxFuture<'static, Result<Vec<u8>, crate::core_api::CoreError>> {
            Box::pin(async { Ok(vec![9, 9, 9]) })
        }
    }

    fn content_req(uri_path: &str, range: Option<&str>) -> HttpRequest<Body> {
        let mut b = HttpRequest::builder().uri(uri_path).header("Host", "content.example.com");
        if let Some(r) = range {
            b = b.header(axum::http::header::RANGE, r);
        }
        b.body(Body::empty()).unwrap()
    }

    #[tokio::test]
    async fn content_get_serves_the_whole_file_for_attachment() {
        let etag = "abcdefgh1234";
        let content = Arc::new(MockContent {
            stat: crate::content_api::ContentStat { name: "a.txt".into(), size: 11, etag: etag.into() },
            bytes: b"hello world".to_vec(),
            denied: false,
        });
        let (state, _dir) = test_state_with_content(content);
        let claim = content::make_claim(42, content::etag8_of(etag), content::Disposition::Attachment, None, 1, None);
        let token = content::sign(&state.signed_url_keys.lock(), claim);
        let app = crate::build_router(state);
        let resp = app.oneshot(content_req(&format!("/c/{token}"), None)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        assert_eq!(&bytes[..], b"hello world");
    }

    #[tokio::test]
    async fn content_get_honours_range_for_stream() {
        let etag = "abcdefgh1234";
        let content = Arc::new(MockContent {
            stat: crate::content_api::ContentStat { name: "a.txt".into(), size: 11, etag: etag.into() },
            bytes: b"hello world".to_vec(),
            denied: false,
        });
        let (state, _dir) = test_state_with_content(content);
        let claim = content::make_claim(42, content::etag8_of(etag), content::Disposition::Stream, None, 1, None);
        let token = content::sign(&state.signed_url_keys.lock(), claim);
        let app = crate::build_router(state);
        let resp = app.oneshot(content_req(&format!("/c/{token}"), Some("bytes=6-10"))).await.unwrap();
        assert_eq!(resp.status(), StatusCode::PARTIAL_CONTENT);
        assert_eq!(resp.headers().get(axum::http::header::CONTENT_RANGE).unwrap(), "bytes 6-10/11");
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        assert_eq!(&bytes[..], b"world");
    }

    #[tokio::test]
    async fn content_get_etag_mismatch_is_410() {
        let content = Arc::new(MockContent {
            stat: crate::content_api::ContentStat { name: "a.txt".into(), size: 5, etag: "newetag00000".into() },
            bytes: b"aaaaa".to_vec(),
            denied: false,
        });
        let (state, _dir) = test_state_with_content(content);
        // Signed against a *different* etag than the mock currently reports.
        let claim = content::make_claim(42, content::etag8_of("oldetag00000"), content::Disposition::Attachment, None, 1, None);
        let token = content::sign(&state.signed_url_keys.lock(), claim);
        let app = crate::build_router(state);
        let resp = app.oneshot(content_req(&format!("/c/{token}"), None)).await.unwrap();
        assert_eq!(resp.status(), StatusCode::GONE);
    }

    #[tokio::test]
    async fn fs_link_refuses_to_mint_a_url_for_a_file_the_caller_cannot_read() {
        let content = Arc::new(MockContent {
            stat: crate::content_api::ContentStat { name: "a.txt".into(), size: 5, etag: "e".into() },
            bytes: b"aaaaa".to_vec(),
            denied: true,
        });
        let (state, _dir) = test_state_with_content(content);
        let app = Router::new().route("/api/fs/link", post(fs_link)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/fs/link")
            .method("POST")
            .header("content-type", "application/json")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::from(r#"{"fid":42}"#))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    /// Regression: `.dev/sc.toml` and production both run with
    /// `content_hosts` empty (single-origin fallback), and `fs_link` used to build its URL with
    /// `format!("https://{host}/c/{token}")` where `host` was `""`. That
    /// produces the literal string `https:///c/<token>`, which WHATWG URL
    /// parsing resolves to host `c` (a real, unrelated, unreachable domain)
    /// rather than "no host" — reproduced against the live dev server as a
    /// same-tab navigation landing on `chrome-error://chromewebdata/`. This
    /// fails on the pre-fix code (asserting the string does *not* contain
    /// `https:///`) and passes now that `fs_link` goes through
    /// `content::content_url`, which returns a host-relative `/c/{token}`
    /// path instead.
    #[tokio::test]
    async fn fs_link_with_no_content_host_mints_a_relative_url_not_triple_slash() {
        let content = Arc::new(MockContent {
            stat: crate::content_api::ContentStat { name: "a.txt".into(), size: 5, etag: "e".into() },
            bytes: b"aaaaa".to_vec(),
            denied: false,
        });
        let (state, _dir) = test_state_single_origin(content);
        assert!(state.cfg.content_hosts.is_empty(), "test must model the single-origin deployment");
        let app = Router::new().route("/api/fs/link", post(fs_link)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/fs/link")
            .method("POST")
            .header("content-type", "application/json")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::from(r#"{"fid":42}"#))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let body: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let url = body["url"].as_str().unwrap();
        assert!(!url.contains("https:///"), "must not be a triple-slash URL, got {url}");
        assert!(url.starts_with("/c/"), "must be a host-relative /c/ path, got {url}");
    }

    // -------------------------------------------------------------- search --

    struct MockSearch;
    impl crate::search_api::SearchApi for MockSearch {
        fn search_tier(&self, _user: sc_vfs::ids::UserId, _q: &crate::search_api::SearchQuery) -> crate::search_limits::SearchTier {
            crate::search_limits::SearchTier::Fast
        }
        fn search(&self, _user: sc_vfs::ids::UserId, q: &crate::search_api::SearchQuery) -> Result<crate::search_api::SearchOutcome, crate::core_api::CoreError> {
            Ok(crate::search_api::SearchOutcome {
                hits: vec![crate::search_api::SearchHit {
                    path: format!("/found/{}", q.text),
                    name: q.text.clone(),
                    is_dir: false,
                    size: Some(3),
                    mtime_ns: Some(0),
                    score: 1.0,
                }],
                completeness: crate::search_api::SearchCompleteness::Full,
            })
        }
        fn search_stream(
            &self,
            _user: sc_vfs::ids::UserId,
            _q: &crate::search_api::SearchQuery,
            on_hit: &mut dyn FnMut(crate::search_api::SearchHit) -> bool,
        ) -> crate::search_api::SearchCompleteness {
            on_hit(crate::search_api::SearchHit { path: "/x".into(), name: "x".into(), is_dir: false, size: None, mtime_ns: None, score: 1.0 });
            crate::search_api::SearchCompleteness::Full
        }
    }

    #[tokio::test]
    async fn search_returns_hits_from_the_backend() {
        let (state, _dir) = test_state_with_search(Arc::new(MockSearch));
        let app = Router::new().route("/api/search", get(search)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/search?q=photo")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(json["hits"][0]["name"], "photo");
    }

    #[tokio::test]
    async fn empty_query_short_circuits_without_touching_the_backend() {
        let (state, _dir) = test_state_with_search(Arc::new(MockSearch));
        let app = Router::new().route("/api/search", get(search)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/search?q=")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let json: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(json["hits"].as_array().unwrap().len(), 0);
    }

    #[tokio::test]
    async fn search_stream_emits_hit_then_done_events() {
        let (state, _dir) = test_state_with_search(Arc::new(MockSearch));
        let app = Router::new().route("/api/search/stream", get(search_stream)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/search/stream?q=x")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let text = String::from_utf8_lossy(&bytes);
        assert!(text.contains("event: hit"), "{text}");
        assert!(text.contains("event: done"), "{text}");
    }

    /// The bug §2/§3 of this refactor fix: an exhausted concurrency budget
    /// (search's per-tier cap, or archive's global cap) must reject with
    /// `429` + `Retry-After` rather than queue silently or run unbounded.
    #[tokio::test]
    async fn search_concurrency_exhausted_returns_429_with_retry_after() {
        let (mut state, _dir) = test_state_with_search(Arc::new(MockSearch));
        state.search_concurrency = Arc::new(crate::search_limits::SearchConcurrency::new(&crate::search_limits::SearchLimitsConfig {
            max_concurrent_fast: 1,
            ..Default::default()
        }));
        // Hold the only fast-tier permit for the duration of the request.
        let _held = state.search_concurrency.try_acquire(crate::search_limits::SearchTier::Fast).unwrap();

        let app = Router::new().route("/api/search", get(search)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/search?q=photo")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::TOO_MANY_REQUESTS);
        assert!(resp.headers().get("Retry-After").is_some());
    }

    #[tokio::test]
    async fn archive_concurrency_exhausted_returns_429_with_retry_after() {
        let (mut state, _dir) = test_state_with_core(Arc::new(MockCore::new(vec![])));
        state.archive_concurrency = Arc::new(crate::state::ResizableSemaphore::new(1));
        let _held = state.archive_concurrency.current().try_acquire_owned().unwrap();

        let app = Router::new().route("/api/fs/archive", post(fs_archive)).with_state(state);
        let req = HttpRequest::builder()
            .uri("/api/fs/archive")
            .method("POST")
            .header("content-type", "application/json")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .body(Body::from(r#"{"paths":["/a"]}"#))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::TOO_MANY_REQUESTS);
        assert!(resp.headers().get("Retry-After").is_some());
    }

    // ------------------------------------------------ TUS checksum parsing --

    /// `Tus-Extension` advertises `checksum`, and until the handler read the
    /// header the guarantee was imaginary: a client sent a digest, nothing
    /// compared it, and the upload "verified". These pin the parse — the
    /// handler refuses on `None` rather than writing unverified bytes, so a
    /// parser that quietly returns `None` for a *valid* header would silently
    /// break real clients, and one that returns `Some` for a bad header would
    /// restore the original bug.
    #[test]
    fn upload_checksum_parses_both_advertised_algorithms() {
        use crate::upload_api::TusChecksumAlgo;
        let c = parse_upload_checksum("blake3 3q2+7w==").expect("valid blake3");
        assert_eq!(c.algo, TusChecksumAlgo::Blake3);
        assert_eq!(c.digest, vec![0xde, 0xad, 0xbe, 0xef]);

        let c = parse_upload_checksum("crc32c 3q2+7w==").expect("valid crc32c");
        assert_eq!(c.algo, TusChecksumAlgo::Crc32c);

        // Algorithm names are case-insensitive on the wire.
        assert!(parse_upload_checksum("BLAKE3 3q2+7w==").is_some());
    }

    #[test]
    fn upload_checksum_refuses_anything_it_cannot_verify() {
        // An algorithm we do not implement must not be waved through as if
        // it had been checked.
        assert!(parse_upload_checksum("sha1 3q2+7w==").is_none());
        assert!(parse_upload_checksum("md5 3q2+7w==").is_none());
        // Structurally malformed.
        assert!(parse_upload_checksum("blake3").is_none());
        assert!(parse_upload_checksum("").is_none());
        assert!(parse_upload_checksum("blake3 not-base64!!").is_none());
        // A syntactically fine header carrying no digest at all.
        assert!(parse_upload_checksum("blake3 ").is_none());
    }

    // --------------------------------------------------- admin: users --

    mod admin_users {
        use super::*;

        fn router(state: AppState) -> Router {
            Router::new()
                .route("/api/admin/users", get(admin_list_users).post(admin_create_user))
                .route("/api/admin/users/{id}", patch(admin_patch_user).delete(admin_delete_user))
                .with_state(state)
        }

        fn as_principal(uid: sc_vfs::UserId, req: axum::http::request::Builder) -> axum::http::request::Builder {
            req.extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
        }

        async fn json_body(resp: axum::response::Response) -> serde_json::Value {
            let b = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&b).unwrap()
        }

        /// A state with one bootstrapped administrator and the router wired
        /// to the four handlers under test. Returns the admin's `UserId`
        /// alongside so tests can act as it.
        fn admin_state() -> (AppState, tempfile::TempDir, sc_vfs::UserId) {
            let (state, dir) = test_state_with_core(Arc::new(UnimplementedCoreForAdmin));
            let admin = state.auth.create_user("root", &secrecy::SecretString::from("correct horse battery")).unwrap();
            state.auth.set_admin(admin, true).unwrap();
            (state, dir, admin)
        }

        // `test_state_with_core` needs a `CoreApi`; these tests never touch
        // it, so the crate's own placeholder does fine.
        use crate::core_api::UnimplementedCore as UnimplementedCoreForAdmin;

        #[tokio::test]
        async fn a_non_admin_is_refused_on_every_admin_users_route() {
            let (state, _dir) = test_state_with_core(Arc::new(UnimplementedCoreForAdmin));
            let plain = state.auth.create_user("plain", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);

            let list = as_principal(plain, HttpRequest::builder().uri("/api/admin/users"))
                .body(Body::empty())
                .unwrap();
            let resp = app.clone().oneshot(list).await.unwrap();
            assert_eq!(resp.status(), StatusCode::FORBIDDEN);

            let create = as_principal(plain, HttpRequest::builder().uri("/api/admin/users").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"x","password":"correct horse battery"}"#))
                .unwrap();
            assert_eq!(app.clone().oneshot(create).await.unwrap().status(), StatusCode::FORBIDDEN);
        }

        #[tokio::test]
        async fn admin_lists_every_account() {
            let (state, _dir, admin) = admin_state();
            let second = state.auth.create_user("second", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);

            let req = as_principal(admin, HttpRequest::builder().uri("/api/admin/users")).body(Body::empty()).unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            let json = json_body(resp).await;
            let arr = json.as_array().unwrap();
            assert_eq!(arr.len(), 2);
            assert!(arr.iter().any(|u| u["id"] == admin.get() && u["is_admin"] == true));
            assert!(arr.iter().any(|u| u["id"] == second.get() && u["is_admin"] == false));
        }

        #[tokio::test]
        async fn admin_creates_a_plain_user_never_an_admin() {
            let (state, _dir, admin) = admin_state();
            let app = router(state.clone());
            let req = as_principal(admin, HttpRequest::builder().uri("/api/admin/users").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"newperson","password":"correct horse battery"}"#))
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::CREATED);
            let json = json_body(resp).await;
            assert_eq!(json["name"], "newperson");
            assert_eq!(json["is_admin"], false);
            assert_eq!(json["disabled"], false);
        }

        #[tokio::test]
        async fn creating_with_a_taken_name_is_409() {
            let (state, _dir, admin) = admin_state();
            state.auth.create_user("dupe", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);
            let req = as_principal(admin, HttpRequest::builder().uri("/api/admin/users").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"dupe","password":"correct horse battery"}"#))
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::CONFLICT);
        }

        #[tokio::test]
        async fn creating_with_a_short_password_is_422_and_quotes_the_minimum() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let req = as_principal(admin, HttpRequest::builder().uri("/api/admin/users").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"shorty","password":"short1"}"#))
                .unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::UNPROCESSABLE_ENTITY);
            let json = json_body(resp).await;
            assert_eq!(json["error"]["detail"]["min_length"], 10);
        }

        #[tokio::test]
        async fn admin_disables_and_re_enables_another_account() {
            let (state, _dir, admin) = admin_state();
            let target = state.auth.create_user("target", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);

            let disable = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", target.get())).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"disabled":true}"#))
                .unwrap();
            let resp = app.clone().oneshot(disable).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            assert_eq!(json_body(resp).await["disabled"], true);

            let enable = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", target.get())).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"disabled":false}"#))
                .unwrap();
            let resp2 = app.oneshot(enable).await.unwrap();
            assert_eq!(json_body(resp2).await["disabled"], false);
        }

        /// Disabling an account must close every WebSocket it has open —
        /// otherwise the tab keeps working past the moment access ended.
        #[tokio::test]
        async fn disabling_an_account_revokes_its_open_sockets() {
            let (state, _dir, admin) = admin_state();
            let target = state.auth.create_user("target", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let (_conn_id, mut rx) = state.ws.connect(target);
            let app = router(state);

            let disable = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", target.get())).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"disabled":true}"#))
                .unwrap();
            assert_eq!(app.oneshot(disable).await.unwrap().status(), StatusCode::OK);

            assert_eq!(rx.try_recv().unwrap(), crate::ws::ServerMsg::Revoked);
        }

        /// Re-enabling must not send `revoked` — the account regained access,
        /// nothing was taken away.
        #[tokio::test]
        async fn re_enabling_an_account_does_not_revoke_sockets() {
            let (state, _dir, admin) = admin_state();
            let target = state.auth.create_user("target", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let (_conn_id, mut rx) = state.ws.connect(target);
            let app = router(state);

            let enable = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", target.get())).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"disabled":false}"#))
                .unwrap();
            assert_eq!(app.oneshot(enable).await.unwrap().status(), StatusCode::OK);

            assert!(rx.try_recv().is_err(), "enabling must not revoke a socket that was never invalid");
        }

        /// The rule the task called out explicitly: disabling, demoting or
        /// deleting the deployment's *only* active administrator must be
        /// refused, not merely discouraged.
        #[tokio::test]
        async fn the_last_admin_cannot_be_disabled_or_deleted() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);

            let disable = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", admin.get())).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"disabled":true}"#))
                .unwrap();
            let resp = app.clone().oneshot(disable).await.unwrap();
            assert_eq!(resp.status(), StatusCode::CONFLICT);
            let json = json_body(resp).await;
            assert_eq!(json["error"]["code"], "admin.last_admin");

            let delete = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", admin.get())).method("DELETE"))
                .body(Body::empty())
                .unwrap();
            let resp2 = app.oneshot(delete).await.unwrap();
            assert_eq!(resp2.status(), StatusCode::CONFLICT);
        }

        #[tokio::test]
        async fn admin_deletes_a_non_admin_account() {
            let (state, _dir, admin) = admin_state();
            let target = state.auth.create_user("throwaway", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state.clone());

            let delete = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", target.get())).method("DELETE"))
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(delete).await.unwrap();
            assert_eq!(resp.status(), StatusCode::NO_CONTENT);
            assert!(state.auth.find_user_by_id(target).unwrap().is_none());
        }

        /// Deleting an account is permanent — every socket it has open must
        /// be told immediately, not left to find out on its next request.
        #[tokio::test]
        async fn deleting_an_account_revokes_its_open_sockets() {
            let (state, _dir, admin) = admin_state();
            let target = state.auth.create_user("throwaway", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let (_conn_id, mut rx) = state.ws.connect(target);
            let app = router(state);

            let delete = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/users/{}", target.get())).method("DELETE"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(delete).await.unwrap().status(), StatusCode::NO_CONTENT);

            assert_eq!(rx.try_recv().unwrap(), crate::ws::ServerMsg::Revoked);
        }

        #[tokio::test]
        async fn deleting_an_unknown_id_is_404() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let delete = as_principal(admin, HttpRequest::builder().uri("/api/admin/users/999999").method("DELETE"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(delete).await.unwrap().status(), StatusCode::NOT_FOUND);
        }
    }

    // ---------------------------------------------------- server settings --

    /// This surface can change `bind`, `allowed_origins` and
    /// `trusted_proxies`, and can trigger a restart — a gating hole here is a
    /// full compromise, so every one of the 9 routes gets its own assertion
    /// rather than relying on one representative sample.
    mod server_settings {
        use super::*;

        fn router(state: AppState) -> Router {
            Router::new()
                .route("/api/admin/server-settings", get(admin_get_server_settings))
                .route("/api/admin/server-settings/smb", patch(admin_set_smb_settings))
                .route("/api/admin/server-settings/search", patch(admin_set_search_settings))
                .route("/api/admin/server-settings/archive", patch(admin_set_archive_settings))
                .route("/api/admin/server-settings/network", patch(admin_set_network_settings))
                .route("/api/admin/server-settings/db", patch(admin_set_db_settings))
                .route("/api/admin/server-settings/symlink-policy", patch(admin_set_symlink_policy_settings))
                .route("/api/admin/server-settings/homes", patch(admin_set_homes_settings))
                .route("/api/admin/server-settings/watch", patch(admin_set_watch_settings))
                .route("/api/admin/server-settings/paths", patch(admin_set_paths_settings))
                .route("/api/admin/server-settings/restart", post(admin_restart_server))
                .with_state(state)
        }

        fn as_principal(uid: sc_vfs::UserId, req: axum::http::request::Builder) -> axum::http::request::Builder {
            req.extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
        }

        async fn json_body(resp: axum::response::Response) -> serde_json::Value {
            let b = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&b).unwrap()
        }

        fn admin_state() -> (AppState, tempfile::TempDir, sc_vfs::UserId) {
            let (state, dir) = test_state_with_core(Arc::new(UnimplementedCoreForSettings));
            let admin = state.auth.create_user("root", &secrecy::SecretString::from("correct horse battery")).unwrap();
            state.auth.set_admin(admin, true).unwrap();
            (state, dir, admin)
        }

        use crate::core_api::UnimplementedCore as UnimplementedCoreForSettings;

        #[tokio::test]
        async fn a_non_admin_is_refused_on_every_server_settings_route() {
            let (state, _dir) = test_state_with_core(Arc::new(UnimplementedCoreForSettings));
            let plain = state.auth.create_user("plain", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);

            let get = as_principal(plain, HttpRequest::builder().uri("/api/admin/server-settings"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.clone().oneshot(get).await.unwrap().status(), StatusCode::FORBIDDEN);

            let patches: &[(&str, &str)] = &[
                ("/api/admin/server-settings/smb", r#"{"enabled":false,"workgroup":"WORKGROUP","service_user":"sc-smb","allow_public_bind":false,"totp_policy":"require_separate","service_uid":1000,"service_gid":1000}"#),
                ("/api/admin/server-settings/search", r#"{"max_concurrent_fast":4,"max_concurrent_slow":2,"walk_deadline_fast_ms":100,"walk_deadline_slow_ms":500,"rate_per_minute":30}"#),
                ("/api/admin/server-settings/archive", r#"{"max_concurrent":2}"#),
                ("/api/admin/server-settings/network", r#"{"bind":"127.0.0.1:8080","app_hosts":[],"content_hosts":[],"allowed_origins":[],"trusted_proxies":[],"compat_canonical_url":null}"#),
                ("/api/admin/server-settings/db", r#"{"size_guard":true,"max_bytes":1000,"min_free_bytes":100}"#),
                ("/api/admin/server-settings/symlink-policy", r#"{"policy":"deny"}"#),
                ("/api/admin/server-settings/homes", r#"{"enabled":false,"root":null}"#),
                ("/api/admin/server-settings/watch", r#"{"backend":"auto","hot_set_max":4096,"full_threshold":50000}"#),
                ("/api/admin/server-settings/paths", r#"{"data_dir":"/var/lib/sc","master_key_file":null,"smb_config_dir":"/etc/sc-smb"}"#),
            ];
            for (path, body) in patches {
                let req = as_principal(plain, HttpRequest::builder().uri(*path).method("PATCH"))
                    .header("content-type", "application/json")
                    .body(Body::from(*body))
                    .unwrap();
                let resp = app.clone().oneshot(req).await.unwrap();
                assert_eq!(resp.status(), StatusCode::FORBIDDEN, "PATCH {path} must 403 for a non-admin");
            }

            let restart = as_principal(plain, HttpRequest::builder().uri("/api/admin/server-settings/restart").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"force":false}"#))
                .unwrap();
            assert_eq!(app.oneshot(restart).await.unwrap().status(), StatusCode::FORBIDDEN);
        }

        #[tokio::test]
        async fn an_admin_can_read_the_settings_snapshot() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let get = as_principal(admin, HttpRequest::builder().uri("/api/admin/server-settings"))
                .body(Body::empty())
                .unwrap();
            let resp = app.oneshot(get).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            let json = json_body(resp).await;
            assert!(json["fields"].is_array());
            assert!(json["smb_public_bind_warning"].is_boolean());
        }

        /// Refuses with the counts, so the settings screen can show the
        /// admin exactly what is about to be interrupted, rather than a
        /// generic "busy" with nothing to decide from.
        #[tokio::test]
        async fn restart_without_force_refuses_while_a_job_is_running() {
            let (state, _dir, admin) = admin_state();
            let owner = sc_vfs::UserId::new(1);
            assert!(state.jobs.insert(crate::state::JobStatus::new_running("job-1".into(), owner, crate::state::JobKind::Copy, 10)));
            let app = router(state);

            let restart = as_principal(admin, HttpRequest::builder().uri("/api/admin/server-settings/restart").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"force":false}"#))
                .unwrap();
            let resp = app.oneshot(restart).await.unwrap();
            assert_eq!(resp.status(), StatusCode::CONFLICT);
            let json = json_body(resp).await;
            assert_eq!(json["error"]["code"], "restart.busy");
            assert_eq!(json["error"]["detail"]["running_jobs"], 1);
            assert_eq!(json["error"]["detail"]["active_uploads"], 0);
        }

        /// `force: true` is the deliberate override — the busy gate must not
        /// block it, whatever `SettingsApi::request_restart` itself then
        /// does with the request (the default test backend has no real
        /// implementation, so it 500s past the gate rather than 202 — the
        /// gate having let it through at all is what this asserts).
        #[tokio::test]
        async fn restart_with_force_bypasses_the_busy_check() {
            let (state, _dir, admin) = admin_state();
            let owner = sc_vfs::UserId::new(1);
            assert!(state.jobs.insert(crate::state::JobStatus::new_running("job-1".into(), owner, crate::state::JobKind::Copy, 10)));
            let app = router(state);

            let restart = as_principal(admin, HttpRequest::builder().uri("/api/admin/server-settings/restart").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"force":true}"#))
                .unwrap();
            let resp = app.oneshot(restart).await.unwrap();
            assert_ne!(resp.status(), StatusCode::CONFLICT);
        }

        #[tokio::test]
        async fn restart_without_force_proceeds_past_the_gate_when_nothing_is_in_flight() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let restart = as_principal(admin, HttpRequest::builder().uri("/api/admin/server-settings/restart").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"force":false}"#))
                .unwrap();
            let resp = app.oneshot(restart).await.unwrap();
            assert_ne!(resp.status(), StatusCode::CONFLICT);
        }
    }

    // ------------------------------------------------------------ logout --

    mod auth_logout_tests {
        use super::*;

        fn router(state: AppState) -> Router {
            Router::new().route("/api/auth/logout", post(auth_logout)).with_state(state)
        }

        /// Logging out closes every WebSocket this user has open, not just
        /// the one behind this cookie — / `WsHub::revoke_user`.
        #[tokio::test]
        async fn logout_revokes_every_socket_of_that_user() {
            let (state, _dir) = test_state_with_core(Arc::new(crate::core_api::UnimplementedCore));
            let uid = state.auth.create_user("alice", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let (_id_a1, mut rx_a1) = state.ws.connect(uid); // tab A
            let (_id_a2, mut rx_a2) = state.ws.connect(uid); // tab B, same user
            let other = state.auth.create_user("bob", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let (_id_b, mut rx_b) = state.ws.connect(other);
            let app = router(state);

            let req = HttpRequest::builder()
                .method("POST")
                .uri("/api/auth/logout")
                .extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(req).await.unwrap().status(), StatusCode::NO_CONTENT);

            assert_eq!(rx_a1.try_recv().unwrap(), crate::ws::ServerMsg::Revoked);
            assert_eq!(rx_a2.try_recv().unwrap(), crate::ws::ServerMsg::Revoked);
            assert!(rx_b.try_recv().is_err(), "logging out must not touch another user's sockets");
        }
    }

    // --------------------------------------------------- revoke one session --

    mod auth_revoke_session_tests {
        use super::*;

        fn router(state: AppState) -> Router {
            Router::new().route("/api/auth/sessions/{id_hash}", delete(auth_revoke_session)).with_state(state)
        }

        /// Item 54's whole point: revoking one specific session (the
        /// sessions UI only ever offers this for a non-current session —
        /// `SessionsSection.svelte`'s `{#if !s.current}`) must close only
        /// that session's socket, leaving every other live session of the
        /// same account — including the one performing the revoke — alone.
        #[tokio::test]
        async fn revoking_one_session_closes_only_its_own_socket() {
            let (state, _dir) = test_state_with_core(Arc::new(crate::core_api::UnimplementedCore));
            let uid = state.auth.create_user("erin", &secrecy::SecretString::from("correct horse battery")).unwrap();

            // Two real sessions for the same account: "this laptop" (doing
            // the revoking) and "an old phone" (being revoked).
            let laptop = state.auth.create_session(uid, "127.0.0.1".parse().unwrap(), "laptop", sc_auth::AMR_PASSWORD).unwrap();
            let phone = state.auth.create_session(uid, "127.0.0.1".parse().unwrap(), "phone", sc_auth::AMR_PASSWORD).unwrap();
            let phone_hash = sc_auth::token_hash_hex(&phone.0);

            let (_laptop_conn, mut laptop_rx) =
                state.ws.connect_with_session(uid, Some(sc_auth::token_hash_hex(&laptop.0)));
            let (_phone_conn, mut phone_rx) = state.ws.connect_with_session(uid, Some(phone_hash.clone()));

            let app = router(state);
            let req = HttpRequest::builder()
                .method("DELETE")
                .uri(format!("/api/auth/sessions/{phone_hash}"))
                .extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(req).await.unwrap().status(), StatusCode::NO_CONTENT);

            assert_eq!(phone_rx.try_recv().unwrap(), crate::ws::ServerMsg::Revoked);
            assert!(laptop_rx.try_recv().is_err(), "revoking the phone session must not touch the laptop's socket");
        }

        /// An unknown/already-revoked hash 404s and must not send `revoked`
        /// to anything — there is nothing to revoke.
        #[tokio::test]
        async fn revoking_an_unknown_hash_is_404_and_touches_no_socket() {
            let (state, _dir) = test_state_with_core(Arc::new(crate::core_api::UnimplementedCore));
            let uid = state.auth.create_user("frank", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let (_conn, mut rx) = state.ws.connect(uid);

            let app = router(state);
            let req = HttpRequest::builder()
                .method("DELETE")
                .uri(format!("/api/auth/sessions/{}", "ab".repeat(32)))
                .extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(req).await.unwrap().status(), StatusCode::NOT_FOUND);

            assert!(rx.try_recv().is_err());
        }
    }

    /// `/api/admin/shares` and `/api/admin/grants*` — the surface the project
    /// owner's complaint was about: after creating a user there was no route
    /// at all to decide which folders they get. These tests exercise the
    /// HTTP wiring (routing, admin gating, wire shapes, the "at least one
    /// bit" and "unknown id" refusals) against `testutil::GrantMockCore`; the
    /// evaluation algorithm and persistence themselves are `sc-acl`'s and
    /// `sc-core::acl_store`'s to test (see `crates/sc-acl/src/tests.rs` and
    /// `crates/sc-core/src/tests_acl_store.rs` — the latter has the exact
    /// three properties the task called out: no grant -> no roots, a subpath
    /// grant exposes only that subtree, `deny` beats `allow`).
    mod admin_grants {
        use super::*;
        use crate::testutil::GrantMockCore;

        fn router(state: AppState) -> Router {
            Router::new()
                .route("/api/admin/shares", get(admin_list_shares).post(admin_create_share))
                .route("/api/admin/shares/{id}", patch(admin_update_share).delete(admin_delete_share))
                .route("/api/admin/grants", get(admin_list_grants).post(admin_create_grant))
                .route("/api/admin/grants/{id}", patch(admin_update_grant).delete(admin_delete_grant))
                .with_state(state)
        }

        fn as_principal(uid: sc_vfs::UserId, req: axum::http::request::Builder) -> axum::http::request::Builder {
            req.extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
        }

        async fn json_body(resp: axum::response::Response) -> serde_json::Value {
            let b = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&b).unwrap()
        }

        fn admin_state() -> (AppState, tempfile::TempDir, sc_vfs::UserId) {
            let (state, dir) = test_state_with_core(Arc::new(GrantMockCore::default()));
            let admin = state.auth.create_user("root", &secrecy::SecretString::from("correct horse battery")).unwrap();
            state.auth.set_admin(admin, true).unwrap();
            (state, dir, admin)
        }

        #[tokio::test]
        async fn a_non_admin_is_refused_on_every_grant_route() {
            let (state, _dir) = test_state_with_core(Arc::new(GrantMockCore::default()));
            let plain = state.auth.create_user("plain", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);

            let shares = as_principal(plain, HttpRequest::builder().uri("/api/admin/shares")).body(Body::empty()).unwrap();
            assert_eq!(app.clone().oneshot(shares).await.unwrap().status(), StatusCode::FORBIDDEN);

            let list = as_principal(plain, HttpRequest::builder().uri("/api/admin/grants")).body(Body::empty()).unwrap();
            assert_eq!(app.clone().oneshot(list).await.unwrap().status(), StatusCode::FORBIDDEN);

            let create = as_principal(plain, HttpRequest::builder().uri("/api/admin/grants").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"principal":{"kind":"user","id":1},"share":1,"subpath":"","allow":["read"],"deny":[],"inherit":true}"#))
                .unwrap();
            assert_eq!(app.oneshot(create).await.unwrap().status(), StatusCode::FORBIDDEN);
        }

        #[tokio::test]
        async fn admin_lists_registered_shares_for_the_picker() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let req = as_principal(admin, HttpRequest::builder().uri("/api/admin/shares")).body(Body::empty()).unwrap();
            let resp = app.oneshot(req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            let json = json_body(resp).await;
            assert_eq!(json[0]["id"], 1);
            assert_eq!(json[0]["name"], "docs");
        }

        /// The core round trip: create a grant naming one user and one
        /// folder, see it appear in the list, patch its permissions, then
        /// revoke it — the exact sequence "create a user, then decide which
        /// folders they get" needs at the HTTP layer.
        #[tokio::test]
        async fn create_list_update_delete_round_trip() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);

            let create = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"principal":{"kind":"user","id":42},"share":1,"subpath":"vacation","allow":["read","download"],"deny":[],"inherit":true,"label":"휴가 사진"}"#,
                ))
                .unwrap();
            let resp = app.clone().oneshot(create).await.unwrap();
            assert_eq!(resp.status(), StatusCode::CREATED);
            let created = json_body(resp).await;
            assert_eq!(created["principal"]["kind"], "user");
            assert_eq!(created["principal"]["id"], 42);
            assert_eq!(created["subpath"], "vacation");
            assert_eq!(created["label"], "휴가 사진");
            let mut allow: Vec<&str> = created["allow"].as_array().unwrap().iter().map(|v| v.as_str().unwrap()).collect();
            allow.sort();
            assert_eq!(allow, vec!["download", "read"]);
            let id = created["id"].as_u64().unwrap();

            let list = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants?user=42"))
                .body(Body::empty())
                .unwrap();
            let listed = json_body(app.clone().oneshot(list).await.unwrap()).await;
            assert_eq!(listed.as_array().unwrap().len(), 1, "filtered to this one user's grants");

            let patch = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/grants/{id}")).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"allow":["read","write"]}"#))
                .unwrap();
            let patched = json_body(app.clone().oneshot(patch).await.unwrap()).await;
            let mut patched_allow: Vec<&str> = patched["allow"].as_array().unwrap().iter().map(|v| v.as_str().unwrap()).collect();
            patched_allow.sort();
            assert_eq!(patched_allow, vec!["read", "write"]);

            let delete = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/grants/{id}")).method("DELETE"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.clone().oneshot(delete).await.unwrap().status(), StatusCode::NO_CONTENT);

            let list2 = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants?user=42"))
                .body(Body::empty())
                .unwrap();
            let listed2 = json_body(app.oneshot(list2).await.unwrap()).await;
            assert!(listed2.as_array().unwrap().is_empty(), "revoked — gone from the list immediately");
        }

        /// The rule `sc_core::Core::create_grant` enforces: a grant with
        /// neither `allow` nor `deny` set would be a silent no-op row, so it
        /// is refused rather than accepted.
        #[tokio::test]
        async fn a_grant_with_no_allow_and_no_deny_is_refused() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let create = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"principal":{"kind":"user","id":1},"share":1,"subpath":"","allow":[],"deny":[],"inherit":true}"#))
                .unwrap();
            let resp = app.oneshot(create).await.unwrap();
            assert_eq!(resp.status(), StatusCode::UNPROCESSABLE_ENTITY);
        }

        #[tokio::test]
        async fn patching_an_unknown_grant_is_404() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let patch = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants/999999").method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"allow":["read"]}"#))
                .unwrap();
            assert_eq!(app.oneshot(patch).await.unwrap().status(), StatusCode::NOT_FOUND);
        }

        #[tokio::test]
        async fn deleting_an_unknown_grant_is_404() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let delete = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants/999999").method("DELETE"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(delete).await.unwrap().status(), StatusCode::NOT_FOUND);
        }

        /// A grant naming both a bit in `allow` and the same bit in `deny`
        /// round-trips as-is — the server does not silently normalize it away
        /// — because it is the evaluator's job (`sc-acl`'s depth-first
        /// algorithm, same-depth `deny` wins) to resolve the conflict at
        /// evaluation time, not the storage layer's job to pretend it cannot
        /// happen. This pins the wire contract the admin UI's warning
        /// ("deny always wins over allow") depends on: both bits really do
        /// come back in the response, so the UI can detect the overlap and
        /// warn about it.
        #[tokio::test]
        async fn a_bit_present_in_both_allow_and_deny_round_trips_unmodified() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let create = as_principal(admin, HttpRequest::builder().uri("/api/admin/grants").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"principal":{"kind":"user","id":1},"share":1,"subpath":"","allow":["read"],"deny":["read"],"inherit":true}"#,
                ))
                .unwrap();
            let created = json_body(app.oneshot(create).await.unwrap()).await;
            assert_eq!(created["allow"].as_array().unwrap(), &vec![serde_json::json!("read")]);
            assert_eq!(created["deny"].as_array().unwrap(), &vec![serde_json::json!("read")]);
        }
    }

    /// Group CRUD + membership at the HTTP layer —
    /// admin gating, wire shapes, and the duplicate-name/not-found refusals.
    /// `AclEngine` refresh itself (`refresh_group_memberships`) is a
    /// one-line passthrough exercised in `sc-server`'s wiring, not here; the
    /// membership round trip through `sc-auth` is covered directly in
    /// `crates/sc-auth/src/tests.rs`.
    mod admin_groups {
        use super::*;
        use crate::testutil::GrantMockCore;

        fn router(state: AppState) -> Router {
            Router::new()
                .route("/api/admin/groups", get(admin_list_groups).post(admin_create_group))
                .route("/api/admin/groups/{id}", patch(admin_rename_group).delete(admin_delete_group))
                .route("/api/admin/groups/{id}/members", post(admin_add_group_member))
                .route("/api/admin/groups/{id}/members/{user}", delete(admin_remove_group_member))
                .with_state(state)
        }

        fn as_principal(uid: sc_vfs::UserId, req: axum::http::request::Builder) -> axum::http::request::Builder {
            req.extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
        }

        async fn json_body(resp: axum::response::Response) -> serde_json::Value {
            let b = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&b).unwrap()
        }

        fn admin_state() -> (AppState, tempfile::TempDir, sc_vfs::UserId) {
            let (state, dir) = test_state_with_core(Arc::new(GrantMockCore::default()));
            let admin = state.auth.create_user("root", &secrecy::SecretString::from("correct horse battery")).unwrap();
            state.auth.set_admin(admin, true).unwrap();
            (state, dir, admin)
        }

        #[tokio::test]
        async fn a_non_admin_is_refused_on_every_group_route() {
            let (state, _dir) = test_state_with_core(Arc::new(GrantMockCore::default()));
            let plain = state.auth.create_user("plain", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);
            let list = as_principal(plain, HttpRequest::builder().uri("/api/admin/groups")).body(Body::empty()).unwrap();
            assert_eq!(app.clone().oneshot(list).await.unwrap().status(), StatusCode::FORBIDDEN);
            let create = as_principal(plain, HttpRequest::builder().uri("/api/admin/groups").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"eng"}"#))
                .unwrap();
            assert_eq!(app.oneshot(create).await.unwrap().status(), StatusCode::FORBIDDEN);
        }

        /// Create, see it listed with an empty member set, add two members,
        /// remove one, rename, then delete — the full lifecycle a group
        /// management screen drives.
        #[tokio::test]
        async fn create_membership_rename_delete_round_trip() {
            let (state, _dir, admin) = admin_state();
            let alice = state.auth.create_user("alice", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let bob = state.auth.create_user("bob", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);

            let create = as_principal(admin, HttpRequest::builder().uri("/api/admin/groups").method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"engineering"}"#))
                .unwrap();
            let resp = app.clone().oneshot(create).await.unwrap();
            assert_eq!(resp.status(), StatusCode::CREATED);
            let created = json_body(resp).await;
            assert_eq!(created["name"], "engineering");
            assert_eq!(created["members"].as_array().unwrap().len(), 0);
            let gid = created["id"].as_u64().unwrap();

            let add_alice = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/groups/{gid}/members")).method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(format!(r#"{{"user":{}}}"#, alice.get())))
                .unwrap();
            assert_eq!(app.clone().oneshot(add_alice).await.unwrap().status(), StatusCode::NO_CONTENT);

            let add_bob = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/groups/{gid}/members")).method("POST"))
                .header("content-type", "application/json")
                .body(Body::from(format!(r#"{{"user":{}}}"#, bob.get())))
                .unwrap();
            assert_eq!(app.clone().oneshot(add_bob).await.unwrap().status(), StatusCode::NO_CONTENT);

            let list = as_principal(admin, HttpRequest::builder().uri("/api/admin/groups")).body(Body::empty()).unwrap();
            let listed = json_body(app.clone().oneshot(list).await.unwrap()).await;
            let mut members: Vec<u64> = listed[0]["members"].as_array().unwrap().iter().map(|v| v.as_u64().unwrap()).collect();
            members.sort();
            assert_eq!(members, vec![alice.get() as u64, bob.get() as u64]);

            let remove_alice = as_principal(
                admin,
                HttpRequest::builder().uri(format!("/api/admin/groups/{gid}/members/{}", alice.get())).method("DELETE"),
            )
            .body(Body::empty())
            .unwrap();
            assert_eq!(app.clone().oneshot(remove_alice).await.unwrap().status(), StatusCode::NO_CONTENT);

            let rename = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/groups/{gid}")).method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"eng-2"}"#))
                .unwrap();
            let renamed = json_body(app.clone().oneshot(rename).await.unwrap()).await;
            assert_eq!(renamed["name"], "eng-2");
            assert_eq!(renamed["members"].as_array().unwrap(), &vec![serde_json::json!(bob.get())]);

            let delete = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/groups/{gid}")).method("DELETE"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.clone().oneshot(delete).await.unwrap().status(), StatusCode::NO_CONTENT);

            let list2 = as_principal(admin, HttpRequest::builder().uri("/api/admin/groups")).body(Body::empty()).unwrap();
            let listed2 = json_body(app.oneshot(list2).await.unwrap()).await;
            assert!(listed2.as_array().unwrap().is_empty());
        }

        #[tokio::test]
        async fn creating_a_group_with_a_taken_name_is_a_conflict() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let create = |body: &'static str| {
                as_principal(admin, HttpRequest::builder().uri("/api/admin/groups").method("POST"))
                    .header("content-type", "application/json")
                    .body(Body::from(body))
                    .unwrap()
            };
            assert_eq!(app.clone().oneshot(create(r#"{"name":"eng"}"#)).await.unwrap().status(), StatusCode::CREATED);
            assert_eq!(app.oneshot(create(r#"{"name":"eng"}"#)).await.unwrap().status(), StatusCode::CONFLICT);
        }

        #[tokio::test]
        async fn an_unknown_group_id_is_404_on_every_route() {
            let (state, _dir, admin) = admin_state();
            let app = router(state);
            let rename = as_principal(admin, HttpRequest::builder().uri("/api/admin/groups/999").method("PATCH"))
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"whatever"}"#))
                .unwrap();
            assert_eq!(app.clone().oneshot(rename).await.unwrap().status(), StatusCode::NOT_FOUND);
            let delete = as_principal(admin, HttpRequest::builder().uri("/api/admin/groups/999").method("DELETE"))
                .body(Body::empty())
                .unwrap();
            assert_eq!(app.oneshot(delete).await.unwrap().status(), StatusCode::NOT_FOUND);
        }
    }

    mod admin_audit {
        use super::*;
        use crate::testutil::GrantMockCore;

        fn router(state: AppState) -> Router {
            Router::new().route("/api/admin/audit", get(admin_list_audit)).with_state(state)
        }

        fn as_principal(uid: sc_vfs::UserId, req: axum::http::request::Builder) -> axum::http::request::Builder {
            req.extension(Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
        }

        async fn json_body(resp: axum::response::Response) -> serde_json::Value {
            let b = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&b).unwrap()
        }

        fn admin_state() -> (AppState, tempfile::TempDir, sc_vfs::UserId) {
            let (state, dir) = test_state_with_core(Arc::new(GrantMockCore::default()));
            let admin = state.auth.create_user("root", &secrecy::SecretString::from("correct horse battery")).unwrap();
            state.auth.set_admin(admin, true).unwrap();
            (state, dir, admin)
        }

        #[tokio::test]
        async fn a_non_admin_is_refused() {
            let (state, _dir) = test_state_with_core(Arc::new(GrantMockCore::default()));
            let plain = state.auth.create_user("plain", &secrecy::SecretString::from("correct horse battery")).unwrap();
            let app = router(state);
            let req = as_principal(plain, HttpRequest::builder().uri("/api/admin/audit")).body(Body::empty()).unwrap();
            assert_eq!(app.oneshot(req).await.unwrap().status(), StatusCode::FORBIDDEN);
        }

        /// Newest first, and `actor_name` resolved against `list_users()`
        /// rather than left as a bare id — the whole reason this wire type
        /// carries a name field the underlying `AuditRow` does not.
        #[tokio::test]
        async fn lists_rows_newest_first_with_resolved_actor_name() {
            let (state, _dir, admin) = admin_state();
            state.auth.audit(Some(admin), "test.one", None, None, true, None);
            state.auth.audit(Some(admin), "test.two", Some("target"), None, false, Some("oops"));
            let app = router(state);
            let req = as_principal(admin, HttpRequest::builder().uri("/api/admin/audit")).body(Body::empty()).unwrap();
            let body = json_body(app.oneshot(req).await.unwrap()).await;
            let rows = body["rows"].as_array().unwrap();
            assert_eq!(rows[0]["event"], "test.two");
            assert_eq!(rows[0]["ok"], false);
            assert_eq!(rows[0]["detail"], "oops");
            assert_eq!(rows[0]["actor_name"], "root");
            assert_eq!(rows[1]["event"], "test.one");
        }

        #[tokio::test]
        async fn filters_by_event_and_actor() {
            let (state, _dir, admin) = admin_state();
            let other = state.auth.create_user("other", &secrecy::SecretString::from("correct horse battery")).unwrap();
            state.auth.audit(Some(admin), "scoped.event", None, None, true, None);
            state.auth.audit(Some(other), "scoped.event", None, None, true, None);
            state.auth.audit(Some(admin), "unrelated.event", None, None, true, None);
            let app = router(state);
            let req = as_principal(
                admin,
                HttpRequest::builder().uri(format!("/api/admin/audit?event=scoped.event&actor={}", other.get())),
            )
            .body(Body::empty())
            .unwrap();
            let body = json_body(app.oneshot(req).await.unwrap()).await;
            let rows = body["rows"].as_array().unwrap();
            assert_eq!(rows.len(), 1);
            assert_eq!(rows[0]["actor"].as_u64().unwrap() as u32, other.get());
        }

        /// `next` is the previous page's last `rowid` — passed back as
        /// `before`, the second page must not repeat anything the first
        /// page already returned.
        #[tokio::test]
        async fn pagination_cursor_never_repeats_a_row() {
            let (state, _dir, admin) = admin_state();
            for i in 0..5 {
                state.auth.audit(Some(admin), "page.event", Some(&i.to_string()), None, true, None);
            }
            let app = router(state);
            let first = as_principal(admin, HttpRequest::builder().uri("/api/admin/audit?event=page.event&limit=2"))
                .body(Body::empty())
                .unwrap();
            let page1 = json_body(app.clone().oneshot(first).await.unwrap()).await;
            let rows1 = page1["rows"].as_array().unwrap();
            assert_eq!(rows1.len(), 2);
            let next = page1["next"].as_i64().expect("a full page must report a cursor");

            let second = as_principal(admin, HttpRequest::builder().uri(format!("/api/admin/audit?event=page.event&limit=2&before={next}")))
                .body(Body::empty())
                .unwrap();
            let page2 = json_body(app.oneshot(second).await.unwrap()).await;
            let rows2 = page2["rows"].as_array().unwrap();
            assert_eq!(rows2.len(), 2);
            assert_ne!(rows1[0]["rowid"], rows2[0]["rowid"]);
            assert_ne!(rows1[1]["rowid"], rows2[0]["rowid"]);
        }
    }

    // ------------------------------------------------- upload idle timeout --

    /// An `UploadApi` with a short `body_idle_timeout` and a `patch_checked`
    /// that just echoes the data length back as the new offset — enough to
    /// drive `uploads_patch` end-to-end without a real engine.
    struct ShortIdleUploads;
    impl crate::upload_api::UploadApi for ShortIdleUploads {
        fn patch_checked(
            &self,
            _user: sc_vfs::ids::UserId,
            _id: &str,
            _offset: u64,
            data: &[u8],
            _checksum: Option<crate::upload_api::TusChecksum>,
        ) -> Result<u64, crate::core_api::CoreError> {
            Ok(data.len() as u64)
        }
        fn body_idle_timeout(&self) -> std::time::Duration {
            std::time::Duration::from_millis(200)
        }
    }

    /// Pins a fixed defect: `uploads_patch` used to read the
    /// body with `axum::body::to_bytes(req.into_body(), usize::MAX)`, which
    /// has no timeout parameter at all — a client that opens a `PATCH` and
    /// then stops sending held the request (and, transitively, the engine's
    /// open part-file handle) forever. This drives the real route, not just
    /// `read_body_with_idle_timeout` in isolation (already covered by
    /// `upload_api.rs`'s `idle_timeout_tests`): against the un-fixed
    /// `to_bytes` call this test hangs instead of ever seeing a response.
    #[tokio::test(start_paused = true)]
    async fn patch_with_a_silent_body_aborts_instead_of_hanging() {
        let (mut state, _dir) = crate::testutil::test_state();
        state.uploads = Arc::new(ShortIdleUploads);
        let app = upload_routes(state);

        // First chunk arrives promptly, then the client goes silent for
        // longer than `ShortIdleUploads::body_idle_timeout` (200ms) before
        // sending a second (and final) chunk. The body is finite so an
        // un-timed-out reader (the pre-fix `axum::body::to_bytes`) would
        // eventually complete it rather than hang this test forever — the
        // defect this pins is that it *does* complete, accepting an
        // arbitrarily long silent gap, instead of aborting the request.
        let stream = futures::stream::unfold(0u8, |step| async move {
            match step {
                0 => {
                    tokio::time::sleep(std::time::Duration::from_millis(10)).await;
                    Some((Ok::<_, std::io::Error>(bytes::Bytes::from_static(b"partial")), 1))
                }
                1 => {
                    tokio::time::sleep(std::time::Duration::from_secs(60)).await;
                    Some((Ok::<_, std::io::Error>(bytes::Bytes::from_static(b"the rest, sent very late")), 2))
                }
                _ => None,
            }
        });
        let req = HttpRequest::builder()
            .method("PATCH")
            .uri("/api/uploads/abc")
            .extension(Principal { user: sc_vfs::ids::UserId::new(1), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session })
            .header("upload-offset", "0")
            .body(Body::from_stream(stream))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::REQUEST_TIMEOUT);
    }

    // ------------------------------------------------------------- jobs --

    mod jobs {
        use super::*;
        use crate::core_api::Aggregate;
        use crate::testutil::JobMockCore;

        fn jobs_router(state: AppState) -> Router {
            Router::new()
                .route("/api/fs/delete", axum::routing::post(fs_delete))
                .route("/api/fs/archive", axum::routing::post(fs_archive))
                .route("/api/jobs", get(job_list))
                .route("/api/jobs/{id}", get(job_status).delete(job_cancel))
                .route("/api/jobs/{id}/download", get(job_download))
                .with_state(state)
        }

        fn job_req(method: &str, uri: &str, user: u32, body: Option<serde_json::Value>) -> HttpRequest<Body> {
            let b = HttpRequest::builder()
                .method(method)
                .uri(uri)
                .extension(Principal { user: sc_vfs::ids::UserId::new(user), scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session });
            match body {
                Some(v) => b.header("Content-Type", "application/json").body(Body::from(v.to_string())).unwrap(),
                None => b.body(Body::empty()).unwrap(),
            }
        }

        async fn json_of(resp: axum::response::Response) -> serde_json::Value {
            let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            serde_json::from_slice(&bytes).unwrap()
        }

        fn agg() -> Aggregate {
            Aggregate { file_count: 1, dir_count: 0, total_bytes: 10 }
        }

        /// Every `fs.delete` request is a durable job now, no matter how
        /// small — a single-file batch must still answer `202 {"job": id}`,
        /// never an inline `results` array (no size
        /// below which an operation is allowed to run outside the job/
        /// `jobs.db` machinery).
        #[tokio::test]
        async fn even_a_single_file_delete_becomes_a_job() {
            let (state, _dir) = test_state_with_core(Arc::new(JobMockCore::new(agg())));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a.txt"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED, "no operation may skip the job machinery, regardless of size");
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            let mut terminal = None;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                let j = json_of(resp).await;
                if j["state"] != "running" {
                    terminal = Some(j);
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            let j = terminal.expect("job did not reach a terminal state");
            assert_eq!(j["state"], "done");
            assert_eq!(j["results"][0]["ok"], true);
        }

        /// A multi-item batch runs to completion as a job: `done` advances to
        /// `total`, and per-item `results` is populated exactly as a
        /// synchronous caller would have gotten it back inline before jobs
        /// existed.
        #[tokio::test]
        async fn multi_item_delete_becomes_a_job_and_runs_to_completion() {
            let (state, _dir) = test_state_with_core(Arc::new(JobMockCore::new(agg())));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a", "/b", "/c"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            let mut terminal = None;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                let j = json_of(resp).await;
                if j["state"] != "running" {
                    terminal = Some(j);
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            let j = terminal.expect("job did not reach a terminal state");
            assert_eq!(j["state"], "done");
            assert_eq!(j["done"], 3);
            assert_eq!(j["total"], 3);
            assert_eq!(j["results"].as_array().unwrap().len(), 3);
            assert_eq!(j["attempting"].as_array().unwrap().len(), 0, "nothing should still be 'attempting' once a job is done");
            assert_eq!(j["pending"].as_array().unwrap().len(), 0, "nothing should still be 'pending' once a job is done");
        }

        /// The zero-loss requirement's core mechanism: `begin_result` commits
        /// an `attempting` row *before* `op(p)` runs, so a poller that catches
        /// the job mid-item sees that path under `attempting`, not silently
        /// absent — the same state a crash between the two writes would
        /// leave behind (`JobStore::begin_result`'s doc).
        #[tokio::test]
        async fn an_in_flight_item_is_visible_as_attempting_before_it_resolves() {
            let (core, release, calls) = JobMockCore::gated(agg());
            let (state, _dir) = test_state_with_core(Arc::new(core));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a", "/b"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            for _ in 0..200 {
                if calls.load(std::sync::atomic::Ordering::SeqCst) >= 1 {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            assert_eq!(calls.load(std::sync::atomic::Ordering::SeqCst), 1, "the first item must have started");

            let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
            let j = json_of(resp).await;
            assert_eq!(j["attempting"], serde_json::json!(["/a"]), "the in-flight item must show as attempting, not absent");
            assert_eq!(j["results"].as_array().unwrap().len(), 0, "an attempting item is not yet a resolved result");

            release.send(()).unwrap();
            release.send(()).unwrap();

            let mut terminal = None;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                let j = json_of(resp).await;
                if j["state"] != "running" {
                    terminal = Some(j);
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            let j = terminal.expect("job did not reach a terminal state");
            assert_eq!(j["state"], "done");
            assert_eq!(j["attempting"].as_array().unwrap().len(), 0);
        }

        /// A `jobs.db` that cannot durably record an "attempting" row (disk
        /// full, a wedged WAL checkpoint, ...) must stop the job before the
        /// destructive op ever runs — proceeding on an item whose record is
        /// known to have failed to persist is exactly the silent-loss case
        /// record-before-act exists to close (`JobStore::begin_result`'s doc).
        ///
        /// The table is broken *mid-job*, after `/a` has started: breaking it
        /// up front is now caught one level earlier (`insert` writes the
        /// `pending` plan into the same table, so the whole job is refused
        /// with a `500` — that path is
        /// `a_jobs_row_write_failure_refuses_the_job_entirely`'s). What is
        /// under test here is the per-item refusal, which only a failure that
        /// appears after the job is already running can reach.
        #[tokio::test]
        async fn a_job_results_write_failure_stops_the_job_before_the_next_delete_runs() {
            let (core, release, calls) = JobMockCore::gated(agg());
            let (state, _dir) = test_state_with_core(Arc::new(core));
            let jobs = state.jobs.clone();
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a", "/b"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            // Wait for `/a` to be in flight (blocked on the gate), then wedge
            // the table so `/b`'s `begin_result` is the write that fails.
            for _ in 0..200 {
                if calls.load(std::sync::atomic::Ordering::SeqCst) >= 1 {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            assert_eq!(calls.load(std::sync::atomic::Ordering::SeqCst), 1, "the first item must have started");
            jobs.break_results_table_for_test();
            release.send(()).unwrap();

            let mut terminal = None;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                let j = json_of(resp).await;
                if j["state"] != "running" {
                    terminal = Some(j);
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            let j = terminal.expect("job did not reach a terminal state");
            assert_eq!(j["state"], "error");
            assert_eq!(
                calls.load(std::sync::atomic::Ordering::SeqCst),
                1,
                "the second delete must never run when its durability record failed to persist"
            );
        }

        /// One level up from the previous test: if the *parent* `jobs` row
        /// itself cannot be persisted (`JobStore::insert`'s doc), the route
        /// must refuse to start the job at all — no `spawn_blocking`, no job
        /// id, a `500` instead of a `202` — since a job id this table could
        /// never record is one a restart could never find either.
        #[tokio::test]
        async fn a_jobs_row_write_failure_refuses_the_job_entirely() {
            let (core, _release, calls) = JobMockCore::gated(agg());
            let (state, _dir) = test_state_with_core(Arc::new(core));
            state.jobs.break_jobs_table_for_test();
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a", "/b"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::INTERNAL_SERVER_ERROR);
            let j = json_of(resp).await;
            assert!(j.get("job").is_none(), "no job id must be handed out for a job that was never recorded");
            assert_eq!(calls.load(std::sync::atomic::Ordering::SeqCst), 0, "the delete must never run when the job itself was never recorded");
        }

        /// A cancel must let the item already running finish (never abort it
        /// mid-flight) and skip every item after it — pins the exact
        /// boundary `spawn_batch_job`'s doc comment describes.
        #[tokio::test]
        async fn cancel_finishes_the_in_flight_item_and_skips_the_rest() {
            let (core, release, calls) = JobMockCore::gated(agg());
            let (state, _dir) = test_state_with_core(Arc::new(core));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a", "/b", "/c"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            // Wait for the first item's call to actually start (it then
            // blocks on the gate) before cancelling, so the cancel cannot
            // race ahead of the job even beginning.
            for _ in 0..200 {
                if calls.load(std::sync::atomic::Ordering::SeqCst) >= 1 {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            assert_eq!(calls.load(std::sync::atomic::Ordering::SeqCst), 1, "the first item must have started");

            let resp = app.clone().oneshot(job_req("DELETE", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
            assert_eq!(resp.status(), StatusCode::NO_CONTENT);

            // Release the in-flight item — it must be allowed to finish.
            release.send(()).unwrap();

            let mut terminal = None;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                let j = json_of(resp).await;
                if j["state"] != "running" {
                    terminal = Some(j);
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            let j = terminal.expect("job did not reach a terminal state");
            assert_eq!(j["state"], "cancelled");
            assert_eq!(j["done"], 1, "the item already in flight must finish");
            assert_eq!(j["total"], 3);
            assert_eq!(calls.load(std::sync::atomic::Ordering::SeqCst), 1, "no item after the cancel point may start");
            assert_eq!(j["pending"], serde_json::json!(["/b", "/c"]), "the skipped items must be nameable, not just a count");
        }

        /// A job id must be invisible and uncancellable from another
        /// account — `get_owned`/`cancel_owned` must 404, not leak whether
        /// the id exists at all.
        #[tokio::test]
        async fn a_job_is_invisible_and_uncancellable_by_another_account() {
            let (state, _dir) = test_state_with_core(Arc::new(JobMockCore::new(agg())));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a", "/b"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 2, None)).await.unwrap();
            assert_eq!(resp.status(), StatusCode::NOT_FOUND, "another account must not be able to read this job");

            let resp = app.clone().oneshot(job_req("DELETE", &format!("/api/jobs/{job_id}"), 2, None)).await.unwrap();
            assert_eq!(resp.status(), StatusCode::NOT_FOUND, "another account must not be able to cancel this job");

            let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK, "the real owner must still be able to read it");
        }

        /// `GET /api/jobs` is how a browser refresh re-attaches: it must list
        /// a caller's own non-terminal jobs and nothing belonging to another
        /// account (same owner-scoping as `job_status`/`job_cancel`), and it
        /// must stop listing a job once it reaches a terminal state.
        #[tokio::test]
        async fn job_list_is_owner_scoped_and_only_shows_open_jobs() {
            let (core, release, calls) = JobMockCore::gated(agg());
            let (state, _dir) = test_state_with_core(Arc::new(core));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/delete", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            for _ in 0..200 {
                if calls.load(std::sync::atomic::Ordering::SeqCst) >= 1 {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }

            let resp = app.clone().oneshot(job_req("GET", "/api/jobs", 1, None)).await.unwrap();
            let listed = json_of(resp).await;
            let ids: Vec<&str> = listed["jobs"].as_array().unwrap().iter().map(|j| j["id"].as_str().unwrap()).collect();
            assert!(ids.contains(&job_id.as_str()), "the owner must see their own running job");

            let resp = app.clone().oneshot(job_req("GET", "/api/jobs", 2, None)).await.unwrap();
            let listed = json_of(resp).await;
            assert!(listed["jobs"].as_array().unwrap().is_empty(), "another account must not see this job");

            release.send(()).unwrap();
            let mut terminal = false;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                if json_of(resp).await["state"] != "running" {
                    terminal = true;
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            assert!(terminal, "job did not reach a terminal state");

            let resp = app.clone().oneshot(job_req("GET", "/api/jobs", 1, None)).await.unwrap();
            let listed = json_of(resp).await;
            let ids: Vec<&str> = listed["jobs"].as_array().unwrap().iter().map(|j| j["id"].as_str().unwrap()).collect();
            assert!(!ids.contains(&job_id.as_str()), "a finished job must drop out of the open list");
        }

        /// An archive job's finished bytes are served exactly once — a
        /// second `download` after the first must 404 rather than replay
        /// stale bytes forever (`JobStore::take_artifact`).
        #[tokio::test]
        async fn archive_job_download_serves_the_zip_once_then_404s() {
            let (state, _dir) = test_state_with_core(Arc::new(JobMockCore::new(agg())));
            let app = jobs_router(state);
            let body = serde_json::json!({ "paths": ["/a"] });
            let resp = app.clone().oneshot(job_req("POST", "/api/fs/archive", 1, Some(body))).await.unwrap();
            assert_eq!(resp.status(), StatusCode::ACCEPTED);
            let job_id = json_of(resp).await["job"].as_str().unwrap().to_string();

            let mut done = false;
            for _ in 0..200 {
                let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}"), 1, None)).await.unwrap();
                let j = json_of(resp).await;
                if j["state"] == "done" {
                    assert_eq!(j["download"], true);
                    done = true;
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
            assert!(done, "archive job did not finish");

            let resp = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}/download"), 1, None)).await.unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            assert_eq!(resp.headers().get(axum::http::header::CONTENT_TYPE).unwrap(), "application/zip");
            let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
            assert!(bytes.starts_with(b"PK"), "must be real zip bytes");

            let resp2 = app.clone().oneshot(job_req("GET", &format!("/api/jobs/{job_id}/download"), 1, None)).await.unwrap();
            assert_eq!(resp2.status(), StatusCode::NOT_FOUND, "a second download of the same job must not replay stale bytes");
        }
    }

    // ------------------------------------------------------------ oidc --

    /// `docs/proposals/stowcloud-0-oidc-login.md` §5-1 and §5-2, route by
    /// route. The relying party is `testutil::ScriptedOidc` -- discovery,
    /// JWKS and the eleven ID-token checks are `sc-oidc`'s to prove, and it
    /// does, against an in-process fake IdP. What is proved here is the part
    /// that lives in this crate: the callback's ordering, the binding cookie,
    /// single-use `state`, `returnTo` validation, and which status code or
    /// redirect each outcome produces.
    mod oidc {
        use super::*;
        use crate::testutil::{test_state_with_oidc, ScriptedOidc};
        use sc_auth::{NewOidcFlow, OidcFlowMode};
        use secrecy::SecretString;

        fn oidc_router(state: AppState) -> Router {
            Router::new()
                .route("/api/auth/oidc/config", get(oidc_config))
                .route("/api/auth/oidc/start", get(oidc_start))
                .route("/api/auth/oidc/callback", get(oidc_callback))
                .route("/api/auth/oidc/link/start", post(oidc_link_start))
                .route("/api/auth/oidc/link", delete(oidc_unlink))
                .route("/api/auth/session", get(auth_session))
                .route(
                    "/api/admin/users/{id}/oidc",
                    get(admin_get_user_oidc).put(admin_put_user_oidc).delete(admin_delete_user_oidc),
                )
                .with_state(state)
        }

        fn principal_of(uid: UserId) -> Principal {
            Principal { user: uid, scope: sc_auth::Scope::default(), via: sc_auth::AuthVia::Session }
        }

        const PASSWORD: &str = "correct horse battery";

        fn user(state: &AppState, name: &str) -> UserId {
            state.auth.create_user(name, &SecretString::from(PASSWORD)).unwrap()
        }

        /// Writes the `oidc_flow` row `/start` would have written for
        /// `ScriptedOidc`'s deterministic secrets, so a callback request can
        /// be built against it.
        fn seed_flow(
            state: &AppState,
            fake: &ScriptedOidc,
            mode: OidcFlowMode,
            link_user: Option<UserId>,
            return_to: Option<&str>,
        ) {
            state
                .auth
                .create_oidc_flow(NewOidcFlow {
                    state_hash: oidc_sha256(&fake.state_param()),
                    binding_hash: oidc_sha256(&fake.binding()),
                    nonce_hash: oidc_sha256(&fake.nonce()),
                    code_verifier: &SecretString::from(fake.code_verifier()),
                    mode,
                    link_user,
                    return_to,
                })
                .unwrap();
        }

        /// Ages every flow past its TTL by editing the row directly.
        /// `create_oidc_flow` derives `expires_ns` from `OIDC_FLOW_TTL` and
        /// offers no override, and the alternative is a ten-minute test.
        fn expire_flows(dir: &tempfile::TempDir) {
            let conn = rusqlite::Connection::open(dir.path().join("auth.sqlite3")).unwrap();
            conn.execute("UPDATE oidc_flow SET expires_ns = 1", []).unwrap();
        }

        fn callback_req(query: &str, binding: Option<&str>, principal: Option<Principal>) -> HttpRequest<Body> {
            let mut b = HttpRequest::builder().method("GET").uri(format!("/api/auth/oidc/callback?{query}"));
            if let Some(v) = binding {
                b = b.header(axum::http::header::COOKIE, format!("{}={v}", crate::OIDC_FLOW_COOKIE));
            }
            if let Some(p) = principal {
                b = b.extension(p);
            }
            b.body(Body::empty()).unwrap()
        }

        fn location_of(resp: &axum::response::Response) -> String {
            resp.headers()
                .get(axum::http::header::LOCATION)
                .unwrap_or_else(|| panic!("a 302 with no Location is a blank page: {:?}", resp.status()))
                .to_str()
                .unwrap()
                .to_string()
        }

        fn set_cookies(resp: &axum::response::Response) -> Vec<String> {
            resp.headers()
                .get_all(axum::http::header::SET_COOKIE)
                .iter()
                .map(|v| v.to_str().unwrap().to_string())
                .collect()
        }

        async fn json_of(resp: axum::response::Response) -> serde_json::Value {
            let bytes = axum::body::to_bytes(resp.into_body(), 256 * 1024).await.unwrap();
            serde_json::from_slice(&bytes).unwrap()
        }

        // ---------------------------------------------------- /config --

        #[tokio::test]
        async fn config_reports_the_button_and_its_label_and_nothing_else() {
            let (state, _dir) = test_state_with_oidc(Arc::new(ScriptedOidc::linked_as("sub-1")));
            let app = oidc_router(state);
            let resp = app
                .oneshot(HttpRequest::builder().uri("/api/auth/oidc/config").body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            let body = json_of(resp).await;
            assert_eq!(body["enabled"], true);
            assert_eq!(body["display_name"], "Example SSO");
            // §5-1: the issuer URL and the client id are not exported here.
            assert!(body.get("issuer").is_none(), "{body}");
            assert!(body.get("client_id").is_none(), "{body}");
        }

        #[tokio::test]
        async fn config_on_a_deployment_without_oidc_says_so_rather_than_erroring() {
            let (state, _dir) = test_state_with_oidc(Arc::new(ScriptedOidc::disabled()));
            let app = oidc_router(state);
            let resp = app
                .oneshot(HttpRequest::builder().uri("/api/auth/oidc/config").body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
            assert_eq!(json_of(resp).await["enabled"], false);
        }

        // ----------------------------------------------------- /start --

        /// A `302` to the IdP, a flow row, and the binding cookie -- and the
        /// cookie has to carry every attribute §4.3.1 names, because a
        /// `SameSite=Strict` one would not survive the cross-site return and
        /// a non-`Secure` one is not a `__Host-` cookie at all.
        #[tokio::test]
        async fn start_redirects_to_the_idp_and_sets_the_binding_cookie() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let auth = state.auth.clone();
            let app = oidc_router(state);
            let resp = app
                .oneshot(HttpRequest::builder().uri("/api/auth/oidc/start").body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::FOUND);
            assert!(location_of(&resp).starts_with("https://idp.example.test/authorize?"));

            let cookie = set_cookies(&resp).into_iter().find(|c| c.starts_with(crate::OIDC_FLOW_COOKIE)).expect("binding cookie");
            assert!(cookie.contains("Secure"), "{cookie}");
            assert!(cookie.contains("HttpOnly"), "{cookie}");
            assert!(cookie.contains("SameSite=Lax"), "{cookie}");
            assert!(cookie.contains("Max-Age=600"), "{cookie}");

            // The flow is really in the database, keyed by sha256(state).
            assert!(auth.take_oidc_flow(&oidc_sha256(&fake.state_param())).unwrap().is_some());
        }

        #[tokio::test]
        async fn start_on_a_deployment_without_oidc_is_404_oidc_disabled() {
            let (state, _dir) = test_state_with_oidc(Arc::new(ScriptedOidc::disabled()));
            let app = oidc_router(state);
            let resp = app
                .oneshot(HttpRequest::builder().uri("/api/auth/oidc/start").body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::NOT_FOUND);
            assert_eq!(json_of(resp).await["error"]["code"], "oidc.disabled");
        }

        #[tokio::test]
        async fn start_reports_an_unreachable_provider_as_503() {
            let fake = ScriptedOidc {
                begin_error: Some(crate::oidc_api::OidcError::ProviderUnavailable("connect timeout".into())),
                ..ScriptedOidc::linked_as("sub-1")
            };
            let (state, _dir) = test_state_with_oidc(Arc::new(fake));
            let app = oidc_router(state);
            let resp = app
                .oneshot(HttpRequest::builder().uri("/api/auth/oidc/start").body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);
            assert_eq!(json_of(resp).await["error"]["code"], "oidc.provider_unavailable");
        }

        #[tokio::test]
        async fn start_is_rate_limited_per_ip() {
            let (mut state, _dir) = test_state_with_oidc(Arc::new(ScriptedOidc::linked_as("sub-1")));
            state.oidc_rate = Arc::new(crate::rate_limit::IpTokenBucket::new(1, std::time::Duration::from_secs(3600)));
            let app = oidc_router(state);
            let req = || HttpRequest::builder().uri("/api/auth/oidc/start").body(Body::empty()).unwrap();
            assert_eq!(app.clone().oneshot(req()).await.unwrap().status(), StatusCode::FOUND);
            let limited = app.oneshot(req()).await.unwrap();
            assert_eq!(limited.status(), StatusCode::TOO_MANY_REQUESTS);
            assert!(limited.headers().get("Retry-After").is_some());
        }

        // ------------------------------------------------- returnTo --

        /// §5-1's rule, as a table. Every rejection falls back to the mode's
        /// default rather than erroring: a bad query parameter must not stop
        /// somebody logging in.
        #[test]
        fn return_to_accepts_only_same_origin_printable_paths() {
            for good in ["/", "/b/", "/b/Documents/Reports", "/settings/security?tab=sso"] {
                assert_eq!(safe_return_to(good), Some(good), "{good}");
            }
            for bad in [
                "https://evil.example.com/",   // not a path at all
                "b/Documents",                 // relative
                "//evil.example.com/",         // protocol-relative
                "/\\evil.example.com/",        // protocol-relative, backslash form
                "/b/\r\nSet-Cookie: x=y",      // header injection
                "/b/\nLocation: https://evil", // response splitting
                "/b/\u{7f}",                   // DEL is outside 0x20..=0x7E
                "/b/\u{202e}",                 // a non-ASCII byte
            ] {
                assert_eq!(safe_return_to(bad), None, "{bad:?}");
            }
        }

        /// The CR/LF case end to end, because rejecting it in a unit test is
        /// only half the claim. `HeaderValue::from_str` would also have
        /// refused these bytes -- and the established pattern in this file
        /// drops a header that fails to parse, which would have produced a
        /// `302` with no `Location`: a blank page, silently.
        #[tokio::test]
        async fn a_return_to_carrying_crlf_is_dropped_and_the_redirect_still_has_a_location() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let auth = state.auth.clone();
            let app = oidc_router(state);
            let resp = app
                .oneshot(
                    HttpRequest::builder()
                        .uri("/api/auth/oidc/start?returnTo=%2Fb%2F%0D%0ASet-Cookie%3A%20evil%3D1")
                        .body(Body::empty())
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::FOUND);
            assert!(location_of(&resp).starts_with("https://idp.example.test/"));
            // Nothing injected a second cookie.
            for c in set_cookies(&resp) {
                assert!(!c.contains("evil"), "{c}");
            }
            // And the rejected value was not persisted for the callback to
            // trip over later.
            let flow = auth.take_oidc_flow(&oidc_sha256(&fake.state_param())).unwrap().unwrap();
            assert_eq!(flow.return_to, None);
        }

        #[tokio::test]
        async fn a_valid_return_to_survives_the_whole_round_trip() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "amelia");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-1").unwrap();
            seed_flow(&state, &fake, OidcFlowMode::Login, None, Some("/b/Documents"));
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::FOUND);
            assert_eq!(location_of(&resp), "/b/Documents");
        }

        // -------------------------------------------------- callback --

        #[tokio::test]
        async fn a_linked_identity_gets_a_session_cookie_and_the_flow_cookie_is_expired() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "bruno");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-1").unwrap();
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let auth = state.auth.clone();
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::FOUND);
            assert_eq!(location_of(&resp), "/b/");

            let cookies = set_cookies(&resp);
            let session = cookies.iter().find(|c| c.starts_with(crate::SESSION_COOKIE)).expect("session cookie");
            let flow_cookie = cookies.iter().find(|c| c.starts_with(crate::OIDC_FLOW_COOKIE)).expect("flow cookie");
            assert!(flow_cookie.contains("Max-Age=0"), "the flow cookie must be expired on the way out: {flow_cookie}");

            // The session is real, and it is recorded as an OIDC one -- which
            // is what makes `unlink` able to find it later (§4.3.6).
            let token = session
                .trim_start_matches(&format!("{}=", crate::SESSION_COOKIE))
                .split(';')
                .next()
                .unwrap()
                .to_string();
            let principal = auth.validate_session(&token).unwrap().expect("a usable session");
            assert_eq!(principal.user, uid);
            let listed = auth.list_sessions(uid).unwrap();
            assert_eq!(listed.len(), 1);
            assert_eq!(listed[0].amr, sc_auth::AMR_OIDC);
        }

        /// The login-CSRF defence, stated as its own test: without the cookie
        /// the callback is refused even though `state` is genuine, unconsumed
        /// and correct. This is the attack correction 2 describes -- an
        /// attacker completes their own flow and hands the resulting URL to a
        /// victim's browser.
        #[tokio::test]
        async fn a_callback_with_no_binding_cookie_is_refused_and_never_redeems_the_code() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "clara");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-1").unwrap();
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let auth = state.auth.clone();
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(&format!("code=abc&state={}", fake.state_param()), None, None))
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::FOUND);
            assert_eq!(location_of(&resp), "/login?oidc_error=oidc.bad_state");
            assert!(set_cookies(&resp).iter().all(|c| !c.starts_with(crate::SESSION_COOKIE)), "no session may be issued");
            assert!(fake.redeemed.lock().is_empty(), "the code must not reach the token endpoint");
            // The flow was consumed anyway: a refused callback is a spent one.
            assert!(auth.take_oidc_flow(&oidc_sha256(&fake.state_param())).unwrap().is_none());
        }

        #[tokio::test]
        async fn a_callback_with_the_wrong_binding_cookie_is_refused() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "dieter");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-1").unwrap();
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some("some-other-browsers-binding"),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&resp), "/login?oidc_error=oidc.bad_state");
            assert!(fake.redeemed.lock().is_empty());
        }

        /// `take_oidc_flow` looks up and deletes in one transaction, so the
        /// second arrival of the same URL finds nothing. This is what makes a
        /// leaked callback URL worthless rather than merely stale.
        #[tokio::test]
        async fn a_replayed_state_is_refused_the_second_time() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "elena");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-1").unwrap();
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let app = oidc_router(state);

            let req = || callback_req(&format!("code=abc&state={}", fake.state_param()), Some(&fake.binding()), None);
            let first = app.clone().oneshot(req()).await.unwrap();
            assert_eq!(location_of(&first), "/b/");

            let second = app.oneshot(req()).await.unwrap();
            assert_eq!(location_of(&second), "/login?oidc_error=oidc.bad_state");
            assert!(set_cookies(&second).iter().all(|c| !c.starts_with(crate::SESSION_COOKIE)));
        }

        #[tokio::test]
        async fn an_expired_flow_says_expired_rather_than_bad_state() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "fabio");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-1").unwrap();
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            expire_flows(&dir);
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&resp), "/login?oidc_error=oidc.expired");
        }

        /// §5-1's pseudocode: `state` is required whether the IdP sent a
        /// result or an error, and exactly one of `code`/`error` is
        /// acceptable.
        #[tokio::test]
        async fn a_callback_missing_state_or_carrying_both_code_and_error_is_a_bad_request() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let app = oidc_router(state.clone());

            let no_state = app.clone().oneshot(callback_req("code=abc", Some(&fake.binding()), None)).await.unwrap();
            assert_eq!(location_of(&no_state), "/login?oidc_error=oidc.bad_request");

            let both = app
                .clone()
                .oneshot(callback_req(
                    &format!("code=abc&error=access_denied&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&both), "/login?oidc_error=oidc.bad_request");

            // The flow was spent by the "both" attempt, so seed another for
            // the "neither" case.
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let neither = app
                .oneshot(callback_req(&format!("state={}", fake.state_param()), Some(&fake.binding()), None))
                .await
                .unwrap();
            assert_eq!(location_of(&neither), "/login?oidc_error=oidc.bad_request");
        }

        #[tokio::test]
        async fn the_idps_own_access_denied_lands_on_the_login_screen_with_its_own_code() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("error=access_denied&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&resp), "/login?oidc_error=oidc.access_denied");
        }

        /// §5-2: an unlinked `sub` and a linked-but-disabled account answer
        /// with the same code, so the callback cannot be used to find out
        /// which accounts exist. The audit log keeps them apart.
        #[tokio::test]
        async fn an_unlinked_subject_and_a_disabled_account_are_indistinguishable() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-nobody"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let app = oidc_router(state.clone());
            let unlinked = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&unlinked), "/login?oidc_error=oidc.not_linked");

            let fake2 = Arc::new(ScriptedOidc::linked_as("sub-disabled"));
            let (state2, _dir2) = test_state_with_oidc(fake2.clone());
            let uid = user(&state2, "gustav");
            state2.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-disabled").unwrap();
            state2.auth.disable_user(uid, true).unwrap();
            seed_flow(&state2, &fake2, OidcFlowMode::Login, None, None);
            let app2 = oidc_router(state2.clone());
            let disabled = app2
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake2.state_param()),
                    Some(&fake2.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&disabled), "/login?oidc_error=oidc.not_linked");
            assert!(set_cookies(&disabled).iter().all(|c| !c.starts_with(crate::SESSION_COOKIE)));

            // Identical on the wire, different in the audit log.
            assert_eq!(state.auth.audit_count("auth.login_failed", Some(false)).unwrap(), 1);
            assert_eq!(state2.auth.audit_count("auth.login_failed", Some(false)).unwrap(), 1);
        }

        #[tokio::test]
        async fn a_provider_failure_during_the_exchange_lands_with_provider_unavailable() {
            let fake = Arc::new(ScriptedOidc {
                redeem_error: Some(crate::oidc_api::OidcError::ProviderUnavailable("bad signature".into())),
                ..ScriptedOidc::linked_as("sub-1")
            });
            let (state, _dir) = test_state_with_oidc(fake.clone());
            seed_flow(&state, &fake, OidcFlowMode::Login, None, None);
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&resp), "/login?oidc_error=oidc.provider_unavailable");
        }

        // -------------------------------------------- callback, link --

        #[tokio::test]
        async fn a_link_callback_attaches_the_identity_and_issues_no_session() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-hanna"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "hanna");
            seed_flow(&state, &fake, OidcFlowMode::Link, Some(uid), None);
            let auth = state.auth.clone();
            let app = oidc_router(state);

            let resp = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    Some(principal_of(uid)),
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&resp), "/settings/security");
            assert!(
                set_cookies(&resp).iter().all(|c| !c.starts_with(crate::SESSION_COOKIE)),
                "linking is an operation on an already authenticated session, not a new login"
            );
            assert_eq!(auth.find_oidc_identity(ScriptedOidc::TEST_ISSUER, "sub-hanna").unwrap(), Some(uid));
            // §4.3.6: the account-password NT hash is gone, so the account
            // password is no longer an SMB credential.
            assert!(!auth.nt_hash_present(uid).unwrap());
        }

        /// §4.3.2 step 2. A logout or an account switch during the IdP round
        /// trip must not attach the identity to whoever happens to be signed
        /// in when the browser comes back.
        #[tokio::test]
        async fn a_link_callback_whose_session_changed_is_refused() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-ida"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let starter = user(&state, "ida");
            let someone_else = user(&state, "jan");
            let auth = state.auth.clone();
            let app = oidc_router(state.clone());

            // A different account is signed in when the callback lands.
            seed_flow(&state, &fake, OidcFlowMode::Link, Some(starter), None);
            let switched = app
                .clone()
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    Some(principal_of(someone_else)),
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&switched), "/settings/security?oidc_error=oidc.link_session_changed");
            assert_eq!(auth.find_oidc_identity(ScriptedOidc::TEST_ISSUER, "sub-ida").unwrap(), None);

            // And nobody is signed in at all -- a logout mid-flow.
            seed_flow(&state, &fake, OidcFlowMode::Link, Some(starter), None);
            let logged_out = app
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    None,
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&logged_out), "/settings/security?oidc_error=oidc.link_session_changed");
            assert_eq!(auth.find_oidc_identity(ScriptedOidc::TEST_ISSUER, "sub-ida").unwrap(), None);
        }

        /// The two verdicts §4.3.2 step 3 asks `link_oidc_identity` for, and
        /// the reason `/link/start` does not pre-check them: both are only
        /// knowable once the IdP has said which `sub` this is.
        #[tokio::test]
        async fn a_link_callback_reports_which_way_the_collision_went() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-taken"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let owner = user(&state, "karl");
            let newcomer = user(&state, "lena");
            state.auth.link_oidc_identity(owner, ScriptedOidc::TEST_ISSUER, "sub-taken").unwrap();
            let app = oidc_router(state.clone());

            // Somebody else already has this identity.
            seed_flow(&state, &fake, OidcFlowMode::Link, Some(newcomer), None);
            let taken = app
                .clone()
                .oneshot(callback_req(
                    &format!("code=abc&state={}", fake.state_param()),
                    Some(&fake.binding()),
                    Some(principal_of(newcomer)),
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&taken), "/settings/security?oidc_error=oidc.subject_already_linked");

            // This account already has a different identity.
            let other = Arc::new(ScriptedOidc { seed: "second".into(), ..ScriptedOidc::linked_as("sub-different") });
            let (state2, _dir2) = test_state_with_oidc(other.clone());
            let uid = user(&state2, "karl");
            state2.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-first").unwrap();
            seed_flow(&state2, &other, OidcFlowMode::Link, Some(uid), None);
            let app2 = oidc_router(state2);
            let already = app2
                .oneshot(callback_req(
                    &format!("code=abc&state={}", other.state_param()),
                    Some(&other.binding()),
                    Some(principal_of(uid)),
                ))
                .await
                .unwrap();
            assert_eq!(location_of(&already), "/settings/security?oidc_error=oidc.already_linked");
        }

        // ------------------------------------------------ link/start --

        #[tokio::test]
        async fn link_start_charges_a_password_before_it_will_start_a_flow() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "mara");
            let auth = state.auth.clone();
            let app = oidc_router(state);

            let wrong = app
                .clone()
                .oneshot(
                    HttpRequest::builder()
                        .method("POST")
                        .uri("/api/auth/oidc/link/start")
                        .header("Content-Type", "application/json")
                        .extension(principal_of(uid))
                        .body(Body::from(r#"{"password":"not my password"}"#))
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(wrong.status(), StatusCode::UNAUTHORIZED);
            assert_eq!(json_of(wrong).await["error"]["code"], "auth.invalid_credentials");
            assert!(
                auth.take_oidc_flow(&oidc_sha256(&fake.state_param())).unwrap().is_none(),
                "a refused re-confirmation must not leave a flow behind"
            );

            let right = app
                .oneshot(
                    HttpRequest::builder()
                        .method("POST")
                        .uri("/api/auth/oidc/link/start")
                        .header("Content-Type", "application/json")
                        .extension(principal_of(uid))
                        .body(Body::from(format!(r#"{{"password":"{PASSWORD}","returnTo":"/settings/security"}}"#)))
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(right.status(), StatusCode::OK);
            let cookie = set_cookies(&right).into_iter().find(|c| c.starts_with(crate::OIDC_FLOW_COOKIE)).expect("binding cookie");
            assert!(cookie.contains("Secure"), "{cookie}");
            let body = json_of(right).await;
            assert!(body["authorize_url"].as_str().unwrap().starts_with("https://idp.example.test/"));

            let flow = auth.take_oidc_flow(&oidc_sha256(&fake.state_param())).unwrap().expect("a link flow");
            assert_eq!(flow.mode, OidcFlowMode::Link);
            assert_eq!(flow.link_user, Some(uid));
            assert_eq!(flow.return_to.as_deref(), Some("/settings/security"));
        }

        /// Cross-review M4: an account that is already linked still gets a
        /// flow. The verdict belongs to the callback, where it is not a
        /// TOCTOU guess.
        #[tokio::test]
        async fn link_start_does_not_pre_check_whether_the_account_is_already_linked() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake.clone());
            let uid = user(&state, "nina");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-already").unwrap();
            let app = oidc_router(state);

            let resp = app
                .oneshot(
                    HttpRequest::builder()
                        .method("POST")
                        .uri("/api/auth/oidc/link/start")
                        .header("Content-Type", "application/json")
                        .extension(principal_of(uid))
                        .body(Body::from(format!(r#"{{"password":"{PASSWORD}"}}"#)))
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(resp.status(), StatusCode::OK);
        }

        #[tokio::test]
        async fn link_start_without_a_session_is_401_and_without_oidc_is_404() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake);
            let app = oidc_router(state);
            let body = || Body::from(format!(r#"{{"password":"{PASSWORD}"}}"#));

            let anonymous = app
                .oneshot(
                    HttpRequest::builder()
                        .method("POST")
                        .uri("/api/auth/oidc/link/start")
                        .header("Content-Type", "application/json")
                        .body(body())
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(anonymous.status(), StatusCode::UNAUTHORIZED);

            let (off_state, _dir2) = test_state_with_oidc(Arc::new(ScriptedOidc::disabled()));
            let off_uid = user(&off_state, "otto");
            let off = oidc_router(off_state)
                .oneshot(
                    HttpRequest::builder()
                        .method("POST")
                        .uri("/api/auth/oidc/link/start")
                        .header("Content-Type", "application/json")
                        .extension(principal_of(off_uid))
                        .body(body())
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(off.status(), StatusCode::NOT_FOUND);
            assert_eq!(json_of(off).await["error"]["code"], "oidc.disabled");
        }

        // ----------------------------------------------- DELETE link --

        #[tokio::test]
        async fn unlink_re_confirms_the_password_and_revokes_the_oidc_sessions() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake);
            let uid = user(&state, "petra");
            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "sub-petra").unwrap();
            let oidc_session = state
                .auth
                .create_session(uid, "127.0.0.1".parse().unwrap(), "sso browser", sc_auth::AMR_OIDC)
                .unwrap();
            let password_session = state
                .auth
                .create_session(uid, "127.0.0.1".parse().unwrap(), "password browser", sc_auth::AMR_PASSWORD)
                .unwrap();
            let (_sso_conn, mut sso_rx) =
                state.ws.connect_with_session(uid, Some(sc_auth::token_hash_hex(&oidc_session.0)));
            let (_pw_conn, mut pw_rx) =
                state.ws.connect_with_session(uid, Some(sc_auth::token_hash_hex(&password_session.0)));
            let auth = state.auth.clone();
            let app = oidc_router(state);

            let unlink = |password: &str| {
                HttpRequest::builder()
                    .method("DELETE")
                    .uri("/api/auth/oidc/link")
                    .header("Content-Type", "application/json")
                    .extension(principal_of(uid))
                    .body(Body::from(format!(r#"{{"password":"{password}"}}"#)))
                    .unwrap()
            };

            let wrong = app.clone().oneshot(unlink("not my password")).await.unwrap();
            assert_eq!(wrong.status(), StatusCode::UNAUTHORIZED);
            assert!(auth.oidc_linked(uid).unwrap(), "a refused re-confirmation must not unlink");

            let ok = app.clone().oneshot(unlink(PASSWORD)).await.unwrap();
            assert_eq!(ok.status(), StatusCode::NO_CONTENT);
            assert!(!auth.oidc_linked(uid).unwrap());
            // The password this route re-confirmed is what makes SMB
            // recoverable here and not on the admin path (§4.3.6).
            assert!(auth.nt_hash_present(uid).unwrap());
            assert!(auth.validate_session(&oidc_session.0).unwrap().is_none());
            assert!(auth.validate_session(&password_session.0).unwrap().is_some(), "unlinking withdraws one way in, not every way in");
            assert_eq!(sso_rx.try_recv().unwrap(), crate::ws::ServerMsg::Revoked);
            assert!(pw_rx.try_recv().is_err(), "the password session's socket must stay open");

            let again = app.oneshot(unlink(PASSWORD)).await.unwrap();
            assert_eq!(again.status(), StatusCode::NOT_FOUND);
            assert_eq!(json_of(again).await["error"]["code"], "oidc.not_linked");
        }

        // -------------------------------------------- GET /api/auth/session --

        #[tokio::test]
        async fn the_session_response_carries_the_link_as_a_hint_and_a_string_timestamp() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake);
            let uid = user(&state, "quirin");
            let app = oidc_router(state.clone());
            let req = || {
                HttpRequest::builder()
                    .uri("/api/auth/session")
                    .extension(principal_of(uid))
                    .body(Body::empty())
                    .unwrap()
            };

            let before = json_of(app.clone().oneshot(req()).await.unwrap()).await;
            assert_eq!(before["oidc"]["linked"], false);
            assert!(before["oidc"].get("subject_hint").is_none(), "{}", before["oidc"]);

            state.auth.link_oidc_identity(uid, ScriptedOidc::TEST_ISSUER, "a1b2c3d4e5f6g7h8").unwrap();
            let after = json_of(app.oneshot(req()).await.unwrap()).await;
            assert_eq!(after["oidc"]["linked"], true);
            assert_eq!(after["oidc"]["subject_hint"], "a1b2...g7h8");
            // A decimal string, not a number: 1.8e18 does not survive
            // JavaScript's `number`.
            let linked_ns = after["oidc"]["linked_ns"].as_str().expect("linked_ns must be a string");
            assert!(linked_ns.parse::<i64>().unwrap() > 0);
        }

        #[test]
        fn a_subject_too_short_to_redact_is_reported_as_a_length() {
            assert_eq!(subject_hint("short"), "...(5 characters)");
            assert_eq!(subject_hint("12345678"), "...(8 characters)");
            assert_eq!(subject_hint("123456789"), "1234...6789");
            // Multi-byte subjects must not be sliced mid-character.
            assert_eq!(subject_hint("가나다라마바사아자차"), "가나다라...사아자차");
        }

        // -------------------------------------------------- admin --

        fn admin_of(state: &AppState, name: &str) -> UserId {
            let uid = user(state, name);
            state.auth.set_admin(uid, true).unwrap();
            uid
        }

        #[tokio::test]
        async fn an_admin_can_read_link_and_unlink_somebody_elses_identity() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake);
            let admin = admin_of(&state, "root");
            let target = user(&state, "rosa");
            let auth = state.auth.clone();
            let app = oidc_router(state);
            let path = format!("/api/admin/users/{}/oidc", target.get());

            let unlinked = app
                .clone()
                .oneshot(HttpRequest::builder().uri(&path).extension(principal_of(admin)).body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(unlinked.status(), StatusCode::OK);
            assert_eq!(json_of(unlinked).await["linked"], false);

            let put = app
                .clone()
                .oneshot(
                    HttpRequest::builder()
                        .method("PUT")
                        .uri(&path)
                        .header("Content-Type", "application/json")
                        .extension(principal_of(admin))
                        .body(Body::from(r#"{"subject":"sub-rosa"}"#))
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(put.status(), StatusCode::NO_CONTENT);
            // Linked under the *configured* issuer, never one from the
            // request -- otherwise the row could name an issuer nothing
            // authenticates against.
            assert_eq!(auth.find_oidc_identity(ScriptedOidc::TEST_ISSUER, "sub-rosa").unwrap(), Some(target));

            let linked = app
                .clone()
                .oneshot(HttpRequest::builder().uri(&path).extension(principal_of(admin)).body(Body::empty()).unwrap())
                .await
                .unwrap();
            let body = json_of(linked).await;
            assert_eq!(body["linked"], true);
            assert_eq!(body["issuer"], ScriptedOidc::TEST_ISSUER);
            // The full subject here, not the hint: an administrator has to be
            // able to compare it with what the IdP shows.
            assert_eq!(body["subject"], "sub-rosa");
            assert!(body["linked_ns"].as_str().unwrap().parse::<i64>().unwrap() > 0);

            let del = app
                .clone()
                .oneshot(
                    HttpRequest::builder()
                        .method("DELETE")
                        .uri(&path)
                        .extension(principal_of(admin))
                        .body(Body::empty())
                        .unwrap(),
                )
                .await
                .unwrap();
            // `200` with a body, not `204`: §4.3.6 requires this response to
            // say that SMB access stays closed, and a `204` cannot carry it.
            // The flag is the whole of that answer -- the wording the admin
            // reads is the client's, so the server does not also ship a
            // sentence nobody reads.
            assert_eq!(del.status(), StatusCode::OK);
            let body = json_of(del).await;
            assert_eq!(body["smb_nt_restored"], false);
            assert!(body["note"].is_null());
            assert!(!auth.oidc_linked(target).unwrap());

            let again = app
                .oneshot(
                    HttpRequest::builder()
                        .method("DELETE")
                        .uri(&path)
                        .extension(principal_of(admin))
                        .body(Body::empty())
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(again.status(), StatusCode::NOT_FOUND);
            assert_eq!(json_of(again).await["error"]["code"], "oidc.not_linked");
        }

        #[tokio::test]
        async fn the_admin_put_reports_both_kinds_of_collision_and_refuses_an_empty_subject() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake);
            let admin = admin_of(&state, "root");
            let owner = user(&state, "sven");
            let other = user(&state, "tanja");
            state.auth.link_oidc_identity(owner, ScriptedOidc::TEST_ISSUER, "sub-sven").unwrap();
            let app = oidc_router(state);

            let put = |uid: UserId, subject: &str| {
                HttpRequest::builder()
                    .method("PUT")
                    .uri(format!("/api/admin/users/{}/oidc", uid.get()))
                    .header("Content-Type", "application/json")
                    .extension(principal_of(admin))
                    .body(Body::from(format!(r#"{{"subject":"{subject}"}}"#)))
                    .unwrap()
            };

            let taken = app.clone().oneshot(put(other, "sub-sven")).await.unwrap();
            assert_eq!(taken.status(), StatusCode::CONFLICT);
            assert_eq!(json_of(taken).await["error"]["code"], "oidc.subject_already_linked");

            let already = app.clone().oneshot(put(owner, "sub-someone-new")).await.unwrap();
            assert_eq!(already.status(), StatusCode::CONFLICT);
            assert_eq!(json_of(already).await["error"]["code"], "oidc.already_linked");

            let empty = app.clone().oneshot(put(other, "   ")).await.unwrap();
            assert_eq!(empty.status(), StatusCode::UNPROCESSABLE_ENTITY);
            assert_eq!(json_of(empty).await["error"]["code"], "oidc.invalid_subject");
        }

        /// A hidden button is not access control,
        /// so every one of the three answers `acl.denied` to a plain account.
        #[tokio::test]
        async fn the_admin_routes_are_closed_to_a_non_admin() {
            let fake = Arc::new(ScriptedOidc::linked_as("sub-1"));
            let (state, _dir) = test_state_with_oidc(fake);
            let plain = user(&state, "ulf");
            let app = oidc_router(state);
            let path = format!("/api/admin/users/{}/oidc", plain.get());

            for (method, body) in [
                ("GET", Body::empty()),
                ("PUT", Body::from(r#"{"subject":"sub-x"}"#)),
                ("DELETE", Body::empty()),
            ] {
                let resp = app
                    .clone()
                    .oneshot(
                        HttpRequest::builder()
                            .method(method)
                            .uri(&path)
                            .header("Content-Type", "application/json")
                            .extension(principal_of(plain))
                            .body(body)
                            .unwrap(),
                    )
                    .await
                    .unwrap();
                assert_eq!(resp.status(), StatusCode::FORBIDDEN, "{method}");
            }
        }
    }
}

/// `--features embed-ui`, exercised only when `web/build` has actually been
/// built — `#[derive(RustEmbed)]` reads that folder at compile time, so
/// these only compile (and only need to) when both are true. `bash
/// scripts/verify.sh` runs this gate whenever `web/build/index.html` exists,
/// and skips it otherwise, so a checkout that hasn't run `cd web && npm run
/// build` yet never fails here.
#[cfg(all(test, feature = "embed-ui"))]
mod embed_ui_tests {
    use axum::body::Body;
    use axum::http::{header, Request, StatusCode};
    use tower::ServiceExt;

    fn app() -> (crate::AppState, tempfile::TempDir) {
        crate::testutil::test_state()
    }

    fn get(uri: &str, host: &str) -> Request<Body> {
        Request::builder().uri(uri).header("Host", host).body(Body::empty()).unwrap()
    }

    #[tokio::test]
    async fn a_deep_link_gets_the_html_document_not_a_404() {
        let (state, _dir) = app();
        let router = crate::build_router(state);
        let resp = router.oneshot(get("/b/Documents/Reports", "localhost")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("text/html"), "{ct}");
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let expected = crate::embed::Assets::get("index.html").expect("build has an index.html").data;
        assert_eq!(&bytes[..], &expected[..]);
    }

    #[tokio::test]
    async fn root_gets_the_same_html_document_as_a_deep_link() {
        let (state, _dir) = app();
        let router = crate::build_router(state);
        let resp = router.oneshot(get("/", "localhost")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn a_real_built_asset_is_served_with_its_real_content_type_and_forever_cache() {
        // Hashed filenames change every build, so this asks the embedded
        // build itself which one exists rather than hardcoding one.
        let rel = crate::embed::Assets::iter()
            .find(|f| f.contains("immutable") && f.ends_with(".js"))
            .expect("a real build embeds at least one hashed JS asset")
            .to_string();
        let (state, _dir) = app();
        let router = crate::build_router(state);
        let resp = router.oneshot(get(&format!("/{rel}"), "localhost")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.contains("javascript"), "{ct}");
        let cache = resp.headers().get(header::CACHE_CONTROL).unwrap().to_str().unwrap();
        assert!(cache.contains("immutable"), "{cache}");
        assert!(resp.headers().get(header::ETAG).is_some());
    }

    #[tokio::test]
    async fn revisiting_the_html_document_with_a_matching_etag_gets_304() {
        let (state, _dir) = app();
        let router = crate::build_router(state);
        let resp1 = router.clone().oneshot(get("/", "localhost")).await.unwrap();
        let etag = resp1.headers().get(header::ETAG).unwrap().clone();
        let mut req2 = get("/", "localhost");
        req2.headers_mut().insert(header::IF_NONE_MATCH, etag);
        let resp2 = router.oneshot(req2).await.unwrap();
        assert_eq!(resp2.status(), StatusCode::NOT_MODIFIED);
    }

    /// A real session cookie, not a forged `Principal` extension — this
    /// module goes through the *whole* router (`crate::build_router`),
    /// `middleware::auth` included, so an unmatched `/api/**` path can only
    /// be shown to answer `404` (`routes::admin_catch_all`), rather than the
    /// `401` an unauthenticated caller would see first, by actually clearing
    /// `auth`.
    fn authed_get(state: &crate::AppState, uri: &str, host: &str) -> Request<Body> {
        let uid = state.auth.create_user("tester", &secrecy::SecretString::from("hunter22222")).unwrap();
        let token = state.auth.create_session(uid, "127.0.0.1".parse().unwrap(), "", sc_auth::AMR_PASSWORD).unwrap();
        Request::builder()
            .uri(uri)
            .header("Host", host)
            .header(header::COOKIE, format!("__Host-sc_sid={}", token.as_str()))
            .body(Body::empty())
            .unwrap()
    }

    #[tokio::test]
    async fn api_404_is_still_a_json_envelope_never_the_spa() {
        let (state, _dir) = app();
        let req = authed_get(&state, "/api/this-route-does-not-exist", "localhost");
        let router = crate::build_router(state);
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("application/json"), "{ct}");
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        assert!(serde_json::from_slice::<serde_json::Value>(&bytes).is_ok());
    }

    #[tokio::test]
    async fn unauthenticated_protected_api_route_still_401s_as_json() {
        let (state, _dir) = app();
        let router = crate::build_router(state);
        let resp = router.oneshot(get("/api/fs/list", "localhost")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("application/json"), "{ct}");
    }

    /// An unauthenticated hit on an unmatched `/api/**` path is `401`, not
    /// `404` — `middleware::auth` gates the whole protected surface before a
    /// route even gets a chance to not exist, on purpose (an anonymous
    /// caller does not get to learn which API routes exist and which don't).
    /// The invariant that actually matters for this task is narrower and
    /// still holds either way: it is JSON, never the SPA's HTML.
    #[tokio::test]
    async fn unauthenticated_unmatched_api_path_is_401_json_not_html() {
        let (state, _dir) = app();
        let router = crate::build_router(state);
        let resp = router.oneshot(get("/api/this-route-does-not-exist", "localhost")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("application/json"), "{ct}");
    }

    #[tokio::test]
    async fn a_reserved_prefix_supplied_by_the_assembler_is_not_swallowed() {
        let (mut state, _dir) = app();
        let mut cfg = (*state.cfg).clone();
        cfg.reserved_path_prefixes = vec!["/dav/".to_string()];
        state.cfg = std::sync::Arc::new(cfg);
        let req = authed_get(&state, "/dav/whatever", "localhost");
        let router = crate::build_router(state);
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("application/json"), "{ct}");
    }

    /// The defect this closes: `GET /s/{token}` used to answer raw JSON
    /// unconditionally, so a browser navigating straight to a share link — a
    /// fresh tab, not an in-app route change — got a JSON blob instead of a
    /// page. `/s/` is carved out of `admin_catch_all`'s fallback on purpose
    /// (`OWN_RESERVED_PREFIXES`), so nothing but `public_link_get` itself can
    /// ever hand this path the SPA document.
    #[tokio::test]
    async fn a_share_link_navigated_to_directly_gets_the_html_document() {
        let (mut state, _dir) = app();
        state.core = std::sync::Arc::new(crate::testutil::LinkMockCore::with_link("tok", 1));
        let router = crate::build_router(state);
        let mut req = get("/s/tok", "localhost");
        req.headers_mut().insert(
            header::ACCEPT,
            "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8".parse().unwrap(),
        );
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("text/html"), "{ct}");
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        let expected = crate::embed::Assets::get("index.html").expect("build has an index.html").data;
        assert_eq!(&bytes[..], &expected[..]);
    }

    /// The other half of the same fix: a client that already knows to ask
    /// for JSON — the share page's own `fetch` (no `Accept` override, which
    /// defaults to `*/*`), `curl`, a future native client — must keep getting
    /// exactly the JSON body it always got. Content negotiation only ever
    /// changes the answer for a request that explicitly prefers HTML.
    #[tokio::test]
    async fn a_json_client_reading_the_same_share_link_still_gets_json() {
        let (mut state, _dir) = app();
        state.core = std::sync::Arc::new(crate::testutil::LinkMockCore::with_link("tok", 1));
        let router = crate::build_router(state);
        let resp = router.oneshot(get("/s/tok", "localhost")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(ct.starts_with("application/json"), "{ct}");
        let bytes = http_body_util::BodyExt::collect(resp.into_body()).await.unwrap().to_bytes();
        assert!(serde_json::from_slice::<serde_json::Value>(&bytes).is_ok());
    }

    #[tokio::test]
    async fn the_content_origin_never_serves_the_app_bundle() {
        let (state, _dir) = crate::testutil::test_state_with_content(std::sync::Arc::new(
            crate::content_api::UnimplementedContent,
        ));
        let router = crate::build_router(state);
        // Not `/c/{token}` — an ordinary SPA-shaped path, which on the app
        // origin would get `index.html`. On the content origin it must not.
        let resp = router.oneshot(get("/b/Documents/Reports", "content.example.com")).await.unwrap();
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let ct = resp.headers().get(header::CONTENT_TYPE).unwrap().to_str().unwrap().to_string();
        assert!(!ct.starts_with("text/html"), "{ct}");
    }
}
