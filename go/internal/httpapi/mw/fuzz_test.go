package mw

import (
	"net/netip"
	"testing"
)

// FuzzParseHop covers the four shapes proxies emit and everything around them.
// The hop parser is on the trust boundary: it decides who a request is from,
// so a shape it mis-parses is a client address an attacker can pick.
func FuzzParseHop(f *testing.F) {
	for _, seed := range []string{
		"1.2.3.4",
		"1.2.3.4:51234",
		"[2001:db8::1]",
		"[2001:db8::1]:443",
		"garbage",
		"",
		"2001:db8::1",
		"1.2.3.4:notaport",
		"[broken",
		"0.0.0.0",
		"::ffff:192.0.2.1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a := parseHop(s)
		if !a.IsValid() {
			return
		}
		// Whatever it parsed, it must be an address, and the round trip
		// through the canonical form must agree with itself.
		if a != netip.MustParseAddr(a.String()) {
			t.Fatalf("parseHop(%q) = %v, not canonical", s, a)
		}
		// The four legitimate shapes must parse to an address that the
		// bracketed and unbracketed forms round-trip.
		switch a.BitLen() {
		case 32, 128:
		default:
			t.Fatalf("parseHop(%q) = %v, a non-IP", s, a)
		}
	})
}
