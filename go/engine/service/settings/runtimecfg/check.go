package runtimecfg

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// The save-time half: administrator input, validated strictly and refused with
// a named field. This is where a bad value is stopped.
//
// The boot-time half is in load.go and does the opposite with the same values,
// deliberately: a document that was already validated at save is clamped rather
// than refused, because a server that will not start over one stale field is a
// server the emergency door has to fix, and the emergency door edits this same
// document.

// CheckListen verifies that a bind address is usable.
//
// A host and a port, with the port present: "0.0.0.0" alone binds nothing and
// is the shape an administrator writes by accident.
func CheckListen(v string) error {
	if v == "" {
		return fmt.Errorf("the bind address must not be empty")
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return fmt.Errorf("the bind address %q needs a host and a port: %w", v, err)
	}
	if port == "" {
		return fmt.Errorf("the bind address %q has no port", v)
	}
	// An empty host is the wildcard, which is legal and is what a first boot
	// runs as.
	if host != "" {
		if _, perr := netip.ParseAddr(host); perr != nil {
			return fmt.Errorf("the bind address %q is not an address: %w", host, perr)
		}
	}
	return nil
}

// CheckHost validates one entry of a host list.
//
// Host-only syntax: a scheme, a path or a port means the administrator wrote a
// URL where a host was asked for, and admitting it would compare a Host header
// against something no Host header can equal.
func CheckHost(v string) error {
	if v == "" {
		return fmt.Errorf("a host must not be empty")
	}
	if strings.Contains(v, "://") {
		return fmt.Errorf("the host %q carries a scheme; write the host alone", v)
	}
	if strings.ContainsAny(v, "/?#") {
		return fmt.Errorf("the host %q carries a path, query or fragment; write the host alone", v)
	}
	if strings.ContainsAny(v, " \t") {
		return fmt.Errorf("the host %q carries whitespace", v)
	}
	// A bracketed literal is how IPv6 is written with a port, and the port is
	// what does not belong here.
	if _, _, err := net.SplitHostPort(v); err == nil {
		return fmt.Errorf("the host %q carries a port; the listener decides the port", v)
	}
	return nil
}

// CheckCIDR validates one trusted-proxy entry, which is an address or a block.
func CheckCIDR(v string) error {
	if v == "" {
		return fmt.Errorf("a proxy range must not be empty")
	}
	if strings.Contains(v, "/") {
		if _, err := netip.ParsePrefix(v); err != nil {
			return fmt.Errorf("the proxy range %q is not a CIDR block: %w", v, err)
		}
		return nil
	}
	if _, err := netip.ParseAddr(v); err != nil {
		return fmt.Errorf("the proxy range %q is neither an address nor a CIDR block: %w", v, err)
	}
	return nil
}

// CheckOrigin validates one allowed CORS origin.
//
// An absolute HTTPS origin and nothing else: scheme, host, optional port, and
// no path, query, fragment or userinfo. An origin carrying any of those is not
// an origin, and a browser would never send it as one.
func CheckOrigin(v string) error {
	if v == "" {
		return fmt.Errorf("an allowed origin must not be empty")
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("the allowed origin %q is not a URL: %w", v, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the allowed origin %q must be https", v)
	}
	if u.Host == "" {
		return fmt.Errorf("the allowed origin %q names no host", v)
	}
	if u.User != nil {
		return fmt.Errorf("the allowed origin %q carries userinfo", v)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("the allowed origin %q carries a path, query or fragment", v)
	}
	return nil
}

// CheckHostRoles validates the two host lists together.
//
// They are two roles rather than one allowlist, and the disjointness is the
// point: one TLS name cannot both carry the session application and be the
// cookie-free content origin, because the whole reason the content origin
// exists is that it never sees a session cookie.
func CheckHostRoles(appHosts, contentHosts []string) error {
	seen := make(map[string]string, len(appHosts)+len(contentHosts))
	for _, role := range []struct {
		name  string
		hosts []string
	}{
		{"app host", appHosts},
		{"content host", contentHosts},
	} {
		for _, h := range role.hosts {
			if err := CheckHost(h); err != nil {
				return fmt.Errorf("%s: %w", role.name, err)
			}
			key := strings.ToLower(h)
			if other, dup := seen[key]; dup {
				if other == role.name {
					return fmt.Errorf("the %s %q is listed twice", role.name, h)
				}
				return fmt.Errorf(
					"the host %q is both an %s and a %s; one name cannot carry the session "+
						"application and be the cookie-free content origin", h, other, role.name)
			}
			seen[key] = role.name
		}
	}
	return nil
}

// CheckCanonicalURL validates the compatibility fallback.
//
// It is used when a request origin is unavailable, so it has to name a host the
// deployment actually answers the application on: a canonical URL outside the
// app hosts would hand clients a name that resolves nowhere.
func CheckCanonicalURL(v string, appHosts []string) error {
	if v == "" {
		return nil
	}
	if err := CheckOrigin(v); err != nil {
		return fmt.Errorf("the canonical URL is not an origin: %w", err)
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("the canonical URL %q is not a URL: %w", v, err)
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range appHosts {
		if strings.ToLower(h) == host {
			return nil
		}
	}
	return fmt.Errorf(
		"the canonical URL %q names %q, which is not one of the declared app hosts", v, u.Hostname())
}
