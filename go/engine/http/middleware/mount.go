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
		return func(c *fiber.Ctx) error {
			cred := Select(Presented{
				Authorization: c.Get(fiber.HeaderAuthorization),
				Cookie:        c.Cookies(SessionCookieName),
			}, publicRead(c))
			c.Locals(string(KeyCredential), cred.Kind)
			return c.Next()
		}
	case StepRequestID, StepSecurityHeaders, StepBodyLimit,
		StepCSRF, StepACLScope, StepAuditSink, StepErrorMapper, StepUnset:
		// Not yet implemented here. Passing through is deliberate and visible:
		// a step that silently did nothing while claiming to run would be worse
		// than one that is plainly a placeholder.
		return func(c *fiber.Ctx) error { return c.Next() }
	default:
		return func(c *fiber.Ctx) error { return c.Next() }
	}
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
	req, ok := c.Locals(string(routeRequirementKey)).(route.Requirement)
	return ok && req.Access == route.AccessPublic
}

// routeRequirementKey is where registration leaves the matched route's
// requirement for the chain to read. One key, set by the server.
const routeRequirementKey contextKey = "sc.route.requirement"

// SetRequirement is how the server attaches a route's metadata.
func SetRequirement(c *fiber.Ctx, req route.Requirement) {
	c.Locals(string(routeRequirementKey), req)
}

func isUpgrade(c *fiber.Ctx) bool {
	return strings.EqualFold(c.Get(fiber.HeaderUpgrade), "websocket")
}
