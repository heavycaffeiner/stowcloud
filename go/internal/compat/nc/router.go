//go:build compat_nc

package nc

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The mounts.
//
// Each is a plain handler the assembly registers when the tag is on. Every one
// resolves the client address through the same trusted-proxy step the native
// API uses, because there is exactly one implementation of "who is this
// request from" and a compat mount with its own copy is how that stops being
// true. That step is applied by the chain the assembly wraps these in, which
// is why nothing here reads a forwarding header.

// The mount prefixes.
const (
	MountStatus       = "/status.php"
	MountWalledGarden = "/index.php/204"
	MountOCSv1        = "/ocs/v1.php/"
	MountOCSv2        = "/ocs/v2.php/"
	MountLoginBegin   = "/index.php/login/v2"
	MountLoginPoll    = "/index.php/login/v2/poll"
	MountLoginGrant   = "/index.php/login/v2/grant"
)

// Mounts is every route the layer owns.
func (l *Layer) Mounts() []Mount {
	return []Mount{
		// Unauthenticated discovery, which is what a client fetches before it
		// knows anything else about this server.
		{Method: "GET", Pattern: MountStatus, Handler: http.HandlerFunc(l.status)},

		// The captive-portal probe. It is deliberately unauthenticated and
		// empty: the Android client treats anything else as "no internet" and
		// parks every upload as pending without ever issuing a request, and an
		// unmounted path answering 401 is exactly the signal it reads as a
		// portal having intercepted it.
		{Method: "GET", Pattern: MountWalledGarden, Handler: http.HandlerFunc(l.walledGarden)},

		// Both OCS versions, because a client may call either and the version
		// decides the envelope.
		{Method: "GET", Pattern: MountOCSv1, Handler: l.ocsHandler(OCSv1)},
		{Method: "POST", Pattern: MountOCSv1, Handler: l.ocsHandler(OCSv1)},
		{Method: "PUT", Pattern: MountOCSv1, Handler: l.ocsHandler(OCSv1)},
		{Method: "DELETE", Pattern: MountOCSv1, Handler: l.ocsHandler(OCSv1)},
		{Method: "GET", Pattern: MountOCSv2, Handler: l.ocsHandler(OCSv2)},
		{Method: "POST", Pattern: MountOCSv2, Handler: l.ocsHandler(OCSv2)},
		{Method: "PUT", Pattern: MountOCSv2, Handler: l.ocsHandler(OCSv2)},
		{Method: "DELETE", Pattern: MountOCSv2, Handler: l.ocsHandler(OCSv2)},

		// Login flow v2. There is deliberately no GET route for the approval:
		// a GET that approves is a credential-issuance hole reachable from a
		// drive-by image tag.
		{Method: "POST", Pattern: MountLoginBegin, Handler: http.HandlerFunc(l.loginBegin)},
		{Method: "POST", Pattern: MountLoginPoll, Handler: http.HandlerFunc(l.loginPoll)},
		{Method: "POST", Pattern: MountLoginGrant, Handler: http.HandlerFunc(l.loginGrant)},
	}
}

// status answers the pre-login probe.
//
// A client reads this before it has credentials, so it carries nothing about
// anything a stranger should not see. It is deliberately not the capabilities
// document, which needs an authenticated principal.
func (l *Layer) status(w http.ResponseWriter, r *http.Request) {
	doc := VMap(
		F("installed", VBool(true)),
		F("maintenance", VBool(false)),
		F("needsDbUpgrade", VBool(false)),
		F("version", VStr(l.caps.VersionString)),
		F("versionstring", VStr(l.caps.VersionString)),
		F("edition", VStr(l.caps.Edition)),
		F("productname", VStr(l.caps.ThemingName)),
		F("extendedSupport", VBool(false)),
	)
	writeBareJSON(w, doc)
}

// walledGarden answers the captive-portal probe with nothing at all.
func (l *Layer) walledGarden(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// ocsHandler dispatches an OCS request.
func (l *Layer) ocsHandler(version OCSVersion) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format := NegotiateFormat(r.URL.RawQuery, r.Header)

		// The four paths with a recorded client crash answer 404 before
		// anything else looks at them.
		if IsNotFoundPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		route := strings.TrimPrefix(r.URL.Path, "/ocs/v1.php")
		route = strings.TrimPrefix(route, "/ocs/v2.php")

		if data, err := l.routeOCS(w, r, route, version, format); err != nil {
			Fail(version, format, err).Write(w)
		} else if data.Kind != KindNull {
			OK(version, format, data).Write(w)
		}
	})
}

