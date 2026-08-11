//! Which addresses smbd may bind, decided where smbd actually runs.
//!
//! `sc-core` renders `interfaces = lo` and `hosts allow = 127.0.0.0/8 ::1/128`
//! and nothing else, because it sits in a different network namespace and
//! cannot see the host's devices. This module runs in the namespace that can,
//! and rewrites those two lines.
//!
//! The rule is: a network this machine is attached to is admitted, no
//! configuration needed. `interfaces` gets the device, `hosts allow` gets the
//! well-known private range enclosing its address.
//!
//! Globally routable addresses are left out unless `network.policy` says
//! `allow_public_bind=1`, and none of this runs at all when it says
//! `pinned_interfaces=1`: the operator named the addresses in
//! `smb.interfaces`, so `sc-core` rendered the final answer.
//!
//! This used to shell out to `ip addr show`, with the failure swallowed
//! (`2>/dev/null || true`). On an image without `ip` the scan came back empty,
//! the closed case survived into the promoted config, `testparm` accepted it,
//! and SMB answered on loopback and nowhere else with nothing logged anywhere.
//! Reading the interfaces through `getifaddrs` removes the dependency, and
//! [`Scope::detected`] makes an empty answer something the caller has to
//! report rather than something that looks like a decision.

use std::net::IpAddr;

/// One network device and every address configured on it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Device {
    pub name: String,
    pub addrs: Vec<IpAddr>,
    /// One end of a veth pair, i.e. we are inside a container network
    /// namespace and this address describes the bridge rather than any
    /// network a client comes from.
    pub veth: bool,
}

/// The two lines that replace what `sc-core` rendered closed.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Scope {
    pub interfaces: String,
    pub hosts_allow: String,
    /// `false` when nothing was found to bind. The config is still the closed
    /// one, which is safe, but it is never what the operator wanted, so the
    /// caller reports it instead of promoting it in silence.
    pub detected: bool,
}

