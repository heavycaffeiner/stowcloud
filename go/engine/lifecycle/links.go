//go:build linux

// The links family: public share links a person makes over their own files.
//
// A link is a credential in a URL. It is minted once and never readable again,
// so the token appears in exactly one response and nowhere else: not in a
// listing, not in an update, not in a log.
package lifecycle

import (
	"encoding/json"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// linksList answers the caller's own links, narrowed to one path when the
// caller names one.
//
// The screen that manages an item's links asks for that item, and the filter
// was dropped on the way to the service: every item's dialogue then listed
// every link the account holds, so opening it on one folder showed the links
// of whatever had been shared before it.
//
// Read rather than Share on the resolution: listing the links over a path is
// reading about it, and an account that may see the path may see what it has
// published there. A path it cannot reach answers not-found from resolution
// rather than an empty list.
func (e *Engine) linksList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var at *core.Resolved
	if raw := c.Query("path"); raw != "" {
		r, rerr := e.resolve(owner, raw, acl.Read)
		if rerr != nil {
			return fail(c, rerr)
		}
		at = &r
	}

	links, err := e.Core.ListLinks(c.UserContext(), owner, at)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.LinksOf(links, e.now()))
}

// createLinkRequest is what a client sends to mint one.
type createLinkRequest struct {
	Path     string  `json:"path"`
	Password *string `json:"password,omitempty"`
	Label    string  `json:"label,omitempty"`
	Note     string  `json:"note,omitempty"`

	// Perms is what the visitor may do, by name. Omitted means reading and
	// downloading, which is what most links are for.
	//
	// It has to be settable, because a drop link is the opposite shape: create
	// and not read, so whoever holds it can put a file in and cannot see what
	// is already there. A fixed permission set cannot express one.
	Perms []string `json:"perms,omitempty"`

	// Raw, so an omitted cap is distinguishable from a cap of zero. The
	// service spells unlimited as -1 and treats 0 as a real limit meaning no
	// downloads at all, so a plain int32 field mints a link nobody can open
	// whenever the client leaves the cap out. It did exactly that.
	Expires json.RawMessage `json:"expires_ns,omitempty"`
	MaxDown json.RawMessage `json:"max_downloads,omitempty"`
}

// linkPerms reads the requested permission set.
//
// An empty request is reading and downloading: the ordinary link, and what
// every caller that does not care should get. A named set is narrowed to what
// a link may carry at all, because a visitor holding a URL is not the account
// that made it: rename, move and delete are the owner's, and sharing a link
// that can re-share is a way to hand out authority nobody granted.
func linkPerms(names []string) (acl.Perms, bool) {
	if len(names) == 0 {
		return acl.Read | acl.Download, true
	}
	requested, ok := permsOf(names)
	if !ok {
		return 0, false
	}
	allowed := requested & (acl.Read | acl.Download | acl.Create)
	if allowed == 0 {
		// A link that permits nothing is one nobody can open, which is a
		// mistake rather than a configuration.
		return 0, false
	}
	return allowed, true
}

