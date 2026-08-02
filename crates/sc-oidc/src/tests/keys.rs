//! Test-only signing keys, and the JWK and JWS construction around them.
//!
//! **These three PKCS#8 blobs are test fixtures and nothing else. No
//! deployment reads them, no code outside `#[cfg(test)]` links them, and
//! they are committed in the clear on purpose.** They exist because `ring`
//! verifies RSA and ECDSA signatures but cannot *generate* an RSA key pair:
//! it parses PKCS#8 and no more. So the alternative to committing key
//! material is adding a key generation dependency to a crate whose entire
//! point is to add as few dependencies as possible. They were produced with
//! `openssl genpkey` (RSA 2048, and P-256) and are used by the fake IdP to
//! sign the ID tokens the tests then verify.
//!
//! `rs256-rotated-pkcs8.der` is a second RSA key. It is what makes the
//! same-kid rotation case (§4.3.4) a real test rather than an approximation:
//! the provider publishes new key material under an unchanged `kid`, and
//! the token signed with it has to fail against the cached key and verify
//! after the refetch.

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ring::rand::SystemRandom;
use ring::signature::{
    EcdsaKeyPair, KeyPair, RsaKeyPair, RsaPublicKeyComponents, ECDSA_P256_SHA256_FIXED_SIGNING,
};
use serde_json::json;

const RS256_PKCS8: &[u8] = include_bytes!("../testdata/rs256-pkcs8.der");
const RS256_ROTATED_PKCS8: &[u8] = include_bytes!("../testdata/rs256-rotated-pkcs8.der");
const ES256_PKCS8: &[u8] = include_bytes!("../testdata/es256-pkcs8.der");

pub(crate) enum TestKey {
    Rsa(RsaKeyPair),
    Ec(EcdsaKeyPair),
}

pub(crate) fn rsa() -> TestKey {
    TestKey::Rsa(RsaKeyPair::from_pkcs8(RS256_PKCS8).expect("rsa fixture parses"))
}

pub(crate) fn rsa_rotated() -> TestKey {
    TestKey::Rsa(RsaKeyPair::from_pkcs8(RS256_ROTATED_PKCS8).expect("rotated rsa fixture parses"))
}

pub(crate) fn ec() -> TestKey {
    let rng = SystemRandom::new();
    TestKey::Ec(
        EcdsaKeyPair::from_pkcs8(&ECDSA_P256_SHA256_FIXED_SIGNING, ES256_PKCS8, &rng)
            .expect("ec fixture parses"),
    )
}

impl TestKey {
    pub(crate) fn alg(&self) -> &'static str {
        match self {
            Self::Rsa(_) => "RS256",
            Self::Ec(_) => "ES256",
        }
    }

    /// The public half, as the provider would publish it.
    pub(crate) fn jwk(&self, kid: &str) -> serde_json::Value {
        match self {
            Self::Rsa(key) => {
                let parts = RsaPublicKeyComponents::<Vec<u8>>::from(key.public());
                json!({
                    "kty": "RSA",
                    "kid": kid,
                    "alg": "RS256",
                    "use": "sig",
                    "n": URL_SAFE_NO_PAD.encode(&parts.n),
                    "e": URL_SAFE_NO_PAD.encode(&parts.e),
                })
            }
            Self::Ec(key) => {
                // SEC 1 uncompressed point: 0x04 || x || y.
                let point = key.public_key().as_ref();
                json!({
                    "kty": "EC",
                    "kid": kid,
                    "alg": "ES256",
                    "crv": "P-256",
                    "use": "sig",
                    "x": URL_SAFE_NO_PAD.encode(&point[1..33]),
                    "y": URL_SAFE_NO_PAD.encode(&point[33..65]),
                })
            }
        }
    }

    fn sign(&self, message: &[u8]) -> Vec<u8> {
        let rng = SystemRandom::new();
        match self {
            Self::Rsa(key) => {
                let mut sig = vec![0u8; key.public().modulus_len()];
                key.sign(&ring::signature::RSA_PKCS1_SHA256, &rng, message, &mut sig)
                    .expect("rsa signing");
                sig
            }
            Self::Ec(key) => key.sign(&rng, message).expect("ecdsa signing").as_ref().to_vec(),
        }
    }
}

/// Serialises a JWS the way a provider would, with the signature computed
/// over the exact bytes that go on the wire.
pub(crate) fn sign_jws(key: &TestKey, header: serde_json::Value, claims: serde_json::Value) -> String {
    let header = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&header).expect("header json"));
    let claims = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&claims).expect("claims json"));
    let signing_input = format!("{header}.{claims}");
    let sig = key.sign(signing_input.as_bytes());
    format!("{signing_input}.{}", URL_SAFE_NO_PAD.encode(sig))
}

/// A JWS whose signature segment is whatever the caller says, for the cases
/// where no real signature could exist: `alg=none`, and an HS256 token
/// nobody has the key for.
pub(crate) fn unsigned_jws(header: serde_json::Value, claims: serde_json::Value, sig: &str) -> String {
    let header = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&header).expect("header json"));
    let claims = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&claims).expect("claims json"));
    format!("{header}.{claims}.{sig}")
}
