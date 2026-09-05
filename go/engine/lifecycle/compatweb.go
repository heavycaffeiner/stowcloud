//go:build linux && compat_nc

// The compatibility layer's HTTP surface.
//
// Mounted beside the native API on paths another product's clients address.
// The envelope and the wire vocabulary live in the http/compat package; what
// this file does is join those to the engine's services and decide, per
// route, what a request needs before it reaches one.
package lifecycle

import (
	"errors"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"net/netip"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/compat"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
)

// The mount points another product's clients address. Data, not constants in
// the handlers: a name that appears in one place is a name that can be found.
const (
	compatStatusPath    = "/status.php"
	compatWalledPath    = "/index.php/204"
	compatOCSv1Prefix   = "/ocs/v1.php/"
	compatOCSv2Prefix   = "/ocs/v2.php/"
	compatLoginBegin    = "/index.php/login/v2"
	compatLoginPoll     = "/index.php/login/v2/poll"
	compatLoginGrant    = "/index.php/login/v2/grant"
	compatLoginConsent  = "/index.php/login/v2/flow/:token"
	compatVersionString = "31.0.4"
)

// mountCompat claims the compatibility paths.
//
// Mounted after the chain like every other surface, so the boundary, the
// limiter and the credential resolution all see these requests. What varies
// per route is what the handler does with the principal it is handed, not
// whether the chain looked for one.
func (e *Engine) mountCompatTagged(app *fiber.App) {
	app.Get("/status.php", e.compatStatus)
	app.Get("/index.php/status.php", e.compatStatus)

	walled := func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	}
	app.Get("/index.php/204", walled)
	app.Get("/204", walled)

	app.All(compatOCSv1Prefix+"*", e.compatOCS(compat.V1))
	app.All("/index.php"+compatOCSv1Prefix+"*", e.compatOCS(compat.V1))
	app.All(compatOCSv2Prefix+"*", e.compatOCS(compat.V2))
	app.All("/index.php"+compatOCSv2Prefix+"*", e.compatOCS(compat.V2))

	app.Post("/login/v2", e.compatLoginBegin)
	app.Post("/index.php/login/v2", e.compatLoginBegin)
	app.Post("/login/v2/poll", e.compatLoginPoll)
	app.Post("/index.php/login/v2/poll", e.compatLoginPoll)
	app.Post("/login/v2/grant", e.compatLoginGrant)
	app.Post("/index.php/login/v2/grant", e.compatLoginGrant)
	app.Get("/login/v2/flow/:token", e.compatLoginConsent)
	app.Get("/index.php/login/v2/flow/:token", e.compatLoginConsent)

	// Previews and thumbnails
	app.Get("/core/preview", e.compatPreview)
	app.Get("/index.php/core/preview", e.compatPreview)
	app.Get("/core/preview.png", e.compatPreview)
	app.Get("/index.php/core/preview.png", e.compatPreview)
	app.Get("/apps/files/api/v1/thumbnail/*", e.compatThumbnailByPath)
	app.Get("/index.php/apps/files/api/v1/thumbnail/*", e.compatThumbnailByPath)
	app.Get("/apps/files_trashbin/preview", e.compatPreview)
	app.Get("/index.php/apps/files_trashbin/preview", e.compatPreview)
	app.Get("/avatar/*", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNotFound) })
	app.Get("/index.php/avatar/*", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNotFound) })

	// Direct stream URL
	app.Get("/remote.php/direct/:claim", e.compatDirectStream)
	app.Get("/index.php/remote.php/direct/:claim", e.compatDirectStream)

	// The reference spells a public link both ways, and its clients build the
	// "/index.php/s/" form from a token. Unmounted, that address fell through
	// to the single-page application, so a link opened to a document instead
	// of to the file it names.
	app.Get("/index.php"+PublicLinkPrefix+"/:token", e.linkLanding)
	app.Post("/index.php"+PublicLinkPrefix+"/:token/auth", e.linkUnlock)
	app.Get("/index.php"+PublicLinkPrefix+"/:token/download", e.linkDownload)
	app.Get("/index.php"+PublicLinkPrefix+"/:token/zip", e.linkZip)
	app.Post("/index.php"+PublicLinkPrefix+"/:token/drop", e.linkDrop)
	// Additional app stubs
	emptyObj := func(c *fiber.Ctx) error { return c.Status(fiber.StatusOK).JSON(fiber.Map{}) }
	app.All("/apps/richdocuments/assets", emptyObj)
	app.All("/index.php/apps/richdocuments/assets", emptyObj)
}

