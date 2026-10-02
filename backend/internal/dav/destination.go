//go:build linux

// The Destination and Overwrite headers of COPY and MOVE.
package dav

import (
	"errors"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrNoDestination reports a COPY or MOVE with no Destination.
	ErrNoDestination = errors.New("no Destination header")
	// ErrForeignDestination reports a Destination naming another host.
	ErrForeignDestination = errors.New("the Destination names a different host")
)

// ParseDestination returns the destination's path segments.
//
// An absolute URL is accepted, and its host must match the request's. The
// scheme is not compared: a reverse proxy terminating TLS makes the server see
// http where the client wrote https, and refusing that breaks every deployment
// behind one.
func ParseDestination(value, requestHost string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, ErrNoDestination
	}

	path := value
	if scheme := schemeOf(value); scheme != "" {
		rest := value[len(scheme)+len("://"):]
		host := rest
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			host, path = rest[:slash], rest[slash:]
		} else {
			path = "/"
		}
		if !strings.EqualFold(stripUserinfo(host), stripUserinfo(requestHost)) {
			return nil, ErrForeignDestination
		}
	}

	if !strings.HasPrefix(path, "/") {
		return nil, ErrNoDestination
	}
	// The fragment is a client-side construct and never reaches a server, so a
	// "#" here is a literal character in a name that should have been escaped.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return SplitPath(path)
}

// schemeOf returns a leading "scheme://" scheme, or empty.
func schemeOf(value string) string {
	i := strings.Index(value, "://")
	if i <= 0 {
		return ""
	}
	scheme := value[:i]
	for j := 0; j < len(scheme); j++ {
		c := scheme[j]
		alpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if j == 0 && !alpha {
			return ""
		}
		digit := c >= '0' && c <= '9'
		if !alpha && !digit && c != '+' && c != '-' && c != '.' {
			return ""
		}
	}
	return scheme
}

// stripUserinfo removes anything before an "@".
func stripUserinfo(host string) string {
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		return host[at+1:]
	}
	return host
}

// Overwrite reports whether an existing destination may be replaced.
//
// Only a case-insensitive "F" means no. An absent header means yes, and so
// does anything unrecognised: the header's default is overwrite, and treating
// a typo as "no" turns a working request into a failing one rather than into a
// safe one.
func Overwrite(value string) bool {
	return !strings.EqualFold(strings.TrimSpace(value), "F")
}
