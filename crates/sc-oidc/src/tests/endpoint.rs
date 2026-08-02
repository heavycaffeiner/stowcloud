//! The scheme and address rules, which are the whole of what replaced the
//! draft's same-host requirement (§4.3.4, correction 4).

use crate::endpoint::{check_endpoint_url, is_blocked, EndpointError};
use std::net::IpAddr;

fn ip(s: &str) -> IpAddr {
    s.parse().expect("test address literal")
}

#[test]
fn endpoint_requires_https() {
    assert_eq!(
        check_endpoint_url("http://idp.example/token", false).unwrap_err(),
        EndpointError::InsecureScheme
    );
    // Not "any scheme with a host is fine": `file:` and friends have to lose
    // to the same check, since a discovery document picks these strings.
    assert_eq!(
        check_endpoint_url("file:///etc/passwd", false).unwrap_err(),
        EndpointError::InsecureScheme
    );
    assert!(check_endpoint_url("https://idp.example/token", false).is_ok());
}

#[test]
fn endpoint_rejects_private_ip_literals() {
    for raw in [
        "https://127.0.0.1/token",
        "https://10.1.2.3/token",
        "https://192.168.0.9/token",
        "https://172.16.5.5/token",
        // The instance metadata service, which is the address this rule is
        // really written for.
        "https://169.254.169.254/latest/meta-data/",
        "https://[::1]/token",
        "https://[fd00::1]/token",
        // A private v4 address wearing a v6 costume.
        "https://[::ffff:10.0.0.1]/token",
    ] {
        assert_eq!(
            check_endpoint_url(raw, false).unwrap_err(),
            EndpointError::PrivateAddress,
            "{raw} should have been refused"
        );
        assert!(
            check_endpoint_url(raw, true).is_ok(),
            "{raw} should be allowed when allow_private_endpoints is set"
        );
    }
}

#[test]
fn endpoint_allows_public_literals_and_names() {
    for raw in [
        "https://93.184.216.34/token",
        "https://[2606:2800:220:1:248:1893:25c8:1946]/token",
        // A hostname is not judged here at all; the resolver guard decides.
        "https://accounts.google.com/o/oauth2/v2/auth",
        "https://internal-idp.corp/token",
    ] {
        assert!(check_endpoint_url(raw, false).is_ok(), "{raw}");
    }
}

#[test]
fn blocked_ranges_are_the_documented_ones() {
    assert!(is_blocked(&ip("0.0.0.0")));
    assert!(is_blocked(&ip("::")));
    assert!(is_blocked(&ip("fe80::1")));
    assert!(!is_blocked(&ip("8.8.8.8")));
    // Named in the doc comment as deliberately out of scope, so it is
    // asserted rather than left to be rediscovered as a surprise.
    assert!(!is_blocked(&ip("100.64.0.1")));
}
