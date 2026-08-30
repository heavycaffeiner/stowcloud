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
	"os"
	"regexp"

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

	// The repair door goes on first, before the chain. It is what an operator
	// reaches when the chain's own configuration is what is broken, so a door
	// behind that chain would be unreachable exactly when it is needed.
	e.mountEmergency(app)

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
		case "files.archive":
			out[r.Name] = e.filesArchive
		case "files.archive.list":
			out[r.Name] = e.filesArchiveList
		case "links.list":
			out[r.Name] = e.linksList
		case "links.create":
			out[r.Name] = e.linksCreate
		case "links.delete":
			out[r.Name] = e.linksDelete
		case "links.update":
			out[r.Name] = e.linksUpdate
		case "uploads.discover":
			out[r.Name] = e.uploadsDiscover
		case "uploads.discover.one":
			out[r.Name] = e.uploadsDiscoverOne
		case "uploads.create":
			out[r.Name] = e.uploadsCreate
		case "uploads.status":
			out[r.Name] = e.uploadsStatus
		case "uploads.patch":
			out[r.Name] = e.uploadsPatch
		case "uploads.abort":
			out[r.Name] = e.uploadsAbort
		case "trash.list":
			out[r.Name] = e.trashList
		case "trash.restore":
			out[r.Name] = e.trashRestore
		case "trash.purge":
			out[r.Name] = e.trashPurge
		case "admin.users.list":
			out[r.Name] = e.adminUsersList
		case "admin.users.create":
			out[r.Name] = e.adminUsersCreate
		case "admin.users.update":
			out[r.Name] = e.adminUsersUpdate
		case "admin.users.delete":
			out[r.Name] = e.adminUsersDelete
		case "admin.groups.list":
			out[r.Name] = e.adminGroupsList
		case "admin.groups.create":
			out[r.Name] = e.adminGroupsCreate
		case "admin.groups.update":
			out[r.Name] = e.adminGroupsUpdate
		case "admin.groups.delete":
			out[r.Name] = e.adminGroupsDelete
		case "admin.groups.members.add":
			out[r.Name] = e.adminGroupMemberAdd
		case "admin.groups.members.remove":
			out[r.Name] = e.adminGroupMemberRemove
		case "admin.audit":
			out[r.Name] = e.adminAudit
		case "admin.settings.get":
			out[r.Name] = e.adminSettingsGet
		case "admin.settings.patch":
			out[r.Name] = e.adminSettingsPatch
		case "admin.storage":
			out[r.Name] = e.adminStorage
		case "admin.index.estimate":
			out[r.Name] = e.adminIndexEstimate
		case "admin.index.build":
			out[r.Name] = e.adminIndexBuild
		case "admin.smb.apply":
			out[r.Name] = e.adminSMBApply
		case "events":
			out[r.Name] = e.eventsSocket()
		case "system.setup.get":
			out[r.Name] = e.systemSetupGet
		case "system.setup.post":
			out[r.Name] = e.systemSetupPost
		case "files.thumbnail":
			out[r.Name] = e.filesThumbnail
		case "search.stream":
			out[r.Name] = e.searchStream
		case "auth.oidc.config":
			out[r.Name] = e.authOIDCConfig
		case "auth.oidc.start":
			out[r.Name] = e.authOIDCStart
		case "auth.oidc.callback":
			out[r.Name] = e.authOIDCCallback
		case "account.oidc-link.start":
			out[r.Name] = e.accountOIDCLinkStart
		case "account.oidc-link.delete":
			out[r.Name] = e.accountOIDCLinkDelete
		case "account.smb.create":
			out[r.Name] = e.accountSMBCreate
		case "account.smb.password.set":
			out[r.Name] = e.accountSMBPasswordSet
		case "account.smb.password.delete":
			out[r.Name] = e.accountSMBPasswordDelete
		case "admin.users.oidc.get":
			out[r.Name] = e.adminUserOIDCGet
		case "admin.users.oidc.delete":
			out[r.Name] = e.adminUserOIDCDelete
		case "admin.shares.list":
			out[r.Name] = e.adminSharesList
		case "admin.shares.create":
			out[r.Name] = e.adminSharesCreate
		case "admin.shares.update":
			out[r.Name] = e.adminSharesUpdate
		case "admin.shares.retry":
			out[r.Name] = e.adminSharesRetry
		case "admin.shares.delete":
			out[r.Name] = e.adminSharesDelete
		case "admin.grants.list":
			out[r.Name] = e.adminGrantsList
		case "admin.grants.create":
			out[r.Name] = e.adminGrantsCreate
		case "admin.grants.update":
			out[r.Name] = e.adminGrantsUpdate
		case "admin.grants.delete":
			out[r.Name] = e.adminGrantsDelete

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

// UnboundRoutesForTest names every route the table declares that the binding
// switch does not handle.
//
// Exported for a test because the fallback became unobservable when the last
// route was bound: no request reaches it any more, so asking which names would
// is the only way left to check that none do.
//
// It reads the switch's case labels rather than the handler map. Every name in
// that map is bound to something, since the ones the switch does not name get
// the fallback, and a lookup cannot tell the two apart.
func UnboundRoutesForTest() []string {
	src, err := os.ReadFile("mount.go")
	if err != nil {
		// The caller is a test in this package, so the file is beside it.
		return []string{"mount.go could not be read: " + err.Error()}
	}
	bound := map[string]struct{}{}
	for _, m := range regexp.MustCompile(`case "([a-z0-9.-]+)":`).FindAllSubmatch(src, -1) {
		bound[string(m[1])] = struct{}{}
	}

	var out []string
	for _, r := range server.Table() {
		if _, ok := bound[r.Name]; !ok {
			out = append(out, r.Name)
		}
	}
	return out
}
