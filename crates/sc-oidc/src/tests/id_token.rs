//! ID token verification, one test per numbered step of §4.3.3 plus the two
//! key rotation shapes from §4.3.4.
//!
//! The cases the proposal's Phase 2 row calls mandatory are all here:
//! `alg=none`, `alg=HS256`, a missing `kid`, a `kty`/`alg` mismatch, an
//! expired token, an `aud` mismatch, a single-element `aud` with a foreign
//! `azp`, a `nonce` mismatch, `kid` rotation, and same-`kid` rotation.

use super::keys::{sign_jws, unsigned_jws, TestKey};
use super::*;
use crate::discovery::Discovery;
use crate::jwt::{IdTokenClaims, JwtError};
use crate::provider::OidcProvider;
use serde_json::json;
use std::sync::Arc;

fn token(key: &TestKey, kid: &str, claims: serde_json::Value) -> String {
    sign_jws(key, header(key.alg(), Some(kid)), claims)
}

async fn verify(
    p: &OidcProvider,
    disco: &Discovery,
    token: &str,
) -> Result<IdTokenClaims, JwtError> {
    p.verify_id_token_at(disco, token, &crate::flow::sha256(NONCE), NOW)
        .await
}

#[tokio::test]
async fn rs256_token_verifies_and_yields_only_iss_and_sub() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let claims = verify(&p, &disco, &token(&key, "k1", good_claims()))
        .await
        .expect("a well formed token from the configured issuer verifies");
    assert_eq!(
        claims,
        IdTokenClaims {
            iss: ISSUER.to_string(),
            sub: SUBJECT.to_string(),
        }
    );
}

#[tokio::test]
async fn es256_token_verifies() {
    let key = super::keys::ec();
    let idp = idp_with_keys(&[key.jwk("ec-1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let claims = verify(&p, &disco, &token(&key, "ec-1", good_claims()))
        .await
        .expect("ES256 is on the allow list too");
    assert_eq!(claims.sub, SUBJECT);
}

// --- step 1: the alg allow list ---

#[tokio::test]
async fn alg_none_is_refused() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    // The canonical unsigned forgery: right claims, right kid, empty
    // signature. It has to lose at step 1, before anything looks at a key.
    let forged = unsigned_jws(header("none", Some("k1")), good_claims(), "");
    assert!(matches!(
        verify(&p, &disco, &forged).await.unwrap_err(),
        JwtError::UnsupportedAlg(alg) if alg == "none"
    ));
}

#[tokio::test]
async fn alg_hs256_is_refused() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    // The other half of alg confusion: a symmetric alg turns the *public*
    // RSA modulus into the verification secret, which the attacker also has.
    let forged = unsigned_jws(header("HS256", Some("k1")), good_claims(), "AAAA");
    assert!(matches!(
        verify(&p, &disco, &forged).await.unwrap_err(),
        JwtError::UnsupportedAlg(alg) if alg == "HS256"
    ));
}

// --- step 2: kid is mandatory ---

#[tokio::test]
async fn missing_kid_is_refused_with_no_single_key_fallback() {
    let key = super::keys::rsa();
    // Exactly one key in the set, which is the situation where "just use
    // the only key" is most tempting and most wrong.
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let no_kid = sign_jws(&key, header("RS256", None), good_claims());
    assert!(matches!(
        verify(&p, &disco, &no_kid).await.unwrap_err(),
        JwtError::MissingKid
    ));
}

// --- step 4: the key has to match the alg ---

#[tokio::test]
async fn key_type_must_match_the_header_alg() {
    let rsa = super::keys::rsa();
    let ec = super::keys::ec();
    // The provider publishes an RSA key under `k1`; the token claims ES256
    // under the same kid. Signed with the real EC key, so only step 4 can
    // catch it.
    let idp = idp_with_keys(&[rsa.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    assert!(matches!(
        verify(&p, &disco, &token(&ec, "k1", good_claims()))
            .await
            .unwrap_err(),
        JwtError::KeyTypeMismatch(_)
    ));
}

// --- step 5: the signature ---

#[tokio::test]
async fn a_signature_from_the_wrong_key_is_refused() {
    let published = super::keys::rsa();
    let attacker = super::keys::rsa_rotated();
    let idp = idp_with_keys(&[published.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    assert!(matches!(
        verify(&p, &disco, &token(&attacker, "k1", good_claims()))
            .await
            .unwrap_err(),
        // One refetch is allowed here (the same-kid rotation rule), and it
        // returns the same key, so the retry fails the same way.
        JwtError::BadSignature
    ));
}

// --- step 6: iss ---

#[tokio::test]
async fn issuer_must_match_exactly() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    // A prefix comparison would accept this. Exact equality does not.
    claims["iss"] = json!("https://idp.example.attacker.test");
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::IssuerMismatch
    ));
}

// --- steps 7, 8a, 8b: aud and azp ---

#[tokio::test]
async fn audience_must_contain_this_client() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    claims["aud"] = json!("some-other-client");
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::AudienceMismatch
    ));
}

