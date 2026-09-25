//go:build linux

// Account-owned SMB credential and access routes.
package smbaccount

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/route"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

// Deps supplies the account service used by SMB routes.
type Deps struct {
	Auth *auth.Service
}

// NewHandlers builds account-owned SMB routes.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"account.smb.create":          h.access,
		"account.smb.password.set":    h.passwordSet,
		"account.smb.password.delete": h.passwordDelete,
	}
}

type handlers struct{ d Deps }

type accessRequest struct {
	Current string `json:"current"`
	OptOut  bool   `json:"opt_out"`
	Enabled bool   `json:"enabled"`
}

type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

type reconfirmRequest struct {
	Current string `json:"current"`
}

func (h *handlers) access(c *gin.Context) {
	owner, ok := owner(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req accessRequest
	if !decode(c, &req) {
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	if err := h.d.Auth.SetSMBAccess(c.Request.Context(), owner, req.OptOut, req.Enabled); err != nil {
		fail(c, err)
		return
	}
	h.state(c, owner)
}

func (h *handlers) passwordSet(c *gin.Context) {
	owner, ok := owner(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req passwordRequest
	if !decode(c, &req) {
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	if err := h.d.Auth.SetSMBPassword(c.Request.Context(), owner, secret.New([]byte(req.New))); err != nil {
		fail(c, err)
		return
	}
	h.state(c, owner)
}

func (h *handlers) passwordDelete(c *gin.Context) {
	owner, ok := owner(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req reconfirmRequest
	if !decode(c, &req) {
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	revertible, err := h.d.Auth.ClearSMBPassword(c.Request.Context(), owner)
	if err != nil {
		fail(c, err)
		return
	}
	state, err := h.d.Auth.SMBStateOf(c.Request.Context(), owner)
	if err != nil {
		fail(c, err)
		return
	}
	json(c, http.StatusOK, handler.SMBClearedOf(state, revertible))
}

func (h *handlers) state(c *gin.Context, owner int64) {
	state, err := h.d.Auth.SMBStateOf(c.Request.Context(), owner)
	if err != nil {
		fail(c, err)
		return
	}
	json(c, http.StatusOK, handler.SMBStateOf(state))
}

func (h *handlers) reconfirm(c *gin.Context, owner int64, password string) bool {
	if password == "" {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	ok, err := h.d.Auth.VerifyAccountPassword(c.Request.Context(), owner, secret.New([]byte(password)))
	if err != nil {
		fail(c, err)
		return false
	}
	if !ok {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	return true
}

func owner(c *gin.Context) (int64, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	p, okp := v.(middleware.Principal)
	if !ok || !okp || p.UserID == 0 {
		return 0, false
	}
	return p.UserID, true
}

func decode(c *gin.Context, into any) bool {
	if err := middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return false
	}
	return true
}

func json(c *gin.Context, status int, value any) { c.JSON(status, value) }

func refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	json(c, status, body)
}

func fail(c *gin.Context, err error) {
	middleware.SetCause(c, err)
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
