//! The JWKS cache, and the two rate rules that keep an attacker from
//! turning ID token verification into a request amplifier against the IdP.
//!
//! Proposal §4.3.4 sets the policy:
//!
//!  * the key set is cached for an hour,
//!  * an unknown `kid` triggers an immediate refetch, subject to a
//!    **per-kid** cooldown plus a global ceiling, and
//!  * a `kid` that is known but whose signature does not verify gets one
//!    refetch and one retry, because providers do rotate key material while
//!    keeping the `kid`, and without this rule every login fails for the
//!    rest of the hour when they do.
//!
//! The cooldown is per-kid because a global one is exploitable: one forged
//! `kid` burns the window, and the genuine rotation that arrives a second
//! later cannot be picked up until it reopens. The global ceiling still
//! exists on top, because "per-kid" bounds nothing on its own when the
//! attacker picks the kid.

use crate::fetch::{FetchError, HttpFetch};
use lru::LruCache;
use parking_lot::Mutex;
use serde::Deserialize;
use std::num::NonZeroUsize;
use std::sync::Arc;
use std::time::{Duration, Instant};

/// How long a fetched key set is served without asking again.
pub const JWKS_TTL: Duration = Duration::from_secs(3600);
/// Minimum spacing between refetches provoked by the *same* `kid`.
pub const KID_COOLDOWN: Duration = Duration::from_secs(300);
/// Minimum spacing between refetches provoked by *any* `kid`. This is the
/// ceiling §4.3.4 asks for on top of the per-kid cooldown: without it, an
/// attacker cycling through fresh `kid` values would face no limit at all,
/// since every one of them starts with an unused cooldown.
pub const GLOBAL_REFETCH_INTERVAL: Duration = Duration::from_secs(60);
/// How many recently-missed `kid`s carry a cooldown. Small on purpose: this
/// is a defence against repetition, not a memory of everything ever seen,
/// and an attacker who overflows it still meets the global ceiling.
pub const KID_COOLDOWN_ENTRIES: usize = 64;

/// One JWK, with only the members this crate reads. Everything is
/// `Option` because a key set is attacker-adjacent input: a missing `n` on
/// an RSA key has to be a verification failure, not a panic.
#[derive(Debug, Clone, Deserialize)]
pub struct Jwk {
    pub kty: String,
    #[serde(default)]
    pub kid: Option<String>,
    #[serde(default)]
    pub alg: Option<String>,
    #[serde(default, rename = "use")]
    pub use_: Option<String>,
    #[serde(default)]
    pub key_ops: Option<Vec<String>>,
    #[serde(default)]
    pub n: Option<String>,
    #[serde(default)]
    pub e: Option<String>,
    #[serde(default)]
    pub crv: Option<String>,
    #[serde(default)]
    pub x: Option<String>,
    #[serde(default)]
    pub y: Option<String>,
}

#[derive(Debug, Deserialize)]
struct JwkSet {
    keys: Vec<Jwk>,
}

#[derive(Debug, thiserror::Error)]
pub enum JwksError {
    #[error(transparent)]
    Fetch(#[from] FetchError),
    #[error("jwks document is not usable: {0}")]
    Malformed(String),
    /// The `kid` is not in the set, and the rate rules would not allow (or
    /// a refetch did not produce) a set that has it.
    #[error("no key in the provider's jwks matches the token's kid")]
    UnknownKid,
}

pub(crate) struct JwksCache {
    inner: Mutex<Inner>,
    limits: Limits,
}

struct Inner {
    set: Option<Entry>,
    /// Last refetch *attempt* per kid, not last success. An attempt that
    /// failed still consumed a request to the IdP, which is what the
    /// cooldown is rationing.
    kid_cooldown: LruCache<String, Instant>,
    last_refetch: Option<Instant>,
}

struct Entry {
    keys: Arc<Vec<Jwk>>,
    fetched: Instant,
}

#[derive(Clone, Copy)]
pub(crate) struct Limits {
    pub ttl: Duration,
    pub kid_cooldown: Duration,
    pub global_interval: Duration,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            ttl: JWKS_TTL,
            kid_cooldown: KID_COOLDOWN,
            global_interval: GLOBAL_REFETCH_INTERVAL,
        }
    }
}

