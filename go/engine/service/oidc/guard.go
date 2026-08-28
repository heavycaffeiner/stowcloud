// Package oidc is the protocol half of single sign-on: discovery, the key
// set, the authorize URL, the code exchange and identity-token verification.
//
// It talks to the internet and holds no state. The durable halves, the flows
// in progress and the identity links, belong to the auth service, and there
// is no import between the two: the layer above hands verified claims from
// here to the account lookup there.
//
// The position is link-only. The provider authenticates and never creates an
// account, so authority over who has one stays in the local database, which
// is what makes a revocation there total.
package oidc

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/netzone"
)

// ErrAddressBlocked is the guard refusing an address a socket was about to
// connect to.
var ErrAddressBlocked = errors.New("the resolved address is refused")

// BlockedAddressError names the address that was refused. A refusal that does
// not say which address is a report nobody can act on.
type BlockedAddressError struct{ Addr string }

func (e *BlockedAddressError) Error() string {
	return fmt.Sprintf(
		"refusing to connect to %s, which is a private, loopback, link-local or unspecified address",
		e.Addr)
}

func (e *BlockedAddressError) Is(target error) bool { return target == ErrAddressBlocked }

// guard decides which addresses this server may connect to.
type guard struct {
	// allowPrivate is the operator's opt-in for a provider on the same network
	// as this server, which is a real deployment. The rule is a default rather
	// than a law.
	allowPrivate bool
}

// allowResolved reports whether a resolved address may be connected to. It
// takes host and port together, which is the shape the socket is about to
// use.
func (g guard) allowResolved(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// An address the hook cannot parse is refused rather than allowed: it
		// is about to be connected to either way, and a guard that gives up on
		// an input it does not understand is not a guard.
		return &BlockedAddressError{Addr: address}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return &BlockedAddressError{Addr: address}
	}
	if g.allowPrivate {
		return nil
	}
	if blocked(ip) {
		return &BlockedAddressError{Addr: address}
	}
	return nil
}

// allowHost applies the rule to a host that may be a literal address.
//
// A hostname passes here and is judged at dial time instead, because it is
// not decided until it is resolved. This catches the literal, which never
// reaches a resolver at all.
func (g guard) allowHost(host string) error {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	if g.allowPrivate || !blocked(ip) {
		return nil
	}
	return &BlockedAddressError{Addr: host}
}

// dialer builds a dialer that applies the guard at connect time.
//
// The hook runs after the address is resolved and before the socket connects,
// with the address that will actually be used. Refusing there closes the
// rebinding gap a resolve-then-check leaves open: a check that resolves a
// name, validates the result and then hands the name to a client makes a
// second lookup, and a hostile provider can answer the two differently.
func (g guard) dialer() *net.Dialer {
	return &net.Dialer{
		Timeout: limits.OIDCConnectTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return g.allowResolved(address)
		},
	}
}

// blocked reports whether an address is one this server refuses to connect
// to.
//
// The private blocks, loopback and link-local come from the shared
// classifier. Link-local is where every cloud's instance metadata service
// lives, which is the address that turns a request-forgery bug into a
// credential theft.
//
// Three more are the same hole wearing a different encoding: the unspecified
// address, which reaches the local host, and the two embeddings of one family
// inside the other, which are a private address no predicate over the outer
// family would ever see.
func blocked(ip netip.Addr) bool {
	// Judged as the family it actually is. Without this, a private address
	// wearing the other family's encoding passes every check written for its
	// own.
	ip = ip.Unmap()

	if ip.IsUnspecified() || ip.IsLinkLocalMulticast() || netzone.IsPrivate(ip) {
		return true
	}
	if v4, ok := embedded4(ip); ok {
		return blocked(v4)
	}
	return false
}

// embedded4 extracts an address of the other family from the two well-known
// embeddings that are not the mapped form Unmap already handles.
func embedded4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()

	// The translation prefix, 64:ff9b::/96.
	if b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && allZero(b[4:12]) {
		return netip.AddrFrom4([4]byte(b[12:16])), true
	}
	// The compatible form, ::a.b.c.d, which is deprecated and still routed by
	// some stacks. The first three bytes being zero is loopback or the
	// unspecified address, which the checks above already answered.
	if allZero(b[0:12]) && (b[12] != 0 || b[13] != 0 || b[14] != 0) {
		return netip.AddrFrom4([4]byte(b[12:16])), true
	}
	return netip.Addr{}, false
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
