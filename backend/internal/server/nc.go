//go:build linux && compat_nc

// Binding the other product's surface to this engine's services.
//
// Everything the surface cannot reach for itself is assembled here: the
// store-backed facts, the device-login flow, the base URL of a request, and
// the two capabilities that carry a signed claim. The surface owns every
// spelling on the wire and this file owns none of them, which is what keeps
// the vocabulary in one package.
package server

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/nextcloud"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// mountNCTagged mounts the compatibility surface and its front-controller
// spelling of the public link routes.
func (e *Engine) mountNCTagged(public, device gin.IRoutes) {
	e.ncServer().Mount(device)
	public.GET("/index.php/s/:token", e.publicLinks.Landing)
	public.POST("/index.php/s/:token/auth", middleware.LimitJSON, e.publicLinks.Unlock)
	public.GET("/index.php/s/:token/download", e.publicLinks.Download)
	public.GET("/index.php/s/:token/zip", e.publicLinks.Zip)
	public.POST("/index.php/s/:token/drop", e.publicLinks.Drop)
}

func (e *Engine) contentRoute(method, path string) bool {
	return (method == http.MethodGet || method == http.MethodHead) && nextcloud.IsDirectPath(path)
}

func (e *Engine) ncServer() *nextcloud.Server {
	e.thumbnailMu.RLock()
	previewSvc := e.Preview
	e.thumbnailMu.RUnlock()
	seal, open := nextcloud.Claims(e.claimKey, func() int64 { return e.clk().Nanos() })
	return nextcloud.New(nextcloud.Deps{
		Core: e.Core, Auth: e.Auth, Errors: e.errs,
		Store:   nextcloud.NewStore(nextcloud.StoreDeps{Core: e.Core, State: e.State, Cache: e.Cache}),
		Uploads: e.Upload, Preview: previewSvc, Search: e.Search, Flow: nextcloud.NewFlow(e.Flow),
		Features: func() nextcloud.Features {
			return nextcloud.FeaturesFor(e.instanceID, e.thumbnailEnabled(), e.Upload != nil)
		},
		Origin: func(r nextcloud.OriginRequest) string {
			return nextcloud.Origin(func() nextcloud.OriginConfig { return e.ncOriginConfig() }, r)
		},
		ContentOrigin: func(r nextcloud.OriginRequest) string {
			return nextcloud.ContentOrigin(func() nextcloud.OriginConfig { return e.ncOriginConfig() }, r)
		},
		OriginAllowed: e.originAllowed,
		ConsentPage:   nextcloud.ConsentPage(e.csrfKey),
		Resolve: func(user files.UserID, path string, need acl.Perms) (files.Resolved, error) {
			return nextcloud.Resolve(e.Core, user, path, need)
		},
		VpathOf: func(user files.UserID, share files.ShareID, path string) (string, error) {
			return nextcloud.VpathOf(e.Core, user, share, path)
		},
		LocateFile: func(ctx context.Context, user files.UserID, id uint64) (string, error) {
			return nextcloud.LocateFile(ctx, e.Core, e.Cache, user, id)
		},
		SealClaim: seal, OpenClaim: open,
		PublicLinkPath: func(token string) string { return shares.PublicLinkPrefix + "/" + token },
		LockGuard:      e.ncLockGuard, Clock: e.clk(), Logger: e.logger,
	})
}

func (e *Engine) ncOriginConfig() nextcloud.OriginConfig {
	h := e.hosts()
	return nextcloud.OriginConfig{CanonicalURL: e.compatCanonicalURL(), ContentHosts: h.Content, Trusted: e.trustedProxies()}
}

func (e *Engine) ncLockGuard(ctx context.Context, res files.Resolved, principal int64) error {
	return e.guardDavLock(ctx, uint32(res.Share()), res.Path().String(), principal)
}
