// Linux only, for the same reason as the rest of this package.
//go:build linux

// The client address, resolved from the peer and whatever forwarding headers
// the peer was allowed to speak for.
//
// Written as a pure function over parsed values rather than over the request,
// because every interesting case here is a table of addresses and the
// framework's own helpers are exactly what must not be trusted: they answer
// "what did the headers say", and the question is "what may this peer say".
package middleware

import (
	"net/netip"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/netzone"
)

// Unroutable is what a request whose client cannot be resolved is keyed as.
//
// One canonical bucket rather than a fresh key per unresolvable request: the
// rate limiter's map is bounded, and minting keys from unparseable input is
// how an attacker evicts everyone else's bucket. No valid peer maps here.
//
// A function rather than a variable, because a package-level address that a
// caller could assign to is a rate-limit key anyone in the process can move.
func Unroutable() netip.Addr { return netip.AddrFrom4([4]byte{}) }

// ClientAddr resolves the address to treat as the client's.
//
// peer is the transport's own view, which cannot be forged. trusted is the set
// of prefixes whose forwarding claims are honoured. cfConnecting and forwarded
// are the two headers, passed as strings exactly as received.
//
// Returns Unroutable when nothing resolves, which the caller keys as one
// bucket rather than as an address.
func ClientAddr(peer netip.Addr, trusted []netip.Prefix, cfConnecting, forwarded string) netip.Addr {
	if !peer.IsValid() {
		return Unroutable()
	}
	// An untrusted peer's headers are not parsed at all. Parsing them and then
	// discarding the result would still let a malformed value reach a parser
	// on behalf of someone with no standing to send one.
	if !trustedPeer(peer, trusted) {
		return unmap(peer)
	}

	// Cloudflare's header names a single address and is unambiguous, so it
	// wins over the list when the peer is allowed to speak.
	if addr, ok := parseAddr(cfConnecting); ok {
		return addr
	}

	return walkForwarded(peer, trusted, forwarded)
}

// walkForwarded reads X-Forwarded-For right to left, crossing trusted hops
// until it reaches one that is not trusted. That hop is the client.
func walkForwarded(peer netip.Addr, trusted []netip.Prefix, forwarded string) netip.Addr {
	if strings.TrimSpace(forwarded) == "" {
		return unmap(peer)
	}
	hops := strings.Split(forwarded, ",")

	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseAddr(hops[i])
		if !ok {
			// A hop that does not parse ends the walk where it stands. Skipping
			// it to keep going would let one unparseable entry carry trust past
			// itself, which is the whole of the attack: append garbage, then
			// append the address to be believed.
			return unmap(peer)
		}
		if !trustedPeer(addr, trusted) {
			return addr
		}
	}

	// Every hop was a trusted proxy. There is no client in the list, and the
	// leftmost proxy is a proxy rather than the client that reached it.
	return Unroutable()
}

func trustedPeer(addr netip.Addr, trusted []netip.Prefix) bool {
	a := unmap(addr)
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// unmap flattens an IPv4-in-IPv6 address to its IPv4 form, so one client does
// not occupy two rate-limit buckets and a prefix written in one family matches
// the same host arriving in the other.
func unmap(a netip.Addr) netip.Addr {
	if a.Is4In6() {
		return a.Unmap()
	}
	return a
}

// parseAddr reads one address in any form an operator or a proxy writes:
// bare, host-port, bracketed IPv6, or bracketed IPv6 with a port.
//
// The bracketed-without-port form is what net/http itself writes into
// X-Forwarded-For for an IPv6 hop when it has no port to add, and
// ParseAddrPort rejects it for lacking one. Falling through to the peer for
// that shape was silently treating a well-formed hop as unparseable.
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return unmap(addr), true
	}
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		if addr, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return unmap(addr), true
		}
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return unmap(ap.Addr()), true
	}
	return netip.Addr{}, false
}

// ParseTrusted reads the operator's prefix list, returning the ones that parse
// and the spellings that did not.
//
// Both halves are returned rather than failing on the first bad entry: a stored
// list with one malformed prefix should still bring up the others, and the
// caller warns about what it dropped. A bare address is accepted as its own
// single-host prefix, which is how an operator writes one proxy.
func ParseTrusted(specs []string) (prefixes []netip.Prefix, rejected []string) {
	for _, raw := range specs {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			prefixes = append(prefixes, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(unmap(a), unmap(a).BitLen()))
			continue
		}
		rejected = append(rejected, s)
	}
	return prefixes, rejected
}

// IsPrivateClient reports whether a resolved client is on a private network,
// which is what first boot admits before any host is named.
//
// Unroutable is not private: it is the placeholder for a client that could not
// be resolved, and admitting it would open first boot to anyone whose
// forwarding headers failed to parse.
func IsPrivateClient(addr netip.Addr) bool {
	if !addr.IsValid() || addr == Unroutable() {
		return false
	}
	return netzone.IsPrivate(addr)
}
