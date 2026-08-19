package mw

import (
	"net/netip"
	"testing"
)

func prefix(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

func TestUntrustedPeerCannotNameItself(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("198.51.100.0/24")}}
	got := resolveClient(tw, "9.9.9.9:51234", "7.7.7.7", "8.8.8.8")
	// The peer is a client, not a proxy; every forwarding header is that
	// client's own claim and is discarded unparsed.
	want := netip.MustParseAddr("9.9.9.9")
	if got != want {
		t.Fatalf("client = %v, want %v", got, want)
	}
}

func TestTrustedPeerReadsTheConnectingIP(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("198.51.100.0/24")}}
	got := resolveClient(tw, "198.51.100.7:443", "203.0.113.9", "8.8.8.8")
	if got != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("client = %v, want the connecting ip", got)
	}
}

func TestGarbageConnectingIPFallsBack(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("198.51.100.0/24")}}
	got := resolveClient(tw, "198.51.100.7:443", "not an ip", "8.8.8.8")
	if got != netip.MustParseAddr("8.8.8.8") {
		t.Fatalf("client = %v, want the forwarded-for hop", got)
	}
}

func TestForwardedForIsReadRightToLeft(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("198.51.100.7/32")}}
	// The proxy appended 198.51.100.7; the client wrote 1.1.1.1. Walking
	// right to left stops at the first untrusted hop, which is the client.
	got := resolveClient(tw, "198.51.100.7:443", "", "1.1.1.1, 198.51.100.7")
	if got != netip.MustParseAddr("1.1.1.1") {
		t.Fatalf("client = %v, want 1.1.1.1", got)
	}
}

func TestAllTrustedForwardedForYieldsThePeer(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("198.51.100.0/24")}}
	got := resolveClient(tw, "198.51.100.7:443", "", "198.51.100.1, 198.51.100.2")
	// There is no client in the list, only infrastructure; the fallback is
	// the peer, which is the proxy that spoke to us.
	if got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("client = %v, want the peer", got)
	}
}

func TestUnparseableHopAbortsTheWalk(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("198.51.100.0/24")}}
	// The garbage is attacker-controlled; skipping it would hand the choice
	// of client address to whoever inserted it, so the peer stands.
	got := resolveClient(tw, "198.51.100.7:443", "", "1.1.1.1, garbage, 198.51.100.7")
	if got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("client = %v, want the peer", got)
	}
}

func TestNoPeerIsTheSharedBucket(t *testing.T) {
	tw := &trusted{prefixes: []netip.Prefix{prefix("0.0.0.0/0")}}
	got := resolveClient(tw, "", "7.7.7.7", "8.8.8.8")
	// No peer at all means the placeholder, and it is untrusted no matter
	// what the configuration says: with 0.0.0.0/0 trusted, every header hop
	// parses as trusted, so without this rule the request would pick its own
	// address out of the headers.
	if got != unknownClient {
		t.Fatalf("client = %v, want 0.0.0.0", got)
	}
}

func TestParseHopFourShapes(t *testing.T) {
	cases := map[string]netip.Addr{
		"1.2.3.4":           netip.MustParseAddr("1.2.3.4"),
		"1.2.3.4:51234":     netip.MustParseAddr("1.2.3.4"),
		"[2001:db8::1]":     netip.MustParseAddr("2001:db8::1"),
		"[2001:db8::1]:443": netip.MustParseAddr("2001:db8::1"),
		"2001:db8::1":       netip.MustParseAddr("2001:db8::1"),
	}
	for in, want := range cases {
		if got := parseHop(in); got != want {
			t.Errorf("parseHop(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseHopRefusesGarbage(t *testing.T) {
	for _, in := range []string{"", "garbage", "1.2.3.4:notaport", "[broken", "1.2.3.4:99999", "1.2.3.4:5:6", "[2001:db8::1]garbage", "1.2.3.4:"} {
		if got := parseHop(in); got.IsValid() {
			t.Errorf("parseHop(%q) = %v, want invalid", in, got)
		}
	}
}
