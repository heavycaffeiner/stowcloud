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

// A forwarded chain whose leftmost entry is a private address is not special:
// it is just the client the walk finds, the same as any other unroutable
// public address would be. Private only matters to IsPrivateClient, which
// runs later and separately.
func TestALeftmostPrivateAddressResolvesAsTheClient(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")
	want := mustAddr(t, "192.168.1.50")

	got := ClientAddr(peer, trusted, "", "192.168.1.50, 10.0.0.5")
	if got != want {
		t.Errorf("a chain starting with a private address resolved to %v, want %v", got, want)
	}
}

// A chain longer than the trusted list still walks correctly: each hop is
// checked against the list on its own, so the list's length bounds nothing
// about how many hops the header may carry.
func TestAChainLongerThanTheTrustedListStillWalks(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/9"), mustPrefix(t, "172.16.0.0/12")}
	peer := mustAddr(t, "10.0.0.1")
	want := mustAddr(t, "198.51.100.9")

	got := ClientAddr(peer, trusted, "", "198.51.100.9, 10.0.0.2, 10.0.0.3, 172.16.0.4")
	if got != want {
		t.Errorf("a chain with more hops than trusted entries resolved to %v, want %v", got, want)
	}
}

// A hop naming a port is read for its address, the port discarded: the
// resolver answers "which host", never "which socket".
func TestAHopNamingAPortIsReadForItsAddress(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")
	want := mustAddr(t, "198.51.100.4")

	if got := ClientAddr(peer, trusted, "", "198.51.100.4:44321, 10.0.0.2"); got != want {
		t.Errorf("a hop with a port resolved to %v, want %v", got, want)
	}
}

// A bracketed IPv6 hop is read whether or not it carries a port. Without one
// is what net/http itself writes for an IPv6 hop with nothing to append, and
// ParseAddrPort alone rejects that shape, which used to fall through to the
// peer as if the hop were unparseable.
func TestABracketedIPv6HopIsReadWithOrWithoutAPort(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")
	want := mustAddr(t, "2001:db8::5")

	if got := ClientAddr(peer, trusted, "", "[2001:db8::5], 10.0.0.2"); got != want {
		t.Errorf("a bracketed hop with no port resolved to %v, want %v", got, want)
	}
	if got := ClientAddr(peer, trusted, "", "[2001:db8::5]:443, 10.0.0.2"); got != want {
		t.Errorf("a bracketed hop with a port resolved to %v, want %v", got, want)
	}
}

// An IPv4-mapped IPv6 hop and its plain IPv4 spelling must land in the same
// rate-limit bucket, whichever header or family carried it: two spellings of
// one host, never two buckets.
func TestAnIPv4MappedHopSharesItsBucketWithPlainIPv4(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")
	want := mustAddr(t, "198.51.100.4")

	if got := ClientAddr(peer, trusted, "", "::ffff:198.51.100.4, 10.0.0.2"); got != want {
		t.Errorf("a mapped hop in the forwarded list resolved to %v, want %v", got, want)
	}
	if got := ClientAddr(peer, trusted, "::ffff:198.51.100.4", ""); got != want {
		t.Errorf("a mapped connecting-IP header resolved to %v, want %v", got, want)
	}
	if got := ClientAddr(peer, trusted, "", "[::ffff:198.51.100.4]:443, 10.0.0.2"); got != want {
		t.Errorf("a bracketed mapped hop with a port resolved to %v, want %v", got, want)
	}
}

// An empty forwarded header, from a trusted peer, resolves to the peer: there
// is nothing to walk, so the peer is the closest thing to a client.
func TestAnEmptyForwardedHeaderResolvesToThePeer(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")

	if got := ClientAddr(peer, trusted, "", ""); got != peer {
		t.Errorf("an empty header resolved to %v, want the peer %v", got, peer)
	}
}

// A header with only separators has no hop to parse, and none of them may
// silently become the peer's own address treated as a claimed hop: the walk
// finds nothing and falls back to the peer.
func TestAHeaderOfOnlySeparatorsFallsBackToThePeer(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "10.0.0.1")

	for _, forwarded := range []string{",,,", " , , "} {
		if got := ClientAddr(peer, trusted, "", forwarded); got != peer {
			t.Errorf("forwarded %q resolved to %v, want the peer %v", forwarded, got, peer)
		}
	}
}

