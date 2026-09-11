//go:build linux && compat_nc

package nc

import (
	"context"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
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
	thumbnailPath  = "/apps/files/api/v1/thumbnail/*"
	trashPrevPath  = "/apps/files_trashbin/preview"
	avatarPath     = "/avatar/:user/:size"
	directPath     = "/remote.php/direct/:token"
)

// IsDirectPath reports whether a path is the direct stream in either
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
//
// Mounted after the middleware chain like every other surface, so the
// boundary, the limiter and the credential resolution all see these requests.
// What varies per route is what the handler does with the principal it is
// handed, not whether the chain looked for one.
func (s *Server) Mount(app *fiber.App) {
	// Both spellings of every framework route. The loop is what keeps the two
	// from drifting apart, which is how one client's calls kept working while
	// another's stopped.
	get := func(path string, h fiber.Handler) {
		app.Get(path, h)
		app.Get(frontPrefix+path, h)
	}
	post := func(path string, h fiber.Handler) {
		app.Post(path, h)
		app.Post(frontPrefix+path, h)
	}
	all := func(path string, h fiber.Handler) {
		app.All(path, h)
		app.All(frontPrefix+path, h)
	}

	get(statusPath, s.status)
	get(probePath, s.probe)

	all(ocsV1Prefix+"/*", s.ocs(V1))
	all(ocsV2Prefix+"/*", s.ocs(V2))

	post(loginBeginPath, s.loginBegin)
	post(loginPollPath, s.loginPoll)
	post(loginGrantPath, s.loginGrant)
	get(loginFlowPath, s.loginConsent)

	get(previewPath, s.preview)
	get(previewPNGPath, s.preview)
	get(trashPrevPath, s.preview)
	get(thumbnailPath, s.thumbnailByPath)
	get(avatarPath, s.avatar)
	get(directPath, s.directStream)

	bridge := adaptor.HTTPHandler(requestScoped(s.DavHandler()))
	for _, prefix := range []string{davMount, webdavMount} {
		for _, p := range []string{prefix, frontPrefix + prefix} {
			app.All(p, bridge)
			app.All(p+"/*", bridge)
		}
	}
}

// DavHandler serves the DAV half.
func (s *Server) DavHandler() http.Handler {
	return http.HandlerFunc(s.serveDav)
}

// requestScoped replaces the adapter's context with one this handler owns.
//
// The adapter hands over fasthttp's own request object as the context, and
// fasthttp rewrites that object when the server shuts down and when it
// reuses it for the next request. Anything that installs a cancellation
// watcher on it keeps a reference past that point: every database/sql query
// does, which is how a listing that reads an owner's groups ends up racing
// the shutdown. Retaining it is what fasthttp documents as forbidden.
//
// The replacement keeps the values the chain put there, installs no watcher
// on the request object, and is cancelled when the handler returns so
// nothing started here outlives the request either.
func requestScoped(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ocs dispatches one envelope version.
//
// The cross-origin headers are answered here rather than by the chain because
// this vocabulary's own clients include browser-hosted ones that preflight
// every call, and a preflight that reaches a handler expecting a credential
// answers 401 to a request that carried none by design.
//
// Only an origin the operator listed is answered, and it is echoed rather
// than replaced by a wildcard: the allowlist is what makes a response
// readable across origins, and no origin outside it reads one. Credentials
// are never allowed across origins, so the list confers no trust.
func (s *Server) ocs(v Version) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if origin := c.Get(fiber.HeaderOrigin); origin != "" &&
			s.deps.OriginAllowed != nil && s.deps.OriginAllowed(origin) {
			c.Set(fiber.HeaderAccessControlAllowOrigin, origin)
			c.Set(fiber.HeaderAccessControlAllowMethods, "GET, POST, PUT, DELETE, OPTIONS")
			c.Set(fiber.HeaderAccessControlAllowHeaders, "Authorization, Content-Type, OCS-APIRequest, Ocs-Apirequest")
			c.Vary(fiber.HeaderOrigin)
		}
		if c.Method() == fiber.MethodOptions {
			return c.SendStatus(fiber.StatusOK)
		}

		format := NegotiateFormat(c.Query("format"), c.Get(fiber.HeaderAccept))
		data, handled, ocsErr := s.routeOCS(c, v)
		if ocsErr != nil {
			return s.WriteOCSError(c, v, format, ocsErr)
		}
		if !handled {
			// The handler answered the request itself, which is what a
			// response carrying something other than an envelope does.
			return nil
		}
		return s.WriteOCS(c, v, format, data)
	}
}

