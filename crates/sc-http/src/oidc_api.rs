//! Trait boundary for the OpenID Connect relying party.
//!
//! `docs/proposals/stowcloud-0-oidc-login.md` §4.1.1 puts the protocol core
//! in its own crate (`sc-oidc`) so that the TLS stack and the outbound HTTP
//! client it needs stay out of everything else. This module is the other end
//! of that split: the narrow waist `sc-http` drives from a route handler,
//! implemented in `sc-server` over a real `sc_oidc::OidcProvider`.
//!
//! It is the same shape `core_api`, `search_api`, `setup_api` and
//! `settings_api` already use, and for the same two reasons. The dependency
//! direction stays one-way -- this crate never learns what a JWKS is, so no
//! build of it ever links rustls, and `--no-default-features` on `sc-server`
//! drops the whole outbound stack by simply not installing an adapter. And
//! the callback's ordering rules (§5-1's pseudocode) become testable without
//! a fake IdP, a signed ID token, or a socket: a scripted [`OidcApi`] answers
//! the four questions a handler asks and the handler's own logic is what the
//! test is actually about.
//!
//! What this trait deliberately does *not* expose: `state`, `nonce`, the PKCE
//! verifier's challenge, the ID token, or any claim other than `iss` and
//! `sub`. §3.2 is explicit that no other claim is read or stored, and a port
//! that could not hand one over is a stronger guarantee than a rule saying
//! nobody should.

use async_trait::async_trait;
use secrecy::SecretString;

use crate::core_api::CoreError;

/// What `GET /api/auth/oidc/config` may tell an anonymous caller: whether to
/// draw the button, and what to write on it. Nothing else -- §5-1 withholds
/// the issuer URL and the client id from this route on purpose.
#[derive(Clone, Debug, Default)]
pub struct OidcDisplay {
    pub enabled: bool,
    pub display_name: String,
}

/// One authorization request, ready to be persisted and handed to a browser.
///
/// Three digests and two secrets rather than the plaintexts they came from,
/// because that is exactly what the caller needs: `oidc_flow` stores the
/// digests, the cookie carries `binding`, and the token endpoint later gets
/// `code_verifier`. The `state` and `nonce` plaintexts never leave the
/// implementation -- they are already inside `authorize_url`, and a second
/// copy in a struct that crosses a crate boundary is a second place for them
/// to be logged.
pub struct StartedFlow {
    /// Where the browser is sent: `authorization_endpoint` plus
    /// `response_type`, `client_id`, `redirect_uri`, `scope`, `state`,
    /// `nonce`, and the S256 PKCE challenge.
    pub authorize_url: String,
    pub state_hash: [u8; 32],
    /// The `__Host-sc_oidc` cookie value. §4.3.1: this, not `state`, is what
    /// stops an attacker delivering a legitimate callback URL to somebody
    /// else's browser.
    pub binding: SecretString,
    pub binding_hash: [u8; 32],
    pub nonce_hash: [u8; 32],
    pub code_verifier: SecretString,
}

/// The entire result of a successful round trip. §3.2 non-goals: `email`,
/// `name` and `preferred_username` are not read, so they are not here to be
/// accidentally trusted later.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct VerifiedIdentity {
    pub issuer: String,
    pub subject: String,
}

/// Why an OIDC operation could not complete.
///
/// Two variants, not the dozen `sc-oidc` distinguishes, because the HTTP
/// layer only has two answers to give. §5-2 table B folds discovery, JWKS,
/// the token endpoint and every ID-token verification failure into the single
/// code `oidc.provider_unavailable`; keeping them apart here would invent a
/// distinction no response can carry. The detail string is for the server
/// log, never for the wire.
#[derive(Clone, Debug)]
pub enum OidcError {
    /// Discovery, JWKS, the token exchange, or ID token verification failed.
    /// `503` on a JSON route, `?oidc_error=oidc.provider_unavailable` on the
    /// callback.
    ProviderUnavailable(String),
    /// This server could not hold up its own end -- no entropy, no adapter
    /// wired. `500`.
    Internal(String),
}

