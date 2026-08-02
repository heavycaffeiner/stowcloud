//! Binds `sc_http::oidc_api::OidcApi` to a real `sc_oidc::OidcProvider`.
//!
//! This is where the two halves of the OIDC feature meet, and it is the only
//! file in the workspace that knows both exist:
//! `docs/proposals/stowcloud-0-oidc-login.md` §4.1.1's crate split says
//! `sc-oidc` never learns about routes or accounts and `sc-http` never learns
//! about TLS, so somebody has to be the assembler, and that is this crate's
//! whole job (`app.rs`'s module doc).
//!
//! It is also the entire footprint of the `oidc` cargo feature. With the
//! feature off, `build_oidc` returns
//! [`sc_http::oidc_api::OidcDisabled`](sc_http::oidc_api::OidcDisabled) and
//! nothing anywhere pulls in `sc-oidc`, so `--no-default-features` drops
//! rustls, hyper's client, `ring` and the compiled-in root certificates from
//! the binary rather than merely leaving them unused. Every OIDC route still
//! exists and still answers `oidc.disabled`, which is what a deployment
//! without an IdP sees anyway.

use std::sync::Arc;

use crate::config::Config;

/// The relying party for this deployment, or the disabled stand-in.
///
/// **A misconfigured `[oidc]` never stops the server starting.** §4.3.1 is
/// explicit: an empty or non-https `oidc.redirect_uri` means OIDC does not
/// activate and everything else keeps working, with the reason in the startup
/// log. A redirect URI is registered at the IdP by hand and has to match byte
/// for byte, so getting it wrong is an ordinary operator mistake — one that
/// should cost single sign-on and nothing else.
pub fn build_oidc(cfg: &Config) -> Arc<dyn sc_http::oidc_api::OidcApi> {
    if let Some(reason) = cfg.oidc.inactive_reason() {
        // `info` when the operator never asked for OIDC at all, `warn` when
        // they did and it did not come up. The difference matters: the second
        // is a broken deployment and the first is every deployment that has
        // no IdP.
        if cfg.oidc.enabled {
            tracing::warn!("single sign-on is configured but inactive: {reason}");
        } else {
            tracing::debug!("single sign-on is off: {reason}");
        }
        return Arc::new(sc_http::oidc_api::OidcDisabled);
    }
    build_active(cfg)
}

#[cfg(not(feature = "oidc"))]
fn build_active(_cfg: &Config) -> Arc<dyn sc_http::oidc_api::OidcApi> {
    tracing::warn!(
        "[oidc] is configured but this binary was built with --no-default-features, \
         so the OpenID Connect client is not compiled in; single sign-on stays off"
    );
    Arc::new(sc_http::oidc_api::OidcDisabled)
}

#[cfg(feature = "oidc")]
fn build_active(cfg: &Config) -> Arc<dyn sc_http::oidc_api::OidcApi> {
    // `inactive_reason` already established that the file is there; a read
    // error here is a permissions problem or a race, and it costs SSO rather
    // than the server.
    let secret = match std::fs::read_to_string(
        cfg.oidc
            .client_secret_file
            .as_ref()
            .expect("inactive_reason accepted a config with no client_secret_file"),
    ) {
        Ok(s) => s.trim().to_string(),
        Err(e) => {
            tracing::warn!(error = %e, "oidc.client_secret_file could not be read; single sign-on stays off");
            return Arc::new(sc_http::oidc_api::OidcDisabled);
        }
    };
    if secret.is_empty() {
        tracing::warn!("oidc.client_secret_file is empty; single sign-on stays off");
        return Arc::new(sc_http::oidc_api::OidcDisabled);
    }

    // Built once. A TLS configuration this process cannot assemble is a fact
    // about the build, not about any particular request, so it is decided
    // here rather than at the first login attempt.
    let http = match sc_oidc::RustlsFetch::new(cfg.oidc.allow_private_endpoints) {
        Ok(f) => Arc::new(f),
        Err(e) => {
            tracing::error!(error = %e, "the outbound TLS client could not be built; single sign-on stays off");
            return Arc::new(sc_http::oidc_api::OidcDisabled);
        }
    };

    let provider = sc_oidc::OidcProvider::new(
        sc_oidc::ProviderConfig {
            issuer: cfg.oidc.issuer.trim().to_string(),
            client_id: cfg.oidc.client_id.trim().to_string(),
            client_secret: secrecy::SecretString::from(secret),
            redirect_uri: cfg.oidc.redirect_uri.trim().to_string(),
            scopes: cfg.oidc.scopes.clone(),
            allow_private_endpoints: cfg.oidc.allow_private_endpoints,
        },
        http,
    );
    tracing::info!(
        issuer = %cfg.oidc.issuer,
        "single sign-on is active; the provider is contacted lazily, not at startup"
    );
    Arc::new(active::OidcBridge {
        provider,
        issuer: cfg.oidc.issuer.trim().to_string(),
        display_name: cfg.oidc.display_name.clone(),
    })
}

