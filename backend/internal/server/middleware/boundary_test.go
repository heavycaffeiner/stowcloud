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
// ignored, and refuses every other name. The content host resolves only for
// its own route family, which is what the content column carries.
func TestANamedDeploymentAdmitsOnlyItsOwnHosts(t *testing.T) {
	h := namedHosts()
	for _, c := range []struct {
		host    string
		content bool
		want    Origin
	}{
		{"app.example.test", false, OriginApp},
		{"APP.example.test", false, OriginApp},
		{"app.example.test:8443", false, OriginApp},
		{"files.example.test", true, OriginContent},
		{"files.example.test:443", true, OriginContent},
		{"evil.example.test", false, OriginNone},
		{"", false, OriginNone},
	} {
		got := Decide(h, BoundaryRequest{Host: c.host, Method: "GET", Client: publicClient(t), ContentRoute: c.content})
		if got.Origin != c.want {
			t.Errorf("Host %q resolved to %v, want %v (%s)", c.host, got.Origin, c.want, got.Reason)
		}
		if got.Admitted != (c.want != OriginNone) {
			t.Errorf("Host %q admitted=%v", c.host, got.Admitted)
		}
	}
}

// The content host serves the direct content family and nothing else, and
// once a content host is named the app host stops serving that family. A
// deployment with no content host keeps serving it from the app host, and
// first boot names nothing to isolate to.
func TestTheHostRoleDecidesWhichRoutesAreServed(t *testing.T) {
	named := namedHosts()
	appOnly := Hosts{App: []string{"app.example.test"}}
	for _, c := range []struct {
		what    string
		hosts   Hosts
		host    string
		content bool
		want    bool
	}{
		{"the application on the content host", named, "files.example.test", false, false},
		{"direct content on the content host", named, "files.example.test", true, true},
		{"the application on the app host", named, "app.example.test", false, true},
		{"direct content on the app host beside a content host", named, "app.example.test", true, false},
		{"direct content on the app host with no content host", appOnly, "app.example.test", true, true},
		{"direct content on first boot", Hosts{}, "anything.example.test", true, true},
	} {
		got := Decide(c.hosts, BoundaryRequest{
			Host: c.host, Method: "GET", Client: privateClient(t), ContentRoute: c.content,
		})
		if got.Admitted != c.want {
			t.Errorf("%s: admitted=%v (%s)", c.what, got.Admitted, got.Reason)
		}
	}
}

