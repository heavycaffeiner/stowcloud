//go:build linux

// Package adminstorage serves administrator storage accounting.
package adminstorage

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

type Deps struct {
	Core  *core.Core
	Auth  *auth.Service
	State *state.DB
}

func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	return map[string]gin.HandlerFunc{"admin.storage": (&handlers{d: d}).get}
}

type handlers struct{ d Deps }

func (h *handlers) get(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	dbBytes, err := h.d.State.FileBytes()
	if err != nil {
		h.fail(c, err)
		return
	}
	shares := make([]handler.ShareUsage, 0, len(h.d.Core.Shares()))
	for _, sh := range h.d.Core.Shares() {
		usage := handler.ShareUsage{ID: sh.ID, Label: sh.Name}
		if root, ok := h.d.Core.ShareRoot(sh.ID); ok {
			if space, serr := root.Space(vfs.RootPath()); serr == nil {
				usage.Total, usage.Free = space.Total, space.Available
				usage.Measured = true
			}
		}
		shares = append(shares, usage)
	}
	c.JSON(http.StatusOK, handler.StorageOf(dbBytes, shares))
}

func (h *handlers) admin(c *gin.Context) (int64, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	p, okp := v.(middleware.Principal)
	if !ok || !okp || p.UserID == 0 {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	isAdmin, err := h.d.Auth.IsAdmin(c.Request.Context(), p.UserID)
	if err != nil {
		h.fail(c, err)
		return 0, false
	}
	if !isAdmin {
		h.refuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return p.UserID, true
}

func (h *handlers) refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	c.JSON(status, body)
}
func (h *handlers) fail(c *gin.Context, err error) {
	if errors.Is(err, core.ErrNotFound) {
		h.refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	middleware.SetCause(c, err)
	h.refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
