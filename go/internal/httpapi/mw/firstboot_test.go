package mw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// The first boot's reachability.
//
// Until somebody names this deployment there is no host list to check a
// request against, and refusing every host would leave a server nobody can
// reach to name it. What bounds the opening is the address: the form behind it
// creates the first administrator, so it is offered to the local network and
// not to the internet.

// guarded runs one request through the guard with a peer address.
func guarded(t *testing.T, hosts *HostSet, host, peer string) int {
	t.Helper()
	var reached bool
	h := HostGuard(hosts)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		reached = true
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/setup", nil)
	r.Host = host
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		t.Fatalf("parsing %q: %v", peer, err)
	}
	r = r.WithContext(context.WithValue(r.Context(), clientKey{}, addr))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if reached && w.Code == http.StatusOK {
		return http.StatusOK
	}
	return w.Code
}

func TestBeforeSetupTheLocalNetworkReachesTheServer(t *testing.T) {
	unnamed := NewHostSet(nil)

	// Every way a person on the same network arrives: by the box's LAN
	// address, by a name their router resolves, over a tailnet, and from the
	// machine itself.
	for _, c := range []struct{ host, peer string }{
		{"192.168.1.10", "192.168.1.50"},
		{"nas.local", "10.0.0.7"},
		{"nas.tail1234.ts.net", "100.101.102.103"},
		{"localhost", "127.0.0.1"},
		{"[::1]", "::1"},
	} {
		if got := guarded(t, unnamed, c.host, c.peer); got != http.StatusOK {
			t.Errorf("a fresh deployment refused %s from %s with %d", c.host, c.peer, got)
		}
	}

	// And not from outside it. A deployment published before anyone has
	// configured it is not offering the setup form to the internet.
	for _, peer := range []string{"203.0.113.9", "8.8.8.8", "2001:db8::1"} {
		if got := guarded(t, unnamed, "nas.example.com", peer); got != http.StatusMisdirectedRequest {
			t.Errorf("a fresh deployment answered a public address %s with %d", peer, got)
		}
	}
}

// Once setup has named the deployment the guard is the origin check it has
// always been, and the address stops deciding anything.
func TestAfterSetupTheHostListDecides(t *testing.T) {
	named := NewHostSet([]string{"nas.example.com"})

	if got := guarded(t, named, "nas.example.com", "203.0.113.9"); got != http.StatusOK {
		t.Errorf("a declared host from a public address answered %d", got)
	}
	// The LAN loses the opening it had: the list is what decides now, and a
	// name nobody declared is refused however close the client is.
	if got := guarded(t, named, "192.168.1.10", "192.168.1.50"); got != http.StatusMisdirectedRequest {
		t.Errorf("an undeclared host from the LAN answered %d", got)
	}
}
