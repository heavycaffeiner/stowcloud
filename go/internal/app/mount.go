//go:build linux

// Serving the constructed engine.
//
// This is where the pieces stop being independent: the route table, the
// middleware chain and the projections are joined to real services and put
// behind a socket.
package app

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	accountapp "github.com/heavycaffeiner/stowcloud/go/internal/app/account"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	featureoidc "github.com/heavycaffeiner/stowcloud/go/internal/feature/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/smb/agent"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminlogs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminsettings"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminshares"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminsmb"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminstorage"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/directtransfer"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/encryption"
	filehttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/jobs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/links"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/oidc"
	previewhttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/setup"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/smbaccount"
	trashhttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/trash"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/uploads"
)

// Mount assembles the native Gin router over a constructed engine.
//
// Hanami owns engine construction and listener generations. Stowcloud owns
// route topology, product middleware, and protocol adapters.
func (e *Engine) Mount(app *gin.Engine) error {
	if app == nil {
		return fmt.Errorf("mounting routes: Gin engine is nil")
	}
	e.publicLinks = e.newPublicLinks()
	table := server.Table()
	handlers := e.handlers(table)
	if err := server.Bind(app, server.Binding{
		Routes: table, Roots: []string{server.Base}, Chain: middleware.Chain(),
		Tasks: e.tasks(), Handlers: handlers, Deps: e.deps(),
		StartTasks:     e.startTasks,
		BeforeAnnounce: func(router *gin.Engine) { e.mountEmergency(router) },
		AfterAnnounce: func(router *gin.Engine) {
			e.publicLinks.Declare(router, links.PublicLinkPrefix)
			e.declarePublicLinkAliases(router)
		},
	}); err != nil {
		return err
	}
	e.mountDav(app)
	e.publicLinks.Mount(app)
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
		case "jobs.list", "jobs.get", "jobs.cancel", "jobs.retry", "jobs.pause", "jobs.resume":
			// Bound by the jobs transport adapter.
		case "direct-uploads.create", "direct-uploads.status", "direct-uploads.part",
			"direct-uploads.complete", "direct-uploads.cancel":
		// Bound by the direct transfer transport adapter.
		case "account.sessions.list", "account.app-passwords.list", "account.app-passwords.delete",
			"account.app-passwords.create", "account.app-passwords.wipe", "account.password",
			"account.sessions.delete", "account.totp.setup", "account.totp.enroll",
			"account.totp.disable", "account.totp.recovery-codes.list", "account.totp.recovery-codes.create":
			// Bound by the account transport adapter.
		case "files.list", "files.stat", "files.mkdir", "files.delete", "files.rename",
			"files.read", "files.write", "files.move", "files.copy", "files.size",
			"files.recent", "files.archive", "files.archive.fetch", "files.archive.list",
			"files.download", "files.download.fetch", "links.list", "admin.links.list",
			"links.create", "links.delete", "links.update", "uploads.discover",
			"uploads.discover.one", "uploads.create", "uploads.status", "uploads.patch", "uploads.abort":
			// Bound by the file, link and upload transport adapters.
		case "trash.list", "trash.restore", "trash.purge":
			// Bound by the trash transport adapter.
		case "admin.users.list", "admin.users.create", "admin.users.update", "admin.users.delete",
			"admin.groups.list", "admin.groups.create", "admin.groups.update", "admin.groups.delete",
			"admin.groups.members.add", "admin.groups.members.remove", "admin.audit":
			// Bound by the administrator transport adapter.
		case "admin.logs.list", "admin.logs.timeline":
			// Bound by the administrative logs adapter.
		case "admin.settings.get", "admin.settings.patch", "admin.system.restart",
			"admin.oidc.endpoints":
			// Bound by the settings and OIDC transport adapters.
		case "admin.storage":
			// Bound by the administrator storage adapter.
		case "admin.index.estimate":
			out[r.Name] = e.searchRuntime.IndexEstimate
		case "admin.index.status":
			out[r.Name] = e.searchRuntime.IndexStatus
		case "admin.index.build":
			out[r.Name] = e.searchRuntime.IndexBuild
		case "admin.smb.apply":
			// Bound by the administrator SMB adapter.
		case "admin.fs.browse":
			// Bound by the host filesystem transport adapter.
		case "events":
			out[r.Name] = e.eventsSocket()
		case "system.setup.get", "system.setup.post", "system.setup.browse":
			// Bound by the first-run setup adapter and setup gate.
		case "files.thumbnail":
			out[r.Name] = previewhttp.ThumbnailHandler(previewhttp.ThumbnailDeps{
				Core: e.Core, Owner: ownerOf, Resolve: e.resolve, OpenClaim: e.openBoundClaim,
				PreviewLease: e.previewLease, Fail: fail, Refuse: refuse, Logger: e.logger,
			})
		case "search.stream":
			out[r.Name] = e.searchRuntime.SearchStream
		case "auth.oidc.config", "auth.oidc.start", "auth.oidc.callback",
			"account.oidc-link.start", "account.oidc-link.delete":
			// Bound by the OIDC transport adapter.
		case "account.smb.create", "account.smb.password.set", "account.smb.password.delete":
			// Bound by the account SMB adapter.
		case "account.roots.order":
			out[r.Name] = accountapp.RootOrderHandler(accountapp.RootOrderDeps{
				State: e.State, Owner: func(c *gin.Context) (int64, bool) {
					owner, ok := ownerOf(c)
					return int64(owner), ok
				}, Fail: fail, Refuse: refuse, Decode: decodeBody,
			})
		case "admin.users.oidc.get", "admin.users.oidc.delete":
			// Bound by the OIDC transport adapter.
		case "admin.shares.list", "admin.shares.create", "admin.shares.update",
			"admin.shares.retry", "admin.shares.delete", "admin.grants.list",
			"admin.grants.create", "admin.grants.update", "admin.grants.delete":
		// Bound by the administrator share transport adapter.
		case "encryption.list", "admin.encryption.enable", "admin.encryption.disable":
			// Bound by the share encryption adapter.

		}
	}
	nativeLinks := links.NewNative(links.NativeDeps{
		Core: e.Core, Auth: e.Auth, Owner: ownerOf, Admin: e.admin,
		Resolve: e.resolve, Now: e.now, Decode: decodeBody,
		VpathOf: func(l core.Link) string {
			vp, err := e.Core.VpathFor(l.Owner, l.Share, l.Path)
			if err != nil {
				return ""
			}
			return vp.String()
		},
		Fail: fail, Refuse: refuse, NotFound: notFound, WriteJSON: writeJSON,
	})
	for name, h := range map[string]gin.HandlerFunc{
		"links.list": nativeLinks.List, "admin.links.list": nativeLinks.AdminList,
		"links.create": nativeLinks.Create, "links.update": nativeLinks.Update,
		"links.delete": nativeLinks.Delete,
	} {
		out[name] = h
	}
	filesHandler := filehttp.NewHandler(filehttp.Deps{
		Core: e.Core, Archives: e.Archives, Gate: e.archiveGate,
		Owner: ownerOf, Resolve: e.resolve, OpenClaim: e.openBoundClaim,
		EntryView: e.entryView, Vpath: e.vpath, Refs: e.refsOf,
		Fail: fail, Refuse: refuse, NotFound: notFound, Decode: decodeBody,
		Body: requestBodyReader, GuardLock: e.guardDavLock,
		Now: e.clk().Now, Journal: e.Journal != nil, Logger: e.log(),
	})
	for name, h := range map[string]gin.HandlerFunc{
		"files.list": filesHandler.List, "files.stat": filesHandler.Stat,
		"files.read": filesHandler.Read, "files.write": filesHandler.Write,
		"files.mkdir": filesHandler.Mkdir, "files.delete": filesHandler.Delete,
		"files.rename": filesHandler.Rename, "files.move": filesHandler.Move,
		"files.copy": filesHandler.Copy, "files.size": filesHandler.Size,
		"files.recent": filesHandler.Recent, "files.archive": filesHandler.Archive,
		"files.archive.fetch": filesHandler.ArchiveFetch, "files.archive.list": filesHandler.ArchiveList,
		"files.download": filesHandler.Download, "files.download.fetch": filesHandler.DownloadFetch,
	} {
		out[name] = h
	}
	oidcRoutes := oidc.New(oidc.Deps{
		Auth:        e.Auth,
		Client:      func() *featureoidc.Client { e.settingsMu.RLock(); defer e.settingsMu.RUnlock(); return e.oidcClient },
		DisplayName: func() string { e.settingsMu.RLock(); defer e.settingsMu.RUnlock(); return e.oidcName },
		AppHosts:    func() []string { return e.Settings.Hosts().App },
		Logger:      e.log(),
		Owner:       func(c *gin.Context) (int64, bool) { owner, ok := ownerOf(c); return int64(owner), ok },
		Admin:       e.admin, Reconfirm: e.reconfirm, Decode: decodeBody,
		Fail: fail, FailKnown: failKnown, Refuse: refuse, WriteJSON: writeJSON,
		ClientAddr: clientAddr, SetSessionCookie: e.setSessionCookie,
	})
	for name, h := range oidcRoutes.Routes() {
		out[name] = h
	}
	for name, h := range handler.NewAuthHandlers(handler.AuthHandlersDeps{
		Service: e.Auth, Clock: e.clock, CSRFKey: e.csrfKey,
		TOTPAllow: e.totpLimiter, SessionDetails: e.authSessionDetails,
		OIDCEndSessionURL: oidcRoutes.EndSessionURL,
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
	for name, h := range adminshares.NewHandlers(adminshares.Deps{
		Core: e.Core, Auth: e.Auth, MarkSearchIncomplete: e.searchRuntime.MarkIncomplete,
		WatchShare: e.watchShare, UnwatchShare: e.unwatchShare, Logger: e.logger,
	}) {
		out[name] = h
	}
	transfer := directtransfer.NewHandler(directtransfer.Deps{
		State: e.State, Owner: ownerOf, Resolve: e.resolve,
		ShareEncrypted: e.Core.ShareEncrypted, GuardLock: e.guardDavLock,
		ProviderForRow: e.directProviderForRow, RevalidateDestination: e.revalidateDirectDestination,
		Now: e.now, Decode: decodeBody, Fail: fail, Refuse: refuse, NotFound: notFound,
		Logger: e.log(),
	})
	out["direct-uploads.create"] = transfer.CreateHandler
	out["direct-uploads.status"] = transfer.StatusHandler
	out["direct-uploads.part"] = transfer.PartHandler
	out["direct-uploads.complete"] = transfer.CompleteHandler
	out["direct-uploads.cancel"] = transfer.CancelHandler
	uploadRoutes := uploads.NewHandlers(uploads.Deps{
		Upload: e.Upload, Core: e.Core, Resolve: e.resolve,
		Owner: ownerOf, Admin: e.admin, Fail: fail, Refuse: refuse,
		Decode: decodeBody, WriteJSON: writeJSON,
	})
	for name, h := range uploadRoutes {
		if name != "admin.settings.upload" {
			out[name] = h
		}
	}
	for name, h := range adminsettings.NewHandlers(adminsettings.Deps{
		State: e.State, Auth: e.Auth, Settings: e.Settings,
		DataDir: e.dataDir, Hardening: e.hardening, Admin: e.admin,
		UploadPatch:  uploadRoutes["admin.settings.upload"],
		SMBAgentView: e.smbAgentView, PublishSMB: e.publishSMBSettings,
		OnRestart: e.Restart.Request, Logger: e.log(),
	}) {
		out[name] = h
	}
	for name, h := range jobs.NewHandlers(jobs.Deps{Core: e.Core, State: e.State, Owner: ownerOf, StartJobs: e.Core.StartJobs, NowNs: e.now}) {
		out[name] = h
	}
	for name, h := range adminlogs.NewHandlers(adminlogs.Deps{Logs: e.Logs, Auth: e.Auth, Admin: e.admin, Fail: failKnown, Refuse: refuse}) {
		out[name] = h
	}
	trashHandler := trashhttp.NewHandler(trashhttp.Deps{Core: e.Core, Owner: ownerOf, Resolve: e.resolve, Decode: decodeBody, Fail: fail, Refuse: refuse})
	out["trash.list"], out["trash.restore"], out["trash.purge"] = trashHandler.List, trashHandler.Restore, trashHandler.Purge
	for name, h := range smbaccount.NewHandlers(smbaccount.Deps{Auth: e.Auth}) {
		out[name] = h
	}
	for name, h := range adminsmb.NewHandlers(adminsmb.Deps{Auth: e.Auth, Apply: func(ctx context.Context) (agent.Report, bool, error) {
		p := e.smbPublisherOf()
		if p == nil {
			return agent.Report{}, false, nil
		}
		r, err := p.Publish(ctx)
		return r, true, err
	}, Logger: e.log()}) {
		out[name] = h
	}
	for name, h := range encryption.NewHandlers(encryption.Deps{Core: e.Core, Auth: e.Auth}) {
		out[name] = h
	}
	for name, h := range adminstorage.NewHandlers(adminstorage.Deps{Core: e.Core, Auth: e.Auth, State: e.State}) {
		out[name] = h
	}
	for name, h := range setup.NewHandlers(setup.Deps{
		Auth: e.Auth, State: e.State, Gate: e.setup,
		GrantEveryShare: e.Core.GrantEveryShare, CreateShare: e.Core.CreateShare,
		Apply: e.Settings.Load, DataDir: e.dataDir, Logger: e.log(),
	}) {
		out[name] = h
	}
	fsDeps := handler.AdminFSDeps{
		Auth: e.Auth, Core: e.Core, DataDir: e.dataDir,
		SetupRefusal: setup.Refusal,
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
