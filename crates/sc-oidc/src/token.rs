//! The authorization code exchange: the one request in this whole design
//! that goes from this server to the IdP with a client secret attached.
//!
//! The exact shape is proposal §4.3.1, and it is exact because the draft's
//! version was not: it omitted `grant_type` and `redirect_uri`, and a
//! conforming provider rejects a token request missing either.
//!
//! ```text
//! POST {token_endpoint}
//! Authorization: Basic base64(client_id ":" client_secret)
//! Content-Type: application/x-www-form-urlencoded
//!
//! grant_type=authorization_code
//! &code={code}
//! &redirect_uri={byte for byte what the authorization request carried}
//! &code_verifier={PKCE verifier}
//! ```

use crate::discovery::Discovery;
use crate::fetch::{FetchError, HttpFetch};
use crate::provider::ProviderConfig;
use secrecy::ExposeSecret;
use serde::Deserialize;

#[derive(Debug, thiserror::Error)]
pub enum TokenError {
    #[error(transparent)]
    Fetch(#[from] FetchError),
    #[error("token response is not usable: {0}")]
    Malformed(String),
    /// The provider advertises neither of the two client authentication
    /// methods this crate implements. Not a failure to recover from at
    /// runtime: it means this deployment cannot talk to this IdP, and the
    /// operator has to register the client differently.
    #[error("provider supports no client auth method this client implements: {0}")]
    UnsupportedAuthMethod(String),
}

/// How the client authenticates to the token endpoint. §4.3.1 supports two
/// and no more: anything else (`private_key_jwt`, mTLS) needs key material
/// this product has nowhere to put.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ClientAuth {
    /// HTTP Basic, the default and the one OIDC Core prefers.
    SecretBasic,
    /// Secret in the form body, for providers that do not offer Basic.
    SecretPost,
}

/// Picks the method from the discovery document.
///
/// An absent (or empty) `token_endpoint_auth_methods_supported` means
/// `client_secret_basic`, which is what OIDC Discovery defines the default
/// to be rather than a guess made here.
pub fn client_auth_method(disco: &Discovery) -> Result<ClientAuth, TokenError> {
    let advertised = &disco.token_endpoint_auth_methods_supported;
    if advertised.is_empty() {
        return Ok(ClientAuth::SecretBasic);
    }
    if advertised.iter().any(|m| m == "client_secret_basic") {
        return Ok(ClientAuth::SecretBasic);
    }
    if advertised.iter().any(|m| m == "client_secret_post") {
        return Ok(ClientAuth::SecretPost);
    }
    Err(TokenError::UnsupportedAuthMethod(advertised.join(", ")))
}

/// What comes back. Only `id_token` is read.
///
/// The access token, refresh token and everything else in the response are
/// deliberately not captured: §3.2 lists refresh token storage and IdP
/// session sync as non-goals, and a field that is never stored cannot leak.
#[derive(Debug, Deserialize)]
pub struct TokenResponse {
    pub id_token: String,
}

pub(crate) async fn exchange_code(
    http: &dyn HttpFetch,
    disco: &Discovery,
    cfg: &ProviderConfig,
    code: &str,
    code_verifier: &str,
    redirect_uri: &str,
) -> Result<TokenResponse, TokenError> {
    let method = client_auth_method(disco)?;
    let secret = cfg.client_secret.expose_secret();

    let mut form: Vec<(&str, &str)> = vec![
        ("grant_type", "authorization_code"),
        ("code", code),
        // Identical to the authorization request's, byte for byte. This is
        // why it arrives as an argument rather than being re-selected here: a
        // value assembled twice is a value that can differ twice.
        ("redirect_uri", redirect_uri),
        ("code_verifier", code_verifier),
    ];
    let basic = match method {
        ClientAuth::SecretBasic => Some((cfg.client_id.as_str(), secret)),
        ClientAuth::SecretPost => {
            // RFC 6749 §2.3.1 requires client_id in the body for this
            // method; the secret alone is not enough to identify the client.
            form.push(("client_id", &cfg.client_id));
            form.push(("client_secret", secret));
            None
        }
    };

    let body = http.post_form(&disco.token_endpoint, &form, basic).await?;
    serde_json::from_slice(&body).map_err(|e| TokenError::Malformed(format!("json: {e}")))
}
