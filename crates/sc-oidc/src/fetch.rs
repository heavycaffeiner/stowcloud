//! The one seam between this crate and the network.
//!
//! Every outbound request
//! goes through `HttpFetch`, so the whole OIDC flow can be exercised on a CI
//! runner that has no identity provider and no outbound access at all.

use async_trait::async_trait;

/// What the caller gets when an outbound request does not produce a usable
/// body. Deliberately three variants and no more (proposal §5-2): the HTTP
/// layer above maps every one of them to `oidc.provider_unavailable`, so a
/// finer taxonomy would only be read by the log line.
///
/// The rustls implementation also refuses non-HTTPS URLs and addresses that
/// resolve into private space. Those arrive here as `Transport`, because by
/// the time a URL reaches `HttpFetch` it has already passed
/// [`crate::endpoint`]'s checks in the discovery layer, so a rejection at
/// this level means something got past that and is worth logging as a
/// transport fault rather than a policy decision.
#[derive(Debug, thiserror::Error)]
pub enum FetchError {
    /// Connect, TLS, DNS, timeout, or a body that never finished arriving.
    #[error("outbound request failed: {0}")]
    Transport(String),
    /// A response arrived with a non-2xx status.
    #[error("provider answered with status {0}")]
    Status(u16),
    /// The body went past [`MAX_RESPONSE_BYTES`] and was abandoned mid-read.
    #[error("response body exceeded the {MAX_RESPONSE_BYTES} byte cap")]
    TooLarge,
}

/// Response body ceiling for every outbound call, per the "response body:
/// impose a size cap" row of proposal §4.3.4. A discovery document is a couple of
/// kilobytes and a JWKS with a dozen keys is under ten; 256 KiB leaves three
/// orders of magnitude of headroom while still bounding what a malicious or
/// simply broken IdP can make this process allocate.
pub const MAX_RESPONSE_BYTES: usize = 256 * 1024;

/// Wall-clock ceiling on a single outbound request, connect and TLS
/// handshake included. Every caller of this trait sits inside a browser
/// redirect that a human is waiting on, so the useful ceiling is "shorter
/// than the user's patience", not "long enough for any IdP".
pub const REQUEST_TIMEOUT_SECS: u64 = 10;

/// Every outbound request `sc-oidc` makes goes through this. The real
/// implementation is rustls-backed; every test in this crate uses an
/// in-process fake, so `cargo test` never opens a socket. This is the only
/// reason the OIDC flow is testable on a runner that has no identity
/// provider.
///
/// `#[async_trait]` rather than a native `async fn` in trait: the production
/// impl and the test fake are swapped at runtime through `Arc<dyn HttpFetch>`,
/// and a native async fn in a trait is not object safe on this workspace's
/// 1.88 MSRV.
#[async_trait]
pub trait HttpFetch: Send + Sync {
    async fn get(&self, url: &str) -> Result<Vec<u8>, FetchError>;
    async fn post_form(
        &self,
        url: &str,
        form: &[(&str, &str)],
        basic: Option<(&str, &str)>,
    ) -> Result<Vec<u8>, FetchError>;
}
