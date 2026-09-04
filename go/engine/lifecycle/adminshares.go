//go:build linux

// Administration: shares and the grants over them.
//
// Nothing here reports a host path. A share's on-disk location is server
// configuration, and a client that learns it learns the layout of the machine.
// The projection has no field for one, so a future edit has to add it
// deliberately rather than by widening a struct.
package lifecycle

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// adminSharesList answers every registered share.
//
// Including the broken ones. A share whose disk never came back is still
// registered, and dropping it from this listing is what once made an
// unreachable share indistinguishable from a deleted one.
func (e *Engine) adminSharesList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	return writeJSON(c, fiber.StatusOK, handler.SharesOf(e.Core.Shares()))
}

// createShareRequest registers a directory.
type createShareRequest struct {
	Name string `json:"name"`

	// Host is where it lives on the server's disk. It arrives from an
	// administrator and never goes back out.
	Host string `json:"host"`
}

// adminSharesCreate registers one.
func (e *Engine) adminSharesCreate(c *fiber.Ctx) error {
	admin, ok, written := e.admin(c)
	if !ok {
		return written
	}

	var req createShareRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	share, err := e.Core.CreateShare(c.UserContext(), core.ShareSpec{
		Name: req.Name,
		Host: req.Host,
	})
	if err != nil {
		return fail(c, err)
	}
	// The watcher learns about it now rather than at the next restart.
	// Without this a share registered while the server is running is one no
	// change is ever reported under, and the symptom is a folder that updates
	// for everybody except the person who just created it.
	e.watchShare(share)

	// The administrator who registered it can reach it. Access is granted
	// separately from registration by design, and everybody else still needs
	// a grant, but a folder that is invisible to the person who just added it
	// reads as the registration having failed. Setup does the same for the
	// shares that exist when it runs.
	if gerr := e.grantShareTo(c, admin, share); gerr != nil {
		e.logger.Warn("the new share was registered without a grant for its creator",
			"share", int64(share.ID), "error", gerr)
	}
	return writeJSON(c, fiber.StatusCreated, handler.ShareOf(share))
}

// grantShareTo gives one account full access to one share.
//
// The same permission set setup writes, and the share's own name as the
// label, so the two paths produce grants a reader cannot tell apart.
func (e *Engine) grantShareTo(c *fiber.Ctx, user int64, share core.ShareDef) error {
	_, err := e.Core.CreateGrant(c.UserContext(), core.GrantSpec{
		User:    &user,
		Share:   share.ID,
		Allow:   acl.Read | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move | acl.Share | acl.Download,
		Inherit: true,
		Label:   share.Name,
	})
	return err
}

// updateShareRequest carries only what changes. Pointers separate an absent
// field from a cleared one, which is the difference between leaving the trash
// alone and turning it off.
type updateShareRequest struct {
	Name         *string `json:"name"`
	Host         *string `json:"host"`
	TrashEnabled *bool   `json:"trash_enabled"`
}

// adminSharesUpdate changes one.
func (e *Engine) adminSharesUpdate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := shareIDOf(c)
	if !ok {
		return notFound(c)
	}

	var req updateShareRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	share, err := e.Core.UpdateShare(c.UserContext(), id, core.SharePatch{
		Name:         req.Name,
		Host:         req.Host,
		TrashEnabled: req.TrashEnabled,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.ShareOf(share))
}

// adminSharesRetry re-opens a share whose backing was unavailable.
//
// A separate route rather than something the listing does on its own: opening
// a dead mount can block, and a listing that retried every broken share would
// take as long as the slowest one every time an administrator looked at the
// screen.
func (e *Engine) adminSharesRetry(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := shareIDOf(c)
	if !ok {
		return notFound(c)
	}

	share, err := e.Core.RetryShare(c.UserContext(), id)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.ShareOf(share))
}