impl JwksCache {
    pub(crate) fn new() -> Self {
        Self::with_limits(Limits::default())
    }

    pub(crate) fn with_limits(limits: Limits) -> Self {
        Self {
            inner: Mutex::new(Inner {
                set: None,
                kid_cooldown: LruCache::new(
                    NonZeroUsize::new(KID_COOLDOWN_ENTRIES).expect("nonzero constant"),
                ),
                last_refetch: None,
            }),
            limits,
        }
    }

    /// The lookup ID token verification step 3 performs.
    ///
    /// A cold or stale cache is loaded first, and that load is not a
    /// "refetch": it is not charged against the global ceiling, because it
    /// is driven by the clock rather than by whatever `kid` an unauthenticated
    /// caller put in a token.
    pub(crate) async fn key_for(
        &self,
        http: &dyn HttpFetch,
        jwks_uri: &str,
        kid: &str,
    ) -> Result<Jwk, JwksError> {
        if let Some(keys) = self.fresh() {
            if let Some(jwk) = find(&keys, kid) {
                return Ok(jwk);
            }
        } else {
            // Cold or expired: load, then answer from what arrived. A kid
            // that is missing from a set fetched a moment ago is not going
            // to be found by fetching the same URL again, so this path does
            // not refetch. It does record the attempt, so a caller replaying
            // that kid cannot use it to force a refetch later either.
            let keys = self.load(http, jwks_uri).await?;
            return match find(&keys, kid) {
                Some(jwk) => Ok(jwk),
                None => {
                    self.inner
                        .lock()
                        .kid_cooldown
                        .put(kid.to_string(), Instant::now());
                    Err(JwksError::UnknownKid)
                }
            };
        }

        // Cached set that does not have this kid: the rotation case.
        match self.refetch_for_kid(http, jwks_uri, kid).await? {
            Some(jwk) => Ok(jwk),
            None => Err(JwksError::UnknownKid),
        }
    }

    /// The refetch behind both the unknown-kid path and the
    /// known-kid-bad-signature path. `Ok(None)` means "the rate rules said
    /// no, or the provider still does not publish that kid"; both are
    /// failures to the caller, and the distinction is not one the caller
    /// can act on differently.
    pub(crate) async fn refetch_for_kid(
        &self,
        http: &dyn HttpFetch,
        jwks_uri: &str,
        kid: &str,
    ) -> Result<Option<Jwk>, JwksError> {
        {
            let mut inner = self.inner.lock();
            let now = Instant::now();
            if let Some(last) = inner.kid_cooldown.get(kid) {
                if now.duration_since(*last) < self.limits.kid_cooldown {
                    return Ok(None);
                }
            }
            if let Some(last) = inner.last_refetch {
                if now.duration_since(last) < self.limits.global_interval {
                    return Ok(None);
                }
            }
            inner.kid_cooldown.put(kid.to_string(), now);
            inner.last_refetch = Some(now);
        }
        let keys = self.load(http, jwks_uri).await?;
        Ok(find(&keys, kid))
    }

    fn fresh(&self) -> Option<Arc<Vec<Jwk>>> {
        let inner = self.inner.lock();
        let entry = inner.set.as_ref()?;
        (entry.fetched.elapsed() < self.limits.ttl).then(|| Arc::clone(&entry.keys))
    }

    async fn load(&self, http: &dyn HttpFetch, jwks_uri: &str) -> Result<Arc<Vec<Jwk>>, JwksError> {
        let body = http.get(jwks_uri).await?;
        let set: JwkSet = serde_json::from_slice(&body)
            .map_err(|e| JwksError::Malformed(format!("json: {e}")))?;
        let keys = Arc::new(set.keys);
        self.inner.lock().set = Some(Entry {
            keys: Arc::clone(&keys),
            fetched: Instant::now(),
        });
        Ok(keys)
    }
}

/// A JWK with no `kid` can never be selected. Proposal §4.3.3 step 2 refuses
/// to fall back to "there is only one key, use it", so a set that omits
/// `kid` is a set this server cannot use, and saying so here keeps the
/// selection rule in one place.
fn find(keys: &[Jwk], kid: &str) -> Option<Jwk> {
    keys.iter()
        .find(|k| k.kid.as_deref() == Some(kid))
        .cloned()
}
