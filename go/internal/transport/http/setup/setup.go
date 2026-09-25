//go:build linux

// Package setup serves the unauthenticated first-run surface.
package setup

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/check"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	secret "github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
	setupserver "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
)

// Gate is the one-use first-run token boundary.
type Gate interface {
	Open(context.Context) (bool, error)
	Use(context.Context, string, func(context.Context) error) error
}

// Deps contains only the services and callbacks required by the setup routes.
type Deps struct {
	Auth  *auth.Service
	State interface {
		MergeSettings(context.Context, string, any) error
	}
	Gate Gate

	GrantEveryShare func(context.Context, int64) error
	CreateShare     func(context.Context, core.ShareSpec) (core.Share, error)
	Apply           func(context.Context)
	DataDir         string
	Logger          *slog.Logger
}

// NewHandlers returns the route-name to handler mapping consumed by app composition.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"system.setup.get":  h.get,
		"system.setup.post": h.post,
	}
}

type handlers struct{ d Deps }

func (h *handlers) get(c *gin.Context) {
	if h.d.Gate == nil {
		c.JSON(http.StatusOK, handler.SetupStateOf(false))
		return
	}
	open, err := h.d.Gate.Open(c.Request.Context())
	if err != nil {
		h.logger().Warn("the setup state could not be read", "error", err)
		c.JSON(http.StatusOK, handler.SetupStateOf(false))
		return
	}
	c.JSON(http.StatusOK, handler.SetupStateOf(open))
}

type request struct {
	Token          string        `json:"token"`
	Username       string        `json:"username"`
	Password       string        `json:"password"`
	AppHosts       []string      `json:"app_hosts"`
	TrustedProxies []string      `json:"trusted_proxies"`
	FirstShare     *shareRequest `json:"first_share"`
}

type shareRequest struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

func (h *handlers) post(c *gin.Context) {
	if h.d.Gate == nil {
		h.refuse(c, apierr.Classified{Class: apierr.SetupComplete, Key: "setup.complete"})
		return
	}

	var req request
	if err := decode(c, &req); err != nil || req.Username == "" || req.Password == "" {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	network := networkOf(req)
	findings := check.Section(check.Input{
		Section: "network", Body: network,
		SelfHost: check.HostOnly(c.Request.Host), DataDir: h.d.DataDir,
		Lockout: check.LockoutWarns,
	})
	if handler.Blocking(findings) {
		c.JSON(http.StatusUnprocessableEntity, handler.ApplyOutcomeOf(false, false, false, findings))
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
		h.refuse(c, Refusal(err))
		return
	}

	out := handler.SetupOutcomeOf(userID, req.Username, findings)
	if h.d.State != nil {
		if err := h.d.State.MergeSettings(c.Request.Context(), "network", network); err != nil {
			h.logger().Error("the first-run network settings were not stored", "error", err)
		} else if h.d.Apply != nil {
			h.d.Apply(c.Request.Context())
		}
	}

	if req.FirstShare != nil && req.FirstShare.Name != "" && req.FirstShare.Host != "" {
		if h.d.CreateShare == nil {
			h.logger().Warn("the first share was not created", "error", errors.New("share creation unavailable"))
			out.ShareFailed = true
		} else {
			share, err := h.d.CreateShare(c.Request.Context(), core.ShareSpec{Name: req.FirstShare.Name, Host: req.FirstShare.Host})
			if err != nil {
				h.logger().Warn("the first share was not created", "error", err)
				out.ShareFailed = true
			} else {
				if h.d.GrantEveryShare != nil {
					if err := h.d.GrantEveryShare(c.Request.Context(), userID); err != nil {
						h.logger().Warn("the first share was created without a grant", "error", err)
					}
				}
				view := handler.ShareOf(share)
				out.Share = &view
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

func networkOf(req request) map[string]any {
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

// Refusal maps setup-gate outcomes onto the wire.
func Refusal(err error) apierr.Classified {
	switch {
	case errors.Is(err, setupserver.ErrSetupClosed):
		return apierr.Classified{Class: apierr.SetupComplete, Key: "setup.complete"}
	case errors.Is(err, setupserver.ErrSetupNotIssued):
		return apierr.Classified{Class: apierr.SetupExpired, Key: "setup.not_issued"}
	case errors.Is(err, setupserver.ErrSetupToken):
		return apierr.Classified{Class: apierr.SetupInvalidToken, Key: "setup.invalid_token"}
	default:
		return apierr.Classify(err, apierr.VisibilityKnown)
	}
}

func decode(c *gin.Context, into any) error {
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}

func (h *handlers) refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	c.JSON(status, body)
}

func (h *handlers) logger() *slog.Logger {
	if h.d.Logger != nil {
		return h.d.Logger
	}
	return slog.Default()
}
