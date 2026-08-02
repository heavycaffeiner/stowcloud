//! Trait boundary for the first-run administrator bootstrap.
//!
//! The *route* lives in this crate because `/api/setup` is part of the native
//! API surface and has to inherit the whole middleware stack — `HostGuard`,
//! `SecurityHeaders`, the per-IP `RateLimit`, the §1.1 error envelope and the
//! `Sc-Trace` correlation id. A route registered outside `build_router` gets
//! none of those, and this is an *unauthenticated endpoint that creates an
//! administrator*: it is the last one that should opt out of them.
//!
//! The *implementation* deliberately does not live here. Only `sc-server`
//! knows where the one-time token was written, when this process booted, and
//! how to take the token out of circulation — so this crate declares the
//! contract and `sc-server` binds it, exactly as [`crate::core_api::CoreApi`]
//! and [`crate::content_api::ContentApi`] do.

use std::net::IpAddr;

use secrecy::SecretString;

/// What the caller gets back when the bootstrap succeeds. Deliberately not a
/// session: the account is created, and the client then authenticates through
/// the one and only credential-issuing path, `POST /api/auth/login`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SetupOutcome {
    pub user_id: u32,
    pub username: String,
}

/// Every way `complete` can refuse. Each maps to exactly one
/// `crate::error::ErrorCode`, so the frontend branches on a stable string.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SetupError {
    /// An account already exists — setup is permanently closed and no
    /// restart will reopen it. `410 Gone`.
    Completed,
    /// The window elapsed (or no token was issued for this process). A
    /// restart issues a fresh one. `403`.
    Expired,
    /// The presented token did not match. `403`.
    InvalidToken,
    /// The requested login name is unusable; the `&'static str` is a fixed
    /// reason string, never an echo of the input. `422`.
    InvalidUsername(&'static str),
    /// Below's minimum. `422`.
    WeakPassword { min_len: usize },
    /// Rejected before the token was spent — the caller may retry.
    Internal,
}

/// First-run bootstrap, as seen by the HTTP layer.
///
/// Implementations own the whole security story: the timing-safe token
/// comparison, the expiry check, single-use consumption, the "an admin
/// already exists" gate, account creation and the audit row. The handler in
/// [`crate::routes`] adds only rate limiting and status-code mapping — there
/// is no security decision it can get wrong on its own.
pub trait SetupApi: Send + Sync {
    /// Whether first-run setup is still outstanding.
    ///
    /// This is the answer `GET /api/setup` returns to an unauthenticated
    /// caller, so it must be a bare boolean and nothing more: no token state,
    /// no expiry timestamp, no account names.
    fn is_required(&self) -> bool;

    /// Verify `token` and create the administrator account.
    ///
    /// Synchronous on purpose — it runs Argon2 — and therefore called from
    /// `spawn_blocking` by the handler.
    fn complete(
        &self,
        token: &str,
        username: &str,
        password: &SecretString,
        ip: IpAddr,
    ) -> Result<SetupOutcome, SetupError>;
}

/// The default binding: setup is over and nothing can reopen it.
///
/// Fails closed, which is the right default for an `AppState` assembled
/// without an explicit gate — a missing binding must never leave an
/// admin-creating endpoint live.
pub struct SetupClosed;

impl SetupApi for SetupClosed {
    fn is_required(&self) -> bool {
        false
    }

    fn complete(
        &self,
        _token: &str,
        _username: &str,
        _password: &SecretString,
        _ip: IpAddr,
    ) -> Result<SetupOutcome, SetupError> {
        Err(SetupError::Completed)
    }
}
