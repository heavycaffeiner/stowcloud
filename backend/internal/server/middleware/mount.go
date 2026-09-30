// Linux only, because it fronts services that are Linux only.
//go:build linux

// Package middleware is the request chain: the global steps every request
// passes, and the per-group steps that decide who may reach a route.
package middleware

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

// contextKey is this package's key namespace on the Gin context. Typed so a
// handler cannot collide with it by using the same string.
type contextKey string

const (
	// KeyOrigin holds the Origin the boundary settled on.
	KeyOrigin contextKey = "sc.origin"
	// KeyClient holds the address TrustedProxy resolved.
	KeyClient contextKey = "sc.client"
	// KeyCredential holds the credential kind Auth selected. The name is what
	// gosec reads; the value is a context key and holds no secret.
	KeyCredential contextKey = "sc.credential" //nolint:gosec // G101: a context key, not a credential.
	// KeyTrace holds the request id.
	KeyTrace contextKey = "sc.trace"
	// KeyCause holds the error a handler classified into a status.
	KeyCause contextKey = "sc.cause"
)

// Deps is what the chain needs from the rest of the process.
type Deps struct {
	Hosts   func() Hosts
	Trusted func() []netip.Prefix
	Limiter *Limiter

	Clock clock.Clock

	ScriptHashes []string

	Principal func(c Credential) (Principal, bool)

	CSRFKey func() []byte

	Access AccessSink

	// Errors classifies what a handler recorded with Fail.
	Errors *apierr.Classifier

	ContentRoute func(method, path string) bool
}

// Global returns the steps every request passes through, outermost first.
//
// The order is the contract: TrustedProxy resolves the address RateLimit keys
// on, the boundary decides the host role before any credential is read, and the
// log step wraps everything so a refusal is recorded with the status it sent.
func Global(d Deps) ([]gin.HandlerFunc, error) {
	if d.Hosts == nil || d.Trusted == nil || d.Limiter == nil || d.Errors == nil {
		return nil, fmt.Errorf("middleware: the chain needs hosts, trusted proxies, a limiter and an error classifier")
	}
	return []gin.HandlerFunc{
		requestID,
		func(c *gin.Context) { logHandler(c, d) },
		func(c *gin.Context) {
			c.Set(string(KeyClient), resolveClient(c, d))
			c.Next()
		},
		func(c *gin.Context) { boundaryHandler(c, d) },
		func(c *gin.Context) { securityHeaders(c, d) },
		func(c *gin.Context) {
			if !d.Limiter.Allow(ClientOf(c).String()) {
				abortClassified(c, apierr.Classified{Class: apierr.RateLimited})
				return
			}
			c.Next()
		},
		func(c *gin.Context) { errorHandler(c, d.Errors) },
	}, nil
}

// Public admits any caller. A session cookie is still resolved, so a public
// route can tell who is signed in, but a device credential is not read.
func Public(d Deps) []gin.HandlerFunc {
	return []gin.HandlerFunc{func(c *gin.Context) { authHandler(c, d, true) }}
}

// Session admits only a browser session, and a mutation only with its CSRF
// token. Any other caller is answered as if the route did not exist.
func Session(d Deps) []gin.HandlerFunc {
	return []gin.HandlerFunc{
		func(c *gin.Context) { authHandler(c, d, false) },
		func(c *gin.Context) { csrfHandler(c, d) },
		requireSession,
	}
}

// Device resolves any credential and leaves the access decision to the
// protocol handler, which applies app-password masks and share limits itself.
func Device(d Deps) []gin.HandlerFunc {
	return []gin.HandlerFunc{
		func(c *gin.Context) { authHandler(c, d, false) },
		func(c *gin.Context) { csrfHandler(c, d) },
	}
}

func requestID(c *gin.Context) {
	id, err := NewTraceID()
	if err != nil {
		abortClassified(c, apierr.Classified{Class: apierr.Internal})
		return
	}
	c.Set(string(KeyTrace), id)
	c.Header(TraceHeader, id)
	c.Next()
}

