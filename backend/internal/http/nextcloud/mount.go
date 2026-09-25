//go:build linux && compat_nc

package nc

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
)

// The route table.
//
// Every path this surface answers appears once, here, in the two spellings
// every client uses: the clean one and the front-controller one. A client
// builds some of its URLs one way and some the other, in the same session, so
// both are load-bearing and neither is a legacy alias.
//
// The DAV half is served through the standard-library handler rather than the
// framework, because it streams bodies both ways and speaks methods the
// framework's router does not know by name.

// The mount points, as constants so a name that appears in a log can be found.
const (
	statusPath     = "/status.php"
	probePath      = "/204"
	ocsV1Prefix    = "/ocs/v1.php"
	ocsV2Prefix    = "/ocs/v2.php"
	loginBeginPath = "/login/v2"
	loginPollPath  = "/login/v2/poll"
	loginGrantPath = "/login/v2/grant"
	loginFlowPath  = "/login/v2/flow/:token"
	previewPath    = "/core/preview"
	previewPNGPath = "/core/preview.png"
	thumbnailPath  = "/apps/files/api/v1/thumbnail/*path"
	trashPrevPath  = "/apps/files_trashbin/preview"
	avatarPath     = "/avatar/:user/:size"
	directPath     = "/remote.php/direct/:token"
)

// IsDirectPath reports whether the path is the direct stream in either
// spelling. It is the one route family a content host serves, so the boundary
// asks this before it decides which host role a request belongs to.
func IsDirectPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/remote.php/direct/")
	if !ok {
		rest, ok = strings.CutPrefix(path, frontPrefix+"/remote.php/direct/")
	}
	return ok && rest != "" && !strings.Contains(rest, "/")
}

// Mount claims every path.
func (s *Server) Mount(app *gin.Engine) {
	get := func(path string, h gin.HandlerFunc) {
		app.GET(path, h)
		app.GET(frontPrefix+path, h)
	}
	post := func(path string, h gin.HandlerFunc) {
		app.POST(path, h)
		app.POST(frontPrefix+path, h)
	}
	all := func(path string, h gin.HandlerFunc) {
		app.Any(path, h)
		app.Any(frontPrefix+path, h)
	}

	get(statusPath, s.status)
	get(probePath, s.probe)
	all(ocsV1Prefix+"/*path", s.ocs(V1))
	all(ocsV2Prefix+"/*path", s.ocs(V2))
	post(loginBeginPath, s.loginBegin)
	post(loginPollPath, s.loginPoll)
	post(loginGrantPath, s.loginGrant)
	get(loginFlowPath, s.loginConsent)
	handle := func(h func(*gin.Context) error) gin.HandlerFunc {
		return func(c *gin.Context) {
			if err := h(c); err != nil {
				if err := c.Error(err); err != nil {
					c.Abort()
				}
			}
		}
	}
	get(previewPath, handle(s.preview))
	get(previewPNGPath, handle(s.preview))
	get(trashPrevPath, handle(s.preview))
	get(thumbnailPath, handle(s.thumbnailByPath))
	get(avatarPath, handle(s.avatar))
	get(directPath, handle(s.directStream))

	bridge := requestScoped(s.DavHandler())
	serveBridge := func(c *gin.Context) {
		request := c.Request
		if principal, ok := c.Get(string(middleware.KeyCredential)); ok {
			request = request.WithContext(context.WithValue(request.Context(), middleware.KeyCredential, principal))
		}
		bridge.ServeHTTP(c.Writer, request)
		c.Abort()
	}
	davMethods := []string{
		http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
		"COPY", "LOCK", "MKCOL", "MOVE", "PROPFIND", "PROPPATCH", "REPORT", "SEARCH", "UNLOCK",
	}
	for _, prefix := range []string{davMount, webdavMount} {
		for _, path := range []string{prefix, frontPrefix + prefix} {
			for _, method := range davMethods {
				app.Handle(method, path, serveBridge)
				app.Handle(method, path+"/*path", serveBridge)
			}
		}
	}
}