// compatStatus answers the pre-sign-in probe.
//
// It carries nothing a stranger could use: version and product name, both of
// which the capabilities document repeats to an authenticated caller anyway.
func (e *Engine) compatStatus(c *fiber.Ctx) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"installed":       true,
		"maintenance":     false,
		"needsDbUpgrade":  false,
		"version":         compatVersionString + ".1",
		"versionstring":   compatVersionString,
		"edition":         "",
		"productname":     "Nextcloud",
		"extendedSupport": false,
	})
}

// compatOCS dispatches one OCS request under one envelope version.
func (e *Engine) compatOCS(version compat.Version) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("Access-Control-Allow-Origin", "*")
		c.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, OCS-APIREQUEST")
		if c.Method() == fiber.MethodOptions {
			return c.SendStatus(fiber.StatusOK)
		}

		format := compat.NegotiateFormat(c.Query("format"), c.Get("Accept"))

		data, written, ocsErr := e.compatRouteOCS(c, version)
		if ocsErr != nil {
			return compat.WriteOCSError(c, version, format, ocsErr)
		}
		if !written {
			return nil
		}
		return compat.WriteOCS(c, version, format, data)
	}
}

// compatRouteOCS answers one OCS route. The prefix and the version have been
// peeled off by the time this runs; route is what remains. Written reports
// whether a body was produced; a handler that answered itself reports false
// and nothing more is sent.
func (e *Engine) compatRouteOCS(
	c *fiber.Ctx, version compat.Version,
) (data compat.Val, written bool, ocsErr *compat.OCSError) {
	p := strings.TrimPrefix(c.Path(), "/index.php")
	route := "/" + strings.TrimPrefix(strings.TrimPrefix(p, versionPrefix(version)), "/")

	switch {
	// --- unauthenticated, so a client can read them before signing in ---
	case c.Method() == fiber.MethodGet && route == "/cloud/capabilities":
		return compat.Capabilities(e.compatWiring(), compatVersionString), true, nil

	// --- endpoints that exist only so a client stops asking ---
	case strings.HasPrefix(route, "/apps/notifications/"):
		if strings.HasSuffix(route, "/push") {
			return compat.Object(), true, nil
		}
		return compat.List(), true, nil
	case strings.HasPrefix(route, "/apps/user_status/"):
		if route == "/apps/user_status/api/v1/user_status" {
			return compat.Object(compat.P("status", compat.Str("online")), compat.P("message", compat.Str("")), compat.P("icon", compat.Str(""))), true, nil
		}
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/core/navigation/apps":
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/core/autocomplete/get":
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/apps/files/api/v1/templates":
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/apps/files/api/v1/direct_editing":
		return compat.Object(), true, nil
	case strings.HasPrefix(route, "/apps/provisioning_api/api/v1/config"):
		return compat.Object(), true, nil
	case strings.HasPrefix(route, "/apps/richdocuments/"):
		return compat.Object(), true, nil
	}

	// What follows speaks for an account, so an unauthenticated request stops
	// at the envelope's own refusal rather than reaching a handler that would
	// have nothing to answer about.
	user, ok := compatUser(c)
	if !ok {
		return compat.Val{}, false, compat.Unauthorized("Unauthorised")
	}

	switch {
	case c.Method() == fiber.MethodGet && route == "/cloud/user":
		return e.compatCurrentUser(c, user)

	case c.Method() == fiber.MethodGet && strings.HasPrefix(route, "/cloud/users/"):
		return e.compatOtherUser(c, user, strings.TrimPrefix(route, "/cloud/users/"))

	case c.Method() == fiber.MethodGet && route == "/search/providers":
		return compat.SearchProviders(), true, nil
	case c.Method() == fiber.MethodGet && route == "/search/providers/files/search":
		return e.compatSearch(c, user)

	case c.Method() == fiber.MethodGet && route == "/apps/files/api/v1/recent":
		return e.compatRecent(c, user)

	case c.Method() == fiber.MethodGet && route == "/apps/files/api/v1/favorites":
		return e.compatFavorites(c, user)

	case c.Method() == fiber.MethodPost && route == "/apps/dav/api/v1/direct":
		return e.compatDirect(c, user)

	case c.Method() == fiber.MethodDelete && route == "/core/apppassword":
		return e.compatRevokeAppPassword(c, user)

	case c.Method() == fiber.MethodGet && route == "/apps/files_sharing/api/v1/sharees":
		return e.compatSharees(c, user)

	case c.Method() == fiber.MethodGet && route == "/apps/files_sharing/api/v1/shares":
		return e.compatListShares(c, user)
	case c.Method() == fiber.MethodPost && route == "/apps/files_sharing/api/v1/shares":
		return e.compatCreateShare(c, user)
	case c.Method() == fiber.MethodGet && strings.HasPrefix(route, "/apps/files_sharing/api/v1/shares/"):
		return e.compatGetShare(c, user, strings.Trim(strings.TrimPrefix(route, "/apps/files_sharing/api/v1/shares/"), "/"))
	case c.Method() == fiber.MethodPut && strings.HasPrefix(route, "/apps/files_sharing/api/v1/shares/"):
		return e.compatUpdateShare(c, user, strings.Trim(strings.TrimPrefix(route, "/apps/files_sharing/api/v1/shares/"), "/"))
	case c.Method() == fiber.MethodDelete && strings.HasPrefix(route, "/apps/files_sharing/api/v1/shares/"):
		return e.compatDeleteShare(c, user, strings.Trim(strings.TrimPrefix(route, "/apps/files_sharing/api/v1/shares/"), "/"))
	}

	return compat.Val{}, false, compat.NotFound("no such OCS endpoint")
}

