//! Error envelope —
//!
//! ```json
//! { "error": { "code": "fs.conflict", "message": "...", "detail": {...} } }
//! ```
//!
//! `code` is the stable machine-readable key the frontend branches on.
//! **500 responses never carry `detail`** — no internal information leaks,
//! only a correlation id (`Sc-Trace`, attached by the `request_id`
//! middleware) ties the response back to a server-side log line.

use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Serialize;
use serde_json::Value;

/// Stable, machine-readable error codes from
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorCode {
    AuthRequired,
    AuthInvalidCredentials,
    /// `POST /api/auth/password` only. Not in the original §1.1 table
    /// (predates the endpoint) but follows the same shape as
    /// `setup.weak_password`: `422` + `detail.min_length`, so a settings
    /// screen and the first-run bootstrap screen render the identical
    /// "need N characters" message from the identical field.
    AuthWeakPassword,
    AclDenied,
    FsNotFound,
    FsConflict,
    FsPrecondition,
    FsInvalidName,
    FsListingExpired,
    /// Not in the §1.1 table, but the share-link contract needs a code the
    /// frontend can branch on: the target moved, expired, or hit its cap, and
    /// **retrying will not help** — which is exactly what `fs.not_found` does
    /// not say.
    FsGone,
    QuotaExceeded,
    RateLimited,
    /// First-run bootstrap. Not in the §1.1 table
    /// because §1.1 predates the endpoint, but they follow its rules: stable
    /// dotted keys the frontend branches on, one per distinct operator
    /// action. `setup.completed` is `410`, not `404`, precisely because the
    /// route *did* exist and is now permanently gone — retrying, restarting
    /// or guessing another token will not bring it back.
    SetupCompleted,
    SetupTokenExpired,
    SetupInvalidToken,
    SetupInvalidUsername,
    SetupWeakPassword,
    Internal,
    /// Not part of the stable table but needed while `sc-core`/`sc-upload`
    /// are placeholders: a route whose backing implementation isn't wired
    /// yet. Never returned once the real dependency lands.
    NotImplemented,
    /// `PATCH`/`DELETE /api/admin/users/{id}` refusing to disable, demote or
    /// delete the deployment's last active administrator
    /// (`sc_auth::AdminGuardError::LastAdmin`). `409`, not `422`: the request
    /// is well-formed, but performing it right now would leave the
    /// deployment with nobody who can administer it — the same shape as
    /// `fs.conflict`, a state conflict rather than a bad input.
    AdminLastAdmin,
    /// `POST /api/auth/app-passwords`, `scope.shares`: a label that is not
    /// one of the caller's own share roots right now. Rejected at creation
    /// rather than silently accepted and then either unsatisfiable or (worse)
    /// silently treated as "all shares" — the app password's scope
    /// contract fails closed here too.
    AuthUnknownShare,
    /// `PATCH /api/admin/users/{id}`, `quota_bytes: 0`.
    /// Rejected rather than silently accepted: a stored `0` reads as
    /// unlimited downstream (`quota_val`'s `$quota > 0` guard,
    /// `crates/sc-compat-nc/src/user.rs`), so accepting it would be a no-op
    /// that looks like it did something.
    AdminInvalidQuota,
    /// OIDC login (`docs/proposals/stowcloud-0-oidc-login.md` §5-2 table A).
    /// The same six symbolic codes also travel as `?oidc_error=` on the
    /// callback's redirects (table B) -- two transports, one vocabulary, which
    /// is why they are named here rather than written as literals at each
    /// route.
    ///
    /// `oidc.disabled` is `404`, not `503`: on a deployment with no `[oidc]`
    /// section these routes are not temporarily unavailable, they are not a
    /// thing this server does.
    OidcDisabled,
    /// No `oidc_identity` row. `404` on `DELETE /api/auth/oidc/link` and the
    /// admin `DELETE`. Deliberately the same code the callback uses for
    /// "linked, but the account is disabled" -- §7.2's account-enumeration
    /// defense; the audit log keeps the two apart.
    OidcNotLinked,
    /// This `(issuer, subject)` already belongs to a different account.
    /// `409` on the admin `PUT`.
    OidcSubjectAlreadyLinked,
    /// This account already has a *different* subject linked; one identity
    /// per account (§4.2). `409` on the admin `PUT`.
    OidcAlreadyLinked,
    /// An empty or unusable `subject` on the admin `PUT`. `422`.
    OidcInvalidSubject,
    /// Discovery, JWKS, the token endpoint, or ID token verification failed.
    /// `503` -- the one OIDC failure that really is "try again later", and the
    /// only one whose cause is on the other side of the network.
    OidcProviderUnavailable,
    /// `POST /api/admin/server-settings/restart` without `force: true` while
    /// uploads or jobs are in flight — the request is well-formed, but
    /// running it right now would drop what those clients are mid-way
    /// through. `409`, same shape as `admin.last_admin`: retry succeeds once
    /// the admin explicitly opts in via `force`.
    RestartBusy,
}