// DavHandler serves the DAV half.
func (s *Server) DavHandler() http.Handler { return http.HandlerFunc(s.serveDav) }

// requestScoped replaces the request context with one cancelled when the
// handler returns, while preserving the middleware values on the request.
func requestScoped(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ocs dispatches one envelope version.
func (s *Server) ocs(v Version) gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" &&
			s.deps.OriginAllowed != nil && s.deps.OriginAllowed(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, OCS-APIRequest, Ocs-Apirequest")
			c.Header("Vary", "Origin")
		}
		if c.Request.Method == http.MethodOptions {
			c.Status(http.StatusOK)
			return
		}
		format := NegotiateFormat(c.Query("format"), c.GetHeader("Accept"))
		data, handled, ocsErr := s.routeOCS(c, v)
		if ocsErr != nil {
			s.WriteOCSError(c, v, format, ocsErr)
			return
		}
		if handled {
			s.WriteOCS(c, v, format, data)
		}
	}
}

func (s *Server) routeOCS(c *gin.Context, v Version) (data Val, handled bool, ocsErr *Error) {
	route := ocsRoute(c.Request.URL.Path, v)
	method := c.Request.Method
	switch {
	case method == http.MethodGet && route == "/cloud/capabilities":
		return s.capabilities(), true, nil
	}
	p, ok := principalOf(c)
	if !ok {
		return Val{}, false, Unauthorized("Unauthorised")
	}
	switch {
	case method == http.MethodGet && route == "/cloud/user":
		return s.currentUser(c, p)
	case method == http.MethodGet && strings.HasPrefix(route, "/cloud/users/"):
		return s.otherUser(c, p, strings.TrimPrefix(route, "/cloud/users/"))
	case method == http.MethodGet && route == "/core/getapppassword":
		return s.appPassword(c, p)
	case method == http.MethodGet && route == "/core/getapppassword-onetime":
		return s.appPassword(c, p)
	case method == http.MethodDelete && route == "/core/apppassword":
		return s.revokeAppPassword(c, p)
	case method == http.MethodGet && route == "/search/providers":
		return s.searchProviders(c, p)
	case method == http.MethodGet && strings.HasPrefix(route, "/search/providers/"):
		return s.searchQuery(c, p, strings.TrimSuffix(strings.TrimPrefix(route, "/search/providers/"), "/search"))
	case method == http.MethodGet && route == "/apps/files/api/v1/recent":
		return s.recentFiles(c, p)
	case method == http.MethodGet && route == "/apps/files/api/v1/favorites":
		return s.favoriteFiles(c, p)
	case method == http.MethodPost && route == "/apps/dav/api/v1/direct":
		return s.directLink(c, p)
	case route == "/apps/files_sharing/api/v1/sharees":
		return s.sharees(c, p)
	case route == "/apps/files_sharing/api/v1/shares":
		switch method {
		case http.MethodGet:
			return s.listShares(c, p)
		case http.MethodPost:
			return s.createShare(c, p)
		}
	case strings.HasPrefix(route, "/apps/files_sharing/api/v1/shares/"):
		id := strings.Trim(strings.TrimPrefix(route, "/apps/files_sharing/api/v1/shares/"), "/")
		switch method {
		case http.MethodGet:
			return s.getShare(c, p, id)
		case http.MethodPut:
			return s.updateShare(c, p, id)
		case http.MethodDelete:
			return s.deleteShare(c, p, id)
		}
	}
	if data, quiet := s.quietRoute(method, route); quiet {
		return data, true, nil
	}
	return Val{}, false, NotFound("no such endpoint")
}

func ocsRoute(path string, v Version) string {
	p := collapseSlashes(path)
	p = strings.TrimPrefix(p, frontPrefix)
	prefix := ocsV2Prefix
	if v == V1 {
		prefix = ocsV1Prefix
	}
	p = strings.TrimPrefix(p, prefix)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimSuffix(p, "/")
}