// versionPrefix is the mount prefix an envelope version arrived under.
func versionPrefix(v compat.Version) string {
	if v == compat.V1 {
		return compatOCSv1Prefix
	}
	return compatOCSv2Prefix
}

// compatUser reads the principal the chain resolved. Zero is not an account:
// a credential that proved nothing leaves nothing for a compat surface to
// answer as.
func compatUser(c *fiber.Ctx) (core.UserID, bool) {
	p, ok := c.Locals(middleware.KeyCredential).(middleware.Principal)
	if !ok || p.UserID == 0 {
		return 0, false
	}
	return core.UserID(p.UserID), true
}

// resolveCompat resolves a path for Nextcloud compat endpoints while enforcing the caller's credential mask.
func (e *Engine) resolveCompat(c *fiber.Ctx, user core.UserID, path string, want acl.Perms) (core.Resolved, error) {
	res, err := e.resolve(user, path, want)
	if err != nil {
		return res, err
	}
	if p, ok := c.Locals(middleware.KeyCredential).(middleware.Principal); ok {
		if len(p.Shares) > 0 {
			vp, perr := vfs.ParseVpath(path)
			if perr == nil && vp.Label() != "" {
				allowed := false
				for _, s := range p.Shares {
					if s == vp.Label() {
						allowed = true
						break
					}
				}
				if !allowed {
					return core.Resolved{}, core.ErrNotFound
				}
			}
		}
		if !p.Mask.IsEmpty() {
			res = res.WithMask(p.Mask)
			if !res.Has(want) {
				return core.Resolved{}, core.ErrDenied
			}
		}
	}
	return res, nil
}

// compatCurrentUser answers the caller's own account record.
func (e *Engine) compatCurrentUser(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	info, quota, ocsErr := e.compatAccount(c, user)
	if ocsErr != nil {
		return compat.Val{}, false, ocsErr
	}
	return compat.CurrentUser(info, quota), true, nil
}

