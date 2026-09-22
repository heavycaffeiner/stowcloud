//go:build linux

// Serving the constructed engine.
//
// This is where the pieces stop being independent: the route table, the
// middleware chain and the projections are joined to real services and put
// behind a socket.
package app

import (
	"fmt"
	"net/http"
	"os"
	"regexp"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
)

// Mount assembles the native Gin router over a constructed engine.
//
// Hanami owns engine construction and listener generations. Stowcloud owns
// route topology, product middleware, and protocol adapters.
func (e *Engine) Mount(app *gin.Engine) error {
	if app == nil {
		return fmt.Errorf("mounting routes: Gin engine is nil")
	}
	table := server.Table()
	handlers := e.handlers(table)

	if err := server.Check(server.Preflight{
		Routes:   table,
		Roots:    []string{server.Base},
		Chain:    middleware.Chain(),
		Tasks:    e.tasks(),
		Handlers: handlers,
	}); err != nil {
		return fmt.Errorf("the assembly is not servable: %w", err)
	}

	e.startTasks()

	e.mountEmergency(app)
	server.Announce(app, table)
	e.declarePublicLinks(app)
	if err := middleware.Mount(app, middleware.Chain(), e.deps(), nil); err != nil {
		return fmt.Errorf("mounting the chain: %w", err)
	}
	if err := server.Register(app, table, handlers); err != nil {
		return fmt.Errorf("registering routes: %w", err)
	}
	e.mountDav(app)
	e.mountPublicLinks(app)
	e.mountNCTagged(app)
	if err := e.mountFrontend(app); err != nil {
		return err
	}
	return nil
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
		case "jobs.retry":
			out[r.Name] = e.jobsRetry
		case "jobs.pause":
			out[r.Name] = e.jobsPause
		case "jobs.resume":
			out[r.Name] = e.jobsResume
		case "direct-uploads.create":
			out[r.Name] = e.directUploadCreate
		case "direct-uploads.status":
			out[r.Name] = e.directUploadStatus
		case "direct-uploads.part":
			out[r.Name] = e.directUploadPart
		case "direct-uploads.complete":
			out[r.Name] = e.directUploadComplete
		case "direct-uploads.cancel":
			out[r.Name] = e.directUploadCancel
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
		case "files.archive.fetch":
			out[r.Name] = e.filesArchiveFetch
		case "files.archive.list":
			out[r.Name] = e.filesArchiveList
		case "files.download":
			out[r.Name] = e.filesDownload
		case "files.download.fetch":
			out[r.Name] = e.filesDownloadFetch
		case "links.list":
			out[r.Name] = e.linksList
		case "admin.links.list":
			out[r.Name] = e.adminLinksList
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
		case "admin.logs.list":
			out[r.Name] = e.adminLogsList
		case "admin.logs.timeline":
			out[r.Name] = e.adminLogsTimeline
		case "admin.settings.get":
			out[r.Name] = e.adminSettingsGet
		case "admin.oidc.endpoints":
			out[r.Name] = e.adminOIDCEndpoints
		case "admin.settings.patch":
			out[r.Name] = e.adminSettingsPatch
		case "admin.system.restart":
			out[r.Name] = e.systemRestart
		case "admin.storage":
			out[r.Name] = e.adminStorage
		case "admin.index.estimate":
			out[r.Name] = e.adminIndexEstimate
		case "admin.index.status":
			out[r.Name] = e.adminIndexStatus
		case "admin.index.build":
			out[r.Name] = e.adminIndexBuild
		case "admin.smb.apply":
			out[r.Name] = e.adminSMBApply
		case "admin.fs.browse":
			out[r.Name] = e.adminFsBrowse
		case "events":
			out[r.Name] = e.eventsSocket()
		case "system.setup.get":
			out[r.Name] = e.systemSetupGet
		case "system.setup.post":
			out[r.Name] = e.systemSetupPost
		case "system.setup.browse":
			out[r.Name] = e.setupFsBrowse
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
		case "account.roots.order":
			out[r.Name] = e.accountRootsOrder
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
		case "encryption.list":
			out[r.Name] = e.shareEncryptionList
		case "admin.encryption.enable":
			out[r.Name] = e.shareEncryptionEnable
		case "admin.encryption.disable":
			out[r.Name] = e.shareEncryptionDisable

		default:
			// Every other route is named by the table and has no binding yet.
			// It answers with the one honest thing available: this build does
			// not serve it. A client reads a refusal rather than a hang.
			name := r.Name
			out[r.Name] = func(c *gin.Context) {
				writeJSON(c, http.StatusNotImplemented, map[string]string{
					"error":   "not_implemented",
					"message": "this build does not serve " + name,
				})
			}
		}
	}
	return out
}

// health answers the probe.
//
// Only to a client on a private network, which is what the container's own
// check is: the probe runs inside the deployment and reaches the server over
// the loopback. To anyone else the address is not there, for the reason the
// rest of this API is not there to them. Liveness is a small thing to leak,
// and a surface that answers one stranger answers every scanner.
func (e *Engine) health(c *gin.Context) {
	if !middleware.IsPrivateClient(middleware.ClientOf(c)) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var reasons []handler.HealthReason
	status := handler.HealthOK
	if e.Journal == nil {
		status = handler.HealthDegraded
		reasons = append(reasons, handler.ReasonJournalDatabase)
	}

	h := handler.HealthOf(status, reasons)
	h.Revision = e.Revision
	writeJSON(c, http.StatusOK, h)
}

// writeJSON sends a value as an API response.
func writeJSON(c *gin.Context, status int, value any) {
	c.JSON(status, value)
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
