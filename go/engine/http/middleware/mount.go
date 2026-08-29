// Linux only, for the same reason as the rest of this package.
//go:build linux

// Mounting the chain on the framework.
//
// This is the only file in the package that names fiber. Everything else
// decides; this translates a request into those decisions' inputs and their
// answers back into a response. Keeping the split means a step's rule can be
// tested without a server, and the framework can be replaced without touching
// a rule.
package middleware

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// contextKey is this package's key namespace on the request context. Typed so
// a handler cannot collide with it by using the same string.
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
)

// Deps is what the chain needs from the rest of the process.
//
// Functions rather than values for anything an operator can change while the
// server runs: hosts and trusted proxies are re-read per request, so a
// settings save takes effect on the next one without a listener swap.
type Deps struct {
	Hosts   func() Hosts
	Trusted func() []netip.Prefix
	Limiter *Limiter

	// ScriptHashes are the embedded hydration scripts' CSP hashes. Empty is a
	// deployment with no inlined hydration, which the policy simply does not
	// name.
	ScriptHashes []string

	// Principal resolves what a credential proves. Nil leaves every request
	// unauthenticated, which is what a server with no auth service wired does.
	Principal func(c Credential) (Principal, bool)

	// CSRFKey is the deployment's durable derivation key. Nil refuses every
	// mutation that would need one, rather than letting them through
	// unchecked.
	CSRFKey func() []byte
}

// Record is one entry in a replay: which step ran, and whether it passed the
// request on or answered it.
type Record struct {
	Step    Step
	Entered bool
	Passed  bool
}

// Recorder collects a replay. Nil is fine and records nothing, which is what
// production uses.
type Recorder interface {
	Record(r Record)
}

// Mount installs the chain on an app, in the order Chain gives.
//
// Returns an error rather than panicking, so a misassembled chain is a startup
// refusal the caller reports rather than a crash with no context.
func Mount(app *fiber.App, steps []Step, d Deps, rec Recorder) error {
	if err := ValidateChain(steps); err != nil {
		return err
	}
	if d.Hosts == nil || d.Trusted == nil || d.Limiter == nil {
		return fmt.Errorf("middleware: the chain needs hosts, trusted proxies and a limiter")
	}

	for _, s := range steps {
		app.Use(handlerFor(s, d, rec))
	}
	return nil
}

// handlerFor builds one step's fiber handler.
func handlerFor(s Step, d Deps, rec Recorder) fiber.Handler {
	inner := stepHandler(s, d)
	if rec == nil {
		return inner
	}
	return func(c *fiber.Ctx) error {
		rec.Record(Record{Step: s, Entered: true})
		err := inner(c)
		// Passed is about whether the request continued, which is what the
		// order of a chain actually means. An error or a short-circuit is a
		// step that answered instead of passing.
		rec.Record(Record{Step: s, Passed: err == nil})
		return err
	}
}