#[tokio::test]
async fn multiple_audiences_require_azp() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    claims["aud"] = json!([CLIENT_ID, "another-client"]);
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::AzpMismatch
    ));
}

#[tokio::test]
async fn a_single_audience_with_a_foreign_azp_is_refused() {
    // This is step 8b, and it is the case that made the cross review worth
    // running. With only 8a implemented, this token verifies: `aud` is us,
    // there is one audience, so the "azp is required" rule never fires and
    // the `azp` value is never looked at. The token was minted for a
    // different client of the same IdP, and this check is the only thing
    // between it and a session here.
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    claims["aud"] = json!(CLIENT_ID);
    claims["azp"] = json!("some-other-client");
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::AzpMismatch
    ));

    // And the same token with our own azp is fine, so the check is not just
    // rejecting every azp it sees.
    let mut ok = good_claims();
    ok["azp"] = json!(CLIENT_ID);
    assert!(verify(&p, &disco, &token(&key, "k1", ok)).await.is_ok());
}

// --- step 9: time ---

#[tokio::test]
async fn expired_token_is_refused() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    // Past the 60 second leeway, so this is expiry and not skew.
    claims["exp"] = json!(NOW - 61);
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::Expired
    ));

    // Inside the leeway it still verifies, which is what the leeway is for.
    let mut just_barely = good_claims();
    just_barely["exp"] = json!(NOW - 30);
    assert!(verify(&p, &disco, &token(&key, "k1", just_barely))
        .await
        .is_ok());
}

#[tokio::test]
async fn a_token_issued_in_the_future_is_refused() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    claims["iat"] = json!(NOW + 3600);
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::Expired
    ));
}

// --- step 10: nonce ---

#[tokio::test]
async fn nonce_must_match_the_one_this_flow_issued() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    claims["nonce"] = json!("a nonce from some other flow");
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::NonceMismatch
    ));

    // Absent is treated exactly like wrong. A provider that drops the nonce
    // must not be a provider whose tokens are replayable.
    let mut none = good_claims();
    none.as_object_mut().expect("object").remove("nonce");
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", none))
            .await
            .unwrap_err(),
        JwtError::NonceMismatch
    ));
}

// --- step 11: sub ---

#[tokio::test]
async fn an_empty_sub_is_refused() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let mut claims = good_claims();
    claims["sub"] = json!("");
    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", claims))
            .await
            .unwrap_err(),
        JwtError::MissingSub
    ));
}

// --- §4.3.4: the two rotation shapes ---

#[tokio::test]
async fn an_unknown_kid_triggers_a_refetch_and_then_verifies() {
    let old = super::keys::rsa();
    let new = super::keys::rsa_rotated();
    let idp = idp_with_keys(&[old.jwk("k1")]);
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    // Warm the cache with the old key set.
    assert!(verify(&p, &disco, &token(&old, "k1", good_claims()))
        .await
        .is_ok());
    assert_eq!(idp.get_count(JWKS_URI), 1);

    // The provider rotates to a new kid. Without the refetch, every login
    // would fail until the hour-long cache expired.
    idp.set_get(JWKS_URI, super::fake::Reply::json(&jwks(&[new.jwk("k2")])));
    assert!(verify(&p, &disco, &token(&new, "k2", good_claims()))
        .await
        .is_ok());
    assert_eq!(idp.get_count(JWKS_URI), 2);
}

