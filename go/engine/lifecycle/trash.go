//go:build linux

// The trash family: what a person deleted and can still get back.
//
// A restore puts an entry where it was deleted from, which the entry records.
// The request never says where: a caller choosing the destination could move a
// file anywhere it can write, using a delete as the first half of the move.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// trashList answers one share's trash.
func (e *Engine) trashList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Read)
	if err != nil {
		return fail(c, err)
	}

	entries, err := e.Core.TrashList(c.UserContext(), r)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.TrashListOf(entries))
}

// trashRequest names a share and one entry in its trash.
type trashRequest struct {
	Path string `json:"path"`
	ID   string `json:"id"`
}

// trashRestore puts one entry back where it came from.
//
// Create is the permission, because a restore adds a file to the tree. Delete
// is not enough: an account that may remove things is not thereby allowed to
// put them back somewhere.
func (e *Engine) trashRestore(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req trashRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if req.ID == "" {
		return notFound(c)
	}

	r, err := e.resolve(owner, req.Path, acl.Create)
	if err != nil {
		return fail(c, err)
	}

	if _, err := e.Core.TrashRestore(c.UserContext(), r, req.ID); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// trashPurge removes one entry permanently, or empties the trash.
//
// An absent id empties. That is the one place a request may ask for a
// permanent removal, and it is deliberate: the trash is where things already
// deleted go, so emptying it is the act a person takes to finish the job.
func (e *Engine) trashPurge(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req trashRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Delete)
	if err != nil {
		return fail(c, err)
	}

	var id *string
	if req.ID != "" {
		id = &req.ID
	}
	if err := e.Core.TrashPurge(c.UserContext(), r, id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
