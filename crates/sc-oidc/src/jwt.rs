//! ID token verification: the eleven steps of proposal §4.3.3, in that
//! order.
//!
//! The order is part of the design, not an accident of how the code fell
//! out. Cheap structural checks come before the signature so that a token
//! nobody could have issued is thrown away without a public key operation,
//! and every claim check comes after the signature so that no decision is
//! made on the strength of an unverified string.
//!
//! Verification is `ring` and nothing else. `jsonwebtoken` would wrap the
//! same crate; `rsa` is under RUSTSEC-2023-0071 and `deny.toml` has an
//! empty ignore list on purpose (proposal §4.1.2).

use crate::jwks::{Jwk, JwksError};
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;

/// Step 1's allow list. `none` and the HS family are absent by
/// construction rather than by a check somewhere: `none` means anyone can
/// mint a token, and an HMAC alg makes the *public* key the verification
/// key, so a token signed with the RSA modulus as an HMAC secret would
/// verify. Both are the textbook alg-confusion attacks.
pub const ALLOWED_ALGS: [&str; 2] = ["RS256", "ES256"];

/// Clock skew allowance, fixed at 60 seconds. Deliberately not a setting:
/// an operator who can raise it has a knob whose only effect is to make
/// expired tokens work, and the failure it appears to fix (a server whose
/// clock is minutes off) is a problem to fix at the server.
pub const CLOCK_LEEWAY_SECS: i64 = 60;

#[derive(Debug, thiserror::Error)]
pub enum JwtError {
    #[error("id token is not a well formed jws: {0}")]
    Malformed(String),
    #[error("id token alg `{0}` is not accepted")]
    UnsupportedAlg(String),
    #[error("id token header has no kid")]
    MissingKid,
    #[error("id token kid is not in the provider's jwks")]
    UnknownKid,
    #[error("jwk does not match the token's alg: {0}")]
    KeyTypeMismatch(String),
    #[error("id token signature does not verify")]
    BadSignature,
    #[error("id token is expired or not yet valid")]
    Expired,
    #[error("id token iss is not the configured issuer")]
    IssuerMismatch,
    #[error("id token aud does not contain this client")]
    AudienceMismatch,
    #[error("id token azp is not this client")]
    AzpMismatch,
    #[error("id token nonce does not match the one this flow issued")]
    NonceMismatch,
    #[error("id token has no usable sub")]
    MissingSub,
    /// Step 3 could not reach or parse the key set. Distinct from
    /// `UnknownKid`: nothing is known to be wrong with the token. The HTTP
    /// layer answers `oidc.provider_unavailable` for this and
    /// `oidc.not_linked` style failures for the rest.
    #[error(transparent)]
    Jwks(#[from] JwksError),
}

/// Everything this crate is willing to say about a verified token.
///
/// Two fields, on purpose. §4.3.3's closing rule is that no claim other
/// than `sub` takes part in authentication: `email` is mutable at the IdP
/// and `email_verified` is not always trustworthy, so neither is read or
/// stored. `iss` is here because the identity link is keyed on the pair.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct IdTokenClaims {
    pub iss: String,
    pub sub: String,
}

#[derive(Debug, Deserialize)]
pub(crate) struct Header {
    pub alg: String,
    #[serde(default)]
    pub kid: Option<String>,
}

/// A compact JWS split into the pieces verification needs, with the signing
/// input kept as the exact bytes that were transmitted. Re-encoding the
/// header and payload to rebuild it would be a subtle way to accept a
/// signature over something other than what arrived.
pub(crate) struct Jws<'a> {
    pub header: Header,
    pub claims: Vec<u8>,
    pub signing_input: &'a [u8],
    pub signature: Vec<u8>,
}

pub(crate) fn split_jws(token: &str) -> Result<Jws<'_>, JwtError> {
    let mut dots = token.match_indices('.');
    let (first, _) = dots
        .next()
        .ok_or_else(|| JwtError::Malformed("no separator".into()))?;
    let (second, _) = dots
        .next()
        .ok_or_else(|| JwtError::Malformed("only two segments".into()))?;
    if dots.next().is_some() {
        // A JWE has five segments. This crate does not do encrypted ID
        // tokens, and letting one through here would mean parsing its
        // header as if it were a JWS.
        return Err(JwtError::Malformed("more than three segments".into()));
    }

    let header_b64 = &token[..first];
    let claims_b64 = &token[first + 1..second];
    let sig_b64 = &token[second + 1..];

    let header_raw = b64(header_b64, "header")?;
    let header: Header = serde_json::from_slice(&header_raw)
        .map_err(|e| JwtError::Malformed(format!("header json: {e}")))?;

    Ok(Jws {
        header,
        claims: b64(claims_b64, "payload")?,
        signing_input: &token.as_bytes()[..second],
        signature: b64(sig_b64, "signature")?,
    })
}

