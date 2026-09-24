// Linux only, because it fronts services that are Linux only.
//go:build linux

// Mounting the chain on the framework.
//
// This file translates native net/http requests into the package's decisions
// and renders those decisions through Gin. Keeping the split means each rule
// remains framework-independent and straightforward to test.
package middleware

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
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

	Audit  AuditSink
	Access AccessSink

	ContentRoute func(method, path string) bool
}

// Record is one entry in a replay: which step ran, and whether it passed the
// request on or answered it.
type Record struct {
	Step    Step
	Entered bool
	Passed  bool
}

// Recorder collects a replay. Nil is fine and records nothing.
type Recorder interface {
	Record(r Record)
}

// Mount installs the chain on an app, in the order Chain gives.
func Mount(app *gin.Engine, steps []Step, d Deps, rec Recorder) error {
	if err := ValidateChain(steps); err != nil {
		return err
	}
	if app == nil {
		return fmt.Errorf("middleware: the chain needs an application")
	}
	if d.Hosts == nil || d.Trusted == nil || d.Limiter == nil {
		return fmt.Errorf("middleware: the chain needs hosts, trusted proxies and a limiter")
	}

	for _, s := range steps {
		app.Use(handlerFor(s, d, rec))
	}
	return nil
}

// handlerFor builds one Gin middleware handler.
func handlerFor(s Step, d Deps, rec Recorder) gin.HandlerFunc {
	inner := stepHandler(s, d)
	if rec == nil {
		return inner
	}
	return func(c *gin.Context) {
		rec.Record(Record{Step: s, Entered: true})
		inner(c)
		// A Gin middleware that reached its downstream handlers remains
		// non-aborted. A refusal aborts the context and therefore did not pass.
		rec.Record(Record{Step: s, Passed: !c.IsAborted()})
	}
}

func stepHandler(s Step, d Deps) gin.HandlerFunc {
	switch s {
	case StepTrustedProxy:
		return func(c *gin.Context) {
			c.Set(string(KeyClient), resolveClient(c, d))
			c.Next()
		}
	case StepHostAndOriginBoundary:
		return func(c *gin.Context) { boundaryHandler(c, d) }
	case StepRateLimit:
		return func(c *gin.Context) {
			key := ClientOf(c).String()
			if !d.Limiter.Allow(key) {
				abortClassified(c, apierr.Classified{Class: apierr.RateLimited})
				return
			}
			c.Next()
		}
	case StepAuth:
		return func(c *gin.Context) { authHandler(c, d) }
	case StepRequestID:
		return func(c *gin.Context) {
			id, err := NewTraceID()
			if err != nil {
				abortClassified(c, apierr.Classified{Class: apierr.Internal})
				return
			}
			c.Set(string(KeyTrace), id)
			c.Header(TraceHeader, id)
			c.Next()
		}
	case StepSecurityHeaders:
		return func(c *gin.Context) {
			for k, v := range SecurityHeaders() {
				c.Header(k, v)
			}
			if originOf(c) != OriginContent {
				c.Header("Content-Security-Policy", CSP(d.ScriptHashes))
			}
			c.Next()
		}
	case StepACLScope:
		return func(c *gin.Context) { scopeHandler(c) }
	case StepBodyLimit:
		return func(c *gin.Context) { bodyLimitHandler(c) }
	case StepCSRF:
		return func(c *gin.Context) { csrfHandler(c, d) }
	case StepAuditSink:
		return func(c *gin.Context) { auditHandler(c, d) }
	case StepErrorMapper, StepUnset:
		return func(c *gin.Context) { c.Next() }
	default:
		return func(c *gin.Context) { c.Next() }
	}
}

// authHandler selects a credential and resolves what it proves.
func authHandler(c *gin.Context, d Deps) {
	cred := Select(Presented{
		Authorization: c.GetHeader("Authorization"),
		Cookie:        cookieValue(c, SessionCookieName),
	}, publicRead(c))

	p := Principal{Kind: CredentialNone}
	if d.Principal != nil && cred.Kind != CredentialNone {
		if resolved, ok := d.Principal(cred); ok {
			p = resolved
		}
	}
	c.Set(string(KeyCredential), p)
	c.Next()
}

// scopeHandler applies the matched route's requirement. A refusal answers as
// a path that is not there, rather than revealing the route's existence.
func scopeHandler(c *gin.Context) {
	m, ok := metaOf(c)
	if !ok {
		c.Next()
		return
	}
	if err := Scope(m.req, principalOf(c)); err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.Next()
}

// auditHandler records the request after the rest of the chain has answered.
func auditHandler(c *gin.Context, d Deps) {
	clk := clockOf(d)
	started := clk.Now()
	c.Next()

	if d.Audit == nil && d.Access == nil {
		return
	}
	name := ""
	if m, ok := metaOf(c); ok {
		name = m.name
	}
	status := statusOf(c)

	if d.Audit != nil {
		d.Audit.Record(AuditRecordFor(
			traceOf(c), c.Request.Method, name, status,
			ClientOf(c), principalOf(c), originOf(c),
		))
	}
	if d.Access != nil {
		d.Access.Access(AccessRecordFor(
			traceOf(c), c.Request.Method, name, c.Request.URL.Path, status,
			clk.Since(started), ClientOf(c), principalOf(c), causeOf(c),
		))
	}
}

func clockOf(d Deps) clock.Clock {
	if d.Clock == nil {
		return clock.System()
	}
	return d.Clock
}

func statusOf(c *gin.Context) int {
	return c.Writer.Status()
}

func traceOf(c *gin.Context) string {
	if v, ok := c.Get(string(KeyTrace)); ok {
		if trace, ok := v.(string); ok {
			return trace
		}
	}
	return ""
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

// bodyLimitHandler refuses a body past its route's class before a handler can
// read it.
func bodyLimitHandler(c *gin.Context) {
	m, ok := metaOf(c)
	if !ok {
		c.Next()
		return
	}
	bound, bounded := BodyBound(m.body)
	if !bounded {
		c.Next()
		return
	}
	if declared := c.Request.ContentLength; declared > bound {
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
	if m, ok := metaOf(c); ok && m.req.Access == route.AccessPublic {
		c.Next()
		return
	}
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

func publicRead(c *gin.Context) bool {
	m, ok := metaOf(c)
	return ok && m.req.Access == route.AccessPublic
}

const routeRequirementKey contextKey = "sc.route.requirement"

// SetRequirement is how the server attaches a route's metadata.
func SetRequirement(c *gin.Context, req route.Requirement, body route.BodyClass, name string) {
	c.Set(string(routeRequirementKey), routeMeta{req: req, body: body, name: name})
}

type routeMeta struct {
	req  route.Requirement
	body route.BodyClass
	name string
}

// RequirementOf reports the matched route's requirement.
func RequirementOf(c *gin.Context) (route.Requirement, bool) {
	m, ok := metaOf(c)
	return m.req, ok
}

func metaOf(c *gin.Context) (routeMeta, bool) {
	v, ok := c.Get(string(routeRequirementKey))
	if !ok {
		return routeMeta{}, false
	}
	m, ok := v.(routeMeta)
	return m, ok
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
