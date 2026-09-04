//go:build linux

// The files family's write side.
//
// Each route resolves with the permission its operation needs, not with Read.
// The core checks again before it writes, so a mismatch here surfaces as a
// refusal rather than as an unchecked write; the point of naming it correctly
// is that the refusal happens before anything is attempted and says the right
// thing.
package lifecycle

import (
	"bytes"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// pathRequest is the body every single-path write takes.
type pathRequest struct {
	Path string `json:"path"`
}

// filesMkdir creates one directory.
func (e *Engine) filesMkdir(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req pathRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Create)
	if err != nil {
		return fail(c, err)
	}

	entry, err := e.Core.Mkdir(c.UserContext(), r)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusCreated, e.entryView(owner, r, entry))
}

// filesDelete removes one entry.
//
// To the trash where the share has one, permanently where it does not. The
// choice is the share's rather than the request's: a caller that could ask for
// a permanent delete could bypass a deployment's own retention.
func (e *Engine) filesDelete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req pathRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Delete)
	if err != nil {
		return fail(c, err)
	}

	if lerr := e.guardDavLock(c.UserContext(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
		return refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
	}
	if err := e.Core.Delete(c.UserContext(), r, false); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// renameRequest is what a rename takes.
type renameRequest struct {
	Path string `json:"path"`
	// new_name is the wire name the client sends; the field renders the new
	// leaf, and a "name" key is what an earlier contract used before the
	// client changed to disambiguate it from the entry's own display name.
	Name string `json:"new_name"`
}

// filesRename changes one entry's name in place.
//
// Rename rather than Move: the two are separate permissions because a grant
// scoped to one subtree should not let its holder carry a file out of it, and
// this route only ever changes the last component.
func (e *Engine) filesRename(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req renameRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Rename)
	if err != nil {
		return fail(c, err)
	}

	if lerr := e.guardDavLock(c.UserContext(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
		return refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
	}
	entry, err := e.Core.Rename(c.UserContext(), r, req.Name, nil)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, e.entryView(owner, r, entry))
}

// decodeBody reads a JSON request body.
//
// Bounded twice, for two different failures. The chain already refused a body
// whose declared length is past its route's class, which stops a client that
// announced an oversized body before the server accepted it. This bounds the
// bytes actually read, which is what stops one that announced a small body and
// then sent more.
//
// Refusing a malformed body changes no outcome today: measured, acting on the
// zero value leaves an empty path, which no resolve accepts, so the request
// fails either way. It is refused here so the client is told its body was
// wrong rather than that its path was not found, and so a field added later
// with a usable zero value does not quietly become a default.
func decodeBody(c *fiber.Ctx, into any) error {
	return middleware.DecodeJSON(
		middleware.LimitBody(bytes.NewReader(c.Body()), route.BodyJSON),
		into,
	)
}