fn b64(s: &str, what: &str) -> Result<Vec<u8>, JwtError> {
    URL_SAFE_NO_PAD
        .decode(s)
        .map_err(|e| JwtError::Malformed(format!("{what} base64url: {e}")))
}

/// Step 4. The point of doing this ourselves, when `ring` would reject a
/// mismatched key anyway, is that the rule is ours: it belongs in this
/// file where a reader can see it, not in the failure mode of whichever
/// parser a dependency happens to use this year.
pub(crate) fn check_key_matches_alg(jwk: &Jwk, alg: &str) -> Result<(), JwtError> {
    match alg {
        "RS256" => {
            if jwk.kty != "RSA" {
                return Err(JwtError::KeyTypeMismatch(format!(
                    "RS256 needs kty=RSA, got {}",
                    jwk.kty
                )));
            }
        }
        "ES256" => {
            if jwk.kty != "EC" {
                return Err(JwtError::KeyTypeMismatch(format!(
                    "ES256 needs kty=EC, got {}",
                    jwk.kty
                )));
            }
            if jwk.crv.as_deref() != Some("P-256") {
                return Err(JwtError::KeyTypeMismatch(format!(
                    "ES256 needs crv=P-256, got {:?}",
                    jwk.crv
                )));
            }
        }
        other => return Err(JwtError::UnsupportedAlg(other.to_string())),
    }
    // The JWK's own `alg`, when it states one, has to agree. A provider
    // that labels a key RS256 and signs ES256 with it is either broken or
    // being replayed at us.
    if let Some(key_alg) = &jwk.alg {
        if key_alg != alg {
            return Err(JwtError::KeyTypeMismatch(format!(
                "jwk alg is {key_alg}, token alg is {alg}"
            )));
        }
    }
    if let Some(use_) = &jwk.use_ {
        if use_ != "sig" {
            return Err(JwtError::KeyTypeMismatch(format!(
                "jwk use is {use_}, not sig"
            )));
        }
    }
    if let Some(ops) = &jwk.key_ops {
        if !ops.iter().any(|o| o == "verify") {
            return Err(JwtError::KeyTypeMismatch(
                "jwk key_ops does not permit verify".into(),
            ));
        }
    }
    Ok(())
}

/// Step 5.
pub(crate) fn verify_signature(jwk: &Jwk, alg: &str, jws: &Jws<'_>) -> Result<(), JwtError> {
    match alg {
        "RS256" => {
            let n = jwk_part(jwk.n.as_deref(), "n")?;
            let e = jwk_part(jwk.e.as_deref(), "e")?;
            let key = ring::signature::RsaPublicKeyComponents { n, e };
            key.verify(
                &ring::signature::RSA_PKCS1_2048_8192_SHA256,
                jws.signing_input,
                &jws.signature,
            )
            .map_err(|_| JwtError::BadSignature)
        }
        "ES256" => {
            let x = jwk_part(jwk.x.as_deref(), "x")?;
            let y = jwk_part(jwk.y.as_deref(), "y")?;
            if x.len() != 32 || y.len() != 32 {
                return Err(JwtError::KeyTypeMismatch(
                    "P-256 coordinates must be 32 bytes each".into(),
                ));
            }
            // SEC 1 uncompressed point, which is what ring's fixed-width
            // ECDSA verifier takes.
            let mut point = Vec::with_capacity(65);
            point.push(0x04);
            point.extend_from_slice(&x);
            point.extend_from_slice(&y);
            ring::signature::UnparsedPublicKey::new(
                &ring::signature::ECDSA_P256_SHA256_FIXED,
                point,
            )
            .verify(jws.signing_input, &jws.signature)
            .map_err(|_| JwtError::BadSignature)
        }
        other => Err(JwtError::UnsupportedAlg(other.to_string())),
    }
}

fn jwk_part(raw: Option<&str>, name: &str) -> Result<Vec<u8>, JwtError> {
    let raw = raw.ok_or_else(|| JwtError::KeyTypeMismatch(format!("jwk has no {name}")))?;
    URL_SAFE_NO_PAD
        .decode(raw)
        .map_err(|e| JwtError::KeyTypeMismatch(format!("jwk {name} base64url: {e}")))
}

