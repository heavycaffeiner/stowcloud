//! What counts as an internal network.
//!
//! "Private" = RFC1918 + `127.0.0.0/8` + `169.254.0.0/16` + `100.64.0.0/10`
//! (IPv4) and `fc00::/7` + `fe80::/10` + `::1` (IPv6). Anything else is
//! "public".
//!
//! The sidecar owns the decision this feeds: it enumerates the host's own
//! interfaces and expands `interfaces`/`hosts allow` from the networks the
//! machine is actually attached to. `sc-core` runs in a different network
//! namespace, so a gate here could only ever describe the wrong machine.
//!
//! What is left here serves the one case sc-core can decide: an operator who
//! pins `smb.interfaces` has named the addresses themself, and naming a public
//! one is checked against `allow_public_bind`.
//!
//! `100.64.0.0/10` is CGNAT, which is where every Tailscale address lives, and
//! it is here for the same reason `sc_http::config::is_private_host_literal`
//! has it: a tailnet is a private network by construction. Without it the two
//! halves disagreed, and a deployment reached over Tailscale served the web app
//! fine while `hosts allow` refused every SMB client from the same tailnet. The
//! cost is that a host genuinely behind an ISP's CGNAT counts as LAN, which is
//! the same trade the HTTP side already makes.

use std::net::IpAddr;

/// `true` if `ip` falls in one of the private/link-local/loopback ranges
/// this project treats as "LAN".
pub fn is_private(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => {
            let o = v4.octets();
            o[0] == 10
                || (o[0] == 172 && (16..=31).contains(&o[1]))
                || (o[0] == 192 && o[1] == 168)
                || o[0] == 127
                || (o[0] == 169 && o[1] == 254)
                // 100.64.0.0/10: top 10 bits fixed => second octet 64..=127.
                || (o[0] == 100 && (64..=127).contains(&o[1]))
        }
        IpAddr::V6(v6) => {
            if v6.is_loopback() {
                return true;
            }
            let seg = v6.segments();
            let first_byte = (seg[0] >> 8) as u8;
            // fc00::/7: top 7 bits fixed => first byte in {0xfc, 0xfd}.
            if (0xfc..=0xfd).contains(&first_byte) {
                return true;
            }
            // fe80::/10: top 10 bits fixed => seg[0] & 0xffc0 == 0xfe80.
            if (seg[0] & 0xffc0) == 0xfe80 {
                return true;
            }
            false
        }
    }
}

/// The address part of `spec`, which is either a bare IP or `addr/prefix`.
/// `Err` carries why it is neither, to be quoted back in a config error.
pub(crate) fn parse_addr_spec(spec: &str) -> Result<IpAddr, &'static str> {
    let (addr, prefix) = match spec.split_once('/') {
        Some((a, p)) => (a, Some(p)),
        None => (spec, None),
    };
    let ip: IpAddr = addr
        .parse()
        .map_err(|_| "not an IP address or CIDR block")?;
    if let Some(p) = prefix {
        let bits: u8 = p.parse().map_err(|_| "prefix length is not a number")?;
        let max = if ip.is_ipv4() { 32 } else { 128 };
        if bits > max {
            return Err("prefix length is too long for the address family");
        }
    }
    Ok(ip)
}

/// The private CIDR list written into `hosts allow` when the operator pins
/// `smb.interfaces`, and the list the agent falls back to inside a container
/// namespace, where the only subnet it can see is the bridge's and LAN
/// clients arrive through DNAT wearing their own addresses.
pub const PRIVATE_CIDRS_V4: &[&str] = &[
    "10.0.0.0/8",
    "172.16.0.0/12",
    "192.168.0.0/16",
    "127.0.0.0/8",
    "100.64.0.0/10",
    "169.254.0.0/16",
];
pub const PRIVATE_CIDRS_V6: &[&str] = &["fc00::/7", "fe80::/10", "::1/128"];

