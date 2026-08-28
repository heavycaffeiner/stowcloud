// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"net/netip"
	"strings"
	"testing"
)

func namedHosts() Hosts {
	return Hosts{App: []string{"app.example.test"}, Content: []string{"files.example.test"}}
}

func privateClient(t *testing.T) netip.Addr { return mustAddr(t, "192.168.1.9") }
func publicClient(t *testing.T) netip.Addr  { return mustAddr(t, "203.0.113.7") }

// A named deployment matches its host case-insensitively with the port
// ignored, and refuses every other name.
func TestANamedDeploymentAdmitsOnlyItsOwnHosts(t *testing.T) {
	h := namedHosts()
	for _, c := range []struct {
		host string
		want Origin
	}{
		{"app.example.test", OriginApp},
		{"APP.example.test", OriginApp},
		{"app.example.test:8443", OriginApp},
		{"files.example.test", OriginContent},
		{"files.example.test:443", OriginContent},
		{"evil.example.test", OriginNone},
		{"", OriginNone},
	} {
		got := Decide(h, BoundaryRequest{Host: c.host, Method: "GET", Client: publicClient(t)})
		if got.Origin != c.want {
			t.Errorf("Host %q resolved to %v, want %v (%s)", c.host, got.Origin, c.want, got.Reason)
		}
		if got.Admitted != (c.want != OriginNone) {
			t.Errorf("Host %q admitted=%v", c.host, got.Admitted)
		}
	}
}

// A host in both roles has no single answer to which middleware runs, so the
// deployment is refused rather than one role being picked arbitrarily.
func TestAHostInBothRolesIsRefused(t *testing.T) {
	h := Hosts{App: []string{"both.example.test"}, Content: []string{"both.example.test"}}
	got := Decide(h, BoundaryRequest{Host: "both.example.test", Method: "GET", Client: publicClient(t)})
	if got.Admitted {
		t.Fatal("a host declared in both roles was served")
	}
	if !strings.Contains(got.Reason, "both app and content") {
		t.Errorf("the refusal says %q", got.Reason)
	}
}

// A mutating cookie request must carry an Origin naming an app host. This is
// the CSRF half of the boundary.
func TestAMutatingCookieRequestNeedsAMatchingOrigin(t *testing.T) {
	h := namedHosts()
	base := BoundaryRequest{
		Host: "app.example.test", Method: "POST",
		Client: publicClient(t), CookieAuth: true,
	}

	for _, c := range []struct {
		what     string
		origin   string
		admitted bool
	}{
		{"a matching origin", "https://app.example.test", true},
		{"a matching origin with a port", "https://app.example.test:8443", true},
		{"a matching origin in another case", "https://APP.example.test", true},
		{"no origin at all", "", false},
		{"a foreign origin", "https://evil.example.test", false},
		{"the content host", "https://files.example.test", false},
		// Refused twice over: as a recognised literal, and failing that as a
		// host name nothing declares. The second is what actually stops it.
		{"the null origin", "null", false},
	} {
		r := base
		r.Origin = c.origin
		got := Decide(h, r)
		if got.Admitted != c.admitted {
			t.Errorf("%s: admitted=%v (%s)", c.what, got.Admitted, got.Reason)
		}
	}
}

// A safe method needs no Origin, and neither does a header-authenticated
// mutation: an Authorization header is not ambient browser authority.
func TestOriginIsRequiredOnlyWhereAmbientAuthorityExists(t *testing.T) {
	h := namedHosts()
	for _, c := range []struct {
		what   string
		method string
		cookie bool
	}{
		{"a safe method with a cookie", "GET", true},
		{"a mutation with an app password", "POST", false},
		{"a delete with an app password", "DELETE", false},
	} {
		got := Decide(h, BoundaryRequest{
			Host: "app.example.test", Method: c.method,
			Client: publicClient(t), CookieAuth: c.cookie,
		})
		if !got.Admitted {
			t.Errorf("%s was refused: %s", c.what, got.Reason)
		}
	}
}