// compatOtherUser answers a lookup by login name.
//
// Outside the caller's scope and no such account are one answer. A different
// one would let a stranger learn which logins exist.
func (e *Engine) compatOtherUser(
	c *fiber.Ctx, caller core.UserID, login string,
) (compat.Val, bool, *compat.OCSError) {
	notFound := compat.NotFound("User does not exist")
	if login == "" || strings.Contains(login, "/") {
		return compat.Val{}, false, notFound
	}

	info, err := e.Auth.AccountInfo(c.UserContext(), int64(caller))
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not read account")
	}
	if info.LoginName == login {
		return compat.OtherUser(compatUserInfoOf(info)), true, nil
	}

	other, ok, err := e.Auth.AccountInfoByLogin(c.UserContext(), int64(caller), login)
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not read account")
	}
	if !ok {
		return compat.Val{}, false, notFound
	}
	return compat.OtherUser(compatUserInfoOf(other)), true, nil
}

// compatAccount reads one account's projection and the quota beside it.
func (e *Engine) compatAccount(
	c *fiber.Ctx, user core.UserID,
) (compat.UserInfo, compat.Quota, *compat.OCSError) {
	info, err := e.Auth.AccountInfo(c.UserContext(), int64(user))
	if err != nil {
		return compat.UserInfo{}, compat.Quota{}, compat.ServerError("could not read account")
	}

	free, ferr := e.compatFreeSpace(c, user)
	if ferr != nil {
		return compat.UserInfo{}, compat.Quota{}, compat.ServerError("could not read storage")
	}
	return compatUserInfoOf(info), compatQuotaOf(info, free), nil
}

// compatUserInfoOf projects the auth service's account record into the wire
// shape.
func compatUserInfoOf(info auth.AccountInfo) compat.UserInfo {
	return compat.UserInfo{
		LoginName:   info.LoginName,
		DisplayName: info.DisplayName,
		Enabled:     info.Enabled,
		Groups:      info.Groups,
		Language:    "",
		Locale:      "",
	}
}

// compatQuotaOf renders the quota block's inputs.
//
// Total carries the configured cap, which may be absent: no cap is a
// different fact from a cap of zero, and every client renders the two
// differently. Free is the storage's real free space either way, because the
// Android client compares a file's size against it before starting an upload,
// and a fabricated number there is an upload that never begins.
func compatQuotaOf(info auth.AccountInfo, free uint64) compat.Quota {
	var total *uint64
	if info.QuotaBytes != nil && *info.QuotaBytes > 0 {
		t := uint64(*info.QuotaBytes)
		total = &t
	}
	return compat.Quota{
		Used:  info.UsageBytes,
		Free:  free,
		Total: total,
	}
}

// compatFreeSpace is what the account's own home has left.
//
// Read at a root the account can reach, because free space is a property of
// a filesystem rather than of an account, and the home is where a client's
// default upload goes.
func (e *Engine) compatFreeSpace(c *fiber.Ctx, user core.UserID) (uint64, error) {
	for _, rt := range e.Core.Roots(user) {
		vp, err := vfs.ParseVpath("/" + rt.Label + "/")
		if err != nil {
			continue
		}
		r, rerr := e.Core.Resolve(user, vp, acl.Read)
		if rerr != nil {
			continue
		}
		space, serr := e.Core.FreeSpace(c.UserContext(), r)
		if serr == nil && space.Available > 0 {
			return space.Available, nil
		}
	}
	vp, err := vfs.ParseVpath("/files/")
	if err == nil {
		if r, rerr := e.Core.Resolve(user, vp, acl.Read); rerr == nil {
			if space, serr := e.Core.FreeSpace(c.UserContext(), r); serr == nil {
				return space.Available, nil
			}
		}
	}
	return 0, nil
}

