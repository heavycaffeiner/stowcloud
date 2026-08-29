//go:build linux

// Serving the constructed engine.
//
// This is where the pieces stop being independent: the route table, the
// middleware chain and the projections are joined to real services and put
// behind a socket.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
)

// Mount builds the Fiber application over a constructed engine.
//
// The preflight check runs before anything binds, so a route with no handler,
// a chain missing a step and a forgotten sweep are all reported at once at a
// point where failing costs nothing.
func (e *Engine) Mount() (*fiber.App, error) {
	table := server.Table()
	handlers := e.handlers(table)

	if err := server.Check(server.Preflight{
		Routes:   table,
		Roots:    []string{server.Base},
		Chain:    middleware.Chain(),
		Tasks:    e.tasks(),
		Handlers: handlers,
	}); err != nil {
		return nil, fmt.Errorf("the assembly is not servable: %w", err)
	}

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		// The framework's own error page is HTML. Every failure this server
		// produces is a JSON body a client can read, so the default is
		// replaced rather than left to leak a page into an API response.
		ErrorHandler: writeError,
	})

	// The chain goes on before the routes. Fiber runs what was mounted in
	// mount order, so a step registered after a route never sees a request
	// that route answers: the boundary, the limiter and the credential check
	// would all be skipped for exactly the paths they exist to guard.
	if err := middleware.Mount(app, middleware.Chain(), e.deps(), nil); err != nil {
		return nil, fmt.Errorf("mounting the chain: %w", err)
	}

	if err := server.Register(app, table, handlers); err != nil {
		return nil, fmt.Errorf("registering routes: %w", err)
	}
	return app, nil
}

// handlers binds a projection to every route the table names.
//
// Each entry is a real function rather than a stub that returns 501: a route
// registered to something that cannot answer is worse than one that is absent,
// because a client discovers it and then fails.
func (e *Engine) handlers(table []route.Route) server.Handlers {
	out := make(server.Handlers, len(table))

	for _, r := range table {
		switch r.Name {
		case "system.health":
			out[r.Name] = e.health
		case "jobs.list":
			out[r.Name] = e.jobsList
		case "jobs.get":
			out[r.Name] = e.jobsGet
		case "jobs.cancel":
			out[r.Name] = e.jobsCancel

		default:
			// Every other route is named by the table and has no binding yet.
			// It answers with the one honest thing available: this build does
			// not serve it. A client reads a refusal rather than a hang.
			name := r.Name
			out[r.Name] = func(c *fiber.Ctx) error {
				return writeJSON(c, fiber.StatusNotImplemented, map[string]string{
					"error":   "not_implemented",
					"message": "this build does not serve " + name,
				})
			}
		}
	}
	return out
}

// health answers the probe.
func (e *Engine) health(c *fiber.Ctx) error {
	// A degradation is a real state rather than a failure: the server answers
	// and says what is wrong, so a supervisor does not restart a deployment
	// whose configuration is the problem.
	var reasons []handler.HealthReason
	status := handler.HealthOK
	if e.Journal == nil {
		status = handler.HealthDegraded
		reasons = append(reasons, handler.ReasonJournalDatabase)
	}

	return writeJSON(c, fiber.StatusOK, handler.HealthOf(status, reasons))
}

// writeJSON sends a value as an API response.
func writeJSON(c *fiber.Ctx, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding a response: %w", err)
	}
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Status(status).Send(body)
}

// writeError renders a failure as JSON rather than the framework's HTML page.
//
// The body says only that the request failed. What went wrong is logged, not
// returned: an error string built from an internal failure names paths, table
// columns and library internals to whoever sent the request.
func writeError(c *fiber.Ctx, err error) error {
	status := fiber.StatusInternalServerError

	var fe *fiber.Error
	if errors.As(err, &fe) {
		status = fe.Code
	}
	return writeJSON(c, status, map[string]string{"error": "request_failed"})
}
