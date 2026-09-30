//go:build linux

// Administrator-owned SMB sidecar apply route.
package smb

import (
	"context"
	"log/slog"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
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

type reportOutput struct{ Body SMBReportView }

// Apply publishes the current shares to the SMB agent and answers its report.
// The agent call outlives a dropped request so a half-applied state is not left.
func (h *AdminHandlers) Apply(ctx context.Context, _ *struct{}) (*reportOutput, error) {
	if h.d.Apply == nil {
		return nil, apierr.AsClassified(apierr.SubsystemUnavailable, "smb.not_configured")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.d.Timeout)
	defer cancel()
	report, configured, err := h.d.Apply(ctx)
	if !configured {
		return nil, apierr.AsClassified(apierr.SubsystemUnavailable, "smb.not_configured")
	}
	if err != nil {
		h.d.Logger.Warn("the SMB agent did not answer an apply", "error", err)
		return nil, apierr.AsClassified(apierr.BadGateway, "smb.agent_unreachable")
	}
	return &reportOutput{Body: SMBReportOf(report)}, nil
}
