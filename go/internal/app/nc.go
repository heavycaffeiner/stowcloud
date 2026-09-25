//go:build linux && compat_nc

// Binding the other product's surface to this engine's services.
//
// Everything the surface cannot reach for itself is assembled here: the
// store-backed facts, the device-login flow, the base URL of a request, and
// the two capabilities that carry a signed claim. The surface owns every
// spelling on the wire and this file owns none of them, which is what keeps
// the vocabulary in one package.
package app

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/links"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/nc"
)

const frontController = "/index.php"

func (e *Engine) declarePublicLinkAliases(app *gin.Engine) {
	e.publicLinks.Declare(app, frontController+links.PublicLinkPrefix)
}

func (e *Engine) mountNCTagged(app *gin.Engine) {
	e.ncServer().Mount(app)
	app.GET(frontController+links.PublicLinkPrefix+"/:token", e.publicLinks.Landing)
	app.POST(frontController+links.PublicLinkPrefix+"/:token/auth", e.publicLinks.Unlock)
	app.GET(frontController+links.PublicLinkPrefix+"/:token/download", e.publicLinks.Download)
	app.GET(frontController+links.PublicLinkPrefix+"/:token/zip", e.publicLinks.Zip)
	app.POST(frontController+links.PublicLinkPrefix+"/:token/drop", e.publicLinks.Drop)
}

func (e *Engine) contentRoute(method, path string) bool {
	return (method == http.MethodGet || method == http.MethodHead) && nc.IsDirectPath(path)
}

func (e *Engine) ncServer() *nc.Server {
	e.thumbnailMu.RLock()
	previewSvc := e.Preview
	e.thumbnailMu.RUnlock()
	seal, open := nc.Claims(e.claimKey, func() int64 { return e.clk().Nanos() })
	return nc.New(nc.Deps{
		Core: e.Core, Auth: e.Auth,
		Store:   nc.NewStore(nc.StoreDeps{Core: e.Core, State: e.State, Cache: e.Cache}),
		Uploads: e.Upload, Preview: previewSvc, Search: e.Search, Flow: nc.NewFlow(e.Flow),
		Features: func() nc.Features { return nc.FeaturesFor(e.instanceID, e.thumbnailEnabled(), e.Upload != nil) },
		Origin: func(r nc.OriginRequest) string {
			return nc.Origin(func() nc.OriginConfig { return e.ncOriginConfig() }, r)
		},
		ContentOrigin: func(r nc.OriginRequest) string {
			return nc.ContentOrigin(func() nc.OriginConfig { return e.ncOriginConfig() }, r)
		},
		OriginAllowed: e.originAllowed,
		ConsentPage:   nc.ConsentPage(e.csrfKey),
		Resolve: func(user core.UserID, path string, need acl.Perms) (core.Resolved, error) {
			return nc.Resolve(e.Core, user, path, need)
		},
		VpathOf: func(user core.UserID, share core.ShareID, path string) (string, error) {
			return nc.VpathOf(e.Core, user, share, path)
		},
		LocateFile: func(ctx context.Context, user core.UserID, id uint64) (string, error) {
			return nc.LocateFile(ctx, e.Core, e.Cache, user, id)
		},
		SealClaim: seal, OpenClaim: open,
		PublicLinkPath: func(token string) string { return links.PublicLinkPrefix + "/" + token },
		LockGuard:      e.ncLockGuard, Clock: e.clk(), Logger: e.log(),
	})
}

func (e *Engine) ncOriginConfig() nc.OriginConfig {
	h := e.hosts()
	return nc.OriginConfig{CanonicalURL: e.compatCanonicalURL(), ContentHosts: h.Content, Trusted: e.trustedProxies()}
}

func (e *Engine) ncLockGuard(ctx context.Context, res core.Resolved, principal int64) error {
	return e.guardDavLock(ctx, uint32(res.Share()), res.Path().String(), principal)
}
