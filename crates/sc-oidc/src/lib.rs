//! `sc-oidc`: the OpenID Connect relying-party core.
//!
//! Discovery, JWKS, ID token verification, PKCE/state/nonce generation, and
//! the authorization-code token exchange. Nothing here knows about HTTP
//! routes, cookies, sessions, or the account database; `sc-http` and
//! `sc-auth` own all of that. See
//! `docs/proposals/stowcloud-0-oidc-login.md` for the design this
//! implements, and for the account model it plugs
//! into.
//!
//! Why a separate crate at all (proposal §4.1.1): this is where the TLS
//! stack and the outbound HTTP client live, and neither existed anywhere in
//! this workspace before. Putting them in `sc-auth` would link rustls into
//! every path that touches the auth database, `sc-server admin smb-sync`
//! included.
//!
//! The rule this crate lives by: it authenticates, it does not authorize. It
//! can tell you that an IdP asserted a particular `sub` under a particular
//! `iss`, and that is all it will ever tell you. Whether that maps to an
//! account, and what that account may do, is `sc-auth`'s question, answered
//! from the local database.

pub mod discovery;
pub mod endpoint;
pub mod fetch;
pub mod flow;
pub mod jwks;
pub mod jwt;
pub mod provider;
pub mod token;

#[cfg(feature = "net")]
pub mod net;

pub use discovery::{Discovery, DiscoveryError, DISCOVERY_TTL};
pub use endpoint::{check_endpoint_url, EndpointError};
pub use fetch::{FetchError, HttpFetch, MAX_RESPONSE_BYTES, REQUEST_TIMEOUT_SECS};
pub use flow::{hash_eq, sha256, FlowError, FlowSecrets, FLOW_SECRET_BYTES};
pub use jwks::{Jwk, JwksError, GLOBAL_REFETCH_INTERVAL, JWKS_TTL, KID_COOLDOWN};
pub use jwt::{IdTokenClaims, JwtError, ALLOWED_ALGS, CLOCK_LEEWAY_SECS};
pub use provider::{OidcProvider, ProviderConfig};
pub use token::{client_auth_method, ClientAuth, TokenError, TokenResponse};

#[cfg(feature = "net")]
pub use net::{ClientError, RustlsFetch};

#[cfg(test)]
mod tests;
