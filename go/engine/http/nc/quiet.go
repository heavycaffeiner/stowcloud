//go:build linux && compat_nc

package nc

import "strings"

// The endpoints that exist so a client stops asking.
//
// Each one is a screen a client opens on its own initiative, checking a
// capability key or just trying: notifications, presence, activity, the
// dashboard. None of them has a backing feature in this engine. A 404 there
// is read by a client as "this account is broken" and shown to a person; an
// empty, well-shaped answer is read as "nothing to show" and is not. So every
// route below answers the shape the reference document says the caller
// parses, holding nothing.
//
// Never a place for a route with a real handler: share, file, search, user
// and capability routes are matched earlier in routeOCS and never reach here.

// quietRoute answers one of the endpoints in this table, or reports that it
// does not recognise the route at all.
func (s *Server) quietRoute(method, route string) (Val, bool) {
	switch {
	case route == "/apps/notifications/api/v2/notifications":
		return List(), true
	case route == "/apps/notifications/api/v2/push":
		return Obj(), true

	case route == "/apps/user_status/api/v1/user_status":
		// A single, permanently online status: this deployment tracks no
		// presence, so there is nothing else true to report.
		return Obj(
			P("status", Str("online")),
			P("message", Str("")),
		), true
	case route == "/apps/user_status/api/v1/predefined_statuses":
		return List(), true
	case route == "/apps/user_status/api/v1/statuses":
		return List(), true

	case route == "/apps/activity/api/v2/activity":
		return List(), true
	case route == "/apps/activity/api/v2/activity/filter":
		return List(), true

	case route == "/core/navigation/apps":
		return List(), true
	case route == "/core/autocomplete/get":
		return List(), true

	case route == "/apps/files/api/v1/templates":
		return List(), true
	case route == "/apps/files/api/v1/directEditing":
		return Obj(), true
	case strings.HasPrefix(route, "/apps/files/api/v1/directEditing/templates/"):
		return Obj(), true

	case route == "/apps/provisioning_api/api/v1/config":
		return Obj(), true

	case route == "/apps/dashboard/api/v1/widgets":
		return Obj(), true

	case route == "/apps/recommendations/api/v1/recommendations":
		return List(), true

	case strings.HasPrefix(route, "/hovercard/v1/"):
		return Obj(), true

	case route == "/apps/external/api/v1":
		return List(), true

	case route == "/apps/terms_of_service/terms":
		// hasSigned true is what the desktop client reads as "nothing to
		// sign": no terms app is installed here, so there is never anything
		// pending.
		return Obj(P("hasSigned", Bool(true))), true

	case route == "/taskprocessing/tasktypes":
		return Obj(), true
	case route == "/textprocessing/tasktypes":
		return Obj(), true
	}
	return Val{}, false
}
