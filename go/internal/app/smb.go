//go:build linux

// The file-sharing protocol's credential, which an account manages itself.
//
// It is a separate password on purpose. The protocol's authentication cannot
// be strengthened to match the web one, so the account password is kept away
// from it: an account that opts in has a credential that works there and
// nowhere else, and one that opts out has none at all.
package app

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
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
func (e *Engine) accountSMBCreate(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req smbAccessRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	if err := e.Auth.SetSMBAccess(c.Request.Context(), int64(owner), req.OptOut, req.Enabled); err != nil {
		failKnown(c, err)
		return
	}
	e.answerSMBState(c, int64(owner))
}

// smbPasswordRequest sets the protocol credential.
type smbPasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// accountSMBPasswordSet stores the protocol credential.
func (e *Engine) accountSMBPasswordSet(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req smbPasswordRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	if err := e.Auth.SetSMBPassword(c.Request.Context(), int64(owner),
		secret.New([]byte(req.New))); err != nil {
		failKnown(c, err)
		return
	}
	e.answerSMBState(c, int64(owner))
}

// accountSMBPasswordDelete removes the protocol credential.
//
// The answer says whether the account keeps protocol access afterwards.
// Clearing is sometimes losing it entirely, and reporting a bare success
// there reads as "nothing changed" to somebody who has just lost a mount.
func (e *Engine) accountSMBPasswordDelete(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	revertible, err := e.Auth.ClearSMBPassword(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}

	state, err := e.Auth.SMBStateOf(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.SMBClearedOf(state, revertible))
}

// answerSMBState reports what the account can do over the protocol now.
//
// Returned rather than a bare 204, because every route here changes the
// answer and a screen that had to ask again could show the previous state as
// though the change had not happened.
func (e *Engine) answerSMBState(c *gin.Context, owner int64) {
	state, err := e.Auth.SMBStateOf(c.Request.Context(), owner)
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.SMBStateOf(state))
}
