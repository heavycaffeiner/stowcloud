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
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	featureoidc "github.com/heavycaffeiner/stowcloud/go/internal/feature/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/smb/agent"
	accounthttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/account"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminlogs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminsettings"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminshares"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminsmb"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/adminstorage"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/directtransfer"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/emergency"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/encryption"
	filehttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/jobs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/links"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/oidc"
	previewhttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/setup"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/smbaccount"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/spa"
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
	handlers := e.handlers()
	if err := server.Bind(app, server.Binding{
		Routes: table, Roots: []string{server.Base}, Chain: middleware.Chain(),
		Tasks: e.tasks(), Handlers: handlers, Deps: e.deps(),
		StartTasks: e.startTasks,
		BeforeAnnounce: func(router *gin.Engine) {
			emergency.Mount(router, emergency.Deps{
				Auth:  emergency.NewAuthenticator(e.Auth, e.clk().Nanos),
				State: e.State, Page: spa.Page(), DataDir: e.dataDir,
				Reason:     func() string { return "" },
				ClientAddr: emergency.ClientAddr(e.trustedProxies),
			})
		},
		AfterAnnounce: func(router *gin.Engine) {
			e.publicLinks.Declare(router, links.PublicLinkPrefix)
			e.declarePublicLinkAliases(router)
		},
	}); err != nil {
		return err
	}
	dav.Mount(app, dav.Deps{Core: e.Core, State: e.State, Locks: e.davLocks, Clock: e.clk(), Logger: e.log(), InfinityEntries: 10_000})
	e.publicLinks.Mount(app)
	e.mountNCTagged(app)
	if err := spa.Install(app); err != nil {
		return err
	}
	return nil
}
func (e *Engine) newPublicLinks() *links.Public {
	return links.NewPublic(links.PublicDeps{
		Core:       e.Core,
		State:      e.State,
		ClaimKey:   e.claimKey.Key,
		Limiter:    e.linkLimiter,
		Now:        e.now,
		ClientAddr: handler.ClientAddr,
		Audit: func(ctx context.Context, event, target, ip, ua string, ok bool) error {
			return e.Auth.Audit(ctx, nil, event, target, ip, ua, ok)
		},
		Logger:      e.log(),
		Frontend:    spa.Page(),
		Fail:        handler.Fail,
		Refuse:      handler.Refuse,
		WriteJSON:   func(c *gin.Context, status int, v any) { c.JSON(status, v) },
		Decode:      filehttp.Decode,
		CloseStream: func(stream *core.Stream, name string) { filehttp.CloseStream(stream, name, e.log()) },
		SendStream:  filehttp.SendStream,
		AcquireArchive: func() (func(), bool) {
			if !e.archiveGate.TryAcquire() {
				return nil, false
			}
			return e.archiveGate.Release, true
		},
		WriteArchive: func(ctx context.Context, w io.Writer, link core.Link, sub, name string) {
			if err := filehttp.BuildArchive(ctx, w, name, func(ctx context.Context, visit filehttp.ArchiveVisit) error {
				return e.Core.LinkArchiveWalk(ctx, link, sub, visit)
			}, e.log()); err != nil {
				e.log().Warn("a link archive ended early", "name", name, "error", err)
			}
		},
	})
}

