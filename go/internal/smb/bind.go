// Package smb generates the Samba sidecar's configuration. It does not
// implement SMB: Samba stays Samba, and this decides what Samba is told.
//
// The generated file is the enforcement point for the bind rule. The sidecar
// shares the host's network stack, so an interface list written too broadly
// reaches everything on that stack, Docker bridges included, and any container
// on the machine can then reach the share. A rule that lives in documentation
// is advice; this one lives in the output.
package smb

import (
	"net/netip"
	"strconv"
	"strings"
)

// What counts as an internal network.
//
// Private means the RFC 1918 blocks, loopback, link-local and the carrier-NAT
// block, in both families. Anything else is public.
//
// The carrier-NAT block is here because that is where every Tailscale address
// lives, and a tailnet is a private network by construction. The HTTP side
// makes the same call, and without it the two halves disagreed: a deployment
// reached over Tailscale served the web app while the share refused every
// client from the same tailnet. The cost is that a host genuinely behind a
// carrier NAT counts as a LAN, which is the trade the HTTP side already takes.

// IsPrivate reports whether an address is one this project treats as a LAN.
func IsPrivate(ip netip.Addr) bool {
	return EnclosingPrivateRange(ip) != ""
}

// EnclosingPrivateRange is the well-known private block containing ip, or the
// empty string when it is globally routable.
//
// The enclosing block rather than the on-link prefix, for two reasons that
// both turned up in practice: an internal network is routinely several subnets
// behind a router, and a tailnet address carries a single-host prefix whose own
// subnet admits nobody while the carrier-NAT block admits the whole tailnet.
func EnclosingPrivateRange(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is4() {
		o := ip.As4()
		switch {
		case o[0] == 10:
			return "10.0.0.0/8"
		case o[0] == 172 && o[1] >= 16 && o[1] <= 31:
			return "172.16.0.0/12"
		case o[0] == 192 && o[1] == 168:
			return "192.168.0.0/16"
		case o[0] == 127:
			return "127.0.0.0/8"
		case o[0] == 169 && o[1] == 254:
			return "169.254.0.0/16"
		// The carrier-NAT block is 100.64.0.0/10, whose top ten bits are
		// fixed, so the second octet runs 64 through 127.
		case o[0] == 100 && o[1] >= 64 && o[1] <= 127:
			return "100.64.0.0/10"
		}
		return ""
	}
	if !ip.Is6() {
		return ""
	}
	if ip.IsLoopback() {
		return "::1/128"
	}
	b := ip.As16()
	// The unique-local block is fc00::/7, whose top seven bits are fixed, so
	// the first byte is 0xfc or 0xfd.
	if b[0] == 0xfc || b[0] == 0xfd {
		return "fc00::/7"
	}
	// The link-local block is fe80::/10, whose top ten bits are fixed.
	if b[0] == 0xfe && b[1]&0xc0 == 0x80 {
		return "fe80::/10"
	}
	return ""
}

// The private blocks written into the admission list when the operator pinned
// the interfaces, and the list the sidecar falls back to inside a container
// namespace, where the only subnet it can see is the bridge's and LAN clients
// arrive through address translation wearing their own addresses.
//
// These are functions rather than package variables because a slice at package
// scope is mutable state any caller can reach into, and this one decides who
// may reach a share.
func privateCIDRs() []string {
	return []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"100.64.0.0/10",
		"169.254.0.0/16",
		"fc00::/7",
		"fe80::/10",
		"::1/128",
	}
}

// ParseAddrSpec reads a bare address or an address with a prefix length, and
// returns the address part.
//
// An interface name is a valid entry to Samba and an unprovable one here: this
// process runs in a different network namespace and cannot tell whether a name
// there is the LAN or the uplink, so an unprovable entry is refused rather than
// passed through.
func ParseAddrSpec(spec string) (netip.Addr, error) {
	addr, prefix, hasPrefix := strings.Cut(spec, "/")
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return netip.Addr{}, &BindError{
			Value:  spec,
			Reason: "not an IP address or CIDR block",
		}
	}
	if hasPrefix {
		bits, err := strconv.Atoi(prefix)
		if err != nil {
			return netip.Addr{}, &BindError{
				Value:  spec,
				Reason: "prefix length is not a number",
			}
		}
		max := 128
		if ip.Is4() {
			max = 32
		}
		if bits < 0 || bits > max {
			return netip.Addr{}, &BindError{
				Value:  spec,
				Reason: "prefix length is too long for the address family",
			}
		}
	}
	return ip, nil
}