impl ErrorCode {
    pub fn as_str(self) -> &'static str {
        match self {
            ErrorCode::AuthRequired => "auth.required",
            ErrorCode::AuthInvalidCredentials => "auth.invalid_credentials",
            ErrorCode::AuthWeakPassword => "auth.weak_password",
            ErrorCode::AclDenied => "acl.denied",
            ErrorCode::FsNotFound => "fs.not_found",
            ErrorCode::FsConflict => "fs.conflict",
            ErrorCode::FsPrecondition => "fs.precondition",
            ErrorCode::FsInvalidName => "fs.invalid_name",
            ErrorCode::FsListingExpired => "fs.listing_expired",
            ErrorCode::FsGone => "fs.gone",
            ErrorCode::QuotaExceeded => "quota.exceeded",
            ErrorCode::RateLimited => "rate.limited",
            ErrorCode::SetupCompleted => "setup.completed",
            ErrorCode::SetupTokenExpired => "setup.token_expired",
            ErrorCode::SetupInvalidToken => "setup.invalid_token",
            ErrorCode::SetupInvalidUsername => "setup.invalid_username",
            ErrorCode::SetupWeakPassword => "setup.weak_password",
            ErrorCode::Internal => "internal",
            ErrorCode::NotImplemented => "internal.not_implemented",
            ErrorCode::AdminLastAdmin => "admin.last_admin",
            ErrorCode::AuthUnknownShare => "auth.unknown_share",
            ErrorCode::AdminInvalidQuota => "admin.invalid_quota",
            ErrorCode::OidcDisabled => "oidc.disabled",
            ErrorCode::OidcNotLinked => "oidc.not_linked",
            ErrorCode::OidcSubjectAlreadyLinked => "oidc.subject_already_linked",
            ErrorCode::OidcAlreadyLinked => "oidc.already_linked",
            ErrorCode::OidcInvalidSubject => "oidc.invalid_subject",
            ErrorCode::OidcProviderUnavailable => "oidc.provider_unavailable",
            ErrorCode::RestartBusy => "restart.busy",
        }
    }

    pub fn status(self) -> StatusCode {
        match self {
            ErrorCode::AuthRequired => StatusCode::UNAUTHORIZED,
            ErrorCode::AuthInvalidCredentials => StatusCode::UNAUTHORIZED,
            ErrorCode::AuthWeakPassword => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::AclDenied => StatusCode::FORBIDDEN,
            ErrorCode::FsNotFound => StatusCode::NOT_FOUND,
            ErrorCode::FsConflict => StatusCode::CONFLICT,
            ErrorCode::FsPrecondition => StatusCode::PRECONDITION_FAILED,
            ErrorCode::FsInvalidName => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::FsListingExpired => StatusCode::CONFLICT,
            ErrorCode::FsGone => StatusCode::GONE,
            ErrorCode::QuotaExceeded => StatusCode::INSUFFICIENT_STORAGE,
            ErrorCode::RateLimited => StatusCode::TOO_MANY_REQUESTS,
            ErrorCode::SetupCompleted => StatusCode::GONE,
            ErrorCode::SetupTokenExpired => StatusCode::FORBIDDEN,
            ErrorCode::SetupInvalidToken => StatusCode::FORBIDDEN,
            ErrorCode::SetupInvalidUsername => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::SetupWeakPassword => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::Internal => StatusCode::INTERNAL_SERVER_ERROR,
            ErrorCode::NotImplemented => StatusCode::NOT_IMPLEMENTED,
            ErrorCode::AdminLastAdmin => StatusCode::CONFLICT,
            ErrorCode::AuthUnknownShare => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::AdminInvalidQuota => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::OidcDisabled => StatusCode::NOT_FOUND,
            ErrorCode::OidcNotLinked => StatusCode::NOT_FOUND,
            ErrorCode::OidcSubjectAlreadyLinked => StatusCode::CONFLICT,
            ErrorCode::OidcAlreadyLinked => StatusCode::CONFLICT,
            ErrorCode::OidcInvalidSubject => StatusCode::UNPROCESSABLE_ENTITY,
            ErrorCode::OidcProviderUnavailable => StatusCode::SERVICE_UNAVAILABLE,
            ErrorCode::RestartBusy => StatusCode::CONFLICT,
        }
    }
}

