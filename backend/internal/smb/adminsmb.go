//go:build linux

// Administrator-owned SMB sidecar apply route.
package smb

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb/agent"
)

// AdminDeps supplies the application-owned publisher.
type AdminDeps struct {
	Apply   func(context.Context) (agent.Report, bool, error)
	Timeout time.Duration
	Logger  *slog.Logger
}
type AdminHandlers struct{ d AdminDeps }

// NewAdminHandlers builds administrator SMB routes.
func NewAdminHandlers(d AdminDeps) *AdminHandlers {
	if d.Timeout <= 0 {
		d.Timeout = agent.DefaultTimeout + 5*time.Second
	}
	return &AdminHandlers{d: d}
}

func (h *AdminHandlers) Apply(c *gin.Context) {
	if h.d.Apply == nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "smb.not_configured"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), h.d.Timeout)
	defer cancel()
	report, configured, err := h.d.Apply(ctx)
	if !configured {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "smb.not_configured"})
		return
	}
	if err != nil {
		h.d.Logger.Warn("the SMB agent did not answer an apply", "error", err)
		middleware.Refuse(c, apierr.Classified{Class: apierr.BadGateway, Key: "smb.agent_unreachable"})
		return
	}
	c.JSON(http.StatusOK, SMBReportOf(report))
}