func stepHandler(s Step, d Deps) fiber.Handler {
	switch s {
	case StepTrustedProxy:
		return func(c *fiber.Ctx) error {
			c.Locals(string(KeyClient), resolveClient(c, d))
			return c.Next()
		}
	case StepHostAndOriginBoundary:
		return func(c *fiber.Ctx) error { return boundaryHandler(c, d) }
	case StepRateLimit:
		return func(c *fiber.Ctx) error {
			key := clientOf(c).String()
			if !d.Limiter.Allow(key) {
				return fiber.NewError(fiber.StatusTooManyRequests)
			}
			return c.Next()
		}
	case StepAuth:
		return func(c *fiber.Ctx) error { return authHandler(c, d) }
	case StepRequestID:
		return func(c *fiber.Ctx) error {
			id, err := NewTraceID()
			if err != nil {
				// A process that cannot read randomness cannot mint a session
				// either, so this is reported rather than worked around with a
				// predictable id.
				return err
			}
			c.Locals(string(KeyTrace), id)
			c.Set(TraceHeader, id)
			return c.Next()
		}
	case StepSecurityHeaders:
		return func(c *fiber.Ctx) error {
			for k, v := range SecurityHeaders() {
				c.Set(k, v)
			}
			if originOf(c) != OriginContent {
				// The content host serves bytes rather than the application,
				// and its own document sets what it needs.
				c.Set("Content-Security-Policy", CSP(d.ScriptHashes))
			}
			return c.Next()
		}
	case StepACLScope:
		return func(c *fiber.Ctx) error { return scopeHandler(c) }
	case StepBodyLimit:
		return func(c *fiber.Ctx) error { return bodyLimitHandler(c) }
	case StepCSRF:
		return func(c *fiber.Ctx) error { return csrfHandler(c, d) }
	case StepAuditSink, StepErrorMapper, StepUnset:
		// Not yet implemented here. Passing through is deliberate and visible:
		// a step that silently did nothing while claiming to run would be worse
		// than one that is plainly a placeholder.
		return func(c *fiber.Ctx) error { return c.Next() }
	default:
		return func(c *fiber.Ctx) error { return c.Next() }
	}
}

// authHandler selects a credential and resolves what it proves.
func authHandler(c *fiber.Ctx, d Deps) error {
	cred := Select(Presented{
		Authorization: c.Get(fiber.HeaderAuthorization),
		Cookie:        c.Cookies(SessionCookieName),
	}, publicRead(c))

	p := Principal{Kind: CredentialNone}
	if d.Principal != nil && cred.Kind != CredentialNone {
		if resolved, ok := d.Principal(cred); ok {
			p = resolved
		}
		// A credential that did not resolve leaves the request
		// unauthenticated. Whether that is a refusal is the route's decision,
		// which ACLScope makes: a public route still serves it.
	}
	c.Locals(string(KeyCredential), p)
	return c.Next()
}

// scopeHandler applies the matched route's requirement.
func scopeHandler(c *fiber.Ctx) error {
	m, ok := metaOf(c)
	if !ok {
		// No metadata means no route matched, so there is nothing to scope and
		// the 404 belongs to the router rather than to this step.
		return c.Next()
	}
	if err := Scope(m.req, principalOf(c)); err != nil {
		if errors.Is(err, ErrCredentialRequired) || errors.Is(err, ErrSessionRequired) {
			return fiber.NewError(fiber.StatusUnauthorized)
		}
		return fiber.NewError(fiber.StatusForbidden)
	}
	return c.Next()
}

// bodyLimitHandler refuses a body past its route's class before a handler can
// read it.
//
// The declared length is what is checked here, because refusing before reading
// is the point: a handler that streams still meets the same bound through
// LimitBody, but a client that announced an oversized body is told so without
// the server first accepting it.
func bodyLimitHandler(c *fiber.Ctx) error {
	m, ok := metaOf(c)
	if !ok {
		return c.Next()
	}
	bound, bounded := BodyBound(m.body)
	if !bounded {
		return c.Next()
	}
	if declared := int64(c.Request().Header.ContentLength()); declared > bound {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge)
	}
	return c.Next()
}

// csrfHandler checks the token on a mutating cookie-authenticated request.
func csrfHandler(c *fiber.Ctx, d Deps) error {
	p := principalOf(c)
	if !CSRFRequired(c.Method(), p.Kind) {
		return c.Next()
	}
	var key []byte
	if d.CSRFKey != nil {
		key = d.CSRFKey()
	}
	if len(key) == 0 {
		// A deployment with no key cannot verify, and serving the mutation
		// anyway would be the check silently not existing. Checked on the
		// value rather than only on the accessor, because HMAC accepts an
		// empty key without complaint: every deployment would then derive the
		// same token from nothing at all.
		return fiber.NewError(fiber.StatusForbidden)
	}
	if !CSRFValid(key, c.Cookies(SessionCookieName), c.Get(CSRFHeader)) {
		// Wrong, missing and cross-origin tokens answer identically: the
		// difference between them is information about the session.
		return fiber.NewError(fiber.StatusForbidden)
	}
	return c.Next()
}