#[derive(Debug, Deserialize)]
struct RawClaims {
    #[serde(default)]
    iss: Option<String>,
    #[serde(default)]
    sub: Option<String>,
    #[serde(default)]
    aud: Option<Audience>,
    #[serde(default)]
    azp: Option<String>,
    #[serde(default)]
    exp: Option<i64>,
    #[serde(default)]
    iat: Option<i64>,
    #[serde(default)]
    nbf: Option<i64>,
    #[serde(default)]
    nonce: Option<String>,
}

/// `aud` is a string or an array of strings, and both are legal.
#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum Audience {
    One(String),
    Many(Vec<String>),
}

impl Audience {
    fn contains(&self, client_id: &str) -> bool {
        match self {
            Self::One(a) => a == client_id,
            Self::Many(v) => v.iter().any(|a| a == client_id),
        }
    }
    fn len(&self) -> usize {
        match self {
            Self::One(_) => 1,
            Self::Many(v) => v.len(),
        }
    }
}

/// Steps 6 through 11, run only after the signature has verified.
///
/// `now` is a parameter rather than a call to the clock so the expiry cases
/// are testable without sleeping.
pub(crate) fn verify_claims(
    claims: &[u8],
    issuer: &str,
    client_id: &str,
    nonce_hash: &[u8; 32],
    now: i64,
) -> Result<IdTokenClaims, JwtError> {
    let raw: RawClaims = serde_json::from_slice(claims)
        .map_err(|e| JwtError::Malformed(format!("claims json: {e}")))?;

    // 6. Exact issuer equality. Not a prefix comparison: `https://idp.example`
    //    must not accept `https://idp.example.attacker.test`.
    let iss = raw.iss.ok_or(JwtError::IssuerMismatch)?;
    if iss != issuer {
        return Err(JwtError::IssuerMismatch);
    }

    // 7. aud contains this client.
    let aud = raw.aud.ok_or(JwtError::AudienceMismatch)?;
    if !aud.contains(client_id) {
        return Err(JwtError::AudienceMismatch);
    }

    // 8a. Multiple audiences oblige the provider to name the party the
    //     token was minted for.
    if aud.len() > 1 && raw.azp.is_none() {
        return Err(JwtError::AzpMismatch);
    }
    // 8b. And whenever azp is present it must be us, however many
    //     audiences there are. OIDC Core §3.1.3.7 splits these into two
    //     obligations and the draft of this design only had the first;
    //     with 8a alone, a token carrying aud="us" and azp="some other
    //     client" verifies. That token was issued for a different client of
    //     the same IdP, and this check is the only thing standing between
    //     it and a session here.
    if let Some(azp) = &raw.azp {
        if azp != client_id {
            return Err(JwtError::AzpMismatch);
        }
    }

    // 9. Time. exp and iat are required of an ID token; a token without
    //    them is malformed rather than expired, and saying so keeps the
    //    log honest.
    let exp = raw
        .exp
        .ok_or_else(|| JwtError::Malformed("no exp".into()))?;
    let iat = raw
        .iat
        .ok_or_else(|| JwtError::Malformed("no iat".into()))?;
    if exp <= now - CLOCK_LEEWAY_SECS {
        return Err(JwtError::Expired);
    }
    if iat >= now + CLOCK_LEEWAY_SECS {
        return Err(JwtError::Expired);
    }
    if let Some(nbf) = raw.nbf {
        if nbf >= now + CLOCK_LEEWAY_SECS {
            return Err(JwtError::Expired);
        }
    }

    // 10. The nonce binds this token to the authorization request this
    //     server started. Compared as hashes in constant time: the stored
    //     value is a hash (the plaintext nonce is never written down), and
    //     a byte-at-a-time comparison of a secret is a timing side channel
    //     even when the secret is short lived.
    let nonce = raw.nonce.ok_or(JwtError::NonceMismatch)?;
    let got = Sha256::digest(nonce.as_bytes());
    if got.as_slice().ct_eq(nonce_hash.as_slice()).unwrap_u8() != 1 {
        return Err(JwtError::NonceMismatch);
    }

    // 11. sub is the identity. An empty one links to nothing.
    let sub = raw.sub.filter(|s| !s.is_empty()).ok_or(JwtError::MissingSub)?;

    Ok(IdTokenClaims { iss, sub })
}
