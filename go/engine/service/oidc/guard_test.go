package oidc

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

// Every range this server refuses, including the encodings that carry an
// address of one family inside the other. Link-local is where every cloud's
// instance metadata service lives, which is the address that turns a
// request-forgery bug into a credential theft.
func TestTheGuardRefusesEveryPrivateEncoding(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1",
		"127.255.255.254",
		"10.0.0.1",
		"172.16.5.5",
		"172.31.255.255",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"0.0.0.0",
		"::1",
		"::",
		"fe80::1",
		"fc00::1",
		"fd12:3456::1",
		// The mapped form of a private address.
		"::ffff:169.254.169.254",
		"::ffff:10.0.0.1",
		// The translation prefix and the deprecated compatible form.
		"64:ff9b::169.254.169.254",
		"::169.254.169.254",
	} {
		ip, err := netip.ParseAddr(addr)
		if err != nil {
			t.Fatalf("parsing %q: %v", addr, err)
		}
		if !blocked(ip) {
			t.Fatalf("%s was allowed", addr)
		}
	}
}

func TestTheGuardAllowsPublicAddresses(t *testing.T) {
	for _, addr := range []string{
		"1.1.1.1",
		"93.184.216.34",
		"2606:4700:4700::1111",
		"::ffff:1.1.1.1",
		"64:ff9b::1.1.1.1",
	} {
		ip, err := netip.ParseAddr(addr)
		if err != nil {
			t.Fatalf("parsing %q: %v", addr, err)
		}
		if blocked(ip) {
			t.Fatalf("%s was refused", addr)
		}
	}
}

// A deployment whose provider really is on the same network says so, and that
// relaxes the classification without removing the second check.
func TestTheOptInAllowsAPrivateProvider(t *testing.T) {
	strict := guard{}
	if err := strict.allowResolved("10.0.0.1:443"); !errors.Is(err, ErrAddressBlocked) {
		t.Fatalf("the strict guard returned %v", err)
	}
	relaxed := guard{allowPrivate: true}
	if err := relaxed.allowResolved("10.0.0.1:443"); err != nil {
		t.Fatalf("the relaxed guard refused: %v", err)
	}
}

// A guard that gives up on an input it does not understand is not a guard:
// the socket is about to connect either way.
func TestAnUnparseableAddressIsRefused(t *testing.T) {
	g := guard{}
	for _, address := range []string{"", "not-an-address", "example.test:443", "[::1"} {
		if err := g.allowResolved(address); !errors.Is(err, ErrAddressBlocked) {
			t.Fatalf("allowResolved(%q) returned %v", address, err)
		}
	}
}

// A hostname passes the literal check and is judged when it resolves; a
// literal address is caught here, because it never reaches a resolver.
func TestTheLiteralCheckCatchesWhatTheResolverNeverSees(t *testing.T) {
	g := guard{}
	if err := g.allowHost("metadata.example.test"); err != nil {
		t.Fatalf("a hostname was refused at parse time: %v", err)
	}
	if err := g.allowHost("169.254.169.254"); !errors.Is(err, ErrAddressBlocked) {
		t.Fatalf("a literal metadata address returned %v", err)
	}
}

// The gap between resolving a name and connecting to it is a window an
// attacker's own DNS controls, so the guard runs again at the socket.
func TestTheGuardRefusesAtTheSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("closing the listener: %v", cerr)
		}
	})

	conn, err := guard{}.dialer().Dial("tcp", ln.Addr().String())
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing the connection: %v", cerr)
		}
		t.Fatal("the dialer connected to a loopback listener")
	}
	if !errors.Is(err, ErrAddressBlocked) {
		t.Fatalf("the dial failed with %v, want the guard's own refusal", err)
	}
}

// The refusal names the address, because one that does not is a report
// nobody can act on.
func TestTheRefusalNamesTheAddress(t *testing.T) {
	err := guard{}.allowResolved("169.254.169.254:80")
	var blockedErr *BlockedAddressError
	if !errors.As(err, &blockedErr) {
		t.Fatalf("the refusal is %v, want a typed one", err)
	}
	if blockedErr.Addr != "169.254.169.254:80" {
		t.Fatalf("the refusal names %q", blockedErr.Addr)
	}
}