// An allowed origin matches exactly after normalization. A default port, a
// case difference and IPv6 brackets are equal; a different scheme, a
// subdomain, a path, the null origin and an unparseable entry are not.
func TestOriginAllowedMatchesExactlyAfterNormalization(t *testing.T) {
	allowed := []string{"https://Other.example.test:443", "https://[2001:db8::1]:8443", "not an origin"}
	for _, c := range []struct {
		origin string
		want   bool
	}{
		{"https://other.example.test", true},
		{"https://other.example.test:443", true},
		{"https://[2001:DB8::1]:8443", true},
		{"http://other.example.test", false},
		{"https://sub.other.example.test", false},
		{"https://other.example.test/path", false},
		{"null", false},
		{"", false},
		{"not an origin", false},
	} {
		if got := OriginAllowed(c.origin, allowed); got != c.want {
			t.Errorf("Origin %q allowed=%v, want %v", c.origin, got, c.want)
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

// A mutating cookie request must carry an Origin matching the request's
// normalized scheme and authority. App-host membership is a separate role
// decision: a configured host is admitted only when the request and Origin
// still name the same authority.
func TestAMutatingCookieRequestNeedsAMatchingOrigin(t *testing.T) {
	h := namedHosts()
	h.App = append(h.App, "alias.example.test")
	base := BoundaryRequest{
		Host: "app.example.test", Scheme: "https", Method: "POST",
		Client: publicClient(t), CookieAuth: true,
	}

	for _, c := range []struct {
		what     string
		host     string
		scheme   string
		origin   string
		admitted bool
	}{
		{"a matching configured origin", "app.example.test", "https", "https://app.example.test", true},
		{"a matching origin in another case", "app.example.test", "https", "HTTPS://APP.example.test", true},
		{"an explicit default HTTPS port", "app.example.test", "https", "https://app.example.test:443", true},
		{"an explicit default HTTP port", "app.example.test", "http", "http://app.example.test:80", true},
		{"a matching non-default port", "app.example.test:8443", "https", "https://app.example.test:8443", true},
		{"a configured alias on its own authority", "alias.example.test", "https", "https://alias.example.test", true},
		{"no origin at all", "app.example.test", "https", "", false},
		{"a mismatched port", "app.example.test", "https", "https://app.example.test:8443", false},
		{"a mismatched scheme", "app.example.test", "https", "http://app.example.test", false},
		{"a foreign origin", "app.example.test", "https", "https://evil.example.test", false},
		{"a different configured app host", "app.example.test", "https", "https://alias.example.test", false},
		{"the content host", "app.example.test", "https", "https://files.example.test", false},
		{"the null origin", "app.example.test", "https", "null", false},
	} {
		r := base
		r.Host, r.Scheme, r.Origin = c.host, c.scheme, c.origin
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
			Host: "app.example.test", Scheme: "https", Method: c.method,
			Client: publicClient(t), CookieAuth: c.cookie,
		})
		if !got.Admitted {
			t.Errorf("%s was refused: %s", c.what, got.Reason)
		}
	}
}

// Password and factor sign-in are ambient-session creation even before a
// cookie exists, so they need an Origin matching the request's origin.
func TestBrowserAuthenticationNeedsAMatchingOriginWithoutACookie(t *testing.T) {
	h := namedHosts()
	base := BoundaryRequest{
		Host: "app.example.test", Scheme: "https", Method: "POST",
		Client: publicClient(t), BrowserAuth: true,
	}
	for _, c := range []struct {
		what     string
		host     string
		scheme   string
		origin   string
		admitted bool
	}{
		{"a matching origin", "app.example.test", "https", "https://app.example.test", true},
		{"an explicit default port", "app.example.test", "https", "https://app.example.test:443", true},
		{"a matching non-default port", "app.example.test:8443", "https", "https://app.example.test:8443", true},
		{"no origin", "app.example.test", "https", "", false},
		{"a mismatched port", "app.example.test", "https", "https://app.example.test:8443", false},
		{"a mismatched scheme", "app.example.test", "https", "http://app.example.test", false},
		{"a foreign origin", "app.example.test", "https", "https://evil.example.test", false},
	} {
		r := base
		r.Host, r.Scheme, r.Origin = c.host, c.scheme, c.origin
		if got := Decide(h, r); got.Admitted != c.admitted {
			t.Errorf("%s: admitted=%v (%s)", c.what, got.Admitted, got.Reason)
		}
	}
}

// A websocket upgrade is a GET, and a browser attaches ambient cookies to it,
// so it requires an Origin match even though its method is safe.
func TestAWebSocketUpgradeRequiresAnOrigin(t *testing.T) {
	h := namedHosts()
	base := BoundaryRequest{
		Host: "app.example.test", Scheme: "https", Method: "GET",
		Client: publicClient(t), WebSocket: true,
	}

	if got := Decide(h, base); got.Admitted {
		t.Error("an upgrade with no Origin was admitted")
	}
	r := base
	r.Origin = "https://evil.example.test"
	if got := Decide(h, r); got.Admitted {
		t.Error("an upgrade from a foreign Origin was admitted")
	}
	r.Origin = "https://app.example.test"
	if got := Decide(h, r); !got.Admitted {
		t.Errorf("an upgrade from the app Origin was refused: %s", got.Reason)
	}
}

// First-boot browser authentication has no configured host role to consult,
// but it still binds the ambient session to the request's exact origin.
func TestFirstBootBrowserAuthenticationNeedsAMatchingOrigin(t *testing.T) {
	for _, c := range []struct {
		what   string
		host   string
		scheme string
		origin string
		want   bool
	}{
		{"a matching origin", "setup.local", "https", "https://setup.local", true},
		{"an explicit default port", "setup.local", "https", "https://setup.local:443", true},
		{"a matching non-default port", "setup.local:8443", "https", "https://setup.local:8443", true},
		{"a mismatched port", "setup.local", "https", "https://setup.local:8443", false},
		{"a mismatched scheme", "setup.local", "https", "http://setup.local", false},
		{"no origin", "setup.local", "https", "", false},
	} {
		got := Decide(Hosts{}, BoundaryRequest{
			Host: c.host, Scheme: c.scheme, Origin: c.origin,
			Method: "POST", Client: privateClient(t), BrowserAuth: true,
		})
		if got.Admitted != c.want {
			t.Errorf("%s: admitted=%v (%s)", c.what, got.Admitted, got.Reason)
		}
	}
}

// The content host never consults an Origin: its one route authenticates an
// encrypted claim and reads no cookie.
func TestTheContentHostDoesNotConsultOrigin(t *testing.T) {
	h := namedHosts()
	got := Decide(h, BoundaryRequest{
		Host: "files.example.test", Method: "POST",
		Client: publicClient(t), CookieAuth: true, ContentRoute: true,
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

// A cookie-backed first-boot mutation still carries ambient authority. The
// private-client gate is not a substitute for an exact Origin check.
func TestFirstBootCookieMutationNeedsAMatchingOrigin(t *testing.T) {
	for _, c := range []struct {
		what   string
		host   string
		scheme string
		origin string
		want   bool
	}{
		{"no origin", "setup.local", "https", "", false},
		{"a matching origin", "setup.local", "https", "https://setup.local", true},
		{"an explicit default port", "setup.local", "https", "https://setup.local:443", true},
		{"a matching non-default port", "setup.local:8443", "https", "https://setup.local:8443", true},
		{"a mismatched port", "setup.local", "https", "https://setup.local:8443", false},
		{"a mismatched scheme", "setup.local", "https", "http://setup.local", false},
	} {
		got := Decide(Hosts{}, BoundaryRequest{
			Host: c.host, Scheme: c.scheme, Origin: c.origin,
			Method: "POST", Client: privateClient(t), CookieAuth: true,
		})
		if got.Admitted != c.want {
			t.Errorf("%s: admitted=%v (%s)", c.what, got.Admitted, got.Reason)
		}
	}

	// An upgrade is not, because there is no configured host to match and a
	// browser would attach ambient cookies to it.
	got := Decide(Hosts{}, BoundaryRequest{
		Host: "setup.local", Scheme: "https", Method: "GET",
		Client: privateClient(t), WebSocket: true,
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
