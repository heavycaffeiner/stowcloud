//! Every test in this crate runs against an in-process fake `HttpFetch`.
//! None of them opens a socket, and that is a requirement rather than a
//! convenience: proposal §3.1's last Goal is that `sc-oidc`'s tests pass on
//! a runner that has no identity provider and no outbound access.
//!
//! What is deliberately *not* covered here is the rustls implementation
//! itself. §4.1.3 says so plainly, and repeating it is cheaper than letting
//! someone infer from a green suite that the real transport is exercised.

mod discovery;
mod endpoint;
mod fake;
mod flow;
mod id_token;
mod keys;
mod token_exchange;

use crate::discovery::Discovery;
use crate::provider::{OidcProvider, ProviderConfig};
use fake::{FakeIdp, Reply};
use secrecy::SecretString;
use serde_json::json;
use std::sync::Arc;

pub(crate) const ISSUER: &str = "https://idp.example";
pub(crate) const DISCOVERY_URL: &str = "https://idp.example/.well-known/openid-configuration";
pub(crate) const JWKS_URI: &str = "https://idp.example/keys";
pub(crate) const TOKEN_URI: &str = "https://idp.example/token";
pub(crate) const AUTHZ_URI: &str = "https://idp.example/authorize";
pub(crate) const CLIENT_ID: &str = "stowcloud-web";
/// Contains a space and a plus so the form encoding is actually tested by
/// the exchange cases rather than assumed.
pub(crate) const CLIENT_SECRET: &str = "s3cret +value";
pub(crate) const REDIRECT_URI: &str = "https://cloud.example/api/auth/oidc/callback";
pub(crate) const SUBJECT: &str = "8f14e45fceea167a";
/// A fixed clock, so expiry is a property of the token under test and not
/// of when the suite ran.
pub(crate) const NOW: i64 = 1_800_000_000;
pub(crate) const NONCE: &str = "the-nonce-this-flow-issued";

pub(crate) fn discovery_doc() -> serde_json::Value {
    json!({
        "issuer": ISSUER,
        "authorization_endpoint": AUTHZ_URI,
        "token_endpoint": TOKEN_URI,
        "jwks_uri": JWKS_URI,
    })
}

pub(crate) fn jwks(keys: &[serde_json::Value]) -> serde_json::Value {
    json!({ "keys": keys })
}

pub(crate) fn provider(idp: Arc<FakeIdp>) -> OidcProvider {
    OidcProvider::new(
        ProviderConfig {
            issuer: ISSUER.to_string(),
            client_id: CLIENT_ID.to_string(),
            client_secret: SecretString::from(CLIENT_SECRET.to_string()),

            scopes: vec!["openid".to_string(), "profile".to_string()],
            allow_private_endpoints: false,
        },
        idp,
    )
}

/// A fake wired up with a working discovery document and the given key set,
/// which is the starting point for nearly every case below.
pub(crate) fn idp_with_keys(keys: &[serde_json::Value]) -> Arc<FakeIdp> {
    let idp = Arc::new(FakeIdp::new());
    idp.set_get(DISCOVERY_URL, Reply::json(&discovery_doc()));
    idp.set_get(JWKS_URI, Reply::json(&jwks(keys)));
    idp
}

pub(crate) async fn discovered(p: &OidcProvider) -> Arc<Discovery> {
    p.discovery().await.expect("discovery succeeds")
}

/// The claim set a well-behaved provider would issue for this client.
pub(crate) fn good_claims() -> serde_json::Value {
    json!({
        "iss": ISSUER,
        "sub": SUBJECT,
        "aud": CLIENT_ID,
        "exp": NOW + 300,
        "iat": NOW - 5,
        "nonce": NONCE,
    })
}

pub(crate) fn header(alg: &str, kid: Option<&str>) -> serde_json::Value {
    match kid {
        Some(kid) => json!({ "alg": alg, "typ": "JWT", "kid": kid }),
        None => json!({ "alg": alg, "typ": "JWT" }),
    }
}
