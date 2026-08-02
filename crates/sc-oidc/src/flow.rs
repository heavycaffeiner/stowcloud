//! The four random values one login attempt needs, and the authorize URL
//! they go into.
//!
//! Proposal §4.3.1: `state`, `nonce`, the PKCE `code_verifier`, and the
//! browser-binding cookie value, each 256 bits of CSPRNG output. Three of
//! them are stored as SHA-256 hashes and one (the verifier) has to be kept
//! in the clear, because PKCE requires presenting the original to the
//! token endpoint.
//!
//! What each one actually defends, since they are easy to confuse:
//!
//!  * `state` stops an attacker choosing the value the callback carries.
//!  * the **binding cookie** stops an attacker delivering a legitimate
//!    `state` to somebody else's browser, which is the login-CSRF that
//!    `state` alone does not prevent (RFC 9700, and correction 2 of the
//!    proposal).
//!  * `nonce` ties the ID token to this authorization request.
//!  * `code_verifier` ties the code redemption to whoever started the flow.

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;

/// 32 bytes, which is 43 base64url characters with no padding. That is also
/// exactly what RFC 7636 wants of a `code_verifier` (43 to 128 characters
/// from the unreserved set) and what §4.3.1 specifies for the cookie.
pub const FLOW_SECRET_BYTES: usize = 32;

#[derive(Debug, thiserror::Error)]
pub enum FlowError {
    #[error("csprng unavailable: {0}")]
    Random(String),
}

/// The secrets for one in-flight authorization request.
///
/// Held only long enough to write the hashes to `oidc_flow`, set the
/// cookie, and build the redirect. The struct redacts itself in `Debug`
/// because these are exactly the values a stray `tracing` field would leak.
#[derive(Clone)]
pub struct FlowSecrets {
    pub state: String,
    pub nonce: String,
    pub binding: String,
    pub code_verifier: String,
}

impl std::fmt::Debug for FlowSecrets {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("FlowSecrets(redacted)")
    }
}

impl FlowSecrets {
    pub fn generate() -> Result<Self, FlowError> {
        Ok(Self {
            state: random_token()?,
            nonce: random_token()?,
            binding: random_token()?,
            code_verifier: random_token()?,
        })
    }

    pub fn state_hash(&self) -> [u8; 32] {
        sha256(&self.state)
    }

    pub fn nonce_hash(&self) -> [u8; 32] {
        sha256(&self.nonce)
    }

    pub fn binding_hash(&self) -> [u8; 32] {
        sha256(&self.binding)
    }

    /// PKCE S256. The `plain` method exists in RFC 7636 and is not offered
    /// here: it makes the challenge equal to the verifier, so anyone who
    /// can read the authorization request can redeem the code.
    pub fn code_challenge(&self) -> String {
        URL_SAFE_NO_PAD.encode(Sha256::digest(self.code_verifier.as_bytes()))
    }
}

fn random_token() -> Result<String, FlowError> {
    let mut raw = [0u8; FLOW_SECRET_BYTES];
    getrandom::getrandom(&mut raw).map_err(|e| FlowError::Random(e.to_string()))?;
    Ok(URL_SAFE_NO_PAD.encode(raw))
}

pub fn sha256(input: &str) -> [u8; 32] {
    Sha256::digest(input.as_bytes()).into()
}

/// Constant-time hash comparison, for the callback's binding cookie check.
/// Lengths that differ answer `false` without comparing, which leaks only
/// the length of a value whose length is fixed and public.
pub fn hash_eq(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.ct_eq(b).unwrap_u8() == 1
}