// A websocket upgrade is a GET, and a browser attaches ambient cookies to it,
// so it requires an Origin match even though its method is safe.
func TestAWebSocketUpgradeRequiresAnOrigin(t *testing.T) {
	h := namedHosts()
	base := BoundaryRequest{
		Host: "app.example.test", Method: "GET",
		Client: publicClient(t), WebSocket: true,
	}

	if got := Decide(h, base); got.Admitted {
		t.Error("an upgrade with no Origin was admitted")
	}
	r := base
	r.Origin = "https://evil.example.test"
	if got := Decide(h, r); got.Admitted {
		t.Error("an upgrade from a foreign origin was admitted")
	}
	r.Origin = "https://app.example.test"
	if got := Decide(h, r); !got.Admitted {
		t.Errorf("an upgrade from the app host was refused: %s", got.Reason)
	}
}

// The content host never consults an Origin: its one route authenticates an
// encrypted claim and reads no cookie.
func TestTheContentHostDoesNotConsultOrigin(t *testing.T) {
	h := namedHosts()
	got := Decide(h, BoundaryRequest{
		Host: "files.example.test", Method: "POST",
		Client: publicClient(t), CookieAuth: true,
	})
	if !got.Admitted || got.Origin != OriginContent {
		t.Fatalf("the content host answered %v (%s)", got.Origin, got.Reason)
	}
}

// First boot admits any host name, but only from a private client. The private
// network is the whole of the check, which is why the two halves are one step.
func TestFirstBootAdmitsOnlyAPrivateClient(t *testing.T) {
	empty := Hosts{}

	got := Decide(empty, BoundaryRequest{
		Host: "anything.example.test", Method: "GET", Client: privateClient(t),
	})
	if !got.Admitted || got.Origin != OriginFirstBoot {
		t.Fatalf("a private client on first boot: %v (%s)", got.Origin, got.Reason)
	}

	got = Decide(empty, BoundaryRequest{
		Host: "anything.example.test", Method: "GET", Client: publicClient(t),
	})
	if got.Admitted {
		t.Fatal("first boot admitted a public client")
	}

	// A client that could not be resolved is not private either, so a broken
	// forwarding header does not open the setup screen.
	got = Decide(empty, BoundaryRequest{
		Host: "anything.example.test", Method: "GET", Client: Unroutable(),
	})
	if got.Admitted {
		t.Fatal("first boot admitted an unresolvable client")
	}
}

// A mutation during first boot is admitted by the private-client check alone,
// since there is no host to match an Origin against.
func TestFirstBootAdmitsAPrivateMutation(t *testing.T) {
	got := Decide(Hosts{}, BoundaryRequest{
		Host: "setup.local", Method: "POST", Client: privateClient(t), CookieAuth: true,
	})
	if !got.Admitted {
		t.Fatalf("the setup mutation was refused: %s", got.Reason)
	}
	// An upgrade is not, because there is no origin to match and a browser
	// would attach ambient cookies to it.
	got = Decide(Hosts{}, BoundaryRequest{
		Host: "setup.local", Method: "GET", Client: privateClient(t), WebSocket: true,
	})
	if got.Admitted {
		t.Fatal("first boot admitted a websocket upgrade")
	}
}

// Host parsing: ports dropped, IPv6 literals kept whole.
func TestHostParsing(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"app.example.test", "app.example.test"},
		{"App.Example.Test:8443", "app.example.test"},
		{"[2001:db8::1]:8443", "[2001:db8::1]"},
		{"[2001:db8::1]", "[2001:db8::1]"},
		{"2001:db8::1", "2001:db8::1"},
		{"  app.example.test  ", "app.example.test"},
		{"", ""},
	} {
		if got := hostName(c.in); got != c.want {
			t.Errorf("hostName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An IPv6 app host reached with a port still matches.
func TestAnIPv6HostMatches(t *testing.T) {
	h := Hosts{App: []string{"[2001:db8::1]"}}
	got := Decide(h, BoundaryRequest{
		Host: "[2001:db8::1]:8443", Method: "GET", Client: publicClient(t),
	})
	if !got.Admitted || got.Origin != OriginApp {
		t.Fatalf("an IPv6 host answered %v (%s)", got.Origin, got.Reason)
	}
}
