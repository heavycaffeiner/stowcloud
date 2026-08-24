package oidc

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The address rule has to hold against the address the socket actually uses.
// A check that resolves a name, validates the result and then hands the name
// to a client makes a second lookup, and a hostile provider answers the two
// differently.

func TestTheBlockedRangesAreRefused(t *testing.T) {
	for _, s := range []string{
		// The named three.
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.1.1",
		// The metadata address every cloud serves credentials from, which is
		// what turns a request-forgery bug into a credential theft.
		"169.254.169.254",
		// The unspecified address, which reaches the local host.
		"0.0.0.0", "::",
		// The private blocks' counterpart in the other family.
		"fc00::1", "fd12:3456::1", "fe80::1",
	} {
		if !IsBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s is not blocked", s)
		}
	}
}

// A private address wearing the other family's encoding is a private address
// no predicate written for its own family would ever see.
func TestAnAddressWearingTheOtherFamilysEncodingIsStillBlocked(t *testing.T) {
	for _, s := range []string{
		// The mapped form.
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"::ffff:192.168.1.1", "::ffff:0.0.0.0",
		// The compatible form, deprecated and still routed by some stacks.
		"::10.0.0.1", "::169.254.169.254",
		// The translation prefix.
		"64:ff9b::10.0.0.1", "64:ff9b::169.254.169.254",
	} {
		if !IsBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s is not blocked, and it reaches a private address", s)
		}
	}
}

func TestAPublicAddressIsAllowed(t *testing.T) {
	for _, s := range []string{
		"8.8.8.8", "1.1.1.1", "203.0.113.5",
		// Just outside each private band.
		"172.15.255.255", "172.32.0.1", "192.169.0.1", "11.0.0.1",
		"2001:db8::1", "2606:4700::1111",
		// The two ranges deliberately not covered, named so the omission is a
		// decision rather than an oversight.
		"100.64.0.1", "198.18.0.1",
	} {
		if IsBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s is blocked and should not be", s)
		}
	}
}

// The guard is handed the address in the shape the dial hook uses.
func TestTheGuardRefusesTheAddressTheSocketWouldUse(t *testing.T) {
	g := Guard{}
	if err := g.Allow("10.0.0.5:443"); !errors.Is(err, ErrAddressBlocked) {
		t.Fatalf("a private address gave %v, want a refusal", err)
	}
	if err := g.Allow("8.8.8.8:443"); err != nil {
		t.Fatalf("a public address gave %v", err)
	}
	if err := g.Allow("[::1]:443"); !errors.Is(err, ErrAddressBlocked) {
		t.Fatalf("loopback gave %v, want a refusal", err)
	}
	// The refusal names the address, which is what an operator can act on.
	err := g.Allow("10.0.0.5:443")
	if !strings.Contains(err.Error(), "10.0.0.5") {
		t.Fatalf("the refusal does not name the address: %v", err)
	}
}

// An address the hook cannot parse is refused rather than allowed: it is about
// to be connected to either way, and a guard that gives up on an input it does
// not understand is not a guard.
func TestAnUnparseableAddressIsRefused(t *testing.T) {
	g := Guard{}
	for _, addr := range []string{"", "not-an-address", "10.0.0.5", "[::1]", "host:port"} {
		if err := g.Allow(addr); !errors.Is(err, ErrAddressBlocked) {
			t.Errorf("Allow(%q) = %v, want a refusal", addr, err)
		}
	}
}

// Self-hosting a provider on the same network is a real deployment, so the
// rule is a default rather than a law.
func TestTheOperatorCanOptIn(t *testing.T) {
	g := Guard{AllowPrivate: true}
	for _, addr := range []string{"10.0.0.5:443", "[::1]:443", "169.254.169.254:80"} {
		if err := g.Allow(addr); err != nil {
			t.Errorf("Allow(%q) with the opt-in = %v", addr, err)
		}
	}
}

// This is the property the whole hook exists for: a name that resolves to a
// private address is refused at connect time, on the address the socket will
// use, rather than on a string that was checked earlier.
func TestANameResolvingToAPrivateAddressIsRefusedAtConnectTime(t *testing.T) {
	// A listener on loopback, which a name resolving to loopback would reach.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("closing the listener: %v", cerr)
		}
	}()

	// "localhost" is the name every system resolves to loopback, so this is a
	// real resolution rather than a stubbed one.
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("splitting the address: %v", err)
	}

	dialer := Guard{}.Dialer()
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
		t.Fatal("the dial succeeded, so a name resolving into private space reached a socket")
	}
	if !errors.Is(err, ErrAddressBlocked) {
		t.Fatalf("the dial failed with %v, want the guard's refusal", err)
	}

	// And with the opt-in the same dial connects, which proves the refusal
	// above came from the guard rather than from the listener being absent.
	allowed := Guard{AllowPrivate: true}.Dialer()
	conn, err = allowed.DialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("the opt-in dial failed: %v", err)
	}
	if cerr := conn.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
}