func (e *Engine) compatSearch(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	limit := compat.ParamInt(c.Query("limit"))
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	cursor := compat.ParamInt(c.Query("cursor"))
	if cursor < 0 {
		cursor = 0
	}
	queryLimit := cursor + limit + 1
	if queryLimit > limits.SearchResults {
		queryLimit = limits.SearchResults
	}
	term := strings.TrimSpace(c.Query("term"))
	if term == "" {
		return compat.SearchPage("Files", nil, -1), true, nil
	}
	if e.Search == nil {
		return compat.Val{}, false, compat.ServerError("search is unavailable")
	}

	results, err := e.Search.Query(c.UserContext(),
		searchSourcesOf(e.Core.UserScanSources(user), user, e.Core),
		svc.QueryOptions{Query: term, Limit: queryLimit},
	)
	if err != nil {
		return compat.Val{}, false, compat.ServerError("search failed")
	}
	if cursor >= len(results.Hits) {
		return compat.SearchPage("Files", nil, -1), true, nil
	}

	end := cursor + limit
	if end > len(results.Hits) {
		end = len(results.Hits)
	}
	entries := make([]compat.Val, 0, end-cursor)
	origin := e.compatOriginOf(c)
	for _, hit := range results.Hits[cursor:end] {
		path := "/" + strings.TrimPrefix(hit.Path, "/")
		r, rerr := e.resolve(user, strings.TrimPrefix(path, "/"), acl.Read)
		if rerr != nil {
			continue
		}
		st, serr := r.Root().Stat(r.Path())
		if serr != nil {
			continue
		}
		entry := e.Core.EntryAt(r, st)
		fid, ferr := e.compatFileID(c.UserContext(), entry)
		if ferr != nil {
			continue
		}
		thumbURL := fmt.Sprintf("%s/index.php/core/preview?fileId=%d&x=64&y=64", origin, fid)
		entries = append(entries, compat.SearchEntry(hit.Name, path, fid, thumbURL, origin))
	}

	next := -1
	if end < len(results.Hits) || (results.Truncated && queryLimit < limits.SearchResults) {
		next = end
	}
	return compat.SearchPage("Files", entries, next), true, nil
}

// compatFavorites answers the starred list.
func (e *Engine) compatFavorites(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	rows, err := e.State.Favorites(c.UserContext(), int64(user))
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not read favourites")
	}
	out := make([]compat.Val, 0, len(rows))
	for _, r := range rows {
		out = append(out, compat.Favorite(r.Path))
	}
	return compat.List(out...), true, nil
}

// compatLoginBegin starts a device login.
//
// The origin comes from the request's own host, which the boundary has
// already matched against the declared names. A client that arrived under one
// declared name is sent back to that name rather than to whichever the
// configuration lists first.
func (e *Engine) compatLoginBegin(c *fiber.Ctx) error {
	tokens, err := e.Flow.Begin(c.UserContext(), e.compatOriginOf(c))
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "the login flow could not be started")
	}
	return compat.WriteBareJSON(c, compat.LoginBeginJSON(compat.LoginTokens{
		PollToken:    tokens.PollToken,
		LoginURL:     tokens.LoginURL,
		PollEndpoint: tokens.PollEndpoint,
	}))
}

// compatLoginPoll delivers the credential once a person has approved.
//
// A pending flow answers 404: that is the "not yet" the client polls against,
// not an error.
func (e *Engine) compatLoginPoll(c *fiber.Ctx) error {
	delivery, err := e.Flow.Poll(c.UserContext(), c.FormValue("token"), e.compatOriginOf(c))
	switch {
	case err == nil:
		return compat.WriteBareJSON(c, compat.LoginPollJSON(compat.LoginDelivery{
			Server:      delivery.Server,
			LoginName:   delivery.LoginName,
			AppPassword: delivery.AppPassword,
		}))
	case errors.Is(err, ErrFlowRateLimited):
		return c.SendStatus(fiber.StatusTooManyRequests)
	case errors.Is(err, ErrFlowPending), errors.Is(err, ErrFlowUnknown):
		// One answer for pending, unknown, expired and taken. Telling them
		// apart tells a prober which tokens exist.
		return fiber.NewError(fiber.StatusNotFound)
	default:
		return fiber.NewError(fiber.StatusInternalServerError, "the poll failed")
	}
}

