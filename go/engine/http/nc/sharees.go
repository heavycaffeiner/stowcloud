//go:build linux && compat_nc

package nc

import (
	"github.com/gofiber/fiber/v2"
)

// Sharee search: who a share dialog offers as a target.
//
// Nobody, on this deployment. Sharing with an account is an acl grant that
// the deployment's administration writes, not a gesture a client makes, so
// no share this surface can create names a person and no listing here
// reports one. A picker that offered accounts would only lead to a refusal
// at the end of it.
//
// The endpoint stays, and answers the whole shape: a client opens this
// before it draws the sharing panel, and a refusal there closes the panel
// that also fronts public links.

// sharees answers GET .../sharees.
//
// Every key both clients read (users, groups, remotes, remote_groups,
// emails, circles, rooms, lookup) is present at both the exact and the
// partial level: the Android client reads several of these arrays with no
// presence check, and a missing key there is a parse failure, not an empty
// result.
func (s *Server) sharees(_ *fiber.Ctx, _ Principal) (Val, bool, *Error) {
	empty := List()
	names := []string{"users", "groups", "remotes", "remote_groups", "emails", "circles", "rooms", "lookup"}

	level := func() Val {
		props := make([]Pair, 0, len(names))
		for _, name := range names {
			props = append(props, P(name, empty))
		}
		return Obj(props...)
	}

	out := make([]Pair, 0, len(names)+1)
	out = append(out, P("exact", level()))
	for _, name := range names {
		out = append(out, P(name, empty))
	}
	return Obj(out...), true, nil
}