// A literal address never reaches a resolver, so it is caught on the URL
// instead. Both enforcement points are needed and neither covers the other.
func TestALiteralAddressIsCaughtOnTheURL(t *testing.T) {
	g := Guard{}
	for _, host := range []string{"10.0.0.5", "127.0.0.1", "[::1]", "169.254.169.254"} {
		if err := g.AllowHost(host); !errors.Is(err, ErrAddressBlocked) {
			t.Errorf("AllowHost(%q) = %v, want a refusal", host, err)
		}
	}
	// A hostname is not decided until it is resolved, so it passes here.
	for _, host := range []string{"accounts.example.com", "localhost"} {
		if err := g.AllowHost(host); err != nil {
			t.Errorf("AllowHost(%q) = %v, want it left to the dial", host, err)
		}
	}
}

// A pool that verifies nothing verifies everything: every certificate fails,
// the operator turns verification off to get past it, and the channel carrying
// the client secret is then unauthenticated.
func TestAnEmptyCertificatePoolIsAStartupRefusal(t *testing.T) {
	dir := t.TempDir()
	empty := dir + "/empty.pem"
	if err := writeFile(empty, ""); err != nil {
		t.Fatalf("writing: %v", err)
	}

	_, err := New(Config{
		Issuer:       "https://idp.example.com",
		ClientID:     "sc",
		ClientSecret: secret.New([]byte("s")),
		CACertFile:   empty,
	}, clock.System())
	if !errors.Is(err, ErrNoTrustAnchors) {
		t.Fatalf("an empty pool gave %v, want a startup refusal", err)
	}

	notPEM := dir + "/garbage.pem"
	if err := writeFile(notPEM, "this is not a certificate"); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := New(Config{
		Issuer:       "https://idp.example.com",
		ClientID:     "sc",
		ClientSecret: secret.New([]byte("s")),
		CACertFile:   notPEM,
	}, clock.System()); !errors.Is(err, ErrNoTrustAnchors) {
		t.Fatalf("a file with no certificate gave %v, want a startup refusal", err)
	}
}

// The secrets are exactly the values a stray log field leaks.
func TestTheFlowSecretsRedactThemselves(t *testing.T) {
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	for _, rendered := range []string{
		f.String(),
		f.GoString(),
	} {
		for _, v := range []string{f.State, f.Nonce, f.Binding, f.CodeVerifier} {
			if strings.Contains(rendered, v) {
				t.Fatalf("a secret appears in %q", rendered)
			}
		}
	}
}

// Four distinct values, each of them full width.
func TestTheFlowSecretsAreDistinctAndFullWidth(t *testing.T) {
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	seen := map[string]bool{}
	for _, v := range []string{f.State, f.Nonce, f.Binding, f.CodeVerifier} {
		if seen[v] {
			t.Fatal("two of the four secrets are the same value")
		}
		seen[v] = true
		// 32 bytes of randomness is 43 characters unpadded, which is also the
		// floor the code exchange requires of a verifier.
		if len(v) != 43 {
			t.Errorf("a secret is %d characters, want 43", len(v))
		}
	}
}

// The other challenge method makes the challenge equal to the verifier, so
// anyone who can read the authorization request can redeem the code.
func TestTheChallengeIsNotTheVerifier(t *testing.T) {
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	if f.CodeChallenge() == f.CodeVerifier {
		t.Fatal("the challenge equals the verifier")
	}
	// And it is derived from the verifier, so the same verifier gives the same
	// challenge and a different one does not.
	same := FlowSecrets{CodeVerifier: f.CodeVerifier}
	if same.CodeChallenge() != f.CodeChallenge() {
		t.Fatal("the challenge is not derived from the verifier alone")
	}
	other := FlowSecrets{CodeVerifier: f.CodeVerifier + "x"}
	if other.CodeChallenge() == f.CodeChallenge() {
		t.Fatal("two verifiers give the same challenge")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
