//! Scheme and address rules for every URL this crate is willing to fetch.
//!
//! Proposal §4.3.4, correction 4: the draft required every endpoint in the
//! discovery document to share a host with the issuer, which would have made
//! Google Workspace (issuer `accounts.google.com`, token endpoint
//! `oauth2.googleapis.com`, JWKS `www.googleapis.com`) permanently
//! unusable. The same-host rule is gone. What replaces it is the pair of
//! checks that actually bound SSRF: HTTPS only, and no address in private,
//! loopback, or link-local space unless the operator has said otherwise.
//!
//! The address half has two enforcement points and needs both.
//!
//!  * Here, on the URL, which catches an IP literal (`https://10.0.0.5/...`)
//!    without a DNS lookup. That matters because this check has to work in a
//!    `--no-default-features` build, where there is no resolver and no
//!    runtime to run one on.
//!  * In [`crate::net`], inside hyper's resolver, which is where a hostname
//!    is decided. Doing it there rather than with a separate lookup before
//!    the request is what closes the DNS rebinding gap: the addresses the
//!    guard inspects are the addresses the connector will use.

use std::net::{IpAddr, Ipv4Addr};
use url::{Host, Url};

/// Why a URL was refused before any packet was sent.
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum EndpointError {
    #[error("endpoint url is not parseable: {0}")]
    Malformed(String),
    #[error("endpoint url is not https")]
    InsecureScheme,
    #[error("endpoint url points at a private, loopback, or link-local address")]
    PrivateAddress,
}

/// Parses `raw` and applies both rules. `allow_private` is
/// `oidc.allow_private_endpoints`: self-hosting a Keycloak on the same
/// network as this server is a real deployment, so the address rule is a
/// default and not a law.
pub fn check_endpoint_url(raw: &str, allow_private: bool) -> Result<Url, EndpointError> {
    let url = Url::parse(raw).map_err(|e| EndpointError::Malformed(e.to_string()))?;
    if url.scheme() != "https" {
        return Err(EndpointError::InsecureScheme);
    }
    match url.host() {
        None => return Err(EndpointError::Malformed("no host".into())),
        Some(Host::Ipv4(ip)) if !allow_private && is_blocked(&IpAddr::V4(ip)) => {
            return Err(EndpointError::PrivateAddress)
        }
        Some(Host::Ipv6(ip)) if !allow_private && is_blocked(&IpAddr::V6(ip)) => {
            return Err(EndpointError::PrivateAddress)
        }
        // A hostname is not decided until it is resolved, so it passes here
        // and is judged by the resolver guard instead.
        Some(_) => {}
    }
    Ok(url)
}

/// True for the address ranges proposal §4.3.4 names, plus the two that are
/// the same hole wearing a different encoding.
///
/// The named three are loopback, RFC 1918 private, and link-local, the last
/// of which is what `169.254.169.254` (every cloud's instance metadata
/// service) lives in. Added on top:
///
///  * the unspecified address, since `0.0.0.0` reaches the local host on
///    Linux, and
///  * IPv6 unique-local `fc00::/7`, which is RFC 1918's counterpart, plus
///    IPv4-mapped IPv6 (`::ffff:10.0.0.1`), which is a private v4 address
///    that no v4 predicate would ever see.
///
/// Not covered, and named here so the omission is a decision rather than an
/// oversight: CGNAT `100.64.0.0/10` and the benchmark range
/// `198.18.0.0/15`. Neither is reachable-but-trusted in the way the ranges
/// above are, and an operator who really has an IdP there can set
/// `allow_private_endpoints`.
pub fn is_blocked(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => is_blocked_v4(v4),
        IpAddr::V6(v6) => match v6.to_ipv4_mapped() {
            Some(v4) => is_blocked_v4(&v4),
            None => {
                v6.is_loopback()
                    || v6.is_unspecified()
                    // `Ipv6Addr::is_unique_local` and `is_unicast_link_local`
                    // are still unstable, so the two prefixes are matched by
                    // hand: fc00::/7 and fe80::/10.
                    || (v6.segments()[0] & 0xfe00) == 0xfc00
                    || (v6.segments()[0] & 0xffc0) == 0xfe80
            }
        },
    }
}

fn is_blocked_v4(ip: &Ipv4Addr) -> bool {
    ip.is_loopback() || ip.is_private() || ip.is_link_local() || ip.is_unspecified()
}

/// Convenience for the resolver guard, which holds `Ipv6Addr` and
/// `Ipv4Addr` behind a `SocketAddr` rather than an `IpAddr`.
#[cfg(feature = "net")]
pub(crate) fn is_blocked_socket(addr: &std::net::SocketAddr) -> bool {
    is_blocked(&addr.ip())
}