// handlers binds a projection to every route the table names.
//
// Each entry is a real function rather than a stub that returns 501: a route
// registered to something that cannot answer is worse than one that is absent,
// because a client discovers it and then fails.
func (e *Engine) handlers() server.Handlers {
	out := make(server.Handlers)
	admin := func(c *gin.Context) (int64, bool) { return handler.Admin(c, e.Auth) }
	resolve := filehttp.Resolve(e.Core)
	openClaim := filehttp.OpenBoundClaim(e.claimKey, e.clk().Nanos)
	projection := filehttp.NewProjection(filehttp.ProjectionDeps{Core: e.Core, ClaimKey: e.claimKey, Now: e.clk().Nanos, Logger: e.log()})
	out["system.health"] = e.health
	out["admin.index.estimate"] = e.searchRuntime.IndexEstimate
	out["admin.index.status"] = e.searchRuntime.IndexStatus
	out["admin.index.build"] = e.searchRuntime.IndexBuild
	out["search.stream"] = e.searchRuntime.SearchStream
	out["events"] = e.eventsSocket()
	out["files.thumbnail"] = previewhttp.ThumbnailHandler(previewhttp.ThumbnailDeps{
		Core: e.Core, Owner: handler.Owner, Resolve: resolve, OpenClaim: openClaim,
		PreviewLease: e.previewLease, Fail: handler.Fail, Refuse: handler.Refuse, Logger: e.logger,
	})
	out["account.roots.order"] = accounthttp.RootOrderHandler(accounthttp.RootOrderDeps{
		State: e.State, Owner: func(c *gin.Context) (int64, bool) {
			owner, ok := handler.Owner(c)
			return int64(owner), ok
		}, Fail: handler.Fail, Refuse: handler.Refuse, Decode: filehttp.Decode,
	})

	nativeLinks := links.NewNative(links.NativeDeps{
		Core: e.Core, Auth: e.Auth, Owner: handler.Owner, Admin: admin,
		Resolve: resolve, Now: e.now, Decode: filehttp.Decode,
		VpathOf: func(l core.Link) string {
			vp, err := e.Core.VpathFor(l.Owner, l.Share, l.Path)
			if err != nil {
				return ""
			}
			return vp.String()
		},
		Fail: handler.Fail, Refuse: handler.Refuse, NotFound: handler.NotFound, WriteJSON: func(c *gin.Context, status int, v any) { c.JSON(status, v) },
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
		Owner: handler.Owner, Resolve: resolve, OpenClaim: openClaim,
		EntryView: projection.EntryView, Vpath: projection.Vpath, Refs: projection.Refs,
		Fail: handler.Fail, Refuse: handler.Refuse, NotFound: handler.NotFound, Decode: filehttp.Decode,
		Body: handler.Body, GuardLock: e.guardDavLock,
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
		Owner:       func(c *gin.Context) (int64, bool) { owner, ok := handler.Owner(c); return int64(owner), ok },
		Admin:       admin, Reconfirm: func(c *gin.Context, owner int64, password string) bool {
			return handler.Reconfirm(c, e.Auth, owner, password)
		}, Decode: filehttp.Decode,
		Fail: handler.Fail, FailKnown: handler.FailKnown, Refuse: handler.Refuse, WriteJSON: func(c *gin.Context, status int, v any) { c.JSON(status, v) },
		ClientAddr: handler.ClientAddr, SetSessionCookie: handler.SetSessionCookie,
	})
	for name, h := range oidcRoutes.Routes() {
		out[name] = h
	}
	for name, h := range handler.NewAuthHandlers(handler.AuthHandlersDeps{
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
		State: e.State, Owner: handler.Owner, Resolve: resolve,
		ShareEncrypted: e.Core.ShareEncrypted, GuardLock: e.guardDavLock,
		ProviderForRow:        directtransfer.ProviderForRow(e.Core, resolve),
		RevalidateDestination: directtransfer.RevalidateDestination(e.Core, resolve, e.guardDavLock),
		Now:                   e.now, Decode: filehttp.Decode, Fail: handler.Fail, Refuse: handler.Refuse, NotFound: handler.NotFound,
		Logger: e.log(),
	})
	out["direct-uploads.create"] = transfer.CreateHandler
	out["direct-uploads.status"] = transfer.StatusHandler
	out["direct-uploads.part"] = transfer.PartHandler
	out["direct-uploads.complete"] = transfer.CompleteHandler
	out["direct-uploads.cancel"] = transfer.CancelHandler
	uploadRoutes := uploads.NewHandlers(uploads.Deps{
		Upload: e.Upload, Core: e.Core, Resolve: resolve,
		Owner: handler.Owner, Admin: admin, Fail: handler.Fail, Refuse: handler.Refuse,
		Decode: filehttp.Decode, WriteJSON: func(c *gin.Context, status int, v any) { c.JSON(status, v) },
	})
	for name, h := range uploadRoutes {
		if name != "admin.settings.upload" {
			out[name] = h
		}
	}
	for name, h := range adminsettings.NewHandlers(adminsettings.Deps{
		State: e.State, Auth: e.Auth, Settings: e.Settings,
		DataDir: e.dataDir, Hardening: e.hardening, Admin: admin,
		UploadPatch: uploadRoutes["admin.settings.upload"],
		SMBAgentView: func() *handler.SMBAgentView {
			p := e.smbPublisherOf()
			if p == nil {
				return nil
			}
			return handler.SMBAgentOf(p.LastReport())
		}, PublishSMB: e.publishSMBSettings,
		OnRestart: e.Restart.Request, Logger: e.log(),
	}) {
		out[name] = h
	}
	for name, h := range jobs.NewHandlers(jobs.Deps{Core: e.Core, State: e.State, Owner: handler.Owner, StartJobs: e.Core.StartJobs, NowNs: e.now}) {
		out[name] = h
	}
	for name, h := range adminlogs.NewHandlers(adminlogs.Deps{Logs: e.Logs, Auth: e.Auth, Admin: admin, Fail: handler.FailKnown, Refuse: handler.Refuse}) {
		out[name] = h
	}
	trashHandler := trashhttp.NewHandler(trashhttp.Deps{Core: e.Core, Owner: handler.Owner, Resolve: resolve, Decode: filehttp.Decode, Fail: handler.Fail, Refuse: handler.Refuse})
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
	c.JSON(http.StatusOK, h)
}

func (e *Engine) guardDavLock(ctx context.Context, share uint32, path string, principal int64) error {
	if e.davLocks == nil {
		return nil
	}
	return e.davLocks.Guard(ctx, share, path, principal, nil)
}
