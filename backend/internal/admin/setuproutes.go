//go:build linux

// Package setup serves the unauthenticated first-run surface.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

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

type setupStateOutput struct{ Body SetupStateView }

func (h *SetupHandlers) Get(ctx context.Context, _ *struct{}) (*setupStateOutput, error) {
	if h.d.Gate == nil {
		return &setupStateOutput{Body: SetupStateOf(false)}, nil
	}
	open, err := h.d.Gate.Open(ctx)
	if err != nil {
		h.d.Logger.Warn("the setup state could not be read", "error", err)
		return &setupStateOutput{Body: SetupStateOf(false)}, nil
	}
	return &setupStateOutput{Body: SetupStateOf(open)}, nil
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

type setupInput struct{ Body setupRequest }

// setupResult is the created account, or the check results when the network
// settings were refused. Exactly one is set.
type setupResult struct {
	*SetupOutcomeView
	*ApplyOutcomeView
}

type setupOutput struct {
	Status int
	Body   setupResult
}

func (h *SetupHandlers) Post(ctx context.Context, in *setupInput) (*setupOutput, error) {
	if h.d.Gate == nil {
		return nil, apierr.AsClassified(apierr.SetupComplete, "setup.complete")
	}
	req := in.Body
	if req.Username == "" || req.Password == "" {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}

	var selfHost string
	if a, ok := middleware.AuthorityFrom(ctx); ok {
		selfHost = config.HostOnly(a.Host)
	}
	network := networkOf(req)
	findings := config.Section(config.Input{
		Section: "network", Body: network,
		SelfHost: selfHost, DataDir: h.d.DataDir,
		Lockout: config.LockoutWarns,
	})
	if Blocking(findings) {
		refused := ApplyOutcomeOf(false, false, false, findings)
		return &setupOutput{Status: http.StatusUnprocessableEntity, Body: setupResult{ApplyOutcomeView: &refused}}, nil
	}

	var userID int64
	err := h.d.Gate.Use(ctx, req.Token, func(ctx context.Context) error {
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
		return nil, err
	}

	out := SetupOutcomeOf(userID, req.Username, findings)
	if h.d.State != nil {
		if err := h.d.State.MergeSettings(ctx, "network", network); err != nil {
			h.d.Logger.Error("the first-run network settings were not stored", "error", err)
		} else if h.d.Apply != nil {
			h.d.Apply(ctx)
		}
	}

	if req.FirstShare != nil && req.FirstShare.Name != "" && req.FirstShare.Host != "" {
		if h.d.CreateShare == nil {
			h.d.Logger.Warn("the first share was not created", "error", errors.New("share creation unavailable"))
			out.ShareFailed = true
		} else {
			share, err := h.d.CreateShare(ctx, files.ShareSpec{Name: req.FirstShare.Name, Host: req.FirstShare.Host})
			if err != nil {
				h.d.Logger.Warn("the first share was not created", "error", err)
				out.ShareFailed = true
			} else {
				if h.d.GrantEveryShare != nil {
					if err := h.d.GrantEveryShare(ctx, userID); err != nil {
						h.d.Logger.Warn("the first share was created without a grant", "error", err)
					}
				}
				view := ShareOf(share)
				out.Share = &view
			}
		}
	}
	return &setupOutput{Status: http.StatusOK, Body: setupResult{SetupOutcomeView: &out}}, nil
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
