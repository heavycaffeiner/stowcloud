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
	"net/url"
	"strconv"
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
	// Scheme is the request scheme after the trusted-proxy policy is applied.
	Scheme string
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
	// BrowserAuth is true for the password and factor requests that can create
	// an ambient browser session. They need the same origin binding even when
	// the browser has no cookie yet.
	BrowserAuth bool
	// WebSocket marks an upgrade request. A browser attaches ambient cookies
	// to an upgrade, so this one safe method still requires an Origin match.
	WebSocket bool
	// ContentRoute is true for the one route family the content host serves:
	// a direct byte stream that authenticates its own claim. A named content
	// host admits nothing else, and once one is named the app host stops
	// serving that family, so uploaded bytes never render on the origin that
	// holds the session cookie.
	ContentRoute bool
}

// isBrowserAuthPath identifies the two requests that can install a browser
// session. Header credentials and protocol mounts never enter this set.
func isBrowserAuthPath(method, path string) bool {
	if !strings.EqualFold(method, "POST") {
		return false
	}
	return strings.HasSuffix(path, "/auth/login") || strings.HasSuffix(path, "/auth/login/totp")
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
		return decideFirstBoot(r)
	}

	if name == "" {
		return Decision{Reason: "the request named no host"}
	}
	switch {
	case containsFold(h.Content, name):
		// The content host's single route family authenticates its own
		// encrypted claim. No cookie is read here and no CSRF applies, so an
		// Origin is not consulted either. Every other path is the
		// application's, and the application is not served under this name.
		if !r.ContentRoute {
			return Decision{Reason: "the content host " + name + " serves direct content only"}
		}
		return Decision{Admitted: true, Origin: OriginContent}
	case containsFold(h.App, name):
		if r.ContentRoute && len(h.Content) > 0 {
			return Decision{Reason: "direct content is served from the content host, not " + name}
		}
		return decideAppOrigin(r, name)
	default:
		return Decision{Reason: "the host " + name + " is not served by this deployment"}
	}
}

// decideAppOrigin applies the Origin rules on the application host.
func decideAppOrigin(r BoundaryRequest, name string) Decision {
	needsOrigin := r.WebSocket || (mutating(r.Method) && (r.CookieAuth || r.BrowserAuth))
	if !needsOrigin {
		return Decision{Admitted: true, Origin: OriginApp}
	}

	origin, ok := parseOrigin(r.Origin)
	if !ok {
		// Referer is never substituted. It is stripped by privacy settings and
		// by proxies, so accepting it would make the check depend on a header
		// an attacker can arrange to have absent.
		return Decision{Reason: "a mutating ambient request on " + name + " sent no Origin"}
	}
	request, ok := parseRequestOrigin(r.Scheme, r.Host)
	if !ok || origin != request {
		return Decision{Reason: "the origin does not match the request scheme and authority"}
	}
	return Decision{Admitted: true, Origin: OriginApp}
}

// decideFirstBoot admits a deployment that has not been configured yet.
//
// The client must be on a private network. That is the baseline for this
// branch; browser authentication and cookie-backed mutations add an exact
// Origin check because both carry ambient authority.
func decideFirstBoot(r BoundaryRequest) Decision {
	if !IsPrivateClient(r.Client) {
		return Decision{Reason: "first boot admits only a private client"}
	}
	if r.WebSocket {
		// An upgrade before any host is named has no origin to match, and a
		// browser would attach ambient cookies to it.
		return Decision{Reason: "first boot does not admit a websocket upgrade"}
	}
	if r.BrowserAuth || (mutating(r.Method) && r.CookieAuth) {
		origin, ok := parseOrigin(r.Origin)
		if !ok {
			if r.BrowserAuth {
				return Decision{Reason: "a browser authentication request on first boot sent no Origin"}
			}
			return Decision{Reason: "a mutating ambient request on first boot sent no Origin"}
		}
		request, ok := parseRequestOrigin(r.Scheme, r.Host)
		if !ok || origin != request {
			if r.BrowserAuth {
				return Decision{Reason: "the browser authentication origin does not match the request scheme and authority"}
			}
			return Decision{Reason: "the mutating ambient origin does not match the request scheme and authority"}
		}
	}
	return Decision{Admitted: true, Origin: OriginFirstBoot}
}

// parseRequestOrigin normalizes the request scheme and authority. Host role
// membership deliberately remains separate in Decide: a configured app host
// admits the request, and this exact comparison binds ambient authority to the
// request that received it.
func parseRequestOrigin(scheme, host string) (originValue, bool) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme != "http" && scheme != "https" {
		return originValue{}, false
	}
	authority, ok := normalizeAuthority(host, scheme)
	if !ok {
		return originValue{}, false
	}
	return originValue{scheme: scheme, authority: authority}, true
}

// parseOrigin parses the serialized Origin header and rejects anything that is
// not an origin. In particular, paths, credentials and the null origin are not
// silently reduced to a hostname.
func parseOrigin(raw string) (originValue, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return originValue{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		u.ForceQuery {
		return originValue{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return originValue{}, false
	}
	authority, ok := normalizeAuthority(u.Host, scheme)
	if !ok {
		return originValue{}, false
	}
	return originValue{scheme: scheme, authority: authority}, true
}

// OriginAllowed reports whether a request Origin names one of the allowed
// origins exactly, after both sides are normalized the way the boundary
// normalizes them: case-folded, default ports dropped, IPv6 bracketed.
//
// A malformed request Origin, the null origin, and a malformed entry in the
// allowed list all answer false: an allowlist entry that cannot be parsed is
// not a wildcard.
func OriginAllowed(origin string, allowed []string) bool {
	want, ok := parseOrigin(origin)
	if !ok {
		return false
	}
	for _, a := range allowed {
		if have, ok := parseOrigin(a); ok && have == want {
			return true
		}
	}
	return false
}

type originValue struct {
	scheme    string
	authority string
}

// normalizeAuthority canonicalizes a host authority and its port. Explicit
// default ports are equivalent to an omitted port, while every other port
// remains part of the authority.
func normalizeAuthority(raw, scheme string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n,/?#@\\") {
		return "", false
	}
	host, port, explicit, ok := splitAuthority(raw)
	if !ok || host == "" {
		return "", false
	}
	host = strings.ToLower(host)
	if explicit {
		if port == "" {
			return "", false
		}
		for i := range len(port) {
			if port[i] < '0' || port[i] > '9' {
				return "", false
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return "", false
		}
		port = strconv.Itoa(n)
		if (scheme == "http" && n == 80) || (scheme == "https" && n == 443) {
			port = ""
		}
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return "[" + host + "]", true
		}
		return host, true
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port, true
	}
	return host + ":" + port, true
}

func splitAuthority(raw string) (host, port string, explicit, ok bool) {
	if strings.HasPrefix(raw, "[") {
		end := strings.IndexByte(raw, ']')
		if end < 0 {
			return "", "", false, false
		}
		host = raw[1:end]
		rest := raw[end+1:]
		if rest == "" {
			return host, "", false, true
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", false, false
		}
		return host, rest[1:], true, true
	}

	switch strings.Count(raw, ":") {
	case 0:
		return raw, "", false, true
	case 1:
		i := strings.LastIndexByte(raw, ':')
		return raw[:i], raw[i+1:], true, true
	default:
		// A bare IPv6 literal has no port. A name with several colons is not
		// an authority and must not become equal merely by lowercasing it.
		if _, err := netip.ParseAddr(raw); err != nil {
			return "", "", false, false
		}
		return raw, "", false, true
	}
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
