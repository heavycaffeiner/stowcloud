//! Runtime configuration for the middleware stack (`DESIGN-API.md` §9).

use std::net::IpAddr;

#[derive(Clone, Debug)]
pub struct HttpConfig {
    /// Host header allowlist for the *app* origin (serves `/api/**`, cookies
    /// parsed here).
    pub app_hosts: Vec<String>,
    /// Host header allowlist for the *content* origin (serves signed URLs
    /// only, no cookies ever parsed —).
    pub content_hosts: Vec<String>,
    /// CIDRs trusted to supply `CF-Connecting-IP` (`DESIGN-API.md` §9 step 2).
    pub trusted_proxy_cidrs: Vec<Cidr>,
    /// Origins accepted by CSRF's `Origin` header check
    /// (`DESIGN-AUTH.md` §3.3).
    pub allowed_origins: Vec<String>,
    /// Per-IP token bucket for the general `RateLimit` middleware (distinct
    /// from `sc-auth`'s login-specific gate).
    pub rate_ip_capacity: u32,
    pub rate_ip_refill: std::time::Duration,
    /// Body size limit applied to everything except `/api/uploads/**`.
    pub body_limit_bytes: usize,
    /// Names of the compatibility layers this build has mounted, reported
    /// verbatim as `capabilities.features.extensions` (`DESIGN-API.md` §8).
    ///
    /// A *list of layer names* rather than one boolean per vendor: a field
    /// called `<vendor>_compat` would put that vendor's name in the core API
    /// — which is the isolation-contract violation the CI grep gate exists to
    /// catch — and would need a new core field for every future layer. Each
    /// layer contributes its own name here at wiring time; this crate never
    /// learns what the strings mean.
    pub extensions: Vec<String>,
    /// URL path prefixes owned by mounts assembled *outside* this crate —
    /// WebDAV, and (feature-gated) any compatibility layer — that get merged
    /// into the same router beside [`crate::build_router`]'s output
    /// (`sc-server`'s `App::router`).
    ///
    /// Those mounts never register a fallback of their own, so this crate's
    /// single router-wide fallback (`routes::admin_catch_all`) is the only
    /// thing standing between an unmatched request under one of those
    /// prefixes and the `embed-ui` SPA fallback silently turning it into an
    /// HTML document. A `404` that quietly becomes an HTML page is a
    /// debugging nightmare for exactly the callers most likely to hit it, so
    /// `admin_catch_all` checks this list before ever trying to serve the
    /// built frontend.
    ///
    /// Deliberately *not* the same prefixes this crate's own
    /// `/api/`·`/c/`·`/s/` use (those are hardcoded in `routes.rs` — they are
    /// this crate's own vocabulary, always true, no assembler required to
    /// name them). What belongs here is everything this crate cannot know
    /// about on its own — in particular the compatibility layer's protocol
    /// vocabulary (`remote.php`, `ocs`, ...), which this crate's own
    /// isolation gate forbids appearing in its *code* at all. Supplying it
    /// as plain runtime data from the assembler that actually does the
    /// mounting keeps that gate meaningful instead of carving out an
    /// exception for it.
    pub reserved_path_prefixes: Vec<String>,
    /// Startup-time seed only. `capabilities`/`GET /api/auth/session` read
    /// the *live* value via `AppState::uploads::chunk_limits()` instead (that
    /// value can change at runtime via `PATCH /api/admin/upload-settings`,
    /// this one cannot) — these two fields exist so a bare `HttpConfig`
    /// (e.g. `AppState::for_tests`) still has plausible numbers before any
    /// engine is attached.
    pub chunk_size_min: u64,
    /// See `chunk_size_min` above.
    pub chunk_size_default: u64,
}

impl Default for HttpConfig {
    fn default() -> Self {
        Self {
            // Loopback forms only. The design's `app.example.com` /
            // `content.example.com` are illustrative hostnames, and shipping
            // them as defaults would mean a real deployment silently accepts
            // requests addressed to somebody else's domain.
            //
            // `127.0.0.1` and `::1` are here because `localhost` alone made a
            // freshly-started server reject every request from its own bind
            // address with 421 — the Host header carries the literal the
            // client dialled, not a resolved name. `sc-server` additionally
            // injects its configured bind address (see its config loader).
            app_hosts: vec!["localhost".into(), "127.0.0.1".into(), "::1".into()],
            // Empty means single-origin deployment:
            // permits it as a fallback but requires a startup warning, since
            // serving user content from the app origin gives up the XSS
            // isolation that separation buys.
            content_hosts: Vec::new(),
            trusted_proxy_cidrs: Vec::new(),
            allowed_origins: vec!["https://app.example.com".into()],
            rate_ip_capacity: 60,
            rate_ip_refill: std::time::Duration::from_secs(1),
            body_limit_bytes: 16 * 1024 * 1024,
            extensions: Vec::new(),
            reserved_path_prefixes: Vec::new(),
            chunk_size_min: 5 * 1024 * 1024,
            chunk_size_default: 10 * 1024 * 1024,
        }
    }
}

/// Minimal IPv4/IPv6 CIDR matcher — no extra crate dependency for this.
#[derive(Clone, Copy, Debug)]
pub struct Cidr {
    pub addr: IpAddr,
    pub prefix: u8,
}

impl Cidr {
    pub fn parse(s: &str) -> Option<Self> {
        let (addr_s, prefix_s) = s.split_once('/')?;
        let addr: IpAddr = addr_s.parse().ok()?;
        let prefix: u8 = prefix_s.parse().ok()?;
        Some(Self { addr, prefix })
    }

    pub fn contains(&self, ip: IpAddr) -> bool {
        match (self.addr, ip) {
            (IpAddr::V4(net), IpAddr::V4(cand)) => {
                let bits = self.prefix.min(32);
                let mask = if bits == 0 { 0u32 } else { u32::MAX << (32 - bits) };
                (u32::from(net) & mask) == (u32::from(cand) & mask)
            }
            (IpAddr::V6(net), IpAddr::V6(cand)) => {
                let bits = self.prefix.min(128);
                let mask = if bits == 0 { 0u128 } else { u128::MAX << (128 - bits) };
                (u128::from(net) & mask) == (u128::from(cand) & mask)
            }
            _ => false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cidr_v4_matches() {
        let c = Cidr::parse("10.0.0.0/8").unwrap();
        assert!(c.contains("10.1.2.3".parse().unwrap()));
        assert!(!c.contains("11.1.2.3".parse().unwrap()));
    }

    #[test]
    fn cidr_v4_exact_host() {
        let c = Cidr::parse("192.168.1.5/32").unwrap();
        assert!(c.contains("192.168.1.5".parse().unwrap()));
        assert!(!c.contains("192.168.1.6".parse().unwrap()));
    }
}
