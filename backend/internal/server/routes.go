//go:build linux

package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	adminhttp "github.com/heavycaffeiner/stowcloud/backend/internal/admin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/dav"
	"github.com/heavycaffeiner/stowcloud/backend/internal/emergency"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	jobshttp "github.com/heavycaffeiner/stowcloud/backend/internal/jobs"
	featureoidc "github.com/heavycaffeiner/stowcloud/backend/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/preview"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/httpx"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	links "github.com/heavycaffeiner/stowcloud/backend/internal/shares"
	adminsmb "github.com/heavycaffeiner/stowcloud/backend/internal/smb"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb/agent"
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
	"github.com/heavycaffeiner/stowcloud/backend/internal/web"
)

// routes registers every route. Paths are written in full so a search for a
// URL lands on the line that serves it.
func (e *Engine) routes(router *gin.Engine) error {
	// The emergency door is mounted before the chain: it must answer when the
	// rest of the server cannot.
	emergency.Mount(router, emergency.Deps{
		Auth: emergency.NewAuthenticator(e.Auth, e.clk().Nanos), State: e.State, Settings: e.Settings,
		Page: web.Page(), DataDir: e.dataDir, Reason: func() string { return "" },
		ClientAddr: emergency.ClientAddr(e.trustedProxies), TrustedProxies: e.trustedProxies,
	})
	deps := e.deps()
	global, err := middleware.Global(deps)
	if err != nil {
		return fmt.Errorf("mounting routes: %w", err)
	}
	router.Use(global...)

	public := router.Group("", middleware.Public(deps)...)
	session := router.Group("", middleware.Session(deps)...)
	admin := session.Group("", e.requireAdmin)
	device := router.Group("", middleware.Device(deps)...)
	config := humaConfig()
	publicAPI := newTyped(router, public, config, e.errs, true)
	sessionAPI := newTyped(router, session, config, e.errs, false)
	adminAPI := newTyped(router, admin, config, e.errs, false)

	resolve := files.Resolve(e.Core)

	op(publicAPI, http.MethodGet, "/api/v1/system/health", "system.health", e.health)
	setupRoutes := adminhttp.NewSetupHandlers(adminhttp.SetupDeps{
		Auth: e.Auth, State: e.State, Gate: e.setup,
		GrantEveryShare: e.Core.GrantEveryShare, CreateShare: e.Core.CreateShare,
		Apply: e.Settings.Load, DataDir: e.dataDir, Logger: e.logger,
	})
	op(publicAPI, http.MethodGet, "/api/v1/system/setup", "system.setup.state", setupRoutes.Get)
	op(publicAPI, http.MethodPost, "/api/v1/system/setup", "system.setup.complete", setupRoutes.Post)
	fsDeps := adminhttp.AdminFSDeps{Auth: e.Auth, Core: e.Core, DataDir: e.dataDir}
	if e.setup != nil {
		fsDeps.SetupVerify = e.setup.Verify
	}
	adminFS := adminhttp.NewAdminFSHandlers(fsDeps)
	op(publicAPI, http.MethodPost, "/api/v1/system/setup/browse", "system.setup.browse", adminFS.SetupBrowse)
	op(adminAPI, http.MethodGet, "/api/v1/admin/fs", "admin.fs.browse", adminFS.Browse)
	session.GET("/api/v1/events", e.eventsSocket())

	oidcRoutes := featureoidc.NewHandlers(featureoidc.Deps{
		Auth:        e.Auth,
		Client:      func() *featureoidc.Client { e.settingsMu.RLock(); defer e.settingsMu.RUnlock(); return e.oidcClient },
		DisplayName: func() string { e.settingsMu.RLock(); defer e.settingsMu.RUnlock(); return e.oidcName },
		AppHosts:    func() []string { return e.Settings.Hosts().App },
		Logger:      e.logger,
	})
	authRoutes := auth.NewAuthHandlers(auth.AuthHandlersDeps{
		Service: e.Auth, Clock: e.clock, CSRFKey: e.csrfKey,
		TOTPAllow: e.totpLimiter, SessionDetails: func(ctx context.Context, id int64) (auth.SessionDetails, error) {
			return SessionDetailsOf(ctx, id, SessionDetailsDeps{
				Auth: e.Auth, Core: e.Core, Upload: e.Upload,
				Features: FeaturesInputs{
					SMBEnabled:     func() bool { return e.smbPublisherOf() != nil },
					PreviewEnabled: e.thumbnailEnabled, SearchHasIndex: e.Search.HasIndex,
				},
			})
		},
		OIDCEndSessionURL: oidcRoutes.EndSessionURL,
	})
	op(publicAPI, http.MethodPost, "/api/v1/auth/login", "auth.login", authRoutes.Login)
	op(publicAPI, http.MethodPost, "/api/v1/auth/login/totp", "auth.login.totp", authRoutes.LoginTOTP)
	op(sessionAPI, http.MethodPost, "/api/v1/auth/logout", "auth.logout", authRoutes.Logout)
	op(sessionAPI, http.MethodGet, "/api/v1/auth/session", "auth.session", authRoutes.Session)
	op(publicAPI, http.MethodGet, "/api/v1/auth/oidc/config", "auth.oidc.config", oidcRoutes.Config)
	op(publicAPI, http.MethodGet, "/api/v1/auth/oidc/start", "auth.oidc.start", oidcRoutes.Start)
	public.GET("/api/v1/auth/oidc/callback", oidcRoutes.Callback)

	account := auth.NewAccountHandlers(auth.AccountHandlersDeps{Service: e.Auth, Clock: e.clock})
	op(sessionAPI, http.MethodPost, "/api/v1/account/password", "account.password.set", account.Password)
	op(sessionAPI, http.MethodGet, "/api/v1/account/sessions", "account.sessions.list", account.Sessions)
	op(sessionAPI, http.MethodDelete, "/api/v1/account/sessions/{id}", "account.sessions.delete", account.SessionDelete)
	op(sessionAPI, http.MethodGet, "/api/v1/account/app-passwords", "account.app_passwords.list", account.AppPasswords)
	op(sessionAPI, http.MethodPost, "/api/v1/account/app-passwords", "account.app_passwords.create", account.AppPasswordCreate)
	op(sessionAPI, http.MethodDelete, "/api/v1/account/app-passwords/{id}", "account.app_passwords.delete", account.AppPasswordDelete)
	op(sessionAPI, http.MethodPost, "/api/v1/account/app-passwords/{id}/wipe", "account.app_passwords.wipe", account.AppPasswordWipe)
	op(sessionAPI, http.MethodPost, "/api/v1/account/totp/setup", "account.totp.setup", account.TOTPSetup)
	op(sessionAPI, http.MethodPost, "/api/v1/account/totp/enroll", "account.totp.enroll", account.TOTPEnroll)
	op(sessionAPI, http.MethodPost, "/api/v1/account/totp/disable", "account.totp.disable", account.TOTPDisable)
	op(sessionAPI, http.MethodGet, "/api/v1/account/totp/recovery-codes", "account.recovery_codes.count", account.RecoveryList)
	op(sessionAPI, http.MethodPost, "/api/v1/account/totp/recovery-codes", "account.recovery_codes.create", account.RecoveryCreate)
	smb := &adminsmb.AccountHandler{Auth: e.Auth}
	op(sessionAPI, http.MethodPost, "/api/v1/account/smb", "account.smb.create", smb.Access)
	op(sessionAPI, http.MethodPost, "/api/v1/account/smb/password", "account.smb.password.set", smb.SetPassword)
	op(sessionAPI, http.MethodDelete, "/api/v1/account/smb/password", "account.smb.password.delete", smb.DeletePassword)
	op(sessionAPI, http.MethodPost, "/api/v1/account/oidc-link/start", "account.oidc_link.start", oidcRoutes.LinkStart)
	op(sessionAPI, http.MethodDelete, "/api/v1/account/oidc-link", "account.oidc_link.delete", oidcRoutes.LinkDelete)
	rootOrder := &auth.RootOrderHandler{State: e.State}
	op(sessionAPI, http.MethodPost, "/api/v1/account/roots/order", "account.roots.order", rootOrder.Set)

	openClaim := files.OpenBoundClaim(e.claimKey, e.clk().Nanos)
	projection := files.NewProjection(files.ProjectionDeps{Core: e.Core, ClaimKey: e.claimKey, Now: e.clk().Nanos, Logger: e.logger})
	fs := files.NewHandler(files.Deps{
		Core: e.Core, Archives: e.Archives, Gate: e.archiveGate,
		Resolve: resolve, OpenClaim: openClaim,
		EntryView: projection.EntryView, Vpath: projection.Vpath, Refs: projection.Refs,
		GuardLock: e.guardDavLock,
		Now:       e.clk().Now, Journal: e.Journal != nil, Logger: e.logger,
	})
	op(sessionAPI, http.MethodGet, "/api/v1/files/list", "files.list", fs.List)
	op(sessionAPI, http.MethodGet, "/api/v1/files/stat", "files.stat", fs.Stat)
	session.GET("/api/v1/files/read", fs.Read)
	op(sessionAPI, http.MethodGet, "/api/v1/files/size", "files.size", fs.Size)
	session.GET("/api/v1/files/thumbnail", preview.ThumbnailHandler(preview.ThumbnailDeps{
		Core: e.Core, Resolve: resolve, OpenClaim: openClaim, PreviewLease: e.previewLease, Logger: e.logger,
	}))
	op(sessionAPI, http.MethodPost, "/api/v1/files/mkdir", "files.mkdir", fs.Mkdir)
	session.POST("/api/v1/files/write", fs.Write)
	op(sessionAPI, http.MethodPost, "/api/v1/files/delete", "files.delete", fs.Delete)
	op(sessionAPI, http.MethodPost, "/api/v1/files/move", "files.move", fs.Move)
	op(sessionAPI, http.MethodPost, "/api/v1/files/copy", "files.copy", fs.Copy)
	op(sessionAPI, http.MethodPost, "/api/v1/files/rename", "files.rename", fs.Rename)
	op(sessionAPI, http.MethodPost, "/api/v1/files/archive", "files.archive", fs.Archive)
	session.GET("/api/v1/files/archive/fetch", fs.ArchiveFetch)
	op(sessionAPI, http.MethodGet, "/api/v1/files/archive/list", "files.archive.list", preview.ArchiveList(preview.ArchiveListDeps{
		Core: e.Core, Resolve: resolve, AcquireArchive: e.acquireArchive, Logger: e.logger,
	}))
	op(sessionAPI, http.MethodPost, "/api/v1/files/download", "files.download", fs.Download)
	session.GET("/api/v1/files/download/fetch", fs.DownloadFetch)
	op(sessionAPI, http.MethodGet, "/api/v1/files/recent", "files.recent", fs.Recent)

	transfer := uploads.NewDirectHandler(uploads.DirectDependencies{
		State: e.State, Resolve: resolve,
		ShareEncrypted: e.Core.ShareEncrypted, GuardLock: e.guardDavLock,
		ProviderForRow:        uploads.DirectProviderForRow(e.Core, resolve),
		RevalidateDestination: uploads.RevalidateDirectDestination(e.Core, resolve, e.guardDavLock),
		Now:                   e.now, Logger: e.logger,
	})
	op(sessionAPI, http.MethodPost, "/api/v1/direct-uploads", "direct_uploads.create", transfer.Create)
	op(sessionAPI, http.MethodGet, "/api/v1/direct-uploads/{id}", "direct_uploads.get", transfer.Status)
	op(sessionAPI, http.MethodPost, "/api/v1/direct-uploads/{id}/parts", "direct_uploads.parts", transfer.Part)
	op(sessionAPI, http.MethodPost, "/api/v1/direct-uploads/{id}/complete", "direct_uploads.complete", transfer.Complete)
	op(sessionAPI, http.MethodPost, "/api/v1/direct-uploads/{id}/cancel", "direct_uploads.cancel", transfer.Cancel)

	upload := uploads.NewHandlers(uploads.Deps{
		Upload: e.Upload, Core: e.Core, Resolve: resolve,
	})
	public.OPTIONS("/api/v1/uploads", upload.Discover)
	session.POST("/api/v1/uploads", upload.Create)
	session.HEAD("/api/v1/uploads/:id", upload.Status)
	session.PATCH("/api/v1/uploads/:id", upload.Patch)
	session.DELETE("/api/v1/uploads/:id", upload.Abort)
	public.OPTIONS("/api/v1/uploads/:id", upload.DiscoverOne)

	linkRoutes := &links.LinksHandler{
		Core: e.Core, Auth: e.Auth, Resolve: resolve, Now: e.now,
		VpathOf: func(l files.Link) string {
			vp, err := e.Core.VpathFor(l.Owner, l.Share, l.Path)
			if err != nil {
				return ""
			}
			return vp.String()
		},
	}
	op(sessionAPI, http.MethodGet, "/api/v1/links", "links.list", linkRoutes.List)
	op(sessionAPI, http.MethodPost, "/api/v1/links", "links.create", linkRoutes.Create)
	op(sessionAPI, http.MethodPatch, "/api/v1/links/{id}", "links.update", linkRoutes.Update)
	op(sessionAPI, http.MethodDelete, "/api/v1/links/{id}", "links.delete", linkRoutes.Delete)
	op(adminAPI, http.MethodGet, "/api/v1/admin/links", "admin.links.list", linkRoutes.AdminList)

	trash := &files.TrashHandler{Core: e.Core, Resolve: resolve, Errors: e.errs}
	op(sessionAPI, http.MethodGet, "/api/v1/trash", "trash.list", trash.List)
	op(sessionAPI, http.MethodPost, "/api/v1/trash/restore", "trash.restore", trash.Restore)
	op(sessionAPI, http.MethodPost, "/api/v1/trash/purge", "trash.purge", trash.Purge)

	jobs := &jobshttp.Handler{Core: e.Core, State: e.State, StartJobs: e.Core.StartJobs, NowNs: e.now}
	op(sessionAPI, http.MethodGet, "/api/v1/jobs", "jobs.list", jobs.List)
	op(sessionAPI, http.MethodGet, "/api/v1/jobs/{id}", "jobs.get", jobs.Get)
	op(sessionAPI, http.MethodPost, "/api/v1/jobs/{id}/cancel", "jobs.cancel", jobs.Cancel)
	op(sessionAPI, http.MethodPost, "/api/v1/jobs/{id}/retry", "jobs.retry", jobs.Retry)
	op(sessionAPI, http.MethodPost, "/api/v1/jobs/{id}/pause", "jobs.pause", jobs.Pause)
	op(sessionAPI, http.MethodPost, "/api/v1/jobs/{id}/resume", "jobs.resume", jobs.Resume)

	session.GET("/api/v1/search/stream", e.searchHTTP.SearchStream)

	enc := &files.EncryptionHandler{Core: e.Core}
	op(sessionAPI, http.MethodGet, "/api/v1/encryption", "encryption.list", enc.List)
	op(adminAPI, http.MethodPost, "/api/v1/encryption/{id}", "admin.encryption.enable", enc.Enable)
	op(adminAPI, http.MethodDelete, "/api/v1/encryption/{id}", "admin.encryption.disable", enc.Disable)

	users := adminhttp.NewAdminUsersHandlers(adminhttp.AdminUsersDeps{Auth: e.Auth, CleanupHome: e.Core.CleanupHome, Logger: e.logger})
	op(adminAPI, http.MethodGet, "/api/v1/admin/users", "admin.users.list", users.UsersList)
	op(adminAPI, http.MethodPost, "/api/v1/admin/users", "admin.users.create", users.UsersCreate)
	op(adminAPI, http.MethodPatch, "/api/v1/admin/users/{id}", "admin.users.update", users.UsersUpdate)
	op(adminAPI, http.MethodDelete, "/api/v1/admin/users/{id}", "admin.users.delete", users.UsersDelete)
	op(adminAPI, http.MethodGet, "/api/v1/admin/users/{id}/oidc", "admin.users.oidc.get", oidcRoutes.AdminGet)
	op(adminAPI, http.MethodDelete, "/api/v1/admin/users/{id}/oidc", "admin.users.oidc.delete", oidcRoutes.AdminDelete)
	op(adminAPI, http.MethodGet, "/api/v1/admin/groups", "admin.groups.list", users.GroupsList)
	op(adminAPI, http.MethodPost, "/api/v1/admin/groups", "admin.groups.create", users.GroupsCreate)
	op(adminAPI, http.MethodPatch, "/api/v1/admin/groups/{id}", "admin.groups.update", users.GroupsUpdate)
	op(adminAPI, http.MethodDelete, "/api/v1/admin/groups/{id}", "admin.groups.delete", users.GroupsDelete)
	op(adminAPI, http.MethodPost, "/api/v1/admin/groups/{id}/members", "admin.groups.members.add", users.MemberAdd)
	op(adminAPI, http.MethodDelete, "/api/v1/admin/groups/{id}/members/{user}", "admin.groups.members.remove", users.MemberRemove)
	op(adminAPI, http.MethodGet, "/api/v1/admin/audit", "admin.audit", users.Audit)

	shares := &adminhttp.SharesHandler{
		Core: e.Core, MarkSearchIncomplete: e.searchController.MarkIncomplete,
		WatchShare: e.watchShare, UnwatchShare: e.unwatchShare, Logger: e.logger,
	}
	op(adminAPI, http.MethodGet, "/api/v1/admin/grants", "admin.grants.list", shares.ListGrants)
	op(adminAPI, http.MethodPost, "/api/v1/admin/grants", "admin.grants.create", shares.CreateGrant)
	op(adminAPI, http.MethodPatch, "/api/v1/admin/grants/{id}", "admin.grants.update", shares.UpdateGrant)
	op(adminAPI, http.MethodDelete, "/api/v1/admin/grants/{id}", "admin.grants.delete", shares.DeleteGrant)
	op(adminAPI, http.MethodGet, "/api/v1/admin/shares", "admin.shares.list", shares.ListShares)
	op(adminAPI, http.MethodPost, "/api/v1/admin/shares", "admin.shares.create", shares.CreateShare)
	op(adminAPI, http.MethodPatch, "/api/v1/admin/shares/{id}", "admin.shares.update", shares.UpdateShare)
	op(adminAPI, http.MethodDelete, "/api/v1/admin/shares/{id}", "admin.shares.delete", shares.DeleteShare)
	op(adminAPI, http.MethodPost, "/api/v1/admin/shares/{id}/retry", "admin.shares.retry", shares.RetryShare)

	logs := &adminhttp.LogsHandler{Logs: e.Logs, Auth: e.Auth}
	op(adminAPI, http.MethodGet, "/api/v1/admin/logs", "admin.logs.list", logs.List)
	op(adminAPI, http.MethodGet, "/api/v1/admin/logs/timeline", "admin.logs.timeline", logs.Timeline)
	storage := &adminhttp.StorageHandler{Core: e.Core, State: e.State}
	op(adminAPI, http.MethodGet, "/api/v1/admin/storage", "admin.storage", storage.Get)

	smbAdmin := adminsmb.NewAdminHandlers(adminsmb.AdminDeps{Apply: func(ctx context.Context) (agent.Report, bool, error) {
		p := e.smbPublisherOf()
		if p == nil {
			return agent.Report{}, false, nil
		}
		r, err := p.Publish(ctx)
		return r, true, err
	}, Logger: e.logger})
	op(adminAPI, http.MethodPost, "/api/v1/admin/smb/apply", "admin.smb.apply", smbAdmin.Apply)
	op(adminAPI, http.MethodPost, "/api/v1/admin/index/build", "admin.index.build", e.searchHTTP.IndexBuild)
	op(adminAPI, http.MethodGet, "/api/v1/admin/index/estimate", "admin.index.estimate", e.searchHTTP.IndexEstimate)
	op(adminAPI, http.MethodGet, "/api/v1/admin/index/status", "admin.index.status", e.searchHTTP.IndexStatus)

	settings := adminhttp.NewSettingsHandlers(adminhttp.SettingsDeps{
		State: e.State, Auth: e.Auth, Settings: e.Settings,
		DataDir: e.dataDir, Hardening: e.hardening,
		SMBAgentView: func() *adminhttp.SMBAgentView {
			p := e.smbPublisherOf()
			if p == nil {
				return nil
			}
			return adminhttp.SMBAgentOf(p.LastReport())
		}, PublishSMB: e.publishSMBSettings,
		OnRestart: e.Restart.Request, Logger: e.logger,
	})
	op(adminAPI, http.MethodGet, "/api/v1/admin/settings", "admin.settings.get", settings.Get)
	op(adminAPI, http.MethodGet, "/api/v1/admin/oidc/endpoints", "admin.oidc.endpoints", oidcRoutes.AdminEndpoints)
	op(adminAPI, http.MethodPatch, "/api/v1/admin/settings/upload", "admin.settings.upload", upload.SettingsPatch)
	op(adminAPI, http.MethodPatch, "/api/v1/admin/settings/{section}", "admin.settings.update", settings.Patch)
	op(adminAPI, http.MethodPost, "/api/v1/admin/system/restart", "admin.system.restart", settings.Restart)
	admin.GET("/api/v1/admin/openapi", func(c *gin.Context) { c.JSON(http.StatusOK, adminAPI.api.OpenAPI()) })

	e.publicLinks = e.newPublicLinks()
	// A browser navigating to a link gets the web client before the typed
	// route runs; only the client's JSON request reaches the operation.
	linkPage := newTyped(router, public.Group("", e.publicLinks.LandingPage), config, e.errs, true)
	op(linkPage, http.MethodGet, "/s/{token}", "links.public.get", e.publicLinks.Landing)
	op(publicAPI, http.MethodPost, "/s/{token}/auth", "links.unlock", e.publicLinks.Unlock(links.PublicLinkPrefix))
	public.GET("/s/:token/download", e.publicLinks.Download)
	public.GET("/s/:token/zip", e.publicLinks.Zip)
	public.POST("/s/:token/drop", e.publicLinks.Drop)

	dav.Mount(device, dav.Deps{Core: e.Core, State: e.State, Locks: e.davLocks, Clock: e.clk(), Logger: e.logger, Errors: e.errs, InfinityEntries: 10_000})
	e.mountNCTagged(public, publicAPI, linkPage, device)
	requireResponseFields(config.OpenAPI)
	return web.Install(router)
}

// requireAdmin stops a request from a session that is not an administrator's.
func (e *Engine) requireAdmin(c *gin.Context) {
	owner, ok := middleware.UserOf(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		c.Abort()
		return
	}
	isAdmin, err := e.Auth.IsAdmin(c.Request.Context(), owner)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !isAdmin {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Denied})
		c.Abort()
		return
	}
	c.Next()
}
