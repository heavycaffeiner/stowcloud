//go:build linux

// The links family: public share links a person makes over their own files.
//
// A link is a credential in a URL. It is minted once and never readable again,
// so the token appears in exactly one response and nowhere else: not in a
// listing, not in an update, not in a log.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// linksList answers the caller's own links.
func (e *Engine) linksList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	links, err := e.Core.ListLinks(c.UserContext(), owner, nil)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.LinksOf(links, e.now()))
}

// createLinkRequest is what a client sends to mint one.
type createLinkRequest struct {
	Path     string  `json:"path"`
	Password *string `json:"password,omitempty"`
	Expires  int64   `json:"expires_ns,omitempty"`
	MaxDown  int32   `json:"max_downloads,omitempty"`
	Label    string  `json:"label,omitempty"`
	Note     string  `json:"note,omitempty"`
}

// linksCreate mints a link over one of the caller's paths.
//
// Share is the permission, not Read: making a file reachable by anyone holding
// a URL is a different act from reading it, and an account that may read a
// share is not thereby allowed to publish it.
//
// Neither this argument nor the link's own permission set is the gate.
// Measured: CreateLink requires Share on the resolution and refuses a link
// asking for more than the caller holds, so resolving with Read or asking for
// Write both still refuse. They are named correctly so a reader finds the
// answer at the route, and so the refusal comes before the service is called.
func (e *Engine) linksCreate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req createLinkRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Share)
	if err != nil {
		return fail(c, err)
	}

	link, token, err := e.Core.CreateLink(c.UserContext(), r, core.LinkSpec{
		// The link grants reading and downloading and nothing else. A visitor
		// holding a URL is not the account that made it.
		Perms:    acl.Read | acl.Download,
		Password: req.Password,
		Expires:  req.Expires,
		MaxDown:  req.MaxDown,
		Label:    req.Label,
		Note:     req.Note,
	})
	if err != nil {
		return fail(c, err)
	}

	view, ok := handler.MintedLinkOf(link, e.now())
	if !ok {
		// The projection refused to render a minted link. Answering with the
		// token anyway would put it on the wire outside the shape a client
		// knows how to read.
		return fail(c, core.ErrNotFound)
	}
	// Reveal, not String: the secret type redacts under every formatting
	// verb precisely so it cannot leak into a log by accident. This is the one
	// response that is allowed to carry it, so it is asked for explicitly.
	view.Token = string(token.Reveal())

	return writeJSON(c, fiber.StatusCreated, view)
}

// linksDelete revokes one of the caller's links.
//
// The owner is passed to the service, so a link belonging to someone else is
// refused there rather than by a check here that could be forgotten.
func (e *Engine) linksDelete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Core.DeleteLink(c.UserContext(), owner, id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
