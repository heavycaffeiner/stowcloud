// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"net/netip"
	"strings"
	"testing"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

// An untrusted peer's forwarding headers are not read. The peer itself is the
// client, whatever the headers claim.
func TestAnUntrustedPeersHeadersAreIgnored(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "203.0.113.7")

	for _, c := range []struct{ what, cf, xff string }{
		{"a claimed address", "198.51.100.1", ""},
		{"a forwarded list", "", "198.51.100.1, 10.0.0.1"},
		{"both", "198.51.100.1", "198.51.100.2"},
		{"garbage", "not-an-address", "also, not, addresses"},
	} {
		got := ClientAddr(peer, trusted, c.cf, c.xff)
		if got != peer {
			t.Errorf("with %s the client resolved to %v, want the peer %v", c.what, got, peer)
		}
	}
}

// An unparseable hop stops the walk where it stands. Skipping it would let an
// attacker carry trust past their own entry by prepending garbage.
func TestAnUnparseableHopStopsTheWalk(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")

	// Read right to left: 10.0.0.2 is a trusted hop, then the garbage. The
	// address to the left of it must never be reached.
	got := ClientAddr(peer, trusted, "", "198.51.100.9, not-an-address, 10.0.0.2")
	if got == mustAddr(t, "198.51.100.9") {
		t.Fatal("the walk crossed an unparseable hop and believed what was behind it")
	}
	if got != peer {
		t.Errorf("the client resolved to %v, want the peer %v", got, peer)
	}
}

// A list of nothing but trusted proxies yields no client. The leftmost entry
// is a proxy, and promoting it would rate-limit every real client as one host.
func TestAListOfOnlyProxiesYieldsNoClient(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")

	got := ClientAddr(peer, trusted, "", "10.0.0.9, 10.0.0.8, 10.0.0.2")
	if got != Unroutable() {
		t.Errorf("a list of proxies resolved to %v, want the unroutable placeholder", got)
	}
}

// The ordinary case: a trusted proxy naming a real client.
func TestATrustedProxyNamesItsClient(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")
	want := mustAddr(t, "198.51.100.4")

	for _, c := range []struct{ what, cf, xff string }{
		{"the forwarded list", "", "198.51.100.4, 10.0.0.2"},
		{"a single-hop list", "", "198.51.100.4"},
		{"the connecting header", "198.51.100.4", ""},
		{"a host-port hop", "", "198.51.100.4:44321, 10.0.0.2"},
		{"whitespace around hops", "", "  198.51.100.4 ,  10.0.0.2  "},
	} {
		if got := ClientAddr(peer, trusted, c.cf, c.xff); got != want {
			t.Errorf("%s resolved to %v, want %v", c.what, got, want)
		}
	}
}

// A bracketed IPv6 hop with a port, and the v4-in-v6 flattening: one client is
// one bucket, not two.
func TestAddressFormsAndTheMappedFlattening(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")

	if got := ClientAddr(peer, trusted, "", "[2001:db8::5]:443, 10.0.0.2"); got != mustAddr(t, "2001:db8::5") {
		t.Errorf("a bracketed IPv6 hop resolved to %v", got)
	}
	if got := ClientAddr(peer, trusted, "::ffff:198.51.100.4", ""); got != mustAddr(t, "198.51.100.4") {
		t.Errorf("a v4-in-v6 client resolved to %v, want its v4 form", got)
	}
}

// No valid peer maps to the placeholder. If one did, an ordinary client would
// share the bucket reserved for requests that could not be resolved.
func TestNoValidPeerBecomesThePlaceholder(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	for _, s := range []string{"203.0.113.7", "10.0.0.1", "192.168.1.9", "2001:db8::1", "127.0.0.1"} {
		peer := mustAddr(t, s)
		if got := ClientAddr(peer, trusted, "", ""); got == Unroutable() {
			t.Errorf("the valid peer %v resolved to the placeholder", peer)
		}
	}
	// An invalid peer is the one case that does.
	if got := ClientAddr(netip.Addr{}, trusted, "", ""); got != Unroutable() {
		t.Errorf("an invalid peer resolved to %v", got)
	}
}

