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
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/compat"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
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
	// Unauthenticated discovery. A client reads both before it has any
	// credential, and a refusal at either reads as a server that is broken
	// rather than one that wants a sign-in.
	app.Get(compatStatusPath, e.compatStatus)
	app.Get(compatWalledPath, func(c *fiber.Ctx) error {
		// The captive-portal probe. Deliberately empty and 204: the Android
		// client reads anything else as "no internet" and parks every upload
		// as pending without ever issuing a request.
		return c.SendStatus(fiber.StatusNoContent)
	})

	app.All(compatOCSv1Prefix+"*", e.compatOCS(compat.V1))
	app.All(compatOCSv2Prefix+"*", e.compatOCS(compat.V2))

	// Login flow v2. There is deliberately no GET on the grant route: a GET
	// that approves is a credential issued to whoever can make a logged-in
	// browser load an image tag. The consent page is what a browser opens;
	// the approval is the POST that page makes.
	app.Post(compatLoginBegin, e.compatLoginBegin)
	app.Post(compatLoginPoll, e.compatLoginPoll)
	app.Post(compatLoginGrant, e.compatLoginGrant)
	app.Get(compatLoginConsent, e.compatLoginConsent)
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
		"version":         compatVersionString,
		"versionstring":   compatVersionString,
		"edition":         "",
		"productname":     "Stowcloud",
		"extendedSupport": false,
	})
}

// compatOCS dispatches one OCS request under one envelope version.
func (e *Engine) compatOCS(version compat.Version) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
	route := "/" + strings.TrimPrefix(c.Path(), versionPrefix(version))

	switch {
	// --- unauthenticated, so a client can read them before signing in ---
	case c.Method() == fiber.MethodGet && route == "/cloud/capabilities":
		return compat.Capabilities(e.compatWiring(), compatVersionString), true, nil

	// --- endpoints that exist only so a client stops asking ---
	case c.Method() == fiber.MethodGet && route == "/apps/notifications/api/v2/notifications":
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/apps/user_status/api/v1/statuses":
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/core/navigation/apps":
		return compat.List(), true, nil
	case c.Method() == fiber.MethodGet && route == "/core/autocomplete/get":
		return compat.List(), true, nil
	case strings.HasPrefix(route, "/apps/provisioning_api/api/v1/config"):
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

	case c.Method() == fiber.MethodGet && route == "/apps/files/api/v1/favorites":
		return e.compatFavorites(c, user)

	case c.Method() == fiber.MethodPost && route == "/apps/dav/api/v1/direct":
		return e.compatDirect(c, user)

	case c.Method() == fiber.MethodDelete && route == "/core/apppassword":
		return e.compatRevokeAppPassword(c, user)
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
	p, ok := c.Locals(string(middleware.KeyCredential)).(middleware.Principal)
	if !ok || p.UserID == 0 {
		return 0, false
	}
	return core.UserID(p.UserID), true
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
	vp, err := vfs.ParseVpath("/files/")
	if err != nil {
		return 0, err
	}
	r, rerr := e.Core.Resolve(user, vp, acl.Read)
	if rerr != nil {
		return 0, rerr
	}
	space, serr := e.Core.FreeSpace(c.UserContext(), r)
	if serr != nil {
		return 0, serr
	}
	return space.Available, nil
}

