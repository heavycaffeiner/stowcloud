//! Runtime configuration for the middleware stack.

use std::net::IpAddr;

#[derive(Clone, Debug)]
pub struct HttpConfig {
    /// Host header allowlist for the *app* origin (serves `/api/**`, cookies
    /// parsed here).
    pub app_hosts: Vec<String>,
    /// Host header allowlist for the *content* origin (serves signed URLs
    /// only, no cookies ever parsed —).
    pub content_hosts: Vec<String>,
    /// CIDRs trusted to supply `CF-Connecting-IP` (step 2).
    pub trusted_proxy_cidrs: Vec<Cidr>,
    /// Origins accepted by CSRF's `Origin` header check.
    pub allowed_origins: Vec<String>,
    /// Per-IP token bucket for the general `RateLimit` middleware (distinct
    /// from `sc-auth`'s login-specific gate).
    pub rate_ip_capacity: u32,
    pub rate_ip_refill: std::time::Duration,
    /// Body size limit applied to everything except `/api/uploads/**`.
    pub body_limit_bytes: usize,
    /// Names of the compatibility layers this build has mounted, reported
    /// verbatim as `capabilities.features.extensions`.
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
    /// The externally reachable origin (`scheme://host[:port]`, no trailing
    /// slash) a public share link is built from, when the deployment has
    /// declared one. `None` falls back to `https://{app_hosts[0]}`.
    ///
    /// This exists because that fallback is a guess and was the only thing
    /// `public_link_url` ever used: it drops the port and hardcodes `https`,
    /// so any deployment not sitting on 443 handed its users a link that
    /// resolves to nothing. The assembler already resolves this origin for
    /// the compatibility layer, which has always been documented as
    /// governing "every public share link" as well.
    pub public_base_url: Option<String>,
    /// Startup-time seed only. `capabilities`/`GET /api/auth/session` read
    /// the *live* value via `AppState::uploads::chunk_limits()` instead (that
    /// value can change at runtime via `PATCH /api/admin/upload-settings`,
    /// this one cannot) — these two fields exist so a bare `HttpConfig`
    /// (e.g. `AppState::for_tests`) still has plausible numbers before any
    /// engine is attached.
    pub chunk_size_min: u64,
    /// See `chunk_size_min` above.
    pub chunk_size_default: u64,
    /// The port this deployment's (only, and always TLS) listener is on.
    /// `None` on a bare `HttpConfig`.
    ///
    /// It exists only so [`is_self_lan_origin`] can tell "a page this server
    /// served over the LAN" from "some other service sharing the same LAN
    /// address". Same-site is computed from the host alone, so a neighbouring
    /// service on `https://192.168.0.50:8096` is same-site with us on
    /// `192.168.0.50` and a `SameSite=Lax` session cookie *is* sent on its
    /// cross-origin writes. The port is the only thing separating the two, so
    /// the CSRF origin check has to know ours.
    pub https_port: Option<u16>,
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
            public_base_url: None,
            chunk_size_min: 5 * 1024 * 1024,
            chunk_size_default: 10 * 1024 * 1024,
            https_port: None,
        }
    }
}

/// Split a `Host`/authority into host and optional port, keeping IPv6 literals
/// intact.
///
/// `split(':').next()` was what the host guard used, and it turns
/// `[::1]:8080` into `[`, so `::1`, one of the three default `app_hosts`,
/// could never actually match and a browser on IPv6 loopback got a 421 from a
/// server that had explicitly allowed it.
pub fn split_host_port(authority: &str) -> (&str, Option<u16>) {
    if let Some(rest) = authority.strip_prefix('[') {
        // `[::1]:8080` / `[::1]`. Everything up to `]` is the address.
        if let Some((addr, tail)) = rest.split_once(']') {
            let port = tail.strip_prefix(':').and_then(|p| p.parse().ok());
            return (addr, port);
        }
        return (authority, None);
    }
    match authority.split_once(':') {
        // A bare IPv6 literal has more than one colon and no brackets; it is
        // not host:port, it is the whole address.
        Some(_) if authority.matches(':').count() > 1 => (authority, None),
        Some((h, p)) => (h, p.parse().ok()),
        None => (authority, None),
    }
}