// A stored list with one bad entry brings up the rest and says what it dropped.
func TestParseTrustedKeepsWhatParsesAndNamesWhatDidNot(t *testing.T) {
	good, bad := ParseTrusted([]string{
		"10.0.0.0/8", " ", "192.168.1.5", "nonsense", "2001:db8::/32", "10.0.0.0/99",
	})
	if len(good) != 3 {
		t.Fatalf("kept %d prefixes: %v", len(good), good)
	}
	if len(bad) != 2 {
		t.Fatalf("rejected %d spellings: %v", len(bad), bad)
	}
	// A bare address becomes its own single-host prefix.
	host := netip.PrefixFrom(mustAddr(t, "192.168.1.5"), 32)
	if good[1] != host {
		t.Errorf("a bare address became %v, want %v", good[1], host)
	}
}

// The placeholder is not private, so a client that could not be resolved does
// not walk into first boot's private-network admission.
func TestThePlaceholderIsNotAPrivateClient(t *testing.T) {
	if IsPrivateClient(Unroutable()) {
		t.Fatal("the unroutable placeholder passed the private-network gate")
	}
	if IsPrivateClient(netip.Addr{}) {
		t.Fatal("an invalid address passed the private-network gate")
	}
	if !IsPrivateClient(mustAddr(t, "192.168.1.9")) {
		t.Fatal("a private address did not pass the gate")
	}
	if IsPrivateClient(mustAddr(t, "203.0.113.7")) {
		t.Fatal("a public address passed the gate")
	}
}

// The chain the server mounts is a valid chain.
func TestTheChainValidates(t *testing.T) {
	if err := ValidateChain(Chain()); err != nil {
		t.Fatalf("the shipped chain: %v", err)
	}
	if len(Chain()) != 11 {
		t.Fatalf("the chain has %d steps", len(Chain()))
	}
	// Every step has a name that is not the fallback.
	for _, s := range Chain() {
		if strings.HasPrefix(s.String(), "Step(") {
			t.Errorf("step %d has no name", uint8(s))
		}
	}
}

// Each ordering rule the document calls load-bearing is refused when broken,
// and the message says what the consequence is rather than only that an order
// changed.
func TestTheOrderingRulesAreEnforced(t *testing.T) {
	swap := func(steps []Step, a, b Step) []Step {
		out := append([]Step(nil), steps...)
		ai, _ := indexOf(out, a)
		bi, _ := indexOf(out, b)
		out[ai], out[bi] = out[bi], out[ai]
		return out
	}

	for _, c := range []struct {
		what  string
		steps []Step
		want  string
	}{
		{
			"ErrorMapper not innermost",
			swap(Chain(), StepErrorMapper, StepAuditSink),
			"escape",
		},
		{
			"RateLimit before TrustedProxy",
			swap(Chain(), StepTrustedProxy, StepRateLimit),
			"not the client",
		},
		{
			"Auth before the boundary",
			swap(Chain(), StepHostAndOriginBoundary, StepAuth),
			"does not serve",
		},
		{
			"CSRF before Auth",
			swap(Chain(), StepAuth, StepCSRF),
			"cookie-authenticated",
		},
	} {
		err := ValidateChain(c.steps)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s reported %q, which does not mention %q", c.what, err, c.want)
		}
	}
}

// A malformed chain reports every problem at once, so an operator is not led
// through them one restart at a time.
func TestAMalformedChainReportsEveryProblem(t *testing.T) {
	err := ValidateChain([]Step{StepUnset, StepAuth, StepAuth, Step(200)})
	if err == nil {
		t.Fatal("a chain with four problems was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"does not name a step", "more than once", "is not a step"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report %q omits %q", msg, want)
		}
	}
	if !strings.Contains(msg, "escape") {
		t.Errorf("the report %q does not mention the missing ErrorMapper", msg)
	}
}

// An empty chain is refused rather than mounted as a server with no middleware.
func TestAnEmptyChainIsRefused(t *testing.T) {
	if err := ValidateChain(nil); err == nil {
		t.Fatal("the empty chain was accepted")
	}
}