/// Application error. `IntoResponse` renders it as the
/// envelope directly — this is the single source of truth for the shape, so
/// no separate "error mapper" needs to duplicate the logic; the `error_mapper`
/// middleware (`middleware::error_mapper`) exists purely as defense in depth
/// for non-`AppError` responses (e.g. tower-http's body-limit 413).
#[derive(Debug, Clone)]
pub struct AppError {
    pub code: ErrorCode,
    pub message: String,
    pub detail: Option<Value>,
    /// Overrides `code.status()` when set (e.g. `fs.cross_device` /
    /// `auth.totp_required` are 200s that carry an error-shaped `code` as a
    /// *flow* signal, not a failure).
    pub status_override: Option<StatusCode>,
}

impl AppError {
    pub fn new(code: ErrorCode, message: impl Into<String>) -> Self {
        Self { code, message: message.into(), detail: None, status_override: None }
    }

    pub fn with_detail(mut self, detail: Value) -> Self {
        self.detail = Some(detail);
        self
    }

    pub fn with_status(mut self, status: StatusCode) -> Self {
        self.status_override = Some(status);
        self
    }

    pub fn auth_required() -> Self {
        Self::new(ErrorCode::AuthRequired, "authentication required")
    }

    pub fn invalid_credentials() -> Self {
        // Deliberately identical message/code whether the account exists or
        // not — account-enumeration defense.
        Self::new(ErrorCode::AuthInvalidCredentials, "invalid credentials")
    }

    pub fn acl_denied(by: impl Into<String>) -> Self {
        Self::new(ErrorCode::AclDenied, "permission denied")
            .with_detail(serde_json::json!({ "by": by.into() }))
    }

    pub fn not_found() -> Self {
        Self::new(ErrorCode::FsNotFound, "not found")
    }

    pub fn conflict(path: impl Into<String>, etag: Option<&str>) -> Self {
        Self::new(ErrorCode::FsConflict, "destination already exists")
            .with_detail(serde_json::json!({ "path": path.into(), "etag": etag }))
    }

    pub fn precondition(current_etag: impl Into<String>) -> Self {
        Self::new(ErrorCode::FsPrecondition, "If-Match precondition failed")
            .with_detail(serde_json::json!({ "current_etag": current_etag.into() }))
    }

    pub fn invalid_name(reason: impl Into<String>) -> Self {
        let reason = reason.into();
        Self::new(ErrorCode::FsInvalidName, "invalid name").with_detail(serde_json::json!({ "reason": reason }))
    }

    /// Same `422 fs.invalid_name` envelope as [`Self::invalid_name`], plus the
    /// catalogue key and placeholders the browser needs to say this in the
    /// reader's own language. `reason` stays for the callers that only ever
    /// wanted a log line — tests, WebDAV, SMB.
    pub fn invalid_keyed(key: &str, params: serde_json::Value, message: &str) -> Self {
        Self::new(ErrorCode::FsInvalidName, "invalid name").with_detail(
            serde_json::json!({ "reason": message, "reason_key": key, "reason_params": params }),
        )
    }

    /// A `404` carrying a catalogue key, for the one route whose "not found"
    /// is a name the caller typed rather than a resource that went away:
    /// `DELETE /api/admin/server-settings/{section}`.
    pub fn not_found_keyed(key: &str, params: serde_json::Value, message: &str) -> Self {
        Self::new(ErrorCode::FsNotFound, "not found").with_detail(
            serde_json::json!({ "reason": message, "reason_key": key, "reason_params": params }),
        )
    }

