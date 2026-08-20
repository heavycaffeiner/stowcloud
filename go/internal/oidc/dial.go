// Package oidc is link-only single sign-on. The provider authenticates and
// never creates an account, so authority over who has an account stays in the
// local database, which is what makes revocation here total.
//
// The back-channel exchange is the only outbound HTTP this server makes.
package oidc

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// ErrAddressBlocked is the dial guard refusing the address a socket was about
// to connect to.
var ErrAddressBlocked = errors.New("oidc: the resolved address is refused")

// BlockedAddressError names the address that was refused.
type BlockedAddressError struct {
	Addr string
}

func (e *BlockedAddressError) Error() string {
	return fmt.Sprintf("oidc: refusing to connect to %s, which is a private, loopback, link-local or unspecified address", e.Addr)
}

func (e *BlockedAddressError) Is(target error) bool { return target == ErrAddressBlocked }

// Guard decides which addresses this server may connect to.
type Guard struct {
	// AllowPrivate is the operator's opt-in for an identity provider on the
	// same network as this server, which is a real deployment. The rule is a
	// default rather than a law.
	AllowPrivate bool
}

// Allow reports whether a resolved address may be connected to.
//
// It takes the address in the form the dial hook is handed it, host and port
// together, because that is the shape the socket is about to use.
func (g Guard) Allow(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// An address the dial hook cannot parse is refused rather than
		// allowed: it is about to be connected to either way, and a guard that
		// gives up on an input it does not understand is not a guard.
		return &BlockedAddressError{Addr: address}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return &BlockedAddressError{Addr: address}
	}
	if g.AllowPrivate {
		return nil
	}
	if IsBlocked(ip) {
		return &BlockedAddressError{Addr: address}
	}
	return nil
}

// Dialer builds a dialer that applies the guard at connect time.
//
// The hook runs after the address is resolved and before the socket connects,
// with the address that will actually be used. Refusing there closes the
// rebinding gap a resolve-then-check leaves open: a check that resolves a
// hostname, validates the result and then hands the hostname to a client makes
// a second lookup, and a hostile provider can answer the two differently.
//
// There is deliberately no exported way to build a client without this.
func (g Guard) Dialer() *net.Dialer {
	return &net.Dialer{
		Timeout: limits.OIDCConnectTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return g.Allow(address)
		},
	}
}

// IsBlocked reports whether an address is one this server refuses to connect
// to.
//
// Loopback, the private blocks and link-local are the named three. Link-local
// is where every cloud's instance metadata service lives, which is the address
// that turns a request-forgery bug into a credential theft.
//
// Three more are the same hole wearing a different encoding:
//   - the unspecified address, which reaches the local host,
//   - the unique-local block, which is the private blocks' counterpart in the
//     other family,
//   - and an address of one family carried inside the other, which is a
//     private address no predicate over the outer family would ever see.
//
// Not covered, so that the omission is a decision rather than an oversight:
// the carrier-NAT block and the benchmarking block. Neither is
// reachable-but-trusted the way the ones above are, and an operator whose
// provider really is there can say so.
func IsBlocked(ip netip.Addr) bool {
	// An address of one family carried inside the other is judged as the
	// family it actually is. Without this, a private address wearing the other
	// family's encoding passes every check written for its own.
	ip = ip.Unmap()

	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.Is4() {
		o := ip.As4()
		switch {
		case o[0] == 10:
			return true
		case o[0] == 172 && o[1] >= 16 && o[1] <= 31:
			return true
		case o[0] == 192 && o[1] == 168:
			return true
		}
		return false
	}
	if ip.Is6() {
		b := ip.As16()
		// The unique-local block is fc00::/7.
		if b[0]&0xfe == 0xfc {
			return true
		}
		// An address embedding one family's address inside the other's, in the
		// forms Unmap does not cover. Both reach the embedded address, so both
		// are judged on it.
		if v4, ok := embedded4(ip); ok {
			return IsBlocked(v4)
		}
	}
	return false
}

// embedded4 extracts an address of the other family from the two well-known
// embeddings that are not the mapped form.
func embedded4(ip netip.Addr) (netip.Addr, bool) {
	b := ip.As16()
	// The NAT64 well-known prefix, 64:ff9b::/96.
	if b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b {
		allZero := true
		for _, v := range b[4:12] {
			if v != 0 {
				allZero = false
			}
		}
		if allZero {
			return netip.AddrFrom4([4]byte(b[12:16])), true
		}
	}
	// The compatible form, ::a.b.c.d, which is deprecated and still routed by
	// some stacks.
	allZero := true
	for _, v := range b[0:12] {
		if v != 0 {
			allZero = false
		}
	}
	// The first three bytes being zero is the loopback address and the
	// unspecified one, which the checks above already answered.
	if allZero && (b[12] != 0 || b[13] != 0 || b[14] != 0) {
		return netip.AddrFrom4([4]byte(b[12:16])), true
	}
	return netip.Addr{}, false
}
