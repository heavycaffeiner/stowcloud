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
		case "auth.login":
			out[r.Name] = e.login
		case "auth.login.totp":
			out[r.Name] = e.loginTOTP
		case "auth.session":
			out[r.Name] = e.session
		case "auth.logout":
			out[r.Name] = e.logout
		case "jobs.list":
			out[r.Name] = e.jobsList
		case "jobs.get":
			out[r.Name] = e.jobsGet
		case "jobs.cancel":
			out[r.Name] = e.jobsCancel
		case "account.sessions.list":
			out[r.Name] = e.accountSessions
		case "account.app-passwords.list":
			out[r.Name] = e.accountAppPasswords
		case "account.app-passwords.delete":
			out[r.Name] = e.accountAppPasswordDelete
		case "account.app-passwords.create":
			out[r.Name] = e.accountAppPasswordCreate
		case "account.app-passwords.wipe":
			out[r.Name] = e.accountAppPasswordWipe
		case "account.password":
			out[r.Name] = e.accountPassword
		case "account.sessions.delete":
			out[r.Name] = e.accountSessionDelete
		case "account.totp.setup":
			out[r.Name] = e.accountTOTPSetup
		case "account.totp.enroll":
			out[r.Name] = e.accountTOTPEnroll
		case "account.totp.disable":
			out[r.Name] = e.accountTOTPDisable
		case "account.totp.recovery-codes.list":
			out[r.Name] = e.accountRecoveryCodesList
		case "account.totp.recovery-codes.create":
			out[r.Name] = e.accountRecoveryCodesCreate
		case "files.list":
			out[r.Name] = e.filesList
		case "files.stat":
			out[r.Name] = e.filesStat
		case "files.mkdir":
			out[r.Name] = e.filesMkdir
		case "files.delete":
			out[r.Name] = e.filesDelete
		case "files.rename":
			out[r.Name] = e.filesRename
		case "files.read":
			out[r.Name] = e.filesRead
		case "files.write":
			out[r.Name] = e.filesWrite
		case "files.move":
			out[r.Name] = e.filesMove
		case "files.copy":
			out[r.Name] = e.filesCopy
		case "files.size":
			out[r.Name] = e.filesSize
		case "files.recent":
			out[r.Name] = e.filesRecent
		case "links.list":
			out[r.Name] = e.linksList
		case "links.create":
			out[r.Name] = e.linksCreate
		case "links.delete":
			out[r.Name] = e.linksDelete
		case "trash.list":
			out[r.Name] = e.trashList
		case "trash.restore":
			out[r.Name] = e.trashRestore
		case "trash.purge":
			out[r.Name] = e.trashPurge

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