// compatLoginGrant records a person's approval.
//
// Reached only through the consent page's POST: a session and a CSRF token
// were both checked by the chain before this ran, which is the whole of what
// makes a credential mint here anything other than an account takeover.
func (e *Engine) compatLoginGrant(c *fiber.Ctx) error {
	p, ok := c.Locals(middleware.KeyCredential).(middleware.Principal)
	if !ok || p.UserID == 0 {
		e.logger.Warn("login grant refused: no authenticated user session")
		return fiber.NewError(fiber.StatusUnauthorized, "sign in first")
	}
	if p.Kind != middleware.CredentialSessionCookie {
		e.logger.Warn("login grant refused: non-session credential used to grant device login", "kind", p.Kind)
		return fiber.NewError(fiber.StatusForbidden, "only browser sessions may grant device logins")
	}
	user := core.UserID(p.UserID)

	login := ""
	if info, ierr := e.Auth.AccountInfo(c.UserContext(), int64(user)); ierr == nil {
		login = info.LoginName
	}

	tok := c.FormValue("token")
	if tok == "" {
		tok = c.Query("token")
	}
	if tok == "" {
		var req struct {
			Token string `json:"token"`
		}
		if perr := c.BodyParser(&req); perr == nil {
			tok = req.Token
		}
	}
	if tok == "" {
		e.logger.Warn("login grant refused: empty token")
		return fiber.NewError(fiber.StatusBadRequest, "missing token")
	}

	switch err := e.Flow.Approve(c.UserContext(), tok, int64(user), login); {
	case err == nil, errors.Is(err, ErrFlowApproved):
		return c.SendStatus(fiber.StatusOK)
	case errors.Is(err, ErrFlowUnknown):
		e.logger.Warn("login grant refused: flow unknown or expired", "user", user)
		return fiber.NewError(fiber.StatusNotFound, "flow unknown or expired")
	default:
		e.logger.Warn("login grant failed", "user", user, "error", err)
		return fiber.NewError(fiber.StatusInternalServerError, "the approval failed")
	}
}

// compatOriginOf is the base URL this request arrived on.
//
// Empty when the host header is absent, which leaves the flow's URLs without
func (e *Engine) isPeerTrusted(c *fiber.Ctx) bool {
	rawIP := c.Context().RemoteIP()
	if rawIP == nil {
		return false
	}
	addr, err := netip.ParseAddr(rawIP.String())
	if err != nil {
		return false
	}
	for _, prefix := range e.trustedPrefixes() {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (e *Engine) compatOriginOf(c *fiber.Ctx) string {
	host := string(c.Request().Host())
	if host == "" {
		host = c.Hostname()
	}

	trusted := e.isPeerTrusted(c)
	if trusted {
		if xfh := c.Get("X-Forwarded-Host"); xfh != "" {
			if !strings.ContainsAny(xfh, "/\\@") {
				host = xfh
			}
		} else if xfp := c.Get("X-Forwarded-Port"); xfp != "" && !strings.Contains(host, ":") {
			if xfp != "80" && xfp != "443" {
				host = host + ":" + xfp
			}
		}
	}
	if host == "" {
		return ""
	}

	scheme := "http"
	if c.Context().IsTLS() || c.Protocol() == "https" {
		scheme = "https"
	} else if trusted {
		if strings.EqualFold(c.Get("X-Forwarded-Proto"), "https") ||
			strings.EqualFold(c.Get("X-Forwarded-Ssl"), "on") ||
			strings.EqualFold(c.Get("Front-End-Https"), "on") {
			scheme = "https"
		}
	}
	return scheme + "://" + host
}

func (e *Engine) compatWiring() compat.Wiring {
	present := map[compat.Port]bool{
		compat.PortFiles:         true,
		compat.PortAccount:       true,
		compat.PortSearch:        e.Search != nil,
		compat.PortFavorites:     true,
		compat.PortTrash:         true,
		compat.PortUploads:       e.Upload != nil,
		compat.PortAppPassword:   true,
		compat.PortLoginFlow:     e.Flow != nil,
		compat.PortSharing:       true,
		compat.PortLinks:         true,
		compat.PortContentOrigin: e.thumbnailEnabled(),
	}
	return compat.Wiring{Present: present}
}