// compatSearch answers the unified-search provider.
func (e *Engine) compatSearch(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	limit := compat.ParamInt(c.Query("limit"))
	cursor := compat.ParamInt(c.Query("cursor"))

	// The unified-search endpoint needs a bounded, non-streaming query
	// returning entries. The engine's search is a stream over the walk, and
	// a buffer-the-whole-stream wrapper here would be a second answer to how
	// much one query may cost. Empty until that seam exists is the honest
	// shape, and the provider list is what tells the client a search exists
	// at all.
	_ = limit
	_ = cursor
	_ = user
	return compat.SearchPage("Files", nil, -1), true, nil
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

// compatDirect answers the direct-URL request an external player opens.
//
// The endpoint needs an id-to-path lookup under the caller's own tree, which
// the engine's identity registry does not expose as a query yet. Rather than
// a second index answering the same question differently, the endpoint is
// honest about absence: the client falls back to its normal download path,
// which is what it does for any file with no preview, and nothing here
// pretends a URL was minted.
func (e *Engine) compatDirect(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	return compat.Val{}, false, compat.NotFound("File not found")
}

// compatRevokeAppPassword ends the credential that made this request.
//
// Both mobile apps call this when the account is removed from the device.
// Without it the credential the login flow issued outlives the account entry
// on the phone that holds it.
//
// Not served yet: the chain resolves a credential to a principal and drops
// the row id on the way, and this endpoint's whole job is naming that row. A
// revocation that guessed would end somebody else's credential; refusing
// until the id crosses the boundary leaves the caller's credential working
// and revocable from the account screen.
func (e *Engine) compatRevokeAppPassword(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	return compat.Val{}, false, compat.NotFound("no such OCS endpoint")
}

// compatLoginBegin starts a device login.
//
// The origin comes from the request's own host, which the boundary has
// already matched against the declared names. A client that arrived under one
// declared name is sent back to that name rather than to whichever the
// configuration lists first.
func (e *Engine) compatLoginBegin(c *fiber.Ctx) error {
	tokens, err := e.Flow.Begin(c.UserContext(), compatOriginOf(c))
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
	delivery, err := e.Flow.Poll(c.UserContext(), c.FormValue("token"), compatOriginOf(c))
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
	user, ok := compatUser(c)
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "sign in first")
	}

	// The name the client records as the account it signed in as. Read from
	// the account service rather than the credential: the chain's principal
	// carries the id and not the name, and a delivery naming an empty login
	// leaves the app showing an account with no name.
	login := ""
	if info, ierr := e.Auth.AccountInfo(c.UserContext(), int64(user)); ierr == nil {
		login = info.LoginName
	}

	switch err := e.Flow.Approve(c.UserContext(), c.FormValue("token"), int64(user), login); {
	case err == nil:
		return c.SendStatus(fiber.StatusOK)
	case errors.Is(err, ErrFlowApproved):
		return fiber.NewError(fiber.StatusConflict, "this flow is already approved")
	case errors.Is(err, ErrFlowUnknown):
		return fiber.NewError(fiber.StatusNotFound)
	default:
		return fiber.NewError(fiber.StatusInternalServerError, "the approval failed")
	}
}

// compatLoginConsent renders the page the browser opened.
//
// Not served yet: the page needs the session cookie's CSRF derivation and a
// redirect for the signed-out visitor, both of which are the server's rather
// than this surface's, and a mount that half-rendered one would leave a
// person approving something they cannot see. A client opening the login URL
// is told there is nothing there, which is the same answer the flow gave
// before the page existed.
func (e *Engine) compatLoginConsent(c *fiber.Ctx) error {
	return fiber.NewError(fiber.StatusNotFound)
}

// compatOriginOf is the base URL this request arrived on.
//
// Empty when the host header is absent, which leaves the flow's URLs without
// a host rather than inventing one.
func compatOriginOf(c *fiber.Ctx) string {
	host := c.Hostname()
	if host == "" {
		return ""
	}
	return "https://" + host
}

// compatWiring is what the capabilities document derives its promises from.
func (e *Engine) compatWiring() compat.Wiring {
	present := map[compat.Port]bool{
		compat.PortFiles:       true,
		compat.PortAccount:     true,
		compat.PortSearch:      true,
		compat.PortFavorites:   true,
		compat.PortTrash:       true,
		compat.PortUploads:     e.Upload != nil,
		compat.PortAppPassword: true,
		compat.PortLoginFlow:   e.Flow != nil,
	}
	return compat.Wiring{Present: present}
}
