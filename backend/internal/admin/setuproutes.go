//go:build linux

// Package setup serves the unauthenticated first-run surface.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/config"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// SetupTokenGate is the one-use first-run token boundary.
type SetupTokenGate interface {
	Open(context.Context) (bool, error)
	Use(context.Context, string, func(context.Context) error) error
}

// SetupDeps contains only the services and callbacks required by the setup routes.
type SetupDeps struct {
	Auth  *auth.Service
	State interface {
		MergeSettings(context.Context, string, any) error
	}
	Gate SetupTokenGate

	GrantEveryShare func(context.Context, int64) error
	CreateShare     func(context.Context, files.ShareSpec) (files.Share, error)
	Apply           func(context.Context)
	DataDir         string
	Logger          *slog.Logger
}

// NewSetupHandlers builds the first-run setup handlers.
func NewSetupHandlers(d SetupDeps) *SetupHandlers {
	return &SetupHandlers{d: d}
}

type SetupHandlers struct{ d SetupDeps }

func (h *SetupHandlers) Get(c *gin.Context) {
	if h.d.Gate == nil {
		c.JSON(http.StatusOK, SetupStateOf(false))
		return
	}
	open, err := h.d.Gate.Open(c.Request.Context())
	if err != nil {
		h.d.Logger.Warn("the setup state could not be read", "error", err)
		c.JSON(http.StatusOK, SetupStateOf(false))
		return
	}
	c.JSON(http.StatusOK, SetupStateOf(open))
}

type setupRequest struct {
	Token          string             `json:"token"`
	Username       string             `json:"username"`
	Password       string             `json:"password"`
	AppHosts       []string           `json:"app_hosts"`
	TrustedProxies []string           `json:"trusted_proxies"`
	FirstShare     *setupShareRequest `json:"first_share"`
}

type setupShareRequest struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

func (h *SetupHandlers) Post(c *gin.Context) {
	if h.d.Gate == nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SetupComplete, Key: "setup.complete"})
		return
	}

	var req setupRequest
	if err := middleware.DecodeJSON(c.Request.Body, &req); err != nil || req.Username == "" || req.Password == "" {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	network := networkOf(req)
	findings := config.Section(config.Input{
		Section: "network", Body: network,
		SelfHost: config.HostOnly(c.Request.Host), DataDir: h.d.DataDir,
		Lockout: config.LockoutWarns,
	})
	if Blocking(findings) {
		c.JSON(http.StatusUnprocessableEntity, ApplyOutcomeOf(false, false, false, findings))
		return
	}

	var userID int64
	err := h.d.Gate.Use(c.Request.Context(), req.Token, func(ctx context.Context) error {
		id, err := h.d.Auth.CreateAdmin(ctx, req.Username, "", secret.New([]byte(req.Password)))
		if err != nil {
			return err
		}
		userID = id
		if h.d.GrantEveryShare == nil {
			return nil
		}
		return h.d.GrantEveryShare(ctx, id)
	})
	if err != nil {
		middleware.Fail(c, err)
		return
	}

	out := SetupOutcomeOf(userID, req.Username, findings)
	if h.d.State != nil {
		if err := h.d.State.MergeSettings(c.Request.Context(), "network", network); err != nil {
			h.d.Logger.Error("the first-run network settings were not stored", "error", err)
		} else if h.d.Apply != nil {
			h.d.Apply(c.Request.Context())
		}
	}

	if req.FirstShare != nil && req.FirstShare.Name != "" && req.FirstShare.Host != "" {
		if h.d.CreateShare == nil {
			h.d.Logger.Warn("the first share was not created", "error", errors.New("share creation unavailable"))
			out.ShareFailed = true
		} else {
			share, err := h.d.CreateShare(c.Request.Context(), files.ShareSpec{Name: req.FirstShare.Name, Host: req.FirstShare.Host})
			if err != nil {
				h.d.Logger.Warn("the first share was not created", "error", err)
				out.ShareFailed = true
			} else {
				if h.d.GrantEveryShare != nil {
					if err := h.d.GrantEveryShare(c.Request.Context(), userID); err != nil {
						h.d.Logger.Warn("the first share was created without a grant", "error", err)
					}
				}
				view := ShareOf(share)
				out.Share = &view
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

func networkOf(req setupRequest) map[string]any {
	out := map[string]any{"app_hosts": anyList(req.AppHosts)}
	if req.TrustedProxies != nil {
		out["trusted_proxies"] = anyList(req.TrustedProxies)
	}
	return out
}

func anyList(in []string) []any {
	out := make([]any, 0, len(in))
	for _, item := range in {
		out = append(out, item)
	}
	return out
}
