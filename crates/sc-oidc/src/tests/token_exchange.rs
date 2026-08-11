//! The token request's exact shape, which the draft got wrong by omitting
//! `grant_type` and `redirect_uri`, and the client authentication method
//! selection.

use super::fake::{FakeIdp, Reply};
use super::*;
use crate::token::{client_auth_method, ClientAuth, TokenError};
use serde_json::json;
use std::sync::Arc;

fn token_response() -> serde_json::Value {
    json!({
        "access_token": "at",
        "token_type": "Bearer",
        "expires_in": 3600,
        "id_token": "header.claims.signature",
        // A refresh token this crate must not so much as read: §3.2 makes
        // storing one a non-goal, and `TokenResponse` has no field for it.
        "refresh_token": "rt",
    })
}

fn field<'a>(call: &'a super::fake::PostCall, name: &str) -> Option<&'a str> {
    call.form
        .iter()
        .find(|(k, _)| k == name)
        .map(|(_, v)| v.as_str())
}

#[tokio::test]
async fn the_token_request_carries_grant_type_redirect_uri_and_the_verifier() {
    let idp = idp_with_keys(&[]);
    idp.set_post(Reply::json(&token_response()));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    let tokens = p
        .exchange_code(&disco, "the-code", "the-verifier", REDIRECT_URI)
        .await
        .expect("exchange succeeds");
    assert_eq!(tokens.id_token, "header.claims.signature");

    let calls = idp.post_calls();
    assert_eq!(calls.len(), 1);
    let call = &calls[0];
    assert_eq!(call.url, TOKEN_URI);
    assert_eq!(field(call, "grant_type"), Some("authorization_code"));
    assert_eq!(field(call, "code"), Some("the-code"));
    // Omitting this is what every conforming provider rejects, and what the
    // draft's pseudocode did.
    assert_eq!(field(call, "redirect_uri"), Some(REDIRECT_URI));
    assert_eq!(field(call, "code_verifier"), Some("the-verifier"));
}

#[tokio::test]
async fn client_secret_basic_is_the_default() {
    let idp = idp_with_keys(&[]);
    idp.set_post(Reply::json(&token_response()));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    p.exchange_code(&disco, "c", "v", REDIRECT_URI).await.expect("exchange");
    let calls = idp.post_calls();
    assert_eq!(
        calls[0].basic,
        Some((CLIENT_ID.to_string(), CLIENT_SECRET.to_string())),
        "a document that says nothing about auth methods means Basic"
    );
    // And the secret is not also in the body, where it would end up in the
    // provider's request logs twice over.
    assert_eq!(field(&calls[0], "client_secret"), None);
}

#[tokio::test]
async fn client_secret_post_is_used_when_that_is_all_the_provider_offers() {
    let idp = Arc::new(FakeIdp::new());
    let mut doc = discovery_doc();
    doc["token_endpoint_auth_methods_supported"] = json!(["client_secret_post"]);
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));
    idp.set_post(Reply::json(&token_response()));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    p.exchange_code(&disco, "c", "v", REDIRECT_URI).await.expect("exchange");
    let calls = idp.post_calls();
    assert_eq!(calls[0].basic, None);
    assert_eq!(field(&calls[0], "client_id"), Some(CLIENT_ID));
    assert_eq!(field(&calls[0], "client_secret"), Some(CLIENT_SECRET));
}

#[tokio::test]
async fn basic_wins_when_the_provider_offers_both() {
    let idp = Arc::new(FakeIdp::new());
    let mut doc = discovery_doc();
    doc["token_endpoint_auth_methods_supported"] =
        json!(["client_secret_post", "client_secret_basic"]);
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));
    idp.set_post(Reply::json(&token_response()));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    p.exchange_code(&disco, "c", "v", REDIRECT_URI).await.expect("exchange");
    assert!(idp.post_calls()[0].basic.is_some());
}

#[tokio::test]
async fn a_provider_offering_neither_method_is_a_configuration_error() {
    let idp = Arc::new(FakeIdp::new());
    let mut doc = discovery_doc();
    doc["token_endpoint_auth_methods_supported"] = json!(["private_key_jwt", "tls_client_auth"]);
    idp.set_get(DISCOVERY_URL, Reply::json(&doc));
    idp.set_post(Reply::json(&token_response()));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    assert!(matches!(
        p.exchange_code(&disco, "c", "v", REDIRECT_URI).await.unwrap_err(),
        TokenError::UnsupportedAuthMethod(_)
    ));
    assert!(
        idp.post_calls().is_empty(),
        "the secret must not be sent to an endpoint we cannot authenticate to"
    );
}

#[tokio::test]
async fn a_rejected_exchange_surfaces_the_status() {
    let idp = idp_with_keys(&[]);
    idp.set_post(Reply::Status(400));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    assert!(matches!(
        p.exchange_code(&disco, "c", "v", REDIRECT_URI).await.unwrap_err(),
        TokenError::Fetch(crate::fetch::FetchError::Status(400))
    ));
}

#[tokio::test]
async fn a_response_without_an_id_token_is_malformed() {
    let idp = idp_with_keys(&[]);
    // A plain OAuth2 provider, or an authorization request that lost its
    // `openid` scope on the way. Either way there is nothing to verify.
    idp.set_post(Reply::json(&json!({ "access_token": "at" })));
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;

    assert!(matches!(
        p.exchange_code(&disco, "c", "v", REDIRECT_URI).await.unwrap_err(),
        TokenError::Malformed(_)
    ));
}

#[test]
fn auth_method_selection_reads_the_discovery_document() {
    let parse = |v: serde_json::Value| -> crate::discovery::Discovery {
        serde_json::from_value(v).expect("discovery fixture")
    };
    let base = discovery_doc();

    assert_eq!(
        client_auth_method(&parse(base.clone())).unwrap(),
        ClientAuth::SecretBasic
    );

    let mut empty = base.clone();
    empty["token_endpoint_auth_methods_supported"] = json!([]);
    assert_eq!(
        client_auth_method(&parse(empty)).unwrap(),
        ClientAuth::SecretBasic,
        "an empty list is not a legal value; treat it as absent"
    );

    let mut post_only = base;
    post_only["token_endpoint_auth_methods_supported"] = json!(["client_secret_post"]);
    assert_eq!(
        client_auth_method(&parse(post_only)).unwrap(),
        ClientAuth::SecretPost
    );
}