/// Is `host` an IP literal that only means something inside a private network?
///
/// Loopback, RFC 1918, IPv4 link-local, CGNAT (`100.64/10`), IPv6 loopback,
/// ULA (`fc00::/7`) and IPv6 link-local (`fe80::/10`).
///
/// The host guard exists to stop DNS rebinding, and rebinding is carried out
/// with a *name*: the attacker points `evil.com` at an internal address, so the
/// browser sends `Host: evil.com`. Nothing an attacker controls makes a browser
/// send `Host: 192.168.0.50` to this server except a user actually typing that
/// address, which is the case being allowed.
///
/// `100.64/10` is here for Tailscale, whose addresses come out of exactly that
/// range, and it is the one entry that is not simply "a private network by
/// definition": the range is shared CGNAT space an ISP may also use. That costs
/// nothing here. Being admitted by this guard is not authorisation, it only
/// means the request is answered rather than 421'd, and everything past it still
/// needs a session. WireGuard needs no entry of its own; its tunnels are
/// conventionally numbered out of RFC 1918 or ULA, both already covered.
pub fn is_private_host_literal(host: &str) -> bool {
    let Ok(ip) = host.parse::<IpAddr>() else {
        return false;
    };
    match ip {
        IpAddr::V4(v4) => {
            let [a, b, ..] = v4.octets();
            let cgnat = a == 100 && (64..128).contains(&b);
            v4.is_loopback() || v4.is_private() || v4.is_link_local() || cgnat
        }
        IpAddr::V6(v6) => {
            let seg = v6.segments()[0];
            v6.is_loopback() || (seg & 0xfe00) == 0xfc00 || (seg & 0xffc0) == 0xfe80
        }
    }
}

/// Does `origin` name a private-LAN address *and* a listener this server
/// actually answers on?
///
/// The port check is the whole point; see [`HttpConfig::http_port`]. Without
/// it, any other service on the same NAS (`http://192.168.0.50:8096`) could
/// forge a same-site write, because `SameSite=Lax` attaches the session cookie
/// on host match alone and ports do not enter into it.
pub fn is_self_lan_origin(cfg: &HttpConfig, origin: &str) -> bool {
    // `https` only. This server has no plaintext listener, so an `http://`
    // origin naming our own port did not come from us.
    let Some(authority) = origin.strip_prefix("https://") else {
        return false;
    };
    let Some(expected) = cfg.https_port else {
        return false;
    };
    let (host, port) = split_host_port(authority);
    port.unwrap_or(443) == expected && is_private_host_literal(host)
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

    #[test]
    fn split_host_port_keeps_ipv6_literals_whole() {
        assert_eq!(split_host_port("[::1]:8080"), ("::1", Some(8080)));
        assert_eq!(split_host_port("[fd00::5]"), ("fd00::5", None));
        assert_eq!(split_host_port("::1"), ("::1", None));
        assert_eq!(split_host_port("192.168.0.50:8443"), ("192.168.0.50", Some(8443)));
        assert_eq!(split_host_port("nas.local"), ("nas.local", None));
    }

    #[test]
    fn private_literals_are_recognised() {
        for h in [
            "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.50", "169.254.1.1",
            // CGNAT, which is where every Tailscale address lives.
            "100.64.0.1", "100.101.102.103", "100.127.255.255",
            "::1", "fd00::5", "fe80::1",
        ] {
            assert!(is_private_host_literal(h), "{h}");
        }
        // Public addresses and names are not this. `100.63` and `100.128` sit
        // either side of CGNAT and are ordinary public space.
        for h in ["8.8.8.8", "100.63.255.255", "100.128.0.1", "2606:4700::1", "nas.local", "localhost", ""] {
            assert!(!is_private_host_literal(h), "{h}");
        }
    }

    #[test]
    fn lan_origin_must_match_our_own_port() {
        let cfg = HttpConfig { https_port: Some(8443), ..Default::default() };
        assert!(is_self_lan_origin(&cfg, "https://192.168.0.50:8443"));
        assert!(is_self_lan_origin(&cfg, "https://100.101.102.103:8443"), "tailscale");
        // A neighbouring service on the same address is a different origin.
        assert!(!is_self_lan_origin(&cfg, "https://192.168.0.50:8096"));
        // Public addresses never qualify, whatever the port.
        assert!(!is_self_lan_origin(&cfg, "https://8.8.8.8:8443"));
    }

    /// There is no plaintext listener, so an `http://` origin naming our port
    /// is something else on the same address, not us.
    #[test]
    fn a_plaintext_lan_origin_is_never_ours() {
        let cfg = HttpConfig { https_port: Some(8443), ..Default::default() };
        assert!(!is_self_lan_origin(&cfg, "http://192.168.0.50:8443"));
    }

    #[test]
    fn lan_origin_is_refused_when_no_port_is_known() {
        let cfg = HttpConfig { https_port: None, ..Default::default() };
        assert!(!is_self_lan_origin(&cfg, "https://192.168.0.50:443"));
    }
}