/// The well-known private range enclosing `ip`, or `None` if it is globally
/// routable.
///
/// The enclosing range and not the on-link prefix, for two reasons that both
/// showed up in practice: an internal network is routinely several subnets
/// behind a router, and a `tailscale0` address carries a /32 whose own subnet
/// admits nobody while `100.64.0.0/10` admits the whole tailnet.
pub fn enclosing_private_range(ip: &IpAddr) -> Option<&'static str> {
    match ip {
        IpAddr::V4(v4) => {
            let o = v4.octets();
            match o {
                [10, ..] => Some("10.0.0.0/8"),
                [172, b, ..] if (16..=31).contains(&b) => Some("172.16.0.0/12"),
                [192, 168, ..] => Some("192.168.0.0/16"),
                [127, ..] => Some("127.0.0.0/8"),
                [169, 254, ..] => Some("169.254.0.0/16"),
                // CGNAT, which is where every Tailscale address lives.
                [100, b, ..] if (64..=127).contains(&b) => Some("100.64.0.0/10"),
                _ => None,
            }
        }
        IpAddr::V6(v6) => {
            if v6.is_loopback() {
                return Some("::1/128");
            }
            let seg = v6.segments();
            if (0xfc..=0xfd).contains(&((seg[0] >> 8) as u8)) {
                return Some("fc00::/7");
            }
            if (seg[0] & 0xffc0) == 0xfe80 {
                return Some("fe80::/10");
            }
            None
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{Ipv4Addr, Ipv6Addr};

    #[test]
    fn private_v4_ranges() {
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(172, 16, 0, 1))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(172, 31, 255, 255))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(192, 168, 1, 10))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(169, 254, 1, 1))));
        // CGNAT, which is what a Tailscale address is.
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(100, 64, 0, 1))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(100, 101, 102, 103))));
        assert!(is_private(&IpAddr::V4(Ipv4Addr::new(100, 127, 255, 255))));
    }

    #[test]
    fn public_v4_rejected() {
        assert!(!is_private(&IpAddr::V4(Ipv4Addr::new(1, 1, 1, 1))));
        assert!(!is_private(&IpAddr::V4(Ipv4Addr::new(203, 0, 113, 5))));
        // just outside the 172.16.0.0/12 band
        assert!(!is_private(&IpAddr::V4(Ipv4Addr::new(172, 32, 0, 1))));
        assert!(!is_private(&IpAddr::V4(Ipv4Addr::new(172, 15, 255, 255))));
        // just outside the 100.64.0.0/10 band
        assert!(!is_private(&IpAddr::V4(Ipv4Addr::new(100, 63, 255, 255))));
        assert!(!is_private(&IpAddr::V4(Ipv4Addr::new(100, 128, 0, 1))));
    }

    #[test]
    fn addr_specs_parse_to_their_address() {
        assert_eq!(
            parse_addr_spec("192.168.1.10").unwrap(),
            IpAddr::V4(Ipv4Addr::new(192, 168, 1, 10))
        );
        assert_eq!(
            parse_addr_spec("10.0.0.0/8").unwrap(),
            IpAddr::V4(Ipv4Addr::new(10, 0, 0, 0))
        );
        assert_eq!(
            parse_addr_spec("fd00::1/64").unwrap(),
            IpAddr::V6(Ipv6Addr::new(0xfd00, 0, 0, 0, 0, 0, 0, 1))
        );
        // An interface name is a valid `interfaces` entry to Samba and an
        // unprovable one here, so it has to fail rather than pass through.
        assert!(parse_addr_spec("eth0").is_err());
        assert!(parse_addr_spec("192.168.1.0/33").is_err());
        assert!(parse_addr_spec("192.168.1.0/lan").is_err());
    }

    #[test]
    fn private_v6_ranges() {
        assert!(is_private(&IpAddr::V6(Ipv6Addr::LOCALHOST)));
        assert!(is_private(&IpAddr::V6(Ipv6Addr::new(
            0xfd00, 0, 0, 0, 0, 0, 0, 1
        ))));
        assert!(is_private(&IpAddr::V6(Ipv6Addr::new(
            0xfc00, 0, 0, 0, 0, 0, 0, 1
        ))));
        assert!(is_private(&IpAddr::V6(Ipv6Addr::new(
            0xfe80, 0, 0, 0, 0, 0, 0, 1
        ))));
    }

    /// The two halves have to answer the same question the same way: an
    /// address the bind gate calls private must have a range the agent can
    /// admit, or a client would reach an interface `hosts allow` then denies.
    #[test]
    fn every_private_address_has_an_enclosing_range() {
        for ip in [
            IpAddr::V4(Ipv4Addr::new(10, 1, 2, 3)),
            IpAddr::V4(Ipv4Addr::new(172, 20, 0, 5)),
            IpAddr::V4(Ipv4Addr::new(192, 168, 1, 10)),
            IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)),
            IpAddr::V4(Ipv4Addr::new(169, 254, 3, 4)),
            IpAddr::V4(Ipv4Addr::new(100, 101, 102, 103)),
            IpAddr::V6(Ipv6Addr::LOCALHOST),
            IpAddr::V6(Ipv6Addr::new(0xfd00, 0, 0, 0, 0, 0, 0, 1)),
            IpAddr::V6(Ipv6Addr::new(0xfe80, 0, 0, 0, 0, 0, 0, 1)),
        ] {
            assert!(is_private(&ip), "{ip} is not private");
            assert!(enclosing_private_range(&ip).is_some(), "{ip} has no range");
        }
    }

    #[test]
    fn public_addresses_have_no_enclosing_range() {
        assert_eq!(enclosing_private_range(&IpAddr::V4(Ipv4Addr::new(8, 8, 8, 8))), None);
        assert_eq!(enclosing_private_range(&IpAddr::V4(Ipv4Addr::new(172, 32, 0, 1))), None);
        assert_eq!(
            enclosing_private_range(&IpAddr::V6(Ipv6Addr::new(0x2001, 0xdb8, 0, 0, 0, 0, 0, 1))),
            None
        );
    }

    #[test]
    fn ranges_are_the_enclosing_one_not_the_on_link_prefix() {
        // A tailnet address carries a /32; the range that admits the rest of
        // the tailnet is the CGNAT block.
        assert_eq!(
            enclosing_private_range(&IpAddr::V4(Ipv4Addr::new(100, 90, 1, 1))),
            Some("100.64.0.0/10")
        );
    }

    #[test]
    fn public_v6_rejected() {
        // 2001:db8::/32 documentation range — globally routable in spirit.
        assert!(!is_private(&IpAddr::V6(Ipv6Addr::new(
            0x2001, 0x0db8, 0, 0, 0, 0, 0, 1
        ))));
    }
}
