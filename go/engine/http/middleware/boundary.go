// Linux only, for the same reason as the rest of this package.
//go:build linux

// The host and origin boundary: whether this deployment serves the request at
// all, which origin it arrived on, and whether a mutating request's Origin is
// acceptable.
//
// One step rather than a host gate and a separate origin check. The first-boot
// bypass, which accepts a request naming no configured host, is only safe
// because the same decision has already required the client to be on a private
// network. Two steps can be mounted apart, and then the bypass survives without
// the gate it depends on.
package middleware

import (
	"net/netip"
	"strings"
)

// Origin names which of the deployment's two host roles a request arrived on.
type Origin uint8

const (
	// OriginNone is a request that was refused, or one not yet decided.
	OriginNone Origin = iota

	// OriginApp is the application host: the interface, the API, the session
	// cookie's home.
	OriginApp

	// OriginContent is the host that serves file bytes. Its one route
	// authenticates an encrypted claim, so nothing here reads a cookie and no
	// app middleware is mounted on it.
	OriginContent

	// OriginFirstBoot is a deployment with no host configured yet, admitted
	// only from a private network.
	OriginFirstBoot
)

// String is the origin's name in a diagnostic or an audit row.
func (o Origin) String() string {
	switch o {
	case OriginApp:
		return "app"
	case OriginContent:
		return "content"
	case OriginFirstBoot:
		return "first boot"
	case OriginNone:
		return "none"
	default:
		return "unknown"
	}
}

// Hosts is the deployment's declared host roles, read live per request.
type Hosts struct {
	App     []string
	Content []string
}

// BoundaryRequest is what the boundary decides over.
type BoundaryRequest struct {
	// Host is the Host header verbatim, port included.
	Host string
	// Origin is the Origin header, or empty when the client sent none.
	Origin string
	// Method is the request verb.
	Method string
	// Client is the address TrustedProxy resolved.
	Client netip.Addr
	// CookieAuth is true when the request's credential is the browser session
	// cookie. An app password is not ambient browser authority and does not
	// need an Origin.
	CookieAuth bool
	// WebSocket marks an upgrade request. A browser attaches ambient cookies
	// to an upgrade, so this one safe method still requires an Origin match.
	WebSocket bool
}

// Decision is the boundary's answer.
type Decision struct {
	// Admitted is false when the request must be refused.
	Admitted bool
	// Origin is which host role admitted it.
	Origin Origin
	// Reason explains a refusal, for the log and for nothing else. It never
	// reaches the client, which learns only that this deployment does not
	// serve that name.
	Reason string
}

// Decide admits or refuses one request.
//
// A named deployment matches its Host case-insensitively with the port
// ignored, because a client legitimately reaches the same deployment on the
// port the operator published and the name is what identifies it.
func Decide(h Hosts, r BoundaryRequest) Decision {
	name := hostName(r.Host)

	if overlap := firstOverlap(h); overlap != "" {
		// A host in both lists has no single answer to "which middleware runs
		// here", so the deployment is misconfigured rather than the request
		// being wrong. Refusing is the safe direction: serving it would pick
		// one role arbitrarily.
		return Decision{Reason: "the host " + overlap + " is declared as both app and content"}
	}

	named := len(h.App) > 0 || len(h.Content) > 0
	if !named {
		return decideFirstBoot(r, name)
	}

	if name == "" {
		return Decision{Reason: "the request named no host"}
	}
	switch {
	case containsFold(h.Content, name):
		// The content host's single route authenticates its own encrypted
		// claim. No cookie is read here and no CSRF applies, so an Origin is
		// not consulted either.
		return Decision{Admitted: true, Origin: OriginContent}
	case containsFold(h.App, name):
		return decideAppOrigin(h, r, name)
	default:
		return Decision{Reason: "the host " + name + " is not served by this deployment"}
	}
}

// decideAppOrigin applies the Origin rules on the application host.
func decideAppOrigin(h Hosts, r BoundaryRequest, name string) Decision {
	needsOrigin := r.WebSocket || (mutating(r.Method) && r.CookieAuth)
	if !needsOrigin {
		return Decision{Admitted: true, Origin: OriginApp}
	}

	origin := hostName(originHost(r.Origin))
	if origin == "" {
		// Referer is never substituted. It is stripped by privacy settings and
		// by proxies, so accepting it would make the check depend on a header
		// an attacker can arrange to have absent.
		return Decision{Reason: "a mutating cookie request on " + name + " sent no Origin"}
	}
	if !containsFold(h.App, origin) {
		return Decision{Reason: "the origin " + origin + " is not an app host"}
	}
	return Decision{Admitted: true, Origin: OriginApp}
}

// decideFirstBoot admits a deployment that has not been configured yet.
//
// The client must be on a private network. That single condition is what makes
// the rest of this branch safe: with no host named there is nothing to match a
// Host or an Origin against, so the network position is the whole check.
func decideFirstBoot(r BoundaryRequest, name string) Decision {
	if !IsPrivateClient(r.Client) {
		return Decision{Reason: "first boot admits only a private client"}
	}
	if r.WebSocket {
		// An upgrade before any host is named has no origin to match, and a
		// browser would attach ambient cookies to it.
		return Decision{Reason: "first boot does not admit a websocket upgrade"}
	}
	_ = name
	return Decision{Admitted: true, Origin: OriginFirstBoot}
}

func mutating(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return false
	default:
		return true
	}
}

// hostName lowercases a Host value and drops the port.
func hostName(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	// A bracketed IPv6 literal keeps its brackets so the colons inside are not
	// read as a port separator.
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end >= 0 {
			return strings.ToLower(h[:end+1])
		}
		return ""
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		// A bare IPv6 address with no brackets has several colons and no port.
		if strings.Count(h, ":") == 1 {
			h = h[:i]
		}
	}
	return strings.ToLower(h)
}

// originHost pulls the host out of an Origin header, which is a serialised
// origin rather than a bare name.
func originHost(origin string) string {
	o := strings.TrimSpace(origin)
	if o == "" || strings.EqualFold(o, "null") {
		// "null" is what a sandboxed or redirected context sends. Folding it to
		// empty here is belt and braces: left alone it becomes the host name
		// "null", which matches no configured host and is refused for that
		// reason instead. Named so a later reader does not mistake the literal
		// for a hostname somebody could configure.
		return ""
	}
	if i := strings.Index(o, "://"); i >= 0 {
		o = o[i+3:]
	}
	if i := strings.IndexAny(o, "/?#"); i >= 0 {
		o = o[:i]
	}
	return o
}

func containsFold(list []string, name string) bool {
	for _, h := range list {
		if strings.EqualFold(hostName(h), name) {
			return true
		}
	}
	return false
}

// firstOverlap returns a host declared in both roles, or empty.
func firstOverlap(h Hosts) string {
	for _, a := range h.App {
		if containsFold(h.Content, hostName(a)) {
			return hostName(a)
		}
	}
	return ""
}
