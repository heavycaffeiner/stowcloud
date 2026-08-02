//! Discovery: the issuer equality check, the endpoint rules applied to what
//! the document names, and the cache.

use super::fake::{FakeIdp, Reply};
use super::*;
use crate::discovery::DiscoveryError;
use serde_json::json;
use std::sync::Arc;

#[tokio::test]
async fn discovery_is_fetched_once_and_then_cached() {
    let idp = idp_with_keys(&[]);
    let p = provider(Arc::clone(&idp));

    let first = discovered(&p).await;
    let second = discovered(&p).await;

    assert_eq!(first.token_endpoint, TOKEN_URI);
    assert_eq!(second.jwks_uri, JWKS_URI);
    assert_eq!(
        idp.get_count(DISCOVERY_URL),
        1,
        "the second call must come from the one hour cache"
    );

    // And when the entry ages out, the next call goes back to the provider.
    p.expire_discovery();
    let _ = discovered(&p).await;
    assert_eq!(idp.get_count(DISCOVERY_URL), 2);
}

#[tokio::test]
async fn discovery_rejects_a_document_claiming_another_issuer() {
    let idp = Arc::new(FakeIdp::new());
    let mut doc = discovery_doc();
    doc["issuer"] = json!("https://someone-else.example");
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));

    let p = provider(idp);
    assert!(matches!(
        p.discovery().await.unwrap_err(),
        DiscoveryError::IssuerMismatch
    ));
}

#[tokio::test]
async fn discovery_rejects_a_plaintext_endpoint() {
    let idp = Arc::new(FakeIdp::new());
    let mut doc = discovery_doc();
    doc["token_endpoint"] = json!("http://idp.example/token");
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));

    let p = provider(idp);
    assert!(matches!(
        p.discovery().await.unwrap_err(),
        DiscoveryError::InsecureScheme
    ));
}

#[tokio::test]
async fn discovery_rejects_an_endpoint_in_private_space() {
    let idp = Arc::new(FakeIdp::new());
    let mut doc = discovery_doc();
    // The SSRF this rule exists for: a provider that answers discovery
    // honestly and then points the back-channel POST at link-local space.
    doc["token_endpoint"] = json!("https://169.254.169.254/token");
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));

    let p = provider(idp);
    assert!(matches!(
        p.discovery().await.unwrap_err(),
        DiscoveryError::PrivateAddress
    ));
}

#[tokio::test]
async fn discovery_accepts_endpoints_on_other_hosts() {
    // Google's real layout, which the draft's same-host rule would have
    // made permanently unusable.
    let idp = Arc::new(FakeIdp::new());
    let doc = json!({
        "issuer": ISSUER,
        "authorization_endpoint": "https://accounts.idp.example/o/oauth2/v2/auth",
        "token_endpoint": "https://oauth2.idpapis.example/token",
        "jwks_uri": "https://www.idpapis.example/oauth2/v3/certs",
    });
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));

    let p = provider(idp);
    let disco = p.discovery().await.expect("other hosts are legitimate");
    assert_eq!(disco.token_endpoint, "https://oauth2.idpapis.example/token");
}

#[tokio::test]
async fn discovery_failure_is_not_cached() {
    let idp = Arc::new(FakeIdp::new());
    idp.set_get(DISCOVERY_URL, Reply::Status(503));
    let p = provider(Arc::clone(&idp));

    assert!(p.discovery().await.is_err());

    // The provider came back up. Nothing should have to wait an hour for
    // that to be noticed.
    idp.set_get(DISCOVERY_URL, Reply::json(&discovery_doc()));
    assert!(p.discovery().await.is_ok());
    assert_eq!(idp.get_count(DISCOVERY_URL), 2);
}

#[tokio::test]
async fn discovery_rejects_an_oversized_body_as_a_fetch_error() {
    let idp = Arc::new(FakeIdp::new());
    idp.set_get(DISCOVERY_URL, Reply::TooLarge);
    let p = provider(idp);
    assert!(matches!(
        p.discovery().await.unwrap_err(),
        DiscoveryError::Fetch(crate::fetch::FetchError::TooLarge)
    ));
}