// unlimitedDownloads is what the service reads as "no cap". Zero is a real
// limit, so an absent cap has to become this rather than the zero value.
const unlimitedDownloads = -1

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

	expires, ok := optionalNumber[int64](req.Expires, 0)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	maxDown, ok := optionalNumber[int32](req.MaxDown, unlimitedDownloads)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	perms, ok := linkPerms(req.Perms)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	link, token, err := e.Core.CreateLink(c.UserContext(), r, core.LinkSpec{
		Perms:    perms,
		Password: req.Password,
		Expires:  expires,
		MaxDown:  maxDown,
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

// updateLinkRequest changes a live link.
//
// Every field distinguishes three states, which a single pointer cannot:
// absent leaves the value alone, null clears it, and a value sets it. The
// difference matters most for the password, where "leave it" and "remove it"
// are opposite decisions about who can open the link.
type updateLinkRequest struct {
	Password json.RawMessage `json:"password,omitempty"`
	Expires  json.RawMessage `json:"expires_ns,omitempty"`
	MaxDown  json.RawMessage `json:"max_downloads,omitempty"`

	// Perms narrows or widens what the link grants, within what its owner
	// holds. Absent leaves it alone.
	Perms []string `json:"perms,omitempty"`

	Label *string `json:"label,omitempty"`
	Note  *string `json:"note,omitempty"`
}

// linksUpdate changes one of the caller's links.
//
// The owner is the caller and not a parameter: the service takes both, so a
// link belonging to somebody else is refused there rather than by a check
// here that a later edit could forget.
func (e *Engine) linksUpdate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	var req updateLinkRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	patch, ok := linkPatchOf(req)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	link, err := e.Core.UpdateLink(c.UserContext(), owner, id, patch)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.LinkOf(link, e.now()))
}

// linkPatchOf reads the three-state fields.
//
// The permission set is passed through as asked. Narrowing to what the link
// already carries happens in the service, which is where the link is read:
// deciding it here would need a second read and a window between the two.
func linkPatchOf(req updateLinkRequest) (core.LinkPatch, bool) {
	patch := core.LinkPatch{Label: req.Label, Note: req.Note}

	if len(req.Perms) > 0 {
		requested, ok := linkPerms(req.Perms)
		if !ok {
			return core.LinkPatch{}, false
		}
		patch.Perms = &requested
	}

	password, ok := tristate[string](req.Password)
	if !ok {
		return core.LinkPatch{}, false
	}
	patch.Password = password

	expires, ok := tristateNumber[int64](req.Expires)
	if !ok {
		return core.LinkPatch{}, false
	}
	patch.Expires = expires

	maxDown, ok := tristateNumber[int32](req.MaxDown)
	if !ok {
		return core.LinkPatch{}, false
	}
	patch.MaxDown = maxDown

	return patch, true
}

// tristate reads absent, null and set from one raw field.
//
// Returns nil for absent, a pointer to nil for null, and a pointer to a
// pointer to the value otherwise. The nesting is the service's own spelling
// for the same three states, so this converts rather than inventing a fourth.
func tristate[T any](raw json.RawMessage) (**T, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if string(raw) == "null" {
		var cleared *T
		return &cleared, true
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	set := &value
	return &set, true
}

// optionalNumber reads a field that may be absent, taking absent to mean the
// given default rather than the zero value.
//
// Both spellings are accepted for the same reason tristateNumber accepts
// them: this API sends 64-bit values as decimal strings, and a client that
// echoes what it read must not be refused.
func optionalNumber[T int64 | int32](raw json.RawMessage, absent T) (T, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return absent, true
	}
	got, ok := tristateNumber[T](raw)
	if !ok || got == nil || *got == nil {
		return absent, false
	}
	return **got, true
}

// tristateNumber is tristate for a value this API spells as a decimal string.
//
// Every 64-bit number leaves this server as a string, because a JavaScript
// number loses exactness past 2^53. Accepting only a JSON number on the way
// back in makes the two directions disagree: a client that reads a timestamp,
// changes a label and sends the object back gets 400 on the field it did not
// touch. Both spellings are accepted, and the string one is what the response
// uses.
func tristateNumber[T int64 | int32](raw json.RawMessage) (**T, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if string(raw) == "null" {
		var cleared *T
		return &cleared, true
	}

	// A quoted decimal first, then a bare number.
	var quoted string
	if err := json.Unmarshal(raw, &quoted); err == nil {
		n, perr := strconv.ParseInt(quoted, 10, 64)
		if perr != nil {
			return nil, false
		}
		narrowed, nerr := num.Narrow[T](n)
		if nerr != nil {
			return nil, false
		}
		set := &narrowed
		return &set, true
	}
	return tristate[T](raw)
}
