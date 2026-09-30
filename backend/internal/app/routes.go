//go:build linux

package app

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/dav"
	"github.com/heavycaffeiner/stowcloud/backend/internal/emergency"
	featuretransfer "github.com/heavycaffeiner/stowcloud/backend/internal/feature/directtransfer"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	accounthttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/account"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminlogs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminsettings"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminshares"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminsmb"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminstorage"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/directtransfer"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/encryption"
	filehttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	jobshttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/jobs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/links"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/oidc"
	previewhttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/preview"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/setup"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/smbaccount"
	trashhttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/trash"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/uploads"
	featureoidc "github.com/heavycaffeiner/stowcloud/backend/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb/agent"
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
	sessionAPI := newTyped(router, session, config, e.errs)
	adminAPI := newTyped(router, admin, config, e.errs)

	resolve := filehttp.Resolve(e.Core)
	adminOf := func(c *gin.Context) (int64, bool) { return handler.Admin(c, e.Auth) }
	ownerOf := func(c *gin.Context) (int64, bool) { owner, ok := handler.Owner(c); return int64(owner), ok }
	writeJSON := func(c *gin.Context, status int, v any) { c.JSON(status, v) }

	public.GET("/api/v1/system/health", e.health)
	setupRoutes := setup.NewHandlers(setup.Deps{
		Auth: e.Auth, State: e.State, Gate: e.setup,
		GrantEveryShare: e.Core.GrantEveryShare, CreateShare: e.Core.CreateShare,
		Apply: e.Settings.Load, DataDir: e.dataDir, Logger: e.log(),
	})
	public.GET("/api/v1/system/setup", setupRoutes.Get)
	public.POST("/api/v1/system/setup", middleware.LimitJSON, setupRoutes.Post)
	fsDeps := handler.AdminFSDeps{Auth: e.Auth, Core: e.Core, DataDir: e.dataDir}
	if e.setup != nil {
		fsDeps.SetupVerify = e.setup.Verify
	}
	adminFS := handler.NewAdminFSHandlers(fsDeps)
	public.POST("/api/v1/system/setup/browse", middleware.LimitJSON, adminFS.SetupBrowse)
	admin.GET("/api/v1/admin/fs", adminFS.Browse)
	session.GET("/api/v1/events", e.eventsSocket())

	oidcRoutes := oidc.New(oidc.Deps{
		Auth:          e.Auth,
		Client:        func() *featureoidc.Client { e.settingsMu.RLock(); defer e.settingsMu.RUnlock(); return e.oidcClient },
		DisplayName:   func() string { e.settingsMu.RLock(); defer e.settingsMu.RUnlock(); return e.oidcName },
		AppHosts:      func() []string { return e.Settings.Hosts().App },
		RequestScheme: func(r *http.Request) string { return middleware.RequestScheme(r, e.trustedProxies()) },
		Logger:        e.log(),
		Owner:         ownerOf,
		Admin:         adminOf,
		Reconfirm: func(c *gin.Context, owner int64, password string) bool {
			return handler.Reconfirm(c, e.Auth, owner, password)
		},
		Decode: filehttp.Decode, Fail: handler.Fail, FailKnown: handler.FailKnown, Refuse: handler.Refuse,
		WriteJSON: writeJSON, ClientAddr: handler.ClientAddr, SetSessionCookie: handler.SetSessionCookie,
	})
	auth := handler.NewAuthHandlers(handler.AuthHandlersDeps{
		Service: e.Auth, Clock: e.clock, CSRFKey: e.csrfKey,
		TOTPAllow: e.totpLimiter, SessionDetails: func(ctx context.Context, id int64) (handler.SessionDetails, error) {
			return handler.SessionDetailsOf(ctx, id, handler.SessionDetailsDeps{
				Auth: e.Auth, Core: e.Core, Upload: e.Upload,
				Features: handler.FeaturesInputs{
					SMBEnabled:     func() bool { return e.smbPublisherOf() != nil },
					PreviewEnabled: e.thumbnailEnabled, SearchHasIndex: e.Search.HasIndex,
				},
			})
		},
		OIDCEndSessionURL: oidcRoutes.EndSessionURL,
	})
	public.POST("/api/v1/auth/login", middleware.LimitJSON, auth.Login)
	public.POST("/api/v1/auth/login/totp", middleware.LimitJSON, auth.LoginTOTP)
	session.POST("/api/v1/auth/logout", auth.Logout)
	session.GET("/api/v1/auth/session", auth.Session)
	public.GET("/api/v1/auth/oidc/config", oidcRoutes.Config)
	public.GET("/api/v1/auth/oidc/start", oidcRoutes.Start)
	public.GET("/api/v1/auth/oidc/callback", oidcRoutes.Callback)

	account := handler.NewAccountHandlers(handler.AccountHandlersDeps{Service: e.Auth, Clock: e.clock})
	session.POST("/api/v1/account/password", middleware.LimitJSON, account.Password)
	session.GET("/api/v1/account/sessions", account.Sessions)
	session.DELETE("/api/v1/account/sessions/:id", account.SessionDelete)
	session.GET("/api/v1/account/app-passwords", account.AppPasswords)
	session.POST("/api/v1/account/app-passwords", middleware.LimitJSON, account.AppPasswordCreate)
	session.DELETE("/api/v1/account/app-passwords/:id", account.AppPasswordDelete)
	session.POST("/api/v1/account/app-passwords/:id/wipe", account.AppPasswordWipe)
	session.POST("/api/v1/account/totp/setup", account.TOTPSetup)
	session.POST("/api/v1/account/totp/enroll", middleware.LimitJSON, account.TOTPEnroll)
	session.POST("/api/v1/account/totp/disable", middleware.LimitJSON, account.TOTPDisable)
	session.GET("/api/v1/account/totp/recovery-codes", account.RecoveryList)
	session.POST("/api/v1/account/totp/recovery-codes", middleware.LimitJSON, account.RecoveryCreate)
	smb := &smbaccount.Handler{Auth: e.Auth}
	op(sessionAPI, http.MethodPost, "/api/v1/account/smb", "account.smb.create", smb.Access)
	op(sessionAPI, http.MethodPost, "/api/v1/account/smb/password", "account.smb.password.set", smb.SetPassword)
	op(sessionAPI, http.MethodDelete, "/api/v1/account/smb/password", "account.smb.password.delete", smb.DeletePassword)
	session.POST("/api/v1/account/oidc-link/start", middleware.LimitJSON, oidcRoutes.LinkStart)
	session.DELETE("/api/v1/account/oidc-link", oidcRoutes.LinkDelete)
	session.POST("/api/v1/account/roots/order", middleware.LimitJSON, accounthttp.RootOrderHandler(accounthttp.RootOrderDeps{
		State: e.State, Owner: ownerOf, Fail: handler.Fail, Refuse: handler.Refuse, Decode: filehttp.Decode,
	}))

	openClaim := filehttp.OpenBoundClaim(e.claimKey, e.clk().Nanos)
	projection := filehttp.NewProjection(filehttp.ProjectionDeps{Core: e.Core, ClaimKey: e.claimKey, Now: e.clk().Nanos, Logger: e.log()})
	fs := filehttp.NewHandler(filehttp.Deps{
		Core: e.Core, Archives: e.Archives, Gate: e.archiveGate,
		Owner: handler.Owner, Resolve: resolve, OpenClaim: openClaim,
		EntryView: projection.EntryView, Vpath: projection.Vpath, Refs: projection.Refs,
		Fail: handler.Fail, Refuse: handler.Refuse, NotFound: handler.NotFound, Decode: filehttp.Decode,
		Body: handler.Body, GuardLock: e.guardDavLock,
		Now: e.clk().Now, Journal: e.Journal != nil, Logger: e.log(),
	})
	session.GET("/api/v1/files/list", fs.List)
	session.GET("/api/v1/files/stat", fs.Stat)
	session.GET("/api/v1/files/read", fs.Read)
	session.GET("/api/v1/files/size", fs.Size)
	session.GET("/api/v1/files/thumbnail", previewhttp.ThumbnailHandler(previewhttp.ThumbnailDeps{
		Core: e.Core, Owner: handler.Owner, Resolve: resolve, OpenClaim: openClaim,
		PreviewLease: e.previewLease, Fail: handler.Fail, Refuse: handler.Refuse, Logger: e.logger,
	}))
	session.POST("/api/v1/files/mkdir", middleware.LimitJSON, fs.Mkdir)
	session.POST("/api/v1/files/write", fs.Write)
	session.POST("/api/v1/files/delete", middleware.LimitJSON, fs.Delete)
	session.POST("/api/v1/files/move", middleware.LimitJSON, fs.Move)
	session.POST("/api/v1/files/copy", middleware.LimitJSON, fs.Copy)
	session.POST("/api/v1/files/rename", middleware.LimitJSON, fs.Rename)
	session.POST("/api/v1/files/archive", middleware.LimitJSON, fs.Archive)
	session.GET("/api/v1/files/archive/fetch", fs.ArchiveFetch)
	session.GET("/api/v1/files/archive/list", fs.ArchiveList)
	session.POST("/api/v1/files/download", middleware.LimitJSON, fs.Download)
	session.GET("/api/v1/files/download/fetch", fs.DownloadFetch)
	session.GET("/api/v1/files/recent", fs.Recent)

	transfer := directtransfer.NewHandler(directtransfer.Deps{
		State: e.State, Owner: handler.Owner, Resolve: resolve,
		ShareEncrypted: e.Core.ShareEncrypted, GuardLock: e.guardDavLock,
		ProviderForRow:        featuretransfer.ProviderForRow(e.Core, resolve),
		RevalidateDestination: featuretransfer.RevalidateDestination(e.Core, resolve, e.guardDavLock),
		Now:                   e.now, Decode: filehttp.Decode, Fail: handler.Fail, Refuse: handler.Refuse, NotFound: handler.NotFound,
		Logger: e.log(),
	})
	session.POST("/api/v1/direct-uploads", middleware.LimitJSON, transfer.Create)
	session.GET("/api/v1/direct-uploads/:id", transfer.Status)
	session.POST("/api/v1/direct-uploads/:id/parts", middleware.LimitJSON, transfer.Part)
	session.POST("/api/v1/direct-uploads/:id/complete", middleware.LimitJSON, transfer.Complete)
	session.POST("/api/v1/direct-uploads/:id/cancel", middleware.LimitJSON, transfer.Cancel)

	upload := uploads.NewHandlers(uploads.Deps{
		Upload: e.Upload, Core: e.Core, Resolve: resolve,
		Owner: handler.Owner, Admin: adminOf, Fail: handler.Fail, Refuse: handler.Refuse,
		Decode: filehttp.Decode, WriteJSON: writeJSON,
	})
	public.OPTIONS("/api/v1/uploads", upload.Discover)
	session.POST("/api/v1/uploads", upload.Create)
	session.HEAD("/api/v1/uploads/:id", upload.Status)
	session.PATCH("/api/v1/uploads/:id", upload.Patch)
	session.DELETE("/api/v1/uploads/:id", upload.Abort)
	public.OPTIONS("/api/v1/uploads/:id", upload.DiscoverOne)

	linkRoutes := &links.Handler{
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

	trash := &trashhttp.Handler{Core: e.Core, Resolve: resolve, Errors: e.errs}
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

	enc := &encryption.Handler{Core: e.Core}
	op(sessionAPI, http.MethodGet, "/api/v1/encryption", "encryption.list", enc.List)
	op(adminAPI, http.MethodPost, "/api/v1/encryption/{id}", "admin.encryption.enable", enc.Enable)
	op(adminAPI, http.MethodDelete, "/api/v1/encryption/{id}", "admin.encryption.disable", enc.Disable)

	users := handler.NewAdminUsersHandlers(handler.AdminUsersDeps{Auth: e.Auth, CleanupHome: e.Core.CleanupHome, Logger: e.logger})
	admin.GET("/api/v1/admin/users", users.UsersList)
	admin.POST("/api/v1/admin/users", middleware.LimitJSON, users.UsersCreate)
	admin.PATCH("/api/v1/admin/users/:id", middleware.LimitJSON, users.UsersUpdate)
	admin.DELETE("/api/v1/admin/users/:id", users.UsersDelete)
	admin.GET("/api/v1/admin/users/:id/oidc", oidcRoutes.AdminGet)
	admin.DELETE("/api/v1/admin/users/:id/oidc", oidcRoutes.AdminDelete)
	admin.GET("/api/v1/admin/groups", users.GroupsList)
	admin.POST("/api/v1/admin/groups", middleware.LimitJSON, users.GroupsCreate)
	admin.PATCH("/api/v1/admin/groups/:id", middleware.LimitJSON, users.GroupsUpdate)
	admin.DELETE("/api/v1/admin/groups/:id", users.GroupsDelete)
	admin.POST("/api/v1/admin/groups/:id/members", middleware.LimitJSON, users.MemberAdd)
	admin.DELETE("/api/v1/admin/groups/:id/members/:user", users.MemberRemove)
	admin.GET("/api/v1/admin/audit", users.Audit)

	shares := &adminshares.Handler{
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

	logs := &adminlogs.Handler{Logs: e.Logs, Auth: e.Auth}
	op(adminAPI, http.MethodGet, "/api/v1/admin/logs", "admin.logs.list", logs.List)
	op(adminAPI, http.MethodGet, "/api/v1/admin/logs/timeline", "admin.logs.timeline", logs.Timeline)
	storage := &adminstorage.Handler{Core: e.Core, State: e.State}
	op(adminAPI, http.MethodGet, "/api/v1/admin/storage", "admin.storage", storage.Get)

	smbAdmin := adminsmb.NewHandlers(adminsmb.Deps{Auth: e.Auth, Apply: func(ctx context.Context) (agent.Report, bool, error) {
		p := e.smbPublisherOf()
		if p == nil {
			return agent.Report{}, false, nil
		}
		r, err := p.Publish(ctx)
		return r, true, err
	}, Logger: e.log()})
	admin.POST("/api/v1/admin/smb/apply", smbAdmin.Apply)
	admin.POST("/api/v1/admin/index/build", middleware.LimitJSON, e.searchHTTP.IndexBuild)
	admin.GET("/api/v1/admin/index/estimate", e.searchHTTP.IndexEstimate)
	admin.GET("/api/v1/admin/index/status", e.searchHTTP.IndexStatus)

	settings := adminsettings.NewHandlers(adminsettings.Deps{
		State: e.State, Auth: e.Auth, Settings: e.Settings,
		DataDir: e.dataDir, Hardening: e.hardening, Admin: adminOf,
		UploadPatch: upload.SettingsPatch,
		SMBAgentView: func() *handler.SMBAgentView {
			p := e.smbPublisherOf()
			if p == nil {
				return nil
			}
			return handler.SMBAgentOf(p.LastReport())
		}, PublishSMB: e.publishSMBSettings,
		OnRestart: e.Restart.Request, Logger: e.log(),
	})
	admin.GET("/api/v1/admin/settings", settings.Get)
	admin.GET("/api/v1/admin/oidc/endpoints", oidcRoutes.AdminEndpoints)
	admin.PATCH("/api/v1/admin/settings/:section", middleware.LimitJSON, settings.Patch)
	admin.POST("/api/v1/admin/system/restart", settings.Restart)
	admin.GET("/api/v1/admin/openapi", func(c *gin.Context) { c.JSON(http.StatusOK, adminAPI.api.OpenAPI()) })

	e.publicLinks = e.newPublicLinks()
	public.GET("/s/:token", e.publicLinks.Landing)
	public.POST("/s/:token/auth", middleware.LimitJSON, e.publicLinks.Unlock)
	public.GET("/s/:token/download", e.publicLinks.Download)
	public.GET("/s/:token/zip", e.publicLinks.Zip)
	public.POST("/s/:token/drop", e.publicLinks.Drop)

	dav.Mount(device, dav.Deps{Core: e.Core, State: e.State, Locks: e.davLocks, Clock: e.clk(), Logger: e.log(), Errors: e.errs, InfinityEntries: 10_000})
	e.mountNCTagged(public, device)
	return web.Install(router)
}

// requireAdmin stops a request from a session that is not an administrator's.
func (e *Engine) requireAdmin(c *gin.Context) {
	if _, ok := handler.Admin(c, e.Auth); !ok {
		c.Abort()
		return
	}
	c.Next()
}