func securityHeaders(c *gin.Context, d Deps) {
	for k, v := range SecurityHeaders() {
		c.Header(k, v)
	}
	if originOf(c) != OriginContent {
		c.Header("Content-Security-Policy", CSP(d.ScriptHashes))
	}
	c.Next()
}

// authHandler selects a credential and resolves what it proves. The principal
// is kept on both the Gin and the request context, so a net/http or typed
// handler reads the same answer.
func authHandler(c *gin.Context, d Deps, sessionOnly bool) {
	cred := Select(Presented{
		Authorization: c.GetHeader("Authorization"),
		Cookie:        cookieValue(c, SessionCookieName),
	}, sessionOnly)

	p := Principal{Kind: CredentialNone}
	if d.Principal != nil && cred.Kind != CredentialNone {
		if resolved, ok := d.Principal(cred); ok {
			p = resolved
		}
	}
	c.Set(string(KeyCredential), p)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), KeyCredential, p))
	c.Next()
}

// PrincipalFrom reads what the auth step resolved from a request context.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(KeyCredential).(Principal)
	return p, ok && p.Kind != CredentialNone
}

// requireSession answers anything but a session as a path that is not there,
// rather than revealing that the route exists.
func requireSession(c *gin.Context) {
	if principalOf(c).Kind != CredentialSessionCookie {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.Next()
}

// logHandler records the request after the rest of the chain has answered.
func logHandler(c *gin.Context, d Deps) {
	clk := clockOf(d)
	started := clk.Now()
	c.Next()

	if d.Access == nil {
		return
	}
	d.Access.Access(AccessRecordFor(
		traceOf(c), c.Request.Method, c.FullPath(), c.Request.URL.Path, c.Writer.Status(),
		clk.Since(started), ClientOf(c), principalOf(c), causeOf(c),
	))
}

func clockOf(d Deps) clock.Clock {
	if d.Clock == nil {
		return clock.System()
	}
	return d.Clock
}

func traceOf(c *gin.Context) string {
	if v, ok := c.Get(string(KeyTrace)); ok {
		if trace, ok := v.(string); ok {
			return trace
		}
	}
	return ""
}

// Fail records err as the request's outcome and stops the chain. The
// ErrorMapper step renders it once the handler returns.
func Fail(c *gin.Context, err error) {
	c.Errors = append(c.Errors, &gin.Error{Err: err, Type: gin.ErrorTypePrivate})
	c.Abort()
}

// errorHandler renders the last recorded error, unless the handler already
// answered.
func errorHandler(c *gin.Context, errs *apierr.Classifier) {
	c.Next()
	if len(c.Errors) == 0 || c.Writer.Written() {
		return
	}
	err := c.Errors.Last().Err
	var wait interface{ RetryAfter() int }
	if errors.As(err, &wait) && wait.RetryAfter() > 0 {
		c.Header("Retry-After", strconv.Itoa(wait.RetryAfter()))
	}
	status, body := apierr.REST(errs.Classify(err, apierr.VisibilityKnown))
	c.JSON(status, body)
}

// SetCause records the error a handler turned into a status for the access log.
func SetCause(c *gin.Context, err error) {
	if err != nil {
		c.Set(string(KeyCause), err)
	}
}

func causeOf(c *gin.Context) error {
	if len(c.Errors) > 0 {
		return c.Errors.Last().Err
	}
	if v, ok := c.Get(string(KeyCause)); ok {
		if err, ok := v.(error); ok {
			return err
		}
	}
	return nil
}

const drainCap = 8 << 20

// LimitJSON refuses a declared body past the JSON bound before the handler
// reads it. The body is drained first so the connection can be reused.
func LimitJSON(c *gin.Context) {
	if c.Request.ContentLength > limits.RequestBody {
		if stream := c.Request.Body; stream != nil {
			drained, drainErr := io.Copy(io.Discard, io.LimitReader(stream, drainCap+1))
			if drainErr != nil || drained > drainCap {
				c.Header("Connection", "close")
			}
		}
		abortClassified(c, apierr.Classified{Class: apierr.BodyTooLarge})
		return
	}
	c.Next()
}

// csrfHandler checks the token on a mutating cookie-authenticated request.
func csrfHandler(c *gin.Context, d Deps) {
	p := principalOf(c)
	if !CSRFRequired(c.Request.Method, p.Kind) {
		c.Next()
		return
	}
	var key []byte
	if d.CSRFKey != nil {
		key = d.CSRFKey()
	}
	if len(key) == 0 || !CSRFValid(key, cookieValue(c, SessionCookieName), c.GetHeader(CSRFHeader)) {
		abortClassified(c, apierr.Classified{Class: apierr.Denied})
		return
	}
	c.Next()
}

func principalOf(c *gin.Context) Principal {
	if v, ok := c.Get(string(KeyCredential)); ok {
		if p, ok := v.(Principal); ok {
			return p
		}
	}
	return Principal{Kind: CredentialNone}
}

func originOf(c *gin.Context) Origin {
	if v, ok := c.Get(string(KeyOrigin)); ok {
		if o, ok := v.(Origin); ok {
			return o
		}
	}
	return OriginNone
}

// boundaryHandler admits or refuses, and records which origin admitted it.
func boundaryHandler(c *gin.Context, d Deps) {
	dec := Decide(d.Hosts(), BoundaryRequest{
		Host:         c.Request.Host,
		Scheme:       requestScheme(c, d),
		Origin:       c.GetHeader("Origin"),
		Method:       c.Request.Method,
		Client:       ClientOf(c),
		CookieAuth:   cookieValue(c, SessionCookieName) != "",
		BrowserAuth:  isBrowserAuthPath(c.Request.Method, c.Request.URL.Path),
		WebSocket:    isUpgrade(c),
		ContentRoute: d.ContentRoute != nil && d.ContentRoute(c.Request.Method, c.Request.URL.Path),
	})
	if !dec.Admitted {
		c.Header("Connection", "close")
		abortStatusJSON(c, http.StatusMisdirectedRequest, "request_refused")
		return
	}
	c.Set(string(KeyOrigin), dec.Origin)
	c.Next()
}

func abortClassified(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	c.AbortWithStatusJSON(status, body)
}

func abortStatusJSON(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": code})
}

// requestScheme follows the scheme contract used by the engine: transport TLS
// is authoritative, and X-Forwarded-Proto is believed only from a trusted
// transport peer and only for the forwarded https value.
func requestScheme(c *gin.Context, d Deps) string {
	if c.Request.TLS != nil {
		return "https"
	}
	peer, err := remoteAddr(c.Request.RemoteAddr)
	if err == nil && PeerTrusted(peer, d.Trusted()) &&
		strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https") {
		return "https"
	}
	return "http"
}

