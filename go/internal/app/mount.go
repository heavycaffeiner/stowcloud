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
	if err := server.Bind(app, server.Binding{
		Routes: table, Roots: []string{server.Base}, Chain: middleware.Chain(),
		Tasks: e.tasks(), Handlers: handlers, Deps: e.deps(),
		StartTasks:     e.startTasks,
		BeforeAnnounce: func(router *gin.Engine) { e.mountEmergency(router) },
		AfterAnnounce:  func(router *gin.Engine) { e.declarePublicLinks(router) },
	}); err != nil {
		return err
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
		case "auth.login", "auth.login.totp", "auth.session", "auth.logout":
			// Bound by the transport adapter after the product routes are enumerated.
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
		case "account.sessions.list", "account.app-passwords.list", "account.app-passwords.delete",
			"account.app-passwords.create", "account.app-passwords.wipe", "account.password",
			"account.sessions.delete", "account.totp.setup", "account.totp.enroll",
			"account.totp.disable", "account.totp.recovery-codes.list", "account.totp.recovery-codes.create":
			// Bound by the account transport adapter.
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
		case "admin.users.list", "admin.users.create", "admin.users.update", "admin.users.delete",
			"admin.groups.list", "admin.groups.create", "admin.groups.update", "admin.groups.delete",
			"admin.groups.members.add", "admin.groups.members.remove", "admin.audit":
			// Bound by the administrator transport adapter.
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
			// Bound by the host filesystem transport adapter.
		case "events":
			out[r.Name] = e.eventsSocket()
		case "system.setup.get":
			out[r.Name] = e.systemSetupGet
		case "system.setup.post":
			out[r.Name] = e.systemSetupPost
		case "system.setup.browse":
			// Bound by the host filesystem transport adapter.
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

		}
	}
	for name, h := range handler.NewAuthHandlers(handler.AuthHandlersDeps{
		Service: e.Auth, Clock: e.clock, CSRFKey: e.csrfKey,
		TOTPAllow: e.totpLimiter, SessionDetails: e.authSessionDetails,
		OIDCEndSessionURL: e.oidcEndSessionURL,
	}) {
		out[name] = h
	}
	for name, h := range handler.NewAccountHandlers(handler.AccountHandlersDeps{
		Service: e.Auth, Clock: e.clock,
	}) {
		out[name] = h
	}
	for name, h := range handler.NewAdminUsersHandlers(handler.AdminUsersDeps{
		Auth: e.Auth, CleanupHome: e.Core.CleanupHome, Logger: e.logger,
	}) {
		out[name] = h
	}
	fsDeps := handler.AdminFSDeps{
		Auth: e.Auth, Core: e.Core, DataDir: e.dataDir,
		SetupRefusal: setupRefusal,
	}
	if e.setup != nil {
		fsDeps.SetupVerify = e.setup.Verify
	}
	for name, h := range handler.NewAdminFSHandlers(fsDeps) {
		out[name] = h
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