#[tokio::test]
async fn same_kid_rotation_verifies_after_exactly_one_retry() {
    // Some providers replace key material and keep the kid, so a cache
    // holds the right kid with the wrong key. The
    // only signal is a signature failure, and the rule is one refetch and
    // one retry.
    let old = super::keys::rsa();
    let new = super::keys::rsa_rotated();
    let idp = idp_with_keys(&[old.jwk("k1")]);
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    assert!(verify(&p, &disco, &token(&old, "k1", good_claims()))
        .await
        .is_ok());
    assert_eq!(idp.get_count(JWKS_URI), 1);

    idp.set_get(JWKS_URI, super::fake::Reply::json(&jwks(&[new.jwk("k1")])));
    assert!(
        verify(&p, &disco, &token(&new, "k1", good_claims()))
            .await
            .is_ok(),
        "new material under an unchanged kid must verify after the retry"
    );
    assert_eq!(idp.get_count(JWKS_URI), 2);
}

#[tokio::test]
async fn unknown_kid_refetches_are_rate_limited() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    assert!(verify(&p, &disco, &token(&key, "k1", good_claims()))
        .await
        .is_ok());
    assert_eq!(idp.get_count(JWKS_URI), 1);

    // A forged kid gets one refetch, which does not produce it.
    let forged = token(&key, "forged-kid", good_claims());
    assert!(matches!(
        verify(&p, &disco, &forged).await.unwrap_err(),
        JwtError::UnknownKid
    ));
    assert_eq!(idp.get_count(JWKS_URI), 2);

    // Replaying it does not buy another request: that is the per-kid
    // cooldown.
    assert!(matches!(
        verify(&p, &disco, &forged).await.unwrap_err(),
        JwtError::UnknownKid
    ));
    assert_eq!(idp.get_count(JWKS_URI), 2);

    // Nor does a fresh kid, whose own cooldown is unused: that is the
    // global ceiling on top, which is the part a purely per-kid rule would
    // be missing.
    assert!(matches!(
        verify(&p, &disco, &token(&key, "another-forged-kid", good_claims()))
            .await
            .unwrap_err(),
        JwtError::UnknownKid
    ));
    assert_eq!(idp.get_count(JWKS_URI), 2);
}

#[tokio::test]
async fn the_cooldown_is_per_kid_and_not_global() {
    // With the global ceiling out of the way, a *different* kid still gets
    // its own refetch while the first kid stays blocked. That asymmetry is
    // the whole of cross-review m5: a purely global cooldown lets one
    // forged kid burn the window that a genuine rotation needs one second
    // later.
    let old = super::keys::rsa();
    let new = super::keys::rsa_rotated();
    let idp = idp_with_keys(&[old.jwk("k1")]);
    let p = provider(Arc::clone(&idp)).with_jwks_limits(crate::jwks::Limits {
        global_interval: std::time::Duration::ZERO,
        ..crate::jwks::Limits::default()
    });
    let disco = discovered(&p).await;

    assert!(verify(&p, &disco, &token(&old, "k1", good_claims()))
        .await
        .is_ok());
    let forged = token(&old, "forged-kid", good_claims());
    assert!(verify(&p, &disco, &forged).await.is_err());
    assert_eq!(idp.get_count(JWKS_URI), 2);

    // Same kid again: still blocked by its own five minute cooldown.
    assert!(verify(&p, &disco, &forged).await.is_err());
    assert_eq!(idp.get_count(JWKS_URI), 2);

    // A genuine rotation arriving right behind it is not blocked.
    idp.set_get(JWKS_URI, super::fake::Reply::json(&jwks(&[new.jwk("k2")])));
    assert!(verify(&p, &disco, &token(&new, "k2", good_claims()))
        .await
        .is_ok());
    assert_eq!(idp.get_count(JWKS_URI), 3);
}

#[tokio::test]
async fn an_unreachable_jwks_is_not_reported_as_an_unknown_kid() {
    // The HTTP layer answers `oidc.provider_unavailable` for one of these
    // and `oidc.not_linked` for the other, so they must not collapse.
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    idp.set_get(
        JWKS_URI,
        super::fake::Reply::Transport("connection refused"),
    );
    let p = provider(idp);
    let disco = discovered(&p).await;

    assert!(matches!(
        verify(&p, &disco, &token(&key, "k1", good_claims()))
            .await
            .unwrap_err(),
        JwtError::Jwks(_)
    ));
}

#[tokio::test]
async fn a_jwe_shaped_token_is_refused() {
    let key = super::keys::rsa();
    let idp = idp_with_keys(&[key.jwk("k1")]);
    let p = provider(idp);
    let disco = discovered(&p).await;

    let five_segments = format!("{}.x.y", token(&key, "k1", good_claims()));
    assert!(matches!(
        verify(&p, &disco, &five_segments).await.unwrap_err(),
        JwtError::Malformed(_)
    ));
}