// adminSharesDelete unregisters one.
//
// The stored files are not touched. Unregistering is an administrative act
// about what this deployment serves; deleting the data would make a mistyped
// id destroy a directory nobody meant to name.
func (e *Engine) adminSharesDelete(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := shareIDOf(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Core.DeleteShare(c.UserContext(), id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// shareIDOf reads the path's share id.
//
// A share id is narrower than the decimal a path can carry, so the value is
// narrowed rather than converted: a converted id past the width wraps onto a
// different share, which turns a mistyped number into a delete of something
// nobody named.
func shareIDOf(c *fiber.Ctx) (core.ShareID, bool) {
	raw, ok := pathID(c)
	if !ok {
		return 0, false
	}
	narrowed, err := num.Narrow[uint32](raw)
	if err != nil {
		return 0, false
	}
	return core.ShareID(narrowed), true
}

// adminGrantsList answers the grants, optionally for one subject or share.
func (e *Engine) adminGrantsList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	rows, err := e.Core.ListGrants(c.UserContext(), core.GrantFilter{
		User:  queryInt(c.Query("user")),
		Group: queryInt(c.Query("group")),
		Share: queryInt(c.Query("share")),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.GrantsOf(rows))
}

// grantRequest is one permission assignment.
type grantRequest struct {
	// Exactly one of these names the subject. A grant to both would be two
	// grants, and a grant to neither would apply to nobody.
	User  string `json:"user"`
	Group string `json:"group"`

	Share   string `json:"share"`
	Subpath string `json:"subpath"`

	// Allow and Deny are permission names. Unknown ones are refused rather
	// than dropped: storing a grant weaker than the one the screen showed is
	// how an administrator believes they gave access that nobody has.
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`

	Inherit bool   `json:"inherit"`
	Label   string `json:"label"`
}

// adminGrantsCreate adds one.
func (e *Engine) adminGrantsCreate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	var req grantRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	spec, ok := grantSpecOf(req)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	// An unlabelled grant over a whole share takes the share's own name. The
	// screen draws the label, and without one the listing falls back to a
	// generated placeholder naming the share's id rather than the folder
	// somebody picked.
	if spec.Label == "" && spec.Subpath == "" {
		if def, found := e.Core.Share(spec.Share); found {
			spec.Label = def.Name
		}
	}

	grant, err := e.Core.CreateGrant(c.UserContext(), spec)
	if err != nil {
		return fail(c, err)
	}
	// The whole grant, because the screen appends it to the list it is already
	// showing. An id alone left every rendered row reading permission arrays
	// that were not there.
	return writeJSON(c, fiber.StatusCreated, handler.GrantOf(grant))
}

// grantSpecOf validates a request into a spec.
//
// The false return is one refusal for every way the request cannot be
// honoured, because each of them means the same thing to the caller: this
// grant was not stored. What must not happen is storing a different grant
// from the one described.
func grantSpecOf(req grantRequest) (core.GrantSpec, bool) {
	share, err := strconv.ParseUint(req.Share, 10, 32)
	if err != nil || share == 0 {
		return core.GrantSpec{}, false
	}

	spec := core.GrantSpec{
		Share:   core.ShareID(share),
		Subpath: req.Subpath,
		Inherit: req.Inherit,
		Label:   req.Label,
	}

	// Exactly one subject. Both would be ambiguous about who it applies to,
	// and neither would be a grant nobody holds.
	//
	// The store refuses both cases too, so this is where the refusal happens
	// rather than the only place it could. Measured: removing the "neither"
	// branch changes no answer, because the store rejects a subjectless grant;
	// removing the "both" branch does change one, since this is what decides
	// which subject wins when a request names two.
	switch {
	case req.User != "" && req.Group != "":
		return core.GrantSpec{}, false
	case req.User != "":
		id, perr := strconv.ParseInt(req.User, 10, 64)
		if perr != nil || id <= 0 {
			return core.GrantSpec{}, false
		}
		spec.User = &id
	case req.Group != "":
		id, perr := strconv.ParseInt(req.Group, 10, 64)
		if perr != nil || id <= 0 {
			return core.GrantSpec{}, false
		}
		spec.Group = &id
	default:
		return core.GrantSpec{}, false
	}

	allow, ok := permsOf(req.Allow)
	if !ok {
		return core.GrantSpec{}, false
	}
	deny, ok := permsOf(req.Deny)
	if !ok {
		return core.GrantSpec{}, false
	}
	spec.Allow, spec.Deny = allow, deny
	return spec, true
}

// permsOf turns permission names into a set.
//
// One unknown name refuses the whole list. Skipping it would store a grant
// that differs from the one requested, and the difference is silent: the
// administrator sees the name they typed and the system holds a set without
// it.
func permsOf(names []string) (acl.Perms, bool) {
	var out acl.Perms
	for _, name := range names {
		bit, known := acl.PermByName(name)
		if !known {
			return 0, false
		}
		out |= bit
	}
	return out, true
}

// updateGrantRequest replaces a grant's permissions.
type updateGrantRequest struct {
	Allow   []string `json:"allow"`
	Deny    []string `json:"deny"`
	Inherit bool     `json:"inherit"`
	Label   string   `json:"label"`
}

// adminGrantsUpdate changes one.
func (e *Engine) adminGrantsUpdate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	var req updateGrantRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	allow, ok := permsOf(req.Allow)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	deny, ok := permsOf(req.Deny)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	grant, err := e.Core.UpdateGrant(c.UserContext(), id, allow, deny, req.Inherit, req.Label)
	if err != nil {
		return fail(c, err)
	}
	// The whole grant, as the create route answers: the screen swaps the
	// edited row for what came back, and every rendered row reads the
	// permission arrays. Answering no content left it with nothing to swap
	// in, and the change applied while the dialogue said it had not.
	return writeJSON(c, fiber.StatusOK, handler.GrantOf(grant))
}

// adminGrantsDelete revokes one.
func (e *Engine) adminGrantsDelete(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Core.DeleteGrant(c.UserContext(), id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