// RequestScheme applies the boundary's transport and trusted-proxy scheme policy
// to a net/http request for handlers that need to construct an absolute origin.
func RequestScheme(r *http.Request, trusted []netip.Prefix) string {
	if r.TLS != nil {
		return "https"
	}
	peer, err := remoteAddr(r.RemoteAddr)
	if err == nil && PeerTrusted(peer, trusted) &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return "https"
	}
	return "http"
}

func resolveClient(c *gin.Context, d Deps) netip.Addr {
	peer, err := remoteAddr(c.Request.RemoteAddr)
	if err != nil {
		return Unroutable()
	}
	return ClientAddr(peer, d.Trusted(), c.GetHeader("CF-Connecting-IP"), c.GetHeader("X-Forwarded-For"))
}

func remoteAddr(raw string) (netip.Addr, error) {
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return netip.ParseAddr(host)
	}
	return netip.ParseAddr(raw)
}

// ClientOf reads what TrustedProxy resolved, or the placeholder if that step
// has not run.
func ClientOf(c *gin.Context) netip.Addr {
	if v, ok := c.Get(string(KeyClient)); ok {
		if addr, ok := v.(netip.Addr); ok {
			return addr
		}
	}
	return Unroutable()
}

func cookieValue(c *gin.Context, name string) string {
	value, err := c.Cookie(name)
	if err != nil {
		return ""
	}
	return value
}

func isUpgrade(c *gin.Context) bool {
	return strings.EqualFold(c.GetHeader("Upgrade"), "websocket")
}
