//! The relying party itself: configuration, the two caches, and the four
//! operations `sc-http` drives from a route.
//!
//! Nothing here touches a database, a cookie, or a session. The flow record
//! (`oidc_flow`), the identity link (`oidc_identity`), and every decision
//! about which local account a verified `sub` may become are `sc-auth`'s,
//! by the same protocol-agnostic split `DESIGN-AUTH.md` opens with.

use crate::discovery::{fetch_discovery, Discovery, DiscoveryCache, DiscoveryError};
use crate::endpoint::{check_endpoint_url, EndpointError};
use crate::fetch::HttpFetch;
use crate::flow::FlowSecrets;
use crate::jwks::JwksCache;
use crate::jwt::{
    check_key_matches_alg, split_jws, verify_claims, verify_signature, IdTokenClaims, JwtError,
    ALLOWED_ALGS,
};
use crate::token::{exchange_code, TokenError, TokenResponse};
use secrecy::SecretString;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

/// The `[oidc]` half that this crate needs. `sc-server` owns the parsing
/// and the settings-screen rules (§6-4); what arrives here is already
/// validated to the extent that an empty or non-https `redirect_uri` means
/// OIDC was never enabled in the first place.
pub struct ProviderConfig {
    pub issuer: String,
    pub client_id: String,
    pub client_secret: SecretString,
    /// One configured constant, never derived from the incoming request.
    /// It has to equal what is registered at the IdP exactly, and the
    /// authorization request and the token request have to carry the same
    /// bytes.
    pub redirect_uri: String,
    /// `openid` is added if the operator left it out; without it no ID
    /// token is issued and the whole flow is pointless.
    pub scopes: Vec<String>,
    pub allow_private_endpoints: bool,
}

pub struct OidcProvider {
    cfg: ProviderConfig,
    http: Arc<dyn HttpFetch>,
    discovery: DiscoveryCache,
    jwks: JwksCache,
}

impl OidcProvider {
    pub fn new(cfg: ProviderConfig, http: Arc<dyn HttpFetch>) -> Self {
        Self {
            cfg,
            http,
            discovery: DiscoveryCache::new(),
            jwks: JwksCache::new(),
        }
    }

    pub fn config(&self) -> &ProviderConfig {
        &self.cfg
    }

    /// Cached for an hour on success, refetched on demand otherwise.
    pub async fn discovery(&self) -> Result<Arc<Discovery>, DiscoveryError> {
        if let Some(doc) = self.discovery.fresh() {
            return Ok(doc);
        }
        let doc = Arc::new(
            fetch_discovery(
                self.http.as_ref(),
                &self.cfg.issuer,
                self.cfg.allow_private_endpoints,
            )
            .await?,
        );
        self.discovery.store(Arc::clone(&doc));
        Ok(doc)
    }

    /// Where `/api/auth/oidc/start` sends the browser.
    pub fn authorize_url(
        &self,
        disco: &Discovery,
        flow: &FlowSecrets,
    ) -> Result<String, EndpointError> {
        let mut url = check_endpoint_url(
            &disco.authorization_endpoint,
            self.cfg.allow_private_endpoints,
        )?;
        let mut scopes: Vec<&str> = self.cfg.scopes.iter().map(String::as_str).collect();
        if !scopes.contains(&"openid") {
            scopes.insert(0, "openid");
        }
        url.query_pairs_mut()
            .append_pair("response_type", "code")
            .append_pair("client_id", &self.cfg.client_id)
            .append_pair("redirect_uri", &self.cfg.redirect_uri)
            .append_pair("scope", &scopes.join(" "))
            .append_pair("state", &flow.state)
            .append_pair("nonce", &flow.nonce)
            .append_pair("code_challenge", &flow.code_challenge())
            .append_pair("code_challenge_method", "S256");
        Ok(url.to_string())
    }

    pub async fn exchange_code(
        &self,
        disco: &Discovery,
        code: &str,
        code_verifier: &str,
    ) -> Result<TokenResponse, TokenError> {
        exchange_code(
            self.http.as_ref(),
            disco,
            &self.cfg,
            code,
            code_verifier,
        )
        .await
    }

    /// The eleven steps of §4.3.3, in order.
    ///
    /// `disco` is passed in rather than fetched here so that a discovery
    /// failure stays a `DiscoveryError` at the call site instead of being
    /// flattened into a `JwtError` that would then have to be un-flattened
    /// to answer `oidc.provider_unavailable`.
    pub async fn verify_id_token(
        &self,
        disco: &Discovery,
        token: &str,
        nonce_hash: &[u8; 32],
    ) -> Result<IdTokenClaims, JwtError> {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0);
        self.verify_id_token_at(disco, token, nonce_hash, now).await
    }

    pub(crate) async fn verify_id_token_at(
        &self,
        disco: &Discovery,
        token: &str,
        nonce_hash: &[u8; 32],
        now: i64,
    ) -> Result<IdTokenClaims, JwtError> {
        // 1. Structure, then the alg allow list.
        let jws = split_jws(token)?;
        let alg = jws.header.alg.clone();
        if !ALLOWED_ALGS.contains(&alg.as_str()) {
            return Err(JwtError::UnsupportedAlg(alg));
        }

        // 2. kid, with no "there is only one key" fallback.
        let kid = jws.header.kid.clone().ok_or(JwtError::MissingKid)?;

        // 3. Key lookup, refetching under the §4.3.4 rate rules on a miss.
        let jwk = match self
            .jwks
            .key_for(self.http.as_ref(), &disco.jwks_uri, &kid)
            .await
        {
            Ok(jwk) => jwk,
            Err(crate::jwks::JwksError::UnknownKid) => return Err(JwtError::UnknownKid),
            Err(e) => return Err(JwtError::Jwks(e)),
        };

        // 4 and 5.
        check_key_matches_alg(&jwk, &alg)?;
        match verify_signature(&jwk, &alg, &jws) {
            Ok(()) => {}
            Err(JwtError::BadSignature) => {
                // A known kid whose signature fails is the shape of a
                // provider that rotated key material without rotating the
                // kid. Without this one retry, every login fails until the
                // hour-long cache expires. With it, the first failing login
                // pays for a refetch and everyone after it succeeds.
                //
                // It is one retry, rate limited exactly like an unknown
                // kid, so a forged signature buys an attacker one request
                // to the IdP per cooldown window and nothing else.
                let refreshed = self
                    .jwks
                    .refetch_for_kid(self.http.as_ref(), &disco.jwks_uri, &kid)
                    .await
                    .map_err(JwtError::Jwks)?;
                let Some(jwk) = refreshed else {
                    return Err(JwtError::BadSignature);
                };
                check_key_matches_alg(&jwk, &alg)?;
                verify_signature(&jwk, &alg, &jws)?;
            }
            Err(e) => return Err(e),
        }

        // 6 through 11.
        verify_claims(
            &jws.claims,
            &self.cfg.issuer,
            &self.cfg.client_id,
            nonce_hash,
            now,
        )
    }

    /// Test seam for the cache policies. Not `pub`: the TTL, the per-kid
    /// cooldown and the global ceiling are §4.3.4's numbers, not an
    /// operator's.
    #[cfg(test)]
    pub(crate) fn with_jwks_limits(mut self, limits: crate::jwks::Limits) -> Self {
        self.jwks = JwksCache::with_limits(limits);
        self
    }

    #[cfg(test)]
    pub(crate) fn expire_discovery(&self) {
        self.discovery.expire();
    }
}
