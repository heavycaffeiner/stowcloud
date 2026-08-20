//go:build compat_nc

package nc

import (
	"net/http"
	"strings"
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
	MountDav       = "/remote.php/dav/"
	MountWebDav    = "/remote.php/webdav/"
	MountOCSv1     = "/ocs/v1.php/"
	MountOCSv2     = "/ocs/v2.php/"
	MountIndex     = "/index.php/"
	MountStatus    = "/status.php"
	MountDavUpload = "/remote.php/dav/uploads/"
)

// Mounts is every route the layer owns.
//
// The layer describes its routes rather than registering them: the route table
// lives in the server, and a layer that registered its own would be a second
// place routes come from.
func (l *Layer) Mounts() []Mount {
	return []Mount{
		// The status document, which is what a client fetches before it knows
		// anything else about this server.
		{Method: "GET", Pattern: MountStatus, Handler: http.HandlerFunc(l.status)},

		// The OCS surfaces. Both versions are mounted because a client may
		// call either, and the version decides the envelope.
		{Method: "GET", Pattern: MountOCSv1, Handler: l.ocsHandler(OCSv1)},
		{Method: "POST", Pattern: MountOCSv1, Handler: l.ocsHandler(OCSv1)},
		{Method: "GET", Pattern: MountOCSv2, Handler: l.ocsHandler(OCSv2)},
		{Method: "POST", Pattern: MountOCSv2, Handler: l.ocsHandler(OCSv2)},
	}
}

// status answers the pre-login probe.
//
// A client reads this before it has credentials, so it carries no information
// about anything a stranger should not see: a version, an installed flag, and
// the edition. It is deliberately not the capabilities document, which needs
// an authenticated principal.
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

	// A bare JSON document rather than an OCS envelope: this endpoint predates
	// the envelope and clients parse it directly.
	body, err := VMap(doc.Map...).writeJSON(nil)
	if err != nil {
		http.Error(w, "the status document could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:errcheck // the status is already sent.
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

		switch {
		case route == "/cloud/capabilities":
			OK(version, format, Capabilities(l.caps)).Write(w)

		case route == "/apps/notifications/api/v2/notifications":
			OK(version, format, Notifications()).Write(w)
		case route == "/apps/user_status/api/v1/statuses":
			OK(version, format, UserStatuses()).Write(w)
		case route == "/core/navigation/apps":
			OK(version, format, NavigationApps()).Write(w)
		case route == "/core/autocomplete/get":
			OK(version, format, Autocomplete()).Write(w)
		case strings.HasPrefix(route, "/apps/provisioning_api/api/v1/config"):
			OK(version, format, EmptyObject()).Write(w)

		default:
			// A refusal produced before any handler runs is logged, because
			// this case existed once and was invisible.
			l.warn("an unrouted OCS request", "path", r.URL.Path)
			Fail(version, format, NotFound("no such OCS endpoint")).Write(w)
		}
	})
}
