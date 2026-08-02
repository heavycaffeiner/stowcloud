//! OIDC Discovery, and the cache in front of it.
//!
//! Lazy, never at startup (proposal §4.3.4). Calling the IdP while the
//! server boots means a server that will not boot while the IdP is down,
//! which trades an outage of one login method for an outage of the whole
//! product.

use crate::endpoint::{check_endpoint_url, EndpointError};
use crate::fetch::{FetchError, HttpFetch};
use parking_lot::Mutex;
use serde::Deserialize;
use std::sync::Arc;
use std::time::{Duration, Instant};

/// How long a successful discovery document is trusted. Failures are not
/// cached at all: a provider that was down a second ago may be up now, and
/// caching the failure would extend a 1 second outage into an hour of them.
pub const DISCOVERY_TTL: Duration = Duration::from_secs(3600);

/// The subset of the discovery document this crate reads. Everything else
/// in it is ignored rather than rejected, since the specification lets a
/// provider add fields and most of them do.
#[derive(Debug, Clone, Deserialize)]
pub struct Discovery {
    pub issuer: String,
    pub authorization_endpoint: String,
    pub token_endpoint: String,
    pub jwks_uri: String,
    /// Absent and empty are treated identically, as "the provider did not
    /// say". OIDC Discovery makes `client_secret_basic` the default in that
    /// case, and an empty array is not a legal value, so collapsing the two
    /// costs nothing and saves an `Option<Vec<_>>`.
    #[serde(default)]
    pub token_endpoint_auth_methods_supported: Vec<String>,
}

#[derive(Debug, thiserror::Error)]
pub enum DiscoveryError {
    #[error(transparent)]
    Fetch(#[from] FetchError),
    #[error("discovery document is not usable: {0}")]
    Malformed(String),
    /// The document's own `issuer` is not the one that was configured.
    /// Required by OIDC Discovery §4.3 and the reason a discovery URL alone
    /// cannot be used to impersonate a provider.
    #[error("discovery document claims a different issuer")]
    IssuerMismatch,
    /// Replaces the draft's `HostMismatch`. See `endpoint.rs` for why the
    /// same-host rule had to go and what took its place.
    #[error("a discovery endpoint points into private, loopback, or link-local space")]
    PrivateAddress,
    #[error("a discovery endpoint is not https")]
    InsecureScheme,
}

impl From<EndpointError> for DiscoveryError {
    fn from(e: EndpointError) -> Self {
        match e {
            EndpointError::InsecureScheme => Self::InsecureScheme,
            EndpointError::PrivateAddress => Self::PrivateAddress,
            EndpointError::Malformed(m) => Self::Malformed(m),
        }
    }
}

/// One slot, guarded by a plain (not async) mutex, never held across an
/// await.
///
/// Two concurrent logins on a cold cache will both fetch, and that is
/// accepted rather than solved. Deduplicating them needs a lock that can be
/// held across an await, which means an async mutex, which means a runtime
/// dependency in a crate whose `--no-default-features` build deliberately
/// has none. The cost of the race is one extra GET.
pub(crate) struct DiscoveryCache {
    slot: Mutex<Option<Entry>>,
    ttl: Duration,
}

struct Entry {
    doc: Arc<Discovery>,
    fetched: Instant,
}

impl DiscoveryCache {
    pub(crate) fn new() -> Self {
        Self {
            slot: Mutex::new(None),
            ttl: DISCOVERY_TTL,
        }
    }

    pub(crate) fn fresh(&self) -> Option<Arc<Discovery>> {
        let slot = self.slot.lock();
        let entry = slot.as_ref()?;
        (entry.fetched.elapsed() < self.ttl).then(|| Arc::clone(&entry.doc))
    }

    pub(crate) fn store(&self, doc: Arc<Discovery>) {
        *self.slot.lock() = Some(Entry {
            doc,
            fetched: Instant::now(),
        });
    }

    /// Ages the cached document out without waiting an hour. Tests only:
    /// `Instant` cannot be moved backwards, so the alternative is a sleep.
    #[cfg(test)]
    pub(crate) fn expire(&self) {
        *self.slot.lock() = None;
    }
}

/// Fetches and validates the document. The validation is the point: what
/// comes back names the endpoints this server will later POST a client
/// secret to.
pub(crate) async fn fetch_discovery(
    http: &dyn HttpFetch,
    issuer: &str,
    allow_private: bool,
) -> Result<Discovery, DiscoveryError> {
    // The configured issuer is subject to the same scheme and address rules
    // as anything the document names. An `http://` issuer must not be a way
    // to skip them.
    check_endpoint_url(issuer, allow_private)?;
    let url = format!(
        "{}/.well-known/openid-configuration",
        issuer.trim_end_matches('/')
    );

    let body = http.get(&url).await?;
    let doc: Discovery = serde_json::from_slice(&body)
        .map_err(|e| DiscoveryError::Malformed(format!("json: {e}")))?;

    // Exact string equality, not a prefix or a host comparison. OIDC
    // Discovery §4.3 requires it, and anything looser lets a document served
    // from one issuer speak for another.
    if doc.issuer != issuer {
        return Err(DiscoveryError::IssuerMismatch);
    }
    for endpoint in [
        &doc.authorization_endpoint,
        &doc.token_endpoint,
        &doc.jwks_uri,
    ] {
        check_endpoint_url(endpoint, allow_private)?;
    }
    Ok(doc)
}