impl std::fmt::Display for OidcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            OidcError::ProviderUnavailable(m) => write!(f, "provider unavailable: {m}"),
            OidcError::Internal(m) => write!(f, "internal error: {m}"),
        }
    }
}

impl std::error::Error for OidcError {}

impl From<OidcError> for CoreError {
    fn from(e: OidcError) -> Self {
        CoreError::Internal(e.to_string())
    }
}

/// Every method has a default body that behaves as "OIDC is not configured",
/// mirroring `UploadApi`/`SettingsApi`, so `AppState` stays constructible
/// without an adapter and a build with the feature stripped answers
/// `oidc.disabled` on every route rather than failing to compile.
///
/// `#[async_trait]` rather than a native `async fn` in trait, for the reason
/// `sc_oidc::HttpFetch` documents: the real implementation and the test fakes
/// are swapped at runtime through `Arc<dyn OidcApi>`, and a native async fn
/// in a trait is not object safe on this workspace's 1.88 MSRV.
#[async_trait]
pub trait OidcApi: Send + Sync {
    fn display(&self) -> OidcDisplay {
        OidcDisplay::default()
    }

    /// The configured issuer, or `None` when OIDC is not active.
    ///
    /// Only `PUT /api/admin/users/{id}/oidc` needs it: an admin types a
    /// `subject`, and the `(issuer, subject)` pair an `oidc_identity` row is
    /// keyed by has to come from somewhere. It comes from configuration, so
    /// that a manual link and a link made through the real flow are the same
    /// row. It is never put on the wire outside that admin surface.
    fn issuer(&self) -> Option<String> {
        None
    }

    /// Mints the secrets for one authorization request and builds the URL the
    /// browser is redirected to. Fetches discovery on the way (cached for an
    /// hour), which is why this is fallible and why a dead IdP shows up here
    /// as `ProviderUnavailable` rather than at startup: §4.3.4 refuses to make
    /// the server's boot depend on the provider being up.
    /// `host` selects which registered `oidc.redirect_uris` entry this flow
    /// uses, the same way every other URL this server hands out is selected.
    /// Nothing is derived from it: an unrecognised host gets the first entry.
    async fn begin(&self, _host: Option<&str>) -> Result<StartedFlow, OidcError> {
        Err(not_wired())
    }

    /// Exchanges `code` for an ID token and verifies it -- §4.3.3's eleven
    /// steps, including that `nonce` hashes to `nonce_hash`.
    ///
    /// One call rather than the three §5-1's pseudocode writes
    /// (`discovery`/`exchange_code`/`verify_id_token`) because all three
    /// answer with the same error code and the caller has no decision to make
    /// between them. The ordering *before* this point -- consume the flow,
    /// check the binding cookie, only then look at what the IdP sent -- is the
    /// part that matters, and it stays entirely in the handler where it can be
    /// read and tested.
    /// `host` selects the same redirect URI `begin` did, and it selects it
    /// the same way rather than by storing it with the flow: the callback
    /// lands *at* that redirect URI, so its `Host` is that entry's authority
    /// by construction.
    async fn redeem(
        &self,
        _host: Option<&str>,
        _code: &str,
        _code_verifier: &SecretString,
        _nonce_hash: &[u8; 32],
    ) -> Result<VerifiedIdentity, OidcError> {
        Err(not_wired())
    }
}

fn not_wired() -> OidcError {
    OidcError::Internal("oidc backend not wired".into())
}

/// The default `AppState::oidc`: OIDC is off.
///
/// Used by every test that is not about OIDC, by a build made with
/// `--no-default-features`, and by a deployment whose `[oidc]` section is
/// absent, disabled, or invalid (§4.3.1: an empty or non-https
/// `oidc.redirect_uri` means OIDC never activates, and the rest of the server
/// keeps working).
pub struct OidcDisabled;
impl OidcApi for OidcDisabled {}