// principalOf reads what Auth resolved.
func principalOf(c *fiber.Ctx) Principal {
	if p, ok := c.Locals(string(KeyCredential)).(Principal); ok {
		return p
	}
	return Principal{Kind: CredentialNone}
}

// originOf reads what the boundary settled on.
func originOf(c *fiber.Ctx) Origin {
	if o, ok := c.Locals(string(KeyOrigin)).(Origin); ok {
		return o
	}
	return OriginNone
}

// boundaryHandler admits or refuses, and records which origin admitted it.
func boundaryHandler(c *fiber.Ctx, d Deps) error {
	dec := Decide(d.Hosts(), BoundaryRequest{
		Host:       string(c.Request().Host()),
		Origin:     c.Get(fiber.HeaderOrigin),
		Method:     c.Method(),
		Client:     clientOf(c),
		CookieAuth: c.Cookies(SessionCookieName) != "",
		WebSocket:  isUpgrade(c),
	})
	if !dec.Admitted {
		// 421 asks the connection to close, which is what stops a client from
		// reusing a connection it opened for a name this deployment serves.
		//
		// Set on the response rather than as a header, because the error
		// handler rewrites the response and a header set here does not
		// survive it.
		c.Response().SetConnectionClose()
		return fiber.NewError(fiber.StatusMisdirectedRequest)
	}
	c.Locals(string(KeyOrigin), dec.Origin)
	return c.Next()
}

func resolveClient(c *fiber.Ctx, d Deps) netip.Addr {
	peer, err := netip.ParseAddr(c.IP())
	if err != nil {
		// A peer address the transport could not give is not a client. Keying
		// it as unroutable puts it in the shared bucket rather than minting one.
		return Unroutable()
	}
	return ClientAddr(peer, d.Trusted(),
		c.Get("CF-Connecting-IP"), c.Get(fiber.HeaderXForwardedFor))
}

// clientOf reads what TrustedProxy resolved, or the placeholder if that step
// has not run.
func clientOf(c *fiber.Ctx) netip.Addr {
	if v, ok := c.Locals(string(KeyClient)).(netip.Addr); ok {
		return v
	}
	return Unroutable()
}

// publicRead reports whether this route attempts only the session cookie.
func publicRead(c *fiber.Ctx) bool {
	m, ok := metaOf(c)
	return ok && m.req.Access == route.AccessPublic
}

// routeRequirementKey is where registration leaves the matched route's
// requirement for the chain to read. One key, set by the server.
const routeRequirementKey contextKey = "sc.route.requirement"

// SetRequirement is how the server attaches a route's metadata.
//
// The requirement and the body class travel together under one key. Two keys
// could be set apart, and a route whose class was forgotten would silently get
// the zero one, which is "no body" and would truncate every request to it.
func SetRequirement(c *fiber.Ctx, req route.Requirement, body route.BodyClass) {
	c.Locals(string(routeRequirementKey), routeMeta{req: req, body: body})
}

// routeMeta is what registration leaves for the chain.
type routeMeta struct {
	req  route.Requirement
	body route.BodyClass
}

// RequirementOf reports the matched route's requirement.
//
// Exported for the server's own tests, which check that registration attaches
// each route's own metadata rather than the last one's. Nothing in a handler
// needs it: the chain has already applied it by the time one runs.
func RequirementOf(c *fiber.Ctx) (route.Requirement, bool) {
	m, ok := metaOf(c)
	return m.req, ok
}

// metaOf reads the matched route's metadata, reporting whether a route matched
// at all.
func metaOf(c *fiber.Ctx) (routeMeta, bool) {
	m, ok := c.Locals(string(routeRequirementKey)).(routeMeta)
	return m, ok
}

func isUpgrade(c *fiber.Ctx) bool {
	return strings.EqualFold(c.Get(fiber.HeaderUpgrade), "websocket")
}