#[cfg(feature = "oidc")]
mod active {
    use async_trait::async_trait;
    use sc_http::oidc_api::{OidcApi, OidcDisplay, OidcError, StartedFlow, VerifiedIdentity};
    use secrecy::SecretString;

    pub struct OidcBridge {
        pub provider: sc_oidc::OidcProvider,
        pub issuer: String,
        pub display_name: String,
    }

    /// Everything `sc-oidc` can fail with, collapsed onto the two answers the
    /// HTTP layer has (§5-2 table B folds discovery, JWKS, the token endpoint
    /// and every ID-token check into one wire code). The specific cause is
    /// carried as a string so it reaches the server log, where it is useful,
    /// and not the response, where it would tell an anonymous caller what
    /// this deployment's IdP is doing.
    fn unavailable(e: impl std::fmt::Display) -> OidcError {
        OidcError::ProviderUnavailable(e.to_string())
    }

    #[async_trait]
    impl OidcApi for OidcBridge {
        fn display(&self) -> OidcDisplay {
            OidcDisplay {
                enabled: true,
                display_name: self.display_name.clone(),
            }
        }

        fn issuer(&self) -> Option<String> {
            Some(self.issuer.clone())
        }

        async fn begin(&self) -> Result<StartedFlow, OidcError> {
            let disco = self.provider.discovery().await.map_err(unavailable)?;
            let flow = sc_oidc::FlowSecrets::generate().map_err(|e| OidcError::Internal(e.to_string()))?;
            let authorize_url = self
                .provider
                .authorize_url(&disco, &flow)
                .map_err(|e| OidcError::Internal(e.to_string()))?;
            Ok(StartedFlow {
                authorize_url,
                state_hash: flow.state_hash(),
                binding: SecretString::from(flow.binding.clone()),
                binding_hash: flow.binding_hash(),
                nonce_hash: flow.nonce_hash(),
                code_verifier: SecretString::from(flow.code_verifier.clone()),
            })
        }

