package mw

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// The client address every step below reads comes from one decision in this
// file: the peer-trust check and the X-Forwarded-For walk. It is the
// load-bearing rule of the whole chain because it decides who pays for rate
// limiting and who the audit log names, so the fail-closed properties from
// the proposal are each stated where they are implemented.
type clientKey struct{}

// unknownClientAddr is the address of an unattributable request. 0.0.0.0 is
// unroutable, so it cannot collide with a real client's address, and it is
// the shared rate-limit bucket for everything that has no determinable source.
func unknownClientAddr() netip.Addr { return netip.AddrFrom4([4]byte{0, 0, 0, 0}) }

// TrustedSet is the live trust boundary. The middleware reads it per request
// and the settings surface writes it, so an administrator's patch to the
// trusted-proxy ranges applies to the next request without a restart.
type TrustedSet struct {
	mu       sync.RWMutex
	prefixes []netip.Prefix
}

// NewTrustedSet builds the holder from boot configuration.
func NewTrustedSet(prefixes []netip.Prefix) *TrustedSet {
	return &TrustedSet{prefixes: prefixes}
}

// Get returns the current ranges.
func (t *TrustedSet) Get() []netip.Prefix {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]netip.Prefix(nil), t.prefixes...)
}

// Set replaces the ranges. Only the settings surface calls it, after
// validation and persistence.
func (t *TrustedSet) Set(prefixes []netip.Prefix) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prefixes = prefixes
}

// trusted is the parsed trust boundary. Ranges come from configuration and
// never from a request: an operator who configured 0.0.0.0/0 has already made
// a mistake, and the rules below keep it from becoming two.
type trusted struct {
	prefixes []netip.Prefix
}

func (t *trusted) isTrusted(a netip.Addr) bool {
	for _, p := range t.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// TrustedProxy is step 2. It decides who this request is from, exactly once,
// and records the result in the context. The rule, restated as the testable
// statements the proposal makes:
//
//   - No peer at all means the placeholder, and it is untrusted no matter what
//     the configuration says.
//   - A peer outside every trusted range means the peer. Forwarding headers
//     from an untrusted source are attacker-supplied strings and are discarded
//     without being parsed, so a direct attacker cannot name their own address.
//   - A peer inside a trusted range is a proxy, and the client is read from
//     CF-Connecting-IP (the edge sets it, never appends, so there is no list)
//     or from X-Forwarded-For, or the peer itself.
func TrustedProxy(set *TrustedSet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addr := resolveClient(&trusted{prefixes: set.Get()}, r.RemoteAddr,
				r.Header.Get("CF-Connecting-IP"), r.Header.Get("X-Forwarded-For"))
			ctx := context.WithValue(r.Context(), clientKey{}, addr)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientFrom reads the address TrustedProxy resolved. It is netip.Addr so the
// rate limiter keys on a canonical form rather than a string a client wrote.
func ClientFrom(ctx context.Context) netip.Addr {
	if a, ok := ctx.Value(clientKey{}).(netip.Addr); ok {
		return a
	}
	return unknownClientAddr()
}

// ResolvedClient is ClientFrom with the absence reported rather than replaced
// by the placeholder.
//
// A mount that runs both inside this chain and on its own needs to tell "the
// chain decided 0.0.0.0" from "no chain ran", because in the second case it
// has to decide for itself and the placeholder is not an answer it can use.
func ResolvedClient(ctx context.Context) (netip.Addr, bool) {
	a, ok := ctx.Value(clientKey{}).(netip.Addr)
	return a, ok
}

// ResolveClient is the peer-trust rule as a function, for a mount that runs
// outside this chain. The rule is stated once, in resolveClient below, so the
// two entrances cannot disagree about who a request is from.
func ResolveClient(prefixes []netip.Prefix, peer, cf, xff string) netip.Addr {
	return resolveClient(&trusted{prefixes: prefixes}, peer, cf, xff)
}

func resolveClient(t *trusted, peer, cf, xff string) netip.Addr {
	peerAddr := parseHop(peer)
	if !peerAddr.IsValid() {
		// No peer at all. 0.0.0.0 is its own rate-limit bucket, shared with
		// nothing, and is never treated as a trusted proxy.
		return unknownClientAddr()
	}
	if !t.isTrusted(peerAddr) {
		// The peer is a client, not a proxy. Everything in the forwarding
		// headers is that client's own claim and is discarded unparsed.
		return peerAddr
	}
	if ip := parseHop(cf); ip.IsValid() {
		return ip
	}
	if ip := forwardedFor(t, xff); ip.IsValid() {
		return ip
	}
	return peerAddr
}

// forwardedFor walks X-Forwarded-For right to left and stops at the first hop
// that is not itself a trusted proxy, yielding the closest address a trusted
// party vouched for. The list is append-only and the leftmost entry is
// whatever the original client sent, attacker-controlled always.
//
// Two fail-closed rules:
//
//   - An unparseable hop aborts the walk and the caller uses the peer. We
//     cannot tell where the trusted chain ends if we cannot read it, and
//     skipping the garbage would hand the choice to whoever inserted it.
//   - A list consisting entirely of trusted proxies yields nothing: there is
//     no client address in it, only infrastructure.
func forwardedFor(t *trusted, raw string) netip.Addr {
	if raw == "" {
		return netip.Addr{}
	}
	hops := strings.Split(raw, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := parseHop(strings.TrimSpace(hops[i]))
		if !ip.IsValid() {
			return netip.Addr{}
		}
		if !t.isTrusted(ip) {
			return ip
		}
	}
	return netip.Addr{}
}

// parseHop accepts the four shapes proxies actually emit:
//
//	1.2.3.4        [2001:db8::1]
//	1.2.3.4:51234  [2001:db8::1]:443
//
// The attempts mirror the reference implementation: a bare address first, then
// a host and port pair, then a bare bracketed address. Anything else is
// invalid, which aborts the X-Forwarded-For walk.
func parseHop(s string) netip.Addr {
	if a, err := netip.ParseAddr(s); err == nil {
		return a
	}
	if host, port, err := net.SplitHostPort(s); err == nil && validPort(port) {
		if a, err := netip.ParseAddr(host); err == nil {
			return a
		}
	}
	if a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")); err == nil {
		return a
	}
	return netip.Addr{}
}

func validPort(s string) bool {
	if len(s) == 0 || len(s) > 5 {
		return false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n <= 65535
}
