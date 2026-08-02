//! Flow secrets, the authorize URL, and the two callback rules that depend
//! on them: a `state` is good for exactly one callback, and the binding
//! cookie has to match.
//!
//! The one-shot `state` record and the binding hash live in `oidc_flow`,
//! which is `sc-auth`'s table and Phase 3's work. What is testable here is
//! the contract that store depends on, so these two cases use a local
//! stand-in for the table: the values, their hashes, and the constant-time
//! comparison are this crate's, and they are what the callback decides on.

use super::*;
use crate::flow::{hash_eq, sha256, FlowSecrets};
use std::collections::HashMap;
use std::sync::Arc;
use url::Url;

#[test]
fn secrets_are_distinct_43_character_base64url_values() {
    let a = FlowSecrets::generate().expect("csprng");
    let b = FlowSecrets::generate().expect("csprng");

    for value in [&a.state, &a.nonce, &a.binding, &a.code_verifier] {
        // 32 bytes, base64url, no padding. Also exactly the shape RFC 7636
        // asks of a code verifier.
        assert_eq!(value.len(), 43, "{value}");
        assert!(
            value
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || c == b'-' || c == b'_'),
            "{value} is not base64url"
        );
    }

    // Four independent draws, not one value reused four times: reusing the
    // state as the nonce would mean the ID token carried the value the
    // callback URL already exposed.
    let all = [&a.state, &a.nonce, &a.binding, &a.code_verifier];
    for (i, x) in all.iter().enumerate() {
        for y in all.iter().skip(i + 1) {
            assert_ne!(x, y);
        }
    }
    assert_ne!(a.state, b.state, "two flows must not collide");
}

#[test]
fn the_pkce_challenge_is_s256_of_the_verifier() {
    let f = FlowSecrets::generate().expect("csprng");
    let expected = base64::Engine::encode(
        &base64::engine::general_purpose::URL_SAFE_NO_PAD,
        sha256(&f.code_verifier),
    );
    assert_eq!(f.code_challenge(), expected);
    // The `plain` method is not on offer, and this is what that means: what
    // travels through the browser is not what redeems the code.
    assert_ne!(f.code_challenge(), f.code_verifier);
}

#[test]
fn the_debug_impl_does_not_print_secrets() {
    let f = FlowSecrets::generate().expect("csprng");
    let printed = format!("{f:?}");
    for value in [&f.state, &f.nonce, &f.binding, &f.code_verifier] {
        assert!(!printed.contains(value.as_str()));
    }
}

/// Stand-in for `oidc_flow`, which Phase 3 puts in `sc-auth`. The only
/// behaviour modelled is the one the callback depends on: a lookup by
/// `sha256(state)` that deletes the row it found.
#[derive(Default)]
struct FlowStore(HashMap<[u8; 32], [u8; 32]>);

impl FlowStore {
    fn insert(&mut self, f: &FlowSecrets) {
        self.0.insert(f.state_hash(), f.binding_hash());
    }
    /// Returns the stored binding hash, once.
    fn take(&mut self, state: &str) -> Option<[u8; 32]> {
        self.0.remove(&sha256(state))
    }
}

#[test]
fn a_state_is_good_for_exactly_one_callback() {
    let mut store = FlowStore::default();
    let f = FlowSecrets::generate().expect("csprng");
    store.insert(&f);

    assert!(store.take(&f.state).is_some(), "the real callback");
    assert!(
        store.take(&f.state).is_none(),
        "a replayed state must find nothing, which is `oidc.bad_state`"
    );
}

#[test]
fn a_callback_from_another_browser_fails_the_binding_check() {
    let mut store = FlowStore::default();
    let f = FlowSecrets::generate().expect("csprng");
    store.insert(&f);

    // The attacker has a genuine, unconsumed `state`: they started the flow
    // themselves. What they cannot do is put their own `__Host-sc_oidc`
    // cookie in the victim's browser, and that is the entire defence
    // (RFC 9700; proposal correction 2).
    let stored = store.take(&f.state).expect("state is genuine");
    let victims_cookie = FlowSecrets::generate().expect("csprng").binding;
    assert!(!hash_eq(&sha256(&victims_cookie), &stored));
    assert!(hash_eq(&f.binding_hash(), &stored));
}

#[tokio::test]
async fn the_authorize_url_carries_everything_the_flow_needs() {
    let idp = idp_with_keys(&[]);
    let p = provider(Arc::clone(&idp));
    let disco = discovered(&p).await;
    let f = FlowSecrets::generate().expect("csprng");

    let raw = p.authorize_url(&disco, &f).expect("authorize url builds");
    let url = Url::parse(&raw).expect("a valid url");
    let q: HashMap<String, String> = url.query_pairs().into_owned().collect();

    assert_eq!(url.origin(), Url::parse(AUTHZ_URI).unwrap().origin());
    assert_eq!(q["response_type"], "code");
    assert_eq!(q["client_id"], CLIENT_ID);
    // Byte for byte the configured value, because the token request has to
    // send the same one back.
    assert_eq!(q["redirect_uri"], REDIRECT_URI);
    assert_eq!(q["state"], f.state);
    assert_eq!(q["nonce"], f.nonce);
    assert_eq!(q["code_challenge"], f.code_challenge());
    assert_eq!(q["code_challenge_method"], "S256");
    assert!(q["scope"].split(' ').any(|s| s == "openid"));
    // The verifier is the one value that must never leave this server
    // through the browser.
    assert!(!raw.contains(&f.code_verifier));
}

#[tokio::test]
async fn openid_is_added_when_the_operator_leaves_it_out() {
    let idp = idp_with_keys(&[]);
    // Configured with scopes that forgot the one scope that makes an ID
    // token happen at all.
    let cfg_provider = crate::provider::OidcProvider::new(
        crate::provider::ProviderConfig {
            issuer: ISSUER.to_string(),
            client_id: CLIENT_ID.to_string(),
            client_secret: secrecy::SecretString::from(CLIENT_SECRET.to_string()),
            redirect_uri: REDIRECT_URI.to_string(),
            scopes: vec!["email".to_string()],
            allow_private_endpoints: false,
        },
        idp,
    );
    let disco = discovered(&cfg_provider).await;
    let f = FlowSecrets::generate().expect("csprng");

    let raw = cfg_provider
        .authorize_url(&disco, &f)
        .expect("authorize url builds");
    let url = Url::parse(&raw).expect("a valid url");
    let scope = url
        .query_pairs()
        .find(|(k, _)| k == "scope")
        .map(|(_, v)| v.to_string())
        .expect("scope is present");
    assert!(scope.split(' ').any(|s| s == "openid"), "{scope}");
    assert!(scope.split(' ').any(|s| s == "email"), "{scope}");
}
