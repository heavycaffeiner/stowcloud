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
func (e *Engine) mountNCTagged(public gin.IRoutes, publicAPI, linkPage typed, device gin.IRoutes) {
	e.ncServer().Mount(device)
	// The published document is the same in every build, so the frontend's
	// generated client does not depend on this tag.
	linkPage.hidden, publicAPI.hidden = true, true
	op(linkPage, http.MethodGet, "/index.php/s/{token}", "nc.links.public.get", e.publicLinks.Landing)
	op(publicAPI, http.MethodPost, "/index.php/s/{token}/auth", "nc.links.unlock", e.publicLinks.Unlock("/index.php"+shares.PublicLinkPrefix))
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

// originAllowed reports whether a request Origin may read a compatibility
// response across origins. Only an operator-listed origin is, matched exactly
// after normalization; the list never widens the host guard.
func (e *Engine) originAllowed(origin string) bool {
	return e.Settings != nil && middleware.OriginAllowed(origin, e.Settings.AllowedOrigins())
}

// compatCanonicalURL is the base URL the compatibility surface falls back to
// when a request carries no host to render one from. Empty when unset.
func (e *Engine) compatCanonicalURL() string {
	if e.Settings == nil {
		return ""
	}
	return e.Settings.CompatCanonicalURL()
}

func (e *Engine) ncOriginConfig() nextcloud.OriginConfig {
	h := e.hosts()
	return nextcloud.OriginConfig{CanonicalURL: e.compatCanonicalURL(), ContentHosts: h.Content, Trusted: e.trustedProxies()}
}

func (e *Engine) ncLockGuard(ctx context.Context, res files.Resolved, principal int64) error {
	return e.guardDavLock(ctx, uint32(res.Share()), res.Path().String(), principal)
}