// routeOCS answers one OCS request.
//
// The prefix and the version are already peeled off; route is what remains.
// Handled reports whether an envelope should be written around the returned
// value: a handler that answered itself reports false and nothing more is
// sent.
func (s *Server) routeOCS(c *fiber.Ctx, v Version) (data Val, handled bool, ocsErr *Error) {
	route := ocsRoute(c.Path(), v)
	method := c.Method()

	// What a client reads before it has a credential. Everything below this
	// block speaks for an account.
	switch {
	case method == fiber.MethodGet && route == "/cloud/capabilities":
		return s.capabilities(), true, nil
	}

	p, ok := principalOf(c)
	if !ok {
		return Val{}, false, Unauthorized("Unauthorised")
	}

	switch {
	case method == fiber.MethodGet && route == "/cloud/user":
		return s.currentUser(c, p)
	case method == fiber.MethodGet && strings.HasPrefix(route, "/cloud/users/"):
		return s.otherUser(c, p, strings.TrimPrefix(route, "/cloud/users/"))

	case method == fiber.MethodGet && route == "/core/getapppassword":
		return s.appPassword(c, p)
	case method == fiber.MethodGet && route == "/core/getapppassword-onetime":
		return s.appPassword(c, p)
	case method == fiber.MethodDelete && route == "/core/apppassword":
		return s.revokeAppPassword(c, p)

	case method == fiber.MethodGet && route == "/search/providers":
		return s.searchProviders(c, p)
	case method == fiber.MethodGet && strings.HasPrefix(route, "/search/providers/"):
		return s.searchQuery(c, p, strings.TrimSuffix(strings.TrimPrefix(route, "/search/providers/"), "/search"))

	case method == fiber.MethodGet && route == "/apps/files/api/v1/recent":
		return s.recentFiles(c, p)
	case method == fiber.MethodGet && route == "/apps/files/api/v1/favorites":
		return s.favoriteFiles(c, p)

	case method == fiber.MethodPost && route == "/apps/dav/api/v1/direct":
		return s.directLink(c, p)

	case route == "/apps/files_sharing/api/v1/sharees":
		return s.sharees(c, p)
	case route == "/apps/files_sharing/api/v1/shares":
		switch method {
		case fiber.MethodGet:
			return s.listShares(c, p)
		case fiber.MethodPost:
			return s.createShare(c, p)
		}
	case strings.HasPrefix(route, "/apps/files_sharing/api/v1/shares/"):
		id := strings.Trim(strings.TrimPrefix(route, "/apps/files_sharing/api/v1/shares/"), "/")
		switch method {
		case fiber.MethodGet:
			return s.getShare(c, p, id)
		case fiber.MethodPut:
			return s.updateShare(c, p, id)
		case fiber.MethodDelete:
			return s.deleteShare(c, p, id)
		}
	}

	// The endpoints that exist so a client stops asking. Each one is a screen
	// a client opens on its own initiative, and a refusal there is shown to a
	// person as an account that is failing rather than a feature this server
	// does not carry.
	if data, quiet := s.quietRoute(method, route); quiet {
		return data, true, nil
	}

	return Val{}, false, NotFound("no such endpoint")
}

// ocsRoute strips the mount and the version prefix from a request path.
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