    /// `active_uploads`/`running_jobs` let the settings screen name the exact
    /// number of jobs in flight instead of showing a generic refusal — the
    /// admin needs that number to decide whether `force` is safe right now.
    pub fn restart_busy(active_uploads: usize, running_jobs: usize) -> Self {
        Self::new(ErrorCode::RestartBusy, "uploads or jobs are in flight")
            .with_detail(serde_json::json!({ "active_uploads": active_uploads, "running_jobs": running_jobs }))
    }

    pub fn rate_limited(retry_after_s: u32) -> Self {
        Self::new(ErrorCode::RateLimited, "rate limited")
            .with_status(StatusCode::TOO_MANY_REQUESTS)
            .with_detail(serde_json::json!({ "retry_after": retry_after_s }))
    }

    /// **Never** attach a `detail` here — that's the whole point (§1.1).
    /// `internal_msg` is logged server-side by the caller (with `Sc-Trace`)
    /// but never serialized into the response.
    pub fn internal() -> Self {
        Self::new(ErrorCode::Internal, "internal error")
    }

    /// `410` — the target of a share link is permanently unavailable.
    pub fn gone() -> Self {
        Self::new(ErrorCode::FsGone, "this link is no longer available")
    }

    pub fn not_implemented() -> Self {
        Self::new(ErrorCode::NotImplemented, "not implemented yet")
    }

    /// `409` — refusing to disable, demote or delete the deployment's last
    /// active administrator (`sc_auth::AdminGuardError::LastAdmin`).
    pub fn admin_last_admin() -> Self {
        Self::new(ErrorCode::AdminLastAdmin, "refusing to remove the last administrator")
    }

    /// `422` — `POST /api/auth/app-passwords`'s `scope.shares` named a label
    /// that isn't one of the caller's own share roots right now.
    pub fn unknown_share(label: impl Into<String>) -> Self {
        let label = label.into();
        Self::new(ErrorCode::AuthUnknownShare, "no such share").with_detail(serde_json::json!({ "label": label }))
    }

    /// `422` — a `quota_bytes: 0` patch (see [`ErrorCode::AdminInvalidQuota`]).
    pub fn invalid_quota() -> Self {
        Self::new(ErrorCode::AdminInvalidQuota, "quota must be greater than zero, or null for unlimited")
    }
}

impl std::fmt::Display for AppError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{} ({})", self.message, self.code.as_str())
    }
}

impl std::error::Error for AppError {}

#[derive(Serialize)]
struct ErrorBody<'a> {
    code: &'a str,
    message: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<&'a Value>,
}

#[derive(Serialize)]
struct Envelope<'a> {
    error: ErrorBody<'a>,
}

impl IntoResponse for AppError {
    fn into_response(self) -> Response {
        // Belt and braces: even if a caller mistakenly attaches `detail` to
        // an Internal error, it never reaches the wire.
        let detail = if matches!(self.code, ErrorCode::Internal) { None } else { self.detail.as_ref() };
        let body = Envelope { error: ErrorBody { code: self.code.as_str(), message: &self.message, detail } };
        let status = self.status_override.unwrap_or_else(|| self.code.status());
        (status, Json(body)).into_response()
    }
}

impl From<anyhow::Error> for AppError {
    fn from(e: anyhow::Error) -> Self {
        tracing::error!(error = %e, "internal error mapped to AppError::internal");
        AppError::internal()
    }
}

impl AppError {
    /// The `{code, message, detail}` object as it appears both inside the
    /// top-level `{"error": ...}` envelope and inside a batch
    /// per-item `error` field.
    pub fn to_wire(&self) -> Value {
        let detail = if matches!(self.code, ErrorCode::Internal) { None } else { self.detail.clone() };
        serde_json::json!({ "code": self.code.as_str(), "message": self.message, "detail": detail })
    }
}

/// Renders the exact §1.1 envelope for a raw `(code, message)` pair without
/// constructing a full `AppError` — used by middleware that runs before a
/// `Principal`/handler context exists (e.g. `HostGuard`, `RateLimit`).
pub fn envelope_response(status: StatusCode, code: ErrorCode, message: &str) -> Response {
    let body = Envelope { error: ErrorBody { code: code.as_str(), message, detail: None } };
    (status, Json(body)).into_response()
}