// routeOCS runs one request.
//
// A nil error with a null value means the handler already answered, which is
// how the surfaces that write their own response opt out of the envelope.
func (l *Layer) routeOCS(
	w http.ResponseWriter, r *http.Request, route string, version OCSVersion, format OCSFormat,
) (Val, *OCSError) {
	q := r.URL.Query()

	switch {
	// --- unauthenticated ---
	case r.Method == "GET" && route == "/cloud/capabilities":
		return Capabilities(l.caps), nil

	// --- the stubs, which need no principal ---
	case r.Method == "GET" && route == "/apps/notifications/api/v2/notifications":
		return Notifications(), nil
	case r.Method == "GET" && route == "/apps/user_status/api/v1/statuses":
		return UserStatuses(), nil
	case r.Method == "GET" && route == "/core/navigation/apps":
		return NavigationApps(), nil
	case r.Method == "GET" && route == "/core/autocomplete/get":
		return Autocomplete(), nil
	case strings.HasPrefix(route, "/apps/provisioning_api/api/v1/config"):
		return EmptyObject(), nil
	}

	// Everything past here needs a principal.
	who, ok := l.authenticate(r)
	if !ok {
		return Val{}, Unauthorized("Unauthorised")
	}
	user := ncport.UserID(who.User)

	switch {
	case r.Method == "GET" && route == "/cloud/user":
		return l.currentUser(r.Context(), user)

	case r.Method == "GET" && strings.HasPrefix(route, "/cloud/users/"):
		return l.otherUser(r.Context(), user, strings.TrimPrefix(route, "/cloud/users/"))

	// --- unified search ---
	case r.Method == "GET" && route == "/search/providers":
		return SearchProviders(), nil
	case r.Method == "GET" && route == "/search/providers/files/search":
		return l.search(r.Context(), user,
			q.Get("term"), atoiOr(q.Get("limit"), 0), atoiOr(q.Get("cursor"), 0))

	// --- the recency query ---
	case r.Method == "GET" && route == "/apps/files/api/v1/recent":
		rq, err := ParseRecentQuery(q.Get("limit"), q.Get("since"), l.now())
		if err != nil {
			return Val{}, BadRequest("a malformed recency query")
		}
		return l.recent(r.Context(), user, rq)

	// --- favourites ---
	case r.Method == "GET" && route == "/apps/files/api/v1/favorites":
		return l.favoritesVal(r.Context(), user)

	// --- the direct URL a media player opens ---
	case r.Method == "POST" && route == "/apps/dav/api/v1/direct":
		return l.direct(r, user)

	// --- account lifecycle ---
	//
	// Both apps call this when the user removes the account. Without it the
	// credential the login flow issued stays valid for the life of the server:
	// the phone forgets it and the server does not.
	case r.Method == "DELETE" && route == "/core/apppassword":
		return l.revokeAppPassword(r, who)
	}

	// A refusal produced before any handler runs is logged, because this case
	// existed once and was invisible.
	l.warn("an unrouted OCS request", "path", r.URL.Path, "method", r.Method)
	return Val{}, NotFound("no such OCS endpoint")
}

// favoritesVal renders the starred list.
func (l *Layer) favoritesVal(ctx context.Context, user ncport.UserID) (Val, *OCSError) {
	if l.deps.State == nil {
		return VEmptyList(), nil
	}
	rows, err := l.deps.State.Favorites(ctx, user)
	if err != nil {
		return Val{}, ServerError("could not read the favourites")
	}
	out := make([]Val, 0, len(rows))
	for _, f := range rows {
		out = append(out, VMap(
			F("path", VStr("/"+strings.TrimPrefix(f.Path, "/"))),
			F("share", VInt(int64(f.Share))),
		))
	}
	return Val{Kind: KindList, List: out}, nil
}

// direct mints the URL an external media player opens.
func (l *Layer) direct(r *http.Request, user ncport.UserID) (Val, *OCSError) {
	if l.deps.Direct == nil {
		return Val{}, NotFound("File not found")
	}
	if err := r.ParseForm(); err != nil {
		return Val{}, BadRequest("a malformed request")
	}
	id, err := ParseFileID(r.PostFormValue("fileId"))
	if err != nil {
		// Not-found rather than a parse error, because a caller learning that
		// an id was well formed but invisible is the leak this avoids.
		return Val{}, NotFound("File not found")
	}
	return MintDirectURL(r.Context(), l.deps.Direct, user, id)
}

// revokeAppPassword ends the credential this request authenticated with.
func (l *Layer) revokeAppPassword(r *http.Request, who Principal) (Val, *OCSError) {
	if l.deps.Revoke == nil {
		return Val{}, ServerError("could not revoke this app password")
	}
	if who.CredentialID == 0 {
		// A session-authenticated request has no app password to revoke.
		// Letting it act on something else would be a sign-out that silently
		// did nothing.
		return Val{}, Forbidden(
			"this endpoint revokes app passwords; a browser session is signed out its own way")
	}
	if err := l.deps.Revoke(r.Context(), ncport.UserID(who.User), who.CredentialID); err != nil {
		return Val{}, ServerError("could not revoke this app password")
	}
	return VEmptyMap(), nil
}

// authenticate resolves the request's principal.
func (l *Layer) authenticate(r *http.Request) (Principal, bool) {
	if l.deps.Authenticate == nil {
		return Principal{}, false
	}
	return l.deps.Authenticate(r)
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// writeBareJSON sends a document that is not an OCS envelope.
//
// The status and the login flow both predate the envelope and clients parse
// them directly.
func writeBareJSON(w http.ResponseWriter, v Val) {
	body, err := v.writeJSON(nil)
	if err != nil {
		http.Error(w, "the response could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// As in the envelope writer: the bytes are this package's own encoder
	// output rather than anything a caller supplied.
	//nolint:errcheck,gosec // G705: see above; and the status is already sent.
	_, _ = w.Write(body)
}
