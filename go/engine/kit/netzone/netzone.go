// Package netzone classifies IP addresses as private or public and parses
// the address spellings an operator supplies for network configuration.
//
// Private means the RFC 1918 blocks, loopback, link-local, and the
// carrier-NAT block, in both address families. The carrier-NAT block
// (100.64.0.0/10) is included on purpose: it is where Tailscale addresses
// live, and a tailnet is a private network by construction. Excluding it
// would make a deployment reached over Tailscale disagree with itself about
// which of its own clients are on a LAN.
package netzone

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// IsPrivate reports whether ip is one this project treats as a LAN address.
func IsPrivate(ip netip.Addr) bool {
	return EnclosingPrivateRange(ip) != ""
}

// EnclosingPrivateRange returns the well-known private block containing ip,
// or the empty string when ip is globally routable.
//
// This names the enclosing block rather than the address's own on-link
// prefix: an internal network is routinely several subnets behind a router,
// and a carrier-NAT address carries a single-host prefix whose own subnet
// admits nobody while the enclosing /10 admits the whole tailnet.
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
		// The carrier-NAT block is 100.64.0.0/10: the top ten bits are
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
	// The unique-local block is fc00::/7: the top seven bits are fixed, so
	// the first byte is 0xfc or 0xfd.
	if b[0] == 0xfc || b[0] == 0xfd {
		return "fc00::/7"
	}
	// The link-local block is fe80::/10: the top ten bits are fixed.
	if b[0] == 0xfe && b[1]&0xc0 == 0x80 {
		return "fe80::/10"
	}
	return ""
}

// PrivateCIDRs returns the private blocks this package recognizes, as a
// fresh slice on every call. It is a function rather than a package
// variable because a slice at package scope is mutable state any caller
// could reach into, and this list decides who may reach a share.
func PrivateCIDRs() []string {
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

// ErrInvalidAddrSpec is the sentinel ParseAddrSpec refuses with.
var ErrInvalidAddrSpec = errors.New("invalid address spec")

// AddrSpecError names the spec that was refused and why. A refusal without
// the offending value is a bug report nobody can act on.
type AddrSpecError struct {
	Spec   string
	Reason string
}

func (e *AddrSpecError) Error() string {
	return fmt.Sprintf("%q: %s", e.Spec, e.Reason)
}

// Is reports ErrInvalidAddrSpec so a caller can match the sentinel and still
// read the fields off the concrete error.
func (e *AddrSpecError) Is(target error) bool { return target == ErrInvalidAddrSpec }

// ParseAddrSpec reads a bare address or an address with a prefix length
// ("192.168.1.1" or "192.168.1.0/24") and returns the address part.
//
// An interface name is a spelling this package deliberately does not accept:
// resolving one to an address depends on the network namespace the caller
// runs in, which this package has no way to know, so an unprovable entry is
// refused rather than passed through.
func ParseAddrSpec(spec string) (netip.Addr, error) {
	addr, prefix, hasPrefix := strings.Cut(spec, "/")
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return netip.Addr{}, &AddrSpecError{
			Spec:   spec,
			Reason: "not an IP address or CIDR block",
		}
	}
	if hasPrefix {
		bits, err := strconv.Atoi(prefix)
		if err != nil {
			return netip.Addr{}, &AddrSpecError{
				Spec:   spec,
				Reason: "prefix length is not a number",
			}
		}
		max := 128
		if ip.Is4() {
			max = 32
		}
		if bits < 0 || bits > max {
			return netip.Addr{}, &AddrSpecError{
				Spec:   spec,
				Reason: "prefix length is too long for the address family",
			}
		}
	}
	return ip, nil
}
