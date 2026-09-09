//go:build linux && compat_nc

package nc

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// Sharee search: who a share dialog offers as a target.
//
// The directory rule is the account service's own, not reinvented here:
// auth.VisibleAccounts already answers "who may this caller look up", and a
// name outside that answer must not become reachable just because it was
// searched for rather than addressed directly. Disabled accounts are left
// out on top of that, since a share with one grants nothing anybody could
// ever use.

// sharees answers GET .../sharees.
//
// Every key both clients read (users, groups, remotes, remote_groups,
// emails, circles, rooms, lookup) is present at both the exact and the
// partial level even when empty: the Android client reads several of these
// arrays with no presence check, and a missing key there is a parse
// failure, not an empty result.
func (s *Server) sharees(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	term := strings.TrimSpace(c.Query("search"))
	page, perPage := shareeQueryInt(c.Query("page"), 1), shareeQueryInt(c.Query("perPage"), 200)

	var exactUsers, users []Val
	var exactGroups, groups []Val

	if term != "" {
		accounts, err := s.deps.Auth.VisibleAccounts(ctx, int64(user(p)))
		if err == nil {
			for _, a := range accounts {
				if a.Disabled {
					continue
				}
				if hit, exact := shareeMatch(term, a.Name, a.Display); hit {
					v := shareeVal(displayOf(a), ShareTypeUser, a.Name)
					if exact {
						exactUsers = append(exactUsers, v)
					} else {
						users = append(users, v)
					}
				}
			}
		}

		groupRows, err := s.deps.Auth.ListGroups(ctx)
		if err == nil {
			for _, g := range groupRows {
				if hit, exact := shareeMatch(term, g.Name, g.Name); hit {
					v := shareeVal(g.Name, ShareTypeGroup, g.Name)
					if exact {
						exactGroups = append(exactGroups, v)
					} else {
						groups = append(groups, v)
					}
				}
			}
		}
	}

	users = pageSlice(users, page, perPage)
	groups = pageSlice(groups, page, perPage)

	empty := List()
	exact := Obj(
		P("users", List(exactUsers...)),
		P("groups", List(exactGroups...)),
		P("remotes", empty),
		P("remote_groups", empty),
		P("emails", empty),
		P("circles", empty),
		P("rooms", empty),
		P("lookup", empty),
	)
	return Obj(
		P("exact", exact),
		P("users", List(users...)),
		P("groups", List(groups...)),
		P("remotes", empty),
		P("remote_groups", empty),
		P("emails", empty),
		P("circles", empty),
		P("rooms", empty),
		P("lookup", empty),
	), true, nil
}

// shareeMatch reports whether a candidate (its login name and its display
// name) matches the search term as a substring, and separately whether it
// matches exactly. Case-insensitive both ways: a person searching for a
// name does not know or care how it was capitalised when the account was
// made.
func shareeMatch(term, login, display string) (hit, exact bool) {
	lowerTerm := strings.ToLower(term)
	lowerLogin, lowerDisplay := strings.ToLower(login), strings.ToLower(display)
	if lowerLogin == lowerTerm || lowerDisplay == lowerTerm {
		return true, true
	}
	if strings.Contains(lowerLogin, lowerTerm) || strings.Contains(lowerDisplay, lowerTerm) {
		return true, false
	}
	return false, false
}

// displayOf picks the label a share dialog shows: the operator-assigned
// display name when there is one, the login name otherwise.
func displayOf(a auth.UserRow) string {
	if a.Display != "" {
		return a.Display
	}
	return a.Name
}

// shareeVal renders one sharee entry, the shape both clients read.
func shareeVal(label string, shareType int, shareWith string) Val {
	return Obj(
		P("label", Str(label)),
		P("value", Obj(
			P("shareType", Int(int64(shareType))),
			P("shareWith", Str(shareWith)),
		)),
	)
}

// pageSlice cuts a one-indexed page out of a result set. A page or
// perPage outside the slice answers empty rather than panicking: a stale
// pagination cursor from a client is not this handler's error to raise.
func pageSlice(items []Val, page, perPage int) []Val {
	if len(items) == 0 {
		return items
	}
	start := (page - 1) * perPage
	if start < 0 || start >= len(items) {
		return nil
	}
	end := min(start+perPage, len(items))
	return items[start:end]
}

// shareeQueryInt reads a positive query integer, or the fallback when it
// is absent or not positive.
func shareeQueryInt(raw string, fallback int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