        async fn redeem(
            &self,
            code: &str,
            code_verifier: &SecretString,
            nonce_hash: &[u8; 32],
        ) -> Result<VerifiedIdentity, OidcError> {
            use secrecy::ExposeSecret;
            let disco = self.provider.discovery().await.map_err(unavailable)?;
            let tokens = self
                .provider
                .exchange_code(&disco, code, code_verifier.expose_secret())
                .await
                .map_err(unavailable)?;
            let claims = self
                .provider
                .verify_id_token(&disco, &tokens.id_token, nonce_hash)
                .await
                .map_err(unavailable)?;
            Ok(VerifiedIdentity {
                issuer: claims.iss,
                subject: claims.sub,
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{OidcConfig, OidcLocalPasswordLoginCfg};

    fn cfg_with(oidc: OidcConfig) -> Config {
        Config { oidc, ..Config::default() }
    }

    /// The activation rules of §4.3.1, one refusal at a time. Each of these
    /// is an ordinary operator mistake, and none of them may stop the server.
    #[test]
    fn a_half_configured_section_reports_which_half_is_missing() {
        let dir = tempfile::tempdir().unwrap();
        let secret = dir.path().join("client_secret");
        std::fs::write(&secret, "s3cr3t").unwrap();

        let complete = OidcConfig {
            enabled: true,
            issuer: "https://idp.example.com".into(),
            client_id: "sc".into(),
            client_secret_file: Some(secret.clone()),
            redirect_uri: "https://cloud.example.com/api/auth/oidc/callback".into(),
            ..OidcConfig::default()
        };
        assert_eq!(complete.inactive_reason(), None);

        let off = OidcConfig { enabled: false, ..complete.clone() };
        assert!(off.inactive_reason().unwrap().contains("enabled"));

        let no_issuer = OidcConfig { issuer: "  ".into(), ..complete.clone() };
        assert!(no_issuer.inactive_reason().unwrap().contains("issuer"));

        let no_client = OidcConfig { client_id: String::new(), ..complete.clone() };
        assert!(no_client.inactive_reason().unwrap().contains("client_id"));

        // The one §4.3.1 calls out by name: a redirect URI that is not https
        // is one the IdP will refuse anyway, and a `__Host-` cookie would not
        // survive the round trip either.
        let plaintext = OidcConfig {
            redirect_uri: "http://cloud.example.com/api/auth/oidc/callback".into(),
            ..complete.clone()
        };
        assert!(plaintext.inactive_reason().unwrap().contains("https://"));

        let no_redirect = OidcConfig { redirect_uri: String::new(), ..complete.clone() };
        assert!(no_redirect.inactive_reason().unwrap().contains("redirect_uri"));

        let no_secret = OidcConfig { client_secret_file: None, ..complete.clone() };
        assert!(no_secret.inactive_reason().unwrap().contains("client_secret_file"));

        let missing_secret = OidcConfig {
            client_secret_file: Some(dir.path().join("nope")),
            ..complete
        };
        assert!(missing_secret.inactive_reason().unwrap().contains("does not exist"));
    }

    /// Whatever is wrong with `[oidc]`, `build_oidc` answers with a relying
    /// party rather than an error, and the rest of the server never finds out.
    #[test]
    fn a_broken_section_yields_a_disabled_relying_party_not_a_failure() {
        let cfg = cfg_with(OidcConfig {
            enabled: true,
            issuer: "https://idp.example.com".into(),
            client_id: "sc".into(),
            redirect_uri: "http://not-https".into(),
            ..OidcConfig::default()
        });
        assert!(!build_oidc(&cfg).display().enabled);
        assert_eq!(build_oidc(&cfg).issuer(), None);
    }

    /// The default section is off, asks for `openid` and nothing more (§3.2
    /// reads no other claim), and leaves local password login permitted
    /// (§4.3.5's recovery argument).
    #[test]
    fn the_default_section_is_off_and_minimal() {
        let d = OidcConfig::default();
        assert!(!d.enabled);
        assert_eq!(d.scopes, vec!["openid".to_string()]);
        assert_eq!(d.local_password_login, OidcLocalPasswordLoginCfg::Allow);
        assert!(!d.allow_private_endpoints);
    }

    #[test]
    fn the_section_is_toml_reachable() {
        let cfg = Config::from_toml_str(
            r#"
            [oidc]
            enabled = true
            issuer = "https://idp.example.com"
            client_id = "sc"
            client_secret_file = "/etc/sc/oidc_client_secret"
            redirect_uri = "https://cloud.example.com/api/auth/oidc/callback"
            display_name = "회사 계정"
            local_password_login = "deny"
            "#,
        )
        .unwrap();
        assert!(cfg.oidc.enabled);
        assert_eq!(cfg.oidc.display_name, "회사 계정");
        assert_eq!(cfg.oidc.local_password_login, OidcLocalPasswordLoginCfg::Deny);
        // An untouched key in a partially specified section keeps its default.
        assert_eq!(cfg.oidc.scopes, vec!["openid".to_string()]);
        assert!(!cfg.oidc.allow_private_endpoints);
    }
}
