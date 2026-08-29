//go:build linux

// The file-sharing protocol's credential, which an account manages itself.
//
// It is a separate password on purpose. The protocol's authentication cannot
// be strengthened to match the web one, so the account password is kept away
// from it: an account that opts in has a credential that works there and
// nowhere else, and one that opts out has none at all.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// smbAccessRequest is the caller's own two switches.
type smbAccessRequest struct {
	// Current is the account password. Every route here changes how the
	// account authenticates somewhere, which is not something a session alone
	// should decide.
	Current string `json:"current"`

	// OptOut is the stronger statement and forces Enabled off: a credential
	// that is not stored cannot be live.
	OptOut  bool `json:"opt_out"`
	Enabled bool `json:"enabled"`
}

// accountSMBCreate writes the caller's access switches.
func (e *Engine) accountSMBCreate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req smbAccessRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	if err := e.Auth.SetSMBAccess(c.UserContext(), int64(owner), req.OptOut, req.Enabled); err != nil {
		return failKnown(c, err)
	}
	return e.answerSMBState(c, int64(owner))
}

// smbPasswordRequest sets the protocol credential.
type smbPasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// accountSMBPasswordSet stores the protocol credential.
func (e *Engine) accountSMBPasswordSet(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req smbPasswordRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	if err := e.Auth.SetSMBPassword(c.UserContext(), int64(owner),
		secret.New([]byte(req.New))); err != nil {
		return failKnown(c, err)
	}
	return e.answerSMBState(c, int64(owner))
}

// accountSMBPasswordDelete removes the protocol credential.
//
// The answer says whether the account keeps protocol access afterwards.
// Clearing is sometimes losing it entirely, and reporting a bare success
// there reads as "nothing changed" to somebody who has just lost a mount.
func (e *Engine) accountSMBPasswordDelete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	revertible, err := e.Auth.ClearSMBPassword(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}

	state, err := e.Auth.SMBStateOf(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.SMBClearedOf(state, revertible))
}

// answerSMBState reports what the account can do over the protocol now.
//
// Returned rather than a bare 204, because every route here changes the
// answer and a screen that had to ask again could show the previous state as
// though the change had not happened.
func (e *Engine) answerSMBState(c *fiber.Ctx, owner int64) error {
	state, err := e.Auth.SMBStateOf(c.UserContext(), owner)
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.SMBStateOf(state))
}
