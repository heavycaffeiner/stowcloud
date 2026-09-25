//go:build linux

// Administrator-owned SMB sidecar apply route.
package adminsmb

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/smb/agent"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
)

// Deps supplies administrator authorization and the application-owned publisher.
type Deps struct {
	Auth    *auth.Service
	Apply   func(context.Context) (agent.Report, bool, error)
	Timeout time.Duration
	Logger  *slog.Logger
}
type handlers struct{ d Deps }

// NewHandlers builds administrator SMB routes.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Timeout <= 0 {
		d.Timeout = agent.DefaultTimeout + 5*time.Second
	}
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{"admin.smb.apply": h.apply}
}

func (h *handlers) apply(c *gin.Context) {
	if !h.admin(c) {
		return
	}
	if h.d.Apply == nil {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "smb.not_configured"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), h.d.Timeout)
	defer cancel()
	report, configured, err := h.d.Apply(ctx)
	if !configured {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "smb.not_configured"})
		return
	}
	if err != nil {
		h.d.Logger.Warn("the SMB agent did not answer an apply", "error", err)
		refuse(c, apierr.Classified{Class: apierr.BadGateway, Key: "smb.agent_unreachable"})
		return
	}
	json(c, http.StatusOK, handler.SMBReportOf(report))
}

func (h *handlers) admin(c *gin.Context) bool {
	v, ok := c.Get(string(middleware.KeyCredential))
	p, okp := v.(middleware.Principal)
	if !ok || !okp || p.UserID == 0 {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return false
	}
	is, err := h.d.Auth.IsAdmin(c.Request.Context(), p.UserID)
	if err != nil {
		fail(c, err)
		return false
	}
	if !is {
		refuse(c, apierr.Classified{Class: apierr.Denied})
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