// A peer that is itself untrusted is not granted its claimed address just
// because that address happens to be on the trusted list. Trust runs from the
// peer outward hop by hop; it is never read backward out of the header.
func TestAnUntrustedPeerCannotClaimATrustedAddress(t *testing.T) {
	trusted := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	peer := mustAddr(t, "203.0.113.7")

	if got := ClientAddr(peer, trusted, "", "198.51.100.1, 10.0.0.5"); got != peer {
		t.Errorf("an untrusted peer's forwarded list resolved to %v, want the peer %v", got, peer)
	}
	if got := ClientAddr(peer, trusted, "10.0.0.5", ""); got != peer {
		t.Errorf("an untrusted peer's connecting-IP header resolved to %v, want the peer %v", got, peer)
	}
}

// The shape a container deployment actually has, both ways round.
//
// A published port, a sidecar proxy or a tunnel daemon means every request
// arrives from an address on the container network, which the operator cannot
// know in advance and which carries no information about who is calling. With
// no list configured that peer is trusted, because it is not on the internet:
// resolving the forwarded address is what records the visitor rather than one
// gateway address for everybody, and it is what keeps a screen gated on a
// private client from admitting the whole internet through such a proxy.
//
// A configured list replaces that fallback entirely, so an operator who names
// their proxies decides on their own terms.
func TestAPrivateGatewayNamesItsVisitorAndAPublicPeerCannot(t *testing.T) {
	gateway := mustAddr(t, "172.19.0.1")
	visitor := "203.0.113.9"
	want := mustAddr(t, visitor)

	// Nothing configured: the gateway is private, so its headers are believed.
	if got := ClientAddr(gateway, nil, "", visitor); got != want {
		t.Errorf("a private gateway's forwarded header resolved to %v, want %v", got, want)
	}
	if got := ClientAddr(gateway, nil, visitor, ""); got != want {
		t.Errorf("a private gateway's connecting-IP header resolved to %v, want %v", got, want)
	}

	// A peer arriving straight off the internet is never trusted this way, so
	// nobody can name their own address by sending a header.
	public := mustAddr(t, "198.51.100.7")
	if got := ClientAddr(public, nil, "", visitor); got != public {
		t.Errorf("a public peer's forwarded header resolved to %v, want the peer %v", got, public)
	}
	if got := ClientAddr(public, nil, visitor, ""); got != public {
		t.Errorf("a public peer's connecting-IP header resolved to %v, want the peer %v", got, public)
	}

	// A local proxy forwarding for a local client resolves to that client
	// rather than to no client at all: the fallback crosses the peer alone.
	lanClient := mustAddr(t, "192.168.1.50")
	if got := ClientAddr(gateway, nil, "", lanClient.String()); got != lanClient {
		t.Errorf("a local client behind a local proxy resolved to %v, want %v", got, lanClient)
	}

	// The gateway's own network trusted: the header is believed and the real
	// caller is recovered.
	trusted := []netip.Prefix{mustPrefix(t, "172.19.0.0/16")}
	if got := ClientAddr(gateway, trusted, "", visitor); got != want {
		t.Errorf("behind a trusted gateway the client is %v, want %v", got, want)
	}
	if got := ClientAddr(gateway, trusted, visitor, ""); got != want {
		t.Errorf("behind a trusted gateway the connecting-IP header resolved to %v, want %v", got, want)
	}

	// A single-host entry is the other spelling an operator writes, and it has
	// to work the same: the loader accepts a bare address as a /32.
	single := []netip.Prefix{mustPrefix(t, "172.19.0.1/32")}
	if got := ClientAddr(gateway, single, "", visitor); got != want {
		t.Errorf("a bare gateway address resolved the client to %v, want %v", got, want)
	}

	// A configured list that does not contain the peer is not widened by the
	// private fallback: the operator's list is the whole rule.
	elsewhere := []netip.Prefix{mustPrefix(t, "10.0.0.0/8")}
	if got := ClientAddr(gateway, elsewhere, "", visitor); got != gateway {
		t.Errorf("a peer outside the configured list resolved to %v, want the peer %v", got, gateway)
	}
}