/// Every UP, non-loopback device carrying at least one address.
///
/// The only part of this module that talks to the operating system. What it
/// returns is folded by [`compute`], which is a pure function and stays
/// compiled everywhere, so the decision that determines who can reach SMB is
/// testable on any development host rather than on CI alone.
#[cfg(unix)]
pub fn devices() -> std::io::Result<Vec<Device>> {
    use std::ffi::CStr;

    let mut out: Vec<Device> = Vec::new();
    // SAFETY: `getifaddrs` fills `head` with a list it owns; every pointer is
    // read before `freeifaddrs` and none escapes the loop.
    unsafe {
        let mut head: *mut libc::ifaddrs = std::ptr::null_mut();
        if libc::getifaddrs(&mut head) != 0 {
            return Err(std::io::Error::last_os_error());
        }
        let mut cur = head;
        while !cur.is_null() {
            let ifa = &*cur;
            cur = ifa.ifa_next;

            if ifa.ifa_name.is_null() || ifa.ifa_addr.is_null() {
                continue;
            }
            let flags = ifa.ifa_flags as libc::c_int;
            if flags & libc::IFF_UP == 0 || flags & libc::IFF_LOOPBACK != 0 {
                continue;
            }
            let Ok(name) = CStr::from_ptr(ifa.ifa_name).to_str() else {
                continue;
            };
            let Some(ip) = sockaddr_ip(ifa.ifa_addr) else {
                continue;
            };
            match out.iter_mut().find(|d| d.name == name) {
                Some(d) => d.addrs.push(ip),
                None => out.push(Device {
                    name: name.to_string(),
                    addrs: vec![ip],
                    veth: is_veth(name),
                }),
            }
        }
        libc::freeifaddrs(head);
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    Ok(out)
}

/// # Safety
/// `sa` must be a valid `sockaddr` from `getifaddrs`.
#[cfg(unix)]
unsafe fn sockaddr_ip(sa: *const libc::sockaddr) -> Option<IpAddr> {
    use std::net::{Ipv4Addr, Ipv6Addr};

    match (*sa).sa_family as libc::c_int {
        libc::AF_INET => {
            let v4: libc::sockaddr_in = std::ptr::read_unaligned(sa.cast());
            Some(IpAddr::V4(Ipv4Addr::from(v4.sin_addr.s_addr.to_ne_bytes())))
        }
        libc::AF_INET6 => {
            let v6: libc::sockaddr_in6 = std::ptr::read_unaligned(sa.cast());
            Some(IpAddr::V6(Ipv6Addr::from(v6.sin6_addr.s6_addr)))
        }
        _ => None,
    }
}

/// A veth's `iflink` names its peer in the other namespace; a physical
/// device, a bridge and a tun all point at themselves. Unreadable means "not
/// a veth" rather than an error: this only decides how wide `hosts allow`
/// gets, and the narrow answer is the safe one.
#[cfg(unix)]
fn is_veth(name: &str) -> bool {
    let read = |f: &str| std::fs::read_to_string(format!("/sys/class/net/{name}/{f}")).ok();
    match (read("iflink"), read("ifindex")) {
        (Some(a), Some(b)) => a.trim() != b.trim(),
        _ => false,
    }
}

/// [`devices`] then [`compute`]. An error here is the caller's to report: it
/// means the machine's own interfaces could not be read, which is not a
/// reason to promote a config that says loopback and call it a scope.
#[cfg(unix)]
pub fn detect(allow_public: bool) -> std::io::Result<Scope> {
    Ok(compute(&devices()?, allow_public))
}

/// Fold devices into the two `smb.conf` lines.
pub fn compute(devices: &[Device], allow_public: bool) -> Scope {
    let mut ifaces = vec!["lo".to_string()];
    let mut hosts: Vec<String> = vec!["127.0.0.0/8".to_string(), "::1/128".to_string()];
    let add_host = |range: &str, hosts: &mut Vec<String>| {
        if !hosts.iter().any(|h| h == range) {
            hosts.push(range.to_string());
        }
    };

    let mut detected = false;
    for dev in devices {
        let mut accepted: Vec<String> = Vec::new();
        let mut rejected = false;
        for ip in &dev.addrs {
            match sc_smb::enclosing_private_range(ip) {
                Some(range) => {
                    accepted.push(ip.to_string());
                    add_host(range, &mut hosts);
                }
                None if allow_public => {
                    // The documented meaning of the opt-in is "SMB is on the
                    // internet". Naming a subnet here would understate it.
                    accepted.push(ip.to_string());
                    add_host("ALL", &mut hosts);
                }
                None => rejected = true,
            }
        }
        if accepted.is_empty() {
            continue;
        }
        detected = true;
        // The device name survives a DHCP lease change; individual addresses
        // do not. Only fall back to addresses when the device also carries one
        // we are refusing, since naming the device there would bind that too.
        if rejected {
            ifaces.extend(accepted);
        } else {
            ifaces.push(dev.name.clone());
        }
    }

    // In a container namespace the detected subnet is the bridge's, and LAN
    // clients arrive through DNAT with their own source addresses, which that
    // subnet does not cover. Detection cannot see their networks from here, so
    // admit private space wholesale rather than deny them. Binding is
    // unaffected: only the veth exists to bind.
    if !devices.is_empty() && devices.iter().all(|d| d.veth) {
        for r in sc_smb::PRIVATE_CIDRS_V4.iter().chain(sc_smb::PRIVATE_CIDRS_V6) {
            add_host(r, &mut hosts);
        }
    }

    Scope {
        interfaces: ifaces.join(" "),
        hosts_allow: hosts.join(" "),
        detected,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn dev(name: &str, addrs: &[&str], veth: bool) -> Device {
        Device {
            name: name.to_string(),
            addrs: addrs.iter().map(|a| a.parse().unwrap()).collect(),
            veth,
        }
    }

    #[test]
    fn nothing_found_is_the_closed_case_and_says_so() {
        let s = compute(&[], false);
        assert_eq!(s.interfaces, "lo");
        assert_eq!(s.hosts_allow, "127.0.0.0/8 ::1/128");
        // The whole point: the caller can tell this apart from a real answer.
        assert!(!s.detected);
    }

    #[test]
    fn a_lan_device_is_named_and_its_range_admitted() {
        let s = compute(&[dev("eth0", &["192.168.1.10"], false)], false);
        assert_eq!(s.interfaces, "lo eth0");
        assert!(s.hosts_allow.contains("192.168.0.0/16"));
        assert!(s.detected);
    }

    /// Binding is per address, but admission is per network: a client at
    /// 192.168.1.50 reaches the address bound above and must not be denied.
    #[test]
    fn admission_is_the_enclosing_range_not_the_address() {
        let s = compute(&[dev("tailscale0", &["100.90.1.1"], false)], false);
        assert!(s.hosts_allow.contains("100.64.0.0/10"));
    }

    #[test]
    fn a_public_address_is_left_out_without_the_opt_in() {
        let s = compute(&[dev("eth0", &["203.0.113.5"], false)], false);
        assert_eq!(s.interfaces, "lo");
        assert!(!s.detected);
        assert!(!s.hosts_allow.contains("ALL"));
    }

    /// The device is named rather than the address, because with the opt-in
    /// nothing on it is being refused and there is nothing to keep off the
    /// bind. That is what the opt-in means: SMB is on the internet.
    #[test]
    fn the_opt_in_takes_the_public_address_and_says_all() {
        let s = compute(&[dev("eth0", &["203.0.113.5"], false)], true);
        assert_eq!(s.interfaces, "lo eth0");
        assert!(s.hosts_allow.contains("ALL"));
        assert!(s.detected);
    }

    /// A device carrying both must not be named wholesale: naming `eth0`
    /// there would bind the public address too.
    #[test]
    fn a_mixed_device_falls_back_to_its_private_addresses() {
        let s = compute(&[dev("eth0", &["192.168.1.10", "203.0.113.5"], false)], false);
        assert_eq!(s.interfaces, "lo 192.168.1.10");
        assert!(s.detected);
    }

    #[test]
    fn a_container_namespace_admits_private_space_wholesale() {
        let s = compute(&[dev("eth0", &["172.18.0.3"], true)], false);
        assert_eq!(s.interfaces, "lo eth0");
        for r in ["10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12"] {
            assert!(s.hosts_allow.contains(r), "missing {r} in {}", s.hosts_allow);
        }
    }

    /// Host networking sees real devices, so the DNAT reasoning does not
    /// apply and the list stays as narrow as what was found.
    #[test]
    fn host_networking_does_not_widen() {
        let s = compute(&[dev("ens160", &["192.168.1.10"], false)], false);
        assert!(!s.hosts_allow.contains("10.0.0.0/8"));
    }

    #[test]
    fn ranges_are_not_repeated() {
        let s = compute(
            &[
                dev("eth0", &["192.168.1.10"], false),
                dev("eth1", &["192.168.2.10"], false),
            ],
            false,
        );
        assert_eq!(s.interfaces, "lo eth0 eth1");
        assert_eq!(s.hosts_allow.matches("192.168.0.0/16").count(), 1);
    }

    /// Not an assertion about this machine's networks, which the test suite
    /// cannot know: only that reading them works at all, which is the call
    /// the shell version silently failed.
    #[test]
    #[cfg(target_os = "linux")]
    fn reading_this_machines_interfaces_succeeds() {
        devices().expect("getifaddrs");
    }
}
